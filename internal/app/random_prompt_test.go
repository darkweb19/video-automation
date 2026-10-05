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
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return fn(request) }

func randomPromptTestResponse(t *testing.T, prompt string) *http.Response {
	t.Helper()
	content, err := json.Marshal(map[string]string{"prompt": prompt})
	if err != nil {
		t.Fatal(err)
	}
	return storyRetryResponse(t, http.StatusOK, map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": string(content)}}}})
}

func randomPromptTestClient(transport roundTripFunc) *OpenRouterClient {
	client := NewOpenRouterClient("test-key")
	client.BaseURL = "https://openrouter.test/api/v1"
	client.StoryHTTPClient = &http.Client{Transport: transport}
	return client
}

func randomPromptRequestBody(t *testing.T, request *http.Request) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if request.Method != http.MethodPost || request.URL.Path != "/api/v1/chat/completions" || request.Header.Get("Authorization") != "Bearer test-key" {
		t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
	}
	return body
}

func assertHaikuPromptSchema(t *testing.T, body map[string]any, maxTokens int) {
	t.Helper()
	if body["model"] != ScriptModel || body["max_tokens"] != float64(maxTokens) || body["reasoning"] != nil || body["max_completion_tokens"] != nil {
		t.Fatalf("Haiku request = %#v", body)
	}
	format, ok := body["response_format"].(map[string]any)
	if !ok || format["type"] != "json_schema" {
		t.Fatalf("response format = %#v", format)
	}
	schema := format["json_schema"].(map[string]any)
	if schema["strict"] != true || schema["schema"].(map[string]any)["additionalProperties"] != false {
		t.Fatalf("non-strict schema = %#v", schema)
	}
	if body["provider"].(map[string]any)["require_parameters"] != true {
		t.Fatal("Haiku route permits dropping structured-output parameters")
	}
}

func TestGenerateRandomPromptUsesQueuedTextClient(t *testing.T) {
	if defaultOpenRouterStoryHTTPClient.Timeout <= randomPromptTimeout || randomPromptTimeout != 110*time.Second || randomPromptHaikuReserve < 60*time.Second {
		t.Fatal("random text budgets do not cover the request/fallback")
	}
	if len(randomPromptModels) != 4 {
		t.Fatalf("free model count = %d", len(randomPromptModels))
	}
	seen := make(map[string]bool)
	for _, model := range randomPromptModels {
		if !strings.HasSuffix(model, ":free") || seen[model] {
			t.Fatalf("free model = %q", model)
		}
		seen[model] = true
	}
	client := NewOpenRouterClient("test-key")
	if client.storyClient() != defaultOpenRouterStoryHTTPClient {
		t.Fatal("default text client is not the queued text client")
	}
	client.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("metadata client handled a prompt request")
		return nil, nil
	})}
	client.StoryHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body := randomPromptRequestBody(t, request)
		assertHaikuPromptSchema(t, body, randomProjectTokens)
		return randomPromptTestResponse(t, "A firefly crosses a moonlit forest."), nil
	})}
	prompt, err := client.GenerateRandomPrompt(context.Background(), RandomPromptRequest{Category: RandomPromptCategoryNature, Mode: RandomPromptModeProject})
	if err != nil || prompt != "A firefly crosses a moonlit forest." {
		t.Fatalf("prompt=%q err=%v", prompt, err)
	}
}

