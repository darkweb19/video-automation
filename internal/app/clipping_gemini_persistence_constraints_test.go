package app

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestClippingReferenceSchemaSeparatesUnknownFileMetadata(t *testing.T) {
	store := newTestStore(t)
	for _, id := range []string{"clipsrc_reference_one", "clipsrc_reference_two"} {
		_, err := store.db.Exec(`INSERT INTO clipping_sources(id,kind,status,source_url,rights_attested,storage_path,declared_size_bytes,reserved_size_bytes,size_bytes,duration_ms,sha256,created_at,updated_at)
			VALUES(?,'youtube_url','ready','encrypted',1,NULL,NULL,0,NULL,NULL,NULL,1,1)`, id)
		if err != nil {
			t.Fatalf("insert independent null-path reference: %v", err)
		}
		var path, digest, declared, size, duration any
		if err := store.db.QueryRow(`SELECT storage_path,sha256,declared_size_bytes,size_bytes,duration_ms FROM clipping_sources WHERE id=?`, id).Scan(&path, &digest, &declared, &size, &duration); err != nil {
			t.Fatal(err)
		}
		if path != nil || digest != nil || declared != nil || size != nil || duration != nil {
			t.Fatalf("unknown reference metadata became fabricated values: %v %v %v %v %v", path, digest, declared, size, duration)
		}
		source, err := store.ClippingSource(id)
		if err != nil || string(source.Kind) != "youtube_url" || source.StoragePath != "" || source.SHA256 != "" || source.SizeBytes != 0 || source.DurationMS != 0 {
			t.Fatalf("reference scan lost absence semantics: %+v error=%v", source, err)
		}
	}
	for _, statement := range []string{
		`UPDATE clipping_sources SET storage_path='fake' WHERE id='clipsrc_reference_one'`,
		`UPDATE clipping_sources SET declared_size_bytes=1 WHERE id='clipsrc_reference_one'`,
		`UPDATE clipping_sources SET size_bytes=0 WHERE id='clipsrc_reference_one'`,
		`UPDATE clipping_sources SET duration_ms=0 WHERE id='clipsrc_reference_one'`,
		`UPDATE clipping_sources SET sha256='' WHERE id='clipsrc_reference_one'`,
		`UPDATE clipping_sources SET reserved_size_bytes=1 WHERE id='clipsrc_reference_one'`,
		`UPDATE clipping_sources SET source_url='' WHERE id='clipsrc_reference_one'`,
		`UPDATE clipping_sources SET status='importing' WHERE id='clipsrc_reference_one'`,
	} {
		if _, err := store.db.Exec(statement); err == nil {
			t.Fatalf("invalid reference update was accepted: %s", statement)
		}
	}
	legacy := makeReadyClippingSource(t, store, 60_000)
	if _, err := store.db.Exec(`UPDATE clipping_sources SET storage_path=NULL WHERE id=?`, legacy.ID); err == nil {
		t.Fatal("legacy file source accepted a null storage path")
	}
	if _, err := store.db.Exec(`UPDATE clipping_sources SET declared_size_bytes=NULL WHERE id=?`, legacy.ID); err == nil {
		t.Fatal("legacy file source accepted null declared size")
	}
	if _, err := os.Stat(filepath.Join(store.dataDir, "clipping-migration-backups")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fresh database unexpectedly created a migration backup: %v", err)
	}
}

func TestClippingReferenceSchemaSeparatesExactGeminiOptions(t *testing.T) {
	store := newTestStore(t)
	source := makeReadyClippingSource(t, store, 60_000)
	job := makeClippingJob(t, store, source.ID, "exact-gemini-job")
	if _, err := store.db.Exec(`UPDATE clipping_jobs SET provider='gemini',min_clip_seconds=NULL,max_clip_seconds=NULL,candidate_limit=1 WHERE id=?`, job.ID); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.ClippingJob(job.ID)
	if err != nil || loaded.Provider != ClippingProviderGemini || loaded.MinClipSeconds != 0 || loaded.MaxClipSeconds != 0 || loaded.CandidateLimit != 1 {
		t.Fatalf("Gemini compatibility scan is incorrect: %+v error=%v", loaded, err)
	}
	for _, statement := range []string{
		`UPDATE clipping_jobs SET min_clip_seconds=15 WHERE id=?`,
		`UPDATE clipping_jobs SET max_clip_seconds=180 WHERE id=?`,
		`UPDATE clipping_jobs SET candidate_limit=0 WHERE id=?`,
		`UPDATE clipping_jobs SET candidate_limit=11 WHERE id=?`,
		`UPDATE clipping_jobs SET provider='modal_legacy' WHERE id=?`,
	} {
		if _, err := store.db.Exec(statement, job.ID); err == nil {
			t.Fatalf("inconsistent Gemini job update was accepted: %s", statement)
		}
	}
	legacyJob := makeClippingJob(t, store, source.ID, "legacy-option-check")
	if _, err := store.db.Exec(`UPDATE clipping_jobs SET candidate_limit=1 WHERE id=?`, legacyJob.ID); err == nil {
		t.Fatal("legacy 7–10 candidate bound was weakened")
	}
	if _, err := store.db.Exec(`UPDATE clipping_jobs SET min_clip_seconds=NULL WHERE id=?`, legacyJob.ID); err == nil {
		t.Fatal("legacy job accepted missing duration")
	}
}

func TestClippingReferenceMigrationRejectsUnknownColumns(t *testing.T) {
	store := newTestStore(t)
	source := makeReadyClippingSource(t, store, 60_000)
	if _, err := store.db.Exec(`ALTER TABLE clipping_sources ADD COLUMN future_data TEXT NOT NULL DEFAULT 'preserve-me'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DELETE FROM clipping_schema_migrations WHERE version=?`, clippingReferenceMigrationVersion); err != nil {
		t.Fatal(err)
	}
	if err := store.migrateClippingReferenceSchema(true); err == nil {
		t.Fatal("unknown column should block a migration that cannot preserve it")
	}
	var value string
	if err := store.db.QueryRow(`SELECT future_data FROM clipping_sources WHERE id=?`, source.ID).Scan(&value); err != nil || value != "preserve-me" {
		t.Fatalf("unknown column data was lost: value=%q error=%v", value, err)
	}
	assertClippingForeignKeys(t, store.db)
}
