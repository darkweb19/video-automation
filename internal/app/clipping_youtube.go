package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	clippingYouTubeRuntime               = "deno:/usr/bin/deno"
	clippingYouTubeWorkspacePollInterval = 50 * time.Millisecond
	clippingYouTubeDownloaderOutputLimit = 16 << 10
	clippingYouTubeDownloaderLineLimit   = 4 << 10
	clippingYouTubeWorkspaceEntryLimit   = 4096
	clippingYouTubeExpectationVersion    = 2
	clippingYouTubeFileLimitLauncher     = "import os,resource,sys; limit=int(sys.argv[1]); _,hard=resource.getrlimit(resource.RLIMIT_FSIZE); (hard != resource.RLIM_INFINITY and hard < limit) and sys.exit(125); resource.setrlimit(resource.RLIMIT_FSIZE,(limit,limit)); os.execv(sys.argv[2],sys.argv[2:])"
	clippingYouTubeMaxDurationSeconds    = MaxClippingSourceDurationMS / 1000
	clippingYouTubeOutputMarker          = "__FRAMEVAULT_OUTPUT__"
	clippingYouTubeFormatMarker          = "__FRAMEVAULT_FORMAT__"
)

var (
	clippingYouTubeExecutablePaths        = []string{"/usr/local/bin/yt-dlp", "/usr/bin/yt-dlp"}
	clippingYouTubePythonPaths            = []string{"/usr/bin/python3", "/usr/local/bin/python3"}
	errClippingDurationLimitExceeded      = errors.New("source duration exceeds the four-hour limit")
	errClippingYouTubeWorkspaceEntryLimit = errors.New("YouTube workspace entry limit exceeded")
)

const clippingYouTubeMaxAVSkewMS int64 = 2_000

// youtubeImportExpectation keeps only properties needed to validate a source
// after restart. Version 1 is retained for unfinished legacy imports; version
// 2 records the native downloader's output contract and bounded workspace.
type youtubeImportExpectation struct {
	Version       int     `json:"version"`
	DurationMS    int64   `json:"duration_ms,omitempty"`
	VideoCodec    string  `json:"video_codec,omitempty"`
	VideoWidth    int     `json:"video_width,omitempty"`
	VideoHeight   int     `json:"video_height,omitempty"`
	VideoFPS      float64 `json:"video_fps,omitempty"`
	VideoQuality  float64 `json:"video_quality,omitempty"`
	VideoTBR      float64 `json:"video_tbr,omitempty"`
	AudioCodec    string  `json:"audio_codec,omitempty"`
	OriginalAudio bool    `json:"original_audio,omitempty"`
	// SeparateTracks is retained in persisted JSON as the v2 workspace-reservation marker.
	// It no longer describes downloader track selection.
	SeparateTracks    bool `json:"separate_tracks,omitempty"`
	WorkspaceComplete bool `json:"workspace_complete,omitempty"`
}

type youtubeDownloaderCapture struct {
	formatSeen bool
	outputSeen bool
	bytes      int
}

type youtubeDownloaderOutputWriter struct {
	data     []byte
	exceeded bool
}

func (w *youtubeDownloaderOutputWriter) Write(data []byte) (int, error) {
	if w.exceeded || len(data) > clippingYouTubeDownloaderOutputLimit-len(w.data) {
		w.exceeded = true
		return 0, errors.New("YouTube downloader output exceeded its bound")
	}
	w.data = append(w.data, data...)
	return len(data), nil
}

func youtubeImportOutcome(reason, message string) *ClippingAcquisitionError {
	return &ClippingAcquisitionError{Status: "unsupported", Reason: reason, Message: message}
}

func youtubeProbeFailureReason(err error) string {
	if errors.Is(err, errClippingDurationLimitExceeded) {
		return "youtube_duration_limit_exceeded"
	}
	return "youtube_media_invalid"
}

func clippingImportSizeLimit(kind ClippingSourceKind, youtubeOverride int64) int64 {
	if kind == ClippingSourceYouTube && youtubeOverride > 0 && youtubeOverride < MaxClippingSourceBytes {
		return youtubeOverride
	}
	return MaxClippingSourceBytes
}

