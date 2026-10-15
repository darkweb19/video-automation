package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func clippingDashboardFixture(t *testing.T, allowLowDisk ...bool) (*Store, *Security, http.Handler, *http.Cookie) {
	availableBytes := int64(0)
	if len(allowLowDisk) > 0 && allowLowDisk[0] {
		availableBytes = 8 << 30
	}
	return clippingDashboardFixtureWithAvailableBytes(t, availableBytes)
}

func clippingDashboardFixtureWithAvailableBytes(t *testing.T, availableBytes int64) (*Store, *Security, http.Handler, *http.Cookie) {
	t.Helper()
	store := newTestStore(t)
	provisionTestUser(t, store)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	acquisition := NewClippingAcquisition(store, security)
	if availableBytes > 0 {
		acquisition.diskAvailable = func(string) (int64, error) { return availableBytes, nil }
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

func clippingDashboardRequestWithHeaders(handler http.Handler, method, path, body string, cookie *http.Cookie, headers map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
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
	request := httptest.NewRequest(http.MethodPost, "/api/clipping/worker-callbacks/"+jobID+"/"+attemptID, strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func clippingCreateJobBody(sourceID string) string {
	body, _ := json.Marshal(map[string]any{
		"source_id": sourceID, "content_type": "general", "min_clip_seconds": 15,
		"max_clip_seconds": 180, "candidate_limit": 10,
	})
	return string(body)
}

func clippingCreateJob(t *testing.T, handler http.Handler, cookie *http.Cookie, sourceID, key string) (clippingJobView, int) {
	t.Helper()
	response := clippingDashboardRequestWithHeaders(handler, http.MethodPost, "/api/clipping/jobs", clippingCreateJobBody(sourceID), cookie,
		map[string]string{"Idempotency-Key": key})
	if response.Code != http.StatusCreated && response.Code != http.StatusOK {
		t.Fatalf("create clipping job: %d %s", response.Code, response.Body.String())
	}
	var result struct {
		Job       clippingJobView `json:"job"`
		Duplicate bool            `json:"duplicate"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode clipping job response: %v (%s)", err, response.Body.String())
	}
	return result.Job, response.Code
}

func TestClippingDashboardWorkerConfigHasNoBillingFields(t *testing.T) {
	_, _, handler, cookie := clippingDashboardFixture(t)
	if response := clippingDashboardRequest(handler, http.MethodGet, "/api/clipping/config/worker", "", nil, ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated worker config=%d", response.Code)
	}
	if response := clippingDashboardRequest(handler, http.MethodPut, "/api/clipping/config/worker", `{"enabled":false}`, nil, ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated worker update=%d", response.Code)
	}
	initial := clippingDashboardRequest(handler, http.MethodGet, "/api/clipping/config/worker", "", cookie, "")
	if initial.Code != http.StatusOK {
		t.Fatalf("initial worker config=%d %s", initial.Code, initial.Body.String())
	}
	for _, forbidden := range []string{"rate_micro_usd_per_second", "billing_basis", "budget_micro_usd", "invoice", "bearer_token", "endpoint_path"} {
		if strings.Contains(initial.Body.String(), forbidden) {
			t.Fatalf("safe worker config contains %q: %s", forbidden, initial.Body.String())
		}
	}

	valid := `{"enabled":true,"endpoint":"https://fn-worker.modal.run/dispatch","bearer_token":"` + strings.Repeat("a", 64) + `","pipeline_revision":"framevault-m3-v1","max_compute_seconds":43200}`
	updated := clippingDashboardRequest(handler, http.MethodPut, "/api/clipping/config/worker", valid, cookie, "")
	if updated.Code != http.StatusOK {
		t.Fatalf("worker config update=%d %s", updated.Code, updated.Body.String())
	}
	for _, expected := range []string{`"configured":true`, `"enabled":true`, `"endpoint_host":"fn-worker.modal.run"`, `"max_compute_seconds":43200`, `"pipeline_revision":"framevault-m3-v1"`} {
		if !strings.Contains(updated.Body.String(), expected) {
			t.Errorf("worker update response missing %s: %s", expected, updated.Body.String())
		}
	}
	for _, forbidden := range []string{strings.Repeat("a", 64), "/dispatch", "rate_micro_usd_per_second", "billing_basis", "budget_micro_usd", "invoice"} {
		if strings.Contains(updated.Body.String(), forbidden) {
			t.Errorf("worker update response contains %q: %s", forbidden, updated.Body.String())
		}
	}

	for _, body := range []string{
		strings.Replace(valid, `"max_compute_seconds":43200`, `"max_compute_seconds":0`, 1),
		strings.Replace(valid, `"max_compute_seconds":43200`, `"max_compute_seconds":43201`, 1),
		strings.TrimSuffix(valid, "}") + `,"rate_micro_usd_per_second":10}`,
		strings.TrimSuffix(valid, "}") + `,"extra":true}`,
		strings.TrimSuffix(valid, "}") + `,"enabled":false}`,
	} {
		if response := clippingDashboardRequest(handler, http.MethodPut, "/api/clipping/config/worker", body, cookie, ""); response.Code < 400 {
			t.Errorf("accepted invalid worker config body: %s", body)
		}
	}
	crossOrigin := clippingDashboardRequestWithHeaders(handler, http.MethodPut, "/api/clipping/config/worker", valid, cookie,
		map[string]string{"Origin": "https://attacker.example"})
	if crossOrigin.Code != http.StatusForbidden {
		t.Fatalf("cross-origin worker update=%d %s", crossOrigin.Code, crossOrigin.Body.String())
	}
}

func TestClippingDashboardImportsOnlyAttestedYouTubeLinks(t *testing.T) {
	store, _, handler, cookie := clippingDashboardFixtureWithAvailableBytes(t, 1<<40)
	config := clippingDashboardRequest(handler, http.MethodGet, "/api/clipping/config", "", cookie, "")
	if config.Code != http.StatusOK {
		t.Fatalf("clipping config=%d %s", config.Code, config.Body.String())
	}
	var configView struct {
		AllowedImportKinds []ClippingSourceKind `json:"allowed_import_kinds"`
	}
	if err := json.Unmarshal(config.Body.Bytes(), &configView); err != nil || len(configView.AllowedImportKinds) != 1 || configView.AllowedImportKinds[0] != ClippingSourceYouTube {
		t.Fatalf("allowed import kinds=%v error=%v body=%s", configView.AllowedImportKinds, err, config.Body.String())
	}
	for _, forbidden := range []string{"retention", "upload", "batch", "budget", "invoice", "dropbox", "google_drive"} {
		if strings.Contains(strings.ToLower(config.Body.String()), forbidden) {
			t.Errorf("clipping config advertises removed workflow %q: %s", forbidden, config.Body.String())
		}
	}

	const watchURL = "https://www.youtube.com/watch?v=abcdefghijk"
	if response := clippingDashboardRequest(handler, http.MethodPost, "/api/clipping/sources/import", `{"url":"`+watchURL+`","rights_attested":true}`, nil, ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated import=%d %s", response.Code, response.Body.String())
	}
	if response := clippingDashboardRequest(handler, http.MethodPost, "/api/clipping/sources/import", `{"url":"`+watchURL+`","rights_attested":false}`, cookie, ""); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("import without rights=%d %s", response.Code, response.Body.String())
	}
	crossOrigin := clippingDashboardRequestWithHeaders(handler, http.MethodPost, "/api/clipping/sources/import", `{"url":"`+watchURL+`","rights_attested":true}`, cookie,
		map[string]string{"Origin": "https://attacker.example"})
	if crossOrigin.Code != http.StatusForbidden {
		t.Fatalf("cross-origin import=%d %s", crossOrigin.Code, crossOrigin.Body.String())
	}
	for _, unsupported := range []string{
		`{"url":"https://www.youtube.com/playlist?list=PL1234567890","rights_attested":true}`,
		`{"url":"https://drive.google.com/file/d/example/view","rights_attested":true}`,
		`{"url":"https://dropbox.com/s/example/video.mp4","rights_attested":true}`,
	} {
		if response := clippingDashboardRequest(handler, http.MethodPost, "/api/clipping/sources/import", unsupported, cookie, ""); response.Code < 400 {
			t.Errorf("accepted non-single-YouTube source import: %d %s", response.Code, response.Body.String())
		}
	}
	if sources, err := store.ClippingSources(); err != nil || len(sources) != 0 {
		t.Fatalf("rejected import created a source: sources=%d error=%v", len(sources), err)
	}
	created := clippingDashboardRequest(handler, http.MethodPost, "/api/clipping/sources/import", `{"url":"`+watchURL+`","rights_attested":true}`, cookie, "")
	if created.Code != http.StatusAccepted {
		t.Fatalf("attested public YouTube import=%d %s", created.Code, created.Body.String())
	}
	var response struct {
		Source clippingSourceView `json:"source"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Source.Kind != ClippingSourceYouTube || response.Source.Status != ClippingSourceImporting || !response.Source.RightsAttested {
		t.Fatalf("YouTube import response lost source kind, lifecycle, or attestation: %+v", response.Source)
	}
	queuedImport := clippingDashboardRequestWithHeaders(handler, http.MethodPost, "/api/clipping/jobs", clippingCreateJobBody(response.Source.ID), cookie,
		map[string]string{"Idempotency-Key": "importing-source-job"})
	if queuedImport.Code < 400 {
		t.Fatalf("queued an analysis job before YouTube import completed: %d %s", queuedImport.Code, queuedImport.Body.String())
	}
}

func TestClippingDashboardCreatesBudgetFreeIdempotentJobs(t *testing.T) {
	store, _, handler, cookie := clippingDashboardFixture(t)
	source := makeReadyClippingSource(t, store, clippingM3FixtureDurationMS)
	body := clippingCreateJobBody(source.ID)
	unauthorized := clippingDashboardRequestWithHeaders(handler, http.MethodPost, "/api/clipping/jobs", body, nil,
		map[string]string{"Idempotency-Key": "unauthorized-job"})
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated job create=%d %s", unauthorized.Code, unauthorized.Body.String())
	}
	missingKey := clippingDashboardRequest(handler, http.MethodPost, "/api/clipping/jobs", body, cookie, "")
	if missingKey.Code < 400 {
		t.Fatalf("created job without idempotency key: %d %s", missingKey.Code, missingKey.Body.String())
	}
	created := clippingDashboardRequestWithHeaders(handler, http.MethodPost, "/api/clipping/jobs", body, cookie,
		map[string]string{"Idempotency-Key": "simple-job-create-001"})
	if created.Code != http.StatusCreated {
		t.Fatalf("budget-free job create=%d %s", created.Code, created.Body.String())
	}
	var result struct {
		Job       clippingJobView `json:"job"`
		Duplicate bool            `json:"duplicate"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &result); err != nil || result.Job.ID == "" || result.Job.SourceID != source.ID || result.Job.Status != ClippingJobQueued || result.Duplicate {
		t.Fatalf("job create response=%+v error=%v body=%s", result, err, created.Body.String())
	}
	for _, forbidden := range []string{"budget_micro_usd", "reserved_micro_usd", "spent_micro_usd", "invoice", "reconcile"} {
		if strings.Contains(created.Body.String(), forbidden) {
			t.Errorf("job create response exposes removed billing workflow %q: %s", forbidden, created.Body.String())
		}
	}
	duplicate := clippingDashboardRequestWithHeaders(handler, http.MethodPost, "/api/clipping/jobs", body, cookie,
		map[string]string{"Idempotency-Key": "simple-job-create-001"})
	if duplicate.Code != http.StatusOK || !strings.Contains(duplicate.Body.String(), `"duplicate":true`) || !strings.Contains(duplicate.Body.String(), result.Job.ID) {
		t.Fatalf("idempotent replay=%d %s", duplicate.Code, duplicate.Body.String())
	}
	withBudget := strings.TrimSuffix(body, "}") + `,"budget_micro_usd":1000000}`
	if response := clippingDashboardRequestWithHeaders(handler, http.MethodPost, "/api/clipping/jobs", withBudget, cookie,
		map[string]string{"Idempotency-Key": "simple-job-with-removed-budget"}); response.Code < 400 {
		t.Fatalf("accepted removed job budget field: %d %s", response.Code, response.Body.String())
	}
	jobs := clippingDashboardRequest(handler, http.MethodGet, "/api/clipping/jobs", "", cookie, "")
	var listed struct {
		Jobs []clippingJobView `json:"jobs"`
	}
	if jobs.Code != http.StatusOK || json.Unmarshal(jobs.Body.Bytes(), &listed) != nil || len(listed.Jobs) != 1 || listed.Jobs[0].ID != result.Job.ID {
		t.Fatalf("jobs list=%d %s", jobs.Code, jobs.Body.String())
	}
	job := clippingDashboardRequest(handler, http.MethodGet, "/api/clipping/jobs/"+result.Job.ID, "", cookie, "")
	if job.Code != http.StatusOK || !strings.Contains(job.Body.String(), result.Job.ID) {
		t.Fatalf("job detail=%d %s", job.Code, job.Body.String())
	}
	results := clippingDashboardRequest(handler, http.MethodGet, "/api/clipping/jobs/"+result.Job.ID+"/results", "", cookie, "")
	if results.Code != http.StatusOK || !strings.Contains(results.Body.String(), `"job_id":"`+result.Job.ID+`"`) || strings.Contains(results.Body.String(), "budget_micro_usd") {
		t.Fatalf("read-only results=%d %s", results.Code, results.Body.String())
	}
}

func TestClippingDashboardRemovedRoutesAreAbsent(t *testing.T) {
	_, _, handler, cookie := clippingDashboardFixture(t)
	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/api/clipping/sources/upload"},
		{http.MethodPut, "/api/clipping/sources/clip_src_0123456789abcdef0123456789abcdef/upload"},
		{http.MethodPost, "/api/clipping/sources/clip_src_0123456789abcdef0123456789abcdef/finalize"},
		{http.MethodPut, "/api/clipping/config/retention"},
		{http.MethodGet, "/api/clipping/batches"},
		{http.MethodPost, "/api/clipping/batches"},
		{http.MethodPost, "/api/clipping/jobs/clipjob_0123456789abcdef0123456789abcdef/selection"},
		{http.MethodPost, "/api/clipping/jobs/clipjob_0123456789abcdef0123456789abcdef/reconcile-cost"},
	} {
		response := clippingDashboardRequest(handler, route.method, route.path, `{}`, cookie, "")
		if response.Code != http.StatusNotFound && response.Code != http.StatusMethodNotAllowed {
			t.Errorf("removed endpoint %s %s still responds: %d %s", route.method, route.path, response.Code, response.Body.String())
		}
	}
}

func clippingCreateWorkerConfigForTest(t *testing.T, store *Store, security *Security) clippingWorkerConfig {
	t.Helper()
	config, err := saveClippingWorkerConfig(store, security, clippingWorkerConfigInput{
		Enabled: true, Endpoint: "https://fn-worker.modal.run/dispatch", BearerToken: strings.Repeat("f", 64),
		PipelineRevision: clippingDefaultPipelineRevision, MaxComputeSeconds: 120,
	})
	if err != nil {
		t.Fatal(err)
	}
	return config
}

func clippingStartSimpleJobAttempt(t *testing.T, store *Store, security *Security, jobID string) (clippingPreparedDispatch, ClippingStage) {
	t.Helper()
	if claimed, err := store.ClaimClippingAnalysisSource(jobID); err != nil || !claimed {
		t.Fatalf("claim source analysis: claimed=%v error=%v", claimed, err)
	}
	app := &dashboardApp{store: store, security: security}
	prepared, claimed, err := app.prepareClippingStageDispatch(jobID, 0, "https://framevault.dev")
	if err != nil || !claimed {
		t.Fatalf("prepare worker dispatch: claimed=%v error=%v", claimed, err)
	}
	if err := store.SetClippingAnalysisClaimAttempt(jobID, prepared.Payload.AttemptID); err != nil {
		t.Fatal(err)
	}
	stages, err := store.ClippingStages(jobID)
	if err != nil || len(stages) != 1 {
		t.Fatalf("analysis stage snapshot: %+v error=%v", stages, err)
	}
	return prepared, stages[0]
}
