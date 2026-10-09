package app

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	clippingWorkerTranscriberVersion        = "faster-whisper==1.1.1/large-v3"
	clippingWorkerAudioEventsVersion        = "ffmpeg-stream-energy-v1"
	clippingWorkerVisualEventsVersion       = "ffmpeg-scene-motion-v1"
	clippingWorkerContextVersion            = "extractive-lexical-context-v1"
	clippingWorkerPromptVersion             = "none-extractive-no-llm-v1"
	clippingAnalysisPipelineRevisionSetting = "clipping_analysis_pipeline_revision"
)

func clippingAnalysisPipelineRevisionTx(tx *sql.Tx) (string, error) {
	var revision string
	err := tx.QueryRow(`SELECT value FROM settings WHERE key=?`, clippingAnalysisPipelineRevisionSetting).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return clippingDefaultPipelineRevision, nil
	}
	if err != nil {
		return "", err
	}
	if !validClippingPipelineRevision(revision) {
		return "", ErrClippingInvalidState
	}
	return revision, nil
}

func (s *Store) ClippingAnalysisPipelineRevision() (string, error) {
	if s == nil || s.db == nil {
		return "", ErrClippingInvalidState
	}
	var revision string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key=?`, clippingAnalysisPipelineRevisionSetting).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return clippingDefaultPipelineRevision, nil
	}
	if err != nil {
		return "", err
	}
	if !validClippingPipelineRevision(revision) {
		return "", ErrClippingInvalidState
	}
	return revision, nil
}

func persistClippingWorkerConfigSettings(store *Store, encryptedConfig, revision string) error {
	if store == nil || store.db == nil || encryptedConfig == "" || (revision != "" && !validClippingPipelineRevision(revision)) {
		return ErrClippingInvalidState
	}
	tx, err := store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if revision == "" {
		var current string
		err := tx.QueryRow(`SELECT value FROM settings WHERE key=?`, clippingAnalysisPipelineRevisionSetting).Scan(&current)
		if errors.Is(err, sql.ErrNoRows) {
			revision = clippingDefaultPipelineRevision
		} else if err != nil {
			return err
		} else if !validClippingPipelineRevision(current) {
			return ErrClippingInvalidState
		} else {
			revision = current
		}
	}
	now := time.Now().Unix()
	for key, value := range map[string]string{
		clippingWorkerConfigSetting:             encryptedConfig,
		clippingAnalysisPipelineRevisionSetting: revision,
	} {
		if _, err := tx.Exec(`INSERT INTO settings(key,value,updated_at) VALUES(?,?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at`, key, value, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) SetClippingAttemptPipelineRevision(jobID, stageName, attemptID, leaseToken, revision string) error {
	if !safeID(jobID) || !strings.HasPrefix(jobID, "clipjob_") || !validStageName(stageName) || attemptID == "" || leaseToken == "" || !validClippingPipelineRevision(revision) {
		return ErrClippingInvalidState
	}
	now := time.Now().Unix()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	attempt, err := readClippingAttempt(tx, jobID, stageName, attemptID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrClippingStaleAttempt
	}
	if err != nil {
		return err
	}
	if !validClippingLease(leaseToken, attempt.TokenHash) || attempt.Status != "running" || attempt.LeaseExpiresAt <= now {
		return ErrClippingStaleAttempt
	}
	var claimedRevision, claimJobID, claimAttemptID string
	if err := tx.QueryRow(`SELECT pipeline_key,job_id,attempt_id FROM clipping_analysis_claims WHERE job_id=?`, jobID).
		Scan(&claimedRevision, &claimJobID, &claimAttemptID); err != nil {
		return err
	}
	if claimedRevision != revision || claimJobID != jobID || claimAttemptID != attemptID {
		return ErrClippingInvalidState
	}
	if attempt.PipelineRevision != "" && attempt.PipelineRevision != revision {
		return ErrClippingInvalidState
	}
	if _, err := tx.Exec(`UPDATE clipping_stage_attempts SET pipeline_revision=? WHERE attempt_id=?`, revision, attemptID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SetClippingAttemptEstimateTerms(jobID, stageName, attemptID, leaseToken string, rateMicroUSDPerSecond, maxComputeSeconds int64) error {
	if !safeID(jobID) || !strings.HasPrefix(jobID, "clipjob_") || !validStageName(stageName) || attemptID == "" || leaseToken == "" ||
		rateMicroUSDPerSecond <= 0 || maxComputeSeconds < 1 || maxComputeSeconds > clippingWorkerMaxAttemptComputeSeconds ||
		rateMicroUSDPerSecond > math.MaxInt64/maxComputeSeconds {
		return ErrClippingInvalidState
	}
	now := time.Now().Unix()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	attempt, err := readClippingAttempt(tx, jobID, stageName, attemptID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrClippingStaleAttempt
	}
	if err != nil {
		return err
	}
	if !validClippingLease(leaseToken, attempt.TokenHash) || attempt.Status != "running" || attempt.LeaseExpiresAt <= now ||
		rateMicroUSDPerSecond*maxComputeSeconds > attempt.ReservedMicroUSD {
		return ErrClippingStaleAttempt
	}
	if attempt.PipelineRevision == "" {
		revision, revisionErr := clippingAnalysisPipelineRevisionTx(tx)
		if revisionErr != nil {
			return revisionErr
		}
		if _, err := tx.Exec(`UPDATE clipping_stage_attempts SET pipeline_revision=? WHERE attempt_id=?`, revision, attemptID); err != nil {
			return err
		}
	}
	stage, err := readClippingStage(tx, jobID, stageName)
	if err != nil || stage.AttemptID != attemptID || stage.Status != ClippingStageRunning {
		return ErrClippingStaleAttempt
	}
	if attempt.OperatorRateMicroUSDPerSecond != 0 || attempt.MaxComputeSeconds != 0 {
		if attempt.OperatorRateMicroUSDPerSecond == rateMicroUSDPerSecond && attempt.MaxComputeSeconds == maxComputeSeconds {
			return tx.Commit()
		}
		return ErrClippingInvalidState
	}
	if _, err := tx.Exec(`UPDATE clipping_stage_attempts SET operator_rate_micro_usd_per_second=?,max_compute_seconds=?,cost_reconciled=0 WHERE attempt_id=?`, rateMicroUSDPerSecond, maxComputeSeconds, attemptID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE clipping_stages SET cost_reconciled=0,updated_at=? WHERE job_id=? AND name=?`, now, jobID, stageName); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ClippingDispatchBudget(jobID string) (int64, error) {
	if !safeID(jobID) || !strings.HasPrefix(jobID, "clipjob_") {
		return 0, ErrClippingJobNotFound
	}
	var jobLimit, jobSpent, jobReserved, batchLimit, batchSpent, batchReserved int64
	err := s.db.QueryRow(`SELECT job.budget_limit_micro_usd,job.spent_micro_usd,job.reserved_micro_usd,
		batch.budget_limit_micro_usd,batch.spent_micro_usd,batch.reserved_micro_usd
		FROM clipping_jobs AS job JOIN clipping_batches AS batch ON batch.id=job.batch_id WHERE job.id=?`, jobID).
		Scan(&jobLimit, &jobSpent, &jobReserved, &batchLimit, &batchSpent, &batchReserved)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrClippingJobNotFound
	}
	if err != nil {
		return 0, err
	}
	jobRemaining := remainingClippingBudget(jobLimit, jobSpent, jobReserved)
	batchRemaining := remainingClippingBudget(batchLimit, batchSpent, batchReserved)
	if batchRemaining < jobRemaining {
		return batchRemaining, nil
	}
	return jobRemaining, nil
}