func (a *ClippingAcquisition) youtubeWorkspaceLimit() int64 {
	if a != nil && a.youtubeWorkspaceMaxBytes > 0 && a.youtubeWorkspaceMaxBytes < clippingYouTubeWorkspaceBytes {
		return a.youtubeWorkspaceMaxBytes
	}
	return clippingYouTubeWorkspaceBytes
}

func youtubeVideoID(u *url.URL) (string, bool) {
	if u == nil || u.Scheme != "https" || u.Host != "www.youtube.com" || u.Path != "/watch" {
		return "", false
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "", false
	}
	values, exists := query["v"]
	if !exists || len(values) != 1 || !validYouTubeVideoID(values[0]) {
		return "", false
	}
	return values[0], true
}

func (a *ClippingAcquisition) importYouTubeToDisk(ctx context.Context, source ClippingSource, canonicalURL *url.URL, tempPath string, trace *clippingDiagnosticTrace) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, ok := youtubeVideoID(canonicalURL); !ok {
		return withClippingDiagnosticCode(permanentClippingImportError{youtubeImportOutcome("youtube_import_unavailable", "The YouTube link could not be imported; check the public video link and try again")}, "invalid_url")
	}
	paths, err := a.sourcePaths(source.ID)
	if err != nil {
		return err
	}
	workDir := youtubeWorkspacePath(paths)
	if err := prepareYouTubeWorkspace(workDir); err != nil {
		return withClippingDiagnosticCode(fmt.Errorf("%w: prepare temporary YouTube workspace", errClippingImportRetryable), "download_failed")
	}

	expectation := youtubeImportExpectation{Version: clippingYouTubeExpectationVersion, SeparateTracks: true}
	a.store.clippingMu.Lock()
	persistErr := a.store.SetClippingSourceImportExpectation(source.ID, expectation)
	if persistErr == nil {
		persistErr = a.reserveYouTubeWorkspaceLocked(source.ID)
	}
	a.store.clippingMu.Unlock()
	if persistErr != nil {
		_ = os.RemoveAll(workDir)
		return persistErr
	}

	trace.start(ClippingDiagnosticStageDownload)
	maxBytes := clippingImportSizeLimit(source.Kind, a.youtubeMaxBytes)
	if err := a.runYouTubeDownloader(ctx, source.ID, canonicalURL, workDir, maxBytes); err != nil {
		trace.finish(ClippingDiagnosticFailed, err)
		return err
	}
	workspaceBytes, entries, workspaceErr := youtubeWorkspaceUsage(workDir)
	if workspaceErr != nil {
		return withClippingDiagnosticCode(fmt.Errorf("%w: inspect YouTube temporary workspace", errClippingImportRetryable), "download_failed")
	}
	if workspaceBytes > a.youtubeWorkspaceLimit() || entries > clippingYouTubeWorkspaceEntryLimit {
		return youtubeWorkspaceLimitFailure()
	}
	if err := a.updateYouTubeWorkspaceReservation(source.ID, workspaceBytes); err != nil {
		return err
	}

	outputPath := filepath.Join(workDir, "source.mp4")
	file, err := openClippingRegularFile(outputPath, os.O_RDONLY)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return withClippingDiagnosticCode(permanentClippingImportError{youtubeImportOutcome("youtube_format_unavailable", "No public MP4 with audio and video was produced; check that the video is publicly available and try again")}, "format_unavailable")
		}
		return withClippingDiagnosticCode(permanentClippingImportError{youtubeImportOutcome("youtube_format_unavailable", "The downloaded video could not be validated safely; check the public video and try again")}, "format_unavailable")
	}
	info, statErr := file.Stat()
	closeErr := file.Close()
	if statErr != nil || closeErr != nil {
		return fmt.Errorf("%w: inspect downloaded YouTube video", errClippingImportRetryable)
	}
	if info.Size() <= 0 {
		return withClippingDiagnosticCode(permanentClippingImportError{youtubeImportOutcome("youtube_format_unavailable", "The public video did not produce an MP4 file; check that it is available without sign-in and try again")}, "format_unavailable")
	}
	if info.Size() > maxBytes {
		return youtubeSizeLimitFailure()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	media, probeErr := a.probe(ctx, outputPath)
	if probeErr != nil {
		if errors.Is(probeErr, errClippingInvalidMedia) {
			return withClippingDiagnosticCode(permanentClippingImportError{youtubeImportOutcome("youtube_format_unavailable", "The public video does not have a supported MP4 video and audio stream; check the video and try again")}, "format_unavailable")
		}
		return probeErr
	}
	if err := validateYouTubeProbeExpectation(expectation, media); err != nil {
		return withClippingDiagnosticCode(permanentClippingImportError{youtubeImportOutcome("youtube_format_unavailable", "The public video did not produce a valid MP4 with both video and audio; check that the video is public and try again")}, "format_unavailable")
	}
	if err := syncRegularGeneratedFile(outputPath); err != nil {
		return fmt.Errorf("%w: sync downloaded YouTube video", errClippingImportRetryable)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := a.ensureImporting(source.ID); err != nil {
		return err
	}
	a.store.clippingMu.Lock()
	capacityErr := a.checkClippingStorageCapacity(0)
	a.store.clippingMu.Unlock()
	if capacityErr != nil {
		return capacityErr
	}
	if err := os.Rename(outputPath, tempPath); err != nil {
		return fmt.Errorf("%w: stage downloaded YouTube video", errClippingImportRetryable)
	}
	if err := syncClippingDirectory(filepath.Dir(paths.final)); err != nil {
		return fmt.Errorf("%w: sync staged YouTube video", errClippingImportRetryable)
	}
	if err := os.RemoveAll(workDir); err != nil {
		return fmt.Errorf("%w: remove YouTube downloader workspace", errClippingImportRetryable)
	}
	if err := syncClippingDirectory(filepath.Dir(paths.final)); err != nil {
		return fmt.Errorf("%w: sync cleaned YouTube workspace", errClippingImportRetryable)
	}
	a.store.clippingMu.Lock()
	completeErr := a.store.UpdateClippingSourceProgress(source.ID, ClippingSourceImporting, 0, info.Size())
	if completeErr == nil {
		completeErr = a.store.CompleteClippingSourceTrackWorkspace(source.ID)
	}
	if completeErr == nil {
		delete(a.store.clippingImportWorkspaces, source.ID)
	}
	a.store.clippingMu.Unlock()
	if completeErr != nil {
		return completeErr
	}
	trace.complete()
	return nil
}

func (a *ClippingAcquisition) runYouTubeDownloader(ctx context.Context, sourceID string, canonicalURL *url.URL, workDir string, maxBytes int64) error {
	executable := a.youtubeExecutable
	if executable == "" {
		executable = findYouTubeExecutable(clippingYouTubeExecutablePaths)
	}
	python := a.youtubePythonExecutable
	if python == "" {
		python = findYouTubeExecutable(clippingYouTubePythonPaths)
	}
	if executable == "" || python == "" {
		return withClippingDiagnosticCode(permanentClippingImportError{youtubeImportOutcome("youtube_import_unavailable", "The YouTube downloader is unavailable in this runtime; contact the service operator")}, "runtime_unavailable")
	}
	runtimeSpec := a.youtubeRuntime
	if runtimeSpec == "" {
		runtimeSpec = clippingYouTubeRuntime
	}
	runtimeName, runtimePath, hasRuntimePath := strings.Cut(runtimeSpec, ":")
	if !hasRuntimePath || runtimeName != "deno" || runtimePath == "" {
		return withClippingDiagnosticCode(permanentClippingImportError{youtubeImportOutcome("youtube_import_unavailable", "The YouTube downloader is unavailable in this runtime; contact the service operator")}, "runtime_unavailable")
	}
	if _, err := exec.LookPath(runtimePath); err != nil {
		return withClippingDiagnosticCode(permanentClippingImportError{youtubeImportOutcome("youtube_import_unavailable", "The YouTube JavaScript runtime is unavailable; contact the service operator")}, "runtime_unavailable")
	}
	if maxBytes <= 0 {
		return errors.New("YouTube source size limit is invalid")
	}

	cacheDir := filepath.Join(workDir, "deno-cache")
	if err := os.Mkdir(cacheDir, 0o700); err != nil {
		return fmt.Errorf("%w: prepare private YouTube runtime cache", errClippingImportRetryable)
	}
	args := youtubeDownloaderArgs(canonicalURL.String(), workDir, maxBytes, runtimeSpec)
	commandArgs := []string{"-I", "-S", "-c", clippingYouTubeFileLimitLauncher, strconv.FormatInt(maxBytes, 10), executable}
	commandArgs = append(commandArgs, args...)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	command := exec.CommandContext(runCtx, python, commandArgs...)
	command.Env = youtubeDownloaderEnvironment(cacheDir)
	command.Stderr = io.Discard
	command.WaitDelay = 2 * time.Second
	configureYouTubeSubprocess(command)
	output := &youtubeDownloaderOutputWriter{}
	command.Stdout = output
	if err := command.Start(); err != nil {
		return withClippingDiagnosticCode(fmt.Errorf("%w: start YouTube downloader", errClippingImportRetryable), "extractor_exit_failure")
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- command.Wait() }()
	ticker := time.NewTicker(clippingYouTubeWorkspacePollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			cancel()
			<-waitDone
			return ctx.Err()
		case runErr := <-waitDone:
			if output.exceeded {
				return withClippingDiagnosticCode(permanentClippingImportError{youtubeImportOutcome("youtube_format_unavailable", "The public video returned an invalid downloader response; check the video link and try again")}, "extractor_output_limit")
			}
			capture, captureErr := captureYouTubeDownloaderOutput(bytes.NewReader(output.data))
			if captureErr != nil {
				return withClippingDiagnosticCode(permanentClippingImportError{youtubeImportOutcome("youtube_format_unavailable", "The public video returned an invalid downloader response; check the video link and try again")}, "extractor_output_limit")
			}
			if runErr != nil && (youtubeSubprocessFileLimitReached(command, runErr) || youtubeWorkspaceHasFileAtLimit(workDir, maxBytes)) {
				return youtubeSizeLimitFailure()
			}
			return finishYouTubeDownloaderCommand(command, runErr, capture, ctx)
		case <-ticker.C:
			used, entries, scanErr := youtubeWorkspaceUsage(workDir)
			if scanErr != nil {
				cancel()
				<-waitDone
				if errors.Is(scanErr, errClippingYouTubeWorkspaceEntryLimit) {
					return youtubeWorkspaceLimitFailure()
				}
				return withClippingDiagnosticCode(fmt.Errorf("%w: inspect YouTube temporary workspace", errClippingImportRetryable), "download_failed")
			}
			if used > a.youtubeWorkspaceLimit() || entries > clippingYouTubeWorkspaceEntryLimit {
				cancel()
				<-waitDone
				return youtubeWorkspaceLimitFailure()
			}
			if err := a.updateYouTubeWorkspaceReservation(sourceID, used); err != nil {
				cancel()
				<-waitDone
				return err
			}
			outputBytes, outputErr := safeGeneratedFileSize(filepath.Join(workDir, "source.mp4"))
			if outputErr != nil {
				cancel()
				<-waitDone
				return withClippingDiagnosticCode(fmt.Errorf("%w: inspect YouTube output", errClippingImportRetryable), "download_failed")
			}
			if outputBytes > maxBytes {
				cancel()
				<-waitDone
				return youtubeSizeLimitFailure()
			}
		}
	}
}

