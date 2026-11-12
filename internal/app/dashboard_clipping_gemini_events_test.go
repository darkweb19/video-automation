package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestClippingGeminiSubmitPublishesSourceAndJobOnce(t *testing.T) {
	enableGeminiDashboard(t)
	store, _, handler := directGeminiDashboardFixture(t)
	events, unsubscribe := store.events.subscribe()
	defer unsubscribe()
	response := postGeminiDashboard(handler, geminiDashboardURLBody(), "gemini-event-key", nil)
	if response.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", response.Code, response.Body.String())
	}
	var result struct {
		Job clippingJobView `json:"job"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []struct{ name, id string }{{"clipping_source", result.Job.SourceID}, {"clipping_job", result.Job.ID}} {
		select {
		case event := <-events:
			if event.name != expected.name || strings.Contains(string(event.data), "abcdefghijk") || strings.Contains(string(event.data), geminiDashboardURL) {
				t.Fatalf("unsafe or unexpected event: %s %s", event.name, event.data)
			}
			var payload struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(event.data, &payload); err != nil || payload.ID != expected.id {
				t.Fatalf("event identity: %+v %v", payload, err)
			}
		default:
			t.Fatalf("missing %s event", expected.name)
		}
	}
	response = postGeminiDashboard(handler, geminiDashboardURLBody(), "gemini-event-key", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("replay: %d %s", response.Code, response.Body.String())
	}
	select {
	case event := <-events:
		t.Fatalf("idempotent replay published %s again", event.name)
	default:
	}
}
