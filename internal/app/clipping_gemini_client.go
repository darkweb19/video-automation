package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"google.golang.org/genai"
)

const clippingGeminiRequestTimeout = 5 * time.Minute

type clippingGeminiClient struct {
	config ClippingGeminiConfig
	sdk    *genai.Client
}

type clippingGeminiClientDependencies struct {
	BaseTransport http.RoundTripper
	Authorize     func(*genai.ClientConfig) error
}

type clippingGeminiAttemptOutcome uint8

const (
	clippingGeminiAttemptNotSubmitted clippingGeminiAttemptOutcome = iota + 1
	clippingGeminiAttemptExplicitRejection
	clippingGeminiAttemptResponseReceived
	clippingGeminiAttemptUnknownPostSubmission
)

var (
	errClippingGeminiAttemptNotSubmitted      = errors.New("Gemini clipping request was not submitted")
	errClippingGeminiAttemptExplicitRejection = errors.New("Gemini explicitly rejected the clipping request")
	errClippingGeminiAttemptResponseReceived  = errors.New("Gemini returned an unusable clipping response")
	errClippingGeminiAttemptOutcomeUnknown    = errors.New("Gemini clipping request outcome is unknown after submission")
	errClippingGeminiCredentialsUnavailable   = errors.New("Gemini clipping credentials are unavailable")
	errClippingGeminiClientUnavailable        = errors.New("Gemini clipping client is unavailable")
	errClippingGeminiInvalidContext           = errors.New("Gemini clipping context is invalid")
)

// clippingGeminiAttemptError exposes only a stable outcome, safe HTTP status,
// and allowlisted cause. In particular, it never retains an SDK error whose
// text can contain provider response bodies or request URLs.
type clippingGeminiAttemptError struct {
	outcome    clippingGeminiAttemptOutcome
	httpStatus int
	reason     error
}

func (e *clippingGeminiAttemptError) Error() string {
	if e == nil {
		return "Gemini clipping request failed"
	}
	switch e.outcome {
	case clippingGeminiAttemptNotSubmitted:
		return errClippingGeminiAttemptNotSubmitted.Error()
	case clippingGeminiAttemptExplicitRejection:
		return errClippingGeminiAttemptExplicitRejection.Error()
	case clippingGeminiAttemptResponseReceived:
		return errClippingGeminiAttemptResponseReceived.Error()
	default:
		return errClippingGeminiAttemptOutcomeUnknown.Error()
	}
}

func (e *clippingGeminiAttemptError) Unwrap() []error {
	if e == nil {
		return nil
	}
	result := []error{clippingGeminiOutcomeSentinel(e.outcome)}
	for _, safe := range []error{
		context.Canceled,
		context.DeadlineExceeded,
		ErrClippingGeminiInvalidConfig,
		ErrClippingGeminiInvalidOptions,
		ErrClippingGeminiInvalidRequest,
		errClippingGeminiCredentialsUnavailable,
		errClippingGeminiClientUnavailable,
		errClippingGeminiInvalidContext,
		errClippingGeminiResponseBlocked,
		errClippingGeminiResponseTruncated,
		errClippingGeminiSourceTooLong,
		errClippingGeminiInvalidResponse,
		errClippingGeminiResponseTooLarge,
		errClippingGeminiInvalidDestination,
	} {
		if e.reason != nil && errors.Is(e.reason, safe) {
			result = append(result, safe)
		}
	}
	return result
}

func clippingGeminiOutcomeSentinel(outcome clippingGeminiAttemptOutcome) error {
	switch outcome {
	case clippingGeminiAttemptNotSubmitted:
		return errClippingGeminiAttemptNotSubmitted
	case clippingGeminiAttemptExplicitRejection:
		return errClippingGeminiAttemptExplicitRejection
	case clippingGeminiAttemptResponseReceived:
		return errClippingGeminiAttemptResponseReceived
	default:
		return errClippingGeminiAttemptOutcomeUnknown
	}
}

type clippingGeminiAttemptTracker struct {
	submitted  atomic.Bool
	httpStatus atomic.Int32
}

type clippingGeminiAttemptTrackerContextKey struct{}

type clippingGeminiSubmissionTrackingTransport struct {
	base http.RoundTripper
}

func (t *clippingGeminiSubmissionTrackingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request != nil {
		if tracker, ok := request.Context().Value(clippingGeminiAttemptTrackerContextKey{}).(*clippingGeminiAttemptTracker); ok {
			tracker.submitted.Store(true)
		}
	}
	response, err := t.base.RoundTrip(request)
	if request != nil && response != nil {
		if tracker, ok := request.Context().Value(clippingGeminiAttemptTrackerContextKey{}).(*clippingGeminiAttemptTracker); ok {
			tracker.httpStatus.Store(int32(response.StatusCode))
		}
	}
	return response, err
}