func youtubeDownloaderArgs(canonicalURL, workDir string, maxBytes int64, runtimeSpec string) []string {
	return []string{
		"--ignore-config",
		"--no-plugin-dirs",
		"--no-cache-dir",
		"--no-cookies",
		"--no-playlist",
		"--no-warnings",
		"--no-progress",
		"--quiet",
		"--no-simulate",
		"--no-remote-components",
		"--js-runtimes", runtimeSpec,
		"--match-filter", fmt.Sprintf("availability=public & duration <= %d & live_status~=^(not_live|was_live)$", clippingYouTubeMaxDurationSeconds),
		"--max-filesize", strconv.FormatInt(maxBytes, 10),
		"--merge-output-format", "mp4",
		"--remux-video", "mp4",
		"--paths", "temp:" + workDir,
		"--output", filepath.Join(workDir, "source.%(ext)s"),
		"--print", "before_dl:" + clippingYouTubeFormatMarker + "%(format_id)s",
		"--print", "after_move:" + clippingYouTubeOutputMarker + "%(filepath)s",
		canonicalURL,
	}
}

func youtubeDownloaderEnvironment(cacheDir string) []string {
	allowed := map[string]struct{}{
		"http_proxy": {}, "https_proxy": {}, "all_proxy": {}, "no_proxy": {},
		"ssl_cert_file": {}, "ssl_cert_dir": {}, "requests_ca_bundle": {}, "curl_ca_bundle": {},
	}
	environment := []string{
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"HOME=/nonexistent",
		"DENO_DIR=" + cacheDir,
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
		"PYTHONDONTWRITEBYTECODE=1",
	}
	for _, entry := range os.Environ() {
		key, _, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		if _, ok := allowed[strings.ToLower(key)]; ok {
			environment = append(environment, entry)
		}
	}
	return environment
}

