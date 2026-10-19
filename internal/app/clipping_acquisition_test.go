package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	acquisition := NewClippingAcquisition(store, security)
	acquisition.diskAvailable = func(string) (int64, error) { return 1 << 40, nil }
	t.Cleanup(func() { _ = store.Close() })
	return dataDir, store, security, acquisition
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

	_, store, _, acquisition := newClippingAcquisitionFixture(t)
	if _, err := acquisition.CreatePublicImport(context.Background(), false, "https://youtu.be/abcdefghijk", 0); err == nil {
		t.Fatal("missing rights attestation was accepted")
	}
	source, err := acquisition.CreatePublicImport(context.Background(), true, "https://www.youtube.com/watch?v=abcdefghijk&si=tracking", 0)
	if err != nil || source.Kind != ClippingSourceYouTube || !source.RightsAttested || source.OriginalName != "youtube-abcdefghijk.mp4" || source.MediaType != "video/mp4" {
		t.Fatalf("attested YouTube source=%+v error=%v", source, err)
	}
	if canonical, err := acquisition.security.DecryptSetting(clippingSourceURLSetting, source.SourceURL); err != nil || canonical != "https://www.youtube.com/watch?v=abcdefghijk" {
		t.Fatalf("canonical encrypted YouTube URL=%q error=%v", canonical, err)
	}
	if _, err := acquisition.CreatePublicImport(context.Background(), true, "https://drive.google.com/file/d/id/view", 0); err == nil {
		t.Fatal("non-YouTube public import was accepted by the server-side API")
	}
	if sources, err := store.ClippingSources(); err != nil || len(sources) != 1 {
		t.Fatalf("only the valid YouTube link should create a source: sources=%d error=%v", len(sources), err)
	}
}

func TestClippingCodecRequiresAllowedInstalledDecoder(t *testing.T) {
	decoders := map[string]struct{}{"h264": {}, "aac": {}, "fictional_codec": {}}
	if !supportedClippingCodec("video", "h264", decoders) || !supportedClippingCodec("audio", "aac", decoders) {
		t.Fatal("installed common codec decoder was rejected")
	}
	if supportedClippingCodec("video", "fictional_codec", decoders) || supportedClippingCodec("video", "h264", map[string]struct{}{}) {
		t.Fatal("unsupported or uninstalled decoder was accepted")
	}
}

func TestClippingMissingApifyConfigurationFailsWithoutRetainingReservations(t *testing.T) {
	t.Setenv("APIFY_API_TOKEN", "")
	t.Setenv("APIFY_YOUTUBE_ACTOR_ID", "")
	_, store, _, acquisition := newClippingAcquisitionFixture(t)
	for index := 0; index < 6; index++ {
		source, err := acquisition.CreatePublicImport(context.Background(), true, "https://www.youtube.com/watch?v=abcdefghijk", 0)
		if err != nil {
			t.Fatalf("create import %d: %v", index, err)
		}
		if _, err := acquisition.ProcessNextImport(context.Background()); err != nil {
			t.Fatalf("process missing-configuration failure %d: %v", index, err)
		}
		failed, err := store.ClippingSource(source.ID)
		if err != nil || failed.Status != ClippingSourceFailed || failed.ReservedSizeBytes != 0 || failed.Failure != "apify_config_missing" {
			t.Fatalf("missing Apify config retained reservation or exposed an unsafe failure: source=%+v error=%v", failed, err)
		}
	}
	var reserved int64
	if err := store.db.QueryRow(`SELECT COALESCE(SUM(reserved_size_bytes),0) FROM clipping_sources`).Scan(&reserved); err != nil || reserved != 0 {
		t.Fatalf("failed source reservations total=%d error=%v", reserved, err)
	}
}

