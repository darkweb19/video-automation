package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestDecodeClippingGeminiAnalysisBoundsDefaultEncodedSize(t *testing.T) {
	options := ClippingGeminiOptions{ContentType: "general", MinClipMS: 15_000, MaxClipMS: 180_000, CandidateLimit: 10}
	makeAnalysis := func(text string) ClippingGeminiAnalysis {
		analysis := ClippingGeminiAnalysis{
			Version: ClippingGeminiAnalysisVersion, SourceDurationMS: 72_000,
			SourceDurationProvenance: ClippingGeminiModelReportedDuration,
			Summary:                  strings.Repeat(text, 4096), LanguageCodes: []string{"en"}, Limitations: []string{},
			Candidates: []ClippingGeminiCandidate{}, FewerCandidatesReason: "",
		}
		for i := 0; i < 8; i++ {
			analysis.Limitations = append(analysis.Limitations, strings.Repeat(text, 1024))
		}
		for i := 0; i < options.CandidateLimit; i++ {
			candidate := ClippingGeminiCandidate{
				Rank: i + 1, StartMS: 1_000, EndMS: 31_000,
				TitleVariants: []string{strings.Repeat(text, 180), strings.Repeat(text, 180), strings.Repeat(text, 180)},
				Hook:          strings.Repeat(text, 480), Rationale: strings.Repeat(text, 2048),
				BoundaryRationale: strings.Repeat(text, 1024), Context: strings.Repeat(text, 2048),
				EditorialScore: 50, Scores: ClippingGeminiScores{Hook: 50, EmotionalImpact: 50, RetentionPotential: 50, Shareability: 50, StandaloneContext: 50},
				Evidence: []ClippingGeminiEvidence{},
			}
			for j := 0; j < 8; j++ {
				candidate.Evidence = append(candidate.Evidence, ClippingGeminiEvidence{
					Kind: "speech", StartMS: 1_000, EndMS: 2_000, Detail: strings.Repeat(text, 1024),
				})
			}
			analysis.Candidates = append(analysis.Candidates, candidate)
		}
		return analysis
	}
	encodeUnescaped := func(analysis ClippingGeminiAnalysis) []byte {
		t.Helper()
		var buffer bytes.Buffer
		encoder := json.NewEncoder(&buffer)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(analysis); err != nil {
			t.Fatal(err)
		}
		return buffer.Bytes()
	}
	largeEscaping := makeAnalysis("<")
	input := encodeUnescaped(largeEscaping)
	defaultEncoding, err := json.Marshal(largeEscaping)
	if err != nil {
		t.Fatal(err)
	}
	if len(input) >= ClippingGeminiMaxAnalysisBytes || len(defaultEncoding) <= ClippingGeminiMaxAnalysisBytes {
		t.Fatalf("invalid test setup: raw=%d encoded=%d limit=%d", len(input), len(defaultEncoding), ClippingGeminiMaxAnalysisBytes)
	}
	result, err := DecodeClippingGeminiAnalysis(input, options)
	if !errors.Is(err, ErrClippingGeminiInvalidAnalysis) || !reflect.DeepEqual(result, ClippingGeminiAnalysis{}) {
		t.Fatalf("expanding result was not rejected without a partial value: result=%+v error=%v", result, err)
	}
	withinBound := makeAnalysis("x")
	result, err = DecodeClippingGeminiAnalysis(encodeUnescaped(withinBound), options)
	if err != nil || !reflect.DeepEqual(result, withinBound) {
		t.Fatalf("bounded result was rejected or changed: error=%v", err)
	}
}