func captureYouTubeDownloaderOutput(reader io.Reader) (youtubeDownloaderCapture, error) {
	var capture youtubeDownloaderCapture
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 1<<10), clippingYouTubeDownloaderLineLimit)
	for scanner.Scan() {
		line := scanner.Text()
		capture.bytes += len(line) + 1
		if capture.bytes > clippingYouTubeDownloaderOutputLimit {
			return capture, errors.New("YouTube downloader output exceeded its bound")
		}
		if strings.HasPrefix(line, clippingYouTubeFormatMarker) {
			capture.formatSeen = true
		}
		if strings.HasPrefix(line, clippingYouTubeOutputMarker) {
			capture.outputSeen = true
		}
	}
	if err := scanner.Err(); err != nil {
		return capture, err
	}
	return capture, nil
}

func finishYouTubeDownloaderCommand(command *exec.Cmd, runErr error, capture youtubeDownloaderCapture, ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if youtubeSubprocessFileLimitReached(command, runErr) {
		return youtubeSizeLimitFailure()
	}
	if runErr != nil {
		return withClippingDiagnosticCode(fmt.Errorf("%w: YouTube could not download this public video; check that it is public and try again", errClippingImportRetryable), "extractor_exit_failure")
	}
	if !capture.formatSeen || !capture.outputSeen {
		return withClippingDiagnosticCode(permanentClippingImportError{youtubeImportOutcome("youtube_format_unavailable", "No public MP4 audio/video format was produced within the importer limits; check the video and try again")}, "format_unavailable")
	}
	return nil
}

