package app

import (
	"net/http"
	"strings"
	"testing"
)

func TestRetiredClippingRoutesRejectWithoutCreatingWork(t *testing.T) {
	store, _, handler, cookie := clippingDashboardFixture(t)
	source := makeReadyClippingSource(t, store, 60_000)
	job := makeClippingJob(t, store, source.ID, "retired-retry")
	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/api/clipping/sources/import"},
		{http.MethodPost, "/api/clipping/jobs"},
		{http.MethodPost, "/api/clipping/jobs/" + job.ID + "/retry"},
		{http.MethodGet, "/api/clipping/config/worker"},
		{http.MethodPut, "/api/clipping/config/worker"},
	} {
		t.Run(route.method+route.path, func(t *testing.T) {
			if response := clippingDashboardRequest(handler, route.method, route.path, `{}`, nil, ""); response.Code != http.StatusUnauthorized {
				t.Fatalf("unauthenticated request=%d", response.Code)
			}
			response := clippingDashboardRequest(handler, route.method, route.path, `{}`, cookie, "")
			if response.Code != http.StatusGone || !strings.Contains(strings.ToLower(response.Body.String()), "retired") {
				t.Fatalf("retired route=%d %s", response.Code, response.Body.String())
			}
			if route.method != http.MethodGet {
				response = clippingDashboardRequestWithHeaders(handler, route.method, route.path, `{}`, cookie, map[string]string{"Origin": "https://attacker.invalid"})
				if response.Code != http.StatusForbidden {
					t.Fatalf("cross-origin request=%d", response.Code)
				}
			}
		})
	}
	var sourceCount, jobCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM clipping_sources`).Scan(&sourceCount); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM clipping_jobs`).Scan(&jobCount); err != nil {
		t.Fatal(err)
	}
	if sourceCount != 1 || jobCount != 1 {
		t.Fatalf("retired routes created work: sources=%d jobs=%d", sourceCount, jobCount)
	}
}
