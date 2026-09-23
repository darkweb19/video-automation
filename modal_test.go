package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestModalVideoClientUsesNormalizedAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer modal-test-key" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/videos/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"modal/wan2.2-lightning-a14b","name":"Wan","supported_durations":[1,6,15],"supported_resolutions":["480p","720p"],"supported_aspect_ratios":["9:16","16:9"],"generate_audio":false}]}`))
		case "/videos":
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), `"resolution":"480p"`) {
				t.Fatalf("request = %s", body)
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"id":"gen_modal_1","status":"pending","model":"modal/wan2.2-lightning-a14b"}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client := NewModalVideoClient(server.URL, "modal-test-key")
	models, err := client.ListVideoModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].Provider != string(VideoProviderModal) || !containsString(models[0].Resolutions, "720p") || models[0].Audio == nil || *models[0].Audio {
		t.Fatalf("models = %#v", models)
	}
	generation, err := client.GenerateVideo(context.Background(), GenerateRequest{Prompt: "sunrise", Model: models[0].ID, Duration: 6, Resolution: "480p", AspectRatio: "9:16"})
	if err != nil {
		t.Fatal(err)
	}
	if generation.ID != "gen_modal_1" || generation.Status != "queued" {
		t.Fatalf("generation = %#v", generation)
	}
}

func TestProviderSnapshotsPersist(t *testing.T) {
	store := newTestStore(t)
	if err := store.InsertGeneration(GenerationRecord{ID: "modal_job", VideoProvider: string(VideoProviderModal), Prompt: "test", Model: "modal/wan", Status: "queued"}); err != nil {
		t.Fatal(err)
	}
	record, err := store.Generation("modal_job")
	if err != nil || record.VideoProvider != string(VideoProviderModal) {
		t.Fatalf("record = %#v, err=%v", record, err)
	}
	project, err := store.InsertProjectForProvider("test", string(VideoProviderModal), "modal/wan")
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Project(project.ID)
	if err != nil || loaded.VideoProvider != string(VideoProviderModal) {
		t.Fatalf("project = %#v, err=%v", loaded, err)
	}
}

func TestEncryptedSettingsAreBoundToTheirKey(t *testing.T) {
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := security.EncryptSetting(modalVideoAPIKeySetting, "modal-secret-value")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := security.DecryptSetting(apiKeySetting, ciphertext); err == nil {
		t.Fatal("ciphertext must not decrypt under another setting name")
	}
	plain, err := security.DecryptSetting(modalVideoAPIKeySetting, ciphertext)
	if err != nil || plain != "modal-secret-value" {
		t.Fatalf("plain=%q err=%v", plain, err)
	}
}