func youtubeWorkspaceHasFileAtLimit(root string, limit int64) bool {
	if limit <= 0 {
		return false
	}
	found := false
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root || entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("YouTube workspace contains a symlink")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("YouTube workspace contains a non-regular file")
		}
		if info.Size() >= limit {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

func findYouTubeExecutable(candidates []string) string {
	for _, candidate := range candidates {
		path, err := exec.LookPath(candidate)
		if err == nil {
			return path
		}
	}
	return ""
}

func youtubeWorkspacePath(paths clippingSourcePaths) string {
	return filepath.Join(filepath.Dir(paths.final), "youtube-work")
}

func prepareYouTubeWorkspace(path string) error {
	if info, err := os.Lstat(path); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("YouTube workspace is not a generated directory")
		}
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		return err
	}
	return nil
}

func youtubeWorkspaceUsage(root string) (int64, int, error) {
	var total int64
	entries := 0
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			info, err := entry.Info()
			if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return errors.New("YouTube workspace root changed type")
			}
			return nil
		}
		entries++
		if entries > clippingYouTubeWorkspaceEntryLimit {
			return errClippingYouTubeWorkspaceEntryLimit
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("YouTube workspace contains a symlink")
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("YouTube workspace contains a non-regular file")
		}
		if info.Size() < 0 || total > int64(^uint64(0)>>1)-info.Size() {
			return errors.New("YouTube workspace size is invalid")
		}
		total += info.Size()
		return nil
	})
	return total, entries, err
}

