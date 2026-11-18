package app

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestClippingGeminiArtifactPreservesRankedPayloadAndRejectsTampering(t *testing.T) {
	options := testClippingGeminiRequest("").Options
	options.CandidateLimit = 2
	payload := readClippingGeminiFixture(t, "ranked.json")
	original, err := DecodeClippingGeminiAnalysis(payload, options)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := clippingGeminiArtifactFromPayload("clipjob_fixture", payload, options)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeClippingGeminiArtifact(artifact, options)
	if err != nil || !reflect.DeepEqual(original, decoded) || artifact.PayloadJSON != string(payload) {
		t.Fatalf("artifact changed Gemini decisions: %v", err)
	}
	if len(artifact.TimeRanges) != 2 || artifact.TimeRanges[0].StartMS != original.Candidates[0].StartMS {
		t.Fatalf("ranked ranges changed: %+v", artifact.TimeRanges)
	}
	tampered := artifact
	tampered.TimeRanges = append([]ClippingTimeRange(nil), artifact.TimeRanges...)
	tampered.TimeRanges[0].StartMS++
	if _, err := decodeClippingGeminiArtifact(tampered, options); err == nil {
		t.Fatal("accepted inconsistent envelope")
	}
	tampered = artifact
	tampered.SchemaVersion = "2"
	if _, err := decodeClippingGeminiArtifact(tampered, options); err == nil {
		t.Fatal("accepted legacy artifact as v3")
	}
}

func TestClippingGeminiArtifactRejectsDurationBeyondRequestLimit(t *testing.T) {
	options := testClippingGeminiRequest("").Options
	options.CandidateLimit = 2
	var analysis ClippingGeminiAnalysis
	if err := json.Unmarshal(readClippingGeminiFixture(t, "ranked.json"), &analysis); err != nil {
		t.Fatal(err)
	}
	analysis.SourceDurationMS = clippingGeminiMaxRequestDurationMS + 1
	payload, err := json.Marshal(analysis)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := clippingGeminiArtifactFromPayload("clipjob_fixture", payload, options); err == nil {
		t.Fatal("accepted duration beyond request ceiling")
	}
	artifact := ClippingArtifact{JobID: "clipjob_fixture", Type: clippingAnalysisArtifactType, SchemaVersion: ClippingGeminiAnalysisVersion, Version: 1, SourceDurationMS: analysis.SourceDurationMS, PayloadJSON: string(payload), TimeRanges: []ClippingTimeRange{{StartMS: analysis.Candidates[0].StartMS, EndMS: analysis.Candidates[0].EndMS}, {StartMS: analysis.Candidates[1].StartMS, EndMS: analysis.Candidates[1].EndMS}}}
	if _, err := decodeClippingGeminiArtifact(artifact, options); err == nil {
		t.Fatal("accepted oversized cached duration")
	}
}
