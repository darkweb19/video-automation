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
	if randomPromptTimeout > 110*time.Second || randomPromptTimeout < 100*time.Second {
		t.Fatalf("prompt operation budget = %s, want at most 110 seconds with room for queued models", randomPromptTimeout)
	}
	if randomPromptMinAttemptTime <= 0 || randomPromptMaxAttemptTime < randomPromptMinAttemptTime {
		t.Fatalf("invalid adaptive attempt range: min=%s max=%s", randomPromptMinAttemptTime, randomPromptMaxAttemptTime)
	}
	budgetContext, cancelBudget := context.WithTimeout(context.Background(), randomPromptTimeout)
	initialAttemptBudget := randomPromptAttemptBudget(budgetContext, len(randomPromptModels))
	cancelBudget()
	if initialAttemptBudget <= 12*time.Second || initialAttemptBudget > randomPromptMaxAttemptTime {
		t.Fatalf("initial adaptive model window = %s, want more than 12 seconds and at most %s", initialAttemptBudget, randomPromptMaxAttemptTime)
	}
	if len(randomPromptModels) != 5 {
		t.Fatalf("random prompt model count = %d, want five", len(randomPromptModels))
	}
	seenModels := make(map[string]bool, len(randomPromptModels))
	for _, model := range randomPromptModels {
		if !strings.HasSuffix(model, ":free") || seenModels[model] {
			t.Fatalf("random prompt model %q is not a unique free variant", model)
		}
		seenModels[model] = true
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

func TestGenerateRandomPromptUsesFreeModelForBothModes(t *testing.T) {
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
		{
			name:       "mature project topic",
			input:      RandomPromptRequest{Category: RandomPromptCategoryMatureContent, Mode: RandomPromptModeProject},
			content:    "A confident adult dancer in a red bikini playfully sways beneath neon lights, her bare back framed by the mirrorball glow.",
			want:       "A confident adult dancer in a red bikini playfully sways beneath neon lights, her bare back framed by the mirrorball glow.",
			maxTokens:  randomProjectTokens,
			userPrefix: "Generate one original bold, sensual, non-explicit adult video idea",
			systemText: "daring two-piece, bikini, or exotic lingerie",
		},
		{
			name:       "mature single prompt",
			input:      RandomPromptRequest{Category: RandomPromptCategoryMatureContent, Mode: RandomPromptModeSingle},
			content:    "A clearly adult woman in an opaque black lingerie set turns to show her bare back, then gives a teasing hip sway under warm amber light, six-second slow push-in, vertical 9:16.",
			want:       "A clearly adult woman in an opaque black lingerie set turns to show her bare back, then gives a teasing hip sway under warm amber light, six-second slow push-in, vertical 9:16.",
			maxTokens:  randomSingleTokens,
			userPrefix: "Generate one original bold, sensual, non-explicit adult single-clip prompt",
			systemText: "no nudity, visible nipples, genitalia",
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
					Reasoning struct {
						Effort  string `json:"effort"`
						Exclude bool   `json:"exclude"`
					} `json:"reasoning"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				modelIsFree := false
				for _, model := range randomPromptModels {
					if body.Model == model {
						modelIsFree = true
						break
					}
				}
				if !modelIsFree || body.MaxTokens != test.maxTokens || body.Reasoning.Effort != "minimal" || !body.Reasoning.Exclude || len(body.Messages) != 2 || !strings.HasPrefix(body.Messages[1].Content, test.userPrefix) || !strings.Contains(body.Messages[1].Content, test.input.Category) || !strings.Contains(body.Messages[0].Content, test.systemText) {
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

func TestGenerateRandomPromptFallsBackAcrossShuffledFreeModels(t *testing.T) {
	var requestedModels []string
	client := NewOpenRouterClient("test-key")
	client.BaseURL = "https://openrouter.test/api/v1"
	client.HTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			return nil, err
		}
		requestedModels = append(requestedModels, body.Model)
		deadline, ok := request.Context().Deadline()
		remaining := time.Until(deadline)
		if !ok || remaining > randomPromptMaxAttemptTime || remaining <= 12*time.Second {
			t.Errorf("attempt deadline = %v, present = %v, remaining = %v", deadline, ok, remaining)
		}
		if len(requestedModels) < len(randomPromptModels) {
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"temporary model outage"}}`)),
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"A lantern drifts through a snowy village."}}]}`)),
		}, nil
	})}

	prompt, err := client.GenerateRandomPrompt(context.Background(), RandomPromptRequest{Category: RandomPromptCategoryNature, Mode: RandomPromptModeProject})
	if err != nil || prompt != "A lantern drifts through a snowy village." {
		t.Fatalf("fallback prompt = %q, error = %v", prompt, err)
	}
	if len(requestedModels) != len(randomPromptModels) {
		t.Fatalf("requested %d models, want all %d: %v", len(requestedModels), len(randomPromptModels), requestedModels)
	}
	seen := make(map[string]bool, len(requestedModels))
	for _, model := range requestedModels {
		if !strings.HasSuffix(model, ":free") || seen[model] {
			t.Fatalf("fallback requested non-free or repeated model %q in %v", model, requestedModels)
		}
		seen[model] = true
	}
	for _, configured := range randomPromptModels {
		if !seen[configured] {
			t.Fatalf("fallback omitted %q: %v", configured, requestedModels)
		}
	}
}

func TestGenerateRandomPromptDoesNotRetryAuthOrInvalidRequests(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusBadRequest} {
		t.Run(fmt.Sprintf("HTTP %d", status), func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests++
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"error":{"message":"request rejected"}}`)
			}))
			defer server.Close()

			client := testClient(server)
			_, err := client.GenerateRandomPrompt(context.Background(), RandomPromptRequest{Category: RandomPromptCategoryNature, Mode: RandomPromptModeSingle})
			var upstream *upstreamError
			if !errors.As(err, &upstream) || upstream.StatusCode != status {
				t.Fatalf("generation error = %v, want upstream HTTP %d", err, status)
			}
			if requests != 1 {
				t.Fatalf("sent %d requests after HTTP %d, want one", requests, status)
			}
		})
	}
}

