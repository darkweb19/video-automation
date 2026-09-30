package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func callbackTestApp(t *testing.T) (*Store, *Security, http.Handler) {
	t.Helper()
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return store, security, NewDashboardHandler(store, security, logger)
}

func prepareTestCallbackJob(t *testing.T, app *dashboardApp, request GenerateRequest) (callbackJob, string) {
	t.Helper()
	t.Setenv("VIDEO_CALLBACK_BASE_URL", "https://dashboard.example.test/")
	job, err := app.prepareCallbackJob(request, "provider_snapshot")
	if err != nil {
		t.Fatal(err)
	}
	token, err := app.security.DecryptSetting("video_callback."+job.ID, job.EncryptedToken)
	if err != nil {
		t.Fatal(err)
	}
	if token == job.EncryptedToken || job.TokenHash != tokenHash(token) {
		t.Fatal("callback token was not encrypted and hashed as expected")
	}
	return job, token
}

func postVideoCallback(t *testing.T, handler http.Handler, id, token string, callback videoCallback) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(callback)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/video-callbacks/"+id, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestCallbackBaseURLRequiresPublicHTTPSOrigin(t *testing.T) {
	for _, test := range []struct {
		name string
		url  string
	}{
		{name: "missing"},
		{name: "http", url: "http://dashboard.example.test"},
		{name: "path", url: "https://dashboard.example.test/dashboard"},
		{name: "query", url: "https://dashboard.example.test?callback=1"},
		{name: "local host", url: "https://localhost"},
		{name: "private address", url: "https://10.0.0.5"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("VIDEO_CALLBACK_BASE_URL", test.url)
			if _, err := callbackBaseURL(); err == nil || !strings.Contains(err.Error(), "VIDEO_CALLBACK_BASE_URL") {
				t.Fatalf("callbackBaseURL(%q) error = %v", test.url, err)
			}
		})
	}
	t.Setenv("VIDEO_CALLBACK_BASE_URL", " https://dashboard.example.test/ ")
	got, err := callbackBaseURL()
	if err != nil || got != "https://dashboard.example.test" {
		t.Fatalf("callbackBaseURL() = %q, %v", got, err)
	}
}