func remainingClippingBudget(limit, spent, reserved int64) int64 {
	if limit < 0 || spent < 0 || reserved < 0 || spent > limit || reserved > limit-spent {
		return 0
	}
	return limit - spent - reserved
}

func (s *Store) PendingClippingJobs(limit int) ([]ClippingJob, error) {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.Query(`SELECT `+clippingJobColumns()+` FROM clipping_jobs WHERE status='queued' ORDER BY created_at,id LIMIT ?`, limit)
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

func loadClippingAnalysisCacheTx(tx *sql.Tx, source ClippingSource, revision string) (clippingAnalysisCore, bool, error) {
	var core clippingAnalysisCore
	if source.Status != ClippingSourceReady || !validLowerHexDigest(source.SHA256) || source.DurationMS < 1 || !validClippingPipelineRevision(revision) {
		return core, false, nil
	}
	var sourceSHA, pipelineKey, payload string
	var duration int64
	err := tx.QueryRow(`SELECT source_sha256,pipeline_key,source_duration_ms,payload_json FROM clipping_analysis_cache WHERE source_id=? AND pipeline_revision=? ORDER BY created_at DESC LIMIT 1`, source.ID, revision).
		Scan(&sourceSHA, &pipelineKey, &duration, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return core, false, nil
	}
	if err != nil {
		return core, false, err
	}
	if sourceSHA != source.SHA256 || duration != source.DurationMS {
		return core, false, nil
	}
	core, err = decodeClippingAnalysisCore(payload, source)
	if err != nil {
		return clippingAnalysisCore{}, false, err
	}
	actualPipelineKey, err := clippingAnalysisPipelineKey(core)
	if err != nil {
		return clippingAnalysisCore{}, false, err
	}
	if core.PipelineRevision != revision || pipelineKey != actualPipelineKey {
		return clippingAnalysisCore{}, false, nil
	}
	return core, true, nil
}

func (s *Store) LoadClippingAnalysisCache(sourceID string) (clippingAnalysisCore, bool, error) {
	if !safeClippingSourceID(sourceID) {
		return clippingAnalysisCore{}, false, ErrClippingSourceNotFound
	}
	source, err := s.ClippingSource(sourceID)
	if err != nil {
		return clippingAnalysisCore{}, false, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return clippingAnalysisCore{}, false, err
	}
	defer tx.Rollback()
	revision, err := clippingAnalysisPipelineRevisionTx(tx)
	if err != nil {
		return clippingAnalysisCore{}, false, err
	}
	core, found, err := loadClippingAnalysisCacheTx(tx, source, revision)
	if err != nil {
		return clippingAnalysisCore{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return clippingAnalysisCore{}, false, err
	}
	return core, found, nil
}

func upsertClippingAnalysisCacheTx(tx *sql.Tx, source ClippingSource, core clippingAnalysisCore, now int64) error {
	if core.SourceSHA256 != source.SHA256 || core.SourceDurationMS != source.DurationMS || source.Status != ClippingSourceReady || !validClippingPipelineRevision(core.PipelineRevision) {
		return fmt.Errorf("%w: cached source identity does not match", ErrClippingInvalidArtifact)
	}
	pipelineKey, err := clippingAnalysisPipelineKey(core)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(core)
	if err != nil || len(payload) > maxClippingArtifactPayloadBytes {
		return fmt.Errorf("%w: cached source analysis exceeds 4 MiB", ErrClippingInvalidArtifact)
	}
	if _, err := decodeClippingAnalysisCore(string(payload), source); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM clipping_analysis_cache WHERE source_id=? AND pipeline_revision=?`, source.ID, core.PipelineRevision); err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO clipping_analysis_cache(source_id,source_sha256,pipeline_key,pipeline_revision,source_duration_ms,payload_json,created_at)
		VALUES(?,?,?,?,?,?,?)`, source.ID, source.SHA256, pipelineKey, core.PipelineRevision, source.DurationMS, string(payload), now)
	if err == nil {
		_, err = tx.Exec(`DELETE FROM clipping_analysis_claims WHERE source_id=? AND pipeline_key=?`, source.ID, core.PipelineRevision)
	}
	return err
}

// ClaimClippingAnalysisSource serializes paid inference for one immutable
// source/pipeline. A claim held by an uncertain attempt stays in place until a
// valid core is cached; this prevents a duplicate bill when the callback is
// late or lost.
func (s *Store) ClaimClippingAnalysisSource(jobID string) (bool, error) {
	if !safeID(jobID) || !strings.HasPrefix(jobID, "clipjob_") {
		return false, ErrClippingJobNotFound
	}
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	job, err := scanClippingJob(tx.QueryRow(`SELECT `+clippingJobColumns()+` FROM clipping_jobs WHERE id=?`, jobID))
	if err != nil {
		return false, err
	}
	source, err := scanClippingSource(tx.QueryRow(`SELECT `+clippingSourceColumns()+` FROM clipping_sources WHERE id=?`, job.SourceID))
	if err != nil {
		return false, err
	}
	if source.Status != ClippingSourceReady || !validLowerHexDigest(source.SHA256) {
		return false, ErrClippingInvalidState
	}
	revision, err := clippingAnalysisPipelineRevisionTx(tx)
	if err != nil {
		return false, err
	}
	_, found, err := loadClippingAnalysisCacheTx(tx, source, revision)
	if err != nil || found {
		return false, err
	}
	now := time.Now().Unix()
	rows, err := tx.Query(`SELECT source_sha256,pipeline_key,job_id,attempt_id FROM clipping_analysis_claims WHERE source_id=?`, source.ID)
	if err != nil {
		return false, err
	}
	type sourceClaim struct {
		sourceSHA string
		pipeline  string
		jobID     string
		attemptID string
	}
	var claims []sourceClaim
	for rows.Next() {
		var claim sourceClaim
		if err := rows.Scan(&claim.sourceSHA, &claim.pipeline, &claim.jobID, &claim.attemptID); err != nil {
			rows.Close()
			return false, err
		}
		claims = append(claims, claim)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return false, err
	}
	if err := rows.Close(); err != nil {
		return false, err
	}
	resumeOwnPreDispatchClaim := false
	for _, claim := range claims {
		if claim.attemptID == "" {
			// The dispatcher binds the durable attempt before any network call.
			// An empty attempt is therefore proof that this claim cannot hide a
			// submitted or uncertain worker request. Keep a queued owner's claim
			// idempotent across a restart, but retire abandoned claims from a
			// terminal job or a superseded pipeline revision.
			var ownerStatus ClippingJobStatus
			if err := tx.QueryRow(`SELECT status FROM clipping_jobs WHERE id=?`, claim.jobID).Scan(&ownerStatus); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return false, err
				}
				return false, err
			}
			if claim.pipeline == revision && claim.jobID == jobID && claim.sourceSHA == source.SHA256 && ownerStatus == ClippingJobQueued {
				resumeOwnPreDispatchClaim = true
				continue
			}
			if claim.pipeline != revision || ownerStatus != ClippingJobQueued || claim.sourceSHA != source.SHA256 {
				if _, err := tx.Exec(`DELETE FROM clipping_analysis_claims WHERE source_id=? AND pipeline_key=? AND job_id=? AND attempt_id=''`, source.ID, claim.pipeline, claim.jobID); err != nil {
					return false, err
				}
				continue
			}
			return false, tx.Commit()
		}

		oldAttempt, attemptErr := readClippingAttempt(tx, claim.jobID, ClippingStageAnalysis, claim.attemptID)
		if attemptErr != nil {
			return false, attemptErr
		}
		callbackExpiresAt := clippingAnalysisCallbackExpiry(oldAttempt, source)
		if claim.sourceSHA != source.SHA256 || oldAttempt.SettledAt == 0 || !oldAttempt.CostReconciled || callbackExpiresAt == 0 || callbackExpiresAt > now {
			return false, tx.Commit()
		}
		if _, err := tx.Exec(`DELETE FROM clipping_analysis_claims WHERE source_id=? AND pipeline_key=? AND job_id=? AND attempt_id=?`, source.ID, claim.pipeline, claim.jobID, claim.attemptID); err != nil {
			return false, err
		}
	}
	if resumeOwnPreDispatchClaim {
		return true, tx.Commit()
	}
	result, err := tx.Exec(`INSERT INTO clipping_analysis_claims(source_id,source_sha256,pipeline_key,job_id,claimed_at)
		VALUES(?,?,?,?,?) ON CONFLICT(source_id,pipeline_key) DO NOTHING`, source.ID, source.SHA256, revision, jobID, now)
	if err != nil {
		return false, err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if inserted != 1 {
		return false, tx.Commit()
	}
	return true, tx.Commit()
}

