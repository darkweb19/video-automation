//go:build !unix && !windows

package app

import "os/exec"

func configureYouTubeSubprocess(*exec.Cmd) {}

func youtubeSubprocessFileLimitReached(*exec.Cmd, error) bool { return false }
