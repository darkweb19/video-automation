package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

type youtubeBackendFixture struct {
	store    *Store
	security *Security
	app      *dashboardApp
	upload   YouTubeUpload
	source   youtubeSource
}

func newYouTubeBackendFixture(t *testing.T, handler http.Handler) (*youtubeBackendFixture, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	const sourceID = "youtube_backend_source"
	if err := store.InsertGeneration(GenerationRecord{ID: sourceID, Prompt: "Quiet forest at dawn", Model: "test/model", Status: "queued"}); err != nil {
		t.Fatal(err)
	}
	content := []byte("framevault-test-video-content")
	path := store.VideoPath(sourceID)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkVideoReady(sourceID, path, int64(len(content))); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	secretCipher, err := security.EncryptSetting("youtube_client_secret", "backend-test-client-secret")
	if err != nil {
		t.Fatal(err)
	}
	accessCipher, err := security.EncryptSetting("youtube_access_token", "backend-test-access-token")
	if err != nil {
		t.Fatal(err)
	}
	refreshCipher, err := security.EncryptSetting("youtube_refresh_token", "backend-test-refresh-token")
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.db.Exec(`UPDATE youtube_config SET client_id='backend-test-client',encrypted_client_secret=?,encrypted_access_token=?,encrypted_refresh_token=?,access_token_expires_at=?,channel_id='backend-test-channel',channel_title='Backend Test Channel',connection_version=1,reconnect_required=0 WHERE id=1`, secretCipher, accessCipher, refreshCipher, time.Now().Add(time.Hour).Unix())
	if err != nil {
		t.Fatal(err)
	}
	client := server.Client()
	client.Timeout = 5 * time.Second
	app := &dashboardApp{
		store: store, security: security,
		logger:               slog.New(slog.NewTextHandler(io.Discard, nil)),
		youtubeHTTPClient:    client,
		youtubeDataAPIOrigin: server.URL,
	}
	source := youtubeSource{Kind: "generation", ID: sourceID, Title: "Quiet forest at dawn", Prompt: "Quiet forest at dawn", Model: "test/model", Path: path, Size: int64(len(content)), ModTimeNS: info.ModTime().UnixNano()}
	upload, err := newYouTubeUploadID()
	if err != nil {
		t.Fatal(err)
	}
	queued := YouTubeUpload{
		ID: upload, SourceKind: source.Kind, SourceID: source.ID, ChannelID: "backend-test-channel", ConnectionVersion: 1,
		Title: "A quiet forest", Description: "A silent forest at sunrise.", RequestedPrivacyStatus: "public", PrivacyStatus: "public",
		MadeForKids: false, ContainsSyntheticMedia: false, Status: youtubeUploadQueued,
		SourcePath: source.Path, SourceSize: source.Size, SourceModTimeNS: source.ModTimeNS,
	}
	if err := store.createYouTubeUpload(queued); err != nil {
		t.Fatal(err)
	}
	queued, err = store.YouTubeUpload(upload)
	if err != nil {
		t.Fatal(err)
	}
	return &youtubeBackendFixture{store: store, security: security, app: app, upload: queued, source: source}, server
}

func (f *youtubeBackendFixture) processor() *Processor {
	return &Processor{
		app: f.app, logger: f.app.logger,
		youtubeSem: make(chan struct{}, 1), inFlight: make(map[string]struct{}),
	}
}

