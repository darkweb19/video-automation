package app

import (
	"errors"
	"fmt"
)

const (
	// Gemini options use source-time milliseconds, rather than the seconds
	// accepted by the existing clipping UI.
	ClippingGeminiMinClipMS     int64 = 15_000
	ClippingGeminiMaxClipMS     int64 = 180_000
	ClippingGeminiMaxCandidates       = 10
)

var ErrClippingGeminiInvalidOptions = errors.New("invalid Gemini clipping options")

// ClippingGeminiRequest identifies a public YouTube video and requests an
// editorial analysis. The provider adapter is responsible for URL validation.
type ClippingGeminiRequest struct {
	YouTubeURL string                `json:"youtube_url"`
	Options    ClippingGeminiOptions `json:"options"`
}

// ClippingGeminiOptions specifies limits for model-selected clips. The
// candidate limit is a maximum; the model may return fewer candidates.
type ClippingGeminiOptions struct {
	ContentType    string `json:"content_type"`
	MinClipMS      int64  `json:"min_clip_ms"`
	MaxClipMS      int64  `json:"max_clip_ms"`
	CandidateLimit int    `json:"candidate_limit"`
}

// ValidateClippingGeminiOptions rejects invalid input without filling defaults
// or changing it. It is separate from the legacy UI's 7–10 candidate policy.
func ValidateClippingGeminiOptions(options ClippingGeminiOptions) error {
	if !validClippingContentType(options.ContentType) {
		return fmt.Errorf("%w: unsupported content_type", ErrClippingGeminiInvalidOptions)
	}
	if options.MinClipMS < ClippingGeminiMinClipMS || options.MaxClipMS > ClippingGeminiMaxClipMS || options.MinClipMS > options.MaxClipMS {
		return fmt.Errorf("%w: clip duration bounds must be within 15000–180000 ms", ErrClippingGeminiInvalidOptions)
	}
	if options.CandidateLimit < 1 || options.CandidateLimit > ClippingGeminiMaxCandidates {
		return fmt.Errorf("%w: candidate_limit must be between 1 and 10", ErrClippingGeminiInvalidOptions)
	}
	return nil
}
