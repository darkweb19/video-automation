package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestClippingGeminiResultsPreserveDirectV3Artifact(t *testing.T) {
	store, security, handler, cookie := clippingDashboardFixture(t)
	origin, _ := geminiCacheJobs(t, store, security, "")
	publishGeminiCacheFixture(t, store, origin, time.Now().Unix())
	path := "/api/clipping/jobs/" + origin.ID + "/results"
	if response := clippingDashboardRequest(handler, http.MethodGet, path, "", nil, ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated results=%d", response.Code)
	}
	response := clippingDashboardRequest(handler, http.MethodGet, path, "", cookie, "")
	if response.Code != http.StatusOK {
		t.Fatalf("results=%d %s", response.Code, response.Body.String())
	}
	var result struct {
		JobID     string             `json:"job_id"`
		SourceID  string             `json:"source_id"`
		Artifacts []ClippingArtifact `json:"artifacts"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.JobID != origin.ID || result.SourceID != origin.SourceID || len(result.Artifacts) != 1 {
		t.Fatalf("result envelope: %+v %v", result, err)
	}
	artifact := result.Artifacts[0]
	if artifact.SchemaVersion != ClippingGeminiAnalysisVersion || artifact.PayloadJSON != string(readClippingGeminiFixture(t, "ranked.json")) {
		t.Fatal("v3 payload or exact schema identifier changed")
	}
	var payload map[string]json.RawMessage
	if json.Unmarshal([]byte(artifact.PayloadJSON), &payload) != nil || payload["candidates"] == nil || payload["analysis"] != nil {
		t.Fatalf("payload is not direct v3 analysis: %s", artifact.PayloadJSON)
	}
	detail := clippingDashboardRequest(handler, http.MethodGet, "/api/clipping/jobs/"+origin.ID, "", cookie, "")
	var jobDetail struct {
		Job clippingJobView `json:"job"`
	}
	if detail.Code != http.StatusOK || json.Unmarshal(detail.Body.Bytes(), &jobDetail) != nil || len(jobDetail.Job.Artifacts) != 1 || jobDetail.Job.Artifacts[0].PayloadJSON != artifact.PayloadJSON {
		t.Fatalf("detail artifact changed: %d %s", detail.Code, detail.Body.String())
	}
	if _, err := store.db.Exec(`UPDATE clipping_artifacts SET payload_json=? WHERE job_id=?`, `{"version":"framevault.clipping.analysis.v3","url":"https://provider.example/private"}`, origin.ID); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{path, "/api/clipping/jobs/" + origin.ID} {
		bad := clippingDashboardRequest(handler, http.MethodGet, target, "", cookie, "")
		if bad.Code != http.StatusInternalServerError || strings.Contains(bad.Body.String(), "provider.example") {
			t.Fatalf("corrupted artifact exposed: %d %s", bad.Code, bad.Body.String())
		}
	}
}

func TestClippingGeminiEmptyResultsRetainArtifactEnvelope(t *testing.T) {
	store, security, handler, cookie := clippingDashboardFixture(t)
	job, _ := fixtureGeminiDashboardJob(t, store, security, "gemini-empty-results")
	response := clippingDashboardRequest(handler, http.MethodGet, "/api/clipping/jobs/"+job.ID+"/results", "", cookie, "")
	var result struct {
		Artifacts []ClippingArtifact `json:"artifacts"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &result) != nil || result.Artifacts == nil || len(result.Artifacts) != 0 {
		t.Fatalf("empty results=%d %s", response.Code, response.Body.String())
	}
}
