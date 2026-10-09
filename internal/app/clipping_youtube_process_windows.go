//go:build windows

package app

import (
	"os"
	"os/exec"
)

// Source acquisition is disabled on native Windows; this keeps the package
// cross-compilable while preserving the standard child cancellation behavior.
func configureYouTubeSubprocess(command *exec.Cmd) {
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		return command.Process.Kill()
	}
}
