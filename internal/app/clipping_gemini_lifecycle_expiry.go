package app

import "time"

// ExpireQueuedClippingGeminiJobs makes retention terminal independently of
// provider configuration or fingerprint contention, so retained queue entries
// cannot prevent source cleanup forever. Unknown claims are never released.
func (s *Store) ExpireQueuedClippingGeminiJobs(now int64) ([]string, error) {
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
	rows, err := tx.Query(`SELECT job.id FROM clipping_jobs AS job JOIN clipping_sources AS source ON source.id=job.source_id
		WHERE job.provider='gemini' AND job.status='queued' AND job.analysis_deleted=0 AND source.retain_until>0 AND source.retain_until<=?`, now)
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
		if _, err := tx.Exec(`UPDATE clipping_stages SET status='failed',lease_expires_at=0,error='source_inaccessible',updated_at=? WHERE job_id=? AND name=? AND status='queued'`, now, id, ClippingStageAnalysis); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`UPDATE clipping_jobs SET status='failed',error='source_inaccessible',updated_at=? WHERE id=? AND provider='gemini' AND status='queued'`, now, id); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`DELETE FROM clipping_gemini_execution_claims WHERE job_id=? AND state='retry_wait'`, id); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return ids, nil
}
