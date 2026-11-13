package app

import (
	"context"
	"testing"
	"time"
)

func TestExpireQueuedGeminiJobsClearsRetryWaitAndUnblocksRetention(t *testing.T) {
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	input := geminiJobInput()
	input.RetainUntil = now.Add(time.Minute).Unix()
	job, _, err := store.CreateClippingGeminiJob(security, input, "expiry-retry")
	if err != nil {
		t.Fatal(err)
	}
	lease, ok, err := store.ClaimClippingGeminiAttempt(job.ID, now)
	if err != nil || !ok {
		t.Fatalf("claim %v %v", ok, err)
	}
	_, err = store.settleClippingGeminiKnownRejection(job.ID, lease.Attempt.AttemptID, lease.LeaseToken, clippingGeminiNotSubmittedError(context.Canceled), now)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := store.ExpireQueuedClippingGeminiJobs(now.Add(2 * time.Minute).Unix())
	if err != nil || len(ids) != 1 || ids[0] != job.ID {
		t.Fatalf("expired %v %v", ids, err)
	}
	got, _ := store.ClippingJob(job.ID)
	if got.Status != ClippingJobFailed || got.Error != "source_inaccessible" {
		t.Fatalf("job %+v", got)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM clipping_gemini_execution_claims WHERE job_id=?`, job.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("claims %d %v", count, err)
	}
	if err := store.ExpireClippingSource(job.SourceID, now.Add(2*time.Minute).Unix()); err != nil {
		t.Fatalf("retention remained blocked: %v", err)
	}
}

func TestExpireQueuedGeminiJobsRunsEvenWhenFingerprintBlocked(t *testing.T) {
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	input := geminiJobInput()
	input.RetainUntil = now.Add(time.Minute).Unix()
	first, _, err := store.CreateClippingGeminiJob(security, input, "expiry-first")
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := store.CreateClippingGeminiJob(security, input, "expiry-second")
	if err != nil {
		t.Fatal(err)
	}
	lease, ok, err := store.ClaimClippingGeminiAttempt(first.ID, now)
	if err != nil || !ok {
		t.Fatalf("claim %v %v", ok, err)
	}
	snap, _ := store.ClippingGeminiRequestSnapshot(first.ID)
	src, _ := store.ClippingSource(first.SourceID)
	if err := store.AuthorizeClippingGeminiSubmission(first.ID, lease.Attempt.AttemptID, lease.LeaseToken, snap.Fingerprint, src.SourceURL, now); err != nil {
		t.Fatal(err)
	}
	if err := store.CancelClippingJob(first.ID); err != nil {
		t.Fatal(err)
	}
	ids, err := store.ExpireQueuedClippingGeminiJobs(now.Add(2 * time.Minute).Unix())
	if err != nil || len(ids) != 1 || ids[0] != second.ID {
		t.Fatalf("expired %v %v", ids, err)
	}
	var state string
	if err := store.db.QueryRow(`SELECT state FROM clipping_gemini_execution_claims WHERE job_id=?`, first.ID).Scan(&state); err != nil || state != "unknown" {
		t.Fatalf("unknown guard %q %v", state, err)
	}
}
