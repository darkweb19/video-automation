package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newClippingAcquisitionFixture(t *testing.T) (string, *Store, *Security, *ClippingAcquisition) {
	t.Helper()
	dataDir := filepath.Join(t.TempDir(), "data")
	store, err := OpenStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	security, err := NewSecurity(store)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return dataDir, store, security, NewClippingAcquisition(store, security)
}

func TestClippingYouTubeOnlyImportURLPolicy(t *testing.T) {
	for _, test := range []struct {
		raw  string
		want string
	}{
		{raw: "https://www.youtube.com/watch?v=abcdefghijk&si=tracking&feature=share", want: "https://www.youtube.com/watch?v=abcdefghijk"},
		{raw: "https://m.youtube.com/shorts/abcdefghijk?t=42&feature=share", want: "https://www.youtube.com/watch?v=abcdefghijk"},
		{raw: "https://www.youtube-nocookie.com/embed/abcdefghijk?start=12", want: "https://www.youtube.com/watch?v=abcdefghijk"},
		{raw: "https://youtu.be/abcdefghijk?si=tracking", want: "https://www.youtube.com/watch?v=abcdefghijk"},
		{raw: "abcdefghijk", want: "https://www.youtube.com/watch?v=abcdefghijk"},
		{raw: "youtu.be/abcdefghijk", want: "https://www.youtube.com/watch?v=abcdefghijk"},
	} {
		normalized, kind, err := normalizeClippingImportURL(test.raw)
		if err != nil || kind != ClippingSourceYouTube || normalized.String() != test.want {
			t.Errorf("YouTube reference normalization for %q: URL=%v kind=%q error=%v", test.raw, normalized, kind, err)
		}
	}

	for _, raw := range []string{
		"https://www.youtube.com/watch?v=abcdefghijk&list=PL123",
		"https://youtu.be/abcdefghijk?playlist=PL123",
		"https://drive.google.com/file/d/id/view",
		"https://www.dropbox.com/s/example/video.mp4?dl=1",
		"https://example.test/watch?v=abcdefghijk",
		"http://youtube.com/watch?v=abcdefghijk",
		"ftp://youtube.com/watch?v=abcdefghijk",
		"https://user:pass@youtube.com/watch?v=abcdefghijk",
		"https://www.youtube.com/watch?v=abcdefghijk#fragment",
		"https://www.youtube.com/watch?v=abcdefghijk&v=abcdefghijk",
		"https://www.youtube.com/shorts/abcdefghijk/extra",
		"abcdefghij!kl",
	} {
		if _, _, err := normalizeClippingImportURL(raw); err == nil {
			t.Errorf("unsupported or unsafe URL accepted: %q", raw)
		}
	}

}

func TestClippingCleanupResumesDeletedTombstone(t *testing.T) {
	dataDir, store, security, _ := newClippingAcquisitionFixture(t)
	source, err := store.historicalCreateClippingSource(ClippingSourceCreate{Kind: ClippingSourceYouTube, SourceURL: "encrypted-history", DeclaredSizeBytes: 100, RightsAttested: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source.StoragePath+".part", []byte("pending bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.TombstoneClippingSource(source.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	security, err = NewSecurity(reopened)
	if err != nil {
		t.Fatal(err)
	}
	restarted := NewClippingAcquisition(reopened, security)
	cleaned, err := restarted.CleanupExpiredSources(context.Background(), time.Now())
	if err != nil || cleaned != 1 {
		t.Fatalf("restart cleanup count=%d error=%v", cleaned, err)
	}
	deleted, err := reopened.ClippingSource(source.ID)
	if err != nil || !deleted.CleanupComplete || deleted.ReservedSizeBytes != 0 {
		t.Fatalf("restart cleanup did not finish tombstone: source=%+v error=%v", deleted, err)
	}
	if _, err := os.Stat(filepath.Dir(source.StoragePath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source directory remains after restart cleanup: %v", err)
	}
}