func TestGenerateRandomPromptStopsWhenCallerCancelsAttempt(t *testing.T) {
	requests := 0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := NewOpenRouterClient("test-key")
	client.BaseURL = "https://openrouter.test/api/v1"
	client.HTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		cancel()
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}
	_, err := client.GenerateRandomPrompt(ctx, RandomPromptRequest{Category: RandomPromptCategoryNature, Mode: RandomPromptModeProject})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("generation error = %v, want caller cancellation", err)
	}
	if requests != 1 {
		t.Fatalf("sent %d requests after caller cancellation, want one", requests)
	}
}

func TestGenerateRandomPromptFallsThroughQuotedEmptyContent(t *testing.T) {
	requests := 0
	client := NewOpenRouterClient("test-key")
	client.BaseURL = "https://openrouter.test/api/v1"
	client.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests++
		content := `{"choices":[{"message":{"content":"\"\""}}]}`
		if requests > 1 {
			content = `{"choices":[{"message":{"content":"A red kite circles above a quiet beach."}}]}`
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(content)),
		}, nil
	})}

	prompt, err := client.GenerateRandomPrompt(context.Background(), RandomPromptRequest{Category: RandomPromptCategoryNature, Mode: RandomPromptModeSingle})
	if err != nil || prompt != "A red kite circles above a quiet beach." {
		t.Fatalf("fallback prompt = %q, error = %v", prompt, err)
	}
	if requests != 2 {
		t.Fatalf("sent %d requests for quoted empty content, want one fallback", requests)
	}
}

