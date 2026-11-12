package app

import (
	"context"
	"sync"
	"time"
)

const (
	clippingGeminiWorkerCount             = 2
	clippingGeminiMaxSubmissionAttempts   = 4
	clippingGeminiNotSubmittedRetryDelay  = 15 * time.Second
	clippingGemini429BackoffBase          = 30 * time.Second
	clippingGemini429BackoffMaximum       = 15 * time.Minute
	clippingGeminiSubmissionLeaseDuration = clippingGeminiRequestTimeout + 30*time.Second
	clippingGeminiSubmissionPollInterval  = 5 * time.Second
)

// clippingGemini429Backoff is deterministic and capped so a 429 retry can be
// persisted as an absolute next-attempt time without an unbounded queue delay.
// attempt is the one-based number of the request that received the 429.
func clippingGemini429Backoff(attempt int) time.Duration {
	delay := clippingGemini429BackoffBase
	if attempt < 1 {
		attempt = 1
	}
	for n := 1; n < attempt && delay < clippingGemini429BackoffMaximum; n++ {
		if delay > clippingGemini429BackoffMaximum/2 {
			return clippingGemini429BackoffMaximum
		}
		delay *= 2
	}
	if delay > clippingGemini429BackoffMaximum {
		return clippingGemini429BackoffMaximum
	}
	return delay
}

func clippingGemini429MayRetry(attempt int) bool {
	return attempt >= 1 && attempt < clippingGeminiMaxSubmissionAttempts
}

// runClippingGeminiWorkerPool always starts the fixed provider concurrency.
// Workers should perform short DB transactions around provider calls and must
// observe ctx while waiting, acquiring, and sending.
func runClippingGeminiWorkerPool(ctx context.Context, worker func(context.Context)) {
	if ctx == nil || worker == nil {
		return
	}
	var workers sync.WaitGroup
	workers.Add(clippingGeminiWorkerCount)
	for range clippingGeminiWorkerCount {
		go func() {
			defer workers.Done()
			worker(ctx)
		}()
	}
	workers.Wait()
}
