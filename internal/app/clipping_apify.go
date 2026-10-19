package app

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	clippingApifyActorID             = "thenetaji~youtube-video-downloader"
	clippingApifyAPIBaseURL          = "https://api.apify.com"
	clippingApifyRegion              = "US"
	clippingApifyRunWaitSeconds      = 30
	clippingApifyDefaultRequestLimit = 60 * time.Second
	clippingApifyDefaultPollLimit    = 4 * time.Hour
	clippingApifyMaxRequestLimit     = 5 * time.Minute
	clippingApifyMaxPollLimit        = 12 * time.Hour
	clippingApifyMaxJSONBytes        = 1 << 20
	clippingApifyMaxRedirects        = 5
)

var clippingApifyIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

var clippingApifySiteLocalIPv6 = netip.MustParsePrefix("fec0::/10")

type clippingApifyHTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type clippingApifyResolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

type clippingApifyDialContext func(context.Context, string, string) (net.Conn, error)

type clippingApifyClient struct {
	token          string
	actorID        string
	region         string
	requestTimeout time.Duration
	maxPoll        time.Duration
	apiBaseURL     string
	apiDoer        clippingApifyHTTPDoer
	mediaDoer      clippingApifyHTTPDoer
	resolver       clippingApifyResolver
	dial           clippingApifyDialContext
	configFailure  error
}

type clippingApifyArtifact struct {
	URL      string
	Bytes    int64
	HasBytes bool
}

type apifyProgressCallbackError struct {
	err error
}

func (e apifyProgressCallbackError) Error() string { return "Apify media progress update failed" }
func (e apifyProgressCallbackError) Unwrap() error { return e.err }

type clippingApifyStartInput struct {
	URLs              []clippingApifyURLInput `json:"urls"`
	Region            string                  `json:"region"`
	IncludeTranscript bool                    `json:"includeTranscript"`
	SaveMedia         bool                    `json:"saveMedia"`
	Quality           string                  `json:"quality"`
	Format            string                  `json:"format"`
}

type clippingApifyURLInput struct {
	URL string `json:"url"`
}

type clippingApifyRun struct {
	ID               string `json:"id"`
	Status           string `json:"status"`
	DefaultDatasetID string `json:"defaultDatasetId"`
}

type clippingApifyRunEnvelope struct {
	Data clippingApifyRun `json:"data"`
}

type clippingApifyDatasetItem struct {
	ID              string          `json:"id"`
	URL             string          `json:"url"`
	DurationSeconds json.RawMessage `json:"durationSeconds"`
	IsLive          *bool           `json:"isLive"`
	SavedFile       *struct {
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
	} `json:"savedFile"`
}

func newClippingApifyClientFromEnv() *clippingApifyClient {
	client := &clippingApifyClient{
		apiBaseURL: clippingApifyAPIBaseURL,
		region:     clippingApifyRegion,
		resolver:   net.DefaultResolver,
	}
	client.configFailure = client.loadConfig(os.Getenv)
	return client
}

func (c *clippingApifyClient) loadConfig(getenv func(string) string) error {
	token := strings.TrimSpace(getenv("APIFY_API_TOKEN"))
	configuredActor := strings.TrimSpace(getenv("APIFY_YOUTUBE_ACTOR_ID"))
	if token == "" || configuredActor == "" {
		return apifyPermanentFailure("apify_config_missing", "The Apify YouTube importer is not configured. Contact the service operator.")
	}
	if len(token) > 4096 || strings.ContainsAny(token, "\r\n") {
		return apifyPermanentFailure("apify_config_missing", "The Apify YouTube importer configuration is invalid. Contact the service operator.")
	}
	if configuredActor != clippingApifyActorID && configuredActor != "thenetaji/youtube-video-downloader" {
		return apifyPermanentFailure("apify_actor_mismatch", "The configured Apify actor does not match the verified YouTube video actor. Contact the service operator.")
	}
	c.token = token
	c.actorID = clippingApifyActorID
	c.requestTimeout = clippingApifyDefaultRequestLimit
	c.maxPoll = clippingApifyDefaultPollLimit
	if raw := strings.TrimSpace(getenv("APIFY_REQUEST_TIMEOUT")); raw != "" {
		value, err := time.ParseDuration(raw)
		if err != nil || value <= 0 || value > clippingApifyMaxRequestLimit {
			return apifyPermanentFailure("apify_config_missing", "APIFY_REQUEST_TIMEOUT must be a positive Go duration no longer than five minutes.")
		}
		c.requestTimeout = value
	}
	if raw := strings.TrimSpace(getenv("APIFY_MAX_POLL_DURATION")); raw != "" {
		value, err := time.ParseDuration(raw)
		if err != nil || value < time.Minute || value > clippingApifyMaxPollLimit {
			return apifyPermanentFailure("apify_config_missing", "APIFY_MAX_POLL_DURATION must be a Go duration from one minute through twelve hours.")
		}
		c.maxPoll = value
	}
	return nil
}

