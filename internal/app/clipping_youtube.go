package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	clippingYouTubeExtractorTimeout = 90 * time.Second
	clippingYouTubeOutputLimit      = 4 << 20
	clippingYouTubeRuntime          = "deno:/usr/bin/deno"
)

// The production image installs the verified yt-dlp wrapper at /usr/local/bin.
// /usr/bin is a fixed-path fallback for native Linux runtimes; ambient PATH is ignored.
var clippingYouTubeExecutablePaths = []string{"/usr/local/bin/yt-dlp", "/usr/bin/yt-dlp"}

var errClippingDurationLimitExceeded = errors.New("source duration exceeds the four-hour limit")

const clippingYouTubeMaxAVSkewMS int64 = 2_000

// youtubeImportExpectation stores only the sanitized properties needed to
// validate a finalized YouTube source after a process restart. Direct URLs,
// extractor IDs, headers, and raw metadata are intentionally absent.
type youtubeImportExpectation struct {
	Version           int     `json:"version"`
	DurationMS        int64   `json:"duration_ms"`
	VideoCodec        string  `json:"video_codec"`
	VideoWidth        int     `json:"video_width"`
	VideoHeight       int     `json:"video_height"`
	VideoFPS          float64 `json:"video_fps"`
	VideoQuality      float64 `json:"video_quality"`
	VideoTBR          float64 `json:"video_tbr"`
	AudioCodec        string  `json:"audio_codec"`
	OriginalAudio     bool    `json:"original_audio"`
	SeparateTracks    bool    `json:"separate_tracks"`
	WorkspaceComplete bool    `json:"workspace_complete,omitempty"`
}

type youtubeSelection struct {
	URL        *url.URL
	DurationMS int64
	SizeBytes  int64
	UserAgent  string
	Referer    string
	Tracks     youtubeFormatSelection
	Separate   bool
	MediaRole  string
}

type youtubeExtractorOutput struct {
	ID           string          `json:"id"`
	Type         string          `json:"_type"`
	Availability string          `json:"availability"`
	LiveStatus   string          `json:"live_status"`
	IsLive       bool            `json:"is_live"`
	Duration     json.Number     `json:"duration"`
	Entries      json.RawMessage `json:"entries"`
	Formats      []youtubeFormat `json:"formats"`
}

type youtubeFormat struct {
	FormatID           string            `json:"format_id"`
	AudioCodec         string            `json:"acodec"`
	VideoCodec         string            `json:"vcodec"`
	Protocol           string            `json:"protocol"`
	Extension          string            `json:"ext"`
	URL                string            `json:"url"`
	HTTPHeaders        map[string]string `json:"http_headers"`
	FileSize           json.Number       `json:"filesize"`
	FileSizeApprox     json.Number       `json:"filesize_approx"`
	Width              int               `json:"width"`
	Height             int               `json:"height"`
	FPS                json.Number       `json:"fps"`
	TBR                json.Number       `json:"tbr"`
	Quality            json.Number       `json:"quality"`
	ABR                json.Number       `json:"abr"`
	ASR                json.Number       `json:"asr"`
	AudioChannels      json.Number       `json:"audio_channels"`
	Language           string            `json:"language"`
	LanguagePreference json.Number       `json:"language_preference"`
	FormatNote         string            `json:"format_note"`
	HasDRM             bool              `json:"has_drm"`
}

// yt-dlp may emit the string "maybe" for manifest formats. Treat every
// explicit value except JSON false or null as DRM-protected/unknown so one
// format cannot make the bounded metadata document fail to decode and no
// ambiguous candidate can be selected.
func (format *youtubeFormat) UnmarshalJSON(data []byte) error {
	type plain youtubeFormat
	var decoded struct {
		*plain
		HasDRM json.RawMessage `json:"has_drm"`
	}
	decoded.plain = (*plain)(format)
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	value := strings.TrimSpace(string(decoded.HasDRM))
	format.HasDRM = value != "" && value != "false" && value != "null"
	return nil
}

