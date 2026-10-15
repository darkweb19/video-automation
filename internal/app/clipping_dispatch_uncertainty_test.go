package app

import (
    "errors"
    "net/http"
    "strings"
    "testing"
    "time"
)

// An HTTP acknowledgement can be lost after Modal has admitted the GPU job.
// The Go backend must not make its first media probe return 410 in that case.
func TestClippingAmbiguousDispatchPreservesMediaAndCompletedCallback(t *testing.T) {
    store, security, handler, _ := clippingDashboardFixture(t)
    source := makeReadyClippingSource(t, store, clippingM3FixtureDurationMS)
    job := clippingM3CreateJob(t, store, source.ID, "uncertain-modal-202")
    stage := clippingM3ClaimAttempt(t, store, job.ID)
    app := &dashboardApp{store: store, security: security}
    mediaBearer, _, err := app.persistClippingDispatchCapabilities(stage)
    if err != nil {
        t.Fatalf("persist worker capabilities: %v", err)
    }

    before, err := store.ClippingStages(job.ID)
    if err != nil || len(before) != 1 {
        t.Fatalf("read claimed stage: %+v, %v", before, err)
    }
    note, err := store.NoteClippingStageSubmissionUncertain(
        job.ID, ClippingStageAnalysis, stage.AttemptID, stage.LeaseToken,
    )
    if err != nil || note.Status != ClippingStageRunning ||
        note.AttemptID != stage.AttemptID || note.LeaseExpiresAt != stage.LeaseExpiresAt {
        t.Fatalf("uncertain dispatch invalidated the live lease: %+v, %v", note, err)
    }
    if note.Error == "" {
        t.Fatal("missing visible stage diagnostic")
    }
    currentJob, err := store.ClippingJob(job.ID)
    if err != nil || currentJob.Status != ClippingJobRunning {
        t.Fatalf("uncertain dispatch should not fail job: %+v, %v", currentJob, err)
    }

    endpoint := "/api/clipping/worker-media/" + job.ID + "/" + stage.AttemptID
    media := clippingDashboardRequestWithHeaders(
        handler, http.MethodGet, endpoint, "", nil,
        map[string]string{"Authorization": "Bearer " + mediaBearer, "Range": "bytes=0-0"},
    )
    if media.Code != http.StatusPartialContent || media.Body.Len() != 1 {
        t.Fatalf("accepted worker media probe=%d bytes=%d, want 206/1", media.Code, media.Body.Len())
    }

    duplicate, claimed, err := store.ClaimClippingStage(job.ID, ClippingStageAnalysis, 0, time.Minute)
    if err != nil || claimed || duplicate.AttemptID != stage.AttemptID {
        t.Fatalf("uncertain dispatch allowed a duplicate attempt: %+v claimed=%v err=%v", duplicate, claimed, err)
    }

    core := clippingM3CoreForRevision(t, store, source)
    completed, repeated, published, err := clippingM3Complete(
        t, store, job, stage, source, core, strings.Repeat("d", 64),
    )
    if err != nil || repeated || !published || completed.Status != ClippingStageCompleted {
        t.Fatalf("accepted worker callback did not complete: %+v duplicate=%v published=%v err=%v", completed, repeated, published, err)
    }
    currentJob, err = store.ClippingJob(job.ID)
    if err != nil || currentJob.Status != ClippingJobCompleted {
        t.Fatalf("job did not complete after uncertain submission: %+v, %v", currentJob, err)
    }
    after := clippingDashboardRequestWithHeaders(
        handler, http.MethodGet, endpoint, "", nil,
        map[string]string{"Authorization": "Bearer " + mediaBearer, "Range": "bytes=0-0"},
    )
    if after.Code != http.StatusGone {
        t.Fatalf("completed worker should lose media access: status=%d", after.Code)
    }
}

