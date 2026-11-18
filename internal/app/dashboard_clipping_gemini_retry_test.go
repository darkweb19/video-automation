package app

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestClippingGeminiManualRetryIsNeverDispatchedThroughLegacy(t *testing.T) {
	store, security, handler, cookie := clippingDashboardFixture(t)
	job, _, err := store.CreateClippingGeminiJob(security, geminiJobInput(), "gemini-manual-retry")
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.ClippingStages(job.ID)
	if err != nil || len(before) != 1 {
		t.Fatalf("initial stages: %+v %v", before, err)
	}
	path := "/api/clipping/jobs/" + job.ID + "/retry"
	if response := clippingDashboardRequest(handler, http.MethodPost, path, `{}`, nil, ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated retry=%d", response.Code)
	}
	if response := clippingDashboardRequestWithHeaders(handler, http.MethodPost, path, `{}`, cookie, map[string]string{"Origin": "https://attacker.example"}); response.Code != http.StatusForbidden {
		t.Fatalf("cross-origin retry=%d", response.Code)
	}
	for _, body := range []string{`{}`, `{"stage_name":"analysis"}`, `{"stage_name":"unknown"}`} {
		response := clippingDashboardRequest(handler, http.MethodPost, path, body, cookie, "")
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "managed automatically") {
			t.Fatalf("manual retry=%d %s", response.Code, response.Body.String())
		}
	}
	after, err := store.ClippingStages(job.ID)
	if err != nil || len(after) != 1 || after[0].Status != before[0].Status || after[0].Attempt != before[0].Attempt || after[0].AttemptID != before[0].AttemptID {
		t.Fatalf("retry changed stage: before=%+v after=%+v err=%v", before, after, err)
	}
}

func TestClippingGeminiManualRetryCannotOverrideAnyLifecycleState(t *testing.T) {
	store, security, handler, cookie := clippingDashboardFixture(t)
	now := time.Now().Unix()
	for _, tc := range []struct {
		name    string
		status  ClippingJobStatus
		attempt int
		outcome string
		failure string
	}{
		{name: "queued", status: ClippingJobQueued},
		{name: "running", status: ClippingJobRunning},
		{name: "completed", status: ClippingJobCompleted},
		{name: "canceled", status: ClippingJobCanceled},
		{name: "unknown", status: ClippingJobFailed, attempt: 1, outcome: "unknown", failure: "outcome_unknown"},
		{name: "exhausted", status: ClippingJobFailed, attempt: clippingGeminiMaxSubmissionAttempts, outcome: "explicit_rejection", failure: "rate_limited"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			job, _ := fixtureGeminiDashboardJob(t, store, security, "gemini-retry-"+tc.name)
			if _, err := store.db.Exec(`UPDATE clipping_jobs SET status=? WHERE id=?`, tc.status, job.ID); err != nil {
				t.Fatal(err)
			}
			if tc.attempt > 0 {
				_, err := store.db.Exec(`INSERT INTO clipping_gemini_attempts(attempt_id,job_id,attempt_no,phase,outcome,failure_code,lease_token_hash,lease_expires_at,started_at,updated_at) VALUES(?,?,?,'settled',?,?,?,?,?,?)`,
					"clipattempt_retry_"+tc.name, job.ID, tc.attempt, tc.outcome, tc.failure, tokenHash("test-lease"), now+3600, now, now)
				if err != nil {
					t.Fatal(err)
				}
			}
			beforeJob, err := store.ClippingJob(job.ID)
			if err != nil {
				t.Fatal(err)
			}
			beforeStages, err := store.ClippingStages(job.ID)
			if err != nil {
				t.Fatal(err)
			}
			beforeAttempts, err := store.ClippingGeminiAttempts(job.ID)
			if err != nil {
				t.Fatal(err)
			}
			response := clippingDashboardRequest(handler, http.MethodPost, "/api/clipping/jobs/"+job.ID+"/retry", `{}`, cookie, "")
			if response.Code != http.StatusConflict {
				t.Fatalf("manual retry=%d %s", response.Code, response.Body.String())
			}
			afterJob, err := store.ClippingJob(job.ID)
			if err != nil {
				t.Fatal(err)
			}
			afterStages, err := store.ClippingStages(job.ID)
			if err != nil {
				t.Fatal(err)
			}
			afterAttempts, err := store.ClippingGeminiAttempts(job.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(beforeJob, afterJob) || !reflect.DeepEqual(beforeStages, afterStages) || !reflect.DeepEqual(beforeAttempts, afterAttempts) {
				t.Fatalf("manual retry changed durable job, stages, or attempts for %s", tc.name)
			}
		})
	}
}
