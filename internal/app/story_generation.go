package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	storyGenerationAttempts         = 2
	storyGenerationOperationTimeout = storyGenerationAttempts*storyGenerationTimeout + 5*time.Second
	storyGenerationRetryPause       = 100 * time.Millisecond
	storyGenerationMinimumAttempt   = 12 * time.Second
)

var errStoryPlanUnusable = errors.New("OpenRouter did not return a valid JSON story plan with exactly 5 scenes")

// GenerateStoryPlan uses Claude Haiku 4.5 and retries an incomplete plan once.
// Every attempt requires the same strict schema. Each provider request has
// its own deadline, inside a bounded operation and any earlier caller deadline.
func (c *OpenRouterClient) GenerateStoryPlan(ctx context.Context, topic string) (StoryPlan, TextGenerationTrace, error) {
	return c.GenerateStoryPlanForCategory(ctx, topic, "")
}

func (c *OpenRouterClient) GenerateStoryPlanForCategory(ctx context.Context, topic, category string) (StoryPlan, TextGenerationTrace, error) {
	trace, err := newTextGenerationTraceForCategory(topic, category)
	if err != nil {
		return StoryPlan{}, trace, err
	}
	operationCtx, cancel := context.WithTimeout(ctx, storyGenerationOperationTimeout)
	defer cancel()
	trace.Status = "started"
	trace.StartedAt, trace.UpdatedAt = time.Now().Unix(), time.Now().Unix()
	fail := func(err error) (StoryPlan, TextGenerationTrace, error) {
		trace.Status, trace.Error = "failed", safeTextGenerationFailure(err)
		trace.CompletedAt, trace.UpdatedAt = time.Now().Unix(), time.Now().Unix()
		return StoryPlan{}, trace, err
	}
	state := c.promptState
	if state == nil {
		state = &randomPromptState{}
	}
	stateKey := randomPromptCredentialScope(c.APIKey, c.baseURL())
	baseUserPrompt := trace.UserPrompt
	plainJSON := false
	for attempt := 0; attempt < storyGenerationAttempts; attempt++ {
		if err := waitForStoryPlatformCooldown(operationCtx, state, stateKey); err != nil {
			return fail(err)
		}
		trace.UserPrompt = baseUserPrompt
		if plainJSON {
			trace.UserPrompt += "\n\nReturn only the complete JSON object matching the schema, with all five scenes. Do not call tools or include Markdown."
		}
		trace.Attempts++
		attemptCtx, cancelAttempt := context.WithTimeout(operationCtx, storyGenerationTimeout)
		plan, actualModel, rawResponse, attemptErr := c.generateStoryPlanAttempt(attemptCtx, trace)
		cancelAttempt()
		trace.ActualModel = actualModel
		trace.RawResponse = c.safeStoryRawResponse(rawResponse)
		if err := operationCtx.Err(); err != nil {
			return fail(err)
		}
		if attemptErr == nil {
			trace.Status, trace.Error = "completed", ""
			trace.CompletedAt, trace.UpdatedAt = time.Now().Unix(), time.Now().Unix()
			return plan, trace, nil
		}
		var upstream *upstreamError
		_ = errors.As(attemptErr, &upstream)
		recordRandomPromptCooldown(state, stateKey, ScriptModel, attemptErr)
		retry, usePlainJSON := storyPlanRetryPolicy(attemptErr)
		if !retry || attempt == storyGenerationAttempts-1 {
			return fail(attemptErr)
		}
		plainJSON = plainJSON || usePlainJSON
		retryAt := time.Now().Add(storyGenerationRetryPause)
		if upstream != nil {
			if upstream.RateLimitScope == "platform" && upstream.RetryAt.IsZero() {
				// A shared quota without a usable reset cannot be fixed by an
				// immediate second request to the same or a different model.
				return fail(attemptErr)
			}
			if upstream.RetryAt.After(retryAt) {
				retryAt = upstream.RetryAt
			}
		}
		if err := waitForStoryRetry(operationCtx, retryAt); err != nil {
			if operationCtx.Err() != nil {
				return fail(operationCtx.Err())
			}
			// Keep rate-limit/reset metadata when it cannot fit this operation.
			return fail(attemptErr)
		}
	}
	return fail(errStoryPlanUnusable)
}

