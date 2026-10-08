package app

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

func makeClippingBatch(t *testing.T, store *Store, sourceIDs []string, batchBudget, jobBudget int64, key string) (ClippingBatch, []ClippingJob) {
	t.Helper()
	jobs := make([]ClippingJobCreate, len(sourceIDs))
	for index, sourceID := range sourceIDs {
		jobs[index] = ClippingJobCreate{SourceID: sourceID, BudgetLimitMicroUSD: jobBudget}
	}
	batch, created, duplicate, err := store.CreateClippingBatch(ClippingBatchCreate{
		IdempotencyKey: key, BudgetLimitMicroUSD: batchBudget, Jobs: jobs,
	})
	if err != nil || duplicate {
		t.Fatalf("create clipping batch: duplicate=%v error=%v", duplicate, err)
	}
	return batch, created
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

func TestClippingBatchCreationIsAtomicAndIdempotent(t *testing.T) {
	store := newTestStore(t)
	ready := makeReadyClippingSource(t, store, 120_000)
	incomplete, err := store.CreateClippingSource(ClippingSourceCreate{
		Kind: ClippingSourceUpload, DeclaredSizeBytes: 10, RightsAttested: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	input := ClippingBatchCreate{
		IdempotencyKey: "batch-request-1", BudgetLimitMicroUSD: 1_000_000,
		Jobs: []ClippingJobCreate{{SourceID: ready.ID, BudgetLimitMicroUSD: 500_000}},
	}
	batch, jobs, duplicate, err := store.CreateClippingBatch(input)
	if err != nil || duplicate || len(jobs) != 1 {
		t.Fatalf("batch=%+v jobs=%+v duplicate=%v error=%v", batch, jobs, duplicate, err)
	}
	gotBatch, gotJobs, duplicate, err := store.CreateClippingBatch(input)
	if err != nil || !duplicate || gotBatch.ID != batch.ID || len(gotJobs) != 1 || gotJobs[0].ID != jobs[0].ID {
		t.Fatalf("idempotent result batch=%+v jobs=%+v duplicate=%v error=%v", gotBatch, gotJobs, duplicate, err)
	}
	changed := input
	changed.Jobs = []ClippingJobCreate{{SourceID: ready.ID, BudgetLimitMicroUSD: 400_000}}
	if _, _, _, err := store.CreateClippingBatch(changed); !errors.Is(err, ErrClippingIdempotencyConflict) {
		t.Fatalf("changed request with same key error=%v", err)
	}
	invalid := ClippingBatchCreate{
		IdempotencyKey: "atomic-invalid", BudgetLimitMicroUSD: 1_000_000,
		Jobs: []ClippingJobCreate{{SourceID: ready.ID, BudgetLimitMicroUSD: 500_000}, {SourceID: incomplete.ID, BudgetLimitMicroUSD: 500_000}},
	}
	if _, _, _, err := store.CreateClippingBatch(invalid); err == nil {
		t.Fatal("batch accepted a non-ready source")
	}
	batches, err := store.ClippingBatches()
	if err != nil || len(batches) != 1 || batches[0].ID != batch.ID {
		t.Fatalf("failed batch creation left partial rows: batches=%+v error=%v", batches, err)
	}
	allJobs, err := store.ClippingJobs("")
	if err != nil || len(allJobs) != 1 || allJobs[0].BatchID != batch.ID {
		t.Fatalf("all clipping jobs=%+v error=%v", allJobs, err)
	}
}

func TestClippingConcurrentBatchBudgetReservationAndOverrunReconciliation(t *testing.T) {
	store := newTestStore(t)
	source := makeReadyClippingSource(t, store, 90_000)
	batch, jobs := makeClippingBatch(t, store, []string{source.ID, source.ID}, 100, 100, "concurrent-budget")
	type result struct {
		stage   ClippingStage
		claimed bool
		err     error
	}
	results := make(chan result, len(jobs))
	var wg sync.WaitGroup
	for _, job := range jobs {
		wg.Add(1)
		go func(jobID string) {
			defer wg.Done()
			stage, claimed, err := store.ClaimClippingStage(jobID, ClippingStageAnalysis, 70, time.Minute)
			results <- result{stage: stage, claimed: claimed, err: err}
		}(job.ID)
	}
	wg.Wait()
	close(results)
	var winner result
	var paused int
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.claimed {
			winner = result
		} else if result.stage.Status == ClippingStagePausedBudget {
			paused++
		}
	}
	if winner.stage.AttemptID == "" || paused != 1 {
		t.Fatalf("winner=%+v paused=%d", winner, paused)
	}
	reservedBatch, err := store.ClippingBatch(batch.ID)
	if err != nil || reservedBatch.ReservedMicroUSD != 70 || reservedBatch.SpentMicroUSD != 0 {
		t.Fatalf("batch reservation before settlement=%+v error=%v", reservedBatch, err)
	}
	artifact := clippingTestArtifact(winner.stage.JobID, source.DurationMS)
	finished, duplicate, err := store.CompleteClippingStage(winner.stage.JobID, winner.stage.Name, winner.stage.AttemptID, winner.stage.LeaseToken, 110, artifact)
	if err != nil || duplicate || finished.Status != ClippingStageCompleted {
		t.Fatalf("stage settlement=%+v duplicate=%v error=%v", finished, duplicate, err)
	}
	settled, err := store.ClippingBatch(batch.ID)
	if err != nil || settled.ReservedMicroUSD != 0 || settled.SpentMicroUSD != 110 {
		t.Fatalf("overrun was not reconciled: batch=%+v error=%v", settled, err)
	}
	other := jobs[0]
	if other.ID == winner.stage.JobID {
		other = jobs[1]
	}
	pausedStage, claimed, err := store.ClaimClippingStage(other.ID, ClippingStageAnalysis, 1, time.Minute)
	if err != nil || claimed || pausedStage.Status != ClippingStagePausedBudget {
		t.Fatalf("overrun did not block subsequent reservation: stage=%+v claimed=%v error=%v", pausedStage, claimed, err)
	}
}

func TestClippingExpiredStageHoldsBudgetUntilLateSettlement(t *testing.T) {
	store := newTestStore(t)
	source := makeReadyClippingSource(t, store, 60_000)
	batch, jobs := makeClippingBatch(t, store, []string{source.ID}, 100, 100, "expired-dispatch")
	job := jobs[0]
	claimed, didClaim, err := store.ClaimClippingStage(job.ID, ClippingStageAnalysis, 80, time.Minute)
	if err != nil || !didClaim {
		t.Fatalf("claim=%+v claimed=%v error=%v", claimed, didClaim, err)
	}
	expiredAt := time.Now().Unix() - 1
	if _, err := store.db.Exec(`UPDATE clipping_stages SET lease_expires_at=? WHERE job_id=? AND name=?`, expiredAt, job.ID, ClippingStageAnalysis); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE clipping_stage_attempts SET lease_expires_at=? WHERE attempt_id=?`, expiredAt, claimed.AttemptID); err != nil {
		t.Fatal(err)
	}
	preRecovery, err := store.ExpiredClippingStages(time.Now().Unix(), 10)
	if err != nil || len(preRecovery) != 1 || preRecovery[0].Status != ClippingStageRunning {
		t.Fatalf("expired stage query before recovery=%+v error=%v", preRecovery, err)
	}
	if err := store.RecoverExpiredClippingStages(time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	postRecovery, err := store.ExpiredClippingStages(time.Now().Unix(), 10)
	if err != nil || len(postRecovery) != 1 || postRecovery[0].Status != ClippingStageUncertain {
		t.Fatalf("uncertain stage query after recovery=%+v error=%v", postRecovery, err)
	}
	uncertain, err := store.ClippingStages(job.ID)
	if err != nil || len(uncertain) != 1 || uncertain[0].Status != ClippingStageUncertain || uncertain[0].ReservedMicroUSD != 80 || uncertain[0].AttemptID != claimed.AttemptID {
		t.Fatalf("uncertain stage=%+v error=%v", uncertain, err)
	}
	pausedJob, err := store.ClippingJob(job.ID)
	if err != nil || pausedJob.Status != ClippingJobPausedBudget || pausedJob.ReservedMicroUSD != 80 {
		t.Fatalf("expired job reservation=%+v error=%v", pausedJob, err)
	}
	if _, claimedAgain, err := store.ClaimClippingStage(job.ID, ClippingStageAnalysis, 1, time.Minute); err != nil || claimedAgain {
		t.Fatalf("uncertain work was redispatched: claimed=%v error=%v", claimedAgain, err)
	}
	if _, err := store.RetryClippingStage(job.ID, ClippingStageAnalysis); err == nil {
		t.Fatal("uncertain work was retried before cost settlement")
	}
	_, duplicate, err := store.CompleteClippingStage(job.ID, ClippingStageAnalysis, claimed.AttemptID, claimed.LeaseToken, 50, clippingTestArtifact(job.ID, source.DurationMS))
	if !errors.Is(err, ErrClippingStaleAttempt) || duplicate {
		t.Fatalf("late result should settle without completing: duplicate=%v error=%v", duplicate, err)
	}
	settledJob, err := store.ClippingJob(job.ID)
	if err != nil || settledJob.Status != ClippingJobFailed || settledJob.SpentMicroUSD != 50 || settledJob.ReservedMicroUSD != 0 {
		t.Fatalf("late actual cost reconciliation job=%+v error=%v", settledJob, err)
	}
	settledBatch, err := store.ClippingBatch(batch.ID)
	if err != nil || settledBatch.SpentMicroUSD != 50 || settledBatch.ReservedMicroUSD != 0 {
		t.Fatalf("late actual cost reconciliation batch=%+v error=%v", settledBatch, err)
	}
	artifacts, err := store.ClippingArtifacts(job.ID)
	if err != nil || len(artifacts) != 0 {
		t.Fatalf("stale result persisted analysis artifact: %+v error=%v", artifacts, err)
	}
	_, duplicate, err = store.CompleteClippingStage(job.ID, ClippingStageAnalysis, claimed.AttemptID, claimed.LeaseToken, 90, clippingTestArtifact(job.ID, source.DurationMS))
	if err != nil || !duplicate {
		t.Fatalf("duplicate late callback should be idempotent: duplicate=%v error=%v", duplicate, err)
	}
	settledBatch, err = store.ClippingBatch(batch.ID)
	if err != nil || settledBatch.SpentMicroUSD != 50 {
		t.Fatalf("duplicate late callback charged again: batch=%+v error=%v", settledBatch, err)
	}
	retried, err := store.RetryClippingStage(job.ID, ClippingStageAnalysis)
	if err != nil || retried.Status != ClippingStageQueued {
		t.Fatalf("settled failure was not explicitly retryable: stage=%+v error=%v", retried, err)
	}
	newAttempt, didClaim, err := store.ClaimClippingStage(job.ID, ClippingStageAnalysis, 50, time.Minute)
	if err != nil || !didClaim || newAttempt.AttemptID == claimed.AttemptID || newAttempt.IdempotencyKey != claimed.IdempotencyKey {
		t.Fatalf("explicit retry claim=%+v claimed=%v error=%v", newAttempt, didClaim, err)
	}
}

func TestClippingConcurrentExpiryAndLateSettlementReconcileOnce(t *testing.T) {
	store := newTestStore(t)
	source := makeReadyClippingSource(t, store, 60_000)
	_, jobs := makeClippingBatch(t, store, []string{source.ID}, 100, 100, "concurrent-expiry-settlement")
	job := jobs[0]
	claimed, didClaim, err := store.ClaimClippingStage(job.ID, ClippingStageAnalysis, 80, time.Minute)
	if err != nil || !didClaim {
		t.Fatalf("claim=%+v claimed=%v error=%v", claimed, didClaim, err)
	}
	expiredAt := time.Now().Unix() - 1
	if _, err := store.db.Exec(`UPDATE clipping_stages SET lease_expires_at=? WHERE job_id=? AND name=?`, expiredAt, job.ID, ClippingStageAnalysis); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE clipping_stage_attempts SET lease_expires_at=? WHERE attempt_id=?`, expiredAt, claimed.AttemptID); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	var recoverErr error
	var callbackErr error
	var duplicate bool
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		recoverErr = store.RecoverExpiredClippingStages(time.Now().Unix())
	}()
	go func() {
		defer wg.Done()
		<-start
		_, duplicate, callbackErr = store.CompleteClippingStage(
			job.ID, ClippingStageAnalysis, claimed.AttemptID, claimed.LeaseToken, 50,
			clippingTestArtifact(job.ID, source.DurationMS),
		)
	}()
	close(start)
	wg.Wait()
	if recoverErr != nil {
		t.Fatalf("recover expired attempt: %v", recoverErr)
	}
	if !errors.Is(callbackErr, ErrClippingStaleAttempt) || duplicate {
		t.Fatalf("late concurrent callback duplicate=%v error=%v", duplicate, callbackErr)
	}
	stages, err := store.ClippingStages(job.ID)
	if err != nil || len(stages) != 1 || stages[0].Status != ClippingStageFailed || stages[0].ReservedMicroUSD != 0 {
		t.Fatalf("concurrent final stage=%+v error=%v", stages, err)
	}
	settledJob, err := store.ClippingJob(job.ID)
	if err != nil || settledJob.Status != ClippingJobFailed || settledJob.SpentMicroUSD != 50 || settledJob.ReservedMicroUSD != 0 {
		t.Fatalf("concurrent final job=%+v error=%v", settledJob, err)
	}
	settledBatch, err := store.ClippingBatch(settledJob.BatchID)
	if err != nil || settledBatch.SpentMicroUSD != 50 || settledBatch.ReservedMicroUSD != 0 {
		t.Fatalf("concurrent final batch=%+v error=%v", settledBatch, err)
	}
	artifacts, err := store.ClippingArtifacts(job.ID)
	if err != nil || len(artifacts) != 0 {
		t.Fatalf("concurrent stale callback persisted artifact: %+v error=%v", artifacts, err)
	}
	_, duplicate, err = store.CompleteClippingStage(job.ID, ClippingStageAnalysis, claimed.AttemptID, claimed.LeaseToken, 99, clippingTestArtifact(job.ID, source.DurationMS))
	if err != nil || !duplicate {
		t.Fatalf("duplicate late callback duplicate=%v error=%v", duplicate, err)
	}
	settledBatch, err = store.ClippingBatch(settledJob.BatchID)
	if err != nil || settledBatch.SpentMicroUSD != 50 {
		t.Fatalf("concurrent duplicate callback charged twice: batch=%+v error=%v", settledBatch, err)
	}
}

