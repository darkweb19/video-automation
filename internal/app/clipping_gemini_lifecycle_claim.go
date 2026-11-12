package app

import (
	"database/sql"
	"errors"
	"time"
)

const clippingGeminiQualifiedJobColumns = `job.id,job.batch_id,job.source_id,job.provider,job.content_type,job.min_clip_seconds,job.max_clip_seconds,
	job.candidate_limit,job.status,job.budget_limit_micro_usd,job.reserved_micro_usd,job.spent_micro_usd,job.analysis_deleted,job.error,job.created_at,job.updated_at`

// PendingClippingGeminiJobs returns only due Gemini work. Legacy Modal jobs
// remain on their existing provider-filtered queue.
func (s *Store) PendingClippingGeminiJobs(now int64, limit int) ([]ClippingJob, error) {
	if s == nil || s.db == nil {
		return nil, ErrClippingInvalidState
	}
	if now <= 0 {
		now = clippingGeminiNowUnix()
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.Query(`SELECT `+clippingGeminiQualifiedJobColumns+` FROM clipping_jobs AS job
		JOIN clipping_gemini_requests AS request ON request.job_id=job.id
		JOIN clipping_sources AS source ON source.id=job.source_id
		WHERE job.provider='gemini' AND job.status='queued' AND job.analysis_deleted=0
		AND source.kind IN ('youtube_url','youtube_original_file') AND source.status='ready' AND source.rights_attested=1
		AND (source.retain_until=0 OR source.retain_until>?)
		AND NOT EXISTS(
			SELECT 1 FROM clipping_gemini_execution_claims AS claim WHERE claim.fingerprint=request.fingerprint
			AND NOT (claim.job_id=job.id AND claim.state='retry_wait' AND claim.retry_at<=?)
		)
		ORDER BY job.created_at,job.id LIMIT ?`, now, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := make([]ClippingJob, 0)
	for rows.Next() {
		job, err := scanClippingJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

// ClaimClippingGeminiAttempt creates durable intent and the global fingerprint
// guard before any SDK call. An active or uncertain fingerprint owned by any
// other job blocks the claim, regardless of source ID or idempotency key.
func (s *Store) ClaimClippingGeminiAttempt(jobID string, now time.Time) (clippingGeminiAttemptLease, bool, error) {
	if s == nil || s.db == nil || !safeID(jobID) || len(jobID) < len("clipjob_") || jobID[:len("clipjob_")] != "clipjob_" {
		return clippingGeminiAttemptLease{}, false, ErrClippingJobNotFound
	}
	if now.IsZero() {
		now = time.Now()
	}
	nowUnix := now.Unix()
	leaseUntil := now.Add(clippingGeminiSubmissionLeaseDuration).Unix()
	if leaseUntil <= nowUnix {
		return clippingGeminiAttemptLease{}, false, ErrClippingInvalidState
	}
	tx, err := s.db.Begin()
	if err != nil {
		return clippingGeminiAttemptLease{}, false, err
	}
	defer tx.Rollback()
	job, err := scanClippingJob(tx.QueryRow(`SELECT `+clippingJobColumns()+` FROM clipping_jobs WHERE id=?`, jobID))
	if errors.Is(err, sql.ErrNoRows) {
		return clippingGeminiAttemptLease{}, false, ErrClippingJobNotFound
	}
	if err != nil {
		return clippingGeminiAttemptLease{}, false, err
	}
	if job.Provider != ClippingProviderGemini || job.Status != ClippingJobQueued || job.AnalysisDeleted {
		return clippingGeminiAttemptLease{}, false, ErrClippingInvalidState
	}
	_, _, fingerprint, _, err := clippingGeminiCacheIntentTx(tx, job.ID, nowUnix)
	if err != nil {
		return clippingGeminiAttemptLease{}, false, ErrClippingInvalidState
	}
	stage, err := readClippingStage(tx, job.ID, ClippingStageAnalysis)
	if err != nil || stage.Status != ClippingStageQueued {
		return clippingGeminiAttemptLease{}, false, ErrClippingInvalidState
	}
	var priorAttempts int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM clipping_gemini_attempts WHERE job_id=?`, job.ID).Scan(&priorAttempts); err != nil {
		return clippingGeminiAttemptLease{}, false, err
	}
	if priorAttempts >= clippingGeminiMaxSubmissionAttempts || stage.Attempt != priorAttempts {
		return clippingGeminiAttemptLease{}, false, ErrClippingJobTerminal
	}
	var existingJob, existingAttempt string
	var existingState clippingGeminiClaimState
	var existingRetryAt int64
	err = tx.QueryRow(`SELECT job_id,attempt_id,state,retry_at FROM clipping_gemini_execution_claims WHERE fingerprint=?`, fingerprint).
		Scan(&existingJob, &existingAttempt, &existingState, &existingRetryAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return clippingGeminiAttemptLease{}, false, err
	}
	claimExists := err == nil
	if claimExists && (existingJob != job.ID || existingState != clippingGeminiClaimRetryWait || existingRetryAt > nowUnix) {
		return clippingGeminiAttemptLease{}, false, nil
	}
	if claimExists && existingAttempt == "" {
		return clippingGeminiAttemptLease{}, false, ErrClippingInvalidState
	}
	attemptID, err := stageAttemptID()
	if err != nil {
		return clippingGeminiAttemptLease{}, false, err
	}
	leaseToken, err := newClippingLeaseToken()
	if err != nil {
		return clippingGeminiAttemptLease{}, false, err
	}
	attemptNumber := priorAttempts + 1
	if _, err := tx.Exec(`INSERT INTO clipping_gemini_attempts(attempt_id,job_id,attempt_no,phase,outcome,lease_token_hash,lease_expires_at,started_at,updated_at)
		VALUES(?,?,?,'claimed','not_submitted',?,?,?,?)`, attemptID, job.ID, attemptNumber, tokenHash(leaseToken), leaseUntil, nowUnix, nowUnix); err != nil {
		return clippingGeminiAttemptLease{}, false, err
	}
	if claimExists {
		if _, err := tx.Exec(`UPDATE clipping_gemini_execution_claims SET attempt_id=?,state='active',retry_at=0,lease_expires_at=?,updated_at=?
			WHERE fingerprint=? AND job_id=? AND state='retry_wait' AND retry_at<=?`, attemptID, leaseUntil, nowUnix, fingerprint, job.ID, nowUnix); err != nil {
			return clippingGeminiAttemptLease{}, false, err
		}
	} else if _, err := tx.Exec(`INSERT INTO clipping_gemini_execution_claims(fingerprint,job_id,attempt_id,state,retry_at,lease_expires_at,created_at,updated_at)
		VALUES(?,?,?,'active',0,?,?,?)`, fingerprint, job.ID, attemptID, leaseUntil, nowUnix, nowUnix); err != nil {
		return clippingGeminiAttemptLease{}, false, err
	}
	if _, err := tx.Exec(`UPDATE clipping_stages SET status='running',attempt_id=?,attempt=?,lease_expires_at=?,error='',updated_at=? WHERE job_id=? AND name=? AND status='queued'`, attemptID, attemptNumber, leaseUntil, nowUnix, job.ID, ClippingStageAnalysis); err != nil {
		return clippingGeminiAttemptLease{}, false, err
	}
	if _, err := tx.Exec(`UPDATE clipping_jobs SET status='running',error='',updated_at=? WHERE id=? AND status='queued' AND analysis_deleted=0`, nowUnix, job.ID); err != nil {
		return clippingGeminiAttemptLease{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return clippingGeminiAttemptLease{}, false, err
	}
	attempt := ClippingGeminiAttempt{JobID: job.ID, AttemptID: attemptID, Attempt: attemptNumber, Phase: ClippingGeminiAttemptClaimed, Outcome: clippingGeminiOutcomeNotSubmitted, LeaseExpires: leaseUntil, StartedAt: nowUnix, UpdatedAt: nowUnix}
	return clippingGeminiAttemptLease{Attempt: attempt, LeaseToken: leaseToken}, true, nil
}

// AuthorizeClippingGeminiSubmission is the final database fence after the
// shared cancellation registry registration. It atomically marks the request
// potentially submitted so a process restart quarantines it rather than replaying.
func (s *Store) AuthorizeClippingGeminiSubmission(jobID, attemptID, leaseToken, fingerprint, expectedSourceURL string, now time.Time) error {
	if s == nil || s.db == nil || !safeID(jobID) || len(jobID) < len("clipjob_") || jobID[:len("clipjob_")] != "clipjob_" ||
		!safeID(attemptID) || len(attemptID) < len("clipatt_") || attemptID[:len("clipatt_")] != "clipatt_" ||
		leaseToken == "" || !validLowerHexDigest(fingerprint) || expectedSourceURL == "" {
		return ErrClippingInvalidState
	}
	if now.IsZero() {
		now = time.Now()
	}
	nowUnix := now.Unix()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var storedHash string
	var phase ClippingGeminiAttemptPhase
	var leaseUntil int64
	if err := tx.QueryRow(`SELECT lease_token_hash,phase,lease_expires_at FROM clipping_gemini_attempts WHERE job_id=? AND attempt_id=?`, jobID, attemptID).
		Scan(&storedHash, &phase, &leaseUntil); err != nil {
		return ErrClippingStaleAttempt
	}
	if !validClippingLease(leaseToken, storedHash) || phase != ClippingGeminiAttemptClaimed || leaseUntil <= nowUnix {
		return ErrClippingStaleAttempt
	}
	var jobStatus ClippingJobStatus
	var deleted int
	if err := tx.QueryRow(`SELECT status,analysis_deleted FROM clipping_jobs WHERE id=? AND provider='gemini'`, jobID).Scan(&jobStatus, &deleted); err != nil {
		return ErrClippingStaleAttempt
	}
	var stageAttempt string
	var stageStatus ClippingStageStatus
	var stageLease int64
	if err := tx.QueryRow(`SELECT attempt_id,status,lease_expires_at FROM clipping_stages WHERE job_id=? AND name=?`, jobID, ClippingStageAnalysis).
		Scan(&stageAttempt, &stageStatus, &stageLease); err != nil {
		return ErrClippingStaleAttempt
	}
	var sourceKind ClippingSourceKind
	var sourceStatus ClippingSourceStatus
	var rights int
	var retention int64
	var sourceURL string
	if err := tx.QueryRow(`SELECT kind,status,rights_attested,retain_until,source_url FROM clipping_sources WHERE id=(SELECT source_id FROM clipping_jobs WHERE id=?)`, jobID).
		Scan(&sourceKind, &sourceStatus, &rights, &retention, &sourceURL); err != nil {
		return ErrClippingStaleAttempt
	}
	var storedFingerprint string
	if err := tx.QueryRow(`SELECT fingerprint FROM clipping_gemini_requests WHERE job_id=?`, jobID).Scan(&storedFingerprint); err != nil {
		return ErrClippingStaleAttempt
	}
	var claimJob, claimAttempt string
	var claimState clippingGeminiClaimState
	if err := tx.QueryRow(`SELECT job_id,attempt_id,state FROM clipping_gemini_execution_claims WHERE fingerprint=?`, fingerprint).
		Scan(&claimJob, &claimAttempt, &claimState); err != nil {
		return ErrClippingStaleAttempt
	}
	if jobStatus != ClippingJobRunning || deleted != 0 || stageStatus != ClippingStageRunning || stageAttempt != attemptID ||
		stageLease != leaseUntil || (sourceKind != ClippingSourceYouTubeURL && sourceKind != ClippingSourceYouTube) ||
		sourceStatus != ClippingSourceReady || rights != 1 || retention > 0 && retention <= nowUnix || sourceURL == "" ||
		storedFingerprint != fingerprint || sourceURL != expectedSourceURL || claimJob != jobID || claimAttempt != attemptID || claimState != clippingGeminiClaimActive {
		return ErrClippingStaleAttempt
	}
	if _, err := tx.Exec(`UPDATE clipping_gemini_attempts SET phase='submitting',outcome='unknown',updated_at=? WHERE job_id=? AND attempt_id=? AND phase='claimed'`, nowUnix, jobID, attemptID); err != nil {
		return err
	}
	return tx.Commit()
}
