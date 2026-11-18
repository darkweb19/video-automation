package app

import (
	"testing"
	"time"
)

// Exercise a real SQLite close/reopen rather than just invoking recovery in
// the same Store; the unknown guard must survive migrations and deletion.
func TestGeminiSubmittedIntentSurvivesStoreRestartAndSourceDeletion(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	first, _, err := store.CreateClippingGeminiJob(security, geminiJobInput(), "persisted-original")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	lease, ok, err := store.ClaimClippingGeminiAttempt(first.ID, now)
	if err != nil || !ok {
		t.Fatalf("claim %v %v", ok, err)
	}
	snapshot, err := store.ClippingGeminiRequestSnapshot(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.ClippingSource(first.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AuthorizeClippingGeminiSubmission(first.ID, lease.Attempt.AttemptID, lease.LeaseToken, snapshot.Fingerprint, source.SourceURL, now); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	security, err = NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := store.RecoverClippingGeminiAttempts(now.Add(time.Second).Unix(), true)
	if err != nil || len(recovered) != 1 || recovered[0] != first.ID {
		t.Fatalf("recovered=%v error=%v", recovered, err)
	}
	if _, err := store.DeleteClippingSource(first.SourceID); err != nil {
		t.Fatal(err)
	}
	duplicate, _, err := store.CreateClippingGeminiJob(security, geminiJobInput(), "persisted-duplicate")
	if err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := store.ClaimClippingGeminiAttempt(duplicate.ID, now.Add(time.Second)); err != nil || claimed {
		t.Fatalf("replayed uncertain fingerprint: claimed=%v error=%v", claimed, err)
	}
	ids, err := store.QuarantineQueuedClippingGeminiJobs(now.Add(time.Second).Unix())
	if err != nil || len(ids) != 1 || ids[0] != duplicate.ID {
		t.Fatalf("quarantined=%v error=%v", ids, err)
	}
	view, err := store.ClippingGeminiLifecycle(duplicate.ID, now.Unix())
	if err != nil || view.RetrySafety != ClippingGeminiRetrySafetyUnknownNoReplay {
		t.Fatalf("view=%+v error=%v", view, err)
	}
}