func TestClippingCancellationSettlesActualCostWithoutAdvancing(t *testing.T) {
	store := newTestStore(t)
	source := makeReadyClippingSource(t, store, 45_000)
	_, jobs := makeClippingBatch(t, store, []string{source.ID}, 100, 100, "cancel-running")
	job := jobs[0]
	claimed, didClaim, err := store.ClaimClippingStage(job.ID, ClippingStageAnalysis, 80, time.Minute)
	if err != nil || !didClaim {
		t.Fatalf("claim=%+v claimed=%v error=%v", claimed, didClaim, err)
	}
	if err := store.CancelClippingJob(job.ID); err != nil {
		t.Fatal(err)
	}
	canceled, err := store.ClippingJob(job.ID)
	if err != nil || canceled.Status != ClippingJobCanceled || canceled.ReservedMicroUSD != 80 {
		t.Fatalf("cancel released unknown in-flight reserve: job=%+v error=%v", canceled, err)
	}
	_, duplicate, err := store.CompleteClippingStage(job.ID, ClippingStageAnalysis, claimed.AttemptID, claimed.LeaseToken, 55, clippingTestArtifact(job.ID, source.DurationMS))
	if !errors.Is(err, ErrClippingJobTerminal) || duplicate {
		t.Fatalf("canceled callback should settle cost without stage completion: duplicate=%v error=%v", duplicate, err)
	}
	canceled, err = store.ClippingJob(job.ID)
	if err != nil || canceled.Status != ClippingJobCanceled || canceled.SpentMicroUSD != 55 || canceled.ReservedMicroUSD != 0 {
		t.Fatalf("canceled job reconciliation=%+v error=%v", canceled, err)
	}
	artifacts, err := store.ClippingArtifacts(job.ID)
	if err != nil || len(artifacts) != 0 {
		t.Fatalf("canceled callback created artifact: %+v error=%v", artifacts, err)
	}
	_, duplicate, err = store.CompleteClippingStage(job.ID, ClippingStageAnalysis, claimed.AttemptID, claimed.LeaseToken, 99, clippingTestArtifact(job.ID, source.DurationMS))
	if err != nil || !duplicate {
		t.Fatalf("duplicate canceled callback=%v error=%v", duplicate, err)
	}
	canceled, _ = store.ClippingJob(job.ID)
	if canceled.SpentMicroUSD != 55 {
		t.Fatalf("duplicate canceled callback charged twice: %+v", canceled)
	}
	if err := store.TombstoneClippingSource(source.ID); err != nil {
		t.Fatal(err)
	}
	tombstone, err := store.ClippingSource(source.ID)
	if err != nil || tombstone.Status != ClippingSourceDeleted || tombstone.SourceURL != "" || tombstone.CleanupComplete || tombstone.ReservedSizeBytes == 0 {
		t.Fatalf("source tombstone lost accounting or cleanup state: source=%+v error=%v", tombstone, err)
	}
	if err := os.RemoveAll(filepath.Dir(tombstone.StoragePath)); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkClippingSourceCleaned(source.ID); err != nil {
		t.Fatal(err)
	}
	cleaned, _ := store.ClippingSource(source.ID)
	jobAfterCleanup, _ := store.ClippingJob(job.ID)
	if cleaned.ReservedSizeBytes != 0 || !cleaned.CleanupComplete || jobAfterCleanup.SpentMicroUSD != 55 {
		t.Fatalf("cleanup must release bytes but preserve cost: source=%+v job=%+v", cleaned, jobAfterCleanup)
	}
}

