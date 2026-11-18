package app

import (
	"testing"
)

func TestClippingGeminiLifecycleViewUsesSafeEmptyAndTerminalDefaults(t *testing.T) {
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	job, _, err := store.CreateClippingGeminiJob(security, geminiJobInput(), "lifecycle-view-empty")
	if err != nil {
		t.Fatal(err)
	}
	view, err := store.ClippingGeminiLifecycle(job.ID, 1)
	if err != nil || view.RetrySafety != ClippingGeminiRetrySafetyNone || view.Attempts != 0 {
		t.Fatalf("empty lifecycle view=%+v error=%v", view, err)
	}
	if _, err := store.db.Exec(`UPDATE clipping_jobs SET status='failed',error='raw provider response with secret' WHERE id=?`, job.ID); err != nil {
		t.Fatal(err)
	}
	view, err = store.ClippingGeminiLifecycle(job.ID, 2)
	if err != nil || view.RetrySafety != ClippingGeminiRetrySafetyTerminal || view.ErrorCode != "analysis_failed" {
		t.Fatalf("safe terminal lifecycle view=%+v error=%v", view, err)
	}
}

func TestSafeClippingGeminiFailureCodeAllowlist(t *testing.T) {
	for _, code := range []string{"config_drift", "credentials_unavailable", "source_inaccessible", "invalid_response", "outcome_unknown", "request_not_submitted", "rate_limited", "provider_rejected_request", "response_blocked", "response_incomplete", "reported_source_too_long", "response_unusable"} {
		if got := safeClippingGeminiFailureCode(code); got != code {
			t.Errorf("safe code %q became %q", code, got)
		}
	}
	if got := safeClippingGeminiFailureCode("raw bearer or response body"); got != "analysis_failed" {
		t.Fatalf("unsafe code became %q", got)
	}
}