func (c *clippingApifyClient) validateConfig() error {
	if c == nil {
		return apifyPermanentFailure("apify_config_missing", "The Apify YouTube importer is not configured. Contact the service operator.")
	}
	if c.configFailure != nil {
		return c.configFailure
	}
	if c.token == "" || c.actorID != clippingApifyActorID || c.requestTimeout <= 0 || c.maxPoll <= 0 {
		return apifyPermanentFailure("apify_config_missing", "The Apify YouTube importer is not configured. Contact the service operator.")
	}
	return nil
}

func (c *clippingApifyClient) acquire(ctx context.Context, store *Store, source ClippingSource, canonicalURL *url.URL, outputPath string, maxBytes int64, trace *clippingDiagnosticTrace, progress ...func(int64) error) error {
	if err := c.validateConfig(); err != nil {
		return err
	}
	if store == nil || canonicalURL == nil || source.Kind != ClippingSourceYouTube || maxBytes <= 0 {
		return apifyPermanentFailure("apify_invalid_response", "The Apify import request could not be validated.")
	}
	videoID, ok := youtubeVideoID(canonicalURL)
	if !ok {
		return apifyPermanentFailure("apify_invalid_response", "The YouTube reference could not be validated.")
	}
	claim, err := store.PrepareClippingApifyRun(source.ID, source.ImportAttempts, c.actorID, c.maxPoll)
	if err != nil {
		return fmt.Errorf("%w: reserve Apify run state", errClippingImportRetryable)
	}
	state := claim.State
	if state.ActorID != c.actorID {
		return apifyPermanentFailure("apify_actor_mismatch", "This import belongs to a different Apify actor. Contact the service operator.")
	}
	if state.RunID == "" {
		if state.StartState == clippingApifyStartUnknown {
			return apifyPermanentFailure("apify_start_uncertain", "Apify may have accepted this import, but its run ID could not be recovered. Contact the service operator before retrying.")
		}
		if state.StartState != clippingApifyStartIntent && state.StartState != clippingApifyStartRejected {
			return apifyPermanentFailure("apify_invalid_response", "Apify import state could not be recovered safely.")
		}
		startTimeoutSeconds := state.DeadlineAt - time.Now().Unix()
		// The persisted deadline is stored as whole Unix seconds. Round the
		// remaining actor timeout down so fractional seconds cannot extend it.
		if startTimeoutSeconds > 0 {
			startTimeoutSeconds--
		}
		if startTimeoutSeconds <= 0 || startTimeoutSeconds > int64(clippingApifyMaxPollLimit/time.Second) {
			return apifyPermanentFailure("apify_timeout", "The saved Apify polling limit has expired before acquisition could start.")
		}
		trace.start(ClippingDiagnosticStageActorStart)
		var dispatch bool
		state, dispatch, err = store.MarkClippingApifyStartUnknown(source.ID, source.ImportAttempts, c.actorID)
		if err != nil {
			return fmt.Errorf("%w: claim Apify actor start", errClippingImportRetryable)
		}
		if !dispatch {
			if state.RunID == "" {
				return apifyPermanentFailure("apify_start_uncertain", "Apify may have accepted this import, but its run ID could not be recovered. Contact the service operator before retrying.")
			}
		} else {
			run, startErr := c.startRun(ctx, canonicalURL.String(), startTimeoutSeconds)
			if startErr != nil {
				if apiErr, ok := startErr.(clippingApifyAPIError); ok && apiErr.definitiveRejection {
					if _, resolveErr := store.ResolveClippingApifyStartRejected(source.ID, source.ImportAttempts, c.actorID); resolveErr != nil {
						return fmt.Errorf("%w: persist rejected Apify start", errClippingImportRetryable)
					}
					return apiErr.failure
				}
				return startErr
			}
			if !clippingApifyIDPattern.MatchString(run.ID) {
				return apifyPermanentFailure("apify_start_uncertain", "Apify accepted the import without returning a usable run ID. Contact the service operator before retrying.")
			}
			state, err = store.AttachClippingApifyRunID(source.ID, source.ImportAttempts, c.actorID, run.ID)
			if err != nil {
				// A user delete can tombstone the source after Apify acknowledges
				// the paid run but before its ID is durably attached. The usual
				// delete cleanup cannot see that ID while the row is still in
				// start_unknown, so reread durable source state and abort only for
				// that confirmed deletion. In particular, keep acknowledged runs
				// alive across shutdown and lease rollover for recovery.
				if current, readErr := store.ClippingSource(source.ID); readErr == nil && current.Status == ClippingSourceDeleted {
					c.abortRunBestEffort(context.Background(), run.ID)
				}
				return fmt.Errorf("%w: persist Apify run ID", errClippingImportRetryable)
			}
			trace.complete()
		}
	}
	if state.RunID == "" || !clippingApifyIDPattern.MatchString(state.RunID) {
		return apifyPermanentFailure("apify_start_uncertain", "Apify may have accepted this import, but its run ID could not be recovered. Contact the service operator before retrying.")
	}
	trace.start(ClippingDiagnosticStageActorPoll)
	datasetID, err := c.waitForRun(ctx, state)
	if err != nil {
		return err
	}
	items, err := c.readDataset(ctx, datasetID)
	if err != nil {
		return err
	}
	artifact, err := validateClippingApifyDataset(items, videoID, canonicalURL.String(), maxBytes)
	if err != nil {
		return err
	}
	trace.complete()
	trace.start(ClippingDiagnosticStageDownload)
	var progressCallback func(int64) error
	if len(progress) > 0 {
		progressCallback = progress[0]
	}
	var downloadErr error
	if artifact.HasBytes {
		downloadErr = c.downloadMediaWithProgress(ctx, artifact.URL, outputPath, maxBytes, artifact.Bytes, true, progressCallback)
	} else {
		downloadErr = c.downloadMediaWithProgress(ctx, artifact.URL, outputPath, maxBytes, 0, false, progressCallback)
	}
	if downloadErr != nil {
		trace.finish(ClippingDiagnosticFailed, downloadErr)
		return downloadErr
	}
	trace.complete()
	return nil
}

