package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const clippingYouTubeMuxLimitLauncher = "import os,resource,sys; limit=int(sys.argv[1]); _,hard=resource.getrlimit(resource.RLIMIT_FSIZE); (hard != resource.RLIM_INFINITY and hard < limit) and sys.exit(125); resource.setrlimit(resource.RLIMIT_FSIZE,(limit,limit)); os.execv(sys.argv[2],sys.argv[2:])"

const (
	clippingYouTubeTrackTransferAttempts = 2
)

var clippingYouTubeTrackRetryDelay = 250 * time.Millisecond

var errClippingYouTubePeerTrackFailed = errors.New("peer YouTube track download failed")

type youtubeTrackTransferRetryError struct{ err error }

func (e youtubeTrackTransferRetryError) Error() string { return e.err.Error() }
func (e youtubeTrackTransferRetryError) Unwrap() error { return e.err }

func retryableYouTubeTrackTransfer(err error) error {
	if err == nil {
		return nil
	}
	return youtubeTrackTransferRetryError{err: err}
}

func isRetryableYouTubeTrackTransfer(err error) bool {
	var retryable youtubeTrackTransferRetryError
	return errors.As(err, &retryable)
}

func youtubeSelectionForTrack(format youtubeFormat, expectedID, role string) (youtubeSelection, error) {
	mediaURL, err := url.Parse(format.URL)
	if err != nil || validateYouTubeMediaURL(mediaURL) != nil {
		return youtubeSelection{}, errUnsafeClippingDestination
	}
	userAgent, referer, err := safeYouTubeRequestHeaders(format.HTTPHeaders, expectedID)
	if err != nil {
		return youtubeSelection{}, permanentClippingImportError{youtubeImportOutcome("youtube_format_unavailable", "No supported audio/video format is available; upload an original file you are permitted to reuse")}
	}
	selection := youtubeSelection{URL: mediaURL, UserAgent: userAgent, MediaRole: role}
	if referer {
		selection.Referer = "https://www.youtube.com/watch?v=" + expectedID
	}
	return selection, nil
}

func (a *ClippingAcquisition) reserveYouTubeWorkspaceLocked(sourceID string) error {
	if a.store.clippingImportWorkspaces == nil {
		a.store.clippingImportWorkspaces = make(map[string]int64)
	}
	if _, exists := a.store.clippingImportWorkspaces[sourceID]; exists {
		return nil
	}
	a.store.clippingImportWorkspaces[sourceID] = clippingYouTubeTrackWorkspaceBytes
	if err := a.checkClippingStorageCapacity(0); err != nil {
		delete(a.store.clippingImportWorkspaces, sourceID)
		return err
	}
	return nil
}

