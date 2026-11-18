package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"time"
)

const clippingReferenceMigrationVersion = "gemini_reference_v1"

// These definitions are shared by fresh databases and the table rebuild. A
// URL reference has no file metadata; legacy source values remain unchanged.
const clippingSourcesTableDefinition = `(
	id TEXT PRIMARY KEY,
	kind TEXT NOT NULL CHECK(kind IN ('upload','youtube_original_file','google_drive_public','dropbox_public','youtube_url')),
	status TEXT NOT NULL CHECK(status IN ('uploading','importing','ready','failed','deleted')),
	original_name TEXT NOT NULL DEFAULT '',
	media_type TEXT NOT NULL DEFAULT '',
	source_url TEXT NOT NULL DEFAULT '',
	rights_attested INTEGER NOT NULL DEFAULT 0 CHECK(rights_attested IN (0,1)),
	storage_path TEXT UNIQUE,
	failure TEXT NOT NULL DEFAULT '',
	declared_size_bytes INTEGER,
	reserved_size_bytes INTEGER NOT NULL CHECK(reserved_size_bytes >= 0),
	size_bytes INTEGER DEFAULT 0 CHECK(size_bytes >= 0),
	upload_offset INTEGER NOT NULL DEFAULT 0 CHECK(upload_offset >= 0),
	duration_ms INTEGER DEFAULT 0 CHECK(duration_ms >= 0),
	sha256 TEXT DEFAULT '',
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
	deleted_at INTEGER NOT NULL DEFAULT 0,
	CHECK (
		(kind='youtube_url' AND storage_path IS NULL AND declared_size_bytes IS NULL AND size_bytes IS NULL
		 AND duration_ms IS NULL AND sha256 IS NULL AND reserved_size_bytes=0 AND upload_offset=0
		 AND import_attempts=0 AND next_import_at=0 AND import_lease_until=0 AND media_lease_until=0
		 AND rights_attested=1 AND ((status='ready' AND source_url<>'') OR (status='deleted' AND source_url='')))
		OR
		(kind<>'youtube_url' AND storage_path IS NOT NULL AND declared_size_bytes IS NOT NULL
		 AND declared_size_bytes BETWEEN 1 AND 21474836480 AND size_bytes IS NOT NULL
		 AND duration_ms IS NOT NULL AND sha256 IS NOT NULL)
	)
)`

// Gemini's exact millisecond options live in its immutable snapshot. Legacy
// seconds are absent for Gemini, and remain mandatory for every legacy job.
const clippingJobsTableDefinition = `(
	id TEXT PRIMARY KEY,
	batch_id TEXT NOT NULL REFERENCES clipping_batches(id) ON DELETE RESTRICT,
	source_id TEXT NOT NULL REFERENCES clipping_sources(id) ON DELETE RESTRICT,
	provider TEXT NOT NULL DEFAULT 'modal_legacy' CHECK(provider IN ('modal_legacy','gemini')),
	content_type TEXT NOT NULL DEFAULT 'general' CHECK(content_type IN ('general','podcast','comedy','gaming','movie')),
	min_clip_seconds INTEGER DEFAULT 15,
	max_clip_seconds INTEGER DEFAULT 180,
	candidate_limit INTEGER NOT NULL DEFAULT 10,
	status TEXT NOT NULL CHECK(status IN ('queued','running','paused_budget','completed','failed','canceled')),
	budget_limit_micro_usd INTEGER NOT NULL CHECK(budget_limit_micro_usd > 0),
	reserved_micro_usd INTEGER NOT NULL DEFAULT 0 CHECK(reserved_micro_usd >= 0),
	spent_micro_usd INTEGER NOT NULL DEFAULT 0 CHECK(spent_micro_usd >= 0),
	analysis_deleted INTEGER NOT NULL DEFAULT 0 CHECK(analysis_deleted IN (0,1)),
	error TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	CHECK (
		(provider='modal_legacy' AND min_clip_seconds IS NOT NULL AND max_clip_seconds IS NOT NULL
		 AND min_clip_seconds BETWEEN 15 AND 180 AND max_clip_seconds BETWEEN 15 AND 180
		 AND candidate_limit BETWEEN 7 AND 10)
		OR
		(provider='gemini' AND min_clip_seconds IS NULL AND max_clip_seconds IS NULL
		 AND candidate_limit BETWEEN 1 AND 10)
	)
)`

type clippingMigrationSchemaObject struct {
	typeName string
	name     string
	sql      string
}

// migrateClippingReferenceSchema rebuilds both parent tables as one atomic
// operation. SQLite's foreign-key pragma is connection-local and must be
// changed outside the transaction; all child references retain their names.
func (s *Store) migrateClippingReferenceSchema(existing bool) error {
	return s.migrateClippingReferenceSchemaWithBackup(existing, backupClippingDatabase, runtime.GOOS == "windows")
}

