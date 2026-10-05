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
