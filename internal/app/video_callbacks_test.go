package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func newVideoCallbackTestApp(t *testing.T) (*Store, *Security, *dashboardApp) {
	t.Helper()
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	app := &dashboardApp{
		store:                store,
		security:             security,
		logger:               slog.New(slog.NewTextHandler(io.Discard, nil)),
		videoCallbackBaseURL: "https://video.example.com",
	}
	return store, security, app
}

func newPersistedCallbackGeneration(t *testing.T, app *dashboardApp, configID string) (callbackJob, string) {
	t.Helper()
	job, err := app.prepareCallbackJob(GenerateRequest{Prompt: "A quiet lake at dawn", Model: "modal/wan", Duration: 6, Resolution: "480p", AspectRatio: "9:16"}, configID)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.store.insertCallbackGeneration(job, "0.12"); err != nil {
		t.Fatal(err)
	}
	token, err := app.security.DecryptSetting("video_callback."+job.ID, job.EncryptedToken)
	if err != nil {
		t.Fatal(err)
	}
	if len(token) != 43 || !validVideoCallbackToken(token) {
		t.Fatalf("callback token does not match the worker's 32-byte URL-safe format: %q", token)
	}
	return job, token
}

func postVideoCallback(t *testing.T, app *dashboardApp, id, token string, callback videoCallback) *httptest.ResponseRecorder {
	t.Helper()
	if callback.ID == "" {
		callback.ID = id
	}
	body, err := json.Marshal(callback)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, videoCallbackPath+id, strings.NewReader(string(body)))
	request.SetPathValue("id", id)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	app.videoCallback(recorder, request)
	return recorder
}

func TestValidateVideoCallbackBaseURLRequiresPublicHTTPSOrigin(t *testing.T) {
	for _, raw := range []string{
		"",
		"http://video.example.com",
		"https://video.example.com/dashboard",
		"https://video.example.com/?tenant=one",
		"https://user:password@video.example.com",
		"https://localhost",
		"https://127.0.0.1",
		"https://192.168.1.20",
		"https://internal",
	} {
		if value, err := validateVideoCallbackBaseURL(raw); err == nil {
			t.Errorf("validateVideoCallbackBaseURL(%q) = %q, want error", raw, value)
		}
	}
	value, err := validateVideoCallbackBaseURL(" https://video.example.com/ ")
	if err != nil || value != "https://video.example.com" {
		t.Fatalf("base URL=%q err=%v", value, err)
	}
	t.Setenv(videoCallbackBaseURLEnv, "")
	t.Setenv(modalCallbackBaseURLEnv, "https://legacy.example.com")
	if got := configuredVideoCallbackBaseURL(); got != "https://legacy.example.com" {
		t.Fatalf("legacy callback base URL fallback=%q", got)
	}
}

