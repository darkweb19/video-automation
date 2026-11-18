//go:build windows

package app

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

var ErrClippingPlatformUnavailable = errors.New("native clipping source files require a Linux or Unix host; run the application in Docker/Linux")

func clippingFilesystemSupported() error {
	return clippingOutcome("unsupported", "platform_unavailable", ErrClippingPlatformUnavailable.Error())
}

// Native source reads and writes are disabled on Windows until the complete
// source-directory walk can be protected against reparse-point races. Keeping
// this helper fail-closed prevents Windows symlinks/junctions from weakening
// the Unix O_NOFOLLOW guarantee while leaving the rest of the application
// available.
func openClippingRegularFile(string, int) (*os.File, error) {
	return nil, clippingFilesystemSupported()
}

func syncClippingDirectory(path string) error {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	handle, err := windows.CreateFile(
		name,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return errors.New("source directory is not a regular directory")
	}
	return windows.FlushFileBuffers(handle)
}
