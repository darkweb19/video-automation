package main

import (
	"context"
	"database/sql"
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

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func provisionTestUser(t *testing.T, store *Store) {
	t.Helper()
	hash, err := hashPassword("Sujan@123")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureUser("sujanshrestha", hash); err != nil {
		t.Fatal(err)
	}
	if err := store.ChangePassword("sujanshrestha", hash); err != nil {
		t.Fatal(err)
	}
}

func TestStorePersistsGenerationAndVideoPath(t *testing.T) {
	store := newTestStore(t)
	record := GenerationRecord{
		ID:               "job_123",
		Prompt:           "A quiet lake at dawn",
		Model:            "google/veo-3.1",
		Duration:         8,
		AspectRatio:      "16:9",
		Status:           "queued",
		CostUSD:          "0.42",
		EstimatedCostUSD: "0.4",
	}
	if err := store.InsertGeneration(record); err != nil {
		t.Fatal(err)
	}
	got, err := store.Generation(record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.CostUSD != "0.42" || got.EstimatedCostUSD != "0.4" || got.Status != "queued" {
		t.Fatalf("stored generation = %#v", got)
	}
	if !strings.HasSuffix(store.VideoPath(record.ID), "videos"+string(os.PathSeparator)+"job_123.mp4") {
		t.Fatalf("video path is outside video directory: %q", store.VideoPath(record.ID))
	}
	if err := store.UpdateGeneration("missing", "processing", "", "", ""); err == nil {
		t.Fatal("expected a missing record error")
	}
}

func TestSecurityEncryptsKeyAndStoresOnlySessionHash(t *testing.T) {
	store := newTestStore(t)
	provisionTestUser(t, store)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := security.Encrypt("sk-or-test-secret")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ciphertext, "sk-or-test-secret") {
		t.Fatal("API key was not encrypted")
	}
	plaintext, err := security.Decrypt(ciphertext)
	if err != nil || plaintext != "sk-or-test-secret" {
		t.Fatalf("decrypt = %q, %v", plaintext, err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/login", nil)
	if err := security.NewSession(recorder, request, "sujanshrestha"); err != nil {
		t.Fatal(err)
	}
	cookie := recorder.Result().Cookies()[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("unsafe session cookie: %#v", cookie)
	}
	if _, _, err := store.SessionUser(tokenHash(cookie.Value), 0); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := store.db.QueryRow(`SELECT token_hash FROM sessions`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == cookie.Value {
		t.Fatal("raw session token was stored")
	}
}

func TestDashboardProtectsHistoryAndServesLocalVideo(t *testing.T) {
	store := newTestStore(t)
	provisionTestUser(t, store)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	app := NewDashboardHandler(store, security, slog.New(slog.NewTextHandler(io.Discard, nil)))

	unauthorized := httptest.NewRecorder()
	app.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/generations", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated history = %d", unauthorized.Code)
	}

	login := httptest.NewRecorder()
	loginRequest := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"username":"sujanshrestha","password":"Sujan@123"}`))
	loginRequest.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(login, loginRequest)
	if login.Code != http.StatusOK {
		t.Fatalf("login = %d: %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]

	if err := store.InsertGeneration(GenerationRecord{ID: "job_456", Prompt: "test", Model: "google/veo", Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	path := store.VideoPath("job_456")
	if err := os.WriteFile(path, []byte("local-video"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkVideoReady("job_456", path, int64(len("local-video"))); err != nil {
		t.Fatal(err)
	}

	video := httptest.NewRecorder()
	videoRequest := httptest.NewRequest(http.MethodGet, "/video?id=job_456", nil)
	videoRequest.AddCookie(cookie)
	app.ServeHTTP(video, videoRequest)
	if video.Code != http.StatusOK || video.Body.String() != "local-video" {
		t.Fatalf("video = %d %q", video.Code, video.Body.String())
	}
}

func TestProcessorDownloadsAtomicallyToVideoDirectory(t *testing.T) {
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InsertGeneration(GenerationRecord{ID: "job_download", Prompt: "test", Model: "google/veo", Status: "downloading"}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/videos/job_download/content" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write([]byte("video-bytes"))
	}))
	defer server.Close()
	provider := NewOpenRouterClient("test-key")
	provider.BaseURL = server.URL
	provider.ContentHTTPClient = server.Client()
	processor := NewProcessor(store, security, slog.New(slog.NewTextHandler(io.Discard, nil)))
	processor.download(context.Background(), provider, GenerationRecord{ID: "job_download"})

	record, err := store.Generation("job_download")
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != "completed" || !record.VideoReady || record.SizeBytes != int64(len("video-bytes")) {
		t.Fatalf("download result = %#v", record)
	}
	data, err := os.ReadFile(record.VideoPath)
	if err != nil || string(data) != "video-bytes" {
		t.Fatalf("stored file = %q, %v", data, err)
	}
	if _, err := os.Stat(record.VideoPath + ".part"); !os.IsNotExist(err) {
		t.Fatalf("temporary download remains: %v", err)
	}
}

func TestBootstrapPasswordMustBeChangedBeforeProtectedOperations(t *testing.T) {
	store := newTestStore(t)
	hash, err := hashPassword("Sujan@123")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureUser("sujanshrestha", hash); err != nil {
		t.Fatal(err)
	}
	_, mustChange, err := store.UserAuthentication("sujanshrestha")
	if err != nil || !mustChange {
		t.Fatalf("bootstrap force-change = %t, %v", mustChange, err)
	}
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	app := NewDashboardHandler(store, security, slog.New(slog.NewTextHandler(io.Discard, nil)))
	login := httptest.NewRecorder()
	loginRequest := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"username":"sujanshrestha","password":"Sujan@123"}`))
	loginRequest.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(login, loginRequest)
	if login.Code != http.StatusOK || !strings.Contains(login.Body.String(), `"must_change_password":true`) {
		t.Fatalf("bootstrap login = %d %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	blocked := httptest.NewRecorder()
	blockedRequest := httptest.NewRequest(http.MethodGet, "/api/generations", nil)
	blockedRequest.AddCookie(cookie)
	app.ServeHTTP(blocked, blockedRequest)
	if blocked.Code != http.StatusPreconditionRequired {
		t.Fatalf("protected route = %d", blocked.Code)
	}
	change := httptest.NewRecorder()
	changeRequest := httptest.NewRequest(http.MethodPut, "/api/settings/password", strings.NewReader(`{"current_password":"Sujan@123","new_password":"A-new-password-123"}`))
	changeRequest.Header.Set("Content-Type", "application/json")
	changeRequest.AddCookie(cookie)
	app.ServeHTTP(change, changeRequest)
	if change.Code != http.StatusOK {
		t.Fatalf("password change = %d %s", change.Code, change.Body.String())
	}
}

func TestLoginThrottleBlocksAndCleansUp(t *testing.T) {
	throttle := newLoginThrottle()
	now := time.Unix(1_000, 0)
	throttle.now = func() time.Time { return now }
	for i := 0; i < loginFailureLimit; i++ {
		throttle.Failed("203.0.113.5")
	}
	if throttle.Allow("203.0.113.5") {
		t.Fatal("expected throttle block")
	}
	now = now.Add(loginBlock + loginWindow + time.Second)
	if !throttle.Allow("203.0.113.5") {
		t.Fatal("expected expired throttle to allow login")
	}
	if len(throttle.attempts) != 0 {
		t.Fatalf("stale attempts = %#v", throttle.attempts)
	}
}

func TestRecoveryCodeIsSingleUseAndInvalidatesSessions(t *testing.T) {
	store := newTestStore(t)
	provisionTestUser(t, store)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	session := httptest.NewRecorder()
	if err := security.NewSession(session, httptest.NewRequest(http.MethodPost, "/", nil), "sujanshrestha"); err != nil {
		t.Fatal(err)
	}
	cookie := session.Result().Cookies()[0]
	code := "ABCD-EFGH-JKLM-NPQR"
	if err := store.CreateRecoveryCode("sujanshrestha", recoveryCodeHash(code), time.Now().Add(recoveryCodeLifetime).Unix()); err != nil {
		t.Fatal(err)
	}
	newHash, err := hashPassword("New-password-123")
	if err != nil {
		t.Fatal(err)
	}
	ok, err := store.ConsumeRecoveryCode("sujanshrestha", recoveryCodeHash(code), newHash, time.Now().Unix())
	if err != nil || !ok {
		t.Fatalf("consume recovery code = %t, %v", ok, err)
	}
	if _, _, err := store.SessionUser(tokenHash(cookie.Value), time.Now().Unix()); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("old session remains valid: %v", err)
	}
	storedHash, err := store.PasswordHash("sujanshrestha")
	if err != nil || !checkPassword(storedHash, "New-password-123") {
		t.Fatalf("new password was not stored: %v", err)
	}
	ok, err = store.ConsumeRecoveryCode("sujanshrestha", recoveryCodeHash(code), newHash, time.Now().Unix())
	if err != nil || ok {
		t.Fatalf("reused recovery code = %t, %v", ok, err)
	}
	if err := store.CreateRecoveryCode("sujanshrestha", recoveryCodeHash("WXYZ-2345-6789-ABCD"), time.Now().Add(-time.Second).Unix()); err != nil {
		t.Fatal(err)
	}
	ok, err = store.ConsumeRecoveryCode("sujanshrestha", recoveryCodeHash("WXYZ-2345-6789-ABCD"), newHash, time.Now().Unix())
	if err != nil || ok {
		t.Fatalf("expired recovery code = %t, %v", ok, err)
	}
}

