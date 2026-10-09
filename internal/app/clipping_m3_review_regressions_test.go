package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func clippingM3ClaimAttempt(t *testing.T, store *Store, jobID string, includeEstimate bool) ClippingStage {
	t.Helper()
	claimedSource, err := store.ClaimClippingAnalysisSource(jobID)
	if err != nil || !claimedSource {
		t.Fatalf("claim analysis source for %s: claimed=%v error=%v", jobID, claimedSource, err)
	}
	stage, claimed, err := store.ClaimClippingStage(jobID, ClippingStageAnalysis, 2_000, time.Minute)
	if err != nil || !claimed {
		t.Fatalf("claim analysis stage for %s: stage=%+v claimed=%v error=%v", jobID, stage, claimed, err)
	}
	if err := store.SetClippingAnalysisClaimAttempt(jobID, stage.AttemptID); err != nil {
		t.Fatalf("bind analysis claim for %s: %v", jobID, err)
	}
	revision, err := store.ClippingAnalysisPipelineRevision()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetClippingAttemptPipelineRevision(jobID, ClippingStageAnalysis, stage.AttemptID, stage.LeaseToken, revision); err != nil {
		t.Fatalf("set pipeline revision for %s: %v", jobID, err)
	}
	if includeEstimate {
		if err := store.SetClippingAttemptEstimateTerms(jobID, ClippingStageAnalysis, stage.AttemptID, stage.LeaseToken, 25, 20); err != nil {
			t.Fatalf("set estimate terms for %s: %v", jobID, err)
		}
	}
	return stage
}

func clippingM3JobsForSources(t *testing.T, store *Store, sourceIDs []string, key string) (ClippingBatch, []ClippingJob) {
	t.Helper()
	jobs := make([]ClippingJobCreate, len(sourceIDs))
	for i, sourceID := range sourceIDs {
		jobs[i] = ClippingJobCreate{
			SourceID: sourceID, BudgetLimitMicroUSD: 10_000, ContentType: "general",
			MinClipSeconds: 15, MaxClipSeconds: 180, CandidateLimit: 10,
		}
	}
	batch, created, duplicate, err := store.CreateClippingBatch(ClippingBatchCreate{
		IdempotencyKey: key, BudgetLimitMicroUSD: int64(len(jobs)) * 10_000, Jobs: jobs,
	})
	if err != nil || duplicate || len(created) != len(jobs) {
		t.Fatalf("create clipping jobs: batch=%+v jobs=%+v duplicate=%v error=%v", batch, created, duplicate, err)
	}
	return batch, created
}

func clippingExpireAnalysisLeaseForTest(t *testing.T, store *Store, stage ClippingStage, expiredAt int64) {
	t.Helper()
	if _, err := store.db.Exec(`UPDATE clipping_stages SET lease_expires_at=? WHERE job_id=? AND name=?`, expiredAt, stage.JobID, stage.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE clipping_stage_attempts SET lease_expires_at=? WHERE attempt_id=?`, expiredAt, stage.AttemptID); err != nil {
		t.Fatal(err)
	}
	if err := store.RecoverExpiredClippingStages(time.Now().Unix()); err != nil {
		t.Fatalf("recover expired analysis lease: %v", err)
	}
}

func clippingM3VideoOnlyCoreForSource(source ClippingSource) clippingAnalysisCore {
	core := clippingM3CoreForSource(source)
	core.ModelVersions["transcription"] = "not-used-no-audio-stream"
	core.ModelVersions["model"] = "not-used-no-audio-stream"
	core.ModelVersions["model_snapshot"] = strings.Repeat("c", 40)
	core.ModelVersions["model_weights_sha256"] = "not-used-no-audio-stream"
	core.ModelVersions["ctranslate2_version"] = "not-used-no-audio-stream"
	core.AlgorithmVersions["audio_events"] = "not-run-no-audio-stream-v1"
	core.AlgorithmVersions["ffmpeg_configuration_sha256"] = strings.Repeat("d", 64)
	core.AlgorithmVersions["ffmpeg_executable_sha256"] = strings.Repeat("e", 64)
	core.AlgorithmVersions["python_distributions_sha256"] = strings.Repeat("f", 64)
	core.AlgorithmVersions["python_distributions_count"] = "17"
	core.AlgorithmVersions["os_release_sha256"] = strings.Repeat("a", 64)
	core.AlgorithmVersions["runtime_manifest_sha256"] = strings.Repeat("b", 64)
	core.MediaStreams = clippingMediaStreams{HasAudio: false, HasVideo: true}
	core.Transcript = clippingTranscript{
		Available: false, Status: "unavailable_no_audio_stream", OriginalScript: true,
		SpeakerLabelsAvailable: false, Segments: nil,
	}
	core.Context = clippingContext{}
	core.AudioAnalysisStatus = "skipped_no_audio_stream"
	core.AudioEvents = nil
	core.VisualEvents = []clippingSignalEvent{{
		StartMS: 70_000, EndMS: 82_000, Kind: "scene_motion", Score: 0.88,
		Evidence: json.RawMessage(`{"visual_evidence_strength":"weak"}`),
	}}
	core.CandidateInspections = []clippingCandidateInspection{{
		AnchorMS: 76_000, StartMS: 64_000, EndMS: 94_000,
		AnchorKind: "visual_scene_motion", AnchorScore: 0.88,
		SampleRateHz: 2, SampledFrames: 60,
		VisualEvents: []clippingSignalEvent{{
			StartMS: 74_000, EndMS: 78_000, Kind: "dense_motion_change", Score: 0.82,
			Evidence: json.RawMessage(`{"visual_evidence_strength":"weak","sample_rate_hz":2}`),
		}},
	}}
	core.Coverage = clippingAnalysisCoverage{
		AudioScannedDurationMS: 0, VisualScannedDurationMS: source.DurationMS,
	}
	return core
}

func TestClippingAnalysisClaimRecoversAfterStoreReopenBeforeAttemptBinding(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	source := makeReadyClippingSource(t, store, clippingM3FixtureDurationMS)
	_, jobs := clippingM3JobsForSources(t, store, []string{source.ID}, "m3-claim-reopen-before-attempt")
	job := jobs[0]
	if claimed, err := store.ClaimClippingAnalysisSource(job.ID); err != nil || !claimed {
		t.Fatalf("claim source before simulated crash: claimed=%v error=%v", claimed, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if completed, err := reopened.TryCompleteClippingJobFromAnalysisCache(job.ID); err != nil || completed {
		t.Fatalf("claim recovery unexpectedly found cached analysis: completed=%v error=%v", completed, err)
	}
	if claimed, err := reopened.ClaimClippingAnalysisSource(job.ID); err != nil || !claimed {
		t.Fatalf("same queued job could not resume its empty pre-dispatch claim: claimed=%v error=%v", claimed, err)
	}
	security, err := NewSecurity(reopened)
	if err != nil {
		t.Fatal(err)
	}
	app := &dashboardApp{store: reopened, security: security, callbackBaseURL: "https://framevault.dev"}
	prepared, claimed, err := app.prepareClippingStageDispatch(job.ID, 2_000, app.callbackBaseURL)
	if err != nil || !claimed || prepared.Payload.AttemptID == "" {
		t.Fatalf("prepare resumed claim without network dispatch: claimed=%v attempt=%q error=%v", claimed, prepared.Payload.AttemptID, err)
	}
	if err := reopened.SetClippingAnalysisClaimAttempt(job.ID, prepared.Payload.AttemptID); err != nil {
		t.Fatalf("bind resumed source claim to durable attempt: %v", err)
	}
	var attemptID string
	if err := reopened.db.QueryRow(`SELECT attempt_id FROM clipping_analysis_claims WHERE source_id=?`, source.ID).Scan(&attemptID); err != nil || attemptID != prepared.Payload.AttemptID {
		t.Fatalf("persisted resumed attempt=%q want=%q error=%v", attemptID, prepared.Payload.AttemptID, err)
	}
}

func TestClippingAnalysisClaimBlocksUnresolvedAttemptAcrossPipelineRevisions(t *testing.T) {
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	const revisionA = "m3-pipeline-claim-a"
	const revisionB = "m3-pipeline-claim-b"
	clippingSetTestPipelineRevision(t, store, security, revisionA)
	source := makeReadyClippingSource(t, store, clippingM3FixtureDurationMS)
	_, jobs := clippingM3JobsForSources(t, store, []string{source.ID, source.ID}, "m3-cross-revision-claim")
	jobA, jobB := jobs[0], jobs[1]
	stage := clippingM3ClaimAttempt(t, store, jobA.ID, true)
	clippingExpireAnalysisLeaseForTest(t, store, stage, time.Now().Unix()-1)

	clippingSetTestPipelineRevision(t, store, security, revisionB)
	if claimed, err := store.ClaimClippingAnalysisSource(jobB.ID); err != nil || claimed {
		t.Fatalf("new revision bypassed an unresolved prior attempt: claimed=%v error=%v", claimed, err)
	}
	var currentRevisionClaims int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM clipping_analysis_claims WHERE source_id=? AND pipeline_key=?`, source.ID, revisionB).Scan(&currentRevisionClaims); err != nil || currentRevisionClaims != 0 {
		t.Fatalf("blocked cross-revision attempt left a new claim: count=%d error=%v", currentRevisionClaims, err)
	}
	if _, duplicate, err := store.ReconcileClippingStageCost(jobA.ID, stage.AttemptID, 0, "cross-revision-known-zero"); err != nil || duplicate {
		t.Fatalf("record explicit zero for prior revision: duplicate=%v error=%v", duplicate, err)
	}
	if claimed, err := store.ClaimClippingAnalysisSource(jobB.ID); err != nil || claimed {
		t.Fatalf("new revision bypassed the prior callback window after invoice settlement: claimed=%v error=%v", claimed, err)
	}
	expiredLease := time.Now().Unix() - int64(clippingCallbackAccountingGrace.Seconds()) - 2
	if _, err := store.db.Exec(`UPDATE clipping_stage_attempts SET lease_expires_at=? WHERE attempt_id=?`, expiredLease, stage.AttemptID); err != nil {
		t.Fatal(err)
	}
	if claimed, err := store.ClaimClippingAnalysisSource(jobB.ID); err != nil || !claimed {
		t.Fatalf("new revision could not claim after prior invoice and callback expiry: claimed=%v error=%v", claimed, err)
	}
}

