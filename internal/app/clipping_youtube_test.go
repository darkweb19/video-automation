package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestYouTubeV3ProbeUsesRealMP4AudioAndRejectsInvalidMedia(t *testing.T) {
	ffmpeg := requireFFmpeg(t)

	t.Run("real audio video MP4", func(t *testing.T) {
		mediaPath := makeLocalYouTubeMP4(t, ffmpeg, false, false)
		probe, err := probeClippingMedia(context.Background(), mediaPath)
		if err != nil {
			t.Fatalf("ffprobe local MP4: %v", err)
		}
		if !isYouTubeMP4Container(probe.FormatName) || probe.AudioStreams < 1 {
			t.Fatalf("real fixture lacks MP4 audio/video: format=%q audio=%d", probe.FormatName, probe.AudioStreams)
		}
		if err := validateYouTubeProbeExpectation(youtubeImportExpectation{Version: clippingYouTubeExpectationVersion}, probe); err != nil {
			t.Fatalf("real MP4 did not satisfy v3 audio/video contract: %+v: %v", probe, err)
		}

		containerMismatch := probe
		containerMismatch.DurationMS += 3 * clippingYouTubeDurationToleranceMS
		if err := validateYouTubeProbeExpectation(youtubeImportExpectation{Version: clippingYouTubeExpectationVersion}, containerMismatch); !errors.Is(err, errClippingYouTubeAVTimingMismatch) {
			t.Fatalf("v3 accepted stream timing that differs from the probed container duration: %+v err=%v", containerMismatch, err)
		}
		overLimitStream := probe
		overLimitStream.DurationMS = MaxClippingSourceDurationMS
		overLimitStream.VideoDurationMS = MaxClippingSourceDurationMS + 1
		overLimitStream.AudioDurationMS = MaxClippingSourceDurationMS
		if err := validateYouTubeProbeExpectation(youtubeImportExpectation{Version: clippingYouTubeExpectationVersion}, overLimitStream); !errors.Is(err, errClippingDurationLimitExceeded) {
			t.Fatalf("v3 accepted a stream beyond the four-hour duration cap: %+v err=%v", overLimitStream, err)
		}
	})

	t.Run("video only MP4", func(t *testing.T) {
		mediaPath := makeLocalYouTubeMP4(t, ffmpeg, true, false)
		probe, err := probeClippingMedia(context.Background(), mediaPath)
		if err != nil {
			t.Fatalf("ffprobe video-only fixture: %v", err)
		}
		if err := validateYouTubeProbeExpectation(youtubeImportExpectation{Version: clippingYouTubeExpectationVersion}, probe); !errors.Is(err, errClippingYouTubeAVStreamMissing) {
			t.Fatalf("v3 accepted a video-only MP4: probe=%+v err=%v", probe, err)
		}
	})

	t.Run("audio only MP4", func(t *testing.T) {
		mediaPath := makeLocalAudioMP4(t, ffmpeg)
		if _, err := probeClippingMedia(context.Background(), mediaPath); !errors.Is(err, errClippingInvalidMedia) {
			t.Fatalf("ffprobe did not reject audio-only media as invalid: err=%v", err)
		}
	})

	t.Run("corrupt bytes", func(t *testing.T) {
		mediaPath := filepath.Join(t.TempDir(), "corrupt.mp4")
		if err := os.WriteFile(mediaPath, []byte("not a media container"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := probeClippingMedia(context.Background(), mediaPath); err == nil {
			t.Fatal("ffprobe accepted corrupt non-media bytes")
		}
	})
}

func TestYouTubeApifyImportStagesAndPublishesValidatedRealMP4(t *testing.T) {
	ffmpeg := requireFFmpeg(t)
	mediaPath := makeLocalYouTubeMP4(t, ffmpeg, false, false)
	mediaBytes, err := os.ReadFile(mediaPath)
	if err != nil {
		t.Fatal(err)
	}
	canonicalURL := "https://www.youtube.com/watch?v=abcdefghijk"
	const maxPollLimit = time.Hour

	api := &clippingApifyHTTPTestDoer{respond: func(request *http.Request, _ int) (*http.Response, error) {
		switch {
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/runs"):
			var input clippingApifyStartInput
			if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
				return nil, err
			}
			timeoutSeconds, timeoutErr := strconv.Atoi(request.URL.Query().Get("timeout"))
			if request.URL.Query().Get("waitForFinish") != "0" || timeoutErr != nil || timeoutSeconds <= 0 || timeoutSeconds > int(maxPollLimit/time.Second) ||
				len(input.URLs) != 1 || input.URLs[0].URL != canonicalURL || input.Region != clippingApifyRegion || input.IncludeTranscript ||
				!input.SaveMedia || input.Quality != "best" || input.Format != "mp4" {
				return nil, fmt.Errorf("unexpected actor start request: query=%v input=%+v", request.URL.Query(), input)
			}
			return clippingApifyTestResponse(http.StatusCreated, nil, `{"data":{"id":"run_real_media"}}`), nil
		case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/actor-runs/run_real_media"):
			return clippingApifyTestResponse(http.StatusOK, nil, `{"data":{"id":"run_real_media","status":"SUCCEEDED","defaultDatasetId":"dataset_real_media"}}`), nil
		case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/datasets/dataset_real_media/items"):
			item, err := json.Marshal([]clippingApifyDatasetItem{{
				ID: "abcdefghijk", URL: canonicalURL,
				DurationSeconds: json.RawMessage("2"),
				IsLive:          boolPointer(false),
				SavedFile: &struct {
					URL          string          `json:"url"`
					Key          string          `json:"key"`
					ContentType  string          `json:"contentType"`
					Container    string          `json:"container"`
					QualityLabel string          `json:"qualityLabel"`
					Width        int             `json:"width"`
					Height       int             `json:"height"`
					Bytes        json.RawMessage `json:"bytes"`
					SizeMB       json.RawMessage `json:"sizeMb"`
					BilledMB     json.RawMessage `json:"billedMb"`
				}{URL: "https://media.example/video.mp4", ContentType: "video/mp4", Bytes: json.RawMessage(fmt.Sprint(len(mediaBytes)))},
			}})
			if err != nil {
				return nil, err
			}
			return clippingApifyTestResponse(http.StatusOK, nil, string(item)), nil
		default:
			return nil, fmt.Errorf("unexpected Apify request %s %s", request.Method, request.URL.Path)
		}
	}}
	media := &clippingApifyHTTPTestDoer{respond: func(request *http.Request, _ int) (*http.Response, error) {
		if request.URL.Host != "media.example" {
			return nil, fmt.Errorf("unexpected media host %q", request.URL.Host)
		}
		return clippingApifyTestMediaResponse(http.StatusOK, http.Header{"Content-Type": []string{"video/mp4"}}, string(mediaBytes), int64(len(mediaBytes))), nil
	}}

	_, store, _, acquisition := newClippingAcquisitionFixture(t)
	client := clippingApifyTestClient()
	client.maxPoll = maxPollLimit
	client.apiDoer = api
	client.mediaDoer = media
	client.resolver = &clippingApifyHTTPTestResolver{answers: map[string][]net.IPAddr{"media.example": {clippingApifyTestPublicIP("8.8.8.8")}}}
	acquisition.apify = client
	source := createYouTubeImport(t, acquisition)
	worked, err := acquisition.ProcessNextImport(context.Background())
	if err != nil || !worked {
		t.Fatalf("process real-media Apify import worked=%v error=%v", worked, err)
	}
	ready, err := store.ClippingSource(source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ready.Status != ClippingSourceReady || ready.SizeBytes != int64(len(mediaBytes)) || !validSHA256Hex(ready.SHA256) || ready.DurationMS <= 0 {
		t.Fatalf("validated MP4 was not durably marked Ready: %+v", ready)
	}
	paths, err := acquisition.sourcePaths(source.ID)
	if err != nil {
		t.Fatal(err)
	}
	finalBytes, err := os.ReadFile(paths.final)
	if err != nil || string(finalBytes) != string(mediaBytes) {
		t.Fatalf("published media differs from downloaded artifact: bytes=%d err=%v", len(finalBytes), err)
	}
	if got := api.requestCount(); got != 3 {
		t.Fatalf("Apify API requests=%d, want start, poll, and dataset fetch", got)
	}
}

func TestYouTubeApifyImportRechecksStorageBeforePaidStart(t *testing.T) {
	_, store, _, acquisition := newClippingAcquisitionFixture(t)
	source := createYouTubeImport(t, acquisition)
	api := &clippingApifyHTTPTestDoer{respond: func(*http.Request, int) (*http.Response, error) {
		return nil, errors.New("storage gate should prevent any Apify request")
	}}
	client := clippingApifyTestClient()
	client.apiDoer = api
	acquisition.apify = client
	acquisition.diskAvailable = func(string) (int64, error) {
		return clippingFreeSpaceFloorBytes + MaxClippingSourceBytes - 1, nil
	}
	worked, err := acquisition.ProcessNextImport(context.Background())
	if err != nil || !worked {
		t.Fatalf("process storage-blocked import worked=%v error=%v", worked, err)
	}
	failed, err := store.ClippingSource(source.ID)
	if err != nil || failed.Status != ClippingSourceFailed || failed.Failure != "storage_reservation_unavailable" || failed.ReservedSizeBytes != 0 {
		t.Fatalf("storage gate did not fail and release source safely: source=%+v error=%v", failed, err)
	}
	if api.requestCount() != 0 {
		t.Fatalf("storage gate issued %d Apify requests before rejecting the import", api.requestCount())
	}
}

func TestFinalizeSourceRejectsOldAttemptWithoutChangingFilesOrExpectation(t *testing.T) {
	ffmpeg := requireFFmpeg(t)
	mediaPath := makeLocalYouTubeMP4(t, ffmpeg, false, false)
	mediaBytes, err := os.ReadFile(mediaPath)
	if err != nil {
		t.Fatal(err)
	}
	_, store, _, acquisition := newClippingAcquisitionFixture(t)
	source := createYouTubeImport(t, acquisition)
	claimed, err := store.BeginClippingImport(source.ID, 0, clippingImportLeaseDuration)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := acquisition.ensureSourcePaths(source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.temp, mediaBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	store.clippingMu.Lock()
	if err := store.SetClippingSourceImportExpectation(source.ID, youtubeImportExpectation{Version: clippingYouTubeExpectationVersion}); err != nil {
		store.clippingMu.Unlock()
		t.Fatal(err)
	}
	if err := store.UpdateClippingSourceImportProgress(source.ID, claimed.ImportAttempts, int64(len(mediaBytes))); err != nil {
		store.clippingMu.Unlock()
		t.Fatal(err)
	}
	if err := store.CompleteClippingSourceTrackWorkspace(source.ID); err != nil {
		store.clippingMu.Unlock()
		t.Fatal(err)
	}
	store.clippingMu.Unlock()
	before, err := store.ClippingSource(source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RetryClippingImport(source.ID, claimed.ImportAttempts, time.Now().Add(-time.Second).Unix(), "simulated attempt rollover"); err != nil {
		t.Fatal(err)
	}
	newClaim, err := store.BeginClippingImport(source.ID, claimed.ImportAttempts, clippingImportLeaseDuration)
	if err != nil {
		t.Fatal(err)
	}
	if newClaim.ImportAttempts == claimed.ImportAttempts {
		t.Fatal("test did not advance the durable import attempt")
	}
	if _, err := acquisition.finalizeSource(context.Background(), source.ID, claimed.ImportAttempts); err == nil {
		t.Fatal("stale finalizer unexpectedly succeeded")
	}
	after, err := store.ClippingSource(source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != ClippingSourceImporting || after.ImportAttempts != newClaim.ImportAttempts ||
		after.ImportExpectation != before.ImportExpectation || after.ImportExpectedSize != before.ImportExpectedSize || after.ImportExpectedSHA != before.ImportExpectedSHA {
		t.Fatalf("stale finalizer changed the new attempt: before=%+v after=%+v", before, after)
	}
	gotTemp, err := os.ReadFile(paths.temp)
	if err != nil || string(gotTemp) != string(mediaBytes) {
		t.Fatalf("stale finalizer modified its staged input: bytes=%d err=%v", len(gotTemp), err)
	}
	if _, err := os.Lstat(paths.final); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale finalizer created/published the final file: %v", err)
	}
}

func TestFinalizedLegacyV2MP4RecoversWithoutApifyStart(t *testing.T) {
	ffmpeg := requireFFmpeg(t)
	mediaPath := makeLocalYouTubeMP4(t, ffmpeg, false, false)
	mediaBytes, err := os.ReadFile(mediaPath)
	if err != nil {
		t.Fatal(err)
	}
	_, store, security, acquisition := newClippingAcquisitionFixture(t)
	source := createYouTubeImport(t, acquisition)
	claimed, err := store.BeginClippingImport(source.ID, 0, clippingImportLeaseDuration)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := acquisition.ensureSourcePaths(source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.final, mediaBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	digest, err := hashClippingFile(context.Background(), paths.final)
	if err != nil {
		t.Fatal(err)
	}
	store.clippingMu.Lock()
	if err := store.SetClippingSourceImportExpectation(source.ID, youtubeImportExpectation{Version: clippingYouTubeLegacyExpectationVersion, SeparateTracks: true}); err != nil {
		store.clippingMu.Unlock()
		t.Fatal(err)
	}
	if err := store.UpdateClippingSourceImportProgress(source.ID, claimed.ImportAttempts, int64(len(mediaBytes))); err != nil {
		store.clippingMu.Unlock()
		t.Fatal(err)
	}
	if err := store.CompleteClippingSourceTrackWorkspace(source.ID); err != nil {
		store.clippingMu.Unlock()
		t.Fatal(err)
	}
	if err := store.SetClippingSourceImportFinalExpectation(source.ID, int64(len(mediaBytes)), digest); err != nil {
		store.clippingMu.Unlock()
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE clipping_sources SET import_lease_until=0,next_import_at=1 WHERE id=?`, source.ID); err != nil {
		store.clippingMu.Unlock()
		t.Fatal(err)
	}
	store.clippingMu.Unlock()

	api := &clippingApifyHTTPTestDoer{respond: func(*http.Request, int) (*http.Response, error) {
		return nil, errors.New("Apify must not be contacted to recover a finalized v2 artifact")
	}}
	client := clippingApifyTestClient()
	client.apiDoer = api
	acquisition = NewClippingAcquisition(store, security)
	acquisition.apify = client
	acquisition.diskAvailable = func(string) (int64, error) { return 1 << 40, nil }
	worked, err := acquisition.ProcessNextImport(context.Background())
	if err != nil || !worked {
		t.Fatalf("recover finalized v2 source worked=%v error=%v", worked, err)
	}
	ready, err := store.ClippingSource(source.ID)
	if err != nil || ready.Status != ClippingSourceReady || ready.SHA256 != digest {
		t.Fatalf("finalized v2 artifact did not recover to Ready: source=%+v error=%v", ready, err)
	}
	for index := 0; index < api.requestCount(); index++ {
		if api.requestAt(index).Method == http.MethodPost {
			t.Fatal("legacy v2 recovery started a paid Apify actor run")
		}
	}
}

func createYouTubeImport(t *testing.T, acquisition *ClippingAcquisition) ClippingSource {
	t.Helper()
	source, err := acquisition.CreatePublicImport(context.Background(), true, "https://www.youtube.com/watch?v=abcdefghijk", 0)
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func makeLocalYouTubeMP4(t *testing.T, ffmpeg string, videoOnly, matroska bool) string {
	t.Helper()
	container := filepath.Join(t.TempDir(), "fixture.mp4")
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc=size=320x240:rate=25"}
	if !videoOnly {
		args = append(args, "-f", "lavfi", "-i", "sine=frequency=1000:sample_rate=44100")
	}
	args = append(args, "-t", "2", "-c:v", "mpeg4", "-q:v", "1")
	if videoOnly {
		args = append(args, "-an")
	} else {
		args = append(args, "-c:a", "aac", "-b:a", "96k")
	}
	if matroska {
		args = append(args, "-f", "matroska")
	}
	args = append(args, container)
	command := exec.Command(ffmpeg, args...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("create local media fixture: %v: %s", err, output)
	}
	return container
}

func makeLocalAudioMP4(t *testing.T, ffmpeg string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audio-only.mp4")
	command := exec.Command(ffmpeg, "-nostdin", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "sine=frequency=1000:sample_rate=44100", "-t", "2", "-vn", "-c:a", "aac", "-f", "mp4", path)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("create audio-only fixture: %v: %s", err, output)
	}
	return path
}

func requireFFmpeg(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is required for real-media YouTube import tests")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe is required for real-media YouTube import tests")
	}
	return path
}

func diagnosticHas(items []ClippingSourceDiagnostic, stage string, status ClippingSourceDiagnosticStatus, code string) bool {
	for _, item := range items {
		if item.Stage == stage && item.Status == status && item.Code == code {
			return true
		}
	}
	return false
}

func jsonMarshalClippingSource(source ClippingSource) (string, error) {
	encoded, err := json.Marshal(source)
	return string(encoded), err
}

func boolPointer(value bool) *bool { return &value }