type clippingApifyAPIError struct {
	failure             error
	definitiveRejection bool
}

func (e clippingApifyAPIError) Error() string {
	if e.failure == nil {
		return "Apify request failed"
	}
	return e.failure.Error()
}

func (e clippingApifyAPIError) Unwrap() error { return e.failure }

func (c *clippingApifyClient) startRun(ctx context.Context, canonicalURL string, pollBudgetSeconds ...int64) (clippingApifyRun, error) {
	timeoutSeconds := int64(c.maxPoll / time.Second)
	if len(pollBudgetSeconds) > 0 {
		timeoutSeconds = pollBudgetSeconds[0]
	}
	if timeoutSeconds <= 0 || timeoutSeconds > int64(clippingApifyMaxPollLimit/time.Second) {
		return clippingApifyRun{}, apifyPermanentFailure("apify_invalid_response", "The persisted Apify run timeout is invalid.")
	}
	input := clippingApifyStartInput{
		URLs:              []clippingApifyURLInput{{URL: canonicalURL}},
		Region:            c.region,
		IncludeTranscript: false,
		SaveMedia:         true,
		Quality:           "best",
		Format:            "mp4",
	}
	body, err := json.Marshal(input)
	if err != nil {
		return clippingApifyRun{}, apifyPermanentFailure("apify_invalid_response", "The Apify actor input could not be prepared.")
	}
	query := url.Values{"waitForFinish": {"0"}, "timeout": {strconv.FormatInt(timeoutSeconds, 10)}}
	response, err := c.doAPI(ctx, http.MethodPost, "/acts/"+url.PathEscape(c.actorID)+"/runs", query, body)
	if err != nil {
		if ctx.Err() != nil {
			return clippingApifyRun{}, ctx.Err()
		}
		return clippingApifyRun{}, apifyPermanentFailure("apify_start_uncertain", "Apify may have accepted this import, but the run ID was not received. Contact the service operator before retrying.")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		failure := apifyHTTPFailure(response.StatusCode, true)
		return clippingApifyRun{}, clippingApifyAPIError{failure: failure, definitiveRejection: isDefinitiveApifyStartRejection(response.StatusCode)}
	}
	var envelope clippingApifyRunEnvelope
	if err := decodeApifyJSON(response.Body, &envelope); err != nil || envelope.Data.ID == "" {
		return clippingApifyRun{}, apifyPermanentFailure("apify_start_uncertain", "Apify accepted the import without returning a usable run ID. Contact the service operator before retrying.")
	}
	return envelope.Data, nil
}

