package app

import (
	"strings"
	"testing"
)

func TestBuildClippingGeminiPromptIncludesValidatedOptionsAndContractRules(t *testing.T) {
	options := ClippingGeminiOptions{
		ContentType:    "podcast",
		MinClipMS:      20_000,
		MaxClipMS:      120_000,
		CandidateLimit: 7,
	}
	version, prompt, err := clippingGeminiPromptSnapshot(options)
	if err != nil {
		t.Fatalf("clippingGeminiPromptSnapshot() error = %v", err)
	}
	if version != "framevault.clipping.editorial.v1" {
		t.Errorf("prompt version = %q", version)
	}
	for _, want := range []string{
		ClippingGeminiAnalysisVersion,
		"Content type: \"podcast\"",
		"Select no more than 7 candidates",
		"between 20000 and 120000 milliseconds",
		"at most 2400000 milliseconds (40 minutes)",
		ClippingGeminiModelReportedDuration,
		"hook is an editorial suggestion, not a quotation",
		"Return fewer than the requested maximum, including zero",
		"Rank your choices by editorial preference, starting at rank 1",
		"Score totals need not be monotonic",
		"Avoid redundant choices; intentional overlap is permitted when moments serve distinct editorial purposes",
		"integer milliseconds from the beginning of the full source",
		"speech, audio, and visual events",
		"Consider moments from the beginning, middle, and end",
		"do not claim complete or verified coverage",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

func TestClippingGeminiPromptUsesValidatedContentTypeGuidance(t *testing.T) {
	tests := []struct {
		contentType string
		want        string
	}{
		{"general", "self-contained highlights"},
		{"podcast", "useful explanation"},
		{"comedy", "punchline, reaction"},
		{"gaming", "clear outcome"},
		{"movie", "narrative and visual continuity"},
	}
	for _, tt := range tests {
		t.Run(tt.contentType, func(t *testing.T) {
			prompt, err := buildClippingGeminiPrompt(ClippingGeminiOptions{
				ContentType:    tt.contentType,
				MinClipMS:      15_000,
				MaxClipMS:      180_000,
				CandidateLimit: 10,
			})
			if err != nil {
				t.Fatalf("buildClippingGeminiPrompt() error = %v", err)
			}
			if !strings.Contains(prompt, tt.want) {
				t.Errorf("prompt does not contain content-type guidance %q", tt.want)
			}
		})
	}
}

func TestBuildClippingGeminiPromptRejectsInvalidOptions(t *testing.T) {
	_, err := buildClippingGeminiPrompt(ClippingGeminiOptions{
		ContentType:    "podcast",
		MinClipMS:      1,
		MaxClipMS:      120_000,
		CandidateLimit: 7,
	})
	if err == nil {
		t.Fatal("buildClippingGeminiPrompt() accepted invalid options")
	}
}