func TestVideoCallbackIsAuthenticatedTransactionalAndMonotonic(t *testing.T) {
	store, _, app := newVideoCallbackTestApp(t)
	job, token := newPersistedCallbackGeneration(t, app, "provider_config_callback")

	var encryptedToken, tokenDigest, requestJSON string
	if err := store.db.QueryRow(`SELECT encrypted_token,token_hash,request_json FROM video_callback_jobs WHERE id=?`, job.ID).Scan(&encryptedToken, &tokenDigest, &requestJSON); err != nil {
		t.Fatal(err)
	}
	if encryptedToken == token || tokenDigest == token || strings.Contains(requestJSON, token) {
		t.Fatal("callback capability was stored without encryption")
	}
	record, err := store.Generation(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	serialized, err := json.Marshal(record)
	if err != nil || strings.Contains(string(serialized), token) || strings.Contains(string(serialized), encryptedToken) {
		t.Fatalf("generation API shape exposed callback capability: %s err=%v", serialized, err)
	}

	unauthorized := postVideoCallback(t, app, job.ID, strings.Repeat("0", 64), videoCallback{Status: "completed", Sequence: 1, Progress: 100})
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized callback status=%d body=%s", unauthorized.Code, unauthorized.Body.String())
	}
	badTarget := postVideoCallback(t, app, job.ID, token, videoCallback{ID: "another_job", Status: "completed", Sequence: 1, Progress: 100})
	if badTarget.Code != http.StatusBadRequest {
		t.Fatalf("mismatched callback status=%d body=%s", badTarget.Code, badTarget.Body.String())
	}

	if _, err := store.db.Exec(`CREATE TRIGGER fail_callback_event BEFORE INSERT ON generation_events BEGIN SELECT RAISE(FAIL, 'forced callback event failure'); END`); err != nil {
		t.Fatal(err)
	}
	failedPersist := postVideoCallback(t, app, job.ID, token, videoCallback{Status: "completed", Sequence: 3, Progress: 100, CostUSD: "0.123456"})
	if failedPersist.Code != http.StatusServiceUnavailable {
		t.Fatalf("event write failure status=%d body=%s", failedPersist.Code, failedPersist.Body.String())
	}
	record, err = store.Generation(job.ID)
	callbackJob, jobErr := store.callbackJob(job.ID)
	if err != nil || jobErr != nil || record.Status != "queued" || callbackJob.Sequence != 0 {
		t.Fatalf("callback was partially applied: record=%#v callback=%#v errors=%v/%v", record, callbackJob, err, jobErr)
	}
	if _, err := store.db.Exec(`DROP TRIGGER fail_callback_event`); err != nil {
		t.Fatal(err)
	}

	processing := postVideoCallback(t, app, job.ID, token, videoCallback{Status: "processing", Sequence: 2, Progress: 45, CostUSD: "0.123456"})
	if processing.Code != http.StatusNoContent {
		t.Fatalf("processing callback status=%d body=%s", processing.Code, processing.Body.String())
	}
	stale := postVideoCallback(t, app, job.ID, token, videoCallback{Status: "processing", Sequence: 1, Progress: 90})
	if stale.Code != http.StatusNoContent {
		t.Fatalf("stale callback status=%d body=%s", stale.Code, stale.Body.String())
	}
	completed := postVideoCallback(t, app, job.ID, token, videoCallback{Status: "completed", Sequence: 3, Progress: 95})
	if completed.Code != http.StatusNoContent {
		t.Fatalf("completion callback status=%d body=%s", completed.Code, completed.Body.String())
	}
	lateFailure := postVideoCallback(t, app, job.ID, token, videoCallback{Status: "failed", Sequence: 4, ErrorCode: "generation_failed"})
	if lateFailure.Code != http.StatusNoContent {
		t.Fatalf("late failure callback status=%d body=%s", lateFailure.Code, lateFailure.Body.String())
	}
	record, err = store.Generation(job.ID)
	callbackJob, jobErr = store.callbackJob(job.ID)
	if err != nil || jobErr != nil || record.Status != "downloading" || record.Progress != 100 || record.CostUSD != "0.123456" || record.Error != "" || callbackJob.State != "completed" || callbackJob.Sequence != 3 {
		t.Fatalf("callback state=%#v job=%#v errors=%v/%v", record, callbackJob, err, jobErr)
	}
}

func TestVideoCallbackFailureUsesSafeReadableError(t *testing.T) {
	store, _, app := newVideoCallbackTestApp(t)
	job, token := newPersistedCallbackGeneration(t, app, "provider_config_failure")
	response := postVideoCallback(t, app, job.ID, token, videoCallback{Status: "failed", Sequence: 1, ErrorCode: "out_of_memory", Stage: "gpu", CostUSD: "0.01"})
	if response.Code != http.StatusNoContent {
		t.Fatalf("failure callback status=%d body=%s", response.Code, response.Body.String())
	}
	record, err := store.Generation(job.ID)
	if err != nil || record.Status != "failed" || !strings.Contains(record.Error, "GPU ran out of memory") || strings.Contains(record.Error, "out_of_memory") {
		t.Fatalf("failure record=%#v err=%v", record, err)
	}
	if len(record.Events) < 2 || record.Events[len(record.Events)-1].Message != record.Error {
		t.Fatalf("callback failure event missing: %#v", record.Events)
	}
}

