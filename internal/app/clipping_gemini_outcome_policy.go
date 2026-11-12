package app

import "errors"

type clippingGeminiPersistedOutcome string

const (
	clippingGeminiOutcomeNotSubmitted      clippingGeminiPersistedOutcome = "not_submitted"
	clippingGeminiOutcomeExplicitRejection clippingGeminiPersistedOutcome = "explicit_rejection"
	clippingGeminiOutcomeResponseReceived  clippingGeminiPersistedOutcome = "response_received"
	clippingGeminiOutcomeUnknown           clippingGeminiPersistedOutcome = "unknown"
)

// clippingGeminiAttemptPolicy contains only bounded, application-owned facts.
// It is safe to persist and expose through a typed status view; SDK error text
// and response bodies are intentionally discarded.
type clippingGeminiAttemptPolicy struct {
	Outcome                  clippingGeminiPersistedOutcome
	HTTPStatus               int
	FailureCode              string
	MayRetryWithoutDuplicate bool
	RetryDelaySeconds        int64
}

func clippingGeminiPolicyForAttempt(err error, attempt int) clippingGeminiAttemptPolicy {
	if err == nil {
		return clippingGeminiAttemptPolicy{Outcome: clippingGeminiOutcomeResponseReceived}
	}
	var attemptErr *clippingGeminiAttemptError
	if !errors.As(err, &attemptErr) || attemptErr == nil {
		return clippingGeminiAttemptPolicy{
			Outcome:     clippingGeminiOutcomeUnknown,
			FailureCode: "outcome_unknown",
		}
	}

	policy := clippingGeminiAttemptPolicy{HTTPStatus: safeClippingGeminiHTTPStatus(attemptErr.httpStatus)}
	switch attemptErr.outcome {
	case clippingGeminiAttemptNotSubmitted:
		policy.Outcome = clippingGeminiOutcomeNotSubmitted
		policy.FailureCode = "request_not_submitted"
		policy.MayRetryWithoutDuplicate = true
	case clippingGeminiAttemptExplicitRejection:
		policy.Outcome = clippingGeminiOutcomeExplicitRejection
		policy.FailureCode = "provider_rejected_request"
		if policy.HTTPStatus == 429 {
			policy.FailureCode = "rate_limited"
			if clippingGemini429MayRetry(attempt) {
				policy.MayRetryWithoutDuplicate = true
				policy.RetryDelaySeconds = int64(clippingGemini429Backoff(attempt).Seconds())
			}
		}
	case clippingGeminiAttemptResponseReceived:
		policy.Outcome = clippingGeminiOutcomeResponseReceived
		policy.FailureCode = clippingGeminiResponseFailureCode(attemptErr)
	case clippingGeminiAttemptUnknownPostSubmission:
		policy.Outcome = clippingGeminiOutcomeUnknown
		policy.FailureCode = "outcome_unknown"
	default:
		policy.Outcome = clippingGeminiOutcomeUnknown
		policy.FailureCode = "outcome_unknown"
	}
	return policy
}

func safeClippingGeminiHTTPStatus(status int) int {
	if status < 100 || status > 599 {
		return 0
	}
	return status
}

func clippingGeminiResponseFailureCode(attemptErr *clippingGeminiAttemptError) string {
	switch {
	case errors.Is(attemptErr.reason, errClippingGeminiResponseBlocked):
		return "response_blocked"
	case errors.Is(attemptErr.reason, errClippingGeminiResponseTruncated):
		return "response_incomplete"
	case errors.Is(attemptErr.reason, errClippingGeminiSourceTooLong):
		return "reported_source_too_long"
	case errors.Is(attemptErr.reason, errClippingGeminiInvalidResponse):
		return "invalid_response"
	default:
		return "response_unusable"
	}
}
