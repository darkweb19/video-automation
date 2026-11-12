package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func geminiJobInput() ClippingGeminiJobCreate {
	request := testClippingGeminiRequest("https://youtu.be/abcdefghijk")
	request.Options.MinClipMS = 15_123
	request.Options.MaxClipMS = 170_456
	return ClippingGeminiJobCreate{YouTubeURL: request.YouTubeURL, RightsAttested: true, RetainUntil: time.Now().Add(time.Hour).Unix(), Config: testClippingGeminiConfig(), Options: request.Options}
}

func TestClippingGeminiJobAtomicIdempotencyAndSnapshot(t *testing.T) {
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	input := geminiJobInput()
	const workers = 8
	var wg sync.WaitGroup
	ids := make(chan string, workers)
	errorsSeen := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			job, _, err := store.CreateClippingGeminiJob(security, input, "gemini-concurrent")
			if err != nil {
				errorsSeen <- err
				return
			}
			ids <- job.ID
		}()
	}
	wg.Wait()
	close(ids)
	close(errorsSeen)
	for err := range errorsSeen {
		t.Errorf("concurrent creation: %v", err)
	}
	jobID := ""
	for id := range ids {
		if jobID != "" && id != jobID {
			t.Fatalf("idempotency returned multiple jobs: %s %s", jobID, id)
		}
		jobID = id
	}
	if jobID == "" {
		t.Fatal("no created job")
	}
	job, err := store.ClippingJob(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Provider != ClippingProviderGemini || job.SourceID == "" {
		t.Fatalf("invalid Gemini job: %+v", job)
	}
	var minSeconds, maxSeconds any
	if err := store.db.QueryRow(`SELECT min_clip_seconds,max_clip_seconds FROM clipping_jobs WHERE id=?`, jobID).Scan(&minSeconds, &maxSeconds); err != nil || minSeconds != nil || maxSeconds != nil {
		t.Fatalf("legacy seconds fabricated: %v %v %v", minSeconds, maxSeconds, err)
	}
	snapshot, err := store.ClippingGeminiRequestSnapshot(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Options != input.Options || snapshot.Config != input.Config || len(snapshot.Fingerprint) != 64 || snapshot.SchemaVersion != ClippingGeminiAnalysisVersion {
		t.Fatalf("request snapshot changed: %+v", snapshot)
	}
	encoded, _ := json.Marshal(snapshot)
	if strings.Contains(string(encoded), "abcdefghijk") || strings.Contains(string(encoded), "youtube.com") {
		t.Fatal("snapshot disclosed URL or identifier")
	}
	if _, err := store.db.Exec(`UPDATE clipping_gemini_requests SET options_json='{}' WHERE job_id=?`, jobID); err == nil {
		t.Fatal("snapshot was mutable")
	}
	var sources, jobs, snapshots int
	if err := store.db.QueryRow(`SELECT (SELECT COUNT(*) FROM clipping_sources),(SELECT COUNT(*) FROM clipping_jobs),(SELECT COUNT(*) FROM clipping_gemini_requests)`).Scan(&sources, &jobs, &snapshots); err != nil || sources != 1 || jobs != 1 || snapshots != 1 {
		t.Fatalf("creation not atomic/idempotent: %d %d %d %v", sources, jobs, snapshots, err)
	}
	retry := input
	retry.RetainUntil += 60
	replayed, duplicate, err := store.CreateClippingGeminiJob(security, retry, "gemini-concurrent")
	if err != nil || !duplicate || replayed.ID != job.ID || replayed.SourceID != job.SourceID {
		t.Fatalf("retention-derived replay changed identity: %+v %v %v", replayed, duplicate, err)
	}
	originalSource, err := store.ClippingSource(job.SourceID)
	if err != nil || originalSource.RetainUntil != input.RetainUntil {
		t.Fatalf("replay changed retention: %+v %v", originalSource, err)
	}
	changed := input
	changed.Options.MinClipMS++
	if _, _, err := store.CreateClippingGeminiJob(security, changed, "gemini-concurrent"); !errors.Is(err, ErrClippingIdempotencyConflict) {
		t.Fatalf("idempotency conflict: %v", err)
	}
}

func TestClippingGeminiJobRollbackAndExistingLegacyYouTube(t *testing.T) {
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER reject_gemini_job BEFORE INSERT ON clipping_jobs WHEN NEW.provider='gemini' BEGIN SELECT RAISE(ABORT,'test'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateClippingGeminiJob(security, geminiJobInput(), "gemini-rollback"); err == nil {
		t.Fatal("expected synthetic insertion failure")
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM clipping_sources`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("source survived rollback: %d %v", count, err)
	}
	if _, err := store.db.Exec(`DROP TRIGGER reject_gemini_job`); err != nil {
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
	input := geminiJobInput()
	input.SourceID = source.ID
	input.YouTubeURL = ""
	input.RetainUntil = 0
	job, duplicate, err := store.CreateClippingGeminiJob(security, input, "gemini-existing")
	if err != nil || duplicate || job.SourceID != source.ID {
		t.Fatalf("reuse legacy source: %+v %v %v", job, duplicate, err)
	}
	if _, duplicate, err := store.CreateClippingGeminiJob(security, input, "gemini-existing"); err != nil || !duplicate {
		t.Fatalf("existing source replay: %v %v", duplicate, err)
	}
	before, err := store.ClippingGeminiRequestSnapshot(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(store.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	after, err := reopened.ClippingGeminiRequestSnapshot(job.ID)
	if err != nil || fmt.Sprint(before) != fmt.Sprint(after) {
		t.Fatalf("restart changed snapshot: %+v %v", after, err)
	}
}

func TestClippingGeminiJobRequiresAvailableMigration(t *testing.T) {
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DELETE FROM clipping_schema_migrations WHERE version=?`, clippingReferenceMigrationVersion); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateClippingGeminiJob(security, geminiJobInput(), "gemini-unavailable"); !errors.Is(err, ErrClippingGeminiPersistenceUnavailable) {
		t.Fatalf("unavailable migration accepted request: %v", err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM clipping_sources`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("unavailable migration wrote source: %d %v", count, err)
	}
}

func TestClippingGeminiSnapshotReprepareFailsClosedOnDrift(t *testing.T) {
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	input := geminiJobInput()
	job, _, err := store.CreateClippingGeminiJob(security, input, "gemini-reprepare")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.ClippingGeminiRequestSnapshot(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.ClippingSource(job.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	url, err := security.DecryptSetting(clippingSourceURLSetting, source.SourceURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := prepareClippingGeminiSnapshotRequest(snapshot, url); err != nil {
		t.Fatalf("unchanged intent rejected: %v", err)
	}
	mutations := []func(*ClippingGeminiSnapshot){
		func(s *ClippingGeminiSnapshot) { s.Options.MinClipMS++ },
		func(s *ClippingGeminiSnapshot) { s.Config.ProjectID = "changed-project" },
		func(s *ClippingGeminiSnapshot) { s.PromptSHA256 = strings.Repeat("a", 64) },
		func(s *ClippingGeminiSnapshot) { s.SchemaSHA256 = strings.Repeat("b", 64) },
		func(s *ClippingGeminiSnapshot) { s.GenerationMode = "changed" },
	}
	for i, mutate := range mutations {
		changed := snapshot
		mutate(&changed)
		if _, err := prepareClippingGeminiSnapshotRequest(changed, url); err == nil {
			t.Errorf("drift %d was accepted", i)
		}
	}
	if _, err := prepareClippingGeminiSnapshotRequest(snapshot, "https://youtu.be/lmnopqrstuv"); err == nil {
		t.Fatal("different source URL was accepted")
	}
}
