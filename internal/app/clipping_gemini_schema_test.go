package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"

	"google.golang.org/genai"
)

type clippingGeminiContractSchema struct {
	Type         string                                  `json:"type"`
	Const        string                                  `json:"const"`
	Enum         []string                                `json:"enum"`
	Minimum      *float64                                `json:"minimum"`
	Maximum      *float64                                `json:"maximum"`
	MinItems     *int64                                  `json:"minItems"`
	MaxItems     *int64                                  `json:"maxItems"`
	MaxLength    *int64                                  `json:"maxLength"`
	MaxUTF8Bytes *int64                                  `json:"x-maxUtf8Bytes"`
	Properties   map[string]clippingGeminiContractSchema `json:"properties"`
	Required     []string                                `json:"required"`
	Items        *clippingGeminiContractSchema           `json:"items"`
	Defs         map[string]clippingGeminiContractSchema `json:"$defs"`
}

func TestClippingGeminiProviderSchemaParityWithMachineContract(t *testing.T) {
	contract := readClippingGeminiContractSchema(t)
	provider, err := clippingGeminiResponseSchema(ClippingGeminiOptions{
		ContentType:    "podcast",
		MinClipMS:      15_000,
		MaxClipMS:      180_000,
		CandidateLimit: ClippingGeminiMaxCandidates,
	})
	if err != nil {
		t.Fatalf("clippingGeminiResponseSchema() error = %v", err)
	}

	assertSchemaObjectParity(t, "analysis", contract, provider)
	assertSchemaNodeType(t, "analysis", provider, "object")
	assertSchemaNodeType(t, "version", provider.Properties["version"], "string")
	assertSchemaNodeType(t, "source_duration_ms", provider.Properties["source_duration_ms"], "integer")
	assertSchemaNodeType(t, "source_duration_provenance", provider.Properties["source_duration_provenance"], "string")
	assertSchemaNodeType(t, "summary", provider.Properties["summary"], "string")
	assertSchemaNodeType(t, "language_codes", provider.Properties["language_codes"], "array")
	assertSchemaNodeType(t, "limitations", provider.Properties["limitations"], "array")
	assertSchemaNodeType(t, "candidates", provider.Properties["candidates"], "array")
	assertSchemaNodeType(t, "fewer_candidates_reason", provider.Properties["fewer_candidates_reason"], "string")

	assertSchemaEnum(t, "version", contract.Properties["version"], provider.Properties["version"])
	assertSchemaEnum(t, "source_duration_provenance", contract.Properties["source_duration_provenance"], provider.Properties["source_duration_provenance"])
	assertSchemaBounds(t, "source_duration_ms", contract.Properties["source_duration_ms"], provider.Properties["source_duration_ms"])
	assertSchemaArrayBounds(t, "language_codes", contract.Properties["language_codes"], provider.Properties["language_codes"])
	assertSchemaArrayBounds(t, "limitations", contract.Properties["limitations"], provider.Properties["limitations"])
	assertSchemaArrayBounds(t, "candidates", contract.Properties["candidates"], provider.Properties["candidates"])
	assertSchemaByteLimit(t, "summary", contract.Defs["summary"], provider.Properties["summary"])
	assertSchemaByteLimit(t, "language_codes.item", *contract.Properties["language_codes"].Items, provider.Properties["language_codes"].Items)
	assertSchemaByteLimit(t, "limitations.item", contract.Defs["limitation"], provider.Properties["limitations"].Items)
	assertSchemaByteLimit(t, "fewer_candidates_reason", contract.Properties["fewer_candidates_reason"], provider.Properties["fewer_candidates_reason"])

	contractCandidate := contract.Defs["candidate"]
	providerCandidate := provider.Properties["candidates"].Items
	if providerCandidate == nil {
		t.Fatal("provider candidates array has no item schema")
	}
	assertSchemaObjectParity(t, "candidate", contractCandidate, providerCandidate)
	assertSchemaNodeType(t, "candidate", providerCandidate, "object")
	for _, name := range []string{"rank", "start_ms", "end_ms"} {
		assertSchemaBounds(t, "candidate."+name, contractCandidate.Properties[name], providerCandidate.Properties[name])
	}
	assertSchemaBounds(t, "candidate.editorial_score", contract.Defs["score"], providerCandidate.Properties["editorial_score"])
	assertSchemaArrayBounds(t, "candidate.title_variants", contractCandidate.Properties["title_variants"], providerCandidate.Properties["title_variants"])
	assertSchemaArrayBounds(t, "candidate.evidence", contractCandidate.Properties["evidence"], providerCandidate.Properties["evidence"])
	assertSchemaByteLimit(t, "candidate.title_variants.item", contract.Defs["title"], providerCandidate.Properties["title_variants"].Items)
	for _, field := range []string{"hook", "rationale", "boundary_rationale", "context"} {
		assertSchemaByteLimit(t, "candidate."+field, contract.Defs[field], providerCandidate.Properties[field])
	}

	contractScores := contract.Defs["scores"]
	providerScores := providerCandidate.Properties["scores"]
	assertSchemaObjectParity(t, "scores", contractScores, providerScores)
	assertSchemaNodeType(t, "scores", providerScores, "object")
	for name := range contractScores.Properties {
		assertSchemaBounds(t, "scores."+name, contract.Defs["score"], providerScores.Properties[name])
	}
	contractEvidence := contract.Defs["evidence"]
	providerEvidence := providerCandidate.Properties["evidence"].Items
	if providerEvidence == nil {
		t.Fatal("candidate evidence array has no item schema")
	}
	assertSchemaObjectParity(t, "evidence", contractEvidence, providerEvidence)
	assertSchemaNodeType(t, "evidence", providerEvidence, "object")
	assertSchemaEnum(t, "evidence.kind", contractEvidence.Properties["kind"], providerEvidence.Properties["kind"])
	assertSchemaByteLimit(t, "evidence.detail", contract.Defs["detail"], providerEvidence.Properties["detail"])
	for _, name := range []string{"start_ms", "end_ms"} {
		assertSchemaBounds(t, "evidence."+name, contractEvidence.Properties[name], providerEvidence.Properties[name])
	}
}

