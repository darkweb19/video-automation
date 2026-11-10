package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"
)

const ClippingGeminiMaxAnalysisBytes = 256 << 10

var ErrClippingGeminiInvalidAnalysis = errors.New("invalid Gemini clipping analysis")

// DecodeClippingGeminiAnalysis decodes one bounded, strict v3 result using
// the options of the request that produced it. Invalid results never return a
// partial analysis. Source duration and timestamps remain model reports.
func DecodeClippingGeminiAnalysis(payload []byte, options ClippingGeminiOptions) (ClippingGeminiAnalysis, error) {
	if err := ValidateClippingGeminiOptions(options); err != nil {
		return ClippingGeminiAnalysis{}, err
	}
	if len(payload) == 0 || len(payload) > ClippingGeminiMaxAnalysisBytes || !utf8.Valid(payload) || !validClippingGeminiUnicodeEscapes(payload) {
		return ClippingGeminiAnalysis{}, fmt.Errorf("%w: invalid or oversized UTF-8 JSON", ErrClippingGeminiInvalidAnalysis)
	}
	var analysis ClippingGeminiAnalysis
	// This shared decoder rejects nulls, duplicate keys, trailing content and
	// unknown fields, and limits JSON nesting to 32 levels. The exact-shape
	// pass additionally rejects missing or case-insensitively matched fields.
	if err := decodeStructuredTextJSON(string(payload), &analysis); err != nil || !exactClippingGeminiAnalysisShape(payload) {
		return ClippingGeminiAnalysis{}, fmt.Errorf("%w: unsupported JSON shape", ErrClippingGeminiInvalidAnalysis)
	}
	if analysis.Version != ClippingGeminiAnalysisVersion || analysis.SourceDurationProvenance != ClippingGeminiModelReportedDuration {
		return ClippingGeminiAnalysis{}, fmt.Errorf("%w: unsupported version or duration provenance", ErrClippingGeminiInvalidAnalysis)
	}
	if err := validateClippingGeminiTimeline(analysis, options); err != nil {
		return ClippingGeminiAnalysis{}, err
	}
	return analysis, nil
}

// encoding/json accepts a case-insensitive field match and does not require
// struct fields to be present. Check every object against exact wire names.
func exactClippingGeminiAnalysisShape(payload []byte) bool {
	var root map[string]json.RawMessage
	if json.Unmarshal(payload, &root) != nil || !hasExactJSONFields(root,
		"version", "source_duration_ms", "source_duration_provenance", "summary", "language_codes", "limitations", "candidates", "fewer_candidates_reason") {
		return false
	}
	var candidates []json.RawMessage
	if json.Unmarshal(root["candidates"], &candidates) != nil {
		return false
	}
	for _, rawCandidate := range candidates {
		var candidate map[string]json.RawMessage
		if json.Unmarshal(rawCandidate, &candidate) != nil || !hasExactJSONFields(candidate,
			"rank", "start_ms", "end_ms", "title_variants", "hook", "rationale", "boundary_rationale", "context", "editorial_score", "scores", "evidence") {
			return false
		}
		var scores map[string]json.RawMessage
		if json.Unmarshal(candidate["scores"], &scores) != nil || !hasExactJSONFields(scores,
			"hook", "emotional_impact", "retention_potential", "shareability", "standalone_context") {
			return false
		}
		var evidence []json.RawMessage
		if json.Unmarshal(candidate["evidence"], &evidence) != nil {
			return false
		}
		for _, rawEvidence := range evidence {
			var entry map[string]json.RawMessage
			if json.Unmarshal(rawEvidence, &entry) != nil || !hasExactJSONFields(entry, "kind", "start_ms", "end_ms", "detail") {
				return false
			}
		}
	}
	return true
}

// encoding/json replaces an unpaired escaped UTF-16 surrogate with U+FFFD.
// Reject such input before decoding so validation never alters model text.
func validClippingGeminiUnicodeEscapes(payload []byte) bool {
	inString := false
	for i := 0; i < len(payload); i++ {
		if !inString {
			if payload[i] == '"' {
				inString = true
			}
			continue
		}
		switch payload[i] {
		case '"':
			inString = false
		case '\\':
			i++
			if i >= len(payload) {
				return false
			}
			if payload[i] != 'u' {
				continue
			}
			if i+4 >= len(payload) {
				return false
			}
			value, ok := clippingGeminiHex16(payload[i+1 : i+5])
			if !ok {
				return false
			}
			i += 4
			if value >= 0xD800 && value <= 0xDBFF {
				if i+6 >= len(payload) || payload[i+1] != '\\' || payload[i+2] != 'u' {
					return false
				}
				low, ok := clippingGeminiHex16(payload[i+3 : i+7])
				if !ok || low < 0xDC00 || low > 0xDFFF {
					return false
				}
				i += 6
			} else if value >= 0xDC00 && value <= 0xDFFF {
				return false
			}
		}
	}
	return true
}

func clippingGeminiHex16(hex []byte) (uint16, bool) {
	var value uint16
	for _, digit := range hex {
		value <<= 4
		switch {
		case digit >= '0' && digit <= '9':
			value += uint16(digit - '0')
		case digit >= 'a' && digit <= 'f':
			value += uint16(digit-'a') + 10
		case digit >= 'A' && digit <= 'F':
			value += uint16(digit-'A') + 10
		default:
			return 0, false
		}
	}
	return value, true
}