func TestRejectedInitialCallbackFailureIsActionableAndSafe(t *testing.T) {
	message := callbackFailure("callback_delivery_rejected")
	for _, want := range []string{"VIDEO_CALLBACK_BASE_URL", "deployed callback receiver", "edge access rules"} {
		if !strings.Contains(message, want) {
			t.Errorf("callback rejection guidance %q does not contain %q", message, want)
		}
	}
	if strings.Contains(message, "Bearer ") || strings.Contains(message, "token") {
		t.Fatalf("callback rejection guidance exposed a capability: %q", message)
	}
}

type callbackSceneProvider struct {
	*mockProvider
	submitCalls int
}

func (p *callbackSceneProvider) SubmitVideo(context.Context, GenerateRequest, VideoCallbackSubmission) (*Generation, error) {
	p.submitCalls++
	return &Generation{ID: "unexpected_dispatch", Status: "queued"}, nil
}

func TestModalProjectScenePersistsCallbackJobBeforeDispatch(t *testing.T) {
	store, _, app := newVideoCallbackTestApp(t)
	project, err := store.InsertProjectWithProviderConfig("callback scene", string(VideoProviderModal), "provider_config_scene_submit", "modal/wan")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveStoryPlan(project.ID, validStoryPlan()); err != nil {
		t.Fatal(err)
	}
	project, err = store.Project(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	provider := &callbackSceneProvider{mockProvider: &mockProvider{}}
	processor := &Processor{app: app}
	processor.submitScene(context.Background(), provider, project, project.Scenes[0])
	if provider.generateCalls != 0 || provider.submitCalls != 0 {
		t.Fatalf("scene submission dispatched before persistence: generate=%d submit=%d", provider.generateCalls, provider.submitCalls)
	}
	project, err = store.Project(project.ID)
	if err != nil || project.Scenes[0].Status != "queued" || project.Scenes[0].ProviderGenerationID == "" {
		t.Fatalf("scene was not queued with a client id: %#v err=%v", project.Scenes[0], err)
	}
	job, err := store.callbackJob(project.Scenes[0].ProviderGenerationID)
	if err != nil || job.ProjectID != project.ID || job.SceneNumber != 1 || job.State != "submitting" {
		t.Fatalf("persisted scene callback job=%#v err=%v", job, err)
	}
}

func TestVideoCallbackJobAndEncryptedTokenSurviveRestart(t *testing.T) {
	store, _, app := newVideoCallbackTestApp(t)
	job, token := newPersistedCallbackGeneration(t, app, "provider_config_restart")
	dataDir := store.dataDir
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := OpenStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	restartedSecurity, err := NewSecurity(restarted)
	if err != nil {
		t.Fatal(err)
	}
	restartedJob, err := restarted.callbackJob(job.ID)
	if err != nil || restartedJob.State != "submitting" || restartedJob.ID != job.ID {
		t.Fatalf("restarted callback job=%#v err=%v", restartedJob, err)
	}
	restartedToken, err := restartedSecurity.DecryptSetting("video_callback."+job.ID, restartedJob.EncryptedToken)
	if err != nil || restartedToken != token {
		t.Fatalf("restarted callback token=%q err=%v", restartedToken, err)
	}
	record, err := restarted.Generation(job.ID)
	if err != nil || record.Status != "queued" {
		t.Fatalf("restarted generation=%#v err=%v", record, err)
	}
}

func TestVideoCallbackSceneAttemptCannotUpdateRetry(t *testing.T) {
	store, _, app := newVideoCallbackTestApp(t)
	project, err := store.InsertProjectWithProviderConfig("callback story", string(VideoProviderModal), "provider_config_scene", "modal/wan")
	if err != nil || store.SaveStoryPlan(project.ID, validStoryPlan()) != nil {
		t.Fatalf("create project=%#v err=%v", project, err)
	}
	job, err := app.prepareCallbackJob(GenerateRequest{Prompt: "scene one", Model: "modal/wan", Duration: 6, Resolution: "480p", AspectRatio: "9:16"}, project.ProviderConfigID)
	if err != nil {
		t.Fatal(err)
	}
	job.ProjectID, job.SceneNumber = project.ID, 1
	if err := store.insertCallbackScene(job); err != nil {
		t.Fatal(err)
	}
	oldToken, err := app.security.DecryptSetting("video_callback."+job.ID, job.EncryptedToken)
	if err != nil {
		t.Fatal(err)
	}
	failed := postVideoCallback(t, app, job.ID, oldToken, videoCallback{Status: "failed", Sequence: 1, ErrorCode: "generation_failed"})
	if failed.Code != http.StatusNoContent {
		t.Fatalf("scene failure callback status=%d body=%s", failed.Code, failed.Body.String())
	}
	if _, err := store.db.Exec(`UPDATE project_scenes SET status='completed' WHERE project_id=? AND scene_number BETWEEN 2 AND 5`, project.ID); err != nil {
		t.Fatal(err)
	}
	processor := NewProcessor(store, app.security, app.logger)
	project, err = store.Project(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	processor.processProjectScenes(context.Background(), nil, project)
	rolledUp, err := store.Project(project.ID)
	if err != nil || rolledUp.Status != "failed" || !strings.Contains(rolledUp.Error, rolledUp.Scenes[0].Error) || !strings.Contains(rolledUp.Error, "retry this scene") {
		t.Fatalf("scene error did not roll up: project=%#v err=%v", rolledUp, err)
	}
	if err := store.RetryScene(project.ID, 1); err != nil {
		t.Fatal(err)
	}

	newJob, err := app.prepareCallbackJob(GenerateRequest{Prompt: "scene one retry", Model: "modal/wan", Duration: 6, Resolution: "480p", AspectRatio: "9:16"}, project.ProviderConfigID)
	if err != nil {
		t.Fatal(err)
	}
	newJob.ProjectID, newJob.SceneNumber = project.ID, 1
	if err := store.insertCallbackScene(newJob); err != nil {
		t.Fatal(err)
	}
	lateOldAttempt := postVideoCallback(t, app, job.ID, oldToken, videoCallback{Status: "completed", Sequence: 2, Progress: 100})
	if lateOldAttempt.Code != http.StatusNoContent {
		t.Fatalf("late old callback status=%d body=%s", lateOldAttempt.Code, lateOldAttempt.Body.String())
	}
	loaded, err := store.Project(project.ID)
	if err != nil || loaded.Scenes[0].ProviderGenerationID != newJob.ID || loaded.Scenes[0].Status != "queued" {
		t.Fatalf("old callback changed retry: scene=%#v err=%v", loaded.Scenes[0], err)
	}
}

func TestProcessorSkipsPollingCallbackJobsAndKeepsLegacyPolling(t *testing.T) {
	store, security, app := newVideoCallbackTestApp(t)
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		switch {
		case strings.HasPrefix(r.URL.Path, "/videos/"):
			_, _ = w.Write([]byte(`{"id":"legacy_job","status":"queued"}`))
		case r.URL.Path == "/videos":
			var submitted VideoCallbackSubmission
			if err := json.NewDecoder(r.Body).Decode(&submitted); err != nil {
				t.Error(err)
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"id": submitted.JobID, "status": "queued"})
		default:
			t.Errorf("unexpected provider request %s", r.URL.Path)
		}
	}))
	defer server.Close()

	modalConfigID := "provider_config_callback_poll"
	modalCiphertext, err := security.EncryptSetting("video_provider_config."+modalConfigID, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertProviderConfig(ProviderConfig{ID: modalConfigID, Provider: string(VideoProviderModal), BaseURL: server.URL, EncryptedAPIKey: modalCiphertext}); err != nil {
		t.Fatal(err)
	}
	callback, _ := newPersistedCallbackGeneration(t, app, modalConfigID)
	if _, err := store.db.Exec(`UPDATE video_callback_jobs SET state='accepted',deadline=? WHERE id=?`, time.Now().Add(time.Hour).Unix(), callback.ID); err != nil {
		t.Fatal(err)
	}
	processor := NewProcessor(store, security, app.logger)
	processor.process(context.Background())
	if requests != 0 {
		t.Fatalf("callback job made %d provider requests; it must wait for callback", requests)
	}

	openRouterConfigID := "provider_config_legacy_poll"
	openRouterCiphertext, err := security.EncryptSetting("video_provider_config."+openRouterConfigID, "legacy-test-key")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertProviderConfig(ProviderConfig{ID: openRouterConfigID, Provider: string(VideoProviderOpenRouter), BaseURL: server.URL, EncryptedAPIKey: openRouterCiphertext}); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertGeneration(GenerationRecord{ID: "legacy_job", VideoProvider: string(VideoProviderOpenRouter), ProviderConfigID: openRouterConfigID, Prompt: "legacy", Model: "test-model", Status: "queued"}); err != nil {
		t.Fatal(err)
	}
	processor.process(context.Background())
	if requests != 1 {
		t.Fatalf("legacy generation made %d provider requests; expected normal polling", requests)
	}
}

