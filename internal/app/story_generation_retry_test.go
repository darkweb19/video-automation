package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func storyRetryResponse(t *testing.T, status int, body any) *http.Response {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(encoded)))}
}

func storyRetryPlanResponse(t *testing.T, plan StoryPlan, extraChoiceFields map[string]any) *http.Response {
	t.Helper()
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	choice := map[string]any{"message": map[string]any{"content": string(raw)}, "finish_reason": "stop"}
	for key, value := range extraChoiceFields {
		choice[key] = value
	}
	return storyRetryResponse(t, http.StatusOK, map[string]any{"model": ScriptModel, "choices": []any{choice}})
}

func storyRetryClient(transport roundTripFunc) *OpenRouterClient {
	client := NewOpenRouterClient("story-test-secret")
	client.BaseURL = "https://openrouter.test/api/v1"
	client.HTTPClient = &http.Client{Transport: transport}
	return client
}

func TestGenerateStoryPlanRetriesInvalidOutputWithPlainJSON(t *testing.T) {
	requests := 0
	client := storyRetryClient(func(request *http.Request) (*http.Response, error) {
		requests++
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		assertHaikuPromptSchema(t, body, storyPlanCompletionTokens)
		if body["tools"] != nil || body["tool_choice"] != nil {
			t.Fatal("story request should use strict JSON output")
		}
		deadline, ok := request.Context().Deadline()
		if !ok || time.Until(deadline) > storyGenerationTimeout {
			t.Fatalf("attempt deadline = %v", deadline)
		}
		plan := validStoryPlan()
		if requests == 1 {
			plan.Scenes = plan.Scenes[:4]
		}
		return storyRetryPlanResponse(t, plan, nil), nil
	})
	plan, trace, err := client.GenerateStoryPlan(context.Background(), "a five-scene rescue")
	if err != nil || requests != 2 || len(plan.Scenes) != ProjectSceneCount || trace.Attempts != 2 || trace.Status != "completed" || trace.Error != "" || !strings.Contains(trace.UserPrompt, "Do not call tools") {
		t.Fatalf("requests=%d plan=%+v trace=%+v err=%v", requests, plan, trace, err)
	}
	if storyGenerationOperationTimeout != 6*time.Minute+5*time.Second || trace.ActualModel != ScriptModel || !strings.Contains(trace.RawResponse, `"number":5`) {
		t.Fatalf("operation budget or final audit mismatch: timeout=%s trace=%+v", storyGenerationOperationTimeout, trace)
	}
}

func TestGenerateStoryPlanRejectsIncompleteFinishReasons(t *testing.T) {
	for _, field := range []string{"finish_reason", "native_finish_reason"} {
		for _, reason := range []string{"length", "error", "content_filter", "refusal"} {
			t.Run(field+"/"+reason, func(t *testing.T) {
				requests := 0
				client := storyRetryClient(func(request *http.Request) (*http.Response, error) {
					requests++
					var body map[string]any
					_ = json.NewDecoder(request.Body).Decode(&body)
					if body["model"] != ScriptModel {
						t.Fatalf("retry model = %v", body["model"])
					}
					if requests == 1 {
						return storyRetryPlanResponse(t, validStoryPlan(), map[string]any{field: reason}), nil
					}
					return storyRetryPlanResponse(t, validStoryPlan(), nil), nil
				})
				_, trace, err := client.GenerateStoryPlan(context.Background(), "complete story")
				if err != nil || requests != 2 || trace.Attempts != 2 || trace.Status != "completed" {
					t.Fatalf("requests=%d trace=%+v err=%v", requests, trace, err)
				}
			})
		}
	}
}

func TestGenerateStoryPlanConfigurationAndFatalErrorsDoNotWeakenSchema(t *testing.T) {
	for _, test := range []struct {
		name    string
		status  int
		message string
		want    int
	}{
		{"unsupported schema", http.StatusBadRequest, "This endpoint does not support response_format", 1},
		{"no structured endpoint", http.StatusNotFound, "No endpoints found that support structured outputs.", 1},
		{"missing model", http.StatusNotFound, "Model not found", 1},
		{"invalid request", http.StatusBadRequest, "Invalid request story-test-secret", 1},
		{"auth", http.StatusUnauthorized, "Invalid key story-test-secret", 1},
		{"permissions", http.StatusForbidden, "Forbidden story-test-secret", 1},
		{"credits", http.StatusPaymentRequired, "Credits rejected story-test-secret", 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			requests := 0
			client := storyRetryClient(func(request *http.Request) (*http.Response, error) {
				requests++
				if requests > 1 {
					var body map[string]any
					_ = json.NewDecoder(request.Body).Decode(&body)
					if body["tools"] != nil || body["tool_choice"] != nil {
						t.Fatalf("unsupported tools retry still required tools: %v", body)
					}
					return storyRetryPlanResponse(t, validStoryPlan(), nil), nil
				}
				return storyRetryResponse(t, test.status, map[string]any{"error": map[string]any{"message": test.message, "metadata": map[string]string{"raw": "Bearer story-test-secret"}}}), nil
			})
			_, trace, err := client.GenerateStoryPlan(context.Background(), "five scenes")
			if requests != test.want || trace.Attempts != test.want || (err == nil) != (test.want == 2) || strings.Contains(trace.Error, "story-test-secret") {
				t.Fatalf("requests=%d trace=%+v err=%v", requests, trace, err)
			}
			if err != nil && (trace.Status != "failed" || trace.RawResponse != "") {
				t.Fatalf("failed trace retained upstream error data: %+v", trace)
			}
		})
	}
}

