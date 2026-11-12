package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
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

func TestClippingLegacyWorkersRejectGeminiJobsUsingLegacySource(t *testing.T) {
	store := newTestStore(t)
	source := makeReadyClippingSource(t, store, 60_000)
	queued := makeClippingJob(t, store, source.ID, "gemini-legacy-source-queued")
	running := makeClippingJob(t, store, source.ID, "gemini-legacy-source-running")
	stage, claimed, err := store.ClaimClippingStage(running.ID, ClippingStageAnalysis, 0, time.Hour)
	if err != nil || !claimed {
		t.Fatalf("legacy setup: %v %v", claimed, err)
	}
	running, err = store.ClippingJob(running.ID)
	if err != nil {
		t.Fatal(err)
	}
	dispatch, err := buildClippingStageDispatch(running, stage, source, "https://worker.example", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{queued.ID, running.ID} {
		if _, err := store.db.Exec(`UPDATE clipping_jobs SET provider='gemini',min_clip_seconds=NULL,max_clip_seconds=NULL WHERE id=?`, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.ClaimClippingAnalysisSource(queued.ID); !errors.Is(err, ErrClippingInvalidState) {
		t.Errorf("legacy source claim accepted Gemini: %v", err)
	}
	if _, err := store.TryCompleteClippingJobFromAnalysisCache(queued.ID); !errors.Is(err, ErrClippingInvalidState) {
		t.Errorf("legacy cache accepted Gemini: %v", err)
	}
	if _, _, err := store.ClaimClippingStage(queued.ID, ClippingStageAnalysis, 0, time.Hour); !errors.Is(err, ErrClippingInvalidState) {
		t.Errorf("legacy stage claim accepted Gemini: %v", err)
	}
	if _, err := store.FailQueuedClippingJob(queued.ID, "legacy failure"); !errors.Is(err, ErrClippingInvalidState) {
		t.Errorf("legacy queued failure accepted Gemini: %v", err)
	}
	if err := store.RenewClippingStageLease(running.ID, stage.Name, stage.AttemptID, stage.LeaseToken, time.Hour); !errors.Is(err, ErrClippingInvalidState) {
		t.Errorf("legacy lease accepted Gemini: %v", err)
	}
	if _, _, _, err := store.CompleteClippingAnalysisStage(running.ID, stage.Name, stage.AttemptID, stage.LeaseToken, strings.Repeat("a", 64), ClippingArtifact{}, clippingAnalysisCore{}, 1); !errors.Is(err, ErrClippingInvalidState) {
		t.Errorf("legacy success callback accepted Gemini: %v", err)
	}
	if _, _, err := store.FailClippingAnalysisCallback(running.ID, stage.Name, stage.AttemptID, stage.LeaseToken, strings.Repeat("a", 64)); !errors.Is(err, ErrClippingInvalidState) {
		t.Errorf("legacy failure callback accepted Gemini: %v", err)
	}
	if _, err := store.FailClippingStageBeforeSubmit(running.ID, stage.Name, stage.AttemptID, stage.LeaseToken, "legacy failure"); !errors.Is(err, ErrClippingInvalidState) {
		t.Errorf("legacy submission failure accepted Gemini: %v", err)
	}
	running.Provider = ClippingProviderGemini
	if _, err := buildClippingStageDispatch(running, stage, source, "https://worker.example", time.Now()); err == nil {
		t.Error("legacy dispatch accepted Gemini")
	}
	app := &dashboardApp{store: store}
	if _, _, _, ok := app.workerStage(running.ID, stage.AttemptID); ok {
		t.Error("legacy worker endpoint accepted Gemini")
	}
	if err := app.checkClippingWorkerAttemptCurrent(context.Background(), dispatch, stage.LeaseToken, ""); err == nil {
		t.Error("legacy dispatch-current check accepted Gemini")
	}
	if _, err := store.db.Exec(`UPDATE clipping_jobs SET status='failed' WHERE id=?`, queued.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE clipping_stages SET status='failed' WHERE job_id=?`, queued.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RetryClippingStage(queued.ID, ClippingStageAnalysis); !errors.Is(err, ErrClippingInvalidState) {
		t.Errorf("legacy retry accepted Gemini: %v", err)
	}
}

func TestClippingLegacyRecoverySkipsGeminiBeforeLimitAndOnStartup(t *testing.T) {
	store := newTestStore(t)
	source := makeReadyClippingSource(t, store, 60_000)
	gemini := makeClippingJob(t, store, source.ID, "gemini-recovery")
	legacy := makeClippingJob(t, store, source.ID, "legacy-recovery")
	for _, job := range []ClippingJob{gemini, legacy} {
		if _, claimed, err := store.ClaimClippingStage(job.ID, ClippingStageAnalysis, 0, time.Second); err != nil || !claimed {
			t.Fatalf("setup stage: %v %v", claimed, err)
		}
	}
	if _, err := store.db.Exec(`UPDATE clipping_jobs SET provider='gemini',min_clip_seconds=NULL,max_clip_seconds=NULL WHERE id=?`, gemini.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE clipping_stages SET lease_expires_at=1 WHERE job_id IN (?,?)`, gemini.ID, legacy.ID); err != nil {
		t.Fatal(err)
	}
	stages, err := store.ExpiredClippingStages(time.Now().Unix(), 1)
	if err != nil || len(stages) != 1 || stages[0].JobID != legacy.ID {
		t.Fatalf("recovery limit includes Gemini: %+v %v", stages, err)
	}
	if err := store.RecoverExpiredClippingStages(time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := store.db.QueryRow(`SELECT status FROM clipping_stages WHERE job_id=?`, gemini.ID).Scan(&status); err != nil || status != "running" {
		t.Fatalf("Gemini recovery altered stage: %q %v", status, err)
	}
	if _, err := store.db.Exec(`UPDATE clipping_jobs SET status='paused_budget' WHERE id=?`, gemini.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE clipping_stages SET status='paused_budget' WHERE job_id=?`, gemini.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.migrateClippingCompatibilityState(); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT status FROM clipping_stages WHERE job_id=?`, gemini.ID).Scan(&status); err != nil || status != "paused_budget" {
		t.Fatalf("legacy compatibility migration altered Gemini stage: %q %v", status, err)
	}
}
