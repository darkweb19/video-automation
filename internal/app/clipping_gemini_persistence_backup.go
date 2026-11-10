package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// backupClippingDatabase creates a private, consistent SQLite snapshot even
// when committed pages are still in the WAL. It must run before the rebuild
// transaction, on the dedicated startup connection that will perform it.
// The existing secret.key remains a separate, necessary part of a restore.
func backupClippingDatabase(ctx context.Context, conn *sql.Conn, dataDir string) (string, error) {
	backupDir := filepath.Join(dataDir, "clipping-migration-backups")
	if err := os.Mkdir(backupDir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", fmt.Errorf("create private clipping backup directory: %w", err)
	}
	info, err := os.Lstat(backupDir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("clipping backup directory is not private")
	}
	file, err := os.CreateTemp(backupDir, "before-gemini-*.sqlite")
	if err != nil {
		return "", fmt.Errorf("create clipping backup: %w", err)
	}
	path := file.Name()
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
	if err := syncClippingDirectory(backupDir); err != nil {
		return "", fmt.Errorf("sync clipping backup directory: %w", err)
	}
	keep = true
	return path, nil
}
