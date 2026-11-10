package app

import (
	"fmt"
	"testing"
)

func TestClippingLegacyQueueFiltersProviderBeforeLimit(t *testing.T) {
	store := newTestStore(t)
	source := makeReadyClippingSource(t, store, 60_000)
	for i := 0; i < 25; i++ {
		job := makeClippingJob(t, store, source.ID, fmt.Sprintf("gemini-queue-%02d", i))
		if _, err := store.db.Exec(`UPDATE clipping_jobs SET provider='gemini',min_clip_seconds=NULL,max_clip_seconds=NULL,created_at=1 WHERE id=?`, job.ID); err != nil {
			t.Fatal(err)
		}
	}
	legacy := makeClippingJob(t, store, source.ID, "legacy-queue-after-gemini")
	if legacy.Provider != ClippingProviderModalLegacy {
		t.Fatalf("new legacy job provider = %q", legacy.Provider)
	}
	pending, err := store.PendingClippingJobs(1)
	if err != nil || len(pending) != 1 || pending[0].ID != legacy.ID || pending[0].Provider != ClippingProviderModalLegacy {
		t.Fatalf("legacy queue starved by another provider: jobs=%+v error=%v", pending, err)
	}
	if _, err := store.db.Exec(`UPDATE clipping_jobs SET provider='other' WHERE id=?`, legacy.ID); err == nil {
		t.Fatal("unknown provider bypassed database constraint")
	}
}
