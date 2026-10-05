package app

import (
	"context"
	"errors"
	"net/http"
	"time"
)

const randomProjectPromptAttempts = 2

// A project idea is text only. Retry a transient or invalid response once before
// asking the user to try again; never relax the JSON or 280-character contract.
func (c *OpenRouterClient) generateRandomProjectPrompt(ctx context.Context, input RandomPromptRequest, systemPrompt, userPrompt string, maxTokens int, state *randomPromptState, stateKey string) (string, error) {
	for attempt := 0; attempt < randomProjectPromptAttempts; attempt++ {
		if err := waitForRandomPromptAccountCooldown(ctx, state, stateKey, true, randomPromptMinAttemptTime); err != nil {
			return "", err
		}
		attemptCtx, cancel := context.WithTimeout(ctx, randomProjectPromptAttemptBudget(ctx, randomProjectPromptAttempts-attempt))
		prompt, err := c.generateRandomPromptAttempt(attemptCtx, input, ScriptModel, systemPrompt, userPrompt, maxTokens)
		cancel()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		if err == nil {
			return prompt, nil
		}
		recordRandomPromptCooldown(state, stateKey, ScriptModel, err)
		if attempt == randomProjectPromptAttempts-1 || !randomProjectPromptShouldRetry(err) {
			return "", err
		}
		if errors.Is(err, errRandomPromptUnusable) {
			// Only application-owned instructions enter the retry. Do not echo
			// malformed provider text or its error strings into the next request.
			userPrompt += randomProjectPromptCorrection
		}
		retryAt := time.Now().Add(randomPromptRetryPause)
		var upstream *upstreamError
		if errors.As(err, &upstream) {
			if upstream.RateLimitScope == "platform" && upstream.RetryAt.IsZero() {
				return "", err
			}
			if upstream.RetryAt.After(retryAt) {
				retryAt = upstream.RetryAt
			}
		}
		if waitErr := waitForRandomPromptPlatformLimit(ctx, retryAt, randomPromptMinAttemptTime); waitErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return "", ctxErr
			}
			// Preserve the validation or quota cause when the reset cannot fit.
			return "", err
		}
	}
	return "", errRandomPromptUnusable
}

func randomProjectPromptAttemptBudget(ctx context.Context, remainingAttempts int) time.Duration {
	remaining := randomPromptTimeout
	if deadline, ok := ctx.Deadline(); ok {
		remaining = time.Until(deadline)
	}
	if remainingAttempts <= 1 || remaining < 2*randomPromptMinAttemptTime {
		return remaining
	}
	// Split the existing 110-second budget, including the retry pause. A
	// stalled first request cannot consume the second attempt's entire budget.
	return (remaining - randomPromptRetryPause) / time.Duration(remainingAttempts)
}

func randomProjectPromptShouldRetry(err error) bool {
	var upstream *upstreamError
	if errors.As(err, &upstream) {
		return upstream.StatusCode == http.StatusRequestTimeout || upstream.StatusCode == http.StatusTooManyRequests || upstream.StatusCode >= http.StatusInternalServerError
	}
	// Caller cancellation is checked before this policy. Attempt timeouts,
	// transport errors, and unusable output can recover on the second request.
	return true
}