func TestSafeTextGenerationFailureMakesUnavailableModelActionable(t *testing.T) {
	err := &upstreamError{StatusCode: http.StatusNotFound, Message: "Model not found; API key: story-test-secret"}
	message := safeTextGenerationFailure(err)
	if !strings.Contains(message, ScriptModel) || !strings.Contains(message, "HTTP 404") || !strings.Contains(message, "model availability") {
		t.Fatalf("missing-model failure is not actionable: %q", message)
	}
	if strings.Contains(message, "story-test-secret") || strings.Contains(message, "API key") {
		t.Fatalf("provider detail leaked a credential: %q", message)
	}
}

func TestGenerateStoryPlanEmbeddedPlatformLimitPreservesCooldown(t *testing.T) {
	reset := time.Now().Add(24 * time.Hour).Truncate(time.Millisecond)
	requests := 0
	client := storyRetryClient(func(*http.Request) (*http.Response, error) {
		requests++
		return storyRetryResponse(t, http.StatusOK, map[string]any{"error": map[string]any{
			"code": "429", "message": "Rate limit exceeded: account-per-day.",
			"metadata": map[string]any{"headers": map[string]string{"X-RateLimit-Reset": strconv.FormatInt(reset.UnixMilli(), 10)}, "raw": "story-test-secret"},
		}}), nil
	})
	for attempt := 0; attempt < 2; attempt++ {
		_, trace, err := client.GenerateStoryPlan(context.Background(), "story")
		var upstream *upstreamError
		if !errors.As(err, &upstream) || upstream.StatusCode != http.StatusTooManyRequests || upstream.RateLimitScope != "platform" || !upstream.RetryAt.Equal(reset) || trace.Status != "failed" || trace.RawResponse != "" || strings.Contains(trace.Error, "story-test-secret") {
			t.Fatalf("trace=%+v err=%#v", trace, err)
		}
		if trace.Attempts != 1-attempt {
			t.Fatalf("call %d sent %d attempts", attempt, trace.Attempts)
		}
	}
	if requests != 1 {
		t.Fatalf("shared daily quota generated %d requests", requests)
	}
}

func TestGenerateStoryPlanInvalidAttemptsRetainSafeAudit(t *testing.T) {
	requests := 0
	client := storyRetryClient(func(*http.Request) (*http.Response, error) {
		requests++
		plan := validStoryPlan()
		plan.Title = "story-test-secret invalid plan"
		plan.Scenes = plan.Scenes[:4]
		return storyRetryPlanResponse(t, plan, nil), nil
	})
	_, trace, err := client.GenerateStoryPlan(context.Background(), "five scenes")
	if err == nil || requests != 2 || trace.Attempts != 2 || trace.Status != "failed" || !strings.Contains(trace.Error, "invalid five-scene") || trace.RawResponse == "" || strings.Contains(trace.RawResponse, client.APIKey) || !strings.Contains(trace.RawResponse, "[redacted]") {
		t.Fatalf("requests=%d trace=%+v err=%v", requests, trace, err)
	}
}

func TestGenerateStoryPlanReadsLaterCompleteChoice(t *testing.T) {
	planJSON, _ := json.Marshal(validStoryPlan())
	requests := 0
	client := storyRetryClient(func(*http.Request) (*http.Response, error) {
		requests++
		return storyRetryResponse(t, http.StatusOK, map[string]any{"model": ScriptModel, "choices": []any{
			map[string]any{"finish_reason": "length", "message": map[string]any{"content": string(planJSON)}},
			map[string]any{"finish_reason": "stop", "native_finish_reason": "unknown_success_value", "message": map[string]any{"content": []any{map[string]string{"type": "text", "text": string(planJSON)}}}},
		}}), nil
	})
	_, trace, err := client.GenerateStoryPlan(context.Background(), "five scenes")
	if err != nil || requests != 1 || trace.Attempts != 1 || trace.RawResponse != string(planJSON) {
		t.Fatalf("requests=%d trace=%+v err=%v", requests, trace, err)
	}
}

