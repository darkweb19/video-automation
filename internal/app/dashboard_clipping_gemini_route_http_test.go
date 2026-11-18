package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClippingGeminiSubmitRouteAuthenticationAndOrigin(t *testing.T) {
	enableGeminiDashboard(t)
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "/nonexistent/credentials-file")
	store, _, handler, cookie := clippingDashboardFixture(t)
	const path = "/api/clipping/gemini/jobs"
	request := func(body string, session *http.Cookie, headers map[string]string) *httptest.ResponseRecorder {
		if headers == nil {
			headers = map[string]string{}
		}
		headers["Idempotency-Key"] = "gemini-routed-request"
		return clippingDashboardRequestWithHeaders(handler, http.MethodPost, path, body, session, headers)
	}
	assertRejected := func(response *httptest.ResponseRecorder, want int) {
		t.Helper()
		if response.Code != want {
			t.Fatalf("status=%d want=%d body=%s", response.Code, want, response.Body.String())
		}
		for _, forbidden := range []string{"abcdefghijk", "youtu.be", "/nonexistent/credentials-file", testClippingGeminiConfig().ProjectID} {
			if strings.Contains(response.Body.String(), forbidden) {
				t.Fatalf("response exposed %q: %s", forbidden, response.Body.String())
			}
		}
	}
	assertRejected(request(geminiDashboardURLBody(), nil, nil), http.StatusUnauthorized)
	assertRejected(request(geminiDashboardURLBody(), cookie, map[string]string{"Origin": "https://attacker.example"}), http.StatusForbidden)
	assertRejected(request(geminiDashboardURLBody(), cookie, map[string]string{"Origin": "https://example.com/path"}), http.StatusForbidden)
	invalid := strings.Replace(geminiDashboardURLBody(), `"candidate_limit":5`, `"candidate_limit":0`, 1)
	assertRejected(request(invalid, cookie, nil), http.StatusBadRequest)
	if _, err := store.db.Exec(`UPDATE users SET must_change_password=1 WHERE username='sujanshrestha'`); err != nil {
		t.Fatal(err)
	}
	assertRejected(request(geminiDashboardURLBody(), cookie, nil), http.StatusPreconditionRequired)
	if _, err := store.db.Exec(`UPDATE users SET must_change_password=0 WHERE username='sujanshrestha'`); err != nil {
		t.Fatal(err)
	}
	t.Setenv(clippingGeminiEnabledEnv, "false")
	assertRejected(request(geminiDashboardURLBody(), cookie, nil), http.StatusServiceUnavailable)
	t.Setenv(clippingGeminiEnabledEnv, "true")
	jobs, err := store.ClippingJobs()
	if err != nil || len(jobs) != 0 {
		t.Fatalf("rejected requests created jobs: %v, %v", jobs, err)
	}
	sources, err := store.ClippingSources()
	if err != nil || len(sources) != 0 {
		t.Fatalf("rejected requests created sources: %v, %v", sources, err)
	}
	created := request(geminiDashboardURLBody(), cookie, map[string]string{"Origin": "https://example.com"})
	var first struct {
		Job       clippingJobView `json:"job"`
		Duplicate bool            `json:"duplicate"`
	}
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &first) != nil || first.Duplicate || first.Job.Provider != ClippingProviderGemini || first.Job.Options == nil || first.Job.Options.MinClipMS != 15123 || first.Job.RetrySafety == "" {
		t.Fatalf("routed create=%d %s", created.Code, created.Body.String())
	}
	if strings.Contains(created.Body.String(), "abcdefghijk") || strings.Contains(created.Body.String(), testClippingGeminiConfig().ProjectID) {
		t.Fatalf("routed create exposed source or routing: %s", created.Body.String())
	}
	replayed := request(geminiDashboardURLBody(), cookie, nil)
	var replay struct {
		Job       clippingJobView `json:"job"`
		Duplicate bool            `json:"duplicate"`
	}
	if replayed.Code != http.StatusOK || json.Unmarshal(replayed.Body.Bytes(), &replay) != nil || !replay.Duplicate || replay.Job.ID != first.Job.ID || replay.Job.SourceID != first.Job.SourceID {
		t.Fatalf("routed replay=%d %s", replayed.Code, replayed.Body.String())
	}
	existingBody := `{"source_id":"` + first.Job.SourceID + `","options":` + geminiDashboardOptions + `}`
	existing := clippingDashboardRequestWithHeaders(handler, http.MethodPost, path, existingBody, cookie, map[string]string{"Idempotency-Key": "gemini-routed-existing"})
	var existingResult struct {
		Job clippingJobView `json:"job"`
	}
	if existing.Code != http.StatusCreated || json.Unmarshal(existing.Body.Bytes(), &existingResult) != nil || existingResult.Job.SourceID != first.Job.SourceID || existingResult.Job.ID == first.Job.ID {
		t.Fatalf("routed existing source=%d %s", existing.Code, existing.Body.String())
	}
	sources, err = store.ClippingSources()
	if err != nil || len(sources) != 1 {
		t.Fatalf("existing source submission created an extra source: %v, %v", sources, err)
	}
}