// clippingGeminiUnicodeResponseTransport preserves provider JSON bytes before
// the SDK decodes them. encoding/json replaces invalid UTF-8 and unpaired
// UTF-16 surrogate escapes with U+FFFD, which would silently alter model text.
// The inner bounded transport guarantees this buffer never exceeds the
// configured response limit.
type clippingGeminiUnicodeResponseTransport struct {
	base http.RoundTripper
}

func (t *clippingGeminiUnicodeResponseTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(request)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return nil, err
	}
	if response == nil || response.Body == nil {
		return nil, errClippingGeminiInvalidResponse
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		return nil, err
	}
	if !utf8.Valid(body) || !validClippingGeminiUnicodeEscapes(body) {
		return nil, errClippingGeminiInvalidResponse
	}
	buffered := *response
	buffered.Body = io.NopCloser(bytes.NewReader(body))
	return &buffered, nil
}

func newClippingGeminiClient(ctx context.Context, config ClippingGeminiConfig) (*clippingGeminiClient, error) {
	return newClippingGeminiClientWithDependencies(ctx, config, clippingGeminiClientDependencies{
		BaseTransport: http.DefaultTransport,
		Authorize: func(config *genai.ClientConfig) error {
			return config.UseDefaultCredentials()
		},
	})
}

func newClippingGeminiClientWithDependencies(ctx context.Context, config ClippingGeminiConfig, dependencies clippingGeminiClientDependencies) (*clippingGeminiClient, error) {
	if ctx == nil {
		return nil, &clippingGeminiAttemptError{outcome: clippingGeminiAttemptNotSubmitted, reason: errClippingGeminiInvalidContext}
	}
	if err := ValidateClippingGeminiConfig(config); err != nil {
		return nil, clippingGeminiNotSubmittedError(err)
	}
	if err := ctx.Err(); err != nil {
		return nil, clippingGeminiNotSubmittedError(err)
	}

	sdkConfig, err := clippingGeminiSDKConfig(config)
	if err != nil {
		return nil, clippingGeminiNotSubmittedError(err)
	}
	endpoint, _ := clippingGeminiEndpoint(config.Location)
	baseTransport := dependencies.BaseTransport
	if baseTransport == nil {
		baseTransport = http.DefaultTransport
	}
	trackedTransport := &clippingGeminiSubmissionTrackingTransport{base: baseTransport}
	boundedTransport, err := newClippingGeminiBoundedTransport(trackedTransport, endpoint)
	if err != nil {
		return nil, &clippingGeminiAttemptError{outcome: clippingGeminiAttemptNotSubmitted, reason: errClippingGeminiInvalidDestination}
	}
	responseTransport := &clippingGeminiUnicodeResponseTransport{base: boundedTransport}
	httpClient := &http.Client{
		Transport: responseTransport,
		Timeout:   clippingGeminiRequestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	sdkConfig.HTTPClient = httpClient
	if dependencies.Authorize == nil {
		dependencies.Authorize = func(config *genai.ClientConfig) error {
			return config.UseDefaultCredentials()
		}
	}
	if err := dependencies.Authorize(sdkConfig); err != nil {
		return nil, &clippingGeminiAttemptError{outcome: clippingGeminiAttemptNotSubmitted, reason: errClippingGeminiCredentialsUnavailable}
	}
	if !clippingGeminiSDKConfigIsPinned(sdkConfig, config, httpClient) {
		return nil, &clippingGeminiAttemptError{outcome: clippingGeminiAttemptNotSubmitted, reason: errClippingGeminiClientUnavailable}
	}
	sdkClient, err := genai.NewClient(ctx, sdkConfig)
	if err != nil {
		return nil, &clippingGeminiAttemptError{outcome: clippingGeminiAttemptNotSubmitted, reason: errClippingGeminiClientUnavailable}
	}
	return &clippingGeminiClient{config: config, sdk: sdkClient}, nil
}

func clippingGeminiSDKConfigIsPinned(sdkConfig *genai.ClientConfig, config ClippingGeminiConfig, httpClient *http.Client) bool {
	if sdkConfig == nil || sdkConfig.Credentials == nil || sdkConfig.APIKey != "" || sdkConfig.Backend != genai.BackendEnterprise ||
		sdkConfig.Project != config.ProjectID || sdkConfig.Location != config.Location || sdkConfig.HTTPClient != httpClient {
		return false
	}
	endpoint, ok := clippingGeminiEndpoint(config.Location)
	if !ok || sdkConfig.HTTPOptions.BaseURL != endpoint || sdkConfig.HTTPOptions.APIVersion != clippingGeminiAPIVersion || sdkConfig.HTTPOptions.RetryOptions == nil {
		return false
	}
	return sdkConfig.HTTPOptions.RetryOptions.Attempts != nil && *sdkConfig.HTTPOptions.RetryOptions.Attempts == 1
}

func (c *clippingGeminiClient) Analyze(ctx context.Context, request ClippingGeminiRequest) (clippingGeminiResponse, error) {
	if ctx == nil {
		return clippingGeminiResponse{}, &clippingGeminiAttemptError{outcome: clippingGeminiAttemptNotSubmitted, reason: errClippingGeminiInvalidContext}
	}
	if c == nil || c.sdk == nil {
		return clippingGeminiResponse{}, &clippingGeminiAttemptError{outcome: clippingGeminiAttemptNotSubmitted, reason: errClippingGeminiClientUnavailable}
	}
	if err := ctx.Err(); err != nil {
		return clippingGeminiResponse{}, clippingGeminiNotSubmittedError(err)
	}
	prepared, err := prepareClippingGeminiRequest(c.config, request)
	if err != nil {
		return clippingGeminiResponse{}, &clippingGeminiAttemptError{outcome: clippingGeminiAttemptNotSubmitted, reason: err}
	}
	if err := ctx.Err(); err != nil {
		return clippingGeminiResponse{}, clippingGeminiNotSubmittedError(err)
	}
	callContext, cancel := context.WithTimeout(ctx, clippingGeminiRequestTimeout)
	defer cancel()
	if err := callContext.Err(); err != nil {
		return clippingGeminiResponse{}, clippingGeminiNotSubmittedError(err)
	}
	tracker := &clippingGeminiAttemptTracker{}
	callContext = context.WithValue(callContext, clippingGeminiAttemptTrackerContextKey{}, tracker)
	response, callErr := c.sdk.Models.GenerateContent(callContext, prepared.Model, []*genai.Content{prepared.Content}, prepared.GenerationConfig)
	if callErr != nil {
		return clippingGeminiResponse{}, clippingGeminiClassifyCallError(callErr, tracker, callContext.Err())
	}
	result, decodeErr := decodeClippingGeminiResponse(response, request.Options)
	if decodeErr != nil {
		return result, &clippingGeminiAttemptError{
			outcome:    clippingGeminiAttemptResponseReceived,
			httpStatus: int(tracker.httpStatus.Load()),
			reason:     decodeErr,
		}
	}
	return result, nil
}

func clippingGeminiClassifyCallError(err error, tracker *clippingGeminiAttemptTracker, contextErr error) *clippingGeminiAttemptError {
	status := 0
	submitted := false
	if tracker != nil {
		status = int(tracker.httpStatus.Load())
		submitted = tracker.submitted.Load()
	}
	if status == 0 {
		status = clippingGeminiAPIErrorStatus(err)
	}
	if status >= 400 && status < 500 && status != http.StatusRequestTimeout && status != 499 {
		return &clippingGeminiAttemptError{
			outcome:    clippingGeminiAttemptExplicitRejection,
			httpStatus: status,
			reason:     errClippingGeminiAttemptExplicitRejection,
		}
	}
	if errors.Is(err, errClippingGeminiInvalidDestination) || !submitted {
		return clippingGeminiNotSubmittedError(clippingGeminiSafeContextError(err, contextErr))
	}
	return &clippingGeminiAttemptError{
		outcome:    clippingGeminiAttemptUnknownPostSubmission,
		httpStatus: status,
		reason:     clippingGeminiSafeUnknownReason(err, contextErr),
	}
}

func clippingGeminiNotSubmittedError(reason error) *clippingGeminiAttemptError {
	return &clippingGeminiAttemptError{outcome: clippingGeminiAttemptNotSubmitted, reason: reason}
}

func clippingGeminiSafeContextError(err, contextErr error) error {
	if cause := clippingGeminiContextCause(err, contextErr); cause != nil {
		return cause
	}
	if errors.Is(err, errClippingGeminiInvalidDestination) {
		return errClippingGeminiInvalidDestination
	}
	return errClippingGeminiCredentialsUnavailable
}

func clippingGeminiContextCause(err, contextErr error) error {
	if errors.Is(err, context.Canceled) || errors.Is(contextErr, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(contextErr, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return nil
}

func clippingGeminiSafeUnknownReason(err, contextErr error) error {
	if errors.Is(err, errClippingGeminiResponseTooLarge) {
		return errClippingGeminiResponseTooLarge
	}
	if errors.Is(err, errClippingGeminiInvalidResponse) {
		return errClippingGeminiInvalidResponse
	}
	return clippingGeminiContextCause(err, contextErr)
}

func clippingGeminiAPIErrorStatus(err error) int {
	var apiError genai.APIError
	if errors.As(err, &apiError) {
		return apiError.Code
	}
	var apiErrorPointer *genai.APIError
	if errors.As(err, &apiErrorPointer) && apiErrorPointer != nil {
		return apiErrorPointer.Code
	}
	return 0
}
