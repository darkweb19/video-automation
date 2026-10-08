package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func clippingDashboardFixture(t *testing.T, allowLowDisk ...bool) (*Store, *Security, http.Handler, *http.Cookie) {
	t.Helper()
	store := newTestStore(t)
	provisionTestUser(t, store)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	acquisition := NewClippingAcquisition(store, security)
	if len(allowLowDisk) > 0 && allowLowDisk[0] {
		acquisition.diskAvailable = func(string) (int64, error) { return 8 << 30, nil }
	}
	handler := newDashboardHandler(&dashboardApp{store: store, security: security, clippingAcquisition: acquisition})
	login := httptest.NewRecorder()
	if err := security.NewSession(login, httptest.NewRequest(http.MethodGet, "/", nil), "sujanshrestha"); err != nil {
		t.Fatal(err)
	}
	cookies := login.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("session cookies=%d", len(cookies))
	}
	return store, security, handler, cookies[0]
}

func clippingDashboardRequest(handler http.Handler, method, path, body string, cookie *http.Cookie, bearer string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func clippingCallbackRequest(handler http.Handler, jobID, attemptID, token string, callback clippingWorkerCallback) *httptest.ResponseRecorder {
	body, err := json.Marshal(callback)
	if err != nil {
		panic(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/clipping/worker-callbacks/"+jobID+"/"+attemptID, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func clippingCallbackFixture(job ClippingJob, stage ClippingStage, source ClippingSource, payload json.RawMessage, actual int64) clippingWorkerCallback {
	return clippingWorkerCallback{
		ProtocolVersion: clippingProtocolVersion, JobID: job.ID, Stage: ClippingStageAnalysis,
		DispatchID: stage.IdempotencyKey, AttemptID: stage.AttemptID, Status: "completed",
		ActualCostMicroUSD: actual,
		Artifact: clippingCallbackArtifact{
			Type: "clipping.analysis", SchemaVersion: "1", Version: 1,
			SourceDurationMS: source.DurationMS,
			TimeRanges:       []ClippingTimeRange{{StartMS: 0, EndMS: source.DurationMS}}, Payload: payload,
		},
	}
}

func clippingClaimWithCapabilities(t *testing.T, store *Store, security *Security, job ClippingJob, reserve int64) (ClippingStage, string, string) {
	t.Helper()
	stage, claimed, err := store.ClaimClippingStage(job.ID, ClippingStageAnalysis, reserve, 12*time.Hour)
	if err != nil || !claimed {
		t.Fatalf("claim stage=%+v claimed=%v error=%v", stage, claimed, err)
	}
	app := &dashboardApp{store: store, security: security}
	media, callback, err := app.persistClippingDispatchCapabilities(stage)
	if err != nil {
		t.Fatal(err)
	}
	return stage, media, callback
}

func collectClippingEvents(t *testing.T, channel <-chan dashboardEvent, count int) map[string][]byte {
	t.Helper()
	events := make(map[string][]byte, count)
	received := 0
	for received < count {
		select {
		case event, open := <-channel:
			if !open {
				t.Fatal("clipping event stream closed unexpectedly")
			}
			events[event.name] = event.data
			received++
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for clipping progress events; received %v", events)
		}
	}
	return events
}

func TestClippingDashboardAuthRightsRetentionAndWorkerAvailability(t *testing.T) {
	store, _, handler, cookie := clippingDashboardFixture(t, true)
	unauthorized := clippingDashboardRequest(handler, http.MethodGet, "/api/clipping/config", "", nil, "")
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated config=%d", unauthorized.Code)
	}
	config := clippingDashboardRequest(handler, http.MethodGet, "/api/clipping/config", "", cookie, "")
	if config.Code != http.StatusOK || !strings.Contains(config.Body.String(), `"worker_available":false`) || !strings.Contains(config.Body.String(), `"worker_state":"unavailable"`) {
		t.Fatalf("worker availability response=%d %s", config.Code, config.Body.String())
	}
	crossOrigin := httptest.NewRequest(http.MethodPut, "/api/clipping/config/retention", strings.NewReader(`{"retention_days":45}`))
	crossOrigin.Header.Set("Content-Type", "application/json")
	crossOrigin.Header.Set("Origin", "https://attacker.example")
	crossOrigin.AddCookie(cookie)
	crossOriginRecorder := httptest.NewRecorder()
	handler.ServeHTTP(crossOriginRecorder, crossOrigin)
	if crossOriginRecorder.Code != http.StatusForbidden {
		t.Fatalf("cross-origin retention update=%d %s", crossOriginRecorder.Code, crossOriginRecorder.Body.String())
	}
	denied := clippingDashboardRequest(handler, http.MethodPost, "/api/clipping/sources/upload", `{"original_name":"talk.mp4","media_type":"video/mp4","size_bytes":10,"rights_attested":false}`, cookie, "")
	if denied.Code != http.StatusUnprocessableEntity {
		t.Fatalf("upload without rights attestation=%d %s", denied.Code, denied.Body.String())
	}
	stream, unsubscribe := store.events.subscribe()
	defer unsubscribe()
	started := clippingDashboardRequest(handler, http.MethodPost, "/api/clipping/sources/upload", `{"original_name":"talk.mp4","media_type":"video/mp4","size_bytes":10,"rights_attested":true}`, cookie, "")
	if started.Code != http.StatusCreated || !strings.Contains(started.Body.String(), `"status":"uploading"`) || !strings.Contains(started.Body.String(), `"rights_attested":true`) {
		t.Fatalf("upload init=%d %s", started.Code, started.Body.String())
	}
	sourceEvents := collectClippingEvents(t, stream, 1)
	var sourceEvent struct {
		Source struct {
			Status string `json:"status"`
		} `json:"source"`
	}
	if err := json.Unmarshal(sourceEvents["clipping_source"], &sourceEvent); err != nil || sourceEvent.Source.Status != string(ClippingSourceUploading) {
		t.Fatalf("source EventSource payload=%s error=%v", sourceEvents["clipping_source"], err)
	}
	retention := clippingDashboardRequest(handler, http.MethodPut, "/api/clipping/config/retention", `{"retention_days":45}`, cookie, "")
	if retention.Code != http.StatusOK {
		t.Fatalf("retention update=%d %s", retention.Code, retention.Body.String())
	}
	stored, err := store.Setting(clippingRetentionSetting)
	if err != nil || stored != "45" {
		t.Fatalf("retention setting=%q error=%v", stored, err)
	}
}

func TestClippingDashboardEnforcesAggregateBudgetAndExposesProgress(t *testing.T) {
	store, _, handler, cookie := clippingDashboardFixture(t)
	stream, unsubscribe := store.events.subscribe()
	defer unsubscribe()
	first := makeReadyClippingSource(t, store, 60_000)
	second := makeReadyClippingSource(t, store, 90_000)
	request := httptest.NewRequest(http.MethodPost, "/api/clipping/batches", strings.NewReader(fmt.Sprintf(`{"source_ids":[%q,%q],"per_job_budget_micro_usd":600000,"budget_micro_usd":1000000}`, first.ID, second.ID)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "underfunded-batch-001")
	request.AddCookie(cookie)
	underfunded := httptest.NewRecorder()
	handler.ServeHTTP(underfunded, request)
	if underfunded.Code != http.StatusBadRequest {
		t.Fatalf("underfunded batch=%d %s", underfunded.Code, underfunded.Body.String())
	}
	request = httptest.NewRequest(http.MethodPost, "/api/clipping/batches", strings.NewReader(fmt.Sprintf(`{"source_ids":[%q,%q],"per_job_budget_micro_usd":600000,"budget_micro_usd":1500000}`, first.ID, second.ID)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "funded-batch-request-001")
	request.AddCookie(cookie)
	created := httptest.NewRecorder()
	handler.ServeHTTP(created, request)
	if created.Code != http.StatusCreated {
		t.Fatalf("batch create=%d %s", created.Code, created.Body.String())
	}
	var response struct {
		Batch struct {
			ID       string   `json:"id"`
			JobIDs   []string `json:"job_ids"`
			Progress int      `json:"progress"`
		} `json:"batch"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &response); err != nil || response.Batch.ID == "" || len(response.Batch.JobIDs) != 2 || response.Batch.Progress != 0 {
		t.Fatalf("batch DTO=%+v decode_error=%v body=%s", response, err, created.Body.String())
	}
	createdEvents := collectClippingEvents(t, stream, 3)
	for _, name := range []string{"clipping_job", "clipping_batch"} {
		if len(createdEvents[name]) == 0 {
			t.Fatalf("create did not publish %s progress event: %v", name, createdEvents)
		}
	}
	jobs := clippingDashboardRequest(handler, http.MethodGet, "/api/clipping/jobs", "", cookie, "")
	if jobs.Code != http.StatusOK || !strings.Contains(jobs.Body.String(), response.Batch.JobIDs[0]) || !strings.Contains(jobs.Body.String(), `"progress":0`) {
		t.Fatalf("jobs list=%d %s", jobs.Code, jobs.Body.String())
	}
	batches := clippingDashboardRequest(handler, http.MethodGet, "/api/clipping/batches/"+response.Batch.ID, "", cookie, "")
	if batches.Code != http.StatusOK || !strings.Contains(batches.Body.String(), `"progress":0`) {
		t.Fatalf("batch detail=%d %s", batches.Code, batches.Body.String())
	}
}

func TestClippingWorkerCallbackReconcilesLargePayloadAndDuplicateOnce(t *testing.T) {
	store, security, handler, cookie := clippingDashboardFixture(t)
	source := makeReadyClippingSource(t, store, 60_000)
	batch, jobs := makeClippingBatch(t, store, []string{source.ID}, 1_000_000, 1_000_000, "large-callback-batch")
	job := jobs[0]
	stage, mediaToken, callbackToken := clippingClaimWithCapabilities(t, store, security, job, 200_000)
	media := clippingDashboardRequest(handler, http.MethodGet, "/api/clipping/sources/"+source.ID+"/media", "", nil, "")
	if media.Code != http.StatusUnauthorized {
		t.Fatalf("browser source media without session=%d", media.Code)
	}
	browserMedia := clippingDashboardRequest(handler, http.MethodGet, "/api/clipping/sources/"+source.ID+"/media", "", cookie, "")
	if browserMedia.Code != http.StatusOK || browserMedia.Body.String() != "synthetic source media" {
		t.Fatalf("browser source media=%d %q", browserMedia.Code, browserMedia.Body.String())
	}
	workerMedia := clippingDashboardRequest(handler, http.MethodGet, "/api/clipping/worker-media/"+job.ID+"/"+stage.AttemptID, "", nil, mediaToken)
	if workerMedia.Code != http.StatusOK || workerMedia.Body.String() != "synthetic source media" {
		t.Fatalf("worker media=%d %q", workerMedia.Code, workerMedia.Body.String())
	}
	if queryToken := clippingDashboardRequest(handler, http.MethodGet, "/api/clipping/worker-media/"+job.ID+"/"+stage.AttemptID+"?token="+mediaToken, "", nil, ""); queryToken.Code != http.StatusBadRequest {
		t.Fatalf("query-string capability accepted: %d", queryToken.Code)
	}
	stream, unsubscribe := store.events.subscribe()
	defer unsubscribe()
	largePayload := json.RawMessage(`{"fixture":"` + strings.Repeat("x", 34_000) + `"}`)
	callback := clippingCallbackFixture(job, stage, source, largePayload, 123)
	completed := clippingCallbackRequest(handler, job.ID, stage.AttemptID, callbackToken, callback)
	if completed.Code != http.StatusNoContent {
		t.Fatalf("large callback=%d %s", completed.Code, completed.Body.String())
	}
	progressEvents := collectClippingEvents(t, stream, 2)
	var jobEvent, batchEvent struct {
		Job struct {
			Progress int `json:"progress"`
		} `json:"job"`
		Batch struct {
			Progress int `json:"progress"`
		} `json:"batch"`
	}
	if err := json.Unmarshal(progressEvents["clipping_job"], &jobEvent); err != nil || jobEvent.Job.Progress != 100 {
		t.Fatalf("job EventSource progress payload=%s error=%v", progressEvents["clipping_job"], err)
	}
	if err := json.Unmarshal(progressEvents["clipping_batch"], &batchEvent); err != nil || batchEvent.Batch.Progress != 100 {
		t.Fatalf("batch EventSource progress payload=%s error=%v", progressEvents["clipping_batch"], err)
	}
	duplicate := clippingCallbackRequest(handler, job.ID, stage.AttemptID, callbackToken, callback)
	if duplicate.Code != http.StatusNoContent {
		t.Fatalf("duplicate callback=%d %s", duplicate.Code, duplicate.Body.String())
	}
	settledBatch, err := store.ClippingBatch(batch.ID)
	if err != nil || settledBatch.SpentMicroUSD != 123 || settledBatch.ReservedMicroUSD != 0 {
		t.Fatalf("callback spend was not settled once: batch=%+v error=%v", settledBatch, err)
	}
	artifacts, err := store.ClippingArtifacts(job.ID)
	if err != nil || len(artifacts) != 1 || len(artifacts[0].PayloadJSON) < 34_000 {
		t.Fatalf("large callback artifact=%d error=%v", len(artifacts), err)
	}
	detail := clippingDashboardRequest(handler, http.MethodGet, "/api/clipping/jobs/"+job.ID, "", cookie, "")
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), `"progress":100`) || !strings.Contains(detail.Body.String(), `"spent_micro_usd":123`) {
		t.Fatalf("completed job progress=%d %s", detail.Code, detail.Body.String())
	}
}

func TestClippingWorkerRejectsOversizedArtifactAfterSettlingKnownCost(t *testing.T) {
	store, security, handler, _ := clippingDashboardFixture(t)
	source := makeReadyClippingSource(t, store, 60_000)
	_, jobs := makeClippingBatch(t, store, []string{source.ID}, 1_000_000, 1_000_000, "oversized-callback-batch")
	job := jobs[0]
	stage, _, callbackToken := clippingClaimWithCapabilities(t, store, security, job, 100_000)
	oversizedPayload := json.RawMessage(`{"blob":"` + strings.Repeat("x", maxClippingArtifactPayloadBytes) + `"}`)
	callback := clippingCallbackFixture(job, stage, source, oversizedPayload, 77)
	rejected := clippingCallbackRequest(handler, job.ID, stage.AttemptID, callbackToken, callback)
	if rejected.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized callback=%d %s", rejected.Code, rejected.Body.String())
	}
	stored, err := store.ClippingJob(job.ID)
	if err != nil || stored.SpentMicroUSD != 77 || stored.ReservedMicroUSD != 0 || stored.Status != ClippingJobFailed {
		t.Fatalf("invalid artifact known cost was not settled: job=%+v error=%v", stored, err)
	}
	artifacts, err := store.ClippingArtifacts(job.ID)
	if err != nil || len(artifacts) != 0 {
		t.Fatalf("oversized artifact persisted: artifacts=%d error=%v", len(artifacts), err)
	}
}

func TestClippingStaleCallbackCannotAdvanceNewAttemptOrDoubleCharge(t *testing.T) {
	store, security, handler, _ := clippingDashboardFixture(t)
	source := makeReadyClippingSource(t, store, 60_000)
	batch, jobs := makeClippingBatch(t, store, []string{source.ID}, 1_000_000, 1_000_000, "stale-callback-batch")
	job := jobs[0]
	oldStage, _, oldCallbackToken := clippingClaimWithCapabilities(t, store, security, job, 200_000)
	if _, err := store.FailClippingStage(job.ID, ClippingStageAnalysis, oldStage.AttemptID, oldStage.LeaseToken, "fixture failure", false, 7); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RetryClippingStage(job.ID, ClippingStageAnalysis); err != nil {
		t.Fatal(err)
	}
	newStage, _, _ := clippingClaimWithCapabilities(t, store, security, job, 100_000)
	if oldStage.AttemptID == newStage.AttemptID || oldStage.IdempotencyKey != newStage.IdempotencyKey {
		t.Fatalf("attempt dispatch identity changed incorrectly: old=%+v new=%+v", oldStage, newStage)
	}
	stale := clippingCallbackFixture(job, oldStage, source, json.RawMessage(`{"old_attempt":true}`), 999)
	response := clippingCallbackRequest(handler, job.ID, oldStage.AttemptID, oldCallbackToken, stale)
	if response.Code != http.StatusNoContent {
		t.Fatalf("settled stale callback response=%d %s", response.Code, response.Body.String())
	}
	currentJob, err := store.ClippingJob(job.ID)
	if err != nil || currentJob.Status != ClippingJobRunning || currentJob.SpentMicroUSD != 7 || currentJob.ReservedMicroUSD != 100_000 {
		t.Fatalf("stale callback changed current attempt or spend: job=%+v error=%v", currentJob, err)
	}
	currentStages, err := store.ClippingStages(job.ID)
	if err != nil || len(currentStages) != 1 || currentStages[0].AttemptID != newStage.AttemptID || currentStages[0].Status != ClippingStageRunning {
		t.Fatalf("stale callback advanced current stage: stages=%+v error=%v", currentStages, err)
	}
	artifacts, err := store.ClippingArtifacts(job.ID)
	if err != nil || len(artifacts) != 0 {
		t.Fatalf("stale callback persisted an artifact: count=%d error=%v", len(artifacts), err)
	}
	storedBatch, err := store.ClippingBatch(batch.ID)
	if err != nil || storedBatch.SpentMicroUSD != 7 || storedBatch.ReservedMicroUSD != 100_000 {
		t.Fatalf("stale callback changed batch accounting: batch=%+v error=%v", storedBatch, err)
	}
}

func TestClippingCancellationRevokesMediaButAllowsOneLateSettlement(t *testing.T) {
	store, security, handler, cookie := clippingDashboardFixture(t)
	source := makeReadyClippingSource(t, store, 60_000)
	batch, jobs := makeClippingBatch(t, store, []string{source.ID}, 1_000_000, 1_000_000, "cancel-late-callback-batch")
	job := jobs[0]
	stage, mediaToken, callbackToken := clippingClaimWithCapabilities(t, store, security, job, 300_000)
	canceled := clippingDashboardRequest(handler, http.MethodPost, "/api/clipping/jobs/"+job.ID+"/cancel", `{}`, cookie, "")
	if canceled.Code != http.StatusOK {
		t.Fatalf("cancel job=%d %s", canceled.Code, canceled.Body.String())
	}
	media := clippingDashboardRequest(handler, http.MethodGet, "/api/clipping/worker-media/"+job.ID+"/"+stage.AttemptID, "", nil, mediaToken)
	if media.Code != http.StatusGone {
		t.Fatalf("canceled worker media=%d %s", media.Code, media.Body.String())
	}
	callback := clippingCallbackFixture(job, stage, source, json.RawMessage(`{"fixture":true}`), 456)
	settled := clippingCallbackRequest(handler, job.ID, stage.AttemptID, callbackToken, callback)
	if settled.Code != http.StatusNoContent {
		t.Fatalf("canceled late callback=%d %s", settled.Code, settled.Body.String())
	}
	duplicate := clippingCallbackRequest(handler, job.ID, stage.AttemptID, callbackToken, callback)
	if duplicate.Code != http.StatusNoContent {
		t.Fatalf("duplicate canceled callback=%d %s", duplicate.Code, duplicate.Body.String())
	}
	storedJob, err := store.ClippingJob(job.ID)
	if err != nil || storedJob.Status != ClippingJobCanceled || storedJob.SpentMicroUSD != 456 || storedJob.ReservedMicroUSD != 0 {
		t.Fatalf("canceled job settlement=%+v error=%v", storedJob, err)
	}
	storedBatch, err := store.ClippingBatch(batch.ID)
	if err != nil || storedBatch.SpentMicroUSD != 456 || storedBatch.ReservedMicroUSD != 0 {
		t.Fatalf("canceled batch settlement=%+v error=%v", storedBatch, err)
	}
	artifacts, err := store.ClippingArtifacts(job.ID)
	if err != nil || len(artifacts) != 0 {
		t.Fatalf("canceled callback published artifact: count=%d error=%v", len(artifacts), err)
	}
}

func TestClippingFailedCallbackSanitizesWorkerErrorAndSettlesOnce(t *testing.T) {
	store, security, handler, cookie := clippingDashboardFixture(t)
	source := makeReadyClippingSource(t, store, 60_000)
	_, jobs := makeClippingBatch(t, store, []string{source.ID}, 1_000_000, 1_000_000, "failed-callback-safe-error")
	job := jobs[0]
	stage, _, callbackToken := clippingClaimWithCapabilities(t, store, security, job, 200_000)
	stream, unsubscribe := store.events.subscribe()
	defer unsubscribe()

	secretURL := "https://private.example/media?signature=signed-url-secret"
	secretToken := "worker-capability-secret"
	callback := clippingCallbackFixture(job, stage, source, json.RawMessage(`{"fixture":true}`), 37)
	callback.Status = "failed"
	callback.Error = "upstream failed fetching " + secretURL + " with " + secretToken
	failed := clippingCallbackRequest(handler, job.ID, stage.AttemptID, callbackToken, callback)
	if failed.Code != http.StatusNoContent {
		t.Fatalf("failed callback=%d %s", failed.Code, failed.Body.String())
	}

	events := collectClippingEvents(t, stream, 2)
	for _, name := range []string{"clipping_job", "clipping_batch"} {
		data := string(events[name])
		if strings.Contains(data, secretURL) || strings.Contains(data, secretToken) {
			t.Fatalf("%s event leaked worker error: %s", name, data)
		}
	}

	stored, err := store.ClippingJob(job.ID)
	if err != nil || stored.Status != ClippingJobFailed || stored.SpentMicroUSD != 37 || stored.ReservedMicroUSD != 0 || stored.Error != "clipping analysis stage failed" {
		t.Fatalf("failed callback job=%+v error=%v", stored, err)
	}
	stages, err := store.ClippingStages(job.ID)
	if err != nil || len(stages) != 1 || stages[0].Status != ClippingStageFailed || stages[0].Error != "clipping analysis stage failed" {
		t.Fatalf("failed callback stages=%+v error=%v", stages, err)
	}
	detail := clippingDashboardRequest(handler, http.MethodGet, "/api/clipping/jobs/"+job.ID, "", cookie, "")
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), `"error":"clipping analysis stage failed"`) || strings.Contains(detail.Body.String(), secretURL) || strings.Contains(detail.Body.String(), secretToken) {
		t.Fatalf("failed job detail=%d %s", detail.Code, detail.Body.String())
	}

	duplicate := clippingCallbackRequest(handler, job.ID, stage.AttemptID, callbackToken, callback)
	if duplicate.Code != http.StatusNoContent {
		t.Fatalf("duplicate failed callback=%d %s", duplicate.Code, duplicate.Body.String())
	}
	storedBatch, err := store.ClippingBatch(job.BatchID)
	if err != nil || storedBatch.SpentMicroUSD != 37 || storedBatch.ReservedMicroUSD != 0 {
		t.Fatalf("duplicate callback charged more than once: batch=%+v error=%v", storedBatch, err)
	}

	retried := clippingDashboardRequest(handler, http.MethodPost, "/api/clipping/jobs/"+job.ID+"/retry", `{"stage_name":"analysis"}`, cookie, "")
	if retried.Code != http.StatusAccepted {
		t.Fatalf("explicit retry=%d %s", retried.Code, retried.Body.String())
	}
	stored, err = store.ClippingJob(job.ID)
	stages, stageErr := store.ClippingStages(job.ID)
	if err != nil || stageErr != nil || stored.Status != ClippingJobQueued || stored.SpentMicroUSD != 37 || len(stages) != 1 || stages[0].Status != ClippingStageQueued {
		t.Fatalf("explicit retry state job=%+v stages=%+v error=%v stage_error=%v", stored, stages, err, stageErr)
	}
}

func TestClippingExpiredCallbackKeepsUncertainReservation(t *testing.T) {
	store, security, handler, _ := clippingDashboardFixture(t)
	source := makeReadyClippingSource(t, store, 60_000)
	batch, jobs := makeClippingBatch(t, store, []string{source.ID}, 1_000_000, 1_000_000, "expired-callback-batch")
	job := jobs[0]
	stage, _, callbackToken := clippingClaimWithCapabilities(t, store, security, job, 250_000)
	app := &dashboardApp{store: store, security: security}
	if err := app.persistClippingCapabilityExpiry(job.ID, stage.AttemptID, clippingCapabilityCallback, time.Now().Add(-time.Second).Unix()); err != nil {
		t.Fatal(err)
	}
	callback := clippingCallbackFixture(job, stage, source, json.RawMessage(`{"fixture":true}`), 11)
	expired := clippingCallbackRequest(handler, job.ID, stage.AttemptID, callbackToken, callback)
	if expired.Code != http.StatusGone {
		t.Fatalf("expired callback=%d %s", expired.Code, expired.Body.String())
	}
	storedBatch, err := store.ClippingBatch(batch.ID)
	if err != nil || storedBatch.ReservedMicroUSD != 250_000 || storedBatch.SpentMicroUSD != 0 {
		t.Fatalf("expiry discarded unresolved reservation: batch=%+v error=%v", storedBatch, err)
	}
}

func TestClippingGoPythonProtocolFixtureRoundTrip(t *testing.T) {
	store, security, handler, _ := clippingDashboardFixture(t)
	source := makeReadyClippingSource(t, store, 60_000)
	batch, jobs := makeClippingBatch(t, store, []string{source.ID}, 1_000_000, 1_000_000, "cross-language-protocol")
	app := &dashboardApp{store: store, security: security}
	prepared, claimed, err := app.prepareClippingStageDispatch(jobs[0].ID, 100_000, "https://framevault.example")
	if err != nil || !claimed {
		t.Fatalf("prepare worker dispatch=%+v claimed=%v error=%v", prepared, claimed, err)
	}
	stage, err := store.ClippingStages(jobs[0].ID)
	if err != nil || len(stage) != 1 {
		t.Fatalf("prepared stage snapshot=%+v error=%v", stage, err)
	}
	job, err := store.ClippingJob(jobs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	dispatchJSON, err := json.Marshal(prepared.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(dispatchJSON, []byte(prepared.MediaBearer)) || bytes.Contains(dispatchJSON, []byte(prepared.CallbackBearer)) {
		t.Fatalf("worker payload contains a capability: %s", dispatchJSON)
	}
	preparedJSON, err := json.Marshal(prepared)
	if err != nil || bytes.Contains(preparedJSON, []byte(prepared.MediaBearer)) || bytes.Contains(preparedJSON, []byte(prepared.CallbackBearer)) {
		t.Fatalf("prepared dispatch serialized bearer capabilities: %s error=%v", preparedJSON, err)
	}
	for scope, token := range map[string]string{clippingCapabilityMedia: prepared.MediaBearer, clippingCapabilityCallback: prepared.CallbackBearer} {
		stored, err := store.Setting(clippingCapabilitySettingKey(job.ID, stage[0].AttemptID, scope))
		if err != nil || strings.Contains(stored, token) {
			t.Fatalf("%s capability was not encrypted at rest: %q error=%v", scope, stored, err)
		}
		recovered, err := app.clippingCapability(job.ID, stage[0].AttemptID, scope)
		if err != nil || recovered != token {
			t.Fatalf("%s capability did not round trip: %q error=%v", scope, recovered, err)
		}
	}
	python := `import json,sys,tempfile
from clipping_protocol import SQLiteDispatchLedger, StageResult, execute_stage
payload=json.load(sys.stdin)
artifact={"type":"clipping.analysis","schema_version":"1","version":1,"source_duration_ms":payload["source_duration_ms"],"time_ranges":[{"start_ms":100,"end_ms":2000}],"payload":{"fixture":"python-result"}}
with tempfile.TemporaryDirectory() as directory:
    ledger=SQLiteDispatchLedger(directory + "/worker.sqlite3")
    result=execute_stage(payload, media_token="media-fixture", callback_token="callback-fixture", ledger=ledger, client=lambda dispatch, token: StageResult(artifact, 42), now=int(__import__("time").time()))
    ledger.close()
print(json.dumps(result,separators=(",",":")))`
	command := exec.Command("python3", "-c", python)
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	command.Dir = repoRoot
	command.Env = append(command.Environ(), "PYTHONPATH="+filepath.Join(repoRoot, "workers", "modal"))
	command.Stdin = bytes.NewReader(dispatchJSON)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	resultJSON, err := command.Output()
	if err != nil {
		t.Fatalf("Python protocol fixture failed: %v: %s", err, stderr.String())
	}
	var callback clippingWorkerCallback
	if err := json.Unmarshal(resultJSON, &callback); err != nil {
		t.Fatalf("decode Python callback into Go contract: %v (%s)", err, resultJSON)
	}
	if callback.ProtocolVersion != clippingProtocolVersion || callback.JobID != jobs[0].ID || callback.AttemptID != stage[0].AttemptID || callback.DispatchID != stage[0].IdempotencyKey || callback.Artifact.SchemaVersion != "1" || callback.Artifact.SourceDurationMS != source.DurationMS || callback.ActualCostMicroUSD != 42 {
		t.Fatalf("Go/Python protocol mismatch: %+v", callback)
	}
	completed := clippingCallbackRequest(handler, job.ID, stage[0].AttemptID, prepared.CallbackBearer, callback)
	if completed.Code != http.StatusNoContent {
		t.Fatalf("Go rejected Python callback contract: %d %s", completed.Code, completed.Body.String())
	}
	duplicate := clippingCallbackRequest(handler, job.ID, stage[0].AttemptID, prepared.CallbackBearer, callback)
	if duplicate.Code != http.StatusNoContent {
		t.Fatalf("Go rejected duplicate Python callback: %d %s", duplicate.Code, duplicate.Body.String())
	}
	storedBatch, err := store.ClippingBatch(batch.ID)
	if err != nil || storedBatch.SpentMicroUSD != 42 || storedBatch.ReservedMicroUSD != 0 {
		t.Fatalf("Python callback cost was not applied once: batch=%+v error=%v", storedBatch, err)
	}
}
