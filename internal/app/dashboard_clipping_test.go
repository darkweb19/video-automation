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

func clippingDashboardFixtureWithAvailableBytes(t *testing.T, _ int64) (*Store, *Security, http.Handler, *http.Cookie) {
	t.Helper()
	store := newTestStore(t)
	provisionTestUser(t, store)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	acquisition := NewClippingAcquisition(store, security)

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

func clippingCreateJob(t *testing.T, store *Store, sourceID, key string) (clippingJobView, int) {
	t.Helper()
	job := makeClippingJob(t, store, sourceID, key)
	return clippingJobView{ID: job.ID}, http.StatusCreated
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
	prepared, claimed, err := app.historicalPrepareClippingStageDispatch(jobID, 0, "https://framevault.dev")
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
