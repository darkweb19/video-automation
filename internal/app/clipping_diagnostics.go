package app

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

const (
	ClippingDiagnosticStageURLValidation  = "source_url_validation"
	ClippingDiagnosticStageReservation    = "source_reservation"
	ClippingDiagnosticStageDispatch       = "import_dispatch"
	ClippingDiagnosticStageDownload       = "media_download"
	ClippingDiagnosticStageOutputValidate = "download_output_validation"
	ClippingDiagnosticStageProbe          = "media_probe"
	ClippingDiagnosticStageHash           = "media_hash"
	ClippingDiagnosticStageReady          = "source_ready"
)

var clippingDiagnosticStages = map[string]struct{}{
	ClippingDiagnosticStageURLValidation:  {},
	ClippingDiagnosticStageReservation:    {},
	ClippingDiagnosticStageDispatch:       {},
	ClippingDiagnosticStageDownload:       {},
	ClippingDiagnosticStageOutputValidate: {},
	ClippingDiagnosticStageProbe:          {},
	ClippingDiagnosticStageHash:           {},
	ClippingDiagnosticStageReady:          {},
}

var clippingDiagnosticCodes = map[string]struct{}{
	"invalid_url":               {},
	"unsupported_source":        {},
	"unsafe_destination":        {},
	"runtime_unavailable":       {},
	"extractor_exit_failure":    {},
	"bot_challenge":             {},
	"extractor_output_limit":    {},
	"format_unavailable":        {},
	"av_stream_missing":         {},
	"av_timing_mismatch":        {},
	"video_unavailable":         {},
	"duration_limit_exceeded":   {},
	"size_limit_exceeded":       {},
	"provider_denied_download":  {},
	"non_media_response":        {},
	"partial_response_rejected": {},
	"download_failed":           {},
	"probe_runtime_unavailable": {},
	"probe_timeout":             {},
	"media_invalid":             {},
	"probe_failed":              {},
	"hash_failed":               {},
	"hash_timeout":              {},
	"import_canceled":           {},
	"import_timeout":            {},
	"retry_scheduled":           {},
	"source_unavailable":        {},
	"download_timeout":          {},
}

// AppendClippingSourceDiagnostic persists one safe, monotonic source trace
// item. Old entries are trimmed transactionally while the sequence counter
// remains durable across restarts and trimming.
func (s *Store) AppendClippingSourceDiagnostic(id, stage string, status ClippingSourceDiagnosticStatus, code string) error {
	if s == nil || s.db == nil || !safeClippingSourceID(id) {
		return ErrClippingSourceNotFound
	}
	if _, ok := clippingDiagnosticStages[stage]; !ok {
		return ErrClippingInvalidState
	}
	switch status {
	case ClippingDiagnosticStarted, ClippingDiagnosticCompleted, ClippingDiagnosticFailed, ClippingDiagnosticRetrying, ClippingDiagnosticQueued:
	default:
		return ErrClippingInvalidState
	}
	if code != "" {
		if _, ok := clippingDiagnosticCodes[code]; !ok {
			return ErrClippingInvalidState
		}
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var encoded string
	var sequence int64
	if err := tx.QueryRow(`SELECT diagnostics_json,diagnostics_sequence FROM clipping_sources WHERE id=?`, id).Scan(&encoded, &sequence); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrClippingSourceNotFound
		}
		return err
	}
	if sequence == int64(^uint64(0)>>1) {
		return ErrClippingInvalidState
	}
	items := decodeClippingSourceDiagnostics(encoded)
	sequence++
	item := ClippingSourceDiagnostic{Sequence: sequence, Stage: stage, Status: status, Code: code, At: time.Now().Unix()}
	items = append(items, item)
	if len(items) > MaxClippingSourceDiagnostics {
		items = append([]ClippingSourceDiagnostic(nil), items[len(items)-MaxClippingSourceDiagnostics:]...)
	}
	encodedBytes, err := json.Marshal(items)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE clipping_sources SET diagnostics_json=?,diagnostics_sequence=? WHERE id=?`, string(encodedBytes), sequence, id); err != nil {
		return err
	}
	return tx.Commit()
}

func decodeClippingSourceDiagnostics(encoded string) []ClippingSourceDiagnostic {
	var items []ClippingSourceDiagnostic
	if err := json.Unmarshal([]byte(encoded), &items); err != nil {
		return nil
	}
	safe := make([]ClippingSourceDiagnostic, 0, min(len(items), MaxClippingSourceDiagnostics))
	for _, item := range items {
		if item.Sequence < 1 || item.At < 1 {
			continue
		}
		if _, ok := clippingDiagnosticStages[item.Stage]; !ok {
			continue
		}
		switch item.Status {
		case ClippingDiagnosticStarted, ClippingDiagnosticCompleted, ClippingDiagnosticFailed, ClippingDiagnosticRetrying, ClippingDiagnosticQueued:
		default:
			continue
		}
		if item.Code != "" {
			if _, ok := clippingDiagnosticCodes[item.Code]; !ok {
				continue
			}
		}
		safe = append(safe, item)
	}
	if len(safe) > MaxClippingSourceDiagnostics {
		safe = safe[len(safe)-MaxClippingSourceDiagnostics:]
	}
	return safe
}
