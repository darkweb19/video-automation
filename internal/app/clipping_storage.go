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
	ErrClippingBatchNotFound       = errors.New("clipping batch not found")
	ErrClippingJobNotFound         = errors.New("clipping job not found")
	ErrClippingStageNotFound       = errors.New("clipping stage not found")
	ErrClippingIdempotencyConflict = errors.New("clipping idempotency key was reused with different input")
	ErrClippingBudgetExceeded      = errors.New("clipping budget limit exceeded")
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
			retain_until INTEGER NOT NULL DEFAULT 0,
			import_attempts INTEGER NOT NULL DEFAULT 0 CHECK(import_attempts >= 0),
			next_import_at INTEGER NOT NULL DEFAULT 0,
			import_lease_until INTEGER NOT NULL DEFAULT 0,
			media_lease_until INTEGER NOT NULL DEFAULT 0,
			cleanup_complete INTEGER NOT NULL DEFAULT 0 CHECK(cleanup_complete IN (0,1)),
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
	`)
	if err != nil {
		return fmt.Errorf("initialize clipping storage: %w", err)
	}
	return nil
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
	return `id,kind,status,original_name,media_type,source_url,rights_attested,storage_path,failure,declared_size_bytes,reserved_size_bytes,size_bytes,upload_offset,duration_ms,sha256,retain_until,import_attempts,next_import_at,import_lease_until,media_lease_until,cleanup_complete,created_at,updated_at,deleted_at`
}

type clippingRowScanner interface {
	Scan(dest ...any) error
}

func scanClippingSource(row clippingRowScanner) (ClippingSource, error) {
	var source ClippingSource
	var cleanupComplete int
	var rightsAttested int
	err := row.Scan(&source.ID, &source.Kind, &source.Status, &source.OriginalName, &source.MediaType, &source.SourceURL, &rightsAttested, &source.StoragePath, &source.Failure,
		&source.DeclaredSizeBytes, &source.ReservedSizeBytes, &source.SizeBytes, &source.UploadOffset, &source.DurationMS, &source.SHA256, &source.RetainUntil,
		&source.ImportAttempts, &source.NextImportAt, &source.ImportLeaseUntil, &source.MediaLeaseUntil, &cleanupComplete, &source.CreatedAt, &source.UpdatedAt, &source.DeletedAt)
	source.CleanupComplete = cleanupComplete != 0
	source.RightsAttested = rightsAttested != 0
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
	result, err := s.db.Exec(`UPDATE clipping_sources SET upload_offset=0,size_bytes=0,updated_at=? WHERE id=? AND status='importing'`, time.Now().Unix(), id)
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
	if err := tx.QueryRow(`SELECT kind,status,declared_size_bytes,upload_offset,rights_attested FROM clipping_sources WHERE id=?`, id).Scan(&kind, &status, &declared, &offset, &rightsAttested); err != nil {
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
		WHERE status='failed' AND reserved_size_bytes>0
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

func (s *Store) ApplyClippingRetention(retainUntil int64) error {
	if retainUntil < 0 {
		return errors.New("retention time cannot be negative")
	}
	_, err := s.db.Exec(`UPDATE clipping_sources SET retain_until=CASE WHEN media_lease_until>? THEN media_lease_until ELSE ? END,updated_at=? WHERE status<>'deleted'`, retainUntil, retainUntil, time.Now().Unix())
	return err
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
	if err := tx.QueryRow(`SELECT COUNT(*) FROM clipping_jobs WHERE source_id=? AND status IN ('queued','running','paused_budget')`, id).Scan(&active); err != nil {
		return err
	}
	if active != 0 {
		return errors.New("clipping source is referenced by an active job")
	}
	// Source-derived artifacts can contain transcripts and analysis content;
	// erase them with the media while keeping job, stage, and cost audit rows.
	if _, err := tx.Exec(`DELETE FROM clipping_artifacts WHERE job_id IN (SELECT id FROM clipping_jobs WHERE source_id=?)`, id); err != nil {
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
	rows, err := s.db.Query(`SELECT `+clippingSourceColumns()+` FROM clipping_sources AS src WHERE
		(src.status='deleted' AND src.cleanup_complete=0) OR
		(src.status<>'deleted' AND src.retain_until>0 AND src.retain_until<=? AND src.media_lease_until<=? AND src.import_lease_until<=? AND NOT EXISTS (
			SELECT 1 FROM clipping_jobs AS job WHERE job.source_id=src.id AND job.status IN ('queued','running','paused_budget')
		)) ORDER BY CASE WHEN src.status='deleted' THEN 0 ELSE 1 END,src.retain_until,src.created_at LIMIT ?`, now, now, now, limit)
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

func clippingBatchColumns() string {
	return `id,idempotency_key,status,budget_limit_micro_usd,reserved_micro_usd,spent_micro_usd,created_at,updated_at`
}

func clippingJobColumns() string {
	return `id,batch_id,source_id,status,budget_limit_micro_usd,reserved_micro_usd,spent_micro_usd,error,created_at,updated_at`
}

func scanClippingBatch(row clippingRowScanner) (ClippingBatch, error) {
	var batch ClippingBatch
	err := row.Scan(&batch.ID, &batch.IdempotencyKey, &batch.Status, &batch.BudgetLimitMicroUSD, &batch.ReservedMicroUSD, &batch.SpentMicroUSD, &batch.CreatedAt, &batch.UpdatedAt)
	return batch, err
}

func scanClippingJob(row clippingRowScanner) (ClippingJob, error) {
	var job ClippingJob
	err := row.Scan(&job.ID, &job.BatchID, &job.SourceID, &job.Status, &job.BudgetLimitMicroUSD, &job.ReservedMicroUSD, &job.SpentMicroUSD, &job.Error, &job.CreatedAt, &job.UpdatedAt)
	return job, err
}

func clippingRequestHash(input ClippingBatchCreate) (string, error) {
	type requestJob struct {
		SourceID            string `json:"source_id"`
		BudgetLimitMicroUSD int64  `json:"budget_limit_micro_usd"`
	}
	canonical := struct {
		BudgetLimitMicroUSD int64        `json:"budget_limit_micro_usd"`
		Jobs                []requestJob `json:"jobs"`
	}{BudgetLimitMicroUSD: input.BudgetLimitMicroUSD, Jobs: make([]requestJob, len(input.Jobs))}
	for i, job := range input.Jobs {
		canonical.Jobs[i] = requestJob{SourceID: job.SourceID, BudgetLimitMicroUSD: job.BudgetLimitMicroUSD}
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:]), nil
}

func (s *Store) CreateClippingBatch(input ClippingBatchCreate) (ClippingBatch, []ClippingJob, bool, error) {
	if strings.TrimSpace(input.IdempotencyKey) == "" || len(input.IdempotencyKey) > 128 || strings.ContainsAny(input.IdempotencyKey, "\r\n\x00") {
		return ClippingBatch{}, nil, false, errors.New("clipping batch idempotency key is required and must be at most 128 bytes")
	}
	if input.BudgetLimitMicroUSD <= 0 {
		return ClippingBatch{}, nil, false, errors.New("clipping batch budget must be positive microUSD")
	}
	if len(input.Jobs) == 0 || len(input.Jobs) > MaxClippingBatchJobs {
		return ClippingBatch{}, nil, false, fmt.Errorf("clipping batch must contain between 1 and %d jobs", MaxClippingBatchJobs)
	}
	for _, job := range input.Jobs {
		if !safeClippingSourceID(job.SourceID) || job.BudgetLimitMicroUSD <= 0 {
			return ClippingBatch{}, nil, false, errors.New("each clipping job requires a valid source and positive microUSD budget")
		}
	}
	requestHash, err := clippingRequestHash(input)
	if err != nil {
		return ClippingBatch{}, nil, false, err
	}
	batchID, err := newClippingStorageID("clipbatch_")
	if err != nil {
		return ClippingBatch{}, nil, false, err
	}
	type pendingJob struct {
		id   string
		spec ClippingJobCreate
	}
	pending := make([]pendingJob, len(input.Jobs))
	for i, job := range input.Jobs {
		id, err := newClippingStorageID("clipjob_")
		if err != nil {
			return ClippingBatch{}, nil, false, err
		}
		pending[i] = pendingJob{id: id, spec: job}
	}
	now := time.Now().Unix()
	tx, err := s.db.Begin()
	if err != nil {
		return ClippingBatch{}, nil, false, err
	}
	defer tx.Rollback()

	var existingHash string
	if err := tx.QueryRow(`SELECT request_hash FROM clipping_batches WHERE idempotency_key=?`, input.IdempotencyKey).Scan(&existingHash); err == nil {
		if existingHash != requestHash {
			return ClippingBatch{}, nil, false, ErrClippingIdempotencyConflict
		}
		batch, err := scanClippingBatch(tx.QueryRow(`SELECT `+clippingBatchColumns()+` FROM clipping_batches WHERE idempotency_key=?`, input.IdempotencyKey))
		if err != nil {
			return ClippingBatch{}, nil, false, err
		}
		jobs, err := listClippingJobs(tx, batch.ID)
		if err != nil {
			return ClippingBatch{}, nil, false, err
		}
		if err := tx.Commit(); err != nil {
			return ClippingBatch{}, nil, false, err
		}
		return batch, jobs, true, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return ClippingBatch{}, nil, false, err
	}
	for _, job := range input.Jobs {
		var status ClippingSourceStatus
		var rightsAttested int
		var retainUntil int64
		if err := tx.QueryRow(`SELECT status,retain_until,rights_attested FROM clipping_sources WHERE id=?`, job.SourceID).Scan(&status, &retainUntil, &rightsAttested); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ClippingBatch{}, nil, false, ErrClippingSourceNotFound
			}
			return ClippingBatch{}, nil, false, err
		}
		if status != ClippingSourceReady || rightsAttested == 0 || (retainUntil > 0 && retainUntil <= now) {
			return ClippingBatch{}, nil, false, errors.New("clipping jobs require a ready source within its retention period")
		}
	}
	_, err = tx.Exec(`INSERT INTO clipping_batches(id,idempotency_key,request_hash,status,budget_limit_micro_usd,created_at,updated_at) VALUES(?,?,?,'queued',?,?,?)`, batchID, input.IdempotencyKey, requestHash, input.BudgetLimitMicroUSD, now, now)
	if err != nil {
		return ClippingBatch{}, nil, false, err
	}
	jobs := make([]ClippingJob, 0, len(pending))
	for _, item := range pending {
		_, err := tx.Exec(`INSERT INTO clipping_jobs(id,batch_id,source_id,status,budget_limit_micro_usd,created_at,updated_at) VALUES(?,?,?,'queued',?,?,?)`, item.id, batchID, item.spec.SourceID, item.spec.BudgetLimitMicroUSD, now, now)
		if err != nil {
			return ClippingBatch{}, nil, false, err
		}
		stageKey := item.id + ":" + ClippingStageAnalysis
		_, err = tx.Exec(`INSERT INTO clipping_stages(job_id,name,status,idempotency_key,created_at,updated_at) VALUES(?,?,'queued',?,?,?)`, item.id, ClippingStageAnalysis, stageKey, now, now)
		if err != nil {
			return ClippingBatch{}, nil, false, err
		}
		jobs = append(jobs, ClippingJob{ID: item.id, BatchID: batchID, SourceID: item.spec.SourceID, Status: ClippingJobQueued, BudgetLimitMicroUSD: item.spec.BudgetLimitMicroUSD, CreatedAt: now, UpdatedAt: now})
	}
	if err := tx.Commit(); err != nil {
		return ClippingBatch{}, nil, false, err
	}
	return ClippingBatch{ID: batchID, IdempotencyKey: input.IdempotencyKey, Status: ClippingJobQueued, BudgetLimitMicroUSD: input.BudgetLimitMicroUSD, CreatedAt: now, UpdatedAt: now}, jobs, false, nil
}

func listClippingJobs(queryer clippingQueryer, batchID string) ([]ClippingJob, error) {
	query := `SELECT ` + clippingJobColumns() + ` FROM clipping_jobs`
	var rows *sql.Rows
	var err error
	if batchID == "" {
		rows, err = queryer.Query(query + ` ORDER BY created_at,id`)
	} else {
		rows, err = queryer.Query(query+` WHERE batch_id=? ORDER BY created_at,id`, batchID)
	}
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

func (s *Store) ClippingBatch(id string) (ClippingBatch, error) {
	if !safeID(id) || !strings.HasPrefix(id, "clipbatch_") {
		return ClippingBatch{}, ErrClippingBatchNotFound
	}
	batch, err := scanClippingBatch(s.db.QueryRow(`SELECT `+clippingBatchColumns()+` FROM clipping_batches WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return ClippingBatch{}, ErrClippingBatchNotFound
	}
	return batch, err
}

