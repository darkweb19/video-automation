package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClippingGeminiSubmitRejectsNonJSONContentType(t *testing.T) {
	enableGeminiDashboard(t)
	store, _, handler := directGeminiDashboardFixture(t)
	for _, contentType := range []string{"", "text/plain", "application/x-www-form-urlencoded"} {
		request := httptest.NewRequest(http.MethodPost, "/api/clipping/gemini/jobs", strings.NewReader(geminiDashboardURLBody()))
		request.Header.Set("Content-Type", contentType)
		request.Header.Set("Idempotency-Key", "gemini-content-type")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnsupportedMediaType {
			t.Fatalf("Content-Type %q returned %d", contentType, response.Code)
		}
	}
	jobs, err := store.ClippingJobs()
	if err != nil || len(jobs) != 0 {
		t.Fatalf("non-JSON created jobs: %v, %v", jobs, err)
	}
}

func TestClippingGeminiSubmitDoesNotExposeDatabaseErrors(t *testing.T) {
	enableGeminiDashboard(t)
	store, _, handler := directGeminiDashboardFixture(t)
	if _, err := store.db.Exec(`CREATE TRIGGER reject_dashboard_gemini BEFORE INSERT ON clipping_jobs WHEN NEW.provider='gemini' BEGIN SELECT RAISE(ABORT,'https://internal.example/secret'); END`); err != nil {
		t.Fatal(err)
	}
	response := postGeminiDashboard(handler, geminiDashboardURLBody(), "gemini-internal-error", nil)
	if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), "internal.example") || strings.Contains(response.Body.String(), "secret") || strings.Contains(response.Body.String(), "abcdefghijk") {
		t.Fatalf("unsafe database error: %d %s", response.Code, response.Body.String())
	}
	jobs, err := store.ClippingJobs()
	if err != nil || len(jobs) != 0 {
		t.Fatalf("failed transaction left jobs: %v, %v", jobs, err)
	}
	sources, err := store.ClippingSources()
	if err != nil || len(sources) != 0 {
		t.Fatalf("failed transaction left sources: %v, %v", sources, err)
	}
}