func clippingAnalysisCallbackExpiry(attempt clippingAttemptRecord, source ClippingSource) int64 {
	if attempt.LeaseExpiresAt <= 0 || attempt.LeaseExpiresAt > math.MaxInt64-int64(clippingCallbackAccountingGrace.Seconds()) {
		return 0
	}
	expiresAt := attempt.LeaseExpiresAt + int64(clippingCallbackAccountingGrace.Seconds())
	if source.RetainUntil > 0 && source.RetainUntil < expiresAt {
		expiresAt = source.RetainUntil
	}
	return expiresAt
}

func (s *Store) SetClippingAnalysisClaimAttempt(jobID, attemptID string) error {
	if !safeID(jobID) || !strings.HasPrefix(jobID, "clipjob_") || attemptID == "" {
		return ErrClippingInvalidState
	}
	job, err := s.ClippingJob(jobID)
	if err != nil {
		return err
	}
	source, err := s.ClippingSource(job.SourceID)
	if err != nil {
		return err
	}
	pipelineKey, err := s.ClippingAnalysisPipelineRevision()
	if err != nil {
		return err
	}
	result, err := s.db.Exec(`UPDATE clipping_analysis_claims SET attempt_id=? WHERE source_id=? AND source_sha256=? AND pipeline_key=? AND job_id=? AND attempt_id=''`, attemptID, source.ID, source.SHA256, pipelineKey, jobID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrClippingInvalidState
	}
	return nil
}