func (c *clippingApifyClient) waitForRun(ctx context.Context, state clippingApifyRunState) (string, error) {
	deadline := time.Unix(state.DeadlineAt, 0)
	if state.DeadlineAt <= 0 || deadline.IsZero() {
		return "", apifyPermanentFailure("apify_invalid_response", "The persisted Apify run deadline is invalid.")
	}
	delay := time.Second
	for {
		if err := ctx.Err(); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				c.abortRunBestEffort(context.Background(), state.RunID)
				return "", apifyPermanentFailure("apify_timeout", "Apify did not finish this video within the configured polling limit.")
			}
			return "", err
		}
		if !time.Now().Before(deadline) {
			c.abortRunBestEffort(context.Background(), state.RunID)
			return "", apifyPermanentFailure("apify_timeout", "Apify did not finish this video within the configured polling limit.")
		}
		path := "/actor-runs/" + url.PathEscape(state.RunID)
		requestTimeout := c.requestTimeout
		if remaining := time.Until(deadline); remaining < requestTimeout {
			requestTimeout = remaining
		}
		response, err := c.doAPIWithTimeout(ctx, requestTimeout, http.MethodGet, path, url.Values{"waitForFinish": {strconv.Itoa(clippingApifyRunWaitSeconds)}}, nil)
		if err != nil {
			if waitErr := waitApify(ctx, delay, deadline); waitErr != nil {
				if errors.Is(waitErr, context.DeadlineExceeded) {
					c.abortRunBestEffort(context.Background(), state.RunID)
					return "", apifyPermanentFailure("apify_timeout", "Apify did not finish this video within the configured polling limit.")
				}
				return "", waitErr
			}
			delay = minApifyDelay(delay * 2)
			continue
		}
		if response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500 {
			response.Body.Close()
			if waitErr := waitApify(ctx, delay, deadline); waitErr != nil {
				if errors.Is(waitErr, context.DeadlineExceeded) {
					c.abortRunBestEffort(context.Background(), state.RunID)
					return "", apifyPermanentFailure("apify_timeout", "Apify did not finish this video within the configured polling limit.")
				}
				return "", waitErr
			}
			delay = minApifyDelay(delay * 2)
			continue
		}
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
			response.Body.Close()
			return "", apifyPermanentFailure("apify_auth_failed", "Apify rejected the configured credentials or actor access. Contact the service operator.")
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			response.Body.Close()
			return "", apifyPermanentFailure("apify_invalid_response", "Apify could not retrieve the saved run state.")
		}
		var envelope clippingApifyRunEnvelope
		decodeErr := decodeApifyJSON(response.Body, &envelope)
		response.Body.Close()
		if decodeErr != nil {
			if waitErr := waitApify(ctx, delay, deadline); waitErr != nil {
				if errors.Is(waitErr, context.DeadlineExceeded) {
					c.abortRunBestEffort(context.Background(), state.RunID)
					return "", apifyPermanentFailure("apify_timeout", "Apify did not finish this video within the configured polling limit.")
				}
				return "", waitErr
			}
			delay = minApifyDelay(delay * 2)
			continue
		}
		run := envelope.Data
		if run.ID != state.RunID || run.ID == "" {
			return "", apifyPermanentFailure("apify_invalid_response", "Apify returned a run that did not match this import.")
		}
		switch strings.ToUpper(run.Status) {
		case "SUCCEEDED":
			if !clippingApifyIDPattern.MatchString(run.DefaultDatasetID) {
				return "", apifyPermanentFailure("apify_artifact_missing", "Apify completed without a saved video result.")
			}
			return run.DefaultDatasetID, nil
		case "FAILED", "ABORTED", "TIMED-OUT":
			return "", apifyPermanentFailure("apify_actor_failed", "Apify could not acquire this public video. Check that the link works and try again later.")
		case "READY", "RUNNING", "ABORTING", "TIMING-OUT":
			// Apify's waitForFinish response may still be active; continue within
			// the persisted deadline rather than starting another actor run.
		default:
			return "", apifyPermanentFailure("apify_invalid_response", "Apify returned an unsupported run status.")
		}
		if waitErr := waitApify(ctx, delay, deadline); waitErr != nil {
			if errors.Is(waitErr, context.DeadlineExceeded) {
				c.abortRunBestEffort(context.Background(), state.RunID)
				return "", apifyPermanentFailure("apify_timeout", "Apify did not finish this video within the configured polling limit.")
			}
			return "", waitErr
		}
		delay = minApifyDelay(delay * 2)
	}
}