func TestVideoCallbackAuthenticationAndMonotonicTerminalState(t *testing.T) {
	store, security, handler := callbackTestApp(t)
	app := &dashboardApp{store: store, security: security, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	job, token := prepareTestCallbackJob(t, app, GenerateRequest{Prompt: "A paper kite", Model: "modal/test", Duration: 6})
	if err := store.insertCallbackGeneration(job, ""); err != nil {
		t.Fatal(err)
	}

	unauthorized := postVideoCallback(t, handler, job.ID, "wrong-token", videoCallback{ID: job.ID, Status: "processing", Sequence: 1, Progress: 20})
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("invalid token status = %d, body = %s", unauthorized.Code, unauthorized.Body.String())
	}
	noAuth := postVideoCallback(t, handler, job.ID, "", videoCallback{ID: job.ID, Status: "processing", Sequence: 1, Progress: 20})
	if noAuth.Code != http.StatusUnauthorized {
		t.Fatalf("missing token status = %d, body = %s", noAuth.Code, noAuth.Body.String())
	}

	accepted := postVideoCallback(t, handler, job.ID, token, videoCallback{ID: job.ID, Status: "processing", Sequence: 1, Progress: 20})
	if accepted.Code != http.StatusNoContent {
		t.Fatalf("processing callback status = %d, body = %s", accepted.Code, accepted.Body.String())
	}
	completed := postVideoCallback(t, handler, job.ID, token, videoCallback{ID: job.ID, Status: "completed", Sequence: 2, Progress: 100})
	if completed.Code != http.StatusNoContent {
		t.Fatalf("completion callback status = %d, body = %s", completed.Code, completed.Body.String())
	}

	// Duplicate and older callbacks must not undo completion or restart the
	// local download state after a worker retries callback delivery.
	for _, callback := range []videoCallback{
		{ID: job.ID, Status: "processing", Sequence: 1, Progress: 35},
		{ID: job.ID, Status: "failed", Sequence: 3, ErrorCode: "worker_startup_failed"},
	} {
		response := postVideoCallback(t, handler, job.ID, token, callback)
		if response.Code != http.StatusNoContent {
			t.Fatalf("duplicate callback status = %d, body = %s", response.Code, response.Body.String())
		}
	}
	record, err := store.Generation(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != "downloading" || record.Progress != 100 || record.Error != "" {
		t.Fatalf("terminal success regressed: %+v", record)
	}
	callbackState, err := store.callbackJob(job.ID)
	if err != nil || callbackState.State != "completed" || callbackState.Sequence != 2 {
		t.Fatalf("callback state = %+v, %v", callbackState, err)
	}
}

func TestVideoCallbackPersistsUsefulFailure(t *testing.T) {
	store, security, handler := callbackTestApp(t)
	app := &dashboardApp{store: store, security: security, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	job, token := prepareTestCallbackJob(t, app, GenerateRequest{Prompt: "A paper kite", Model: "modal/test", Duration: 6})
	if err := store.insertCallbackGeneration(job, ""); err != nil {
		t.Fatal(err)
	}
	response := postVideoCallback(t, handler, job.ID, token, videoCallback{
		ID: job.ID, Status: "failed", Sequence: 1, ErrorCode: "worker_startup_failed", Stage: "startup",
	})
	if response.Code != http.StatusNoContent {
		t.Fatalf("failure callback status = %d, body = %s", response.Code, response.Body.String())
	}
	record, err := store.Generation(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != "failed" || !strings.Contains(record.Error, "could not start or load its model") {
		t.Fatalf("generation failure was not useful: %+v", record)
	}
	if !strings.Contains(record.Error, "worker") {
		t.Fatalf("failure event missing user-facing error: %+v", record.Events)
	}
}

func TestCallbackLookupDistinguishesMissingJobsFromDatabaseErrors(t *testing.T) {
	store, _, handler := callbackTestApp(t)
	missing := postVideoCallback(t, handler, "gen_missing_callback", strings.Repeat("a", 64), videoCallback{
		ID: "gen_missing_callback", Status: "completed", Sequence: 1, Progress: 100,
	})
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing callback status = %d, body = %s", missing.Code, missing.Body.String())
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	databaseFailure := postVideoCallback(t, handler, "gen_missing_callback", strings.Repeat("b", 64), videoCallback{
		ID: "gen_missing_callback", Status: "completed", Sequence: 1, Progress: 100,
	})
	if databaseFailure.Code != http.StatusServiceUnavailable {
		t.Fatalf("database failure status = %d, body = %s", databaseFailure.Code, databaseFailure.Body.String())
	}
	if strings.Contains(databaseFailure.Body.String(), "database is closed") || strings.Contains(databaseFailure.Body.String(), strings.Repeat("b", 64)) {
		t.Fatalf("database internals or callback credential leaked: %s", databaseFailure.Body.String())
	}
}

func TestLateCallbackFromOldSceneAttemptCannotChangeRetriedScene(t *testing.T) {
	store, security, handler := callbackTestApp(t)
	app := &dashboardApp{store: store, security: security, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	project, err := store.InsertProjectWithProviderConfig("A fox rescue", string(VideoProviderModal), "provider_snapshot", "modal/test")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveStoryPlan(project.ID, validStoryPlan()); err != nil {
		t.Fatal(err)
	}
	request := GenerateRequest{Prompt: "A fox in a forest", Model: "modal/test", Duration: ProjectSceneSeconds, Resolution: ProjectResolution, AspectRatio: ProjectAspectRatio}
	oldJob, oldToken := prepareTestCallbackJob(t, app, request)
	oldJob.ProjectID, oldJob.SceneNumber = project.ID, 1
	if err := store.insertCallbackScene(oldJob); err != nil {
		t.Fatal(err)
	}
	processing := postVideoCallback(t, handler, oldJob.ID, oldToken, videoCallback{ID: oldJob.ID, Status: "processing", Sequence: 1, Progress: 20})
	if processing.Code != http.StatusNoContent {
		t.Fatalf("old attempt processing status = %d", processing.Code)
	}
	if err := store.UpdateScene(project.ID, 1, "failed", "", "simulated failure before late callback"); err != nil {
		t.Fatal(err)
	}
	if err := store.RetryScene(project.ID, 1); err != nil {
		t.Fatal(err)
	}
	newJob, newToken := prepareTestCallbackJob(t, app, request)
	newJob.ProjectID, newJob.SceneNumber = project.ID, 1
	if err := store.insertCallbackScene(newJob); err != nil {
		t.Fatal(err)
	}

	late := postVideoCallback(t, handler, oldJob.ID, oldToken, videoCallback{ID: oldJob.ID, Status: "completed", Sequence: 2, Progress: 100})
	if late.Code != http.StatusNoContent {
		t.Fatalf("late callback status = %d, body = %s", late.Code, late.Body.String())
	}
	updated, err := store.Project(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Scenes[0].ProviderGenerationID != newJob.ID || updated.Scenes[0].Status != "queued" {
		t.Fatalf("old callback changed new attempt: %+v", updated.Scenes[0])
	}
	newAttempt := postVideoCallback(t, handler, newJob.ID, newToken, videoCallback{ID: newJob.ID, Status: "processing", Sequence: 1, Progress: 25})
	if newAttempt.Code != http.StatusNoContent {
		t.Fatalf("new attempt callback status = %d, body = %s", newAttempt.Code, newAttempt.Body.String())
	}
	updated, err = store.Project(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Scenes[0].ProviderGenerationID != newJob.ID || updated.Scenes[0].Status != "processing" || updated.Scenes[0].Progress != 25 {
		t.Fatalf("new callback did not update its own attempt: %+v", updated.Scenes[0])
	}
}
