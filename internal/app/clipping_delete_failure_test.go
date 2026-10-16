package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestClippingDashboardDeleteAnalysisCancelsAndSettlesLateCallback(t *testing.T) {
	store, security, handler, cookie := clippingDashboardFixture(t)
	source := makeReadyClippingSource(t, store, clippingM3FixtureDurationMS)
	jobView, _ := clippingCreateJob(t, handler, cookie, source.ID, "delete-analysis-active-001")
	job, err := store.ClippingJob(jobView.ID)
	if err != nil {
		t.Fatal(err)
	}
	prepared, stage := clippingStartSimpleJobAttempt(t, store, security, job.ID)

	if response := clippingDashboardRequest(handler, http.MethodDelete, "/api/clipping/jobs/"+job.ID, "", nil, ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated analysis delete=%d %s", response.Code, response.Body.String())
	}
	deleted := clippingDashboardRequest(handler, http.MethodDelete, "/api/clipping/jobs/"+job.ID, "", cookie, "")
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete active analysis=%d %s", deleted.Code, deleted.Body.String())
	}
	job, err = store.ClippingJob(job.ID)
	if err != nil || !job.AnalysisDeleted || job.Status != ClippingJobCanceled {
		t.Fatalf("deleted active analysis job=%+v error=%v", job, err)
	}
	if _, err := store.ClippingArtifacts(job.ID); err != nil {
		t.Fatalf("deleted job artifacts could not be read as empty: %v", err)
	}
	if _, err := store.Setting(clippingCapabilitySettingKey(job.ID, stage.AttemptID, clippingCapabilityMedia)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("media capability remains after analysis deletion: %v", err)
	}
	if token, err := (&dashboardApp{store: store, security: security}).clippingCapability(job.ID, stage.AttemptID, clippingCapabilityCallback); err != nil || token != prepared.CallbackBearer {
		t.Fatalf("callback capability was not retained to settle the canceled attempt: token_match=%v error=%v", token == prepared.CallbackBearer, err)
	}
	for _, path := range []string{
		"/api/clipping/jobs/" + job.ID,
		"/api/clipping/jobs/" + job.ID + "/results",
	} {
		if response := clippingDashboardRequest(handler, http.MethodGet, path, "", cookie, ""); response.Code != http.StatusNotFound {
			t.Errorf("deleted analysis remained visible at %s: %d %s", path, response.Code, response.Body.String())
		}
	}
	listed := clippingDashboardRequest(handler, http.MethodGet, "/api/clipping/jobs", "", cookie, "")
	if listed.Code != http.StatusOK || strings.Contains(listed.Body.String(), job.ID) {
		t.Fatalf("deleted analysis remains in job list: %d %s", listed.Code, listed.Body.String())
	}

	failure := clippingWorkerCallback{
		ProtocolVersion: clippingProtocolVersion, JobID: job.ID, Stage: ClippingStageAnalysis,
		DispatchID: stage.IdempotencyKey, AttemptID: stage.AttemptID, Status: "failed",
		ActualCostMicroUSD: 0,
	}
	callback := clippingCallbackRequest(handler, job.ID, stage.AttemptID, prepared.CallbackBearer, failure)
	if callback.Code != http.StatusNoContent {
		t.Fatalf("late callback for deleted analysis=%d %s", callback.Code, callback.Body.String())
	}
	tx, err := store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := readClippingAttempt(tx, job.ID, ClippingStageAnalysis, stage.AttemptID)
	_ = tx.Rollback()
	if err != nil || attempt.Status != "settled_canceled" || attempt.CallbackReceivedAt == 0 {
		t.Fatalf("late callback did not settle without publishing: attempt=%+v error=%v", attempt, err)
	}
	artifacts, err := store.ClippingArtifacts(job.ID)
	if err != nil || len(artifacts) != 0 {
		t.Fatalf("late callback restored deleted analysis: artifacts=%d error=%v", len(artifacts), err)
	}
	newJob := clippingM3CreateJob(t, store, source.ID, "delete-analysis-new-job-001")
	claimed, err := store.ClaimClippingAnalysisSource(newJob.ID)
	if err != nil || !claimed {
		t.Fatalf("settled deleted attempt still blocks new analysis: claimed=%v error=%v", claimed, err)
	}
	_ = store.ReleaseClippingAnalysisSourceClaim(newJob.ID)
}

