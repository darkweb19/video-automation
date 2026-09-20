package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testClient(server *httptest.Server) *OpenRouterClient {
	return &OpenRouterClient{APIKey: "test-key", HTTPClient: server.Client(), BaseURL: server.URL}
}

func TestOpenRouterListVideoModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/videos/models" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("authorization = %q", got)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"google/veo-3.1","name":"Veo 3.1","supported_durations":[4,8],"supported_aspect_ratios":["9:16"],"generate_audio":true,"pricing_skus":{"per-video-second":"0.50","per-video-second-1080p":"0.75"}}]}`))
	}))
	defer server.Close()

	models, err := testClient(server).ListVideoModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].Provider != "google" || len(models[0].Durations) != 2 || models[0].Audio == nil || !*models[0].Audio || models[0].PricePerSecond != "0.50" {
		t.Fatalf("unexpected models: %#v", models)
	}
}

func TestOpenRouterGenerationNormalization(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/videos" {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"id":"job_123","status":"pending","usage":{"cost":0.42}}`))
	}))
	defer server.Close()

	generation, err := testClient(server).GenerateVideo(context.Background(), GenerateRequest{Prompt: "test", Model: "google/veo-3.1"})
	if err != nil {
		t.Fatal(err)
	}
	if generation.ID != "job_123" || generation.Status != "queued" || generation.Model != "google/veo-3.1" || generation.CostUSD != "0.42" {
		t.Fatalf("unexpected generation: %#v", generation)
	}
}

func TestOpenRouterRejectsUnknownStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"job_123","status":"mystery"}`))
	}))
	defer server.Close()
	_, err := testClient(server).GetGeneration(context.Background(), "job_123")
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("error = %v", err)
	}
}

func TestOpenRouterHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"slow down"}}`))
	}))
	defer server.Close()
	_, err := testClient(server).ListVideoModels(context.Background())
	var upstream *upstreamError
	if !errors.As(err, &upstream) || upstream.StatusCode != http.StatusTooManyRequests || !strings.Contains(err.Error(), "slow down") {
		t.Fatalf("error = %#v", err)
	}
}

func TestOpenRouterRequestHonorsContextTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	context, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := testClient(server).ListVideoModels(context)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestOpenRouterContentRedirectDropsAuthorization(t *testing.T) {
	storage := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("redirect authorization = %q", got)
		}
		if got := r.Header.Get("Range"); got != "bytes=0-10" {
			t.Errorf("range = %q", got)
		}
		_, _ = w.Write([]byte("video"))
	}))
	defer storage.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, storage.URL+"/video", http.StatusFound)
	}))
	defer origin.Close()
	client := testClient(origin)
	client.ContentHTTPClient = storage.Client()
	client.ContentHTTPClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.GetVideoContent(context.Background(), "job_123", "bytes=0-10")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
}

func TestOpenRouterContentPreservesRangeNotSatisfiable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Range", "bytes */5")
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
	}))
	defer server.Close()

	client := testClient(server)
	client.ContentHTTPClient = server.Client()
	response, err := client.GetVideoContent(context.Background(), "job_123", "bytes=10-20")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusRequestedRangeNotSatisfiable || response.Header.Get("Content-Range") != "bytes */5" {
		t.Fatalf("range response = %d, headers = %#v", response.StatusCode, response.Header)
	}
}
