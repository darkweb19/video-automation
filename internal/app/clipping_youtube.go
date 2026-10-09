package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	clippingYouTubeMuxedFormat      = "best[acodec!=none][vcodec!=none][protocol=https][ext=mp4]/best[acodec!=none][vcodec!=none][protocol=https][ext=webm]"
	clippingYouTubeRuntime          = "deno:/usr/bin/deno"
)

// The production image installs the verified yt-dlp wrapper at /usr/local/bin.
// /usr/bin is a fixed-path fallback for native Linux runtimes; ambient PATH is ignored.
var clippingYouTubeExecutablePaths = []string{"/usr/local/bin/yt-dlp", "/usr/bin/yt-dlp"}

var errClippingDurationLimitExceeded = errors.New("source duration exceeds the four-hour limit")

type youtubeSelection struct {
	URL        *url.URL
	DurationMS int64
	SizeBytes  int64
	UserAgent  string
	Referer    string
}

type youtubeExtractorOutput struct {
	ID                  string          `json:"id"`
	Type                string          `json:"_type"`
	Availability        string          `json:"availability"`
	LiveStatus          string          `json:"live_status"`
	IsLive              bool            `json:"is_live"`
	Duration            json.Number     `json:"duration"`
	Entries             json.RawMessage `json:"entries"`
	RequestedDownloads  []youtubeFormat `json:"requested_downloads"`
	TopLevelFormatID    string          `json:"format_id"`
	TopLevelAudioCodec  string          `json:"acodec"`
	TopLevelVideoCodec  string          `json:"vcodec"`
	TopLevelProtocol    string          `json:"protocol"`
	TopLevelExtension   string          `json:"ext"`
	TopLevelDirectURL   string          `json:"url"`
	TopLevelFileSize    json.Number     `json:"filesize"`
	TopLevelFileSizeEst json.Number     `json:"filesize_approx"`
}

