package app

// These test-only helpers construct pre-retirement rows with the old durable
// transitions. They never ship in the application and allow callback, media,
// retention, and recovery tests to exercise already-issued work.
import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

func (s *Store) historicalCreateClippingSource(input ClippingSourceCreate) (ClippingSource, error) {
	if !validClippingSourceKind(input.Kind) {
		return ClippingSource{}, errors.New("invalid clipping source kind")
	}
	if input.Kind == ClippingSourceYouTubeURL {
		return ClippingSource{}, errors.New("URL references require the protected reference creator")
	}
	if !input.RightsAttested {
		return ClippingSource{}, errors.New("reuse and processing rights must be explicitly confirmed")
	}
	if input.DeclaredSizeBytes < 1 || input.DeclaredSizeBytes > MaxClippingSourceBytes {
		return ClippingSource{}, errors.New("declared source size must be between 1 byte and 20 GiB")
	}
	if input.RetainUntil < 0 {
		return ClippingSource{}, errors.New("retention time cannot be negative")
	}
	if len(input.MediaType) > 128 {
		return ClippingSource{}, errors.New("media type is too long")
	}
	if input.Kind == ClippingSourceUpload {
		if input.SourceURL != "" {
			return ClippingSource{}, errors.New("uploaded sources cannot include a source URL")
		}
	} else if input.SourceURL == "" || len(input.SourceURL) > 4096 {
		return ClippingSource{}, errors.New("import source URL is required and must be at most 4096 bytes")
	}
	id, err := newClippingStorageID("clipsrc_")
	if err != nil {
		return ClippingSource{}, err
	}
	dir := clippingSourceDir(s.clippingDir, id)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return ClippingSource{}, fmt.Errorf("create clipping source directory: %w", err)
	}
	cleanupDir := true
	defer func() {
		if cleanupDir {
			_ = os.RemoveAll(dir)
		}
	}()
	path := clippingStoragePath(s.clippingDir, id)
	status := clippingSourceInitialStatus(input.Kind)
	now := time.Now().Unix()
	tx, err := s.db.Begin()
	if err != nil {
		return ClippingSource{}, err
	}
	defer tx.Rollback()
	var used int64
	if err := tx.QueryRow(`SELECT COALESCE(SUM(reserved_size_bytes),0) FROM clipping_sources`).Scan(&used); err != nil {
		return ClippingSource{}, err
	}
	if input.DeclaredSizeBytes > MaxClippingStorageBytes-used {
		return ClippingSource{}, errors.New("clipping source storage reservation would exceed 100 GiB")
	}
	_, err = tx.Exec(`INSERT INTO clipping_sources(id,kind,status,original_name,media_type,source_url,rights_attested,storage_path,declared_size_bytes,reserved_size_bytes,retain_until,next_import_at,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, input.Kind, status, cleanClippingSourceName(input.OriginalName), input.MediaType, input.SourceURL, true, path, input.DeclaredSizeBytes, input.DeclaredSizeBytes, input.RetainUntil, now, now, now)
	if err != nil {
		return ClippingSource{}, err
	}
	if err := tx.Commit(); err != nil {
		return ClippingSource{}, err
	}
	cleanupDir = false
	return ClippingSource{
		ID: id, Kind: input.Kind, Status: status, OriginalName: cleanClippingSourceName(input.OriginalName), MediaType: input.MediaType, RightsAttested: true,
		SourceURL: input.SourceURL, StoragePath: path, DeclaredSizeBytes: input.DeclaredSizeBytes, ReservedSizeBytes: input.DeclaredSizeBytes,
		RetainUntil: input.RetainUntil, NextImportAt: now, CreatedAt: now, UpdatedAt: now,
	}, nil
}

func (s *Store) historicalCreateClippingJob(input ClippingJobCreate, idempotencyKey string) (ClippingJob, bool, error) {
	if strings.TrimSpace(idempotencyKey) == "" || len(idempotencyKey) > 128 || strings.ContainsAny(idempotencyKey, "\r\n\x00") {
		return ClippingJob{}, false, errors.New("clipping job idempotency key is required and must be at most 128 bytes")
	}
	if !safeClippingSourceID(input.SourceID) {
		return ClippingJob{}, false, errors.New("clipping job requires a valid source")
	}
	normalized, err := normalizeClippingSelection(input)
	if err != nil {
		return ClippingJob{}, false, err
	}
	requestHash, err := clippingJobRequestHash(normalized)
	if err != nil {
		return ClippingJob{}, false, err
	}
	jobID, err := newClippingStorageID("clipjob_")
	if err != nil {
		return ClippingJob{}, false, err
	}
	batchID, err := newClippingStorageID("clipbatch_")
	if err != nil {
		return ClippingJob{}, false, err
	}
	now := time.Now().Unix()
	tx, err := s.db.Begin()
	if err != nil {
		return ClippingJob{}, false, err
	}
	defer tx.Rollback()

	var existingHash, existingBatchID string
	err = tx.QueryRow(`SELECT id,request_hash FROM clipping_batches WHERE idempotency_key=?`, idempotencyKey).Scan(&existingBatchID, &existingHash)
	if err == nil {
		if existingHash != requestHash {
			return ClippingJob{}, false, ErrClippingIdempotencyConflict
		}
		var count int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM clipping_jobs WHERE batch_id=?`, existingBatchID).Scan(&count); err != nil {
			return ClippingJob{}, false, err
		}
		if count != 1 {
			return ClippingJob{}, false, ErrClippingIdempotencyConflict
		}
		job, err := scanClippingJob(tx.QueryRow(`SELECT `+clippingJobColumns()+` FROM clipping_jobs WHERE batch_id=?`, existingBatchID))
		if err != nil {
			return ClippingJob{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return ClippingJob{}, false, err
		}
		return job, true, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return ClippingJob{}, false, err
	}
	var status ClippingSourceStatus
	var kind ClippingSourceKind
	var rightsAttested int
	var retainUntil int64
	if err := tx.QueryRow(`SELECT status,kind,retain_until,rights_attested FROM clipping_sources WHERE id=?`, normalized.SourceID).Scan(&status, &kind, &retainUntil, &rightsAttested); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ClippingJob{}, false, ErrClippingSourceNotFound
		}
		return ClippingJob{}, false, err
	}
	if kind == ClippingSourceYouTubeURL {
		return ClippingJob{}, false, fmt.Errorf("%w: URL reference cannot use legacy analysis", ErrClippingInvalidState)
	}
	if status != ClippingSourceReady || rightsAttested == 0 || retainUntil > 0 && retainUntil <= now {
		return ClippingJob{}, false, errors.New("clipping jobs require a ready source within its retention period")
	}
	// Fixed value 1 satisfies only the legacy schema's positive constraint.
	if _, err := tx.Exec(`INSERT INTO clipping_batches(id,idempotency_key,request_hash,status,budget_limit_micro_usd,created_at,updated_at) VALUES(?,?,?,'queued',1,?,?)`, batchID, idempotencyKey, requestHash, now, now); err != nil {
		return ClippingJob{}, false, err
	}
	if _, err := tx.Exec(`INSERT INTO clipping_jobs(id,batch_id,source_id,content_type,min_clip_seconds,max_clip_seconds,candidate_limit,status,budget_limit_micro_usd,created_at,updated_at) VALUES(?,?,?,?,?,?,?,'queued',1,?,?)`, jobID, batchID, normalized.SourceID, normalized.ContentType, normalized.MinClipSeconds, normalized.MaxClipSeconds, normalized.CandidateLimit, now, now); err != nil {
		return ClippingJob{}, false, err
	}
	stageKey := jobID + ":" + ClippingStageAnalysis
	if _, err := tx.Exec(`INSERT INTO clipping_stages(job_id,name,status,idempotency_key,created_at,updated_at) VALUES(?,?,'queued',?,?,?)`, jobID, ClippingStageAnalysis, stageKey, now, now); err != nil {
		return ClippingJob{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return ClippingJob{}, false, err
	}
	return ClippingJob{ID: jobID, BatchID: batchID, SourceID: normalized.SourceID, Provider: ClippingProviderModalLegacy, ContentType: normalized.ContentType,
		MinClipSeconds: normalized.MinClipSeconds, MaxClipSeconds: normalized.MaxClipSeconds,
		CandidateLimit: normalized.CandidateLimit, Status: ClippingJobQueued, CreatedAt: now, UpdatedAt: now}, false, nil
}

