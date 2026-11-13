package app

import "time"

// QuarantineQueuedClippingGeminiJobs makes a predecessor's unknown outcome
// visible to duplicate requests rather than leaving them queued indefinitely.
// It never changes the original fingerprint guard.
func (s *Store) QuarantineQueuedClippingGeminiJobs(now int64) ([]string, error) {
	if s == nil || s.db == nil {
		return nil, ErrClippingInvalidState
	}
	if now <= 0 {
		now = time.Now().Unix()
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT job.id FROM clipping_jobs AS job JOIN clipping_gemini_requests AS request ON request.job_id=job.id
		JOIN clipping_gemini_execution_claims AS claim ON claim.fingerprint=request.fingerprint
		WHERE job.provider='gemini' AND job.status='queued' AND job.analysis_deleted=0 AND claim.state='unknown'`)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, id := range ids {
		if _, err := tx.Exec(`UPDATE clipping_stages SET status='failed',lease_expires_at=0,error='outcome_unknown',updated_at=? WHERE job_id=? AND name=? AND status='queued'`, now, id, ClippingStageAnalysis); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`UPDATE clipping_jobs SET status='failed',error='outcome_unknown',updated_at=? WHERE id=? AND provider='gemini' AND status='queued'`, now, id); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return ids, nil
}
