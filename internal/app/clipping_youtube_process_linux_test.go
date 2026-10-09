//go:build linux

package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestYouTubeExtractorCancellationKillsProcessGroup(t *testing.T) {
	if _, err := execLookPathForYouTubeTest("sleep"); err != nil {
		t.Skip("sleep is unavailable")
	}
	_, _, _, acquisition := newClippingAcquisitionFixture(t)
	acquisition.resolver = clippingTestResolver{{IP: net.ParseIP("8.8.8.8")}}
	directory := t.TempDir()
	pidPath := filepath.Join(directory, "child.pid")
	script := fmt.Sprintf("sleep 300 &\nchild=$!\nprintf '%%s' \"$child\" > %q\nwait \"$child\"\n", pidPath)
	acquisition.youtubeExecutable = writeFakeYouTubeExecutable(t, directory, "yt-dlp", script)
	acquisition.youtubeRuntime = "deno:" + writeFakeYouTubeExecutable(t, directory, "deno", "exit 0\n")
	canonicalURL, err := url.Parse("https://www.youtube.com/watch?v=abcdefghijk")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := acquisition.extractYouTubeMedia(ctx, canonicalURL)
		result <- err
	}()

	deadline := time.Now().Add(3 * time.Second)
	var childPID int
	for time.Now().Before(deadline) {
		pidBytes, readErr := os.ReadFile(pidPath)
		if readErr == nil {
			childPID, err = strconv.Atoi(strings.TrimSpace(string(pidBytes)))
			if err != nil || childPID <= 0 {
				t.Fatalf("invalid child PID file: %q", pidBytes)
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if childPID == 0 {
		cancel()
		t.Fatal("extractor child process did not start")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("extractor cancellation error=%v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("extractor cancellation did not return promptly")
	}

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !youtubeTestProcessAlive(childPID) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("extractor child process %d survived cancellation", childPID)
}

func execLookPathForYouTubeTest(name string) (string, error) {
	return exec.LookPath(name)
}

func youtubeTestProcessAlive(pid int) bool {
	if err := syscall.Kill(pid, 0); err != nil {
		return !errors.Is(err, syscall.ESRCH)
	}
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err == nil {
		end := strings.LastIndexByte(string(stat), ')')
		if end >= 0 && len(stat) > end+2 && stat[end+2] == 'Z' {
			return false
		}
	}
	return true
}
