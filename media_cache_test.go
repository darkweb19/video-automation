package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func mediaCacheRequest(app http.Handler, target string, cookie *http.Cookie, headerName, headerValue string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, target, nil)
	if cookie != nil {
		request.AddCookie(cookie)
	}
	if headerName != "" {
		request.Header.Set(headerName, headerValue)
	}
	recorder := httptest.NewRecorder()
	app.ServeHTTP(recorder, request)
	return recorder
}

func TestSecurityHeadersAllowVaultBlobMedia(t *testing.T) {
	handler := securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	policy := recorder.Header().Get("Content-Security-Policy")
	if !strings.Contains(policy, "media-src 'self' blob:") {
		t.Fatalf("CSP media policy = %q, want same-origin and blob media", policy)
	}
}

func TestOrdinaryGenerationMediaCacheRechecksAuthorizationAndVaultMembership(t *testing.T) {
	store := newTestStore(t)
	provisionTestUser(t, store)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	app := NewDashboardHandler(store, security, slog.New(slog.NewTextHandler(io.Discard, nil)))
	cookie := vaultTestLogin(t, app)
	vaultTestSetCode(t, app, cookie, "9031")
	addVaultGeneration(t, store, "cache_generation")
	target := "/video?id=cache_generation"

	initial := mediaCacheRequest(app, target, cookie, "", "")
	etag := initial.Header().Get("ETag")
	if initial.Code != http.StatusOK || initial.Body.String() != "vault-video-content" || etag == "" {
		t.Fatalf("initial media = %d body=%q etag=%q", initial.Code, initial.Body.String(), etag)
	}
	if initial.Header().Get("Cache-Control") != "private, no-cache" {
		t.Fatalf("ordinary cache control = %q", initial.Header().Get("Cache-Control"))
	}

	unauthenticated := mediaCacheRequest(app, target, nil, "If-None-Match", etag)
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated conditional request = %d, want 401", unauthenticated.Code)
	}
	unchanged := mediaCacheRequest(app, target, cookie, "If-None-Match", etag)
	if unchanged.Code != http.StatusNotModified || unchanged.Body.Len() != 0 || unchanged.Header().Get("ETag") != etag {
		t.Fatalf("matching validator = %d body=%q headers=%v", unchanged.Code, unchanged.Body.String(), unchanged.Header())
	}

	rangeResponse := mediaCacheRequest(app, target, cookie, "Range", "bytes=0-4")
	if rangeResponse.Code != http.StatusPartialContent || rangeResponse.Body.String() != "vault" || rangeResponse.Header().Get("Content-Range") != "bytes 0-4/19" {
		t.Fatalf("range response = %d body=%q content-range=%q", rangeResponse.Code, rangeResponse.Body.String(), rangeResponse.Header().Get("Content-Range"))
	}

	legacyDownload := mediaCacheRequest(app, target+"&download=1", cookie, "", "")
	if legacyDownload.Code != http.StatusOK || legacyDownload.Header().Get("ETag") != etag || !strings.Contains(legacyDownload.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("legacy download = %d etag=%q disposition=%q", legacyDownload.Code, legacyDownload.Header().Get("ETag"), legacyDownload.Header().Get("Content-Disposition"))
	}

	moved := vaultTestRequest(app, http.MethodPost, "/api/vault/items/generation/cache_generation/move", "", cookie, "")
	if moved.Code != http.StatusNoContent {
		t.Fatalf("move to Vault = %d: %s", moved.Code, moved.Body.String())
	}
	ordinaryAfterMove := mediaCacheRequest(app, target, cookie, "If-None-Match", etag)
	if ordinaryAfterMove.Code != http.StatusNotFound {
		t.Fatalf("ordinary conditional request after Vault move = %d, want 404", ordinaryAfterMove.Code)
	}

	token := vaultTestUnlock(t, app, cookie, "9031")
	vaultRequest := httptest.NewRequest(http.MethodGet, "/api/vault/items/generation/cache_generation/video", nil)
	vaultRequest.AddCookie(cookie)
	vaultRequest.Header.Set("Authorization", "Vault "+token)
	vaultRequest.Header.Set("If-None-Match", etag)
	vaultResponse := httptest.NewRecorder()
	app.ServeHTTP(vaultResponse, vaultRequest)
	if vaultResponse.Code != http.StatusOK || vaultResponse.Body.String() != "vault-video-content" || !strings.Contains(vaultResponse.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("Vault media = %d body=%q cache=%q", vaultResponse.Code, vaultResponse.Body.String(), vaultResponse.Header().Get("Cache-Control"))
	}
}