func TestGenerateRandomPromptFallsBackFromMalformedRefusedAndTruncatedOutput(t *testing.T) {
	badResponses := []struct {
		name   string
		status int
		body   string
	}{
		{
			name: "truncated completion",
			body: `{"choices":[{"message":{"content":"A hiker starts walking through a forest but"},"finish_reason":"length"}]}`,
		},
		{
			name: "error finish reason",
			body: `{"choices":[{"message":{"content":"A valid-looking but incomplete scene."},"finish_reason":"error"}]}`,
		},
		{
			name: "refusal finish reason",
			body: `{"choices":[{"message":{"content":"A valid-looking scene."},"finish_reason":"refusal"}]}`,
		},
		{
			name: "refusal text",
			body: `{"choices":[{"message":{"content":"I cannot help with that request."}}]}`,
		},
		{
			name: "explicit refusal field",
			body: `{"choices":[{"message":{"refusal":"policy refusal","content":"A valid but refused prompt."}}]}`,
		},
		{
			name: "reasoning text",
			body: `{"choices":[{"message":{"content":"Reasoning: I should turn this into a forest scene."}}]}`,
		},
		{
			name: "reasoning only",
			body: `{"choices":[{"message":{"content":"<think>Consider what would make a good prompt.</think>"}}]}`,
		},
		{
			name: "structured content",
			body: `{"choices":[{"message":{"content":"{\"prompt\":\"A lantern crosses a snowy village.\"}"}}]}`,
		},
		{
			name: "HTTP 200 embedded error",
			body: `{"error":{"code":429,"message":"Provider limit","metadata":{"provider_name":"Example","limit_source":"upstream_provider_shared_pool"}}}`,
		},
		{
			name:   "moderation error tries another model",
			status: http.StatusForbidden,
			body:   `{"error":{"message":"Content policy violation","metadata":{"error_type":"content_policy_violation"}}}`,
		},
	}
	for _, test := range badResponses {
		t.Run(test.name, func(t *testing.T) {
			requests := 0
			client := NewOpenRouterClient("test-key")
			client.BaseURL = "https://openrouter.test/api/v1"
			client.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				requests++
				content := test.body
				if requests > 1 {
					content = `{"choices":[{"message":{"content":"A lantern drifts past a snow-covered village at dusk."}}]}`
				}
				status := test.status
				if status == 0 || requests > 1 {
					status = http.StatusOK
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(content))}, nil
			})}

			prompt, err := client.GenerateRandomPrompt(context.Background(), RandomPromptRequest{Category: RandomPromptCategoryNature, Mode: RandomPromptModeSingle})
			if err != nil || prompt != "A lantern drifts past a snow-covered village at dusk." {
				t.Fatalf("fallback prompt = %q, error = %v", prompt, err)
			}
			if requests != 2 {
				t.Fatalf("sent %d requests for unusable output, want one free-model fallback", requests)
			}
		})
	}
}

