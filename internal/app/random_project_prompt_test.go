package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestRandomProjectPromptCorrectsInvalidOutputOnce(t *testing.T) {
	const valid = `{"prompt":"A seed catches in a rock crack; rain swells it into a sprout that opens toward the sunrise."}`
	for _, test := range []struct {
		name, content, finish, native string
	}{
		{name: "overlong Unicode idea", content: `{"prompt":"` + strings.Repeat("界", maxRandomProjectTopicRunes+1) + `"}`},
		{name: "incomplete JSON", content: `{"prompt":"A seed`},
		{name: "unknown fields", content: `{"prompt":"A seed opens.","secret-extra":"must-not-be-echoed"}`},
		{name: "Markdown wrapper", content: "```json\n" + valid + "\n```"},
		{name: "empty content"},
		{name: "normalized truncation", content: valid, finish: "length"},
		{name: "native truncation", content: valid, finish: "stop", native: "max_tokens"},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			client := randomPromptTestClient(func(request *http.Request) (*http.Response, error) {
				calls++
				body := randomPromptRequestBody(t, request)
				assertHaikuPromptSchema(t, body, randomProjectTokens)
				messages := body["messages"].([]any)
				user := messages[1].(map[string]any)["content"].(string)
				if !strings.Contains(user, "160-220 Unicode characters") || !strings.Contains(user, RandomPromptCategoryNature) {
					t.Fatalf("missing concise category instruction: %q", user)
				}
				if calls == 1 {
					return storyRetryResponse(t, http.StatusOK, map[string]any{"choices": []any{map[string]any{
						"finish_reason": test.finish, "native_finish_reason": test.native, "message": map[string]string{"content": test.content},
					}}}), nil
				}
				if !strings.Contains(user, "previous response failed application validation") || strings.Contains(user, "must-not-be-echoed") {
					t.Fatalf("unsafe or missing correction: %q", user)
				}
				return randomPromptTestResponse(t, "A seed catches in a rock crack; rain swells it into a sprout that opens toward the sunrise."), nil
			})
			prompt, err := client.GenerateRandomPrompt(context.Background(), RandomPromptRequest{Category: RandomPromptCategoryNature, Mode: RandomPromptModeProject})
			if err != nil || calls != 2 || !strings.HasPrefix(prompt, "A seed catches") {
				t.Fatalf("prompt=%q calls=%d err=%v", prompt, calls, err)
			}
		})
	}
}

func TestRandomProjectPromptRejectsBothMalformedAttempts(t *testing.T) {
	for _, body := range []string{`{"choices":[]}`, `{"choices":`, strings.Repeat("x", maxRandomPromptResponse+1)} {
		t.Run(fmt.Sprintf("%d bytes", len(body)), func(t *testing.T) {
			calls := 0
			client := randomPromptTestClient(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			prompt, err := client.GenerateRandomPrompt(context.Background(), RandomPromptRequest{Category: RandomPromptCategoryNature, Mode: RandomPromptModeProject})
			if prompt != "" || !errors.Is(err, errRandomPromptUnusable) || calls != randomProjectPromptAttempts {
				t.Fatalf("prompt=%q calls=%d err=%v", prompt, calls, err)
			}
		})
	}
}

func TestRandomProjectPromptRetriesTransientFailureWithSameHaikuContract(t *testing.T) {
	for _, failure := range []string{"transport", "gateway"} {
		t.Run(failure, func(t *testing.T) {
			calls := 0
			client := randomPromptTestClient(func(request *http.Request) (*http.Response, error) {
				calls++
				body := randomPromptRequestBody(t, request)
				assertHaikuPromptSchema(t, body, randomProjectTokens)
				if calls == 1 {
					if failure == "transport" {
						return nil, io.ErrUnexpectedEOF
					}
					return storyRetryResponse(t, http.StatusBadGateway, map[string]any{"error": map[string]any{"code": http.StatusBadGateway}}), nil
				}
				return randomPromptTestResponse(t, "A small wave frees a leaf trapped between river stones."), nil
			})
			prompt, err := client.GenerateRandomPrompt(context.Background(), RandomPromptRequest{Category: RandomPromptCategoryNature, Mode: RandomPromptModeProject})
			if err != nil || prompt == "" || calls != 2 {
				t.Fatalf("prompt=%q calls=%d err=%v", prompt, calls, err)
			}
		})
	}
}

func TestRandomProjectPromptReservesRetryWithinCallerDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	operationDeadline, _ := ctx.Deadline()
	calls := 0
	client := randomPromptTestClient(func(request *http.Request) (*http.Response, error) {
		calls++
		deadline, ok := request.Context().Deadline()
		if !ok || deadline.After(operationDeadline) {
			t.Fatal("attempt exceeded caller deadline")
		}
		if calls == 1 {
			if remaining := time.Until(deadline); remaining <= 40*time.Second || remaining >= 45*time.Second {
				t.Fatalf("first attempt did not reserve retry budget: %s", remaining)
			}
			return nil, context.DeadlineExceeded
		}
		return randomPromptTestResponse(t, "A small wave frees a leaf trapped between river stones."), nil
	})
	_, err := client.GenerateRandomPrompt(ctx, RandomPromptRequest{Category: RandomPromptCategoryNature, Mode: RandomPromptModeProject})
	if err != nil || calls != 2 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestRandomProjectPromptStopsBeforeRetryOnCancellationOrShortBudget(t *testing.T) {
	for _, cancelCaller := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel=%t", cancelCaller), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			calls := 0
			client := randomPromptTestClient(func(*http.Request) (*http.Response, error) {
				calls++
				if cancelCaller {
					cancel()
				}
				return randomPromptTestResponse(t, strings.Repeat("x", maxRandomProjectTopicRunes+1)), nil
			})
			prompt, err := client.GenerateRandomPrompt(ctx, RandomPromptRequest{Category: RandomPromptCategoryNature, Mode: RandomPromptModeProject})
			if prompt != "" || err == nil || calls != 1 {
				t.Fatalf("prompt=%q calls=%d err=%v", prompt, calls, err)
			}
			if cancelCaller && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cancellation: %v", err)
			}
		})
	}
}