func TestPasswordRecoveryEndpoint(t *testing.T) {
	store := newTestStore(t)
	provisionTestUser(t, store)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	code := "ABCD-EFGH-JKLM-NPQR"
	if err := store.CreateRecoveryCode("sujanshrestha", recoveryCodeHash(code), time.Now().Add(recoveryCodeLifetime).Unix()); err != nil {
		t.Fatal(err)
	}
	app := NewDashboardHandler(store, security, slog.New(slog.NewTextHandler(io.Discard, nil)))
	recovery := httptest.NewRecorder()
	recoveryRequest := httptest.NewRequest(http.MethodPost, "/api/password/recover", strings.NewReader(`{"username":"sujanshrestha","code":"abcd-efgh-jklm-npqr","new_password":"Recovered-password-123"}`))
	recoveryRequest.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(recovery, recoveryRequest)
	if recovery.Code != http.StatusOK {
		t.Fatalf("recovery = %d: %s", recovery.Code, recovery.Body.String())
	}

	login := httptest.NewRecorder()
	loginRequest := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"username":"sujanshrestha","password":"Recovered-password-123"}`))
	loginRequest.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(login, loginRequest)
	if login.Code != http.StatusOK {
		t.Fatalf("login after recovery = %d: %s", login.Code, login.Body.String())
	}

	reuse := httptest.NewRecorder()
	reuseRequest := httptest.NewRequest(http.MethodPost, "/api/password/recover", strings.NewReader(`{"username":"sujanshrestha","code":"abcd-efgh-jklm-npqr","new_password":"Another-password-123"}`))
	reuseRequest.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(reuse, reuseRequest)
	if reuse.Code != http.StatusBadRequest || !strings.Contains(reuse.Body.String(), "invalid or expired") {
		t.Fatalf("reused recovery = %d: %s", reuse.Code, reuse.Body.String())
	}
}

func TestGenerateRecoveryCodeFormat(t *testing.T) {
	code, err := generateRecoveryCode()
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(code, "-")
	if len(parts) != 4 {
		t.Fatalf("recovery code format = %q", code)
	}
	for _, part := range parts {
		if len(part) != 4 {
			t.Fatalf("recovery code format = %q", code)
		}
	}
}

func TestHistoryStatsPaginationAndDownloadBackoff(t *testing.T) {
	store := newTestStore(t)
	if err := store.InsertGeneration(GenerationRecord{ID: "job_a", Prompt: "a", Model: "model", Status: "completed", CostUSD: "1.25"}); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertGeneration(GenerationRecord{ID: "job_b", Prompt: "b", Model: "model", Status: "downloading"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE generations SET created_at=100 WHERE id='job_a'; UPDATE generations SET created_at=99 WHERE id='job_b'`); err != nil {
		t.Fatal(err)
	}
	page, err := store.GenerationsPage(1, 0, "")
	if err != nil || len(page) != 1 || page[0].ID != "job_a" {
		t.Fatalf("page = %#v %v", page, err)
	}
	next, err := store.GenerationsPage(1, page[0].CreatedAt, page[0].ID)
	if err != nil || len(next) != 1 || next[0].ID != "job_b" {
		t.Fatalf("next = %#v %v", next, err)
	}
	stats, err := store.GenerationStats()
	if err != nil || stats.Total != 2 || stats.Completed != 1 || stats.Downloading != 1 || stats.TotalCostUSD != "1.25" {
		t.Fatalf("stats = %#v %v", stats, err)
	}
	if err := store.ScheduleDownloadRetry("job_b"); err != nil {
		t.Fatal(err)
	}
	record, err := store.Generation("job_b")
	if err != nil || record.DownloadAttempts != 1 || record.NextDownloadAt <= time.Now().Unix() {
		t.Fatalf("retry record = %#v %v", record, err)
	}
}

func TestAPIKeyValidationDistinguishesInvalidAndTransientFailures(t *testing.T) {
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name           string
		upstream, want int
	}{{"invalid", http.StatusUnauthorized, http.StatusBadRequest}, {"transient", http.StatusServiceUnavailable, http.StatusBadGateway}} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(test.upstream) }))
			defer server.Close()
			app := &dashboardApp{store: store, security: security, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), baseURL: server.URL}
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPut, "/api/settings/api-key", strings.NewReader(`{"api_key":"test-key-12345"}`))
			request.Header.Set("Content-Type", "application/json")
			app.updateAPIKey(recorder, request)
			if recorder.Code != test.want {
				t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}