func TestClippingAnalysisClaimReopensForSameAndDifferentJobOnlyAfterInvoiceAndExpiry(t *testing.T) {
	store := newTestStore(t)
	source := makeReadyClippingSource(t, store, clippingM3FixtureDurationMS)
	_, jobs := clippingM3JobsForSources(t, store, []string{source.ID, source.ID}, "m3-claim-expiry-retry")
	job, otherJob := jobs[0], jobs[1]
	stage := clippingM3ClaimAttempt(t, store, job.ID, false)

	// Simulate an accepted dispatch whose lease expired without a callback.
	// The source's retention remains live so lease+24h is the callback bound.
	now := time.Now().Unix()
	clippingExpireAnalysisLeaseForTest(t, store, stage, now-1)
	if retryStage, err := store.RetryClippingStage(job.ID, ClippingStageAnalysis); !errors.Is(err, ErrClippingInvalidState) {
		t.Fatalf("unreconciled uncertain attempt became retryable: stage=%+v error=%v", retryStage, err)
	}
	if claimed, err := store.ClaimClippingAnalysisSource(otherJob.ID); err != nil || claimed {
		t.Fatalf("unsettled expired attempt released source claim: claimed=%v error=%v", claimed, err)
	}
	reconciledStage, duplicate, err := store.ReconcileClippingStageCost(job.ID, stage.AttemptID, 0, "invoice-zero-expired-worker")
	if err != nil || duplicate || reconciledStage.Status != ClippingStageFailed || !reconciledStage.CostReconciled || reconciledStage.ActualMicroUSD != 0 {
		t.Fatalf("settle known zero invoice for expired attempt: stage=%+v duplicate=%v error=%v", reconciledStage, duplicate, err)
	}
	if claimed, err := store.ClaimClippingAnalysisSource(job.ID); err != nil || claimed {
		t.Fatalf("settled attempt released its source claim before callback expiry: claimed=%v error=%v", claimed, err)
	}
	if retryStage, err := store.RetryClippingStage(job.ID, ClippingStageAnalysis); err == nil ||
		!strings.Contains(strings.ToLower(err.Error()), "callback") || !strings.Contains(strings.ToLower(err.Error()), "window") {
		t.Fatalf("retry before callback-window expiry: stage=%+v error=%v", retryStage, err)
	}
	expiredLease := time.Now().Unix() - int64(clippingCallbackAccountingGrace.Seconds()) - 2
	if _, err := store.db.Exec(`UPDATE clipping_stage_attempts SET lease_expires_at=? WHERE attempt_id=?`, expiredLease, stage.AttemptID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE clipping_stages SET lease_expires_at=? WHERE job_id=? AND name=?`, expiredLease, job.ID, ClippingStageAnalysis); err != nil {
		t.Fatal(err)
	}
	if claimed, err := store.ClaimClippingAnalysisSource(job.ID); err != nil || !claimed {
		t.Fatalf("same job could not reacquire settled expired analysis claim: claimed=%v error=%v", claimed, err)
	}
	if _, err := store.RetryClippingStage(job.ID, ClippingStageAnalysis); err != nil {
		t.Fatalf("same job retry after invoice and callback expiry: %v", err)
	}
	if err := store.ReleaseClippingAnalysisSourceClaim(job.ID); err != nil {
		t.Fatalf("release new pre-attempt claim for same-job retry test: %v", err)
	}
	if claimed, err := store.ClaimClippingAnalysisSource(otherJob.ID); err != nil || !claimed {
		t.Fatalf("different job could not claim after invoice and callback expiry: claimed=%v error=%v", claimed, err)
	}
}

func TestClippingRetryWaitsForSettledNoCallbackWindowExpiry(t *testing.T) {
	store := newTestStore(t)
	source := makeReadyClippingSource(t, store, clippingM3FixtureDurationMS)
	_, jobs := clippingM3JobsForSources(t, store, []string{source.ID, source.ID}, "m3-retry-after-callback-window")
	job, otherJob := jobs[0], jobs[1]
	stage := clippingM3ClaimAttempt(t, store, job.ID, true)

	// Expire the dispatch lease but keep its 24-hour callback capability live.
	clippingExpireAnalysisLeaseForTest(t, store, stage, time.Now().Unix()-1)
	reconciled, duplicate, err := store.ReconcileClippingStageCost(job.ID, stage.AttemptID, 177, "invoice-no-callback-window")
	if err != nil || duplicate || reconciled.Status != ClippingStageFailed || !reconciled.CostReconciled || reconciled.ActualMicroUSD != 177 {
		t.Fatalf("reconcile expired no-callback attempt: stage=%+v duplicate=%v error=%v", reconciled, duplicate, err)
	}
	if claimed, err := store.ClaimClippingAnalysisSource(otherJob.ID); err != nil || claimed {
		t.Fatalf("settled attempt released its source claim before callback expiry: claimed=%v error=%v", claimed, err)
	}
	if retryStage, err := store.RetryClippingStage(job.ID, ClippingStageAnalysis); err == nil ||
		!strings.Contains(strings.ToLower(err.Error()), "callback") || !strings.Contains(strings.ToLower(err.Error()), "window") {
		t.Fatalf("retry before callback-window expiry: stage=%+v error=%v", retryStage, err)
	}

	// Move the persisted lease back beyond the callback grace period. This
	// advances the capability clock deterministically without sleeping.
	expiredLease := time.Now().Unix() - int64(clippingCallbackAccountingGrace.Seconds()) - 2
	if _, err := store.db.Exec(`UPDATE clipping_stage_attempts SET lease_expires_at=? WHERE attempt_id=?`, expiredLease, stage.AttemptID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE clipping_stages SET lease_expires_at=? WHERE job_id=? AND name=?`, expiredLease, job.ID, ClippingStageAnalysis); err != nil {
		t.Fatal(err)
	}
	if claimed, err := store.ClaimClippingAnalysisSource(job.ID); err != nil || !claimed {
		t.Fatalf("same job could not reclaim its source after callback expiry: claimed=%v error=%v", claimed, err)
	}
	retried, err := store.RetryClippingStage(job.ID, ClippingStageAnalysis)
	if err != nil || retried.Status != ClippingStageQueued || retried.AttemptID != "" {
		t.Fatalf("retry after callback expiry: stage=%+v error=%v", retried, err)
	}
	storedStages, err := store.ClippingStages(job.ID)
	var storedRetry ClippingStage
	for _, storedStage := range storedStages {
		if storedStage.Name == ClippingStageAnalysis {
			storedRetry = storedStage
			break
		}
	}
	if err != nil || storedRetry.Status != ClippingStageQueued || storedRetry.AttemptID != "" || storedRetry.ReservedMicroUSD != 0 {
		t.Fatalf("retry did not clear the expired attempt in storage: stage=%+v error=%v", storedRetry, err)
	}
}