func TestClippingArtifactTimestampsAndRetentionCleanup(t *testing.T) {
	store := newTestStore(t)
	source := makeReadyClippingSource(t, store, 5_000)
	batch, jobs := makeClippingBatch(t, store, []string{source.ID}, 100, 100, "artifact-retention")
	job := jobs[0]
	claimed, ok, err := store.ClaimClippingStage(job.ID, ClippingStageAnalysis, 30, time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim=%+v ok=%v error=%v", claimed, ok, err)
	}
	invalid := clippingTestArtifact(job.ID, source.DurationMS)
	invalid.TimeRanges[0].EndMS = source.DurationMS + 1
	if _, _, err := store.CompleteClippingStage(job.ID, ClippingStageAnalysis, claimed.AttemptID, claimed.LeaseToken, 20, invalid); !errors.Is(err, ErrClippingInvalidArtifact) {
		t.Fatalf("out-of-source timestamp error=%v", err)
	}
	failed, err := store.ClippingJob(job.ID)
	if err != nil || failed.Status != ClippingJobFailed || failed.SpentMicroUSD != 20 || failed.ReservedMicroUSD != 0 {
		t.Fatalf("invalid artifact failed to settle actual cost: job=%+v error=%v", failed, err)
	}
	if err := store.ApplyClippingRetention(time.Now().Add(-time.Hour).Unix()); err != nil {
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
	if audit, err := store.ClippingJob(job.ID); err != nil || audit.SpentMicroUSD != 20 || audit.Status != ClippingJobFailed {
		t.Fatalf("source cleanup removed job/cost audit: job=%+v error=%v", audit, err)
	}
	if _, err := store.ClippingBatch(batch.ID); err != nil {
		t.Fatal(err)
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
	batch, jobs := makeClippingBatch(t, store, []string{source.ID}, 100, 100, "active-source-retention")
	if err := store.ApplyClippingRetention(time.Now().Add(-time.Hour).Unix()); err != nil {
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
	if err := store.CancelClippingBatch(batch.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.TombstoneClippingSource(source.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClippingJob(jobs[0].ID); err != nil {
		t.Fatal(err)
	}
}
