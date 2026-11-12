package app

import (
	"net/http"
	"strings"
	"testing"
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