func (a *ClippingAcquisition) importYouTubeSeparateTracks(ctx context.Context, source ClippingSource, videoID, tempPath string, selection youtubeSelection, expectation youtubeImportExpectation, trace *clippingDiagnosticTrace) error {
	if !selection.Separate || selection.Tracks.Video == nil || selection.Tracks.Audio == nil || !validYouTubeImportExpectation(expectation) {
		return withClippingDiagnosticCode(permanentClippingImportError{youtubeImportOutcome("youtube_format_unavailable", "No compatible separate audio/video tracks are available; upload an original file you are permitted to reuse")}, "format_unavailable")
	}
	maxTracks := clippingImportSizeLimit(source.Kind, a.youtubeMaxBytes)
	if selection.SizeBytes > 0 && selection.SizeBytes > maxTracks {
		return withClippingDiagnosticCode(permanentClippingImportError{youtubeImportOutcome("youtube_size_limit_exceeded", "The selected source tracks exceed the 20 GiB limit; upload a smaller original file you are permitted to reuse")}, "track_size_limit_exceeded")
	}
	paths, err := a.sourcePaths(source.ID)
	if err != nil {
		return err
	}
	videoRequest, err := youtubeSelectionForTrack(*selection.Tracks.Video, videoID, "video")
	if err != nil {
		return err
	}
	audioRequest, err := youtubeSelectionForTrack(*selection.Tracks.Audio, videoID, "audio")
	if err != nil {
		return err
	}
	trackCtx, cancelTracks := context.WithCancelCause(ctx)
	defer cancelTracks(nil)
	type trackDownloadResult struct {
		role string
		err  error
	}
	results := make(chan trackDownloadResult, 2)
	go func() {
		results <- trackDownloadResult{role: "video", err: a.downloadYouTubeTrack(trackCtx, source.ID, videoRequest, *selection.Tracks.Video, paths.videoTrack, "video", maxTracks, trace.fork())}
	}()
	go func() {
		results <- trackDownloadResult{role: "audio", err: a.downloadYouTubeTrack(trackCtx, source.ID, audioRequest, *selection.Tracks.Audio, paths.audioTrack, "audio", maxTracks, trace.fork())}
	}()
	first := <-results
	if first.err != nil && trackCtx.Err() == nil {
		cancelTracks(errClippingYouTubePeerTrackFailed)
	}
	second := <-results
	if err := ctx.Err(); err != nil {
		return err
	}
	if first.err != nil {
		return first.err
	}
	if second.err != nil {
		return second.err
	}
	if first.role == second.role {
		return errors.New("duplicate YouTube track download result")
	}
	a.store.clippingMu.Lock()
	capacityErr := a.checkClippingStorageCapacity(0)
	a.store.clippingMu.Unlock()
	if capacityErr != nil {
		return capacityErr
	}
	trace.start(ClippingDiagnosticStageTrackMux)
	if err := syncClippingDirectory(filepath.Dir(paths.final)); err != nil {
		err = withClippingDiagnosticCode(fmt.Errorf("%w: sync staged tracks", errClippingImportRetryable), "track_mux_failed")
		trace.finish(ClippingDiagnosticFailed, err)
		return err
	}
	if err := a.muxYouTubeTracks(ctx, source.ID, paths.videoTrack, paths.audioTrack, tempPath, maxTracks); err != nil {
		trace.finish(ClippingDiagnosticFailed, err)
		return err
	}
	info, err := os.Lstat(tempPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
		err = withClippingDiagnosticCode(permanentClippingImportError{errClippingInvalidMedia}, "track_content_invalid")
		trace.finish(ClippingDiagnosticFailed, err)
		return err
	}
	if info.Size() > maxTracks || info.Size() > MaxClippingSourceBytes {
		err = withClippingDiagnosticCode(permanentClippingImportError{youtubeImportOutcome("youtube_size_limit_exceeded", "The merged source exceeds the 20 GiB limit; upload smaller original tracks you are permitted to reuse")}, "mux_output_size_limit_exceeded")
		trace.finish(ClippingDiagnosticFailed, err)
		return err
	}
	file, err := openClippingRegularFile(tempPath, os.O_RDONLY)
	if err != nil {
		err = withClippingDiagnosticCode(fmt.Errorf("%w: open merged source", errClippingImportRetryable), "track_mux_failed")
		trace.finish(ClippingDiagnosticFailed, err)
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		err = withClippingDiagnosticCode(fmt.Errorf("%w: sync merged source", errClippingImportRetryable), "track_mux_failed")
		trace.finish(ClippingDiagnosticFailed, err)
		return err
	}
	if err := file.Close(); err != nil {
		err = withClippingDiagnosticCode(fmt.Errorf("%w: close merged source", errClippingImportRetryable), "track_mux_failed")
		trace.finish(ClippingDiagnosticFailed, err)
		return err
	}
	if err := syncClippingDirectory(filepath.Dir(paths.final)); err != nil {
		err = withClippingDiagnosticCode(fmt.Errorf("%w: sync merged source directory", errClippingImportRetryable), "track_mux_failed")
		trace.finish(ClippingDiagnosticFailed, err)
		return err
	}
	if err := a.cleanupYouTubeTracks(source.ID, paths); err != nil {
		err = withClippingDiagnosticCode(fmt.Errorf("%w: clean staged tracks", errClippingImportRetryable), "track_mux_failed")
		trace.finish(ClippingDiagnosticFailed, err)
		return err
	}
	if err := a.store.UpdateClippingSourceProgress(source.ID, ClippingSourceImporting, 0, info.Size()); err != nil {
		err = withClippingDiagnosticCode(fmt.Errorf("%w: persist merged source size", errClippingImportRetryable), "track_mux_failed")
		trace.finish(ClippingDiagnosticFailed, err)
		return err
	}
	trace.complete()
	return nil
}

