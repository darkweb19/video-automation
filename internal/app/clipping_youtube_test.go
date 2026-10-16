package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestYouTubeDownloaderArgsAreFixedForOnePublicMP4(t *testing.T) {
	args := youtubeDownloaderArgs("https://www.youtube.com/watch?v=abcdefghijk", "/private/work", 12345, "deno:/usr/bin/deno")
	joined := strings.Join(args, "\n")
	for _, required := range []string{
		"--ignore-config", "--no-plugin-dirs", "--no-cookies", "--no-playlist", "--no-remote-components",
		"--js-runtimes", "deno:/usr/bin/deno", "--match-filter", "availability=public & duration <= 14400 & live_status~=^(not_live|was_live)$",
		"--max-filesize", "12345", "--merge-output-format", "mp4", "--remux-video", "mp4",
		"--output", filepath.Join("/private/work", "source.%(ext)s"), "https://www.youtube.com/watch?v=abcdefghijk",
	} {
		if !strings.Contains(joined, required) {
			t.Errorf("native downloader arguments omitted %q: %q", required, args)
		}
	}
	for _, forbidden := range []string{"--format", "--proxy", "--cookies", "--extractor-args", "signature", "googlevideo.com"} {
		for _, arg := range args {
			if arg == forbidden || strings.Contains(arg, forbidden) {
				t.Errorf("unexpected custom selection, credential, proxy, or media URL argument %q in %q", forbidden, args)
			}
		}
	}
}

func TestYouTubeDownloaderInheritsOnlyTransportAndCertificateEnvironment(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://proxy.example:8443")
	t.Setenv("REQUESTS_CA_BUNDLE", "/etc/ssl/test-ca.pem")
	t.Setenv("YOUTUBE_IMPORT_TEST_SECRET", "must-not-pass")
	values := map[string]string{}
	for _, entry := range youtubeDownloaderEnvironment(t.TempDir()) {
		key, value, found := strings.Cut(entry, "=")
		if found {
			values[key] = value
		}
	}
	if values["HTTPS_PROXY"] != "http://proxy.example:8443" || values["REQUESTS_CA_BUNDLE"] != "/etc/ssl/test-ca.pem" {
		t.Fatalf("transport proxy/CA settings were not inherited: %#v", values)
	}
	if _, exists := values["YOUTUBE_IMPORT_TEST_SECRET"]; exists {
		t.Fatalf("unrelated process environment leaked to downloader: %#v", values)
	}
}