func TestGenerateStoryPlanPlatformLimitWithoutResetSuppressesRepeatedCalls(t *testing.T) {
	requests := 0
	client := storyRetryClient(func(*http.Request) (*http.Response, error) {
		requests++
		return storyRetryResponse(t, http.StatusTooManyRequests, map[string]any{"error": map[string]any{"message": "Account rate limit exceeded", "metadata": map[string]string{"limit_source": "openrouter"}}}), nil
	})
	for call := 0; call < 2; call++ {
		_, trace, err := client.GenerateStoryPlan(context.Background(), "five scenes")
		var upstream *upstreamError
		if !errors.As(err, &upstream) || upstream.StatusCode != http.StatusTooManyRequests || !upstream.RetryAt.IsZero() || trace.Attempts != 1-call {
			t.Fatalf("call=%d requests=%d trace=%+v err=%v", call, requests, trace, err)
		}
	}
	if requests != 1 {
		t.Fatalf("known platform limit without a reset sent %d calls", requests)
	}
}

func TestGenerateStoryPlanRetryHonorsResetAndCancellation(t *testing.T) {
	t.Run("provider reset", func(t *testing.T) {
		reset := time.Now().Add(200 * time.Millisecond).Truncate(time.Millisecond)
		requests := 0
		client := storyRetryClient(func(*http.Request) (*http.Response, error) {
			requests++
			if requests == 1 {
				response := storyRetryResponse(t, http.StatusTooManyRequests, map[string]any{"error": map[string]any{"metadata": map[string]string{"provider_name": "Example"}}})
				response.Header.Set("Retry-After", reset.UTC().Format(http.TimeFormat))
				// Milliseconds preserve a short deterministic test wait.
				response.Header.Set("X-RateLimit-Reset", strconv.FormatInt(reset.UnixMilli(), 10))
				return response, nil
			}
			if time.Now().Before(reset) {
				t.Fatal("retried before reset")
			}
			return storyRetryPlanResponse(t, validStoryPlan(), nil), nil
		})
		_, trace, err := client.GenerateStoryPlan(context.Background(), "story")
		if err != nil || requests != 2 || trace.Attempts != 2 {
			t.Fatalf("requests=%d trace=%+v err=%v", requests, trace, err)
		}
	})
	t.Run("caller cancellation", func(t *testing.T) {
		requests := 0
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		client := storyRetryClient(func(*http.Request) (*http.Response, error) {
			requests++
			cancel()
			return storyRetryResponse(t, http.StatusServiceUnavailable, nil), nil
		})
		_, trace, err := client.GenerateStoryPlan(ctx, "story")
		if !errors.Is(err, context.Canceled) || requests != 1 || trace.Attempts != 1 || trace.Status != "failed" {
			t.Fatalf("requests=%d trace=%+v err=%v", requests, trace, err)
		}
	})
	t.Run("reset beyond caller budget", func(t *testing.T) {
		requests := 0
		client := storyRetryClient(func(*http.Request) (*http.Response, error) {
			requests++
			response := storyRetryResponse(t, http.StatusTooManyRequests, nil)
			response.Header.Set("Retry-After", "60")
			return response, nil
		})
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, trace, err := client.GenerateStoryPlan(ctx, "story")
		var upstream *upstreamError
		if !errors.As(err, &upstream) || upstream.StatusCode != http.StatusTooManyRequests || requests != 1 || trace.Attempts != 1 {
			t.Fatalf("requests=%d trace=%+v err=%v", requests, trace, err)
		}
	})
}