func (a *ClippingAcquisition) commitYouTubeTrackChunk(ctx context.Context, sourceID string, file *os.File, chunk []byte, previous, maxTracks int64) error {
	a.store.clippingMu.Lock()
	defer a.store.clippingMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	source, err := a.store.ClippingSource(sourceID)
	if err != nil {
		return err
	}
	if source.Status != ClippingSourceImporting {
		return errors.New("source import was canceled")
	}
	remaining, reserved := a.store.clippingImportWorkspaces[sourceID]
	if !reserved || remaining < 0 || remaining > clippingYouTubeTrackWorkspaceBytes {
		return errors.New("source track workspace reservation is missing")
	}
	used := clippingYouTubeTrackWorkspaceBytes - remaining
	if importStreamExceedsLimit(used, int64(len(chunk)), maxTracks) || importStreamExceedsLimit(used, int64(len(chunk)), clippingYouTubeTrackWorkspaceBytes) {
		return withClippingDiagnosticCode(permanentClippingImportError{youtubeImportOutcome("youtube_size_limit_exceeded", "The selected source tracks exceed the 20 GiB limit; upload smaller original tracks you are permitted to reuse")}, "track_size_limit_exceeded")
	}
	if _, err := file.Seek(previous, io.SeekStart); err != nil {
		return fmt.Errorf("%w: seek staged track", errClippingImportRetryable)
	}
	if err := writeFull(file, chunk); err != nil {
		return fmt.Errorf("%w: write staged track", errClippingImportRetryable)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("%w: sync staged track", errClippingImportRetryable)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	a.store.clippingImportWorkspaces[sourceID] = remaining - int64(len(chunk))
	return nil
}

func (a *ClippingAcquisition) downloadYouTubeTrack(ctx context.Context, sourceID string, request youtubeSelection, format youtubeFormat, localPath, role string, maxTracks int64, trace *clippingDiagnosticTrace) error {
	stage := ClippingDiagnosticStageVideoDownload
	if role == "audio" {
		stage = ClippingDiagnosticStageAudioDownload
	}
	for attempt := 1; attempt <= clippingYouTubeTrackTransferAttempts; attempt++ {
		trace.start(stage)
		committed, err := a.downloadYouTubeTrackAttempt(ctx, sourceID, request, format, localPath, role, maxTracks)
		if err == nil {
			trace.complete()
			return nil
		}
		if ctx.Err() != nil {
			failure := ctx.Err()
			if errors.Is(context.Cause(ctx), errClippingYouTubePeerTrackFailed) {
				failure = withClippingDiagnosticCode(failure, "track_transfer_aborted")
			}
			trace.finish(ClippingDiagnosticFailed, failure)
			return ctx.Err()
		}
		retrying := attempt < clippingYouTubeTrackTransferAttempts && isRetryableYouTubeTrackTransfer(err)
		status := ClippingDiagnosticFailed
		if retrying {
			status = ClippingDiagnosticRetrying
		}
		trace.finish(status, err)
		if !retrying {
			return err
		}
		if resetErr := a.resetYouTubeTrackAttempt(sourceID, localPath, committed); resetErr != nil {
			coded := withClippingDiagnosticCode(fmt.Errorf("%w: reset failed track", errClippingImportRetryable), "track_storage_failed")
			trace.record(stage, ClippingDiagnosticFailed, clippingDiagnosticCode(stage, coded))
			return coded
		}
		timer := time.NewTimer(clippingYouTubeTrackRetryDelay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			failure := ctx.Err()
			if errors.Is(context.Cause(ctx), errClippingYouTubePeerTrackFailed) {
				failure = withClippingDiagnosticCode(failure, "track_transfer_aborted")
			}
			trace.record(stage, ClippingDiagnosticFailed, clippingDiagnosticCode(stage, failure))
			return ctx.Err()
		case <-timer.C:
		}
	}
	return errors.New("YouTube track transfer attempts exhausted")
}

func (a *ClippingAcquisition) downloadYouTubeTrackAttempt(ctx context.Context, sourceID string, request youtubeSelection, format youtubeFormat, localPath, role string, maxTracks int64) (int64, error) {
	stage := ClippingDiagnosticStageVideoDownload
	if role == "audio" {
		stage = ClippingDiagnosticStageAudioDownload
	}
	response, err := a.fetchYouTubeMediaURL(ctx, request)
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		if isPermanentClippingImportError(err) {
			return 0, withClippingDiagnosticCode(err, clippingDiagnosticCode(stage, err))
		}
		coded := withClippingDiagnosticCode(fmt.Errorf("%w: selected track request failed", errClippingImportRetryable), "track_request_failed")
		return 0, retryableYouTubeTrackTransfer(coded)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Range") != "" {
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusGone {
			// Signed media URLs can be denied after extraction. Let the durable
			// import retry re-extract metadata before treating that URL as final.
			coded := withClippingDiagnosticCode(fmt.Errorf("%w: selected track access was denied", errClippingImportRetryable), "track_access_denied")
			return 0, coded
		}
		if response.StatusCode == http.StatusRequestTimeout || response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500 && response.StatusCode <= 599 {
			coded := withClippingDiagnosticCode(fmt.Errorf("%w: selected track endpoint returned a retryable status", errClippingImportRetryable), "track_http_retryable")
			return 0, retryableYouTubeTrackTransfer(coded)
		}
		coded := withClippingDiagnosticCode(permanentClippingImportError{youtubeImportOutcome("youtube_format_unavailable", "The selected YouTube track could not be downloaded directly; upload an original file you are permitted to reuse")}, "track_content_invalid")
		return 0, coded
	}
	if response.ContentLength < -1 || response.ContentLength >= 0 && !a.trackContentLengthWithinReservation(sourceID, response.ContentLength, maxTracks) {
		coded := withClippingDiagnosticCode(permanentClippingImportError{youtubeImportOutcome("youtube_size_limit_exceeded", "The selected source tracks exceed the 20 GiB limit; upload smaller original tracks you are permitted to reuse")}, "track_size_limit_exceeded")
		return 0, coded
	}
	contentType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if !validYouTubeTrackContentType(contentType, role, format.Extension) {
		coded := withClippingDiagnosticCode(permanentClippingImportError{youtubeImportOutcome("youtube_format_unavailable", "The selected track response is not the expected media type; upload an original file you are permitted to reuse")}, "track_content_invalid")
		return 0, coded
	}
	file, err := openClippingPartTruncate(localPath)
	if err != nil {
		coded := withClippingDiagnosticCode(fmt.Errorf("%w: prepare staged track", errClippingImportRetryable), "track_storage_failed")
		return 0, coded
	}
	defer func() { _ = file.Close() }()
	buffer := make([]byte, clippingImportReadBytes)
	var committed int64
	for {
		if err := ctx.Err(); err != nil {
			return committed, err
		}
		n, readErr := response.Body.Read(buffer)
		if n > 0 {
			if importStreamExceedsLimit(committed, int64(n), maxTracks) {
				coded := withClippingDiagnosticCode(permanentClippingImportError{youtubeImportOutcome("youtube_size_limit_exceeded", "The selected source tracks exceed the 20 GiB limit; upload smaller original tracks you are permitted to reuse")}, "track_size_limit_exceeded")
				return committed, coded
			}
			if err := a.commitYouTubeTrackChunk(ctx, sourceID, file, buffer[:n], committed, maxTracks); err != nil {
				if ctx.Err() != nil {
					return committed, ctx.Err()
				}
				if isPermanentClippingImportError(err) {
					return committed, err
				}
				coded := withClippingDiagnosticCode(fmt.Errorf("%w: commit staged track bytes", errClippingImportRetryable), "track_storage_failed")
				return committed, coded
			}
			committed += int64(n)
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				coded := withClippingDiagnosticCode(fmt.Errorf("%w: read staged track", errClippingImportRetryable), "track_body_incomplete")
				return committed, retryableYouTubeTrackTransfer(coded)
			}
			break
		}
		if n == 0 {
			continue
		}
	}
	if committed == 0 || response.ContentLength >= 0 && committed != response.ContentLength {
		coded := withClippingDiagnosticCode(fmt.Errorf("%w: selected track length did not match response headers", errClippingImportRetryable), "track_body_incomplete")
		return committed, retryableYouTubeTrackTransfer(coded)
	}
	if err := file.Sync(); err != nil {
		coded := withClippingDiagnosticCode(fmt.Errorf("%w: sync staged track", errClippingImportRetryable), "track_storage_failed")
		return committed, coded
	}
	if err := file.Close(); err != nil {
		coded := withClippingDiagnosticCode(fmt.Errorf("%w: close staged track", errClippingImportRetryable), "track_storage_failed")
		return committed, coded
	}
	return committed, nil
}