type youtubeFormat struct {
	FormatID       string            `json:"format_id"`
	AudioCodec     string            `json:"acodec"`
	VideoCodec     string            `json:"vcodec"`
	Protocol       string            `json:"protocol"`
	Extension      string            `json:"ext"`
	URL            string            `json:"url"`
	HTTPHeaders    map[string]string `json:"http_headers"`
	FileSize       json.Number       `json:"filesize"`
	FileSizeApprox json.Number       `json:"filesize_approx"`
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
	if err := ctx.Err(); err != nil {
		return youtubeSelection{}, err
	}
	var err error
	videoID, ok := youtubeVideoID(canonicalURL)
	if !ok {
		return youtubeSelection{}, permanentClippingImportError{youtubeImportOutcome("youtube_import_unavailable", "The YouTube link could not be imported; upload an original file you are permitted to reuse")}
	}
	executable := a.youtubeExecutable
	if executable == "" {
		executable = findYouTubeExecutable(clippingYouTubeExecutablePaths)
	}
	if err != nil || executable == "" {
		return youtubeSelection{}, permanentClippingImportError{youtubeImportOutcome("youtube_import_unavailable", "The YouTube importer is unavailable in this runtime; upload an original file you are permitted to reuse")}
	}
	runtimeSpec := a.youtubeRuntime
	if runtimeSpec == "" {
		runtimeSpec = clippingYouTubeRuntime
	}
	runtimeName, runtimePath, hasRuntimePath := strings.Cut(runtimeSpec, ":")
	if !hasRuntimePath || runtimeName != "deno" || runtimePath == "" {
		return youtubeSelection{}, permanentClippingImportError{youtubeImportOutcome("youtube_import_unavailable", "The YouTube importer is unavailable in this runtime; upload an original file you are permitted to reuse")}
	}
	if _, err := exec.LookPath(runtimePath); err != nil {
		return youtubeSelection{}, permanentClippingImportError{youtubeImportOutcome("youtube_import_unavailable", "The YouTube importer is unavailable in this runtime; upload an original file you are permitted to reuse")}
	}

	extractorCtx, cancel := context.WithTimeout(ctx, clippingYouTubeExtractorTimeout)
	defer cancel()
	proxy, err := startYouTubeProxy(extractorCtx, a.resolver)
	if err != nil {
		if ctx.Err() != nil {
			return youtubeSelection{}, ctx.Err()
		}
		return youtubeSelection{}, fmt.Errorf("%w: YouTube importer proxy could not start", errClippingImportRetryable)
	}
	defer proxy.Close()
	denoCache, err := createYouTubeDenoCache()
	if err != nil {
		return youtubeSelection{}, permanentClippingImportError{youtubeImportOutcome("youtube_import_unavailable", "The YouTube importer is unavailable in this runtime; upload an original file you are permitted to reuse")}
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
		"--format", clippingYouTubeMuxedFormat,
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
		return youtubeSelection{}, fmt.Errorf("%w: YouTube metadata request timed out", errClippingImportRetryable)
	}
	if limitedOutput.exceeded {
		return youtubeSelection{}, permanentClippingImportError{youtubeImportOutcome("youtube_format_unavailable", "No supported audio/video format is available; upload an original file you are permitted to reuse")}
	}
	if runErr != nil {
		// The child output can contain signed media URLs and provider responses.
		// Never include command output, stderr, or the child error in a returned error.
		return youtubeSelection{}, fmt.Errorf("%w: YouTube metadata request failed", errClippingImportRetryable)
	}
	if err := proxy.Err(); err != nil {
		return youtubeSelection{}, fmt.Errorf("%w: YouTube metadata proxy stopped", errClippingImportRetryable)
	}
	selection, err := parseYouTubeExtractorOutput(limitedOutput.data, videoID)
	if err != nil {
		return youtubeSelection{}, err
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
	if metadata.IsLive || metadata.LiveStatus != "" && metadata.LiveStatus != "not_live" {
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
	if len(metadata.RequestedDownloads) != 1 {
		return fallback("youtube_format_unavailable")
	}
	format := metadata.RequestedDownloads[0]
	if strings.TrimSpace(format.FormatID) == "" || !hasYouTubeAudioVideo(format.AudioCodec, format.VideoCodec) ||
		!strings.EqualFold(strings.TrimSpace(format.Protocol), "https") || !supportedYouTubeExtension(format.Extension) {
		return fallback("youtube_format_unavailable")
	}
	if metadata.TopLevelFormatID != "" && metadata.TopLevelFormatID != format.FormatID ||
		metadata.TopLevelAudioCodec != "" && !hasYouTubeAudioVideo(metadata.TopLevelAudioCodec, metadata.TopLevelVideoCodec) ||
		metadata.TopLevelProtocol != "" && !strings.EqualFold(metadata.TopLevelProtocol, format.Protocol) ||
		metadata.TopLevelExtension != "" && !strings.EqualFold(metadata.TopLevelExtension, format.Extension) {
		return fallback("youtube_format_unavailable")
	}
	if metadata.TopLevelDirectURL != "" && metadata.TopLevelDirectURL != format.URL {
		return fallback("youtube_format_unavailable")
	}
	for _, size := range []json.Number{format.FileSize, format.FileSizeApprox, metadata.TopLevelFileSize, metadata.TopLevelFileSizeEst} {
		value, present, sizeErr := parseYouTubeDeclaredSize(size)
		if sizeErr != nil {
			return fallback("youtube_format_unavailable")
		}
		if present && value > MaxClippingSourceBytes {
			return youtubeSelection{}, permanentClippingImportError{youtubeImportOutcome("youtube_size_limit_exceeded", "The video exceeds the 20 GiB limit; upload a smaller original file you are permitted to reuse")}
		}
	}
	mediaURL, err := url.Parse(format.URL)
	if err != nil || validateYouTubeMediaURL(mediaURL) != nil {
		return fallback("youtube_format_unavailable")
	}
	userAgent, referer, err := safeYouTubeRequestHeaders(format.HTTPHeaders, expectedID)
	if err != nil {
		return fallback("youtube_format_unavailable")
	}
	selection := youtubeSelection{URL: mediaURL, DurationMS: durationMS, UserAgent: userAgent}
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
		request.Header.Set("Accept", "video/*,application/octet-stream")
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
