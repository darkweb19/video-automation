package app

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestClippingDispatchUsesStableVersionedSafeEnvelope(t *testing.T) {
	store := newTestStore(t)
	source := makeReadyClippingSource(t, store, 90_000)
	batch, jobs := makeClippingBatch(t, store, []string{source.ID}, 1_000_000, 1_000_000, "dispatch-contract")
	stage, claimed, err := store.ClaimClippingStage(jobs[0].ID, ClippingStageAnalysis, 100_000, time.Hour)
	if err != nil || !claimed {
		t.Fatalf("stage claim=%+v claimed=%v error=%v", stage, claimed, err)
	}
	job, err := store.ClippingJob(jobs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	dispatch, err := buildClippingStageDispatch(job, stage, source, "https://worker.example", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(dispatch)
	if err != nil {
		t.Fatal(err)
	}
	if dispatch.ProtocolVersion != clippingProtocolVersion || dispatch.Stage != ClippingStageAnalysis || dispatch.DispatchID != job.ID+":analysis" || dispatch.AttemptID != stage.AttemptID || dispatch.BatchID != batch.ID || dispatch.SourceDurationMS != source.DurationMS {
		t.Fatalf("dispatch does not reflect durable job snapshot: %+v", dispatch)
	}
	if strings.Contains(string(encoded), "token") || strings.Contains(string(encoded), "Bearer") || strings.Contains(dispatch.MediaURL, "?") || strings.Contains(dispatch.CallbackURL, "?") {
		t.Fatalf("dispatch envelope contains a capability or URL query: %s", encoded)
	}
	for _, origin := range []string{"http://worker.example", "https://user:secret@worker.example", "https://worker.example/path", "https://worker.example?token=secret"} {
		if _, err := buildClippingStageDispatch(job, stage, source, origin, time.Now()); err == nil {
			t.Errorf("accepted unsafe worker origin %q", origin)
		}
	}
	if _, err := buildClippingStageDispatch(job, stage, source, "https://worker.example", time.Unix(stage.LeaseExpiresAt+1, 0)); err == nil {
		t.Fatal("accepted an expired stage lease")
	}
}

func TestClippingPendingStageAndEncryptedCapabilitiesSurviveRestart(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	source := makeReadyClippingSource(t, store, 45_000)
	batch, jobs := makeClippingBatch(t, store, []string{source.ID}, 1_000_000, 1_000_000, "restart-pending-stage")
	stage, claimed, err := store.ClaimClippingStage(jobs[0].ID, ClippingStageAnalysis, 200_000, 12*time.Hour)
	if err != nil || !claimed {
		_ = store.Close()
		t.Fatalf("stage claim=%+v claimed=%v error=%v", stage, claimed, err)
	}
	security, err := NewSecurity(store)
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	app := &dashboardApp{store: store, security: security}
	mediaToken, callbackToken, err := app.persistClippingDispatchCapabilities(stage)
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	storedCiphertext, err := store.Setting(clippingCapabilitySettingKey(jobs[0].ID, stage.AttemptID, clippingCapabilityCallback))
	if err != nil || strings.Contains(storedCiphertext, callbackToken) || strings.Contains(storedCiphertext, mediaToken) {
		_ = store.Close()
		t.Fatalf("raw worker capability was not encrypted at rest: %q error=%v", storedCiphertext, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restartedSecurity, err := NewSecurity(reopened)
	if err != nil {
		t.Fatal(err)
	}
	restartedApp := &dashboardApp{store: reopened, security: restartedSecurity}
	loadedJob, err := reopened.ClippingJob(jobs[0].ID)
	if err != nil || loadedJob.Status != ClippingJobRunning || loadedJob.BatchID != batch.ID {
		t.Fatalf("job state after restart=%+v error=%v", loadedJob, err)
	}
	stages, err := reopened.ClippingStages(jobs[0].ID)
	if err != nil || len(stages) != 1 || stages[0].Status != ClippingStageRunning || stages[0].AttemptID != stage.AttemptID || stages[0].IdempotencyKey != jobs[0].ID+":analysis" {
		t.Fatalf("stage after restart=%+v error=%v", stages, err)
	}
	recoveredToken, err := restartedApp.clippingCapability(jobs[0].ID, stage.AttemptID, clippingCapabilityCallback)
	if err != nil || recoveredToken != callbackToken {
		t.Fatalf("encrypted callback capability after restart=%q error=%v", recoveredToken, err)
	}
}

func TestProcessorMarksExpiredClippingAttemptUncertainWithoutRedispatch(t *testing.T) {
	store := newTestStore(t)
	source := makeReadyClippingSource(t, store, 30_000)
	batch, jobs := makeClippingBatch(t, store, []string{source.ID}, 1_000_000, 1_000_000, "processor-stage-recovery")
	stage, claimed, err := store.ClaimClippingStage(jobs[0].ID, ClippingStageAnalysis, 300_000, time.Minute)
	if err != nil || !claimed {
		t.Fatalf("stage claim=%+v claimed=%v error=%v", stage, claimed, err)
	}
	if _, err := store.db.Exec(`UPDATE clipping_stages SET lease_expires_at=? WHERE job_id=? AND name=?`, time.Now().Unix()-1, jobs[0].ID, ClippingStageAnalysis); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE clipping_stage_attempts SET lease_expires_at=? WHERE attempt_id=?`, time.Now().Unix()-1, stage.AttemptID); err != nil {
		t.Fatal(err)
	}
	processor := &Processor{app: &dashboardApp{store: store}}
	if err := processor.recoverClippingStages(); err != nil {
		t.Fatal(err)
	}
	recovered, err := store.ClippingStages(jobs[0].ID)
	if err != nil || len(recovered) != 1 || recovered[0].Status != ClippingStageUncertain || recovered[0].ReservedMicroUSD != 300_000 {
		t.Fatalf("recovered stage=%+v error=%v", recovered, err)
	}
	pausedJob, err := store.ClippingJob(jobs[0].ID)
	if err != nil || pausedJob.Status != ClippingJobPausedBudget || pausedJob.ReservedMicroUSD != 300_000 {
		t.Fatalf("recovered job=%+v error=%v", pausedJob, err)
	}
	pausedBatch, err := store.ClippingBatch(batch.ID)
	if err != nil || pausedBatch.Status != ClippingJobPausedBudget || pausedBatch.ReservedMicroUSD != 300_000 {
		t.Fatalf("recovered batch=%+v error=%v", pausedBatch, err)
	}
	if _, err := os.Stat(source.StoragePath); err != nil {
		t.Fatalf("recovery affected source media: %v", err)
	}
	if _, claimedAgain, err := store.ClaimClippingStage(jobs[0].ID, ClippingStageAnalysis, 1, time.Minute); err != nil || claimedAgain {
		t.Fatalf("uncertain attempt was redispatched: claimed=%v error=%v", claimedAgain, err)
	}
}
