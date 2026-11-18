package app

import (
	"testing"
	"time"
)

func TestGeminiRestartRecoversClaimedAndQuarantinesSubmitting(t *testing.T) {
	for _, submitted := range []bool{false, true} {
		store := newTestStore(t)
		security, err := NewSecurity(store)
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC().Truncate(time.Second)
		job, _, err := store.CreateClippingGeminiJob(security, geminiJobInput(), "restart")
		if err != nil {
			t.Fatal(err)
		}
		lease, ok, err := store.ClaimClippingGeminiAttempt(job.ID, now)
		if err != nil || !ok {
			t.Fatalf("claim %v %v", ok, err)
		}
		if submitted {
			snap, _ := store.ClippingGeminiRequestSnapshot(job.ID)
			src, _ := store.ClippingSource(job.SourceID)
			if err := store.AuthorizeClippingGeminiSubmission(job.ID, lease.Attempt.AttemptID, lease.LeaseToken, snap.Fingerprint, src.SourceURL, now); err != nil {
				t.Fatal(err)
			}
		}
		ids, err := store.RecoverClippingGeminiAttempts(now.Unix(), false)
		if err != nil || len(ids) != 0 {
			t.Fatalf("premature recovery %v %v", ids, err)
		}
		ids, err = store.RecoverClippingGeminiAttempts(now.Unix(), true)
		if err != nil || len(ids) != 1 {
			t.Fatalf("startup %v %v", ids, err)
		}
		attempt, err := store.ClippingGeminiLatestAttempt(job.ID)
		if err != nil {
			t.Fatal(err)
		}
		view, err := store.ClippingGeminiLifecycle(job.ID, now.Unix())
		if err != nil {
			t.Fatal(err)
		}
		if submitted && (attempt.Outcome != clippingGeminiOutcomeUnknown || view.RetrySafety != ClippingGeminiRetrySafetyUnknownNoReplay) {
			t.Fatalf("uncertain %+v %+v", attempt, view)
		}
		if !submitted && (attempt.Outcome != clippingGeminiOutcomeNotSubmitted || view.RetrySafety != ClippingGeminiRetrySafetyNotSubmittedRetryable) {
			t.Fatalf("known zero %+v %+v", attempt, view)
		}
	}
}
