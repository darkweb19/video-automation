package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
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

func TestClippingImportURLPolicyAndErrorRedaction(t *testing.T) {
	viewer := "https://drive.google.com/file/d/example/view?resourcekey=private-token"
	u, kind, err := normalizeClippingImportURL(viewer)
	if err != nil || kind != ClippingSourceGoogleDrive || u.String() != viewer || strings.Contains(u.String(), "/uc?") {
		t.Fatalf("Drive URL must stay exactly as supplied: url=%v kind=%q error=%v", u, kind, err)
	}

	dropbox, kind, err := normalizeClippingImportURL("https://www.dropbox.com/scl/fi/abc123/movie.mp4?rlkey=private-key&dl=0")
	if err != nil || kind != ClippingSourceDropbox || dropbox.Query().Get("dl") != "1" || !strings.HasPrefix(dropbox.Path, "/scl/fi/") {
		t.Fatalf("recognized Dropbox share was not normalized: url=%v kind=%q error=%v", dropbox, kind, err)
	}
	if _, _, err := normalizeClippingImportURL("https://www.dropbox.com/home/file.mp4?dl=1"); err == nil {
		t.Fatal("Dropbox URL outside the approved shared-link forms was accepted")
	}
	for _, test := range []struct {
		raw  string
		want string
	}{
		{raw: "https://www.youtube.com/watch?v=abcdefghijk&si=tracking&feature=share", want: "https://www.youtube.com/watch?v=abcdefghijk"},
		{raw: "https://m.youtube.com/shorts/abcdefghijk?t=42&feature=share", want: "https://www.youtube.com/watch?v=abcdefghijk"},
		{raw: "https://www.youtube-nocookie.com/embed/abcdefghijk?start=12", want: "https://www.youtube.com/watch?v=abcdefghijk"},
		{raw: "https://youtu.be/abcdefghijk?si=tracking", want: "https://www.youtube.com/watch?v=abcdefghijk"},
	} {
		normalized, sourceKind, normalizeErr := normalizeClippingImportURL(test.raw)
		if normalizeErr != nil || sourceKind != ClippingSourceYouTube || normalized.String() != test.want {
			t.Errorf("YouTube reference normalization for %q: URL=%v kind=%q error=%v", test.raw, normalized, sourceKind, normalizeErr)
		}
	}

	for _, raw := range []string{
		"http://drive.google.com/file/d/id/view",
		"https://drive.google.com.evil.test/file/d/id/view",
		"https://user@drive.google.com/file/d/id/view",
		"https://drive.google.com./file/d/id/view",
		"https://drive.google.com:8443/file/d/id/view",
		"https://127.0.0.1/file/d/id/view",
		"https://drive.google.com/file/d/id/view#fragment",
		"https://example.test/movie.mp4",
		"https://drive.google.com/file/d/id/view\n",
		"https://www.youtube.com/watch?v=abcdefghijk&list=PL123",
		"https://www.youtube.com/watch?v=abcdefghijk&v=abcdefghijk",
		"https://www.youtube.com/live/abcdefghijk",
		"https://www.youtube.com/shorts/abcdefghijk/extra",
		"https://www.youtu.be/abcdefghijk",
	} {
		if _, _, err := normalizeClippingImportURL(raw); err == nil {
			t.Errorf("unsafe or unsupported URL accepted: %q", raw)
		}
	}

	_, _, _, acquisition := newClippingAcquisitionFixture(t)
	_, err = acquisition.CreatePublicImport(context.Background(), false, viewer, 0)
	var outcome *ClippingAcquisitionError
	if !errors.As(err, &outcome) || outcome.Status != "authorization_needed" {
		t.Fatalf("missing rights attestation error=%v", err)
	}
	youtubeSource, err := acquisition.CreatePublicImport(context.Background(), true, "https://www.youtube.com/watch?v=abcdefghijk&si=tracking&feature=share", 0)
	if err != nil || youtubeSource.Kind != ClippingSourceYouTube || !youtubeSource.RightsAttested || youtubeSource.OriginalName != "youtube-abcdefghijk.media" {
		t.Fatalf("attested YouTube source=%+v error=%v", youtubeSource, err)
	}
	if canonical, err := acquisition.security.DecryptSetting(clippingSourceURLSetting, youtubeSource.SourceURL); err != nil || canonical != "https://www.youtube.com/watch?v=abcdefghijk" {
		t.Fatalf("canonical encrypted YouTube URL=%q error=%v", canonical, err)
	}
	if sources, err := acquisition.store.ClippingSources(); err != nil || len(sources) != 1 {
		t.Fatalf("only the attested YouTube link should create a source: sources=%d error=%v", len(sources), err)
	}

	secret := "resourcekey=private-token"
	transportError := &url.Error{Op: "Get", URL: viewer, Err: errors.New("dial failure")}
	if failure := safeClippingFailure(fmt.Errorf("request failed: %w", transportError)); strings.Contains(failure, secret) || failure == transportError.Error() {
		t.Fatalf("transport error leaked a share capability: %q", failure)
	}
}

