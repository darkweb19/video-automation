package app

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestClippingGeminiPolicyPreservesSafeOutcomeClasses(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		attempt int
		want    clippingGeminiAttemptPolicy
	}{
		{
			name: "valid response",
			want: clippingGeminiAttemptPolicy{Outcome: clippingGeminiOutcomeResponseReceived},
		},
		{
			name: "not submitted",
			err:  &clippingGeminiAttemptError{outcome: clippingGeminiAttemptNotSubmitted, reason: errClippingGeminiCredentialsUnavailable},
			want: clippingGeminiAttemptPolicy{Outcome: clippingGeminiOutcomeNotSubmitted, FailureCode: "request_not_submitted"},
		},
		{
			name:    "transient preflight",
			err:     &clippingGeminiAttemptError{outcome: clippingGeminiAttemptNotSubmitted, reason: context.Canceled},
			attempt: 1,
			want:    clippingGeminiAttemptPolicy{Outcome: clippingGeminiOutcomeNotSubmitted, FailureCode: "request_not_submitted", MayRetryWithoutDuplicate: true, RetryDelaySeconds: int64(clippingGeminiNotSubmittedRetryDelay.Seconds())},
		},
		{
			name:    "last not submitted attempt is terminal",
			err:     &clippingGeminiAttemptError{outcome: clippingGeminiAttemptNotSubmitted},
			attempt: clippingGeminiMaxSubmissionAttempts,
			want:    clippingGeminiAttemptPolicy{Outcome: clippingGeminiOutcomeNotSubmitted, FailureCode: "request_not_submitted"},
		},
		{
			name:    "429 retry",
			err:     &clippingGeminiAttemptError{outcome: clippingGeminiAttemptExplicitRejection, httpStatus: 429},
			attempt: 2,
			want: clippingGeminiAttemptPolicy{Outcome: clippingGeminiOutcomeExplicitRejection, HTTPStatus: 429,
				FailureCode: "rate_limited", MayRetryWithoutDuplicate: true, RetryDelaySeconds: 60},
		},
		{
			name:    "429 attempt limit",
			err:     &clippingGeminiAttemptError{outcome: clippingGeminiAttemptExplicitRejection, httpStatus: 429},
			attempt: clippingGeminiMaxSubmissionAttempts,
			want:    clippingGeminiAttemptPolicy{Outcome: clippingGeminiOutcomeExplicitRejection, HTTPStatus: 429, FailureCode: "rate_limited"},
		},
		{
			name:    "other rejection",
			err:     &clippingGeminiAttemptError{outcome: clippingGeminiAttemptExplicitRejection, httpStatus: 403},
			attempt: 1,
			want:    clippingGeminiAttemptPolicy{Outcome: clippingGeminiOutcomeExplicitRejection, HTTPStatus: 403, FailureCode: "provider_rejected_request"},
		},
		{
			name: "invalid response retains response outcome",
			err:  &clippingGeminiAttemptError{outcome: clippingGeminiAttemptResponseReceived, reason: errClippingGeminiInvalidResponse},
			want: clippingGeminiAttemptPolicy{Outcome: clippingGeminiOutcomeResponseReceived, FailureCode: "invalid_response"},
		},
		{
			name:    "unknown is not replayable",
			err:     &clippingGeminiAttemptError{outcome: clippingGeminiAttemptUnknownPostSubmission},
			attempt: 1,
			want:    clippingGeminiAttemptPolicy{Outcome: clippingGeminiOutcomeUnknown, FailureCode: "outcome_unknown"},
		},
		{
			name:    "untyped error fails closed",
			err:     errors.New("private provider body marker"),
			attempt: 1,
			want:    clippingGeminiAttemptPolicy{Outcome: clippingGeminiOutcomeUnknown, FailureCode: "outcome_unknown"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := clippingGeminiPolicyForAttempt(tc.err, tc.attempt); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("policy=%+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestClippingGeminiPolicyDiscardsProviderErrorText(t *testing.T) {
	private := "private-provider-body-marker"
	err := &clippingGeminiAttemptError{
		outcome: clippingGeminiAttemptResponseReceived,
		reason:  errors.New(private),
	}
	policy := clippingGeminiPolicyForAttempt(err, 1)
	if policy.FailureCode != "response_unusable" || policy.Outcome != clippingGeminiOutcomeResponseReceived {
		t.Fatalf("policy=%+v", policy)
	}
	if policy.FailureCode == private {
		t.Fatal("policy retained provider error text")
	}
}

func TestSafeClippingGeminiHTTPStatusBounds(t *testing.T) {
	for _, tc := range []struct{ input, want int }{{99, 0}, {100, 100}, {429, 429}, {599, 599}, {600, 0}} {
		if got := safeClippingGeminiHTTPStatus(tc.input); got != tc.want {
			t.Errorf("safe status %d=%d, want %d", tc.input, got, tc.want)
		}
	}
}
