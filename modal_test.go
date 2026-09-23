package main

import (
	"context"
	"encoding/json"
	"io"
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