func TestClippingCanceledAttemptNeedsInvoiceAndCallbackExpiryBeforeNextJob(t *testing.T) {
	store := newTestStore(t)
	source := makeReadyClippingSource(t, store, clippingM3FixtureDurationMS)
	_, jobs := clippingM3JobsForSources(t, store, []string{source.ID, source.ID}, "m3-canceled-claim-expiry")
	canceledJob, nextJob := jobs[0], jobs[1]
	stage := clippingM3ClaimAttempt(t, store, canceledJob.ID, true)
	if err := store.CancelClippingJob(canceledJob.ID); err != nil {
		t.Fatal(err)
	}
	if _, duplicate, err := store.ReconcileClippingStageCost(canceledJob.ID, stage.AttemptID, 177, "invoice-canceled-worker"); err != nil || duplicate {
		t.Fatalf("settle canceled worker invoice: duplicate=%v error=%v", duplicate, err)
	}
	if claimed, err := store.ClaimClippingAnalysisSource(nextJob.ID); err != nil || claimed {
		t.Fatalf("canceled attempt released claim before callback expiry: claimed=%v error=%v", claimed, err)
	}
	if _, err := store.db.Exec(`UPDATE clipping_sources SET retain_until=? WHERE id=?`, time.Now().Unix()-1, source.ID); err != nil {
		t.Fatal(err)
	}
	if claimed, err := store.ClaimClippingAnalysisSource(nextJob.ID); err != nil || !claimed {
		t.Fatalf("next job could not claim after canceled attempt invoice and callback expiry: claimed=%v error=%v", claimed, err)
	}
}

