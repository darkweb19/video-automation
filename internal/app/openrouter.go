package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const openRouterBaseURL = "https://openrouter.ai/api/v1"

var defaultOpenRouterHTTPClient = &http.Client{
	Timeout: 45 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

var defaultOpenRouterStoryHTTPClient = &http.Client{
	// Leave a small margin beyond the story context so context cancellation,
	// rather than the transport timer, is recorded in the project trace.
	Timeout: storyGenerationTimeout + 15*time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

var defaultOpenRouterContentClient = &http.Client{
	Transport: func() http.RoundTripper {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.ResponseHeaderTimeout = 45 * time.Second
		return transport
	}(),
	// Video bodies can take longer than an API request to transfer. The request
	// context and transport timeouts still bound connection and header waits.
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// OpenRouterClient is the only upstream integration used by this application.
type OpenRouterClient struct {
	APIKey            string
	HTTPClient        *http.Client
	StoryHTTPClient   *http.Client
	ContentHTTPClient *http.Client
	BaseURL           string
	promptState       *randomPromptState
}

type upstreamError struct {
	StatusCode     int
	Message        string
	RetryAt        time.Time
	RateLimitScope string
	LimitSource    string
	ProviderName   string
	ErrorType      string
}

func (e *upstreamError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("OpenRouter request failed with status %d", e.StatusCode)
	}
	return fmt.Sprintf("OpenRouter request failed: %s", e.Message)
}

func NewOpenRouterClient(apiKey string) *OpenRouterClient {
	return &OpenRouterClient{
		APIKey: apiKey,
		// Do not allow Go's default redirect behavior to retain credentials
		// for related hosts. Video content redirects are followed explicitly.
		HTTPClient:        defaultOpenRouterHTTPClient,
		ContentHTTPClient: defaultOpenRouterContentClient,
		BaseURL:           openRouterBaseURL,
		promptState:       &randomPromptState{},
	}
}

func (c *OpenRouterClient) baseURL() string {
	if c.BaseURL != "" {
		return strings.TrimRight(c.BaseURL, "/")
	}
	return openRouterBaseURL
}

func (c *OpenRouterClient) client() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return defaultOpenRouterHTTPClient
}

func (c *OpenRouterClient) storyClient() *http.Client {
	if c.StoryHTTPClient != nil {
		return c.StoryHTTPClient
	}
	// HTTPClient is the established dependency-injection point across tests and
	// custom callers. Honor an injected replacement for story requests too.
	if c.HTTPClient != nil && c.HTTPClient != defaultOpenRouterHTTPClient {
		return c.HTTPClient
	}
	return defaultOpenRouterStoryHTTPClient
}

func (c *OpenRouterClient) contentClient() *http.Client {
	if c.ContentHTTPClient != nil {
		return c.ContentHTTPClient
	}
	return defaultOpenRouterContentClient
}

func (c *OpenRouterClient) GenerateVideo(ctx context.Context, request GenerateRequest) (*Generation, error) {
	var response openRouterGeneration
	if err := c.doJSON(ctx, http.MethodPost, "/videos", request, &response); err != nil {
		return nil, err
	}
	generation, err := c.normalizeGeneration(response)
	if err != nil {
		return nil, err
	}
	if generation.ID == "" {
		return nil, errors.New("OpenRouter returned a video job without an id")
	}
	if generation.Model == "" {
		generation.Model = request.Model
	}
	return generation, nil
}

func (c *OpenRouterClient) GetGeneration(ctx context.Context, id string) (*Generation, error) {
	if !validGenerationID(id) {
		return nil, errors.New("invalid video generation id")
	}
	var response openRouterGeneration
	if err := c.doJSON(ctx, http.MethodGet, "/videos/"+url.PathEscape(id), nil, &response); err != nil {
		return nil, err
	}
	generation, err := c.normalizeGeneration(response)
	if err != nil {
		return nil, err
	}
	if generation.ID == "" {
		return nil, errors.New("OpenRouter returned a video job without an id")
	}
	return generation, nil
}

func (c *OpenRouterClient) ListVideoModels(ctx context.Context) ([]VideoModel, error) {
	var response struct {
		Data []openRouterVideoModel `json:"data"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/videos/models", nil, &response); err != nil {
		return nil, err
	}

	models := make([]VideoModel, 0, len(response.Data))
	for _, model := range response.Data {
		pricePerSecond := lowestPerSecondPrice(model.PricingSKUs)
		pricePerGeneration := generationPrice(model.PricingSKUs)
		priceUnit := ""
		if pricePerSecond != "" {
			priceUnit = "second"
		} else if pricePerGeneration != "" {
			priceUnit = "generation"
		}
		models = append(models, VideoModel{
			ID: model.ID, Name: model.Name, Provider: providerFromModelID(model.ID),
			Durations: model.SupportedDurations, Resolutions: model.SupportedResolutions, AspectRatios: model.SupportedAspectRatios,
			Audio: model.GenerateAudio, PricingSKUs: model.PricingSKUs,
			PricePerSecond: pricePerSecond, PricePerGeneration: pricePerGeneration, PriceUnit: priceUnit,
		})
	}
	return models, nil
}

func (c *OpenRouterClient) GetVideoContent(ctx context.Context, id, rangeHeader string) (*http.Response, error) {
	if !validGenerationID(id) {
		return nil, errors.New("invalid video generation id")
	}
	contentURL := c.baseURL() + "/videos/" + url.PathEscape(id) + "/content?index=0"
	response, err := c.doContent(ctx, contentURL, rangeHeader, true)
	if err != nil {
		return nil, err
	}
	// Storage providers commonly redirect the authenticated OpenRouter content
	// route to a signed URL. Follow only without the authorization header.
	for redirects := 0; response.StatusCode >= 300 && response.StatusCode < 400 && redirects < 5; redirects++ {
		location := response.Header.Get("Location")
		response.Body.Close()
		if location == "" {
			return nil, errors.New("OpenRouter video content redirect had no location")
		}
		next, err := response.Request.URL.Parse(location)
		if err != nil {
			return nil, fmt.Errorf("invalid OpenRouter video content redirect: %w", err)
		}
		if next.Scheme != "https" {
			return nil, errors.New("OpenRouter video content redirect must use HTTPS")
		}
		response, err = c.doContent(ctx, next.String(), rangeHeader, false)
		if err != nil {
			return nil, err
		}
	}
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		response.Body.Close()
		return nil, errors.New("too many OpenRouter video content redirects")
	}
	// Preserve range semantics so browsers can recover from an out-of-range
	// seek using the upstream Content-Range header.
	if response.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		return response, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		defer response.Body.Close()
		return nil, readUpstreamError(response)
	}
	return response, nil
}

func (c *OpenRouterClient) doContent(ctx context.Context, endpoint, rangeHeader string, includeAuth bool) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	if includeAuth {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	return c.contentClient().Do(req)
}

func (c *OpenRouterClient) doJSON(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return fmt.Errorf("encode OpenRouter request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL()+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Accept", "application/json")
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	response, err := c.client().Do(req)
	if err != nil {
		return fmt.Errorf("OpenRouter request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return readUpstreamError(response)
	}
	if output == nil {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(output); err != nil {
		return fmt.Errorf("decode OpenRouter response: %w", err)
	}
	return nil
}

func readUpstreamError(response *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	return parseOpenRouterError(data, response.StatusCode, response.Header)
}

// Only selected error fields are retained. Provider metadata.raw and arbitrary
// response bodies may contain sensitive request details and must not become an
// error message. Successful responses can carry errors after HTTP 200 headers.
func parseOpenRouterError(data []byte, status int, headers http.Header) error {
	var envelope struct {
		Error   json.RawMessage `json:"error"`
		Message json.RawMessage `json:"message"`
	}
	_ = json.Unmarshal(data, &envelope)
	message := openRouterErrorString(envelope.Message)
	errorBody := bytes.TrimSpace(envelope.Error)
	hasError := len(errorBody) > 0 && !bytes.Equal(errorBody, []byte("null")) && !bytes.Equal(errorBody, []byte(`""`))
	if status >= 200 && status < 300 && !hasError {
		return nil
	}
	var detail map[string]json.RawMessage
	if hasError {
		_ = json.Unmarshal(errorBody, &detail)
		if detailMessage := openRouterErrorString(detail["message"]); detailMessage != "" {
			message = detailMessage
		}
		if message == "" {
			message = openRouterErrorString(errorBody)
		}
	}
	if status >= 200 && status < 300 {
		status = openRouterErrorStatus(detail["code"])
		if status == 0 {
			status = openRouterErrorStatus(detail["status"])
		}
		if status == 0 {
			status = http.StatusBadGateway
		}
	}
	if message == "" {
		message = http.StatusText(status)
	}
	var metadata map[string]json.RawMessage
	_ = json.Unmarshal(detail["metadata"], &metadata)
	metadataHeaders := openRouterErrorHeaders(metadata["headers"])
	upstream := &upstreamError{
		StatusCode:   status,
		Message:      message,
		LimitSource:  openRouterErrorString(metadata["limit_source"]),
		ProviderName: openRouterErrorString(metadata["provider_name"]),
		ErrorType:    openRouterErrorString(metadata["error_type"]),
		RetryAt:      openRouterErrorRetryAt(headers, metadataHeaders, time.Now()),
	}
	if status == http.StatusTooManyRequests {
		hasProviderCode := openRouterErrorString(metadata["provider_code"]) != "" || openRouterErrorStatus(metadata["provider_code"]) != 0
		upstream.RateLimitScope = openRouterErrorLimitScope(upstream, headers, metadataHeaders, hasProviderCode)
	}
	return upstream
}

func openRouterErrorString(raw json.RawMessage) string {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

func openRouterErrorStatus(raw json.RawMessage) int {
	value := openRouterErrorString(raw)
	if value == "" {
		value = string(raw)
	}
	number, err := strconv.ParseFloat(value, 64)
	if err != nil || !(number >= 400 && number <= 599) || number != float64(int(number)) {
		return 0
	}
	return int(number)
}

func openRouterErrorHeaders(raw json.RawMessage) http.Header {
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	headers := make(http.Header)
	for name, rawValue := range fields {
		switch strings.ToLower(name) {
		case "retry-after", "x-ratelimit-reset", "x-ratelimit-limit", "x-ratelimit-remaining":
			value := openRouterErrorString(rawValue)
			if value == "" {
				// Providers sometimes encode epoch timestamps as JSON numbers.
				var number json.Number
				if json.Unmarshal(rawValue, &number) == nil {
					value = number.String()
				}
			}
			if value != "" {
				headers.Add(name, value)
			}
		}
	}
	return headers
}

func openRouterHeaderValues(headers http.Header, name string) []string {
	var values []string
	for key, headerValues := range headers {
		if strings.EqualFold(key, name) {
			values = append(values, headerValues...)
		}
	}
	return values
}

func openRouterErrorRetryAt(headers, metadataHeaders http.Header, now time.Time) time.Time {
	var retryAt time.Time
	keepLater := func(candidate time.Time) {
		if candidate.After(retryAt) {
			retryAt = candidate
		}
	}
	for _, source := range []http.Header{headers, metadataHeaders} {
		for _, value := range openRouterHeaderValues(source, "Retry-After") {
			value = strings.TrimSpace(value)
			if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds >= 0 && seconds <= int64((1<<63-1)/time.Second) {
				keepLater(now.Add(time.Duration(seconds) * time.Second))
			} else if date, err := http.ParseTime(value); err == nil {
				keepLater(date)
			}
		}
		for _, value := range openRouterHeaderValues(source, "X-RateLimit-Reset") {
			keepLater(openRouterRateLimitReset(value))
		}
	}
	return retryAt
}

func openRouterRateLimitReset(value string) time.Time {
	value = strings.TrimSpace(value)
	seconds, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return time.Time{}
	}
	// OpenRouter uses Unix milliseconds. Accept plausible Unix seconds too,
	// without treating small delay values or overflowing numbers as epochs.
	if seconds >= 1e12 {
		seconds /= 1000
	}
	if !(seconds >= 946684800 && seconds <= 4102444800) { // 2000 through 2100
		return time.Time{}
	}
	if epoch, err := strconv.ParseInt(value, 10, 64); err == nil {
		if epoch >= 1e12 {
			return time.UnixMilli(epoch)
		}
		return time.Unix(epoch, 0)
	}
	wholeSeconds := int64(seconds)
	return time.Unix(wholeSeconds, int64((seconds-float64(wholeSeconds))*float64(time.Second)))
}

func openRouterErrorLimitScope(upstream *upstreamError, headers, metadataHeaders http.Header, hasProviderCode bool) string {
	limitSource := strings.ToLower(upstream.LimitSource)
	switch {
	case limitSource == "openrouter", limitSource == "platform", strings.HasPrefix(limitSource, "openrouter_"):
		return "platform"
	case limitSource == "provider", limitSource == "upstream", strings.HasPrefix(limitSource, "upstream_provider"), strings.HasPrefix(limitSource, "provider_"):
		return "provider"
	}
	hasPlatformHeaders := func(source http.Header) bool {
		for _, name := range []string{"X-RateLimit-Limit", "X-RateLimit-Remaining", "X-RateLimit-Reset"} {
			for _, value := range openRouterHeaderValues(source, name) {
				if name == "X-RateLimit-Reset" {
					if !openRouterRateLimitReset(value).IsZero() {
						return true
					}
				} else if number, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil && number >= 0 && number < 1e12 {
					return true
				}
			}
		}
		return false
	}
	if hasPlatformHeaders(headers) {
		return "platform"
	}
	// Older free-model errors name the shared platform quota only in message.
	message := strings.TrimSpace(strings.ToLower(upstream.Message))
	if strings.HasPrefix(message, "rate limit exceeded:") {
		quota := strings.TrimSpace(strings.TrimPrefix(message, "rate limit exceeded:"))
		for _, prefix := range []string{"free-models-per-min", "free-models-per-day"} {
			if strings.HasPrefix(quota, prefix) {
				suffix := strings.TrimPrefix(quota, prefix)
				if suffix == "" || strings.ContainsAny(suffix[:1], "-. :\t\r\n") {
					return "platform"
				}
			}
		}
	}
	if upstream.ProviderName != "" || hasProviderCode {
		return "provider"
	}
	if hasPlatformHeaders(metadataHeaders) {
		return "platform"
	}
	return ""
}

type openRouterGeneration struct {
	ID           string   `json:"id"`
	GenerationID string   `json:"generation_id"`
	Status       string   `json:"status"`
	Model        string   `json:"model"`
	Error        string   `json:"error"`
	Progress     *int     `json:"progress"`
	UnsignedURLs []string `json:"unsigned_urls"`
	Usage        struct {
		Cost json.Number `json:"cost"`
	} `json:"usage"`
}

type openRouterVideoModel struct {
	ID                    string            `json:"id"`
	Name                  string            `json:"name"`
	SupportedDurations    []int             `json:"supported_durations"`
	SupportedResolutions  []string          `json:"supported_resolutions"`
	SupportedAspectRatios []string          `json:"supported_aspect_ratios"`
	GenerateAudio         *bool             `json:"generate_audio"`
	PricingSKUs           map[string]string `json:"pricing_skus"`
}

func (c *OpenRouterClient) normalizeGeneration(source openRouterGeneration) (*Generation, error) {
	return normalizeVideoGeneration(source, "OpenRouter")
}

func normalizeVideoGeneration(source openRouterGeneration, provider string) (*Generation, error) {
	status := source.Status
	switch status {
	case "pending":
		status = "queued"
	case "in_progress":
		status = "processing"
	case "cancelled", "expired":
		status = "failed"
		if source.Error == "" {
			source.Error = "generation " + source.Status
		}
	case "completed", "failed", "queued", "processing":
	default:
		return nil, fmt.Errorf("%s returned an unsupported video status %q", provider, source.Status)
	}
	generation := &Generation{ID: source.ID, Status: status, Model: source.Model, Progress: source.Progress, Error: source.Error, CostUSD: string(source.Usage.Cost)}
	if generation.Status == "completed" && generation.ID != "" {
		generation.OutputURL = "/video?id=" + url.QueryEscape(generation.ID)
	}
	return generation, nil
}

func lowestPerSecondPrice(skus map[string]string) string {
	var lowest string
	var lowestValue float64
	for name, price := range skus {
		if !strings.HasPrefix(name, "per-video-second") {
			continue
		}
		value, err := strconv.ParseFloat(price, 64)
		if err != nil || value < 0 {
			continue
		}
		if lowest == "" || value < lowestValue {
			lowest, lowestValue = price, value
		}
	}
	return lowest
}

func generationPrice(skus map[string]string) string {
	price, found := skus["generate"]
	if !found {
		return ""
	}
	value, err := strconv.ParseFloat(price, 64)
	if err != nil || value < 0 {
		return ""
	}
	return price
}

func providerFromModelID(id string) string {
	provider, _, found := strings.Cut(id, "/")
	if found {
		return provider
	}
	return ""
}

func validGenerationID(id string) bool {
	if len(id) == 0 || len(id) > 200 {
		return false
	}
	for _, char := range id {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '-' || char == '_') {
			return false
		}
	}
	return true
}
