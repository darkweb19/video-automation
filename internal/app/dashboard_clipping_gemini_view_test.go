package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func fixtureGeminiDashboardJob(t *testing.T, store *Store, security *Security, key string) (ClippingJob, ClippingGeminiOptions) {
	t.Helper()
	input := geminiJobInput()
	input.Options.MinClipMS = 15123
	input.Options.MaxClipMS = 170456
	job, _, err := store.CreateClippingGeminiJob(security, input, key)
	if err != nil {
		t.Fatal(err)
	}
	return job, input.Options
}

func assertGeminiDashboardProjection(t *testing.T, job clippingJobView, expected ClippingGeminiOptions) {
	t.Helper()
	if job.Provider != ClippingProviderGemini || job.Options == nil || *job.Options != expected || job.ClippingGeminiLifecycleView == nil || job.RetrySafety == "" || job.MinClipSeconds != nil || job.MaxClipSeconds != nil {
		t.Fatalf("Gemini projection=%+v", job)
	}
}

func TestClippingGeminiJobViewsAndLiveEventsShareSafeProjection(t *testing.T) {
	store, security, handler, cookie := clippingDashboardFixture(t)
	job, options := fixtureGeminiDashboardJob(t, store, security, "gemini-view-test")
	if _, err := store.db.Exec(`UPDATE clipping_jobs SET status='failed',error='https://provider.example/private' WHERE id=?`, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE clipping_stages SET status='failed',error='raw credential secret' WHERE job_id=?`, job.ID); err != nil {
		t.Fatal(err)
	}
	path := "/api/clipping/jobs/" + job.ID
	if response := clippingDashboardRequest(handler, http.MethodGet, path, "", nil, ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated detail=%d", response.Code)
	}
	response := clippingDashboardRequest(handler, http.MethodGet, path, "", cookie, "")
	if response.Code != http.StatusOK {
		t.Fatalf("detail=%d %s", response.Code, response.Body.String())
	}
	var detail struct {
		Job clippingJobView `json:"job"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	assertGeminiDashboardProjection(t, detail.Job, options)
	if detail.Job.RetrySafety != ClippingGeminiRetrySafetyTerminal || detail.Job.ErrorCode != "analysis_failed" || detail.Job.Error != "analysis_failed" || len(detail.Job.Stages) != 1 || detail.Job.Stages[0].Error != "analysis_failed" {
		t.Fatalf("unsafe error projection: %+v", detail.Job)
	}
	for _, forbidden := range []string{"provider.example", "credential secret", "abcdefghijk", testClippingGeminiConfig().ProjectID} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Errorf("detail exposed %q: %s", forbidden, response.Body.String())
		}
	}
	list := clippingDashboardRequest(handler, http.MethodGet, "/api/clipping/jobs", "", cookie, "")
	var listed struct {
		Jobs []clippingJobView `json:"jobs"`
	}
	if list.Code != http.StatusOK || json.Unmarshal(list.Body.Bytes(), &listed) != nil || len(listed.Jobs) != 1 {
		t.Fatalf("list=%d %s", list.Code, list.Body.String())
	}
	assertGeminiDashboardProjection(t, listed.Jobs[0], options)
	events, unsubscribe := store.events.subscribe()
	defer unsubscribe()
	store.PublishClippingJob(job.ID)
	select {
	case event := <-events:
		var payload struct {
			Job clippingJobView `json:"job"`
		}
		if event.name != "clipping_job" || json.Unmarshal(event.data, &payload) != nil {
			t.Fatalf("event=%s %s", event.name, event.data)
		}
		assertGeminiDashboardProjection(t, payload.Job, options)
		if strings.Contains(string(event.data), "provider.example") || strings.Contains(string(event.data), "credential secret") {
			t.Fatalf("event exposed provider text: %s", event.data)
		}
	default:
		t.Fatal("missing clipping job event")
	}
}

func TestClippingGeminiCancelResponseIncludesOptionsAndLifecycle(t *testing.T) {
	store, security, handler, cookie := clippingDashboardFixture(t)
	job, options := fixtureGeminiDashboardJob(t, store, security, "gemini-cancel-view")
	response := clippingDashboardRequest(handler, http.MethodPost, "/api/clipping/jobs/"+job.ID+"/cancel", "", cookie, "")
	if response.Code != http.StatusOK {
		t.Fatalf("cancel=%d %s", response.Code, response.Body.String())
	}
	var result struct {
		Job clippingJobView `json:"job"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	assertGeminiDashboardProjection(t, result.Job, options)
	if result.Job.Status != ClippingJobCanceled || result.Job.Error != "" || len(result.Job.Stages) != 1 || result.Job.Stages[0].Error != "" {
		t.Fatalf("canceled projection: %+v", result.Job)
	}
}

func TestLegacyClippingJobProjectionDoesNotGainGeminiFields(t *testing.T) {
	legacy := clippingJobSummary(ClippingJob{Provider: ClippingProviderModalLegacy, MinClipSeconds: 15, MaxClipSeconds: 180}, nil, nil)
	encoded, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{`"options"`, `"retry_safety"`, `"usage"`, `"error_code"`} {
		if strings.Contains(string(encoded), forbidden) {
			t.Errorf("legacy view acquired %s: %s", forbidden, encoded)
		}
	}
}