func syncRegularGeneratedFile(path string) error {
	file, err := openClippingRegularFile(path, os.O_RDONLY)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func (a *ClippingAcquisition) reserveYouTubeWorkspaceLocked(sourceID string) error {
	if a.store.clippingImportWorkspaces == nil {
		a.store.clippingImportWorkspaces = make(map[string]int64)
	}
	if _, exists := a.store.clippingImportWorkspaces[sourceID]; exists {
		return nil
	}
	a.store.clippingImportWorkspaces[sourceID] = a.youtubeWorkspaceLimit()
	if err := a.checkClippingStorageCapacity(0); err != nil {
		delete(a.store.clippingImportWorkspaces, sourceID)
		return err
	}
	return nil
}

func (a *ClippingAcquisition) updateYouTubeWorkspaceReservation(sourceID string, used int64) error {
	limit := a.youtubeWorkspaceLimit()
	if used < 0 || used > limit {
		return youtubeWorkspaceLimitFailure()
	}
	a.store.clippingMu.Lock()
	defer a.store.clippingMu.Unlock()
	if a.store.clippingImportWorkspaces == nil {
		a.store.clippingImportWorkspaces = make(map[string]int64)
	}
	if _, exists := a.store.clippingImportWorkspaces[sourceID]; !exists {
		return withClippingDiagnosticCode(fmt.Errorf("%w: YouTube workspace reservation is missing", errClippingImportRetryable), "download_failed")
	}
	a.store.clippingImportWorkspaces[sourceID] = limit - used
	return a.checkClippingStorageCapacity(0)
}

func (a *ClippingAcquisition) clearYouTubeImportArtifactsLocked(sourceID string, paths clippingSourcePaths, includeFinal bool) error {
	for _, path := range []string{paths.temp, paths.videoTrack, paths.audioTrack} {
		if err := removeGeneratedRegularFile(path); err != nil {
			return err
		}
	}
	workDir := youtubeWorkspacePath(paths)
	if info, err := os.Lstat(workDir); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("YouTube workspace is not a generated directory")
		}
		if err := os.RemoveAll(workDir); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if includeFinal {
		if err := removeGeneratedRegularFile(paths.final); err != nil {
			return err
		}
	}
	if err := syncClippingDirectory(filepath.Dir(paths.final)); err != nil {
		return err
	}
	delete(a.store.clippingImportWorkspaces, sourceID)
	return nil
}

func youtubeFinalExpectationMatches(source ClippingSource, info os.FileInfo) bool {
	if info == nil || !info.Mode().IsRegular() || source.ImportExpectedSize <= 0 || source.ImportExpectedSize != info.Size() || !validSHA256Hex(source.ImportExpectedSHA) {
		return false
	}
	var expectation youtubeImportExpectation
	if json.Unmarshal([]byte(source.ImportExpectation), &expectation) != nil || !validYouTubeImportExpectation(expectation) {
		return false
	}
	return expectation.Version == clippingYouTubeExpectationVersion && expectation.SeparateTracks && expectation.WorkspaceComplete
}

func validYouTubeImportExpectation(expectation youtubeImportExpectation) bool {
	switch expectation.Version {
	case 1:
		return expectation.DurationMS > 0 && expectation.DurationMS <= MaxClippingSourceDurationMS &&
			reasonableClippingDimensions(expectation.VideoWidth, expectation.VideoHeight) &&
			isSupportedExpectedYouTubeCodec("video", expectation.VideoCodec) && isSupportedExpectedYouTubeCodec("audio", expectation.AudioCodec) &&
			finiteNonnegative(expectation.VideoFPS) && finiteNumber(expectation.VideoQuality) && finiteNonnegative(expectation.VideoTBR)
	case clippingYouTubeExpectationVersion:
		return expectation.DurationMS == 0 && expectation.SeparateTracks && expectation.VideoCodec == "" && expectation.AudioCodec == "" &&
			expectation.VideoWidth == 0 && expectation.VideoHeight == 0 && expectation.VideoFPS == 0 && expectation.VideoQuality == 0 && expectation.VideoTBR == 0
	default:
		return false
	}
}

