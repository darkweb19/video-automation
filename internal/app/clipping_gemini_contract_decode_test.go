package app

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestDecodeClippingGeminiAnalysisStrictJSON(t *testing.T) {
	options := ClippingGeminiOptions{ContentType: "general", MinClipMS: 15_000, MaxClipMS: 180_000, CandidateLimit: 2}
	valid := readClippingGeminiFixture(t, "ranked.json")
	if _, err := DecodeClippingGeminiAnalysis(valid, options); err != nil {
		t.Fatalf("valid fixture: %v", err)
	}
	replace := func(old, new string) []byte {
		t.Helper()
		if !bytes.Contains(valid, []byte(old)) {
			t.Fatalf("test fixture missing %q", old)
		}
		return bytes.Replace(valid, []byte(old), []byte(new), 1)
	}
	for _, tc := range []struct {
		name    string
		payload []byte
	}{
		{"empty", nil},
		{"oversized", bytes.Repeat([]byte("x"), ClippingGeminiMaxAnalysisBytes+1)},
		{"invalid UTF-8", append([]byte{'"', 0xff, '"'}, valid...)},
		{"malformed", valid[:len(valid)-2]},
		{"trailing JSON", append(bytes.Clone(valid), []byte("{}")...)},
		{"unknown field", replace(`"version":`, `"other":1,"version":`)},
		{"fabricated source SHA", replace(`"version":`, `"source_sha256":"abc","version":`)},
		{"missing field", replace(`"summary":`, `"missing_summary":`)},
		{"case-insensitive field", replace(`"version":`, `"Version":`)},
		{"duplicate field", replace(`"version":`, `"version":"duplicate","version":`)},
		{"nested duplicate", replace(`"hook": 84`, `"hook": 84, "hook": 84`)},
		{"null", replace(`"summary": "A speaker describes a small experiment and its outcome."`, `"summary": null`)},
		{"missing nested field", replace(`"boundary_rationale":`, `"wrong_boundary_rationale":`)},
		{"missing score", replace(`"emotional_impact":`, `"wrong_emotional_impact":`)},
		{"missing evidence detail", replace(`"detail":`, `"wrong_detail":`)},
		{"fractional milliseconds", replace(`"source_duration_ms": 72000`, `"source_duration_ms": 72000.5`)},
		{"exponent milliseconds", replace(`"source_duration_ms": 72000`, `"source_duration_ms": 72e3`)},
		{"integer overflow", replace(`"source_duration_ms": 72000`, `"source_duration_ms": 9223372036854775808`)},
		{"excessive nesting", replace(`"summary": "A speaker describes a small experiment and its outcome."`, `"summary": `+strings.Repeat("[", 35)+`"x"`+strings.Repeat("]", 35))},
		{"unpaired high surrogate", replace(`"A speaker describes a small experiment and its outcome."`, `"\ud800"`)},
		{"unpaired low surrogate", replace(`"A speaker describes a small experiment and its outcome."`, `"\udc00"`)},
		{"high surrogate followed by non-low", replace(`"A speaker describes a small experiment and its outcome."`, `"\ud800\u0041"`)},
		{"unsupported version", replace(`framevault.clipping.analysis.v3`, `framevault.clipping.analysis.v2`)},
		{"false provenance", replace(`"model_reported"`, `"verified"`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := DecodeClippingGeminiAnalysis(tc.payload, options)
			if !errors.Is(err, ErrClippingGeminiInvalidAnalysis) || !reflect.DeepEqual(result, ClippingGeminiAnalysis{}) {
				t.Fatalf("got result=%+v error=%v; want empty result and invalid-analysis error", result, err)
			}
		})
	}
	validPair := replace(`"A speaker describes a small experiment and its outcome."`, `"Music \ud834\udd1e"`)
	analysis, err := DecodeClippingGeminiAnalysis(validPair, options)
	if err != nil || analysis.Summary != "Music 𝄞" {
		t.Fatalf("valid surrogate pair changed: summary=%q error=%v", analysis.Summary, err)
	}
	invalidOptions := options
	invalidOptions.MinClipMS = 0
	if result, err := DecodeClippingGeminiAnalysis(valid, invalidOptions); !errors.Is(err, ErrClippingGeminiInvalidOptions) || !reflect.DeepEqual(result, ClippingGeminiAnalysis{}) {
		t.Fatalf("invalid options yielded result=%+v error=%v", result, err)
	}
}