func (s *Store) historicalClaimClippingStage(jobID, name string, reserveMicroUSD int64, leaseFor time.Duration) (ClippingStage, bool, error) {
	if !safeID(jobID) || !strings.HasPrefix(jobID, "clipjob_") || !validStageName(name) || reserveMicroUSD < 0 {
		return ClippingStage{}, false, errors.New("invalid clipping stage claim")
	}
	now := time.Now()
	nowUnix := now.Unix()
	leaseUntil := randomLeaseExpiry(now, leaseFor)
	tx, err := s.db.Begin()
	if err != nil {
		return ClippingStage{}, false, err
	}
	defer tx.Rollback()
	job, err := scanClippingJob(tx.QueryRow(`SELECT `+clippingJobColumns()+` FROM clipping_jobs WHERE id=?`, jobID))
	if errors.Is(err, sql.ErrNoRows) {
		return ClippingStage{}, false, ErrClippingJobNotFound
	}
	if err != nil {
		return ClippingStage{}, false, err
	}
	if job.Provider != ClippingProviderModalLegacy {
		return ClippingStage{}, false, ErrClippingInvalidState
	}
	// Preserve the legacy parameter for in-process compatibility, but never
	// reserve against it. New analysis runs have no user budget or spend gate.
	reserveMicroUSD = 0
	stage, err := readClippingStage(tx, jobID, name)
	if errors.Is(err, ErrClippingStageNotFound) {
		stageKey := jobID + ":" + name
		_, err = tx.Exec(`INSERT INTO clipping_stages(job_id,name,status,idempotency_key,created_at,updated_at) VALUES(?,?,'queued',?,?,?)`, jobID, name, stageKey, nowUnix, nowUnix)
		if err != nil {
			return ClippingStage{}, false, err
		}
		stage = ClippingStage{JobID: jobID, Name: name, Status: ClippingStageQueued, IdempotencyKey: stageKey, CreatedAt: nowUnix, UpdatedAt: nowUnix}
	} else if err != nil {
		return ClippingStage{}, false, err
	}
	if job.AnalysisDeleted || job.Status == ClippingJobCanceled || job.Status == ClippingJobCompleted || job.Status == ClippingJobFailed || stage.Status == ClippingStageCanceled || stage.Status == ClippingStageCompleted || stage.Status == ClippingStageFailed || stage.Status == ClippingStageUncertain {
		if err := tx.Commit(); err != nil {
			return ClippingStage{}, false, err
		}
		return stage, false, nil
	}
	if stage.Status == ClippingStageRunning && stage.LeaseExpiresAt > nowUnix {
		if err := tx.Commit(); err != nil {
			return ClippingStage{}, false, err
		}
		return stage, false, nil
	}
	if stage.Status == ClippingStageRunning {
		if stage.AttemptID != "" {
			if _, err := tx.Exec(`UPDATE clipping_stage_attempts SET status='expired' WHERE attempt_id=? AND status='running'`, stage.AttemptID); err != nil {
				return ClippingStage{}, false, err
			}
		}
		if _, err := tx.Exec(`UPDATE clipping_stages SET status='uncertain',error='Worker dispatch outcome is unknown; waiting for its callback window',updated_at=? WHERE job_id=? AND name=?`, nowUnix, jobID, name); err != nil {
			return ClippingStage{}, false, err
		}
		if _, err := tx.Exec(`UPDATE clipping_jobs SET status='running',error='Worker dispatch outcome is unknown; waiting for its callback window',updated_at=? WHERE id=?`, nowUnix, jobID); err != nil {
			return ClippingStage{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return ClippingStage{}, false, err
		}
		stage.Status = ClippingStageUncertain
		stage.Error = "Worker dispatch outcome is unknown; waiting for its callback window"
		stage.UpdatedAt = nowUnix
		return stage, false, nil
	}
	attemptID, err := stageAttemptID()
	if err != nil {
		return ClippingStage{}, false, err
	}
	leaseToken, err := newClippingLeaseToken()
	if err != nil {
		return ClippingStage{}, false, err
	}
	attemptNumber := stage.Attempt + 1
	_, err = tx.Exec(`INSERT INTO clipping_stage_attempts(attempt_id,job_id,stage_name,attempt_no,idempotency_key,token_hash,status,lease_expires_at,reserved_micro_usd,started_at) VALUES(?,?,?,?,?,?,'running',?,?,?)`, attemptID, jobID, name, attemptNumber, stage.IdempotencyKey, tokenHash(leaseToken), leaseUntil, reserveMicroUSD, nowUnix)
	if err != nil {
		return ClippingStage{}, false, err
	}
	_, err = tx.Exec(`UPDATE clipping_stages SET status='running',attempt_id=?,attempt=?,lease_expires_at=?,reserved_micro_usd=?,error='',updated_at=? WHERE job_id=? AND name=?`, attemptID, attemptNumber, leaseUntil, reserveMicroUSD, nowUnix, jobID, name)
	if err != nil {
		return ClippingStage{}, false, err
	}
	_, err = tx.Exec(`UPDATE clipping_jobs SET status='running',error='',updated_at=? WHERE id=?`, nowUnix, jobID)
	if err != nil {
		return ClippingStage{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return ClippingStage{}, false, err
	}
	stage.Status = ClippingStageRunning
	stage.AttemptID = attemptID
	stage.Attempt = attemptNumber
	stage.LeaseExpiresAt = leaseUntil
	stage.ReservedMicroUSD = reserveMicroUSD
	stage.LeaseToken = leaseToken
	stage.Error = ""
	stage.UpdatedAt = nowUnix
	return stage, true, nil
}

func (s *Store) historicalRetryClippingStage(jobID, name string) (ClippingStage, error) {
	if !safeID(jobID) || !strings.HasPrefix(jobID, "clipjob_") || !validStageName(name) {
		return ClippingStage{}, ErrClippingInvalidState
	}
	now := time.Now().Unix()
	tx, err := s.db.Begin()
	if err != nil {
		return ClippingStage{}, err
	}
	defer tx.Rollback()
	job, err := scanClippingJob(tx.QueryRow(`SELECT `+clippingJobColumns()+` FROM clipping_jobs WHERE id=?`, jobID))
	if errors.Is(err, sql.ErrNoRows) {
		return ClippingStage{}, ErrClippingJobNotFound
	}
	if err != nil {
		return ClippingStage{}, err
	}
	if job.Provider != ClippingProviderModalLegacy {
		return ClippingStage{}, ErrClippingInvalidState
	}
	if job.AnalysisDeleted || job.Status == ClippingJobCanceled || job.Status == ClippingJobCompleted {
		return ClippingStage{}, ErrClippingJobTerminal
	}
	stage, err := readClippingStage(tx, jobID, name)
	if err != nil {
		return ClippingStage{}, err
	}
	if stage.Status != ClippingStageFailed || job.Status != ClippingJobFailed {
		return ClippingStage{}, ErrClippingInvalidState
	}
	source, err := scanClippingSource(tx.QueryRow(`SELECT `+clippingSourceColumns()+` FROM clipping_sources WHERE id=?`, job.SourceID))
	if err != nil {
		return ClippingStage{}, err
	}
	if source.Status != ClippingSourceReady || source.RetainUntil > 0 && source.RetainUntil <= now {
		return ClippingStage{}, fmt.Errorf("%w: source media is no longer retained; import the source again before retrying", ErrClippingInvalidState)
	}
	if stage.AttemptID != "" {
		attempt, err := readClippingAttempt(tx, jobID, name, stage.AttemptID)
		if err != nil {
			return ClippingStage{}, err
		}
		if attempt.CallbackReceivedAt == 0 && attempt.ReconciliationReference != "known_no_submission" && attempt.ReconciliationReference != "callback_window_expired" {
			callbackExpiresAt, expiryErr := clippingIssuedCallbackExpiryTx(tx, attempt, source)
			if expiryErr != nil || callbackExpiresAt <= 0 {
				return ClippingStage{}, fmt.Errorf("%w: prior worker callback expiry is invalid; verify source retention and the attempt lease before retrying", ErrClippingInvalidState)
			}
			if callbackExpiresAt > now {
				return ClippingStage{}, fmt.Errorf("%w: prior worker callback window remains open until %s UTC; wait for it to expire before retrying", ErrClippingInvalidState, time.Unix(callbackExpiresAt, 0).UTC().Format("2006-01-02 15:04:05"))
			}
			if attempt.CallbackReceivedAt == 0 {
				if err := releaseLegacyClippingHold(tx, &job, attempt, name, now); err != nil {
					return ClippingStage{}, err
				}
				if _, err := tx.Exec(`UPDATE clipping_stage_attempts SET status='failed',reconciliation_reference='callback_window_expired',error='callback window expired',settled_at=CASE WHEN settled_at=0 THEN ? ELSE settled_at END WHERE attempt_id=?`, now, attempt.AttemptID); err != nil {
					return ClippingStage{}, err
				}
				if _, err := tx.Exec(`DELETE FROM clipping_analysis_claims WHERE job_id=? AND attempt_id=?`, jobID, attempt.AttemptID); err != nil {
					return ClippingStage{}, err
				}
			}
		}
	}
	if _, err := tx.Exec(`UPDATE clipping_stages SET status='queued',attempt_id='',error='',lease_expires_at=0,reserved_micro_usd=0,updated_at=? WHERE job_id=? AND name=?`, now, jobID, name); err != nil {
		return ClippingStage{}, err
	}
	if _, err := tx.Exec(`UPDATE clipping_jobs SET status='queued',error='',updated_at=? WHERE id=?`, now, jobID); err != nil {
		return ClippingStage{}, err
	}
	stage.Status = ClippingStageQueued
	stage.Error = ""
	stage.AttemptID = ""
	stage.LeaseExpiresAt = 0
	stage.ReservedMicroUSD = 0
	stage.UpdatedAt = now
	if err := tx.Commit(); err != nil {
		return ClippingStage{}, err
	}
	return stage, nil
}

func (a *dashboardApp) historicalPrepareClippingStageDispatch(jobID string, _ int64, workerOrigin string) (clippingPreparedDispatch, bool, error) {
	if a == nil || a.store == nil || a.security == nil {
		return clippingPreparedDispatch{}, false, errors.New("clipping worker dispatch security is unavailable")
	}
	stage, claimed, err := a.store.historicalClaimClippingStage(jobID, ClippingStageAnalysis, 0, clippingWorkerLeaseDuration)
	if err != nil || !claimed {
		return clippingPreparedDispatch{}, claimed, err
	}
	job, err := a.store.ClippingJob(jobID)
	if err != nil {
		_, _ = a.store.FailClippingStageBeforeSubmit(jobID, stage.Name, stage.AttemptID, stage.LeaseToken, "Worker job snapshot could not be loaded")
		return clippingPreparedDispatch{}, false, errors.New("clipping worker job snapshot could not be loaded")
	}
	source, err := a.store.ClippingSource(job.SourceID)
	if err != nil {
		_, _ = a.store.FailClippingStageBeforeSubmit(jobID, stage.Name, stage.AttemptID, stage.LeaseToken, "Worker source snapshot could not be loaded")
		a.store.PublishClippingJob(jobID)
		return clippingPreparedDispatch{}, false, errors.New("clipping worker source snapshot could not be loaded")
	}
	payload, err := buildClippingStageDispatch(job, stage, source, workerOrigin, time.Now())
	if err != nil {
		_, _ = a.store.FailClippingStageBeforeSubmit(jobID, stage.Name, stage.AttemptID, stage.LeaseToken, "Worker dispatch envelope could not be built")
		a.store.PublishClippingJob(jobID)
		return clippingPreparedDispatch{}, false, err
	}
	mediaBearer, callbackBearer, err := a.persistClippingDispatchCapabilitiesUntil(stage, payload.CallbackExpiresAt)
	if err != nil {
		_, _ = a.store.FailClippingStageBeforeSubmit(jobID, stage.Name, stage.AttemptID, stage.LeaseToken, "Worker capability persistence failed")
		a.store.PublishClippingJob(jobID)
		return clippingPreparedDispatch{}, false, errors.New("clipping worker capabilities could not be persisted")
	}
	a.store.PublishClippingJob(jobID)
	return clippingPreparedDispatch{Payload: payload, MediaBearer: mediaBearer, CallbackBearer: callbackBearer}, true, nil
}

func (s *Store) historicalBeginClippingImport(id string, expectedAttempts int, leaseFor time.Duration) (ClippingSource, error) {
	if !safeClippingSourceID(id) || expectedAttempts < 0 || expectedAttempts >= MaxClippingImportAttempts {
		return ClippingSource{}, ErrClippingInvalidState
	}
	now := time.Now()
	leaseUntil := clippingLeaseExpiry(now, leaseFor)
	tx, err := s.db.Begin()
	if err != nil {
		return ClippingSource{}, err
	}
	defer tx.Rollback()
	result, err := tx.Exec(`UPDATE clipping_sources SET import_attempts=import_attempts+1,import_lease_until=?,next_import_at=?,updated_at=? WHERE id=? AND status='importing' AND rights_attested=1 AND import_attempts=? AND next_import_at<=? AND import_lease_until<=?`, leaseUntil, leaseUntil, now.Unix(), id, expectedAttempts, now.Unix(), now.Unix())
	if err != nil {
		return ClippingSource{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return ClippingSource{}, err
	}
	if count != 1 {
		return ClippingSource{}, ErrClippingInvalidState
	}
	source, err := scanClippingSource(tx.QueryRow(`SELECT `+clippingSourceColumns()+` FROM clipping_sources WHERE id=?`, id))
	if err != nil {
		return ClippingSource{}, err
	}
	if err := tx.Commit(); err != nil {
		return ClippingSource{}, err
	}
	return source, nil
}

func (s *Store) historicalRetryClippingImport(id string, expectedAttempts int, nextAt int64, reason string) error {
	if !safeClippingSourceID(id) || expectedAttempts < 1 || nextAt < 0 {
		return ErrClippingInvalidState
	}
	status := ClippingSourceImporting
	if expectedAttempts >= MaxClippingImportAttempts {
		status = ClippingSourceFailed
		nextAt = 0
	}
	result, err := s.db.Exec(`UPDATE clipping_sources SET status=?,failure=?,next_import_at=?,import_lease_until=0,updated_at=? WHERE id=? AND status='importing' AND import_attempts=?`, status, cleanClippingError(reason), nextAt, time.Now().Unix(), id, expectedAttempts)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrClippingInvalidState
	}
	return nil
}
