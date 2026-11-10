package app

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestDecodeClippingGeminiAnalysisTimeline(t *testing.T) {
	options := ClippingGeminiOptions{ContentType: "general", MinClipMS: 15_000, MaxClipMS: 180_000, CandidateLimit: 2}
	var original ClippingGeminiAnalysis
	if err := json.Unmarshal(readClippingGeminiFixture(t, "ranked.json"), &original); err != nil {
		t.Fatal(err)
	}
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
		{"zero source duration", func(a *ClippingGeminiAnalysis) { a.SourceDurationMS = 0 }},
		{"negative source duration", func(a *ClippingGeminiAnalysis) { a.SourceDurationMS = -1 }},
		{"source over defensive maximum", func(a *ClippingGeminiAnalysis) { a.SourceDurationMS = ClippingGeminiMaxSourceDurationMS + 1 }},
		{"too many candidates", func(a *ClippingGeminiAnalysis) { a.Candidates = append(a.Candidates, a.Candidates[1]) }},
		{"first rank not one", func(a *ClippingGeminiAnalysis) { a.Candidates[0].Rank = 0 }},
		{"duplicate rank", func(a *ClippingGeminiAnalysis) { a.Candidates[1].Rank = 1 }},
		{"rank gap", func(a *ClippingGeminiAnalysis) { a.Candidates[1].Rank = 3 }},
		{"reversed rank order", func(a *ClippingGeminiAnalysis) { a.Candidates[0], a.Candidates[1] = a.Candidates[1], a.Candidates[0] }},
		{"negative start", func(a *ClippingGeminiAnalysis) { a.Candidates[0].StartMS = -1 }},
		{"empty range", func(a *ClippingGeminiAnalysis) { a.Candidates[0].EndMS = a.Candidates[0].StartMS }},
		{"end before start", func(a *ClippingGeminiAnalysis) { a.Candidates[0].EndMS = a.Candidates[0].StartMS - 1 }},
		{"end beyond duration", func(a *ClippingGeminiAnalysis) { a.Candidates[0].EndMS = a.SourceDurationMS + 1 }},
		{"short clip", func(a *ClippingGeminiAnalysis) {
			a.Candidates[0].EndMS = a.Candidates[0].StartMS + options.MinClipMS - 1
		}},
		{"long clip", func(a *ClippingGeminiAnalysis) {
			a.SourceDurationMS = 200_000
			a.Candidates[0].StartMS = 0
			a.Candidates[0].EndMS = options.MaxClipMS + 1
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := DecodeClippingGeminiAnalysis(encode(tc.change), options)
			if !errors.Is(err, ErrClippingGeminiInvalidAnalysis) || !reflect.DeepEqual(result, ClippingGeminiAnalysis{}) {
				t.Fatalf("got result=%+v error=%v; want empty result and invalid-analysis error", result, err)
			}
		})
	}
	result, err := DecodeClippingGeminiAnalysis(readClippingGeminiFixture(t, "ranked.json"), options)
	if err != nil || !reflect.DeepEqual(result, original) {
		t.Fatalf("ranked overlaps or model decisions changed: result=%+v error=%v", result, err)
	}
	boundaryPayload := encode(func(a *ClippingGeminiAnalysis) {
		a.Candidates[0].StartMS = 0
		a.Candidates[0].EndMS = options.MinClipMS
		a.Candidates[0].Evidence[0].StartMS, a.Candidates[0].Evidence[0].EndMS = 1_000, 2_000
		a.Candidates[0].Evidence[1].StartMS, a.Candidates[0].Evidence[1].EndMS = 3_000, 4_000
		a.Candidates[1].EndMS = a.SourceDurationMS
	})
	if _, err := DecodeClippingGeminiAnalysis(boundaryPayload, options); err != nil {
		t.Fatalf("valid endpoint and minimum boundaries rejected: %v", err)
	}
	if _, err := DecodeClippingGeminiAnalysis(readClippingGeminiFixture(t, "empty.json"), options); err != nil {
		t.Fatalf("short source with zero candidates rejected: %v", err)
	}
}
