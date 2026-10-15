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
	job := makeClippingJob(t, store, source.ID, "dispatch-contract")
	stage, claimed, err := store.ClaimClippingStage(job.ID, ClippingStageAnalysis, 0, time.Hour)
	if err != nil || !claimed {
		t.Fatalf("stage claim=%+v claimed=%v error=%v", stage, claimed, err)
	}
	job, err = store.ClippingJob(job.ID)
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
	if dispatch.ProtocolVersion != clippingProtocolVersion || dispatch.Stage != ClippingStageAnalysis || dispatch.DispatchID != job.ID+":analysis" || dispatch.AttemptID != stage.AttemptID || dispatch.BatchID != job.BatchID || dispatch.SourceDurationMS != source.DurationMS {
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
	job := makeClippingJob(t, store, source.ID, "restart-pending-stage")
	stage, claimed, err := store.ClaimClippingStage(job.ID, ClippingStageAnalysis, 0, 12*time.Hour)
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
	storedCiphertext, err := store.Setting(clippingCapabilitySettingKey(job.ID, stage.AttemptID, clippingCapabilityCallback))
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
	loadedJob, err := reopened.ClippingJob(job.ID)
	if err != nil || loadedJob.Status != ClippingJobRunning || loadedJob.BatchID != job.BatchID {
		t.Fatalf("job state after restart=%+v error=%v", loadedJob, err)
	}
	stages, err := reopened.ClippingStages(job.ID)
	if err != nil || len(stages) != 1 || stages[0].Status != ClippingStageRunning || stages[0].AttemptID != stage.AttemptID || stages[0].IdempotencyKey != job.ID+":analysis" {
		t.Fatalf("stage after restart=%+v error=%v", stages, err)
	}
	recoveredToken, err := restartedApp.clippingCapability(job.ID, stage.AttemptID, clippingCapabilityCallback)
	if err != nil || recoveredToken != callbackToken {
		t.Fatalf("encrypted callback capability after restart=%q error=%v", recoveredToken, err)
	}
}

func TestProcessorMarksExpiredClippingAttemptUncertainWithoutRedispatch(t *testing.T) {
	store := newTestStore(t)
	source := makeReadyClippingSource(t, store, 30_000)
	job := makeClippingJob(t, store, source.ID, "processor-stage-recovery")
	stage, claimed, err := store.ClaimClippingStage(job.ID, ClippingStageAnalysis, 0, time.Minute)
	if err != nil || !claimed {
		t.Fatalf("stage claim=%+v claimed=%v error=%v", stage, claimed, err)
	}
	if _, err := store.db.Exec(`UPDATE clipping_stages SET lease_expires_at=? WHERE job_id=? AND name=?`, time.Now().Unix()-1, job.ID, ClippingStageAnalysis); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE clipping_stage_attempts SET lease_expires_at=? WHERE attempt_id=?`, time.Now().Unix()-1, stage.AttemptID); err != nil {
		t.Fatal(err)
	}
	processor := &Processor{app: &dashboardApp{store: store}}
	if err := processor.recoverClippingStages(); err != nil {
		t.Fatal(err)
	}
	recovered, err := store.ClippingStages(job.ID)
	if err != nil || len(recovered) != 1 || recovered[0].Status != ClippingStageUncertain || recovered[0].ReservedMicroUSD != 0 {
		t.Fatalf("recovered stage=%+v error=%v", recovered, err)
	}
	uncertainJob, err := store.ClippingJob(job.ID)
	if err != nil || uncertainJob.Status != ClippingJobRunning || uncertainJob.ReservedMicroUSD != 0 {
		t.Fatalf("recovered job=%+v error=%v", uncertainJob, err)
	}
	if _, err := os.Stat(source.StoragePath); err != nil {
		t.Fatalf("recovery affected source media: %v", err)
	}
	if _, claimedAgain, err := store.ClaimClippingStage(job.ID, ClippingStageAnalysis, 0, time.Minute); err != nil || claimedAgain {
		t.Fatalf("uncertain attempt was redispatched: claimed=%v error=%v", claimedAgain, err)
	}
}

func TestCanceledPreSubmitAttemptReleasesSourceClaim(t *testing.T) {
	store := newTestStore(t)
	source := makeReadyClippingSource(t, store, 30_000)
	job := makeClippingJob(t, store, source.ID, "cancel-before-submit")
	claimedSource, err := store.ClaimClippingAnalysisSource(job.ID)
	if err != nil || !claimedSource {
		t.Fatalf("claim source=%v error=%v", claimedSource, err)
	}
	stage, claimed, err := store.ClaimClippingStage(job.ID, ClippingStageAnalysis, 0, time.Minute)
	if err != nil || !claimed {
		t.Fatalf("claim stage=%+v claimed=%v error=%v", stage, claimed, err)
	}
	if err := store.SetClippingAnalysisClaimAttempt(job.ID, stage.AttemptID); err != nil {
		t.Fatal(err)
	}
	if err := store.CancelClippingJob(job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FailClippingStageBeforeSubmit(job.ID, ClippingStageAnalysis, stage.AttemptID, stage.LeaseToken, "Canceled before worker submission"); err != nil {
		t.Fatalf("record known-no-submission cancellation: %v", err)
	}
	if err := store.ReleaseClippingAnalysisSourceClaimBeforeSubmit(job.ID, stage.AttemptID); err != nil {
		t.Fatalf("release known-no-submission source claim: %v", err)
	}
	second := makeClippingJob(t, store, source.ID, "cancel-before-submit-second")
	if claimed, err := store.ClaimClippingAnalysisSource(second.ID); err != nil || !claimed {
		t.Fatalf("same source remained blocked after known no submission: claimed=%v error=%v", claimed, err)
	}
}
