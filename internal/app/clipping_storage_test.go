package app

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func makeReadyClippingSource(t *testing.T, store *Store, durationMS int64) ClippingSource {
	t.Helper()
	content := []byte("synthetic source media")
	source, err := store.CreateClippingSource(ClippingSourceCreate{
		Kind: ClippingSourceUpload, OriginalName: "talk.mp4", MediaType: "video/mp4",
		DeclaredSizeBytes: int64(len(content)), RightsAttested: true, RetainUntil: time.Now().Add(24 * time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source.StoragePath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateClippingSourceProgress(source.ID, ClippingSourceUploading, int64(len(content)), int64(len(content))); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkClippingSourceReady(source.ID, int64(len(content)), durationMS, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	ready, err := store.ClippingSource(source.ID)
	if err != nil {
		t.Fatal(err)
	}
	return ready
}

func makeClippingJob(t *testing.T, store *Store, sourceID, key string) ClippingJob {
	t.Helper()
	job, duplicate, err := store.CreateClippingJob(ClippingJobCreate{SourceID: sourceID}, key)
	if err != nil || duplicate {
		t.Fatalf("create clipping job: duplicate=%v error=%v", duplicate, err)
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
	if _, err := store.RetryClippingStage(job.ID, ClippingStageAnalysis); err != nil {
		t.Fatalf("explicit retry after callback expiry: %v", err)
	}
}

func TestClippingSourceRightsPathBoundsAndStorageReservation(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.CreateClippingSource(ClippingSourceCreate{Kind: ClippingSourceUpload, DeclaredSizeBytes: 10}); err == nil {
		t.Fatal("source was created without explicit rights attestation")
	}
	if _, err := store.CreateClippingSource(ClippingSourceCreate{Kind: ClippingSourceUpload, DeclaredSizeBytes: MaxClippingSourceBytes + 1, RightsAttested: true}); err == nil {
		t.Fatal("source above the 20 GiB limit was accepted")
	}
	for index := 0; index < 5; index++ {
		_, err := store.CreateClippingSource(ClippingSourceCreate{
			Kind: ClippingSourceUpload, OriginalName: "../../unsafe.mp4", DeclaredSizeBytes: MaxClippingSourceBytes,
			RightsAttested: true, RetainUntil: time.Now().Add(time.Hour).Unix(),
		})
		if err != nil {
			t.Fatalf("reserve source %d of 5: %v", index+1, err)
		}
	}
	if _, err := store.CreateClippingSource(ClippingSourceCreate{Kind: ClippingSourceUpload, DeclaredSizeBytes: 1, RightsAttested: true}); err == nil {
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

func TestClippingUploadProgressAndActualDurationBounds(t *testing.T) {
	store := newTestStore(t)
	source, err := store.CreateClippingSource(ClippingSourceCreate{
		Kind: ClippingSourceUpload, DeclaredSizeBytes: 50, RightsAttested: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateClippingSourceProgress(source.ID, ClippingSourceUploading, 25, 25); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateClippingSourceProgress(source.ID, ClippingSourceUploading, 24, 24); err == nil {
		t.Fatal("upload offset moved backwards")
	}
	if err := store.UpdateClippingSourceProgress(source.ID, ClippingSourceUploading, 51, 51); err == nil {
		t.Fatal("upload exceeded declared size")
	}
	if err := store.UpdateClippingSourceProgress(source.ID, ClippingSourceUploading, 50, 50); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkClippingSourceReady(source.ID, 50, MaxClippingSourceDurationMS+1, strings.Repeat("b", 64)); err == nil {
		t.Fatal("source longer than four hours was accepted")
	}
	if err := store.MarkClippingSourceReady(source.ID, 50, MaxClippingSourceDurationMS, strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.ClippingSource(source.ID)
	if err != nil || loaded.Status != ClippingSourceReady || loaded.DurationMS != MaxClippingSourceDurationMS || loaded.ReservedSizeBytes != 50 {
		t.Fatalf("ready source=%+v error=%v", loaded, err)
	}
}

func TestClippingImportLeaseRenewalGuardsAutomaticExpiry(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().Unix()
	source, err := store.CreateClippingSource(ClippingSourceCreate{
		Kind: ClippingSourceGoogleDrive, SourceURL: "encrypted-url",
		DeclaredSizeBytes: 100, RightsAttested: true, RetainUntil: now - 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.BeginClippingImport(source.ID, 0, time.Minute)
	if err != nil || claimed.ImportAttempts != 1 {
		t.Fatalf("begin import=%+v error=%v", claimed, err)
	}
	if err := store.RenewClippingImportLease(source.ID, 1, time.Hour); err != nil {
		t.Fatalf("renew active import lease: %v", err)
	}
	renewed, err := store.ClippingSource(source.ID)
	if err != nil || renewed.ImportLeaseUntil <= claimed.ImportLeaseUntil || renewed.NextImportAt != renewed.ImportLeaseUntil {
		t.Fatalf("renewed import lease=%+v error=%v", renewed, err)
	}
	if err := store.RenewClippingImportLease(source.ID, 2, time.Hour); !errors.Is(err, ErrClippingInvalidState) {
		t.Fatalf("wrong import attempt renewed lease: %v", err)
	}
	if err := store.ExpireClippingSource(source.ID, time.Now().Unix()); err == nil {
		t.Fatal("automatic cleanup ignored the active import lease")
	}
	if _, err := store.db.Exec(`UPDATE clipping_sources SET import_lease_until=0,next_import_at=0 WHERE id=?`, source.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.RenewClippingImportLease(source.ID, 1, time.Hour); !errors.Is(err, ErrClippingInvalidState) {
		t.Fatalf("expired import lease was revived: %v", err)
	}
	if err := store.ExpireClippingSource(source.ID, time.Now().Unix()); err != nil {
		t.Fatalf("automatic cleanup after import lease expiry: %v", err)
	}
}

func TestFailedClippingSourceReleasesReservationOnlyAfterRemoval(t *testing.T) {
	store := newTestStore(t)
	sources := make([]ClippingSource, 0, 5)
	for index := 0; index < 5; index++ {
		source, err := store.CreateClippingSource(ClippingSourceCreate{
			Kind: ClippingSourceUpload, OriginalName: "failed-upload.mp4",
			DeclaredSizeBytes: MaxClippingSourceBytes, RightsAttested: true,
		})
		if err != nil {
			t.Fatalf("create source %d: %v", index+1, err)
		}
		sources = append(sources, source)
	}
	partial := []byte("partial upload bytes")
	if err := os.WriteFile(sources[0].StoragePath, partial, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateClippingSourceProgress(sources[0].ID, ClippingSourceUploading, int64(len(partial)), int64(len(partial))); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateClippingSource(ClippingSourceCreate{Kind: ClippingSourceUpload, DeclaredSizeBytes: 1, RightsAttested: true}); err == nil {
		t.Fatal("five active reservations should exhaust the 100 GiB limit")
	}
	if failed, err := store.FailedClippingSources(10); err != nil || len(failed) != 0 {
		t.Fatalf("failed-source cleanup list before failure=%+v error=%v", failed, err)
	}
	if err := store.ReleaseFailedClippingSourceReservation(sources[0].ID); !errors.Is(err, ErrClippingInvalidState) {
		t.Fatalf("non-failed source release error=%v", err)
	}

	for _, source := range sources {
		if err := store.FailClippingSource(source.ID, "permanent_import_failure"); err != nil {
			t.Fatal(err)
		}
	}
	failed, err := store.FailedClippingSources(2)
	if err != nil || len(failed) != 2 {
		t.Fatalf("bounded failed-source cleanup list=%+v error=%v", failed, err)
	}
	for _, item := range failed {
		if item.Status != ClippingSourceFailed || item.ReservedSizeBytes != MaxClippingSourceBytes {
			t.Fatalf("failed-source cleanup query returned ineligible source: %+v", item)
		}
	}

	for _, source := range sources {
		if err := store.ReleaseFailedClippingSourceReservation(source.ID); err == nil {
			t.Fatal("reservation released while generated media directory still existed")
		}
		before, err := store.ClippingSource(source.ID)
		if err != nil || before.ReservedSizeBytes != MaxClippingSourceBytes {
			t.Fatalf("failed source lost reservation before removal: source=%+v error=%v", before, err)
		}
		store.clippingMu.Lock()
		removeErr := os.RemoveAll(clippingSourceDir(store.clippingDir, source.ID))
		releaseErr := error(nil)
		if removeErr == nil {
			releaseErr = store.ReleaseFailedClippingSourceReservation(source.ID)
		}
		store.clippingMu.Unlock()
		if removeErr != nil || releaseErr != nil {
			t.Fatalf("remove/release source %q: remove=%v release=%v", source.ID, removeErr, releaseErr)
		}
		after, err := store.ClippingSource(source.ID)
		if err != nil || after.Status != ClippingSourceFailed || after.ReservedSizeBytes != 0 || after.DeclaredSizeBytes != MaxClippingSourceBytes || after.OriginalName != source.OriginalName || after.Failure != "permanent_import_failure" {
			t.Fatalf("released failed source lost audit fields or retained bytes: source=%+v error=%v", after, err)
		}
		if source.ID == sources[0].ID && (after.SizeBytes != int64(len(partial)) || after.UploadOffset != int64(len(partial))) {
			t.Fatalf("reservation release discarded upload progress audit: source=%+v", after)
		}
		if err := store.ReleaseFailedClippingSourceReservation(source.ID); err != nil {
			t.Fatalf("repeated failed-source release: %v", err)
		}
	}

	if failed, err := store.FailedClippingSources(10); err != nil || len(failed) != 0 {
		t.Fatalf("failed-source cleanup list after release=%+v error=%v", failed, err)
	}
	newSource, err := store.CreateClippingSource(ClippingSourceCreate{
		Kind: ClippingSourceUpload, DeclaredSizeBytes: MaxClippingSourceBytes, RightsAttested: true,
	})
	if err != nil {
		t.Fatalf("released failed sources did not restore aggregate capacity: %v", err)
	}
	for index := 1; index < 5; index++ {
		if _, err := store.CreateClippingSource(ClippingSourceCreate{
			Kind: ClippingSourceUpload, DeclaredSizeBytes: MaxClippingSourceBytes, RightsAttested: true,
		}); err != nil {
			t.Fatalf("restored capacity rejected source %d: %v", index+1, err)
		}
	}
	if _, err := store.CreateClippingSource(ClippingSourceCreate{Kind: ClippingSourceUpload, DeclaredSizeBytes: 1, RightsAttested: true}); err == nil {
		t.Fatal("restored capacity exceeded 100 GiB")
	}
	var reserved int64
	if err := store.db.QueryRow(`SELECT SUM(reserved_size_bytes) FROM clipping_sources`).Scan(&reserved); err != nil {
		t.Fatal(err)
	}
	if reserved != MaxClippingStorageBytes || newSource.ReservedSizeBytes != MaxClippingSourceBytes {
		t.Fatalf("post-release reservations=%d, want 100 GiB", reserved)
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
