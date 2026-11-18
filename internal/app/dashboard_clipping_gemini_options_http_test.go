package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestClippingGeminiSubmitRejectsMalformedOptionsWithoutSideEffects(t *testing.T) {
	enableGeminiDashboard(t)
	store, _, handler := directGeminiDashboardFixture(t)
	valid := geminiDashboardURLBody()
	for _, tc := range []struct {
		name, body string
	}{
		{"below minimum", strings.Replace(valid, `"min_clip_ms":15123`, `"min_clip_ms":14999`, 1)},
		{"above maximum", strings.Replace(valid, `"max_clip_ms":170456`, `"max_clip_ms":180001`, 1)},
		{"inverted durations", strings.Replace(valid, `"min_clip_ms":15123`, `"min_clip_ms":175000`, 1)},
		{"too many candidates", strings.Replace(valid, `"candidate_limit":5`, `"candidate_limit":11`, 1)},
		{"unknown content type", strings.Replace(valid, `"general"`, `"secret-content-type"`, 1)},
		{"fractional duration", strings.Replace(valid, `15123`, `15123.5`, 1)},
		{"duplicate option", strings.Replace(valid, `"candidate_limit":5`, `"candidate_limit":5,"candidate_limit":1`, 1)},
		{"missing option", strings.Replace(valid, `,"candidate_limit":5`, "", 1)},
		{"client routing", strings.Replace(valid, `"options":`, `"model":"secret-model","options":`, 1)},
		{"malformed unicode", strings.Replace(valid, `general`, `\ud800`, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := postGeminiDashboard(handler, tc.body, "gemini-invalid-options", nil)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			for _, forbidden := range []string{"abcdefghijk", "youtu.be", "secret-content-type", "secret-model", testClippingGeminiConfig().ProjectID} {
				if strings.Contains(response.Body.String(), forbidden) {
					t.Fatalf("invalid request echoed %q: %s", forbidden, response.Body.String())
				}
			}
		})
	}
	jobs, err := store.ClippingJobs()
	if err != nil || len(jobs) != 0 {
		t.Fatalf("invalid options created jobs: %v, %v", jobs, err)
	}
	sources, err := store.ClippingSources()
	if err != nil || len(sources) != 0 {
		t.Fatalf("invalid options created sources: %v, %v", sources, err)
	}
}

func TestClippingGeminiSubmitPreservesOptionBoundaries(t *testing.T) {
	enableGeminiDashboard(t)
	store, _, handler := directGeminiDashboardFixture(t)
	for _, tc := range []struct {
		name    string
		options string
		want    ClippingGeminiOptions
	}{
		{"minimum", `{"content_type":"general","min_clip_ms":15000,"max_clip_ms":15000,"candidate_limit":1}`, ClippingGeminiOptions{ContentType: "general", MinClipMS: 15000, MaxClipMS: 15000, CandidateLimit: 1}},
		{"maximum", `{"content_type":"movie","min_clip_ms":180000,"max_clip_ms":180000,"candidate_limit":10}`, ClippingGeminiOptions{ContentType: "movie", MinClipMS: 180000, MaxClipMS: 180000, CandidateLimit: 10}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"youtube_url":"` + geminiDashboardURL + `","rights_attested":true,"options":` + tc.options + `}`
			response := postGeminiDashboard(handler, body, "gemini-options-"+tc.name, nil)
			var result struct {
				Job clippingJobView `json:"job"`
			}
			if response.Code != http.StatusCreated || json.Unmarshal(response.Body.Bytes(), &result) != nil || result.Job.Options == nil || *result.Job.Options != tc.want {
				t.Fatalf("option boundary response=%d %s", response.Code, response.Body.String())
			}
			snapshot, err := store.ClippingGeminiRequestSnapshot(result.Job.ID)
			if err != nil || snapshot.Options != tc.want {
				t.Fatalf("option boundary snapshot=%+v error=%v", snapshot.Options, err)
			}
		})
	}
}
