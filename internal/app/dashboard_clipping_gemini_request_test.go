package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeClippingGeminiJobInputStrict(t *testing.T) {
	const options = `{"content_type":"general","min_clip_ms":15123,"max_clip_ms":170456,"candidate_limit":5}`
	const urlForm = `{"youtube_url":"https://youtu.be/abcdefghijk","rights_attested":true,"options":` + options + `}`
	const sourceForm = `{"source_id":"clipsrc_0123456789abcdef0123456789abcdef","options":` + options + `}`
	for _, tc := range []struct {
		name, body string
		want       int
	}{
		{"URL source", urlForm, http.StatusOK},
		{"existing source", sourceForm, http.StatusOK},
		{"both source forms", `{"source_id":"x","youtube_url":"x","rights_attested":true,"options":` + options + `}`, http.StatusBadRequest},
		{"neither source", `{"options":` + options + `}`, http.StatusBadRequest},
		{"existing source rights field", `{"source_id":"x","rights_attested":true,"options":` + options + `}`, http.StatusBadRequest},
		{"missing attestation", strings.Replace(urlForm, `"rights_attested":true,`, "", 1), http.StatusBadRequest},
		{"missing option", strings.Replace(urlForm, `,"candidate_limit":5`, "", 1), http.StatusBadRequest},
		{"duplicate top key", strings.Replace(urlForm, `"youtube_url":`, `"youtube_url":"x","youtube_url":`, 1), http.StatusBadRequest},
		{"duplicate option", strings.Replace(urlForm, `"min_clip_ms":15123`, `"min_clip_ms":15123,"min_clip_ms":15000`, 1), http.StatusBadRequest},
		{"case variant", strings.Replace(urlForm, `"youtube_url"`, `"YouTube_URL"`, 1), http.StatusBadRequest},
		{"case variant option", strings.Replace(urlForm, `"min_clip_ms"`, `"Min_Clip_MS"`, 1), http.StatusBadRequest},
		{"unknown routing field", strings.Replace(urlForm, `"options":`, `"project_id":"attacker","options":`, 1), http.StatusBadRequest},
		{"unknown option", strings.Replace(urlForm, `"candidate_limit":5`, `"candidate_limit":5,"model":"attacker"`, 1), http.StatusBadRequest},
		{"null", strings.Replace(urlForm, `"rights_attested":true`, `"rights_attested":null`, 1), http.StatusBadRequest},
		{"fractional milliseconds", strings.Replace(urlForm, `15123`, `15123.5`, 1), http.StatusBadRequest},
		{"exponent milliseconds", strings.Replace(urlForm, `15123`, `15123e0`, 1), http.StatusBadRequest},
		{"trailing object", urlForm + `{}`, http.StatusBadRequest},
		{"invalid UTF-8", strings.Replace(urlForm, "general", string([]byte{0xff}), 1), http.StatusBadRequest},
		{"unpaired surrogate", strings.Replace(urlForm, "general", `\ud800`, 1), http.StatusBadRequest},
		{"too large", urlForm + strings.Repeat(" ", MaxJSONBodyBytes), http.StatusRequestEntityTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/clipping/gemini/jobs", strings.NewReader(tc.body))
			resp := httptest.NewRecorder()
			input, ok := decodeClippingGeminiJobInput(resp, req)
			if resp.Code != tc.want || ok != (tc.want == http.StatusOK) {
				t.Fatalf("status=%d ok=%t, want %d; body=%s", resp.Code, ok, tc.want, resp.Body.String())
			}
			if ok && (input.Options.MinClipMS != 15123 || input.Options.MaxClipMS != 170456) {
				t.Fatalf("milliseconds changed: %+v", input.Options)
			}
		})
	}
}
