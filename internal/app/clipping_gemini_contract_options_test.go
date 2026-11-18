package app

import (
	"errors"
	"testing"
)

func TestValidateClippingGeminiOptions(t *testing.T) {
	valid := ClippingGeminiOptions{ContentType: "general", MinClipMS: 15_000, MaxClipMS: 180_000, CandidateLimit: 10}
	for _, contentType := range []string{"general", "podcast", "comedy", "gaming", "movie"} {
		options := valid
		options.ContentType = contentType
		if err := ValidateClippingGeminiOptions(options); err != nil {
			t.Fatalf("valid content type %q: %v", contentType, err)
		}
	}
	minimal := ClippingGeminiOptions{ContentType: "general", MinClipMS: 15_000, MaxClipMS: 15_000, CandidateLimit: 1}
	if err := ValidateClippingGeminiOptions(minimal); err != nil {
		t.Fatalf("valid minimum and fixed duration: %v", err)
	}
	for _, tc := range []struct {
		name   string
		change func(*ClippingGeminiOptions)
	}{
		{"empty profile is not defaulted", func(o *ClippingGeminiOptions) { o.ContentType = "" }},
		{"unknown profile", func(o *ClippingGeminiOptions) { o.ContentType = "sports" }},
		{"profile is case sensitive", func(o *ClippingGeminiOptions) { o.ContentType = "General" }},
		{"minimum below bound", func(o *ClippingGeminiOptions) { o.MinClipMS = 14_999 }},
		{"maximum above bound", func(o *ClippingGeminiOptions) { o.MaxClipMS = 180_001 }},
		{"inverted duration", func(o *ClippingGeminiOptions) { o.MinClipMS, o.MaxClipMS = 60_000, 30_000 }},
		{"zero candidate limit", func(o *ClippingGeminiOptions) { o.CandidateLimit = 0 }},
		{"too many candidates", func(o *ClippingGeminiOptions) { o.CandidateLimit = 11 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options := valid
			tc.change(&options)
			before := options
			if err := ValidateClippingGeminiOptions(options); !errors.Is(err, ErrClippingGeminiInvalidOptions) {
				t.Fatalf("got %v, want invalid-options error", err)
			}
			if options != before {
				t.Fatalf("validation changed options: before=%+v after=%+v", before, options)
			}
		})
	}
}
