package app

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
)

func clippingSimpleCallback(t *testing.T, source ClippingSource, job ClippingJob, stage ClippingStage, revision string, computeSeconds int64) clippingWorkerCallback {
	t.Helper()
	core := clippingM3CoreForSource(source)
	core.PipelineRevision = revision
	coreJSON, err := json.Marshal(core)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(coreJSON, &payload); err != nil {
		t.Fatal(err)
	}
	fields := map[string]any{
		"cost_basis":                "operator_declared_worker_second_rate_estimate",
		"rate_micro_usd_per_second": int64(1),
		"compute_seconds":           computeSeconds,
		"cost_estimate_micro_usd":   computeSeconds,
	}
	for key, value := range fields {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		payload[key] = encoded
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return clippingWorkerCallback{
		ProtocolVersion: clippingProtocolVersion, JobID: job.ID, Stage: ClippingStageAnalysis,
		DispatchID: stage.IdempotencyKey, AttemptID: stage.AttemptID, Status: "completed",
		ActualCostMicroUSD: 0, CostEstimateMicroUSD: computeSeconds,
		CostBasis: "operator_declared_worker_second_rate_estimate",
		Artifact: clippingCallbackArtifact{
			Type: clippingAnalysisArtifactType, SchemaVersion: clippingAnalysisSchemaVersion, Version: 1,
			SourceDurationMS: source.DurationMS, TimeRanges: []ClippingTimeRange{}, Payload: encoded,
		},
	}
}

func TestClippingSimpleCallbackStoresRuntimeAndServesReadOnlyResults(t *testing.T) {
	store, security, handler, cookie := clippingDashboardFixture(t)
	source := makeReadyClippingSource(t, store, clippingM3FixtureDurationMS)
	jobView, _ := clippingCreateJob(t, handler, cookie, source.ID, "simple-callback-job")
	job, err := store.ClippingJob(jobView.ID)
	if err != nil {
		t.Fatal(err)
	}
	config := clippingCreateWorkerConfigForTest(t, store, security)
	prepared, stage := clippingStartSimpleJobAttempt(t, store, security, job.ID)
	app := &dashboardApp{store: store, security: security}
	var dispatchBody []byte
	client := &http.Client{Transport: clippingRuntimeRoundTripper(func(request *http.Request) (*http.Response, error) {
		if request.Body != nil {
			dispatchBody, _ = io.ReadAll(request.Body)
		}
		return clippingRuntimeResponse(request, http.StatusAccepted,
			`{"accepted":true,"job_id":"`+job.ID+`","attempt_id":"`+stage.AttemptID+`","dispatch_id":"`+stage.IdempotencyKey+`"}`), nil
	})}
	dispatcher := newClippingWorkerDispatcher(&clippingRuntimeResolver{answers: []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}}, nil, client)
	if err := app.dispatchPreparedClippingStageWithDispatcher(context.Background(), prepared, dispatcher); err != nil {
		t.Fatalf("mock Modal dispatch: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(dispatchBody, &wire); err != nil {
		t.Fatal(err)
	}
	if wire["rate_micro_usd_per_second"] != float64(1) || wire["max_compute_seconds"] != float64(config.MaxComputeSeconds) {
		t.Fatalf("dispatch did not use the bounded v2 compatibility fields: %s", dispatchBody)
	}

	callback := clippingSimpleCallback(t, source, job, stage, config.PipelineRevision, 42)
	if response := clippingCallbackRequest(handler, job.ID, stage.AttemptID, "", callback); response.Code != http.StatusUnauthorized {
		t.Fatalf("callback without capability=%d %s", response.Code, response.Body.String())
	}
	wrongAttempt := callback
	wrongAttempt.AttemptID = "clipatt_0123456789abcdef0123456789abcdef"
	if response := clippingCallbackRequest(handler, job.ID, stage.AttemptID, prepared.CallbackBearer, wrongAttempt); response.Code != http.StatusBadRequest {
		t.Fatalf("callback with mismatched attempt identity=%d %s", response.Code, response.Body.String())
	}
	wrongDispatch := callback
	wrongDispatch.DispatchID = job.ID + ":another-stage"
	if response := clippingCallbackRequest(handler, job.ID, stage.AttemptID, prepared.CallbackBearer, wrongDispatch); response.Code != http.StatusConflict {
		t.Fatalf("callback with stale dispatch identity=%d %s", response.Code, response.Body.String())
	}
	completed := clippingCallbackRequest(handler, job.ID, stage.AttemptID, prepared.CallbackBearer, callback)
	if completed.Code != http.StatusNoContent {
		t.Fatalf("valid analysis callback=%d %s", completed.Code, completed.Body.String())
	}
	duplicate := clippingCallbackRequest(handler, job.ID, stage.AttemptID, prepared.CallbackBearer, callback)
	if duplicate.Code != http.StatusNoContent {
		t.Fatalf("matching callback replay=%d %s", duplicate.Code, duplicate.Body.String())
	}
	completedJob, err := store.ClippingJob(job.ID)
	if err != nil || completedJob.Status != ClippingJobCompleted {
		t.Fatalf("job completion state=%q error=%v", completedJob.Status, err)
	}

	if response := clippingDashboardRequest(handler, http.MethodGet, "/api/clipping/jobs/"+job.ID+"/results", "", nil, ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated results=%d", response.Code)
	}
	results := clippingDashboardRequest(handler, http.MethodGet, "/api/clipping/jobs/"+job.ID+"/results", "", cookie, "")
	if results.Code != http.StatusOK {
		t.Fatalf("read-only results=%d %s", results.Code, results.Body.String())
	}
	var result struct {
		JobID     string             `json:"job_id"`
		SourceID  string             `json:"source_id"`
		Artifacts []ClippingArtifact `json:"artifacts"`
	}
	if err := json.Unmarshal(results.Body.Bytes(), &result); err != nil || result.JobID != job.ID || result.SourceID != source.ID || len(result.Artifacts) != 1 {
		t.Fatalf("results response=%+v error=%v body=%s", result, err, results.Body.String())
	}
	var artifactPayload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(result.Artifacts[0].PayloadJSON), &artifactPayload); err != nil {
		t.Fatal(err)
	}
	if _, ok := artifactPayload["transcript"]; !ok {
		t.Fatalf("read-only result is missing transcript data: %s", result.Artifacts[0].PayloadJSON)
	}
	if string(artifactPayload["compute_seconds"]) != "42" {
		t.Fatalf("runtime estimate was not retained as runtime: %s", result.Artifacts[0].PayloadJSON)
	}
	for _, forbidden := range []string{
		"cost_basis", "cost_estimate_micro_usd", "rate_micro_usd_per_second", "actual_cost_micro_usd",
		"actual_cost_status", "budget_micro_usd", "reserved_micro_usd", "invoice", "reconcile",
	} {
		if strings.Contains(results.Body.String(), forbidden) {
			t.Errorf("public result contains removed billing data %q: %s", forbidden, results.Body.String())
		}
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		if response := clippingDashboardRequest(handler, method, "/api/clipping/jobs/"+job.ID+"/results", `{}`, cookie, ""); response.Code < 400 {
			t.Errorf("read-only results accepted %s: %d %s", method, response.Code, response.Body.String())
		}
	}
}