func (s *Store) ClippingBatches() ([]ClippingBatch, error) {
	rows, err := s.db.Query(`SELECT ` + clippingBatchColumns() + ` FROM clipping_batches ORDER BY created_at DESC,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	batches := make([]ClippingBatch, 0)
	for rows.Next() {
		batch, err := scanClippingBatch(rows)
		if err != nil {
			return nil, err
		}
		batches = append(batches, batch)
	}
	return batches, rows.Err()
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

func (s *Store) ClippingJobs(batchID string) ([]ClippingJob, error) {
	if batchID != "" && (!safeID(batchID) || !strings.HasPrefix(batchID, "clipbatch_")) {
		return nil, ErrClippingBatchNotFound
	}
	return listClippingJobs(s.db, batchID)
}

func clippingStageColumns() string {
	return `job_id,name,status,idempotency_key,attempt_id,attempt,lease_expires_at,reserved_micro_usd,actual_micro_usd,error,created_at,updated_at`
}

func scanClippingStage(row clippingRowScanner) (ClippingStage, error) {
	var stage ClippingStage
	err := row.Scan(&stage.JobID, &stage.Name, &stage.Status, &stage.IdempotencyKey, &stage.AttemptID, &stage.Attempt, &stage.LeaseExpiresAt, &stage.ReservedMicroUSD, &stage.ActualMicroUSD, &stage.Error, &stage.CreatedAt, &stage.UpdatedAt)
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

func clippingBudgetAvailable(limit, spent, reserved, request int64) bool {
	if limit < 0 || spent < 0 || reserved < 0 || request < 0 || spent > limit || reserved > limit-spent {
		return false
	}
	return request <= limit-spent-reserved
}

func addClippingMoney(left, right int64) (int64, error) {
	if left < 0 || right < 0 || right > math.MaxInt64-left {
		return 0, errors.New("clipping cost counter overflow")
	}
	return left + right, nil
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
		status = ClippingJobPausedBudget
	case counts[ClippingStagePausedBudget] > 0:
		status = ClippingJobPausedBudget
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

func refreshClippingBatchStatus(tx *sql.Tx, batchID string, now int64) (ClippingJobStatus, error) {
	var current ClippingJobStatus
	if err := tx.QueryRow(`SELECT status FROM clipping_batches WHERE id=?`, batchID).Scan(&current); err != nil {
		return "", err
	}
	if current == ClippingJobCanceled {
		return current, nil
	}
	rows, err := tx.Query(`SELECT status,COUNT(*) FROM clipping_jobs WHERE batch_id=? GROUP BY status`, batchID)
	if err != nil {
		return "", err
	}
	counts := make(map[ClippingJobStatus]int)
	for rows.Next() {
		var status ClippingJobStatus
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
	case counts[ClippingJobRunning] > 0:
		status = ClippingJobRunning
	case counts[ClippingJobPausedBudget] > 0:
		status = ClippingJobPausedBudget
	case counts[ClippingJobQueued] > 0:
		status = ClippingJobQueued
	case counts[ClippingJobFailed] > 0:
		status = ClippingJobFailed
	case counts[ClippingJobCanceled] > 0:
		status = ClippingJobCanceled
	default:
		status = ClippingJobCompleted
	}
	_, err = tx.Exec(`UPDATE clipping_batches SET status=?,updated_at=? WHERE id=?`, status, now, batchID)
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
	var batchLimit, batchReserved, batchSpent int64
	if err := tx.QueryRow(`SELECT budget_limit_micro_usd,reserved_micro_usd,spent_micro_usd FROM clipping_batches WHERE id=?`, job.BatchID).Scan(&batchLimit, &batchReserved, &batchSpent); err != nil {
		return ClippingStage{}, false, err
	}
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
		if _, err := tx.Exec(`UPDATE clipping_stages SET status='uncertain',error='Dispatch outcome is unknown; budget remains reserved',updated_at=? WHERE job_id=? AND name=?`, nowUnix, jobID, name); err != nil {
			return ClippingStage{}, false, err
		}
		if _, err := tx.Exec(`UPDATE clipping_jobs SET status='paused_budget',error='Dispatch outcome is unknown; budget remains reserved',updated_at=? WHERE id=?`, nowUnix, jobID); err != nil {
			return ClippingStage{}, false, err
		}
		if _, err := refreshClippingBatchStatus(tx, job.BatchID, nowUnix); err != nil {
			return ClippingStage{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return ClippingStage{}, false, err
		}
		stage.Status = ClippingStageUncertain
		stage.Error = "Dispatch outcome is unknown; budget remains reserved"
		stage.UpdatedAt = nowUnix
		return stage, false, nil
	}
	if !clippingBudgetAvailable(job.BudgetLimitMicroUSD, job.SpentMicroUSD, job.ReservedMicroUSD, reserveMicroUSD) ||
		!clippingBudgetAvailable(batchLimit, batchSpent, batchReserved, reserveMicroUSD) {
		_, err := tx.Exec(`UPDATE clipping_stages SET status='paused_budget',reserved_micro_usd=0,lease_expires_at=0,error='Budget limit reached',updated_at=? WHERE job_id=? AND name=?`, nowUnix, jobID, name)
		if err != nil {
			return ClippingStage{}, false, err
		}
		if _, err := tx.Exec(`UPDATE clipping_jobs SET status='paused_budget',error='Budget limit reached',updated_at=? WHERE id=?`, nowUnix, jobID); err != nil {
			return ClippingStage{}, false, err
		}
		if _, err := refreshClippingBatchStatus(tx, job.BatchID, nowUnix); err != nil {
			return ClippingStage{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return ClippingStage{}, false, err
		}
		stage.Status, stage.Error, stage.UpdatedAt = ClippingStagePausedBudget, "Budget limit reached", nowUnix
		stage.LeaseExpiresAt, stage.ReservedMicroUSD = 0, 0
		return stage, false, nil
	}
	reservedJob, err := addClippingMoney(job.ReservedMicroUSD, reserveMicroUSD)
	if err != nil {
		return ClippingStage{}, false, err
	}
	reservedBatch, err := addClippingMoney(batchReserved, reserveMicroUSD)
	if err != nil {
		return ClippingStage{}, false, err
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
	_, err = tx.Exec(`UPDATE clipping_jobs SET status='running',reserved_micro_usd=?,error='',updated_at=? WHERE id=?`, reservedJob, nowUnix, jobID)
	if err != nil {
		return ClippingStage{}, false, err
	}
	_, err = tx.Exec(`UPDATE clipping_batches SET reserved_micro_usd=?,updated_at=? WHERE id=?`, reservedBatch, nowUnix, job.BatchID)
	if err != nil {
		return ClippingStage{}, false, err
	}
	if _, err := refreshClippingBatchStatus(tx, job.BatchID, nowUnix); err != nil {
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
// while retaining their reservations. A worker must not redispatch uncertain
// paid work automatically; an authenticated late callback or explicit
// reconciliation must settle the held amount first.
func (s *Store) RecoverExpiredClippingStages(now int64) error {
	if now <= 0 {
		now = time.Now().Unix()
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT job_id,name,attempt_id FROM clipping_stages WHERE status='running' AND lease_expires_at<=?`, now)
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
	batches := make(map[string]struct{})
	for _, item := range expired {
		if item.attemptID != "" {
			if _, err := tx.Exec(`UPDATE clipping_stage_attempts SET status='expired' WHERE attempt_id=? AND status='running'`, item.attemptID); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`UPDATE clipping_stages SET status='uncertain',error='Dispatch outcome is unknown; budget remains reserved',updated_at=? WHERE job_id=? AND name=? AND status='running' AND lease_expires_at<=?`, now, item.jobID, item.name, now); err != nil {
			return err
		}
		var batchID string
		if err := tx.QueryRow(`SELECT batch_id FROM clipping_jobs WHERE id=?`, item.jobID).Scan(&batchID); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE clipping_jobs SET status='paused_budget',error='Dispatch outcome is unknown; budget remains reserved',updated_at=? WHERE id=? AND status='running'`, now, item.jobID); err != nil {
			return err
		}
		batches[batchID] = struct{}{}
	}
	for batchID := range batches {
		if _, err := refreshClippingBatchStatus(tx, batchID, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ExpiredClippingStages lists lease-expired work that needs recovery or sparse
// callback polling. Uncertain attempts remain visible until their costs are
// reconciled; callers must not redispatch them automatically.
func (s *Store) ExpiredClippingStages(now int64, limit int) ([]ClippingStage, error) {
	if now <= 0 {
		now = time.Now().Unix()
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT `+clippingStageColumns()+` FROM clipping_stages
		WHERE status IN ('running','uncertain') AND lease_expires_at<=?
		ORDER BY lease_expires_at,job_id,name LIMIT ?`, now, limit)
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
	if stage.AttemptID != "" {
		var settledAt int64
		if err := tx.QueryRow(`SELECT settled_at FROM clipping_stage_attempts WHERE attempt_id=?`, stage.AttemptID).Scan(&settledAt); err != nil {
			return ClippingStage{}, err
		}
		if settledAt == 0 {
			return ClippingStage{}, errors.New("cannot retry a stage while prior dispatch cost is unresolved")
		}
	}
	if _, err := tx.Exec(`UPDATE clipping_stages SET status='queued',attempt_id='',error='',lease_expires_at=0,reserved_micro_usd=0,updated_at=? WHERE job_id=? AND name=?`, now, jobID, name); err != nil {
		return ClippingStage{}, err
	}
	if _, err := tx.Exec(`UPDATE clipping_jobs SET status='queued',error='',updated_at=? WHERE id=?`, now, jobID); err != nil {
		return ClippingStage{}, err
	}
	if _, err := refreshClippingBatchStatus(tx, job.BatchID, now); err != nil {
		return ClippingStage{}, err
	}
	stage.Status = ClippingStageQueued
	stage.Error = ""
	stage.LeaseExpiresAt = 0
	stage.ReservedMicroUSD = 0
	stage.UpdatedAt = now
	if err := tx.Commit(); err != nil {
		return ClippingStage{}, err
	}
	return stage, nil
}