func TestClippingGeminiProviderSchemaUsesRequestCandidateLimit(t *testing.T) {
	provider, err := clippingGeminiResponseSchema(ClippingGeminiOptions{
		ContentType:    "general",
		MinClipMS:      15_000,
		MaxClipMS:      180_000,
		CandidateLimit: 3,
	})
	if err != nil {
		t.Fatalf("clippingGeminiResponseSchema() error = %v", err)
	}
	if got := *provider.Properties["candidates"].MaxItems; got != 3 {
		t.Errorf("candidate maxItems = %d, want 3", got)
	}
	if got := *provider.Properties["candidates"].Items.Properties["rank"].Maximum; got != 3 {
		t.Errorf("candidate rank maximum = %v, want 3", got)
	}
}

func TestClippingGeminiProviderSchemaRejectsInvalidOptions(t *testing.T) {
	_, err := clippingGeminiResponseSchema(ClippingGeminiOptions{ContentType: "invalid"})
	if err == nil {
		t.Fatal("clippingGeminiResponseSchema() accepted invalid options")
	}
}

func readClippingGeminiContractSchema(t *testing.T) clippingGeminiContractSchema {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() failed")
	}
	path := filepath.Join(filepath.Dir(file), "..", "..", "docs", "clipping", "contracts", "gemini-analysis.v3.schema.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read machine contract schema: %v", err)
	}
	var schema clippingGeminiContractSchema
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatalf("decode machine contract schema: %v", err)
	}
	return schema
}