func (c *clippingApifyClient) readDataset(ctx context.Context, datasetID string) ([]clippingApifyDatasetItem, error) {
	query := url.Values{"format": {"json"}, "clean": {"true"}}
	response, err := c.doAPI(ctx, http.MethodGet, "/datasets/"+url.PathEscape(datasetID)+"/items", query, nil)
	if err != nil {
		return nil, apifyRetryableFailure("apify_invalid_response", "Apify's saved video result could not be retrieved.", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return nil, apifyPermanentFailure("apify_auth_failed", "Apify rejected the configured credentials or actor access. Contact the service operator.")
	}
	if response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500 {
		return nil, apifyRetryableFailure("apify_rate_limited", "Apify is temporarily limiting requests. The saved run will be resumed automatically.", nil)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, apifyPermanentFailure("apify_artifact_missing", "Apify completed without a retrievable saved video result.")
	}
	var items []clippingApifyDatasetItem
	if err := decodeApifyJSON(response.Body, &items); err != nil {
		return nil, apifyPermanentFailure("apify_invalid_response", "Apify returned an invalid saved video result.")
	}
	return items, nil
}

func validateClippingApifyDataset(items []clippingApifyDatasetItem, expectedID, canonicalURL string, maxBytes int64) (clippingApifyArtifact, error) {
	if len(items) != 1 {
		return clippingApifyArtifact{}, apifyPermanentFailure("apify_artifact_missing", "Apify did not return exactly one saved video for this import.")
	}
	item := items[0]
	if item.ID != "" && item.ID != expectedID {
		return clippingApifyArtifact{}, apifyPermanentFailure("apify_invalid_response", "Apify returned a result for a different YouTube video.")
	}
	if item.URL != "" {
		u, kind, err := normalizeClippingImportURL(item.URL)
		if err != nil || kind != ClippingSourceYouTube || u.String() != canonicalURL {
			return clippingApifyArtifact{}, apifyPermanentFailure("apify_invalid_response", "Apify returned a result for a different YouTube video.")
		}
	}
	if item.IsLive != nil && *item.IsLive {
		return clippingApifyArtifact{}, apifyPermanentFailure("youtube_media_invalid", "Live and upcoming YouTube videos cannot be imported.")
	}
	if len(item.DurationSeconds) > 0 && string(item.DurationSeconds) != "null" {
		seconds, err := strconv.ParseFloat(string(item.DurationSeconds), 64)
		if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 {
			return clippingApifyArtifact{}, apifyPermanentFailure("apify_invalid_response", "Apify returned invalid video duration metadata.")
		}
		if seconds > float64(MaxClippingSourceDurationMS)/1000 {
			return clippingApifyArtifact{}, apifyPermanentFailure("youtube_duration_limit_exceeded", "This YouTube video is longer than the four-hour import limit.")
		}
	}
	if item.SavedFile == nil || strings.TrimSpace(item.SavedFile.URL) == "" {
		return clippingApifyArtifact{}, apifyPermanentFailure("apify_artifact_missing", "Apify returned video information without a downloadable video file.")
	}
	artifact := clippingApifyArtifact{URL: item.SavedFile.URL}
	if len(item.SavedFile.Bytes) > 0 && string(item.SavedFile.Bytes) != "null" {
		if bytes, err := parseApifyOptionalBytes(item.SavedFile.Bytes); err != nil || bytes == 0 {
			return clippingApifyArtifact{}, apifyPermanentFailure("apify_invalid_response", "Apify returned invalid video size metadata.")
		} else if bytes > maxBytes {
			return clippingApifyArtifact{}, youtubeSizeLimitFailure()
		} else {
			artifact.Bytes, artifact.HasBytes = bytes, true
		}
	}
	if item.SavedFile.ContentType != "" {
		mediaType, _, err := mime.ParseMediaType(item.SavedFile.ContentType)
		if err != nil || !(strings.HasPrefix(strings.ToLower(mediaType), "video/") || strings.EqualFold(mediaType, "application/octet-stream")) {
			return clippingApifyArtifact{}, apifyPermanentFailure("apify_invalid_response", "Apify returned a non-video artifact.")
		}
	}
	return artifact, nil
}

func parseApifyOptionalBytes(raw json.RawMessage) (int64, error) {
	var number json.Number
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&number); err != nil {
		return 0, err
	}
	value, err := strconv.ParseInt(number.String(), 10, 64)
	if err != nil || value < 0 {
		return 0, errors.New("invalid Apify media size")
	}
	return value, nil
}

func (c *clippingApifyClient) downloadMedia(ctx context.Context, rawURL, destination string, maxBytes int64, expectedBytes ...int64) error {
	var expected int64
	hasExpected := len(expectedBytes) > 0
	if hasExpected {
		expected = expectedBytes[0]
	}
	return c.downloadMediaWithProgress(ctx, rawURL, destination, maxBytes, expected, hasExpected, nil)
}