func TestClippingDashboardDeleteSourceRemovesMediaAndHidesAnalyses(t *testing.T) {
	store, _, handler, cookie := clippingDashboardFixture(t)
	source := makeReadyClippingSource(t, store, clippingM3FixtureDurationMS)
	jobView, _ := clippingCreateJob(t, handler, cookie, source.ID, "delete-source-job-001")
	if response := clippingDashboardRequest(handler, http.MethodDelete, "/api/clipping/sources/"+source.ID, "", nil, ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated source delete=%d %s", response.Code, response.Body.String())
	}
	deleted := clippingDashboardRequest(handler, http.MethodDelete, "/api/clipping/sources/"+source.ID, "", cookie, "")
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete source=%d %s", deleted.Code, deleted.Body.String())
	}
	loadedSource, err := store.ClippingSource(source.ID)
	if err != nil || loadedSource.Status != ClippingSourceDeleted || !loadedSource.CleanupComplete {
		t.Fatalf("deleted source state=%+v error=%v", loadedSource, err)
	}
	if _, err := os.Stat(source.StoragePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted source media remains on disk: %v", err)
	}
	job, err := store.ClippingJob(jobView.ID)
	if err != nil || !job.AnalysisDeleted || job.Status != ClippingJobCanceled {
		t.Fatalf("source deletion did not cancel and hide analysis: job=%+v error=%v", job, err)
	}
	for _, path := range []string{
		"/api/clipping/sources/" + source.ID,
		"/api/clipping/jobs/" + job.ID,
		"/api/clipping/jobs/" + job.ID + "/results",
	} {
		if response := clippingDashboardRequest(handler, http.MethodGet, path, "", cookie, ""); response.Code != http.StatusNotFound {
			t.Errorf("deleted source data remained visible at %s: %d %s", path, response.Code, response.Body.String())
		}
	}
	listed := clippingDashboardRequest(handler, http.MethodGet, "/api/clipping/jobs", "", cookie, "")
	if listed.Code != http.StatusOK || strings.Contains(listed.Body.String(), job.ID) {
		t.Fatalf("source analysis remains in job list: %d %s", listed.Code, listed.Body.String())
	}
}