func TestRandomProjectPromptCancellationDuringRetryPause(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	client := randomPromptTestClient(func(*http.Request) (*http.Response, error) {
		calls++
		time.AfterFunc(10*time.Millisecond, cancel)
		return randomPromptTestResponse(t, strings.Repeat("x", maxRandomProjectTopicRunes+1)), nil
	})
	_, err := client.GenerateRandomPrompt(ctx, RandomPromptRequest{Category: RandomPromptCategoryNature, Mode: RandomPromptModeProject})
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestRandomProjectPromptDoesNotRetryFatalProviderErrors(t *testing.T) {
	for _, status := range []int{400, 401, 402, 403, 404, 422} {
		t.Run(fmt.Sprintf("HTTP%d", status), func(t *testing.T) {
			calls := 0
			client := randomPromptTestClient(func(*http.Request) (*http.Response, error) {
				calls++
				return storyRetryResponse(t, status, map[string]any{"error": map[string]any{"code": status, "message": "provider failed"}}), nil
			})
			_, err := client.GenerateRandomPrompt(context.Background(), RandomPromptRequest{Category: RandomPromptCategoryNature, Mode: RandomPromptModeProject})
			if err == nil || calls != 1 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestRandomProjectPromptPreservesLongAccountCooldown(t *testing.T) {
	calls := 0
	client := randomPromptTestClient(func(*http.Request) (*http.Response, error) {
		calls++
		response := storyRetryResponse(t, http.StatusTooManyRequests, map[string]any{"error": map[string]any{"message": "account rate limited", "metadata": map[string]string{"limit_source": "openrouter"}}})
		response.Header.Set("Retry-After", "7200")
		return response, nil
	})
	for attempt := 0; attempt < 2; attempt++ {
		_, err := client.GenerateRandomPrompt(context.Background(), RandomPromptRequest{Category: RandomPromptCategoryNature, Mode: RandomPromptModeProject})
		var upstream *upstreamError
		if !errors.As(err, &upstream) || upstream.StatusCode != http.StatusTooManyRequests || upstream.RetryAt.IsZero() {
			t.Fatalf("lost quota metadata: %v", err)
		}
	}
	if calls != 1 {
		t.Fatalf("long quota caused %d paid calls", calls)
	}
}

func TestRandomProjectPromptRefusalRemainsActionable(t *testing.T) {
	calls := 0
	client := randomPromptTestClient(func(*http.Request) (*http.Response, error) {
		calls++
		return storyRetryResponse(t, http.StatusOK, map[string]any{"choices": []any{map[string]any{"finish_reason": "content_filter", "message": map[string]json.RawMessage{"content": json.RawMessage(`null`)}}}}), nil
	})
	_, err := client.GenerateRandomPrompt(context.Background(), RandomPromptRequest{Category: RandomPromptCategoryNature, Mode: RandomPromptModeProject})
	if err == nil || calls != 1 || !strings.Contains(safeRandomPromptFailure(err), "declined") {
		t.Fatalf("calls=%d err=%v safe=%q", calls, err, safeRandomPromptFailure(err))
	}
}

func TestRandomPromptFailureDoesNotExposeProviderText(t *testing.T) {
	for _, test := range []struct {
		err  error
		want string
	}{
		{fmt.Errorf("%w: %w: provider-private-data", errRandomPromptUnusable, errRandomPromptTooLong), "too long"},
		{fmt.Errorf("%w: provider-private-data", errRandomPromptUnusable), "enter your own prompt"},
		{errors.New("transport provider-private-data"), "Unable to reach"},
	} {
		message := safeRandomPromptFailure(test.err)
		if !strings.Contains(message, test.want) || strings.Contains(message, "provider-private-data") {
			t.Fatalf("unsafe or unhelpful failure message: %q", message)
		}
	}
}
