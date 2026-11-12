package app

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// clippingGeminiJobInput deliberately excludes every routing or credential
// field. Exactly one source form is accepted; source references already carry
// their original rights attestation.
type clippingGeminiJobInput struct {
	SourceID       string                `json:"source_id"`
	YouTubeURL     string                `json:"youtube_url"`
	RightsAttested bool                  `json:"rights_attested"`
	Options        ClippingGeminiOptions `json:"options"`
}

func decodeClippingGeminiJobInput(w http.ResponseWriter, r *http.Request) (clippingGeminiJobInput, bool) {
	var input clippingGeminiJobInput
	r.Body = http.MaxBytesReader(w, r.Body, MaxJSONBodyBytes)
	payload, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body is too large")
		} else {
			writeError(w, http.StatusBadRequest, "invalid JSON request body")
		}
		return input, false
	}
	// The shared strict decoder rejects duplicate fields, null, unknown
	// fields, trailing data and excessive nesting. The exact-shape check also
	// prevents encoding/json's case-insensitive field matching and omissions.
	if !validClippingGeminiUnicodeEscapes(payload) || decodeStructuredTextJSON(string(payload), &input) != nil || !exactClippingGeminiJobInputShape(payload) {
		writeError(w, http.StatusBadRequest, "invalid JSON request body")
		return clippingGeminiJobInput{}, false
	}
	return input, true
}

func exactClippingGeminiJobInputShape(payload []byte) bool {
	var root map[string]json.RawMessage
	if json.Unmarshal(payload, &root) != nil ||
		!(hasExactJSONFields(root, "source_id", "options") || hasExactJSONFields(root, "youtube_url", "rights_attested", "options")) {
		return false
	}
	var options map[string]json.RawMessage
	return json.Unmarshal(root["options"], &options) == nil &&
		hasExactJSONFields(options, "content_type", "min_clip_ms", "max_clip_ms", "candidate_limit")
}
