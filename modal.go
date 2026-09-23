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

var defaultModalHTTPClient = &http.Client{Timeout: 45 * time.Second}
var defaultModalContentClient = &http.Client{
	Transport: func() http.RoundTripper {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.ResponseHeaderTimeout = 45 * time.Second
		return transport
	}(),
}

// ModalVideoClient implements the same OpenRouter-style API exposed by the
// checked-in Modal service. Its base URL is intentionally runtime-configured.
type ModalVideoClient struct {
	APIKey            string
	BaseURL           string
	HTTPClient        *http.Client
	ContentHTTPClient *http.Client
}

func NewModalVideoClient(baseURL, apiKey string) *ModalVideoClient {
	return &ModalVideoClient{BaseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"), APIKey: apiKey, HTTPClient: defaultModalHTTPClient}
}

func validateModalBaseURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("Modal base URL must be an HTTPS URL without query or fragment")
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func (c *ModalVideoClient) client() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return defaultModalHTTPClient
}

func (c *ModalVideoClient) contentClient() *http.Client {
	if c.ContentHTTPClient != nil {
		return c.ContentHTTPClient
	}
	return defaultModalContentClient
}

func (c *ModalVideoClient) doJSON(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return fmt.Errorf("encode Modal request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, body)
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
		return fmt.Errorf("Modal request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return readUpstreamError(response)
	}
	if output == nil {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(output); err != nil {
		return fmt.Errorf("decode Modal response: %w", err)
	}
	return nil
}

func (c *ModalVideoClient) GenerateVideo(ctx context.Context, request GenerateRequest) (*Generation, error) {
	var source openRouterGeneration
	if err := c.doJSON(ctx, http.MethodPost, "/videos", request, &source); err != nil {
		return nil, err
	}
	generation, err := normalizeVideoGeneration(source, "Modal")
	if err != nil {
		return nil, err
	}
	if generation.ID == "" {
		return nil, errors.New("Modal returned a video job without an id")
	}
	if generation.Model == "" {
		generation.Model = request.Model
	}
	return generation, nil
}

func (c *ModalVideoClient) GetGeneration(ctx context.Context, id string) (*Generation, error) {
	if !validGenerationID(id) {
		return nil, errors.New("invalid video generation id")
	}
	var source openRouterGeneration
	if err := c.doJSON(ctx, http.MethodGet, "/videos/"+url.PathEscape(id), nil, &source); err != nil {
		return nil, err
	}
	generation, err := normalizeVideoGeneration(source, "Modal")
	if err != nil {
		return nil, err
	}
	if generation.ID == "" {
		return nil, errors.New("Modal returned a video job without an id")
	}
	return generation, nil
}

func (c *ModalVideoClient) ListVideoModels(ctx context.Context) ([]VideoModel, error) {
	var response struct {
		Data []openRouterVideoModel `json:"data"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/videos/models", nil, &response); err != nil {
		return nil, err
	}
	models := make([]VideoModel, 0, len(response.Data))
	for _, model := range response.Data {
		models = append(models, VideoModel{ID: model.ID, Name: model.Name, Provider: string(VideoProviderModal), Durations: model.SupportedDurations, Resolutions: model.SupportedResolutions, AspectRatios: model.SupportedAspectRatios, Audio: model.GenerateAudio, PricingSKUs: model.PricingSKUs})
	}
	return models, nil
}

func (c *ModalVideoClient) GetVideoContent(ctx context.Context, id, rangeHeader string) (*http.Response, error) {
	if !validGenerationID(id) {
		return nil, errors.New("invalid video generation id")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.BaseURL, "/")+"/videos/"+url.PathEscape(id)+"/content?index=0", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	response, err := c.contentClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("Modal content request: %w", err)
	}
	if response.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		return response, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		defer response.Body.Close()
		return nil, readUpstreamError(response)
	}
	return response, nil
}
