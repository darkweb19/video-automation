package app

import (
	"errors"
	"os"
)

const (
	clippingGeminiEnabledEnv  = "CLIPPING_GEMINI_ENABLED"
	clippingGeminiProjectEnv  = "CLIPPING_GEMINI_PROJECT_ID"
	clippingGeminiLocationEnv = "CLIPPING_GEMINI_LOCATION"
)

var errClippingGeminiRuntimeConfig = errors.New("invalid Gemini clipping runtime configuration")

// clippingGeminiRuntimeConfig is the process-level opt-in for executing
// persisted Gemini jobs. Routing values are non-secret and deliberately kept
// separate from the durable per-job request snapshot.
type clippingGeminiRuntimeConfig struct {
	Enabled bool
	Routing ClippingGeminiConfig
}

// loadClippingGeminiRuntimeConfig is inert unless the operator explicitly sets
// CLIPPING_GEMINI_ENABLED=true. Project and location are required only for an
// enabled runtime. It neither creates an SDK client nor checks ADC, so reading
// configuration cannot make a provider call.
func loadClippingGeminiRuntimeConfig(getenv func(string) string) (clippingGeminiRuntimeConfig, error) {
	if getenv == nil {
		return clippingGeminiRuntimeConfig{}, errClippingGeminiRuntimeConfig
	}
	if !clippingGeminiRuntimeEnabled(getenv(clippingGeminiEnabledEnv)) {
		return clippingGeminiRuntimeConfig{}, nil
	}
	routing := ClippingGeminiConfig{
		ProjectID: getenv(clippingGeminiProjectEnv),
		Location:  getenv(clippingGeminiLocationEnv),
	}
	if ValidateClippingGeminiConfig(routing) != nil {
		return clippingGeminiRuntimeConfig{}, errClippingGeminiRuntimeConfig
	}
	return clippingGeminiRuntimeConfig{Enabled: true, Routing: routing}, nil
}

func clippingGeminiRuntimeEnabled(value string) bool {
	return value == "true"
}

func currentClippingGeminiRuntimeConfig() (clippingGeminiRuntimeConfig, error) {
	return loadClippingGeminiRuntimeConfig(os.Getenv)
}
