package app

import (
	"context"
	"testing"
	"time"
)

func TestGeminiCancellationPreservesUncertainFingerprint(t *testing.T) {
	for _, mode := range []string{"cancel", "delete_analysis", "delete_source"} {
		t.Run(mode, func(t *testing.T) {
			store := newTestStore(t)
			security, err := NewSecurity(store)
			if err != nil {
				t.Fatal(err)
			}
			first, _, err := store.CreateClippingGeminiJob(security, geminiJobInput(), "cancel-first")
			if err != nil {
				t.Fatal(err)
			}
			other, _, err := store.CreateClippingGeminiJob(security, geminiJobInput(), "cancel-second")
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			lease, ok, err := store.ClaimClippingGeminiAttempt(first.ID, now)
			if err != nil || !ok {
				t.Fatalf("claim %v %v", ok, err)
			}
			snapshot, _ := store.ClippingGeminiRequestSnapshot(first.ID)
			source, _ := store.ClippingSource(first.SourceID)
			if err := store.AuthorizeClippingGeminiSubmission(first.ID, lease.Attempt.AttemptID, lease.LeaseToken, snapshot.Fingerprint, source.SourceURL, now); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			release, ok := store.clippingGeminiCancellationRegistry().register(first.ID, lease.Attempt.AttemptID, cancel)
			if !ok {
				t.Fatal("register")
			}
			defer release()
			switch mode {
			case "cancel":
				err = store.CancelClippingJob(first.ID)
			case "delete_analysis":
				err = store.DeleteClippingAnalysis(first.ID)
			case "delete_source":
				_, err = store.DeleteClippingSource(first.SourceID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if ctx.Err() == nil {
				t.Fatal("worker context not canceled")
			}
			attempt, err := store.ClippingGeminiLatestAttempt(first.ID)
			if err != nil || attempt.Outcome != clippingGeminiOutcomeUnknown || attempt.Phase != ClippingGeminiAttemptSettled {
				t.Fatalf("attempt %+v %v", attempt, err)
			}
			if _, ok, err := store.ClaimClippingGeminiAttempt(other.ID, now); err != nil || ok {
				t.Fatalf("unknown fingerprint not guarded %v %v", ok, err)
			}
		})
	}
}

func TestGeminiCancelReleasesOnlyKnownSafeClaims(t *testing.T) {
	for _, retryWait := range []bool{false, true} {
		store := newTestStore(t)
		security, err := NewSecurity(store)
		if err != nil {
			t.Fatal(err)
		}
		job, _, err := store.CreateClippingGeminiJob(security, geminiJobInput(), "cancel-safe")
		if err != nil {
			t.Fatal(err)
		}
		lease, ok, err := store.ClaimClippingGeminiAttempt(job.ID, time.Now())
		if err != nil || !ok {
			t.Fatalf("claim %v %v", ok, err)
		}
		if retryWait {
			_, err = store.settleClippingGeminiKnownRejection(job.ID, lease.Attempt.AttemptID, lease.LeaseToken, clippingGeminiNotSubmittedError(context.Canceled), time.Now())
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := store.CancelClippingJob(job.ID); err != nil {
			t.Fatal(err)
		}
		var count int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM clipping_gemini_execution_claims WHERE job_id=?`, job.ID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("claims=%d error=%v", count, err)
		}
	}
}