func (c *OpenRouterClient) safeStoryRawResponse(raw string) string {
	if len(raw) > MaxScriptRawResponseBytes {
		return ""
	}
	if c.APIKey != "" {
		raw = strings.ReplaceAll(raw, c.APIKey, "[redacted]")
	}
	if len(raw) > MaxScriptRawResponseBytes {
		return ""
	}
	return raw
}

func (c *OpenRouterClient) generateStoryPlanAttempt(ctx context.Context, trace TextGenerationTrace) (StoryPlan, string, string, error) {
	request := map[string]any{
		"model":       ScriptModel,
		"messages":    []map[string]string{{"role": "system", "content": trace.SystemPrompt}, {"role": "user", "content": trace.UserPrompt}},
		"temperature": 0.4,
		"max_tokens":  storyPlanCompletionTokens,
		"provider":    map[string]any{"require_parameters": true},
		"response_format": map[string]any{
			"type":        "json_schema",
			"json_schema": map[string]any{"name": "video_story_plan", "strict": true, "schema": storyPlanSchema()},
		},
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return StoryPlan{}, "", "", fmt.Errorf("encode OpenRouter story request: %w", err)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL()+"/chat/completions", bytes.NewReader(encoded))
	if err != nil {
		return StoryPlan{}, "", "", err
	}
	httpRequest.Header.Set("Authorization", "Bearer "+c.APIKey)
	httpRequest.Header.Set("Accept", "application/json")
	httpRequest.Header.Set("Content-Type", "application/json")
	httpResponse, err := c.storyClient().Do(httpRequest)
	if err != nil {
		return StoryPlan{}, "", "", fmt.Errorf("OpenRouter story request: %w", err)
	}
	defer httpResponse.Body.Close()
	body, err := io.ReadAll(io.LimitReader(httpResponse.Body, MaxScriptRawResponseBytes+1))
	if err != nil {
		return StoryPlan{}, "", "", fmt.Errorf("read OpenRouter story response: %w", err)
	}
	if upstreamErr := parseOpenRouterError(body, httpResponse.StatusCode, httpResponse.Header); upstreamErr != nil {
		return StoryPlan{}, "", "", upstreamErr
	}
	if len(body) > MaxScriptRawResponseBytes {
		return StoryPlan{}, "", "", fmt.Errorf("OpenRouter script response exceeds %d bytes", MaxScriptRawResponseBytes)
	}
	return parseStoryPlanResponse(body, httpResponse.Header)
}

