package app

import "database/sql"

// Cancellation commits before notifying the worker. A claimed attempt has not
// crossed the submission fence, while a submitting attempt may have reached
// Google and therefore retains the global fingerprint guard indefinitely.
func cancelClippingGeminiJobTx(tx *sql.Tx, job *ClippingJob, now int64) error {
	if _, err := tx.Exec(`UPDATE clipping_gemini_attempts SET phase='settled',outcome='not_submitted',failure_code='request_not_submitted',finished_at=?,updated_at=? WHERE job_id=? AND phase='claimed'`, now, now, job.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE clipping_gemini_attempts SET phase='settled',outcome='unknown',failure_code='outcome_unknown',finished_at=?,updated_at=? WHERE job_id=? AND phase='submitting'`, now, now, job.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE clipping_gemini_execution_claims SET state='unknown',retry_at=0,updated_at=? WHERE job_id=? AND state='active' AND attempt_id IN (SELECT attempt_id FROM clipping_gemini_attempts WHERE job_id=? AND outcome='unknown')`, now, job.ID, job.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM clipping_gemini_execution_claims WHERE job_id=? AND state IN ('active','retry_wait')`, job.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE clipping_stages SET status='canceled',reserved_micro_usd=0,lease_expires_at=0,error='Canceled',updated_at=? WHERE job_id=? AND status IN ('queued','running','paused_budget','uncertain')`, now, job.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE clipping_jobs SET status='canceled',reserved_micro_usd=0,error='Canceled',updated_at=? WHERE id=?`, now, job.ID); err != nil {
		return err
	}
	job.Status = ClippingJobCanceled
	return nil
}
