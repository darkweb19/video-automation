package app

import (
	"database/sql"
	"errors"
)

const (
	ClippingGeminiRetrySafetyNotSubmittedRetryable = "not_submitted_retryable"
	ClippingGeminiRetrySafetyRateLimitedWait       = "rate_limited_wait"
	ClippingGeminiRetrySafetyResponseReceived      = "response_received_terminal"
	ClippingGeminiRetrySafetyUnknownNoReplay       = "outcome_unknown_no_replay"
	ClippingGeminiRetrySafetyTerminal              = "terminal"
	ClippingGeminiRetrySafetyNone                  = "none"
)

// ClippingGeminiLifecycleView is the provider-safe API contract used by the
// separate route owner. RetrySafety is a fact about durable inference outcome,
// not a promise that provider execution stopped when local cancellation ran.
type ClippingGeminiLifecycleView struct {
	RetrySafety   string              `json:"retry_safety"`
	RetryAt       int64               `json:"retry_at,omitempty"`
	ErrorCode     string              `json:"error_code,omitempty"`
	Attempt       int                 `json:"attempt,omitempty"`
	Attempts      int                 `json:"attempts"`
	Usage         ClippingGeminiUsage `json:"usage"`
	CacheOriginID string              `json:"cache_origin_job_id,omitempty"`
}

func (s *Store) ClippingGeminiLifecycle(jobID string, now int64) (ClippingGeminiLifecycleView, error) {
	var view ClippingGeminiLifecycleView
	if now <= 0 {
		now = clippingGeminiNowUnix()
	}
	job, err := s.ClippingJob(jobID)
	if err != nil {
		return view, err
	}
	if job.Provider != ClippingProviderGemini {
		return view, ErrClippingInvalidState
	}
	attempts, err := s.ClippingGeminiAttempts(jobID)
	if err != nil {
		return view, err
	}
	view.Attempts = len(attempts)
	if job.Status == ClippingJobCompleted {
		err := s.db.QueryRow(`SELECT origin_job_id FROM clipping_gemini_cache_reuses WHERE job_id=?`, jobID).Scan(&view.CacheOriginID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return ClippingGeminiLifecycleView{}, err
		}
		if err == nil {
			view.RetrySafety = ClippingGeminiRetrySafetyNone
			return view, nil
		}
	}
	if len(attempts) == 0 {
		view.RetrySafety = ClippingGeminiRetrySafetyNone
		if job.Status == ClippingJobFailed && job.Error != "" {
			view.RetrySafety = ClippingGeminiRetrySafetyTerminal
			view.ErrorCode = safeClippingGeminiFailureCode(job.Error)
			if job.Error == "outcome_unknown" {
				view.RetrySafety = ClippingGeminiRetrySafetyUnknownNoReplay
			}
		}
		return view, nil
	}
	latest := attempts[len(attempts)-1]
	view.Attempt = latest.Attempt
	if latest.Phase != ClippingGeminiAttemptSettled {
		view.RetrySafety = ClippingGeminiRetrySafetyNone
		return view, nil
	}
	view.Usage, view.ErrorCode = latest.Usage, safeClippingGeminiFailureCode(latest.FailureCode)
	switch latest.Outcome {
	case clippingGeminiOutcomeNotSubmitted:
		if job.Status == ClippingJobQueued && latest.Attempt < clippingGeminiMaxSubmissionAttempts {
			view.RetrySafety = ClippingGeminiRetrySafetyNotSubmittedRetryable
		} else {
			view.RetrySafety = ClippingGeminiRetrySafetyTerminal
		}
	case clippingGeminiOutcomeExplicitRejection:
		if latest.FailureCode == "rate_limited" && job.Status == ClippingJobQueued && latest.Attempt < clippingGeminiMaxSubmissionAttempts {
			view.RetrySafety = ClippingGeminiRetrySafetyRateLimitedWait
			view.RetryAt = latest.RetryAt
		} else {
			view.RetrySafety = ClippingGeminiRetrySafetyTerminal
		}
	case clippingGeminiOutcomeResponseReceived:
		view.RetrySafety = ClippingGeminiRetrySafetyResponseReceived
	case clippingGeminiOutcomeUnknown:
		view.RetrySafety = ClippingGeminiRetrySafetyUnknownNoReplay
	default:
		view.RetrySafety = ClippingGeminiRetrySafetyTerminal
	}
	return view, nil
}

func safeClippingGeminiFailureCode(code string) string {
	switch code {
	case "config_drift", "credentials_unavailable", "source_inaccessible", "invalid_response", "outcome_unknown",
		"request_not_submitted", "rate_limited", "provider_rejected_request", "response_blocked",
		"response_incomplete", "reported_source_too_long", "response_unusable":
		return code
	default:
		if code == "" {
			return ""
		}
		return "analysis_failed"
	}
}
