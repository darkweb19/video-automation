package app

import "testing"

func TestClippingGeminiPublicFailureAllowsOnlyKnownCodes(t *testing.T) {
	for _, code := range []string{"", "config_drift", "credentials_unavailable", "source_inaccessible", "invalid_response", "outcome_unknown", "request_not_submitted", "rate_limited", "provider_rejected_request", "response_blocked", "response_incomplete", "reported_source_too_long", "response_unusable"} {
		if got := clippingGeminiPublicFailure(code); got != code {
			t.Errorf("failure %q = %q", code, got)
		}
	}
	for _, code := range []string{"custom failure", "https://youtube.com/watch?v=abcdefghijk", "credentials_unavailable: secret", "CONFIG_DRIFT", " config_drift"} {
		if got := clippingGeminiPublicFailure(code); got != "analysis_failed" {
			t.Errorf("unexpected failure exposed as %q", got)
		}
	}
}
