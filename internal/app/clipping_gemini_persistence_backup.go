package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// backupClippingDatabase creates a private, consistent SQLite snapshot even
// when committed pages are still in the WAL. It must run before the rebuild
// transaction, on the dedicated startup connection that will perform it.
// The existing secret.key remains a separate, necessary part of a restore.
func backupClippingDatabase(ctx context.Context, conn *sql.Conn, dataDir string) (string, error) {
	return backupClippingDatabaseWithSync(ctx, conn, dataDir, syncClippingDirectory)
}

func backupClippingDatabaseWithSync(ctx context.Context, conn *sql.Conn, dataDir string, syncDirectory func(string) error) (string, error) {
	backupDir := filepath.Join(dataDir, "clipping-migration-backups")
	if err := os.Mkdir(backupDir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", fmt.Errorf("create private clipping backup directory: %w", err)
	}
	info, err := os.Lstat(backupDir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("clipping backup directory is not private")
	}
	// Persist the backup directory's entry before creating the snapshot; a
	// sync of the backup directory alone does not make its parent entry durable.
	if err := syncDirectory(dataDir); err != nil {
		return "", fmt.Errorf("sync clipping backup parent directory: %w", err)
	}
	managed, err := readClippingBackupManifest(backupDir)
	if err != nil {
		return "", err
	}
	// Reclaim only tracked incomplete allocations before attempting another
	// full snapshot; the last complete backup stays available even on ENOSPC.
	remaining := make([]clippingManagedBackup, 0, len(managed))
	for _, item := range managed {
		if item.Complete || !removeClippingManagedBackup(backupDir, item.Name) {
			remaining = append(remaining, item)
		}
	}
	if err := syncDirectory(backupDir); err != nil {
		return "", err
	}
	if err := writeClippingBackupManifest(backupDir, remaining, syncDirectory); err != nil {
		return "", err
	}
	managed = remaining
	// Reserve a random, absent name in the durable manifest before creating a
	// potentially large file. A crash during VACUUM is then recoverable without
	// treating unrelated files as owned. The private directory and the app's
	// single-owner invariant prevent concurrent allocation by another process.
	backupName, err := newClippingStorageID("before-gemini-")
	if err != nil {
		return "", err
	}
	backupName += ".sqlite"
	path := filepath.Join(backupDir, backupName)
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return "", errors.New("clipping backup allocation already exists")
	}
	managed = append(managed, clippingManagedBackup{Name: backupName})
	if err := writeClippingBackupManifest(backupDir, managed, syncDirectory); err != nil {
		return "", err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", fmt.Errorf("create clipping backup: %w", err)
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(path)
		}
	}()
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("prepare clipping backup: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `VACUUM INTO ?`, path); err != nil {
		return "", fmt.Errorf("snapshot clipping database: %w", err)
	}
	file, err = os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open clipping backup for sync: %w", err)
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	if syncErr != nil || closeErr != nil {
		return "", fmt.Errorf("sync clipping backup: %w", errors.Join(syncErr, closeErr))
	}
	if err := syncDirectory(backupDir); err != nil {
		return "", fmt.Errorf("sync clipping backup directory: %w", err)
	}
	// Only mark complete after the file and its directory entry are durable.
	managed[len(managed)-1].Complete = true
	if err := writeClippingBackupManifest(backupDir, managed, syncDirectory); err != nil {
		return "", err
	}
	keep = true
	remaining = make([]clippingManagedBackup, 0, len(managed))
	for _, item := range managed[:len(managed)-1] {
		if !removeClippingManagedBackup(backupDir, item.Name) {
			remaining = append(remaining, item)
		}
	}
	remaining = append(remaining, managed[len(managed)-1])
	// A failed cleanup or manifest rewrite is harmless: its prior durable
	// manifest still tracks every managed backup for a later startup.
	_ = syncDirectory(backupDir)
	_ = writeClippingBackupManifest(backupDir, remaining, syncDirectory)
	return path, nil
}

const clippingBackupManifest = "managed-backups.json"

type clippingManagedBackup struct {
	Name     string `json:"name"`
	Complete bool   `json:"complete"`
}

// Only a manifest-owned regular file may be removed; a symlink is never followed or removed.
func removeClippingManagedBackup(dir, name string) bool {
	path := filepath.Join(dir, name)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	return err == nil && info.Mode().IsRegular() && os.Remove(path) == nil
}

func readClippingBackupManifest(dir string) ([]clippingManagedBackup, error) {
	path := filepath.Join(dir, clippingBackupManifest)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > 64<<10 {
		return nil, errors.New("clipping backup manifest is not a private regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read clipping backup manifest: %w", err)
	}
	var names []clippingManagedBackup
	if err := json.Unmarshal(data, &names); err != nil {
		return nil, errors.New("invalid clipping backup manifest")
	}
	seen := make(map[string]bool, len(names))
	for _, item := range names {
		name := item.Name
		if name != filepath.Base(name) || !strings.HasPrefix(name, "before-gemini-") || !strings.HasSuffix(name, ".sqlite") || seen[name] {
			return nil, errors.New("invalid clipping backup manifest entry")
		}
		seen[name] = true
	}
	return names, nil
}

func writeClippingBackupManifest(dir string, names []clippingManagedBackup, syncDirectory func(string) error) error {
	data, err := json.Marshal(names)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".manifest-*")
	if err != nil {
		return fmt.Errorf("create clipping backup manifest: %w", err)
	}
	name := file.Name()
	defer os.Remove(name)
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return fmt.Errorf("write clipping backup manifest: %w", err)
	}
	if err := os.Rename(name, filepath.Join(dir, clippingBackupManifest)); err != nil {
		return fmt.Errorf("replace clipping backup manifest: %w", err)
	}
	if err := syncDirectory(dir); err != nil {
		return fmt.Errorf("sync clipping backup manifest: %w", err)
	}
	return nil
}
