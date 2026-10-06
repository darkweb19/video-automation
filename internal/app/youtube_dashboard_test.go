package app

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These fixtures use synthetic database rows and local bytes. They do not
// contact Google, OpenRouter, or a video provider.
const (
	youtubeDashboardFixtureChannel = "UCFrameVaultSyntheticTest"
	youtubeDashboardFixtureOrigin  = "https://dashboard.fixture.example"
)

type youtubeDashboardFixture struct {
	store   *Store
	handler http.Handler
	cookie  *http.Cookie
	channel string
}

func newYouTubeDashboardFixture(t *testing.T) *youtubeDashboardFixture {
	t.Helper()
	store := newTestStore(t)
	provisionTestUser(t, store)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &youtubeDashboardFixture{
		store:   store,
		handler: NewDashboardHandler(store, security, slog.New(slog.NewTextHandler(io.Discard, nil))),
		channel: youtubeDashboardFixtureChannel,
	}
	fixture.cookie = vaultTestLogin(t, fixture.handler)
	secret, err := security.EncryptSetting("youtube_client_secret", "synthetic-oauth-client-secret")
	if err != nil {
		t.Fatal(err)
	}
	refresh, err := security.EncryptSetting("youtube_refresh_token", "synthetic-refresh-token")
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.db.Exec(`UPDATE youtube_config SET client_id=?,encrypted_client_secret=?,base_url=?,encrypted_refresh_token=?,channel_id=?,channel_title=?,connection_version=1,config_version=1,reconnect_required=0 WHERE id=1`,
		testYouTubeClientID, secret, youtubeDashboardFixtureOrigin, refresh, fixture.channel, "Synthetic test channel")
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}

func (f *youtubeDashboardFixture) request(method, path, body, vaultGrant string) *httptest.ResponseRecorder {
	return vaultTestRequest(f.handler, method, path, body, f.cookie, vaultGrant)
}

