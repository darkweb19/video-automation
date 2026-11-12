package app

import (
	"errors"
	"time"
)

func (s *Store) settleClippingGeminiKnownRejection(jobID, attemptID, leaseToken string, attemptErr error, now time.Time) (ClippingGeminiAttempt, error) {
	if s == nil || s.db == nil || !safeID(jobID) || !safeID(attemptID) || leaseToken == "" {
		return ClippingGeminiAttempt{}, ErrClippingStaleAttempt
	}
	if now.IsZero() {
		now = time.Now()
	}
	nowUnix := now.Unix()
	var attemptRecord *clippingGeminiAttemptError
	if !errors.As(attemptErr, &attemptRecord) || attemptRecord == nil {
		return ClippingGeminiAttempt{}, ErrClippingInvalidState
	}
	tx, err := s.db.Begin()
	if err != nil {
		return ClippingGeminiAttempt{}, err
	}
	defer tx.Rollback()
	var storedHash string
	var phase ClippingGeminiAttemptPhase
	var attemptNo int
	var leaseUntil int64
	if err := tx.QueryRow(`SELECT lease_token_hash,phase,attempt_no,lease_expires_at FROM clipping_gemini_attempts WHERE job_id=? AND attempt_id=?`, jobID, attemptID).
		Scan(&storedHash, &phase, &attemptNo, &leaseUntil); err != nil {
		return ClippingGeminiAttempt{}, ErrClippingStaleAttempt
	}
	if !validClippingLease(leaseToken, storedHash) || phase == ClippingGeminiAttemptSettled || leaseUntil <= nowUnix {
		return ClippingGeminiAttempt{}, ErrClippingStaleAttempt
	}
	attemptPolicy := clippingGeminiPolicyForAttempt(attemptRecord, attemptNo)
	if attemptPolicy.Outcome != clippingGeminiOutcomeNotSubmitted && attemptPolicy.Outcome != clippingGeminiOutcomeExplicitRejection {
		return ClippingGeminiAttempt{}, ErrClippingInvalidState
	}
	var job ClippingJob
	job, err = scanClippingJob(tx.QueryRow(`SELECT `+clippingJobColumns()+` FROM clipping_jobs WHERE id=? AND provider='gemini'`, jobID))
	if err != nil {
		return ClippingGeminiAttempt{}, ErrClippingStaleAttempt
	}
	stage, err := readClippingStage(tx, jobID, ClippingStageAnalysis)
	if err != nil || stage.AttemptID != attemptID {
		return ClippingGeminiAttempt{}, ErrClippingStaleAttempt
	}
	var fingerprint string
	if err := tx.QueryRow(`SELECT fingerprint FROM clipping_gemini_requests WHERE job_id=?`, jobID).Scan(&fingerprint); err != nil {
		return ClippingGeminiAttempt{}, err
	}
	var claimJob, claimAttempt string
	var claimState clippingGeminiClaimState
	if err := tx.QueryRow(`SELECT job_id,attempt_id,state FROM clipping_gemini_execution_claims WHERE fingerprint=?`, fingerprint).
		Scan(&claimJob, &claimAttempt, &claimState); err != nil {
		return ClippingGeminiAttempt{}, ErrClippingStaleAttempt
	}
	if claimJob != jobID || claimAttempt != attemptID || (claimState != clippingGeminiClaimActive && claimState != clippingGeminiClaimRetryWait) {
		return ClippingGeminiAttempt{}, ErrClippingStaleAttempt
	}
	canRetry := job.Status == ClippingJobRunning && !job.AnalysisDeleted && stage.Status == ClippingStageRunning &&
		attemptPolicy.MayRetryWithoutDuplicate && attemptNo < clippingGeminiMaxSubmissionAttempts
	retryAt := int64(0)
	if canRetry {
		if attemptPolicy.RetryDelaySeconds <= 0 || attemptPolicy.RetryDelaySeconds > int64(clippingGemini429BackoffMaximum.Seconds()) {
			return ClippingGeminiAttempt{}, ErrClippingInvalidState
		}
		retryAt = nowUnix + attemptPolicy.RetryDelaySeconds
		if retryAt <= nowUnix {
			return ClippingGeminiAttempt{}, ErrClippingInvalidState
		}
	}
	finishedAt := nowUnix
	if _, err := tx.Exec(`UPDATE clipping_gemini_attempts SET phase='settled',outcome=?,failure_code=?,http_status=?,retry_at=?,finished_at=?,updated_at=?
		WHERE job_id=? AND attempt_id=? AND phase IN ('claimed','submitting')`, attemptPolicy.Outcome, attemptPolicy.FailureCode, attemptPolicy.HTTPStatus, retryAt, finishedAt, nowUnix, jobID, attemptID); err != nil {
		return ClippingGeminiAttempt{}, err
	}
	if job.Status == ClippingJobRunning && !job.AnalysisDeleted && stage.Status == ClippingStageRunning {
		if canRetry {
			if _, err := tx.Exec(`UPDATE clipping_stages SET status='queued',lease_expires_at=0,error=?,updated_at=? WHERE job_id=? AND name=? AND attempt_id=?`, attemptPolicy.FailureCode, nowUnix, jobID, ClippingStageAnalysis, attemptID); err != nil {
				return ClippingGeminiAttempt{}, err
			}
			if _, err := tx.Exec(`UPDATE clipping_jobs SET status='queued',error=?,updated_at=? WHERE id=? AND status='running' AND analysis_deleted=0`, attemptPolicy.FailureCode, nowUnix, jobID); err != nil {
				return ClippingGeminiAttempt{}, err
			}
			if _, err := tx.Exec(`UPDATE clipping_gemini_execution_claims SET state='retry_wait',retry_at=?,lease_expires_at=?,updated_at=? WHERE fingerprint=? AND job_id=? AND attempt_id=?`, retryAt, retryAt, nowUnix, fingerprint, jobID, attemptID); err != nil {
				return ClippingGeminiAttempt{}, err
			}
		} else {
			if _, err := tx.Exec(`UPDATE clipping_stages SET status='failed',lease_expires_at=0,error=?,updated_at=? WHERE job_id=? AND name=? AND attempt_id=?`, attemptPolicy.FailureCode, nowUnix, jobID, ClippingStageAnalysis, attemptID); err != nil {
				return ClippingGeminiAttempt{}, err
			}
			if _, err := tx.Exec(`UPDATE clipping_jobs SET status='failed',error=?,updated_at=? WHERE id=? AND status='running' AND analysis_deleted=0`, attemptPolicy.FailureCode, nowUnix, jobID); err != nil {
				return ClippingGeminiAttempt{}, err
			}
			if _, err := tx.Exec(`DELETE FROM clipping_gemini_execution_claims WHERE fingerprint=? AND job_id=? AND attempt_id=?`, fingerprint, jobID, attemptID); err != nil {
				return ClippingGeminiAttempt{}, err
			}
		}
	} else {
		if _, err := tx.Exec(`DELETE FROM clipping_gemini_execution_claims WHERE fingerprint=? AND job_id=? AND attempt_id=?`, fingerprint, jobID, attemptID); err != nil {
			return ClippingGeminiAttempt{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return ClippingGeminiAttempt{}, err
	}
	return s.ClippingGeminiLatestAttempt(jobID)
}