// resetYouTubeTrackAttempt removes a failed track from byte zero. `committed`
// is the number of bytes already deducted from the shared scratch reservation;
// the file's size is deliberately not used because a failed write or fsync can
// leave uncommitted bytes on disk.
func (a *ClippingAcquisition) resetYouTubeTrackAttempt(sourceID, localPath string, committed int64) error {
	a.store.clippingMu.Lock()
	defer a.store.clippingMu.Unlock()
	source, err := a.store.ClippingSource(sourceID)
	if err != nil {
		return err
	}
	if source.Status != ClippingSourceImporting {
		return errors.New("source import is no longer active")
	}
	remaining, reserved := a.store.clippingImportWorkspaces[sourceID]
	if !reserved || committed < 0 || remaining < 0 || remaining > clippingYouTubeTrackWorkspaceBytes-committed {
		return errors.New("source track workspace reservation cannot be restored")
	}
	if err := removeGeneratedRegularFile(localPath); err != nil {
		return err
	}
	if err := syncClippingDirectory(filepath.Dir(localPath)); err != nil {
		return err
	}
	a.store.clippingImportWorkspaces[sourceID] = remaining + committed
	return nil
}

func (a *ClippingAcquisition) trackContentLengthWithinReservation(sourceID string, contentLength, maximum int64) bool {
	a.store.clippingMu.Lock()
	defer a.store.clippingMu.Unlock()
	remaining, ok := a.store.clippingImportWorkspaces[sourceID]
	if !ok || contentLength < 0 {
		return false
	}
	used := clippingYouTubeTrackWorkspaceBytes - remaining
	return !importStreamExceedsLimit(used, contentLength, maximum) && !importStreamExceedsLimit(used, contentLength, clippingYouTubeTrackWorkspaceBytes)
}

