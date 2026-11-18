package app

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

type fakeGeminiAnalyzer func(context.Context, ClippingGeminiRequest) (clippingGeminiResponse, error)

func (f fakeGeminiAnalyzer) Analyze(ctx context.Context, request ClippingGeminiRequest) (clippingGeminiResponse, error) {
	return f(ctx, request)
}

func geminiExecutorFixture(t *testing.T) (*Processor, ClippingJob, clippingGeminiRuntimeConfig, clippingGeminiResponse) {
	t.Helper()
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	input := geminiJobInput()
	input.Options = testClippingGeminiRequest("").Options
	input.Options.CandidateLimit = 2
	job, _, err := store.CreateClippingGeminiJob(security, input, "executor")
	if err != nil {
		t.Fatal(err)
	}
	analysis, err := DecodeClippingGeminiAnalysis(readClippingGeminiFixture(t, "ranked.json"), input.Options)
	if err != nil {
		t.Fatal(err)
	}
	p := &Processor{app: &dashboardApp{store: store, security: security}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	return p, job, clippingGeminiRuntimeConfig{Enabled: true, Routing: input.Config}, clippingGeminiResponse{Analysis: analysis, UsageAvailable: true, TotalTokens: 33}
}

func TestGeminiExecutorCompletesWithDurableIntentBeforeFakeProviderCall(t *testing.T) {
	p, job, config, response := geminiExecutorFixture(t)
	var calls int
	factory := func(ctx context.Context, routing ClippingGeminiConfig) (clippingGeminiAnalyzer, error) {
		if routing != config.Routing {
			t.Fatalf("routing %+v", routing)
		}
		return fakeGeminiAnalyzer(func(ctx context.Context, request ClippingGeminiRequest) (clippingGeminiResponse, error) {
			calls++
			latest, err := p.app.store.ClippingGeminiLatestAttempt(job.ID)
			if err != nil || latest.Phase != ClippingGeminiAttemptSubmitting || latest.Outcome != clippingGeminiOutcomeUnknown {
				t.Fatalf("intent %+v %v", latest, err)
			}
			if request.YouTubeURL != "https://www.youtube.com/watch?v=abcdefghijk" {
				t.Fatalf("canonical URL %q", request.YouTubeURL)
			}
			return response, nil
		}), nil
	}
	worked, err := p.processNextClippingGemini(context.Background(), config, factory)
	if err != nil || !worked || calls != 1 {
		t.Fatalf("worked=%v calls=%d error=%v", worked, calls, err)
	}
	got, _ := p.app.store.ClippingJob(job.ID)
	if got.Status != ClippingJobCompleted {
		t.Fatalf("job %+v", got)
	}
	view, err := p.app.store.ClippingGeminiLifecycle(job.ID, time.Now().Unix())
	if err != nil || !view.Usage.Available || view.Usage.TotalTokens != 33 {
		t.Fatalf("view %+v %v", view, err)
	}
}

func TestGeminiExecutorRespectsOptInAndSnapshotDrift(t *testing.T) {
	p, job, config, _ := geminiExecutorFixture(t)
	var calls int
	factory := func(context.Context, ClippingGeminiConfig) (clippingGeminiAnalyzer, error) { calls++; return nil, nil }
	worked, err := p.processNextClippingGemini(context.Background(), clippingGeminiRuntimeConfig{}, factory)
	if err != nil || worked || calls != 0 {
		t.Fatalf("disabled %v %d %v", worked, calls, err)
	}
	config.Routing.ProjectID = "other-project"
	worked, err = p.processNextClippingGemini(context.Background(), config, factory)
	if err != nil || !worked || calls != 0 {
		t.Fatalf("drift %v %d %v", worked, calls, err)
	}
	got, _ := p.app.store.ClippingJob(job.ID)
	if got.Status != ClippingJobFailed || got.Error != "config_drift" {
		t.Fatalf("job %+v", got)
	}
}

func TestGeminiExecutorCancellationBeforeAuthorizationPreventsProviderCall(t *testing.T) {
	p, job, config, response := geminiExecutorFixture(t)
	entered := make(chan struct{})
	unblock := make(chan struct{})
	done := make(chan error, 1)
	var calls atomic.Int32
	factory := func(context.Context, ClippingGeminiConfig) (clippingGeminiAnalyzer, error) {
		close(entered)
		<-unblock
		return fakeGeminiAnalyzer(func(context.Context, ClippingGeminiRequest) (clippingGeminiResponse, error) {
			calls.Add(1)
			return response, nil
		}), nil
	}
	go func() { _, err := p.processNextClippingGemini(context.Background(), config, factory); done <- err }()
	<-entered
	if err := p.app.store.CancelClippingJob(job.ID); err != nil {
		t.Fatal(err)
	}
	close(unblock)
	if err := <-done; err != nil || calls.Load() != 0 {
		t.Fatalf("calls=%d error=%v", calls.Load(), err)
	}
	attempt, err := p.app.store.ClippingGeminiLatestAttempt(job.ID)
	if err != nil || attempt.Outcome != clippingGeminiOutcomeNotSubmitted {
		t.Fatalf("attempt %+v %v", attempt, err)
	}
}

func TestGeminiExecutorLateCanceledResponseRetainsUsageWithoutPublishing(t *testing.T) {
	p, job, config, response := geminiExecutorFixture(t)
	entered := make(chan struct{})
	done := make(chan error, 1)
	factory := func(context.Context, ClippingGeminiConfig) (clippingGeminiAnalyzer, error) {
		return fakeGeminiAnalyzer(func(ctx context.Context, _ ClippingGeminiRequest) (clippingGeminiResponse, error) {
			close(entered)
			<-ctx.Done()
			return response, nil
		}), nil
	}
	go func() { _, err := p.processNextClippingGemini(context.Background(), config, factory); done <- err }()
	<-entered
	if err := p.app.store.DeleteClippingAnalysis(job.ID); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	got, _ := p.app.store.ClippingJob(job.ID)
	if !got.AnalysisDeleted || got.Status != ClippingJobCanceled {
		t.Fatalf("job %+v", got)
	}
	attempt, err := p.app.store.ClippingGeminiLatestAttempt(job.ID)
	if err != nil || attempt.Outcome != clippingGeminiOutcomeUnknown || !attempt.Usage.Available || attempt.Usage.TotalTokens != 33 {
		t.Fatalf("attempt %+v %v", attempt, err)
	}
	artifacts, err := p.app.store.ClippingArtifacts(job.ID)
	if err != nil || len(artifacts) != 0 {
		t.Fatalf("artifacts %+v %v", artifacts, err)
	}
}
