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
	ErrClippingLegacyRetired       = fmt.Errorf("%w: legacy clipping has been retired", ErrClippingInvalidState)
	errClippingRetentionDeferred   = errors.New("clipping source retention deferred")
	ErrClippingStaleAttempt        = errors.New("clipping stage attempt is stale")
	ErrClippingJobTerminal         = errors.New("clipping job is terminal")
	ErrClippingInvalidArtifact     = errors.New("invalid clipping artifact")
)

const maxClippingArtifactPayloadBytes = 4 << 20

var clippingStageNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,40}$`)
var clippingArtifactTypePattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,127}$`)

func (s *Store) migrateClipping() error {
	var existingTables int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name IN ('clipping_sources','clipping_jobs')`).Scan(&existingTables); err != nil {
		return err
	}
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS clipping_sources ` + clippingSourcesTableDefinition + `;
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
		CREATE TABLE IF NOT EXISTS clipping_jobs ` + clippingJobsTableDefinition + `;
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
			"provider":         "TEXT NOT NULL DEFAULT 'modal_legacy' CHECK(provider IN ('modal_legacy','gemini'))",
			"content_type":     "TEXT NOT NULL DEFAULT 'general' CHECK(content_type IN ('general','podcast','comedy','gaming','movie'))",
			"min_clip_seconds": "INTEGER NOT NULL DEFAULT 15 CHECK(min_clip_seconds BETWEEN 15 AND 180)",
			"max_clip_seconds": "INTEGER NOT NULL DEFAULT 180 CHECK(max_clip_seconds BETWEEN 15 AND 180)",
			"candidate_limit":  "INTEGER NOT NULL DEFAULT 10 CHECK(candidate_limit BETWEEN 7 AND 10)",
			"analysis_deleted": "INTEGER NOT NULL DEFAULT 0 CHECK(analysis_deleted IN (0,1))",
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
	if err := s.migrateClippingReferenceSchema(existingTables != 0); err != nil {
		return fmt.Errorf("migrate clipping reference schema: %w", err)
	}
	if err := s.migrateClippingGeminiRequests(); err != nil {
		return fmt.Errorf("initialize Gemini request snapshots: %w", err)
	}
	if err := s.migrateClippingGeminiCache(); err != nil {
		return fmt.Errorf("initialize Gemini analysis cache: %w", err)
	}
	if err := s.migrateClippingGeminiLifecycle(); err != nil {
		return fmt.Errorf("initialize Gemini attempt lifecycle: %w", err)
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
		JOIN clipping_jobs AS job ON job.id=attempt.job_id AND job.provider='modal_legacy'
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
			WHERE job.provider='modal_legacy' AND stage.status='paused_budget' AND job.status='paused_budget' AND stage.attempt_id=attempt.attempt_id
		)`); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE clipping_stages SET status='uncertain',error='Worker dispatch outcome is unknown; waiting for its callback window',updated_at=?
		WHERE status='paused_budget' AND EXISTS(SELECT 1 FROM clipping_jobs WHERE id=clipping_stages.job_id AND provider='modal_legacy') AND EXISTS(
			SELECT 1 FROM clipping_stage_attempts AS attempt WHERE attempt.job_id=clipping_stages.job_id
			AND attempt.stage_name=clipping_stages.name AND attempt.attempt_id=clipping_stages.attempt_id
			AND attempt.status='expired' AND attempt.callback_received_at=0 AND attempt.settled_at=0
		)`, now); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE clipping_stages SET status='completed',lease_expires_at=0,error='',updated_at=?
		WHERE status='paused_budget' AND EXISTS(SELECT 1 FROM clipping_jobs WHERE id=clipping_stages.job_id AND provider='modal_legacy') AND EXISTS(
			SELECT 1 FROM clipping_stage_attempts AS attempt WHERE attempt.job_id=clipping_stages.job_id
			AND attempt.stage_name=clipping_stages.name AND attempt.attempt_id=clipping_stages.attempt_id
			AND attempt.status='completed' AND attempt.callback_received_at>0
		) AND EXISTS(SELECT 1 FROM clipping_artifacts AS artifact WHERE artifact.job_id=clipping_stages.job_id)`, now); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE clipping_stages SET status='failed',lease_expires_at=0,error='Worker reported a failure',updated_at=?
		WHERE status='paused_budget' AND EXISTS(SELECT 1 FROM clipping_jobs WHERE id=clipping_stages.job_id AND provider='modal_legacy') AND EXISTS(
			SELECT 1 FROM clipping_stage_attempts AS attempt WHERE attempt.job_id=clipping_stages.job_id
			AND attempt.stage_name=clipping_stages.name AND attempt.attempt_id=clipping_stages.attempt_id
			AND attempt.status='failed' AND attempt.callback_received_at>0
		)`, now); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE clipping_stages SET status='queued',reserved_micro_usd=0,lease_expires_at=0,error='',updated_at=?
		WHERE status='paused_budget' AND EXISTS(SELECT 1 FROM clipping_jobs WHERE id=clipping_stages.job_id AND provider='modal_legacy') AND NOT EXISTS(
			SELECT 1 FROM clipping_stage_attempts AS attempt WHERE attempt.job_id=clipping_stages.job_id
			AND attempt.stage_name=clipping_stages.name AND attempt.attempt_id=clipping_stages.attempt_id
			AND attempt.status IN ('running','expired') AND attempt.callback_received_at=0 AND attempt.settled_at=0
		)`, now); err != nil {
		return err
	}
	rows, err = tx.Query(`SELECT id FROM clipping_jobs WHERE provider='modal_legacy' AND status='paused_budget'`)
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

