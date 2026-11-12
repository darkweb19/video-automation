package app

import (
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClippingReferenceSchemaMigratesPopulatedForeignKeysAndRestoresBackup(t *testing.T) {
	dataDir := t.TempDir()
	security, err := NewSecurity(&Store{dataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	const originalURL = "https://www.youtube.com/watch?v=abcdefghijk"
	encryptedURL, err := security.EncryptSetting(clippingSourceURLSetting, originalURL)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(clippingM2SchemaWithoutM3Columns); err != nil {
		t.Fatal(err)
	}
	const sourceID, batchID, jobID, attemptID = "clipsrc_legacy", "clipbatch_legacy", "clipjob_legacy", "clipatt_legacy"
	_, err = db.Exec(`INSERT INTO clipping_sources(id,kind,status,source_url,rights_attested,storage_path,declared_size_bytes,reserved_size_bytes,size_bytes,duration_ms,sha256,created_at,updated_at)
		VALUES(?,'youtube_original_file','ready',?,1,?,42,42,42,60000,?,1,1)`, sourceID, encryptedURL, filepath.Join(dataDir, "clipping", sourceID, "source.media"), strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO clipping_batches(id,idempotency_key,request_hash,status,budget_limit_micro_usd,created_at,updated_at) VALUES(?,'old-key','old-hash','completed',1,1,1)`, []any{batchID}},
		{`INSERT INTO clipping_jobs(id,batch_id,source_id,status,budget_limit_micro_usd,created_at,updated_at) VALUES(?,?,?,'completed',1,1,1)`, []any{jobID, batchID, sourceID}},
		{`INSERT INTO clipping_stages(job_id,name,status,idempotency_key,created_at,updated_at) VALUES(?,'analysis','completed','old-stage',1,1)`, []any{jobID}},
		{`INSERT INTO clipping_stage_attempts(attempt_id,job_id,stage_name,attempt_no,idempotency_key,token_hash,status,lease_expires_at,reserved_micro_usd,started_at,settled_at) VALUES(?,?,'analysis',1,'old-stage','hash','completed',1,0,1,1)`, []any{attemptID, jobID}},
		{`INSERT INTO clipping_artifacts(job_id,artifact_type,schema_version,version,source_duration_ms,time_ranges_json,payload_json,created_at) VALUES(?,'clipping.analysis','2',1,60000,'[]','{}',1)`, []any{jobID}},
		{`CREATE INDEX clipping_user_source_name ON clipping_sources(original_name)`, nil},
		{`CREATE TRIGGER clipping_user_source_update AFTER UPDATE ON clipping_sources BEGIN SELECT 1; END`, nil},
		{`CREATE VIEW "source view" AS SELECT id FROM clipping_sources`, nil},
		{`CREATE VIEW job_view AS SELECT id FROM clipping_jobs`, nil},
		{`CREATE VIEW nested_view AS SELECT id FROM "source view"`, nil},
		{`CREATE TABLE other_table(value TEXT)`, nil},
		{`CREATE TRIGGER "other trigger" AFTER INSERT ON other_table BEGIN SELECT id FROM clipping_jobs; SELECT id FROM "source view"; END`, nil},
	} {
		if _, err := db.Exec(query.sql, query.args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.ClippingSource(sourceID)
	if err != nil || source.SHA256 != strings.Repeat("a", 64) || source.SizeBytes != 42 || source.DurationMS != 60_000 {
		t.Fatalf("legacy source changed: source=%+v error=%v", source, err)
	}
	job, err := store.ClippingJob(jobID)
	if err != nil || job.Provider != ClippingProviderModalLegacy || job.SourceID != sourceID || job.Status != ClippingJobCompleted {
		t.Fatalf("legacy job changed: job=%+v error=%v", job, err)
	}
	for _, table := range []string{"clipping_stages", "clipping_stage_attempts", "clipping_artifacts"} {
		var count int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("legacy %s count=%d error=%v", table, count, err)
		}
	}
	for _, name := range []string{"clipping_sources_import_queue", "clipping_sources_retention", "clipping_jobs_batch", "clipping_jobs_source", "clipping_user_source_name", "clipping_user_source_update", "source view", "job_view", "nested_view", "other trigger"} {
		var count int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE name=?`, name).Scan(&count); err != nil || count != 1 {
			t.Fatalf("schema object %q lost: count=%d error=%v", name, count, err)
		}
	}
	if _, err := store.db.Exec(`INSERT INTO other_table VALUES('ok')`); err != nil {
		t.Fatalf("restored cross-table trigger failed: %v", err)
	}
	var viewCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM nested_view`).Scan(&viewCount); err != nil || viewCount != 1 {
		t.Fatalf("restored nested view count=%d error=%v", viewCount, err)
	}
	assertClippingForeignKeys(t, store.db)
	backups, err := filepath.Glob(filepath.Join(dataDir, "clipping-migration-backups", "before-gemini-*.sqlite"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("expected one consistent backup: %v error=%v", backups, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	assertClippingForeignKeys(t, reopened.db)
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	again, _ := filepath.Glob(filepath.Join(dataDir, "clipping-migration-backups", "before-gemini-*.sqlite"))
	if len(again) != 1 {
		t.Fatalf("reopen created repeated backup: %v", again)
	}
	// A database backup alone cannot decrypt settings; a restore also needs the
	// existing secret.key. Link this test-generated key without reading it.
	restoreDir := t.TempDir()
	if err := os.Link(filepath.Join(dataDir, "secret.key"), filepath.Join(restoreDir, "secret.key")); err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(backups[0])
	if err != nil {
		t.Fatal(err)
	}
	output, err := os.OpenFile(filepath.Join(restoreDir, "app.db"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(output, input); err != nil {
		t.Fatal(err)
	}
	if err := input.Close(); err != nil {
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := OpenStore(restoreDir)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	restoredSource, err := restored.ClippingSource(sourceID)
	if err != nil {
		t.Fatal(err)
	}
	restoredSecurity, err := NewSecurity(restored)
	if err != nil {
		t.Fatal(err)
	}
	if plaintext, err := restoredSecurity.DecryptSetting(clippingSourceURLSetting, restoredSource.SourceURL); err != nil || plaintext != originalURL {
		t.Fatalf("backup plus original key failed to restore encrypted source URL: %v", err)
	}
	assertClippingForeignKeys(t, restored.db)
}

func TestClippingReferenceMigrationRollbackPreservesOriginalTables(t *testing.T) {
	store := newTestStore(t)
	source := makeReadyClippingSource(t, store, 60_000)
	job := makeClippingJob(t, store, source.ID, "rollback-legacy-job")
	for _, statement := range []string{
		`CREATE VIEW rollback_view AS SELECT id FROM clipping_sources`,
		`CREATE TABLE rollback_other(value TEXT)`,
		`CREATE TRIGGER rollback_trigger AFTER INSERT ON rollback_other BEGIN SELECT id FROM clipping_jobs; END`,
	} {
		if _, err := store.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.Exec(`PRAGMA ignore_check_constraints=ON`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE clipping_sources SET declared_size_bytes=0 WHERE id=?`, source.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`PRAGMA ignore_check_constraints=OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DELETE FROM clipping_schema_migrations WHERE version=?`, clippingReferenceMigrationVersion); err != nil {
		t.Fatal(err)
	}
	if err := store.migrateClippingReferenceSchema(true); err == nil {
		t.Fatal("invalid old row should abort table copy")
	}
	var declared int64
	if err := store.db.QueryRow(`SELECT declared_size_bytes FROM clipping_sources WHERE id=?`, source.ID).Scan(&declared); err != nil || declared != 0 {
		t.Fatalf("original source was lost after rollback: declared=%d error=%v", declared, err)
	}
	if loaded, err := store.ClippingJob(job.ID); err != nil || loaded.SourceID != source.ID {
		t.Fatalf("original job was lost after rollback: %+v error=%v", loaded, err)
	}
	var leftovers int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE name IN ('clipping_sources_new','clipping_jobs_new')`).Scan(&leftovers); err != nil || leftovers != 0 {
		t.Fatalf("partial rebuild persisted: %d error=%v", leftovers, err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM rollback_view`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("rollback lost view: count=%d error=%v", count, err)
	}
	if _, err := store.db.Exec(`INSERT INTO rollback_other VALUES('ok')`); err != nil {
		t.Fatalf("rollback lost cross-table trigger: %v", err)
	}
	assertClippingForeignKeys(t, store.db)
}

func assertClippingForeignKeys(t *testing.T, db *sql.DB) {
	t.Helper()
	var enabled int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&enabled); err != nil || enabled != 1 {
		t.Fatalf("foreign keys not enabled: %d error=%v", enabled, err)
	}
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() || rows.Err() != nil {
		t.Fatalf("foreign key violations after migration: %v", rows.Err())
	}
}
