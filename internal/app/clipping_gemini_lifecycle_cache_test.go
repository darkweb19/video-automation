package app

import (
	"context"
	"testing"
	"time"
)

func TestGeminiCacheHitAfterSafeRetryPublishesOriginAndNoNewUsage(t *testing.T) {
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	origin, target := geminiCacheJobs(t, store, security, "")
	now := time.Now().UTC().Truncate(time.Second)
	lease, ok, err := store.ClaimClippingGeminiAttempt(target.ID, now)
	if err != nil || !ok {
		t.Fatalf("claim %v %v", ok, err)
	}
	if _, err := store.settleClippingGeminiKnownRejection(target.ID, lease.Attempt.AttemptID, lease.LeaseToken, clippingGeminiNotSubmittedError(context.Canceled), now); err != nil {
		t.Fatal(err)
	}
	publishGeminiCacheFixture(t, store, origin, now.Unix())
	hit, err := store.CompleteClippingGeminiFromCache(target.ID, now.Add(time.Second))
	if err != nil || !hit {
		t.Fatalf("cache hit %v %v", hit, err)
	}
	view, err := store.ClippingGeminiLifecycle(target.ID, now.Unix())
	if err != nil || view.CacheOriginID != origin.ID || view.RetrySafety != ClippingGeminiRetrySafetyNone || view.ErrorCode != "" || view.Usage.Available || view.Attempts != 1 {
		t.Fatalf("view %+v %v", view, err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM clipping_gemini_execution_claims WHERE job_id=?`, target.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("guard %d %v", count, err)
	}
}
