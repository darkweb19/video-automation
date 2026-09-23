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
	Prompt        string `json:"prompt"`
	Model         string `json:"model"`
	Duration      int    `json:"duration,omitempty"`
	Resolution    string `json:"resolution,omitempty"`
	AspectRatio   string `json:"aspect_ratio,omitempty"`
	GenerateAudio *bool  `json:"generate_audio,omitempty"`
}

// Generation is the normalized state returned to the browser.
type Generation struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	Model     string `json:"model,omitempty"`
	Progress  *int   `json:"progress,omitempty"`
	OutputURL string `json:"output_url,omitempty"`
	Error     string `json:"error,omitempty"`
	CostUSD   string `json:"cost_usd,omitempty"`
}

// VideoModel describes the capabilities OpenRouter exposes for a video model.
type VideoModel struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Provider       string            `json:"provider,omitempty"`
	Durations      []int             `json:"durations,omitempty"`
	Resolutions    []string          `json:"resolutions,omitempty"`
	AspectRatios   []string          `json:"aspect_ratios,omitempty"`
	Audio          *bool             `json:"audio,omitempty"`
	PricingSKUs    map[string]string `json:"pricing_skus,omitempty"`
	PricePerSecond string            `json:"price_per_second,omitempty"`
	// PricePerGeneration is populated only for OpenRouter's fixed "generate" SKU.
	// It must not be multiplied by the requested duration.
	PricePerGeneration string `json:"price_per_generation,omitempty"`
	PriceUnit          string `json:"price_unit,omitempty"`
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

// VideoService is the complete provider-neutral contract consumed by the
// dashboard and durable processor.
type VideoService interface {
	VideoProvider
	VideoContentProvider
}

type VideoProviderID string

const (
	VideoProviderOpenRouter VideoProviderID = "openrouter"
	VideoProviderModal      VideoProviderID = "modal"
)

func validVideoProvider(id VideoProviderID) bool {
	return id == VideoProviderOpenRouter || id == VideoProviderModal
}

const (
	ProjectSceneCount   = 5
	ProjectSceneSeconds = 6
	ProjectAspectRatio  = "9:16"
	ScriptModel         = "openrouter/free"
	ProjectResolution   = "480p"
)

type ProjectRequest struct {
	Topic string `json:"topic"`
	Model string `json:"model"`
}

type StoryPlan struct {
	Title      string           `json:"title"`
	Story      string           `json:"story"`
	Script     string           `json:"script"`
	Continuity string           `json:"continuity"`
	Scenes     []StoryPlanScene `json:"scenes"`
}

type StoryPlanScene struct {
	Number      int    `json:"number"`
	Title       string `json:"title"`
	Script      string `json:"script"`
	VideoPrompt string `json:"video_prompt"`
}

type ProjectScene struct {
	ProjectID            string `json:"project_id,omitempty"`
	Number               int    `json:"number"`
	Title                string `json:"title"`
	Script               string `json:"script"`
	Prompt               string `json:"prompt"`
	Status               string `json:"status"`
	Progress             int    `json:"progress"`
	Attempts             int    `json:"attempts"`
	ProviderGenerationID string `json:"provider_generation_id,omitempty"`
	CostUSD              string `json:"cost_usd,omitempty"`
	VideoPath            string `json:"-"`
	VideoReady           bool   `json:"video_ready"`
	SizeBytes            int64  `json:"size_bytes,omitempty"`
	Error                string `json:"error,omitempty"`
	DownloadAttempts     int    `json:"download_attempts,omitempty"`
	NextAttemptAt        int64  `json:"next_attempt_at,omitempty"`
	CreatedAt            int64  `json:"created_at"`
	UpdatedAt            int64  `json:"updated_at"`
}

type VideoProject struct {
	ID              string         `json:"id"`
	Topic           string         `json:"topic"`
	Title           string         `json:"title,omitempty"`
	Story           string         `json:"story,omitempty"`
	Script          string         `json:"script,omitempty"`
	Continuity      string         `json:"continuity,omitempty"`
	Model           string         `json:"model"`
	VideoProvider   string         `json:"video_provider"`
	Status          string         `json:"status"`
	Progress        int            `json:"progress"`
	Error           string         `json:"error,omitempty"`
	FinalVideoPath  string         `json:"-"`
	FinalVideoReady bool           `json:"final_video_ready"`
	FinalSizeBytes  int64          `json:"final_size_bytes,omitempty"`
	TotalCostUSD    string         `json:"total_cost_usd,omitempty"`
	Scenes          []ProjectScene `json:"scenes"`
	// TextGeneration contains auditable request/response artifacts for the
	// script planner. It never includes credentials or provider headers.
	TextGeneration TextGenerationTrace `json:"text_generation"`
	// PipelineEvents is ordered oldest first and intentionally records state
	// transitions rather than every polling loop.
	PipelineEvents []PipelineEvent `json:"pipeline_events"`
	CreatedAt      int64           `json:"created_at"`
	UpdatedAt      int64           `json:"updated_at"`
}

// TextGenerationTrace is the durable, user-visible record of a structured
// script-generation request. RawResponse is the assistant content exactly as
// received, subject to MaxScriptRawResponseBytes.
type TextGenerationTrace struct {
	RouterModel    string `json:"router_model"`
	ActualModel    string `json:"actual_model,omitempty"`
	SystemPrompt   string `json:"system_prompt"`
	UserPrompt     string `json:"user_prompt"`
	ResponseSchema string `json:"response_schema"`
	RawResponse    string `json:"raw_response,omitempty"`
	Status         string `json:"status"`
	Error          string `json:"error,omitempty"`
	StartedAt      int64  `json:"started_at,omitempty"`
	CompletedAt    int64  `json:"completed_at,omitempty"`
	UpdatedAt      int64  `json:"updated_at"`
}

// PipelineEvent records an auditable workflow transition. SceneNumber and
// Attempt are omitted for project-wide steps.
type PipelineEvent struct {
	ID          int64  `json:"id"`
	Stage       string `json:"stage"`
	Status      string `json:"status"`
	Message     string `json:"message"`
	SceneNumber int    `json:"scene_number,omitempty"`
	Attempt     int    `json:"attempt,omitempty"`
	CreatedAt   int64  `json:"created_at"`
}
