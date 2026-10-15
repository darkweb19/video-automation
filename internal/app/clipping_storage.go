package app

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var (
	ErrClippingSourceNotFound      = errors.New("clipping source not found")
	ErrClippingJobNotFound         = errors.New("clipping job not found")
	ErrClippingStageNotFound       = errors.New("clipping stage not found")
	ErrClippingIdempotencyConflict = errors.New("clipping idempotency key was reused with different input")
	ErrClippingInvalidState        = errors.New("invalid clipping state transition")
	ErrClippingStaleAttempt        = errors.New("clipping stage attempt is stale")
	ErrClippingJobTerminal         = errors.New("clipping job is terminal")
	ErrClippingInvalidArtifact     = errors.New("invalid clipping artifact")
)

const maxClippingArtifactPayloadBytes = 4 << 20

var clippingStageNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,40}$`)
var clippingArtifactTypePattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,127}$`)

func (s *Store) migrateClipping() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS clipping_sources (
			id TEXT PRIMARY KEY,
			kind TEXT NOT NULL CHECK(kind IN ('upload','youtube_original_file','google_drive_public','dropbox_public')),
			status TEXT NOT NULL CHECK(status IN ('uploading','importing','ready','failed','deleted')),
			original_name TEXT NOT NULL DEFAULT '',
			media_type TEXT NOT NULL DEFAULT '',
			source_url TEXT NOT NULL DEFAULT '',
			rights_attested INTEGER NOT NULL DEFAULT 0 CHECK(rights_attested IN (0,1)),
			storage_path TEXT NOT NULL UNIQUE,
			failure TEXT NOT NULL DEFAULT '',
			declared_size_bytes INTEGER NOT NULL CHECK(declared_size_bytes BETWEEN 1 AND 21474836480),
			reserved_size_bytes INTEGER NOT NULL CHECK(reserved_size_bytes >= 0),
			size_bytes INTEGER NOT NULL DEFAULT 0 CHECK(size_bytes >= 0),
			upload_offset INTEGER NOT NULL DEFAULT 0 CHECK(upload_offset >= 0),
			duration_ms INTEGER NOT NULL DEFAULT 0 CHECK(duration_ms >= 0),
			sha256 TEXT NOT NULL DEFAULT '',
			import_expectation_json TEXT NOT NULL DEFAULT '',
			import_expected_size_bytes INTEGER NOT NULL DEFAULT 0 CHECK(import_expected_size_bytes >= 0),
			import_expected_sha256 TEXT NOT NULL DEFAULT '',
			retain_until INTEGER NOT NULL DEFAULT 0,
			import_attempts INTEGER NOT NULL DEFAULT 0 CHECK(import_attempts >= 0),
			next_import_at INTEGER NOT NULL DEFAULT 0,
			import_lease_until INTEGER NOT NULL DEFAULT 0,
			media_lease_until INTEGER NOT NULL DEFAULT 0,
			cleanup_complete INTEGER NOT NULL DEFAULT 0 CHECK(cleanup_complete IN (0,1)),
			diagnostics_json TEXT NOT NULL DEFAULT '[]',
			diagnostics_sequence INTEGER NOT NULL DEFAULT 0 CHECK(diagnostics_sequence >= 0),
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			deleted_at INTEGER NOT NULL DEFAULT 0
		);
		CREATE INDEX IF NOT EXISTS clipping_sources_import_queue ON clipping_sources(status,next_import_at,import_lease_until,import_attempts);
		CREATE INDEX IF NOT EXISTS clipping_sources_retention ON clipping_sources(status,retain_until,media_lease_until,cleanup_complete);
		CREATE TABLE IF NOT EXISTS clipping_batches (
			id TEXT PRIMARY KEY,
			idempotency_key TEXT NOT NULL UNIQUE,
			request_hash TEXT NOT NULL,
			status TEXT NOT NULL CHECK(status IN ('queued','running','paused_budget','completed','failed','canceled')),
			budget_limit_micro_usd INTEGER NOT NULL CHECK(budget_limit_micro_usd > 0),
			reserved_micro_usd INTEGER NOT NULL DEFAULT 0 CHECK(reserved_micro_usd >= 0),
			spent_micro_usd INTEGER NOT NULL DEFAULT 0 CHECK(spent_micro_usd >= 0),
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		);
		CREATE TABLE IF NOT EXISTS clipping_jobs (
			id TEXT PRIMARY KEY,
			batch_id TEXT NOT NULL REFERENCES clipping_batches(id) ON DELETE RESTRICT,
			source_id TEXT NOT NULL REFERENCES clipping_sources(id) ON DELETE RESTRICT,
			content_type TEXT NOT NULL DEFAULT 'general' CHECK(content_type IN ('general','podcast','comedy','gaming','movie')),
			min_clip_seconds INTEGER NOT NULL DEFAULT 15 CHECK(min_clip_seconds BETWEEN 15 AND 180),
			max_clip_seconds INTEGER NOT NULL DEFAULT 180 CHECK(max_clip_seconds BETWEEN 15 AND 180),
			candidate_limit INTEGER NOT NULL DEFAULT 10 CHECK(candidate_limit BETWEEN 7 AND 10),
			status TEXT NOT NULL CHECK(status IN ('queued','running','paused_budget','completed','failed','canceled')),
			budget_limit_micro_usd INTEGER NOT NULL CHECK(budget_limit_micro_usd > 0),
			reserved_micro_usd INTEGER NOT NULL DEFAULT 0 CHECK(reserved_micro_usd >= 0),
			spent_micro_usd INTEGER NOT NULL DEFAULT 0 CHECK(spent_micro_usd >= 0),
			error TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS clipping_jobs_batch ON clipping_jobs(batch_id,created_at,id);
		CREATE INDEX IF NOT EXISTS clipping_jobs_source ON clipping_jobs(source_id,status);
		CREATE TABLE IF NOT EXISTS clipping_stages (
			job_id TEXT NOT NULL REFERENCES clipping_jobs(id) ON DELETE RESTRICT,
			name TEXT NOT NULL,
			status TEXT NOT NULL CHECK(status IN ('queued','running','paused_budget','uncertain','completed','failed','canceled')),
			idempotency_key TEXT NOT NULL,
			attempt_id TEXT NOT NULL DEFAULT '',
			attempt INTEGER NOT NULL DEFAULT 0 CHECK(attempt >= 0),
			lease_expires_at INTEGER NOT NULL DEFAULT 0,
			reserved_micro_usd INTEGER NOT NULL DEFAULT 0 CHECK(reserved_micro_usd >= 0),
			actual_micro_usd INTEGER NOT NULL DEFAULT 0 CHECK(actual_micro_usd >= 0),
			estimated_micro_usd INTEGER NOT NULL DEFAULT 0 CHECK(estimated_micro_usd >= 0),
			cost_reconciled INTEGER NOT NULL DEFAULT 1 CHECK(cost_reconciled IN (0,1)),
			error TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			PRIMARY KEY(job_id,name),
			UNIQUE(idempotency_key)
		);
		CREATE INDEX IF NOT EXISTS clipping_stages_queue ON clipping_stages(status,updated_at);
		CREATE TABLE IF NOT EXISTS clipping_stage_attempts (
			attempt_id TEXT PRIMARY KEY,
			job_id TEXT NOT NULL,
			stage_name TEXT NOT NULL,
			attempt_no INTEGER NOT NULL CHECK(attempt_no > 0),
			idempotency_key TEXT NOT NULL,
			token_hash TEXT NOT NULL,
			status TEXT NOT NULL CHECK(status IN ('running','expired','completed','failed','canceled','settled_stale','settled_canceled')),
			lease_expires_at INTEGER NOT NULL,
			reserved_micro_usd INTEGER NOT NULL CHECK(reserved_micro_usd >= 0),
			actual_micro_usd INTEGER NOT NULL DEFAULT 0 CHECK(actual_micro_usd >= 0),
			estimated_micro_usd INTEGER NOT NULL DEFAULT 0 CHECK(estimated_micro_usd >= 0),
			cost_reconciled INTEGER NOT NULL DEFAULT 1 CHECK(cost_reconciled IN (0,1)),
			operator_rate_micro_usd_per_second INTEGER NOT NULL DEFAULT 0 CHECK(operator_rate_micro_usd_per_second >= 0),
			max_compute_seconds INTEGER NOT NULL DEFAULT 0 CHECK(max_compute_seconds >= 0),
			pipeline_revision TEXT NOT NULL DEFAULT '',
			reconciliation_reference TEXT NOT NULL DEFAULT '',
			callback_received_at INTEGER NOT NULL DEFAULT 0,
			callback_digest TEXT NOT NULL DEFAULT '',
			started_at INTEGER NOT NULL,
			settled_at INTEGER NOT NULL DEFAULT 0,
			error TEXT NOT NULL DEFAULT '',
			FOREIGN KEY(job_id,stage_name) REFERENCES clipping_stages(job_id,name) ON DELETE RESTRICT,
			UNIQUE(job_id,stage_name,attempt_no)
		);
		CREATE INDEX IF NOT EXISTS clipping_attempts_stage ON clipping_stage_attempts(job_id,stage_name,attempt_no);
		CREATE TABLE IF NOT EXISTS clipping_artifacts (
			job_id TEXT NOT NULL REFERENCES clipping_jobs(id) ON DELETE RESTRICT,
			artifact_type TEXT NOT NULL,
			schema_version TEXT NOT NULL,
			version INTEGER NOT NULL CHECK(version > 0),
			source_duration_ms INTEGER NOT NULL CHECK(source_duration_ms BETWEEN 1 AND 14400000),
			time_ranges_json TEXT NOT NULL,
			payload_json TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			PRIMARY KEY(job_id,artifact_type,version)
		);
		CREATE INDEX IF NOT EXISTS clipping_artifacts_job ON clipping_artifacts(job_id,artifact_type,version);
		CREATE TABLE IF NOT EXISTS clipping_analysis_cache (
			source_id TEXT NOT NULL REFERENCES clipping_sources(id) ON DELETE RESTRICT,
			source_sha256 TEXT NOT NULL CHECK(length(source_sha256)=64),
			pipeline_key TEXT NOT NULL,
			pipeline_revision TEXT NOT NULL DEFAULT 'framevault-m3-v1',
			source_duration_ms INTEGER NOT NULL CHECK(source_duration_ms BETWEEN 1 AND 14400000),
			payload_json TEXT NOT NULL,
			compute_seconds INTEGER NOT NULL DEFAULT 0 CHECK(compute_seconds >= 0),
			created_at INTEGER NOT NULL,
			PRIMARY KEY(source_id,pipeline_key)
		);
		CREATE TABLE IF NOT EXISTS clipping_analysis_claims (
			source_id TEXT NOT NULL REFERENCES clipping_sources(id) ON DELETE RESTRICT,
			source_sha256 TEXT NOT NULL CHECK(length(source_sha256)=64),
			pipeline_key TEXT NOT NULL,
			job_id TEXT NOT NULL REFERENCES clipping_jobs(id) ON DELETE RESTRICT,
			attempt_id TEXT NOT NULL DEFAULT '',
			claimed_at INTEGER NOT NULL,
			PRIMARY KEY(source_id,pipeline_key)
		);
		CREATE TABLE IF NOT EXISTS clipping_capability_expiry_index (
			job_id TEXT NOT NULL,
			attempt_id TEXT NOT NULL,
			expires_at INTEGER NOT NULL CHECK(expires_at > 0),
			PRIMARY KEY(job_id,attempt_id)
		);
		CREATE INDEX IF NOT EXISTS clipping_capability_expiry_due ON clipping_capability_expiry_index(expires_at,job_id,attempt_id);
	`)
	if err != nil {
		return fmt.Errorf("initialize clipping storage: %w", err)
	}
	// Keep already-installed M2 databases forward compatible. SQLite does not
	// support ADD COLUMN IF NOT EXISTS on every version used by the app, so
	// inspect fixed table names and issue only missing additive columns.
	for table, columns := range map[string]map[string]string{
		"clipping_sources": {
			"diagnostics_json":           "TEXT NOT NULL DEFAULT '[]'",
			"diagnostics_sequence":       "INTEGER NOT NULL DEFAULT 0 CHECK(diagnostics_sequence >= 0)",
			"import_expectation_json":    "TEXT NOT NULL DEFAULT ''",
			"import_expected_size_bytes": "INTEGER NOT NULL DEFAULT 0 CHECK(import_expected_size_bytes >= 0)",
			"import_expected_sha256":     "TEXT NOT NULL DEFAULT ''",
		},
		"clipping_jobs": {
			"content_type":     "TEXT NOT NULL DEFAULT 'general' CHECK(content_type IN ('general','podcast','comedy','gaming','movie'))",
			"min_clip_seconds": "INTEGER NOT NULL DEFAULT 15 CHECK(min_clip_seconds BETWEEN 15 AND 180)",
			"max_clip_seconds": "INTEGER NOT NULL DEFAULT 180 CHECK(max_clip_seconds BETWEEN 15 AND 180)",
			"candidate_limit":  "INTEGER NOT NULL DEFAULT 10 CHECK(candidate_limit BETWEEN 7 AND 10)",
		},
		"clipping_stages": {
			"estimated_micro_usd": "INTEGER NOT NULL DEFAULT 0 CHECK(estimated_micro_usd >= 0)",
			"cost_reconciled":     "INTEGER NOT NULL DEFAULT 1 CHECK(cost_reconciled IN (0,1))",
		},
		"clipping_stage_attempts": {
			"estimated_micro_usd":                "INTEGER NOT NULL DEFAULT 0 CHECK(estimated_micro_usd >= 0)",
			"cost_reconciled":                    "INTEGER NOT NULL DEFAULT 1 CHECK(cost_reconciled IN (0,1))",
			"operator_rate_micro_usd_per_second": "INTEGER NOT NULL DEFAULT 0 CHECK(operator_rate_micro_usd_per_second >= 0)",
			"max_compute_seconds":                "INTEGER NOT NULL DEFAULT 0 CHECK(max_compute_seconds >= 0)",
			"pipeline_revision":                  "TEXT NOT NULL DEFAULT ''",
			"reconciliation_reference":           "TEXT NOT NULL DEFAULT ''",
			"callback_received_at":               "INTEGER NOT NULL DEFAULT 0",
			"callback_digest":                    "TEXT NOT NULL DEFAULT ''",
		},
		"clipping_analysis_cache": {
			"pipeline_revision": "TEXT NOT NULL DEFAULT 'framevault-m3-v1'",
			"compute_seconds":   "INTEGER NOT NULL DEFAULT 0 CHECK(compute_seconds >= 0)",
		},
	} {
		for column, declaration := range columns {
			if err := ensureClippingColumn(s.db, table, column, declaration); err != nil {
				return fmt.Errorf("migrate clipping %s.%s: %w", table, column, err)
			}
		}
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS clipping_analysis_cache_revision ON clipping_analysis_cache(source_id,pipeline_revision,created_at DESC)`); err != nil {
		return fmt.Errorf("index clipping analysis cache revision: %w", err)
	}
	if err := s.migrateClippingCompatibilityState(); err != nil {
		return fmt.Errorf("migrate clipping compatibility state: %w", err)
	}
	return nil
}

