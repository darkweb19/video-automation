//go:build !unix && !windows

package app

import "os/exec"

func configureYouTubeSubprocess(*exec.Cmd) {}
