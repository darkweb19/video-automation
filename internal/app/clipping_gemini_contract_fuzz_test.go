package app

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func FuzzDecodeClippingGeminiAnalysis(f *testing.F) {
	for _, fixture := range []string{"ranked.json", "shortfall.json", "empty.json"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "clipping_gemini", fixture))
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}
	for _, input := range []string{
		"",
		`{"version":"framevault.clipping.analysis.v3","version":"duplicate"}`,
		`{"summary":"\ud800"}`,
		`{"candidates":null}`,
		strings.Repeat("[", 40) + "0" + strings.Repeat("]", 40),
	} {
		f.Add([]byte(input))
	}
	options := ClippingGeminiOptions{ContentType: "general", MinClipMS: 15_000, MaxClipMS: 180_000, CandidateLimit: 2}
	f.Fuzz(func(t *testing.T, payload []byte) {
		result, err := DecodeClippingGeminiAnalysis(payload, options)
		if err != nil {
			if !errors.Is(err, ErrClippingGeminiInvalidAnalysis) || !reflect.DeepEqual(result, ClippingGeminiAnalysis{}) {
				t.Fatalf("rejected input returned a partial result or unexpected error: %+v, %v", result, err)
			}
			return
		}
		if result.Version != ClippingGeminiAnalysisVersion || result.SourceDurationProvenance != ClippingGeminiModelReportedDuration || result.SourceDurationMS < 1 || result.SourceDurationMS > ClippingGeminiMaxSourceDurationMS || len(result.Candidates) > options.CandidateLimit {
			t.Fatal("accepted result violates source identity, duration or count")
		}
		if len(result.Candidates) < options.CandidateLimit && strings.TrimSpace(result.FewerCandidatesReason) == "" {
			t.Fatal("accepted shortfall without explanation")
		}
		for i, candidate := range result.Candidates {
			if candidate.Rank != i+1 || candidate.StartMS < 0 || candidate.EndMS <= candidate.StartMS || candidate.EndMS > result.SourceDurationMS || candidate.EndMS-candidate.StartMS < options.MinClipMS || candidate.EndMS-candidate.StartMS > options.MaxClipMS {
				t.Fatal("accepted an invalid rank or source-time candidate range")
			}
			for _, evidence := range candidate.Evidence {
				if evidence.StartMS < 0 || evidence.EndMS <= evidence.StartMS || evidence.EndMS > result.SourceDurationMS || evidence.StartMS >= candidate.EndMS || evidence.EndMS <= candidate.StartMS {
					t.Fatal("accepted an invalid evidence range")
				}
			}
		}
		encoded, marshalErr := json.Marshal(result)
		if marshalErr != nil {
			t.Fatalf("accepted result could not be encoded: %v", marshalErr)
		}
		second, secondErr := DecodeClippingGeminiAnalysis(encoded, options)
		if secondErr != nil || !reflect.DeepEqual(result, second) {
			t.Fatalf("accepted result changed after round trip: %v", secondErr)
		}
	})
}