func (f *youtubeDashboardFixture) addReadyGeneration(t *testing.T, id string) {
	t.Helper()
	if err := f.store.InsertGeneration(GenerationRecord{
		ID: id, Prompt: "Synthetic test clip", Model: "fixture/video", Status: "queued",
	}); err != nil {
		t.Fatal(err)
	}
	path := f.store.VideoPath(id)
	if err := os.WriteFile(path, []byte("synthetic test video bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.MarkVideoReady(id, path, info.Size()); err != nil {
		t.Fatal(err)
	}
}

func (f *youtubeDashboardFixture) addIncompleteGeneration(t *testing.T, id string) {
	t.Helper()
	if err := f.store.InsertGeneration(GenerationRecord{
		ID: id, Prompt: "Synthetic incomplete clip", Model: "fixture/video", Status: "processing",
	}); err != nil {
		t.Fatal(err)
	}
}

func (f *youtubeDashboardFixture) addReadyProject(t *testing.T, topic string) VideoProject {
	t.Helper()
	project, err := f.store.InsertProject(topic, "fixture/video")
	if err != nil {
		t.Fatal(err)
	}
	path := f.store.ProjectFinalPath(project.ID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("synthetic test project video bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.MarkProjectReady(project.ID, path, info.Size()); err != nil {
		t.Fatal(err)
	}
	updated, err := f.store.Project(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	return updated
}

func youtubeDashboardUploadBody(t *testing.T, kind, sourceID, channelID, title, description, privacy string, madeForKids, synthetic bool) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"source_kind": kind, "source_id": sourceID, "channel_id": channelID,
		"title": title, "description": description, "privacy_status": privacy,
		"made_for_kids": madeForKids, "contains_synthetic_media": synthetic,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func youtubeDashboardCreatedUpload(t *testing.T, response *httptest.ResponseRecorder) YouTubeUpload {
	t.Helper()
	if response.Code != http.StatusAccepted {
		t.Fatalf("create YouTube upload = %d: %s", response.Code, response.Body.String())
	}
	var payload struct {
		Upload YouTubeUpload `json:"upload"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode YouTube upload response: %v (%s)", err, response.Body.String())
	}
	if payload.Upload.ID == "" {
		t.Fatalf("YouTube upload response omitted its ID: %s", response.Body.String())
	}
	return payload.Upload
}

func TestYouTubeDashboardAuthPasswordOriginAndEmbeddedStylesheet(t *testing.T) {
	fixture := newYouTubeDashboardFixture(t)

	for _, path := range []string{
		"/api/youtube/status",
		"/api/youtube/uploads?source_kind=generation&source_id=clip_fixture",
	} {
		response := httptest.NewRecorder()
		fixture.handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusUnauthorized {
			t.Errorf("unauthenticated %s = %d, want %d", path, response.Code, http.StatusUnauthorized)
		}
	}

	asset := httptest.NewRecorder()
	fixture.handler.ServeHTTP(asset, httptest.NewRequest(http.MethodGet, "/static/youtube.css?v=test", nil))
	if asset.Code != http.StatusOK || !strings.Contains(asset.Header().Get("Content-Type"), "text/css") || !strings.Contains(asset.Body.String(), ".youtube-dialog") {
		t.Fatalf("embedded YouTube stylesheet = %d %q", asset.Code, asset.Header().Get("Content-Type"))
	}

	wrongOrigin := httptest.NewRequest(http.MethodPost, "https://dashboard.fixture.example/api/youtube/uploads", strings.NewReader(`{}`))
	wrongOrigin.Header.Set("Content-Type", "application/json")
	wrongOrigin.Header.Set("Origin", "https://attacker.fixture.example")
	wrongOrigin.AddCookie(fixture.cookie)
	originResponse := httptest.NewRecorder()
	fixture.handler.ServeHTTP(originResponse, wrongOrigin)
	if originResponse.Code != http.StatusForbidden {
		t.Fatalf("cross-origin upload = %d: %s", originResponse.Code, originResponse.Body.String())
	}

	if _, err := fixture.store.db.Exec(`UPDATE users SET must_change_password=1 WHERE username='sujanshrestha'`); err != nil {
		t.Fatal(err)
	}
	passwordGate := fixture.request(http.MethodGet, "/api/youtube/status", "", "")
	if passwordGate.Code != http.StatusPreconditionRequired {
		t.Fatalf("YouTube status before password change = %d: %s", passwordGate.Code, passwordGate.Body.String())
	}
}

func TestYouTubeDashboardQueuesCompletedGenerationAndProject(t *testing.T) {
	fixture := newYouTubeDashboardFixture(t)
	fixture.addReadyGeneration(t, "clip_fixture_complete")
	project := fixture.addReadyProject(t, "Synthetic project topic")

	generationBody := youtubeDashboardUploadBody(t, "generation", "clip_fixture_complete", fixture.channel,
		"Synthetic clip title", "Synthetic clip description", "unlisted", true, false)
	generationResponse := fixture.request(http.MethodPost, "/api/youtube/uploads", generationBody, "")
	generation := youtubeDashboardCreatedUpload(t, generationResponse)
	if generation.SourceKind != "generation" || generation.SourceID != "clip_fixture_complete" || generation.ChannelID != fixture.channel ||
		generation.Title != "Synthetic clip title" || generation.Description != "Synthetic clip description" ||
		generation.RequestedPrivacyStatus != "unlisted" || generation.PrivacyStatus != "unlisted" ||
		!generation.MadeForKids || generation.ContainsSyntheticMedia || generation.Status != youtubeUploadQueued {
		t.Fatalf("queued generation upload = %#v", generation)
	}

	projectBody := youtubeDashboardUploadBody(t, "project", project.ID, fixture.channel,
		"Synthetic project title", "Synthetic project description", "public", false, true)
	projectResponse := fixture.request(http.MethodPost, "/api/youtube/uploads", projectBody, "")
	projectUpload := youtubeDashboardCreatedUpload(t, projectResponse)
	if projectUpload.SourceKind != "project" || projectUpload.SourceID != project.ID || projectUpload.ChannelID != fixture.channel ||
		projectUpload.PrivacyStatus != "public" || projectUpload.MadeForKids || !projectUpload.ContainsSyntheticMedia || projectUpload.Status != youtubeUploadQueued {
		t.Fatalf("queued project upload = %#v", projectUpload)
	}

	stored, err := fixture.store.YouTubeUpload(projectUpload.ID)
	if err != nil || stored.SourcePath != project.FinalVideoPath || stored.SourceSize != project.FinalSizeBytes {
		t.Fatalf("queued upload did not capture its completed source: %#v, %v", stored, err)
	}
}

func TestYouTubeDashboardEnforcesUTF8TitleAndDescriptionLimits(t *testing.T) {
	fixture := newYouTubeDashboardFixture(t)
	fixture.addReadyGeneration(t, "clip_fixture_utf8")

	validBody := youtubeDashboardUploadBody(t, "generation", "clip_fixture_utf8", fixture.channel,
		strings.Repeat("界", 100), strings.Repeat("é", 2500), "private", false, true)
	valid := fixture.request(http.MethodPost, "/api/youtube/uploads", validBody, "")
	upload := youtubeDashboardCreatedUpload(t, valid)
	if len([]rune(upload.Title)) != 100 || len([]byte(upload.Description)) != 5000 {
		t.Fatalf("UTF-8 boundary values changed: title runes=%d description bytes=%d", len([]rune(upload.Title)), len([]byte(upload.Description)))
	}

	tests := []struct {
		name        string
		title       string
		description string
	}{
		{name: "title over 100 Unicode code points", title: strings.Repeat("😀", 101)},
		{name: "description over 5000 UTF-8 bytes", title: "Valid title", description: strings.Repeat("é", 2501)},
		{name: "angle brackets", title: "Invalid <title>", description: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := youtubeDashboardUploadBody(t, "generation", "clip_fixture_utf8", fixture.channel,
				test.title, test.description, "private", false, true)
			response := fixture.request(http.MethodPost, "/api/youtube/uploads", body, "")
			if response.Code != http.StatusBadRequest {
				t.Fatalf("invalid metadata = %d: %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestYouTubeDashboardRejectsIncompleteSourcesAndChannelChanges(t *testing.T) {
	fixture := newYouTubeDashboardFixture(t)
	fixture.addIncompleteGeneration(t, "clip_fixture_processing")
	fixture.addReadyGeneration(t, "clip_fixture_channel")
	project, err := fixture.store.InsertProject("Synthetic planning project", "fixture/video")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		sourceKind string
		sourceID   string
		channel    string
		want       int
	}{
		{name: "generation still processing", sourceKind: "generation", sourceID: "clip_fixture_processing", channel: fixture.channel, want: http.StatusConflict},
		{name: "project still planning", sourceKind: "project", sourceID: project.ID, channel: fixture.channel, want: http.StatusConflict},
		{name: "missing source", sourceKind: "generation", sourceID: "clip_fixture_missing", channel: fixture.channel, want: http.StatusNotFound},
		{name: "connected channel changed", sourceKind: "generation", sourceID: "clip_fixture_channel", channel: "UCAnotherSyntheticChannel", want: http.StatusConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := youtubeDashboardUploadBody(t, test.sourceKind, test.sourceID, test.channel,
				"Synthetic title", "Synthetic description", "private", false, true)
			response := fixture.request(http.MethodPost, "/api/youtube/uploads", body, "")
			if response.Code != test.want {
				t.Fatalf("upload invalid source = %d, want %d: %s", response.Code, test.want, response.Body.String())
			}
		})
	}
}

func TestYouTubeDashboardVaultEndpointsRequireCurrentGrant(t *testing.T) {
	fixture := newYouTubeDashboardFixture(t)
	addVaultGeneration(t, fixture.store, "clip_fixture_vault")
	vaultTestSetCode(t, fixture.handler, fixture.cookie, "1462")
	move := fixture.request(http.MethodPost, "/api/vault/items/generation/clip_fixture_vault/move", "", "")
	if move.Code != http.StatusNoContent {
		t.Fatalf("move synthetic video into Vault = %d: %s", move.Code, move.Body.String())
	}

	createBody := youtubeDashboardUploadBody(t, "generation", "clip_fixture_vault", fixture.channel,
		"Synthetic Vault title", "Synthetic Vault description", "private", false, true)
	for _, test := range []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "create", method: http.MethodPost, path: "/api/youtube/uploads", body: createBody},
		{name: "history", method: http.MethodGet, path: "/api/youtube/uploads?source_kind=generation&source_id=clip_fixture_vault"},
		{name: "metadata", method: http.MethodPost, path: "/api/youtube/metadata", body: `{"source_kind":"generation","source_id":"clip_fixture_vault"}`},
	} {
		t.Run("locked/"+test.name, func(t *testing.T) {
			response := fixture.request(test.method, test.path, test.body, "")
			if response.Code != http.StatusLocked {
				t.Fatalf("locked Vault %s = %d: %s", test.name, response.Code, response.Body.String())
			}
		})
	}

	grant := vaultTestUnlock(t, fixture.handler, fixture.cookie, "1462")
	created := youtubeDashboardCreatedUpload(t, fixture.request(http.MethodPost, "/api/youtube/uploads", createBody, grant))
	uploadBase := "/api/youtube/uploads/" + created.ID

	for _, test := range []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "detail", method: http.MethodGet, path: uploadBase},
		{name: "retry", method: http.MethodPost, path: uploadBase + "/retry"},
		{name: "cancel", method: http.MethodPost, path: uploadBase + "/cancel"},
		{name: "restart", method: http.MethodPost, path: uploadBase + "/restart", body: `{"confirm_not_uploaded":true}`},
	} {
		t.Run("locked/"+test.name, func(t *testing.T) {
			response := fixture.request(test.method, test.path, test.body, "")
			if response.Code != http.StatusLocked {
				t.Fatalf("locked Vault %s = %d: %s", test.name, response.Code, response.Body.String())
			}
		})
	}

	listed := fixture.request(http.MethodGet, "/api/youtube/uploads?source_kind=generation&source_id=clip_fixture_vault", "", grant)
	if listed.Code != http.StatusOK {
		t.Fatalf("unlocked Vault upload history = %d: %s", listed.Code, listed.Body.String())
	}
	detail := fixture.request(http.MethodGet, uploadBase, "", grant)
	if detail.Code != http.StatusOK {
		t.Fatalf("unlocked Vault upload detail = %d: %s", detail.Code, detail.Body.String())
	}

	if _, err := fixture.store.db.Exec(`UPDATE youtube_uploads SET status='failed',error='synthetic test failure' WHERE id=?`, created.ID); err != nil {
		t.Fatal(err)
	}
	retry := fixture.request(http.MethodPost, uploadBase+"/retry", "", grant)
	if retry.Code != http.StatusAccepted {
		t.Fatalf("unlocked Vault retry = %d: %s", retry.Code, retry.Body.String())
	}
	cancel := fixture.request(http.MethodPost, uploadBase+"/cancel", "", grant)
	if cancel.Code != http.StatusOK {
		t.Fatalf("unlocked Vault cancel = %d: %s", cancel.Code, cancel.Body.String())
	}
	if _, err := fixture.store.db.Exec(`UPDATE youtube_uploads SET status='attention_required',outcome_uncertain=1,error='synthetic uncertain result' WHERE id=?`, created.ID); err != nil {
		t.Fatal(err)
	}
	restart := fixture.request(http.MethodPost, uploadBase+"/restart", `{"confirm_not_uploaded":true}`, grant)
	if restart.Code != http.StatusAccepted {
		t.Fatalf("unlocked Vault restart = %d: %s", restart.Code, restart.Body.String())
	}
}

func TestYouTubeDashboardBlocksDeletingOrMovingActiveSources(t *testing.T) {
	fixture := newYouTubeDashboardFixture(t)
	fixture.addReadyGeneration(t, "clip_fixture_active")
	project := fixture.addReadyProject(t, "Synthetic active project")

	for _, source := range []struct {
		kind string
		id   string
	}{
		{kind: "generation", id: "clip_fixture_active"},
		{kind: "project", id: project.ID},
	} {
		body := youtubeDashboardUploadBody(t, source.kind, source.id, fixture.channel,
			"Synthetic active title", "Synthetic active description", "private", false, true)
		created := youtubeDashboardCreatedUpload(t, fixture.request(http.MethodPost, "/api/youtube/uploads", body, ""))
		if created.Status != youtubeUploadQueued {
			t.Fatalf("synthetic source did not queue: %#v", created)
		}

		var deletePath string
		if source.kind == "generation" {
			deletePath = "/api/generations/" + source.id
		} else {
			deletePath = "/api/projects/" + source.id
		}
		deleted := fixture.request(http.MethodDelete, deletePath, "", "")
		if deleted.Code != http.StatusConflict {
			t.Fatalf("delete source during queued upload = %d: %s", deleted.Code, deleted.Body.String())
		}

		moved := fixture.request(http.MethodPost, "/api/vault/items/"+source.kind+"/"+source.id+"/move", "", "")
		if moved.Code != http.StatusConflict {
			t.Fatalf("move source during queued upload = %d: %s", moved.Code, moved.Body.String())
		}
	}
}
