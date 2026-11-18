package app

import (
	"testing"
	"time"
)

func TestGeminiCancelAfterRateLimitShowsCancellationWithoutRetry(t *testing.T) {
	for _, deleteAnalysis := range []bool{false, true} {
		store := newTestStore(t)
		security, err := NewSecurity(store)
		if err != nil {
			t.Fatal(err)
		}
		job, _, err := store.CreateClippingGeminiJob(security, geminiJobInput(), "cancel-after-429")
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC().Truncate(time.Second)
		lease, claimed, err := store.ClaimClippingGeminiAttempt(job.ID, now)
		if err != nil || !claimed {
			t.Fatalf("claim=%v error=%v", claimed, err)
		}
		snapshot, _ := store.ClippingGeminiRequestSnapshot(job.ID)
		source, _ := store.ClippingSource(job.SourceID)
		if err := store.AuthorizeClippingGeminiSubmission(job.ID, lease.Attempt.AttemptID, lease.LeaseToken, snapshot.Fingerprint, source.SourceURL, now); err != nil {
			t.Fatal(err)
		}
		if _, err := store.settleClippingGeminiKnownRejection(job.ID, lease.Attempt.AttemptID, lease.LeaseToken, &clippingGeminiAttemptError{outcome: clippingGeminiAttemptExplicitRejection, httpStatus: 429}, now); err != nil {
			t.Fatal(err)
		}
		if deleteAnalysis {
			err = store.DeleteClippingAnalysis(job.ID)
		} else {
			err = store.CancelClippingJob(job.ID)
		}
		if err != nil {
			t.Fatal(err)
		}
		view, err := store.ClippingGeminiLifecycle(job.ID, now.Unix())
		if err != nil || view.RetrySafety != ClippingGeminiRetrySafetyTerminal || view.ErrorCode != "canceled" || view.Attempts != 1 || view.Attempt != 1 || view.RetryAt != 0 {
			t.Fatalf("deleted=%v view=%+v error=%v", deleteAnalysis, view, err)
		}
	}
}

func TestGeminiCancelAfterSubmissionStillShowsUnknownNoReplay(t *testing.T) {
	store, job, _, _, _, _, now := geminiSubmittedFixture(t)
	if err := store.CancelClippingJob(job.ID); err != nil {
		t.Fatal(err)
	}
	view, err := store.ClippingGeminiLifecycle(job.ID, now.Unix())
	if err != nil || view.RetrySafety != ClippingGeminiRetrySafetyUnknownNoReplay || view.ErrorCode != "outcome_unknown" {
		t.Fatalf("view=%+v error=%v", view, err)
	}
}
