package app

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
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

// SetClippingAttemptComputeLimit stores the validated worker timeout needed to
// check the callback's genuine runtime. Legacy budget/rate columns stay inert.
func (s *Store) SetClippingAttemptComputeLimit(jobID, stageName, attemptID, leaseToken string, maxComputeSeconds int64) error {
	if !safeID(jobID) || !strings.HasPrefix(jobID, "clipjob_") || !validStageName(stageName) || attemptID == "" || leaseToken == "" ||
		maxComputeSeconds < 1 || maxComputeSeconds > clippingWorkerMaxAttemptComputeSeconds {
		return ErrClippingInvalidState
	}
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
	if !validClippingLease(leaseToken, attempt.TokenHash) || attempt.Status != "running" || attempt.LeaseExpiresAt <= time.Now().Unix() {
		return ErrClippingStaleAttempt
	}
	var stageAttempt string
	var stageStatus ClippingStageStatus
	var jobStatus ClippingJobStatus
	if err := tx.QueryRow(`SELECT stage.attempt_id,stage.status,job.status FROM clipping_stages AS stage
		JOIN clipping_jobs AS job ON job.id=stage.job_id WHERE stage.job_id=? AND stage.name=?`, jobID, stageName).
		Scan(&stageAttempt, &stageStatus, &jobStatus); err != nil {
		return ErrClippingStaleAttempt
	}
	if stageAttempt != attemptID || stageStatus != ClippingStageRunning || clippingJobTerminal(jobStatus) {
		return ErrClippingStaleAttempt
	}
	if attempt.MaxComputeSeconds != 0 && attempt.MaxComputeSeconds != maxComputeSeconds {
		return ErrClippingInvalidState
	}
	_, err = tx.Exec(`UPDATE clipping_stage_attempts SET max_compute_seconds=? WHERE attempt_id=?`, maxComputeSeconds, attemptID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) PendingClippingJobs(limit int) ([]ClippingJob, error) {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.Query(`SELECT `+clippingJobColumns()+` FROM clipping_jobs WHERE status='queued' AND analysis_deleted=0 ORDER BY created_at,id LIMIT ?`, limit)
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

// DeleteClippingAnalysis removes user-visible results while preserving the
// job, stage, and attempt audit. An active attempt is canceled in the same
// transaction; its callback claim remains until the issued capability window
// closes or a late callback settles it.
func (s *Store) DeleteClippingAnalysis(jobID string) error {
	if !safeID(jobID) || !strings.HasPrefix(jobID, "clipjob_") {
		return ErrClippingJobNotFound
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	job, err := scanClippingJob(tx.QueryRow(`SELECT `+clippingJobColumns()+` FROM clipping_jobs WHERE id=?`, jobID))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrClippingJobNotFound
	}
	if err != nil {
		return err
	}
	if job.AnalysisDeleted {
		return tx.Commit()
	}
	if !clippingJobTerminal(job.Status) {
		if err := cancelClippingJobTx(tx, &job, time.Now().Unix()); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM clipping_artifacts WHERE job_id=?`, jobID); err != nil {
		return err
	}
	// The cache contains the source transcript and analysis core. Removing it
	// prevents a later Analyze action from silently recreating deleted results.
	if _, err := tx.Exec(`DELETE FROM clipping_analysis_cache WHERE source_id=?`, job.SourceID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE clipping_jobs SET analysis_deleted=1,updated_at=? WHERE id=?`, time.Now().Unix(), jobID); err != nil {
		return err
	}
	return tx.Commit()
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

func upsertClippingAnalysisCacheTx(tx *sql.Tx, source ClippingSource, core clippingAnalysisCore, computeSeconds int64, now int64) error {
	if core.SourceSHA256 != source.SHA256 || core.SourceDurationMS != source.DurationMS || source.Status != ClippingSourceReady || !validClippingPipelineRevision(core.PipelineRevision) {
		return fmt.Errorf("%w: cached source identity does not match", ErrClippingInvalidArtifact)
	}
	if computeSeconds < 0 || computeSeconds > clippingWorkerMaxAttemptComputeSeconds {
		return fmt.Errorf("%w: worker runtime is invalid", ErrClippingInvalidArtifact)
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
	_, err = tx.Exec(`INSERT INTO clipping_analysis_cache(source_id,source_sha256,pipeline_key,pipeline_revision,source_duration_ms,payload_json,compute_seconds,created_at)
		VALUES(?,?,?,?,?,?,?,?)`, source.ID, source.SHA256, pipelineKey, core.PipelineRevision, source.DurationMS, string(payload), computeSeconds, now)
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
	if job.AnalysisDeleted || job.Status != ClippingJobQueued {
		return false, nil
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
		callbackExpiresAt, expiryErr := clippingIssuedCallbackExpiryTx(tx, oldAttempt, source)
		if expiryErr != nil {
			return false, expiryErr
		}
		if claim.sourceSHA != source.SHA256 || oldAttempt.SettledAt == 0 || callbackExpiresAt > now {
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
	if attempt.LeaseExpiresAt <= 0 || attempt.LeaseExpiresAt > math.MaxInt64-int64(clippingCallbackCapabilityGrace.Seconds()) {
		return 0
	}
	expiresAt := attempt.LeaseExpiresAt + int64(clippingCallbackCapabilityGrace.Seconds())
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

// ReleaseClippingAnalysisSourceClaimBeforeSubmit removes a claim only after
// dispatch is known not to have started. An unknown accepted outcome keeps its
// source claim until the callback capability expires.
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
	if job.AnalysisDeleted || job.Status != ClippingJobQueued {
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
	var computeSeconds int64
	if err := tx.QueryRow(`SELECT compute_seconds FROM clipping_analysis_cache WHERE source_id=? AND source_sha256=? AND pipeline_revision=?`, source.ID, source.SHA256, revision).Scan(&computeSeconds); err != nil {
		return false, err
	}
	artifact, err := buildCachedClippingAnalysisArtifact(job, source, core, 1, computeSeconds)
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
	if _, err := tx.Exec(`UPDATE clipping_stages SET status='completed',error='',updated_at=? WHERE job_id=? AND name=?`, now, job.ID, ClippingStageAnalysis); err != nil {
		return false, err
	}
	if _, err := refreshClippingJobStatus(tx, job.ID, now); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func buildCachedClippingAnalysisArtifact(job ClippingJob, source ClippingSource, core clippingAnalysisCore, version int, computeSeconds int64) (ClippingArtifact, error) {
	// Older additive-migration cache rows have no runtime measurement. Zero is
	// the truthful compatibility value and remains valid for a reused analysis.
	rate := int64(0)
	if computeSeconds > 0 {
		rate = clippingWorkerProtocolCompatibilityRate
	}
	artifact, err := buildClippingAnalysisArtifact(job, source, core, version, computeSeconds, rate, computeSeconds)
	if err != nil {
		return ClippingArtifact{}, err
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(artifact.PayloadJSON), &payload); err != nil {
		return ClippingArtifact{}, err
	}
	for _, key := range []string{"cost_basis", "cost_estimate_micro_usd", "rate_micro_usd_per_second", "actual_cost_status"} {
		delete(payload, key)
	}
	payload["analysis_reused"] = json.RawMessage("true")
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

func stripClippingArtifactCostMetadata(artifact *ClippingArtifact) error {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(artifact.PayloadJSON), &payload); err != nil {
		return err
	}
	for _, key := range []string{"cost_basis", "cost_estimate_micro_usd", "rate_micro_usd_per_second", "actual_cost_status"} {
		delete(payload, key)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	artifact.PayloadJSON = string(encoded)
	return nil
}

func releaseLegacyClippingHold(tx *sql.Tx, job *ClippingJob, attempt clippingAttemptRecord, stageName string, now int64) error {
	if attempt.SettledAt > 0 {
		return nil
	}
	holdReleased := false
	if attempt.ReservedMicroUSD > 0 {
		var batchReserved int64
		if err := tx.QueryRow(`SELECT reserved_micro_usd FROM clipping_batches WHERE id=?`, job.BatchID).Scan(&batchReserved); err != nil {
			return err
		}
		// Legacy attempts can retain their original reservation value after
		// settlement. Decrement only when both aggregates still contain this hold.
		if attempt.ReservedMicroUSD <= job.ReservedMicroUSD && attempt.ReservedMicroUSD <= batchReserved {
			if err := releaseLegacyClippingReservation(tx, job, attempt.ReservedMicroUSD, now); err != nil {
				return err
			}
			holdReleased = true
		}
	}
	if attempt.CostReconciled && !holdReleased {
		return nil
	}
	if _, err := tx.Exec(`UPDATE clipping_stage_attempts SET reserved_micro_usd=0,actual_micro_usd=0,estimated_micro_usd=0,
		cost_reconciled=1,reconciliation_reference='legacy_hold_released',settled_at=CASE WHEN settled_at=0 THEN ? ELSE settled_at END
		WHERE attempt_id=?`, now, attempt.AttemptID); err != nil {
		return err
	}
	_, err := tx.Exec(`UPDATE clipping_stages SET reserved_micro_usd=0,actual_micro_usd=0,estimated_micro_usd=0,cost_reconciled=1,updated_at=? WHERE job_id=? AND name=?`, now, job.ID, stageName)
	return err
}

// CompleteClippingAnalysisStage accepts a capability-authenticated Modal
// result, publishes it only while that durable attempt remains current, and
// automatically releases any reservation left by a pre-simplification row.
func (s *Store) CompleteClippingAnalysisStage(jobID, name, attemptID, leaseToken, callbackDigest string, callbackArtifact ClippingArtifact, core clippingAnalysisCore, computeSeconds int64) (ClippingStage, bool, bool, error) {
	if !safeID(jobID) || !strings.HasPrefix(jobID, "clipjob_") || name != ClippingStageAnalysis || attemptID == "" || leaseToken == "" ||
		!validLowerHexDigest(callbackDigest) || computeSeconds <= 0 {
		return ClippingStage{}, false, false, ErrClippingStaleAttempt
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
	job, err := scanClippingJob(tx.QueryRow(`SELECT `+clippingJobColumns()+` FROM clipping_jobs WHERE id=?`, jobID))
	if err != nil {
		return ClippingStage{}, false, false, err
	}
	source, err := scanClippingSource(tx.QueryRow(`SELECT `+clippingSourceColumns()+` FROM clipping_sources WHERE id=?`, job.SourceID))
	if err != nil {
		return ClippingStage{}, false, false, err
	}
	if attempt.CallbackReceivedAt > 0 {
		if attempt.CallbackDigest != callbackDigest {
			return ClippingStage{}, false, false, ErrClippingStaleAttempt
		}
		if err := tx.Commit(); err != nil {
			return ClippingStage{}, false, false, err
		}
		return stage, true, stage.Status == ClippingStageCompleted, nil
	}
	callbackExpiry, expiryErr := clippingIssuedCallbackExpiryTx(tx, attempt, source)
	if expiryErr != nil {
		return ClippingStage{}, false, false, expiryErr
	}
	if callbackExpiry <= now {
		return ClippingStage{}, false, false, ErrClippingStaleAttempt
	}
	if attempt.MaxComputeSeconds < 1 || computeSeconds > attempt.MaxComputeSeconds || computeSeconds > clippingWorkerMaxAttemptComputeSeconds {
		return ClippingStage{}, false, false, fmt.Errorf("%w: callback runtime exceeds the configured timeout", ErrClippingInvalidArtifact)
	}
	if callbackArtifact.Type != clippingAnalysisArtifactType || callbackArtifact.SchemaVersion != clippingAnalysisSchemaVersion || callbackArtifact.Version != 1 ||
		len(callbackArtifact.TimeRanges) != 0 || callbackArtifact.JobID != jobID || callbackArtifact.SourceDurationMS != source.DurationMS {
		return ClippingStage{}, false, false, fmt.Errorf("%w: source analysis callback metadata is invalid", ErrClippingInvalidArtifact)
	}
	coreJSON, err := json.Marshal(core)
	if err != nil {
		return ClippingStage{}, false, false, err
	}
	decodedCore, err := decodeClippingAnalysisCore(string(coreJSON), source)
	if err != nil {
		return ClippingStage{}, false, false, err
	}
	if !validClippingPipelineRevision(attempt.PipelineRevision) || decodedCore.PipelineRevision != attempt.PipelineRevision || decodedCore.SourceSHA256 != source.SHA256 {
		return ClippingStage{}, false, false, fmt.Errorf("%w: callback pipeline or source identity does not match its dispatch", ErrClippingInvalidArtifact)
	}
	var claimSource, claimSHA, claimPipeline, claimAttempt string
	claimErr := tx.QueryRow(`SELECT source_id,source_sha256,pipeline_key,attempt_id FROM clipping_analysis_claims WHERE job_id=?`, jobID).
		Scan(&claimSource, &claimSHA, &claimPipeline, &claimAttempt)
	claimCurrent := claimErr == nil && claimSource == source.ID && claimSHA == source.SHA256 && claimPipeline == attempt.PipelineRevision && claimAttempt == attemptID
	if claimErr != nil && !errors.Is(claimErr, sql.ErrNoRows) {
		return ClippingStage{}, false, false, claimErr
	}
	current := claimCurrent && (attempt.Status == "running" || attempt.Status == "expired") && stage.AttemptID == attemptID &&
		(stage.Status == ClippingStageRunning || stage.Status == ClippingStageUncertain) && !clippingJobTerminal(job.Status) && source.Status == ClippingSourceReady
	if current {
		finalArtifact, err := buildClippingAnalysisArtifact(job, source, decodedCore, 1, computeSeconds, clippingWorkerProtocolCompatibilityRate, computeSeconds)
		if err != nil {
			return ClippingStage{}, false, false, err
		}
		if err := stripClippingArtifactCostMetadata(&finalArtifact); err != nil {
			return ClippingStage{}, false, false, err
		}
		rangesJSON, err := validateClippingArtifact(finalArtifact, jobID, source.DurationMS)
		if err != nil {
			return ClippingStage{}, false, false, err
		}
		if err := insertClippingArtifact(tx, finalArtifact, rangesJSON, now); err != nil {
			return ClippingStage{}, false, false, err
		}
		if err := upsertClippingAnalysisCacheTx(tx, source, decodedCore, computeSeconds, now); err != nil {
			return ClippingStage{}, false, false, err
		}
		if err := releaseLegacyClippingHold(tx, &job, attempt, name, now); err != nil {
			return ClippingStage{}, false, false, err
		}
		if _, err := tx.Exec(`UPDATE clipping_stage_attempts SET status='completed',callback_received_at=?,callback_digest=?,error='',settled_at=CASE WHEN settled_at=0 THEN ? ELSE settled_at END WHERE attempt_id=?`, now, callbackDigest, now, attemptID); err != nil {
			return ClippingStage{}, false, false, err
		}
		if _, err := tx.Exec(`UPDATE clipping_stages SET status='completed',lease_expires_at=0,error='',updated_at=? WHERE job_id=? AND name=?`, now, jobID, name); err != nil {
			return ClippingStage{}, false, false, err
		}
		if _, err := refreshClippingJobStatus(tx, jobID, now); err != nil {
			return ClippingStage{}, false, false, err
		}
		if err := tx.Commit(); err != nil {
			return ClippingStage{}, false, false, err
		}
		stage.Status, stage.LeaseExpiresAt, stage.ReservedMicroUSD, stage.Error, stage.UpdatedAt = ClippingStageCompleted, 0, 0, "", now
		return stage, false, true, nil
	}

	// A canceled, terminal, tombstoned, or superseded attempt never publishes.
	if err := releaseLegacyClippingHold(tx, &job, attempt, name, now); err != nil {
		return ClippingStage{}, false, false, err
	}
	attemptStatus := "settled_stale"
	if job.Status == ClippingJobCanceled || stage.Status == ClippingStageCanceled {
		attemptStatus = "settled_canceled"
	}
	if _, err := tx.Exec(`UPDATE clipping_stage_attempts SET status=?,callback_received_at=?,callback_digest=?,error='non-publishing callback',settled_at=CASE WHEN settled_at=0 THEN ? ELSE settled_at END WHERE attempt_id=?`, attemptStatus, now, callbackDigest, now, attemptID); err != nil {
		return ClippingStage{}, false, false, err
	}
	if _, err := tx.Exec(`DELETE FROM clipping_analysis_claims WHERE job_id=? AND attempt_id=?`, jobID, attemptID); err != nil {
		return ClippingStage{}, false, false, err
	}
	if err := tx.Commit(); err != nil {
		return ClippingStage{}, false, false, err
	}
	stage, err = scanClippingStage(s.db.QueryRow(`SELECT `+clippingStageColumns()+` FROM clipping_stages WHERE job_id=? AND name=?`, jobID, name))
	return stage, false, false, err
}

func (s *Store) FailClippingAnalysisCallback(jobID, name, attemptID, leaseToken, callbackDigest string, failureCodes ...string) (ClippingStage, bool, error) {
	if !safeID(jobID) || !strings.HasPrefix(jobID, "clipjob_") || name != ClippingStageAnalysis || attemptID == "" || leaseToken == "" || !validLowerHexDigest(callbackDigest) {
		return ClippingStage{}, false, ErrClippingStaleAttempt
	}
	failureMessage := "clipping analysis stage failed"
	if len(failureCodes) > 0 {
		// Only these fixed codes can become persisted, browser-visible errors.
		// Never persist callback bodies, parser errors, or worker credentials.
		switch failureCodes[0] {
		case "invalid_callback_body", "callback_payload_too_large", "invalid_cost_metadata",
			"invalid_artifact_metadata", "source_identity_mismatch", "invalid_pipeline_provenance",
			"invalid_media_coverage", "invalid_analysis_payload", "artifact_persistence_failed":
			failureMessage += " (" + failureCodes[0] + ")"
		}
	}
	now := time.Now().Unix()
	tx, err := s.db.Begin()
	if err != nil {
		return ClippingStage{}, false, err
	}
	defer tx.Rollback()
	attempt, err := readClippingAttempt(tx, jobID, name, attemptID)
	if errors.Is(err, sql.ErrNoRows) {
		return ClippingStage{}, false, ErrClippingStaleAttempt
	}
	if err != nil {
		return ClippingStage{}, false, err
	}
	if !validClippingLease(leaseToken, attempt.TokenHash) {
		return ClippingStage{}, false, ErrClippingStaleAttempt
	}
	stage, err := readClippingStage(tx, jobID, name)
	if err != nil {
		return ClippingStage{}, false, err
	}
	job, err := scanClippingJob(tx.QueryRow(`SELECT `+clippingJobColumns()+` FROM clipping_jobs WHERE id=?`, jobID))
	if err != nil {
		return ClippingStage{}, false, err
	}
	source, err := scanClippingSource(tx.QueryRow(`SELECT `+clippingSourceColumns()+` FROM clipping_sources WHERE id=?`, job.SourceID))
	if err != nil {
		return ClippingStage{}, false, err
	}
	if attempt.CallbackReceivedAt > 0 {
		if attempt.CallbackDigest != callbackDigest {
			return ClippingStage{}, false, ErrClippingStaleAttempt
		}
		if err := tx.Commit(); err != nil {
			return ClippingStage{}, false, err
		}
		return stage, true, nil
	}
	callbackExpiry, expiryErr := clippingIssuedCallbackExpiryTx(tx, attempt, source)
	if expiryErr != nil {
		return ClippingStage{}, false, expiryErr
	}
	if callbackExpiry <= now {
		return ClippingStage{}, false, ErrClippingStaleAttempt
	}
	var claimSource, claimSHA, claimPipeline, claimAttempt string
	claimErr := tx.QueryRow(`SELECT source_id,source_sha256,pipeline_key,attempt_id FROM clipping_analysis_claims WHERE job_id=?`, jobID).
		Scan(&claimSource, &claimSHA, &claimPipeline, &claimAttempt)
	claimCurrent := claimErr == nil && claimSource == source.ID && claimSHA == source.SHA256 && claimPipeline == attempt.PipelineRevision && claimAttempt == attemptID
	if claimErr != nil && !errors.Is(claimErr, sql.ErrNoRows) {
		return ClippingStage{}, false, claimErr
	}
	current := claimCurrent && (attempt.Status == "running" || attempt.Status == "expired") && stage.AttemptID == attemptID &&
		(stage.Status == ClippingStageRunning || stage.Status == ClippingStageUncertain) && !clippingJobTerminal(job.Status) && source.Status == ClippingSourceReady
	if err := releaseLegacyClippingHold(tx, &job, attempt, name, now); err != nil {
		return ClippingStage{}, false, err
	}
	if current {
		if _, err := tx.Exec(`UPDATE clipping_stage_attempts SET status='failed',callback_received_at=?,callback_digest=?,error='analysis worker reported failure',settled_at=CASE WHEN settled_at=0 THEN ? ELSE settled_at END WHERE attempt_id=?`, now, callbackDigest, now, attemptID); err != nil {
			return ClippingStage{}, false, err
		}
		if _, err := tx.Exec(`UPDATE clipping_stages SET status='failed',lease_expires_at=0,error=?,updated_at=? WHERE job_id=? AND name=?`, failureMessage, now, jobID, name); err != nil {
			return ClippingStage{}, false, err
		}
		if _, err := tx.Exec(`UPDATE clipping_jobs SET status='failed',error=?,updated_at=? WHERE id=?`, failureMessage, now, jobID); err != nil {
			return ClippingStage{}, false, err
		}
	} else {
		attemptStatus := "settled_stale"
		if job.Status == ClippingJobCanceled || stage.Status == ClippingStageCanceled {
			attemptStatus = "settled_canceled"
		}
		if _, err := tx.Exec(`UPDATE clipping_stage_attempts SET status=?,callback_received_at=?,callback_digest=?,error='non-publishing failure callback',settled_at=CASE WHEN settled_at=0 THEN ? ELSE settled_at END WHERE attempt_id=?`, attemptStatus, now, callbackDigest, now, attemptID); err != nil {
			return ClippingStage{}, false, err
		}
	}
	if _, err := tx.Exec(`DELETE FROM clipping_analysis_claims WHERE job_id=? AND attempt_id=?`, jobID, attemptID); err != nil {
		return ClippingStage{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return ClippingStage{}, false, err
	}
	stage, err = scanClippingStage(s.db.QueryRow(`SELECT `+clippingStageColumns()+` FROM clipping_stages WHERE job_id=? AND name=?`, jobID, name))
	return stage, false, err
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
