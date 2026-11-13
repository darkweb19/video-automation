package app

import (
	"database/sql"
	"errors"
	"time"
)

// CompleteClippingGeminiFromCache applies a validated source-scoped origin
// atomically, without creating provider usage or refreshing cache lifetime.
func (s *Store) CompleteClippingGeminiFromCache(jobID string, now time.Time) (bool, error) {
	if s == nil || s.db == nil {
		return false, ErrClippingInvalidState
	}
	if now.IsZero() {
		now = time.Now()
	}
	n := now.Unix()
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	job, _, fingerprint, _, err := clippingGeminiCacheIntentTx(tx, jobID, n)
	if err != nil {
		return false, err
	}
	if job.Status != ClippingJobQueued {
		return false, ErrClippingInvalidState
	}
	stage, err := readClippingStage(tx, jobID, ClippingStageAnalysis)
	if err != nil || stage.Status != ClippingStageQueued {
		return false, ErrClippingInvalidState
	}
	var claimJob, state string
	err = tx.QueryRow(`SELECT job_id,state FROM clipping_gemini_execution_claims WHERE fingerprint=?`, fingerprint).Scan(&claimJob, &state)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if err == nil && (claimJob != jobID || state != "retry_wait") {
		return false, nil
	}
	hit, found, err := loadClippingGeminiCacheTx(tx, jobID, n)
	if err != nil || !found {
		return false, err
	}
	ranges, err := validateClippingArtifact(hit.Artifact, jobID, hit.Artifact.SourceDurationMS)
	if err != nil {
		return false, err
	}
	if err := insertClippingArtifact(tx, hit.Artifact, ranges, n); err != nil {
		return false, err
	}
	if _, err := tx.Exec(`UPDATE clipping_stages SET status='completed',lease_expires_at=0,error='',updated_at=? WHERE job_id=? AND name=? AND status='queued'`, n, jobID, ClippingStageAnalysis); err != nil {
		return false, err
	}
	if _, err := tx.Exec(`UPDATE clipping_jobs SET status='completed',error='',updated_at=? WHERE id=? AND provider='gemini' AND status='queued' AND analysis_deleted=0`, n, jobID); err != nil {
		return false, err
	}
	if _, err := tx.Exec(`DELETE FROM clipping_gemini_execution_claims WHERE fingerprint=? AND job_id=? AND state='retry_wait'`, fingerprint, jobID); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}
