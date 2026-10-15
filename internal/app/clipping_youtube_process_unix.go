//go:build unix

package app

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func configureYouTubeSubprocess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}

func youtubeSubprocessFileLimitReached(command *exec.Cmd, runErr error) bool {
	if runErr == nil || command == nil || command.ProcessState == nil {
		return false
	}
	status, ok := command.ProcessState.Sys().(syscall.WaitStatus)
	return ok && status.Signaled() && status.Signal() == syscall.SIGXFSZ
}
