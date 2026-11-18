package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestClippingGeminiSchemaMatchesContract(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "clipping", "contracts", "gemini-analysis.v3.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	comment, ok := schema["$comment"].(string)
	if !ok || !strings.Contains(comment, "default encoding/json Marshal") || !strings.Contains(comment, "256 KiB") {
		t.Fatal("schema must document the raw and default encoded size limits")
	}
	object := func(value any) map[string]any {
		t.Helper()
		result, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("schema entry is not an object: %T", value)
		}
		return result
	}
	defs := object(schema["$defs"])
	for _, tc := range []struct {
		name   string
		schema map[string]any
		typeOf reflect.Type
	}{
		{"analysis", schema, reflect.TypeOf(ClippingGeminiAnalysis{})},
		{"candidate", object(defs["candidate"]), reflect.TypeOf(ClippingGeminiCandidate{})},
		{"scores", object(defs["scores"]), reflect.TypeOf(ClippingGeminiScores{})},
		{"evidence", object(defs["evidence"]), reflect.TypeOf(ClippingGeminiEvidence{})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.schema["type"] != "object" || tc.schema["additionalProperties"] != false {
				t.Fatal("objects must reject additional fields")
			}
			properties := object(tc.schema["properties"])
			var goFields, required []string
			for i := 0; i < tc.typeOf.NumField(); i++ {
				goFields = append(goFields, strings.Split(tc.typeOf.Field(i).Tag.Get("json"), ",")[0])
			}
			for _, value := range tc.schema["required"].([]any) {
				required = append(required, value.(string))
			}
			var schemaFields []string
			for field := range properties {
				schemaFields = append(schemaFields, field)
			}
			sort.Strings(goFields)
			sort.Strings(schemaFields)
			sort.Strings(required)
			if !reflect.DeepEqual(goFields, schemaFields) || !reflect.DeepEqual(goFields, required) {
				t.Fatalf("Go fields=%v schema fields=%v required=%v", goFields, schemaFields, required)
			}
		})
	}
	props := object(schema["properties"])
	if object(props["version"])["const"] != ClippingGeminiAnalysisVersion || object(props["source_duration_provenance"])["const"] != ClippingGeminiModelReportedDuration {
		t.Fatal("version or duration provenance differs from the Go contract")
	}
	if object(props["source_duration_ms"])["maximum"] != float64(ClippingGeminiMaxSourceDurationMS) || object(props["candidates"])["maxItems"] != float64(ClippingGeminiMaxCandidates) {
		t.Fatal("source or candidate ceiling differs from the Go contract")
	}
	for name, limit := range map[string]float64{
		"summary": 4096, "limitation": 1024, "title": 180, "hook": 480,
		"rationale": 2048, "boundary_rationale": 1024, "context": 2048, "detail": 1024,
	} {
		entry := object(defs[name])
		if entry["maxLength"] != limit || entry["x-maxUtf8Bytes"] != limit {
			t.Errorf("%s schema does not document its character and UTF-8 byte limits", name)
		}
	}
	shortfall := object(props["fewer_candidates_reason"])
	if shortfall["maxLength"] != float64(2048) || shortfall["x-maxUtf8Bytes"] != float64(2048) {
		t.Error("shortfall explanation limit differs")
	}
	score := object(defs["score"])
	if score["type"] != "integer" || score["minimum"] != float64(0) || score["maximum"] != float64(100) {
		t.Error("editorial score scale differs")
	}
	languages := object(props["language_codes"])
	if languages["minItems"] != float64(1) || languages["maxItems"] != float64(8) || object(languages["items"])["pattern"] != clippingGeminiLanguageCodePattern.String() {
		t.Error("language code constraints differ")
	}
	candidate := object(defs["candidate"])
	candidateProps := object(candidate["properties"])
	titles := object(candidateProps["title_variants"])
	evidence := object(candidateProps["evidence"])
	if titles["minItems"] != float64(1) || titles["maxItems"] != float64(3) || evidence["minItems"] != float64(1) || evidence["maxItems"] != float64(8) {
		t.Error("candidate title or evidence counts differ")
	}
	refs := []struct {
		entry any
		name  string
	}{
		{props["summary"], "summary"}, {object(props["limitations"])["items"], "limitation"},
		{object(props["candidates"])["items"], "candidate"}, {titles["items"], "title"},
		{candidateProps["hook"], "hook"}, {candidateProps["rationale"], "rationale"},
		{candidateProps["boundary_rationale"], "boundary_rationale"}, {candidateProps["context"], "context"},
		{candidateProps["editorial_score"], "score"}, {candidateProps["scores"], "scores"}, {evidence["items"], "evidence"},
		{object(object(defs["evidence"])["properties"])["detail"], "detail"},
	}
	for _, ref := range refs {
		if object(ref.entry)["$ref"] != "#/$defs/"+ref.name {
			t.Errorf("missing or incorrect schema reference to %s", ref.name)
		}
	}
	options := ClippingGeminiOptions{ContentType: "general", MinClipMS: 15_000, MaxClipMS: 180_000, CandidateLimit: 2}
	for _, fixture := range []string{"ranked.json", "shortfall.json", "empty.json"} {
		if _, err := DecodeClippingGeminiAnalysis(readClippingGeminiFixture(t, fixture), options); err != nil {
			t.Errorf("fixture %s rejected by Go contract: %v", fixture, err)
		}
	}
}