func TestDeleteSourceDefersFileCleanupUntilCanceledImportStops(t *testing.T) {
	store, security, _, cookie := clippingDashboardFixture(t)
	source := makeReadyClippingSource(t, store, clippingM3FixtureDurationMS)
	acquisition := NewClippingAcquisition(store, security)
	acquisition.deleteOperationWait = 10 * time.Millisecond
	ctx, finish, err := acquisition.beginOperation(context.Background(), source.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := newDashboardHandler(&dashboardApp{store: store, security: security, clippingAcquisition: acquisition})
	writeAfterDelete := make(chan struct{})
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		<-writeAfterDelete
		_ = os.WriteFile(source.StoragePath, []byte("late import write"), 0o600)
		finish()
	}()

	response := clippingDashboardRequest(handler, http.MethodDelete, "/api/clipping/sources/"+source.ID, "", cookie, "")
	if response.Code != http.StatusAccepted || !strings.Contains(response.Body.String(), `"cleanup_pending":true`) {
		t.Fatalf("active import deletion response=%d %s", response.Code, response.Body.String())
	}
	deleted, err := store.ClippingSource(source.ID)
	if err != nil || deleted.Status != ClippingSourceDeleted || deleted.CleanupComplete {
		t.Fatalf("source cleanup was marked complete while importer was active: source=%+v error=%v", deleted, err)
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("active import was not canceled: %v", ctx.Err())
	}
	if _, err := os.Stat(source.StoragePath); err != nil {
		t.Fatalf("media was removed before the active importer stopped: %v", err)
	}
	close(writeAfterDelete)
	select {
	case <-writerDone:
	case <-time.After(time.Second):
		t.Fatal("simulated importer did not stop")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		deleted, err = store.ClippingSource(source.ID)
		_, statErr := os.Stat(source.StoragePath)
		if err == nil && deleted.CleanupComplete && errors.Is(statErr, os.ErrNotExist) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("deferred cleanup did not remove a late importer write: source=%+v error=%v", deleted, err)
}

func TestAmbiguousFailedAnalysisClaimReleasesAfterCallbackExpiry(t *testing.T) {
	store, security, handler, cookie := clippingDashboardFixture(t)
	source := makeReadyClippingSource(t, store, clippingM3FixtureDurationMS)
	jobView, _ := clippingCreateJob(t, handler, cookie, source.ID, "ambiguous-failure-old-job-001")
	job, err := store.ClippingJob(jobView.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, stage := clippingStartSimpleJobAttempt(t, store, security, job.ID)
	lease, err := (&dashboardApp{store: store, security: security}).clippingCapability(job.ID, stage.AttemptID, clippingCapabilityLease)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.FailClippingStageAfterSubmit(job.ID, ClippingStageAnalysis, stage.AttemptID, lease, "Analysis worker submission failed"); err != nil {
		t.Fatalf("record ambiguous submission failure: %v", err)
	}
	if err := store.DeleteClippingAnalysis(job.ID); err != nil {
		t.Fatalf("delete failed analysis: %v", err)
	}
	now := time.Now().Unix()
	expiry := now + 60
	if _, err := store.db.Exec(`UPDATE clipping_capability_expiry_index SET expires_at=? WHERE job_id=? AND attempt_id=?`, expiry, job.ID, stage.AttemptID); err != nil {
		t.Fatal(err)
	}
	newJob := clippingM3CreateJob(t, store, source.ID, "ambiguous-failure-new-job-001")
	if _, err := store.RetryClippingStage(job.ID, ClippingStageAnalysis); !errors.Is(err, ErrClippingJobTerminal) {
		t.Fatalf("deleted failed analysis retry result=%v", err)
	}
	if claimed, err := store.ClaimClippingAnalysisSource(newJob.ID); err != nil || claimed {
		t.Fatalf("new analysis bypassed open callback window: claimed=%v error=%v", claimed, err)
	}
	if err := store.RecoverExpiredClippingStages(expiry + 1); err != nil {
		t.Fatalf("settle expired callback guard: %v", err)
	}
	if claimed, err := store.ClaimClippingAnalysisSource(newJob.ID); err != nil || !claimed {
		t.Fatalf("new analysis stayed blocked after callback expiry: claimed=%v error=%v", claimed, err)
	}
	_ = store.ReleaseClippingAnalysisSourceClaim(newJob.ID)
	if _, err := store.ClippingJob(job.ID); err != nil {
		t.Fatalf("deleted job audit was not retained: %v", err)
	}
	if _, err := (&dashboardApp{store: store, security: security}).clippingCapability(job.ID, stage.AttemptID, clippingCapabilityCallback); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expired callback capability was retained: %v", err)
	}
}

func TestClippingCurrentAttemptResultErrorsFailAnalysis(t *testing.T) {
	store, security, handler, _ := clippingDashboardFixture(t)
	source := makeReadyClippingSource(t, store, clippingM3FixtureDurationMS)
	config := clippingCreateWorkerConfigForTest(t, store, security)
	for _, testCase := range []struct {
		name   string
		mutate func(*testing.T, *clippingWorkerCallback)
	}{
		{
			name: "source identity mismatch",
			mutate: func(t *testing.T, callback *clippingWorkerCallback) {
				var payload map[string]json.RawMessage
				if err := json.Unmarshal(callback.Artifact.Payload, &payload); err != nil {
					t.Fatal(err)
				}
				payload["source_sha256"] = json.RawMessage(`"` + strings.Repeat("b", 64) + `"`)
				encoded, err := json.Marshal(payload)
				if err != nil {
					t.Fatal(err)
				}
				callback.Artifact.Payload = encoded
			},
		},
		{
			name: "compute ceiling exceeded",
			mutate: func(t *testing.T, callback *clippingWorkerCallback) {
				_ = callback
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			job := clippingM3CreateJob(t, store, source.ID, "callback-error-"+strings.ReplaceAll(testCase.name, " ", "-")+"-001")
			prepared, stage := clippingStartSimpleJobAttempt(t, store, security, job.ID)
			computeSeconds := int64(42)
			if testCase.name == "compute ceiling exceeded" {
				computeSeconds = config.MaxComputeSeconds + 1
			}
			callback := clippingSimpleCallback(t, source, job, stage, config.PipelineRevision, computeSeconds)
			testCase.mutate(t, &callback)
			clippingAcceptDeleteFailureTestDispatch(t, store, security, prepared)
			response := clippingCallbackRequest(handler, job.ID, stage.AttemptID, prepared.CallbackBearer, callback)
			if response.Code != http.StatusNoContent {
				t.Fatalf("invalid current result callback=%d %s", response.Code, response.Body.String())
			}
			failed, err := store.ClippingJob(job.ID)
			if err != nil || failed.Status != ClippingJobFailed {
				t.Fatalf("invalid current result did not fail the analysis: job=%+v error=%v", failed, err)
			}
		})
	}
}

// Real Modal GPU images send Rust scene provenance. The Go callback must
// commit those results, not acknowledge their rejection as if they succeeded.
func TestClippingRustSceneWorkerCallbackReconcilesDashboard(t *testing.T) {
	for _, tc := range []struct {
		name, visual, inspection string
		completed bool
	}{
		{"rust-pair", clippingWorkerRustVisualEventsVersion, clippingWorkerRustCandidateInspectionVersion, true},
		{"mixed-engine", clippingWorkerRustVisualEventsVersion, clippingWorkerFFmpegCandidateInspectionVersion, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, security, handler, cookie := clippingDashboardFixture(t)
			source := makeReadyClippingSource(t, store, clippingM3FixtureDurationMS)
			config := clippingCreateWorkerConfigForTest(t, store, security)
			job := clippingM3CreateJob(t, store, source.ID, "rust-worker-callback-"+tc.name)
			prepared, stage := clippingStartSimpleJobAttempt(t, store, security, job.ID)
			callback := clippingSimpleCallback(t, source, job, stage, config.PipelineRevision, 42)
			var payload map[string]any
			if err := json.Unmarshal(callback.Artifact.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			algorithms, ok := payload["algorithm_versions"].(map[string]any)
			if !ok {
				t.Fatal("callback has no algorithm provenance")
			}
			algorithms["visual_events"] = tc.visual
			algorithms["candidate_inspection"] = tc.inspection
			encoded, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			callback.Artifact.Payload = encoded
			clippingAcceptDeleteFailureTestDispatch(t, store, security, prepared)
			for i := 0; i < 2; i++ {
				response := clippingCallbackRequest(handler, job.ID, stage.AttemptID, prepared.CallbackBearer, callback)
				if response.Code != http.StatusNoContent {
					t.Fatalf("callback replay %d status=%d body=%s", i, response.Code, response.Body.String())
				}
			}
			got, err := store.ClippingJob(job.ID)
			if err != nil {
				t.Fatal(err)
			}
			artifacts, err := store.ClippingArtifacts(job.ID)
			if err != nil {
				t.Fatal(err)
			}
			if tc.completed {
				if got.Status != ClippingJobCompleted || len(artifacts) != 1 {
					t.Fatalf("Rust callback should publish: job=%+v artifacts=%d", got, len(artifacts))
				}
				results := clippingDashboardRequest(handler, http.MethodGet, "/api/clipping/jobs/"+job.ID+"/results", "", cookie, "")
				if results.Code != http.StatusOK || !strings.Contains(results.Body.String(), "\"artifacts\"") {
					t.Fatalf("dashboard result status=%d body=%s", results.Code, results.Body.String())
				}
			} else if got.Status != ClippingJobFailed || len(artifacts) != 0 || !strings.Contains(got.Error, "invalid_pipeline_provenance") {
				t.Fatalf("invalid Rust callback should fail with safe reason: job=%+v artifacts=%d", got, len(artifacts))
			}
		})
	}
}

func TestClippingOversizeCurrentCallbackFailsAnalysis(t *testing.T) {
	for _, contentLengthKnown := range []bool{true, false} {
		name := "max-bytes-reader"
		if contentLengthKnown {
			name = "content-length"
		}
		t.Run(name, func(t *testing.T) {
			store, security, handler, _ := clippingDashboardFixture(t)
			source := makeReadyClippingSource(t, store, clippingM3FixtureDurationMS)
			clippingCreateWorkerConfigForTest(t, store, security)
			job := clippingM3CreateJob(t, store, source.ID, "oversize-callback-"+name+"-001")
			prepared, stage := clippingStartSimpleJobAttempt(t, store, security, job.ID)
			clippingAcceptDeleteFailureTestDispatch(t, store, security, prepared)
			body := strings.NewReader(strings.Repeat("x", maxClippingCallbackBodyBytes+1))
			request := httptest.NewRequest(http.MethodPost, "/api/clipping/worker-callbacks/"+job.ID+"/"+stage.AttemptID, body)
			if !contentLengthKnown {
				request.ContentLength = -1
			}
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Bearer "+prepared.CallbackBearer)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusNoContent {
				t.Fatalf("oversize callback=%d %s", response.Code, response.Body.String())
			}
			failed, err := store.ClippingJob(job.ID)
			if err != nil || failed.Status != ClippingJobFailed {
				t.Fatalf("oversize callback did not fail analysis: job=%+v error=%v", failed, err)
			}
		})
	}
}

