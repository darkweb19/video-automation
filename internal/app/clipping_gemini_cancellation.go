package app

import (
	"context"
	"sync"
)

type clippingGeminiActiveCancellation struct {
	attemptID string
	cancel    context.CancelFunc
}

// clippingGeminiCancellationRegistry belongs to Store rather than a
// dashboardApp or Processor wrapper. Both request handling and workers can
// therefore cancel the same in-flight attempt.
type clippingGeminiCancellationRegistry struct {
	mu     sync.Mutex
	active map[string]clippingGeminiActiveCancellation
}

func newClippingGeminiCancellationRegistry() *clippingGeminiCancellationRegistry {
	return &clippingGeminiCancellationRegistry{active: make(map[string]clippingGeminiActiveCancellation)}
}

func (s *Store) clippingGeminiCancellationRegistry() *clippingGeminiCancellationRegistry {
	if s == nil {
		return nil
	}
	if s.clippingGeminiCancellations == nil {
		s.clippingGeminiCancellations = newClippingGeminiCancellationRegistry()
	}
	return s.clippingGeminiCancellations
}

// register installs one active context per job. The returned release function
// is idempotent and removes only the matching attempt, so a late cleanup cannot
// erase a newer registration.
func (r *clippingGeminiCancellationRegistry) register(jobID, attemptID string, cancel context.CancelFunc) (func(), bool) {
	if r == nil || jobID == "" || attemptID == "" || cancel == nil {
		return func() {}, false
	}
	r.mu.Lock()
	if r.active == nil {
		r.active = make(map[string]clippingGeminiActiveCancellation)
	}
	if _, exists := r.active[jobID]; exists {
		r.mu.Unlock()
		return func() {}, false
	}
	r.active[jobID] = clippingGeminiActiveCancellation{attemptID: attemptID, cancel: cancel}
	r.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			if current, exists := r.active[jobID]; exists && current.attemptID == attemptID {
				delete(r.active, jobID)
			}
			r.mu.Unlock()
		})
	}, true
}

func (r *clippingGeminiCancellationRegistry) cancelJob(jobID string) bool {
	if r == nil || jobID == "" {
		return false
	}
	r.mu.Lock()
	active, exists := r.active[jobID]
	r.mu.Unlock()
	if !exists || active.cancel == nil {
		return false
	}
	active.cancel()
	return true
}

func (r *clippingGeminiCancellationRegistry) cancelJobs(jobIDs []string) int {
	canceled := 0
	for _, jobID := range jobIDs {
		if r.cancelJob(jobID) {
			canceled++
		}
	}
	return canceled
}
