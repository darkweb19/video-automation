package app

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestDecodeClippingGeminiAnalysisEditorial(t *testing.T) {
	options := ClippingGeminiOptions{ContentType: "general", MinClipMS: 15_000, MaxClipMS: 180_000, CandidateLimit: 2}
	encode := func(change func(*ClippingGeminiAnalysis)) []byte {
		t.Helper()
		var analysis ClippingGeminiAnalysis
		if err := json.Unmarshal(readClippingGeminiFixture(t, "ranked.json"), &analysis); err != nil {
			t.Fatal(err)
		}
		change(&analysis)
		payload, err := json.Marshal(analysis)
		if err != nil {
			t.Fatal(err)
		}
		return payload
	}
	for _, tc := range []struct {
		name   string
		change func(*ClippingGeminiAnalysis)
	}{
		{"blank summary", func(a *ClippingGeminiAnalysis) { a.Summary = " \t" }},
		{"summary byte bound", func(a *ClippingGeminiAnalysis) { a.Summary = strings.Repeat("界", 1366) }},
		{"no language", func(a *ClippingGeminiAnalysis) { a.LanguageCodes = []string{} }},
		{"too many languages", func(a *ClippingGeminiAnalysis) {
			a.LanguageCodes = []string{"en", "en", "en", "en", "en", "en", "en", "en", "en"}
		}},
		{"invalid language", func(a *ClippingGeminiAnalysis) { a.LanguageCodes = []string{"en_US"} }},
		{"empty language subtag", func(a *ClippingGeminiAnalysis) { a.LanguageCodes = []string{"en--US"} }},
		{"too many limitations", func(a *ClippingGeminiAnalysis) { a.Limitations = []string{"a", "a", "a", "a", "a", "a", "a", "a", "a"} }},
		{"blank limitation", func(a *ClippingGeminiAnalysis) { a.Limitations = []string{"  "} }},
		{"long limitation", func(a *ClippingGeminiAnalysis) { a.Limitations = []string{strings.Repeat("x", 1025)} }},
		{"missing shortfall explanation", func(a *ClippingGeminiAnalysis) { a.Candidates = a.Candidates[:1]; a.FewerCandidatesReason = " " }},
		{"long shortfall explanation", func(a *ClippingGeminiAnalysis) { a.FewerCandidatesReason = strings.Repeat("x", 2049) }},
		{"no title", func(a *ClippingGeminiAnalysis) { a.Candidates[0].TitleVariants = []string{} }},
		{"too many titles", func(a *ClippingGeminiAnalysis) { a.Candidates[0].TitleVariants = []string{"a", "b", "c", "d"} }},
		{"blank title", func(a *ClippingGeminiAnalysis) { a.Candidates[0].TitleVariants[0] = " " }},
		{"long multibyte title", func(a *ClippingGeminiAnalysis) { a.Candidates[0].TitleVariants[0] = strings.Repeat("界", 61) }},
		{"blank hook", func(a *ClippingGeminiAnalysis) { a.Candidates[0].Hook = "" }},
		{"long hook", func(a *ClippingGeminiAnalysis) { a.Candidates[0].Hook = strings.Repeat("x", 481) }},
		{"blank rationale", func(a *ClippingGeminiAnalysis) { a.Candidates[0].Rationale = " " }},
		{"long rationale", func(a *ClippingGeminiAnalysis) { a.Candidates[0].Rationale = strings.Repeat("x", 2049) }},
		{"blank boundary rationale", func(a *ClippingGeminiAnalysis) { a.Candidates[0].BoundaryRationale = " " }},
		{"long boundary rationale", func(a *ClippingGeminiAnalysis) { a.Candidates[0].BoundaryRationale = strings.Repeat("x", 1025) }},
		{"blank context", func(a *ClippingGeminiAnalysis) { a.Candidates[0].Context = " " }},
		{"long context", func(a *ClippingGeminiAnalysis) { a.Candidates[0].Context = strings.Repeat("x", 2049) }},
		{"editorial score negative", func(a *ClippingGeminiAnalysis) { a.Candidates[0].EditorialScore = -1 }},
		{"editorial score over 100", func(a *ClippingGeminiAnalysis) { a.Candidates[0].EditorialScore = 101 }},
		{"component score negative", func(a *ClippingGeminiAnalysis) { a.Candidates[0].Scores.Hook = -1 }},
		{"component score over 100", func(a *ClippingGeminiAnalysis) { a.Candidates[0].Scores.StandaloneContext = 101 }},
		{"no evidence", func(a *ClippingGeminiAnalysis) { a.Candidates[0].Evidence = []ClippingGeminiEvidence{} }},
		{"too much evidence", func(a *ClippingGeminiAnalysis) {
			for len(a.Candidates[0].Evidence) < 9 {
				a.Candidates[0].Evidence = append(a.Candidates[0].Evidence, a.Candidates[0].Evidence[0])
			}
		}},
		{"unknown evidence kind", func(a *ClippingGeminiAnalysis) { a.Candidates[0].Evidence[0].Kind = "transcript_verified" }},
		{"blank evidence detail", func(a *ClippingGeminiAnalysis) { a.Candidates[0].Evidence[0].Detail = " " }},
		{"long evidence detail", func(a *ClippingGeminiAnalysis) { a.Candidates[0].Evidence[0].Detail = strings.Repeat("x", 1025) }},
		{"negative evidence start", func(a *ClippingGeminiAnalysis) { a.Candidates[0].Evidence[0].StartMS = -1 }},
		{"empty evidence range", func(a *ClippingGeminiAnalysis) {
			a.Candidates[0].Evidence[0].EndMS = a.Candidates[0].Evidence[0].StartMS
		}},
		{"evidence past duration", func(a *ClippingGeminiAnalysis) { a.Candidates[0].Evidence[0].EndMS = a.SourceDurationMS + 1 }},
		{"evidence ends at candidate start", func(a *ClippingGeminiAnalysis) {
			a.Candidates[0].Evidence[0].StartMS = 1
			a.Candidates[0].Evidence[0].EndMS = a.Candidates[0].StartMS
		}},
		{"evidence starts at candidate end", func(a *ClippingGeminiAnalysis) {
			a.Candidates[0].Evidence[0].StartMS = a.Candidates[0].EndMS
			a.Candidates[0].Evidence[0].EndMS = a.SourceDurationMS
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := DecodeClippingGeminiAnalysis(encode(tc.change), options)
			if !errors.Is(err, ErrClippingGeminiInvalidAnalysis) || !reflect.DeepEqual(result, ClippingGeminiAnalysis{}) {
				t.Fatalf("got result=%+v error=%v; want empty result and invalid-analysis error", result, err)
			}
		})
	}
	for _, name := range []string{"shortfall.json", "empty.json"} {
		if _, err := DecodeClippingGeminiAnalysis(readClippingGeminiFixture(t, name), options); err != nil {
			t.Fatalf("valid fixture %s rejected: %v", name, err)
		}
	}
	payload := encode(func(a *ClippingGeminiAnalysis) {
		a.Summary = "  Keep model whitespace  "
		a.LanguageCodes = []string{"und", "en-US"}
		a.Candidates[0].TitleVariants[0] = "  Original title  "
		a.Candidates[0].Evidence[0].StartMS = a.Candidates[0].StartMS - 1
		a.Candidates[0].Evidence[0].EndMS = a.Candidates[0].StartMS + 1
		a.Candidates[0].Scores.Hook = 0
		a.Candidates[1].Scores.Hook = 100
	})
	var original ClippingGeminiAnalysis
	if err := json.Unmarshal(payload, &original); err != nil {
		t.Fatal(err)
	}
	result, err := DecodeClippingGeminiAnalysis(payload, options)
	if err != nil || !reflect.DeepEqual(result, original) {
		t.Fatalf("valid creative decisions or text changed: result=%+v error=%v", result, err)
	}
}
