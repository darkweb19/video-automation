package app

import (
	"context"
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

type clippingDiagnosticError struct {
	code string
	err  error
}

func (e clippingDiagnosticError) Error() string { return e.err.Error() }
func (e clippingDiagnosticError) Unwrap() error { return e.err }

func withClippingDiagnosticCode(err error, code string) error {
	if err == nil {
		return nil
	}
	if _, ok := clippingDiagnosticCodes[code]; !ok {
		code = "download_failed"
	}
	return clippingDiagnosticError{code: code, err: err}
}

func clippingDiagnosticCode(stage string, err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, errClippingYouTubeAVStreamMissing) {
		return "av_stream_missing"
	}
	if errors.Is(err, errClippingYouTubeAVTimingMismatch) {
		return "av_timing_mismatch"
	}
	var coded clippingDiagnosticError
	if errors.As(err, &coded) {
		return coded.code
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		if errors.Is(err, context.Canceled) {
			return "import_canceled"
		}
		switch stage {
		case ClippingDiagnosticStageProbe:
			return "probe_timeout"
		case ClippingDiagnosticStageHash:
			return "hash_timeout"
		case ClippingDiagnosticStageDownload:
			return "download_timeout"
		default:
			return "import_timeout"
		}
	}
	if errors.Is(err, errClippingDurationLimitExceeded) {
		return "duration_limit_exceeded"
	}
	if errors.Is(err, errClippingInvalidMedia) {
		return "media_invalid"
	}
	if stage == ClippingDiagnosticStageProbe {
		if errors.Is(err, errClippingProbeUnavailable) {
			return "probe_runtime_unavailable"
		}
		return "probe_failed"
	}
	if stage == ClippingDiagnosticStageHash {
		return "hash_failed"
	}
	if stage == ClippingDiagnosticStageReady || stage == ClippingDiagnosticStageReservation {
		return "source_unavailable"
	}
	if stage == ClippingDiagnosticStageURLValidation {
		return "invalid_url"
	}
	var outcome *ClippingAcquisitionError
	if errors.As(err, &outcome) {
		switch outcome.Reason {
		case "unsupported_source_host", "unsupported_youtube_url_shape":
			return "unsupported_source"
		case "unsafe_dns_answer", "redirect_provider_mismatch", "ip_literal_forbidden":
			return "unsafe_destination"
		case "invalid_url", "https_required", "invalid_host", "port_forbidden", "invalid_redirect", "redirect_location_missing":
			return "invalid_url"
		case "youtube_format_unavailable":
			return "format_unavailable"
		case "youtube_import_unavailable":
			return "runtime_unavailable"
		case "youtube_video_unavailable":
			return "video_unavailable"
		case "youtube_duration_limit_exceeded":
			return "duration_limit_exceeded"
		case "youtube_size_limit_exceeded", "source_size_limit_exceeded":
			return "size_limit_exceeded"
		case "provider_denied_download":
			return "provider_denied_download"
		case "partial_response_not_accepted":
			return "partial_response_rejected"
		case "non_media_response":
			return "non_media_response"
		}
	}
	return "download_failed"
}

// The trace writer deliberately drops diagnostic persistence failures so
// instrumentation never changes the source import's outcome.
type clippingDiagnosticTrace struct {
	acquisition *ClippingAcquisition
	sourceID    string
	activeStage string
}

func newClippingDiagnosticTrace(acquisition *ClippingAcquisition, sourceID string) *clippingDiagnosticTrace {
	return &clippingDiagnosticTrace{acquisition: acquisition, sourceID: sourceID}
}

// fork gives concurrent import stages independent active-stage state while
// retaining the shared durable diagnostic writer.
func (trace *clippingDiagnosticTrace) fork() *clippingDiagnosticTrace {
	if trace == nil {
		return nil
	}
	return &clippingDiagnosticTrace{acquisition: trace.acquisition, sourceID: trace.sourceID}
}

func (trace *clippingDiagnosticTrace) record(stage string, status ClippingSourceDiagnosticStatus, code string) {
	if trace == nil || trace.acquisition == nil || trace.acquisition.store == nil {
		return
	}
	if err := trace.acquisition.store.AppendClippingSourceDiagnostic(trace.sourceID, stage, status, code); err == nil {
		trace.acquisition.store.PublishClippingSource(trace.sourceID)
	}
}

func (trace *clippingDiagnosticTrace) start(stage string) {
	if trace == nil {
		return
	}
	trace.activeStage = stage
	trace.record(stage, ClippingDiagnosticStarted, "")
}

func (trace *clippingDiagnosticTrace) complete() {
	if trace == nil || trace.activeStage == "" {
		return
	}
	stage := trace.activeStage
	trace.activeStage = ""
	trace.record(stage, ClippingDiagnosticCompleted, "")
}

func (trace *clippingDiagnosticTrace) finish(status ClippingSourceDiagnosticStatus, err error) {
	if trace == nil || trace.activeStage == "" {
		return
	}
	stage := trace.activeStage
	trace.activeStage = ""
	trace.record(stage, status, clippingDiagnosticCode(stage, err))
}
