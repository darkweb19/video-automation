package main

import (
	"context"
	"net/http"
)

const (
	MaxPromptLength  = 4000
	MaxJSONBodyBytes = 32 << 10
)

// GenerateRequest is the application's provider-independent video request.
type GenerateRequest struct {
	Prompt      string `json:"prompt"`
	Model       string `json:"model"`
	Duration    int    `json:"duration,omitempty"`
	AspectRatio string `json:"aspect_ratio,omitempty"`
}

// Generation is the normalized state returned to the browser.
type Generation struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	Model     string `json:"model,omitempty"`
	Progress  *int   `json:"progress,omitempty"`
	OutputURL string `json:"output_url,omitempty"`
	Error     string `json:"error,omitempty"`
}

// VideoModel describes the capabilities OpenRouter exposes for a video model.
type VideoModel struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Provider     string   `json:"provider,omitempty"`
	Durations    []int    `json:"durations,omitempty"`
	AspectRatios []string `json:"aspect_ratios,omitempty"`
	Audio        *bool    `json:"audio,omitempty"`
}

type VideoProvider interface {
	GenerateVideo(context.Context, GenerateRequest) (*Generation, error)
	GetGeneration(context.Context, string) (*Generation, error)
	ListVideoModels(context.Context) ([]VideoModel, error)
}

// VideoContentProvider is implemented when video bytes can be proxied to a
// browser without exposing the upstream API credential.
type VideoContentProvider interface {
	GetVideoContent(context.Context, string, string) (*http.Response, error)
}
