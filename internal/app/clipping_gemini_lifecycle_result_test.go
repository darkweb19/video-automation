package app

import (
	"testing"
	"time"
)

func geminiSubmittedFixture(t *testing.T) (*Store, ClippingJob, clippingGeminiAttemptLease, ClippingGeminiSnapshot, ClippingSource, clippingGeminiResponse, time.Time) {
	t.Helper()
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	input := geminiJobInput()
	input.Options = testClippingGeminiRequest("").Options
	input.Options.CandidateLimit = 2
	job, _, err := store.CreateClippingGeminiJob(security, input, "settlement")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	lease, ok, err := store.ClaimClippingGeminiAttempt(job.ID, now)
	if err != nil || !ok {
		t.Fatalf("claim %v %v", ok, err)
	}
	snapshot, err := store.ClippingGeminiRequestSnapshot(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.ClippingSource(job.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AuthorizeClippingGeminiSubmission(job.ID, lease.Attempt.AttemptID, lease.LeaseToken, snapshot.Fingerprint, source.SourceURL, now); err != nil {
		t.Fatal(err)
	}
	analysis, err := DecodeClippingGeminiAnalysis(readClippingGeminiFixture(t, "ranked.json"), snapshot.Options)
	if err != nil {
		t.Fatal(err)
	}
	response := clippingGeminiResponse{Analysis: analysis, UsageAvailable: true, PromptTokens: 100, OutputTokens: 40, TotalTokens: 140, ModelVersion: "model-v1", ResponseID: "response-1"}
	return store, job, lease, snapshot, source, response, now
}

func TestGeminiCompletionAtomicallyPersistsArtifactUsageAndCache(t *testing.T) {
	store, job, lease, snapshot, source, response, now := geminiSubmittedFixture(t)
	if err := store.settleClippingGeminiResult(job.ID, lease.Attempt.AttemptID, lease.LeaseToken, snapshot.Fingerprint, source.SourceURL, response, nil, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	got, _ := store.ClippingJob(job.ID)
	if got.Status != ClippingJobCompleted {
		t.Fatalf("job %+v", got)
	}
	artifacts, err := store.ClippingArtifacts(job.ID)
	if err != nil || len(artifacts) != 1 || artifacts[0].SchemaVersion != ClippingGeminiAnalysisVersion {
		t.Fatalf("artifacts %+v %v", artifacts, err)
	}
	attempt, err := store.ClippingGeminiLatestAttempt(job.ID)
	if err != nil || !attempt.Usage.Available || attempt.Usage.TotalTokens != 140 || attempt.Outcome != clippingGeminiOutcomeResponseReceived {
		t.Fatalf("attempt %+v %v", attempt, err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM clipping_gemini_cache WHERE origin_job_id=?`, job.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("cache %d %v", count, err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM clipping_gemini_execution_claims WHERE job_id=?`, job.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("guard %d %v", count, err)
	}
}

func TestGeminiInvalidAndLateResultsPreserveUsageWithoutArtifact(t *testing.T) {
	for _, mode := range []string{"invalid", "canceled", "expired"} {
		t.Run(mode, func(t *testing.T) {
			store, job, lease, snapshot, source, response, now := geminiSubmittedFixture(t)
			if mode == "invalid" {
				response.Analysis.Version = "bad"
			}
			if mode == "canceled" {
				if err := store.DeleteClippingAnalysis(job.ID); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "expired" {
				now = now.Add(clippingGeminiSubmissionLeaseDuration + time.Second)
			}
			if err := store.settleClippingGeminiResult(job.ID, lease.Attempt.AttemptID, lease.LeaseToken, snapshot.Fingerprint, source.SourceURL, response, nil, now); err != nil {
				t.Fatal(err)
			}
			got, _ := store.ClippingJob(job.ID)
			if got.Status == ClippingJobCompleted {
				t.Fatalf("late/invalid completion %+v", got)
			}
			artifacts, err := store.ClippingArtifacts(job.ID)
			if err != nil || len(artifacts) != 0 {
				t.Fatalf("artifacts %+v %v", artifacts, err)
			}
			attempt, err := store.ClippingGeminiLatestAttempt(job.ID)
			if err != nil || !attempt.Usage.Available || attempt.Usage.TotalTokens != 140 {
				t.Fatalf("usage %+v %v", attempt, err)
			}
			var claims int
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM clipping_gemini_execution_claims WHERE job_id=? AND state='unknown'`, job.ID).Scan(&claims); err != nil {
				t.Fatal(err)
			}
			if mode == "invalid" && (attempt.Outcome != clippingGeminiOutcomeResponseReceived || claims != 0) {
				t.Fatalf("invalid outcome %+v claims=%d", attempt, claims)
			}
			if mode != "invalid" && (attempt.Outcome != clippingGeminiOutcomeUnknown || claims != 1) {
				t.Fatalf("late outcome %+v claims=%d", attempt, claims)
			}
		})
	}
}

func TestGeminiUnknownResultNeverReplaysAndRejectsForgedLease(t *testing.T) {
	store, job, lease, snapshot, source, response, now := geminiSubmittedFixture(t)
	callErr := &clippingGeminiAttemptError{outcome: clippingGeminiAttemptUnknownPostSubmission}
	if err := store.settleClippingGeminiResult(job.ID, lease.Attempt.AttemptID, "forged", snapshot.Fingerprint, source.SourceURL, response, callErr, now); err == nil {
		t.Fatal("forged lease accepted")
	}
	if err := store.settleClippingGeminiResult(job.ID, lease.Attempt.AttemptID, lease.LeaseToken, snapshot.Fingerprint, source.SourceURL, clippingGeminiResponse{}, callErr, now); err != nil {
		t.Fatal(err)
	}
	view, err := store.ClippingGeminiLifecycle(job.ID, now.Unix())
	if err != nil || view.RetrySafety != ClippingGeminiRetrySafetyUnknownNoReplay || view.Usage.Available {
		t.Fatalf("view %+v %v", view, err)
	}
}