func TestProjectMediaValidatorChangesWhenRetryReplacesFinalFile(t *testing.T) {
	store := newTestStore(t)
	provisionTestUser(t, store)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	app := NewDashboardHandler(store, security, slog.New(slog.NewTextHandler(io.Discard, nil)))
	cookie := vaultTestLogin(t, app)
	project, err := store.InsertProject("Retry validator test", "test/model")
	if err != nil {
		t.Fatal(err)
	}
	path := store.ProjectFinalPath(project.ID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	firstBytes := []byte("final-version-01")
	if err := os.WriteFile(path, firstBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	firstTime := time.Unix(100, 123456789)
	if err := os.Chtimes(path, firstTime, firstTime); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkProjectReady(project.ID, path, int64(len(firstBytes))); err != nil {
		t.Fatal(err)
	}
	target := "/api/projects/" + project.ID + "/video"
	initial := mediaCacheRequest(app, target, cookie, "", "")
	oldETag := initial.Header().Get("ETag")
	if initial.Code != http.StatusOK || initial.Body.String() != string(firstBytes) || oldETag == "" {
		t.Fatalf("initial project video = %d body=%q etag=%q", initial.Code, initial.Body.String(), oldETag)
	}

	if err := store.UpdateProjectStatus(project.ID, "failed", "simulated retry"); err != nil {
		t.Fatal(err)
	}
	if err := store.RetryProject(project.ID); err != nil {
		t.Fatal(err)
	}
	whileRetrying := mediaCacheRequest(app, target, cookie, "If-None-Match", oldETag)
	if whileRetrying.Code != http.StatusNotFound {
		t.Fatalf("project media while retrying = %d, want 404", whileRetrying.Code)
	}

	secondBytes := []byte("final-version-02")
	if len(secondBytes) != len(firstBytes) {
		t.Fatal("fixture replacements must have equal lengths")
	}
	if err := os.WriteFile(path, secondBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	// Keep the replacement within the same second while using a representable
	// Windows timestamp delta.
	secondTime := firstTime.Add(time.Millisecond)
	if err := os.Chtimes(path, secondTime, secondTime); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkProjectReady(project.ID, path, int64(len(secondBytes))); err != nil {
		t.Fatal(err)
	}
	replaced := mediaCacheRequest(app, target, cookie, "If-None-Match", oldETag)
	newETag := replaced.Header().Get("ETag")
	if replaced.Code != http.StatusOK || replaced.Body.String() != string(secondBytes) || newETag == "" || newETag == oldETag {
		t.Fatalf("retried project video = %d body=%q old etag=%q new etag=%q", replaced.Code, replaced.Body.String(), oldETag, newETag)
	}
}

func TestProjectMediaRoutesKeepLargeAuditDataAndServeVideo(t *testing.T) {
	store := newTestStore(t)
	provisionTestUser(t, store)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	app := NewDashboardHandler(store, security, slog.New(slog.NewTextHandler(io.Discard, nil)))
	cookie := vaultTestLogin(t, app)
	vaultTestSetCode(t, app, cookie, "9031")
	project, err := store.InsertProject("Large audit media test", "test/model")
	if err != nil {
		t.Fatal(err)
	}
	rawResponse := strings.Repeat("r", MaxScriptRawResponseBytes)
	if err := store.SaveTextGenerationTrace(project.ID, TextGenerationTrace{RouterModel: ScriptModel, RawResponse: rawResponse, Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 8; index++ {
		if err := store.AppendPipelineEvent(project.ID, "audit", "visible", strings.Repeat("event", 100), index%ProjectSceneCount+1, index+1); err != nil {
			t.Fatal(err)
		}
	}
	path := store.ProjectFinalPath(project.ID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	content := []byte("large-audit-project-video")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkProjectReady(project.ID, path, int64(len(content))); err != nil {
		t.Fatal(err)
	}

	ordinary := mediaCacheRequest(app, "/api/projects/"+project.ID+"/video", cookie, "", "")
	if ordinary.Code != http.StatusOK || ordinary.Body.String() != string(content) {
		t.Fatalf("ordinary project media = %d body=%q", ordinary.Code, ordinary.Body.String())
	}
	if err := store.SetProjectVaulted(project.ID, true); err != nil {
		t.Fatal(err)
	}
	token := vaultTestUnlock(t, app, cookie, "9031")
	vaulted := vaultTestRequest(app, http.MethodGet, "/api/vault/items/project/"+project.ID+"/video", "", cookie, token)
	if vaulted.Code != http.StatusOK || vaulted.Body.String() != string(content) || !strings.Contains(vaulted.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("Vault project media = %d body=%q cache=%q", vaulted.Code, vaulted.Body.String(), vaulted.Header().Get("Cache-Control"))
	}

	fullProject, err := store.Project(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fullProject.TextGeneration.RawResponse != rawResponse || len(fullProject.PipelineEvents) < 9 {
		t.Fatalf("media reads lost persisted audit data: raw bytes=%d events=%d", len(fullProject.TextGeneration.RawResponse), len(fullProject.PipelineEvents))
	}
}