// The issued index is authoritative when present. Unknown or corrupt indexed
// expiries and pre-index leases must never be treated as elapsed.
const clippingElapsedCallbackCondition = `(EXISTS(SELECT 1 FROM clipping_capability_expiry_index AS expiry
 WHERE expiry.job_id=attempt.job_id AND expiry.attempt_id=attempt.attempt_id
 AND typeof(expiry.expires_at)='integer' AND expiry.expires_at>0 AND expiry.expires_at<=?)
 OR (NOT EXISTS(SELECT 1 FROM clipping_capability_expiry_index AS expiry
 WHERE expiry.job_id=attempt.job_id AND expiry.attempt_id=attempt.attempt_id)
 AND typeof(attempt.lease_expires_at)='integer' AND attempt.lease_expires_at>0
 AND attempt.lease_expires_at<=? AND attempt.lease_expires_at<=?))`

func clippingElapsedCallbackArgs(now int64) (int64, int64, int64) {
	grace := int64(clippingCallbackCapabilityGrace.Seconds())
	return now, math.MaxInt64 - grace, now - grace
}

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
		WHERE typeof(expiry.expires_at)='integer' AND expiry.expires_at>0 AND expiry.expires_at<=? AND EXISTS(
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
	if _, err := tx.Exec(`DELETE FROM clipping_capability_expiry_index AS expiry WHERE typeof(expiry.expires_at)='integer' AND expiry.expires_at>0 AND expiry.expires_at<=?
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
	var storagePath, sha256 sql.NullString
	var declaredSize, size, duration sql.NullInt64
	var cleanupComplete int
	var rightsAttested int
	var diagnosticsJSON string
	var diagnosticsSequence int64
	err := row.Scan(&source.ID, &source.Kind, &source.Status, &source.OriginalName, &source.MediaType, &source.SourceURL, &rightsAttested, &storagePath, &source.Failure,
		&declaredSize, &source.ReservedSizeBytes, &size, &source.UploadOffset, &duration, &sha256, &source.ImportExpectation, &source.ImportExpectedSize, &source.ImportExpectedSHA, &source.RetainUntil,
		&source.ImportAttempts, &source.NextImportAt, &source.ImportLeaseUntil, &source.MediaLeaseUntil, &cleanupComplete, &diagnosticsJSON, &diagnosticsSequence, &source.CreatedAt, &source.UpdatedAt, &source.DeletedAt)
	source.StoragePath, source.SHA256 = storagePath.String, sha256.String
	source.DeclaredSizeBytes, source.SizeBytes, source.DurationMS = declaredSize.Int64, size.Int64, duration.Int64
	source.CleanupComplete = cleanupComplete != 0
	source.RightsAttested = rightsAttested != 0
	source.Diagnostics = decodeClippingSourceDiagnostics(diagnosticsJSON)
	return source, err
}

func (s *Store) CreateClippingSource(input ClippingSourceCreate) (ClippingSource, error) {
	return ClippingSource{}, ErrClippingLegacyRetired
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
	var kind ClippingSourceKind
	var path sql.NullString
	if err := s.db.QueryRow(`SELECT kind,storage_path FROM clipping_sources WHERE id=?`, id).Scan(&kind, &path); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrClippingSourceNotFound
		}
		return "", err
	}
	if kind == ClippingSourceYouTubeURL || !path.Valid {
		return "", fmt.Errorf("%w: reference has no local media", ErrClippingInvalidState)
	}
	expected := clippingStoragePath(s.clippingDir, id)
	if filepath.Clean(path.String) != filepath.Clean(expected) {
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

func (s *Store) MarkClippingSourceReady(id string, sizeBytes, durationMS int64, sha256Hex string) error {
	return ErrClippingLegacyRetired
}

func (s *Store) BeginClippingImport(id string, expectedAttempts int, leaseFor time.Duration) (ClippingSource, error) {
	return ClippingSource{}, ErrClippingLegacyRetired
}

func (s *Store) RetryClippingImport(id string, expectedAttempts int, nextAt int64, reason string) error {
	return ErrClippingLegacyRetired
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
	var stored any
	err := tx.QueryRow(`SELECT expires_at FROM clipping_capability_expiry_index WHERE job_id=? AND attempt_id=?`, attempt.JobID, attempt.AttemptID).Scan(&stored)
	if err == nil {
		expiresAt, ok := stored.(int64)
		if !ok || expiresAt <= 0 {
			return 0, ErrClippingInvalidState
		}
		return expiresAt, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	expiresAt := clippingConservativeCallbackExpiry(attempt, source)
	if expiresAt <= 0 {
		return 0, ErrClippingInvalidState
	}
	return expiresAt, nil
}

func clippingSourceHasActiveCallbackWindowTx(tx *sql.Tx, source ClippingSource, now int64) (bool, error) {
	rows, err := tx.Query(`SELECT attempt.job_id,attempt.stage_name,attempt.attempt_id
		FROM clipping_stage_attempts AS attempt
		JOIN clipping_jobs AS job ON job.id=attempt.job_id
		WHERE job.source_id=? AND job.provider='modal_legacy' ORDER BY attempt.started_at,attempt.attempt_id`, source.ID)
	if err != nil {
		return false, err
	}
	type attemptKey struct {
		jobID, stageName, attemptID string
	}
	keys := make([]attemptKey, 0)
	for rows.Next() {
		var key attemptKey
		if err := rows.Scan(&key.jobID, &key.stageName, &key.attemptID); err != nil {
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
		attempt, err := readClippingAttempt(tx, key.jobID, key.stageName, key.attemptID)
		if err != nil {
			return false, err
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

// DeleteClippingSource removes user-visible source media and analysis data,
// cancels any current analysis, and retains the job/attempt audit rows. A late
// callback remains authenticated but cannot publish against the terminal job.
func (s *Store) DeleteClippingSource(id string) ([]string, error) {
	if !safeClippingSourceID(id) {
		return nil, ErrClippingSourceNotFound
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := scanClippingSource(tx.QueryRow(`SELECT `+clippingSourceColumns()+` FROM clipping_sources WHERE id=?`, id)); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrClippingSourceNotFound
		}
		return nil, err
	}
	rows, err := tx.Query(`SELECT `+clippingJobColumns()+` FROM clipping_jobs WHERE source_id=? ORDER BY created_at,id`, id)
	if err != nil {
		return nil, err
	}
	jobs := make([]ClippingJob, 0)
	for rows.Next() {
		job, err := scanClippingJob(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	jobIDs := make([]string, 0, len(jobs))
	for index := range jobs {
		job := &jobs[index]
		jobIDs = append(jobIDs, job.ID)
		if !clippingJobTerminal(job.Status) {
			if err := cancelClippingJobTx(tx, job, now); err != nil {
				return nil, err
			}
		}
	}
	if _, err := tx.Exec(`UPDATE clipping_jobs SET analysis_deleted=1,updated_at=? WHERE source_id=?`, now, id); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM clipping_analysis_claims WHERE source_id=?`, id); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM clipping_artifacts WHERE job_id IN (SELECT id FROM clipping_jobs WHERE source_id=?)`, id); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM clipping_gemini_cache WHERE source_id=?`, id); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM clipping_analysis_cache WHERE source_id=?`, id); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE clipping_sources SET status='deleted',source_url='',deleted_at=CASE WHEN deleted_at=0 THEN ? ELSE deleted_at END,updated_at=?,cleanup_complete=0,next_import_at=0,import_lease_until=0,media_lease_until=0 WHERE id=?`, now, now, id); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.clippingGeminiCancellationRegistry().cancelJobs(jobIDs)
	return jobIDs, nil
}