// migrateClippingCompatibilityState releases outstanding legacy holds and
// removes the retired paused-budget state. Startup never resubmits an unknown
// dispatch.
func (s *Store) migrateClippingCompatibilityState() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT attempt.job_id,attempt.stage_name,attempt.attempt_id,attempt.reserved_micro_usd
		FROM clipping_stage_attempts AS attempt
		JOIN clipping_stages AS stage ON stage.job_id=attempt.job_id AND stage.name=attempt.stage_name
		WHERE attempt.callback_received_at>0 AND attempt.cost_reconciled=0 AND attempt.settled_at=0
		AND attempt.reserved_micro_usd>0`)
	if err != nil {
		return err
	}
	type legacySuccess struct {
		jobID, stageName, attemptID string
		reserved                    int64
	}
	var successes []legacySuccess
	for rows.Next() {
		var item legacySuccess
		if err := rows.Scan(&item.jobID, &item.stageName, &item.attemptID, &item.reserved); err != nil {
			rows.Close()
			return err
		}
		successes = append(successes, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	now := time.Now().Unix()
	// Pending rows from removed intake adapters are inert after upgrade. Keep
	// their metadata and files for existing-data safety; only stop future work.
	if _, err := tx.Exec(`UPDATE clipping_sources SET status='failed',failure='legacy_source_import_unsupported',
		import_lease_until=0,next_import_at=0,updated_at=?
		WHERE kind<>'youtube_original_file' AND status IN ('uploading','importing')`, now); err != nil {
		return err
	}
	for _, item := range successes {
		job, err := scanClippingJob(tx.QueryRow(`SELECT `+clippingJobColumns()+` FROM clipping_jobs WHERE id=?`, item.jobID))
		if err != nil {
			return err
		}
		if item.reserved > 0 {
			var batchReserved int64
			if err := tx.QueryRow(`SELECT reserved_micro_usd FROM clipping_batches WHERE id=?`, job.BatchID).Scan(&batchReserved); err != nil {
				return err
			}
			// An already-reconciled legacy attempt retains its original per-attempt
			// reservation value. Only decrement counters when this attempt is still
			// explicitly unsettled and both aggregate counters contain the hold.
			if item.reserved <= job.ReservedMicroUSD && item.reserved <= batchReserved {
				if err := releaseLegacyClippingReservation(tx, &job, item.reserved, now); err != nil {
					return err
				}
			}
		}
		if _, err := tx.Exec(`UPDATE clipping_stage_attempts SET reserved_micro_usd=0,cost_reconciled=1,
			reconciliation_reference='legacy_hold_released',settled_at=CASE WHEN settled_at=0 THEN ? ELSE settled_at END
			WHERE attempt_id=?`, now, item.attemptID); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE clipping_stages SET reserved_micro_usd=0,cost_reconciled=1,updated_at=? WHERE job_id=? AND name=?`, now, item.jobID, item.stageName); err != nil {
			return err
		}
	}
	// Old paused-budget attempts with an outstanding callback capability become
	// uncertain. The callback remains valid through its capability window, and
	// the ordinary expiry sweep then releases its claim without resubmitting it.
	if _, err := tx.Exec(`UPDATE clipping_stage_attempts SET status='expired'
		WHERE status='running' AND callback_received_at=0 AND attempt_id IN (
			SELECT attempt.attempt_id FROM clipping_stage_attempts AS attempt
			JOIN clipping_stages AS stage ON stage.job_id=attempt.job_id AND stage.name=attempt.stage_name
			JOIN clipping_jobs AS job ON job.id=attempt.job_id
			WHERE stage.status='paused_budget' AND job.status='paused_budget' AND stage.attempt_id=attempt.attempt_id
		)`); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE clipping_stages SET status='uncertain',error='Worker dispatch outcome is unknown; waiting for its callback window',updated_at=?
		WHERE status='paused_budget' AND EXISTS(
			SELECT 1 FROM clipping_stage_attempts AS attempt WHERE attempt.job_id=clipping_stages.job_id
			AND attempt.stage_name=clipping_stages.name AND attempt.attempt_id=clipping_stages.attempt_id
			AND attempt.status='expired' AND attempt.callback_received_at=0 AND attempt.settled_at=0
		)`, now); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE clipping_stages SET status='completed',lease_expires_at=0,error='',updated_at=?
		WHERE status='paused_budget' AND EXISTS(
			SELECT 1 FROM clipping_stage_attempts AS attempt WHERE attempt.job_id=clipping_stages.job_id
			AND attempt.stage_name=clipping_stages.name AND attempt.attempt_id=clipping_stages.attempt_id
			AND attempt.status='completed' AND attempt.callback_received_at>0
		) AND EXISTS(SELECT 1 FROM clipping_artifacts AS artifact WHERE artifact.job_id=clipping_stages.job_id)`, now); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE clipping_stages SET status='failed',lease_expires_at=0,error='Worker reported a failure',updated_at=?
		WHERE status='paused_budget' AND EXISTS(
			SELECT 1 FROM clipping_stage_attempts AS attempt WHERE attempt.job_id=clipping_stages.job_id
			AND attempt.stage_name=clipping_stages.name AND attempt.attempt_id=clipping_stages.attempt_id
			AND attempt.status='failed' AND attempt.callback_received_at>0
		)`, now); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE clipping_stages SET status='queued',reserved_micro_usd=0,lease_expires_at=0,error='',updated_at=?
		WHERE status='paused_budget' AND NOT EXISTS(
			SELECT 1 FROM clipping_stage_attempts AS attempt WHERE attempt.job_id=clipping_stages.job_id
			AND attempt.stage_name=clipping_stages.name AND attempt.attempt_id=clipping_stages.attempt_id
			AND attempt.status IN ('running','expired') AND attempt.callback_received_at=0 AND attempt.settled_at=0
		)`, now); err != nil {
		return err
	}
	rows, err = tx.Query(`SELECT id FROM clipping_jobs WHERE status='paused_budget'`)
	if err != nil {
		return err
	}
	var pausedJobIDs []string
	for rows.Next() {
		var jobID string
		if err := rows.Scan(&jobID); err != nil {
			rows.Close()
			return err
		}
		pausedJobIDs = append(pausedJobIDs, jobID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, jobID := range pausedJobIDs {
		if _, err := refreshClippingJobStatus(tx, jobID, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func ensureClippingColumn(db *sql.DB, table, name, declaration string) error {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var columnName, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &columnName, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return err
		}
		if columnName == name {
			return rows.Err()
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + name + ` ` + declaration)
	return err
}

const maxClippingCapabilityCleanupBatch = 100

type clippingLegacyCallbackExpirySetting struct {
	Key        string
	Ciphertext string
}

// SetClippingCallbackCapabilityExpiryIndex stores only public identifiers and
// the absolute callback expiry. The bearer capabilities remain encrypted in
// the settings table under their own stable keys.
func (s *Store) SetClippingCallbackCapabilityExpiryIndex(jobID, attemptID string, expiresAt int64) error {
	if s == nil || s.db == nil || !safeID(jobID) || !strings.HasPrefix(jobID, "clipjob_") ||
		!safeID(attemptID) || !strings.HasPrefix(attemptID, "clipatt_") || expiresAt <= 0 {
		return ErrClippingInvalidState
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var existing int64
	err = tx.QueryRow(`SELECT expires_at FROM clipping_capability_expiry_index WHERE job_id=? AND attempt_id=?`, jobID, attemptID).Scan(&existing)
	if err == nil {
		if existing != expiresAt {
			return ErrClippingInvalidState
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO clipping_capability_expiry_index(job_id,attempt_id,expires_at) VALUES(?,?,?)`, jobID, attemptID, expiresAt); err != nil {
		return err
	}
	return tx.Commit()
}

// LegacyClippingCallbackExpirySettings returns a bounded page of old encrypted
// callback-expiry settings that predate the durable index.
func (s *Store) LegacyClippingCallbackExpirySettings(limit int) ([]clippingLegacyCallbackExpirySetting, error) {
	if s == nil || s.db == nil {
		return nil, ErrClippingInvalidState
	}
	if limit <= 0 || limit > maxClippingCapabilityCleanupBatch {
		limit = maxClippingCapabilityCleanupBatch
	}
	const prefix = "clipping_cap_"
	const suffix = "_callback_expiry"
	keyLength := len(prefix) + len("clipjob_") + 32 + 1 + len("clipatt_") + 32 + len(suffix)
	rows, err := s.db.Query(`SELECT setting.key,setting.value FROM settings AS setting
		WHERE substr(setting.key,1,?)=? AND length(setting.key)=? AND substr(setting.key,length(setting.key)-?+1)=?
		AND NOT EXISTS (
			SELECT 1 FROM clipping_capability_expiry_index AS expiry
			WHERE setting.key=? || expiry.job_id || '_' || expiry.attempt_id || ?
		)
		ORDER BY setting.updated_at,setting.key LIMIT ?`, len(prefix), prefix, keyLength, len(suffix), suffix, prefix, suffix, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	settings := make([]clippingLegacyCallbackExpirySetting, 0)
	for rows.Next() {
		var setting clippingLegacyCallbackExpirySetting
		if err := rows.Scan(&setting.Key, &setting.Ciphertext); err != nil {
			return nil, err
		}
		settings = append(settings, setting)
	}
	return settings, rows.Err()
}

// DeleteClippingCapabilitiesForAttempt removes the five encrypted settings and
// their nonsecret expiry index in one transaction. It is used for failed
// pre-dispatch persistence and after expiry validation has established that
// the callback window is closed.
func (s *Store) DeleteClippingCapabilitiesForAttempt(jobID, attemptID string) error {
	if s == nil || s.db == nil || !safeID(jobID) || !strings.HasPrefix(jobID, "clipjob_") ||
		!safeID(attemptID) || !strings.HasPrefix(attemptID, "clipatt_") {
		return ErrClippingInvalidState
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM settings WHERE key IN (?,?,?,?,?)`,
		clippingCapabilitySettingKey(jobID, attemptID, clippingCapabilityMedia),
		clippingCapabilitySettingKey(jobID, attemptID, clippingCapabilityCallback),
		clippingCapabilitySettingKey(jobID, attemptID, clippingCapabilityLease),
		clippingCapabilitySettingKey(jobID, attemptID, clippingCapabilityMedia+"_expiry"),
		clippingCapabilitySettingKey(jobID, attemptID, clippingCapabilityCallback+"_expiry")); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM clipping_capability_expiry_index WHERE job_id=? AND attempt_id=?`, jobID, attemptID); err != nil {
		return err
	}
	return tx.Commit()
}

// CleanupExpiredClippingCapabilities removes expired encrypted capability
// settings. It retains the nonsecret absolute callback expiry while that
// attempt can still publish a callback, because recovery must use the exact
// expiry that was issued even after the settings have been removed.
func (s *Store) CleanupExpiredClippingCapabilities(now int64, limit int) (int, error) {
	if s == nil || s.db == nil {
		return 0, ErrClippingInvalidState
	}
	if now <= 0 {
		now = time.Now().Unix()
	}
	if limit <= 0 || limit > maxClippingCapabilityCleanupBatch {
		limit = maxClippingCapabilityCleanupBatch
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT job_id,attempt_id FROM clipping_capability_expiry_index AS expiry
		WHERE expiry.expires_at<=? AND EXISTS(
			SELECT 1 FROM settings WHERE key IN (
				'clipping_cap_' || expiry.job_id || '_' || expiry.attempt_id || '_media',
				'clipping_cap_' || expiry.job_id || '_' || expiry.attempt_id || '_callback',
				'clipping_cap_' || expiry.job_id || '_' || expiry.attempt_id || '_lease',
				'clipping_cap_' || expiry.job_id || '_' || expiry.attempt_id || '_media_expiry',
				'clipping_cap_' || expiry.job_id || '_' || expiry.attempt_id || '_callback_expiry'))
		ORDER BY expiry.expires_at,expiry.job_id,expiry.attempt_id LIMIT ?`, now, limit)
	if err != nil {
		return 0, err
	}
	type expiredCapability struct{ jobID, attemptID string }
	expired := make([]expiredCapability, 0)
	for rows.Next() {
		var item expiredCapability
		if err := rows.Scan(&item.jobID, &item.attemptID); err != nil {
			rows.Close()
			return 0, err
		}
		expired = append(expired, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	cleaned := 0
	for _, item := range expired {
		result, err := tx.Exec(`DELETE FROM settings WHERE key IN (?,?,?,?,?)`,
			clippingCapabilitySettingKey(item.jobID, item.attemptID, clippingCapabilityMedia),
			clippingCapabilitySettingKey(item.jobID, item.attemptID, clippingCapabilityCallback),
			clippingCapabilitySettingKey(item.jobID, item.attemptID, clippingCapabilityLease),
			clippingCapabilitySettingKey(item.jobID, item.attemptID, clippingCapabilityMedia+"_expiry"),
			clippingCapabilitySettingKey(item.jobID, item.attemptID, clippingCapabilityCallback+"_expiry"))
		if err != nil {
			return 0, err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		if changed > 0 {
			cleaned++
		}
	}
	// Expiry indexes are needed only for the current, unsettled attempt. Keep
	// those rows after deleting the bearer settings; remove indexes for terminal
	// or superseded attempts in the same transaction.
	if _, err := tx.Exec(`DELETE FROM clipping_capability_expiry_index AS expiry WHERE expiry.expires_at<=?
		AND NOT EXISTS (
			SELECT 1 FROM clipping_stage_attempts AS attempt
			JOIN clipping_stages AS stage ON stage.job_id=attempt.job_id AND stage.name=attempt.stage_name
			JOIN clipping_jobs AS job ON job.id=attempt.job_id
			WHERE attempt.job_id=expiry.job_id AND attempt.attempt_id=expiry.attempt_id
			AND stage.attempt_id=attempt.attempt_id AND stage.status IN ('running','uncertain')
			AND job.status NOT IN ('completed','failed','canceled')
			AND attempt.status IN ('running','expired') AND attempt.callback_received_at=0 AND attempt.settled_at=0)`, now); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return cleaned, nil
}

func newClippingStorageID(prefix string) (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(raw[:]), nil
}

func safeClippingSourceID(id string) bool {
	return safeID(id) && strings.HasPrefix(id, "clipsrc_")
}

func clippingSourceDir(root, id string) string {
	return filepath.Join(root, id)
}

func clippingStoragePath(root, id string) string {
	return filepath.Join(clippingSourceDir(root, id), "source.media")
}

func clippingTempPath(root, id string) string {
	return clippingStoragePath(root, id) + ".part"
}

func cleanClippingSourceName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, `\`, "/"))
	var b strings.Builder
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
		if b.Len() > 255 {
			break
		}
	}
	name = strings.TrimSpace(b.String())
	if name == "" || name == "." || name == string(filepath.Separator) {
		return "source"
	}
	return name
}

func clippingSourceColumns() string {
	return `id,kind,status,original_name,media_type,source_url,rights_attested,storage_path,failure,declared_size_bytes,reserved_size_bytes,size_bytes,upload_offset,duration_ms,sha256,import_expectation_json,import_expected_size_bytes,import_expected_sha256,retain_until,import_attempts,next_import_at,import_lease_until,media_lease_until,cleanup_complete,diagnostics_json,diagnostics_sequence,created_at,updated_at,deleted_at`
}

type clippingRowScanner interface {
	Scan(dest ...any) error
}

func scanClippingSource(row clippingRowScanner) (ClippingSource, error) {
	var source ClippingSource
	var cleanupComplete int
	var rightsAttested int
	var diagnosticsJSON string
	var diagnosticsSequence int64
	err := row.Scan(&source.ID, &source.Kind, &source.Status, &source.OriginalName, &source.MediaType, &source.SourceURL, &rightsAttested, &source.StoragePath, &source.Failure,
		&source.DeclaredSizeBytes, &source.ReservedSizeBytes, &source.SizeBytes, &source.UploadOffset, &source.DurationMS, &source.SHA256, &source.ImportExpectation, &source.ImportExpectedSize, &source.ImportExpectedSHA, &source.RetainUntil,
		&source.ImportAttempts, &source.NextImportAt, &source.ImportLeaseUntil, &source.MediaLeaseUntil, &cleanupComplete, &diagnosticsJSON, &diagnosticsSequence, &source.CreatedAt, &source.UpdatedAt, &source.DeletedAt)
	source.CleanupComplete = cleanupComplete != 0
	source.RightsAttested = rightsAttested != 0
	source.Diagnostics = decodeClippingSourceDiagnostics(diagnosticsJSON)
	return source, err
}

func (s *Store) CreateClippingSource(input ClippingSourceCreate) (ClippingSource, error) {
	if !validClippingSourceKind(input.Kind) {
		return ClippingSource{}, errors.New("invalid clipping source kind")
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

func (s *Store) ClippingSource(id string) (ClippingSource, error) {
	if !safeClippingSourceID(id) {
		return ClippingSource{}, ErrClippingSourceNotFound
	}
	source, err := scanClippingSource(s.db.QueryRow(`SELECT `+clippingSourceColumns()+` FROM clipping_sources WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return ClippingSource{}, ErrClippingSourceNotFound
	}
	return source, err
}

func (s *Store) ClippingSources() ([]ClippingSource, error) {
	rows, err := s.db.Query(`SELECT ` + clippingSourceColumns() + ` FROM clipping_sources ORDER BY created_at DESC,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sources := make([]ClippingSource, 0)
	for rows.Next() {
		source, err := scanClippingSource(rows)
		if err != nil {
			return nil, err
		}
		sources = append(sources, source)
	}
	return sources, rows.Err()
}

func (s *Store) ClippingSourcePath(id string) (string, error) {
	if !safeClippingSourceID(id) {
		return "", ErrClippingSourceNotFound
	}
	var path string
	if err := s.db.QueryRow(`SELECT storage_path FROM clipping_sources WHERE id=?`, id).Scan(&path); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrClippingSourceNotFound
		}
		return "", err
	}
	expected := clippingStoragePath(s.clippingDir, id)
	if filepath.Clean(path) != filepath.Clean(expected) {
		return "", errors.New("clipping source path failed storage containment check")
	}
	return expected, nil
}

func (s *Store) ClippingSourceTempPath(id string) (string, error) {
	if _, err := s.ClippingSourcePath(id); err != nil {
		return "", err
	}
	return clippingTempPath(s.clippingDir, id), nil
}

func (s *Store) UpdateClippingSourceProgress(id string, status ClippingSourceStatus, uploadOffset, sizeBytes int64) error {
	if !safeClippingSourceID(id) || (status != ClippingSourceUploading && status != ClippingSourceImporting) || uploadOffset < 0 || sizeBytes < 0 {
		return errors.New("invalid clipping source progress")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var currentStatus ClippingSourceStatus
	var declared, currentOffset, currentSize int64
	var rightsAttested int
	if err := tx.QueryRow(`SELECT status,declared_size_bytes,upload_offset,size_bytes,rights_attested FROM clipping_sources WHERE id=?`, id).Scan(&currentStatus, &declared, &currentOffset, &currentSize, &rightsAttested); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrClippingSourceNotFound
		}
		return err
	}
	if currentStatus != status || rightsAttested == 0 || sizeBytes > declared || declared > MaxClippingSourceBytes {
		return ErrClippingInvalidState
	}
	if status == ClippingSourceUploading {
		if uploadOffset < currentOffset || sizeBytes < currentSize || uploadOffset > sizeBytes || uploadOffset > declared {
			return errors.New("upload progress must be monotonic and within the declared size")
		}
	} else if uploadOffset != 0 || sizeBytes < currentSize {
		return errors.New("import progress must be monotonic and use zero upload offset")
	}
	_, err = tx.Exec(`UPDATE clipping_sources SET upload_offset=?,size_bytes=?,updated_at=? WHERE id=? AND status=?`, uploadOffset, sizeBytes, time.Now().Unix(), id, status)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ResetClippingSourceImportProgress(id string) error {
	if !safeClippingSourceID(id) {
		return ErrClippingSourceNotFound
	}
	result, err := s.db.Exec(`UPDATE clipping_sources SET upload_offset=0,size_bytes=0,import_expectation_json='',import_expected_size_bytes=0,import_expected_sha256='',updated_at=? WHERE id=? AND status='importing'`, time.Now().Unix(), id)
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

func (s *Store) SetClippingSourceImportExpectation(id string, expectation youtubeImportExpectation) error {
	if !safeClippingSourceID(id) || !validYouTubeImportExpectation(expectation) {
		return ErrClippingInvalidState
	}
	encoded, err := json.Marshal(expectation)
	if err != nil || len(encoded) > 2048 {
		return ErrClippingInvalidState
	}
	result, err := s.db.Exec(`UPDATE clipping_sources SET import_expectation_json=?,import_expected_size_bytes=0,import_expected_sha256='',updated_at=? WHERE id=? AND status='importing' AND kind='youtube_original_file'`, string(encoded), time.Now().Unix(), id)
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

// CompleteClippingSourceTrackWorkspace persists that the split tracks were
// removed and their directory synced. The marker prevents restart-time
// capacity reconciliation from recreating a scratch reservation after mux.
// Callers serialize this with clippingMu and only after durable cleanup.
func (s *Store) CompleteClippingSourceTrackWorkspace(id string) error {
	if !safeClippingSourceID(id) {
		return ErrClippingInvalidState
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var kind ClippingSourceKind
	var status ClippingSourceStatus
	var encoded string
	if err := tx.QueryRow(`SELECT kind,status,import_expectation_json FROM clipping_sources WHERE id=?`, id).Scan(&kind, &status, &encoded); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrClippingSourceNotFound
		}
		return err
	}
	var expectation youtubeImportExpectation
	if kind != ClippingSourceYouTube || status != ClippingSourceImporting || json.Unmarshal([]byte(encoded), &expectation) != nil || !validYouTubeImportExpectation(expectation) || !expectation.SeparateTracks {
		return ErrClippingInvalidState
	}
	expectation.WorkspaceComplete = true
	updated, err := json.Marshal(expectation)
	if err != nil || len(updated) > 2048 {
		return ErrClippingInvalidState
	}
	if _, err := tx.Exec(`UPDATE clipping_sources SET import_expectation_json=?,updated_at=? WHERE id=? AND status='importing' AND kind='youtube_original_file'`, string(updated), time.Now().Unix(), id); err != nil {
		return err
	}
	return tx.Commit()
}

// SetClippingSourceImportFinalExpectation durably binds a complete temporary
// output to its validated size and digest before atomic publication. Recovery
// after rename can therefore reject a same-size corrupted or partial file.
func (s *Store) SetClippingSourceImportFinalExpectation(id string, sizeBytes int64, sha256Hex string) error {
	if !safeClippingSourceID(id) || sizeBytes < 1 || sizeBytes > MaxClippingSourceBytes || len(sha256Hex) != sha256.Size*2 {
		return ErrClippingInvalidState
	}
	if _, err := hex.DecodeString(sha256Hex); err != nil {
		return ErrClippingInvalidState
	}
	result, err := s.db.Exec(`UPDATE clipping_sources SET import_expected_size_bytes=?,import_expected_sha256=?,updated_at=? WHERE id=? AND status='importing' AND kind='youtube_original_file' AND import_expectation_json<>''`, sizeBytes, strings.ToLower(sha256Hex), time.Now().Unix(), id)
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

func (s *Store) MarkClippingSourceReady(id string, sizeBytes, durationMS int64, sha256Hex string) error {
	if !safeClippingSourceID(id) || sizeBytes < 1 || sizeBytes > MaxClippingSourceBytes || durationMS < 1 || durationMS > MaxClippingSourceDurationMS {
		return errors.New("source size or duration is outside clipping limits")
	}
	if len(sha256Hex) != sha256.Size*2 {
		return errors.New("source SHA-256 must contain 64 hexadecimal characters")
	}
	if _, err := hex.DecodeString(sha256Hex); err != nil {
		return errors.New("source SHA-256 must be hexadecimal")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var kind ClippingSourceKind
	var status ClippingSourceStatus
	var rightsAttested int
	var declared, offset int64
	var expectedSize int64
	var expectedSHA, expectationJSON string
	if err := tx.QueryRow(`SELECT kind,status,declared_size_bytes,upload_offset,rights_attested,import_expectation_json,import_expected_size_bytes,import_expected_sha256 FROM clipping_sources WHERE id=?`, id).Scan(&kind, &status, &declared, &offset, &rightsAttested, &expectationJSON, &expectedSize, &expectedSHA); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrClippingSourceNotFound
		}
		return err
	}
	if (status != ClippingSourceUploading && status != ClippingSourceImporting) || rightsAttested == 0 {
		return ErrClippingInvalidState
	}
	if sizeBytes > declared {
		return errors.New("actual source size exceeds its durable reservation")
	}
	if kind == ClippingSourceUpload && (sizeBytes != declared || offset != sizeBytes) {
		return errors.New("upload is incomplete")
	}
	if kind != ClippingSourceUpload && status == ClippingSourceImporting && offset != 0 {
		return ErrClippingInvalidState
	}
	if kind == ClippingSourceYouTube {
		var expectation youtubeImportExpectation
		if expectationJSON == "" || json.Unmarshal([]byte(expectationJSON), &expectation) != nil || !validYouTubeImportExpectation(expectation) ||
			expectation.SeparateTracks && !expectation.WorkspaceComplete || expectedSize != sizeBytes || !strings.EqualFold(expectedSHA, sha256Hex) {
			return ErrClippingInvalidState
		}
	}
	now := time.Now().Unix()
	_, err = tx.Exec(`UPDATE clipping_sources SET status='ready',reserved_size_bytes=?,size_bytes=?,upload_offset=?,duration_ms=?,sha256=?,failure='',next_import_at=0,import_lease_until=0,updated_at=? WHERE id=?`, sizeBytes, sizeBytes, sizeBytes, durationMS, strings.ToLower(sha256Hex), now, id)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) FailClippingSource(id, reason string) error {
	if !safeClippingSourceID(id) {
		return ErrClippingSourceNotFound
	}
	result, err := s.db.Exec(`UPDATE clipping_sources SET status='failed',failure=?,next_import_at=0,import_lease_until=0,updated_at=? WHERE id=? AND status IN ('uploading','importing')`, cleanClippingError(reason), time.Now().Unix(), id)
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

// ReleaseFailedClippingSourceReservation returns aggregate capacity after an
// acquisition failure has removed the generated source directory. Callers
// must serialize this with acquisition and cleanup using clippingMu. The
// storage layer also verifies that the generated directory is absent before
// it releases capacity, and only failed sources are eligible.
func (s *Store) ReleaseFailedClippingSourceReservation(id string) error {
	if !safeClippingSourceID(id) {
		return ErrClippingSourceNotFound
	}
	var status ClippingSourceStatus
	err := s.db.QueryRow(`SELECT status FROM clipping_sources WHERE id=?`, id).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrClippingSourceNotFound
	}
	if err != nil {
		return err
	}
	if status != ClippingSourceFailed {
		return ErrClippingInvalidState
	}
	if _, err := os.Lstat(clippingSourceDir(s.clippingDir, id)); err == nil {
		return errors.New("cannot release source reservation while its media directory exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("verify failed source media cleanup: %w", err)
	}
	result, err := s.db.Exec(`UPDATE clipping_sources SET reserved_size_bytes=0,updated_at=? WHERE id=? AND status='failed'`, time.Now().Unix(), id)
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

func (s *Store) BeginClippingImport(id string, expectedAttempts int, leaseFor time.Duration) (ClippingSource, error) {
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

func (s *Store) RetryClippingImport(id string, expectedAttempts int, nextAt int64, reason string) error {
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

func (s *Store) PendingClippingImports(now int64) ([]ClippingSource, error) {
	if now <= 0 {
		now = time.Now().Unix()
	}
	if _, err := s.db.Exec(`UPDATE clipping_sources SET status='failed',failure='Import attempt limit reached',next_import_at=0,import_lease_until=0,updated_at=? WHERE status='importing' AND import_attempts>=? AND next_import_at<=? AND import_lease_until<=?`, now, MaxClippingImportAttempts, now, now); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT `+clippingSourceColumns()+` FROM clipping_sources WHERE status='importing' AND import_attempts<? AND next_import_at<=? AND import_lease_until<=? ORDER BY next_import_at,created_at,id`, MaxClippingImportAttempts, now, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sources := make([]ClippingSource, 0)
	for rows.Next() {
		source, err := scanClippingSource(rows)
		if err != nil {
			return nil, err
		}
		sources = append(sources, source)
	}
	return sources, rows.Err()
}

// FailedClippingSources returns terminally failed sources whose durable byte
// reservation still needs cleanup. Callers remove files under clippingMu and
// then invoke ReleaseFailedClippingSourceReservation.
func (s *Store) FailedClippingSources(limit int) ([]ClippingSource, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT `+clippingSourceColumns()+` FROM clipping_sources
		WHERE status='failed' AND reserved_size_bytes>0 AND failure<>'legacy_source_import_unsupported'
		ORDER BY updated_at,created_at,id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sources := make([]ClippingSource, 0)
	for rows.Next() {
		source, err := scanClippingSource(rows)
		if err != nil {
			return nil, err
		}
		sources = append(sources, source)
	}
	return sources, rows.Err()
}

// RenewClippingImportLease extends a currently live import attempt. A worker
// cannot revive an expired lease or renew a different attempt after recovery.
func (s *Store) RenewClippingImportLease(id string, expectedAttempts int, leaseFor time.Duration) error {
	if !safeClippingSourceID(id) || expectedAttempts < 1 || expectedAttempts > MaxClippingImportAttempts {
		return ErrClippingInvalidState
	}
	now := time.Now()
	leaseUntil := randomLeaseExpiry(now, leaseFor)
	result, err := s.db.Exec(`UPDATE clipping_sources SET import_lease_until=?,next_import_at=?,updated_at=?
		WHERE id=? AND status='importing' AND import_attempts=? AND import_lease_until>?`,
		leaseUntil, leaseUntil, now.Unix(), id, expectedAttempts, now.Unix())
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

func (s *Store) SetClippingSourceMediaLease(id string, expiresAt int64) error {
	if !safeClippingSourceID(id) || expiresAt < 0 {
		return ErrClippingInvalidState
	}
	now := time.Now().Unix()
	if expiresAt > now+24*60*60 {
		return errors.New("clipping media lease may not exceed 24 hours")
	}
	result, err := s.db.Exec(`UPDATE clipping_sources SET media_lease_until=?,updated_at=? WHERE id=? AND status='ready'`, expiresAt, now, id)
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

func clippingConservativeCallbackExpiry(attempt clippingAttemptRecord, source ClippingSource) int64 {
	// The persisted expiry index is authoritative. For old attempts without an
	// index, lease+the full callback grace is a safe upper bound even if the
	// source retention value was shortened after the callback capability was
	// issued. Keep the source-capped calculation in the maximum for consistency
	// with the capability's source retention policy.
	graceSeconds := int64(clippingCallbackCapabilityGrace.Seconds())
	if attempt.LeaseExpiresAt <= 0 || attempt.LeaseExpiresAt > math.MaxInt64-graceSeconds {
		return 0
	}
	expiry := attempt.LeaseExpiresAt + graceSeconds
	if calculated := clippingAnalysisCallbackExpiry(attempt, source); calculated > expiry {
		expiry = calculated
	}
	return expiry
}

func (s *Store) conservativeClippingCallbackExpiry(jobID, attemptID string) (int64, error) {
	if s == nil || s.db == nil || !safeID(jobID) || !strings.HasPrefix(jobID, "clipjob_") ||
		!safeID(attemptID) || !strings.HasPrefix(attemptID, "clipatt_") {
		return 0, ErrClippingInvalidState
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var stageName string
	if err := tx.QueryRow(`SELECT stage_name FROM clipping_stage_attempts WHERE job_id=? AND attempt_id=?`, jobID, attemptID).Scan(&stageName); err != nil {
		return 0, err
	}
	attempt, err := readClippingAttempt(tx, jobID, stageName, attemptID)
	if err != nil {
		return 0, err
	}
	var sourceID string
	if err := tx.QueryRow(`SELECT source_id FROM clipping_jobs WHERE id=?`, jobID).Scan(&sourceID); err != nil {
		return 0, err
	}
	source, err := scanClippingSource(tx.QueryRow(`SELECT `+clippingSourceColumns()+` FROM clipping_sources WHERE id=?`, sourceID))
	if err != nil {
		return 0, err
	}
	expiresAt := clippingConservativeCallbackExpiry(attempt, source)
	if expiresAt <= 0 {
		return 0, ErrClippingInvalidState
	}
	return expiresAt, tx.Commit()
}

// clippingIssuedCallbackExpiryTx returns the absolute capability expiry that
// was persisted before dispatch. Source retention can change after submission,
// so it must not shorten an already-issued callback window. Attempts predating
// the index use the full lease-plus-grace bound conservatively.
func clippingIssuedCallbackExpiryTx(tx *sql.Tx, attempt clippingAttemptRecord, source ClippingSource) (int64, error) {
	var expiresAt int64
	err := tx.QueryRow(`SELECT expires_at FROM clipping_capability_expiry_index WHERE job_id=? AND attempt_id=?`, attempt.JobID, attempt.AttemptID).Scan(&expiresAt)
	if err == nil {
		if expiresAt <= 0 {
			return 0, ErrClippingInvalidState
		}
		return expiresAt, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	expiresAt = clippingConservativeCallbackExpiry(attempt, source)
	if expiresAt <= 0 {
		return 0, ErrClippingInvalidState
	}
	return expiresAt, nil
}

func clippingSourceHasActiveCallbackWindowTx(tx *sql.Tx, source ClippingSource, now int64) (bool, error) {
	rows, err := tx.Query(`SELECT attempt.job_id,attempt.stage_name,attempt.attempt_id,job.status
		FROM clipping_stage_attempts AS attempt
		JOIN clipping_jobs AS job ON job.id=attempt.job_id
		WHERE job.source_id=? ORDER BY attempt.started_at,attempt.attempt_id`, source.ID)
	if err != nil {
		return false, err
	}
	type attemptKey struct {
		jobID, stageName, attemptID string
		jobStatus                   ClippingJobStatus
	}
	keys := make([]attemptKey, 0)
	for rows.Next() {
		var key attemptKey
		if err := rows.Scan(&key.jobID, &key.stageName, &key.attemptID, &key.jobStatus); err != nil {
			rows.Close()
			return false, err
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return false, err
	}
	if err := rows.Close(); err != nil {
		return false, err
	}
	for _, key := range keys {
		if key.jobStatus == ClippingJobCompleted || key.jobStatus == ClippingJobFailed || key.jobStatus == ClippingJobCanceled {
			continue
		}
		attempt, err := readClippingAttempt(tx, key.jobID, key.stageName, key.attemptID)
		if err != nil {
			return false, err
		}
		if attempt.ReconciliationReference == "known_no_submission" {
			continue
		}
		expiry, err := clippingIssuedCallbackExpiryTx(tx, attempt, source)
		if err != nil {
			return false, err
		}
		if expiry <= 0 || expiry > now {
			return true, nil
		}
	}
	return false, nil
}

func (s *Store) TombstoneClippingSource(id string) error {
	return s.tombstoneClippingSource(id, time.Now().Unix(), false)
}

func (s *Store) ExpireClippingSource(id string, now int64) error {
	if now <= 0 {
		now = time.Now().Unix()
	}
	return s.tombstoneClippingSource(id, now, true)
}

func (s *Store) tombstoneClippingSource(id string, now int64, automatic bool) error {
	if !safeClippingSourceID(id) {
		return ErrClippingSourceNotFound
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status ClippingSourceStatus
	var mediaLease, importLease, retainUntil int64
	if err := tx.QueryRow(`SELECT status,media_lease_until,import_lease_until,retain_until FROM clipping_sources WHERE id=?`, id).Scan(&status, &mediaLease, &importLease, &retainUntil); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrClippingSourceNotFound
		}
		return err
	}
	if status == ClippingSourceDeleted {
		if _, err := tx.Exec(`DELETE FROM clipping_analysis_claims WHERE source_id=?`, id); err != nil {
			return err
		}
		return tx.Commit()
	}
	if automatic && (retainUntil <= 0 || retainUntil > now) {
		return errors.New("clipping source retention period has not expired")
	}
	if automatic && importLease > now {
		return errors.New("clipping source is protected by an active import lease")
	}
	if mediaLease > now {
		return errors.New("clipping source is protected by an active media lease")
	}
	var active int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM clipping_jobs WHERE source_id=? AND status IN ('queued','running')`, id).Scan(&active); err != nil {
		return err
	}
	if active != 0 {
		return errors.New("clipping source is referenced by an active job")
	}
	source, err := scanClippingSource(tx.QueryRow(`SELECT `+clippingSourceColumns()+` FROM clipping_sources WHERE id=?`, id))
	if err != nil {
		return err
	}
	callbackActive, err := clippingSourceHasActiveCallbackWindowTx(tx, source, now)
	if err != nil {
		return err
	}
	if callbackActive {
		return errors.New("clipping source is protected until every worker callback window expires")
	}
	// Source-derived artifacts can contain transcripts and analysis content;
	// erase them with the media while keeping job, stage, and cost audit rows.
	if _, err := tx.Exec(`DELETE FROM clipping_analysis_claims WHERE source_id=?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM clipping_artifacts WHERE job_id IN (SELECT id FROM clipping_jobs WHERE source_id=?)`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM clipping_analysis_cache WHERE source_id=?`, id); err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE clipping_sources SET status='deleted',source_url='',deleted_at=?,updated_at=?,cleanup_complete=0,next_import_at=0,import_lease_until=0,media_lease_until=0 WHERE id=?`, now, now, id)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) MarkClippingSourceCleaned(id string) error {
	if !safeClippingSourceID(id) {
		return ErrClippingSourceNotFound
	}
	result, err := s.db.Exec(`UPDATE clipping_sources SET reserved_size_bytes=0,cleanup_complete=1,updated_at=? WHERE id=? AND status='deleted'`, time.Now().Unix(), id)
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

func (s *Store) ExpiredClippingSources(now int64, limit int) ([]ClippingSource, error) {
	if now <= 0 {
		now = time.Now().Unix()
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	scanLimit := limit * 5
	if scanLimit > 500 {
		scanLimit = 500
	}
	rows, err := s.db.Query(`SELECT `+clippingSourceColumns()+` FROM clipping_sources AS src WHERE
		(src.status='deleted' AND src.cleanup_complete=0) OR
		(src.status<>'deleted' AND src.retain_until>0 AND src.retain_until<=? AND src.media_lease_until<=? AND src.import_lease_until<=? AND NOT EXISTS (
			SELECT 1 FROM clipping_jobs AS job WHERE job.source_id=src.id AND job.status IN ('queued','running')
		)) ORDER BY CASE WHEN src.status='deleted' THEN 0 ELSE 1 END,src.retain_until,src.created_at LIMIT ?`, now, now, now, scanLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	candidates := make([]ClippingSource, 0, scanLimit)
	for rows.Next() {
		source, err := scanClippingSource(rows)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, source)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	sources := make([]ClippingSource, 0, limit)
	for _, source := range candidates {
		if source.Status != ClippingSourceDeleted {
			tx, err := s.db.Begin()
			if err != nil {
				return nil, err
			}
			blocked, checkErr := clippingSourceHasActiveCallbackWindowTx(tx, source, now)
			rollbackErr := tx.Rollback()
			if checkErr != nil {
				return nil, checkErr
			}
			if rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
				return nil, rollbackErr
			}
			if blocked {
				continue
			}
		}
		sources = append(sources, source)
		if len(sources) >= limit {
			break
		}
	}
	return sources, nil
}

func cleanClippingError(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 2048 {
		value = value[:2048]
	}
	return value
}

type clippingQueryer interface {
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

func clippingJobColumns() string {
	return `id,batch_id,source_id,content_type,min_clip_seconds,max_clip_seconds,candidate_limit,status,budget_limit_micro_usd,reserved_micro_usd,spent_micro_usd,error,created_at,updated_at`
}

func scanClippingJob(row clippingRowScanner) (ClippingJob, error) {
	var job ClippingJob
	err := row.Scan(&job.ID, &job.BatchID, &job.SourceID, &job.ContentType, &job.MinClipSeconds, &job.MaxClipSeconds, &job.CandidateLimit, &job.Status, &job.BudgetLimitMicroUSD, &job.ReservedMicroUSD, &job.SpentMicroUSD, &job.Error, &job.CreatedAt, &job.UpdatedAt)
	return job, err
}

func clippingJobRequestHash(input ClippingJobCreate) (string, error) {
	encoded, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:]), nil
}

// CreateClippingJob persists one source analysis request. The existing schema
// and Modal v2 protocol require a parent ID, so this creates a private
// one-to-one compatibility row with fixed inert values for legacy NOT NULL
// columns. No batch or monetary workflow is exposed or consulted.
func (s *Store) CreateClippingJob(input ClippingJobCreate, idempotencyKey string) (ClippingJob, bool, error) {
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
	var rightsAttested int
	var retainUntil int64
	if err := tx.QueryRow(`SELECT status,retain_until,rights_attested FROM clipping_sources WHERE id=?`, normalized.SourceID).Scan(&status, &retainUntil, &rightsAttested); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ClippingJob{}, false, ErrClippingSourceNotFound
		}
		return ClippingJob{}, false, err
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
	return ClippingJob{ID: jobID, BatchID: batchID, SourceID: normalized.SourceID, ContentType: normalized.ContentType,
		MinClipSeconds: normalized.MinClipSeconds, MaxClipSeconds: normalized.MaxClipSeconds,
		CandidateLimit: normalized.CandidateLimit, Status: ClippingJobQueued, CreatedAt: now, UpdatedAt: now}, false, nil
}

func (s *Store) ClippingJobs() ([]ClippingJob, error) {
	rows, err := s.db.Query(`SELECT ` + clippingJobColumns() + ` FROM clipping_jobs ORDER BY created_at,id`)
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

func (s *Store) ClippingJob(id string) (ClippingJob, error) {
	if !safeID(id) || !strings.HasPrefix(id, "clipjob_") {
		return ClippingJob{}, ErrClippingJobNotFound
	}
	job, err := scanClippingJob(s.db.QueryRow(`SELECT `+clippingJobColumns()+` FROM clipping_jobs WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return ClippingJob{}, ErrClippingJobNotFound
	}
	return job, err
}

func clippingStageColumns() string {
	return `job_id,name,status,idempotency_key,attempt_id,attempt,lease_expires_at,reserved_micro_usd,actual_micro_usd,estimated_micro_usd,cost_reconciled,error,created_at,updated_at`
}

func scanClippingStage(row clippingRowScanner) (ClippingStage, error) {
	var stage ClippingStage
	var reconciled int
	err := row.Scan(&stage.JobID, &stage.Name, &stage.Status, &stage.IdempotencyKey, &stage.AttemptID, &stage.Attempt, &stage.LeaseExpiresAt, &stage.ReservedMicroUSD, &stage.ActualMicroUSD, &stage.EstimatedMicroUSD, &reconciled, &stage.Error, &stage.CreatedAt, &stage.UpdatedAt)
	stage.CostReconciled = reconciled != 0
	return stage, err
}

func (s *Store) ClippingStages(jobID string) ([]ClippingStage, error) {
	if !safeID(jobID) || !strings.HasPrefix(jobID, "clipjob_") {
		return nil, ErrClippingJobNotFound
	}
	rows, err := s.db.Query(`SELECT `+clippingStageColumns()+` FROM clipping_stages WHERE job_id=? ORDER BY created_at,name`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	stages := make([]ClippingStage, 0)
	for rows.Next() {
		stage, err := scanClippingStage(rows)
		if err != nil {
			return nil, err
		}
		stages = append(stages, stage)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(stages) == 0 {
		if _, err := s.ClippingJob(jobID); err != nil {
			return nil, err
		}
	}
	return stages, nil
}

func readClippingStage(tx *sql.Tx, jobID, name string) (ClippingStage, error) {
	stage, err := scanClippingStage(tx.QueryRow(`SELECT `+clippingStageColumns()+` FROM clipping_stages WHERE job_id=? AND name=?`, jobID, name))
	if errors.Is(err, sql.ErrNoRows) {
		return ClippingStage{}, ErrClippingStageNotFound
	}
	return stage, err
}

func validStageName(name string) bool { return clippingStageNamePattern.MatchString(name) }

func validClippingArtifactType(name string) bool {
	return clippingArtifactTypePattern.MatchString(name)
}

func stageAttemptID() (string, error) { return newClippingStorageID("clipatt_") }

func newClippingLeaseToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func validClippingLease(token, storedHash string) bool {
	if len(token) != 64 || storedHash == "" {
		return false
	}
	if _, err := hex.DecodeString(token); err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(tokenHash(token)), []byte(storedHash)) == 1
}

func randomLeaseExpiry(now time.Time, leaseFor time.Duration) int64 {
	if leaseFor < time.Minute {
		leaseFor = time.Minute
	}
	if leaseFor > 24*time.Hour {
		leaseFor = 24 * time.Hour
	}
	return now.Add(leaseFor).Unix()
}

func refreshClippingJobStatus(tx *sql.Tx, jobID string, now int64) (ClippingJobStatus, error) {
	var current ClippingJobStatus
	if err := tx.QueryRow(`SELECT status FROM clipping_jobs WHERE id=?`, jobID).Scan(&current); err != nil {
		return "", err
	}
	if current == ClippingJobCanceled {
		return current, nil
	}
	rows, err := tx.Query(`SELECT status,COUNT(*) FROM clipping_stages WHERE job_id=? GROUP BY status`, jobID)
	if err != nil {
		return "", err
	}
	counts := make(map[ClippingStageStatus]int)
	for rows.Next() {
		var status ClippingStageStatus
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			rows.Close()
			return "", err
		}
		counts[status] = count
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return "", err
	}
	rows.Close()
	total := 0
	for _, count := range counts {
		total += count
	}
	status := ClippingJobQueued
	switch {
	case total == 0:
		status = ClippingJobQueued
	case counts[ClippingStageRunning] > 0:
		status = ClippingJobRunning
	case counts[ClippingStageUncertain] > 0:
		status = ClippingJobRunning
	case counts[ClippingStagePausedBudget] > 0:
		status = ClippingJobQueued
	case counts[ClippingStageFailed] > 0:
		status = ClippingJobFailed
	case counts[ClippingStageQueued] > 0:
		status = ClippingJobQueued
	case counts[ClippingStageCanceled] > 0:
		status = ClippingJobCanceled
	default:
		status = ClippingJobCompleted
	}
	_, err = tx.Exec(`UPDATE clipping_jobs SET status=?,updated_at=? WHERE id=?`, status, now, jobID)
	return status, err
}

func (s *Store) ClaimClippingStage(jobID, name string, reserveMicroUSD int64, leaseFor time.Duration) (ClippingStage, bool, error) {
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
	if job.Status == ClippingJobCanceled || job.Status == ClippingJobCompleted || job.Status == ClippingJobFailed || stage.Status == ClippingStageCanceled || stage.Status == ClippingStageCompleted || stage.Status == ClippingStageFailed || stage.Status == ClippingStageUncertain {
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

// RecoverExpiredClippingStages moves expired dispatches into an uncertain state
// while the issued callback capability remains valid. It makes an explicit
// retry available only after that capability expires, and never redispatches an
// unknown submission automatically.
func (s *Store) RecoverExpiredClippingStages(now int64) error {
	if now <= 0 {
		now = time.Now().Unix()
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT stage.job_id,stage.name,stage.attempt_id FROM clipping_stages AS stage
		WHERE stage.status='running' AND stage.attempt_id<>'' AND (
			stage.lease_expires_at<=? OR EXISTS(
				SELECT 1 FROM clipping_stage_attempts AS attempt
				WHERE attempt.job_id=stage.job_id AND attempt.stage_name=stage.name AND attempt.attempt_id=stage.attempt_id
				AND attempt.status IN ('running','expired') AND attempt.callback_received_at=0
				AND COALESCE((SELECT expiry.expires_at FROM clipping_capability_expiry_index AS expiry
					WHERE expiry.job_id=attempt.job_id AND expiry.attempt_id=attempt.attempt_id),
					attempt.lease_expires_at+?)<=?))`, now, int64(clippingCallbackCapabilityGrace.Seconds()), now)
	if err != nil {
		return err
	}
	type expiredStage struct{ jobID, name, attemptID string }
	var expired []expiredStage
	for rows.Next() {
		var item expiredStage
		if err := rows.Scan(&item.jobID, &item.name, &item.attemptID); err != nil {
			rows.Close()
			return err
		}
		expired = append(expired, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, item := range expired {
		if item.attemptID != "" {
			if _, err := tx.Exec(`UPDATE clipping_stage_attempts SET status='expired' WHERE attempt_id=? AND status='running'`, item.attemptID); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`UPDATE clipping_stages SET status='uncertain',error='Worker dispatch outcome is unknown; waiting for its callback window',updated_at=? WHERE job_id=? AND name=? AND attempt_id=? AND status='running'`, now, item.jobID, item.name, item.attemptID); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE clipping_jobs SET status='running',error='Worker dispatch outcome is unknown; waiting for its callback window',updated_at=? WHERE id=? AND status='running'`, now, item.jobID); err != nil {
			return err
		}
	}

	// Once the callback capability itself has expired, release the old hold
	// and source claim and make an explicit retry possible. Nothing is
	// automatically resubmitted from this recovery path.
	rows, err = tx.Query(`SELECT attempt.job_id,attempt.stage_name,attempt.attempt_id
		FROM clipping_stage_attempts AS attempt
		JOIN clipping_stages AS stage ON stage.job_id=attempt.job_id AND stage.name=attempt.stage_name
		WHERE stage.status='uncertain' AND stage.attempt_id=attempt.attempt_id
		AND attempt.callback_received_at=0 AND attempt.status='expired'
		AND COALESCE((SELECT expiry.expires_at FROM clipping_capability_expiry_index AS expiry
			WHERE expiry.job_id=attempt.job_id AND expiry.attempt_id=attempt.attempt_id),
			attempt.lease_expires_at+?)<=?`, int64(clippingCallbackCapabilityGrace.Seconds()), now)
	if err != nil {
		return err
	}
	var due []expiredStage
	for rows.Next() {
		var item expiredStage
		if err := rows.Scan(&item.jobID, &item.name, &item.attemptID); err != nil {
			rows.Close()
			return err
		}
		due = append(due, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, item := range due {
		job, err := scanClippingJob(tx.QueryRow(`SELECT `+clippingJobColumns()+` FROM clipping_jobs WHERE id=?`, item.jobID))
		if err != nil {
			return err
		}
		attempt, err := readClippingAttempt(tx, item.jobID, item.name, item.attemptID)
		if err != nil {
			return err
		}
		if err := releaseLegacyClippingHold(tx, &job, attempt, item.name, now); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE clipping_stage_attempts SET status='failed',reconciliation_reference='callback_window_expired',error='callback window expired',settled_at=CASE WHEN settled_at=0 THEN ? ELSE settled_at END WHERE attempt_id=?`, now, item.attemptID); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE clipping_stages SET status='failed',lease_expires_at=0,error='Worker callback window expired; retry is available',updated_at=? WHERE job_id=? AND name=? AND attempt_id=? AND status='uncertain'`, now, item.jobID, item.name, item.attemptID); err != nil {
			return err
		}
		if !clippingJobTerminal(job.Status) {
			if _, err := tx.Exec(`UPDATE clipping_jobs SET status='failed',error='Worker callback window expired; retry is available',updated_at=? WHERE id=?`, now, item.jobID); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`DELETE FROM clipping_analysis_claims WHERE job_id=? AND attempt_id=?`, item.jobID, item.attemptID); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM settings WHERE key IN (?,?,?,?,?)`,
			clippingCapabilitySettingKey(item.jobID, item.attemptID, clippingCapabilityMedia),
			clippingCapabilitySettingKey(item.jobID, item.attemptID, clippingCapabilityCallback),
			clippingCapabilitySettingKey(item.jobID, item.attemptID, clippingCapabilityLease),
			clippingCapabilitySettingKey(item.jobID, item.attemptID, clippingCapabilityMedia+"_expiry"),
			clippingCapabilitySettingKey(item.jobID, item.attemptID, clippingCapabilityCallback+"_expiry")); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM clipping_capability_expiry_index WHERE job_id=? AND attempt_id=?`, item.jobID, item.attemptID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ExpiredClippingStages(now int64, limit int) ([]ClippingStage, error) {
	if now <= 0 {
		now = time.Now().Unix()
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT `+clippingStageColumns()+` FROM clipping_stages AS stage
		WHERE (stage.status='running' AND (stage.lease_expires_at<=? OR EXISTS(
			SELECT 1 FROM clipping_stage_attempts AS attempt
			WHERE attempt.job_id=stage.job_id AND attempt.stage_name=stage.name AND attempt.attempt_id=stage.attempt_id
			AND attempt.callback_received_at=0 AND attempt.status IN ('running','expired')
			AND COALESCE((SELECT expiry.expires_at FROM clipping_capability_expiry_index AS expiry
				WHERE expiry.job_id=attempt.job_id AND expiry.attempt_id=attempt.attempt_id),
				attempt.lease_expires_at+?)<=?))) OR
		(stage.status='uncertain' AND EXISTS(
			SELECT 1 FROM clipping_stage_attempts AS attempt
			WHERE attempt.attempt_id=stage.attempt_id AND attempt.callback_received_at=0 AND attempt.status='expired'
			AND COALESCE((SELECT expiry.expires_at FROM clipping_capability_expiry_index AS expiry
				WHERE expiry.job_id=attempt.job_id AND expiry.attempt_id=attempt.attempt_id),
				attempt.lease_expires_at+?)<=?))
		ORDER BY stage.lease_expires_at,stage.job_id,stage.name LIMIT ?`,
		now, int64(clippingCallbackCapabilityGrace.Seconds()), now, int64(clippingCallbackCapabilityGrace.Seconds()), now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	stages := make([]ClippingStage, 0)
	for rows.Next() {
		stage, err := scanClippingStage(rows)
		if err != nil {
			return nil, err
		}
		stages = append(stages, stage)
	}
	return stages, rows.Err()
}

func (s *Store) RenewClippingStageLease(jobID, name, attemptID, leaseToken string, leaseFor time.Duration) error {
	if !safeID(jobID) || !strings.HasPrefix(jobID, "clipjob_") || !validStageName(name) || attemptID == "" || leaseToken == "" {
		return ErrClippingStaleAttempt
	}
	now := time.Now()
	leaseUntil := randomLeaseExpiry(now, leaseFor)
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var storedHash, attemptStatus string
	var oldLease int64
	if err := tx.QueryRow(`SELECT token_hash,status,lease_expires_at FROM clipping_stage_attempts WHERE attempt_id=? AND job_id=? AND stage_name=?`, attemptID, jobID, name).Scan(&storedHash, &attemptStatus, &oldLease); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrClippingStaleAttempt
		}
		return err
	}
	if !validClippingLease(leaseToken, storedHash) || attemptStatus != "running" || oldLease <= now.Unix() {
		return ErrClippingStaleAttempt
	}
	var stageAttempt string
	var stageStatus ClippingStageStatus
	if err := tx.QueryRow(`SELECT attempt_id,status FROM clipping_stages WHERE job_id=? AND name=?`, jobID, name).Scan(&stageAttempt, &stageStatus); err != nil {
		return ErrClippingStaleAttempt
	}
	var jobStatus ClippingJobStatus
	if err := tx.QueryRow(`SELECT status FROM clipping_jobs WHERE id=?`, jobID).Scan(&jobStatus); err != nil {
		return ErrClippingJobNotFound
	}
	if stageAttempt != attemptID || stageStatus != ClippingStageRunning || jobStatus == ClippingJobCanceled || jobStatus == ClippingJobCompleted || jobStatus == ClippingJobFailed {
		return ErrClippingStaleAttempt
	}
	if _, err := tx.Exec(`UPDATE clipping_stage_attempts SET lease_expires_at=? WHERE attempt_id=?`, leaseUntil, attemptID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE clipping_stages SET lease_expires_at=?,updated_at=? WHERE job_id=? AND name=?`, leaseUntil, now.Unix(), jobID, name); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RetryClippingStage(jobID, name string) (ClippingStage, error) {
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
	if job.Status == ClippingJobCanceled || job.Status == ClippingJobCompleted {
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

type clippingAttemptRecord struct {
	AttemptID                     string
	JobID                         string
	StageName                     string
	AttemptNo                     int
	IdempotencyKey                string
	TokenHash                     string
	Status                        string
	LeaseExpiresAt                int64
	ReservedMicroUSD              int64
	ActualMicroUSD                int64
	EstimatedMicroUSD             int64
	CostReconciled                bool
	ReconciliationReference       string
	OperatorRateMicroUSDPerSecond int64
	MaxComputeSeconds             int64
	PipelineRevision              string
	CallbackReceivedAt            int64
	CallbackDigest                string
	SettledAt                     int64
}

func readClippingAttempt(tx *sql.Tx, jobID, stageName, attemptID string) (clippingAttemptRecord, error) {
	var attempt clippingAttemptRecord
	var reconciled int
	err := tx.QueryRow(`SELECT attempt_id,job_id,stage_name,attempt_no,idempotency_key,token_hash,status,lease_expires_at,reserved_micro_usd,actual_micro_usd,estimated_micro_usd,cost_reconciled,reconciliation_reference,operator_rate_micro_usd_per_second,max_compute_seconds,pipeline_revision,callback_received_at,callback_digest,settled_at FROM clipping_stage_attempts WHERE attempt_id=? AND job_id=? AND stage_name=?`, attemptID, jobID, stageName).Scan(
		&attempt.AttemptID, &attempt.JobID, &attempt.StageName, &attempt.AttemptNo, &attempt.IdempotencyKey, &attempt.TokenHash, &attempt.Status, &attempt.LeaseExpiresAt, &attempt.ReservedMicroUSD, &attempt.ActualMicroUSD, &attempt.EstimatedMicroUSD, &reconciled, &attempt.ReconciliationReference, &attempt.OperatorRateMicroUSDPerSecond, &attempt.MaxComputeSeconds, &attempt.PipelineRevision, &attempt.CallbackReceivedAt, &attempt.CallbackDigest, &attempt.SettledAt)
	attempt.CostReconciled = reconciled != 0
	return attempt, err
}

func clippingStageTerminal(status ClippingStageStatus) bool {
	return status == ClippingStageCompleted || status == ClippingStageFailed || status == ClippingStageCanceled
}

func clippingJobTerminal(status ClippingJobStatus) bool {
	return status == ClippingJobCompleted || status == ClippingJobFailed || status == ClippingJobCanceled
}

func releaseLegacyClippingReservation(tx *sql.Tx, job *ClippingJob, amount int64, now int64) error {
	if amount < 0 || amount > job.ReservedMicroUSD {
		return errors.New("clipping job reservation counters are inconsistent")
	}
	var batchReserved int64
	if err := tx.QueryRow(`SELECT reserved_micro_usd FROM clipping_batches WHERE id=?`, job.BatchID).Scan(&batchReserved); err != nil {
		return err
	}
	if amount > batchReserved {
		return errors.New("clipping batch reservation counters are inconsistent")
	}
	job.ReservedMicroUSD -= amount
	if _, err := tx.Exec(`UPDATE clipping_jobs SET reserved_micro_usd=?,updated_at=? WHERE id=?`, job.ReservedMicroUSD, now, job.ID); err != nil {
		return err
	}
	_, err := tx.Exec(`UPDATE clipping_batches SET reserved_micro_usd=?,updated_at=? WHERE id=?`, batchReserved-amount, now, job.BatchID)
	return err
}

func canonicalClippingArtifactSize(artifact ClippingArtifact, rangesJSON string) (int, error) {
	var compactPayload bytes.Buffer
	if err := json.Compact(&compactPayload, []byte(artifact.PayloadJSON)); err != nil {
		return 0, err
	}
	envelope := struct {
		Type             string          `json:"type"`
		SchemaVersion    string          `json:"schema_version"`
		Version          int             `json:"version"`
		SourceDurationMS int64           `json:"source_duration_ms"`
		TimeRanges       json.RawMessage `json:"time_ranges"`
		Payload          json.RawMessage `json:"payload"`
	}{
		Type: artifact.Type, SchemaVersion: artifact.SchemaVersion, Version: artifact.Version,
		SourceDurationMS: artifact.SourceDurationMS, TimeRanges: json.RawMessage(rangesJSON),
		Payload: compactPayload.Bytes(),
	}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(envelope); err != nil {
		return 0, err
	}
	// Go always escapes U+2028/U+2029, while the worker's canonical UTF-8 JSON
	// leaves them literal. Each rune is three UTF-8 bytes versus six escaped
	// ASCII bytes; compensate so the size matches the worker's 4 MiB check.
	size := encoded.Len() - 1 // Encoder.Encode appends one trailing newline.
	for _, escaped := range [][]byte{[]byte(`\u2028`), []byte(`\u2029`)} {
		size -= 3 * bytes.Count(encoded.Bytes(), escaped)
	}
	return size, nil
}

func validateClippingArtifact(artifact ClippingArtifact, jobID string, sourceDurationMS int64) (string, error) {
	if artifact.JobID != jobID || !validClippingArtifactType(artifact.Type) || strings.TrimSpace(artifact.SchemaVersion) == "" || len(artifact.SchemaVersion) > 128 || artifact.Version < 1 {
		return "", ErrClippingInvalidArtifact
	}
	if artifact.SourceDurationMS != sourceDurationMS || sourceDurationMS < 1 || sourceDurationMS > MaxClippingSourceDurationMS {
		return "", fmt.Errorf("%w: artifact source duration does not match source", ErrClippingInvalidArtifact)
	}
	if len(artifact.PayloadJSON) == 0 || len(artifact.PayloadJSON) > maxClippingArtifactPayloadBytes || !json.Valid([]byte(artifact.PayloadJSON)) {
		return "", fmt.Errorf("%w: artifact payload must be valid JSON under 4 MiB", ErrClippingInvalidArtifact)
	}
	if artifact.TimeRanges == nil || len(artifact.TimeRanges) > 1000 {
		return "", fmt.Errorf("%w: invalid timestamp ranges", ErrClippingInvalidArtifact)
	}
	for _, span := range artifact.TimeRanges {
		if span.StartMS < 0 || span.EndMS <= span.StartMS || span.EndMS > sourceDurationMS {
			return "", fmt.Errorf("%w: timestamp range falls outside source", ErrClippingInvalidArtifact)
		}
	}
	rangesJSON, err := json.Marshal(artifact.TimeRanges)
	if err != nil {
		return "", fmt.Errorf("%w: invalid time ranges", ErrClippingInvalidArtifact)
	}
	artifactSize, err := canonicalClippingArtifactSize(artifact, string(rangesJSON))
	if err != nil || artifactSize > maxClippingArtifactPayloadBytes {
		return "", fmt.Errorf("%w: complete artifact exceeds 4 MiB", ErrClippingInvalidArtifact)
	}
	return string(rangesJSON), nil
}

func insertClippingArtifact(tx *sql.Tx, artifact ClippingArtifact, rangesJSON string, now int64) error {
	if artifact.CreatedAt <= 0 {
		artifact.CreatedAt = now
	}
	var existingSchema, existingRanges, existingPayload string
	var existingDuration int64
	err := tx.QueryRow(`SELECT schema_version,source_duration_ms,time_ranges_json,payload_json FROM clipping_artifacts WHERE job_id=? AND artifact_type=? AND version=?`, artifact.JobID, artifact.Type, artifact.Version).Scan(&existingSchema, &existingDuration, &existingRanges, &existingPayload)
	if err == nil {
		if existingSchema != artifact.SchemaVersion || existingDuration != artifact.SourceDurationMS || existingRanges != rangesJSON || existingPayload != artifact.PayloadJSON {
			return errors.New("clipping artifact version already exists with different content")
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = tx.Exec(`INSERT INTO clipping_artifacts(job_id,artifact_type,schema_version,version,source_duration_ms,time_ranges_json,payload_json,created_at) VALUES(?,?,?,?,?,?,?,?)`, artifact.JobID, artifact.Type, artifact.SchemaVersion, artifact.Version, artifact.SourceDurationMS, rangesJSON, artifact.PayloadJSON, artifact.CreatedAt)
	return err
}

func (s *Store) FailClippingStageBeforeSubmit(jobID, name, attemptID, leaseToken, reason string) (ClippingStage, error) {
	return s.failClippingStage(jobID, name, attemptID, leaseToken, reason, false, true)
}

func (s *Store) failClippingStage(jobID, name, attemptID, leaseToken, reason string, retryable, knownNoSubmission bool) (ClippingStage, error) {
	if !safeID(jobID) || !strings.HasPrefix(jobID, "clipjob_") || !validStageName(name) || attemptID == "" || leaseToken == "" {
		return ClippingStage{}, ErrClippingStaleAttempt
	}
	now := time.Now().Unix()
	reason = cleanClippingError(reason)
	tx, err := s.db.Begin()
	if err != nil {
		return ClippingStage{}, err
	}
	defer tx.Rollback()
	attempt, err := readClippingAttempt(tx, jobID, name, attemptID)
	if errors.Is(err, sql.ErrNoRows) {
		return ClippingStage{}, ErrClippingStaleAttempt
	}
	if err != nil {
		return ClippingStage{}, err
	}
	if !validClippingLease(leaseToken, attempt.TokenHash) {
		return ClippingStage{}, ErrClippingStaleAttempt
	}
	stage, err := readClippingStage(tx, jobID, name)
	if err != nil {
		return ClippingStage{}, err
	}
	if attempt.SettledAt > 0 {
		// Cancellation can win the race after capability persistence but before
		// the dispatcher starts its HTTP request. The dispatcher can then prove
		// there was no submission; record that proof so the source claim can be
		// released immediately rather than waiting out an unused callback window.
		if knownNoSubmission && attempt.CallbackReceivedAt == 0 && attempt.Status == "canceled" &&
			stage.AttemptID == attemptID && stage.Status == ClippingStageCanceled {
			if _, err := tx.Exec(`UPDATE clipping_stage_attempts SET status='settled_canceled',reconciliation_reference='known_no_submission',error=? WHERE attempt_id=?`, reason, attemptID); err != nil {
				return ClippingStage{}, err
			}
		}
		if err := tx.Commit(); err != nil {
			return ClippingStage{}, err
		}
		return stage, nil
	}
	job, err := scanClippingJob(tx.QueryRow(`SELECT `+clippingJobColumns()+` FROM clipping_jobs WHERE id=?`, jobID))
	if err != nil {
		return ClippingStage{}, err
	}
	current := attempt.Status == "running" && stage.AttemptID == attemptID && stage.Status == ClippingStageRunning && stage.LeaseExpiresAt > now && !clippingJobTerminal(job.Status)
	if err := releaseLegacyClippingHold(tx, &job, attempt, name, now); err != nil {
		return ClippingStage{}, err
	}
	if knownNoSubmission {
		if _, err := tx.Exec(`UPDATE clipping_stage_attempts SET reconciliation_reference='known_no_submission',settled_at=CASE WHEN settled_at=0 THEN ? ELSE settled_at END WHERE attempt_id=?`, now, attemptID); err != nil {
			return ClippingStage{}, err
		}
	}
	if current {
		stageStatus, jobStatus := ClippingStageFailed, ClippingJobFailed
		if retryable {
			stageStatus, jobStatus = ClippingStageQueued, ClippingJobQueued
		}
		if _, err := tx.Exec(`UPDATE clipping_stage_attempts SET status='failed',error=? WHERE attempt_id=?`, reason, attemptID); err != nil {
			return ClippingStage{}, err
		}
		if _, err := tx.Exec(`UPDATE clipping_stages SET status=?,lease_expires_at=0,error=?,updated_at=? WHERE job_id=? AND name=?`, stageStatus, reason, now, jobID, name); err != nil {
			return ClippingStage{}, err
		}
		if _, err := tx.Exec(`UPDATE clipping_jobs SET status=?,error=?,updated_at=? WHERE id=?`, jobStatus, reason, now, jobID); err != nil {
			return ClippingStage{}, err
		}
	} else {
		status := "settled_stale"
		if job.Status == ClippingJobCanceled || stage.Status == ClippingStageCanceled {
			status = "settled_canceled"
		}
		if _, err := tx.Exec(`UPDATE clipping_stage_attempts SET status=?,error=? WHERE attempt_id=?`, status, reason, attemptID); err != nil {
			return ClippingStage{}, err
		}
	}
	deleteClaim := 0
	if knownNoSubmission {
		deleteClaim = 1
	}
	if _, err := tx.Exec(`DELETE FROM clipping_analysis_claims WHERE job_id=? AND attempt_id=? AND ?=1`, jobID, attemptID, deleteClaim); err != nil {
		return ClippingStage{}, err
	}
	if err := tx.Commit(); err != nil {
		return ClippingStage{}, err
	}
	stage, err = scanClippingStage(s.db.QueryRow(`SELECT `+clippingStageColumns()+` FROM clipping_stages WHERE job_id=? AND name=?`, jobID, name))
	return stage, err
}

func (s *Store) CancelClippingJob(id string) error {
	if !safeID(id) || !strings.HasPrefix(id, "clipjob_") {
		return ErrClippingJobNotFound
	}
	now := time.Now().Unix()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	job, err := scanClippingJob(tx.QueryRow(`SELECT `+clippingJobColumns()+` FROM clipping_jobs WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrClippingJobNotFound
	}
	if err != nil {
		return err
	}
	if err := cancelClippingJobTx(tx, &job, now); err != nil {
		return err
	}
	return tx.Commit()
}

func cancelClippingJobTx(tx *sql.Tx, job *ClippingJob, now int64) error {
	if clippingJobTerminal(job.Status) {
		return nil
	}
	rows, err := tx.Query(`SELECT `+clippingStageColumns()+` FROM clipping_stages WHERE job_id=? AND status IN ('queued','running','paused_budget','uncertain')`, job.ID)
	if err != nil {
		return err
	}
	var stages []ClippingStage
	for rows.Next() {
		stage, err := scanClippingStage(rows)
		if err != nil {
			rows.Close()
			return err
		}
		stages = append(stages, stage)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, stage := range stages {
		if stage.AttemptID != "" {
			attempt, err := readClippingAttempt(tx, job.ID, stage.Name, stage.AttemptID)
			if err != nil {
				return err
			}
			if err := releaseLegacyClippingHold(tx, job, attempt, stage.Name, now); err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE clipping_stage_attempts SET status='canceled',error='Canceled',settled_at=CASE WHEN settled_at=0 THEN ? ELSE settled_at END WHERE attempt_id=? AND status IN ('running','expired')`, now, stage.AttemptID); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`UPDATE clipping_stages SET status='canceled',reserved_micro_usd=0,lease_expires_at=0,error='Canceled',updated_at=? WHERE job_id=? AND name=?`, now, job.ID, stage.Name); err != nil {
			return err
		}
	}
	job.Status = ClippingJobCanceled
	if _, err := tx.Exec(`UPDATE clipping_jobs SET status='canceled',reserved_micro_usd=0,error='Canceled',updated_at=? WHERE id=?`, now, job.ID); err != nil {
		return err
	}
	return nil
}

func (s *Store) ClippingArtifacts(jobID string) ([]ClippingArtifact, error) {
	if !safeID(jobID) || !strings.HasPrefix(jobID, "clipjob_") {
		return nil, ErrClippingJobNotFound
	}
	rows, err := s.db.Query(`SELECT artifact_type,schema_version,version,source_duration_ms,time_ranges_json,payload_json,created_at FROM clipping_artifacts WHERE job_id=? ORDER BY artifact_type,version`, jobID)
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(artifacts) == 0 {
		if _, err := s.ClippingJob(jobID); err != nil {
			return nil, err
		}
	}
	return artifacts, nil
}