func (c *clippingApifyClient) downloadMediaWithProgress(ctx context.Context, rawURL, destination string, maxBytes, expectedSize int64, hasExpectedBytes bool, progress func(int64) error) error {
	mediaCtx, cancel := context.WithTimeout(ctx, clippingImportMaxDuration)
	defer cancel()
	ctx = mediaCtx
	current, err := validateApifyMediaURL(rawURL)
	if err != nil {
		return apifyPermanentFailure("apify_download_failed", "Apify returned an unsafe video download destination.")
	}
	for redirects := 0; ; redirects++ {
		response, err := c.doMedia(ctx, current)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			return apifyRetryableFailure("apify_download_failed", "The saved video could not be downloaded from Apify.", err)
		}
		if response.StatusCode >= 300 && response.StatusCode < 400 {
			location := response.Header.Get("Location")
			response.Body.Close()
			if location == "" || redirects >= clippingApifyMaxRedirects {
				return apifyPermanentFailure("apify_download_failed", "Apify returned an invalid video download redirect.")
			}
			next, parseErr := current.Parse(location)
			if parseErr != nil {
				return apifyPermanentFailure("apify_download_failed", "Apify returned an invalid video download redirect.")
			}
			current, err = validateApifyMediaURL(next.String())
			if err != nil {
				return apifyPermanentFailure("apify_download_failed", "Apify redirected the video download to an unsafe destination.")
			}
			continue
		}
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusGone {
			response.Body.Close()
			return apifyRetryableFailure("apify_download_expired", "Apify's temporary video download link expired. The saved run will be queried again automatically.", nil)
		}
		if response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500 {
			response.Body.Close()
			return apifyRetryableFailure("apify_rate_limited", "Apify is temporarily limiting video downloads. The saved run will be resumed automatically.", nil)
		}
		if response.StatusCode != http.StatusOK {
			response.Body.Close()
			return apifyRetryableFailure("apify_download_failed", "The saved video could not be downloaded from Apify.", nil)
		}
		if response.ContentLength > maxBytes {
			response.Body.Close()
			return youtubeSizeLimitFailure()
		}
		if response.ContentLength == 0 {
			response.Body.Close()
			return apifyRetryableFailure("apify_download_failed", "Apify returned an empty video file.", nil)
		}
		if contentType := response.Header.Get("Content-Type"); contentType != "" && !apifyMediaContentType(contentType) {
			response.Body.Close()
			return apifyPermanentFailure("apify_invalid_response", "Apify returned a non-video download response.")
		}
		actualBytes, streamErr := streamApifyMediaCount(ctx, response.Body, destination, maxBytes, progress)
		if streamErr != nil {
			response.Body.Close()
			var callbackErr apifyProgressCallbackError
			if errors.As(streamErr, &callbackErr) {
				return callbackErr.err
			}
			if errors.Is(streamErr, errClippingSourceSizeLimit) {
				return youtubeSizeLimitFailure()
			}
			if errors.Is(streamErr, context.Canceled) || errors.Is(streamErr, context.DeadlineExceeded) {
				return streamErr
			}
			return apifyRetryableFailure("apify_download_failed", "The saved video download did not complete.", nil)
		}
		if err := response.Body.Close(); err != nil {
			_ = os.Remove(destination)
			return apifyRetryableFailure("apify_download_failed", "The saved video download did not complete.", nil)
		}
		if (response.ContentLength >= 0 && actualBytes != response.ContentLength) || (hasExpectedBytes && expectedSize != actualBytes) {
			_ = os.Remove(destination)
			return apifyRetryableFailure("apify_download_failed", "Apify returned a truncated video download.", nil)
		}
		return nil
	}
}

var errClippingSourceSizeLimit = errors.New("source size limit exceeded")

func streamApifyMedia(ctx context.Context, body io.Reader, destination string, maxBytes int64) error {
	_, err := streamApifyMediaCount(ctx, body, destination, maxBytes)
	return err
}

func streamApifyMediaCount(ctx context.Context, body io.Reader, destination string, maxBytes int64, progress ...func(int64) error) (int64, error) {
	if maxBytes <= 0 || maxBytes == math.MaxInt64 {
		return 0, errClippingSourceSizeLimit
	}
	file, err := openClippingRegularFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return 0, err
	}
	ok := false
	defer func() {
		_ = file.Close()
		if !ok {
			_ = os.Remove(destination)
		}
	}()
	limited := &io.LimitedReader{R: body, N: maxBytes + 1}
	buffer := make([]byte, 256<<10)
	var total int64
	var progressCallback func(int64) error
	if len(progress) > 0 {
		progressCallback = progress[0]
	}
	const progressInterval = int64(32 << 20)
	nextProgress := progressInterval
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		count, readErr := limited.Read(buffer)
		if count > 0 {
			if int64(count) > maxBytes-total {
				return total, errClippingSourceSizeLimit
			}
			written, writeErr := file.Write(buffer[:count])
			if writeErr != nil {
				return total, writeErr
			}
			if written != count {
				return total, io.ErrShortWrite
			}
			total += int64(count)
			if progressCallback != nil && total >= nextProgress {
				if err := progressCallback(total); err != nil {
					return total, apifyProgressCallbackError{err: err}
				}
				nextProgress = total + progressInterval
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return total, readErr
		}
	}
	if total < 1 {
		return total, io.ErrUnexpectedEOF
	}
	if total > maxBytes {
		return total, errClippingSourceSizeLimit
	}
	if progressCallback != nil {
		if err := progressCallback(total); err != nil {
			return total, apifyProgressCallbackError{err: err}
		}
	}
	if err := file.Sync(); err != nil {
		return total, err
	}
	if err := file.Close(); err != nil {
		return total, err
	}
	ok = true
	return total, nil
}