func (s *Store) ReleaseClippingAnalysisSourceClaim(jobID string) error {
	if !safeID(jobID) || !strings.HasPrefix(jobID, "clipjob_") {
		return ErrClippingJobNotFound
	}
	_, err := s.db.Exec(`DELETE FROM clipping_analysis_claims WHERE job_id=? AND attempt_id=''`, jobID)
	return err
}

// ReleaseClippingAnalysisSourceClaimBeforeSubmit removes a claim only after a
// caller has failed a reserved attempt with known-zero cost and before any
// network dispatch began. Unknown accepted outcomes keep their claim through
// explicit invoice settlement and callback-capability expiry.
func (s *Store) ReleaseClippingAnalysisSourceClaimBeforeSubmit(jobID, attemptID string) error {
	if !safeID(jobID) || !strings.HasPrefix(jobID, "clipjob_") || !safeID(attemptID) || !strings.HasPrefix(attemptID, "clipatt_") {
		return ErrClippingInvalidState
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	attempt, err := readClippingAttempt(tx, jobID, ClippingStageAnalysis, attemptID)
	if err != nil {
		return err
	}
	if (attempt.Status != "failed" && attempt.Status != "settled_canceled") || attempt.SettledAt == 0 ||
		attempt.ActualMicroUSD != 0 || attempt.CallbackReceivedAt != 0 || !attempt.CostReconciled ||
		attempt.ReconciliationReference != "known_no_submission" {
		return ErrClippingInvalidState
	}
	result, err := tx.Exec(`DELETE FROM clipping_analysis_claims WHERE job_id=? AND attempt_id=?`, jobID, attemptID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrClippingInvalidState
	}
	return tx.Commit()
}

func (s *Store) TryCompleteClippingJobFromAnalysisCache(jobID string) (bool, error) {
	if !safeID(jobID) || !strings.HasPrefix(jobID, "clipjob_") {
		return false, ErrClippingJobNotFound
	}
	now := time.Now().Unix()
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	job, err := scanClippingJob(tx.QueryRow(`SELECT `+clippingJobColumns()+` FROM clipping_jobs WHERE id=?`, jobID))
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrClippingJobNotFound
	}
	if err != nil {
		return false, err
	}
	if job.Status != ClippingJobQueued {
		return false, nil
	}
	source, err := scanClippingSource(tx.QueryRow(`SELECT `+clippingSourceColumns()+` FROM clipping_sources WHERE id=?`, job.SourceID))
	if err != nil {
		return false, err
	}
	revision, err := clippingAnalysisPipelineRevisionTx(tx)
	if err != nil {
		return false, err
	}
	core, found, err := loadClippingAnalysisCacheTx(tx, source, revision)
	if err != nil || !found {
		return false, err
	}
	artifact, err := buildCachedClippingAnalysisArtifact(job, source, core, 1)
	if err != nil {
		return false, err
	}
	stage, err := readClippingStage(tx, job.ID, ClippingStageAnalysis)
	if errors.Is(err, ErrClippingStageNotFound) {
		key := job.ID + ":" + ClippingStageAnalysis
		if _, err := tx.Exec(`INSERT INTO clipping_stages(job_id,name,status,idempotency_key,created_at,updated_at) VALUES(?,?,'queued',?,?,?)`, job.ID, ClippingStageAnalysis, key, now, now); err != nil {
			return false, err
		}
		stage = ClippingStage{JobID: job.ID, Name: ClippingStageAnalysis, Status: ClippingStageQueued, IdempotencyKey: key}
	} else if err != nil {
		return false, err
	}
	if stage.Status != ClippingStageQueued || stage.AttemptID != "" {
		return false, nil
	}
	rangesJSON, err := validateClippingArtifact(artifact, job.ID, source.DurationMS)
	if err != nil {
		return false, err
	}
	if err := insertClippingArtifact(tx, artifact, rangesJSON, now); err != nil {
		return false, err
	}
	if _, err := tx.Exec(`UPDATE clipping_stages SET status='completed',estimated_micro_usd=0,cost_reconciled=1,error='',updated_at=? WHERE job_id=? AND name=?`, now, job.ID, ClippingStageAnalysis); err != nil {
		return false, err
	}
	if _, err := refreshClippingJobStatus(tx, job.ID, now); err != nil {
		return false, err
	}
	if _, err := refreshClippingBatchStatus(tx, job.BatchID, now); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func buildCachedClippingAnalysisArtifact(job ClippingJob, source ClippingSource, core clippingAnalysisCore, version int) (ClippingArtifact, error) {
	artifact, err := buildClippingAnalysisArtifact(job, source, core, version, 0, 0, 0)
	if err != nil {
		return ClippingArtifact{}, err
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(artifact.PayloadJSON), &payload); err != nil {
		return ClippingArtifact{}, err
	}
	for key, value := range map[string]any{
		"cost_basis":         "reused_source_analysis_no_new_worker_charge",
		"actual_cost_status": "not_applicable_cached",
		"analysis_reused":    true,
	} {
		encoded, err := json.Marshal(value)
		if err != nil {
			return ClippingArtifact{}, err
		}
		payload[key] = encoded
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ClippingArtifact{}, err
	}
	artifact.PayloadJSON = string(encoded)
	if _, err := validateClippingArtifact(artifact, job.ID, source.DurationMS); err != nil {
		return ClippingArtifact{}, err
	}
	return artifact, nil
}

func clippingArtifactDigest(artifact ClippingArtifact, estimate int64) (string, error) {
	encoded, err := json.Marshal(struct {
		Artifact ClippingArtifact `json:"artifact"`
		Estimate int64            `json:"estimate_micro_usd"`
	}{artifact, estimate})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// CompleteClippingStageEstimate records a successful v2 callback without
// treating its operator-rate estimate as an invoice. The reservation remains
// held until an operator records the corresponding Modal invoice.
func (s *Store) CompleteClippingStageEstimate(jobID, name, attemptID, leaseToken string, estimateMicroUSD, computeSeconds int64, artifact ClippingArtifact, core clippingAnalysisCore) (ClippingStage, bool, bool, error) {
	if !safeID(jobID) || !strings.HasPrefix(jobID, "clipjob_") || !validStageName(name) || attemptID == "" || leaseToken == "" || estimateMicroUSD <= 0 || computeSeconds <= 0 {
		return ClippingStage{}, false, false, ErrClippingStaleAttempt
	}
	digest, err := clippingArtifactDigest(artifact, estimateMicroUSD)
	if err != nil {
		return ClippingStage{}, false, false, err
	}
	now := time.Now().Unix()
	tx, err := s.db.Begin()
	if err != nil {
		return ClippingStage{}, false, false, err
	}
	defer tx.Rollback()
	attempt, err := readClippingAttempt(tx, jobID, name, attemptID)
	if errors.Is(err, sql.ErrNoRows) {
		return ClippingStage{}, false, false, ErrClippingStaleAttempt
	}
	if err != nil {
		return ClippingStage{}, false, false, err
	}
	if !validClippingLease(leaseToken, attempt.TokenHash) {
		return ClippingStage{}, false, false, ErrClippingStaleAttempt
	}
	stage, err := readClippingStage(tx, jobID, name)
	if err != nil {
		return ClippingStage{}, false, false, err
	}
	if attempt.CallbackReceivedAt > 0 {
		if attempt.CallbackDigest != digest || attempt.EstimatedMicroUSD != estimateMicroUSD {
			return ClippingStage{}, false, false, ErrClippingStaleAttempt
		}
		if err := tx.Commit(); err != nil {
			return ClippingStage{}, false, false, err
		}
		return stage, true, stage.Status == ClippingStageCompleted, nil
	}
	job, err := scanClippingJob(tx.QueryRow(`SELECT `+clippingJobColumns()+` FROM clipping_jobs WHERE id=?`, jobID))
	if err != nil {
		return ClippingStage{}, false, false, err
	}
	source, err := scanClippingSource(tx.QueryRow(`SELECT `+clippingSourceColumns()+` FROM clipping_sources WHERE id=?`, job.SourceID))
	if err != nil {
		return ClippingStage{}, false, false, err
	}
	if attempt.OperatorRateMicroUSDPerSecond <= 0 || attempt.MaxComputeSeconds <= 0 || computeSeconds > attempt.MaxComputeSeconds ||
		attempt.OperatorRateMicroUSDPerSecond > math.MaxInt64/computeSeconds || estimateMicroUSD != attempt.OperatorRateMicroUSDPerSecond*computeSeconds || estimateMicroUSD > attempt.ReservedMicroUSD {
		return ClippingStage{}, false, false, fmt.Errorf("%w: callback estimate does not match the recorded dispatch ceiling", ErrClippingInvalidArtifact)
	}
	if artifact.Type != clippingAnalysisArtifactType || artifact.SchemaVersion != clippingAnalysisSchemaVersion || artifact.Version != 1 || len(artifact.TimeRanges) != 0 {
		return ClippingStage{}, false, false, fmt.Errorf("%w: source analysis callback must contain an empty candidate range list", ErrClippingInvalidArtifact)
	}
	coreJSON, err := json.Marshal(core)
	if err != nil {
		return ClippingStage{}, false, false, err
	}
	decodedCore, err := decodeClippingAnalysisCore(string(coreJSON), source)
	if err != nil {
		return ClippingStage{}, false, false, err
	}
	if !validClippingPipelineRevision(attempt.PipelineRevision) || decodedCore.PipelineRevision != attempt.PipelineRevision {
		return ClippingStage{}, false, false, fmt.Errorf("%w: callback pipeline revision does not match its dispatch", ErrClippingInvalidArtifact)
	}
	if artifact.JobID != jobID || artifact.SourceDurationMS != source.DurationMS {
		return ClippingStage{}, false, false, ErrClippingInvalidArtifact
	}
	current := attempt.Status == "running" && stage.AttemptID == attemptID && stage.Status == ClippingStageRunning && stage.LeaseExpiresAt > now && !clippingJobTerminal(job.Status) && source.Status == ClippingSourceReady
	if current {
		finalArtifact, err := buildClippingAnalysisArtifact(job, source, decodedCore, 1, estimateMicroUSD, attempt.OperatorRateMicroUSDPerSecond, computeSeconds)
		if err != nil {
			return ClippingStage{}, false, false, err
		}
		rangesJSON, err := validateClippingArtifact(finalArtifact, jobID, source.DurationMS)
		if err != nil {
			return ClippingStage{}, false, false, err
		}
		if err := insertClippingArtifact(tx, finalArtifact, rangesJSON, now); err != nil {
			return ClippingStage{}, false, false, err
		}
		if err := upsertClippingAnalysisCacheTx(tx, source, decodedCore, now); err != nil {
			return ClippingStage{}, false, false, err
		}
		if _, err := tx.Exec(`UPDATE clipping_stage_attempts SET status='completed',estimated_micro_usd=?,cost_reconciled=0,callback_received_at=?,callback_digest=?,error='' WHERE attempt_id=?`, estimateMicroUSD, now, digest, attemptID); err != nil {
			return ClippingStage{}, false, false, err
		}
		if _, err := tx.Exec(`UPDATE clipping_stages SET status='completed',estimated_micro_usd=?,cost_reconciled=0,lease_expires_at=0,error='',updated_at=? WHERE job_id=? AND name=?`, estimateMicroUSD, now, jobID, name); err != nil {
			return ClippingStage{}, false, false, err
		}
		if _, err := refreshClippingJobStatus(tx, jobID, now); err != nil {
			return ClippingStage{}, false, false, err
		}
		if _, err := refreshClippingBatchStatus(tx, job.BatchID, now); err != nil {
			return ClippingStage{}, false, false, err
		}
		if err := tx.Commit(); err != nil {
			return ClippingStage{}, false, false, err
		}
		stage.Status, stage.EstimatedMicroUSD, stage.CostReconciled, stage.LeaseExpiresAt, stage.UpdatedAt = ClippingStageCompleted, estimateMicroUSD, false, 0, now
		return stage, false, true, nil
	}

	// A valid late result settles callback receipt and preserves the hold, but
	// never publishes stale/canceled transcripts or advances the job.
	attemptStatus := "settled_stale"
	if job.Status == ClippingJobCanceled || stage.Status == ClippingStageCanceled {
		attemptStatus = "settled_canceled"
	}
	if attempt.SettledAt > 0 && attempt.CostReconciled {
		if _, err := tx.Exec(`UPDATE clipping_stage_attempts SET status=?,estimated_micro_usd=?,callback_received_at=?,callback_digest=?,error='late analysis callback; prior invoice reconciliation retained' WHERE attempt_id=?`, attemptStatus, estimateMicroUSD, now, digest, attemptID); err != nil {
			return ClippingStage{}, false, false, err
		}
	} else if _, err := tx.Exec(`UPDATE clipping_stage_attempts SET status=?,estimated_micro_usd=?,cost_reconciled=0,callback_received_at=?,callback_digest=?,error='late analysis callback; invoice reconciliation required' WHERE attempt_id=?`, attemptStatus, estimateMicroUSD, now, digest, attemptID); err != nil {
		return ClippingStage{}, false, false, err
	}
	if stage.AttemptID == attemptID {
		if stage.Status != ClippingStageCanceled {
			if attempt.SettledAt > 0 && attempt.CostReconciled {
				if _, err := tx.Exec(`UPDATE clipping_stages SET estimated_micro_usd=?,updated_at=? WHERE job_id=? AND name=?`, estimateMicroUSD, now, jobID, name); err != nil {
					return ClippingStage{}, false, false, err
				}
			} else {
				if _, err := tx.Exec(`UPDATE clipping_stages SET status='failed',estimated_micro_usd=?,cost_reconciled=0,lease_expires_at=0,error='Late analysis result was not published; invoice reconciliation required',updated_at=? WHERE job_id=? AND name=?`, estimateMicroUSD, now, jobID, name); err != nil {
					return ClippingStage{}, false, false, err
				}
				if _, err := tx.Exec(`UPDATE clipping_jobs SET status='failed',error='Late analysis result was not published; invoice reconciliation required',updated_at=? WHERE id=? AND status<>'canceled'`, now, jobID); err != nil {
					return ClippingStage{}, false, false, err
				}
			}
		} else if attempt.SettledAt > 0 && attempt.CostReconciled {
			if _, err := tx.Exec(`UPDATE clipping_stages SET estimated_micro_usd=?,updated_at=? WHERE job_id=? AND name=?`, estimateMicroUSD, now, jobID, name); err != nil {
				return ClippingStage{}, false, false, err
			}
		} else if _, err := tx.Exec(`UPDATE clipping_stages SET estimated_micro_usd=?,cost_reconciled=0,updated_at=? WHERE job_id=? AND name=?`, estimateMicroUSD, now, jobID, name); err != nil {
			return ClippingStage{}, false, false, err
		}
	}
	if _, err := refreshClippingBatchStatus(tx, job.BatchID, now); err != nil {
		return ClippingStage{}, false, false, err
	}
	if err := tx.Commit(); err != nil {
		return ClippingStage{}, false, false, err
	}
	stage, err = scanClippingStage(s.db.QueryRow(`SELECT `+clippingStageColumns()+` FROM clipping_stages WHERE job_id=? AND name=?`, jobID, name))
	return stage, false, false, err
}

func (s *Store) ReconcileClippingStageCost(jobID, attemptID string, actualCostMicroUSD int64, reference string) (ClippingStage, bool, error) {
	reference = strings.TrimSpace(reference)
	if !safeID(jobID) || !strings.HasPrefix(jobID, "clipjob_") || !safeID(attemptID) || !strings.HasPrefix(attemptID, "clipatt_") || actualCostMicroUSD < 0 || len(reference) == 0 || len(reference) > 256 || !utf8.ValidString(reference) || strings.ContainsAny(reference, "\r\n\x00") {
		return ClippingStage{}, false, ErrClippingInvalidState
	}
	now := time.Now().Unix()
	tx, err := s.db.Begin()
	if err != nil {
		return ClippingStage{}, false, err
	}
	defer tx.Rollback()
	var name string
	if err := tx.QueryRow(`SELECT stage_name FROM clipping_stage_attempts WHERE attempt_id=? AND job_id=?`, attemptID, jobID).Scan(&name); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ClippingStage{}, false, ErrClippingStaleAttempt
		}
		return ClippingStage{}, false, err
	}
	attempt, err := readClippingAttempt(tx, jobID, name, attemptID)
	if err != nil {
		return ClippingStage{}, false, err
	}
	stage, err := readClippingStage(tx, jobID, name)
	if err != nil {
		return ClippingStage{}, false, err
	}
	if attempt.SettledAt > 0 {
		if attempt.ActualMicroUSD != actualCostMicroUSD || attempt.ReconciliationReference != reference {
			return ClippingStage{}, false, ErrClippingInvalidState
		}
		if err := tx.Commit(); err != nil {
			return ClippingStage{}, false, err
		}
		return stage, true, nil
	}
	if stage.AttemptID != attemptID || attempt.ReservedMicroUSD <= 0 {
		return ClippingStage{}, false, ErrClippingInvalidState
	}
	if attempt.CallbackReceivedAt == 0 && attempt.Status != "canceled" && (attempt.Status == "running" || attempt.LeaseExpiresAt > now) {
		return ClippingStage{}, false, ErrClippingInvalidState
	}
	if attempt.CallbackReceivedAt > 0 && attempt.EstimatedMicroUSD <= 0 {
		return ClippingStage{}, false, ErrClippingInvalidState
	}
	job, err := scanClippingJob(tx.QueryRow(`SELECT `+clippingJobColumns()+` FROM clipping_jobs WHERE id=?`, jobID))
	if err != nil {
		return ClippingStage{}, false, err
	}
	if err := releaseClippingReservation(tx, &job, attempt.ReservedMicroUSD, now); err != nil {
		return ClippingStage{}, false, err
	}
	if err := settleClippingCost(tx, &job, actualCostMicroUSD, now); err != nil {
		return ClippingStage{}, false, err
	}
	if _, err := tx.Exec(`UPDATE clipping_stage_attempts SET actual_micro_usd=?,cost_reconciled=1,reconciliation_reference=?,settled_at=? WHERE attempt_id=?`, actualCostMicroUSD, reference, now, attemptID); err != nil {
		return ClippingStage{}, false, err
	}
	if _, err := tx.Exec(`UPDATE clipping_stages SET actual_micro_usd=actual_micro_usd+?,reserved_micro_usd=0,cost_reconciled=1,updated_at=? WHERE job_id=? AND name=?`, actualCostMicroUSD, now, jobID, name); err != nil {
		return ClippingStage{}, false, err
	}
	// Explicit reconciliation settles an expired dispatch whose callback did
	// not arrive. Make that job retryable, but keep the source claim until its
	// callback capability expires so a late worker cannot trigger duplicate
	// paid analysis during the validity window. Canceled work stays terminal.
	if stage.Status == ClippingStageUncertain && job.Status == ClippingJobPausedBudget {
		if _, err := tx.Exec(`UPDATE clipping_stages SET status='failed',error='Dispatch was reconciled without a callback; retry is available after the callback window',updated_at=? WHERE job_id=? AND name=? AND status='uncertain'`, now, jobID, name); err != nil {
			return ClippingStage{}, false, err
		}
		if _, err := tx.Exec(`UPDATE clipping_jobs SET status='failed',error='Dispatch was reconciled without a callback; retry is available after the callback window',updated_at=? WHERE id=? AND status='paused_budget'`, now, jobID); err != nil {
			return ClippingStage{}, false, err
		}
	}
	if _, err := refreshClippingBatchStatus(tx, job.BatchID, now); err != nil {
		return ClippingStage{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return ClippingStage{}, false, err
	}
	stage, err = scanClippingStage(s.db.QueryRow(`SELECT `+clippingStageColumns()+` FROM clipping_stages WHERE job_id=? AND name=?`, jobID, name))
	return stage, false, err
}

func (s *Store) UpdateClippingJobSelection(jobID string, requested ClippingJobCreate) (ClippingJob, ClippingArtifact, error) {
	requested.SourceID = ""
	selection, err := normalizeClippingSelection(requested)
	if err != nil || !safeID(jobID) || !strings.HasPrefix(jobID, "clipjob_") {
		return ClippingJob{}, ClippingArtifact{}, ErrClippingInvalidState
	}
	now := time.Now().Unix()
	tx, err := s.db.Begin()
	if err != nil {
		return ClippingJob{}, ClippingArtifact{}, err
	}
	defer tx.Rollback()
	job, err := scanClippingJob(tx.QueryRow(`SELECT `+clippingJobColumns()+` FROM clipping_jobs WHERE id=?`, jobID))
	if errors.Is(err, sql.ErrNoRows) {
		return ClippingJob{}, ClippingArtifact{}, ErrClippingJobNotFound
	}
	if err != nil {
		return ClippingJob{}, ClippingArtifact{}, err
	}
	if job.Status != ClippingJobCompleted {
		return ClippingJob{}, ClippingArtifact{}, ErrClippingInvalidState
	}
	source, err := scanClippingSource(tx.QueryRow(`SELECT `+clippingSourceColumns()+` FROM clipping_sources WHERE id=?`, job.SourceID))
	if err != nil {
		return ClippingJob{}, ClippingArtifact{}, err
	}
	revision, err := clippingJobAnalysisPipelineRevisionTx(tx, jobID)
	if err != nil {
		return ClippingJob{}, ClippingArtifact{}, err
	}
	core, found, err := loadClippingAnalysisCacheTx(tx, source, revision)
	if err != nil {
		return ClippingJob{}, ClippingArtifact{}, err
	}
	if !found {
		return ClippingJob{}, ClippingArtifact{}, errClippingAnalysisUnavailable
	}
	if job.ContentType == selection.ContentType && job.MinClipSeconds == selection.MinClipSeconds && job.MaxClipSeconds == selection.MaxClipSeconds && job.CandidateLimit == selection.CandidateLimit {
		artifacts, err := queryClippingArtifactsTx(tx, jobID)
		if err != nil || len(artifacts) == 0 {
			return ClippingJob{}, ClippingArtifact{}, err
		}
		if err := tx.Commit(); err != nil {
			return ClippingJob{}, ClippingArtifact{}, err
		}
		return job, artifacts[len(artifacts)-1], nil
	}
	job.ContentType, job.MinClipSeconds, job.MaxClipSeconds, job.CandidateLimit = selection.ContentType, selection.MinClipSeconds, selection.MaxClipSeconds, selection.CandidateLimit
	if _, err := tx.Exec(`UPDATE clipping_jobs SET content_type=?,min_clip_seconds=?,max_clip_seconds=?,candidate_limit=?,updated_at=? WHERE id=?`, job.ContentType, job.MinClipSeconds, job.MaxClipSeconds, job.CandidateLimit, now, jobID); err != nil {
		return ClippingJob{}, ClippingArtifact{}, err
	}
	var version int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(version),0) FROM clipping_artifacts WHERE job_id=? AND artifact_type=?`, jobID, clippingAnalysisArtifactType).Scan(&version); err != nil {
		return ClippingJob{}, ClippingArtifact{}, err
	}
	artifact, err := buildCachedClippingAnalysisArtifact(job, source, core, version+1)
	if err != nil {
		return ClippingJob{}, ClippingArtifact{}, err
	}
	stage, err := readClippingStage(tx, jobID, ClippingStageAnalysis)
	if err == nil && stage.AttemptID != "" {
		attempt, attemptErr := readClippingAttempt(tx, jobID, ClippingStageAnalysis, stage.AttemptID)
		if attemptErr != nil {
			return ClippingJob{}, ClippingArtifact{}, attemptErr
		}
		if err := annotateClippingArtifactCost(&artifact, attempt.EstimatedMicroUSD, attempt.OperatorRateMicroUSDPerSecond, attempt.MaxComputeSeconds); err != nil {
			return ClippingJob{}, ClippingArtifact{}, err
		}
	} else if err != nil && !errors.Is(err, ErrClippingStageNotFound) {
		return ClippingJob{}, ClippingArtifact{}, err
	}
	rangesJSON, err := validateClippingArtifact(artifact, jobID, source.DurationMS)
	if err != nil {
		return ClippingJob{}, ClippingArtifact{}, err
	}
	if err := insertClippingArtifact(tx, artifact, rangesJSON, now); err != nil {
		return ClippingJob{}, ClippingArtifact{}, err
	}
	if err := tx.Commit(); err != nil {
		return ClippingJob{}, ClippingArtifact{}, err
	}
	job.UpdatedAt = now
	artifact.CreatedAt = now
	return job, artifact, nil
}

func clippingJobAnalysisPipelineRevisionTx(tx *sql.Tx, jobID string) (string, error) {
	var payload string
	err := tx.QueryRow(`SELECT payload_json FROM clipping_artifacts WHERE job_id=? AND artifact_type=? ORDER BY version DESC LIMIT 1`, jobID, clippingAnalysisArtifactType).Scan(&payload)
	if err != nil {
		return "", err
	}
	var artifactPayload struct {
		PipelineRevision string `json:"pipeline_revision"`
	}
	if err := json.Unmarshal([]byte(payload), &artifactPayload); err != nil || !validClippingPipelineRevision(artifactPayload.PipelineRevision) {
		return "", ErrClippingInvalidArtifact
	}
	return artifactPayload.PipelineRevision, nil
}

func annotateClippingArtifactCost(artifact *ClippingArtifact, estimate, rate, compute int64) error {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(artifact.PayloadJSON), &payload); err != nil {
		return err
	}
	for key, value := range map[string]any{
		"cost_basis":                "operator_declared_worker_second_rate_estimate",
		"cost_estimate_micro_usd":   estimate,
		"rate_micro_usd_per_second": rate,
		"compute_seconds":           compute,
	} {
		encoded, err := json.Marshal(value)
		if err != nil {
			return err
		}
		payload[key] = encoded
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	artifact.PayloadJSON = string(encoded)
	return nil
}

func queryClippingArtifactsTx(tx *sql.Tx, jobID string) ([]ClippingArtifact, error) {
	rows, err := tx.Query(`SELECT artifact_type,schema_version,version,source_duration_ms,time_ranges_json,payload_json,created_at FROM clipping_artifacts WHERE job_id=? ORDER BY artifact_type,version`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	artifacts := make([]ClippingArtifact, 0)
	for rows.Next() {
		var artifact ClippingArtifact
		var rangesJSON string
		artifact.JobID = jobID
		if err := rows.Scan(&artifact.Type, &artifact.SchemaVersion, &artifact.Version, &artifact.SourceDurationMS, &rangesJSON, &artifact.PayloadJSON, &artifact.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(rangesJSON), &artifact.TimeRanges); err != nil {
			return nil, err
		}
		artifacts = append(artifacts, artifact)
	}
	return artifacts, rows.Err()
}
