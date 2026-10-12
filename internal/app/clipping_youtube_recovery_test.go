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
	"testing"
	"time"
)

func c25ValidExpectation() youtubeImportExpectation {
	video := youtubeFormat{
		VideoCodec: "avc1.42001E", Width: 320, Height: 240,
		FPS: "12", Quality: "4", TBR: "900",
	}
	audio := youtubeFormat{AudioCodec: "mp4a.40.2", LanguagePreference: "10"}
	selection := youtubeSelection{
		DurationMS: 1_000,
		Separate:   true,
		Tracks: youtubeFormatSelection{
			Video: &video, Audio: &audio, Separate: true,
		},
	}
	expectation, err := expectationForYouTubeSelection(selection)
	if err != nil {
		panic(err)
	}
	return expectation
}

func TestYouTubeRestartDropsStaleTrackPartsBeforeReextracting(t *testing.T) {
	tracks := c25MakeSyntheticTracks(t)
	dataDir, store, _, acquisition := newClippingAcquisitionFixture(t)
	source, err := acquisition.CreatePublicImport(context.Background(), true, "https://youtu.be/"+c25SyntheticVideoID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginClippingImport(source.ID, 0, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := store.SetClippingSourceImportExpectation(source.ID, c25ValidExpectation()); err != nil {
		t.Fatal(err)
	}
	paths, err := acquisition.sourcePaths(source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.videoTrack, []byte("crash-left-video-track"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.audioTrack, []byte("crash-left-audio-track"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE clipping_sources SET next_import_at=0,import_lease_until=0 WHERE id=?`, source.ID); err != nil {
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
	runLog := filepath.Join(t.TempDir(), "extractor-runs")
	preflight := fmt.Sprintf("test ! -e %q && test ! -e %q", paths.videoTrack, paths.audioTrack)
	c25InstallExtractor(t, restarted, c25SeparateMetadata(tracks, true), runLog, preflight)
	c25InstallCountingFFmpeg(t, restarted, filepath.Join(t.TempDir(), "mux-arguments"))
	c25InstallMediaTransport(restarted, tracks, false, nil)
	worked, err := restarted.ProcessNextImport(context.Background())
	if err != nil || !worked {
		t.Fatalf("restart import worked=%v error=%v", worked, err)
	}
	ready, err := reopened.ClippingSource(source.ID)
	if err != nil || ready.Status != ClippingSourceReady || ready.ImportAttempts != 2 {
		t.Fatalf("restart did not re-extract and finalize the source: %+v error=%v", ready, err)
	}
	if runs, err := os.ReadFile(runLog); err != nil || string(runs) != "x" {
		t.Fatalf("restart did not execute one fresh metadata extraction: %q error=%v", runs, err)
	}
	for _, path := range []string{paths.videoTrack, paths.audioTrack} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("recovered source retained staged track %s: %v", filepath.Base(path), err)
		}
	}
	reopened.clippingMu.Lock()
	_, claimRemains := reopened.clippingImportWorkspaces[source.ID]
	reopened.clippingMu.Unlock()
	if claimRemains {
		t.Fatal("restart left a scratch reservation after successful finalization")
	}
}

func TestYouTubeFinalFileExpectationAllowsRenameRecoveryAndRejectsSameSizeCorruption(t *testing.T) {
	tracks := c25MakeSyntheticTracks(t)
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is required for the local recovery fixture")
	}
	alternate := filepath.Join(t.TempDir(), "alternate-same-size.mkv")
	c25RunFixtureCommand(t, ffmpeg,
		"-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-i", tracks.videoPath, "-i", tracks.audioPath,
		"-map", "0:v:0", "-map", "1:a:0", "-c", "copy", "-metadata", "title=Title-B", "-f", "matroska", alternate)
	goodPath := filepath.Join(t.TempDir(), "alternate-reference.mkv")
	c25RunFixtureCommand(t, ffmpeg,
		"-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-i", tracks.videoPath, "-i", tracks.audioPath,
		"-map", "0:v:0", "-map", "1:a:0", "-c", "copy", "-metadata", "title=Title-A", "-f", "matroska", goodPath)
	good, err := os.ReadFile(goodPath)
	if err != nil {
		t.Fatal(err)
	}
	bad, err := os.ReadFile(alternate)
	if err != nil {
		t.Fatal(err)
	}
	if len(good) != len(bad) || string(good) == string(bad) {
		t.Fatalf("recovery fixture must be different valid media with the same size: good=%d bad=%d", len(good), len(bad))
	}
	for _, test := range []struct {
		name      string
		published []byte
		ready     bool
	}{
		{name: "validated file renamed before Ready transaction", published: good, ready: true},
		{name: "same-size different finalized bytes", published: bad, ready: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			dataDir, store, _, acquisition := newClippingAcquisitionFixture(t)
			source, err := acquisition.CreatePublicImport(context.Background(), true, "https://youtu.be/"+c25SyntheticVideoID, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.BeginClippingImport(source.ID, 0, time.Minute); err != nil {
				t.Fatal(err)
			}
			if err := store.SetClippingSourceImportExpectation(source.ID, c25ValidExpectation()); err != nil {
				t.Fatal(err)
			}
			paths, err := acquisition.sourcePaths(source.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(paths.temp, test.published, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := store.UpdateClippingSourceProgress(source.ID, ClippingSourceImporting, 0, int64(len(test.published))); err != nil {
				t.Fatal(err)
			}
			if err := syncClippingDirectory(filepath.Dir(paths.final)); err != nil {
				t.Fatal(err)
			}
			if err := store.CompleteClippingSourceTrackWorkspace(source.ID); err != nil {
				t.Fatal(err)
			}
			goodHash := sha256.Sum256(good)
			if err := store.SetClippingSourceImportFinalExpectation(source.ID, int64(len(good)), hex.EncodeToString(goodHash[:])); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(paths.temp, paths.final); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.Exec(`UPDATE clipping_sources SET next_import_at=0,import_lease_until=0 WHERE id=?`, source.ID); err != nil {
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
			restarted.youtubeExecutable = writeFakeYouTubeExecutable(t, t.TempDir(), "unexpected-extractor", "exit 23\n")
			worked, err := restarted.ProcessNextImport(context.Background())
			if err != nil || !worked {
				t.Fatalf("final-file recovery worked=%v error=%v", worked, err)
			}
			recovered, err := reopened.ClippingSource(source.ID)
			if err != nil {
				t.Fatal(err)
			}
			if test.ready {
				if recovered.Status != ClippingSourceReady || recovered.SHA256 != hex.EncodeToString(goodHash[:]) || recovered.SizeBytes != int64(len(good)) {
					t.Fatalf("rename-before-Ready crash was not safely recovered: %+v", recovered)
				}
				published, err := os.ReadFile(recovered.StoragePath)
				if err != nil || string(published) != string(good) {
					t.Fatalf("recovered Ready bytes=%d error=%v", len(published), err)
				}
				if _, err := os.Stat(paths.temp); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("recovery recreated or retained a temporary file: %v", err)
				}
			} else {
				if recovered.Status == ClippingSourceReady || recovered.Status != ClippingSourceFailed || recovered.ReservedSizeBytes != 0 {
					t.Fatalf("same-size corrupted final file was accepted or retained: %+v", recovered)
				}
				if _, err := os.Stat(filepath.Dir(recovered.StoragePath)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("same-size corrupted final source was not cleaned: %v", err)
				}
			}
			for _, path := range []string{paths.videoTrack, paths.audioTrack} {
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("final-file recovery left staged track %s: %v", filepath.Base(path), err)
				}
			}
		})
	}
}

func TestYouTubeFinalizationRejectsMissingStreamsAndBadExpectedTiming(t *testing.T) {
	validProbe := clippingMediaProbeResult{
		DurationMS: 1_021, Width: 320, Height: 240, AudioStreams: 1,
		VideoCodec: "h264", AudioCodec: "aac",
		VideoStartMS: 21, AudioStartMS: 0,
		VideoDurationMS: 1_000, AudioDurationMS: 1_000,
		VideoTimingValid: true, AudioTimingValid: true,
		VideoFPS: 12, VideoFPSValid: true,
	}
	for _, test := range []struct {
		name  string
		probe clippingMediaProbeResult
	}{
		{name: "valid Matroska start offset under two seconds", probe: validProbe},
		{name: "missing audio stream", probe: func() clippingMediaProbeResult { value := validProbe; value.AudioStreams = 0; return value }()},
		{name: "unexpected copied video codec", probe: func() clippingMediaProbeResult { value := validProbe; value.VideoCodec = "vp9"; return value }()},
		{name: "audio and video starts exceed skew bound", probe: func() clippingMediaProbeResult {
			value := validProbe
			value.VideoStartMS = clippingYouTubeMaxAVSkewMS + 1
			return value
		}()},
		{name: "track durations diverge", probe: func() clippingMediaProbeResult {
			value := validProbe
			value.AudioDurationMS = value.VideoDurationMS + clippingYouTubeMaxAVSkewMS + 1
			return value
		}()},
		{name: "timing fields unavailable", probe: func() clippingMediaProbeResult { value := validProbe; value.AudioTimingValid = false; return value }()},
		{name: "container duration mismatches selected metadata", probe: func() clippingMediaProbeResult { value := validProbe; value.DurationMS = 4_000; return value }()},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, store, _, acquisition := newClippingAcquisitionFixture(t)
			source, err := acquisition.CreatePublicImport(context.Background(), true, "https://youtu.be/"+c25SyntheticVideoID, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.BeginClippingImport(source.ID, 0, time.Minute); err != nil {
				t.Fatal(err)
			}
			if err := store.SetClippingSourceImportExpectation(source.ID, c25ValidExpectation()); err != nil {
				t.Fatal(err)
			}
			paths, err := acquisition.sourcePaths(source.ID)
			if err != nil {
				t.Fatal(err)
			}
			payload := []byte("synthetic finalized media probe target")
			if err := os.WriteFile(paths.temp, payload, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := store.UpdateClippingSourceProgress(source.ID, ClippingSourceImporting, 0, int64(len(payload))); err != nil {
				t.Fatal(err)
			}
			if err := store.CompleteClippingSourceTrackWorkspace(source.ID); err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(payload)
			if err := store.SetClippingSourceImportFinalExpectation(source.ID, int64(len(payload)), hex.EncodeToString(digest[:])); err != nil {
				t.Fatal(err)
			}
			acquisition.probe = func(context.Context, string) (clippingMediaProbeResult, error) { return test.probe, nil }
			_, finalizeErr := acquisition.finalizeSource(context.Background(), source.ID, ClippingSourceImporting)
			if test.name == "valid Matroska start offset under two seconds" {
				if finalizeErr != nil {
					t.Fatalf("valid audio/video expectation rejected: %v", finalizeErr)
				}
				ready, err := store.ClippingSource(source.ID)
				if err != nil || ready.Status != ClippingSourceReady {
					t.Fatalf("valid expected media did not atomically become Ready: %+v error=%v", ready, err)
				}
				if _, err := os.Stat(paths.final); err != nil {
					t.Fatalf("Ready source is not at its atomically published final path: %v", err)
				}
				if _, err := os.Stat(paths.temp); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("Ready source still has a temporary file: %v", err)
				}
			} else {
				if finalizeErr == nil {
					t.Fatal("incompatible YouTube stream/timing metadata reached Ready")
				}
				current, err := store.ClippingSource(source.ID)
				if err != nil || current.Status == ClippingSourceReady {
					t.Fatalf("incompatible media was published Ready: %+v error=%v", current, err)
				}
			}
		})
	}
}

func TestYouTubeSourceCannotBecomeReadyWithoutDurableFinalSizeAndSHA(t *testing.T) {
	_, store, _, acquisition := newClippingAcquisitionFixture(t)
	source, err := acquisition.CreatePublicImport(context.Background(), true, "https://youtu.be/"+c25SyntheticVideoID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginClippingImport(source.ID, 0, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := store.SetClippingSourceImportExpectation(source.ID, c25ValidExpectation()); err != nil {
		t.Fatal(err)
	}
	paths, err := acquisition.sourcePaths(source.ID)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("incomplete final expectation")
	if err := os.WriteFile(paths.temp, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateClippingSourceProgress(source.ID, ClippingSourceImporting, 0, int64(len(payload))); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteClippingSourceTrackWorkspace(source.ID); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	if err := store.MarkClippingSourceReady(source.ID, int64(len(payload)), 1_000, hex.EncodeToString(digest[:])); err == nil {
		t.Fatal("YouTube source became Ready without a persisted final size/hash expectation")
	}
	current, err := store.ClippingSource(source.ID)
	if err != nil || current.Status != ClippingSourceImporting {
		t.Fatalf("rejected premature Ready transaction changed source state: %+v error=%v", current, err)
	}
}

func TestYouTubeMuxCancellationStopsProcessGroupAndCleansWorkspace(t *testing.T) {
	_, store, security, acquisition := newClippingAcquisitionFixture(t)
	metadata := c25TinySeparateMetadata("https://rr1---sn.googlevideo.com/video-track?signature=v", "https://rr1---sn.googlevideo.com/original-audio?signature=a", 4, 4)
	c25InstallExtractor(t, acquisition, metadata, "", "")
	acquisition.resolver = clippingTestResolver{{IP: net.ParseIP("8.8.8.8")}}
	acquisition.transportFactory = func(*url.URL, []net.IP) http.RoundTripper {
		return c25RoundTripFunc(func(request *http.Request) (*http.Response, error) {
			payload, contentType := []byte("VID!"), "video/mp4"
			if request.URL.Path == "/original-audio" {
				payload, contentType = []byte("AUD!"), "audio/mp4"
			}
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(bytes.NewReader(payload)), ContentLength: int64(len(payload))}, nil
		})
	}
	marker := filepath.Join(t.TempDir(), "mux-started")
	descendantMarker := filepath.Join(t.TempDir(), "mux-descendant-survived")
	acquisition.youtubeFFmpegExecutable = writeFakeYouTubeExecutable(t, t.TempDir(), "ffmpeg-blocked", fmt.Sprintf("printf started > %q\n(sleep 1; printf survived > %q) &\nwait\n", marker, descendantMarker))
	source, err := acquisition.CreatePublicImport(context.Background(), true, "https://youtu.be/"+c25SyntheticVideoID, 0)
	if err != nil {
		t.Fatal(err)
	}
	processed := make(chan error, 1)
	go func() {
		_, err := acquisition.ProcessNextImport(context.Background())
		processed <- err
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("synthetic FFmpeg process did not reach the mux stage")
		}
		time.Sleep(10 * time.Millisecond)
	}
	canceler := NewClippingAcquisition(store, security)
	canceler.diskAvailable = func(string) (int64, error) { return 1 << 40, nil }
	if err := canceler.CancelSource(context.Background(), source.ID); err != nil {
		t.Fatalf("cancel source during mux: %v", err)
	}
	select {
	case err := <-processed:
		if err != nil {
			t.Fatalf("canceled mux ProcessNextImport returned %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("canceled FFmpeg process group did not stop")
	}
	select {
	case <-time.After(1200 * time.Millisecond):
		if _, err := os.Stat(descendantMarker); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("FFmpeg child outlived process-group cancellation and wrote its marker: %v", err)
		}
	}
	deleted, err := store.ClippingSource(source.ID)
	if err != nil || deleted.Status != ClippingSourceDeleted || !deleted.CleanupComplete || deleted.ReservedSizeBytes != 0 {
		t.Fatalf("mux cancellation left an active source or reservation: %+v error=%v", deleted, err)
	}
	if _, err := os.Stat(filepath.Dir(deleted.StoragePath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("mux cancellation retained track/intermediate files: %v", err)
	}
	store.clippingMu.Lock()
	_, claimRemains := store.clippingImportWorkspaces[source.ID]
	store.clippingMu.Unlock()
	if claimRemains {
		t.Fatal("mux cancellation retained the separate-track workspace claim")
	}
}
