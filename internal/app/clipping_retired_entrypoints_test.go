package app

import (
	"errors"
	"testing"
	"time"
)

func TestLegacyStoreEntryPointsCannotCreateOrRetryWork(t *testing.T) {
	store := newTestStore(t)
	source := makeReadyClippingSource(t, store, 60_000)
	job := makeClippingJob(t, store, source.ID, "retired-store-job")
	if _, err := store.CreateClippingSource(ClippingSourceCreate{Kind: ClippingSourceUpload, RightsAttested: true, DeclaredSizeBytes: 1}); !errors.Is(err, ErrClippingLegacyRetired) {
		t.Fatalf("source creation: %v", err)
	}
	if _, _, err := store.CreateClippingJob(ClippingJobCreate{SourceID: source.ID}, "new-job-key"); !errors.Is(err, ErrClippingLegacyRetired) {
		t.Fatalf("job creation: %v", err)
	}
	if _, claimed, err := store.ClaimClippingStage(job.ID, ClippingStageAnalysis, 0, time.Hour); claimed || !errors.Is(err, ErrClippingLegacyRetired) {
		t.Fatalf("stage claim=%v error=%v", claimed, err)
	}
	if _, err := store.RetryClippingStage(job.ID, ClippingStageAnalysis); !errors.Is(err, ErrClippingLegacyRetired) {
		t.Fatalf("retry: %v", err)
	}
	app := &dashboardApp{store: store}
	if _, claimed, err := app.prepareClippingStageDispatch(job.ID, 0, "https://example.invalid"); claimed || !errors.Is(err, ErrClippingLegacyRetired) {
		t.Fatalf("dispatch preparation=%v error=%v", claimed, err)
	}
	stored, err := store.ClippingJob(job.ID)
	if err != nil || stored.Status != ClippingJobQueued {
		t.Fatalf("retired entry points changed job=%+v error=%v", stored, err)
	}
}
