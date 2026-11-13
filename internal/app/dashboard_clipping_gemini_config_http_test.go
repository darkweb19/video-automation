package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestClippingConfigDescribesGeminiWithoutReadinessProbe(t *testing.T) {
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "/nonexistent/credentials-file")
	t.Setenv(clippingGeminiEnabledEnv, "true")
	t.Setenv(clippingGeminiProjectEnv, "private-project-name")
	t.Setenv(clippingGeminiLocationEnv, "global")
	_, _, handler, cookie := clippingDashboardFixture(t)
	if response := clippingDashboardRequest(handler, http.MethodGet, "/api/clipping/config", "", nil, ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated config=%d", response.Code)
	}
	response := clippingDashboardRequest(handler, http.MethodGet, "/api/clipping/config", "", cookie, "")
	if response.Code != http.StatusOK {
		t.Fatalf("config: %d %s", response.Code, response.Body.String())
	}
	var result struct {
		Gemini      clippingGeminiConfigView `json:"gemini"`
		WorkerState string                   `json:"worker_state"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Gemini.Configured || !result.Gemini.Enabled || result.Gemini.Verification != "unverified" ||
		result.Gemini.Limits.MinClipMS != ClippingGeminiMinClipMS || result.Gemini.Limits.MaxClipMS != ClippingGeminiMaxClipMS ||
		result.Gemini.Limits.MaxCandidates != ClippingGeminiMaxCandidates || result.Gemini.Limits.MaxSourceDurationMS != clippingGeminiMaxRequestDurationMS ||
		result.WorkerState != "unavailable" {
		t.Fatalf("config = %+v", result)
	}
	for _, forbidden := range []string{"private-project-name", "/nonexistent/credentials-file", "global", "aiplatform"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Errorf("config exposes %q: %s", forbidden, response.Body.String())
		}
	}
	t.Setenv(clippingGeminiEnabledEnv, "false")
	response = clippingDashboardRequest(handler, http.MethodGet, "/api/clipping/config", "", cookie, "")
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || !result.Gemini.Configured || result.Gemini.Enabled {
		t.Fatalf("disabled config = %+v, %v", result.Gemini, err)
	}
	t.Setenv(clippingGeminiEnabledEnv, "true")
	t.Setenv(clippingGeminiLocationEnv, "https://provider.invalid/private")
	response = clippingDashboardRequest(handler, http.MethodGet, "/api/clipping/config", "", cookie, "")
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.Gemini.Configured || !result.Gemini.Enabled || result.Gemini.Verification != "unverified" {
		t.Fatalf("invalid config = %+v, %v", result.Gemini, err)
	}
	for _, forbidden := range []string{"provider.invalid", "private-project-name", "/nonexistent/credentials-file"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Errorf("invalid config exposes %q: %s", forbidden, response.Body.String())
		}
	}
}
