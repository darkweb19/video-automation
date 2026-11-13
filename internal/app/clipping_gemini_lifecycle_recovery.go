package app

import "time"

// RecoverClippingGeminiAttempts is called before workers start and later only
// for expired leases. A process restart proves claimed intent was never sent;
// anything past the submission fence stays globally quarantined.
func (s *Store) RecoverClippingGeminiAttempts(now int64, startup bool) ([]string, error) {
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
	rows, err := tx.Query(`SELECT attempt.job_id,attempt.attempt_id,attempt.attempt_no,attempt.phase,claim.fingerprint,source.retain_until
		FROM clipping_gemini_attempts AS attempt JOIN clipping_gemini_execution_claims AS claim ON claim.attempt_id=attempt.attempt_id AND claim.job_id=attempt.job_id
		JOIN clipping_jobs AS job ON job.id=attempt.job_id JOIN clipping_sources AS source ON source.id=job.source_id
		WHERE claim.state='active' AND attempt.phase IN ('claimed','submitting') AND (? OR attempt.lease_expires_at<=?)`, startup, now)
	if err != nil {
		return nil, err
	}
	type item struct {
		job, attempt, fingerprint string
		number                    int
		phase                     ClippingGeminiAttemptPhase
		retention                 int64
	}
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.job, &it.attempt, &it.number, &it.phase, &it.fingerprint, &it.retention); err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.job)
		outcome, code, retry := "unknown", "outcome_unknown", int64(0)
		if it.phase == ClippingGeminiAttemptClaimed {
			outcome, code = "not_submitted", "request_not_submitted"
			if it.number < clippingGeminiMaxSubmissionAttempts && (it.retention == 0 || it.retention > now) {
				retry = now + int64(clippingGeminiNotSubmittedRetryDelay.Seconds())
			}
			if it.retention > 0 && it.retention <= now {
				code = "source_inaccessible"
			}
		}
		if _, err := tx.Exec(`UPDATE clipping_gemini_attempts SET phase='settled',outcome=?,failure_code=?,retry_at=?,finished_at=?,updated_at=? WHERE attempt_id=? AND phase=?`, outcome, code, retry, now, now, it.attempt, it.phase); err != nil {
			return nil, err
		}
		if outcome == "unknown" {
			if _, err := tx.Exec(`UPDATE clipping_gemini_execution_claims SET state='unknown',retry_at=0,updated_at=? WHERE fingerprint=? AND attempt_id=?`, now, it.fingerprint, it.attempt); err != nil {
				return nil, err
			}
		} else if retry > 0 {
			if _, err := tx.Exec(`UPDATE clipping_gemini_execution_claims SET state='retry_wait',retry_at=?,lease_expires_at=?,updated_at=? WHERE fingerprint=? AND attempt_id=?`, retry, retry, now, it.fingerprint, it.attempt); err != nil {
				return nil, err
			}
		} else if _, err := tx.Exec(`DELETE FROM clipping_gemini_execution_claims WHERE fingerprint=? AND attempt_id=?`, it.fingerprint, it.attempt); err != nil {
			return nil, err
		}
		status := "failed"
		if retry > 0 {
			status = "queued"
		}
		if _, err := tx.Exec(`UPDATE clipping_stages SET status=?,lease_expires_at=0,error=?,updated_at=? WHERE job_id=? AND name=? AND attempt_id=? AND status='running'`, status, code, now, it.job, ClippingStageAnalysis, it.attempt); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`UPDATE clipping_jobs SET status=?,error=?,updated_at=? WHERE id=? AND provider='gemini' AND status='running' AND analysis_deleted=0`, status, code, now, it.job); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return ids, nil
}
