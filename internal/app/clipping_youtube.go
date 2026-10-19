package app

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const (
	clippingYouTubeLegacyExpectationVersion       = 2
	clippingYouTubeExpectationVersion             = 3
	clippingYouTubeMaxAVSkewMS              int64 = 2_000
)

// youtubeImportExpectation records the output contract needed to validate or
// recover a downloaded source. Version 2 remains readable for imports created
// by the previous local downloader; version 3 records one Apify MP4 artifact.
type youtubeImportExpectation struct {
	Version           int     `json:"version"`
	DurationMS        int64   `json:"duration_ms,omitempty"`
	VideoCodec        string  `json:"video_codec,omitempty"`
	VideoWidth        int     `json:"video_width,omitempty"`
	VideoHeight       int     `json:"video_height,omitempty"`
	VideoFPS          float64 `json:"video_fps,omitempty"`
	VideoQuality      float64 `json:"video_quality,omitempty"`
	VideoTBR          float64 `json:"video_tbr,omitempty"`
	AudioCodec        string  `json:"audio_codec,omitempty"`
	OriginalAudio     bool    `json:"original_audio,omitempty"`
	SeparateTracks    bool    `json:"separate_tracks,omitempty"`
	WorkspaceComplete bool    `json:"workspace_complete,omitempty"`
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

func (a *ClippingAcquisition) youtubeWorkspaceLimit() int64 {
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
	if a.apify == nil {
		return apifyPermanentFailure("apify_config_missing", "The Apify YouTube importer is not configured. Contact the service operator.")
	}

	expectation := youtubeImportExpectation{Version: clippingYouTubeExpectationVersion}
	a.store.clippingMu.Lock()
	current, err := a.store.ClippingSource(source.ID)
	if err != nil {
		a.store.clippingMu.Unlock()
		return err
	}
	if !clippingImportClaimIsLive(current, source.ID, source.ImportAttempts, a.now()) {
		a.store.clippingMu.Unlock()
		return ErrClippingStaleAttempt
	}
	if err := a.checkClippingStorageCapacity(0); err != nil {
		a.store.clippingMu.Unlock()
		return err
	}
	err = a.store.SetClippingSourceImportExpectation(source.ID, expectation)
	a.store.clippingMu.Unlock()
	if err != nil {
		return err
	}

	maxBytes := MaxClippingSourceBytes
	slog.Info("clipping YouTube Apify acquisition prepared", "source_id", source.ID, "max_bytes", maxBytes)
	progress := func(sizeBytes int64) error {
		a.store.clippingMu.Lock()
		defer a.store.clippingMu.Unlock()
		current, err := a.store.ClippingSource(source.ID)
		if err != nil {
			return err
		}
		if current.Status != ClippingSourceImporting || current.Kind != ClippingSourceYouTube ||
			current.ImportAttempts != source.ImportAttempts || current.ImportLeaseUntil <= a.now().Unix() {
			return ErrClippingInvalidState
		}
		if err := a.store.UpdateClippingSourceImportProgress(source.ID, source.ImportAttempts, sizeBytes); err != nil {
			return err
		}
		return a.checkClippingStorageCapacity(0)
	}
	if err := a.apify.acquire(ctx, a.store, source, canonicalURL, tempPath, maxBytes, trace, progress); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := openClippingRegularFile(tempPath, os.O_RDONLY)
	if err != nil {
		return apifyRetryableFailure("apify_download_failed", "The saved Apify video file could not be opened safely.", nil)
	}
	info, statErr := file.Stat()
	closeErr := file.Close()
	if statErr != nil || closeErr != nil || info.Size() <= 0 {
		return apifyRetryableFailure("apify_download_failed", "The saved Apify video file could not be inspected.", nil)
	}
	if info.Size() > maxBytes {
		return youtubeSizeLimitFailure()
	}
	if err := syncRegularGeneratedFile(tempPath); err != nil {
		return apifyRetryableFailure("apify_download_failed", "The saved Apify video file could not be synced.", nil)
	}
	if err := progress(info.Size()); err != nil {
		return err
	}
	a.store.clippingMu.Lock()
	defer a.store.clippingMu.Unlock()
	current, err = a.store.ClippingSource(source.ID)
	if err != nil {
		return err
	}
	if current.Status != ClippingSourceImporting || current.Kind != ClippingSourceYouTube ||
		current.ImportAttempts != source.ImportAttempts || current.ImportLeaseUntil <= a.now().Unix() {
		return ErrClippingInvalidState
	}
	if err := a.store.CompleteClippingSourceTrackWorkspace(source.ID); err != nil {
		return err
	}
	delete(a.store.clippingImportWorkspaces, source.ID)
	a.store.PublishClippingSource(source.ID)
	slog.Info("clipping YouTube Apify media staged", "source_id", source.ID, "bytes", info.Size())
	return nil
}

func youtubeWorkspacePath(paths clippingSourcePaths) string {
	return filepath.Join(filepath.Dir(paths.final), "youtube-work")
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
	return expectation.WorkspaceComplete &&
		(expectation.Version == clippingYouTubeExpectationVersion && !expectation.SeparateTracks ||
			expectation.Version == clippingYouTubeLegacyExpectationVersion && expectation.SeparateTracks)
}

func validYouTubeImportExpectation(expectation youtubeImportExpectation) bool {
	switch expectation.Version {
	case 1:
		return expectation.DurationMS > 0 && expectation.DurationMS <= MaxClippingSourceDurationMS &&
			reasonableClippingDimensions(expectation.VideoWidth, expectation.VideoHeight) &&
			isSupportedExpectedYouTubeCodec("video", expectation.VideoCodec) && isSupportedExpectedYouTubeCodec("audio", expectation.AudioCodec) &&
			finiteNonnegative(expectation.VideoFPS) && finiteNumber(expectation.VideoQuality) && finiteNonnegative(expectation.VideoTBR)
	case clippingYouTubeLegacyExpectationVersion:
		return expectation.DurationMS == 0 && expectation.SeparateTracks && expectation.VideoCodec == "" && expectation.AudioCodec == "" &&
			expectation.VideoWidth == 0 && expectation.VideoHeight == 0 && expectation.VideoFPS == 0 && expectation.VideoQuality == 0 && expectation.VideoTBR == 0
	case clippingYouTubeExpectationVersion:
		return expectation.DurationMS == 0 && !expectation.SeparateTracks && expectation.VideoCodec == "" && expectation.AudioCodec == "" &&
			expectation.VideoWidth == 0 && expectation.VideoHeight == 0 && expectation.VideoFPS == 0 && expectation.VideoQuality == 0 && expectation.VideoTBR == 0
	default:
		return false
	}
}

func validateYouTubeProbeExpectation(expectation youtubeImportExpectation, media clippingMediaProbeResult) error {
	if !validYouTubeImportExpectation(expectation) || media.AudioStreams < 1 || media.VideoCodec == "" || media.AudioCodec == "" {
		return errors.Join(errClippingInvalidMedia, errClippingYouTubeAVStreamMissing)
	}
	if expectation.Version == clippingYouTubeExpectationVersion || expectation.Version == clippingYouTubeLegacyExpectationVersion {
		if !isYouTubeMP4Container(media.FormatName) {
			return errors.Join(errClippingInvalidMedia, errClippingYouTubeAVStreamMissing)
		}
		if expectation.Version == clippingYouTubeExpectationVersion &&
			(media.VideoDurationMS > MaxClippingSourceDurationMS || media.AudioDurationMS > MaxClippingSourceDurationMS) {
			return errors.Join(errClippingInvalidMedia, errClippingDurationLimitExceeded)
		}
		if media.DurationMS <= 0 || media.DurationMS > MaxClippingSourceDurationMS {
			return errors.Join(errClippingInvalidMedia, errClippingYouTubeAVTimingMismatch)
		}
		if expectation.Version == clippingYouTubeExpectationVersion &&
			(!media.VideoTimingValid || !media.AudioTimingValid || media.VideoDurationMS <= 0 || media.AudioDurationMS <= 0) {
			return errors.Join(errClippingInvalidMedia, errClippingYouTubeAVStreamMissing)
		}
		if media.VideoTimingValid && media.AudioTimingValid &&
			(media.VideoDurationMS <= 0 || media.AudioDurationMS <= 0 ||
				!youtubeTimesWithin(media.VideoDurationMS, media.AudioDurationMS, clippingYouTubeDurationToleranceMS) ||
				!youtubeTimesWithin(media.VideoDurationMS, media.DurationMS, clippingYouTubeDurationToleranceMS) ||
				!youtubeTimesWithin(media.AudioDurationMS, media.DurationMS, clippingYouTubeDurationToleranceMS) ||
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
