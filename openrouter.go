package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	ContentHTTPClient *http.Client
	BaseURL           string
}

type upstreamError struct {
	StatusCode int
	Message    string
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
		models = append(models, VideoModel{
			ID: model.ID, Name: model.Name, Provider: providerFromModelID(model.ID),
			Durations: model.SupportedDurations, AspectRatios: model.SupportedAspectRatios,
			Audio: model.GenerateAudio,
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
	var envelope struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
	}
	_ = json.Unmarshal(data, &envelope)
	message := envelope.Message
	if len(envelope.Error) > 0 {
		var detail struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(envelope.Error, &detail) == nil && detail.Message != "" {
			message = detail.Message
		}
		if message == "" {
			_ = json.Unmarshal(envelope.Error, &message)
		}
	}
	if message == "" {
		message = http.StatusText(response.StatusCode)
	}
	return &upstreamError{StatusCode: response.StatusCode, Message: message}
}

type openRouterGeneration struct {
	ID           string   `json:"id"`
	GenerationID string   `json:"generation_id"`
	Status       string   `json:"status"`
	Model        string   `json:"model"`
	Error        string   `json:"error"`
	UnsignedURLs []string `json:"unsigned_urls"`
}

type openRouterVideoModel struct {
	ID                    string   `json:"id"`
	Name                  string   `json:"name"`
	SupportedDurations    []int    `json:"supported_durations"`
	SupportedAspectRatios []string `json:"supported_aspect_ratios"`
	GenerateAudio         *bool    `json:"generate_audio"`
}

func (c *OpenRouterClient) normalizeGeneration(source openRouterGeneration) (*Generation, error) {
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
		return nil, fmt.Errorf("OpenRouter returned an unsupported video status %q", source.Status)
	}
	generation := &Generation{ID: source.ID, Status: status, Model: source.Model, Error: source.Error}
	if generation.Status == "completed" && generation.ID != "" {
		generation.OutputURL = "/video?id=" + url.QueryEscape(generation.ID)
	}
	return generation, nil
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