func validYouTubeTrackContentType(contentType, role, extension string) bool {
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	if contentType == "application/octet-stream" {
		return true
	}
	if role == "video" {
		return contentType == "video/mp4" && strings.EqualFold(extension, "mp4") || contentType == "video/webm" && strings.EqualFold(extension, "webm")
	}
	switch strings.ToLower(extension) {
	case "m4a", "mp4":
		return contentType == "audio/mp4" || contentType == "application/mp4"
	case "webm":
		return contentType == "audio/webm"
	default:
		return false
	}
}

func (a *ClippingAcquisition) muxYouTubeTracks(ctx context.Context, sourceID, videoPath, audioPath, outputPath string, maxOutput int64) error {
	if err := removeGeneratedRegularFile(outputPath); err != nil {
		return withClippingDiagnosticCode(fmt.Errorf("%w: prepare mux output", errClippingImportRetryable), "track_mux_failed")
	}
	executable := a.youtubeFFmpegExecutable
	if executable == "" {
		executable = findYouTubeExecutable([]string{"/usr/bin/ffmpeg", "/usr/local/bin/ffmpeg"})
	}
	if executable == "" {
		return withClippingDiagnosticCode(permanentClippingImportError{errors.New("FFmpeg is unavailable in this runtime")}, "track_mux_failed")
	}
	if maxOutput <= 0 || maxOutput > MaxClippingSourceBytes {
		maxOutput = MaxClippingSourceBytes
	}
	if !filepath.IsAbs(executable) {
		return withClippingDiagnosticCode(permanentClippingImportError{errors.New("FFmpeg path is not fixed and absolute")}, "track_mux_failed")
	}
	python := findYouTubeExecutable([]string{"/usr/bin/python3"})
	if python == "" {
		return withClippingDiagnosticCode(permanentClippingImportError{errors.New("Python is unavailable for bounded YouTube muxing")}, "runtime_unavailable")
	}
	muxCtx, cancel := context.WithTimeout(ctx, clippingYouTubeMuxTimeout)
	defer cancel()
	ffmpegArgs := []string{
		"-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-probesize", "5000000", "-analyzeduration", "15000000", "-max_alloc", strconv.Itoa(clippingProbeMaxAllocation),
		"-protocol_whitelist", "file,pipe", "-format_whitelist", "mov,matroska,webm", "-i", videoPath,
		"-protocol_whitelist", "file,pipe", "-format_whitelist", "mov,matroska,webm", "-i", audioPath,
		"-map", "0:v:0", "-map", "1:a:0", "-c", "copy", "-map_metadata", "-1",
		"-f", "matroska", outputPath,
	}
	commandArgs := []string{"-I", "-S", "-c", clippingYouTubeMuxLimitLauncher, strconv.FormatInt(maxOutput, 10), executable}
	commandArgs = append(commandArgs, ffmpegArgs...)
	command := exec.CommandContext(muxCtx, python, commandArgs...)
	command.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/nonexistent", "LANG=C.UTF-8", "LC_ALL=C.UTF-8"}
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	command.WaitDelay = 2 * time.Second
	configureYouTubeSubprocess(command)
	file, err := openClippingPartTruncate(outputPath)
	if err != nil {
		return withClippingDiagnosticCode(fmt.Errorf("%w: create merged source", errClippingImportRetryable), "track_mux_failed")
	}
	if err := file.Close(); err != nil {
		_ = removeGeneratedRegularFile(outputPath)
		return withClippingDiagnosticCode(fmt.Errorf("%w: close merged source before FFmpeg", errClippingImportRetryable), "track_mux_failed")
	}
	if err := command.Start(); err != nil {
		_ = removeGeneratedRegularFile(outputPath)
		return withClippingDiagnosticCode(fmt.Errorf("%w: start FFmpeg copy mux", errClippingImportRetryable), "track_mux_failed")
	}
	runErr := command.Wait()
	if youtubeMuxOutputLimitReached(command, runErr) || youtubeMuxFailedAtByteLimit(runErr, outputPath, maxOutput) {
		_ = removeGeneratedRegularFile(outputPath)
		return withClippingDiagnosticCode(permanentClippingImportError{youtubeImportOutcome("youtube_size_limit_exceeded", "The merged source exceeds the 20 GiB limit; upload smaller original tracks you are permitted to reuse")}, "mux_output_size_limit_exceeded")
	}
	if ctx.Err() != nil {
		_ = removeGeneratedRegularFile(outputPath)
		return ctx.Err()
	}
	if errors.Is(muxCtx.Err(), context.DeadlineExceeded) {
		_ = removeGeneratedRegularFile(outputPath)
		return withClippingDiagnosticCode(fmt.Errorf("%w: FFmpeg copy mux timed out", errClippingImportRetryable), "track_mux_timeout")
	}
	if runErr != nil {
		_ = removeGeneratedRegularFile(outputPath)
		return withClippingDiagnosticCode(permanentClippingImportError{youtubeImportOutcome("youtube_format_unavailable", "The selected tracks could not be safely stream-copy muxed; upload an original file you are permitted to reuse")}, "track_mux_failed")
	}
	if err := syncClippingDirectory(filepath.Dir(outputPath)); err != nil {
		_ = removeGeneratedRegularFile(outputPath)
		return withClippingDiagnosticCode(fmt.Errorf("%w: sync merged source", errClippingImportRetryable), "track_mux_failed")
	}
	info, err := os.Lstat(outputPath)
	if err != nil || !info.Mode().IsRegular() {
		return withClippingDiagnosticCode(permanentClippingImportError{errClippingInvalidMedia}, "track_mux_failed")
	}
	if info.Size() <= 0 {
		return withClippingDiagnosticCode(permanentClippingImportError{errClippingInvalidMedia}, "track_mux_failed")
	}
	if info.Size() > maxOutput {
		_ = removeGeneratedRegularFile(outputPath)
		return withClippingDiagnosticCode(permanentClippingImportError{youtubeImportOutcome("youtube_size_limit_exceeded", "The merged source exceeds the 20 GiB limit; upload smaller original tracks you are permitted to reuse")}, "mux_output_size_limit_exceeded")
	}
	return nil
}

