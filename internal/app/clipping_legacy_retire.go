package app

import "database/sql"

const legacyClippingRetiredReason = "Legacy clipping analysis has been retired"

// retirePendingClippingImports marks abandoned imports unavailable. It does
// not clear live leases, release reservations, or unlink any source files.
// Normal retention and explicit deletion continue to own physical cleanup.
func (s *Store) retirePendingClippingImports(now int64, limit int) ([]string, error) {
	if s == nil || s.db == nil || now <= 0 {
		return nil, ErrClippingInvalidState
	}
	if limit < 1 || limit > 500 {
		limit = 100
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	ids, err := clippingRetirementIDs(tx, `SELECT id FROM clipping_sources
		WHERE kind<>'youtube_url' AND status='importing' ORDER BY created_at,id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		// This failure code is excluded from failed-import file cleanup.
		_, err = tx.Exec(`UPDATE clipping_sources SET status='failed',failure='legacy_source_import_retired',next_import_at=0,updated_at=?
			WHERE id=? AND kind<>'youtube_url' AND status='importing'`, now, id)
		if err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return ids, nil
}

// retireNeverIssuedClippingJobs fails only queued jobs for which all durable
// evidence rules out any issued attempt or capability. A claim, previous
// attempt, inconsistent stage, or orphaned capability keeps the job intact
// for investigation and normal callback/recovery handling.
func (s *Store) retireNeverIssuedClippingJobs(now int64, limit int) ([]string, error) {
	if s == nil || s.db == nil || now <= 0 {
		return nil, ErrClippingInvalidState
	}
	if limit < 1 || limit > 500 {
		limit = 100
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	ids, err := clippingRetirementIDs(tx, `SELECT job.id FROM clipping_jobs AS job
		WHERE job.provider='modal_legacy' AND job.status='queued' AND job.analysis_deleted=0
		AND EXISTS (SELECT 1 FROM clipping_stages AS stage WHERE stage.job_id=job.id AND stage.name='analysis'
			AND stage.status='queued' AND stage.attempt_id='' AND stage.attempt=0)
		AND NOT EXISTS (SELECT 1 FROM clipping_stages AS stage WHERE stage.job_id=job.id
			AND (stage.status<>'queued' OR stage.attempt_id<>'' OR stage.attempt<>0))
		AND NOT EXISTS (SELECT 1 FROM clipping_stage_attempts AS attempt WHERE attempt.job_id=job.id)
		AND NOT EXISTS (SELECT 1 FROM clipping_analysis_claims AS claim WHERE claim.job_id=job.id)
		AND NOT EXISTS (SELECT 1 FROM clipping_capability_expiry_index AS expiry WHERE expiry.job_id=job.id)
		AND NOT EXISTS (SELECT 1 FROM settings AS setting WHERE substr(setting.key,1,length('clipping_cap_' || job.id || '_'))='clipping_cap_' || job.id || '_')
		ORDER BY job.created_at,job.id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if _, err = tx.Exec(`UPDATE clipping_stages SET status='failed',error=?,updated_at=? WHERE job_id=? AND status='queued'`, legacyClippingRetiredReason, now, id); err != nil {
			return nil, err
		}
		if _, err = tx.Exec(`UPDATE clipping_jobs SET status='failed',error=?,updated_at=? WHERE id=? AND provider='modal_legacy' AND status='queued'`, legacyClippingRetiredReason, now, id); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return ids, nil
}

func clippingRetirementIDs(tx *sql.Tx, query string, limit int) ([]string, error) {
	rows, err := tx.Query(query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// reconcileRetiredClipping runs with the existing recovery cadence, without a
// downloader or worker dispatch loop. Batches are bounded and repeatable.
func (p *Processor) reconcileRetiredClipping(now int64) error {
	const batchSize = 100
	for {
		ids, err := p.app.store.retirePendingClippingImports(now, batchSize)
		if err != nil {
			return err
		}
		for _, id := range ids {
			p.app.store.PublishClippingSource(id)
		}
		if len(ids) < batchSize {
			break
		}
	}
	for {
		ids, err := p.app.store.retireNeverIssuedClippingJobs(now, batchSize)
		if err != nil {
			return err
		}
		for _, id := range ids {
			p.app.store.PublishClippingJob(id)
		}
		if len(ids) < batchSize {
			return nil
		}
	}
}
