package app

import (
	"testing"
	"time"
)

func TestGeminiUnknownPredecessorQuarantinesDuplicateAndLeavesOtherWorkRunnable(t *testing.T) {
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	first, _, err := store.CreateClippingGeminiJob(security, geminiJobInput(), "unknown-first")
	if err != nil {
		t.Fatal(err)
	}
	duplicate, _, err := store.CreateClippingGeminiJob(security, geminiJobInput(), "unknown-duplicate")
	if err != nil {
		t.Fatal(err)
	}
	differentInput := geminiJobInput()
	differentInput.Options.MinClipMS++
	different, _, err := store.CreateClippingGeminiJob(security, differentInput, "unknown-different")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
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
	pending, err := store.PendingClippingGeminiJobs(now.Unix(), 1)
	if err != nil || len(pending) != 1 || pending[0].ID != different.ID {
		t.Fatalf("pending=%v error=%v", pending, err)
	}
	ids, err := store.QuarantineQueuedClippingGeminiJobs(now.Unix())
	if err != nil || len(ids) != 1 || ids[0] != duplicate.ID {
		t.Fatalf("quarantined=%v error=%v", ids, err)
	}
	view, err := store.ClippingGeminiLifecycle(duplicate.ID, now.Unix())
	if err != nil || view.RetrySafety != ClippingGeminiRetrySafetyUnknownNoReplay || view.Attempts != 0 {
		t.Fatalf("view=%+v error=%v", view, err)
	}
}
