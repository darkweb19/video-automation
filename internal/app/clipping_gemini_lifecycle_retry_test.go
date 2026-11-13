package app

import (
	"context"
	"testing"
	"time"
)

func TestGeminiKnownNotSubmittedRetryIsDelayedAndBounded(t *testing.T) {
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	input := geminiJobInput()
	input.RetainUntil = now.Add(time.Hour).Unix()
	job, _, err := store.CreateClippingGeminiJob(security, input, "known-zero-retry")
	if err != nil {
		t.Fatal(err)
	}
	claim, claimed, err := store.ClaimClippingGeminiAttempt(job.ID, now)
	if err != nil || !claimed {
		t.Fatalf("claim=%+v claimed=%v error=%v", claim, claimed, err)
	}
	first, err := store.settleClippingGeminiKnownRejection(job.ID, claim.Attempt.AttemptID, claim.LeaseToken,
		&clippingGeminiAttemptError{outcome: clippingGeminiAttemptNotSubmitted, reason: context.Canceled}, now)
	if err != nil || first.Outcome != clippingGeminiOutcomeNotSubmitted || first.RetryAt != now.Unix()+int64(clippingGeminiNotSubmittedRetryDelay.Seconds()) {
		t.Fatalf("first known-zero settlement=%+v error=%v", first, err)
	}
	view, err := store.ClippingGeminiLifecycle(job.ID, now.Unix())
	if err != nil || view.RetrySafety != ClippingGeminiRetrySafetyNotSubmittedRetryable || view.RetryAt != 0 {
		t.Fatalf("not-submitted public state=%+v error=%v", view, err)
	}
	if jobs, err := store.PendingClippingGeminiJobs(first.RetryAt-1, 10); err != nil || len(jobs) != 0 {
		t.Fatalf("known-zero retry became due early: jobs=%d error=%v", len(jobs), err)
	}
	if jobs, err := store.PendingClippingGeminiJobs(first.RetryAt, 10); err != nil || len(jobs) != 1 || jobs[0].ID != job.ID {
		t.Fatalf("known-zero retry did not become due: jobs=%v error=%v", jobs, err)
	}
	second, claimed, err := store.ClaimClippingGeminiAttempt(job.ID, time.Unix(first.RetryAt, 0))
	if err != nil || !claimed || second.Attempt.Attempt != 2 {
		t.Fatalf("second claim=%+v claimed=%v error=%v", second, claimed, err)
	}
	limited, err := store.settleClippingGeminiKnownRejection(job.ID, second.Attempt.AttemptID, second.LeaseToken,
		&clippingGeminiAttemptError{outcome: clippingGeminiAttemptExplicitRejection, httpStatus: 429}, time.Unix(first.RetryAt, 0))
	if err != nil || limited.Outcome != clippingGeminiOutcomeExplicitRejection {
		t.Fatalf("429 settlement=%+v error=%v", limited, err)
	}
	view, err = store.ClippingGeminiLifecycle(job.ID, limited.UpdatedAt)
	if err != nil || view.RetrySafety != ClippingGeminiRetrySafetyRateLimitedWait || view.RetryAt != limited.RetryAt || limited.RetryAt <= limited.UpdatedAt {
		t.Fatalf("429 public state=%+v latest=%+v error=%v", view, limited, err)
	}
}

func TestGeminiCredentialFailureIsTerminalWithoutHotRetryLoop(t *testing.T) {
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	input := geminiJobInput()
	input.RetainUntil = now.Add(time.Hour).Unix()
	job, _, err := store.CreateClippingGeminiJob(security, input, "credential-no-hot-loop")
	if err != nil {
		t.Fatal(err)
	}
	claim, claimed, err := store.ClaimClippingGeminiAttempt(job.ID, now)
	if err != nil || !claimed {
		t.Fatalf("claim=%+v claimed=%v error=%v", claim, claimed, err)
	}
	latest, err := store.settleClippingGeminiKnownRejection(job.ID, claim.Attempt.AttemptID, claim.LeaseToken,
		&clippingGeminiAttemptError{outcome: clippingGeminiAttemptNotSubmitted, reason: errClippingGeminiCredentialsUnavailable}, now)
	if err != nil || latest.FailureCode != "credentials_unavailable" || latest.Outcome != clippingGeminiOutcomeNotSubmitted {
		t.Fatalf("credential settlement=%+v error=%v", latest, err)
	}
	failed, err := store.ClippingJob(job.ID)
	if err != nil || failed.Status != ClippingJobFailed {
		t.Fatalf("credential failure job=%+v error=%v", failed, err)
	}
	view, err := store.ClippingGeminiLifecycle(job.ID, now.Unix())
	if err != nil || view.RetrySafety != ClippingGeminiRetrySafetyTerminal {
		t.Fatalf("credential public state=%+v error=%v", view, err)
	}
	if jobs, err := store.PendingClippingGeminiJobs(now.Add(time.Hour).Unix(), 10); err != nil || len(jobs) != 0 {
		t.Fatalf("permanent credential error hot-looped: jobs=%d error=%v", len(jobs), err)
	}
}
