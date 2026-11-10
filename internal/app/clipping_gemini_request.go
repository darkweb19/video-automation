package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"google.golang.org/genai"
)

const clippingGeminiFingerprintVersion = "framevault.clipping.gemini.request.v1"

var ErrClippingGeminiInvalidRequest = errors.New("invalid Gemini clipping request")

type clippingGeminiPreparedRequest struct {
	Model               string
	CanonicalYouTubeURL string
	Content             *genai.Content
	GenerationConfig    *genai.GenerateContentConfig
	PromptVersion       string
	PromptText          string
	PromptSHA256        string
	SchemaVersion       string
	Schema              *genai.Schema
	SchemaSHA256        string
	Fingerprint         string
}

type clippingGeminiFingerprintMaterial struct {
	FingerprintVersion    string                `json:"fingerprint_version"`
	Backend               string                `json:"backend"`
	ProjectID             string                `json:"project_id"`
	Location              string                `json:"location"`
	YouTubeURL            string                `json:"youtube_url"`
	Model                 string                `json:"model"`
	APIVersion            string                `json:"api_version"`
	GenerationMode        string                `json:"generation_mode"`
	VideoProcessingMode   string                `json:"video_processing_mode"`
	InputMIMEType         string                `json:"input_mime_type"`
	ResponseMIMEType      string                `json:"response_mime_type"`
	OneCandidateRequired  bool                  `json:"one_candidate_required"`
	AutomaticContinuation bool                  `json:"automatic_continuation"`
	MaxOutputTokens       int32                 `json:"max_output_tokens"`
	PromptVersion         string                `json:"prompt_version"`
	PromptSHA256          string                `json:"prompt_sha256"`
	SchemaVersion         string                `json:"schema_version"`
	SchemaSHA256          string                `json:"schema_sha256"`
	Options               ClippingGeminiOptions `json:"options"`
}

func clippingGeminiRequestFingerprint(config ClippingGeminiConfig, request ClippingGeminiRequest) (string, error) {
	prepared, err := prepareClippingGeminiRequest(config, request)
	if err != nil {
		return "", err
	}
	return prepared.Fingerprint, nil
}

func prepareClippingGeminiRequest(config ClippingGeminiConfig, request ClippingGeminiRequest) (*clippingGeminiPreparedRequest, error) {
	if err := ValidateClippingGeminiConfig(config); err != nil {
		return nil, err
	}
	if err := ValidateClippingGeminiOptions(request.Options); err != nil {
		return nil, err
	}
	canonicalURL, err := canonicalClippingGeminiYouTubeURL(request.YouTubeURL)
	if err != nil {
		return nil, err
	}
	promptVersion, prompt, err := clippingGeminiPromptSnapshot(request.Options)
	if err != nil {
		return nil, err
	}
	schema, err := clippingGeminiResponseSchema(request.Options)
	if err != nil {
		return nil, err
	}
	promptDigest := sha256.Sum256([]byte(prompt))
	schemaJSON, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("%w: response schema could not be serialized", ErrClippingGeminiInvalidRequest)
	}
	schemaDigest := sha256.Sum256(schemaJSON)
	const automaticContinuation = false
	material := clippingGeminiFingerprintMaterial{
		FingerprintVersion:    clippingGeminiFingerprintVersion,
		Backend:               "gemini_enterprise_agent_platform",
		ProjectID:             config.ProjectID,
		Location:              config.Location,
		YouTubeURL:            canonicalURL,
		Model:                 clippingGeminiModel,
		APIVersion:            clippingGeminiAPIVersion,
		GenerationMode:        clippingGeminiGenerationMode,
		VideoProcessingMode:   clippingGeminiVideoProcessingMode,
		InputMIMEType:         clippingGeminiInputMIMEType,
		ResponseMIMEType:      clippingGeminiResponseMIMEType,
		OneCandidateRequired:  true,
		AutomaticContinuation: automaticContinuation,
		MaxOutputTokens:       clippingGeminiMaxOutputTokens,
		PromptVersion:         promptVersion,
		PromptSHA256:          hex.EncodeToString(promptDigest[:]),
		SchemaVersion:         ClippingGeminiAnalysisVersion,
		SchemaSHA256:          hex.EncodeToString(schemaDigest[:]),
		Options:               request.Options,
	}
	fingerprintJSON, err := json.Marshal(material)
	if err != nil {
		return nil, fmt.Errorf("%w: request fingerprint could not be serialized", ErrClippingGeminiInvalidRequest)
	}
	fingerprintDigest := sha256.Sum256(fingerprintJSON)
	content := genai.NewContentFromParts([]*genai.Part{
		{
			FileData:        &genai.FileData{FileURI: canonicalURL, MIMEType: clippingGeminiInputMIMEType},
			MediaProcessing: genai.MediaProcessingStatic,
		},
		{Text: prompt},
	}, genai.RoleUser)
	return &clippingGeminiPreparedRequest{
		Model:               clippingGeminiModel,
		CanonicalYouTubeURL: canonicalURL,
		Content:             content,
		GenerationConfig: &genai.GenerateContentConfig{
			AutomaticContinuation: genai.Ptr(automaticContinuation),
			MaxOutputTokens:       clippingGeminiMaxOutputTokens,
			ResponseMIMEType:      clippingGeminiResponseMIMEType,
			ResponseSchema:        schema,
		},
		PromptVersion: promptVersion,
		PromptText:    prompt,
		PromptSHA256:  hex.EncodeToString(promptDigest[:]),
		SchemaVersion: ClippingGeminiAnalysisVersion,
		Schema:        schema,
		SchemaSHA256:  hex.EncodeToString(schemaDigest[:]),
		Fingerprint:   hex.EncodeToString(fingerprintDigest[:]),
	}, nil
}

func canonicalClippingGeminiYouTubeURL(raw string) (string, error) {
	canonicalURL, kind, err := normalizeClippingImportURL(raw)
	if err != nil || canonicalURL == nil || kind != ClippingSourceYouTube {
		return "", fmt.Errorf("%w: youtube_url must be a supported public YouTube video link", ErrClippingGeminiInvalidRequest)
	}
	return canonicalURL.String(), nil
}
