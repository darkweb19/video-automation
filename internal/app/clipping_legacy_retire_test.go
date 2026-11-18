package app

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRetirePendingImportsPreservesFilesReservationsAndLeases(t *testing.T) {
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	source := makeReadyClippingSource(t, store, 60_000)
	now := time.Now().Unix()
	lease := now + 300
	if _, err := store.db.Exec(`UPDATE clipping_sources SET status='importing',import_lease_until=?,next_import_at=? WHERE id=?`, lease, lease, source.ID); err != nil {
		t.Fatal(err)
	}
	ids, err := store.retirePendingClippingImports(now, 10)
	if err != nil || len(ids) != 1 || ids[0] != source.ID {
		t.Fatalf("retired imports=%v error=%v", ids, err)
	}
	after, err := store.ClippingSource(source.ID)
	if err != nil || after.Status != ClippingSourceFailed || after.Failure != "legacy_source_import_retired" || after.ImportLeaseUntil != lease || after.ReservedSizeBytes != source.ReservedSizeBytes {
		t.Fatalf("retired source=%+v error=%v", after, err)
	}
	if _, err := os.Stat(source.StoragePath); err != nil {
		t.Fatalf("retirement removed source media: %v", err)
	}
	if _, err := NewClippingAcquisition(store, security).CleanupExpiredSources(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(source.StoragePath); err != nil {
		t.Fatalf("failed-source cleanup removed retired import: %v", err)
	}
	inventory, err := store.LegacyClippingDrainInventory(now)
	if err != nil || inventory.PendingImports != 0 || inventory.ActiveImportLeases != 1 {
		t.Fatalf("inventory=%+v error=%v", inventory, err)
	}
	if err := store.MarkClippingSourceReady(source.ID, source.SizeBytes, source.DurationMS, source.SHA256); err == nil {
		t.Fatal("retired import could become ready")
	}
	if ids, err := store.retirePendingClippingImports(now, 10); err != nil || len(ids) != 0 {
		t.Fatalf("repeated reconciliation=%v error=%v", ids, err)
	}
}

func TestRetireQueuedJobsRequiresProofNoAttemptWasIssued(t *testing.T) {
	store := newTestStore(t)
	source := makeReadyClippingSource(t, store, 60_000)
	never := makeClippingJob(t, store, source.ID, "retire-never-issued")
	claimed := makeClippingJob(t, store, source.ID, "retire-claimed")
	withIndex := makeClippingJob(t, store, source.ID, "retire-index")
	withSetting := makeClippingJob(t, store, source.ID, "retire-capability")
	withCount := makeClippingJob(t, store, source.ID, "retire-stage-counter")
	gemini := makeClippingJob(t, store, source.ID, "retire-gemini")
	if _, err := store.db.Exec(`UPDATE clipping_jobs SET provider='gemini',min_clip_seconds=NULL,max_clip_seconds=NULL WHERE id=?`, gemini.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO clipping_analysis_claims(source_id,source_sha256,pipeline_key,job_id,attempt_id,claimed_at) VALUES(?,?,?,?,?,1)`, source.ID, source.SHA256, "claim", claimed.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO clipping_capability_expiry_index(job_id,attempt_id,expires_at) VALUES(?,?,?)`, withIndex.ID, "orphan-attempt", time.Now().Unix()+300); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting("clipping_cap_"+withSetting.ID+"_orphan_callback", "encrypted"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE clipping_stages SET attempt=1 WHERE job_id=?`, withCount.ID); err != nil {
		t.Fatal(err)
	}
	legacyDrainAttemptFixture(t, store, "old-issued", "modal_legacy", "queued", "expired", time.Now().Unix(), nil)
	oldID, _ := legacyDrainFixtureIDs("old-issued")
	if _, err := store.db.Exec(`UPDATE clipping_stages SET status='queued',attempt_id='',attempt=0 WHERE job_id=?`, oldID); err != nil {
		t.Fatal(err)
	}
	ids, err := store.retireNeverIssuedClippingJobs(time.Now().Unix(), 100)
	if err != nil || len(ids) != 1 || ids[0] != never.ID {
		t.Fatalf("retired jobs=%v error=%v", ids, err)
	}
	job, err := store.ClippingJob(never.ID)
	if err != nil || job.Status != ClippingJobFailed || !strings.Contains(job.Error, "retired") {
		t.Fatalf("retired job=%+v error=%v", job, err)
	}
	for _, id := range []string{claimed.ID, withIndex.ID, withSetting.ID, withCount.ID, gemini.ID, oldID} {
		job, err := store.ClippingJob(id)
		if err != nil || job.Status != ClippingJobQueued {
			t.Errorf("ambiguous job %s changed: %+v error=%v", id, job, err)
		}
	}
}