type clippingTestResolver []net.IPAddr

func (resolver clippingTestResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return append([]net.IPAddr(nil), resolver...), nil
}

func TestClippingPublicDNSRejectsPrivateReservedMappedAndMixedAnswers(t *testing.T) {
	_, _, _, acquisition := newClippingAcquisitionFixture(t)
	u, _, err := normalizeClippingImportURL("https://drive.google.com/file/d/id/view")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		addresses []net.IPAddr
	}{
		{name: "private ipv4", addresses: []net.IPAddr{{IP: net.ParseIP("10.1.2.3")}}},
		{name: "loopback", addresses: []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}},
		{name: "reserved documentation ipv4", addresses: []net.IPAddr{{IP: net.ParseIP("192.0.2.9")}}},
		{name: "reserved documentation ipv6", addresses: []net.IPAddr{{IP: net.ParseIP("2001:db8::1")}}},
		{name: "ipv4 mapped private ipv6", addresses: []net.IPAddr{{IP: net.ParseIP("::ffff:127.0.0.1")}}},
		{name: "ipv6 zone", addresses: []net.IPAddr{{IP: net.ParseIP("2001:4860:4860::8888"), Zone: "eth0"}}},
		{name: "mixed public and private", addresses: []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}, {IP: net.ParseIP("192.168.0.1")}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			acquisition.resolver = clippingTestResolver(test.addresses)
			if _, err := acquisition.resolvePublicDestination(context.Background(), u, ClippingSourceGoogleDrive); err == nil {
				t.Fatal("unsafe DNS answer was accepted")
			}
		})
	}
	acquisition.resolver = clippingTestResolver{{IP: net.ParseIP("8.8.8.8")}, {IP: net.ParseIP("2001:4860:4860::8888")}}
	addresses, err := acquisition.resolvePublicDestination(context.Background(), u, ClippingSourceGoogleDrive)
	if err != nil || len(addresses) != 2 {
		t.Fatalf("public dual-stack DNS should be accepted: addresses=%v error=%v", addresses, err)
	}
}

type clippingRoundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip clippingRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func TestClippingRedirectCannotLeaveProviderFamily(t *testing.T) {
	_, _, _, acquisition := newClippingAcquisitionFixture(t)
	acquisition.resolver = clippingTestResolver{{IP: net.ParseIP("8.8.8.8")}}
	var requests atomic.Int32
	acquisition.transportFactory = func(*url.URL, []net.IP) http.RoundTripper {
		return clippingRoundTripFunc(func(*http.Request) (*http.Response, error) {
			requests.Add(1)
			return &http.Response{
				StatusCode: http.StatusFound,
				Header:     http.Header{"Location": []string{"https://attacker.example/private"}},
				Body:       io.NopCloser(strings.NewReader("")),
			}, nil
		})
	}
	u, _, _ := normalizeClippingImportURL("https://drive.google.com/file/d/id/view")
	if _, err := acquisition.fetchClippingURL(context.Background(), u, ClippingSourceGoogleDrive); err == nil {
		t.Fatal("cross-provider redirect was followed")
	}
	if requests.Load() != 1 {
		t.Fatalf("redirect made %d requests; expected only the validated provider hop", requests.Load())
	}
}