func youtubeMuxFailedAtByteLimit(runErr error, outputPath string, maxOutput int64) bool {
	if runErr == nil || maxOutput <= 0 {
		return false
	}
	info, err := os.Lstat(outputPath)
	return err == nil && info.Mode().IsRegular() && info.Size() >= maxOutput
}

func removeGeneratedRegularFile(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("generated media path is not a regular file")
	}
	return os.Remove(path)
}

func (a *ClippingAcquisition) cleanupYouTubeTracks(sourceID string, paths clippingSourcePaths) error {
	a.store.clippingMu.Lock()
	defer a.store.clippingMu.Unlock()
	for _, path := range []string{paths.videoTrack, paths.audioTrack} {
		if err := removeGeneratedRegularFile(path); err != nil {
			return err
		}
	}
	if err := syncClippingDirectory(filepath.Dir(paths.final)); err != nil {
		return err
	}
	if err := a.store.CompleteClippingSourceTrackWorkspace(sourceID); err != nil {
		return err
	}
	delete(a.store.clippingImportWorkspaces, sourceID)
	return nil
}

// clearYouTubeImportArtifactsLocked removes every generated artifact that is
// unsafe to resume. The caller holds clippingMu; its directory sync must
// succeed before the shared scratch claim can be released.
func (a *ClippingAcquisition) clearYouTubeImportArtifactsLocked(sourceID string, paths clippingSourcePaths, includeFinal bool) error {
	for _, path := range []string{paths.temp, paths.videoTrack, paths.audioTrack} {
		if err := removeGeneratedRegularFile(path); err != nil {
			return err
		}
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
	if source.Kind != ClippingSourceYouTube || info == nil || !info.Mode().IsRegular() || info.Size() <= 0 ||
		source.ImportExpectedSize <= 0 || source.ImportExpectedSize != source.SizeBytes || source.ImportExpectedSize != info.Size() || !validSHA256Hex(source.ImportExpectedSHA) {
		return false
	}
	var expectation youtubeImportExpectation
	if json.Unmarshal([]byte(source.ImportExpectation), &expectation) != nil || !validYouTubeImportExpectation(expectation) || expectation.SeparateTracks && !expectation.WorkspaceComplete {
		return false
	}
	return true
}