func validateYouTubeProbeExpectation(expectation youtubeImportExpectation, media clippingMediaProbeResult) error {
	if !validYouTubeImportExpectation(expectation) || media.AudioStreams != 1 || media.VideoCodec == "" || media.AudioCodec == "" {
		return errors.Join(errClippingInvalidMedia, errClippingYouTubeAVStreamMissing)
	}
	if expectation.Version == clippingYouTubeExpectationVersion {
		if !isYouTubeMP4Container(media.FormatName) {
			return errors.Join(errClippingInvalidMedia, errClippingYouTubeAVStreamMissing)
		}
		if media.DurationMS <= 0 || media.DurationMS > MaxClippingSourceDurationMS || media.VideoTimingValid && media.AudioTimingValid &&
			(media.VideoDurationMS <= 0 || media.AudioDurationMS <= 0 ||
				!youtubeTimesWithin(media.VideoDurationMS, media.AudioDurationMS, clippingYouTubeDurationToleranceMS) ||
				!youtubeTimesWithin(media.VideoStartMS, media.AudioStartMS, clippingYouTubeMaxAVSkewMS) ||
				media.VideoStartMS < -clippingYouTubeMaxAVSkewMS || media.AudioStartMS < -clippingYouTubeMaxAVSkewMS ||
				media.VideoStartMS > media.DurationMS+clippingYouTubeMaxAVSkewMS || media.AudioStartMS > media.DurationMS+clippingYouTubeMaxAVSkewMS) {
			return errors.Join(errClippingInvalidMedia, errClippingYouTubeAVTimingMismatch)
		}
		return nil
	}
	if expectation.Version != 1 || media.VideoCodec != expectation.VideoCodec || media.AudioCodec != expectation.AudioCodec ||
		media.Width != expectation.VideoWidth || media.Height != expectation.VideoHeight ||
		!media.VideoTimingValid || !media.AudioTimingValid || media.VideoDurationMS <= 0 || media.AudioDurationMS <= 0 {
		return errors.Join(errClippingInvalidMedia, errClippingYouTubeAVStreamMissing)
	}
	if media.DurationMS <= 0 || media.DurationMS > MaxClippingSourceDurationMS ||
		!youtubeTimesWithin(media.DurationMS, expectation.DurationMS, clippingYouTubeDurationToleranceMS) ||
		!youtubeTimesWithin(media.VideoDurationMS, media.AudioDurationMS, clippingYouTubeDurationToleranceMS) ||
		!youtubeTimesWithin(media.VideoDurationMS, media.DurationMS, clippingYouTubeDurationToleranceMS) ||
		!youtubeTimesWithin(media.AudioDurationMS, media.DurationMS, clippingYouTubeDurationToleranceMS) ||
		!youtubeTimesWithin(media.VideoStartMS, media.AudioStartMS, clippingYouTubeMaxAVSkewMS) ||
		media.VideoStartMS < -clippingYouTubeMaxAVSkewMS || media.AudioStartMS < -clippingYouTubeMaxAVSkewMS ||
		media.VideoStartMS > media.DurationMS+clippingYouTubeMaxAVSkewMS || media.AudioStartMS > media.DurationMS+clippingYouTubeMaxAVSkewMS {
		return errors.Join(errClippingInvalidMedia, errClippingYouTubeAVTimingMismatch)
	}
	if expectation.VideoFPS > 0 && (!media.VideoFPSValid || math.Abs(media.VideoFPS-expectation.VideoFPS) > 0.5) {
		return errors.Join(errClippingInvalidMedia, errClippingYouTubeAVTimingMismatch)
	}
	return nil
}

func isYouTubeMP4Container(formatName string) bool {
	for _, name := range strings.Split(strings.ToLower(formatName), ",") {
		if strings.TrimSpace(name) == "mp4" {
			return true
		}
	}
	return false
}

func finiteNonnegative(value float64) bool {
	return finiteNumber(value) && value >= 0
}

func finiteNumber(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func isSupportedExpectedYouTubeCodec(kind, codec string) bool {
	if kind == "video" {
		_, ok := clippingSupportedVideoCodecs[codec]
		return ok
	}
	if kind == "audio" {
		_, ok := clippingSupportedAudioCodecs[codec]
		return ok
	}
	return false
}

func youtubeSizeLimitFailure() error {
	return withClippingDiagnosticCode(permanentClippingImportError{youtubeImportOutcome("youtube_size_limit_exceeded", "The YouTube video exceeds the 20 GiB source limit; choose a smaller public video and try again")}, "size_limit_exceeded")
}

func youtubeWorkspaceLimitFailure() error {
	return withClippingDiagnosticCode(permanentClippingImportError{youtubeImportOutcome("youtube_size_limit_exceeded", "Temporary YouTube download files exceeded the workspace limit; choose a shorter or smaller public video")}, "size_limit_exceeded")
}
