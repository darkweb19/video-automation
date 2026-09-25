package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGenerateRandomPromptUsesLingModelForBothModes(t *testing.T) {
	for _, test := range []struct {
		name       string
		input      RandomPromptRequest
		content    string
		want       string
		maxTokens  float64
		userPrefix string
	}{
		{
			name:       "project topic",
			input:      RandomPromptRequest{Category: RandomPromptCategoryNature, Mode: RandomPromptModeProject},
			content:    `"A firefly guides a lost traveler through a moonlit forest."`,
			want:       "A firefly guides a lost traveler through a moonlit forest.",
			maxTokens:  randomProjectTokens,
			userPrefix: "Generate one original topic",
		},
		{
			name:       "single prompt",
			input:      RandomPromptRequest{Category: RandomPromptCategoryHorrorStory, Mode: RandomPromptModeSingle},
			content:    "A lone adult hiker crosses a foggy forest trail at dusk, handheld camera slowly pushes forward as branches bend, ending on a distant cabin in vertical 9:16.",
			want:       "A lone adult hiker crosses a foggy forest trail at dusk, handheld camera slowly pushes forward as branches bend, ending on a distant cabin in vertical 9:16.",
			maxTokens:  randomSingleTokens,
			userPrefix: "Generate one original single-clip video prompt",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/chat/completions" {
					t.Fatalf("request = %s %s", r.Method, r.URL.Path)
				}
				if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
					t.Fatalf("authorization = %q", got)
				}
				var body struct {
					Model    string `json:"model"`
					Messages []struct {
						Content string `json:"content"`
					} `json:"messages"`
					MaxTokens float64 `json:"max_completion_tokens"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if body.Model != "inclusionai/ling-3.0-flash-fin:free" || body.MaxTokens != test.maxTokens || len(body.Messages) != 2 || !strings.HasPrefix(body.Messages[1].Content, test.userPrefix) || !strings.Contains(body.Messages[1].Content, test.input.Category) {
					t.Fatalf("unexpected request body: %#v", body)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": test.content}}}})
			}))
			defer server.Close()

			client := testClient(server)
			prompt, err := client.GenerateRandomPrompt(context.Background(), test.input)
			if err != nil || prompt != test.want {
				t.Fatalf("prompt = %q, error = %v", prompt, err)
			}
		})
	}
}

func TestGenerateRandomPromptRejectsInvalidInputAndLongProjectTopic(t *testing.T) {
	client := NewOpenRouterClient("test-key")
	if _, err := client.GenerateRandomPrompt(context.Background(), RandomPromptRequest{Category: "unknown", Mode: RandomPromptModeSingle}); err == nil {
		t.Fatal("expected invalid category error")
	}
	if _, err := client.GenerateRandomPrompt(context.Background(), RandomPromptRequest{Category: RandomPromptCategoryNature, Mode: "other"}); err == nil {
		t.Fatal("expected invalid mode error")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": strings.Repeat("x", maxRandomProjectTopicRunes+1)}}}})
	}))
	defer server.Close()
	client.BaseURL = server.URL
	client.HTTPClient = server.Client()
	if _, err := client.GenerateRandomPrompt(context.Background(), RandomPromptRequest{Category: RandomPromptCategoryNature, Mode: RandomPromptModeProject}); err == nil || !strings.Contains(err.Error(), "project topic exceeds") {
		t.Fatalf("expected project topic limit error, got %v", err)
	}
}

func TestRandomPromptAPIAuthenticationValidationAndSecretHandling(t *testing.T) {
	store := newTestStore(t)
	provisionTestUser(t, store)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	const apiKey = "test-openrouter-secret"
	encrypted, err := security.EncryptSetting(apiKeySetting, apiKey)
	if err != nil || store.SetSetting(apiKeySetting, encrypted) != nil {
		t.Fatal("save encrypted API key")
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+apiKey {
			t.Fatalf("upstream authorization = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": "A waterfall reveals a hidden glowing cave at dawn."}}}})
	}))
	defer upstream.Close()
	app := &dashboardApp{store: store, security: security, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), baseURL: upstream.URL}
	handler := app.requirePasswordChanged(http.HandlerFunc(app.randomPrompt))

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, "/api/prompts/random", strings.NewReader(`{"category":"Nature","mode":"single"}`)))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", unauthorized.Code)
	}

	session := httptest.NewRecorder()
	if err := security.NewSession(session, httptest.NewRequest(http.MethodPost, "/", nil), "sujanshrestha"); err != nil {
		t.Fatal(err)
	}
	cookie := session.Result().Cookies()[0]
	invalid := httptest.NewRecorder()
	invalidRequest := httptest.NewRequest(http.MethodPost, "/api/prompts/random", strings.NewReader(`{"category":"Unknown","mode":"single","extra":true}`))
	invalidRequest.Header.Set("Content-Type", "application/json")
	invalidRequest.AddCookie(cookie)
	handler.ServeHTTP(invalid, invalidRequest)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid input status = %d: %s", invalid.Code, invalid.Body.String())
	}

	success := httptest.NewRecorder()
	successRequest := httptest.NewRequest(http.MethodPost, "/api/prompts/random", strings.NewReader(`{"category":"Nature","mode":"single"}`))
	successRequest.Header.Set("Content-Type", "application/json")
	successRequest.AddCookie(cookie)
	handler.ServeHTTP(success, successRequest)
	if success.Code != http.StatusOK || !strings.Contains(success.Body.String(), `"prompt":"A waterfall`) || strings.Contains(success.Body.String(), apiKey) {
		t.Fatalf("success = %d: %s", success.Code, success.Body.String())
	}

	crossOrigin := httptest.NewRecorder()
	crossOriginRequest := httptest.NewRequest(http.MethodPost, "/api/prompts/random", strings.NewReader(`{"category":"Nature","mode":"single"}`))
	crossOriginRequest.Header.Set("Content-Type", "application/json")
	crossOriginRequest.Header.Set("Origin", "https://not-this-host.example")
	crossOriginRequest.AddCookie(cookie)
	handler.ServeHTTP(crossOrigin, crossOriginRequest)
	if crossOrigin.Code != http.StatusForbidden {
		t.Fatalf("cross-origin status = %d", crossOrigin.Code)
	}
}

func TestRandomPromptCategoriesAreExact(t *testing.T) {
	for _, category := range []string{
		RandomPromptCategoryKidAnimation,
		RandomPromptCategoryHorrorStory,
		RandomPromptCategoryNature,
		RandomPromptCategorySeduction,
		RandomPromptCategoryMatureContent,
		RandomPromptCategorySoftCorn,
	} {
		if !validRandomPromptCategory(category) {
			t.Fatalf("category %q should be supported", category)
		}
	}
	if validRandomPromptCategory("soft corn") {
		t.Fatal("categories must remain exact")
	}
}

func TestSafeRandomPromptFailureExplainsActionableUpstreamErrors(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{context.DeadlineExceeded, "timed out"},
		{&upstreamError{StatusCode: http.StatusUnauthorized}, "Update it in Settings"},
		{&upstreamError{StatusCode: http.StatusTooManyRequests}, "rate-limited"},
		{&upstreamError{StatusCode: http.StatusBadGateway}, "HTTP 502"},
	}
	for _, test := range tests {
		if got := safeRandomPromptFailure(test.err); !strings.Contains(got, test.want) {
			t.Fatalf("safeRandomPromptFailure(%v) = %q, want substring %q", test.err, got, test.want)
		}
	}
}