func TestClippingUploadResumesFromDurableOffsetAndTruncatesCrashTail(t *testing.T) {
	dataDir, store, security, acquisition := newClippingAcquisitionFixture(t)
	content := []byte("streamed-media-fixture")
	acquisition.probe = func(context.Context, string) (clippingMediaProbeResult, error) {
		return clippingMediaProbeResult{DurationMS: 1200, Width: 640, Height: 360}, nil
	}
	source, err := acquisition.CreateUpload(context.Background(), true, "../../clip.mp4", "video/mp4", int64(len(content)), 0)
	if err != nil {
		t.Fatal(err)
	}
	offset, err := acquisition.UploadChunk(context.Background(), source.ID, 0, bytes.NewReader(content[:7]))
	if err != nil || offset != 7 {
		t.Fatalf("first chunk offset=%d error=%v", offset, err)
	}
	tempPath, err := store.ClippingSourceTempPath(source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	security, err = NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	acquisition = NewClippingAcquisition(store, security)
	acquisition.diskAvailable = func(string) (int64, error) { return 1 << 40, nil }
	acquisition.probe = func(context.Context, string) (clippingMediaProbeResult, error) {
		return clippingMediaProbeResult{DurationMS: 1200, Width: 640, Height: 360}, nil
	}
	// Simulate bytes written just before a crash but never committed to SQLite.
	file, err := os.OpenFile(tempPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("uncommitted-tail")); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()

	loaded, err := store.ClippingSource(source.ID)
	if err != nil || loaded.UploadOffset != 7 {
		t.Fatalf("persisted offset after reopen=%+v error=%v", loaded, err)
	}
	offset, err = acquisition.UploadChunk(context.Background(), source.ID, loaded.UploadOffset, bytes.NewReader(content[7:]))
	if err != nil || offset != int64(len(content)) {
		t.Fatalf("resumed upload offset=%d error=%v", offset, err)
	}
	ready, err := acquisition.FinalizeUpload(context.Background(), source.ID)
	if err != nil || ready.Status != ClippingSourceReady {
		t.Fatalf("finalized source=%+v error=%v", ready, err)
	}
	media, err := os.ReadFile(ready.StoragePath)
	if err != nil || !bytes.Equal(media, content) {
		t.Fatalf("final media=%q error=%v", media, err)
	}
	digest := sha256.Sum256(content)
	if ready.SHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("SHA256=%q", ready.SHA256)
	}
}

type clippingStreamingReader struct {
	remaining int64
	maxRead   int
}

func (reader *clippingStreamingReader) Read(buffer []byte) (int, error) {
	if reader.remaining == 0 {
		return 0, io.EOF
	}
	if len(buffer) > reader.maxRead {
		reader.maxRead = len(buffer)
	}
	count := int64(len(buffer))
	if count > reader.remaining {
		count = reader.remaining
	}
	clear(buffer[:int(count)])
	reader.remaining -= count
	return int(count), nil
}

func TestClippingUploadChunkIsBoundedAndOversizeRollsBack(t *testing.T) {
	_, store, _, acquisition := newClippingAcquisitionFixture(t)
	declared := ClippingUploadChunkBytes + 1
	source, err := acquisition.CreateUpload(context.Background(), true, "large.mp4", "video/mp4", declared, 0)
	if err != nil {
		t.Fatal(err)
	}
	reader := &clippingStreamingReader{remaining: declared}
	if _, err := acquisition.UploadChunk(context.Background(), source.ID, 0, reader); !errors.Is(err, ErrClippingUploadChunkTooLarge) {
		t.Fatalf("oversized chunk error=%v", err)
	}
	loaded, err := store.ClippingSource(source.ID)
	if err != nil || loaded.UploadOffset != 0 {
		t.Fatalf("oversized chunk persisted progress: source=%+v error=%v", loaded, err)
	}
	path, err := store.ClippingSourceTempPath(source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Size() != 0 {
		t.Fatalf("oversized chunk left bytes on disk: info=%v error=%v", info, err)
	}

	streamSource, err := acquisition.CreateUpload(context.Background(), true, "bounded.mp4", "video/mp4", ClippingUploadChunkBytes, 0)
	if err != nil {
		t.Fatal(err)
	}
	stream := &clippingStreamingReader{remaining: ClippingUploadChunkBytes}
	offset, err := acquisition.UploadChunk(context.Background(), streamSource.ID, 0, stream)
	if err != nil || offset != ClippingUploadChunkBytes {
		t.Fatalf("large streamed chunk offset=%d error=%v", offset, err)
	}
	if stream.maxRead > 64<<10 {
		t.Fatalf("upload reader was requested to allocate %d bytes at once", stream.maxRead)
	}
}

func TestClippingPublicImportStreamsAndFinalizesWithoutStoringShareURLPlaintext(t *testing.T) {
	_, store, _, acquisition := newClippingAcquisitionFixture(t)
	media := []byte("imported-media-fixture")
	acquisition.probe = func(context.Context, string) (clippingMediaProbeResult, error) {
		return clippingMediaProbeResult{DurationMS: 2400, Width: 1280, Height: 720}, nil
	}
	acquisition.resolver = clippingTestResolver{{IP: net.ParseIP("8.8.8.8")}}
	acquisition.transportFactory = func(*url.URL, []net.IP) http.RoundTripper {
		return clippingRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			if request.Header.Get("Cookie") != "" || request.Header.Get("Authorization") != "" {
				return nil, errors.New("unexpected ambient credentials")
			}
			return &http.Response{
				StatusCode:    http.StatusOK,
				Header:        http.Header{"Content-Type": []string{"video/mp4"}},
				Body:          io.NopCloser(bytes.NewReader(media)),
				ContentLength: int64(len(media)),
			}, nil
		})
	}
	rawURL := "https://drive.google.com/file/d/id/view?resourcekey=highly-sensitive"
	source, err := acquisition.CreatePublicImport(context.Background(), true, rawURL, 0)
	if err != nil {
		t.Fatal(err)
	}
	if source.SourceURL == rawURL || source.SourceURL == "" {
		t.Fatal("source URL was not encrypted before persistence")
	}
	if plaintext, err := acquisition.security.DecryptSetting(clippingSourceURLSetting, source.SourceURL); err != nil || plaintext != rawURL {
		t.Fatalf("encrypted URL round trip=%q error=%v", plaintext, err)
	}
	didWork, err := acquisition.ProcessNextImport(context.Background())
	if err != nil || !didWork {
		t.Fatalf("process import didWork=%v error=%v", didWork, err)
	}
	ready, err := store.ClippingSource(source.ID)
	if err != nil || ready.Status != ClippingSourceReady || ready.SizeBytes != int64(len(media)) || ready.UploadOffset != int64(len(media)) {
		t.Fatalf("imported source=%+v error=%v", ready, err)
	}
	stored, err := os.ReadFile(ready.StoragePath)
	if err != nil || !bytes.Equal(stored, media) {
		t.Fatalf("stored imported media=%q error=%v", stored, err)
	}
}

