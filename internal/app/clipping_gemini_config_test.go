package app

import (
	"errors"
	"testing"

	"google.golang.org/genai"
)

func TestClippingGeminiConfigUsesFixedADCCloudBackend(t *testing.T) {
	tests := []struct {
		location string
		baseURL  string
	}{
		{location: "global", baseURL: "https://aiplatform.googleapis.com/"},
		{location: "us", baseURL: "https://aiplatform.us.rep.googleapis.com/"},
		{location: "eu", baseURL: "https://aiplatform.eu.rep.googleapis.com/"},
	}
	for _, tt := range tests {
		t.Run(tt.location, func(t *testing.T) {
			config, err := clippingGeminiSDKConfig(ClippingGeminiConfig{
				ProjectID: "framevault-test-123",
				Location:  tt.location,
			})
			if err != nil {
				t.Fatalf("clippingGeminiSDKConfig() error = %v", err)
			}
			if config.Backend != genai.BackendEnterprise {
				t.Errorf("Backend = %v, want BackendEnterprise", config.Backend)
			}
			if config.Project != "framevault-test-123" || config.Location != tt.location {
				t.Errorf("project/location = %q/%q, want framevault-test-123/%q", config.Project, config.Location, tt.location)
			}
			if config.APIKey != "" || config.Credentials != nil || config.HTTPClient != nil {
				t.Errorf("SDK config must leave API key, credentials, and client unset for ADC construction; got apiKeySet=%t credentialsSet=%t clientSet=%t", config.APIKey != "", config.Credentials != nil, config.HTTPClient != nil)
			}
			if config.HTTPOptions.BaseURL != tt.baseURL {
				t.Errorf("BaseURL = %q, want %q", config.HTTPOptions.BaseURL, tt.baseURL)
			}
			if config.HTTPOptions.APIVersion != "v1" {
				t.Errorf("APIVersion = %q, want v1", config.HTTPOptions.APIVersion)
			}
			if config.HTTPOptions.RetryOptions == nil || config.HTTPOptions.RetryOptions.Attempts == nil || *config.HTTPOptions.RetryOptions.Attempts != 1 {
				t.Errorf("RetryOptions.Attempts must be explicitly set to one")
			}
		})
	}
}

func TestValidateClippingGeminiConfigRejectsUnboundedValues(t *testing.T) {
	tests := []struct {
		name   string
		config ClippingGeminiConfig
	}{
		{name: "empty project", config: ClippingGeminiConfig{Location: "global"}},
		{name: "short project", config: ClippingGeminiConfig{ProjectID: "short", Location: "global"}},
		{name: "uppercase project", config: ClippingGeminiConfig{ProjectID: "FrameVault-123", Location: "global"}},
		{name: "path in project", config: ClippingGeminiConfig{ProjectID: "project/other", Location: "global"}},
		{name: "empty location", config: ClippingGeminiConfig{ProjectID: "framevault-123"}},
		{name: "arbitrary regional host", config: ClippingGeminiConfig{ProjectID: "framevault-123", Location: "https://attacker.example"}},
		{name: "unknown region", config: ClippingGeminiConfig{ProjectID: "framevault-123", Location: "us-central1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateClippingGeminiConfig(tt.config)
			if !errors.Is(err, ErrClippingGeminiInvalidConfig) {
				t.Errorf("ValidateClippingGeminiConfig() error = %v, want ErrClippingGeminiInvalidConfig", err)
			}
		})
	}
}

func TestValidateClippingGeminiConfigAcceptsFixedCloudLocations(t *testing.T) {
	for _, location := range []string{"global", "us", "eu"} {
		if err := ValidateClippingGeminiConfig(ClippingGeminiConfig{ProjectID: "framevault-123", Location: location}); err != nil {
			t.Errorf("ValidateClippingGeminiConfig(%q) error = %v", location, err)
		}
	}
}
