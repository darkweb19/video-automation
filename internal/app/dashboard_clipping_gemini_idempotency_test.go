package app

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"
)

func TestClippingGeminiSubmitConcurrentReplayCreatesOneJobAndSource(t *testing.T) {
	enableGeminiDashboard(t)
	store, _, handler := directGeminiDashboardFixture(t)
	const requests = 8
	type response struct {
		status int
		body   string
	}
	start := make(chan struct{})
	responses := make(chan response, requests)
	var workers sync.WaitGroup
	for i := 0; i < requests; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			got := postGeminiDashboard(handler, geminiDashboardURLBody(), "gemini-concurrent-key", nil)
			responses <- response{got.Code, got.Body.String()}
		}()
	}
	close(start)
	workers.Wait()
	close(responses)
	var created int
	var jobID, sourceID string
	for got := range responses {
		if got.status != http.StatusCreated && got.status != http.StatusOK {
			t.Fatalf("concurrent create: %d %s", got.status, got.body)
		}
		var result struct {
			Job       clippingJobView `json:"job"`
			Duplicate bool            `json:"duplicate"`
		}
		if err := json.Unmarshal([]byte(got.body), &result); err != nil {
			t.Fatal(err)
		}
		if got.status == http.StatusCreated {
			created++
			if result.Duplicate {
				t.Fatal("new job reported as duplicate")
			}
		} else if !result.Duplicate {
			t.Fatal("replayed job not reported as duplicate")
		}
		if jobID == "" {
			jobID, sourceID = result.Job.ID, result.Job.SourceID
		}
		if result.Job.ID != jobID || result.Job.SourceID != sourceID {
			t.Fatalf("replay changed durable identity: %+v", result.Job)
		}
	}
	jobs, err := store.ClippingJobs()
	if err != nil || created != 1 || len(jobs) != 1 || jobs[0].ID != jobID {
		t.Fatalf("created=%d jobs=%v err=%v", created, jobs, err)
	}
	sources, err := store.ClippingSources()
	if err != nil || len(sources) != 1 || sources[0].ID != sourceID {
		t.Fatalf("sources=%v err=%v", sources, err)
	}
}
