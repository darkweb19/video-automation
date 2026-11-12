package app

import (
	"context"
	"testing"
	"time"
)

func TestClippingGemini429BackoffIsBoundedAndRetryCountIsFinite(t *testing.T) {
	want := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 15 * time.Minute, 15 * time.Minute}
	for index, expected := range want {
		attempt := index + 1
		if got := clippingGemini429Backoff(attempt); got != expected {
			t.Errorf("backoff(%d)=%s, want %s", attempt, got, expected)
		}
	}
	if got := clippingGemini429Backoff(0); got != clippingGemini429BackoffBase {
		t.Errorf("backoff(0)=%s, want base %s", got, clippingGemini429BackoffBase)
	}
	for attempt := 0; attempt <= clippingGeminiMaxSubmissionAttempts+1; attempt++ {
		if got, want := clippingGemini429MayRetry(attempt), attempt >= 1 && attempt < clippingGeminiMaxSubmissionAttempts; got != want {
			t.Errorf("429 retry decision for attempt %d=%t, want %t", attempt, got, want)
		}
	}
	if clippingGeminiSubmissionLeaseDuration <= clippingGeminiRequestTimeout {
		t.Fatalf("submission lease %s must outlive request deadline %s", clippingGeminiSubmissionLeaseDuration, clippingGeminiRequestTimeout)
	}
}

func TestRunClippingGeminiWorkerPoolStartsExactlyTwoWorkers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{}, clippingGeminiWorkerCount+1)
	done := make(chan struct{})
	go func() {
		runClippingGeminiWorkerPool(ctx, func(ctx context.Context) {
			started <- struct{}{}
			<-ctx.Done()
		})
		close(done)
	}()

	for i := 0; i < clippingGeminiWorkerCount; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatalf("worker %d did not start", i+1)
		}
	}
	select {
	case <-started:
		t.Fatal("worker pool started more than the fixed concurrency")
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker pool did not stop after cancellation")
	}

	runClippingGeminiWorkerPool(context.Background(), nil)
}