type clippingAttemptRecord struct {
	AttemptID        string
	JobID            string
	StageName        string
	AttemptNo        int
	IdempotencyKey   string
	TokenHash        string
	Status           string
	LeaseExpiresAt   int64
	ReservedMicroUSD int64
	ActualMicroUSD   int64
	SettledAt        int64
}

func readClippingAttempt(tx *sql.Tx, jobID, stageName, attemptID string) (clippingAttemptRecord, error) {
	var attempt clippingAttemptRecord
	err := tx.QueryRow(`SELECT attempt_id,job_id,stage_name,attempt_no,idempotency_key,token_hash,status,lease_expires_at,reserved_micro_usd,actual_micro_usd,settled_at FROM clipping_stage_attempts WHERE attempt_id=? AND job_id=? AND stage_name=?`, attemptID, jobID, stageName).Scan(
		&attempt.AttemptID, &attempt.JobID, &attempt.StageName, &attempt.AttemptNo, &attempt.IdempotencyKey, &attempt.TokenHash, &attempt.Status, &attempt.LeaseExpiresAt, &attempt.ReservedMicroUSD, &attempt.ActualMicroUSD, &attempt.SettledAt)
	return attempt, err
}

func clippingStageTerminal(status ClippingStageStatus) bool {
	return status == ClippingStageCompleted || status == ClippingStageFailed || status == ClippingStageCanceled
}