func clippingAcceptDeleteFailureTestDispatch(t *testing.T, store *Store, security *Security, prepared clippingPreparedDispatch) {
	t.Helper()
	client := &http.Client{Transport: clippingRuntimeRoundTripper(func(request *http.Request) (*http.Response, error) {
		return clippingRuntimeResponse(request, http.StatusAccepted,
			`{"accepted":true,"job_id":"`+prepared.Payload.JobID+`","attempt_id":"`+prepared.Payload.AttemptID+`","dispatch_id":"`+prepared.Payload.DispatchID+`"}`), nil
	})}
	dispatcher := newClippingWorkerDispatcher(
		&clippingRuntimeResolver{answers: []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}}, nil, client)
	app := &dashboardApp{store: store, security: security}
	if err := app.dispatchPreparedClippingStageWithDispatcher(context.Background(), prepared, dispatcher); err != nil {
		t.Fatalf("submit clipping test attempt: %v", err)
	}
}

func TestProcessorConfigurationErrorFailsQueuedAnalysis(t *testing.T) {
	store, security, handler, cookie := clippingDashboardFixture(t)
	source := makeReadyClippingSource(t, store, clippingM3FixtureDurationMS)
	jobView, _ := clippingCreateJob(t, handler, cookie, source.ID, "processor-config-error-001")
	if err := store.SetSetting(clippingWorkerConfigSetting, "invalid encrypted configuration"); err != nil {
		t.Fatal(err)
	}
	processor := &Processor{app: &dashboardApp{store: store, security: security}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	worked, err := processor.processNextClippingAnalysis(context.Background())
	if err != nil || worked {
		t.Fatalf("processor handling queued config error: worked=%v error=%v", worked, err)
	}
	job, err := store.ClippingJob(jobView.ID)
	if err != nil || job.Status != ClippingJobFailed || !strings.Contains(job.Error, "configuration") {
		t.Fatalf("queued analysis did not fail visibly on configuration error: job=%+v error=%v", job, err)
	}
	if err := store.SetSetting(clippingWorkerConfigSetting, ""); err != nil {
		t.Fatal(err)
	}
}
