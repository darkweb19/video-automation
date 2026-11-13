package app

import (
	"context"
	"errors"
	"net/http"
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
	if _, err := store.BeginClippingImport(source.ID, 0, time.Hour); !errors.Is(err, ErrClippingLegacyRetired) {
		t.Fatalf("import claim: %v", err)
	}
	if err := store.RetryClippingImport(source.ID, 1, time.Now().Unix(), "retry"); !errors.Is(err, ErrClippingLegacyRetired) {
		t.Fatalf("import retry: %v", err)
	}
	if err := store.MarkClippingSourceReady(source.ID, source.SizeBytes, source.DurationMS, source.SHA256); !errors.Is(err, ErrClippingLegacyRetired) {
		t.Fatalf("import finalization: %v", err)
	}
	acquisition := NewClippingAcquisition(store, nil)
	if _, err := acquisition.CreatePublicImport(context.Background(), true, "https://www.youtube.com/watch?v=abcdefghijk", 0); !errors.Is(err, ErrClippingLegacyRetired) {
		t.Fatalf("import creation: %v", err)
	}
	if worked, err := acquisition.ProcessNextImport(context.Background()); worked || !errors.Is(err, ErrClippingLegacyRetired) {
		t.Fatalf("import processing=%v error=%v", worked, err)
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

func TestLegacyDispatcherCannotSendEvenWhenCalledDirectly(t *testing.T) {
	called := false
	dispatcher := newClippingWorkerDispatcher(nil, nil, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		called = true
		return nil, errors.New("unexpected network request")
	})})
	if err := dispatcher.dispatch(context.Background(), clippingWorkerConfig{}, clippingPreparedDispatch{}); !errors.Is(err, ErrClippingLegacyRetired) {
		t.Fatalf("direct dispatch: %v", err)
	}
	if err := (&dashboardApp{}).dispatchPreparedClippingStageWithDispatcher(context.Background(), clippingPreparedDispatch{}, dispatcher); !errors.Is(err, ErrClippingLegacyRetired) {
		t.Fatalf("prepared dispatch: %v", err)
	}
	if called {
		t.Fatal("retired dispatch attempted a network request")
	}
}
