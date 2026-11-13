package app

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
	"time"
)

// Each surface should describe the same durable job, including after a state
// change. Detail alone may include artifacts; this fixture has none.
func TestClippingGeminiProjectionAgreesAcrossHTTPAndEvents(t *testing.T) {
	store, security, handler, cookie := clippingDashboardFixture(t)
	job, options := fixtureGeminiDashboardJob(t, store, security, "gemini-shared-projection")
	now := time.Now().Unix()
	retryAt := now + 90
	_, err := store.db.Exec(`INSERT INTO clipping_gemini_attempts(attempt_id,job_id,attempt_no,phase,outcome,failure_code,http_status,retry_at,lease_token_hash,lease_expires_at,started_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		"clipattempt_shared_projection", job.ID, 1, "settled", "explicit_rejection", "rate_limited", 429, retryAt, tokenHash("test-lease"), now+3600, now, now)
	if err != nil {
		t.Fatal(err)
	}
	events, unsubscribe := store.events.subscribe()
	defer unsubscribe()

	assertViews := func(eventJob clippingJobView) {
		t.Helper()
		assertGeminiDashboardProjection(t, eventJob, options)
		list := clippingDashboardRequest(handler, http.MethodGet, "/api/clipping/jobs", "", cookie, "")
		var listed struct {
			Jobs []clippingJobView `json:"jobs"`
		}
		if list.Code != http.StatusOK || json.Unmarshal(list.Body.Bytes(), &listed) != nil || len(listed.Jobs) != 1 {
			t.Fatalf("list=%d %s", list.Code, list.Body.String())
		}
		detail := clippingDashboardRequest(handler, http.MethodGet, "/api/clipping/jobs/"+job.ID, "", cookie, "")
		var detailed struct {
			Job clippingJobView `json:"job"`
		}
		if detail.Code != http.StatusOK || json.Unmarshal(detail.Body.Bytes(), &detailed) != nil {
			t.Fatalf("detail=%d %s", detail.Code, detail.Body.String())
		}
		if !reflect.DeepEqual(eventJob, listed.Jobs[0]) || !reflect.DeepEqual(eventJob, detailed.Job) {
			t.Fatalf("job views differ: event=%+v list=%+v detail=%+v", eventJob, listed.Jobs[0], detailed.Job)
		}
	}
	readEvent := func() clippingJobView {
		t.Helper()
		select {
		case event := <-events:
			var payload struct {
				Job clippingJobView `json:"job"`
			}
			if event.name != "clipping_job" || json.Unmarshal(event.data, &payload) != nil {
				t.Fatalf("event=%s %s", event.name, event.data)
			}
			return payload.Job
		default:
			t.Fatal("missing clipping job event")
			return clippingJobView{}
		}
	}

	store.PublishClippingJob(job.ID)
	waiting := readEvent()
	if waiting.RetrySafety != ClippingGeminiRetrySafetyRateLimitedWait || waiting.RetryAt != retryAt {
		t.Fatalf("retry state=%+v", waiting.ClippingGeminiLifecycleView)
	}
	assertViews(waiting)

	response := clippingDashboardRequest(handler, http.MethodPost, "/api/clipping/jobs/"+job.ID+"/cancel", "", cookie, "")
	var canceled struct {
		Job clippingJobView `json:"job"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &canceled) != nil {
		t.Fatalf("cancel=%d %s", response.Code, response.Body.String())
	}
	cancelEvent := readEvent()
	if canceled.Job.Status != ClippingJobCanceled || !reflect.DeepEqual(canceled.Job, cancelEvent) {
		t.Fatalf("cancel and event differ: cancel=%+v event=%+v", canceled.Job, cancelEvent)
	}
	assertViews(cancelEvent)
}
