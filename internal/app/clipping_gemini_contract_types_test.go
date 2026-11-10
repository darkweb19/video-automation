package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func readClippingGeminiFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "clipping_gemini", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

func TestClippingGeminiAnalysisFixturesRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name       string
		candidates int
		shortfall  bool
	}{
		{"ranked.json", 2, false},
		{"shortfall.json", 1, true},
		{"empty.json", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var analysis ClippingGeminiAnalysis
			if err := json.Unmarshal(readClippingGeminiFixture(t, tc.name), &analysis); err != nil {
				t.Fatal(err)
			}
			if analysis.Version != ClippingGeminiAnalysisVersion || analysis.SourceDurationProvenance != ClippingGeminiModelReportedDuration {
				t.Fatalf("unexpected version/provenance: %+v", analysis)
			}
			if len(analysis.Candidates) != tc.candidates || (analysis.FewerCandidatesReason != "") != tc.shortfall || analysis.Candidates == nil || analysis.Limitations == nil {
				t.Fatalf("fixture lost its candidate or shortfall semantics: %+v", analysis)
			}
			encoded, err := json.Marshal(analysis)
			if err != nil {
				t.Fatal(err)
			}
			var roundTrip ClippingGeminiAnalysis
			if err := json.Unmarshal(encoded, &roundTrip); err != nil || !reflect.DeepEqual(analysis, roundTrip) {
				t.Fatalf("editorial result changed on round trip: %v", err)
			}
		})
	}
	var ranked ClippingGeminiAnalysis
	if err := json.Unmarshal(readClippingGeminiFixture(t, "ranked.json"), &ranked); err != nil {
		t.Fatal(err)
	}
	if ranked.Candidates[0].StartMS >= ranked.Candidates[1].StartMS || ranked.Candidates[0].EndMS <= ranked.Candidates[1].StartMS || ranked.Candidates[0].EditorialScore >= ranked.Candidates[1].EditorialScore {
		t.Fatal("fixture must exercise model ordering independent of overlap and score")
	}
}