func TestClippingCodecRequiresAllowedInstalledDecoder(t *testing.T) {
	decoders := map[string]struct{}{"h264": {}, "aac": {}, "fictional_codec": {}}
	if !supportedClippingCodec("video", "h264", decoders) {
		t.Fatal("installed common video decoder was rejected")
	}
	if !supportedClippingCodec("audio", "aac", decoders) {
		t.Fatal("installed common audio decoder was rejected")
	}
	if supportedClippingCodec("video", "fictional_codec", decoders) {
		t.Fatal("unapproved codec was accepted even though FFmpeg listed it")
	}
	if supportedClippingCodec("video", "h264", map[string]struct{}{}) {
		t.Fatal("codec was accepted without an installed decoder")
	}
}

func TestClippingPermanentImportFailuresReleaseSourceReservations(t *testing.T) {
	_, store, _, acquisition := newClippingAcquisitionFixture(t)
	acquisition.resolver = clippingTestResolver{{IP: net.ParseIP("8.8.8.8")}}
	acquisition.transportFactory = func(*url.URL, []net.IP) http.RoundTripper {
		return clippingRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode:    http.StatusOK,
				Header:        http.Header{"Content-Type": []string{"text/html"}},
				Body:          io.NopCloser(strings.NewReader("<html>viewer</html>")),
				ContentLength: int64(len("<html>viewer</html>")),
			}, nil
		})
	}
	for index := 0; index < 6; index++ {
		share := fmt.Sprintf("https://drive.google.com/file/d/id/view?resourcekey=token-%d", index)
		source, err := acquisition.CreatePublicImport(context.Background(), true, share, 0)
		if err != nil {
			t.Fatalf("create import %d: %v", index, err)
		}
		if _, err := acquisition.ProcessNextImport(context.Background()); err != nil {
			t.Fatalf("process permanent failure %d: %v", index, err)
		}
		failed, err := store.ClippingSource(source.ID)
		if err != nil || failed.Status != ClippingSourceFailed || failed.ReservedSizeBytes != 0 || failed.Failure != "drive_viewer_page_requires_original_file" {
			t.Fatalf("failed import retained reservation: source=%+v error=%v", failed, err)
		}
	}
	var reserved int64
	if err := store.db.QueryRow(`SELECT COALESCE(SUM(reserved_size_bytes),0) FROM clipping_sources`).Scan(&reserved); err != nil || reserved != 0 {
		t.Fatalf("failed source reservations total=%d error=%v", reserved, err)
	}
}