func TestYouTubeImportDownloadsAndPublishesRealMP4(t *testing.T) {
	ffmpeg := requireFFmpeg(t)
	media := makeLocalYouTubeMP4(t, ffmpeg, false, false)
	_, store, _, acquisition := newClippingAcquisitionFixture(t)
	argsPath := filepath.Join(t.TempDir(), "yt-dlp-args.bin")
	configureFakeYouTubeDownloader(t, acquisition, ffmpeg, media, "remux", argsPath)
	source := createYouTubeImport(t, acquisition)
	worked, err := acquisition.ProcessNextImport(context.Background())
	if err != nil || !worked {
		t.Fatalf("process YouTube import worked=%v error=%v", worked, err)
	}
	loaded, err := store.ClippingSource(source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != ClippingSourceReady || loaded.SizeBytes <= 0 || !validSHA256Hex(loaded.SHA256) {
		t.Fatalf("source was not atomically marked ready with a durable hash: %+v", loaded)
	}
	if loaded.OriginalName != "youtube-abcdefghijk.mp4" || loaded.MediaType != "video/mp4" {
		t.Fatalf("validated YouTube MP4 has unexpected user-facing file metadata: name=%q mediaType=%q", loaded.OriginalName, loaded.MediaType)
	}
	paths, err := acquisition.sourcePaths(source.ID)
	if err != nil {
		t.Fatal(err)
	}
	probe, err := probeClippingMedia(context.Background(), paths.final)
	if err != nil || !isYouTubeMP4Container(probe.FormatName) || probe.AudioStreams < 1 {
		t.Fatalf("final file is not a probed MP4 with audio: format=%q streams=%d err=%v", probe.FormatName, probe.AudioStreams, err)
	}
	if _, err := os.Stat(youtubeWorkspacePath(paths)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary downloader workspace was not removed: %v", err)
	}
	if _, err := os.Stat(paths.temp); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary source stage remained after Ready: %v", err)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	argv := splitNUL(args)
	if containsArgument(argv, "--format") || containsArgument(argv, "--proxy") || !containsArgument(argv, "--remux-video") || !containsArgument(argv, "--no-cookies") {
		t.Fatalf("fake downloader saw unexpected command shape: %q", argv)
	}
}

func TestYouTubeImportRejectsMislabeledNonMP4Container(t *testing.T) {
	ffmpeg := requireFFmpeg(t)
	media := makeLocalYouTubeMP4(t, ffmpeg, false, true)
	_, store, _, acquisition := newClippingAcquisitionFixture(t)
	configureFakeYouTubeDownloader(t, acquisition, ffmpeg, media, "copy", "")
	source := createYouTubeImport(t, acquisition)
	worked, err := acquisition.ProcessNextImport(context.Background())
	if err != nil || !worked {
		t.Fatalf("process non-MP4 import worked=%v error=%v", worked, err)
	}
	loaded, err := store.ClippingSource(source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != ClippingSourceFailed || loaded.Failure != "youtube_format_unavailable" {
		t.Fatalf("mislabeled Matroska source was not rejected: status=%s failure=%q", loaded.Status, loaded.Failure)
	}
}

func TestYouTubeImportRejectsPublicVideoWithoutAudio(t *testing.T) {
	ffmpeg := requireFFmpeg(t)
	media := makeLocalYouTubeMP4(t, ffmpeg, true, false)
	_, store, _, acquisition := newClippingAcquisitionFixture(t)
	configureFakeYouTubeDownloader(t, acquisition, ffmpeg, media, "remux", "")
	source := createYouTubeImport(t, acquisition)
	worked, err := acquisition.ProcessNextImport(context.Background())
	if err != nil || !worked {
		t.Fatalf("process video-only import worked=%v error=%v", worked, err)
	}
	loaded, err := store.ClippingSource(source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != ClippingSourceFailed || loaded.Failure != "youtube_format_unavailable" {
		t.Fatalf("video without audio was not rejected: status=%s failure=%q", loaded.Status, loaded.Failure)
	}
}

func TestYouTubeImportRejectsBadLinkBeforeStartingDownloader(t *testing.T) {
	_, _, _, acquisition := newClippingAcquisitionFixture(t)
	calledPath := filepath.Join(t.TempDir(), "downloader-was-called")
	acquisition.youtubeExecutable = writeFakeYouTubeExecutable(t, t.TempDir(), "yt-dlp", "touch "+shellQuote(calledPath)+"\n")
	acquisition.youtubePythonExecutable = requirePython(t)
	acquisition.youtubeRuntime = "deno:" + writeFakeYouTubeExecutable(t, t.TempDir(), "deno", "exit 0\n")
	if _, err := acquisition.CreatePublicImport(context.Background(), true, "https://youtube.com.evil.example/watch?v=abcdefghijk", 0); err == nil {
		t.Fatal("host-suffix attack was accepted")
	}
	if _, err := os.Stat(calledPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid link started downloader: %v", err)
	}
}

func TestYouTubeImportFailureRedactsSignedOutputAndCleansWorkspace(t *testing.T) {
	_, store, _, acquisition := newClippingAcquisitionFixture(t)
	secret := "signed-media-secret-marker"
	acquisition.youtubeExecutable = writeFakeYouTubeExecutable(t, t.TempDir(), "yt-dlp", "printf 'https://media.example/video?sig="+secret+"\\n' >&2\nexit 17\n")
	acquisition.youtubePythonExecutable = requirePython(t)
	acquisition.youtubeRuntime = "deno:" + writeFakeYouTubeExecutable(t, t.TempDir(), "deno", "exit 0\n")
	source := createYouTubeImport(t, acquisition)
	worked, err := acquisition.ProcessNextImport(context.Background())
	if err != nil || !worked {
		t.Fatalf("process failed downloader worked=%v error=%v", worked, err)
	}
	loaded, err := store.ClippingSource(source.ID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := jsonMarshalClippingSource(loaded)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(encoded, secret) || strings.Contains(encoded, "media.example") || strings.Contains(loaded.Failure, secret) {
		t.Fatalf("signed media URL leaked to durable source state: %s", encoded)
	}
	if loaded.Status != ClippingSourceImporting || !diagnosticHas(loaded.Diagnostics, ClippingDiagnosticStageDownload, ClippingDiagnosticFailed, "extractor_exit_failure") {
		t.Fatalf("downloader failure did not retry with a safe diagnostic: status=%s diagnostics=%+v", loaded.Status, loaded.Diagnostics)
	}
	paths, err := acquisition.sourcePaths(source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(youtubeWorkspacePath(paths)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retry left a stale downloader workspace: %v", err)
	}
}

func TestYouTubeBotChallengeFailsWithoutAutomaticRetriesOrLeakingOutput(t *testing.T) {
	_, store, _, acquisition := newClippingAcquisitionFixture(t)
	secret := "signed-media-secret-marker"
	acquisition.youtubeExecutable = writeFakeYouTubeExecutable(t, t.TempDir(), "yt-dlp",
		"printf \"ERROR: Sign in to confirm you're not a bot. https://media.example/video?token="+secret+"\\n\" >&2\nexit 1\n")
	acquisition.youtubePythonExecutable = requirePython(t)
	acquisition.youtubeRuntime = "deno:" + writeFakeYouTubeExecutable(t, t.TempDir(), "deno", "exit 0\n")
	source := createYouTubeImport(t, acquisition)

	worked, err := acquisition.ProcessNextImport(context.Background())
	if err != nil || !worked {
		t.Fatalf("process challenged downloader worked=%v error=%v", worked, err)
	}
	loaded, err := store.ClippingSource(source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != ClippingSourceFailed || loaded.ImportAttempts != 1 || loaded.Failure != "youtube_bot_challenge" {
		t.Fatalf("bot challenge should fail once with a distinct reason: status=%s attempts=%d failure=%s", loaded.Status, loaded.ImportAttempts, loaded.Failure)
	}
	if !diagnosticHas(loaded.Diagnostics, ClippingDiagnosticStageDownload, ClippingDiagnosticFailed, "bot_challenge") {
		t.Fatalf("missing safe bot_challenge diagnostic: %+v", loaded.Diagnostics)
	}
	encoded, err := jsonMarshalClippingSource(loaded)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(encoded, secret) || strings.Contains(encoded, "media.example") {
		t.Fatal("sensitive extractor text leaked to durable state")
	}
	view := clippingSourceSummary(loaded)
	if view.Error != "youtube_bot_challenge" || !strings.Contains(view.ErrorMessage, "verification of automated access") {
		t.Fatalf("source API lacks actionable message: error=%s message=%s", view.Error, view.ErrorMessage)
	}
	paths, err := acquisition.sourcePaths(source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(youtubeWorkspacePath(paths)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("terminal bot challenge left a temporary workspace: %v", err)
	}
	worked, err = acquisition.ProcessNextImport(context.Background())
	if err != nil || worked {
		t.Fatalf("terminal bot challenge was retried: worked=%v error=%v", worked, err)
	}
}

func TestYouTubeImportPerFileLimitStopsOversizeDownloader(t *testing.T) {
	ffmpeg := requireFFmpeg(t)
	media := makeLocalYouTubeMP4(t, ffmpeg, false, false)
	_, store, _, acquisition := newClippingAcquisitionFixture(t)
	configureFakeYouTubeDownloader(t, acquisition, ffmpeg, media, "remux", "")
	acquisition.youtubeMaxBytes = 4096
	source := createYouTubeImport(t, acquisition)
	worked, err := acquisition.ProcessNextImport(context.Background())
	if err != nil || !worked {
		t.Fatalf("process oversize import worked=%v error=%v", worked, err)
	}
	loaded, err := store.ClippingSource(source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != ClippingSourceFailed || loaded.Failure != "youtube_size_limit_exceeded" {
		t.Fatalf("per-file cap did not reject oversize output: status=%s failure=%q diagnostics=%+v", loaded.Status, loaded.Failure, loaded.Diagnostics)
	}
	if !diagnosticHas(loaded.Diagnostics, ClippingDiagnosticStageDownload, ClippingDiagnosticFailed, "size_limit_exceeded") {
		t.Fatalf("per-file limit diagnostic missing: %+v", loaded.Diagnostics)
	}
}

func TestYouTubeImportBoundsInputPlusRemuxWorkspace(t *testing.T) {
	ffmpeg := requireFFmpeg(t)
	media := makeLocalYouTubeMP4(t, ffmpeg, false, false)
	info, err := os.Stat(media)
	if err != nil {
		t.Fatal(err)
	}
	_, store, _, acquisition := newClippingAcquisitionFixture(t)
	configureFakeYouTubeDownloader(t, acquisition, ffmpeg, media, "workspace", "")
	acquisition.youtubeMaxBytes = info.Size() + 1<<20
	acquisition.youtubeWorkspaceMaxBytes = info.Size() * 18 / 10
	source := createYouTubeImport(t, acquisition)
	worked, err := acquisition.ProcessNextImport(context.Background())
	if err != nil || !worked {
		t.Fatalf("process workspace-limited import worked=%v error=%v", worked, err)
	}
	loaded, err := store.ClippingSource(source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != ClippingSourceFailed || loaded.Failure != "youtube_size_limit_exceeded" {
		t.Fatalf("aggregate input+remux workspace cap did not fail safely: input=%d cap=%d status=%s failure=%q diagnostics=%+v", info.Size(), acquisition.youtubeWorkspaceMaxBytes, loaded.Status, loaded.Failure, loaded.Diagnostics)
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

func configureFakeYouTubeDownloader(t *testing.T, acquisition *ClippingAcquisition, ffmpeg, input, mode, argsPath string) {
	t.Helper()
	acquisition.youtubePythonExecutable = requirePython(t)
	acquisition.youtubeRuntime = "deno:" + writeFakeYouTubeExecutable(t, t.TempDir(), "deno", "exit 0\n")
	var script strings.Builder
	script.WriteString("set -eu\n")
	if argsPath != "" {
		script.WriteString("printf '%s\\0' \"$@\" > " + shellQuote(argsPath) + "\n")
	}
	if mode == "fail" {
		script.WriteString("printf 'https://signed.example/video?sig=fake-secret\\n' >&2\nexit 17\n")
	} else if mode == "block" {
		script.WriteString("(sleep 2; touch " + shellQuote(input+".survived") + ") &\necho $! > " + shellQuote(input+".pid") + "\ntouch " + shellQuote(input+".started") + "\nwait\n")
	} else {
		script.WriteString("output_template=''\nwhile [ \"$#\" -gt 0 ]; do\n  if [ \"$1\" = '--output' ]; then shift; output_template=$1; fi\n  shift\ndone\ncase \"$output_template\" in\n  *'.%(ext)s') output=\"${output_template%.*}.mp4\" ;;\n  *) exit 24 ;;\nesac\n")
		if mode == "workspace" {
			script.WriteString("cp " + shellQuote(input) + " \"$output.partial\"\n")
		}
		if mode == "copy" {
			script.WriteString("cp " + shellQuote(input) + " \"$output\"\n")
		} else {
			script.WriteString(shellQuote(ffmpeg) + " -nostdin -hide_banner -loglevel error -y -i " + shellQuote(input) + " -map 0:v:0 -map '0:a?' -c copy -f mp4 \"$output\"\n")
		}
		if mode == "workspace" {
			script.WriteString("sleep 0.5\n")
		}
		script.WriteString("printf '__FRAMEVAULT_FORMAT__synthetic\\n'\nprintf '__FRAMEVAULT_OUTPUT__%s\\n' \"$output\"\n")
	}
	acquisition.youtubeExecutable = writeFakeYouTubeExecutable(t, t.TempDir(), "yt-dlp", script.String())
}

func writeFakeYouTubeExecutable(t *testing.T, directory, name, body string) string {
	t.Helper()
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
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

func requireFFmpeg(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is required for native YouTube downloader integration tests")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe is required for native YouTube downloader integration tests")
	}
	return path
}

func requirePython(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is required for native YouTube downloader integration tests")
	}
	return path
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func splitNUL(value []byte) []string {
	parts := bytes.Split(value, []byte{0})
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) != 0 {
			result = append(result, string(part))
		}
	}
	return result
}

func containsArgument(arguments []string, expected string) bool {
	for _, argument := range arguments {
		if argument == expected {
			return true
		}
	}
	return false
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