func TestCallbackRecoveryScheduleIsSparseAndBounded(t *testing.T) {
	now := time.Date(2026, time.October, 2, 12, 0, 0, 0, time.UTC)
	deadline := now.Add(8 * time.Hour).Unix()
	want := []time.Duration{15 * time.Minute, time.Hour, 3 * time.Hour, 6 * time.Hour}
	base := now
	for attempts, delay := range want {
		got := callbackRecoveryAt(base, attempts, deadline)
		if got != now.Add(delay).Unix() {
			t.Errorf("attempt %d recovery at %s, want %s", attempts, time.Unix(got, 0), now.Add(delay))
		}
		base = time.Unix(got, 0)
	}
	if got := callbackRecoveryAt(now, callbackRecoveryMaxAttempts, deadline); got != deadline {
		t.Fatalf("recovery after maximum attempts=%s, want deadline %s", time.Unix(got, 0), time.Unix(deadline, 0))
	}
}

func TestCallbackRecoveryClosesCrashGapAndKeepsCompletedMedia(t *testing.T) {
	store, security, app := newVideoCallbackTestApp(t)
	var job callbackJob
	job, _ = newPersistedCallbackGeneration(t, app, "provider_config_recovery")
	modalKey := "immutable-modal-key"
	ciphertext, err := security.EncryptSetting("video_provider_config.provider_config_recovery", modalKey)
	if err != nil {
		t.Fatal(err)
	}
	var content = []byte("recovered-terminal-video")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+modalKey {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/videos/"+job.ID:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"` + job.ID + `","status":"completed","model":"modal/wan","progress":100,"callback_delivery":{"state":"rejected","status_code":403,"attempts":1}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/videos/"+job.ID+"/content":
			_, _ = w.Write(content)
		default:
			t.Errorf("unexpected Modal recovery request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	if _, err := store.InsertProviderConfig(ProviderConfig{ID: "provider_config_recovery", Provider: string(VideoProviderModal), BaseURL: server.URL, EncryptedAPIKey: ciphertext}); err != nil {
		t.Fatal(err)
	}
	// This is the durable state after ApplyModalGenerationRecovery committed,
	// but before the separate callback-job terminal update could be written.
	if _, err := store.db.Exec(`UPDATE generations SET status='downloading',progress=100 WHERE id=?`, job.GenerationID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE video_callback_jobs SET state='accepted',deadline=?,recovery_at=?,recovery_attempts=0 WHERE id=?`, time.Now().Add(time.Hour).Unix(), time.Now().Add(-time.Second).Unix(), job.ID); err != nil {
		t.Fatal(err)
	}
	job, err = store.callbackJob(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	processor := NewProcessor(store, security, app.logger)
	processor.app.videoCallbackBaseURL = "https://video.example.com"
	processor.recoverCallbackJob(context.Background(), job)

	deadline := time.Now().Add(3 * time.Second)
	var record GenerationRecord
	for time.Now().Before(deadline) {
		record, err = store.Generation(job.GenerationID)
		if err != nil {
			t.Fatal(err)
		}
		if record.Status == "completed" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if record.Status != "completed" || !record.VideoReady {
		t.Fatalf("recovered video record=%#v", record)
	}
	video, err := os.ReadFile(record.VideoPath)
	if err != nil || string(video) != string(content) {
		t.Fatalf("recovered media=%q err=%v", video, err)
	}
	callback, err := store.callbackJob(job.ID)
	if err != nil || callback.State != "completed" || callback.RecoveryAttempts != 1 {
		t.Fatalf("callback recovery state=%#v err=%v", callback, err)
	}
	if callback.DeliveryState != "rejected" || callback.DeliveryCode != "http_403" || callback.DeliveryStatus != http.StatusForbidden {
		t.Fatalf("callback rejection diagnostic=%#v", callback)
	}
	foundDiagnostic := false
	for _, event := range record.Events {
		if event.Stage == "callback_delivery" && strings.Contains(event.Message, "HTTP 403") {
			foundDiagnostic = true
		}
	}
	if !foundDiagnostic {
		t.Fatalf("safe callback rejection event missing: %#v", record.Events)
	}
}

func TestCallbackRecoveryCannotFinishReplacedSceneAttempt(t *testing.T) {
	store, security, app := newVideoCallbackTestApp(t)
	const configID = "provider_config_stale_scene_recovery"
	const modalKey = "immutable-scene-modal-key"
	project, err := store.InsertProjectWithProviderConfig("stale callback recovery", string(VideoProviderModal), configID, "modal/wan")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveStoryPlan(project.ID, validStoryPlan()); err != nil {
		t.Fatal(err)
	}
	job, err := app.prepareCallbackJob(GenerateRequest{Prompt: "scene one", Model: "modal/wan", Duration: 6, Resolution: "480p", AspectRatio: "9:16"}, configID)
	if err != nil {
		t.Fatal(err)
	}
	job.ProjectID, job.SceneNumber = project.ID, 1
	if err := store.insertCallbackScene(job); err != nil {
		t.Fatal(err)
	}
	ciphertext, err := security.EncryptSetting("video_provider_config."+configID, modalKey)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+modalKey {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet || r.URL.Path != "/videos/"+job.ID {
			t.Errorf("unexpected Modal recovery request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"id":"` + job.ID + `","status":"completed","model":"modal/wan","progress":100}`))
	}))
	defer server.Close()
	if _, err := store.InsertProviderConfig(ProviderConfig{ID: configID, Provider: string(VideoProviderModal), BaseURL: server.URL, EncryptedAPIKey: ciphertext}); err != nil {
		t.Fatal(err)
	}
	const replacementID = "gen_replacement_scene_attempt"
	if _, err := store.db.Exec(`UPDATE project_scenes SET provider_generation_id=?,status='queued' WHERE project_id=? AND scene_number=1`, replacementID, project.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE video_callback_jobs SET state='accepted',deadline=?,recovery_at=? WHERE id=?`, time.Now().Add(time.Hour).Unix(), time.Now().Add(-time.Second).Unix(), job.ID); err != nil {
		t.Fatal(err)
	}
	job, err = store.callbackJob(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	processor := NewProcessor(store, security, app.logger)
	processor.recoverCallbackJob(context.Background(), job)

	loaded, err := store.projectForWorker(context.Background(), project.ID)
	if err != nil || loaded.Scenes[0].ProviderGenerationID != replacementID || loaded.Scenes[0].Status != "queued" {
		t.Fatalf("replacement scene was changed by stale recovery: scene=%#v err=%v", loaded.Scenes[0], err)
	}
	oldCallback, err := store.callbackJob(job.ID)
	if err != nil || oldCallback.State != "accepted" || oldCallback.RecoveryAttempts != 1 {
		t.Fatalf("stale callback attempt was marked terminal: job=%#v err=%v", oldCallback, err)
	}
}

func TestModalSubmitVideoRequiresMatchingClientIDAndNoRedirect(t *testing.T) {
	redirectTargetCalls := 0
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectTargetCalls++
		_, _ = w.Write([]byte(`{"id":"callback_job","status":"queued"}`))
	}))
	defer redirectTarget.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, redirectTarget.URL+"/videos", http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()
	callback := VideoCallbackSubmission{JobID: "callback_job", CallbackURL: "https://video.example.com/api/video-callbacks/callback_job", CallbackToken: strings.Repeat("a", 64)}
	client := NewModalVideoClient(redirector.URL, "modal-test-key")
	if _, err := client.SubmitVideo(context.Background(), GenerateRequest{Prompt: "test", Model: "modal/wan"}, callback); !errors.Is(err, errCallbackSubmissionRedirect) {
		t.Fatalf("redirect submission error=%v", err)
	}
	if redirectTargetCalls != 0 {
		t.Fatal("callback submission followed a redirect")
	}

	mismatched := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"some_other_job","status":"queued"}`))
	}))
	defer mismatched.Close()
	client = NewModalVideoClient(mismatched.URL, "modal-test-key")
	if _, err := client.SubmitVideo(context.Background(), GenerateRequest{Prompt: "test", Model: "modal/wan"}, callback); !errors.Is(err, errCallbackWorkerUnsupported) {
		t.Fatalf("mismatched acknowledgment error=%v", err)
	}

	invalidJSON := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("accepted but not a job response"))
	}))
	defer invalidJSON.Close()
	client = NewModalVideoClient(invalidJSON.URL, "modal-test-key")
	if _, err := client.SubmitVideo(context.Background(), GenerateRequest{Prompt: "test", Model: "modal/wan"}, callback); !errors.Is(err, errCallbackWorkerUnsupported) {
		t.Fatalf("invalid acknowledgment error=%v", err)
	}
}

