package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestClippingGeminiLifecycleHTTPPreservesRetrySafety(t *testing.T) {
	store, security, handler, cookie := clippingDashboardFixture(t)
	now := time.Now().Unix()
	for i, tc := range []struct {
		name                    string
		status                  ClippingJobStatus
		phase, outcome, failure string
		attempt, httpStatus     int
		retryAt                 int64
		wantSafety, wantError   string
	}{
		{"in flight", ClippingJobRunning, "submitting", "not_submitted", "", 1, 0, now + 60, "none", ""},
		{"not submitted", ClippingJobQueued, "settled", "not_submitted", "request_not_submitted", 1, 0, 0, "not_submitted_retryable", "request_not_submitted"},
		{"rate limited", ClippingJobQueued, "settled", "explicit_rejection", "rate_limited", 1, 429, now + 60, "rate_limited_wait", "rate_limited"},
		{"rate limit exhausted", ClippingJobFailed, "settled", "explicit_rejection", "rate_limited", clippingGeminiMaxSubmissionAttempts, 429, now + 60, "terminal", "rate_limited"},
		{"invalid response", ClippingJobFailed, "settled", "response_received", "invalid_response", 1, 200, now + 60, "response_received_terminal", "invalid_response"},
		{"unknown outcome", ClippingJobFailed, "settled", "unknown", "outcome_unknown", 1, 0, now + 60, "outcome_unknown_no_replay", "outcome_unknown"},
		{"other rejection", ClippingJobFailed, "settled", "explicit_rejection", "https://provider.example/secret", 1, 400, 0, "terminal", "analysis_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			job, _ := fixtureGeminiDashboardJob(t, store, security, "gemini-http-lifecycle-"+string(rune('a'+i)))
			if _, err := store.db.Exec(`UPDATE clipping_jobs SET status=? WHERE id=?`, tc.status, job.ID); err != nil {
				t.Fatal(err)
			}
			_, err := store.db.Exec(`INSERT INTO clipping_gemini_attempts(attempt_id,job_id,attempt_no,phase,outcome,failure_code,http_status,retry_at,lease_token_hash,lease_expires_at,started_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
				"clipattempt_"+string(rune('a'+i)), job.ID, tc.attempt, tc.phase, tc.outcome, tc.failure, tc.httpStatus, tc.retryAt, tokenHash("test-lease"), now+3600, now, now)
			if err != nil {
				t.Fatal(err)
			}
			response := clippingDashboardRequest(handler, http.MethodGet, "/api/clipping/jobs/"+job.ID, "", cookie, "")
			if response.Code != http.StatusOK {
				t.Fatalf("detail=%d %s", response.Code, response.Body.String())
			}
			var result struct {
				Job map[string]any `json:"job"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.Job["retry_safety"] != tc.wantSafety {
				t.Errorf("retry_safety=%v want %q", result.Job["retry_safety"], tc.wantSafety)
			}
			if tc.wantError == "" {
				if _, exists := result.Job["error_code"]; exists {
					t.Errorf("unexpected error_code: %v", result.Job["error_code"])
				}
			} else if result.Job["error_code"] != tc.wantError {
				t.Errorf("error_code=%v want %q", result.Job["error_code"], tc.wantError)
			}
			if tc.wantSafety == "rate_limited_wait" {
				if result.Job["retry_at"] != float64(tc.retryAt) {
					t.Errorf("retry_at=%v want %d", result.Job["retry_at"], tc.retryAt)
				}
			} else if _, exists := result.Job["retry_at"]; exists {
				t.Errorf("retry_at exposed for %s", tc.wantSafety)
			}
			if strings.Contains(response.Body.String(), "provider.example") || strings.Contains(response.Body.String(), "secret") {
				t.Fatalf("response exposed provider failure text: %s", response.Body.String())
			}
		})
	}
}
