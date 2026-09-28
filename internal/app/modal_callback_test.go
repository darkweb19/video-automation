package app

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func callbackTestApp(store *Store) *dashboardApp {
	return &dashboardApp{store: store, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func postModalCallback(t *testing.T, app *dashboardApp, token, id, status string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(modalCompletionCallback{ID: id, Status: status, Model: "modal/wan", CostUSD: "0.123456", Error: "Authorization: Bearer should-not-persist"})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, modalCallbackPath, strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Modal-Callback-Token", token)
	app.modalCompletionCallback(recorder, request)
	return recorder
}

func TestModalCallbackCompletionIsAuthenticatedAndMonotonic(t *testing.T) {
	store := newTestStore(t)
	token, err := newModalCallbackToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RegisterModalCallbackToken(token, "", 0); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertGenerationWithModalCallback(GenerationRecord{ID: "callback_single", VideoProvider: string(VideoProviderModal), Prompt: "test", Model: "modal/wan", Status: "queued"}, token); err != nil {
		t.Fatal(err)
	}
	app := callbackTestApp(store)
	completed := postModalCallback(t, app, token, "callback_single", "completed")
	if completed.Code != http.StatusNoContent {
		t.Fatalf("completion status=%d body=%s", completed.Code, completed.Body.String())
	}
	record, err := store.Generation("callback_single")
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != "downloading" || record.Progress != 100 || record.CostUSD != "0.123456" {
		t.Fatalf("completion record=%#v", record)
	}
	serialized, _ := json.Marshal(record)
	if strings.Contains(string(serialized), token) || strings.Contains(string(serialized), "should-not-persist") {
		t.Fatalf("callback secret/error leaked: %s", serialized)
	}
	var storedHash string
	if err := store.db.QueryRow(`SELECT token_hash FROM modal_callback_tokens WHERE generation_id=?`, "callback_single").Scan(&storedHash); err != nil || storedHash == token || strings.Contains(storedHash, token) {
		t.Fatalf("raw callback token persisted hash=%q err=%v", storedHash, err)
	}

	// A delayed conflicting failure cannot undo the accepted completion.
	lateFailure := postModalCallback(t, app, token, "callback_single", "failed")
	if lateFailure.Code != http.StatusNoContent {
		t.Fatalf("late failure status=%d", lateFailure.Code)
	}
	record, err = store.Generation("callback_single")
	if err != nil || record.Status != "downloading" {
		t.Fatalf("out-of-order callback changed record=%#v err=%v", record, err)
	}

	forged := postModalCallback(t, app, strings.Repeat("x", 43), "callback_single", "failed")
	if forged.Code != http.StatusUnauthorized {
		t.Fatalf("forged callback status=%d body=%s", forged.Code, forged.Body.String())
	}
}

func TestModalCallbackPublishesSafeLiveGenerationEvent(t *testing.T) {
	store := newTestStore(t)
	token, err := newModalCallbackToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RegisterModalCallbackToken(token, "", 0); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertGenerationWithModalCallback(GenerationRecord{ID: "callback_event", VideoProvider: string(VideoProviderModal), Prompt: "test", Model: "modal/wan", Status: "queued"}, token); err != nil {
		t.Fatal(err)
	}
	channel, unsubscribe := store.events.subscribe()
	defer unsubscribe()
	if response := postModalCallback(t, callbackTestApp(store), token, "callback_event", "completed"); response.Code != http.StatusNoContent {
		t.Fatalf("callback status=%d", response.Code)
	}
	select {
	case event := <-channel:
		if event.name != "generation" || strings.Contains(string(event.data), token) {
			t.Fatalf("unsafe/unexpected event=%q data=%s", event.name, event.data)
		}
		var payload struct {
			ID         string           `json:"id"`
			Generation GenerationRecord `json:"generation"`
		}
		if err := json.Unmarshal(event.data, &payload); err != nil || payload.ID != "callback_event" || payload.Generation.Status != "downloading" {
			t.Fatalf("event payload=%s err=%v", event.data, err)
		}
	case <-time.After(time.Second):
		t.Fatal("expected live generation event")
	}
}

func TestModalCallbackRetriesRaceBeforeGenerationInsert(t *testing.T) {
	store := newTestStore(t)
	token, err := newModalCallbackToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RegisterModalCallbackToken(token, "", 0); err != nil {
		t.Fatal(err)
	}
	app := callbackTestApp(store)
	early := postModalCallback(t, app, token, "callback_race", "completed")
	if early.Code != http.StatusServiceUnavailable || early.Header().Get("Retry-After") != "1" {
		t.Fatalf("early callback status=%d retry=%q body=%s", early.Code, early.Header().Get("Retry-After"), early.Body.String())
	}
	if err := store.InsertGenerationWithModalCallback(GenerationRecord{ID: "callback_race", VideoProvider: string(VideoProviderModal), Prompt: "test", Model: "modal/wan", Status: "queued"}, token); err != nil {
		t.Fatal(err)
	}
	accepted := postModalCallback(t, app, token, "callback_race", "completed")
	if accepted.Code != http.StatusNoContent {
		t.Fatalf("retried callback status=%d body=%s", accepted.Code, accepted.Body.String())
	}
}

func TestModalSceneCallbackAndRetryRejectsOldToken(t *testing.T) {
	store := newTestStore(t)
	project, err := store.InsertProjectForProvider("topic", string(VideoProviderModal), "modal/wan")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveStoryPlan(project.ID, validStoryPlan()); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkSceneSubmitting(project.ID, 1); err != nil {
		t.Fatal(err)
	}
	token, err := newModalCallbackToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RegisterModalCallbackToken(token, project.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSceneGenerationWithModalCallback(project.ID, 1, Generation{ID: "callback_scene", Status: "queued", Model: "modal/wan"}, token); err != nil {
		t.Fatal(err)
	}
	app := callbackTestApp(store)
	failed := postModalCallback(t, app, token, "callback_scene", "failed")
	if failed.Code != http.StatusNoContent {
		t.Fatalf("scene callback status=%d body=%s", failed.Code, failed.Body.String())
	}
	loaded, err := store.Project(project.ID)
	if err != nil || loaded.Scenes[0].Status != "failed" {
		t.Fatalf("scene callback project=%#v err=%v", loaded, err)
	}
	if err := store.RetryScene(project.ID, 1); err != nil {
		t.Fatal(err)
	}
	old := postModalCallback(t, app, token, "callback_scene", "completed")
	if old.Code != http.StatusUnauthorized {
		t.Fatalf("old scene token status=%d body=%s", old.Code, old.Body.String())
	}
}

func TestModalRecoveryUpdateCannotRegressCallbackState(t *testing.T) {
	store := newTestStore(t)
	token, err := newModalCallbackToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RegisterModalCallbackToken(token, "", 0); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertGenerationWithModalCallback(GenerationRecord{ID: "callback_cas", VideoProvider: string(VideoProviderModal), Prompt: "test", Model: "modal/wan", Status: "queued"}, token); err != nil {
		t.Fatal(err)
	}
	app := callbackTestApp(store)
	if result := postModalCallback(t, app, token, "callback_cas", "completed"); result.Code != http.StatusNoContent {
		t.Fatalf("completion status=%d", result.Code)
	}
	processing := 25
	updated, err := store.ApplyModalGenerationRecovery("callback_cas", Generation{ID: "callback_cas", Status: "processing", Progress: &processing})
	if err != nil || updated {
		t.Fatalf("stale recovery updated=%v err=%v", updated, err)
	}
	record, err := store.Generation("callback_cas")
	if err != nil || record.Status != "downloading" {
		t.Fatalf("recovery regressed record=%#v err=%v", record, err)
	}

	// The recovery timestamp is persisted with the generation, so an app
	// restart does not turn Modal into a five-second poller.
	if _, err := store.db.Exec(`UPDATE generations SET status='queued',modal_callback_recovery_at=? WHERE id=?`, time.Now().Add(time.Hour).Unix(), "callback_cas"); err != nil {
		t.Fatal(err)
	}
	restarted, err := store.Generation("callback_cas")
	if err != nil || restarted.ModalCallbackRecoveryAt <= time.Now().Unix() {
		t.Fatalf("recovery schedule did not persist: %#v err=%v", restarted, err)
	}
}

func TestModalCallbackURLRequiresExplicitHTTPSPublicBaseURL(t *testing.T) {
	app := &dashboardApp{}
	if _, err := app.modalCallbackURL(); err == nil {
		t.Fatal("missing public callback URL must fail closed")
	}
	app.callbackBaseURL = "http://localhost:8080"
	if _, err := app.modalCallbackURL(); err == nil {
		t.Fatal("non-HTTPS callback URL must fail closed")
	}
	app.callbackBaseURL = "https://video.example.com/dashboard"
	url, err := app.modalCallbackURL()
	if err != nil || url != "https://video.example.com/dashboard/api/provider-callbacks/modal" {
		t.Fatalf("callback URL=%q err=%v", url, err)
	}
}