func TestGenerateRandomPromptProjectUsesHaikuSingleUsesFree(t *testing.T) {
	for _, mode := range []RandomPromptMode{RandomPromptModeProject, RandomPromptModeSingle} {
		t.Run(string(mode), func(t *testing.T) {
			calls := 0
			client := randomPromptTestClient(func(request *http.Request) (*http.Response, error) {
				calls++
				body := randomPromptRequestBody(t, request)
				messages := body["messages"].([]any)
				if len(messages) != 2 || !strings.Contains(messages[1].(map[string]any)["content"].(string), RandomPromptCategoryNature) {
					t.Fatalf("messages=%#v", messages)
				}
				if mode == RandomPromptModeProject {
					assertHaikuPromptSchema(t, body, randomProjectTokens)
				} else {
					if !strings.HasSuffix(body["model"].(string), ":free") || body["max_completion_tokens"] != float64(randomSingleTokens) || body["response_format"].(map[string]any)["type"] != "json_object" {
						t.Fatalf("free request=%#v", body)
					}
					if body["reasoning"].(map[string]any)["exclude"] != true {
						t.Fatal("free reasoning was not excluded")
					}
				}
				return randomPromptTestResponse(t, "A fox follows a trail of fireflies through a moonlit grove."), nil
			})
			prompt, err := client.GenerateRandomPrompt(context.Background(), RandomPromptRequest{Category: RandomPromptCategoryNature, Mode: mode})
			if err != nil || prompt == "" || calls != 1 {
				t.Fatalf("calls=%d prompt=%q err=%v", calls, prompt, err)
			}
		})
	}
}

func TestGenerateRandomPromptSwitchesDirectlyFromFreeErrorToHaiku(t *testing.T) {
	for _, status := range []int{400, 401, 402, 403, 404, 408, 409, 425, 429, 500, 503} {
		t.Run(fmt.Sprintf("HTTP%d", status), func(t *testing.T) {
			var models []string
			client := randomPromptTestClient(func(request *http.Request) (*http.Response, error) {
				body := randomPromptRequestBody(t, request)
				models = append(models, body["model"].(string))
				if len(models) == 1 {
					return storyRetryResponse(t, status, map[string]any{"error": map[string]any{"message": "model unavailable", "metadata": map[string]string{"provider_name": "Example"}}}), nil
				}
				assertHaikuPromptSchema(t, body, randomSingleTokens)
				return randomPromptTestResponse(t, "A lantern drifts through a snowy village."), nil
			})
			prompt, err := client.GenerateRandomPrompt(context.Background(), RandomPromptRequest{Category: RandomPromptCategoryNature, Mode: RandomPromptModeSingle})
			if err != nil || prompt == "" || len(models) != 2 || !strings.HasSuffix(models[0], ":free") || models[1] != ScriptModel {
				t.Fatalf("models=%v prompt=%q err=%v", models, prompt, err)
			}
		})
	}
}

func TestGenerateRandomPromptTimeoutReservesHaikuBudget(t *testing.T) {
	var models []string
	client := randomPromptTestClient(func(request *http.Request) (*http.Response, error) {
		body := randomPromptRequestBody(t, request)
		models = append(models, body["model"].(string))
		deadline, ok := request.Context().Deadline()
		remaining := time.Until(deadline)
		if !ok {
			t.Fatal("attempt has no deadline")
		}
		if len(models) == 1 {
			if remaining <= 0 || remaining > randomPromptMaxAttemptTime {
				t.Fatalf("free budget = %s", remaining)
			}
			return nil, context.DeadlineExceeded
		}
		if remaining < randomPromptHaikuReserve || remaining > randomPromptTimeout {
			t.Fatalf("Haiku budget = %s", remaining)
		}
		return randomPromptTestResponse(t, "A paper boat reaches a warm streetlamp."), nil
	})
	_, err := client.GenerateRandomPrompt(context.Background(), RandomPromptRequest{Category: RandomPromptCategoryNature, Mode: RandomPromptModeSingle})
	if err != nil || len(models) != 2 || models[1] != ScriptModel {
		t.Fatalf("models=%v err=%v", models, err)
	}
	short, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if budget := randomPromptFreeAttemptBudget(short); budget != 0 {
		t.Fatalf("short caller budget spent on free request: %s", budget)
	}
}