func TestVideoDownloadRetriesEndWithClearFailures(t *testing.T) {
	store := newTestStore(t)
	if err := store.InsertGeneration(GenerationRecord{ID: "download_retries", VideoProvider: string(VideoProviderModal), Prompt: "test", Model: "modal/wan", Status: "downloading"}); err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= maxVideoDownloadAttempts; attempt++ {
		if err := store.ScheduleDownloadRetry("download_retries"); err != nil {
			t.Fatal(err)
		}
	}
	record, err := store.Generation("download_retries")
	if err != nil || record.Status != "failed" || record.DownloadAttempts != maxVideoDownloadAttempts || record.Error != videoDownloadFinalFailure {
		t.Fatalf("generation download failure=%#v err=%v", record, err)
	}

	project, err := store.InsertProjectForProvider("download project", string(VideoProviderModal), "modal/wan")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveStoryPlan(project.ID, validStoryPlan()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE project_scenes SET status='downloading',provider_generation_id='scene_download_job' WHERE project_id=? AND scene_number=1`, project.ID); err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= maxVideoDownloadAttempts; attempt++ {
		if err := store.ScheduleSceneDownloadRetry(project.ID, 1); err != nil {
			t.Fatal(err)
		}
	}
	project, err = store.Project(project.ID)
	if err != nil || project.Scenes[0].Status != "failed" || !strings.Contains(project.Scenes[0].Error, "could not be downloaded after 5 attempts") {
		t.Fatalf("scene download failure=%#v err=%v", project.Scenes[0], err)
	}
}

func TestVideoCallbackMissingJobIsNotConfusedWithDatabaseFailure(t *testing.T) {
	store, _, app := newVideoCallbackTestApp(t)
	request := httptest.NewRequest(http.MethodPost, videoCallbackPath+"missing_job", strings.NewReader(`{"id":"missing_job","status":"completed","sequence":1,"progress":100}`))
	request.SetPathValue("id", "missing_job")
	request.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 64))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	app.videoCallback(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unknown callback job status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if _, err := store.callbackJob("missing_job"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing job lookup err=%v", err)
	}
}
