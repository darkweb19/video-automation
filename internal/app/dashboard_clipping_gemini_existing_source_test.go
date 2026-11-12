package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestClippingGeminiSubmitExistingSourceEligibility(t *testing.T) {
	enableGeminiDashboard(t)
	store, security, handler := directGeminiDashboardFixture(t)
	upload := makeReadyClippingSource(t, store, 60_000)
	body := func(sourceID string) string {
		return `{"source_id":"` + sourceID + `","options":` + geminiDashboardOptions + `}`
	}
	unsupported := postGeminiDashboard(handler, body(upload.ID), "gemini-upload-key", nil)
	if unsupported.Code != http.StatusConflict {
		t.Fatalf("upload source accepted: %d %s", unsupported.Code, unsupported.Body.String())
	}
	legacy := makeReadyClippingSource(t, store, 60_000)
	encrypted, err := security.EncryptSetting(clippingSourceURLSetting, geminiDashboardURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE clipping_sources SET kind='youtube_original_file',source_url=? WHERE id=?`, encrypted, legacy.ID); err != nil {
		t.Fatal(err)
	}
	accepted := postGeminiDashboard(handler, body(legacy.ID), "gemini-legacy-key", nil)
	if accepted.Code != http.StatusCreated {
		t.Fatalf("legacy YouTube source: %d %s", accepted.Code, accepted.Body.String())
	}
	var result struct {
		Job clippingJobView `json:"job"`
	}
	if err := json.Unmarshal(accepted.Body.Bytes(), &result); err != nil || result.Job.SourceID != legacy.ID {
		t.Fatalf("wrong source in result: %+v, %v", result.Job, err)
	}
	if strings.Contains(accepted.Body.String(), "abcdefghijk") || strings.Contains(accepted.Body.String(), encrypted) {
		t.Fatal("existing-source response exposed its URL or ciphertext")
	}
	jobs, err := store.ClippingJobs()
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs: %d, %v", len(jobs), err)
	}
}
