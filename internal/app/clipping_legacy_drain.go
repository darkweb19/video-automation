package app

import (
	"database/sql"
	"math"
)

// ClippingLegacyDrainInventory is a point-in-time inventory of persisted
// legacy work. A zero inventory is not a release or deployment approval: the
// caller must first stop new legacy work and verify the other release gates.
// Counts contain no source URLs, media paths, or bearer capabilities.
type ClippingLegacyDrainInventory struct {
	ObservedAt                 int64 `json:"observed_at"`
	PendingImports             int64 `json:"pending_imports"`
	ActiveImportLeases         int64 `json:"active_import_leases"`
	UnexpiredCallbackWindows   int64 `json:"unexpired_callback_windows"`
	UnknownCallbackExpiry      int64 `json:"unknown_callback_expiry"`
	UnresolvedClaims           int64 `json:"unresolved_claims"`
	InconsistentCapabilityRows int64 `json:"inconsistent_capability_rows"`
}

// LegacyClippingDrainInventory reads one SQLite snapshot. It never cancels,
// settles, or deletes work. Callers must treat a query error, unknown expiry,
// or inconsistent capability record as a failed drain check.
func (s *Store) LegacyClippingDrainInventory(now int64) (ClippingLegacyDrainInventory, error) {
	if s == nil || s.db == nil || now <= 0 {
		return ClippingLegacyDrainInventory{}, ErrClippingInvalidState
	}
	tx, err := s.db.Begin()
	if err != nil {
		return ClippingLegacyDrainInventory{}, err
	}
	defer tx.Rollback()

	inventory := ClippingLegacyDrainInventory{ObservedAt: now}
	if err := tx.QueryRow(`SELECT
		COALESCE(SUM(CASE WHEN status='importing' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN import_lease_until>? THEN 1 ELSE 0 END),0)
		FROM clipping_sources WHERE kind<>'youtube_url'`, now).
		Scan(&inventory.PendingImports, &inventory.ActiveImportLeases); err != nil {
		return ClippingLegacyDrainInventory{}, err
	}
	// Claims are legacy-only. Count every row, including an orphan or a claim
	// attached to a terminal job; recovery, not this inventory, owns release.
	if err := tx.QueryRow(`SELECT COUNT(*) FROM clipping_analysis_claims`).Scan(&inventory.UnresolvedClaims); err != nil {
		return ClippingLegacyDrainInventory{}, err
	}

	rows, err := tx.Query(`SELECT attempt.lease_expires_at,expiry.expires_at
		FROM clipping_stage_attempts AS attempt
		JOIN clipping_jobs AS job ON job.id=attempt.job_id AND job.provider='modal_legacy'
		LEFT JOIN clipping_capability_expiry_index AS expiry
			ON expiry.job_id=attempt.job_id AND expiry.attempt_id=attempt.attempt_id`)
	if err != nil {
		return ClippingLegacyDrainInventory{}, err
	}
	for rows.Next() {
		var lease int64
		var issued sql.NullInt64
		if err := rows.Scan(&lease, &issued); err != nil {
			rows.Close()
			return ClippingLegacyDrainInventory{}, err
		}
		var expiry int64
		if issued.Valid {
			expiry = issued.Int64
		} else if lease > 0 && lease <= math.MaxInt64-int64(clippingCallbackCapabilityGrace.Seconds()) {
			// Older attempts may lack an index. Source retention and job status
			// can change after issuance, so neither may shorten this bound.
			expiry = lease + int64(clippingCallbackCapabilityGrace.Seconds())
		}
		if expiry <= 0 {
			inventory.UnknownCallbackExpiry++
		} else if expiry > now {
			inventory.UnexpiredCallbackWindows++
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return ClippingLegacyDrainInventory{}, err
	}
	if err := rows.Close(); err != nil {
		return ClippingLegacyDrainInventory{}, err
	}

	// An orphaned attempt or index cannot establish that every capability is
	// accounted for. Count such records instead of silently omitting them.
	if err := tx.QueryRow(`SELECT COUNT(*) FROM clipping_stage_attempts AS attempt
		LEFT JOIN clipping_jobs AS job ON job.id=attempt.job_id
		WHERE job.id IS NULL OR job.provider IS NULL OR job.provider NOT IN ('modal_legacy','gemini')`).Scan(&inventory.InconsistentCapabilityRows); err != nil {
		return ClippingLegacyDrainInventory{}, err
	}
	var orphanedIndexes int64
	if err := tx.QueryRow(`SELECT COUNT(*) FROM clipping_capability_expiry_index AS expiry
		LEFT JOIN clipping_stage_attempts AS attempt ON attempt.job_id=expiry.job_id AND attempt.attempt_id=expiry.attempt_id
		LEFT JOIN clipping_jobs AS job ON job.id=expiry.job_id
		WHERE attempt.attempt_id IS NULL OR job.id IS NULL OR job.provider IS NULL OR job.provider<>'modal_legacy'`).Scan(&orphanedIndexes); err != nil {
		return ClippingLegacyDrainInventory{}, err
	}
	inventory.InconsistentCapabilityRows += orphanedIndexes

	// Inspect keys only, never encrypted values. A callback setting without a
	// matching legacy attempt could otherwise escape both expiry calculations.
	var orphanedSettings int64
	if err := tx.QueryRow(`SELECT COUNT(*) FROM settings AS setting
		WHERE substr(setting.key,1,13)='clipping_cap_'
		AND (substr(setting.key,-9)='_callback' OR substr(setting.key,-16)='_callback_expiry')
		AND NOT EXISTS (
			SELECT 1 FROM clipping_stage_attempts AS attempt
			JOIN clipping_jobs AS job ON job.id=attempt.job_id AND job.provider='modal_legacy'
			WHERE setting.key IN (
				'clipping_cap_' || attempt.job_id || '_' || attempt.attempt_id || '_callback',
				'clipping_cap_' || attempt.job_id || '_' || attempt.attempt_id || '_callback_expiry'))`).Scan(&orphanedSettings); err != nil {
		return ClippingLegacyDrainInventory{}, err
	}
	inventory.InconsistentCapabilityRows += orphanedSettings
	if err := tx.Commit(); err != nil {
		return ClippingLegacyDrainInventory{}, err
	}
	return inventory, nil
}