func TestGenerateRandomPromptModelCooldownPersistsByCredentialScope(t *testing.T) {
	client := NewOpenRouterClient("first-key")
	client.BaseURL = "https://openrouter.test/api/v1"
	limitedModel := "nvidia/nemotron-3.5-lightning:free"
	stateKey := randomPromptCredentialScope(client.APIKey, client.baseURL())
	reset := time.Now().Add(2 * time.Minute)
	cooldownError := &upstreamError{StatusCode: http.StatusTooManyRequests, RetryAt: reset, RateLimitScope: "provider", ProviderName: "nvidia"}
	client.promptState.setModelCooldown(stateKey, randomPromptModelCooldownKey(limitedModel, cooldownError), reset, time.Now())

	var requestedModels []string
	client.HTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			return nil, err
		}
		requestedModels = append(requestedModels, body.Model)
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"A kite dances above a quiet lake."}}]}`))}, nil
	})}
	if _, err := client.GenerateRandomPrompt(context.Background(), RandomPromptRequest{Category: RandomPromptCategoryNature, Mode: RandomPromptModeProject}); err != nil {
		t.Fatal(err)
	}
	if len(requestedModels) != 1 || strings.HasPrefix(requestedModels[0], "nvidia/") {
		t.Fatalf("requested model during NVIDIA cooldown: %v", requestedModels)
	}

	otherCredentialKey := randomPromptCredentialScope("replacement-key", client.baseURL())
	otherScope := client.promptState.cooldownSnapshot(otherCredentialKey, time.Now())
	if len(otherScope.models) != 0 {
		t.Fatalf("credential replacement reused old cooldowns: %#v", otherScope.models)
	}
	if strings.Contains(stateKey, client.APIKey) {
		t.Fatal("random prompt cooldown scope retained a raw API key")
	}
}

func TestGenerateRandomPromptSkipsModelAfterRateLimitAcrossCalls(t *testing.T) {
	client := NewOpenRouterClient("test-key")
	client.BaseURL = "https://openrouter.test/api/v1"
	var requestedModels []string
	client.HTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			return nil, err
		}
		requestedModels = append(requestedModels, body.Model)
		if len(requestedModels) == 1 {
			header := make(http.Header)
			header.Set("Retry-After", "120")
			return &http.Response{StatusCode: http.StatusTooManyRequests, Header: header, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"upstream model busy"}}`))}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"A fox follows lanterns through a snowy wood."}}]}`))}, nil
	})}
	input := RandomPromptRequest{Category: RandomPromptCategoryNature, Mode: RandomPromptModeProject}
	for call := 0; call < 2; call++ {
		if _, err := client.GenerateRandomPrompt(context.Background(), input); err != nil {
			t.Fatalf("generation %d: %v", call+1, err)
		}
	}
	if len(requestedModels) != 3 {
		t.Fatalf("sent %d model requests, want first-model fallback plus one cooldown-skipping call: %v", len(requestedModels), requestedModels)
	}
	if requestedModels[0] == requestedModels[2] {
		t.Fatalf("rate-limited model was retried in the next call: %v", requestedModels)
	}
}

func TestGenerateRandomPromptRefreshesSharedPlatformCooldownBeforeEachAttempt(t *testing.T) {
	client := NewOpenRouterClient("test-key")
	client.BaseURL = "https://openrouter.test/api/v1"
	stateKey := randomPromptCredentialScope(client.APIKey, client.baseURL())
	reset := time.Now().Add(2 * time.Hour)
	var requests int
	client.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests++
		if requests == 1 {
			// Simulate another in-flight dashboard request learning the shared
			// platform reset while this request is waiting on its model response.
			client.promptState.setPlatformCooldown(stateKey, &upstreamError{
				StatusCode:     http.StatusTooManyRequests,
				Message:        "shared free-model limit",
				RetryAt:        reset,
				RateLimitScope: "platform",
			}, time.Now())
			return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"message":"temporary model outage"}}`))}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"A lantern crosses a quiet lake."}}]}`))}, nil
	})}

	_, err := client.GenerateRandomPrompt(context.Background(), RandomPromptRequest{Category: RandomPromptCategoryNature, Mode: RandomPromptModeProject})
	var upstream *upstreamError
	if !errors.As(err, &upstream) || upstream.StatusCode != http.StatusTooManyRequests || upstream.RateLimitScope != "platform" || !upstream.RetryAt.Equal(reset) {
		t.Fatalf("generation error = %v, want shared platform limit until %s", err, reset)
	}
	if requests != 1 {
		t.Fatalf("sent %d requests after shared cooldown appeared, want only the in-flight request", requests)
	}
}

func TestGenerateRandomPromptSuppressesPlatformLimitWithoutInventingReset(t *testing.T) {
	client := NewOpenRouterClient("test-key")
	client.BaseURL = "https://openrouter.test/api/v1"
	var requests int
	client.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests++
		header := make(http.Header)
		header.Set("X-RateLimit-Remaining", "0")
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"shared free-model limit"}}`)),
		}, nil
	})}
	for attempt := 0; attempt < 2; attempt++ {
		_, err := client.GenerateRandomPrompt(context.Background(), RandomPromptRequest{Category: RandomPromptCategoryNature, Mode: RandomPromptModeProject})
		var upstream *upstreamError
		if !errors.As(err, &upstream) || upstream.StatusCode != http.StatusTooManyRequests || upstream.RateLimitScope != "platform" || !upstream.RetryAt.IsZero() {
			t.Fatalf("generation error = %v, want platform limit without a fabricated reset", err)
		}
	}
	if requests != 1 {
		t.Fatalf("sent %d requests while reset time is unknown, want one suppressed request", requests)
	}
}

