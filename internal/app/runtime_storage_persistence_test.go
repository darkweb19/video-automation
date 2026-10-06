package app

import (
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Fixtures are synthetic; closing and reopening simulates a process restart
// against the same volume without making any external provider requests.
func TestRuntimeStorageReopenPreservesAccountCredentialsHistoryAndMedia(t *testing.T) {
	volume := t.TempDir()
	env := map[string]string{"RAILWAY_SERVICE_ID": "synthetic-service", "RAILWAY_VOLUME_MOUNT_PATH": volume, "DATA_DIR": filepath.Join(volume, "state")}
	resolved, err := resolveStorageFixture(env, syntheticMountInfo(volume))
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(resolved.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	initialHash, err := hashPassword("synthetic-bootstrap-password")
	if err != nil {
		t.Fatal(err)
	}
	changedHash, err := hashPassword("synthetic-changed-password")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureUser("synthetic-user", initialHash); err != nil {
		t.Fatal(err)
	}
	if err := store.ChangePassword("synthetic-user", changedHash); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "/", nil)
	response := httptest.NewRecorder()
	if err := security.NewSession(response, request, "synthetic-user"); err != nil {
		t.Fatal(err)
	}
	cookie := response.Result().Cookies()[0]
	request.AddCookie(cookie)
	keyBefore, err := os.ReadFile(filepath.Join(resolved.dataDir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	openRouterCipher, err := security.Encrypt("synthetic-openrouter-credential")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(apiKeySetting, openRouterCipher); err != nil {
		t.Fatal(err)
	}
	modalID, configID := "synthetic-modal-account", "synthetic-provider-config"
	modalCipher, err := security.EncryptSetting(modalAccountAAD(modalID), "synthetic-modal-credential")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertModalVideoAccount(ModalVideoAccount{ID: modalID, Name: "Synthetic account", Endpoint: "https://synthetic.invalid", EncryptedAPIKey: modalCipher}); err != nil {
		t.Fatal(err)
	}
	configCipher, err := security.EncryptSetting("video_provider_config."+configID, "synthetic-immutable-credential")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertProviderConfig(ProviderConfig{ID: configID, Provider: string(VideoProviderModal), BaseURL: "https://synthetic.invalid", EncryptedAPIKey: configCipher}); err != nil {
		t.Fatal(err)
	}
	generationID := "synthetic-generation"
	if err := store.InsertGeneration(GenerationRecord{ID: generationID, VideoProvider: string(VideoProviderModal), ProviderConfigID: configID, Prompt: "Synthetic restart fixture", Model: "synthetic-model", Duration: 6, AspectRatio: "9:16", Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	project, err := store.InsertProjectWithProviderConfig("Synthetic project", string(VideoProviderModal), configID, "synthetic-model")
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{store.VideoPath(generationID), store.SceneVideoPath(project.ID, 1), store.ProjectFinalPath(project.ID)}
	for _, path := range paths {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("synthetic-media-fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.MarkVideoReady(generationID, paths[0], 23); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkProjectReady(project.ID, paths[2], 23); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	resolved, err = resolveStorageFixture(env, syntheticMountInfo(volume))
	if err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(resolved.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	security, err = NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureUser("synthetic-user", initialHash); err != nil {
		t.Fatal(err)
	}
	storedHash, mustChange, err := store.UserAuthentication("synthetic-user")
	if err != nil || storedHash != changedHash || mustChange || !checkPassword(storedHash, "synthetic-changed-password") {
		t.Fatalf("changed authentication did not survive reopen: %v", err)
	}
	if username, ok := security.SessionUser(request); !ok || username != "synthetic-user" {
		t.Fatal("existing session did not survive reopen")
	}
	if _, _, err := store.SessionUser(tokenHash(cookie.Value), cookie.Expires.Unix()+1); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expired session remained valid: %v", err)
	}
	keyAfter, err := os.ReadFile(filepath.Join(resolved.dataDir, "secret.key"))
	if err != nil || string(keyBefore) != string(keyAfter) {
		t.Fatal("encryption key changed on reopen")
	}
	storedCipher, err := store.Setting(apiKeySetting)
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := security.DecryptSetting(apiKeySetting, storedCipher); err != nil || plain != "synthetic-openrouter-credential" {
		t.Fatalf("OpenRouter credential did not survive: %v", err)
	}
	account, err := store.ModalVideoAccount(modalID)
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := security.DecryptSetting(modalAccountAAD(modalID), account.EncryptedAPIKey); err != nil || plain != "synthetic-modal-credential" {
		t.Fatalf("Modal credential did not survive: %v", err)
	}
	config, err := store.ProviderConfig(configID)
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := security.DecryptSetting("video_provider_config."+configID, config.EncryptedAPIKey); err != nil || plain != "synthetic-immutable-credential" {
		t.Fatalf("provider snapshot did not survive: %v", err)
	}
	generation, err := store.Generation(generationID)
	if err != nil || generation.ProviderConfigID != configID || !generation.VideoReady || generation.VideoPath != paths[0] {
		t.Fatalf("generation did not survive: %v", err)
	}
	projects, err := store.Projects(10)
	if err != nil || len(projects) != 1 || projects[0].ID != project.ID || !projects[0].FinalVideoReady {
		t.Fatalf("project history did not survive: %v", err)
	}
	generations, err := store.Generations(10)
	if err != nil || len(generations) != 1 || generations[0].ID != generationID {
		t.Fatalf("generation history did not survive: %v", err)
	}
	for _, path := range paths {
		if content, err := os.ReadFile(path); err != nil || string(content) != "synthetic-media-fixture" {
			t.Fatalf("media did not survive: %v", err)
		}
	}
	if cookie.Expires.Before(time.Now()) {
		t.Fatal("fixture session unexpectedly expired")
	}
}

func TestRuntimeStorageLocalUpgradePreservesRelativeMediaPlayback(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relativeDir, err := filepath.Rel(cwd, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(relativeDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	provisionTestUser(t, store)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	if err := security.NewSession(response, httptest.NewRequest(http.MethodGet, "/", nil), "sujanshrestha"); err != nil {
		t.Fatal(err)
	}
	cookie := response.Result().Cookies()[0]
	const generationID = "synthetic-relative-generation"
	if err := store.InsertGeneration(GenerationRecord{ID: generationID, Prompt: "Synthetic relative path fixture", Model: "synthetic-model", Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	path := store.VideoPath(generationID)
	if filepath.IsAbs(path) {
		t.Fatal("upgrade fixture must store a relative media path")
	}
	if err := os.WriteFile(path, []byte("synthetic-relative-media"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkVideoReady(generationID, path, 24); err != nil {
		t.Fatal(err)
	}
	project, err := store.InsertProject("Synthetic relative project", "synthetic-model")
	if err != nil {
		t.Fatal(err)
	}
	finalPath := store.ProjectFinalPath(project.ID)
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(finalPath, []byte("synthetic-relative-project"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkProjectReady(project.ID, finalPath, 26); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	resolved, err := resolveStorageFixture(map[string]string{"DATA_DIR": "  " + relativeDir + "  "}, nil)
	if err != nil || resolved.dataDir != relativeDir {
		t.Fatalf("local path convention changed: %v", err)
	}
	store, err = OpenStore(resolved.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	security, err = NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewDashboardHandler(store, security, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for target, want := range map[string]string{"/video?id=" + generationID: "synthetic-relative-media", "/api/projects/" + project.ID + "/video": "synthetic-relative-project"} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		request.AddCookie(cookie)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK || response.Body.String() != want {
			t.Fatalf("relative media playback failed after upgrade: status=%d body=%q", response.Code, response.Body.String())
		}
	}
}
