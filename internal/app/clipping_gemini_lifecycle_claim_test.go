package app

import (
	"testing"
	"time"
)

func TestPendingAndClaimGeminiAttemptPersistsIntentBeforeSubmission(t *testing.T) {
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	input := geminiJobInput()
	input.RetainUntil = now.Add(time.Hour).Unix()
	first, _, err := store.CreateClippingGeminiJob(security, input, "claim-global-first")
	if err != nil {
		t.Fatal(err)
	}
	secondInput := input
	secondInput.RetainUntil = now.Add(2 * time.Hour).Unix()
	second, _, err := store.CreateClippingGeminiJob(security, secondInput, "claim-global-second")
	if err != nil {
		t.Fatal(err)
	}
	pending, err := store.PendingClippingGeminiJobs(now.Unix(), 10)
	if err != nil || len(pending) != 2 {
		t.Fatalf("pending jobs=%d error=%v", len(pending), err)
	}
	claim, claimed, err := store.ClaimClippingGeminiAttempt(first.ID, now)
	if err != nil || !claimed || claim.Attempt.Phase != ClippingGeminiAttemptClaimed || claim.LeaseToken == "" {
		t.Fatalf("first claim=%+v claimed=%v error=%v", claim, claimed, err)
	}
	var phase, outcome string
	var storedJobStatus ClippingJobStatus
	if err := store.db.QueryRow(`SELECT attempt.phase,attempt.outcome,job.status FROM clipping_gemini_attempts AS attempt JOIN clipping_jobs AS job ON job.id=attempt.job_id WHERE attempt.attempt_id=?`, claim.Attempt.AttemptID).
		Scan(&phase, &outcome, &storedJobStatus); err != nil || phase != "claimed" || outcome != "not_submitted" || storedJobStatus != ClippingJobRunning {
		t.Fatalf("durable intent phase=%q outcome=%q job=%q error=%v", phase, outcome, storedJobStatus, err)
	}
	if _, secondClaimed, err := store.ClaimClippingGeminiAttempt(second.ID, now); err != nil || secondClaimed {
		t.Fatalf("second source with same fingerprint was not blocked: claimed=%v error=%v", secondClaimed, err)
	}
	snapshot, err := store.ClippingGeminiRequestSnapshot(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.ClippingSource(first.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AuthorizeClippingGeminiSubmission(first.ID, claim.Attempt.AttemptID, claim.LeaseToken, snapshot.Fingerprint, source.SourceURL, now); err != nil {
		t.Fatalf("authorize durable submission: %v", err)
	}
	latest, err := store.ClippingGeminiLatestAttempt(first.ID)
	if err != nil || latest.Phase != ClippingGeminiAttemptSubmitting || latest.Outcome != clippingGeminiOutcomeUnknown {
		t.Fatalf("submitting marker=%+v error=%v", latest, err)
	}
	view, err := store.ClippingGeminiLifecycle(first.ID, now.Unix())
	if err != nil || view.RetrySafety != ClippingGeminiRetrySafetyNone {
		t.Fatalf("in-flight attempt was exposed as settled: %+v error=%v", view, err)
	}
}

func TestPendingClippingGeminiJobsDoesNotReturnExpiredOrLegacyWork(t *testing.T) {
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	input := geminiJobInput()
	input.RetainUntil = now.Add(time.Hour).Unix()
	job, _, err := store.CreateClippingGeminiJob(security, input, "claim-expired-reference")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE clipping_sources SET retain_until=? WHERE id=?`, now.Add(-time.Second).Unix(), job.SourceID); err != nil {
		t.Fatal(err)
	}
	jobs, err := store.PendingClippingGeminiJobs(now.Unix(), 10)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("expired source leaked into runnable queue: jobs=%d error=%v", len(jobs), err)
	}
}
