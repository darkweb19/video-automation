package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const geminiDashboardOptions = `{"content_type":"general","min_clip_ms":15123,"max_clip_ms":170456,"candidate_limit":5}`
const geminiDashboardURL = "https://youtu.be/abcdefghijk"

func geminiDashboardURLBody() string {
	return `{"youtube_url":"` + geminiDashboardURL + `","rights_attested":true,"options":` + geminiDashboardOptions + `}`
}

func enableGeminiDashboard(t *testing.T) {
	t.Helper()
	config := testClippingGeminiConfig()
	t.Setenv(clippingGeminiEnabledEnv, "true")
	t.Setenv(clippingGeminiProjectEnv, config.ProjectID)
	t.Setenv(clippingGeminiLocationEnv, config.Location)
}

func directGeminiDashboardFixture(t *testing.T) (*Store, *Security, http.Handler) {
	t.Helper()
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	app := &dashboardApp{store: store, security: security}
	return store, security, http.HandlerFunc(app.createClippingGeminiJob)
}

func postGeminiDashboard(handler http.Handler, body, key string, headers map[string]string) *httptest.ResponseRecorder {
	if headers == nil {
		headers = make(map[string]string)
	}
	headers["Idempotency-Key"] = key
	return clippingDashboardRequestWithHeaders(handler, http.MethodPost, "/api/clipping/gemini/jobs", body, nil, headers)
}

