package app

import (
	"errors"
	"fmt"

	"google.golang.org/genai"
)

const (
	clippingGeminiModel                  = "gemini-3.8-flash"
	clippingGeminiDefaultLocation        = "global"
	clippingGeminiMaxRequestDurationMS   = int64(40 * 60 * 1000)
	clippingGeminiMaxProviderResponseLen = 1 << 20
)

var ErrClippingGeminiInvalidConfig = errors.New("invalid Gemini clipping configuration")

// ClippingGeminiConfig contains non-secret routing values for the isolated
// feasibility client. It deliberately has no API-key, credential, or endpoint
// fields; the client uses Application Default Credentials and a fixed Google
// Cloud endpoint derived from Location.
type ClippingGeminiConfig struct {
	ProjectID string
	Location  string
}

// ValidateClippingGeminiConfig rejects values that could select another
// provider, endpoint, or project path. Values are not trimmed or repaired.
func ValidateClippingGeminiConfig(config ClippingGeminiConfig) error {
	if !validClippingGeminiProjectID(config.ProjectID) {
		return fmt.Errorf("%w: project_id must be a 6–30 character lowercase Google Cloud project ID", ErrClippingGeminiInvalidConfig)
	}
	if _, ok := clippingGeminiEndpoint(config.Location); !ok {
		return fmt.Errorf("%w: location must be global, us, or eu", ErrClippingGeminiInvalidConfig)
	}
	return nil
}

func validClippingGeminiProjectID(projectID string) bool {
	if len(projectID) < 6 || len(projectID) > 30 || projectID[0] < 'a' || projectID[0] > 'z' {
		return false
	}
	last := projectID[len(projectID)-1]
	if (last < 'a' || last > 'z') && (last < '0' || last > '9') {
		return false
	}
	for i := 1; i < len(projectID)-1; i++ {
		c := projectID[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

func clippingGeminiEndpoint(location string) (string, bool) {
	switch location {
	case "global":
		return "https://aiplatform.googleapis.com/", true
	case "us":
		return "https://aiplatform.us.rep.googleapis.com/", true
	case "eu":
		return "https://aiplatform.eu.rep.googleapis.com/", true
	default:
		return "", false
	}
}

func clippingGeminiSDKConfig(config ClippingGeminiConfig) (*genai.ClientConfig, error) {
	if err := ValidateClippingGeminiConfig(config); err != nil {
		return nil, err
	}
	baseURL, _ := clippingGeminiEndpoint(config.Location)
	return &genai.ClientConfig{
		Backend:  genai.BackendEnterprise,
		Project:  config.ProjectID,
		Location: config.Location,
		HTTPOptions: genai.HTTPOptions{
			BaseURL:    baseURL,
			APIVersion: "v1",
			RetryOptions: &genai.HTTPRetryOptions{
				Attempts: genai.Ptr[int32](1),
			},
		},
	}, nil
}