func TestClippingAmbiguousDispatchCannotReviveCancelledOrSupersededAttempt(t *testing.T) {
    store := newTestStore(t)
    source := makeReadyClippingSource(t, store, clippingM3FixtureDurationMS)
    job := clippingM3CreateJob(t, store, source.ID, "uncertain-cancel")
    stage := clippingM3ClaimAttempt(t, store, job.ID)
    if err := store.CancelClippingJob(job.ID); err != nil {
        t.Fatal(err)
    }
    if _, err := store.NoteClippingStageSubmissionUncertain(
        job.ID, ClippingStageAnalysis, stage.AttemptID, stage.LeaseToken,
    ); !errors.Is(err, ErrClippingStaleAttempt) {
        t.Fatalf("cancelled attempt was revived: %v", err)
    }
    jobAfter, err := store.ClippingJob(job.ID)
    if err != nil || jobAfter.Status != ClippingJobCanceled {
        t.Fatalf("cancelled job status changed: %+v, %v", jobAfter, err)
    }
    if _, err := store.NoteClippingStageSubmissionUncertain(
        job.ID, ClippingStageAnalysis, stage.AttemptID, strings.Repeat("f", 64),
    ); !errors.Is(err, ErrClippingStaleAttempt) {
        t.Fatalf("wrong lease token was accepted: %v", err)
    }
}

func TestClippingAmbiguousDispatchExpiryRemainsRecoverableWithoutRedispatch(t *testing.T) {
    store := newTestStore(t)
    source := makeReadyClippingSource(t, store, clippingM3FixtureDurationMS)
    job := clippingM3CreateJob(t, store, source.ID, "uncertain-expiry")
    stage := clippingM3ClaimAttempt(t, store, job.ID)
    if _, err := store.NoteClippingStageSubmissionUncertain(
        job.ID, ClippingStageAnalysis, stage.AttemptID, stage.LeaseToken,
    ); err != nil {
        t.Fatal(err)
    }
    if err := store.RecoverExpiredClippingStages(stage.LeaseExpiresAt + 1); err != nil {
        t.Fatal(err)
    }
    stages, err := store.ClippingStages(job.ID)
    if err != nil || len(stages) != 1 || stages[0].Status != ClippingStageUncertain {
        t.Fatalf("expired lease did not become uncertain: %+v, %v", stages, err)
    }
    if _, retryErr := store.RetryClippingStage(job.ID, ClippingStageAnalysis); retryErr == nil {
        t.Fatal("unsettled callback window was retried")
    }
    if _, claimed, err := store.ClaimClippingStage(job.ID, ClippingStageAnalysis, 0, time.Minute); err != nil || claimed {
        t.Fatalf("unsettled attempt automatically redispatched: claimed=%v err=%v", claimed, err)
    }
}

func TestClippingAmbiguousDispatchDiagnosticCategoriesAndDeadlines(t *testing.T) {
    if clippingWorkerResponseHeaderTimeout <= 8*time.Second ||
        clippingWorkerResponseHeaderTimeout >= clippingWorkerSubmitTimeout {
        t.Fatalf("response header timeout must allow cold-start ledger admission: header=%s submit=%s",
            clippingWorkerResponseHeaderTimeout, clippingWorkerSubmitTimeout)
    }
    cases := []struct {
        category string
        status int
    }{
        {"submit_timeout", 0},
        {"transport_failure", 0},
        {"unexpected_http_status", 503},
        {"invalid_acknowledgement", 202},
    }
    for _, tc := range cases {
        dispatched := clippingWorkerDispatchDiagnosticFailure(
            errors.New("Bearer super-secret https://private.example"), tc.category, tc.status,
        )
        if clippingWorkerDispatchDefinitelyNotSubmitted(dispatched) ||
            !clippingWorkerDispatchSubmissionAttempted(dispatched) {
            t.Fatalf("diagnostic lost ambiguous transport state for %q", tc.category)
        }
        category, status := clippingWorkerDispatchFailureDetails(dispatched)
        if category != tc.category || status != tc.status {
            t.Fatalf("got category=%q status=%d, want %q/%d", category, status, tc.category, tc.status)
        }
        if strings.Contains(dispatched.Error(), "super-secret") || strings.Contains(dispatched.Error(), "private.example") {
            t.Fatal("diagnostic exposed untrusted URL or credentials")
        }
    }
}