// FailQueuedClippingJob records a terminal local failure before a worker
// attempt is claimed. It cannot change a job that another worker has started.
func (s *Store) FailQueuedClippingJob(jobID, reason string) (bool, error) {
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
	if job.Provider != ClippingProviderModalLegacy {
		return false, ErrClippingInvalidState
	}
	if job.Status != ClippingJobQueued || job.AnalysisDeleted {
		return false, nil
	}
	stage, err := readClippingStage(tx, jobID, ClippingStageAnalysis)
	if err != nil || stage.Status != ClippingStageQueued || stage.AttemptID != "" {
		return false, err
	}
	reason = cleanClippingError(reason)
	if reason == "" {
		reason = "Analysis could not be started"
	}
	if _, err := tx.Exec(`UPDATE clipping_stages SET status='failed',error=?,updated_at=? WHERE job_id=? AND name=? AND status='queued' AND attempt_id=''`, reason, now, jobID, ClippingStageAnalysis); err != nil {
		return false, err
	}
	if _, err := tx.Exec(`UPDATE clipping_jobs SET status='failed',error=?,updated_at=? WHERE id=? AND status='queued'`, reason, now, jobID); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
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
		return fmt.Errorf("%w: retention period has not expired", errClippingRetentionDeferred)
	}
	if automatic && importLease > now {
		return fmt.Errorf("%w: active import lease", errClippingRetentionDeferred)
	}
	if mediaLease > now {
		return fmt.Errorf("%w: active media lease", errClippingRetentionDeferred)
	}
	var active int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM clipping_jobs WHERE source_id=? AND status IN ('queued','running')`, id).Scan(&active); err != nil {
		return err
	}
	if active != 0 {
		return fmt.Errorf("%w: referenced by an active job", errClippingRetentionDeferred)
	}
	source, err := scanClippingSource(tx.QueryRow(`SELECT `+clippingSourceColumns()+` FROM clipping_sources WHERE id=?`, id))
	if err != nil {
		return err
	}
	if automatic {
		callbackActive, err := clippingSourceHasActiveCallbackWindowTx(tx, source, now)
		if err != nil {
			return err
		}
		if callbackActive {
			return fmt.Errorf("%w: worker callback window remains open", errClippingRetentionDeferred)
		}
	}
	// Source-derived artifacts can contain transcripts and analysis content;
	// erase them with the media while keeping job, stage, and cost audit rows.
	if _, err := tx.Exec(`DELETE FROM clipping_analysis_claims WHERE source_id=?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM clipping_artifacts WHERE job_id IN (SELECT id FROM clipping_jobs WHERE source_id=?)`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM clipping_gemini_cache WHERE source_id=?`, id); err != nil {
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
	pageSize := limit * 5
	if pageSize > 500 {
		pageSize = 500
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	sources := make([]ClippingSource, 0, limit)
	// Continue past protected or malformed legacy records; one unresolved
	// source must not prevent normal retention of unrelated sources.
	for offset := 0; ; offset += pageSize {
		rows, err := tx.Query(`SELECT `+clippingSourceColumns()+` FROM clipping_sources AS src WHERE
   (src.status='deleted' AND src.cleanup_complete=0) OR
   (src.status<>'deleted' AND src.retain_until>0 AND src.retain_until<=? AND src.media_lease_until<=? AND src.import_lease_until<=? AND NOT EXISTS (
    SELECT 1 FROM clipping_jobs AS job WHERE job.source_id=src.id AND job.status IN ('queued','running')
   )) ORDER BY CASE WHEN src.status='deleted' THEN 0 ELSE 1 END,src.retain_until,src.created_at,src.id LIMIT ? OFFSET ?`, now, now, now, pageSize, offset)
		if err != nil {
			return nil, err
		}
		candidates := make([]ClippingSource, 0, pageSize)
		for rows.Next() {
			source, err := scanClippingSource(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			candidates = append(candidates, source)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		for _, source := range candidates {
			if source.Status != ClippingSourceDeleted {
				blocked, err := clippingSourceHasActiveCallbackWindowTx(tx, source, now)
				if errors.Is(err, ErrClippingInvalidState) {
					continue
				}
				if err != nil {
					return nil, err
				}
				if blocked {
					continue
				}
			}
			sources = append(sources, source)
			if len(sources) == limit {
				break
			}
		}
		if len(sources) == limit || len(candidates) < pageSize {
			break
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
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
	return `id,batch_id,source_id,provider,content_type,min_clip_seconds,max_clip_seconds,candidate_limit,status,budget_limit_micro_usd,reserved_micro_usd,spent_micro_usd,analysis_deleted,error,created_at,updated_at`
}

func scanClippingJob(row clippingRowScanner) (ClippingJob, error) {
	var job ClippingJob
	var minSeconds, maxSeconds sql.NullInt64
	var analysisDeleted int
	err := row.Scan(&job.ID, &job.BatchID, &job.SourceID, &job.Provider, &job.ContentType, &minSeconds, &maxSeconds, &job.CandidateLimit, &job.Status, &job.BudgetLimitMicroUSD, &job.ReservedMicroUSD, &job.SpentMicroUSD, &analysisDeleted, &job.Error, &job.CreatedAt, &job.UpdatedAt)
	job.MinClipSeconds, job.MaxClipSeconds = int(minSeconds.Int64), int(maxSeconds.Int64)
	job.AnalysisDeleted = analysisDeleted != 0
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

// CreateClippingJob is retained as an explicit fence for old callers.
func (s *Store) CreateClippingJob(input ClippingJobCreate, idempotencyKey string) (ClippingJob, bool, error) {
	return ClippingJob{}, false, ErrClippingLegacyRetired
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
	return ClippingStage{}, false, ErrClippingLegacyRetired
}

// RecoverExpiredClippingStages moves expired dispatches into an uncertain state
// while the issued callback capability remains valid. It never redispatches an
// unknown submission automatically.
func (s *Store) RecoverExpiredClippingStages(now int64) error {
	if now <= 0 {
		now = time.Now().Unix()
	}
	elapsedNow, maxLease, preIndexDeadline := clippingElapsedCallbackArgs(now)
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT stage.job_id,stage.name,stage.attempt_id FROM clipping_stages AS stage
		WHERE EXISTS(SELECT 1 FROM clipping_jobs WHERE id=stage.job_id AND provider='modal_legacy') AND stage.status='running' AND stage.attempt_id<>'' AND (
			stage.lease_expires_at<=? OR EXISTS(
				SELECT 1 FROM clipping_stage_attempts AS attempt
				WHERE attempt.job_id=stage.job_id AND attempt.stage_name=stage.name AND attempt.attempt_id=stage.attempt_id
				AND attempt.status IN ('running','expired') AND attempt.callback_received_at=0
				AND `+clippingElapsedCallbackCondition+`))`, now, elapsedNow, maxLease, preIndexDeadline)
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

	// Release the hold and claim only when an issued or conservative expiry
	// is known to have elapsed. Invalid or missing evidence remains unresolved.
	rows, err = tx.Query(`SELECT attempt.job_id,attempt.stage_name,attempt.attempt_id
		FROM clipping_stage_attempts AS attempt
		JOIN clipping_stages AS stage ON stage.job_id=attempt.job_id AND stage.name=attempt.stage_name
		WHERE EXISTS(SELECT 1 FROM clipping_jobs WHERE id=stage.job_id AND provider='modal_legacy') AND stage.status='uncertain' AND stage.attempt_id=attempt.attempt_id
		AND attempt.callback_received_at=0 AND attempt.status='expired'
		AND `+clippingElapsedCallbackCondition, elapsedNow, maxLease, preIndexDeadline)
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
		if _, err := tx.Exec(`UPDATE clipping_stages SET status='failed',lease_expires_at=0,error='Worker callback window expired; legacy analysis is retired',updated_at=? WHERE job_id=? AND name=? AND attempt_id=? AND status='uncertain'`, now, item.jobID, item.name, item.attemptID); err != nil {
			return err
		}
		if !clippingJobTerminal(job.Status) {
			if _, err := tx.Exec(`UPDATE clipping_jobs SET status='failed',error='Worker callback window expired; legacy analysis is retired',updated_at=? WHERE id=?`, now, item.jobID); err != nil {
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
	// User cancellation can leave an authenticated callback window open. Once
	// that exact capability expires, release the source claim without changing
	// the terminal job or making another inference attempt.
	rows, err = tx.Query(`SELECT attempt.job_id,attempt.stage_name,attempt.attempt_id
		FROM clipping_stage_attempts AS attempt
		JOIN clipping_stages AS stage ON stage.job_id=attempt.job_id AND stage.name=attempt.stage_name
		WHERE EXISTS(SELECT 1 FROM clipping_jobs WHERE id=stage.job_id AND provider='modal_legacy') AND stage.status='canceled' AND stage.attempt_id=attempt.attempt_id
		AND attempt.status='canceled' AND attempt.callback_received_at=0
		AND attempt.reconciliation_reference<>'known_no_submission'
		AND `+clippingElapsedCallbackCondition+``, elapsedNow, maxLease, preIndexDeadline)
	if err != nil {
		return err
	}
	var canceledDue []expiredStage
	for rows.Next() {
		var item expiredStage
		if err := rows.Scan(&item.jobID, &item.name, &item.attemptID); err != nil {
			rows.Close()
			return err
		}
		canceledDue = append(canceledDue, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range canceledDue {
		if _, err := tx.Exec(`UPDATE clipping_stage_attempts SET status='settled_canceled',reconciliation_reference='callback_window_expired',error='callback window expired' WHERE attempt_id=? AND status='canceled' AND callback_received_at=0`, item.attemptID); err != nil {
			return err
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
	// Failed submitted attempts keep their callback and source claim until the
	// capability window closes. Settle them then so a later explicit Analyze
	// action can safely claim the source without repeating uncertain work.
	rows, err = tx.Query(`SELECT attempt.job_id,attempt.stage_name,attempt.attempt_id
		FROM clipping_stage_attempts AS attempt
		JOIN clipping_stages AS stage ON stage.job_id=attempt.job_id AND stage.name=attempt.stage_name
		WHERE EXISTS(SELECT 1 FROM clipping_jobs WHERE id=stage.job_id AND provider='modal_legacy') AND stage.status='failed' AND stage.attempt_id=attempt.attempt_id
		AND attempt.status='failed' AND attempt.callback_received_at=0
		AND attempt.reconciliation_reference NOT IN ('known_no_submission','callback_window_expired')
		AND `+clippingElapsedCallbackCondition+``, elapsedNow, maxLease, preIndexDeadline)
	if err != nil {
		return err
	}
	var failedDue []expiredStage
	for rows.Next() {
		var item expiredStage
		if err := rows.Scan(&item.jobID, &item.name, &item.attemptID); err != nil {
			rows.Close()
			return err
		}
		failedDue = append(failedDue, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range failedDue {
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
		if _, err := tx.Exec(`UPDATE clipping_stage_attempts SET reconciliation_reference='callback_window_expired',error='callback window expired',settled_at=CASE WHEN settled_at=0 THEN ? ELSE settled_at END WHERE attempt_id=? AND status='failed'`, now, item.attemptID); err != nil {
			return err
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
	elapsedNow, maxLease, preIndexDeadline := clippingElapsedCallbackArgs(now)
	rows, err := s.db.Query(`SELECT `+clippingStageColumns()+` FROM clipping_stages AS stage
		WHERE EXISTS(SELECT 1 FROM clipping_jobs WHERE id=stage.job_id AND provider='modal_legacy') AND ((stage.status='running' AND (stage.lease_expires_at<=? OR EXISTS(
			SELECT 1 FROM clipping_stage_attempts AS attempt
			WHERE attempt.job_id=stage.job_id AND attempt.stage_name=stage.name AND attempt.attempt_id=stage.attempt_id
			AND attempt.callback_received_at=0 AND attempt.status IN ('running','expired')
			AND `+clippingElapsedCallbackCondition+`))) OR
		(stage.status='uncertain' AND EXISTS(
			SELECT 1 FROM clipping_stage_attempts AS attempt
			WHERE attempt.attempt_id=stage.attempt_id AND attempt.callback_received_at=0 AND attempt.status='expired'
			AND `+clippingElapsedCallbackCondition+`)))
		ORDER BY stage.lease_expires_at,stage.job_id,stage.name LIMIT ?`,
		now, elapsedNow, maxLease, preIndexDeadline, elapsedNow, maxLease, preIndexDeadline, limit)
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
	if err := requireLegacyClippingJobTx(tx, jobID); err != nil {
		return ErrClippingInvalidState
	}
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
	return ClippingStage{}, ErrClippingLegacyRetired
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

// NoteClippingStageSubmissionUncertain records a missing dispatch
// acknowledgement without revoking a potentially accepted worker's lease.
// The attempt stays running, its source claim and media capability remain
// valid, and the existing callback/recovery paths settle it exactly once.
// A genuine cancellation or already settled attempt cannot be resurrected.
func (s *Store) NoteClippingStageSubmissionUncertain(jobID, name, attemptID, leaseToken string) (ClippingStage, error) {
	if !safeID(jobID) || !strings.HasPrefix(jobID, "clipjob_") || !validStageName(name) || !safeID(attemptID) || leaseToken == "" {
		return ClippingStage{}, ErrClippingStaleAttempt
	}
	now := time.Now().Unix()
	tx, err := s.db.Begin()
	if err != nil {
		return ClippingStage{}, err
	}
	defer tx.Rollback()
	if err := requireLegacyClippingJobTx(tx, jobID); err != nil {
		return ClippingStage{}, ErrClippingInvalidState
	}
	attempt, err := readClippingAttempt(tx, jobID, name, attemptID)
	if errors.Is(err, sql.ErrNoRows) {
		return ClippingStage{}, ErrClippingStaleAttempt
	}
	if err != nil {
		return ClippingStage{}, err
	}
	stage, err := readClippingStage(tx, jobID, name)
	if err != nil {
		return ClippingStage{}, err
	}
	job, err := scanClippingJob(tx.QueryRow(`SELECT `+clippingJobColumns()+` FROM clipping_jobs WHERE id=?`, jobID))
	if err != nil {
		return ClippingStage{}, err
	}
	if !validClippingLease(leaseToken, attempt.TokenHash) ||
		attempt.Status != "running" || attempt.SettledAt != 0 ||
		attempt.CallbackReceivedAt != 0 || attempt.LeaseExpiresAt <= now ||
		stage.AttemptID != attemptID || stage.Status != ClippingStageRunning ||
		stage.LeaseExpiresAt <= now || stage.LeaseExpiresAt != attempt.LeaseExpiresAt ||
		job.Status != ClippingJobRunning {
		return ClippingStage{}, ErrClippingStaleAttempt
	}

	// Only the stage note changes. Never clear lease_expires_at, modify
	// attempt status, release the source claim, or alter callback credentials.
	const note = "Worker dispatch acknowledgement is uncertain; awaiting callback"
	result, err := tx.Exec(`UPDATE clipping_stages SET error=?,updated_at=?
		WHERE job_id=? AND name=? AND attempt_id=? AND status='running' AND lease_expires_at>?`,
		note, now, jobID, name, attemptID, now)
	if err != nil {
		return ClippingStage{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return ClippingStage{}, err
	}
	if count != 1 {
		return ClippingStage{}, ErrClippingStaleAttempt
	}
	if err := tx.Commit(); err != nil {
		return ClippingStage{}, err
	}
	stage.Error = note
	stage.UpdatedAt = now
	return stage, nil
}

func (s *Store) FailClippingStageBeforeSubmit(jobID, name, attemptID, leaseToken, reason string) (ClippingStage, error) {
	return s.failClippingStage(jobID, name, attemptID, leaseToken, reason, false, true)
}

// FailClippingStageAfterSubmit makes a current submitted attempt visibly
// terminal while retaining its source claim and callback window. A late
// callback can settle as stale, and a manual retry remains blocked until the
// original callback capability expires.
func (s *Store) FailClippingStageAfterSubmit(jobID, name, attemptID, leaseToken, reason string) (ClippingStage, error) {
	return s.failClippingStage(jobID, name, attemptID, leaseToken, reason, false, false)
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
	if err := requireLegacyClippingJobTx(tx, jobID); err != nil {
		return ClippingStage{}, ErrClippingInvalidState
	}
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
	if err := tx.Commit(); err != nil {
		return err
	}
	if job.Provider == ClippingProviderGemini {
		s.clippingGeminiCancellationRegistry().cancelJob(id)
	}
	return nil
}

func cancelClippingJobTx(tx *sql.Tx, job *ClippingJob, now int64) error {
	if clippingJobTerminal(job.Status) {
		return nil
	}
	if job.Provider == ClippingProviderGemini {
		return cancelClippingGeminiJobTx(tx, job, now)
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

// Legacy worker operations must never claim or mutate another provider's job.
func requireLegacyClippingJobTx(tx *sql.Tx, jobID string) error {
	var provider ClippingAnalysisProvider
	if err := tx.QueryRow(`SELECT provider FROM clipping_jobs WHERE id=?`, jobID).Scan(&provider); err != nil {
		return err
	}
	if provider != ClippingProviderModalLegacy {
		return ErrClippingInvalidState
	}
	return nil
}
