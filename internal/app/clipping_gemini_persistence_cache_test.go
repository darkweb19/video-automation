package app

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

func geminiCacheJobs(t *testing.T, store *Store, security *Security, sourceID string) (ClippingJob, ClippingJob) {
	t.Helper()
	input := geminiJobInput()
	input.Options = testClippingGeminiRequest("").Options
	input.Options.CandidateLimit = 2
	if sourceID != "" {
		input.SourceID = sourceID
		input.YouTubeURL = ""
		input.RetainUntil = 0
	}
	origin, _, err := store.CreateClippingGeminiJob(security, input, "cache-origin")
	if err != nil {
		t.Fatal(err)
	}
	input.SourceID = origin.SourceID
	input.YouTubeURL = ""
	input.RetainUntil = 0
	target, _, err := store.CreateClippingGeminiJob(security, input, "cache-target")
	if err != nil {
		t.Fatal(err)
	}
	return origin, target
}

func publishGeminiCacheFixture(t *testing.T, store *Store, job ClippingJob, now int64) {
	t.Helper()
	snapshot, err := store.ClippingGeminiRequestSnapshot(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := clippingGeminiArtifactFromPayload(job.ID, readClippingGeminiFixture(t, "ranked.json"), snapshot.Options)
	if err != nil {
		t.Fatal(err)
	}
	ranges, err := validateClippingArtifact(artifact, job.ID, artifact.SourceDurationMS)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE clipping_jobs SET status='completed' WHERE id=?`, job.ID); err != nil {
		t.Fatal(err)
	}
	if err := insertClippingArtifact(tx, artifact, ranges, now); err != nil {
		t.Fatal(err)
	}
	if err := storeClippingGeminiCacheTx(tx, job.ID, now); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func loadGeminiCacheFixture(t *testing.T, store *Store, jobID string, now int64) (clippingGeminiCacheHit, bool, error) {
	t.Helper()
	tx, err := store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	return loadClippingGeminiCacheTx(tx, jobID, now)
}

func TestClippingGeminiCacheIsScopedToSourceFingerprintRetentionAndOrigin(t *testing.T) {
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	origin, target := geminiCacheJobs(t, store, security, "")
	now := time.Now().Unix()
	publishGeminiCacheFixture(t, store, origin, now)
	artifact, found, err := loadGeminiCacheFixture(t, store, target.ID, now+1)
	if err != nil || !found || artifact.Artifact.JobID != target.ID || artifact.Artifact.SchemaVersion != ClippingGeminiAnalysisVersion || artifact.OriginJobID != origin.ID {
		t.Fatalf("cache reuse: %+v %v %v", artifact, found, err)
	}
	var created, expires int64
	if err := store.db.QueryRow(`SELECT created_at,expires_at FROM clipping_gemini_cache WHERE origin_job_id=?`, origin.ID).Scan(&created, &expires); err != nil || expires-created > 86400 {
		t.Fatalf("invalid TTL: %d %d %v", created, expires, err)
	}
	if _, found, err := loadGeminiCacheFixture(t, store, target.ID, expires); (err != nil && !errors.Is(err, ErrClippingInvalidState)) || found {
		t.Fatalf("expired cache reused: %v %v", found, err)
	}
	input := geminiJobInput()
	input.SourceID = origin.SourceID
	input.YouTubeURL = ""
	input.RetainUntil = 0
	input.Options = testClippingGeminiRequest("").Options
	input.Options.CandidateLimit = 2
	input.Options.MinClipMS++
	changed, _, err := store.CreateClippingGeminiJob(security, input, "cache-different-options")
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := loadGeminiCacheFixture(t, store, changed.ID, now+1); err != nil || found {
		t.Fatalf("different fingerprint reused: %v %v", found, err)
	}
	input = geminiJobInput()
	input.Options = testClippingGeminiRequest("").Options
	input.Options.CandidateLimit = 2
	other, _, err := store.CreateClippingGeminiJob(security, input, "cache-different-source")
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := loadGeminiCacheFixture(t, store, other.ID, now+1); err != nil || found {
		t.Fatalf("different source reused: %v %v", found, err)
	}
	for _, state := range []struct {
		status  string
		deleted int
	}{{"failed", 0}, {"completed", 1}} {
		if _, err := store.db.Exec(`UPDATE clipping_jobs SET status=?,analysis_deleted=? WHERE id=?`, state.status, state.deleted, origin.ID); err != nil {
			t.Fatal(err)
		}
		if _, found, err := loadGeminiCacheFixture(t, store, target.ID, now+1); err != nil || found {
			t.Fatalf("invalid origin reused: %+v %v %v", state, found, err)
		}
	}
	if _, err := store.db.Exec(`UPDATE clipping_jobs SET status='completed',analysis_deleted=0 WHERE id=?`, origin.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE clipping_sources SET retain_until=? WHERE id=?`, now, origin.SourceID); err != nil {
		t.Fatal(err)
	}
	if _, found, err := loadGeminiCacheFixture(t, store, target.ID, now+1); found || !errors.Is(err, ErrClippingInvalidState) {
		t.Fatalf("expired source reused: %v %v", found, err)
	}
	if _, err := store.db.Exec(`UPDATE clipping_gemini_cache SET expires_at=created_at+86401 WHERE origin_job_id=?`, origin.ID); err == nil {
		t.Fatal("database accepted >24h cache")
	}
}

