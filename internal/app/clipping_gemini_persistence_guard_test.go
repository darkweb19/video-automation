package app

import (
	"errors"
	"testing"
	"time"
)

func TestClippingReferenceCannotEnterLegacyFileOrJobPaths(t *testing.T) {
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.CreateClippingYouTubeReference(security, "https://youtu.be/abcdefghijk", true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if path, err := store.ClippingSourcePath(source.ID); path != "" || !errors.Is(err, ErrClippingInvalidState) {
		t.Fatalf("reference yielded a local source path: %q error=%v", path, err)
	}
	if path, err := store.ClippingSourceTempPath(source.ID); path != "" || !errors.Is(err, ErrClippingInvalidState) {
		t.Fatalf("reference yielded a temporary media path: %q error=%v", path, err)
	}
	if job, _, err := store.CreateClippingJob(ClippingJobCreate{SourceID: source.ID}, "reference-legacy-job"); !errors.Is(err, ErrClippingInvalidState) || job.ID != "" {
		t.Fatalf("legacy job accepted reference: %+v error=%v", job, err)
	}
	retired, err := store.retirePendingClippingImports(time.Now().Unix(), 100)
	if err != nil || len(retired) != 0 {
		t.Fatalf("reference entered legacy retirement queue: %+v error=%v", retired, err)
	}
}
