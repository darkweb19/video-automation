package app

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestClippingDatabaseBackupIsPrivateAndConsistentWithWAL(t *testing.T) {
	store := newTestStore(t)
	if err := store.InsertGeneration(GenerationRecord{ID: "snapshot-generation", Prompt: "before backup", Model: "fixture", Status: "queued"}); err != nil {
		t.Fatal(err)
	}
	conn, err := store.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	path, err := backupClippingDatabase(context.Background(), conn, store.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("backup must be private: info=%v error=%v", info, err)
	}
	if _, err := conn.ExecContext(context.Background(), `UPDATE generations SET prompt='after backup' WHERE id='snapshot-generation'`); err != nil {
		t.Fatal(err)
	}
	backup, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	var prompt string
	if err := backup.QueryRow(`SELECT prompt FROM generations WHERE id='snapshot-generation'`).Scan(&prompt); err != nil || prompt != "before backup" {
		t.Fatalf("backup was not a point-in-time database snapshot: prompt=%q error=%v", prompt, err)
	}
}

func TestClippingDatabaseBackupRejectsPublicDirectory(t *testing.T) {
	store := newTestStore(t)
	dir := filepath.Join(store.dataDir, "clipping-migration-backups")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	conn, err := store.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := backupClippingDatabase(context.Background(), conn, store.dataDir); err == nil {
		t.Fatal("public backup directory should fail closed")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("unexpected files in rejected backup directory: %v, %v", entries, err)
	}
}