func TestRandomPromptCooldownStateStaysBoundedAndMonotonic(t *testing.T) {
	state := &randomPromptState{}
	now := time.Now()
	short := now.Add(time.Minute)
	long := now.Add(2 * time.Minute)
	state.setModelCooldown("scope", "model:test", long, now)
	state.setModelCooldown("scope", "model:test", short, now)
	state.setPlatformCooldown("scope", &upstreamError{StatusCode: 429, RetryAt: long}, now)
	state.setPlatformCooldown("scope", &upstreamError{StatusCode: 429, RetryAt: short}, now)
	snapshot := state.cooldownSnapshot("scope", now)
	if !snapshot.models["model:test"].Equal(long) || !snapshot.platformUntil.Equal(long) {
		t.Fatalf("shorter cooldown overwrote a longer reset: %#v", snapshot)
	}
	for index := 0; index < maxRandomPromptCooldownScopes+10; index++ {
		state.cooldownSnapshot(fmt.Sprintf("credential-%d", index), now.Add(time.Duration(index+1)*time.Second))
	}
	state.mu.Lock()
	scopeCount := len(state.scopes)
	state.mu.Unlock()
	if scopeCount > maxRandomPromptCooldownScopes {
		t.Fatalf("cooldown state retained %d credential scopes, limit is %d", scopeCount, maxRandomPromptCooldownScopes)
	}
}

func TestGenerateRandomPromptFallsBackFromOversizedSinglePrompt(t *testing.T) {
	requests := 0
	client := NewOpenRouterClient("test-key")
	client.BaseURL = "https://openrouter.test/api/v1"
	client.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests++
		content := strings.Repeat("x", MaxPromptLength+1)
		if requests > 1 {
			content = "A single clip follows a paper boat downstream in the rain, ending beneath a warm streetlamp, vertical 9:16."
		}
		body := `{"choices":[{"message":{"content":"` + content + `"}}]}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	})}

	prompt, err := client.GenerateRandomPrompt(context.Background(), RandomPromptRequest{Category: RandomPromptCategoryNature, Mode: RandomPromptModeSingle})
	if err != nil || prompt == "" || utf8.RuneCountInString(prompt) > MaxPromptLength {
		t.Fatalf("single prompt = %q (%d runes), error = %v", prompt, utf8.RuneCountInString(prompt), err)
	}
	if requests != 2 {
		t.Fatalf("sent %d requests for oversized single prompt, want one fallback", requests)
	}
}

func TestGenerateRandomPromptReadsTextBlocksAndFallsThroughEmptyChoices(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":""}},{"message":{"content":[{"type":"image","image_url":"ignored"},{"type":"text","text":"A confident adult woman in an opaque red bikini turns to reveal her bare back"},{"type":"text","text":" under golden studio light, vertical 9:16."}]}}]}`)
	}))
	defer server.Close()
	client := testClient(server)
	prompt, err := client.GenerateRandomPrompt(context.Background(), RandomPromptRequest{Category: RandomPromptCategoryMatureContent, Mode: RandomPromptModeSingle})
	want := "A confident adult woman in an opaque red bikini turns to reveal her bare back under golden studio light, vertical 9:16."
	if err != nil || prompt != want {
		t.Fatalf("prompt = %q, error = %v", prompt, err)
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