func youtubeImportOutcome(reason, message string) *ClippingAcquisitionError {
	return &ClippingAcquisitionError{Status: "original_file_required", Reason: reason, Message: message}
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

func importStreamExceedsLimit(committed, incoming, limit int64) bool {
	if committed < 0 || incoming < 0 || limit < 0 || incoming > limit {
		return true
	}
	return committed > limit-incoming
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

func (a *ClippingAcquisition) extractYouTubeMedia(ctx context.Context, canonicalURL *url.URL) (youtubeSelection, error) {
	return a.extractYouTubeMediaWithTrace(ctx, canonicalURL, nil, nil, nil)
}

func (a *ClippingAcquisition) extractYouTubeMediaWithTrace(ctx context.Context, canonicalURL *url.URL, onMetadataRetrieved, onFormatSelectionStarted func(), onFormatSelected func(youtubeSelection)) (youtubeSelection, error) {
	if err := ctx.Err(); err != nil {
		return youtubeSelection{}, err
	}
	var err error
	videoID, ok := youtubeVideoID(canonicalURL)
	if !ok {
		return youtubeSelection{}, withClippingDiagnosticCode(permanentClippingImportError{youtubeImportOutcome("youtube_import_unavailable", "The YouTube link could not be imported; upload an original file you are permitted to reuse")}, "invalid_url")
	}
	executable := a.youtubeExecutable
	if executable == "" {
		executable = findYouTubeExecutable(clippingYouTubeExecutablePaths)
	}
	if err != nil || executable == "" {
		return youtubeSelection{}, withClippingDiagnosticCode(permanentClippingImportError{youtubeImportOutcome("youtube_import_unavailable", "The YouTube importer is unavailable in this runtime; upload an original file you are permitted to reuse")}, "runtime_unavailable")
	}
	runtimeSpec := a.youtubeRuntime
	if runtimeSpec == "" {
		runtimeSpec = clippingYouTubeRuntime
	}
	runtimeName, runtimePath, hasRuntimePath := strings.Cut(runtimeSpec, ":")
	if !hasRuntimePath || runtimeName != "deno" || runtimePath == "" {
		return youtubeSelection{}, withClippingDiagnosticCode(permanentClippingImportError{youtubeImportOutcome("youtube_import_unavailable", "The YouTube importer is unavailable in this runtime; upload an original file you are permitted to reuse")}, "runtime_unavailable")
	}
	if _, err := exec.LookPath(runtimePath); err != nil {
		return youtubeSelection{}, withClippingDiagnosticCode(permanentClippingImportError{youtubeImportOutcome("youtube_import_unavailable", "The YouTube importer is unavailable in this runtime; upload an original file you are permitted to reuse")}, "runtime_unavailable")
	}

	extractorCtx, cancel := context.WithTimeout(ctx, clippingYouTubeExtractorTimeout)
	defer cancel()
	proxy, err := startYouTubeProxy(extractorCtx, a.resolver)
	if err != nil {
		if ctx.Err() != nil {
			return youtubeSelection{}, ctx.Err()
		}
		return youtubeSelection{}, withClippingDiagnosticCode(fmt.Errorf("%w: YouTube importer proxy could not start", errClippingImportRetryable), "extractor_proxy_failure")
	}
	defer proxy.Close()
	denoCache, err := createYouTubeDenoCache()
	if err != nil {
		return youtubeSelection{}, withClippingDiagnosticCode(permanentClippingImportError{youtubeImportOutcome("youtube_import_unavailable", "The YouTube importer is unavailable in this runtime; upload an original file you are permitted to reuse")}, "runtime_unavailable")
	}
	defer os.RemoveAll(denoCache)

	args := []string{
		"--ignore-config",
		"--no-plugin-dirs",
		"--no-cache-dir",
		"--no-cookies",
		"--no-playlist",
		"--no-warnings",
		"--no-progress",
		"--quiet",
		"--skip-download",
		"--no-simulate",
		"--dump-single-json",
		"--no-remote-components",
		"--js-runtimes", runtimeSpec,
		"--proxy", proxy.AuthenticatedURL(),
		canonicalURL.String(),
	}
	command := exec.CommandContext(extractorCtx, executable, args...)
	command.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/nonexistent", "DENO_DIR=" + denoCache, "LANG=C.UTF-8", "LC_ALL=C.UTF-8"}
	command.Stderr = io.Discard
	limitedOutput := &clippingLimitedWriter{limit: clippingYouTubeOutputLimit}
	command.Stdout = limitedOutput
	command.WaitDelay = 2 * time.Second
	configureYouTubeSubprocess(command)
	runErr := command.Run()
	if ctx.Err() != nil {
		return youtubeSelection{}, ctx.Err()
	}
	if errors.Is(extractorCtx.Err(), context.DeadlineExceeded) {
		return youtubeSelection{}, withClippingDiagnosticCode(fmt.Errorf("%w: YouTube metadata request timed out", errClippingImportRetryable), "extractor_timeout")
	}
	if limitedOutput.exceeded {
		return youtubeSelection{}, withClippingDiagnosticCode(permanentClippingImportError{youtubeImportOutcome("youtube_format_unavailable", "No supported audio/video format is available; upload an original file you are permitted to reuse")}, "extractor_output_limit")
	}
	if runErr != nil {
		// The child output can contain signed media URLs and provider responses.
		// Never include command output, stderr, or the child error in a returned error.
		return youtubeSelection{}, withClippingDiagnosticCode(fmt.Errorf("%w: YouTube metadata request failed", errClippingImportRetryable), "extractor_exit_failure")
	}
	if err := proxy.Err(); err != nil {
		return youtubeSelection{}, withClippingDiagnosticCode(fmt.Errorf("%w: YouTube metadata proxy stopped", errClippingImportRetryable), "extractor_proxy_failure")
	}
	if onMetadataRetrieved != nil {
		onMetadataRetrieved()
	}
	if onFormatSelectionStarted != nil {
		onFormatSelectionStarted()
	}
	selection, err := parseYouTubeExtractorOutput(limitedOutput.data, videoID)
	if err != nil {
		code := "metadata_invalid"
		var outcome *ClippingAcquisitionError
		if errors.As(err, &outcome) {
			switch outcome.Reason {
			case "youtube_format_unavailable":
				code = "format_unavailable"
			case "youtube_original_audio_unavailable":
				code = "original_audio_unavailable"
			case "youtube_video_unavailable":
				code = "video_unavailable"
			case "youtube_duration_limit_exceeded":
				code = "duration_limit_exceeded"
			case "youtube_size_limit_exceeded":
				code = "size_limit_exceeded"
			}
		}
		return youtubeSelection{}, withClippingDiagnosticCode(err, code)
	}
	if onFormatSelected != nil {
		onFormatSelected(selection)
	}
	return selection, nil
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

func createYouTubeDenoCache() (string, error) {
	directory, err := os.MkdirTemp("", "framevault-youtube-deno-")
	if err != nil {
		return "", err
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		_ = os.RemoveAll(directory)
		return "", err
	}
	return directory, nil
}

func parseYouTubeExtractorOutput(output []byte, expectedID string) (youtubeSelection, error) {
	fallback := func(reason string) (youtubeSelection, error) {
		return youtubeSelection{}, permanentClippingImportError{youtubeImportOutcome(reason, "No supported audio/video format is available; upload an original file you are permitted to reuse")}
	}
	decoder := json.NewDecoder(strings.NewReader(string(output)))
	decoder.UseNumber()
	var metadata youtubeExtractorOutput
	if err := decoder.Decode(&metadata); err != nil {
		return fallback("youtube_format_unavailable")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return fallback("youtube_format_unavailable")
	}
	if metadata.ID != expectedID || !validYouTubeVideoID(metadata.ID) {
		return fallback("youtube_import_unavailable")
	}
	if metadata.Type != "" && metadata.Type != "video" || len(metadata.Entries) > 0 && string(metadata.Entries) != "null" {
		return fallback("youtube_format_unavailable")
	}
	availability := strings.ToLower(strings.TrimSpace(metadata.Availability))
	if youtubeRestrictedAvailability(availability) {
		return youtubeSelection{}, permanentClippingImportError{youtubeImportOutcome("youtube_video_unavailable", "The video is unavailable without sign-in; upload an original file you are permitted to reuse")}
	}
	if availability != "" && availability != "public" {
		return fallback("youtube_format_unavailable")
	}
	liveStatus := strings.ToLower(strings.TrimSpace(metadata.LiveStatus))
	if metadata.IsLive || liveStatus != "" && liveStatus != "not_live" && liveStatus != "was_live" {
		return fallback("youtube_format_unavailable")
	}
	if metadata.Duration == "" {
		return fallback("youtube_format_unavailable")
	}
	duration, ok := new(big.Rat).SetString(metadata.Duration.String())
	if !ok || duration.Sign() <= 0 {
		return fallback("youtube_format_unavailable")
	}
	if duration.Cmp(big.NewRat(MaxClippingSourceDurationMS, 1000)) > 0 {
		return youtubeSelection{}, permanentClippingImportError{youtubeImportOutcome("youtube_duration_limit_exceeded", "The video exceeds the four-hour limit; upload a shorter original file you are permitted to reuse")}
	}
	durationMS, ok := parseDurationMilliseconds(metadata.Duration.String())
	if !ok || durationMS > MaxClippingSourceDurationMS {
		return fallback("youtube_format_unavailable")
	}
	formats := metadata.Formats
	if len(formats) == 0 {
		return fallback("youtube_format_unavailable")
	}
	chosen, err := selectYouTubeFormats(formats, expectedID)
	if err != nil {
		if errors.Is(err, errYouTubeOriginalAudioUnavailable) {
			return youtubeSelection{}, permanentClippingImportError{youtubeImportOutcome("youtube_original_audio_unavailable", "An original audio track could not be identified safely; upload an original file you are permitted to reuse")}
		}
		return fallback("youtube_format_unavailable")
	}
	selection := youtubeSelection{DurationMS: durationMS, Tracks: chosen, Separate: chosen.Separate}
	selectedFormats := make([]youtubeFormat, 0, 2)
	if chosen.Separate {
		selectedFormats = append(selectedFormats, *chosen.Video, *chosen.Audio)
	} else if chosen.Combined != nil {
		selectedFormats = append(selectedFormats, *chosen.Combined)
	}
	var aggregateDeclared int64
	for _, format := range selectedFormats {
		declared := format.FileSize
		if declared == "" {
			declared = format.FileSizeApprox
		}
		value, present, sizeErr := parseYouTubeDeclaredSize(declared)
		if sizeErr != nil {
			return fallback("youtube_format_unavailable")
		}
		if present {
			if importStreamExceedsLimit(aggregateDeclared, value, MaxClippingSourceBytes) {
				return youtubeSelection{}, permanentClippingImportError{youtubeImportOutcome("youtube_size_limit_exceeded", "The selected source tracks exceed the 20 GiB limit; upload a smaller original file you are permitted to reuse")}
			}
			aggregateDeclared += value
		}
	}
	if chosen.Separate {
		selection.SizeBytes = aggregateDeclared
		return selection, nil
	}
	format := *chosen.Combined
	mediaURL, err := url.Parse(format.URL)
	if err != nil || validateYouTubeMediaURL(mediaURL) != nil {
		return fallback("youtube_format_unavailable")
	}
	userAgent, referer, err := safeYouTubeRequestHeaders(format.HTTPHeaders, expectedID)
	if err != nil {
		return fallback("youtube_format_unavailable")
	}
	selection.URL = mediaURL
	selection.SizeBytes = aggregateDeclared
	selection.UserAgent = userAgent
	if referer {
		selection.Referer = "https://www.youtube.com/watch?v=" + expectedID
	}
	return selection, nil
}

func supportedYouTubeExtension(extension string) bool {
	switch strings.ToLower(strings.TrimSpace(extension)) {
	case "mp4", "webm":
		return true
	default:
		return false
	}
}

func safeYouTubeRequestHeaders(headers map[string]string, expectedID string) (string, bool, error) {
	if len(headers) > 32 {
		return "", false, errors.New("too many extractor request headers")
	}
	var userAgent string
	userAgentFound := false
	refererFound := false
	var totalBytes int
	for name, value := range headers {
		totalBytes += len(name) + len(value)
		if len(name) == 0 || len(name) > 128 || len(value) > 4096 || totalBytes > 8192 || !validYouTubeHeaderValue(value) {
			return "", false, errors.New("invalid extractor request header")
		}
		switch strings.ToLower(name) {
		case "user-agent":
			if userAgentFound || len(value) == 0 || len(value) > 512 || strings.TrimSpace(value) != value || !validYouTubeUserAgent(value) {
				return "", false, errors.New("invalid extractor user agent")
			}
			userAgent = value
			userAgentFound = true
		case "referer":
			canonicalReferer := "https://www.youtube.com/watch?v=" + expectedID
			if refererFound || value != "https://www.youtube.com/" && value != canonicalReferer {
				return "", false, errors.New("invalid extractor referer")
			}
			refererFound = true
		case "cookie", "authorization", "proxy-authorization", "host":
			return "", false, errors.New("extractor requested a forbidden header")
		default:
			// yt-dlp supplies other defaults, but the Go request keeps its own fixed headers.
		}
	}
	return userAgent, refererFound, nil
}

func validYouTubeHeaderValue(value string) bool {
	for _, character := range value {
		if character < 0x20 && character != '\t' || character == 0x7f {
			return false
		}
	}
	return true
}

func validYouTubeUserAgent(value string) bool {
	for _, character := range value {
		if character < 0x20 || character > 0x7e {
			return false
		}
	}
	return true
}

func youtubeRestrictedAvailability(availability string) bool {
	switch strings.ToLower(strings.TrimSpace(availability)) {
	case "private", "needs_auth", "subscriber_only", "premium_only", "unlisted":
		return true
	default:
		return false
	}
}

func hasYouTubeAudioVideo(audioCodec, videoCodec string) bool {
	audioCodec = strings.ToLower(strings.TrimSpace(audioCodec))
	videoCodec = strings.ToLower(strings.TrimSpace(videoCodec))
	return audioCodec != "" && audioCodec != "none" && videoCodec != "" && videoCodec != "none"
}

func expectationForYouTubeSelection(selection youtubeSelection) (youtubeImportExpectation, error) {
	var videoFormat, audioFormat *youtubeFormat
	if selection.Separate {
		videoFormat, audioFormat = selection.Tracks.Video, selection.Tracks.Audio
	} else {
		videoFormat, audioFormat = selection.Tracks.Combined, selection.Tracks.Combined
	}
	if videoFormat == nil || audioFormat == nil {
		return youtubeImportExpectation{}, errClippingInvalidMedia
	}
	width, height := videoFormat.Width, videoFormat.Height
	if !reasonableClippingDimensions(width, height) {
		return youtubeImportExpectation{}, errClippingInvalidMedia
	}
	videoCodec, ok := normalizedYouTubeVideoCodec(videoFormat.VideoCodec)
	if !ok {
		return youtubeImportExpectation{}, errClippingInvalidMedia
	}
	audioCodec, ok := normalizedYouTubeAudioCodec(audioFormat.AudioCodec)
	if !ok {
		return youtubeImportExpectation{}, errClippingInvalidMedia
	}
	return youtubeImportExpectation{
		Version: 1, DurationMS: selection.DurationMS, VideoCodec: videoCodec,
		VideoWidth: width, VideoHeight: height,
		VideoFPS: safeYouTubeMetadataNumber(videoFormat.FPS), VideoQuality: safeYouTubeMetadataNumber(videoFormat.Quality),
		VideoTBR: safeYouTubeMetadataNumber(videoFormat.TBR), AudioCodec: audioCodec,
		OriginalAudio:  youtubeFormatLanguagePreference(audioFormat.LanguagePreference) == youtubeOriginalLanguagePreference,
		SeparateTracks: selection.Separate,
	}, nil
}

func validYouTubeImportExpectation(expectation youtubeImportExpectation) bool {
	return expectation.Version == 1 && expectation.DurationMS > 0 && expectation.DurationMS <= MaxClippingSourceDurationMS &&
		reasonableClippingDimensions(expectation.VideoWidth, expectation.VideoHeight) &&
		isSupportedExpectedYouTubeCodec("video", expectation.VideoCodec) && isSupportedExpectedYouTubeCodec("audio", expectation.AudioCodec) &&
		finiteNonnegative(expectation.VideoFPS) && finiteNumber(expectation.VideoQuality) && finiteNonnegative(expectation.VideoTBR)
}

func validateYouTubeProbeExpectation(expectation youtubeImportExpectation, media clippingMediaProbeResult) error {
	if !validYouTubeImportExpectation(expectation) || media.AudioStreams != 1 || media.VideoCodec == "" || media.AudioCodec == "" ||
		media.VideoCodec != expectation.VideoCodec || media.AudioCodec != expectation.AudioCodec ||
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

func normalizedYouTubeVideoCodec(raw string) (string, bool) {
	codec := strings.ToLower(strings.TrimSpace(raw))
	switch {
	case strings.HasPrefix(codec, "avc1"), strings.HasPrefix(codec, "avc3"):
		return "h264", true
	case strings.HasPrefix(codec, "hev1"), strings.HasPrefix(codec, "hvc1"):
		return "hevc", true
	case strings.HasPrefix(codec, "vp09"), codec == "vp9":
		return "vp9", true
	case strings.HasPrefix(codec, "vp08"), codec == "vp8":
		return "vp8", true
	case strings.HasPrefix(codec, "av01"), codec == "av1":
		return "av1", true
	case strings.HasPrefix(codec, "mpeg1video"):
		return "mpeg1video", true
	case strings.HasPrefix(codec, "mpeg2video"):
		return "mpeg2video", true
	case strings.HasPrefix(codec, "mpeg4"):
		return "mpeg4", true
	case strings.HasPrefix(codec, "theora"):
		return "theora", true
	default:
		return "", false
	}
}

func normalizedYouTubeAudioCodec(raw string) (string, bool) {
	codec := strings.ToLower(strings.TrimSpace(raw))
	switch {
	case strings.HasPrefix(codec, "mp4a"):
		return "aac", true
	case strings.HasPrefix(codec, "opus"):
		return "opus", true
	case strings.HasPrefix(codec, "vorbis"):
		return "vorbis", true
	case strings.HasPrefix(codec, "ac-3"), strings.HasPrefix(codec, "ac3"):
		return "ac3", true
	case strings.HasPrefix(codec, "ec-3"), strings.HasPrefix(codec, "eac3"):
		return "eac3", true
	case strings.HasPrefix(codec, "flac"):
		return "flac", true
	case strings.HasPrefix(codec, "mp3"):
		return "mp3", true
	case strings.HasPrefix(codec, "alac"):
		return "alac", true
	default:
		return "", false
	}
}

// parseYouTubeDeclaredSize keeps JSON sizes exact near the 20 GiB bound.
func parseYouTubeDeclaredSize(number json.Number) (int64, bool, error) {
	if number == "" {
		return 0, false, nil
	}
	rational, ok := new(big.Rat).SetString(number.String())
	if !ok || rational.Sign() < 0 || !rational.IsInt() {
		return 0, true, errors.New("invalid declared size")
	}
	if rational.Num().Cmp(big.NewInt(MaxClippingSourceBytes)) > 0 {
		return MaxClippingSourceBytes + 1, true, nil
	}
	if !rational.Num().IsInt64() {
		return 0, true, errors.New("invalid declared size")
	}
	return rational.Num().Int64(), true, nil
}

func validateYouTubeMediaURL(u *url.URL) error {
	if u == nil || !strings.EqualFold(u.Scheme, "https") || u.Opaque != "" || u.User != nil || u.Fragment != "" || u.RawFragment != "" || u.Host == "" {
		return errInvalidClippingImportURL
	}
	if port := u.Port(); port != "" && port != "443" {
		return errInvalidClippingImportURL
	}
	host := strings.ToLower(u.Hostname())
	if !asciiDNSName(host) || strings.HasSuffix(host, ".") || !isYouTubeCDNHost(host) {
		return errUnsafeClippingDestination
	}
	return nil
}

func isYouTubeCDNHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	return host != "googlevideo.com" && strings.HasSuffix(host, ".googlevideo.com")
}

func (a *ClippingAcquisition) fetchYouTubeMediaURL(ctx context.Context, selection youtubeSelection) (*http.Response, error) {
	current := cloneURL(selection.URL)
	seen := make(map[string]struct{}, clippingImportMaxRedirects+1)
	for redirects := 0; ; redirects++ {
		if err := validateYouTubeMediaURL(current); err != nil {
			return nil, permanentClippingImportError{youtubeImportOutcome("youtube_format_unavailable", "No supported audio/video format is available; upload an original file you are permitted to reuse")}
		}
		if _, exists := seen[current.String()]; exists {
			return nil, permanentClippingImportError{youtubeImportOutcome("youtube_format_unavailable", "No supported audio/video format is available; upload an original file you are permitted to reuse")}
		}
		seen[current.String()] = struct{}{}
		addresses, err := a.resolveYouTubeMediaDestination(ctx, current)
		if err != nil {
			return nil, err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, current.String(), nil)
		if err != nil {
			return nil, permanentClippingImportError{youtubeImportOutcome("youtube_format_unavailable", "No supported audio/video format is available; upload an original file you are permitted to reuse")}
		}
		request.Header.Set("Accept", "video/*,audio/*,application/octet-stream")
		request.Header.Set("Accept-Encoding", "identity")
		if selection.UserAgent != "" {
			request.Header.Set("User-Agent", selection.UserAgent)
		}
		if selection.Referer != "" {
			request.Header.Set("Referer", selection.Referer)
		}
		transport := a.transportFactory(current, addresses)
		client := &http.Client{Transport: transport, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
		response, err := client.Do(request)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("%w: YouTube media request failed", errClippingImportRetryable)
		}
		if !isRedirectStatus(response.StatusCode) {
			return response, nil
		}
		location := response.Header.Get("Location")
		_ = response.Body.Close()
		if redirects >= clippingImportMaxRedirects || location == "" || strings.Contains(location, "#") {
			return nil, permanentClippingImportError{youtubeImportOutcome("youtube_format_unavailable", "No supported audio/video format is available; upload an original file you are permitted to reuse")}
		}
		reference, err := url.Parse(location)
		if err != nil {
			return nil, permanentClippingImportError{youtubeImportOutcome("youtube_format_unavailable", "No supported audio/video format is available; upload an original file you are permitted to reuse")}
		}
		current = current.ResolveReference(reference)
	}
}

func (a *ClippingAcquisition) resolveYouTubeMediaDestination(ctx context.Context, u *url.URL) ([]net.IP, error) {
	if err := validateYouTubeMediaURL(u); err != nil {
		return nil, permanentClippingImportError{youtubeImportOutcome("youtube_format_unavailable", "No supported audio/video format is available; upload an original file you are permitted to reuse")}
	}
	addresses, err := a.resolver.LookupIPAddr(ctx, strings.ToLower(u.Hostname()))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: YouTube media DNS lookup failed", errClippingImportRetryable)
	}
	if len(addresses) == 0 {
		return nil, permanentClippingImportError{youtubeImportOutcome("youtube_import_unavailable", "The YouTube link could not be imported safely; upload an original file you are permitted to reuse")}
	}
	pinned := make([]net.IP, 0, len(addresses))
	for _, address := range addresses {
		if address.Zone != "" || !isPublicUnicastIP(address.IP) {
			return nil, permanentClippingImportError{youtubeImportOutcome("youtube_import_unavailable", "The YouTube link could not be imported safely; upload an original file you are permitted to reuse")}
		}
		pinned = append(pinned, append(net.IP(nil), address.IP...))
	}
	return pinned, nil
}
