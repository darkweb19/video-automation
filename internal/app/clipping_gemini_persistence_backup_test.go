package app

import (
	"context"
	"database/sql"
	"errors"
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

func TestClippingDatabaseBackupSyncsParentBeforeSnapshot(t *testing.T) {
	store := newTestStore(t)
	conn, err := store.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	wantErr := errors.New("synthetic parent sync failure")
	var synced []string
	_, err = backupClippingDatabaseWithSync(context.Background(), conn, store.dataDir, func(path string) error {
		synced = append(synced, path)
		if path == store.dataDir {
			return wantErr
		}
		return nil
	})
	if !errors.Is(err, wantErr) || len(synced) != 1 || synced[0] != store.dataDir {
		t.Fatalf("backup did not fail at parent sync before snapshot: paths=%v error=%v", synced, err)
	}
	entries, err := os.ReadDir(filepath.Join(store.dataDir, "clipping-migration-backups"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("snapshot was created after failed parent sync: %v error=%v", entries, err)
	}
}

func TestClippingDatabaseBackupBoundsManagedSnapshotsAndPreservesUnknownFiles(t *testing.T) {
	store := newTestStore(t)
	conn, err := store.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	first, err := backupClippingDatabase(context.Background(), conn, store.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(first)
	unknown := filepath.Join(dir, "before-gemini-untracked.sqlite")
	if err := os.WriteFile(unknown, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "before-gemini-link.sqlite")
	if err := os.Symlink(unknown, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	for i := 0; i < 4; i++ {
		latest, err := backupClippingDatabase(context.Background(), conn, store.dataDir)
		if err != nil {
			t.Fatal(err)
		}
		managed, err := readClippingBackupManifest(dir)
		if err != nil || len(managed) != 1 || managed[0] != filepath.Base(latest) {
			t.Fatalf("managed snapshots not bounded: %v error=%v", managed, err)
		}
	}
	if _, err := os.Lstat(first); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old managed backup remains: %v", err)
	}
	if data, err := os.ReadFile(unknown); err != nil || string(data) != "keep" {
		t.Fatalf("unknown backup changed: %q %v", data, err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("unknown symlink changed: %v %v", info, err)
	}
}

func TestClippingDatabaseBackupNeverRemovesReplacedManagedSymlink(t *testing.T) {
	store := newTestStore(t)
	conn, err := store.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	first, err := backupClippingDatabase(context.Background(), conn, store.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(first); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(filepath.Dir(first), "untracked")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, first); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := backupClippingDatabase(context.Background(), conn, store.dataDir); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(first); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("managed-name symlink changed: %v %v", info, err)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "keep" {
		t.Fatalf("symlink target changed: %q %v", data, err)
	}
}
