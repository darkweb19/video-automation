package app

import (
	"encoding/json"
	"time"
)

// settleClippingGeminiResult authenticates the original lease even for a late
// response. Only a live current lease may publish; authenticated late responses
// can retain usage without restoring canceled, deleted, or expired results.
func (s *Store) settleClippingGeminiResult(jobID, attemptID, leaseToken, fingerprint, expectedSourceURL string, response clippingGeminiResponse, callErr error, now time.Time) error {
	if s == nil || s.db == nil || !safeID(jobID) || !safeID(attemptID) || leaseToken == "" || !validLowerHexDigest(fingerprint) || expectedSourceURL == "" {
		return ErrClippingInvalidState
	}
	if now.IsZero() {
		now = time.Now()
	}
	n := now.Unix()
	policy := clippingGeminiPolicyForAttempt(callErr, 1)
	if policy.Outcome == clippingGeminiOutcomeNotSubmitted || policy.Outcome == clippingGeminiOutcomeExplicitRejection {
		_, err := s.settleClippingGeminiKnownRejection(jobID, attemptID, leaseToken, callErr, now)
		return err
	}
	var artifact ClippingArtifact
	var ranges string
	if callErr == nil {
		snapshot, err := s.ClippingGeminiRequestSnapshot(jobID)
		if err != nil {
			return err
		}
		payload, err := json.Marshal(response.Analysis)
		if err == nil {
			artifact, err = clippingGeminiArtifactFromPayload(jobID, payload, snapshot.Options)
		}
		if err == nil {
			ranges, err = validateClippingArtifact(artifact, jobID, artifact.SourceDurationMS)
		}
		if err != nil {
			policy = clippingGeminiAttemptPolicy{Outcome: clippingGeminiOutcomeResponseReceived, FailureCode: "invalid_response"}
		}
	}
	usage := safeClippingGeminiUsage(response)
	modelVersion, responseID := safeClippingGeminiResultMetadata(response.ModelVersion), safeClippingGeminiResultMetadata(response.ResponseID)
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var hash, phase, existingOutcome string
	var leaseUntil int64
	if err := tx.QueryRow(`SELECT lease_token_hash,phase,outcome,lease_expires_at FROM clipping_gemini_attempts WHERE job_id=? AND attempt_id=?`, jobID, attemptID).Scan(&hash, &phase, &existingOutcome, &leaseUntil); err != nil || !validClippingLease(leaseToken, hash) {
		return ErrClippingStaleAttempt
	}
	var storedFP, sourceURL, sourceKind, sourceStatus string
	var jobStatus ClippingJobStatus
	var deleted, rights int
	var retention int64
	if err := tx.QueryRow(`SELECT request.fingerprint,source.source_url,source.kind,source.status,source.rights_attested,source.retain_until,job.status,job.analysis_deleted
		FROM clipping_jobs AS job JOIN clipping_gemini_requests AS request ON request.job_id=job.id JOIN clipping_sources AS source ON source.id=job.source_id
		WHERE job.id=? AND job.provider='gemini'`, jobID).Scan(&storedFP, &sourceURL, &sourceKind, &sourceStatus, &rights, &retention, &jobStatus, &deleted); err != nil || storedFP != fingerprint {
		return ErrClippingStaleAttempt
	}
	// A canceled or recovered uncertain attempt may receive a late provider
	// response. Preserve only safe usage on the original attempt; do not alter
	// its guard or terminal state, and never trust a repeated settlement.
	if phase == string(ClippingGeminiAttemptSettled) {
		if existingOutcome == string(clippingGeminiOutcomeUnknown) && policy.Outcome == clippingGeminiOutcomeResponseReceived && usage.Available {
			if _, err := tx.Exec(`UPDATE clipping_gemini_attempts SET usage_available=1,prompt_tokens=?,output_tokens=?,thoughts_tokens=?,cached_tokens=?,tool_prompt_tokens=?,total_tokens=?,model_version=?,response_id=?,updated_at=? WHERE job_id=? AND attempt_id=? AND phase='settled' AND outcome='unknown' AND usage_available=0`, usage.PromptTokens, usage.OutputTokens, usage.ThoughtsTokens, usage.CachedTokens, usage.ToolPromptTokens, usage.TotalTokens, modelVersion, responseID, n, jobID, attemptID); err != nil {
				return err
			}
		}
		return tx.Commit()
	}
	if phase != string(ClippingGeminiAttemptSubmitting) {
		return ErrClippingStaleAttempt
	}
	stage, err := readClippingStage(tx, jobID, ClippingStageAnalysis)
	if err != nil {
		return ErrClippingStaleAttempt
	}
	var claimJob, claimAttempt, claimState string
	if err := tx.QueryRow(`SELECT job_id,attempt_id,state FROM clipping_gemini_execution_claims WHERE fingerprint=?`, fingerprint).Scan(&claimJob, &claimAttempt, &claimState); err != nil {
		return ErrClippingStaleAttempt
	}
	current := jobStatus == ClippingJobRunning && deleted == 0 && stage.Status == ClippingStageRunning && stage.AttemptID == attemptID && stage.LeaseExpiresAt == leaseUntil && leaseUntil > n && claimJob == jobID && claimAttempt == attemptID && claimState == "active" && sourceURL == expectedSourceURL && sourceURL != "" && (sourceKind == string(ClippingSourceYouTubeURL) || sourceKind == string(ClippingSourceYouTube)) && sourceStatus == string(ClippingSourceReady) && rights == 1 && (retention == 0 || retention > n)
	// Failure of a completion fence does not authorize replay. Retain the
	// uncertain guard; recovery or cancellation may have raced the response.
	if !current {
		policy = clippingGeminiAttemptPolicy{Outcome: clippingGeminiOutcomeUnknown, FailureCode: "outcome_unknown"}
	}
	if _, err := tx.Exec(`UPDATE clipping_gemini_attempts SET phase='settled',outcome=?,failure_code=?,http_status=?,retry_at=0,usage_available=?,prompt_tokens=?,output_tokens=?,thoughts_tokens=?,cached_tokens=?,tool_prompt_tokens=?,total_tokens=?,model_version=?,response_id=?,finished_at=?,updated_at=? WHERE job_id=? AND attempt_id=? AND phase='submitting'`, policy.Outcome, policy.FailureCode, policy.HTTPStatus, usage.Available, usage.PromptTokens, usage.OutputTokens, usage.ThoughtsTokens, usage.CachedTokens, usage.ToolPromptTokens, usage.TotalTokens, modelVersion, responseID, n, n, jobID, attemptID); err != nil {
		return err
	}
	if policy.Outcome == clippingGeminiOutcomeUnknown {
		if _, err := tx.Exec(`UPDATE clipping_gemini_execution_claims SET state='unknown',retry_at=0,updated_at=? WHERE fingerprint=? AND job_id=? AND attempt_id=?`, n, fingerprint, jobID, attemptID); err != nil {
			return err
		}
	} else if _, err := tx.Exec(`DELETE FROM clipping_gemini_execution_claims WHERE fingerprint=? AND job_id=? AND attempt_id=?`, fingerprint, jobID, attemptID); err != nil {
		return err
	}
	if current {
		status, code := "failed", policy.FailureCode
		if callErr == nil && policy.FailureCode == "" && policy.Outcome == clippingGeminiOutcomeResponseReceived {
			if err := insertClippingArtifact(tx, artifact, ranges, n); err != nil {
				return err
			}
			status = "completed"
		}
		if _, err := tx.Exec(`UPDATE clipping_stages SET status=?,lease_expires_at=0,error=?,updated_at=? WHERE job_id=? AND name=? AND attempt_id=? AND status='running'`, status, code, n, jobID, ClippingStageAnalysis, attemptID); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE clipping_jobs SET status=?,error=?,updated_at=? WHERE id=? AND status='running' AND analysis_deleted=0`, status, code, n, jobID); err != nil {
			return err
		}
		if status == "completed" {
			if err := storeClippingGeminiCacheTx(tx, jobID, n); err != nil {
				return err
			}
		}
	} else if jobStatus == ClippingJobRunning && deleted == 0 && stage.AttemptID == attemptID && stage.Status == ClippingStageRunning {
		if _, err := tx.Exec(`UPDATE clipping_stages SET status='failed',lease_expires_at=0,error='outcome_unknown',updated_at=? WHERE job_id=? AND name=? AND attempt_id=?`, n, jobID, ClippingStageAnalysis, attemptID); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE clipping_jobs SET status='failed',error='outcome_unknown',updated_at=? WHERE id=? AND status='running'`, n, jobID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func safeClippingGeminiUsage(response clippingGeminiResponse) ClippingGeminiUsage {
	if !response.UsageAvailable || response.PromptTokens < 0 || response.OutputTokens < 0 || response.ThoughtsTokens < 0 || response.CachedTokens < 0 || response.ToolUsePromptTokens < 0 || response.TotalTokens < 0 {
		return ClippingGeminiUsage{}
	}
	return ClippingGeminiUsage{Available: true, PromptTokens: int64(response.PromptTokens), OutputTokens: int64(response.OutputTokens), ThoughtsTokens: int64(response.ThoughtsTokens), CachedTokens: int64(response.CachedTokens), ToolPromptTokens: int64(response.ToolUsePromptTokens), TotalTokens: int64(response.TotalTokens)}
}

func safeClippingGeminiResultMetadata(value string) string {
	if len(value) > clippingGeminiMaxResponseMetadataBytes {
		return ""
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return ""
		}
	}
	return value
}
