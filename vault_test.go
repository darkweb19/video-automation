package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func vaultTestApp(t *testing.T, store *Store, security *Security) http.Handler {
	t.Helper()
	return NewDashboardHandler(store, security, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func vaultTestDashboardState(store *Store, security *Security) *dashboardApp {
	return &dashboardApp{
		store:           store,
		security:        security,
		logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		vault:           newVaultRuntime(),
		limiter:         newLoginThrottle(),
		recoveryLimiter: newLoginThrottle(),
	}
}

type blockingVaultReader struct {
	started chan struct{}
	release chan struct{}
}

func (r *blockingVaultReader) Read(buffer []byte) (int, error) {
	close(r.started)
	<-r.release
	for index := range buffer {
		buffer[index] = 0x5a
	}
	return len(buffer), nil
}

func vaultTestLogin(t *testing.T, app http.Handler) *http.Cookie {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"username":"sujanshrestha","password":"Sujan@123"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	app.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || len(recorder.Result().Cookies()) == 0 {
		t.Fatalf("login = %d: %s", recorder.Code, recorder.Body.String())
	}
	return recorder.Result().Cookies()[0]
}

func vaultTestRequest(app http.Handler, method, path, body string, cookie *http.Cookie, token string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	if token != "" {
		request.Header.Set("Authorization", "Vault "+token)
	}
	recorder := httptest.NewRecorder()
	app.ServeHTTP(recorder, request)
	return recorder
}

func vaultTestSetCode(t *testing.T, app http.Handler, cookie *http.Cookie, code string) {
	t.Helper()
	recorder := vaultTestRequest(app, http.MethodPut, "/api/settings/vault-code", `{"new_code":"`+code+`"}`, cookie, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("set Vault code = %d: %s", recorder.Code, recorder.Body.String())
	}
}

func vaultTestUnlock(t *testing.T, app http.Handler, cookie *http.Cookie, code string) string {
	t.Helper()
	recorder := vaultTestRequest(app, http.MethodPost, "/api/vault/unlock", `{"code":"`+code+`"}`, cookie, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("unlock Vault = %d: %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || response.Token == "" {
		t.Fatalf("unlock response did not contain a token: %s (%v)", recorder.Body.String(), err)
	}
	return response.Token
}

func addVaultGeneration(t *testing.T, store *Store, id string) {
	t.Helper()
	if err := store.InsertGeneration(GenerationRecord{ID: id, Prompt: "Private generated clip", Model: "test/model", Status: "completed", Duration: 8}); err != nil {
		t.Fatal(err)
	}
	path := store.VideoPath(id)
	if err := os.WriteFile(path, []byte("vault-video-content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkVideoReady(id, path, int64(len("vault-video-content"))); err != nil {
		t.Fatal(err)
	}
}

func TestVaultCodeEncryptionRateLimitAndSecretResponses(t *testing.T) {
	store := newTestStore(t)
	provisionTestUser(t, store)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	app := NewDashboardHandler(store, security, slog.New(slog.NewTextHandler(&logs, nil)))
	cookie := vaultTestLogin(t, app)
	code := "1462"
	vaultTestSetCode(t, app, cookie, code)

	stored, err := store.Setting(vaultCodeSetting)
	if err != nil || stored == code || strings.Contains(stored, code) {
		t.Fatalf("Vault code was not encrypted at rest: %q (%v)", stored, err)
	}
	settings := vaultTestRequest(app, http.MethodGet, "/api/settings", "", cookie, "")
	if settings.Code != http.StatusOK || !strings.Contains(settings.Body.String(), `"vault_code_configured":true`) || strings.Contains(settings.Body.String(), code) {
		t.Fatalf("settings response leaked or omitted Vault state: %d %s", settings.Code, settings.Body.String())
	}
	token := vaultTestUnlock(t, app, cookie, code)
	if strings.Contains(settings.Body.String(), token) || strings.Contains(logs.String(), code) || strings.Contains(logs.String(), token) {
		t.Fatalf("Vault code or grant appeared in settings or logs: settings=%s logs=%s", settings.Body.String(), logs.String())
	}

	for attempt := 0; attempt < vaultAttemptLimit; attempt++ {
		wrong := vaultTestRequest(app, http.MethodPut, "/api/settings/vault-code", `{"current_code":"0000","new_code":"1379"}`, cookie, "")
		if wrong.Code != http.StatusForbidden {
			t.Fatalf("wrong current code attempt %d = %d: %s", attempt+1, wrong.Code, wrong.Body.String())
		}
	}
	blocked := vaultTestRequest(app, http.MethodPost, "/api/vault/unlock", `{"code":"`+code+`"}`, cookie, "")
	if blocked.Code != http.StatusTooManyRequests {
		t.Fatalf("correct code bypassed Settings PIN throttle: %d %s", blocked.Code, blocked.Body.String())
	}
	settings = vaultTestRequest(app, http.MethodGet, "/api/settings", "", cookie, "")
	if strings.Contains(settings.Body.String(), code) || strings.Contains(settings.Body.String(), "1379") || strings.Contains(settings.Body.String(), token) {
		t.Fatalf("settings response exposed Vault material: %s", settings.Body.String())
	}
	if strings.Contains(logs.String(), code) || strings.Contains(logs.String(), "0000") || strings.Contains(logs.String(), "1379") || strings.Contains(logs.String(), token) {
		t.Fatalf("Vault code appeared in logs: %s", logs.String())
	}
}

func TestVaultMigrationKeepsExistingRowsVisible(t *testing.T) {
	store := newTestStore(t)
	if err := store.InsertGeneration(GenerationRecord{ID: "legacy_job", Prompt: "legacy prompt", Model: "test/model", Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	project, err := store.InsertProject("Legacy project", "test/model")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`ALTER TABLE generations DROP COLUMN in_vault; ALTER TABLE video_projects DROP COLUMN in_vault;`); err != nil {
		t.Fatalf("prepare pre-Vault schema: %v", err)
	}
	if err := store.migrate(); err != nil {
		t.Fatalf("migrate pre-Vault schema: %v", err)
	}
	generation, err := store.Generation("legacy_job")
	if err != nil || generation.InVault {
		t.Fatalf("legacy generation after migration = %#v, %v", generation, err)
	}
	projects, err := store.Projects(24)
	if err != nil || len(projects) != 1 || projects[0].ID != project.ID || projects[0].InVault {
		t.Fatalf("legacy projects after migration = %#v, %v", projects, err)
	}
}

func TestVaultProtectsMediaAndRestoresHistoryAfterRestart(t *testing.T) {
	store := newTestStore(t)
	provisionTestUser(t, store)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	app := vaultTestApp(t, store, security)
	cookie := vaultTestLogin(t, app)
	code := "5274"
	vaultTestSetCode(t, app, cookie, code)
	addVaultGeneration(t, store, "vault_job")

	moved := vaultTestRequest(app, http.MethodPost, "/api/vault/items/generation/vault_job/move", "", cookie, "")
	if moved.Code != http.StatusNoContent {
		t.Fatalf("move generation = %d: %s", moved.Code, moved.Body.String())
	}

	for _, target := range []string{"/api/generations", "/status?id=vault_job", "/video?id=vault_job"} {
		response := vaultTestRequest(app, http.MethodGet, target, "", cookie, "")
		if strings.Contains(response.Body.String(), "vault_job") || strings.Contains(response.Body.String(), "Private generated clip") {
			t.Fatalf("standard route leaked Vault metadata at %s: %s", target, response.Body.String())
		}
		if target != "/api/generations" && response.Code != http.StatusNotFound {
			t.Fatalf("standard route %s = %d, want not found", target, response.Code)
		}
	}
	if response := vaultTestRequest(app, http.MethodGet, "/api/vault/items", "", cookie, ""); response.Code != http.StatusLocked {
		t.Fatalf("Vault list without grant = %d", response.Code)
	}

	token := vaultTestUnlock(t, app, cookie, code)
	listed := vaultTestRequest(app, http.MethodGet, "/api/vault/items", "", cookie, token)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), "vault_job") || strings.Contains(listed.Body.String(), token) {
		t.Fatalf("Vault list = %d: %s", listed.Code, listed.Body.String())
	}
	media := vaultTestRequest(app, http.MethodGet, "/api/vault/items/generation/vault_job/video", "", cookie, token)
	if media.Code != http.StatusOK || media.Body.String() != "vault-video-content" || !strings.Contains(media.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("Vault media with grant = %d %q", media.Code, media.Body.String())
	}
	deniedMedia := vaultTestRequest(app, http.MethodGet, "/api/vault/items/generation/vault_job/video", "", cookie, "")
	if deniedMedia.Code != http.StatusLocked {
		t.Fatalf("Vault media without grant = %d", deniedMedia.Code)
	}
	lock := vaultTestRequest(app, http.MethodPost, "/api/vault/lock", "", cookie, token)
	if lock.Code != http.StatusNoContent {
		t.Fatalf("lock Vault = %d", lock.Code)
	}
	if locked := vaultTestRequest(app, http.MethodGet, "/api/vault/items", "", cookie, token); locked.Code != http.StatusLocked {
		t.Fatalf("Vault list after lock = %d", locked.Code)
	}

	// Reopen SQLite and construct a fresh handler to verify the encrypted PIN and
	// membership persist while the in-memory unlock grant does not.
	dataDir := store.dataDir
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	reopenedSecurity, err := NewSecurity(reopened)
	if err != nil {
		t.Fatal(err)
	}
	app = vaultTestApp(t, reopened, reopenedSecurity)
	if locked := vaultTestRequest(app, http.MethodGet, "/api/vault/items", "", cookie, token); locked.Code != http.StatusLocked {
		t.Fatalf("Vault grant survived restart = %d", locked.Code)
	}
	token = vaultTestUnlock(t, app, cookie, code)
	if listed := vaultTestRequest(app, http.MethodGet, "/api/vault/items", "", cookie, token); listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), "vault_job") {
		t.Fatalf("Vault membership did not persist: %d %s", listed.Code, listed.Body.String())
	}

	restored := vaultTestRequest(app, http.MethodPost, "/api/vault/items/generation/vault_job/restore", "", cookie, token)
	if restored.Code != http.StatusNoContent {
		t.Fatalf("restore generation = %d: %s", restored.Code, restored.Body.String())
	}
	history := vaultTestRequest(app, http.MethodGet, "/api/generations", "", cookie, "")
	if history.Code != http.StatusOK || !strings.Contains(history.Body.String(), "vault_job") {
		t.Fatalf("restored generation missing from History: %d %s", history.Code, history.Body.String())
	}
	media = vaultTestRequest(app, http.MethodGet, "/video?id=vault_job", "", cookie, "")
	if media.Code != http.StatusOK || media.Body.String() != "vault-video-content" {
		t.Fatalf("restored video access = %d %q", media.Code, media.Body.String())
	}

	// A page refresh cannot retain the in-memory bearer token. Its pagehide
	// request clears the server grant for the whole login session without one.
	refreshToken := vaultTestUnlock(t, app, cookie, code)
	refreshLock := vaultTestRequest(app, http.MethodPost, "/api/vault/lock", "", cookie, "")
	if refreshLock.Code != http.StatusNoContent {
		t.Fatalf("refresh lock = %d: %s", refreshLock.Code, refreshLock.Body.String())
	}
	if locked := vaultTestRequest(app, http.MethodGet, "/api/vault/items", "", cookie, refreshToken); locked.Code != http.StatusLocked {
		t.Fatalf("grant remained valid after refresh lock = %d", locked.Code)
	}
}

func TestVaultProtectsCompletedProjectFinalVideo(t *testing.T) {
	store := newTestStore(t)
	provisionTestUser(t, store)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	app := vaultTestApp(t, store, security)
	cookie := vaultTestLogin(t, app)
	vaultTestSetCode(t, app, cookie, "9031")
	project, err := store.InsertProject("A private short film", "test/model")
	if err != nil {
		t.Fatal(err)
	}
	path := store.ProjectFinalPath(project.ID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("project-video-content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkProjectReady(project.ID, path, int64(len("project-video-content"))); err != nil {
		t.Fatal(err)
	}

	moved := vaultTestRequest(app, http.MethodPost, "/api/vault/items/project/"+project.ID+"/move", "", cookie, "")
	if moved.Code != http.StatusNoContent {
		t.Fatalf("move project = %d: %s", moved.Code, moved.Body.String())
	}
	projects := vaultTestRequest(app, http.MethodGet, "/api/projects", "", cookie, "")
	projectDetails := vaultTestRequest(app, http.MethodGet, "/api/projects/"+project.ID, "", cookie, "")
	projectMedia := vaultTestRequest(app, http.MethodGet, "/api/projects/"+project.ID+"/video", "", cookie, "")
	if strings.Contains(projects.Body.String(), project.ID) || projectDetails.Code != http.StatusNotFound || projectMedia.Code != http.StatusNotFound {
		t.Fatalf("ordinary project routes leaked Vault item: list=%s details=%d media=%d", projects.Body.String(), projectDetails.Code, projectMedia.Code)
	}

	token := vaultTestUnlock(t, app, cookie, "9031")
	media := vaultTestRequest(app, http.MethodGet, "/api/vault/items/project/"+project.ID+"/video", "", cookie, token)
	if media.Code != http.StatusOK || media.Body.String() != "project-video-content" {
		t.Fatalf("project Vault media = %d %q", media.Code, media.Body.String())
	}
	restored := vaultTestRequest(app, http.MethodPost, "/api/vault/items/project/"+project.ID+"/restore", "", cookie, token)
	if restored.Code != http.StatusNoContent {
		t.Fatalf("restore project = %d: %s", restored.Code, restored.Body.String())
	}
	projects = vaultTestRequest(app, http.MethodGet, "/api/projects", "", cookie, "")
	projectMedia = vaultTestRequest(app, http.MethodGet, "/api/projects/"+project.ID+"/video", "", cookie, "")
	if !strings.Contains(projects.Body.String(), project.ID) || projectMedia.Code != http.StatusOK || projectMedia.Body.String() != "project-video-content" {
		t.Fatalf("restored project did not return to History: list=%s media=%d %q", projects.Body.String(), projectMedia.Code, projectMedia.Body.String())
	}
}

func TestVaultConcurrentWrongCodesRespectAttemptLimit(t *testing.T) {
	for _, route := range []string{"unlock", "change"} {
		t.Run(route, func(t *testing.T) {
			store := newTestStore(t)
			provisionTestUser(t, store)
			security, err := NewSecurity(store)
			if err != nil {
				t.Fatal(err)
			}
			app := vaultTestApp(t, store, security)
			cookie := vaultTestLogin(t, app)
			vaultTestSetCode(t, app, cookie, "3481")

			attempts := vaultAttemptLimit * 8
			start := make(chan struct{})
			statuses := make(chan int, attempts)
			var workers sync.WaitGroup
			for i := 0; i < attempts; i++ {
				workers.Add(1)
				go func() {
					defer workers.Done()
					<-start
					var response *httptest.ResponseRecorder
					if route == "unlock" {
						response = vaultTestRequest(app, http.MethodPost, "/api/vault/unlock", `{"code":"0000"}`, cookie, "")
					} else {
						response = vaultTestRequest(app, http.MethodPut, "/api/settings/vault-code", `{"current_code":"0000","new_code":"1649"}`, cookie, "")
					}
					statuses <- response.Code
				}()
			}
			close(start)
			workers.Wait()
			close(statuses)

			var forbidden, limited int
			for status := range statuses {
				switch status {
				case http.StatusForbidden:
					forbidden++
				case http.StatusTooManyRequests:
					limited++
				default:
					t.Fatalf("unexpected concurrent PIN response: %d", status)
				}
			}
			if forbidden != vaultAttemptLimit || limited != attempts-vaultAttemptLimit {
				t.Fatalf("responses bypassed atomic attempt limit: %d incorrect, %d throttled", forbidden, limited)
			}
		})
	}
}

func TestVaultConcurrentRotationRevokesOldCodeGrant(t *testing.T) {
	store := newTestStore(t)
	provisionTestUser(t, store)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	app := vaultTestApp(t, store, security)
	cookie := vaultTestLogin(t, app)
	oldCode, newCode := "7519", "2864"
	vaultTestSetCode(t, app, cookie, oldCode)

	start := make(chan struct{})
	var workers sync.WaitGroup
	var unlockResponse, rotateResponse *httptest.ResponseRecorder
	workers.Add(2)
	go func() {
		defer workers.Done()
		<-start
		unlockResponse = vaultTestRequest(app, http.MethodPost, "/api/vault/unlock", `{"code":"`+oldCode+`"}`, cookie, "")
	}()
	go func() {
		defer workers.Done()
		<-start
		rotateResponse = vaultTestRequest(app, http.MethodPut, "/api/settings/vault-code", `{"current_code":"`+oldCode+`","new_code":"`+newCode+`"}`, cookie, "")
	}()
	close(start)
	workers.Wait()
	if rotateResponse.Code != http.StatusOK {
		t.Fatalf("rotate Vault code = %d: %s", rotateResponse.Code, rotateResponse.Body.String())
	}
	if unlockResponse.Code == http.StatusOK {
		var response struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal(unlockResponse.Body.Bytes(), &response); err != nil || response.Token == "" {
			t.Fatalf("old-code unlock returned invalid response: %s (%v)", unlockResponse.Body.String(), err)
		}
		if granted := vaultTestRequest(app, http.MethodGet, "/api/vault/items", "", cookie, response.Token); granted.Code != http.StatusLocked {
			t.Fatalf("old-code grant survived rotation = %d", granted.Code)
		}
	} else if unlockResponse.Code != http.StatusForbidden {
		t.Fatalf("old-code unlock = %d: %s", unlockResponse.Code, unlockResponse.Body.String())
	}
	newToken := vaultTestUnlock(t, app, cookie, newCode)
	if unlocked := vaultTestRequest(app, http.MethodGet, "/api/vault/items", "", cookie, newToken); unlocked.Code != http.StatusOK {
		t.Fatalf("new code did not unlock after rotation = %d: %s", unlocked.Code, unlocked.Body.String())
	}
}

func TestVaultLockWaitsForInFlightGrantIssuance(t *testing.T) {
	store := newTestStore(t)
	provisionTestUser(t, store)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	cookie := vaultTestLogin(t, vaultTestApp(t, store, security))
	app := vaultTestDashboardState(store, security)
	vaultTestSetCode(t, http.HandlerFunc(app.updateVaultCode), cookie, "6842")

	random := &blockingVaultReader{started: make(chan struct{}), release: make(chan struct{})}
	app.vault.random = random
	var releaseOnce sync.Once
	releaseReader := func() { releaseOnce.Do(func() { close(random.release) }) }
	defer releaseReader()

	unlockDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		unlockDone <- vaultTestRequest(http.HandlerFunc(app.unlockVault), http.MethodPost, "/api/vault/unlock", `{"code":"6842"}`, cookie, "")
	}()
	select {
	case <-random.started:
	case <-time.After(5 * time.Second):
		t.Fatal("unlock did not reach grant issuance")
	}

	lockStarted := make(chan struct{})
	lockDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		close(lockStarted)
		lockDone <- vaultTestRequest(http.HandlerFunc(app.lockVault), http.MethodPost, "/api/vault/lock", "", cookie, "")
	}()
	<-lockStarted
	select {
	case response := <-lockDone:
		releaseReader()
		t.Fatalf("Lock returned while grant issuance was in flight: %d", response.Code)
	case <-time.After(100 * time.Millisecond):
	}
	releaseReader()

	unlockResponse := <-unlockDone
	lockResponse := <-lockDone
	if unlockResponse.Code != http.StatusOK || lockResponse.Code != http.StatusNoContent {
		t.Fatalf("unlock/lock responses = %d/%d: %s / %s", unlockResponse.Code, lockResponse.Code, unlockResponse.Body.String(), lockResponse.Body.String())
	}
	var unlockResult struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(unlockResponse.Body.Bytes(), &unlockResult); err != nil || unlockResult.Token == "" {
		t.Fatalf("unlock response did not contain a token: %s (%v)", unlockResponse.Body.String(), err)
	}
	protectedList := app.requireVaultUnlock(http.HandlerFunc(app.vaultItems))
	locked := vaultTestRequest(protectedList, http.MethodGet, "/api/vault/items", "", cookie, unlockResult.Token)
	if locked.Code != http.StatusLocked {
		t.Fatalf("in-flight unlock grant survived Lock = %d: %s", locked.Code, locked.Body.String())
	}
}