func TestStoryResponseStrictlyRejectsAmbiguousOrInvalidJSON(t *testing.T) {
	plan := validStoryPlan()
	encoded, _ := json.Marshal(plan)
	valid := string(encoded)
	for _, test := range []struct{ name, raw string }{
		{"unknown root field", strings.Replace(valid, `{"title":`, `{"extra":true,"title":`, 1)},
		{"missing root field", strings.Replace(valid, `"title":"A small rescue",`, "", 1)},
		{"null title", strings.Replace(valid, `"title":"A small rescue"`, `"title":null`, 1)},
		{"uppercase title", strings.Replace(valid, `"title":`, `"TITLE":`, 1)},
		{"case variant duplicate", strings.Replace(valid, `{"title":`, `{"TITLE":"other","title":`, 1)},
		{"duplicate title", strings.Replace(valid, `{"title":`, `{"title":"other","title":`, 1)},
		{"unknown scene field", strings.Replace(valid, `{"number":1,`, `{"number":1,"extra":true,`, 1)},
		{"uppercase scene field", strings.Replace(valid, `"number":1`, `"NUMBER":1`, 1)},
		{"null scene field", strings.Replace(valid, `"number":1`, `"number":null`, 1)},
		{"duplicate scene field", strings.Replace(valid, `"number":1`, `"number":1,"number":2`, 1)},
		{"trailing JSON", valid + `{}`},
		{"trailing prose", valid + " this is your story"},
		{"prose wrapper", "Here is your story: " + valid},
		{"fenced wrapper", "```json\n" + valid + "\n```"},
		{"out of sequence", strings.Replace(strings.Replace(valid, `"number":1`, `"number":9`, 1), `"number":9`, `"number":2`, 1)},
		{"oversized continuity", strings.Replace(valid, plan.Continuity, strings.Repeat("x", maxGeneratedContinuity+1), 1)},
		{"oversized scene prompt", strings.Replace(valid, plan.Scenes[0].VideoPrompt, strings.Repeat("x", maxGeneratedScenePrompt+1), 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			body, _ := json.Marshal(map[string]any{"model": ScriptModel, "choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": test.raw}}}})
			_, model, raw, err := parseStoryPlanResponse(body, nil)
			if !errors.Is(err, errStoryPlanUnusable) || model != ScriptModel || raw != test.raw {
				t.Fatalf("model=%q raw=%q err=%v", model, raw, err)
			}
		})
	}
}

func TestGenerateStoryPlanIgnoresFreeQuotaButHonorsPaidProviderCooldown(t *testing.T) {
	calls := 0
	client := storyRetryClient(func(*http.Request) (*http.Response, error) {
		calls++
		return storyRetryPlanResponse(t, validStoryPlan(), nil), nil
	})
	key := randomPromptCredentialScope(client.APIKey, client.baseURL())
	reset := time.Now().Add(2 * time.Hour)
	client.promptState.setPlatformCooldown(key, &upstreamError{StatusCode: 429, RateLimitScope: "platform", LimitSource: "openrouter_free_models", RetryAt: reset}, time.Now())
	if _, _, err := client.GenerateStoryPlan(context.Background(), "story"); err != nil {
		t.Fatalf("free quota blocked Haiku story: %v", err)
	}
	client.promptState.setModelCooldown(key, "provider:anthropic", reset, time.Now())
	_, trace, err := client.GenerateStoryPlan(context.Background(), "story")
	var upstream *upstreamError
	if !errors.As(err, &upstream) || upstream.StatusCode != 429 || trace.Attempts != 0 || calls != 1 {
		t.Fatalf("calls=%d trace=%+v err=%v", calls, trace, err)
	}
}

func TestRandomPromptFreeQuotaClassificationIsExplicit(t *testing.T) {
	for _, test := range []struct {
		message, source string
		want            bool
	}{
		{"quota reached", "openrouter_free_models", true},
		{"Rate limit exceeded: free-models-per-day-high-balance.", "", true},
		{"Rate limit exceeded: free-models-per-min.", "", true},
		{"Rate limit exceeded: free-models-per-minor.", "", false},
		{"Provider mentions free-models-per-min", "", false},
		{"quota reached", "openrouter", false},
		{"quota reached", "openrouter_account", false},
	} {
		upstream := &upstreamError{StatusCode: 429, RateLimitScope: "platform", Message: test.message, LimitSource: test.source}
		if got := randomPromptIsFreeQuota(upstream); got != test.want {
			t.Fatalf("message=%q source=%q got=%v", test.message, test.source, got)
		}
	}
}

func TestCanceledTextGenerationDoesNotAcceptLateValidResponse(t *testing.T) {
	t.Run("story", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		calls := 0
		client := storyRetryClient(func(*http.Request) (*http.Response, error) {
			calls++
			cancel()
			return storyRetryPlanResponse(t, validStoryPlan(), nil), nil
		})
		_, trace, err := client.GenerateStoryPlan(ctx, "story")
		if !errors.Is(err, context.Canceled) || trace.Status != "failed" || calls != 1 {
			t.Fatalf("calls=%d trace=%+v err=%v", calls, trace, err)
		}
	})
	t.Run("single prompt", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		calls := 0
		client := randomPromptTestClient(func(*http.Request) (*http.Response, error) {
			calls++
			cancel()
			return randomPromptTestResponse(t, "A fox follows a trail of fireflies."), nil
		})
		prompt, err := client.GenerateRandomPrompt(ctx, RandomPromptRequest{Category: RandomPromptCategoryNature, Mode: RandomPromptModeSingle})
		if !errors.Is(err, context.Canceled) || prompt != "" || calls != 1 {
			t.Fatalf("calls=%d prompt=%q err=%v", calls, prompt, err)
		}
	})
}