func TestSettingsDoNotExposeSavedKeyCharacters(t *testing.T) {
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{apiKeySetting: "openrouter-visible-secret", modalVideoAPIKeySetting: "modal-visible-secret"} {
		ciphertext, err := security.EncryptSetting(key, value)
		if err != nil || store.SetSetting(key, ciphertext) != nil {
			t.Fatal("save encrypted setting")
		}
	}
	if err := store.SetSetting(modalVideoBaseURLSetting, "https://modal.example/api/v1"); err != nil {
		t.Fatal(err)
	}
	app := &dashboardApp{store: store, security: security}
	recorder := httptest.NewRecorder()
	app.settings(recorder, httptest.NewRequest(http.MethodGet, "/api/settings", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "visible-secret") || strings.Contains(recorder.Body.String(), "masked") {
		t.Fatalf("settings exposed secret material: %s", recorder.Body.String())
	}
	var response map[string]any
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response["openrouter_api_key_configured"] != true || response["modal_video_api_key_configured"] != true {
		t.Fatalf("response=%#v", response)
	}
}

func TestProviderConfigSnapshotSurvivesAccountChangesAndRestart(t *testing.T) {
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer original-account-key" {
			t.Fatalf("authorization=%q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()
	configID := "provider_config_restart_test"
	ciphertext, err := security.EncryptSetting("video_provider_config."+configID, "original-account-key")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertProviderConfig(ProviderConfig{ID: configID, Provider: string(VideoProviderModal), BaseURL: server.URL, EncryptedAPIKey: ciphertext}); err != nil {
		t.Fatal(err)
	}
	changed, err := security.EncryptSetting(modalVideoAPIKeySetting, "new-account-key")
	if err != nil || store.SetSetting(modalVideoAPIKeySetting, changed) != nil || store.SetSetting(modalVideoBaseURLSetting, "https://new-account.example/api/v1") != nil {
		t.Fatal("change mutable account settings")
	}
	// A fresh app instance simulates a process restart; it must use the saved
	// snapshot, not mutable settings.
	restarted := &dashboardApp{store: store, security: security}
	service, err := restarted.videoProviderForSnapshot(configID, VideoProviderModal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ListVideoModels(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestOpenRouterProviderConfigSnapshotSurvivesKeyChange(t *testing.T) {
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer original-openrouter-key" {
			t.Fatalf("authorization=%q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()
	configID := "provider_config_openrouter_restart_test"
	ciphertext, err := security.EncryptSetting("video_provider_config."+configID, "original-openrouter-key")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertProviderConfig(ProviderConfig{ID: configID, Provider: string(VideoProviderOpenRouter), BaseURL: server.URL, EncryptedAPIKey: ciphertext}); err != nil {
		t.Fatal(err)
	}
	changed, err := security.EncryptSetting(apiKeySetting, "new-openrouter-key")
	if err != nil || store.SetSetting(apiKeySetting, changed) != nil {
		t.Fatal("change mutable OpenRouter setting")
	}
	restarted := &dashboardApp{store: store, security: security}
	service, err := restarted.videoProviderForSnapshot(configID, VideoProviderOpenRouter)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ListVideoModels(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestProcessorBackfillsLegacyPendingProviderSnapshots(t *testing.T) {
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	key, err := security.EncryptSetting(apiKeySetting, "legacy-openrouter-key")
	if err != nil || store.SetSetting(apiKeySetting, key) != nil {
		t.Fatal("save legacy provider configuration")
	}
	if err := store.InsertGeneration(GenerationRecord{ID: "legacy_pending", VideoProvider: string(VideoProviderOpenRouter), Prompt: "test", Model: "provider/model", Status: "queued"}); err != nil {
		t.Fatal(err)
	}
	_ = NewProcessor(store, security, nil)
	record, err := store.Generation("legacy_pending")
	if err != nil || record.ProviderConfigID == "" {
		t.Fatalf("record=%#v err=%v", record, err)
	}
	if _, err := store.ProviderConfig(record.ProviderConfigID); err != nil {
		t.Fatal(err)
	}
}

func providerConfigCount(t *testing.T, store *Store) int {
	t.Helper()
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM video_provider_configs`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestRejectedSubmissionsDoNotRetainProviderSnapshots(t *testing.T) {
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	key, err := security.EncryptSetting(apiKeySetting, "test-openrouter-key")
	if err != nil || store.SetSetting(apiKeySetting, key) != nil {
		t.Fatal("save API key")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/videos/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"provider/model","name":"Model","supported_durations":[6],"supported_resolutions":["480p"],"supported_aspect_ratios":["9:16"]}]}`))
		case "/videos":
			w.WriteHeader(http.StatusBadGateway)
		default:
			t.Fatalf("path=%s", r.URL.Path)
		}
	}))
	defer server.Close()
	app := &dashboardApp{store: store, security: security, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), baseURL: server.URL}
	for _, body := range []string{
		`{"prompt":"test","model":"missing"}`,
		`{"prompt":"test","model":"provider/model"}`,
	} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/generate", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		app.generate(recorder, request)
		if providerConfigCount(t, store) != 0 {
			t.Fatalf("snapshot leaked after status=%d", recorder.Code)
		}
	}
}

func TestProviderConfigGarbageCollectionKeepsReferencedSnapshots(t *testing.T) {
	store := newTestStore(t)
	configs := []ProviderConfig{{ID: "orphan_config", Provider: string(VideoProviderOpenRouter), EncryptedAPIKey: "ciphertext"}, {ID: "referenced_config", Provider: string(VideoProviderOpenRouter), EncryptedAPIKey: "ciphertext"}}
	for _, config := range configs {
		if _, err := store.InsertProviderConfig(config); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.InsertGeneration(GenerationRecord{ID: "completed_reference", VideoProvider: string(VideoProviderOpenRouter), ProviderConfigID: "referenced_config", Prompt: "test", Model: "model", Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	if err := store.GarbageCollectProviderConfigs(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ProviderConfig("orphan_config"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("orphan lookup err=%v", err)
	}
	if _, err := store.ProviderConfig("referenced_config"); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyWorkWithoutSnapshotFailsSafe(t *testing.T) {
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	processor := NewProcessor(store, security, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if processor.app.legacySnapshotBackfillErr != nil {
		t.Fatalf("unexpected empty-work backfill failure: %v", processor.app.legacySnapshotBackfillErr)
	}
	if _, err := processor.app.videoProviderForSnapshot("", VideoProviderOpenRouter); err == nil {
		t.Fatal("legacy work without a snapshot must not use mutable credentials")
	}
}

type failingRoundTripper struct{}

func (failingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("wrong HTTP client")
}

func TestModalContentUsesDedicatedStreamingClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("video-bytes")) }))
	defer server.Close()
	client := NewModalVideoClient(server.URL, "key")
	client.HTTPClient = &http.Client{Transport: failingRoundTripper{}}
	client.ContentHTTPClient = server.Client()
	response, err := client.GetVideoContent(context.Background(), "gen_streaming", "")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || string(body) != "video-bytes" {
		t.Fatalf("body=%q err=%v", body, err)
	}
}