func TestClippingExpiredRetentionWaitsForCallbackAndPreservesAudit(t *testing.T) {
	store, security, _, _ := clippingDashboardFixture(t)
	source := makeReadyClippingSource(t, store, clippingM3FixtureDurationMS)
	batch, jobs := clippingM3JobsForSources(t, store, []string{source.ID}, "m3-retention-callback-window-audit")
	job := jobs[0]
	if claimedSource, err := store.ClaimClippingAnalysisSource(job.ID); err != nil || !claimedSource {
		t.Fatalf("claim source before dispatch: claimed=%v error=%v", claimedSource, err)
	}
	app := &dashboardApp{store: store, security: security}
	prepared, claimed, err := app.prepareClippingStageDispatch(job.ID, 2_000, "https://framevault.dev")
	if err != nil || !claimed {
		t.Fatalf("prepare no-callback dispatch for retention test: claimed=%v error=%v", claimed, err)
	}
	stagesBefore, err := store.ClippingStages(job.ID)
	if err != nil || len(stagesBefore) != 1 {
		t.Fatalf("load dispatched stage: stages=%+v error=%v", stagesBefore, err)
	}
	stage := stagesBefore[0]
	if err := store.SetClippingAnalysisClaimAttempt(job.ID, stage.AttemptID); err != nil {
		t.Fatalf("bind source claim to attempt: %v", err)
	}

	// Seed a valid artifact and cache alongside the in-flight attempt so source
	// retention removes derived content while preserving the audit rows.
	core := clippingM3CoreForSource(source)
	revision, err := store.ClippingAnalysisPipelineRevision()
	if err != nil {
		t.Fatal(err)
	}
	core.PipelineRevision = revision
	core.AlgorithmVersions["pipeline_revision"] = revision
	pipelineKey, err := clippingAnalysisPipelineKey(core)
	if err != nil {
		t.Fatalf("compute analysis pipeline key: %v", err)
	}
	corePayload := mustJSONClippingM3(t, core)
	artifact, err := buildClippingAnalysisArtifact(job, source, core, 1, 0, 0, 0)
	if err != nil {
		t.Fatalf("build source-derived fixture artifact: %v", err)
	}
	rangesJSON, err := validateClippingArtifact(artifact, job.ID, source.DurationMS)
	if err != nil {
		t.Fatalf("validate source-derived fixture artifact: %v", err)
	}
	tx, err := store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := insertClippingArtifact(tx, artifact, rangesJSON, time.Now().Unix()); err != nil {
		tx.Rollback()
		t.Fatalf("insert source-derived fixture artifact: %v", err)
	}
	if _, err := tx.Exec(`INSERT INTO clipping_analysis_cache(source_id,source_sha256,pipeline_key,pipeline_revision,source_duration_ms,payload_json,created_at)
		VALUES(?,?,?,?,?,?,?)`, source.ID, source.SHA256, pipelineKey, core.PipelineRevision, source.DurationMS, corePayload, time.Now().Unix()); err != nil {
		tx.Rollback()
		t.Fatalf("insert source-derived fixture cache: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.LoadClippingAnalysisCache(source.ID); err != nil || !found {
		t.Fatalf("source cache invalid before tombstone: found=%v error=%v", found, err)
	}
	if artifacts, err := store.ClippingArtifacts(job.ID); err != nil || len(artifacts) != 1 {
		t.Fatalf("source artifact missing before retention cleanup: artifacts=%+v error=%v", artifacts, err)
	}
	indexExpiry := prepared.Payload.CallbackExpiresAt
	var indexedExpiry int64
	if err := store.db.QueryRow(`SELECT expires_at FROM clipping_capability_expiry_index WHERE job_id=? AND attempt_id=?`, job.ID, stage.AttemptID).Scan(&indexedExpiry); err != nil || indexedExpiry != indexExpiry {
		t.Fatalf("callback expiry index=%d want %d error=%v", indexedExpiry, indexExpiry, err)
	}

	clippingExpireAnalysisLeaseForTest(t, store, stage, time.Now().Unix()-1)
	realNow := time.Now().Unix()
	if _, err := store.db.Exec(`UPDATE clipping_sources SET retain_until=? WHERE id=?`, realNow-1, source.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.ExpireClippingSource(source.ID, realNow); err == nil || !strings.Contains(strings.ToLower(err.Error()), "callback") {
		t.Fatalf("expired retention with live callback capability: error=%v", err)
	}
	if _, found, err := store.LoadClippingAnalysisCache(source.ID); err != nil || !found {
		t.Fatalf("refused tombstone removed cache before callback expiry: found=%v error=%v", found, err)
	}

	jobBefore, err := store.ClippingJob(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	batchBefore, err := store.ClippingBatch(batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if jobBefore.Status != ClippingJobPausedBudget || jobBefore.ReservedMicroUSD <= 0 || jobBefore.SpentMicroUSD != 0 {
		t.Fatalf("expected retained paused-budget hold before cleanup: %+v", jobBefore)
	}
	var attemptBefore struct {
		Status                                                  string
		Reserved, Actual, Reconciled, CallbackReceived, Settled int64
	}
	if err := store.db.QueryRow(`SELECT status,reserved_micro_usd,actual_micro_usd,cost_reconciled,callback_received_at,settled_at
		FROM clipping_stage_attempts WHERE attempt_id=?`, stage.AttemptID).Scan(
		&attemptBefore.Status, &attemptBefore.Reserved, &attemptBefore.Actual, &attemptBefore.Reconciled, &attemptBefore.CallbackReceived, &attemptBefore.Settled); err != nil {
		t.Fatal(err)
	}
	later := indexExpiry + 1
	if err := store.ExpireClippingSource(source.ID, later); err != nil {
		t.Fatalf("tombstone expired source after callback capability expiry: %v", err)
	}
	deleted, err := store.ClippingSource(source.ID)
	if err != nil || deleted.Status != ClippingSourceDeleted {
		t.Fatalf("source tombstone=%+v error=%v", deleted, err)
	}
	jobAfter, err := store.ClippingJob(job.ID)
	if err != nil || jobAfter.Status != jobBefore.Status || jobAfter.ReservedMicroUSD != jobBefore.ReservedMicroUSD || jobAfter.SpentMicroUSD != jobBefore.SpentMicroUSD {
		t.Fatalf("retention cleanup changed paused job accounting: before=%+v after=%+v error=%v", jobBefore, jobAfter, err)
	}
	batchAfter, err := store.ClippingBatch(batchBefore.ID)
	if err != nil || batchAfter.ReservedMicroUSD != batchBefore.ReservedMicroUSD || batchAfter.SpentMicroUSD != batchBefore.SpentMicroUSD {
		t.Fatalf("retention cleanup changed batch accounting: before=%+v after=%+v error=%v", batchBefore, batchAfter, err)
	}
	stagesAfter, err := store.ClippingStages(job.ID)
	if err != nil || len(stagesAfter) != 1 || stagesAfter[0].Status != ClippingStageUncertain || stagesAfter[0].AttemptID != stage.AttemptID ||
		stagesAfter[0].ReservedMicroUSD != stage.ReservedMicroUSD {
		t.Fatalf("retention cleanup changed stage audit: stages=%+v before=%+v error=%v", stagesAfter, stage, err)
	}
	var attemptAfter struct {
		Status                                                  string
		Reserved, Actual, Reconciled, CallbackReceived, Settled int64
	}
	if err := store.db.QueryRow(`SELECT status,reserved_micro_usd,actual_micro_usd,cost_reconciled,callback_received_at,settled_at
		FROM clipping_stage_attempts WHERE attempt_id=?`, stage.AttemptID).Scan(
		&attemptAfter.Status, &attemptAfter.Reserved, &attemptAfter.Actual, &attemptAfter.Reconciled, &attemptAfter.CallbackReceived, &attemptAfter.Settled); err != nil {
		t.Fatal(err)
	}
	if attemptAfter != attemptBefore {
		t.Fatalf("retention cleanup changed attempt audit: before=%+v after=%+v", attemptBefore, attemptAfter)
	}
	var jobsRetained, stagesRetained, attemptsRetained, claimsRetained int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM clipping_jobs WHERE source_id=?`, source.ID).Scan(&jobsRetained); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM clipping_stages WHERE job_id IN (SELECT id FROM clipping_jobs WHERE source_id=?)`, source.ID).Scan(&stagesRetained); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM clipping_stage_attempts WHERE job_id IN (SELECT id FROM clipping_jobs WHERE source_id=?)`, source.ID).Scan(&attemptsRetained); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM clipping_analysis_claims WHERE source_id=?`, source.ID).Scan(&claimsRetained); err != nil {
		t.Fatal(err)
	}
	if jobsRetained != 1 || stagesRetained != 1 || attemptsRetained != 1 || claimsRetained != 0 {
		t.Fatalf("retention tombstone rows jobs=%d stages=%d attempts=%d claims=%d", jobsRetained, stagesRetained, attemptsRetained, claimsRetained)
	}
	if artifacts, err := store.ClippingArtifacts(job.ID); err != nil || len(artifacts) != 0 {
		t.Fatalf("tombstone retained derived artifacts: artifacts=%+v error=%v", artifacts, err)
	}
	if _, found, err := store.LoadClippingAnalysisCache(source.ID); err != nil || found {
		t.Fatalf("tombstone retained source cache: found=%v error=%v", found, err)
	}
}

func TestClippingCapabilityCleanupUsesPersistedExpiryAfterRestart(t *testing.T) {
	store, security, _, _ := clippingDashboardFixture(t)
	source := makeReadyClippingSource(t, store, clippingM3FixtureDurationMS)
	_, jobs := clippingM3JobsForSources(t, store, []string{source.ID}, "m3-capability-cleanup-restart")
	job := jobs[0]
	stage, claimed, err := store.ClaimClippingStage(job.ID, ClippingStageAnalysis, 2_000, time.Minute)
	if err != nil || !claimed {
		t.Fatalf("claim capability cleanup stage: stage=%+v claimed=%v error=%v", stage, claimed, err)
	}
	app := &dashboardApp{store: store, security: security}
	callbackExpiry := stage.LeaseExpiresAt + 30
	if callbackExpiry > source.RetainUntil {
		callbackExpiry = source.RetainUntil
	}
	mediaToken, callbackToken, err := app.persistClippingDispatchCapabilitiesUntil(stage, callbackExpiry)
	if err != nil || mediaToken == "" || callbackToken == "" {
		t.Fatalf("persist encrypted attempt capabilities: media=%q callback=%q error=%v", mediaToken, callbackToken, err)
	}
	settings := []string{
		clippingCapabilityMedia,
		clippingCapabilityCallback,
		clippingCapabilityLease,
		clippingCapabilityMedia + "_expiry",
		clippingCapabilityCallback + "_expiry",
	}
	for _, scope := range settings {
		key := clippingCapabilitySettingKey(job.ID, stage.AttemptID, scope)
		value, err := store.Setting(key)
		if err != nil || value == "" {
			t.Fatalf("persisted capability setting %q missing: value=%q error=%v", scope, value, err)
		}
		if scope == clippingCapabilityCallback && value == callbackToken {
			t.Fatal("callback bearer was stored in plaintext")
		}
	}
	var indexedExpiry int64
	if err := store.db.QueryRow(`SELECT expires_at FROM clipping_capability_expiry_index WHERE job_id=? AND attempt_id=?`, job.ID, stage.AttemptID).Scan(&indexedExpiry); err != nil || indexedExpiry != callbackExpiry {
		t.Fatalf("persisted callback expiry index=%d want %d error=%v", indexedExpiry, callbackExpiry, err)
	}
	if cleaned, err := app.cleanupExpiredClippingCapabilities(time.Now().Unix(), 10); err != nil || cleaned != 0 {
		t.Fatalf("cleanup ran before capability expiry: cleaned=%d error=%v", cleaned, err)
	}
	if decrypted, err := app.clippingCapability(job.ID, stage.AttemptID, clippingCapabilityCallback); err != nil || decrypted != callbackToken {
		t.Fatalf("unexpired callback did not remain decryptable: token=%q error=%v", decrypted, err)
	}

	// Retire this attempt and create a newer current stage attempt. Cleanup must
	// follow the expiry index key rather than depending on the stage's current ID.
	if _, err := store.FailClippingStageBeforeSubmit(job.ID, ClippingStageAnalysis, stage.AttemptID, stage.LeaseToken, "fixture retired before submit"); err != nil {
		t.Fatalf("retire capability-bearing attempt: %v", err)
	}
	if _, err := store.RetryClippingStage(job.ID, ClippingStageAnalysis); err != nil {
		t.Fatalf("retry after known-zero retired attempt: %v", err)
	}
	newStage, claimed, err := store.ClaimClippingStage(job.ID, ClippingStageAnalysis, 2_000, time.Minute)
	if err != nil || !claimed || newStage.AttemptID == stage.AttemptID {
		t.Fatalf("create a newer current attempt: stage=%+v claimed=%v error=%v", newStage, claimed, err)
	}

	dataDir := store.dataDir
	if err := store.Close(); err != nil {
		t.Fatalf("close store before cleanup restart: %v", err)
	}
	reopened, err := OpenStore(dataDir)
	if err != nil {
		t.Fatalf("reopen store before capability cleanup: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	reopenedSecurity, err := NewSecurity(reopened)
	if err != nil {
		t.Fatalf("restore encryption key after restart: %v", err)
	}
	reopenedApp := &dashboardApp{store: reopened, security: reopenedSecurity}
	if decrypted, err := reopenedApp.clippingCapability(job.ID, stage.AttemptID, clippingCapabilityCallback); err != nil || decrypted != callbackToken {
		t.Fatalf("persisted callback capability did not survive restart: token=%q error=%v", decrypted, err)
	}
	cleaned, err := reopenedApp.cleanupExpiredClippingCapabilities(callbackExpiry+1, 10)
	if err != nil || cleaned != 1 {
		t.Fatalf("cleanup expired historical attempt after restart: cleaned=%d error=%v", cleaned, err)
	}
	for _, scope := range settings {
		key := clippingCapabilitySettingKey(job.ID, stage.AttemptID, scope)
		var count int
		if err := reopened.db.QueryRow(`SELECT COUNT(*) FROM settings WHERE key=?`, key).Scan(&count); err != nil || count != 0 {
			t.Fatalf("expired capability setting %q remains: count=%d error=%v", scope, count, err)
		}
	}
	var remainingIndex int
	if err := reopened.db.QueryRow(`SELECT COUNT(*) FROM clipping_capability_expiry_index WHERE job_id=? AND attempt_id=?`, job.ID, stage.AttemptID).Scan(&remainingIndex); err != nil || remainingIndex != 0 {
		t.Fatalf("expired historical attempt index remains: count=%d error=%v", remainingIndex, err)
	}
	if _, err := reopenedApp.clippingCapability(job.ID, stage.AttemptID, clippingCapabilityCallback); err == nil {
		t.Fatal("expired callback capability remained decryptable after cleanup")
	}
	if cleanedAgain, err := reopenedApp.cleanupExpiredClippingCapabilities(callbackExpiry+1, 10); err != nil || cleanedAgain != 0 {
		t.Fatalf("capability cleanup was not idempotent: cleaned=%d error=%v", cleanedAgain, err)
	}
	currentStages, err := reopened.ClippingStages(job.ID)
	if err != nil || len(currentStages) != 1 || currentStages[0].AttemptID != newStage.AttemptID || currentStages[0].Status != ClippingStageRunning {
		t.Fatalf("historical capability cleanup changed current stage: stages=%+v error=%v", currentStages, err)
	}
}

func TestClippingPreSubmitKnownZeroFailureUsesDedicatedClaimRelease(t *testing.T) {
	store := newTestStore(t)
	source := makeReadyClippingSource(t, store, clippingM3FixtureDurationMS)
	_, jobs := clippingM3JobsForSources(t, store, []string{source.ID, source.ID}, "m3-presubmit-claim-release")
	job, nextJob := jobs[0], jobs[1]
	stage := clippingM3ClaimAttempt(t, store, job.ID, false)
	failedStage, err := store.FailClippingStageBeforeSubmit(job.ID, ClippingStageAnalysis, stage.AttemptID, stage.LeaseToken, "dispatch preparation failed before submit")
	if err != nil || failedStage.Status != ClippingStageFailed || failedStage.ActualMicroUSD != 0 {
		t.Fatalf("record known-zero pre-submit failure: %v", err)
	}
	var actual, costReconciled, settledAt int64
	var reference string
	if err := store.db.QueryRow(`SELECT actual_micro_usd,cost_reconciled,reconciliation_reference,settled_at
		FROM clipping_stage_attempts WHERE attempt_id=?`, stage.AttemptID).Scan(&actual, &costReconciled, &reference, &settledAt); err != nil {
		t.Fatal(err)
	}
	if actual != 0 || costReconciled != 1 || reference != "known_no_submission" || settledAt == 0 {
		t.Fatalf("pre-submit failure did not record reconciled known-zero settlement: actual=%d reconciled=%d reference=%q settled_at=%d", actual, costReconciled, reference, settledAt)
	}
	storedStages, err := store.ClippingStages(job.ID)
	var storedAnalysisStage ClippingStage
	for _, storedStage := range storedStages {
		if storedStage.Name == ClippingStageAnalysis {
			storedAnalysisStage = storedStage
			break
		}
	}
	if err != nil || storedAnalysisStage.Name == "" || !storedAnalysisStage.CostReconciled || storedAnalysisStage.ActualMicroUSD != 0 {
		t.Fatalf("pre-submit failure did not mark zero-cost stage reconciled: stages=%+v error=%v", storedStages, err)
	}
	if claimed, err := store.ClaimClippingAnalysisSource(nextJob.ID); err != nil || claimed {
		t.Fatalf("ordinary claim release dropped an attempt-bound pre-submit claim: claimed=%v error=%v", claimed, err)
	}
	if err := store.ReleaseClippingAnalysisSourceClaimBeforeSubmit(job.ID, stage.AttemptID); err != nil {
		t.Fatalf("dedicated known-zero release failed: %v", err)
	}
	if claimed, err := store.ClaimClippingAnalysisSource(nextJob.ID); err != nil || !claimed {
		t.Fatalf("next job could not claim after known-zero pre-submit release: claimed=%v error=%v", claimed, err)
	}
}

func TestClippingLateV2CallbackPreservesInvoiceSettlementWithoutPublishing(t *testing.T) {
	store := newTestStore(t)
	source := makeReadyClippingSource(t, store, clippingM3FixtureDurationMS)
	batch, jobs := clippingM3JobsForSources(t, store, []string{source.ID}, "m3-invoice-before-late-callback")
	job := jobs[0]
	stage := clippingM3ClaimAttempt(t, store, job.ID, true)
	if err := store.CancelClippingJob(job.ID); err != nil {
		t.Fatal(err)
	}
	const actualCost int64 = 287
	const invoiceReference = "modal-invoice-before-late-callback"
	if _, duplicate, err := store.ReconcileClippingStageCost(job.ID, stage.AttemptID, actualCost, invoiceReference); err != nil || duplicate {
		t.Fatalf("settle invoice before callback: duplicate=%v error=%v", duplicate, err)
	}
	core := clippingM3CoreForSource(source)
	callbackArtifact := clippingM3CallbackArtifact(job.ID, source.DurationMS)
	settledStage, duplicate, published, err := store.CompleteClippingStageEstimate(
		job.ID, ClippingStageAnalysis, stage.AttemptID, stage.LeaseToken, 250, 10, callbackArtifact, core,
	)
	if err != nil || duplicate || published || settledStage.Status != ClippingStageCanceled || !settledStage.CostReconciled ||
		settledStage.ActualMicroUSD != actualCost || settledStage.ReservedMicroUSD != 0 {
		t.Fatalf("late callback changed invoice-settled stage or published: stage=%+v duplicate=%v published=%v error=%v", settledStage, duplicate, published, err)
	}
	var attemptActual, reconciled, settledAt int64
	var storedReference string
	if err := store.db.QueryRow(`SELECT actual_micro_usd,cost_reconciled,reconciliation_reference,settled_at
		FROM clipping_stage_attempts WHERE attempt_id=?`, stage.AttemptID).Scan(&attemptActual, &reconciled, &storedReference, &settledAt); err != nil {
		t.Fatal(err)
	}
	if attemptActual != actualCost || reconciled != 1 || storedReference != invoiceReference || settledAt == 0 {
		t.Fatalf("late callback overwrote invoice attempt: actual=%d reconciled=%d reference=%q settled_at=%d", attemptActual, reconciled, storedReference, settledAt)
	}
	if artifacts, err := store.ClippingArtifacts(job.ID); err != nil || len(artifacts) != 0 {
		t.Fatalf("canceled late callback published analysis artifact: artifacts=%+v error=%v", artifacts, err)
	}
	if _, found, err := store.LoadClippingAnalysisCache(source.ID); err != nil || found {
		t.Fatalf("canceled late callback populated analysis cache: found=%v error=%v", found, err)
	}
	_, duplicate, published, err = store.CompleteClippingStageEstimate(
		job.ID, ClippingStageAnalysis, stage.AttemptID, stage.LeaseToken, 250, 10, callbackArtifact, core,
	)
	if err != nil || !duplicate || published {
		t.Fatalf("duplicate late callback: duplicate=%v published=%v error=%v", duplicate, published, err)
	}
	settledBatch, err := store.ClippingBatch(batch.ID)
	if err != nil || settledBatch.SpentMicroUSD != actualCost || settledBatch.ReservedMicroUSD != 0 {
		t.Fatalf("late callback changed invoice batch accounting: batch=%+v error=%v", settledBatch, err)
	}
}

func TestClippingLargeM3CoreEnrichesWithinArtifactLimitAndRejectsInvalidCores(t *testing.T) {
	store := newTestStore(t)
	source := makeReadyClippingSource(t, store, clippingM3FixtureDurationMS)
	core := clippingM3CoreForSource(source)
	largeText := strings.Repeat("w", 8_100)
	core.Transcript.Segments = make([]clippingTranscriptSegment, 0, 360)
	for i := 0; i < 360; i++ {
		start := int64(i) * 1_000
		core.Transcript.Segments = append(core.Transcript.Segments, clippingTranscriptSegment{
			StartMS: start, EndMS: start + 1_000, Text: largeText,
		})
	}
	coreJSON, err := json.Marshal(core)
	if err != nil {
		t.Fatal(err)
	}
	const minCoreBytes = 2_936_013 // 2.8 MiB
	const maxCoreBytes = 3_145_728 // 3 MiB
	if len(coreJSON) < minCoreBytes || len(coreJSON) > maxCoreBytes {
		t.Fatalf("fixture core size=%d bytes, want %d..%d", len(coreJSON), minCoreBytes, maxCoreBytes)
	}
	job := ClippingJob{
		ID: "clipjob_large_core", SourceID: source.ID, ContentType: "general",
		MinClipSeconds: 15, MaxClipSeconds: 180, CandidateLimit: 10,
	}
	artifact, err := buildClippingAnalysisArtifact(job, source, core, 1, 0, 0, 0)
	if err != nil {
		t.Fatalf("enrich near-limit core: %v", err)
	}
	if len(artifact.PayloadJSON) > maxClippingArtifactPayloadBytes {
		t.Fatalf("enriched analysis payload=%d bytes exceeds %d", len(artifact.PayloadJSON), maxClippingArtifactPayloadBytes)
	}
	if _, err := decodeClippingAnalysisCore("{malformed", source); !errors.Is(err, ErrClippingInvalidArtifact) {
		t.Fatalf("malformed core error=%v", err)
	}
	if _, err := decodeClippingAnalysisCore(strings.Repeat("x", maxClippingArtifactPayloadBytes+1), source); !errors.Is(err, ErrClippingInvalidArtifact) {
		t.Fatalf("oversized core error=%v", err)
	}
}

func TestClippingAnalysisCoreRejectsPythonTranscriptLimitOverflow(t *testing.T) {
	store := newTestStore(t)
	source := makeReadyClippingSource(t, store, clippingM3FixtureDurationMS)
	core := clippingM3CoreForSource(source)
	core.Transcript.Segments = make([]clippingTranscriptSegment, 20_001)
	for index := range core.Transcript.Segments {
		start := int64(index) * 16
		core.Transcript.Segments[index] = clippingTranscriptSegment{
			StartMS: start, EndMS: start + 16, Text: "x",
		}
	}
	encoded, err := json.Marshal(core)
	if err != nil {
		t.Fatal(err)
	}
	if len(core.Transcript.Segments) != 20_001 {
		t.Fatalf("cross-language overflow fixture has %d transcript segments, want 20001", len(core.Transcript.Segments))
	}
	if _, err := decodeClippingAnalysisCore(string(encoded), source); !errors.Is(err, ErrClippingInvalidArtifact) {
		t.Fatalf("Go accepted core above Python's 20000 transcript-segment limit: %v", err)
	}
}

func TestClippingProfileContinuityEvidenceDoesNotClaimFacecamDetection(t *testing.T) {
	store := newTestStore(t)
	source := makeReadyClippingSource(t, store, clippingM3FixtureDurationMS)
	tests := []struct {
		contentType string
		kind        string
		detailCue   string
		segments    []clippingTranscriptSegment
		audio       []clippingSignalEvent
	}{
		{
			contentType: "comedy", kind: "comedy_setup_punchline", detailCue: "lexical setup and later payoff",
			segments: []clippingTranscriptSegment{
				{StartMS: 0, EndMS: 3_000, Text: "The setup is that I thought this would be easy."},
				{StartMS: 3_000, EndMS: 6_000, Text: "But actually the joke is ridiculous; that is the punchline."},
				{StartMS: 6_000, EndMS: 8_000, Text: "Ha ha, laughter!"},
			},
		},
		{
			contentType: "podcast", kind: "podcast_intro_payoff", detailCue: "lexical topic-introduction and later payoff",
			segments: []clippingTranscriptSegment{
				{StartMS: 0, EndMS: 4_000, Text: "Welcome back. Today we are going to discuss the main question."},
				{StartMS: 20_000, EndMS: 24_000, Text: "Finally, the answer explains what happened and that is the takeaway."},
			},
		},
		{
			contentType: "gaming", kind: "gaming_action_with_commentary", detailCue: "measured audio or visual timeline signal",
			segments: []clippingTranscriptSegment{
				{StartMS: 20_000, EndMS: 24_000, Text: "I defeated the boss and won the round!"},
				{StartMS: 25_000, EndMS: 28_000, Text: "I did not expect that, what a clutch play."},
			},
			audio: []clippingSignalEvent{{StartMS: 27_000, EndMS: 29_000, Kind: "non_speech_audio_transient", Score: 0.82}},
		},
		{
			contentType: "movie", kind: "movie_dialogue_sequence", detailCue: "source-timed dialogue segments",
			segments: []clippingTranscriptSegment{
				{StartMS: 0, EndMS: 2_000, Text: "Are you sure we should go inside?"},
				{StartMS: 2_300, EndMS: 4_800, Text: "There is no turning back now."},
				{StartMS: 5_000, EndMS: 7_000, Text: "Then tell me what happened."},
			},
		},
	}
	for index, fixture := range tests {
		t.Run(fixture.contentType, func(t *testing.T) {
			core := clippingM3CoreForSource(source)
			core.Transcript.Segments = fixture.segments
			if len(fixture.audio) > 0 {
				core.AudioEvents = append(core.AudioEvents, fixture.audio...)
			}
			job := ClippingJob{
				ID: fmt.Sprintf("clipjob_profile_%d", index), SourceID: source.ID, ContentType: fixture.contentType,
				MinClipSeconds: 15, MaxClipSeconds: 30, CandidateLimit: 10,
			}
			artifact, err := buildClippingAnalysisArtifact(job, source, core, 1, 0, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			var payload struct {
				Candidates []clippingCandidate `json:"candidates"`
			}
			if err := json.Unmarshal([]byte(artifact.PayloadJSON), &payload); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, candidate := range payload.Candidates {
				for _, evidence := range candidate.Evidence {
					detail := strings.ToLower(evidence.Detail)
					if evidence.Kind == fixture.kind {
						found = found || strings.Contains(detail, fixture.detailCue)
					}
					for _, unsupported := range []string{"facecam detected", "facecam is visible", "facecam shows", "visual action detected", "camera shows"} {
						if strings.Contains(detail, unsupported) {
							t.Fatalf("profile evidence made an unsupported visual detection claim: %+v", evidence)
						}
					}
					if fixture.contentType == "gaming" && evidence.Kind == fixture.kind && strings.Contains(detail, fixture.detailCue) &&
						!strings.Contains(detail, "no facecam or visual action is identified") {
						t.Fatalf("gaming profile evidence does not limit its claim to measured signals: %+v", evidence)
					}
				}
			}
			if !found {
				t.Fatalf("profile %s did not preserve continuity evidence %s; candidates=%+v", fixture.contentType, fixture.kind, payload.Candidates)
			}
		})
	}
}

func TestClippingVideoOnlyAnalysisAcceptsNoAudioProvenanceAndSelectsVisualEvidence(t *testing.T) {
	store := newTestStore(t)
	source := makeReadyClippingSource(t, store, clippingM3FixtureDurationMS)
	core := clippingM3VideoOnlyCoreForSource(source)
	revision, err := store.ClippingAnalysisPipelineRevision()
	if err != nil {
		t.Fatal(err)
	}
	core.PipelineRevision = revision
	core.AlgorithmVersions["pipeline_revision"] = revision
	coreJSON, err := json.Marshal(core)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeClippingAnalysisCore(string(coreJSON), source); err != nil {
		t.Fatalf("decode valid video-only core: %v", err)
	}
	_, jobs, duplicate, err := store.CreateClippingBatch(ClippingBatchCreate{
		IdempotencyKey: "m3-video-only-persist-callback", BudgetLimitMicroUSD: 10_000,
		Jobs: []ClippingJobCreate{{
			SourceID: source.ID, BudgetLimitMicroUSD: 10_000, ContentType: "general",
			MinClipSeconds: 15, MaxClipSeconds: 30, CandidateLimit: 10,
		}},
	})
	if err != nil || duplicate || len(jobs) != 1 {
		t.Fatalf("create video-only clipping job: jobs=%+v duplicate=%v error=%v", jobs, duplicate, err)
	}
	job := jobs[0]
	stage := clippingM3ClaimAttempt(t, store, job.ID, true)
	completed, callbackDuplicate, published, err := store.CompleteClippingStageEstimate(
		job.ID, ClippingStageAnalysis, stage.AttemptID, stage.LeaseToken, 250, 10,
		clippingM3CallbackArtifact(job.ID, source.DurationMS), core,
	)
	if err != nil || callbackDuplicate || !published || completed.Status != ClippingStageCompleted {
		t.Fatalf("persist video-only callback result: stage=%+v duplicate=%v published=%v error=%v", completed, callbackDuplicate, published, err)
	}
	artifacts, err := store.ClippingArtifacts(job.ID)
	if err != nil || len(artifacts) != 1 {
		t.Fatalf("load persisted video-only artifact: artifacts=%+v error=%v", artifacts, err)
	}
	candidates := clippingM3Candidates(t, artifacts[0])
	if len(candidates) == 0 {
		t.Fatal("video-only analysis produced no visual candidates")
	}
	visualEvidenceFound := false
	for _, candidate := range candidates {
		for _, evidence := range candidate.Evidence {
			if evidence.Kind == "transcript" || strings.HasPrefix(evidence.Kind, "audio_") {
				t.Fatalf("video-only candidate contains unavailable audio evidence: %+v", evidence)
			}
			if evidence.Kind == "visual_scene_motion" {
				visualEvidenceFound = true
			}
		}
	}
	if !visualEvidenceFound {
		t.Fatalf("video-only candidates lack visual event evidence: %+v", candidates)
	}

	invalidMutations := []struct {
		name   string
		mutate func(*clippingAnalysisCore)
	}{
		{name: "transcript_available", mutate: func(mutated *clippingAnalysisCore) { mutated.Transcript.Available = true }},
		{name: "transcript_status", mutate: func(mutated *clippingAnalysisCore) { mutated.Transcript.Status = "complete" }},
		{name: "audio_status", mutate: func(mutated *clippingAnalysisCore) { mutated.AudioAnalysisStatus = "complete" }},
		{name: "audio_coverage", mutate: func(mutated *clippingAnalysisCore) { mutated.Coverage.AudioScannedDurationMS = source.DurationMS }},
	}
	for _, mutation := range invalidMutations {
		t.Run(mutation.name, func(t *testing.T) {
			mutated := core
			mutation.mutate(&mutated)
			payload, err := json.Marshal(mutated)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeClippingAnalysisCore(string(payload), source); !errors.Is(err, ErrClippingInvalidArtifact) {
				t.Fatalf("inconsistent video-only provenance error=%v", err)
			}
		})
	}
}

func TestClippingVideoOnlyPythonWireArtifactPassesGoCallback(t *testing.T) {
	fixture, err := os.ReadFile("../../workers/modal/testdata/video_only_source_core.v2.json")
	if err != nil {
		t.Fatal(err)
	}
	var core clippingAnalysisCore
	if err := json.Unmarshal(fixture, &core); err != nil {
		t.Fatalf("decode Python worker source-core fixture: %v", err)
	}
	if core.AlgorithmVersions["audio_events"] != "not-run-no-audio-stream-v1" {
		t.Fatalf("Python video-only audio provenance=%v", core.AlgorithmVersions["audio_events"])
	}
	if err := decodeVideoOnlyFixtureSource(t, core); err != nil {
		t.Fatalf("validate Python worker video-only core: %v", err)
	}
	store := newTestStore(t)
	if err := store.SetSetting(clippingAnalysisPipelineRevisionSetting, core.PipelineRevision); err != nil {
		t.Fatal(err)
	}
	source := makeReadyClippingSource(t, store, core.SourceDurationMS)
	if source.SHA256 != core.SourceSHA256 {
		t.Fatalf("fixture source digest=%s want=%s", core.SourceSHA256, source.SHA256)
	}
	_, jobs := clippingM3JobsForSources(t, store, []string{source.ID}, "m3-python-video-only-callback")
	job := jobs[0]
	stage := clippingM3ClaimAttempt(t, store, job.ID, true)
	artifact := clippingM3CallbackArtifact(job.ID, source.DurationMS)
	artifact.PayloadJSON = string(fixture)
	completed, duplicate, published, err := store.CompleteClippingStageEstimate(
		job.ID, ClippingStageAnalysis, stage.AttemptID, stage.LeaseToken, 250, 10, artifact, core,
	)
	if err != nil || duplicate || !published || completed.Status != ClippingStageCompleted {
		t.Fatalf("persist Python video-only callback: stage=%+v duplicate=%v published=%v error=%v", completed, duplicate, published, err)
	}
	artifacts, err := store.ClippingArtifacts(job.ID)
	if err != nil || len(artifacts) != 1 {
		t.Fatalf("load Python video-only callback artifact: artifacts=%+v error=%v", artifacts, err)
	}
	if candidates := clippingM3Candidates(t, artifacts[0]); len(candidates) == 0 {
		t.Fatal("Python video-only wire artifact produced no visual candidates")
	}
}

func decodeVideoOnlyFixtureSource(t *testing.T, core clippingAnalysisCore) error {
	t.Helper()
	source := ClippingSource{
		ID: "clipsrc_python_wire_fixture", Status: ClippingSourceReady, SHA256: strings.Repeat("a", 64),
		DurationMS: core.SourceDurationMS,
	}
	encoded, err := json.Marshal(core)
	if err != nil {
		return err
	}
	_, err = decodeClippingAnalysisCore(string(encoded), source)
	return err
}
