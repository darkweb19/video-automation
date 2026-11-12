package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestClippingReferenceViewOmitsUnverifiedMediaFields(t *testing.T) {
	store, security, handler, cookie := clippingDashboardFixture(t)
	source, err := store.CreateClippingYouTubeReference(security, "https://youtu.be/abcdefghijk", true, 0)
	if err != nil {
		t.Fatal(err)
	}
	response := clippingDashboardRequest(handler, http.MethodGet, "/api/clipping/sources/"+source.ID, "", cookie, "")
	if response.Code != http.StatusOK {
		t.Fatalf("reference detail: %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "abcdefghijk") || strings.Contains(response.Body.String(), "youtube.com/watch") {
		t.Fatal("reference detail exposed URL or video ID")
	}
	var body struct {
		Source map[string]any `json:"source"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"media_type", "size_bytes", "duration_ms", "sha256", "media_url"} {
		if _, present := body.Source[field]; present {
			t.Errorf("reference exposes unverified file field %q", field)
		}
	}
	if response := clippingDashboardRequest(handler, http.MethodGet, "/api/clipping/sources/"+source.ID+"/media", "", cookie, ""); response.Code != http.StatusNotFound {
		t.Fatalf("reference media endpoint returned %d", response.Code)
	}
	legacy := ClippingSource{Kind: ClippingSourceYouTube, Status: ClippingSourceImporting}
	encoded, err := json.Marshal(clippingSourceSummary(legacy))
	if err != nil {
		t.Fatal(err)
	}
	var legacyView map[string]any
	if err := json.Unmarshal(encoded, &legacyView); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"media_type", "size_bytes", "duration_ms"} {
		if _, present := legacyView[field]; !present {
			t.Errorf("legacy zero-valued field %q disappeared", field)
		}
	}
}