func assertSchemaObjectParity(t *testing.T, name string, contract clippingGeminiContractSchema, provider *genai.Schema) {
	t.Helper()
	if provider == nil {
		t.Fatalf("%s provider schema is nil", name)
	}
	if got, want := sortedSchemaNames(provider.Required), sortedSchemaNames(contract.Required); !reflect.DeepEqual(got, want) {
		t.Errorf("%s required fields = %v, want %v", name, got, want)
	}
	if got, want := sortedSchemaMapKeys(provider.Properties), sortedSchemaMapKeys(contract.Properties); !reflect.DeepEqual(got, want) {
		t.Errorf("%s fields = %v, want %v", name, got, want)
	}
}

func assertSchemaNodeType(t *testing.T, name string, provider *genai.Schema, want string) {
	t.Helper()
	if provider == nil {
		t.Fatalf("%s provider schema is nil", name)
	}
	if got := string(provider.Type); got != strings.ToUpper(want) {
		t.Errorf("%s type = %q, want %q", name, got, strings.ToUpper(want))
	}
}

func assertSchemaEnum(t *testing.T, name string, contract clippingGeminiContractSchema, provider *genai.Schema) {
	t.Helper()
	want := contract.Enum
	if contract.Const != "" {
		want = []string{contract.Const}
	}
	if got := sortedSchemaNames(provider.Enum); !reflect.DeepEqual(got, sortedSchemaNames(want)) {
		t.Errorf("%s enum = %v, want %v", name, got, want)
	}
}

func assertSchemaBounds(t *testing.T, name string, contract clippingGeminiContractSchema, provider *genai.Schema) {
	t.Helper()
	if provider == nil {
		t.Fatalf("%s provider schema is nil", name)
	}
	if contract.Minimum != nil {
		if provider.Minimum == nil || *provider.Minimum != *contract.Minimum {
			t.Errorf("%s minimum = %v, want %v", name, floatPointerValue(provider.Minimum), *contract.Minimum)
		}
	}
	if contract.Maximum != nil {
		if provider.Maximum == nil || *provider.Maximum != *contract.Maximum {
			t.Errorf("%s maximum = %v, want %v", name, floatPointerValue(provider.Maximum), *contract.Maximum)
		}
	}
}

func assertSchemaByteLimit(t *testing.T, name string, contract clippingGeminiContractSchema, provider *genai.Schema) {
	t.Helper()
	if provider == nil {
		t.Fatalf("%s provider schema is nil", name)
	}
	want := contract.MaxUTF8Bytes
	if want == nil {
		want = contract.MaxLength
	}
	if want == nil {
		t.Fatalf("%s machine contract has no UTF-8 byte or ASCII character limit", name)
	}
	expected := "at most " + strconv.FormatInt(*want, 10) + " UTF-8 bytes"
	if !strings.Contains(provider.Description, expected) {
		t.Errorf("%s description %q does not include %q", name, provider.Description, expected)
	}
}

func assertSchemaArrayBounds(t *testing.T, name string, contract clippingGeminiContractSchema, provider *genai.Schema) {
	t.Helper()
	if provider == nil || provider.Type != genai.TypeArray {
		t.Fatalf("%s provider schema is not an array", name)
	}
	if contract.MinItems != nil {
		if provider.MinItems == nil || *provider.MinItems != *contract.MinItems {
			t.Errorf("%s minItems = %v, want %d", name, intPointerValue(provider.MinItems), *contract.MinItems)
		}
	} else if provider.MinItems != nil && *provider.MinItems != 0 {
		t.Errorf("%s minItems = %d, want no minimum", name, *provider.MinItems)
	}
	if contract.MaxItems != nil {
		if provider.MaxItems == nil || *provider.MaxItems != *contract.MaxItems {
			t.Errorf("%s maxItems = %v, want %d", name, intPointerValue(provider.MaxItems), *contract.MaxItems)
		}
	}
}

func sortedSchemaNames(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}

func sortedSchemaMapKeys[T any](values map[string]T) []string {
	result := make([]string, 0, len(values))
	for key := range values {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}

func floatPointerValue(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

func intPointerValue(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}
