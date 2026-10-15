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
	data := []byte(content)
	digest := sha256.Sum256(data)
	digestHex := hex.EncodeToString(digest[:])
	source, err := store.CreateClippingSource(ClippingSourceCreate{
		Kind: ClippingSourceYouTube, OriginalName: name, MediaType: "video/mp4",
		DeclaredSizeBytes: int64(len(data)), SourceURL: "https://www.youtube.com/watch?v=abcdefghijk",
		RightsAttested: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetClippingSourceImportExpectation(source.ID, youtubeImportExpectation{
		Version: clippingYouTubeExpectationVersion, SeparateTracks: true,
	}); err != nil {
		t.Fatalf("set YouTube import expectation: %v", err)
	}
	if err := store.CompleteClippingSourceTrackWorkspace(source.ID); err != nil {
		t.Fatalf("complete YouTube source workspace: %v", err)
	}
	if err := store.SetClippingSourceImportFinalExpectation(source.ID, int64(len(data)), digestHex); err != nil {
		t.Fatalf("set YouTube source digest expectation: %v", err)
	}
	if err := os.WriteFile(source.StoragePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateClippingSourceProgress(source.ID, ClippingSourceImporting, 0, int64(len(data))); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkClippingSourceReady(source.ID, int64(len(data)), durationMS, digestHex); err != nil {
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
		BearerToken: strings.Repeat("a", 64), MaxComputeSeconds: 120,
		PipelineRevision: revision,
	})
	if err != nil {
		t.Fatalf("save pipeline revision %q: %v", revision, err)
	}
}

func clippingPublishAnalysisAtRevision(t *testing.T, store *Store, job ClippingJob, source ClippingSource, revision string) string {
	t.Helper()
	stage := clippingM3ClaimAttempt(t, store, job.ID)
	core := clippingM3CoreForSource(source)
	core.PipelineRevision = revision
	core.AlgorithmVersions["pipeline_revision"] = revision
	const callbackDigest = "abababababababababababababababababababababababababababababababab"
	completed, duplicate, published, err := clippingM3Complete(t, store, job, stage, source, core, callbackDigest)
	if err != nil || duplicate || !published || completed.Status != ClippingStageCompleted {
		t.Fatalf("publish analysis at revision %s: stage=%+v duplicate=%v published=%v error=%v", revision, completed, duplicate, published, err)
	}
	return stage.AttemptID
}

func TestClippingPipelineRevisionInvalidatesFutureReuseButKeepsCompletedRevision(t *testing.T) {
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	const oldRevision = "simple-pipeline-rev-a"
	const newRevision = "simple-pipeline-rev-b"
	clippingSetTestPipelineRevision(t, store, security, oldRevision)
	source := clippingRevisionTestSource(t, store, "source-a.mp4", "source A with distinct deterministic bytes", clippingM3FixtureDurationMS)
	otherSource := clippingRevisionTestSource(t, store, "source-b.mp4", "source B has different bytes", clippingM3FixtureDurationMS)
	if source.SHA256 == otherSource.SHA256 {
		t.Fatal("source fixture hashes must differ")
	}
	completedJob := clippingM3CreateJob(t, store, source.ID, "simple-revision-completed")
	futureSameSourceJob := clippingM3CreateJob(t, store, source.ID, "simple-revision-future")
	differentSHAJob := clippingM3CreateJob(t, store, otherSource.ID, "simple-revision-different-source")
	clippingPublishAnalysisAtRevision(t, store, completedJob, source, oldRevision)
	loaded, found, err := store.LoadClippingAnalysisCache(source.ID)
	if err != nil || !found || loaded.PipelineRevision != oldRevision {
		t.Fatalf("load old-revision analysis cache: found=%v revision=%q error=%v", found, loaded.PipelineRevision, err)
	}
	completedArtifacts, err := store.ClippingArtifacts(completedJob.ID)
	if err != nil || len(completedArtifacts) != 1 || clippingRevisionArtifactRevision(t, completedArtifacts[0]) != oldRevision {
		t.Fatalf("completed result lost its pinned pipeline revision: artifacts=%+v error=%v", completedArtifacts, err)
	}

	// An accidentally copied cache row must still fail the source digest check.
	if _, err := store.db.Exec(`INSERT INTO clipping_analysis_cache(
		source_id,source_sha256,pipeline_key,pipeline_revision,source_duration_ms,payload_json,compute_seconds,created_at
	) SELECT ?,source_sha256,pipeline_key,pipeline_revision,source_duration_ms,payload_json,compute_seconds,created_at
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
		t.Fatalf("pipeline revision update: revision=%q error=%v", current, err)
	}
	if completed, err := store.TryCompleteClippingJobFromAnalysisCache(futureSameSourceJob.ID); err != nil || completed {
		t.Fatalf("future job reused old-revision cache: completed=%v error=%v", completed, err)
	}
	if claimed, err := store.ClaimClippingAnalysisSource(futureSameSourceJob.ID); err != nil || !claimed {
		t.Fatalf("future job could not claim analysis for the new revision: claimed=%v error=%v", claimed, err)
	}
	if _, found, err := store.LoadClippingAnalysisCache(source.ID); err != nil || found {
		t.Fatalf("current revision loaded stale cache: found=%v error=%v", found, err)
	}
	completedArtifacts, err = store.ClippingArtifacts(completedJob.ID)
	if err != nil || len(completedArtifacts) != 1 || clippingRevisionArtifactRevision(t, completedArtifacts[0]) != oldRevision {
		t.Fatalf("revision update rewrote completed result provenance: artifacts=%+v error=%v", completedArtifacts, err)
	}
}

func TestClippingStageDispatchCallbackExpiryUsesCapabilityGrace(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	jobID := "clipjob_0123456789abcdef0123456789abcdef"
	batchID := "clipbatch_0123456789abcdef0123456789abcdef"
	sourceID := "clipsrc_0123456789abcdef0123456789abcdef"
	job := ClippingJob{
		ID: jobID, BatchID: batchID, SourceID: sourceID,
		ContentType: "general", MinClipSeconds: 15, MaxClipSeconds: 180, CandidateLimit: 10, Status: ClippingJobRunning,
	}
	stage := ClippingStage{
		JobID: job.ID, Name: ClippingStageAnalysis, Status: ClippingStageRunning,
		IdempotencyKey: job.ID + ":" + ClippingStageAnalysis, AttemptID: "clipatt_0123456789abcdef0123456789abcdef",
		LeaseExpiresAt: now.Add(time.Hour).Unix(),
	}
	source := ClippingSource{
		ID: job.SourceID, Status: ClippingSourceReady, RightsAttested: true,
		DurationMS: 60_000, SHA256: strings.Repeat("a", 64),
	}
	dispatch, err := buildClippingStageDispatch(job, stage, source, "https://worker.example", now)
	if err != nil {
		t.Fatal(err)
	}
	wantExpiry := stage.LeaseExpiresAt + int64(clippingCallbackCapabilityGrace.Seconds())
	if dispatch.CallbackExpiresAt != wantExpiry {
		t.Fatalf("callback expiry=%d, want capability grace boundary %d", dispatch.CallbackExpiresAt, wantExpiry)
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
