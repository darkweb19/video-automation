package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestGenerateRandomPromptUsesQueuedTextClientOnFirstRequest(t *testing.T) {
	if defaultOpenRouterStoryHTTPClient.Timeout <= randomPromptTimeout {
		t.Fatalf("text client timeout %s does not cover the %s prompt budget", defaultOpenRouterStoryHTTPClient.Timeout, randomPromptTimeout)
	}
	if randomPromptTimeout >= 2*time.Minute {
		t.Fatalf("prompt budget %s exceeds server write deadline", randomPromptTimeout)
	}
	client := NewOpenRouterClient("test-key")
	if client.storyClient() != defaultOpenRouterStoryHTTPClient {
		t.Fatal("default prompt client does not allow queued text responses")
	}
	client.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("ordinary 45-second metadata client handled prompt request")
		return nil, nil
	})}
	client.StoryHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/api/v1/chat/completions" {
			t.Fatalf("prompt request path = %q", request.URL.Path)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"A firefly crosses a moonlit forest."}}]}`)),
		}, nil
	})}
	ctx, cancel := context.WithTimeout(context.Background(), randomPromptTimeout)
	defer cancel()
	prompt, err := client.GenerateRandomPrompt(ctx, RandomPromptRequest{Category: RandomPromptCategoryNature, Mode: RandomPromptModeProject})
	if err != nil || prompt != "A firefly crosses a moonlit forest." {
		t.Fatalf("first prompt = %q, error = %v", prompt, err)
	}
}

func TestGenerateRandomPromptUsesLingModelForBothModes(t *testing.T) {
	for _, test := range []struct {
		name       string
		input      RandomPromptRequest
		content    string
		want       string
		maxTokens  float64
		userPrefix string
		systemText string
	}{
		{
			name:       "project topic",
			input:      RandomPromptRequest{Category: RandomPromptCategoryNature, Mode: RandomPromptModeProject},
			content:    `"A firefly guides a lost traveler through a moonlit forest."`,
			want:       "A firefly guides a lost traveler through a moonlit forest.",
			maxTokens:  randomProjectTokens,
			userPrefix: "Generate one original topic",
			systemText: "at most 200 characters",
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
				if body.Model != "inclusionai/ling-3.0-flash-fin:free" || body.MaxTokens != test.maxTokens || len(body.Messages) != 2 || !strings.HasPrefix(body.Messages[1].Content, test.userPrefix) || !strings.Contains(body.Messages[1].Content, test.input.Category) || !strings.Contains(body.Messages[0].Content, test.systemText) {
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

func TestGenerateRandomPromptRejectsInvalidInputAndFitsLongProjectTopic(t *testing.T) {
	client := NewOpenRouterClient("test-key")
	if _, err := client.GenerateRandomPrompt(context.Background(), RandomPromptRequest{Category: "unknown", Mode: RandomPromptModeSingle}); err == nil {
		t.Fatal("expected invalid category error")
	}
	if _, err := client.GenerateRandomPrompt(context.Background(), RandomPromptRequest{Category: RandomPromptCategoryNature, Mode: "other"}); err == nil {
		t.Fatal("expected invalid mode error")
	}
	for _, count := range []int{maxRandomProjectTopicRunes + 1, MaxPromptLength + 1} {
		t.Run(fmt.Sprintf("%d runes", count), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": strings.Repeat("x", count)}}}})
			}))
			defer server.Close()
			client.BaseURL = server.URL
			client.HTTPClient = server.Client()
			prompt, err := client.GenerateRandomPrompt(context.Background(), RandomPromptRequest{Category: RandomPromptCategoryNature, Mode: RandomPromptModeProject})
			if err != nil || utf8.RuneCountInString(prompt) > maxRandomProjectTopicRunes || !strings.HasSuffix(prompt, "…") {
				t.Fatalf("long project topic = %q (%d runes), error = %v", prompt, utf8.RuneCountInString(prompt), err)
			}
		})
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