func TestClippingGeminiSubmitQueuesAtomicallyAndReplays(t *testing.T) {
	enableGeminiDashboard(t)
	store, _, handler := directGeminiDashboardFixture(t)
	first := postGeminiDashboard(handler, geminiDashboardURLBody(), "gemini-dashboard-key", nil)
	if first.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", first.Code, first.Body.String())
	}
	var result struct {
		Job       clippingJobView `json:"job"`
		Duplicate bool            `json:"duplicate"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Duplicate || result.Job.Provider != ClippingProviderGemini || result.Job.Status != ClippingJobQueued {
		t.Fatalf("created job: %+v", result)
	}
	snapshot, err := store.ClippingGeminiRequestSnapshot(result.Job.ID)
	if err != nil || snapshot.Options.MinClipMS != 15123 || snapshot.Options.MaxClipMS != 170456 || snapshot.Config != testClippingGeminiConfig() {
		t.Fatalf("snapshot: %+v, %v", snapshot, err)
	}
	source, err := store.ClippingSource(result.Job.SourceID)
	if err != nil || source.Kind != ClippingSourceYouTubeURL || !source.RightsAttested || source.RetainUntil <= source.CreatedAt {
		t.Fatalf("source: %+v, %v", source, err)
	}
	if strings.Contains(first.Body.String(), "abcdefghijk") || strings.Contains(first.Body.String(), testClippingGeminiConfig().ProjectID) {
		t.Fatalf("create response exposed source or routing: %s", first.Body.String())
	}
	replay := postGeminiDashboard(handler, geminiDashboardURLBody(), "gemini-dashboard-key", nil)
	if replay.Code != http.StatusOK {
		t.Fatalf("replay: %d %s", replay.Code, replay.Body.String())
	}
	var repeated struct {
		Job       clippingJobView `json:"job"`
		Duplicate bool            `json:"duplicate"`
	}
	if err := json.Unmarshal(replay.Body.Bytes(), &repeated); err != nil || !repeated.Duplicate || repeated.Job.ID != result.Job.ID || repeated.Job.SourceID != result.Job.SourceID {
		t.Fatalf("replay: %+v, %v", repeated, err)
	}
	conflictBody := strings.Replace(geminiDashboardURLBody(), `"candidate_limit":5`, `"candidate_limit":4`, 1)
	conflict := postGeminiDashboard(handler, conflictBody, "gemini-dashboard-key", nil)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("changed replay: %d %s", conflict.Code, conflict.Body.String())
	}
	jobs, err := store.ClippingJobs()
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs after replay: %d, %v", len(jobs), err)
	}
}

func TestClippingGeminiSubmitAcceptsExistingReference(t *testing.T) {
	enableGeminiDashboard(t)
	store, security, handler := directGeminiDashboardFixture(t)
	source, err := store.CreateClippingYouTubeReference(security, geminiDashboardURL, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"source_id":"` + source.ID + `","options":` + geminiDashboardOptions + `}`
	response := postGeminiDashboard(handler, body, "gemini-existing-key", nil)
	if response.Code != http.StatusCreated {
		t.Fatalf("existing source: %d %s", response.Code, response.Body.String())
	}
	var result struct {
		Job clippingJobView `json:"job"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.Job.SourceID != source.ID {
		t.Fatalf("source in response: %+v, %v", result.Job, err)
	}
	sources, err := store.ClippingSources()
	if err != nil || len(sources) != 1 {
		t.Fatalf("extra source created: %d, %v", len(sources), err)
	}
}

func TestClippingGeminiSubmitRejectsWithoutSideEffects(t *testing.T) {
	enableGeminiDashboard(t)
	store, _, handler := directGeminiDashboardFixture(t)
	for _, tc := range []struct {
		name, body, key string
		headers         map[string]string
		want            int
	}{
		{"cross origin", geminiDashboardURLBody(), "gemini-reject-key", map[string]string{"Origin": "https://attacker.example"}, http.StatusForbidden},
		{"missing key", geminiDashboardURLBody(), "", nil, http.StatusBadRequest},
		{"no rights", strings.Replace(geminiDashboardURLBody(), `"rights_attested":true`, `"rights_attested":false`, 1), "gemini-reject-key", nil, http.StatusUnprocessableEntity},
		{"private URL", strings.Replace(geminiDashboardURLBody(), geminiDashboardURL, "https://private.example/secret", 1), "gemini-reject-key", nil, http.StatusBadRequest},
		{"invalid options", strings.Replace(geminiDashboardURLBody(), `"candidate_limit":5`, `"candidate_limit":0`, 1), "gemini-reject-key", nil, http.StatusBadRequest},
		{"missing source", `{"source_id":"clipsrc_missing","options":` + geminiDashboardOptions + `}`, "gemini-reject-key", nil, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := postGeminiDashboard(handler, tc.body, tc.key, tc.headers)
			if response.Code != tc.want {
				t.Fatalf("status=%d want=%d body=%s", response.Code, tc.want, response.Body.String())
			}
			for _, forbidden := range []string{"abcdefghijk", "private.example", "secret", testClippingGeminiConfig().ProjectID} {
				if strings.Contains(response.Body.String(), forbidden) {
					t.Errorf("response exposed %q: %s", forbidden, response.Body.String())
				}
			}
		})
	}
	jobs, err := store.ClippingJobs()
	if err != nil || len(jobs) != 0 {
		t.Fatalf("jobs after rejections: %d, %v", len(jobs), err)
	}
	sources, err := store.ClippingSources()
	if err != nil || len(sources) != 0 {
		t.Fatalf("sources after rejections: %d, %v", len(sources), err)
	}
}

func TestClippingGeminiSubmitRequiresServerOptIn(t *testing.T) {
	store, _, handler := directGeminiDashboardFixture(t)
	t.Setenv(clippingGeminiEnabledEnv, "")
	t.Setenv(clippingGeminiProjectEnv, "")
	t.Setenv(clippingGeminiLocationEnv, "")
	for _, env := range []struct{ enabled, project, location string }{
		{"", "", ""},
		{"true", "", "global"},
		{"false", "example-project", "global"},
	} {
		t.Setenv(clippingGeminiEnabledEnv, env.enabled)
		t.Setenv(clippingGeminiProjectEnv, env.project)
		t.Setenv(clippingGeminiLocationEnv, env.location)
		response := postGeminiDashboard(handler, geminiDashboardURLBody(), "gemini-disabled-key", nil)
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("opt-in %q: %d %s", env.enabled, response.Code, response.Body.String())
		}
	}
	jobs, err := store.ClippingJobs()
	if err != nil || len(jobs) != 0 {
		t.Fatalf("disabled created jobs: %d, %v", len(jobs), err)
	}
}
