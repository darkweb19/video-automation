package app

import (
	"fmt"

	"google.golang.org/genai"
)

// clippingGeminiResponseSchema returns the subset Google Cloud structured
// output supports. The local decoder remains authoritative for exact object
// shape, UTF-8 byte limits, duplicate keys, and request-dependent ranges.
func clippingGeminiResponseSchema(options ClippingGeminiOptions) (*genai.Schema, error) {
	if err := ValidateClippingGeminiOptions(options); err != nil {
		return nil, err
	}
	limit := int64(options.CandidateLimit)

	score := func() *genai.Schema { return clippingGeminiIntegerSchema(0, 100) }
	scores := &genai.Schema{
		Type: genai.TypeObject,
		Properties: map[string]*genai.Schema{
			"hook":                score(),
			"emotional_impact":    score(),
			"retention_potential": score(),
			"shareability":        score(),
			"standalone_context":  score(),
		},
		Required:         []string{"hook", "emotional_impact", "retention_potential", "shareability", "standalone_context"},
		PropertyOrdering: []string{"hook", "emotional_impact", "retention_potential", "shareability", "standalone_context"},
	}
	evidence := &genai.Schema{
		Type: genai.TypeObject,
		Properties: map[string]*genai.Schema{
			"kind":     clippingGeminiStringEnumSchema("speech", "audio", "visual"),
			"start_ms": clippingGeminiIntegerSchema(0, ClippingGeminiMaxSourceDurationMS),
			"end_ms":   clippingGeminiIntegerSchema(1, ClippingGeminiMaxSourceDurationMS),
			"detail":   clippingGeminiStringSchema("Concrete model observation; not independently verified.", 1024),
		},
		Required:         []string{"kind", "start_ms", "end_ms", "detail"},
		PropertyOrdering: []string{"kind", "start_ms", "end_ms", "detail"},
	}
	candidate := &genai.Schema{
		Type: genai.TypeObject,
		Properties: map[string]*genai.Schema{
			"rank":               clippingGeminiIntegerSchema(1, limit),
			"start_ms":           clippingGeminiIntegerSchema(0, ClippingGeminiMaxSourceDurationMS),
			"end_ms":             clippingGeminiIntegerSchema(1, ClippingGeminiMaxSourceDurationMS),
			"title_variants":     clippingGeminiArraySchema(1, 3, clippingGeminiStringSchema("Short title option; the first is primary.", 180)),
			"hook":               clippingGeminiStringSchema("Editorial suggestion, not a verified quotation.", 480),
			"rationale":          clippingGeminiStringSchema("Why this moment is editorially useful.", 2048),
			"boundary_rationale": clippingGeminiStringSchema("Why the proposed opening and ending work.", 1024),
			"context":            clippingGeminiStringSchema("Context a viewer needs to follow the moment.", 2048),
			"editorial_score":    score(),
			"scores":             scores,
			"evidence":           clippingGeminiArraySchema(1, 8, evidence),
		},
		Required: []string{
			"rank", "start_ms", "end_ms", "title_variants", "hook", "rationale",
			"boundary_rationale", "context", "editorial_score", "scores", "evidence",
		},
		PropertyOrdering: []string{
			"rank", "start_ms", "end_ms", "title_variants", "hook", "rationale",
			"boundary_rationale", "context", "editorial_score", "scores", "evidence",
		},
	}
	return &genai.Schema{
		Type: genai.TypeObject,
		Properties: map[string]*genai.Schema{
			"version":                    clippingGeminiStringEnumSchema(ClippingGeminiAnalysisVersion),
			"source_duration_ms":         clippingGeminiIntegerSchema(1, ClippingGeminiMaxSourceDurationMS),
			"source_duration_provenance": clippingGeminiStringEnumSchema(ClippingGeminiModelReportedDuration),
			"summary":                    clippingGeminiStringSchema("Concise summary of the attached source.", 4096),
			"language_codes":             clippingGeminiArraySchema(1, 8, clippingGeminiStringSchema("BCP-47 language tag, or und when uncertain.", 35)),
			"limitations":                clippingGeminiArraySchema(0, 8, clippingGeminiStringSchema("Concrete limitation of the source analysis.", 1024)),
			"candidates":                 clippingGeminiArraySchema(0, limit, candidate),
			"fewer_candidates_reason":    clippingGeminiStringSchema("Required by local validation when fewer candidates are returned than requested.", 2048),
		},
		Required: []string{
			"version", "source_duration_ms", "source_duration_provenance", "summary",
			"language_codes", "limitations", "candidates", "fewer_candidates_reason",
		},
		PropertyOrdering: []string{
			"version", "source_duration_ms", "source_duration_provenance", "summary",
			"language_codes", "limitations", "candidates", "fewer_candidates_reason",
		},
	}, nil
}

func clippingGeminiStringSchema(description string, maxUTF8Bytes int) *genai.Schema {
	return &genai.Schema{
		Type:        genai.TypeString,
		Description: fmt.Sprintf("%s Keep at most %d UTF-8 bytes.", description, maxUTF8Bytes),
	}
}

func clippingGeminiStringEnumSchema(values ...string) *genai.Schema {
	return &genai.Schema{Type: genai.TypeString, Enum: values}
}

func clippingGeminiIntegerSchema(minimum, maximum int64) *genai.Schema {
	min, max := float64(minimum), float64(maximum)
	return &genai.Schema{Type: genai.TypeInteger, Minimum: &min, Maximum: &max}
}

func clippingGeminiArraySchema(minimum, maximum int64, item *genai.Schema) *genai.Schema {
	return &genai.Schema{Type: genai.TypeArray, MinItems: &minimum, MaxItems: &maximum, Items: item}
}
