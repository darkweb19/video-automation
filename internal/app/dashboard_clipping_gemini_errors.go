package app

// Stored Gemini failures are application-owned codes, never raw transport
// errors. Fail closed if a corrupted or older record contains arbitrary text.
func clippingGeminiPublicFailure(code string) string {
	switch code {
	case "":
		return ""
	case "config_drift", "credentials_unavailable", "source_inaccessible", "invalid_response", "outcome_unknown",
		"request_not_submitted", "rate_limited", "provider_rejected_request", "response_blocked", "response_incomplete",
		"reported_source_too_long", "response_unusable":
		return code
	default:
		return "analysis_failed"
	}
}