func apifyMediaContentType(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return false
	}
	return strings.HasPrefix(strings.ToLower(mediaType), "video/") || strings.EqualFold(mediaType, "application/octet-stream")
}

func validateApifyMediaURL(raw string) (*url.URL, error) {
	if raw == "" || strings.TrimSpace(raw) != raw || len(raw) > 8192 || hasURLControl(raw) {
		return nil, errors.New("invalid Apify media URL")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Opaque != "" || u.User != nil || u.Fragment != "" || u.Host == "" {
		return nil, errors.New("invalid Apify media URL")
	}
	if strings.Contains(u.Host, "%") {
		return nil, errors.New("invalid Apify media host")
	}
	port := u.Port()
	if port != "" && port != "443" {
		return nil, errors.New("Apify media URL uses a forbidden port")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || strings.Contains(host, ":") || strings.HasSuffix(host, ".") {
		return nil, errors.New("Apify media URL must use a hostname")
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return nil, errors.New("Apify media URL must not use an IP literal")
	}
	u.Host = host
	if port != "" {
		u.Host = net.JoinHostPort(host, "443")
	}
	return u, nil
}

func (c *clippingApifyClient) doMedia(ctx context.Context, target *url.URL) (*http.Response, error) {
	validated, err := validateApifyMediaURL(target.String())
	if err != nil {
		return nil, err
	}
	target = validated
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "video/mp4,video/*,application/octet-stream")
	if c.mediaDoer != nil {
		if _, err := c.resolveAndValidate(ctx, target.Hostname()); err != nil {
			return nil, err
		}
		return c.mediaDoer.Do(req)
	}
	return c.doPinned(ctx, req, target)
}

func (c *clippingApifyClient) doAPI(ctx context.Context, method, path string, query url.Values, body []byte) (*http.Response, error) {
	return c.doAPIWithTimeout(ctx, c.requestTimeout, method, path, query, body)
}

func (c *clippingApifyClient) doAPIWithTimeout(ctx context.Context, timeout time.Duration, method, path string, query url.Values, body []byte) (*http.Response, error) {
	if timeout <= 0 {
		return nil, context.DeadlineExceeded
	}
	base, err := url.Parse(c.apiBaseURL)
	if err != nil || base.Scheme != "https" || base.Host != "api.apify.com" {
		if c.apiDoer == nil {
			return nil, errors.New("invalid Apify API endpoint")
		}
	}
	if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "?#") {
		return nil, errors.New("invalid Apify API path")
	}
	target := strings.TrimRight(c.apiBaseURL, "/") + "/v2" + path
	parsed, err := url.Parse(target)
	if err != nil {
		return nil, err
	}
	parsed.RawQuery = query.Encode()
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	req, err := http.NewRequestWithContext(requestCtx, method, parsed.String(), bytes.NewReader(body))
	if err != nil {
		cancel()
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	var response *http.Response
	if c.apiDoer != nil {
		response, err = c.apiDoer.Do(req)
	} else {
		response, err = c.doPinned(requestCtx, req, parsed)
	}
	if err != nil {
		cancel()
		return nil, err
	}
	if response == nil || response.Body == nil {
		cancel()
		return nil, errors.New("Apify returned an empty HTTP response")
	}
	response.Body = &apifyCancelBody{ReadCloser: response.Body, cancel: cancel}
	return response, nil
}

func (c *clippingApifyClient) doPinned(ctx context.Context, req *http.Request, target *url.URL) (*http.Response, error) {
	addresses, err := c.resolveAndValidate(ctx, target.Hostname())
	if err != nil {
		return nil, err
	}
	port := "443"
	if target.Port() != "" {
		port = target.Port()
	}
	var dial clippingApifyDialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext(ctx, network, address)
	}
	if c.dial != nil {
		dial = c.dial
	}
	transport := &http.Transport{
		Proxy:                 nil,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   c.requestTimeout,
		ResponseHeaderTimeout: c.requestTimeout,
		DisableCompression:    true,
		DisableKeepAlives:     true,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			requestedHost, requestedPort, splitErr := net.SplitHostPort(address)
			if splitErr != nil || !strings.EqualFold(strings.TrimSuffix(requestedHost, "."), target.Hostname()) || requestedPort != port {
				return nil, errors.New("Apify HTTP request target changed after validation")
			}
			var lastErr error
			for _, ip := range addresses {
				if !isClippingApifyPublicIP(ip) {
					continue
				}
				connection, dialErr := dial(ctx, network, net.JoinHostPort(ip.String(), port))
				if dialErr == nil {
					return connection, nil
				}
				lastErr = dialErr
			}
			if lastErr == nil {
				return nil, errors.New("Apify host has no validated public address")
			}
			return nil, lastErr
		},
	}
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		transport.CloseIdleConnections()
		return nil, err
	}
	response.Body = &apifyClosingBody{ReadCloser: response.Body, close: transport.CloseIdleConnections}
	return response, nil
}

type apifyCancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *apifyCancelBody) Close() error {
	err := b.ReadCloser.Close()
	if b.cancel != nil {
		b.cancel()
	}
	return err
}

type apifyClosingBody struct {
	io.ReadCloser
	close func()
}

func (b *apifyClosingBody) Close() error {
	err := b.ReadCloser.Close()
	if b.close != nil {
		b.close()
	}
	return err
}

func (c *clippingApifyClient) resolveAndValidate(ctx context.Context, host string) ([]net.IP, error) {
	if host == "" || strings.HasSuffix(host, ".") || strings.Contains(host, "%") {
		return nil, errors.New("invalid Apify host")
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return nil, errors.New("IP literal is forbidden")
	}
	resolver := c.resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	resolved, err := resolver.LookupIPAddr(ctx, host)
	if err != nil || len(resolved) == 0 {
		return nil, errors.New("Apify host DNS lookup failed")
	}
	addresses := make([]net.IP, 0, len(resolved))
	for _, answer := range resolved {
		if answer.Zone != "" || !isClippingApifyPublicIP(answer.IP) {
			return nil, errors.New("Apify host resolved to a non-public address")
		}
		addresses = append(addresses, append(net.IP(nil), answer.IP...))
	}
	return addresses, nil
}

func isClippingApifyPublicIP(ip net.IP) bool {
	if !isPublicUnicastIP(ip) {
		return false
	}
	address, ok := netipFromIP(ip)
	return ok && !clippingApifySiteLocalIPv6.Contains(address)
}

func (c *clippingApifyClient) abortRunBestEffort(parent context.Context, runID string) {
	if !clippingApifyIDPattern.MatchString(runID) || c.validateConfig() != nil {
		return
	}
	ctx, cancel := context.WithTimeout(parent, minApifyDuration(c.requestTimeout, 5*time.Second))
	defer cancel()
	response, err := c.doAPI(ctx, http.MethodPost, "/actor-runs/"+url.PathEscape(runID)+"/abort", nil, nil)
	if err == nil && response != nil {
		_ = response.Body.Close()
	}
}

func decodeApifyJSON(reader io.Reader, destination any) error {
	bounded, err := io.ReadAll(io.LimitReader(reader, clippingApifyMaxJSONBytes+1))
	if err != nil {
		return err
	}
	if len(bounded) > clippingApifyMaxJSONBytes {
		return errors.New("Apify response exceeds its size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(bounded))
	decoder.UseNumber()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("Apify returned a non-single JSON response")
	}
	return nil
}

func apifyHTTPFailure(statusCode int, start bool) error {
	switch statusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return apifyPermanentFailure("apify_auth_failed", "Apify rejected the configured credentials or actor access. Contact the service operator.")
	case http.StatusTooManyRequests:
		return apifyRetryableFailure("apify_rate_limited", "Apify is temporarily limiting requests. The saved run will be resumed automatically.", nil)
	default:
		if start && isDefinitiveApifyStartRejection(statusCode) {
			return apifyPermanentFailure("apify_invalid_response", "Apify rejected the import request. Check the service configuration.")
		}
		if statusCode >= 500 && !start {
			return apifyRetryableFailure("apify_invalid_response", "Apify is temporarily unavailable. The saved run will be resumed automatically.", nil)
		}
		if start {
			return apifyPermanentFailure("apify_start_uncertain", "Apify may have accepted this import, but the run ID was not received. Contact the service operator before retrying.")
		}
		return apifyPermanentFailure("apify_invalid_response", "Apify rejected the import request. Check the service configuration.")
	}
}

func isDefinitiveApifyStartRejection(statusCode int) bool {
	switch statusCode {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusPaymentRequired,
		http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity,
		http.StatusTooManyRequests:
		return true
	default:
		return false
	}
}

func apifyPermanentFailure(code, message string) error {
	return withClippingDiagnosticCode(permanentClippingImportError{&ClippingAcquisitionError{Status: "unsupported", Reason: code, Message: message}}, code)
}

func apifyRetryableFailure(code, message string, cause error) error {
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		return cause
	}
	return withClippingDiagnosticCode(fmt.Errorf("%w: %s", errClippingImportRetryable, message), code)
}

func minApifyDuration(left, right time.Duration) time.Duration {
	if left < right {
		return left
	}
	return right
}

func waitApify(ctx context.Context, delay time.Duration, deadline time.Time) error {
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return context.DeadlineExceeded
	}
	if delay > remaining {
		delay = remaining
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		if !time.Now().Before(deadline) {
			return context.DeadlineExceeded
		}
		return nil
	}
}

func minApifyDelay(delay time.Duration) time.Duration {
	if delay > 30*time.Second {
		return 30 * time.Second
	}
	return delay
}
