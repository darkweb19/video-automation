package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClippingGeminiSubmitOriginGuard(t *testing.T) {
	enableGeminiDashboard(t)
	store, _, handler := directGeminiDashboardFixture(t)
	request := func(origins ...string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/clipping/gemini/jobs", strings.NewReader(geminiDashboardURLBody()))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "gemini-origin-test")
		for _, origin := range origins {
			req.Header.Add("Origin", origin)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	for _, tc := range []struct {
		name    string
		origins []string
	}{
		{"cross host", []string{"https://attacker.example"}},
		{"null", []string{"null"}},
		{"empty", []string{""}},
		{"userinfo", []string{"https://user@example.com"}},
		{"path", []string{"https://example.com/path"}},
		{"query", []string{"https://example.com?x=1"}},
		{"fragment", []string{"https://example.com#fragment"}},
		{"multiple", []string{"https://example.com", "https://attacker.example"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := request(tc.origins...)
			if response.Code != http.StatusForbidden || strings.Contains(response.Body.String(), "abcdefghijk") {
				t.Fatalf("origin rejection=%d %s", response.Code, response.Body.String())
			}
		})
	}
	jobs, err := store.ClippingJobs()
	if err != nil || len(jobs) != 0 {
		t.Fatalf("rejected origins created jobs: %v, %v", jobs, err)
	}
	sources, err := store.ClippingSources()
	if err != nil || len(sources) != 0 {
		t.Fatalf("rejected origins created sources: %v, %v", sources, err)
	}
	// The app may sit behind TLS termination, so the established check accepts
	// either scheme when the Origin host matches the request host.
	for _, origins := range [][]string{nil, {"http://example.com"}, {"https://example.com"}} {
		response := request(origins...)
		if response.Code != http.StatusCreated && response.Code != http.StatusOK {
			t.Fatalf("matching origin %q rejected: %d %s", origins, response.Code, response.Body.String())
		}
	}
}
