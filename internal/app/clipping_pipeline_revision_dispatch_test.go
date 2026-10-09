package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func clippingRevisionTestSource(t *testing.T, store *Store, name, content string, durationMS int64) ClippingSource {
	t.Helper()
	source, err := store.CreateClippingSource(ClippingSourceCreate{
		Kind: ClippingSourceUpload, OriginalName: name, MediaType: "video/mp4",
		DeclaredSizeBytes: int64(len(content)), RightsAttested: true,
		RetainUntil: time.Now().Add(48 * time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source.StoragePath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateClippingSourceProgress(source.ID, ClippingSourceUploading, int64(len(content)), int64(len(content))); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(content))
	if err := store.MarkClippingSourceReady(source.ID, int64(len(content)), durationMS, hex.EncodeToString(digest[:])); err != nil {
		t.Fatal(err)
	}
	ready, err := store.ClippingSource(source.ID)
	if err != nil {
		t.Fatal(err)
	}
	return ready
}

func clippingSetTestPipelineRevision(t *testing.T, store *Store, security *Security, revision string) {
	t.Helper()
	_, err := saveClippingWorkerConfig(store, security, clippingWorkerConfigInput{
		Enabled: false, Endpoint: "https://worker.modal.run:443/dispatch",
		BearerToken: strings.Repeat("a", 64), RateMicroUSDPerSecond: 25,
		PipelineRevision: revision,
	})
	if err != nil {
		t.Fatalf("save operator pipeline revision %q: %v", revision, err)
	}
}

func clippingPublishAnalysisAtRevision(t *testing.T, store *Store, job ClippingJob, source ClippingSource, revision string) string {
	t.Helper()
	claimedSource, err := store.ClaimClippingAnalysisSource(job.ID)
	if err != nil || !claimedSource {
		t.Fatalf("claim analysis source at %s: claimed=%v error=%v", revision, claimedSource, err)
	}
	stage, claimed, err := store.ClaimClippingStage(job.ID, ClippingStageAnalysis, 2_000, time.Minute)
	if err != nil || !claimed {
		t.Fatalf("claim analysis stage at %s: stage=%+v claimed=%v error=%v", revision, stage, claimed, err)
	}
	if err := store.SetClippingAnalysisClaimAttempt(job.ID, stage.AttemptID); err != nil {
		t.Fatalf("bind source claim to attempt: %v", err)
	}
	if err := store.SetClippingAttemptPipelineRevision(job.ID, ClippingStageAnalysis, stage.AttemptID, stage.LeaseToken, revision); err != nil {
		t.Fatalf("pin attempt pipeline revision: %v", err)
	}
	if err := store.SetClippingAttemptEstimateTerms(job.ID, ClippingStageAnalysis, stage.AttemptID, stage.LeaseToken, 25, 20); err != nil {
		t.Fatalf("record attempt estimate terms: %v", err)
	}
	core := clippingM3CoreForSource(source)
	core.PipelineRevision = revision
	core.AlgorithmVersions["pipeline_revision"] = revision
	_, duplicate, published, err := store.CompleteClippingStageEstimate(
		job.ID, ClippingStageAnalysis, stage.AttemptID, stage.LeaseToken, 250, 10,
		clippingM3CallbackArtifact(job.ID, source.DurationMS), core,
	)
	if err != nil || duplicate || !published {
		t.Fatalf("publish analysis at %s: duplicate=%v published=%v error=%v", revision, duplicate, published, err)
	}
	return stage.AttemptID
}

func TestClippingPipelineRevisionInvalidatesFutureReuseButKeepsCompletedRevision(t *testing.T) {
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	const oldRevision = "m3-operator-rev-a"
	const newRevision = "m3-operator-rev-b"
	clippingSetTestPipelineRevision(t, store, security, oldRevision)
	source := clippingRevisionTestSource(t, store, "source-a.mp4", "source A with distinct deterministic bytes", clippingM3FixtureDurationMS)
	otherSource := clippingRevisionTestSource(t, store, "source-b.mp4", "source B has different bytes", clippingM3FixtureDurationMS)
	if source.SHA256 == otherSource.SHA256 {
		t.Fatal("source fixture hashes must differ")
	}
	batch, jobs, duplicate, err := store.CreateClippingBatch(ClippingBatchCreate{
		IdempotencyKey: "pipeline-revision-cache-reuse", BudgetLimitMicroUSD: 30_000,
		Jobs: []ClippingJobCreate{
			{SourceID: source.ID, BudgetLimitMicroUSD: 10_000, ContentType: "general", MinClipSeconds: 15, MaxClipSeconds: 30, CandidateLimit: 10},
			{SourceID: source.ID, BudgetLimitMicroUSD: 10_000, ContentType: "comedy", MinClipSeconds: 100, MaxClipSeconds: 180, CandidateLimit: 10},
			{SourceID: otherSource.ID, BudgetLimitMicroUSD: 10_000, ContentType: "general", MinClipSeconds: 15, MaxClipSeconds: 30, CandidateLimit: 10},
		},
	})
	if err != nil || duplicate || len(jobs) != 3 {
		t.Fatalf("create revision test jobs: batch=%+v jobs=%+v duplicate=%v error=%v", batch, jobs, duplicate, err)
	}
	completedJob, futureSameSourceJob, differentSHAJob := jobs[0], jobs[1], jobs[2]
	attemptID := clippingPublishAnalysisAtRevision(t, store, completedJob, source, oldRevision)
	if attemptID == "" {
		t.Fatal("completed analysis has no attempt")
	}
	loaded, found, err := store.LoadClippingAnalysisCache(source.ID)
	if err != nil || !found || loaded.PipelineRevision != oldRevision {
		t.Fatalf("load old-revision analysis cache: found=%v revision=%q error=%v", found, loaded.PipelineRevision, err)
	}
	// A new pipeline may invalidate completed analysis reuse only after the
	// prior invoice is reconciled and its callback capability window closes.
	expiredLease := time.Now().Unix() - int64(clippingCallbackAccountingGrace.Seconds()) - 2
	if _, err := store.db.Exec(`UPDATE clipping_stage_attempts SET lease_expires_at=? WHERE attempt_id=?`, expiredLease, attemptID); err != nil {
		t.Fatal(err)
	}
	if _, duplicate, err := store.ReconcileClippingStageCost(completedJob.ID, attemptID, 250, "pipeline-revision-invoice"); err != nil || duplicate {
		t.Fatalf("reconcile completed old-revision attempt: duplicate=%v error=%v", duplicate, err)
	}

	// Simulate a stale cache entry accidentally associated with another source
	// ID. The source hash guard must reject it even when its pipeline revision
	// and duration match the cached core.
	if _, err := store.db.Exec(`INSERT INTO clipping_analysis_cache(
		source_id,source_sha256,pipeline_key,pipeline_revision,source_duration_ms,payload_json,created_at
	) SELECT ?,source_sha256,pipeline_key,pipeline_revision,source_duration_ms,payload_json,created_at
	FROM clipping_analysis_cache WHERE source_id=? AND pipeline_revision=?`, otherSource.ID, source.ID, oldRevision); err != nil {
		t.Fatalf("seed cross-source stale cache row: %v", err)
	}
	if _, found, err := store.LoadClippingAnalysisCache(otherSource.ID); err != nil || found {
		t.Fatalf("different source SHA reused cached core: found=%v error=%v", found, err)
	}
	if completed, err := store.TryCompleteClippingJobFromAnalysisCache(differentSHAJob.ID); err != nil || completed {
		t.Fatalf("different source job completed from mismatched core: completed=%v error=%v", completed, err)
	}

	clippingSetTestPipelineRevision(t, store, security, newRevision)
	if current, err := store.ClippingAnalysisPipelineRevision(); err != nil || current != newRevision {
		t.Fatalf("operator revision change: revision=%q error=%v", current, err)
	}
	if completed, err := store.TryCompleteClippingJobFromAnalysisCache(futureSameSourceJob.ID); err != nil || completed {
		t.Fatalf("future job reused old-revision cache: completed=%v error=%v", completed, err)
	}
	claimedForNewRevision, err := store.ClaimClippingAnalysisSource(futureSameSourceJob.ID)
	if err != nil || !claimedForNewRevision {
		t.Fatalf("future same-source job could not claim new revision analysis: claimed=%v error=%v", claimedForNewRevision, err)
	}
	if _, found, err := store.LoadClippingAnalysisCache(source.ID); err != nil || found {
		t.Fatalf("current revision loaded stale cache: found=%v error=%v", found, err)
	}

	updatedJob, updatedArtifact, err := store.UpdateClippingJobSelection(completedJob.ID, ClippingJobCreate{
		SourceID: source.ID, ContentType: "comedy", MinClipSeconds: 100, MaxClipSeconds: 180, CandidateLimit: 10,
	})
	if err != nil || updatedJob.MinClipSeconds != 100 || updatedJob.MaxClipSeconds != 180 || updatedJob.ContentType != "comedy" {
		t.Fatalf("completed-job edit did not use its persisted revision: job=%+v error=%v", updatedJob, err)
	}
	artifactRevision := clippingRevisionArtifactRevision(t, updatedArtifact)
	if artifactRevision != oldRevision {
		t.Fatalf("completed job edit rewrote pipeline revision: got=%q want=%q", artifactRevision, oldRevision)
	}
}

func TestClippingStageDispatchCallbackExpiryRespectsSourceRetention(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	job := ClippingJob{
		ID: "clipjob_dispatch_retention", BatchID: "clipbatch_dispatch_retention", SourceID: "clipsrc_dispatch_retention",
		ContentType: "general", MinClipSeconds: 15, MaxClipSeconds: 180, CandidateLimit: 10, Status: ClippingJobRunning,
	}
	stage := ClippingStage{
		JobID: job.ID, Name: ClippingStageAnalysis, Status: ClippingStageRunning,
		IdempotencyKey: job.ID + ":" + ClippingStageAnalysis, AttemptID: "clipatt_dispatch_retention",
		LeaseExpiresAt: now.Add(time.Hour).Unix(),
	}
	source := ClippingSource{
		ID: job.SourceID, Status: ClippingSourceReady, RightsAttested: true,
		DurationMS: 60_000, SHA256: strings.Repeat("a", 64), RetainUntil: now.Add(12 * time.Hour).Unix(),
	}
	dispatch, err := buildClippingStageDispatch(job, stage, source, "https://worker.example", now)
	if err != nil {
		t.Fatal(err)
	}
	if dispatch.CallbackExpiresAt != source.RetainUntil {
		t.Fatalf("callback expiry=%d, want source retention cap %d", dispatch.CallbackExpiresAt, source.RetainUntil)
	}

	source.RetainUntil = now.Unix()
	if _, err := buildClippingStageDispatch(job, stage, source, "https://worker.example", now); err == nil {
		t.Fatal("dispatch accepted a source with no valid callback window before retention expires")
	}
}

func clippingRevisionArtifactRevision(t *testing.T, artifact ClippingArtifact) string {
	t.Helper()
	var payload struct {
		PipelineRevision string `json:"pipeline_revision"`
	}
	if err := json.Unmarshal([]byte(artifact.PayloadJSON), &payload); err != nil {
		t.Fatal(err)
	}
	return payload.PipelineRevision
}