func TestClippingAttemptFiveCrashIsFailedAndCleanedAfterRestart(t *testing.T) {
	dataDir, store, _, acquisition := newClippingAcquisitionFixture(t)
	source, err := acquisition.CreatePublicImport(context.Background(), true, "https://drive.google.com/file/d/id/view?resourcekey=restart-token", 0)
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
	source, err := acquisition.CreatePublicImport(context.Background(), true, "https://drive.google.com/file/d/id/view", 0)
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

func TestClippingImportCancellationIsSharedAcrossAcquisitionInstances(t *testing.T) {
	_, store, security, processorAcquisition := newClippingAcquisitionFixture(t)
	dashboardAcquisition := NewClippingAcquisition(store, security)
	dashboardAcquisition.diskAvailable = func(string) (int64, error) { return 1 << 40, nil }
	processorAcquisition.resolver = clippingTestResolver{{IP: net.ParseIP("8.8.8.8")}}
	entered := make(chan struct{})
	processorAcquisition.transportFactory = func(*url.URL, []net.IP) http.RoundTripper {
		return clippingRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			close(entered)
			<-request.Context().Done()
			return nil, request.Context().Err()
		})
	}
	source, err := dashboardAcquisition.CreatePublicImport(context.Background(), true, "https://drive.google.com/file/d/id/view?resourcekey=capability", 0)
	if err != nil {
		t.Fatal(err)
	}
	workerDone := make(chan error, 1)
	go func() {
		_, processErr := processorAcquisition.ProcessNextImport(context.Background())
		workerDone <- processErr
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("import worker did not begin its remote request")
	}
	start := time.Now()
	if err := dashboardAcquisition.CancelSource(context.Background(), source.ID); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("cancel took too long to stop a blocked response body: %s", elapsed)
	}
	if err := <-workerDone; err != nil {
		t.Fatalf("worker returned error after cancellation: %v", err)
	}
	deleted, err := store.ClippingSource(source.ID)
	if err != nil || deleted.Status != ClippingSourceDeleted || !deleted.CleanupComplete {
		t.Fatalf("canceled source=%+v error=%v", deleted, err)
	}
	if _, err := os.Stat(deleted.StoragePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled source media still exists: %v", err)
	}
}

func TestClippingCleanupResumesDeletedTombstone(t *testing.T) {
	dataDir, store, security, acquisition := newClippingAcquisitionFixture(t)
	source, err := acquisition.CreateUpload(context.Background(), true, "expired.mp4", "video/mp4", 12, 0)
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

func TestClippingActualFFprobeValidationForValidAndHTMLMedia(t *testing.T) {
	ffmpegPath, ffmpegErr := exec.LookPath("ffmpeg")
	if ffmpegErr != nil {
		t.Skip("ffmpeg is unavailable")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe is unavailable")
	}
	dataDir, store, security, acquisition := newClippingAcquisitionFixture(t)
	videoPath := filepath.Join(t.TempDir(), "tiny.mp4")
	command := exec.Command(ffmpegPath, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "color=c=black:s=320x240:d=1", "-c:v", "mpeg4", "-pix_fmt", "yuv420p", "-movflags", "+faststart", "-y", videoPath)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generate local ffmpeg test video: %v (%s)", err, output)
	}
	video, err := os.Open(videoPath)
	if err != nil {
		t.Fatal(err)
	}
	videoInfo, err := video.Stat()
	if err != nil {
		video.Close()
		t.Fatal(err)
	}
	validSource, err := acquisition.CreateUpload(context.Background(), true, "tiny.mp4", "video/mp4", videoInfo.Size(), 0)
	if err != nil {
		video.Close()
		t.Fatal(err)
	}
	offset, err := acquisition.UploadChunk(context.Background(), validSource.ID, 0, video)
	video.Close()
	if err != nil || offset != videoInfo.Size() {
		t.Fatalf("valid video upload offset=%d error=%v", offset, err)
	}
	ready, err := acquisition.FinalizeUpload(context.Background(), validSource.ID)
	if err != nil || ready.Status != ClippingSourceReady || ready.DurationMS < 900 || ready.DurationMS > 1100 {
		t.Fatalf("valid ffprobe result=%+v error=%v", ready, err)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	security, err = NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	acquisition = NewClippingAcquisition(store, security)
	acquisition.diskAvailable = func(string) (int64, error) { return 1 << 40, nil }
	html := []byte("<!doctype html><html><body>not a video</body></html>")
	invalidSource, err := acquisition.CreateUpload(context.Background(), true, "viewer.html", "text/html", int64(len(html)), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquisition.UploadChunk(context.Background(), invalidSource.ID, 0, bytes.NewReader(html)); err != nil {
		t.Fatal(err)
	}
	if _, err := acquisition.FinalizeUpload(context.Background(), invalidSource.ID); !errors.Is(err, errClippingInvalidMedia) {
		t.Fatalf("HTML media validation error=%v", err)
	}
	failed, err := store.ClippingSource(invalidSource.ID)
	if err != nil || failed.Status != ClippingSourceFailed || failed.Failure != "invalid_media" {
		t.Fatalf("invalid source was not marked failed: source=%+v error=%v", failed, err)
	}
}