func TestYouTubeUploadResumesStoredGoogleSessionAndStreamsRemainingBytes(t *testing.T) {
	video := []byte("framevault-test-video-content")
	var initiations, probes, chunks int
	var sessionURL string
	serverHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/upload/youtube/v3/videos":
			initiations++
			if r.URL.Query().Get("uploadType") != "resumable" || r.URL.Query().Get("part") != "snippet,status" {
				t.Errorf("initiation query = %q", r.URL.RawQuery)
			}
			if r.Header.Get("Content-Type") != "application/json" || r.Header.Get("X-Upload-Content-Type") != "video/mp4" {
				t.Errorf("initiation content headers = %#v", r.Header)
			}
			var payload youtubeInitiateRequest
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("decode initiation body: %v", err)
			}
			if payload.Snippet.Title != "A quiet forest" || payload.Status.PrivacyStatus != "public" {
				t.Errorf("initiation payload = %#v", payload)
			}
			sessionURL = serverLocation(r, "uploadType=resumable&upload_id=opaque-resumable-capability&part=snippet%2Cstatus%2CcontentDetails")
			w.Header().Set("Location", sessionURL)
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPut && r.URL.Path == "/upload/youtube/v3/videos":
			if r.URL.Query().Get("upload_id") != "opaque-resumable-capability" || r.URL.Query().Get("part") != "snippet,status,contentDetails" {
				t.Errorf("session query = %q", r.URL.RawQuery)
			}
			if r.Header.Get("Authorization") != "Bearer backend-test-access-token" {
				t.Errorf("authorization header = %q", r.Header.Get("Authorization"))
			}
			contentRange := r.Header.Get("Content-Range")
			if strings.HasPrefix(contentRange, "bytes */") {
				probes++
				body, _ := io.ReadAll(r.Body)
				if len(body) != 0 || r.Header.Get("Content-Type") != "video/mp4" {
					t.Errorf("probe body/content type = %q / %q", body, r.Header.Get("Content-Type"))
				}
				w.Header().Set("Range", "bytes=0-0")
				w.WriteHeader(http.StatusPermanentRedirect)
				return
			}
			chunks++
			if contentRange != "bytes 1-"+strconvItoa(len(video)-1)+"/"+strconvItoa(len(video)) {
				t.Errorf("chunk Content-Range = %q", contentRange)
			}
			if r.Header.Get("Content-Type") != "video/mp4" || r.ContentLength != int64(len(video)-1) {
				t.Errorf("chunk headers/content length = %q / %d", r.Header.Get("Content-Type"), r.ContentLength)
			}
			body, _ := io.ReadAll(r.Body)
			if string(body) != string(video[1:]) {
				t.Errorf("streamed bytes = %q, want remaining source %q", body, video[1:])
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"youtube-video-123","status":{"privacyStatus":"private","uploadStatus":"uploaded"}}`)
		default:
			t.Errorf("unexpected fake YouTube request %s %s", r.Method, r.URL.String())
			http.NotFound(w, r)
		}
	})
	fixture, _ := newYouTubeBackendFixture(t, serverHandler)
	checkedFile, sourceErr := youtubeUploadSourceFile(fixture.store, fixture.upload)
	if sourceErr != nil {
		info, _ := os.Stat(fixture.upload.SourcePath)
		baseResolved, baseErr := filepath.EvalSymlinks(fixture.store.videoDir)
		pathResolved, pathErr := filepath.EvalSymlinks(fixture.upload.SourcePath)
		t.Fatalf("validate test source: %v (stored size/time=%d/%d, current=%v; base=%q/%v source=%q/%v)", sourceErr, fixture.upload.SourceSize, fixture.upload.SourceModTimeNS, info, baseResolved, baseErr, pathResolved, pathErr)
	}
	_ = checkedFile.Close()
	processor := fixture.processor()
	processor.processYouTubeUpload(context.Background(), fixture.upload)
	started, err := fixture.store.YouTubeUpload(fixture.upload.ID)
	if err != nil {
		t.Fatal(err)
	}
	if started.Status != youtubeUploadSending || started.EncryptedSessionURL == "" || started.EncryptedSessionURL == sessionURL {
		t.Fatalf("persisted initiation = %#v", started)
	}
	processor = fixture.processor() // simulate a process restart between initiation and transfer
	processor.processYouTubeUpload(context.Background(), started)
	completedTransfer, err := fixture.store.YouTubeUpload(fixture.upload.ID)
	if err != nil {
		t.Fatal(err)
	}
	if initiations != 1 || probes != 1 || chunks != 1 {
		t.Fatalf("initiation/probe/chunk calls = %d/%d/%d", initiations, probes, chunks)
	}
	if completedTransfer.Status != youtubeUploadProcessing || completedTransfer.YouTubeVideoID != "youtube-video-123" || completedTransfer.PrivacyStatus != "private" || completedTransfer.Progress != 100 {
		t.Fatalf("confirmed upload = %#v", completedTransfer)
	}
	publicJSON, err := json.Marshal(completedTransfer)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(publicJSON), "opaque-resumable-capability") || strings.Contains(string(publicJSON), "encrypted_session_url") {
		t.Fatalf("public upload exposes resumable capability: %s", publicJSON)
	}
}

func serverLocation(request *http.Request, query string) string {
	return "http://" + request.Host + "/upload/youtube/v3/videos?" + query
}

func strconvItoa(value int) string {
	return fmt.Sprintf("%d", value)
}

func TestYouTubeSessionValidationAllowsGoogleDocumentedPartParameter(t *testing.T) {
	app := &dashboardApp{youtubeDataAPIOrigin: "https://www.googleapis.com"}
	valid := "https://www.googleapis.com/upload/youtube/v3/videos?uploadType=resumable&upload_id=capability&part=snippet%2Cstatus%2CcontentDetails"
	if !app.validYouTubeUploadSessionURL(valid) {
		t.Fatal("documented resumable Location with part parameter was rejected")
	}
	for _, invalid := range []string{
		"https://attacker.example/upload/youtube/v3/videos?uploadType=resumable&upload_id=capability",
		"https://www.googleapis.com/upload/youtube/v3/videos?uploadType=resumable&upload_id=capability&redirect=https://attacker.example",
		"https://www.googleapis.com/upload/youtube/v3/videos?uploadType=resumable&upload_id=capability&part=snippet%2Cunknown",
	} {
		if app.validYouTubeUploadSessionURL(invalid) {
			t.Errorf("accepted invalid upload session URL %q", invalid)
		}
	}
}

func TestYouTubeProcessingAndQueuedRetriesAreBounded(t *testing.T) {
	fixture, _ := newYouTubeBackendFixture(t, http.NotFoundHandler())
	processor := fixture.processor()
	queued := fixture.upload
	processor.retryYouTubeLater(queued, 0)
	backedOff, err := fixture.store.YouTubeUpload(queued.ID)
	if err != nil {
		t.Fatal(err)
	}
	if backedOff.Status != youtubeUploadQueued || backedOff.AttemptCount != 1 || backedOff.NextAttemptAt <= time.Now().Unix() {
		t.Fatalf("queued transient retry was not backed off: %#v", backedOff)
	}
	if _, err := fixture.store.db.Exec(`UPDATE youtube_uploads SET status='processing',youtube_video_id='known-video',attempt_count=?,processing_check_count=3 WHERE id=?`, youtubeTransientAttemptLimit-1, queued.ID); err != nil {
		t.Fatal(err)
	}
	processing, err := fixture.store.YouTubeUpload(queued.ID)
	if err != nil {
		t.Fatal(err)
	}
	processor.retryYouTubeProcessingCheck(processing, "checking")
	bounded, err := fixture.store.YouTubeUpload(queued.ID)
	if err != nil {
		t.Fatal(err)
	}
	if bounded.Status != youtubeUploadAttentionRequired || bounded.YouTubeVideoID != "known-video" {
		t.Fatalf("processing retry bound lost the confirmed remote video: %#v", bounded)
	}
}

func TestYouTubeProcessingStoresEffectivePrivacyFromGoogle(t *testing.T) {
	fixture, _ := newYouTubeBackendFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/youtube/v3/videos" || r.URL.Query().Get("id") != "known-video" {
			t.Errorf("processing request = %s %s", r.Method, r.URL.String())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"items":[{"id":"known-video","status":{"uploadStatus":"processed","privacyStatus":"private"},"processingDetails":{"processingStatus":"succeeded"}}]}`)
	}))
	if _, err := fixture.store.db.Exec(`UPDATE youtube_uploads SET status='uploading' WHERE id=?`, fixture.upload.ID); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.completeYouTubeUpload(fixture.upload.ID, "known-video", "public", time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	processor := fixture.processor()
	processing, err := fixture.store.YouTubeUpload(fixture.upload.ID)
	if err != nil {
		t.Fatal(err)
	}
	processor.pollYouTubeProcessing(context.Background(), processing, false)
	completed, err := fixture.store.YouTubeUpload(fixture.upload.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != youtubeUploadCompleted || completed.PrivacyStatus != "private" || completed.YouTubeVideoID != "known-video" {
		t.Fatalf("processed YouTube result = %#v", completed)
	}
}

func TestYouTubeMetadataUsesFreeModelsWithoutUnsupportedJSONMode(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode OpenRouter request: %v", err)
		}
		model, _ := payload["model"].(string)
		if !strings.HasSuffix(model, ":free") {
			t.Errorf("metadata used non-free model %q", model)
		}
		if _, exists := payload["response_format"]; exists {
			t.Error("metadata request sent response_format, unsupported by some free models")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"{\"title\":\"Dawn in the Forest\",\"description\":\"A quiet forest wakes in the first light.\"}"}}]}`)
	}))
	defer server.Close()
	client := NewOpenRouterClient("test-openrouter-key")
	client.BaseURL = server.URL
	client.StoryHTTPClient = server.Client()
	client.promptState = &randomPromptState{}
	result, err := client.generateYouTubeMetadata(context.Background(), youtubeSource{Title: "A forest at dawn", Prompt: "A forest at dawn"})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || result.Title != "Dawn in the Forest" || result.Description != "A quiet forest wakes in the first light." {
		t.Fatalf("metadata calls/result = %d / %#v", calls, result)
	}
}

func TestYouTubeMetadataStopsAfterInvalidOpenRouterCredentials(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"invalid key","code":401}}`)
	}))
	defer server.Close()
	client := NewOpenRouterClient("test-invalid-key")
	client.BaseURL = server.URL
	client.StoryHTTPClient = server.Client()
	client.promptState = &randomPromptState{}
	_, err := client.generateYouTubeMetadata(context.Background(), youtubeSource{Title: "A forest at dawn"})
	var upstream *upstreamError
	if !errors.As(err, &upstream) || upstream.StatusCode != http.StatusUnauthorized {
		t.Fatalf("metadata error = %v, want unauthorized upstream error", err)
	}
	if calls != 1 {
		t.Fatalf("sent %d OpenRouter requests after invalid credentials, want 1", calls)
	}
}

func TestYouTubeSourceDeletionGuardEndsAfterVideoIDIsKnown(t *testing.T) {
	activeFixture, _ := newYouTubeBackendFixture(t, http.NotFoundHandler())
	if _, err := activeFixture.store.BeginDelete(activeFixture.source.ID); !errors.Is(err, ErrYouTubeUploadInUse) {
		t.Fatalf("delete with queued upload error = %v, want upload guard", err)
	}

	finishedFixture, _ := newYouTubeBackendFixture(t, http.NotFoundHandler())
	if _, err := finishedFixture.store.db.Exec(`UPDATE youtube_uploads SET status='attention_required',youtube_video_id='known-video' WHERE id=?`, finishedFixture.upload.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := finishedFixture.store.BeginDelete(finishedFixture.source.ID); err != nil {
		t.Fatalf("delete after remote video ID is known: %v", err)
	}
}
