package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type mockProvider struct {
	models        []VideoModel
	modelsErr     error
	generation    *Generation
	generateErr   error
	status        *Generation
	statusErr     error
	generateCalls int
	statusID      string
	content       *http.Response
	contentErr    error
	contentID     string
	rangeHeader   string
}

func (m *mockProvider) GetVideoContent(_ context.Context, id, rangeHeader string) (*http.Response, error) {
	m.contentID = id
	m.rangeHeader = rangeHeader
	return m.content, m.contentErr
}

func (m *mockProvider) GenerateVideo(_ context.Context, request GenerateRequest) (*Generation, error) {
	m.generateCalls++
	return m.generation, m.generateErr
}

func (m *mockProvider) GetGeneration(_ context.Context, id string) (*Generation, error) {
	m.statusID = id
	return m.status, m.statusErr
}

func (m *mockProvider) ListVideoModels(context.Context) ([]VideoModel, error) {
	return m.models, m.modelsErr
}

func testHandler(provider VideoProvider) http.Handler {
	return NewHandler(provider, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func request(handler http.Handler, method, target, body string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	handler.ServeHTTP(recorder, req)
	return recorder
}

func validModels() []VideoModel {
	return []VideoModel{{ID: "google/veo", Name: "Veo", Durations: []int{4, 8}, AspectRatios: []string{"9:16", "16:9"}}}
}

func TestHealth(t *testing.T) {
	response := request(testHandler(&mockProvider{}), http.MethodGet, "/health", "")
	if response.Code != http.StatusOK || response.Body.String() != "{\"status\":\"ok\"}\n" {
		t.Fatalf("health response = %d %q", response.Code, response.Body.String())
	}
}

func TestGenerateValidRequest(t *testing.T) {
	provider := &mockProvider{models: validModels(), generation: &Generation{ID: "gen_123", Status: "queued"}}
	response := request(testHandler(provider), http.MethodPost, "/generate", `{"prompt":"night city","model":"google/veo","duration":8,"aspect_ratio":"9:16"}`)
	if response.Code != http.StatusAccepted || provider.generateCalls != 1 {
		t.Fatalf("generate = %d, calls = %d: %s", response.Code, provider.generateCalls, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"id":"gen_123"`) {
		t.Fatalf("unexpected response: %s", response.Body.String())
	}
}

func TestGenerateValidation(t *testing.T) {
	cases := []struct {
		name string
		body string
		code int
	}{
		{"empty prompt", `{"prompt":" ","model":"google/veo"}`, http.StatusBadRequest},
		{"missing model", `{"prompt":"hello"}`, http.StatusBadRequest},
		{"malformed JSON", `{`, http.StatusBadRequest},
		{"unknown field", `{"prompt":"hello","model":"google/veo","extra":true}`, http.StatusBadRequest},
		{"trailing JSON", `{"prompt":"hello","model":"google/veo"} {}`, http.StatusBadRequest},
		{"unsupported duration", `{"prompt":"hello","model":"google/veo","duration":12}`, http.StatusBadRequest},
		{"unsupported ratio", `{"prompt":"hello","model":"google/veo","aspect_ratio":"1:1"}`, http.StatusBadRequest},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			provider := &mockProvider{models: validModels(), generation: &Generation{ID: "gen_123"}}
			response := request(testHandler(provider), http.MethodPost, "/generate", test.body)
			if response.Code != test.code || provider.generateCalls != 0 {
				t.Fatalf("generate = %d, calls = %d: %s", response.Code, provider.generateCalls, response.Body.String())
			}
		})
	}
}

func TestGenerateOversizedBody(t *testing.T) {
	provider := &mockProvider{models: validModels()}
	body := `{"prompt":"` + strings.Repeat("x", MaxJSONBodyBytes) + `","model":"google/veo"}`
	response := request(testHandler(provider), http.MethodPost, "/generate", body)
	if response.Code != http.StatusRequestEntityTooLarge || provider.generateCalls != 0 {
		t.Fatalf("generate = %d, calls = %d", response.Code, provider.generateCalls)
	}
}

func TestGenerateMethodAndUpstreamFailure(t *testing.T) {
	handler := testHandler(&mockProvider{models: validModels(), generateErr: errors.New("upstream rejected")})
	if response := request(handler, http.MethodGet, "/generate", ""); response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /generate = %d", response.Code)
	}
	response := request(handler, http.MethodPost, "/generate", `{"prompt":"hello","model":"google/veo"}`)
	if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), "OpenRouter rejected") {
		t.Fatalf("upstream response = %d %s", response.Code, response.Body.String())
	}
}

func TestGenerateRejectsOptionsWithoutCapabilities(t *testing.T) {
	provider := &mockProvider{models: []VideoModel{{ID: "google/veo", Name: "Veo"}}}
	response := request(testHandler(provider), http.MethodPost, "/generate", `{"prompt":"hello","model":"google/veo","duration":8}`)
	if response.Code != http.StatusBadRequest || provider.generateCalls != 0 {
		t.Fatalf("response = %d, calls = %d", response.Code, provider.generateCalls)
	}
}

func TestStatus(t *testing.T) {
	provider := &mockProvider{status: &Generation{ID: "gen_123", Status: "processing"}}
	handler := testHandler(provider)
	if response := request(handler, http.MethodGet, "/status", ""); response.Code != http.StatusBadRequest {
		t.Fatalf("missing id = %d", response.Code)
	}
	response := request(handler, http.MethodGet, "/status?id=gen_123", "")
	if response.Code != http.StatusOK || provider.statusID != "gen_123" {
		t.Fatalf("status = %d id = %q", response.Code, provider.statusID)
	}
}

func TestStatusUpstreamFailure(t *testing.T) {
	provider := &mockProvider{statusErr: errors.New("upstream unavailable")}
	response := request(testHandler(provider), http.MethodGet, "/status?id=gen_123", "")
	if response.Code != http.StatusBadGateway {
		t.Fatalf("status = %d", response.Code)
	}
}

func TestModels(t *testing.T) {
	response := request(testHandler(&mockProvider{models: validModels()}), http.MethodGet, "/models", "")
	if response.Code != http.StatusOK {
		t.Fatalf("models = %d: %s", response.Code, response.Body.String())
	}
	var payload struct {
		Models          []VideoModel `json:"models"`
		MaxPromptLength int          `json:"max_prompt_length"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil || len(payload.Models) != 1 || payload.MaxPromptLength != MaxPromptLength {
		t.Fatalf("models payload = %#v, error = %v", payload, err)
	}
}

func TestModelsUpstreamFailure(t *testing.T) {
	response := request(testHandler(&mockProvider{modelsErr: errors.New("unavailable")}), http.MethodGet, "/models", "")
	if response.Code != http.StatusBadGateway {
		t.Fatalf("models = %d", response.Code)
	}
}

func TestStaticAssets(t *testing.T) {
	handler := testHandler(&mockProvider{})
	for _, target := range []string{"/", "/static/app.js?v=test", "/static/styles.css?v=test", "/static/model-picker.css?v=test"} {
		response := request(handler, http.MethodGet, target, "")
		if response.Code != http.StatusOK || response.Body.Len() == 0 {
			t.Fatalf("GET %s = %d (%d bytes)", target, response.Code, response.Body.Len())
		}
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("GET %s cache control = %q", target, response.Header().Get("Cache-Control"))
		}
	}
}

func TestCrossOriginGenerateRejected(t *testing.T) {
	provider := &mockProvider{models: validModels()}
	handler := testHandler(provider)
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/generate", bytes.NewBufferString(`{"prompt":"hello","model":"google/veo"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://elsewhere.example")
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusForbidden || provider.generateCalls != 0 {
		t.Fatalf("response = %d calls = %d", recorder.Code, provider.generateCalls)
	}
}

func TestVideoProxyForwardsRangeAndHeaders(t *testing.T) {
	provider := &mockProvider{content: &http.Response{
		StatusCode: http.StatusPartialContent,
		Header: http.Header{
			"Content-Type":   []string{"video/mp4"},
			"Content-Range":  []string{"bytes 0-4/5"},
			"Accept-Ranges":  []string{"bytes"},
			"Content-Length": []string{"5"},
		},
		Body: io.NopCloser(strings.NewReader("video")),
	}}
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/video?id=job_123", nil)
	req.Header.Set("Range", "bytes=0-4")
	testHandler(provider).ServeHTTP(recorder, req)

	if recorder.Code != http.StatusPartialContent || recorder.Body.String() != "video" {
		t.Fatalf("video response = %d %q", recorder.Code, recorder.Body.String())
	}
	if provider.contentID != "job_123" || provider.rangeHeader != "bytes=0-4" {
		t.Fatalf("content call = id %q, range %q", provider.contentID, provider.rangeHeader)
	}
	if recorder.Header().Get("Content-Type") != "video/mp4" || recorder.Header().Get("Content-Range") != "bytes 0-4/5" {
		t.Fatalf("video headers = %#v", recorder.Header())
	}
}

func TestVideoProxyFailure(t *testing.T) {
	provider := &mockProvider{contentErr: errors.New("content unavailable")}
	response := request(testHandler(provider), http.MethodGet, "/video?id=job_123", "")
	if response.Code != http.StatusBadGateway {
		t.Fatalf("video failure = %d: %s", response.Code, response.Body.String())
	}
}

func TestVideoProxyPreservesRangeNotSatisfiable(t *testing.T) {
	provider := &mockProvider{content: &http.Response{
		StatusCode: http.StatusRequestedRangeNotSatisfiable,
		Header:     http.Header{"Content-Range": []string{"bytes */5"}},
		Body:       io.NopCloser(strings.NewReader("")),
	}}
	response := request(testHandler(provider), http.MethodGet, "/video?id=job_123", "")
	if response.Code != http.StatusRequestedRangeNotSatisfiable || response.Header().Get("Content-Range") != "bytes */5" {
		t.Fatalf("range response = %d, headers = %#v", response.Code, response.Header())
	}
}