func parseStoryPlanResponse(body []byte, headers http.Header) (StoryPlan, string, string, error) {
	var response struct {
		Model   string `json:"model"`
		Choices []struct {
			Message            json.RawMessage `json:"message"`
			FinishReason       string          `json:"finish_reason"`
			NativeFinishReason string          `json:"native_finish_reason"`
			Error              json.RawMessage `json:"error"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return StoryPlan{}, "", "", fmt.Errorf("%w: malformed response: %v", errStoryPlanUnusable, err)
	}
	lastErr := error(errStoryPlanUnusable)
	var lastRaw string
	for _, choice := range response.Choices {
		lastRaw = ""
		if len(choice.Error) > 0 && !bytes.Equal(bytes.TrimSpace(choice.Error), []byte("null")) {
			envelope, _ := json.Marshal(map[string]json.RawMessage{"error": choice.Error})
			if choiceErr := parseOpenRouterError(envelope, http.StatusOK, headers); choiceErr != nil {
				lastErr = choiceErr
				continue
			}
		}
		finishReason := storyChoiceFinishReason(choice.FinishReason, choice.NativeFinishReason)
		switch finishReason {
		case "", "stop", "tool_calls", "function_call":
		case "content_filter", "refusal":
			errorType := "content_policy_violation"
			if finishReason == "refusal" {
				errorType = "refusal"
			}
			lastErr = &upstreamError{StatusCode: http.StatusForbidden, ErrorType: errorType}
			continue
		case "error":
			lastErr = &upstreamError{StatusCode: http.StatusBadGateway, Message: "Story provider did not complete the response"}
			continue
		case "length":
			_, lastRaw, _ = parseStoryPlanChoice(choice.Message)
			lastErr = fmt.Errorf("%w: response reached the completion token limit", errStoryPlanUnusable)
			continue
		default:
			lastErr = fmt.Errorf("%w: response did not complete", errStoryPlanUnusable)
			continue
		}
		plan, raw, err := parseStoryPlanChoice(choice.Message)
		if err == nil {
			// Compatibility response envelopes and tool arguments remain
			// readable, but generated content must be one exact JSON object.
			plan, err = parseGeneratedStoryJSON(raw)
		}
		if err == nil {
			return plan, response.Model, raw, nil
		}
		var upstream *upstreamError
		if errors.As(err, &upstream) {
			lastErr = err
		} else {
			lastRaw = raw
			lastErr = fmt.Errorf("%w: %v", errStoryPlanUnusable, err)
		}
	}
	return StoryPlan{}, response.Model, lastRaw, lastErr
}

func storyChoiceFinishReason(normalized, native string) string {
	normalized = strings.ToLower(strings.TrimSpace(normalized))
	switch strings.ToLower(strings.TrimSpace(native)) {
	case "length", "max_tokens", "max_output_tokens", "token_limit":
		return "length"
	case "content_filter", "safety":
		return "content_filter"
	case "refusal", "refused", "content_policy_violation":
		return "refusal"
	case "error", "failed":
		return "error"
	default:
		return normalized
	}
}

func storyPlanRetryPolicy(err error) (retry, plainJSON bool) {
	if errors.Is(err, errStoryPlanUnusable) || strings.HasPrefix(err.Error(), "OpenRouter script response exceeds ") {
		return true, true
	}
	var upstream *upstreamError
	if errors.As(err, &upstream) {
		switch {
		case upstream.StatusCode == http.StatusBadRequest, upstream.StatusCode == http.StatusNotFound:
			// The pinned model supports structured outputs. Do not weaken the
			// schema or require_parameters after a request/configuration error.
			return false, false
		case upstream.StatusCode == http.StatusForbidden:
			refused := upstream.ErrorType == "content_policy_violation" || upstream.ErrorType == "refusal"
			return refused, refused
		case upstream.StatusCode == http.StatusRequestTimeout, upstream.StatusCode == http.StatusTooManyRequests, upstream.StatusCode >= http.StatusInternalServerError:
			return true, false
		default:
			return false, false
		}
	}
	// Transport and body-read failures may be temporary. Caller cancellation is
	// checked by GenerateStoryPlan before any retry can be sent.
	return true, false
}

func waitForStoryRetry(ctx context.Context, retryAt time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok && retryAt.Add(storyGenerationMinimumAttempt).After(deadline) {
		return context.DeadlineExceeded
	}
	timer := time.NewTimer(time.Until(retryAt))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func waitForStoryPlatformCooldown(ctx context.Context, state *randomPromptState, scopeKey string) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		snapshot := state.cooldownSnapshot(scopeKey, time.Now())
		if modelUntil := randomPromptModelCooldownUntil(snapshot.models, ScriptModel); modelUntil.After(snapshot.platformUntil) {
			snapshot.platformUntil = modelUntil
			snapshot.platformError = paidTextModelCooldownError(modelUntil)
		}
		if snapshot.platformUntil.IsZero() {
			return nil
		}
		if snapshot.platformError != nil && snapshot.platformError.RetryAt.IsZero() {
			return snapshot.platformError
		}
		if err := waitForStoryRetry(ctx, snapshot.platformUntil); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if snapshot.platformError != nil {
				return snapshot.platformError
			}
			return err
		}
	}
}