func TestGenerateRandomPromptStopsWhenCallerCancelsAttempt(t *testing.T) {
	requests := 0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := randomPromptTestClient(func(request *http.Request) (*http.Response, error) {
		requests++
		cancel()
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
	_, err := client.GenerateRandomPrompt(ctx, RandomPromptRequest{Category: RandomPromptCategoryNature, Mode: RandomPromptModeSingle})
	if !errors.Is(err, context.Canceled) || requests != 1 {
		t.Fatalf("requests=%d err=%v", requests, err)
	}
}

func TestGenerateRandomPromptFallsBackFromInvalidStructuredResponses(t *testing.T) {
	for _, test := range []struct{ name, body string }{
		{"empty", `{"choices":[{"message":{"content":""}}]}`},
		{"plain text", `{"choices":[{"message":{"content":"A lantern crosses a quiet lake."}}]}`},
		{"unknown field", `{"choices":[{"message":{"content":"{\"prompt\":\"A lantern crosses a lake.\",\"extra\":true}"}}]}`},
		{"null", `{"choices":[{"message":{"content":"{\"prompt\":null}"}}]}`},
		{"truncated", `{"choices":[{"message":{"content":"{\"prompt\":\"A lantern crosses a lake.\"}"},"finish_reason":"length"}]}`},
		{"native max output", `{"choices":[{"message":{"content":"{\"prompt\":\"A lantern crosses a lake.\"}"},"finish_reason":"stop","native_finish_reason":"max_output_tokens"}]}`},
		{"unknown finish", `{"choices":[{"message":{"content":"{\"prompt\":\"A lantern crosses a lake.\"}"},"finish_reason":"unknown"}]}`},
		{"error finish", `{"choices":[{"message":{"content":"{\"prompt\":\"A lantern crosses a lake.\"}"},"finish_reason":"error"}]}`},
		{"refusal field", `{"choices":[{"message":{"refusal":"declined","content":"{\"prompt\":\"A lantern crosses a lake.\"}"}}]}`},
		{"refusal block", `{"choices":[{"message":{"content":[{"type":"text","text":"{\"prompt\":\"A lantern crosses a lake.\"}"},{"type":"refusal","refusal":"declined"}]}}]}`},
		{"refusal text", `{"choices":[{"message":{"content":"{\"prompt\":\"I cannot help with that request.\"}"}}]}`},
		{"analysis", `{"choices":[{"message":{"content":"{\"prompt\":\"Reasoning: I should write a scene.\"}"}}]}`},
		{"embedded provider error", `{"choices":[{"error":{"code":503,"message":"provider unavailable"},"message":{"content":"{\"prompt\":\"A lantern crosses a lake.\"}"}}]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			var models []string
			client := randomPromptTestClient(func(request *http.Request) (*http.Response, error) {
				calls++
				body := randomPromptRequestBody(t, request)
				models = append(models, body["model"].(string))
				if calls == 1 {
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(test.body))}, nil
				}
				return randomPromptTestResponse(t, "A lantern drifts through a snow-covered village."), nil
			})
			prompt, err := client.GenerateRandomPrompt(context.Background(), RandomPromptRequest{Category: RandomPromptCategoryNature, Mode: RandomPromptModeSingle})
			if err != nil || prompt == "" || calls != 2 || models[1] != ScriptModel {
				t.Fatalf("models=%v prompt=%q err=%v", models, prompt, err)
			}
		})
	}
}

func TestGenerateRandomPromptFreeQuotaDoesNotBlockHaiku(t *testing.T) {
	for _, source := range []string{"metadata", "legacy daily", "legacy minute"} {
		t.Run(source, func(t *testing.T) {
			var models []string
			client := randomPromptTestClient(func(request *http.Request) (*http.Response, error) {
				body := randomPromptRequestBody(t, request)
				models = append(models, body["model"].(string))
				if len(models) == 1 {
					limit := map[string]any{"message": "Rate limit exceeded: free-models-per-day."}
					if source == "metadata" {
						limit = map[string]any{"message": "free quota exhausted", "metadata": map[string]string{"limit_source": "openrouter_free_models"}}
					}
					if source == "legacy minute" {
						limit["message"] = "Rate limit exceeded: free-models-per-min."
					}
					response := storyRetryResponse(t, 429, map[string]any{"error": limit})
					response.Header.Set("Retry-After", "7200")
					return response, nil
				}
				return randomPromptTestResponse(t, "A fox reaches a glowing forest clearing."), nil
			})
			for _, mode := range []RandomPromptMode{RandomPromptModeSingle, RandomPromptModeSingle, RandomPromptModeProject} {
				if _, err := client.GenerateRandomPrompt(context.Background(), RandomPromptRequest{Category: RandomPromptCategoryNature, Mode: mode}); err != nil {
					t.Fatal(err)
				}
			}
			if len(models) != 4 || !strings.HasSuffix(models[0], ":free") {
				t.Fatalf("models=%v", models)
			}
			for _, model := range models[1:] {
				if model != ScriptModel {
					t.Fatalf("free quota retried free model: %v", models)
				}
			}
		})
	}
}

func TestGenerateRandomPromptGenericPlatformLimitRemainsShared(t *testing.T) {
	calls := 0
	client := randomPromptTestClient(func(*http.Request) (*http.Response, error) {
		calls++
		response := storyRetryResponse(t, 429, map[string]any{"error": map[string]any{"message": "account rate limited", "metadata": map[string]string{"limit_source": "openrouter"}}})
		response.Header.Set("Retry-After", "7200")
		return response, nil
	})
	for call := 0; call < 2; call++ {
		_, err := client.GenerateRandomPrompt(context.Background(), RandomPromptRequest{Category: RandomPromptCategoryNature, Mode: RandomPromptModeSingle})
		var upstream *upstreamError
		if !errors.As(err, &upstream) || upstream.StatusCode != 429 || upstream.RateLimitScope != "platform" {
			t.Fatalf("err=%v", err)
		}
	}
	if calls != 1 {
		t.Fatalf("generic platform cooldown sent %d requests", calls)
	}
}

func TestGenerateRandomPromptHaikuCooldownStillAllowsFreeSuccess(t *testing.T) {
	calls := 0
	client := randomPromptTestClient(func(request *http.Request) (*http.Response, error) {
		calls++
		body := randomPromptRequestBody(t, request)
		if !strings.HasSuffix(body["model"].(string), ":free") {
			t.Fatal("healthy free generation was blocked by Haiku cooldown")
		}
		return randomPromptTestResponse(t, "A kite rises over a quiet lake."), nil
	})
	key := randomPromptCredentialScope(client.APIKey, client.baseURL())
	client.promptState.setModelCooldown(key, "model:"+ScriptModel, time.Now().Add(2*time.Hour), time.Now())
	if _, err := client.GenerateRandomPrompt(context.Background(), RandomPromptRequest{Category: RandomPromptCategoryNature, Mode: RandomPromptModeSingle}); err != nil {
		t.Fatal(err)
	}
	_, err := client.GenerateRandomPrompt(context.Background(), RandomPromptRequest{Category: RandomPromptCategoryNature, Mode: RandomPromptModeProject})
	var upstream *upstreamError
	if !errors.As(err, &upstream) || upstream.StatusCode != 429 || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	short, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := waitForRandomPromptPlatformCooldown(short, client.promptState, key); err != nil {
		t.Fatalf("paid cooldown blocked free-only metadata helper: %v", err)
	}
}

func TestGenerateRandomPromptModelCooldownPersistsByCredentialScope(t *testing.T) {
	client := randomPromptTestClient(func(request *http.Request) (*http.Response, error) {
		body := randomPromptRequestBody(t, request)
		if strings.HasPrefix(body["model"].(string), "nvidia/") {
			t.Fatal("provider cooldown retried NVIDIA")
		}
		return randomPromptTestResponse(t, "A kite dances above a quiet lake."), nil
	})
	key := randomPromptCredentialScope(client.APIKey, client.baseURL())
	reset := time.Now().Add(2 * time.Hour)
	limited := &upstreamError{StatusCode: 429, RetryAt: reset, RateLimitScope: "provider", ProviderName: "nvidia"}
	client.promptState.setModelCooldown(key, randomPromptModelCooldownKey(randomPromptModels[0], limited), reset, time.Now())
	if _, err := client.GenerateRandomPrompt(context.Background(), RandomPromptRequest{Category: RandomPromptCategoryNature, Mode: RandomPromptModeSingle}); err != nil {
		t.Fatal(err)
	}
	other := client.promptState.cooldownSnapshot(randomPromptCredentialScope("replacement-key", client.baseURL()), time.Now())
	if len(other.models) != 0 || strings.Contains(key, client.APIKey) {
		t.Fatal("credential cooldown scope leaked or reused key state")
	}
}

func TestRandomPromptCooldownStateStaysBoundedAndMonotonic(t *testing.T) {
	state := &randomPromptState{}
	now := time.Now()
	short, long := now.Add(time.Minute), now.Add(2*time.Minute)
	state.setModelCooldown("scope", "model:test", long, now)
	state.setModelCooldown("scope", "model:test", short, now)
	state.setPlatformCooldown("scope", &upstreamError{StatusCode: 429, RetryAt: long}, now)
	state.setPlatformCooldown("scope", &upstreamError{StatusCode: 429, RetryAt: short}, now)
	state.setPlatformCooldown("scope", &upstreamError{StatusCode: 429, RateLimitScope: "platform", LimitSource: "openrouter_free_models", RetryAt: long}, now)
	snapshot := state.cooldownSnapshot("scope", now)
	if !snapshot.models["model:test"].Equal(long) || !snapshot.platformUntil.Equal(long) || !snapshot.freeUntil.Equal(long) {
		t.Fatalf("snapshot=%#v", snapshot)
	}
	for i := 0; i < maxRandomPromptCooldownScopes+10; i++ {
		state.cooldownSnapshot(fmt.Sprintf("credential-%d", i), now.Add(time.Duration(i+1)*time.Second))
	}
	state.mu.Lock()
	count := len(state.scopes)
	state.mu.Unlock()
	if count > maxRandomPromptCooldownScopes {
		t.Fatalf("scope count=%d", count)
	}
}

func TestGenerateRandomPromptTextBlocksAndLaterChoicesRemainStructured(t *testing.T) {
	content := `{"prompt":"A fox follows fireflies through a moonlit grove."}`
	client := randomPromptTestClient(func(*http.Request) (*http.Response, error) {
		return storyRetryResponse(t, 200, map[string]any{"choices": []any{
			map[string]any{"message": map[string]any{"content": ""}},
			map[string]any{"finish_reason": "stop", "message": map[string]any{"content": []any{map[string]string{"type": "image", "image_url": "ignored"}, map[string]string{"type": "text", "text": content[:len(content)/2]}, map[string]string{"type": "text", "text": content[len(content)/2:]}}}},
		}}), nil
	})
	prompt, err := client.GenerateRandomPrompt(context.Background(), RandomPromptRequest{Category: RandomPromptCategoryNature, Mode: RandomPromptModeSingle})
	if err != nil || prompt != "A fox follows fireflies through a moonlit grove." {
		t.Fatalf("prompt=%q err=%v", prompt, err)
	}
}

func TestGenerateRandomPromptRejectsInvalidInputAndOversizedProjectIdea(t *testing.T) {
	client := NewOpenRouterClient("test-key")
	for _, input := range []RandomPromptRequest{{Category: "unknown", Mode: RandomPromptModeSingle}, {Category: RandomPromptCategoryNature, Mode: "other"}} {
		if _, err := client.GenerateRandomPrompt(context.Background(), input); err == nil {
			t.Fatalf("accepted input=%#v", input)
		}
	}
	calls := 0
	client = randomPromptTestClient(func(*http.Request) (*http.Response, error) {
		calls++
		return randomPromptTestResponse(t, strings.Repeat("x", maxRandomProjectTopicRunes+1)), nil
	})
	prompt, err := client.GenerateRandomPrompt(context.Background(), RandomPromptRequest{Category: RandomPromptCategoryNature, Mode: RandomPromptModeProject})
	if err == nil || prompt != "" || calls != randomProjectPromptAttempts {
		t.Fatalf("oversized idea was silently truncated: prompt=%q err=%v calls=%d", prompt, err, calls)
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
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": `{"prompt":"A waterfall reveals a hidden glowing cave at dawn."}`}}}})
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