// On Windows the existing app must remain usable when a durable private
// backup cannot be established. The migration marker remains absent and all
// Gemini persistence entry points reject writes until a migration succeeds.
func (s *Store) migrateClippingReferenceSchemaWithBackup(existing bool, backup func(context.Context, *sql.Conn, string) (string, error), deferBackupFailure bool) (resultErr error) {
	ctx := context.Background()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS clipping_schema_migrations(version TEXT PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		return err
	}
	var applied int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM clipping_schema_migrations WHERE version=?`, clippingReferenceMigrationVersion).Scan(&applied); err != nil {
		return err
	}
	if applied != 0 {
		return nil
	}
	if !existing {
		_, err := conn.ExecContext(ctx, `INSERT INTO clipping_schema_migrations(version,applied_at) VALUES(?,?)`, clippingReferenceMigrationVersion, time.Now().Unix())
		return err
	}
	if err := validateClippingMigrationColumns(ctx, conn, "clipping_sources", clippingSourceColumns()); err != nil {
		return err
	}
	if err := validateClippingMigrationColumns(ctx, conn, "clipping_jobs", clippingJobColumns()); err != nil {
		return err
	}
	if _, err := backup(ctx, conn, s.dataDir); err != nil {
		if deferBackupFailure {
			return nil
		}
		return err
	}
	var enabled int
	if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&enabled); err != nil || enabled != 1 {
		return errors.New("clipping migration requires enabled foreign keys")
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return err
	}
	defer func() {
		_, restoreErr := conn.ExecContext(ctx, `PRAGMA foreign_keys=ON`)
		var restored int
		if restoreErr == nil {
			restoreErr = conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&restored)
		}
		if restoreErr == nil && restored != 1 {
			restoreErr = errors.New("foreign keys remain disabled")
		}
		if restoreErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("restore clipping foreign keys: %w", restoreErr))
		}
	}()
	if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&enabled); err != nil || enabled != 0 {
		return errors.New("could not disable foreign keys for clipping migration")
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	objects, err := clippingMigrationSchemaObjects(ctx, tx)
	if err != nil {
		return err
	}
	// SQLite validates the entire schema during ALTER TABLE. Even views and
	// triggers owned by unrelated tables can mention the parents being rebuilt.
	// Drop them transactionally, then restore their exact definitions after both
	// parent names exist again. Rollback restores every object on any failure.
	for _, object := range objects {
		if object.typeName == "view" || object.typeName == "trigger" {
			quotedName := `"` + strings.ReplaceAll(object.name, `"`, `""`) + `"`
			if _, err := tx.ExecContext(ctx, `DROP `+strings.ToUpper(object.typeName)+` `+quotedName); err != nil {
				return fmt.Errorf("drop clipping dependent schema object %q: %w", object.name, err)
			}
		}
	}
	for _, statement := range []string{
		`CREATE TABLE clipping_sources_new ` + clippingSourcesTableDefinition,
		`INSERT INTO clipping_sources_new (` + clippingSourceColumns() + `) SELECT ` + clippingSourceColumns() + ` FROM clipping_sources`,
		`CREATE TABLE clipping_jobs_new ` + clippingJobsTableDefinition,
		`INSERT INTO clipping_jobs_new (` + clippingJobColumns() + `) SELECT ` + clippingJobColumns() + ` FROM clipping_jobs`,
		`DROP TABLE clipping_sources`,
		`ALTER TABLE clipping_sources_new RENAME TO clipping_sources`,
		`DROP TABLE clipping_jobs`,
		`ALTER TABLE clipping_jobs_new RENAME TO clipping_jobs`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("rebuild clipping tables: %w", err)
		}
	}
	for _, object := range objects {
		if _, err := tx.ExecContext(ctx, object.sql); err != nil {
			return fmt.Errorf("restore clipping schema object %q: %w", object.name, err)
		}
	}
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	violated := rows.Next()
	rowsErr := rows.Err()
	closeErr := rows.Close()
	if rowsErr != nil || closeErr != nil {
		return errors.Join(rowsErr, closeErr)
	}
	if violated {
		return errors.New("clipping migration would leave foreign key violations")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO clipping_schema_migrations(version,applied_at) VALUES(?,?)`, clippingReferenceMigrationVersion, time.Now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

func validateClippingMigrationColumns(ctx context.Context, conn *sql.Conn, table, columns string) error {
	// Both table names and expected columns are fixed internal constants.
	want := make(map[string]bool)
	for _, name := range strings.Split(columns, ",") {
		want[name] = true
	}
	rows, err := conn.QueryContext(ctx, `PRAGMA table_xinfo(`+table+`)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var ordinal, notNull, primaryKey, hidden int
		var name, typ string
		var defaultValue sql.NullString
		if err := rows.Scan(&ordinal, &name, &typ, &notNull, &defaultValue, &primaryKey, &hidden); err != nil {
			return err
		}
		if !want[name] || hidden != 0 {
			return fmt.Errorf("clipping migration cannot safely preserve unknown %s column", table)
		}
		delete(want, name)
		count++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(want) != 0 || count == 0 {
		return fmt.Errorf("clipping migration found incomplete %s columns", table)
	}
	return nil
}

func clippingMigrationSchemaObjects(ctx context.Context, tx *sql.Tx) ([]clippingMigrationSchemaObject, error) {
	rows, err := tx.QueryContext(ctx, `SELECT type,name,sql FROM sqlite_schema
		WHERE sql IS NOT NULL AND (type IN ('view','trigger') OR
		(type='index' AND tbl_name IN ('clipping_sources','clipping_jobs')))
		ORDER BY CASE type WHEN 'view' THEN 0 WHEN 'index' THEN 1 ELSE 2 END, rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var objects []clippingMigrationSchemaObject
	for rows.Next() {
		var object clippingMigrationSchemaObject
		if err := rows.Scan(&object.typeName, &object.name, &object.sql); err != nil {
			return nil, err
		}
		objects = append(objects, object)
	}
	return objects, rows.Err()
}

// requireClippingGeminiPersistence prevents reference and snapshot writes on
// a legacy database for which the reference rebuild was deferred.
func (s *Store) requireClippingGeminiPersistence() error {
	if s == nil || s.db == nil {
		return ErrClippingGeminiPersistenceUnavailable
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM clipping_schema_migrations WHERE version=?`, clippingReferenceMigrationVersion).Scan(&count); err != nil || count != 1 {
		return ErrClippingGeminiPersistenceUnavailable
	}
	return nil
}

var ErrClippingGeminiPersistenceUnavailable = errors.New("Gemini clipping persistence is unavailable until its database migration succeeds")