func clippingJobTerminal(status ClippingJobStatus) bool {
	return status == ClippingJobCompleted || status == ClippingJobFailed || status == ClippingJobCanceled
}

func releaseClippingReservation(tx *sql.Tx, job *ClippingJob, amount int64, now int64) error {
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

func settleClippingCost(tx *sql.Tx, job *ClippingJob, actual int64, now int64) error {
	if actual < 0 {
		return errors.New("actual clipping cost cannot be negative")
	}
	jobSpent, err := addClippingMoney(job.SpentMicroUSD, actual)
	if err != nil {
		return err
	}
	var batchSpent int64
	if err := tx.QueryRow(`SELECT spent_micro_usd FROM clipping_batches WHERE id=?`, job.BatchID).Scan(&batchSpent); err != nil {
		return err
	}
	newBatchSpent, err := addClippingMoney(batchSpent, actual)
	if err != nil {
		return err
	}
	job.SpentMicroUSD = jobSpent
	if _, err := tx.Exec(`UPDATE clipping_jobs SET spent_micro_usd=?,updated_at=? WHERE id=?`, jobSpent, now, job.ID); err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE clipping_batches SET spent_micro_usd=?,updated_at=? WHERE id=?`, newBatchSpent, now, job.BatchID)
	return err
}

func addClippingStageActual(tx *sql.Tx, jobID, name string, actual int64, now int64) error {
	if actual < 0 {
		return errors.New("actual clipping cost cannot be negative")
	}
	var prior int64
	if err := tx.QueryRow(`SELECT actual_micro_usd FROM clipping_stages WHERE job_id=? AND name=?`, jobID, name).Scan(&prior); err != nil {
		return err
	}
	value, err := addClippingMoney(prior, actual)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE clipping_stages SET actual_micro_usd=?,updated_at=? WHERE job_id=? AND name=?`, value, now, jobID, name)
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

func (s *Store) CompleteClippingStage(jobID, name, attemptID, leaseToken string, actualCostMicroUSD int64, artifact ClippingArtifact) (ClippingStage, bool, error) {
	if !safeID(jobID) || !strings.HasPrefix(jobID, "clipjob_") || !validStageName(name) || attemptID == "" || leaseToken == "" || actualCostMicroUSD < 0 {
		return ClippingStage{}, false, ErrClippingStaleAttempt
	}
	now := time.Now().Unix()
	tx, err := s.db.Begin()
	if err != nil {
		return ClippingStage{}, false, err
	}
	defer tx.Rollback()
	attempt, err := readClippingAttempt(tx, jobID, name, attemptID)
	if errors.Is(err, sql.ErrNoRows) {
		return ClippingStage{}, false, ErrClippingStaleAttempt
	}
	if err != nil {
		return ClippingStage{}, false, err
	}
	if !validClippingLease(leaseToken, attempt.TokenHash) {
		return ClippingStage{}, false, ErrClippingStaleAttempt
	}
	stage, err := readClippingStage(tx, jobID, name)
	if err != nil {
		return ClippingStage{}, false, err
	}
	if attempt.SettledAt > 0 {
		if err := tx.Commit(); err != nil {
			return ClippingStage{}, false, err
		}
		return stage, true, nil
	}
	job, err := scanClippingJob(tx.QueryRow(`SELECT `+clippingJobColumns()+` FROM clipping_jobs WHERE id=?`, jobID))
	if err != nil {
		return ClippingStage{}, false, err
	}
	var durationMS int64
	var sourceStatus ClippingSourceStatus
	if err := tx.QueryRow(`SELECT duration_ms,status FROM clipping_sources WHERE id=?`, job.SourceID).Scan(&durationMS, &sourceStatus); err != nil {
		return ClippingStage{}, false, err
	}
	current := attempt.Status == "running" && stage.AttemptID == attemptID && stage.Status == ClippingStageRunning && stage.LeaseExpiresAt > now && !clippingJobTerminal(job.Status) && sourceStatus == ClippingSourceReady
	if current {
		rangesJSON, artifactErr := validateClippingArtifact(artifact, jobID, durationMS)
		if artifactErr == nil {
			artifactErr = insertClippingArtifact(tx, artifact, rangesJSON, now)
		}
		if err := releaseClippingReservation(tx, &job, attempt.ReservedMicroUSD, now); err != nil {
			return ClippingStage{}, false, err
		}
		if err := settleClippingCost(tx, &job, actualCostMicroUSD, now); err != nil {
			return ClippingStage{}, false, err
		}
		if err := addClippingStageActual(tx, jobID, name, actualCostMicroUSD, now); err != nil {
			return ClippingStage{}, false, err
		}
		attemptStatus := "completed"
		stageStatus := ClippingStageCompleted
		stageError := ""
		if artifactErr != nil {
			attemptStatus = "failed"
			stageStatus = ClippingStageFailed
			stageError = cleanClippingError(artifactErr.Error())
			if _, err := tx.Exec(`UPDATE clipping_jobs SET error=?,status='failed',updated_at=? WHERE id=?`, stageError, now, jobID); err != nil {
				return ClippingStage{}, false, err
			}
		}
		if _, err := tx.Exec(`UPDATE clipping_stage_attempts SET status=?,actual_micro_usd=?,settled_at=?,error=? WHERE attempt_id=?`, attemptStatus, actualCostMicroUSD, now, stageError, attemptID); err != nil {
			return ClippingStage{}, false, err
		}
		if _, err := tx.Exec(`UPDATE clipping_stages SET status=?,reserved_micro_usd=0,lease_expires_at=0,error=?,updated_at=? WHERE job_id=? AND name=?`, stageStatus, stageError, now, jobID, name); err != nil {
			return ClippingStage{}, false, err
		}
		if artifactErr == nil {
			if _, err := refreshClippingJobStatus(tx, jobID, now); err != nil {
				return ClippingStage{}, false, err
			}
		}
		if _, err := refreshClippingBatchStatus(tx, job.BatchID, now); err != nil {
			return ClippingStage{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return ClippingStage{}, false, err
		}
		stage.Status, stage.ReservedMicroUSD, stage.LeaseExpiresAt, stage.Error, stage.UpdatedAt = stageStatus, 0, 0, stageError, now
		stage.ActualMicroUSD += actualCostMicroUSD
		if artifactErr != nil {
			return stage, false, artifactErr
		}
		return stage, false, nil
	}

	canceled := job.Status == ClippingJobCanceled || stage.Status == ClippingStageCanceled
	if err := releaseClippingReservation(tx, &job, attempt.ReservedMicroUSD, now); err != nil {
		return ClippingStage{}, false, err
	}
	if err := settleClippingCost(tx, &job, actualCostMicroUSD, now); err != nil {
		return ClippingStage{}, false, err
	}
	if err := addClippingStageActual(tx, jobID, name, actualCostMicroUSD, now); err != nil {
		return ClippingStage{}, false, err
	}
	attemptStatus := "settled_stale"
	if canceled {
		attemptStatus = "settled_canceled"
	}
	if _, err := tx.Exec(`UPDATE clipping_stage_attempts SET status=?,actual_micro_usd=?,settled_at=?,error=? WHERE attempt_id=?`, attemptStatus, actualCostMicroUSD, now, "stale callback", attemptID); err != nil {
		return ClippingStage{}, false, err
	}
	if stage.AttemptID == attemptID {
		if canceled {
			if _, err := tx.Exec(`UPDATE clipping_stages SET reserved_micro_usd=0,updated_at=? WHERE job_id=? AND name=?`, now, jobID, name); err != nil {
				return ClippingStage{}, false, err
			}
		} else {
			if _, err := tx.Exec(`UPDATE clipping_stages SET status='failed',reserved_micro_usd=0,lease_expires_at=0,error='Expired attempt settled without advancing the stage',updated_at=? WHERE job_id=? AND name=?`, now, jobID, name); err != nil {
				return ClippingStage{}, false, err
			}
			if _, err := tx.Exec(`UPDATE clipping_jobs SET status='failed',error='Expired attempt settled without advancing the stage',updated_at=? WHERE id=?`, now, jobID); err != nil {
				return ClippingStage{}, false, err
			}
		}
		if !canceled {
			if _, err := refreshClippingJobStatus(tx, jobID, now); err != nil {
				return ClippingStage{}, false, err
			}
		}
	}
	if _, err := refreshClippingBatchStatus(tx, job.BatchID, now); err != nil {
		return ClippingStage{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return ClippingStage{}, false, err
	}
	stage, err = scanClippingStage(s.db.QueryRow(`SELECT `+clippingStageColumns()+` FROM clipping_stages WHERE job_id=? AND name=?`, jobID, name))
	if err != nil {
		return ClippingStage{}, false, err
	}
	if canceled {
		return stage, false, ErrClippingJobTerminal
	}
	return stage, false, ErrClippingStaleAttempt
}

func (s *Store) FailClippingStage(jobID, name, attemptID, leaseToken, reason string, retryable bool, actualCostMicroUSD int64) (ClippingStage, error) {
	if !safeID(jobID) || !strings.HasPrefix(jobID, "clipjob_") || !validStageName(name) || attemptID == "" || leaseToken == "" || actualCostMicroUSD < 0 {
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
	if current {
		if err := releaseClippingReservation(tx, &job, attempt.ReservedMicroUSD, now); err != nil {
			return ClippingStage{}, err
		}
		if err := settleClippingCost(tx, &job, actualCostMicroUSD, now); err != nil {
			return ClippingStage{}, err
		}
		if err := addClippingStageActual(tx, jobID, name, actualCostMicroUSD, now); err != nil {
			return ClippingStage{}, err
		}
		attemptStatus := "failed"
		stageStatus := ClippingStageFailed
		jobStatus := ClippingJobFailed
		if retryable {
			stageStatus = ClippingStageQueued
			jobStatus = ClippingJobQueued
		}
		if _, err := tx.Exec(`UPDATE clipping_stage_attempts SET status=?,actual_micro_usd=?,settled_at=?,error=? WHERE attempt_id=?`, attemptStatus, actualCostMicroUSD, now, reason, attemptID); err != nil {
			return ClippingStage{}, err
		}
		if _, err := tx.Exec(`UPDATE clipping_stages SET status=?,reserved_micro_usd=0,lease_expires_at=0,error=?,updated_at=? WHERE job_id=? AND name=?`, stageStatus, reason, now, jobID, name); err != nil {
			return ClippingStage{}, err
		}
		if _, err := tx.Exec(`UPDATE clipping_jobs SET status=?,error=?,updated_at=? WHERE id=?`, jobStatus, reason, now, jobID); err != nil {
			return ClippingStage{}, err
		}
		if _, err := refreshClippingBatchStatus(tx, job.BatchID, now); err != nil {
			return ClippingStage{}, err
		}
		if err := tx.Commit(); err != nil {
			return ClippingStage{}, err
		}
		stage.Status, stage.ReservedMicroUSD, stage.LeaseExpiresAt, stage.Error, stage.UpdatedAt = stageStatus, 0, 0, reason, now
		stage.ActualMicroUSD += actualCostMicroUSD
		return stage, nil
	}

	canceled := job.Status == ClippingJobCanceled || stage.Status == ClippingStageCanceled
	if err := releaseClippingReservation(tx, &job, attempt.ReservedMicroUSD, now); err != nil {
		return ClippingStage{}, err
	}
	if err := settleClippingCost(tx, &job, actualCostMicroUSD, now); err != nil {
		return ClippingStage{}, err
	}
	if err := addClippingStageActual(tx, jobID, name, actualCostMicroUSD, now); err != nil {
		return ClippingStage{}, err
	}
	attemptStatus := "settled_stale"
	if canceled {
		attemptStatus = "settled_canceled"
	}
	if _, err := tx.Exec(`UPDATE clipping_stage_attempts SET status=?,actual_micro_usd=?,settled_at=?,error=? WHERE attempt_id=?`, attemptStatus, actualCostMicroUSD, now, reason, attemptID); err != nil {
		return ClippingStage{}, err
	}
	if stage.AttemptID == attemptID {
		if canceled {
			if _, err := tx.Exec(`UPDATE clipping_stages SET reserved_micro_usd=0,updated_at=? WHERE job_id=? AND name=?`, now, jobID, name); err != nil {
				return ClippingStage{}, err
			}
		} else {
			if _, err := tx.Exec(`UPDATE clipping_stages SET status='failed',reserved_micro_usd=0,lease_expires_at=0,error=?,updated_at=? WHERE job_id=? AND name=?`, reason, now, jobID, name); err != nil {
				return ClippingStage{}, err
			}
			if _, err := tx.Exec(`UPDATE clipping_jobs SET status='failed',error=?,updated_at=? WHERE id=?`, reason, now, jobID); err != nil {
				return ClippingStage{}, err
			}
		}
		if !canceled {
			if _, err := refreshClippingJobStatus(tx, jobID, now); err != nil {
				return ClippingStage{}, err
			}
		}
	}
	if _, err := refreshClippingBatchStatus(tx, job.BatchID, now); err != nil {
		return ClippingStage{}, err
	}
	if err := tx.Commit(); err != nil {
		return ClippingStage{}, err
	}
	stage, err = scanClippingStage(s.db.QueryRow(`SELECT `+clippingStageColumns()+` FROM clipping_stages WHERE job_id=? AND name=?`, jobID, name))
	if err != nil {
		return ClippingStage{}, err
	}
	if canceled {
		return stage, ErrClippingJobTerminal
	}
	return stage, ErrClippingStaleAttempt
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
	if _, err := refreshClippingBatchStatus(tx, job.BatchID, now); err != nil {
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
	stages := make([]ClippingStage, 0)
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
	reservedToRelease := int64(0)
	for _, stage := range stages {
		unsettledDispatch := false
		if stage.AttemptID != "" && stage.ReservedMicroUSD > 0 {
			var settledAt int64
			if err := tx.QueryRow(`SELECT settled_at FROM clipping_stage_attempts WHERE attempt_id=?`, stage.AttemptID).Scan(&settledAt); err != nil {
				return err
			}
			unsettledDispatch = settledAt == 0
		}
		if !unsettledDispatch {
			if stage.ReservedMicroUSD > math.MaxInt64-reservedToRelease {
				return errors.New("clipping reservation counter overflow")
			}
			reservedToRelease += stage.ReservedMicroUSD
		}
		if stage.AttemptID != "" && (stage.Status == ClippingStageRunning || stage.Status == ClippingStageUncertain) {
			if _, err := tx.Exec(`UPDATE clipping_stage_attempts SET status='canceled',error='Canceled' WHERE attempt_id=? AND status IN ('running','expired')`, stage.AttemptID); err != nil {
				return err
			}
		}
		reserved := int64(0)
		if unsettledDispatch {
			reserved = stage.ReservedMicroUSD
		}
		if _, err := tx.Exec(`UPDATE clipping_stages SET status='canceled',reserved_micro_usd=?,lease_expires_at=0,error='Canceled',updated_at=? WHERE job_id=? AND name=?`, reserved, now, job.ID, stage.Name); err != nil {
			return err
		}
	}
	if reservedToRelease > job.ReservedMicroUSD {
		return errors.New("clipping job reservation counters are inconsistent")
	}
	var batchReserved int64
	if err := tx.QueryRow(`SELECT reserved_micro_usd FROM clipping_batches WHERE id=?`, job.BatchID).Scan(&batchReserved); err != nil {
		return err
	}
	if reservedToRelease > batchReserved {
		return errors.New("clipping batch reservation counters are inconsistent")
	}
	job.ReservedMicroUSD -= reservedToRelease
	job.Status = ClippingJobCanceled
	if _, err := tx.Exec(`UPDATE clipping_jobs SET status='canceled',reserved_micro_usd=?,error='Canceled',updated_at=? WHERE id=?`, job.ReservedMicroUSD, now, job.ID); err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE clipping_batches SET reserved_micro_usd=?,updated_at=? WHERE id=?`, batchReserved-reservedToRelease, now, job.BatchID)
	return err
}

func (s *Store) CancelClippingBatch(id string) error {
	if !safeID(id) || !strings.HasPrefix(id, "clipbatch_") {
		return ErrClippingBatchNotFound
	}
	now := time.Now().Unix()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var batchStatus ClippingJobStatus
	if err := tx.QueryRow(`SELECT status FROM clipping_batches WHERE id=?`, id).Scan(&batchStatus); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrClippingBatchNotFound
		}
		return err
	}
	if clippingJobTerminal(batchStatus) {
		return tx.Commit()
	}
	rows, err := tx.Query(`SELECT `+clippingJobColumns()+` FROM clipping_jobs WHERE batch_id=? AND status IN ('queued','running','paused_budget')`, id)
	if err != nil {
		return err
	}
	jobs := make([]ClippingJob, 0)
	for rows.Next() {
		job, err := scanClippingJob(rows)
		if err != nil {
			rows.Close()
			return err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for i := range jobs {
		if err := cancelClippingJobTx(tx, &jobs[i], now); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE clipping_batches SET status='canceled',updated_at=? WHERE id=?`, now, id); err != nil {
		return err
	}
	return tx.Commit()
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
