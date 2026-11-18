package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDeleteClippingReferenceErasesURLWithoutRemovingFiles(t *testing.T) {
	_, store, security, acquisition := newClippingAcquisitionFixture(t)
	source, err := store.CreateClippingYouTubeReference(security, "https://youtu.be/abcdefghijk", true, 0)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(store.clippingDir, source.ID)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "unowned"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		jobs, pending, err := acquisition.DeleteSource(context.Background(), source.ID)
		if err != nil || pending || len(jobs) != 0 {
			t.Fatalf("reference delete=%d jobs=%v pending=%v error=%v", i, jobs, pending, err)
		}
	}
	deleted, err := store.ClippingSource(source.ID)
	if err != nil || deleted.Status != ClippingSourceDeleted || deleted.SourceURL != "" || !deleted.CleanupComplete {
		t.Fatalf("reference was not erased and marked cleaned: %+v error=%v", deleted, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "unowned")); err != nil {
		t.Fatalf("reference deletion removed an unowned path: %v", err)
	}
}

func TestExpiredClippingReferenceCleansAcrossRestart(t *testing.T) {
	dataDir, store, security, acquisition := newClippingAcquisitionFixture(t)
	now := time.Now()
	source, err := store.CreateClippingYouTubeReference(security, "https://youtu.be/abcdefghijk", true, now.Add(time.Minute).Unix())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ExpireClippingSource(source.ID, now.Add(2*time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	tombstone, err := store.ClippingSource(source.ID)
	if err != nil || tombstone.Status != ClippingSourceDeleted || tombstone.SourceURL != "" || tombstone.CleanupComplete {
		t.Fatalf("reference tombstone is not restartable: %+v error=%v", tombstone, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	acquisition = NewClippingAcquisition(reopened, nil)
	count, err := acquisition.CleanupExpiredSources(context.Background(), now.Add(2*time.Minute))
	if err != nil || count != 1 {
		t.Fatalf("reference cleanup after restart: count=%d error=%v", count, err)
	}
	deleted, err := reopened.ClippingSource(source.ID)
	if err != nil || !deleted.CleanupComplete || deleted.SourceURL != "" {
		t.Fatalf("reference cleanup did not finish: %+v error=%v", deleted, err)
	}
}