func TestClippingAttemptFiveCrashIsFailedAndCleanedAfterRestart(t *testing.T) {
	dataDir, store, _, acquisition := newClippingAcquisitionFixture(t)
	source, err := acquisition.CreatePublicImport(context.Background(), true, "https://www.youtube.com/watch?v=abcdefghijk", 0)
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < MaxClippingImportAttempts; attempt++ {
		store.clippingMu.Lock()
		claimed, err := store.BeginClippingImport(source.ID, attempt, time.Minute)
		if err == nil && claimed.ImportAttempts != attempt+1 {
			err = fmt.Errorf("claimed attempt %d, want %d", claimed.ImportAttempts, attempt+1)
		}
		if err == nil {
			_, err = store.db.Exec(`UPDATE clipping_sources SET import_lease_until=0,next_import_at=1 WHERE id=?`, source.ID)
		}
		store.clippingMu.Unlock()
		if err != nil {
			t.Fatalf("claim simulated attempt %d: %v", attempt+1, err)
		}
	}
	partPath, err := store.ClippingSourceTempPath(source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(partPath, []byte("crash-leftover"), 0o600); err != nil {
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
	reopenedSecurity, err := NewSecurity(reopened)
	if err != nil {
		t.Fatal(err)
	}
	restarted := NewClippingAcquisition(reopened, reopenedSecurity)
	restarted.diskAvailable = func(string) (int64, error) { return 1 << 40, nil }
	if didWork, err := restarted.ProcessNextImport(context.Background()); err != nil || didWork {
		t.Fatalf("attempt-limit reconciliation didWork=%v error=%v", didWork, err)
	}
	failed, err := reopened.ClippingSource(source.ID)
	if err != nil || failed.Status != ClippingSourceFailed || failed.ImportAttempts != MaxClippingImportAttempts || failed.ReservedSizeBytes != 0 {
		t.Fatalf("restarted attempt-limit source=%+v error=%v", failed, err)
	}
	if _, err := os.Stat(filepath.Dir(partPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale retry file remains after restart reconciliation: %v", err)
	}
	if sources, err := reopened.FailedClippingSources(10); err != nil || len(sources) != 0 {
		t.Fatalf("failed cleanup queue still contains source: sources=%+v error=%v", sources, err)
	}
}

func TestClippingImportLeaseKeepaliveRenewsActiveAttempt(t *testing.T) {
	_, store, _, acquisition := newClippingAcquisitionFixture(t)
	source, err := acquisition.CreatePublicImport(context.Background(), true, "https://www.youtube.com/watch?v=abcdefghijk", 0)
	if err != nil {
		t.Fatal(err)
	}
	store.clippingMu.Lock()
	claimed, err := store.BeginClippingImport(source.ID, 0, time.Minute)
	if err == nil {
		shortExpiry := time.Now().Add(time.Second).Unix()
		_, err = store.db.Exec(`UPDATE clipping_sources SET import_lease_until=?,next_import_at=? WHERE id=?`, shortExpiry, shortExpiry, source.ID)
	}
	store.clippingMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	acquisition.leaseRenewEvery = 10 * time.Millisecond
	leaseCtx, stop, leaseFailure := acquisition.startImportLeaseKeepalive(context.Background(), claimed)
	defer stop()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		current, err := store.ClippingSource(source.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.ImportLeaseUntil > time.Now().Add(time.Minute).Unix() {
			if err := leaseFailure(); err != nil {
				t.Fatal(err)
			}
			if err := leaseCtx.Err(); err != nil {
				t.Fatalf("lease renewal canceled active import: %v", err)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("active import lease was not renewed")
}

func TestClippingCleanupResumesDeletedTombstone(t *testing.T) {
	dataDir, store, security, acquisition := newClippingAcquisitionFixture(t)
	source, err := acquisition.CreatePublicImport(context.Background(), true, "https://www.youtube.com/watch?v=abcdefghijk", 0)
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
	restarted.diskAvailable = func(string) (int64, error) { return 1 << 40, nil }
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

func TestClippingUnsupportedLegacyImportIsFailedWithoutDeletingSourceFiles(t *testing.T) {
	_, store, security, acquisition := newClippingAcquisitionFixture(t)
	source, err := store.CreateClippingSource(ClippingSourceCreate{
		Kind: ClippingSourceGoogleDrive, OriginalName: "legacy.mp4", MediaType: "video/mp4",
		DeclaredSizeBytes: 1024, RightsAttested: true, SourceURL: "encrypted-legacy-reference",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source.StoragePath+".part", []byte("legacy source bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	acquisition.security = security
	if worked, err := acquisition.ProcessNextImport(context.Background()); err != nil || !worked {
		t.Fatalf("unsupported legacy import worked=%v error=%v", worked, err)
	}
	failed, err := store.ClippingSource(source.ID)
	if err != nil || failed.Status != ClippingSourceFailed || failed.Failure != "legacy_source_import_unsupported" {
		t.Fatalf("unsupported source was not parked safely: source=%+v error=%v", failed, err)
	}
	if _, err := os.Stat(source.StoragePath + ".part"); err != nil {
		t.Fatalf("legacy source file was deleted: %v", err)
	}
	if pending, err := store.FailedClippingSources(10); err != nil || len(pending) != 0 {
		t.Fatalf("preserved legacy source entered automatic delete queue: rows=%v error=%v", pending, err)
	}
}

func TestClippingImportExpectationRemainsSanitized(t *testing.T) {
	dataDir, store, _, acquisition := newClippingAcquisitionFixture(t)
	source, err := acquisition.CreatePublicImport(context.Background(), true, "https://www.youtube.com/watch?v=abcdefghijk", 0)
	if err != nil {
		t.Fatal(err)
	}
	store.clippingMu.Lock()
	claimed, err := store.BeginClippingImport(source.ID, 0, clippingImportLeaseDuration)
	if err == nil {
		err = store.SetClippingSourceImportExpectation(claimed.ID, youtubeImportExpectation{Version: clippingYouTubeExpectationVersion})
	}
	store.clippingMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.ClippingSource(source.ID)
	if err != nil {
		t.Fatal(err)
	}
	var expectation map[string]any
	if err := json.Unmarshal([]byte(loaded.ImportExpectation), &expectation); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"url", "format_id", "headers", "signed_url", "title"} {
		if _, ok := expectation[forbidden]; ok {
			t.Fatalf("import expectation contains disallowed key %q: %s", forbidden, loaded.ImportExpectation)
		}
	}
	if strings.Contains(dataDir, loaded.ImportExpectation) {
		t.Fatal("import expectation unexpectedly contains source path")
	}
}