func TestClippingGeminiCacheDeletionDoesNotTouchLegacyCache(t *testing.T) {
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	source := makeReadyClippingSource(t, store, 60_000)
	encrypted, err := security.EncryptSetting(clippingSourceURLSetting, "https://www.youtube.com/watch?v=abcdefghijk")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE clipping_sources SET kind='youtube_original_file',source_url=? WHERE id=?`, encrypted, source.ID); err != nil {
		t.Fatal(err)
	}
	origin, target := geminiCacheJobs(t, store, security, source.ID)
	now := time.Now().Unix()
	publishGeminiCacheFixture(t, store, origin, now)
	if _, err := store.db.Exec(`INSERT INTO clipping_analysis_cache(source_id,source_sha256,pipeline_key,source_duration_ms,payload_json,created_at) VALUES(?,?,?,60000,'{}',?)`, source.ID, strings.Repeat("a", 64), "legacy", now); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteClippingAnalysis(origin.ID); err != nil {
		t.Fatal(err)
	}
	var legacy, gemini int
	if err := store.db.QueryRow(`SELECT (SELECT COUNT(*) FROM clipping_analysis_cache WHERE source_id=?),(SELECT COUNT(*) FROM clipping_gemini_cache WHERE source_id=?)`, source.ID, source.ID).Scan(&legacy, &gemini); err != nil || legacy != 1 || gemini != 0 {
		t.Fatalf("provider cache deletion: legacy=%d gemini=%d err=%v", legacy, gemini, err)
	}
	if _, found, err := loadGeminiCacheFixture(t, store, target.ID, now+1); err != nil || found {
		t.Fatalf("deleted origin reused: %v %v", found, err)
	}
}

func TestClippingGeminiCacheRejectsUnfinishedOriginAndSourceDeletionErasesCache(t *testing.T) {
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	origin, _ := geminiCacheJobs(t, store, security, "")
	now := time.Now().Unix()
	tx, err := store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := storeClippingGeminiCacheTx(tx, origin.ID, now); !errors.Is(err, ErrClippingInvalidState) {
		t.Fatalf("unfinished origin cached: %v", err)
	}
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		t.Fatal(err)
	}
	publishGeminiCacheFixture(t, store, origin, now)
	if _, err := store.db.Exec(`UPDATE clipping_jobs SET status='canceled' WHERE source_id=? AND status='queued'`, origin.SourceID); err != nil {
		t.Fatal(err)
	}
	if err := store.TombstoneClippingSource(origin.SourceID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM clipping_gemini_cache WHERE source_id=?`, origin.SourceID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("source deletion left cache: %d %v", count, err)
	}
}

func TestClippingGeminiCacheReindexCannotExtendOrLaunderReuse(t *testing.T) {
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	origin, target := geminiCacheJobs(t, store, security, "")
	now := time.Now().Unix()
	publishGeminiCacheFixture(t, store, origin, now)
	var originalExpiry int64
	if err := store.db.QueryRow(`SELECT expires_at FROM clipping_gemini_cache WHERE origin_job_id=?`, origin.ID).Scan(&originalExpiry); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := storeClippingGeminiCacheTx(tx, origin.ID, now+60); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var expiry int64
	if err := store.db.QueryRow(`SELECT expires_at FROM clipping_gemini_cache WHERE origin_job_id=?`, origin.ID).Scan(&expiry); err != nil || expiry != originalExpiry {
		t.Fatalf("reindex extended cache: %d -> %d %v", originalExpiry, expiry, err)
	}
	tx, err = store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	hit, found, err := loadClippingGeminiCacheTx(tx, target.ID, now+61)
	if err != nil || !found || hit.OriginJobID != origin.ID {
		t.Fatalf("cache hit: %+v %v %v", hit, found, err)
	}
	ranges, err := validateClippingArtifact(hit.Artifact, target.ID, hit.Artifact.SourceDurationMS)
	if err != nil {
		t.Fatal(err)
	}
	if err := insertClippingArtifact(tx, hit.Artifact, ranges, now+61); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE clipping_jobs SET status='completed' WHERE id=?`, target.ID); err != nil {
		t.Fatal(err)
	}
	if err := storeClippingGeminiCacheTx(tx, target.ID, now+61); !errors.Is(err, ErrClippingInvalidState) {
		t.Fatalf("reused result refreshed cache: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteClippingAnalysis(target.ID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM clipping_gemini_cache WHERE source_id=?`, origin.SourceID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("deleting reuse left matching cache: %d %v", count, err)
	}
}
