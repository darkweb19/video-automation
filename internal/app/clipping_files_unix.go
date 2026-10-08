//go:build unix

package app

import (
	"errors"
	"os"
	"syscall"
)

func clippingFilesystemSupported() error { return nil }

func openClippingRegularFile(path string, flags int) (*os.File, error) {
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return nil, errors.New("source media path is not a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	file, err := os.OpenFile(path, flags|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	opened, statErr := file.Stat()
	lstat, lstatErr := os.Lstat(path)
	if statErr != nil || lstatErr != nil || !opened.Mode().IsRegular() || !lstat.Mode().IsRegular() || !os.SameFile(opened, lstat) {
		file.Close()
		return nil, errors.Join(statErr, lstatErr, errors.New("source media path changed while opening"))
	}
	return file, nil
}

func syncClippingDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func clippingAvailableDiskBytes(path string) (int64, error) {
	var stats syscall.Statfs_t
	if err := syscall.Statfs(path, &stats); err != nil {
		return 0, err
	}
	blockSize := int64(stats.Bsize)
	if blockSize <= 0 || stats.Bavail > uint64((int64(1<<63-1))/blockSize) {
		return 0, errors.New("invalid filesystem capacity")
	}
	return int64(stats.Bavail) * blockSize, nil
}
