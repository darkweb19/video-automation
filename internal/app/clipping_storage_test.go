package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Historical fixtures are inserted directly; production no longer creates
// legacy file sources or Modal jobs.
func makeReadyClippingSource(t *testing.T, store *Store, durationMS int64) ClippingSource {
	t.Helper()
	content := []byte("synthetic source media")
	id, err := newClippingStorageID("clipsrc_")
	if err != nil {
		t.Fatal(err)
	}
	dir := clippingSourceDir(store.clippingDir, id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := clippingStoragePath(store.clippingDir, id)
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	_, err = store.db.Exec(`INSERT INTO clipping_sources(id,kind,status,original_name,media_type,rights_attested,storage_path,declared_size_bytes,reserved_size_bytes,size_bytes,upload_offset,duration_ms,sha256,retain_until,created_at,updated_at) VALUES(?,'upload','ready','talk.mp4','video/mp4',1,?,?,?,?,?,?,?,?,?,?)`, id, path, len(content), len(content), len(content), len(content), durationMS, strings.Repeat("a", 64), now+86400, now, now)
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.ClippingSource(id)
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func makeClippingJob(t *testing.T, store *Store, sourceID, key string) ClippingJob {
	t.Helper()
	jobID, err := newClippingStorageID("clipjob_")
	if err != nil {
		t.Fatal(err)
	}
	batchID, err := newClippingStorageID("clipbatch_")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	if _, err := store.db.Exec(`INSERT INTO clipping_batches(id,idempotency_key,request_hash,status,budget_limit_micro_usd,created_at,updated_at) VALUES(?,?,?,'queued',1,?,?)`, batchID, key, "historical", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO clipping_jobs(id,batch_id,source_id,provider,content_type,min_clip_seconds,max_clip_seconds,candidate_limit,status,budget_limit_micro_usd,created_at,updated_at) VALUES(?,?,?,'modal_legacy','general',15,180,10,'queued',1,?,?)`, jobID, batchID, sourceID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO clipping_stages(job_id,name,status,idempotency_key,created_at,updated_at) VALUES(?,'analysis','queued',?,?,?)`, jobID, jobID+":analysis", now, now); err != nil {
		t.Fatal(err)
	}
	job, err := store.ClippingJob(jobID)
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func clippingTestArtifact(jobID string, durationMS int64) ClippingArtifact {
	return ClippingArtifact{
		JobID: jobID, Type: "clipping.analysis", SchemaVersion: "ai-clip.transcript.v1", Version: 1,
		SourceDurationMS: durationMS, TimeRanges: []ClippingTimeRange{{StartMS: 0, EndMS: durationMS}}, PayloadJSON: `{"segments":[]}`,
	}
}

func TestClippingMigrationPreservesExistingGeneration(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	record := GenerationRecord{ID: "existing-generation", Prompt: "Persisted before clipping", Model: "fixture/model", Status: "queued"}
	if err := store.InsertGeneration(record); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	loaded, err := reopened.Generation(record.ID)
	if err != nil || loaded.Prompt != record.Prompt || loaded.Status != record.Status {
		t.Fatalf("generation after additive clipping migration=%+v error=%v", loaded, err)
	}
	if sources, err := reopened.ClippingSources(); err != nil || len(sources) != 0 {
		t.Fatalf("new source table should be empty after reopen: sources=%+v error=%v", sources, err)
	}
}

func TestClippingLegacyCacheWithoutRuntimeCompletesFromCache(t *testing.T) {
	store := newTestStore(t)
	source := makeReadyClippingSource(t, store, clippingM3FixtureDurationMS)
	firstJob := clippingM3CreateJob(t, store, source.ID, "legacy-cache-source-job")
	secondJob := clippingM3CreateJob(t, store, source.ID, "legacy-cache-reuse-job")
	stage := clippingM3ClaimAttempt(t, store, firstJob.ID)
	core := clippingM3CoreForRevision(t, store, source)
	if _, duplicate, published, err := clippingM3Complete(t, store, firstJob, stage, source, core, strings.Repeat("c", 64)); err != nil || duplicate || !published {
		t.Fatalf("complete cache source analysis: duplicate=%v published=%v error=%v", duplicate, published, err)
	}
	// Before the additive runtime column existed, migrated cache rows receive 0.
	if _, err := store.db.Exec(`UPDATE clipping_analysis_cache SET compute_seconds=0 WHERE source_id=?`, source.ID); err != nil {
		t.Fatal(err)
	}
	completed, err := store.TryCompleteClippingJobFromAnalysisCache(secondJob.ID)
	if err != nil || !completed {
		t.Fatalf("legacy cache with unknown runtime did not complete: completed=%v error=%v", completed, err)
	}
	artifacts, err := store.ClippingArtifacts(secondJob.ID)
	if err != nil || len(artifacts) != 1 {
		t.Fatalf("cached artifacts=%+v error=%v", artifacts, err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(artifacts[0].PayloadJSON), &payload); err != nil {
		t.Fatal(err)
	}
	var runtime int64
	if err := json.Unmarshal(payload["compute_seconds"], &runtime); err != nil || runtime != 0 {
		t.Fatalf("unknown legacy runtime=%d error=%v", runtime, err)
	}
	if string(payload["analysis_reused"]) != "true" {
		t.Fatalf("cached result lacks reuse marker: %s", artifacts[0].PayloadJSON)
	}
	for _, key := range []string{"cost_basis", "cost_estimate_micro_usd", "rate_micro_usd_per_second", "actual_cost_status"} {
		if _, exists := payload[key]; exists {
			t.Fatalf("cached result exposed legacy accounting field %q: %s", key, artifacts[0].PayloadJSON)
		}
	}
}

func TestClippingCapabilityCleanupRetainsIssuedExpiryUntilRecovery(t *testing.T) {
	store := newTestStore(t)
	source := makeReadyClippingSource(t, store, clippingM3FixtureDurationMS)
	job := clippingM3CreateJob(t, store, source.ID, "capability-expiry-cleanup-job")
	stage := clippingM3ClaimAttempt(t, store, job.ID)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	app := &dashboardApp{store: store, security: security}
	expiresAt := time.Now().Unix() + 2
	if expiresAt >= stage.LeaseExpiresAt {
		t.Fatalf("test callback expiry %d must precede lease expiry %d", expiresAt, stage.LeaseExpiresAt)
	}
	if _, _, err := app.persistClippingDispatchCapabilitiesUntil(stage, expiresAt); err != nil {
		t.Fatalf("persist callback capabilities: %v", err)
	}
	cleaned, err := app.cleanupExpiredClippingCapabilities(expiresAt+1, 10)
	if err != nil || cleaned != 1 {
		t.Fatalf("cleanup expired bearer settings: cleaned=%d error=%v", cleaned, err)
	}
	var indexedExpiry int64
	if err := store.db.QueryRow(`SELECT expires_at FROM clipping_capability_expiry_index WHERE job_id=? AND attempt_id=?`, job.ID, stage.AttemptID).Scan(&indexedExpiry); err != nil || indexedExpiry != expiresAt {
		t.Fatalf("cleanup discarded issued callback expiry: indexed=%d expected=%d error=%v", indexedExpiry, expiresAt, err)
	}
	if rows, err := store.ExpiredClippingStages(expiresAt+1, 10); err != nil || len(rows) != 1 {
		t.Fatalf("indexed expiry was not actionable before lease: rows=%+v error=%v", rows, err)
	}
	if err := store.RecoverExpiredClippingStages(expiresAt + 1); err != nil {
		t.Fatalf("recover expired callback attempt: %v", err)
	}
	stages, err := store.ClippingStages(job.ID)
	if err != nil || len(stages) != 1 || stages[0].Status != ClippingStageFailed {
		t.Fatalf("expired callback attempt state=%+v error=%v", stages, err)
	}
	var remaining int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM clipping_capability_expiry_index WHERE job_id=? AND attempt_id=?`, job.ID, stage.AttemptID).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("recovered attempt retained callback expiry index: count=%d error=%v", remaining, err)
	}
	if _, err := store.historicalRetryClippingStage(job.ID, ClippingStageAnalysis); err != nil {
		t.Fatalf("explicit retry after callback expiry: %v", err)
	}
}

func TestClippingSourceRightsPathBoundsAndStorageReservation(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.historicalCreateClippingSource(ClippingSourceCreate{Kind: ClippingSourceUpload, DeclaredSizeBytes: 10}); err == nil {
		t.Fatal("source was created without explicit rights attestation")
	}
	if _, err := store.historicalCreateClippingSource(ClippingSourceCreate{Kind: ClippingSourceUpload, DeclaredSizeBytes: MaxClippingSourceBytes + 1, RightsAttested: true}); err == nil {
		t.Fatal("source above the 20 GiB limit was accepted")
	}
	for index := 0; index < 5; index++ {
		_, err := store.historicalCreateClippingSource(ClippingSourceCreate{
			Kind: ClippingSourceUpload, OriginalName: "../../unsafe.mp4", DeclaredSizeBytes: MaxClippingSourceBytes,
			RightsAttested: true, RetainUntil: time.Now().Add(time.Hour).Unix(),
		})
		if err != nil {
			t.Fatalf("reserve source %d of 5: %v", index+1, err)
		}
	}
	if _, err := store.historicalCreateClippingSource(ClippingSourceCreate{Kind: ClippingSourceUpload, DeclaredSizeBytes: 1, RightsAttested: true}); err == nil {
		t.Fatal("aggregate 100 GiB reservation limit was exceeded")
	}
	var reserved int64
	if err := store.db.QueryRow(`SELECT SUM(reserved_size_bytes) FROM clipping_sources`).Scan(&reserved); err != nil {
		t.Fatal(err)
	}
	if reserved != MaxClippingStorageBytes {
		t.Fatalf("reserved bytes=%d, want exactly 100 GiB", reserved)
	}
	sources, err := store.ClippingSources()
	if err != nil || len(sources) != 5 {
		t.Fatalf("sources=%d error=%v", len(sources), err)
	}
	for _, source := range sources {
		path, err := store.ClippingSourcePath(source.ID)
		if err != nil {
			t.Fatal(err)
		}
		wantPrefix := filepath.Join(store.clippingDir, source.ID) + string(os.PathSeparator)
		if !strings.HasPrefix(path, wantPrefix) || filepath.Base(path) != "source.media" || strings.Contains(source.OriginalName, "..") {
			t.Fatalf("unsafe source path/name: path=%q source=%+v", path, source)
		}
		encoded, err := json.Marshal(source)
		if err != nil || strings.Contains(string(encoded), source.StoragePath) || (source.SourceURL != "" && strings.Contains(string(encoded), source.SourceURL)) {
			t.Fatalf("source JSON exposed internal path or URL: %s error=%v", encoded, err)
		}
	}
}

func TestHistoricalImportLeaseGuardsAutomaticExpiry(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().Unix()
	source, err := store.historicalCreateClippingSource(ClippingSourceCreate{
		Kind: ClippingSourceGoogleDrive, SourceURL: "encrypted-url",
		DeclaredSizeBytes: 100, RightsAttested: true, RetainUntil: now - 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE clipping_sources SET import_attempts=1,import_lease_until=?,next_import_at=? WHERE id=?`, now+60, now+60, source.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.ExpireClippingSource(source.ID, now); err == nil {
		t.Fatal("automatic cleanup ignored the active historical import lease")
	}
	if _, err := store.db.Exec(`UPDATE clipping_sources SET import_lease_until=0,next_import_at=0 WHERE id=?`, source.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.ExpireClippingSource(source.ID, now); err != nil {
		t.Fatalf("automatic cleanup after import lease expiry: %v", err)
	}
}

func TestHistoricalFailedSourceReleasesReservationAfterRetentionCleanup(t *testing.T) {
	store := newTestStore(t)
	source, err := store.historicalCreateClippingSource(ClippingSourceCreate{
		Kind: ClippingSourceUpload, OriginalName: "failed-upload.mp4", DeclaredSizeBytes: 100, RightsAttested: true, RetainUntil: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	partial := []byte("partial upload bytes")
	if err := os.WriteFile(source.StoragePath, partial, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE clipping_sources SET status='failed',failure='permanent_import_failure',size_bytes=?,upload_offset=? WHERE id=?`, len(partial), len(partial), source.ID); err != nil {
		t.Fatal(err)
	}
	cleaned, err := NewClippingAcquisition(store, nil).CleanupExpiredSources(context.Background(), time.Now())
	if err != nil || cleaned != 1 {
		t.Fatalf("retention cleanup count=%d error=%v", cleaned, err)
	}
	after, err := store.ClippingSource(source.ID)
	if err != nil || after.Status != ClippingSourceDeleted || !after.CleanupComplete || after.ReservedSizeBytes != 0 ||
		after.DeclaredSizeBytes != 100 || after.OriginalName != source.OriginalName || after.Failure != "permanent_import_failure" ||
		after.SizeBytes != int64(len(partial)) || after.UploadOffset != int64(len(partial)) {
		t.Fatalf("cleanup lost audit fields or retained reservation: source=%+v error=%v", after, err)
	}
	if _, err := os.Stat(source.StoragePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed source media remains: %v", err)
	}
}

func TestClippingArtifactTimestampsAndRetentionCleanup(t *testing.T) {
	store := newTestStore(t)
	source := makeReadyClippingSource(t, store, 5_000)
	job := makeClippingJob(t, store, source.ID, "artifact-retention")
	invalid := clippingTestArtifact(job.ID, source.DurationMS)
	invalid.TimeRanges[0].EndMS = source.DurationMS + 1
	if _, err := validateClippingArtifact(invalid, job.ID, source.DurationMS); !errors.Is(err, ErrClippingInvalidArtifact) {
		t.Fatalf("out-of-source timestamp error=%v", err)
	}
	artifact := clippingTestArtifact(job.ID, source.DurationMS)
	rangesJSON, err := validateClippingArtifact(artifact, job.ID, source.DurationMS)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := insertClippingArtifact(tx, artifact, rangesJSON, time.Now().Unix()); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := store.CancelClippingJob(job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE clipping_sources SET retain_until=? WHERE id=?`, time.Now().Add(-time.Hour).Unix(), source.ID); err != nil {
		t.Fatal(err)
	}
	expired, err := store.ExpiredClippingSources(time.Now().Unix(), 10)
	if err != nil || len(expired) != 1 || expired[0].ID != source.ID {
		t.Fatalf("expired source listing=%+v error=%v", expired, err)
	}
	if err := store.ExpireClippingSource(source.ID, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if artifacts, err := store.ClippingArtifacts(job.ID); err != nil || len(artifacts) != 0 {
		t.Fatalf("source cleanup retained canonical artifact: %+v error=%v", artifacts, err)
	}
	if audit, err := store.ClippingJob(job.ID); err != nil || audit.Status != ClippingJobCanceled {
		t.Fatalf("source cleanup removed job audit: job=%+v error=%v", audit, err)
	}
}

func TestClippingArtifactLimitIncludesCanonicalEnvelope(t *testing.T) {
	duration := int64(5_000)
	missingRanges := ClippingArtifact{
		JobID: "clipjob_test", Type: "clipping.analysis", SchemaVersion: "ai-clip.transcript.v1",
		Version: 1, SourceDurationMS: duration, PayloadJSON: `{"segments":[]}`,
	}
	if _, err := validateClippingArtifact(missingRanges, missingRanges.JobID, duration); !errors.Is(err, ErrClippingInvalidArtifact) {
		t.Fatalf("missing time_ranges should violate the canonical protocol: %v", err)
	}
	u2028 := string([]rune{'\u2028'})
	unicodeArtifact := ClippingArtifact{
		JobID: "clipjob_test", Type: "clipping.analysis", SchemaVersion: "ai-clip.transcript.v1",
		Version: 1, SourceDurationMS: duration, TimeRanges: []ClippingTimeRange{}, PayloadJSON: `{"text":"<>&` + u2028 + `"}`,
	}
	rangesJSON, err := json.Marshal(unicodeArtifact.TimeRanges)
	if err != nil {
		t.Fatal(err)
	}
	actualSize, err := canonicalClippingArtifactSize(unicodeArtifact, string(rangesJSON))
	if err != nil {
		t.Fatal(err)
	}
	expectedJSON := `{"type":"clipping.analysis","schema_version":"ai-clip.transcript.v1","version":1,"source_duration_ms":5000,"time_ranges":[],"payload":{"text":"<>&` + u2028 + `"}}`
	if actualSize != len(expectedJSON) {
		t.Fatalf("canonical artifact UTF-8 size=%d, want %d", actualSize, len(expectedJSON))
	}
	payload := `{"text":"` + strings.Repeat("x", maxClippingArtifactPayloadBytes-80) + `"}`
	artifact := ClippingArtifact{
		JobID: "clipjob_test", Type: "clipping.analysis", SchemaVersion: "ai-clip.transcript.v1",
		Version: 1, SourceDurationMS: duration, TimeRanges: []ClippingTimeRange{}, PayloadJSON: payload,
	}
	if len(payload) >= maxClippingArtifactPayloadBytes {
		t.Fatal("test payload should fit by itself under 4 MiB")
	}
	if _, err := validateClippingArtifact(artifact, artifact.JobID, duration); !errors.Is(err, ErrClippingInvalidArtifact) || !strings.Contains(err.Error(), "4 MiB") {
		t.Fatalf("oversized full artifact envelope error=%v", err)
	}
}

func TestClippingExpiredSourcesRespectLeasesAndActiveJobs(t *testing.T) {
	store := newTestStore(t)
	source := makeReadyClippingSource(t, store, 30_000)
	job := makeClippingJob(t, store, source.ID, "active-source-retention")
	if _, err := store.db.Exec(`UPDATE clipping_sources SET retain_until=? WHERE id=?`, time.Now().Add(-time.Hour).Unix(), source.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.SetClippingSourceMediaLease(source.ID, time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if expired, err := store.ExpiredClippingSources(time.Now().Unix(), 10); err != nil || len(expired) != 0 {
		t.Fatalf("leased active source was eligible for cleanup: %+v error=%v", expired, err)
	}
	if err := store.TombstoneClippingSource(source.ID); err == nil {
		t.Fatal("source with active media lease was tombstoned")
	}
	if err := store.SetClippingSourceMediaLease(source.ID, 0); err != nil {
		t.Fatal(err)
	}
	if err := store.TombstoneClippingSource(source.ID); err == nil {
		t.Fatal("source referenced by a nonterminal job was tombstoned")
	}
	if err := store.CancelClippingJob(job.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.TombstoneClippingSource(source.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClippingJob(job.ID); err != nil {
		t.Fatal(err)
	}
}
