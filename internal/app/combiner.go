package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type commandRunner func(context.Context, string, ...string) error

// commandErrorTailLimit keeps an unexpectedly noisy failed command from
// retaining its entire output in the application heap. FFmpeg normally writes
// diagnostics to stderr, even with -loglevel error.
const commandErrorTailLimit = 4 * 1024

// ffmpegWorkerThreadLimit is a deliberately small fixed cap for each input
// decoder and the x264 encoder. Filter paths remain single-threaded because
// the five-scene graph is already concurrent across inputs.
const ffmpegWorkerThreadLimit = "4"

const ffmpegFilterThreadLimit = "1"

// commandErrorTail retains only the most recent diagnostics, which are the
// useful part of FFmpeg failures (the final error is usually emitted last).
type commandErrorTail struct {
	data  []byte
	limit int
}

func (tail *commandErrorTail) Write(data []byte) (int, error) {
	written := len(data)
	if tail.limit <= 0 || written == 0 {
		return written, nil
	}
	if written >= tail.limit {
		tail.data = append(tail.data[:0], data[written-tail.limit:]...)
		return written, nil
	}
	overflow := len(tail.data) + written - tail.limit
	if overflow > 0 {
		copy(tail.data, tail.data[overflow:])
		tail.data = tail.data[:len(tail.data)-overflow]
	}
	tail.data = append(tail.data, data...)
	return written, nil
}

func (tail *commandErrorTail) String() string {
	return string(tail.data)
}

func runCommand(ctx context.Context, name string, args ...string) error {
	command := exec.CommandContext(ctx, name, args...)
	output := commandErrorTail{limit: commandErrorTailLimit}
	command.Stdout = io.Discard
	command.Stderr = &output
	err := command.Run()
	if err != nil {
		message := strings.TrimSpace(output.String())
		if message != "" {
			return fmt.Errorf("%s: %w", message, err)
		}
		return err
	}
	return nil
}

func combineProjectVideo(ctx context.Context, runner commandRunner, inputs []string, finalPath string) (int64, error) {
	if len(inputs) != ProjectSceneCount {
		return 0, fmt.Errorf("expected %d scene clips", ProjectSceneCount)
	}
	if runner == nil {
		runner = runCommand
	}
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o700); err != nil {
		return 0, err
	}
	tempPath := strings.TrimSuffix(finalPath, filepath.Ext(finalPath)) + ".part.mp4"
	_ = os.Remove(tempPath)
	args := make([]string, 0, len(inputs)*3+28)
	args = append(args, "-hide_banner", "-nostdin", "-loglevel", "error", "-filter_threads", ffmpegFilterThreadLimit, "-filter_complex_threads", ffmpegFilterThreadLimit)
	for _, input := range inputs {
		args = append(args, "-threads", ffmpegWorkerThreadLimit, "-i", input)
	}
	filters := make([]string, 0, len(inputs)+1)
	labels := strings.Builder{}
	for index := range inputs {
		label := fmt.Sprintf("v%d", index)
		filters = append(filters, fmt.Sprintf("[%d:v]scale=1080:1920:force_original_aspect_ratio=decrease,pad=1080:1920:(ow-iw)/2:(oh-ih)/2,setsar=1,fps=30,tpad=stop_mode=clone:stop_duration=%d,trim=duration=%d,setpts=PTS-STARTPTS[%s]", index, ProjectSceneSeconds, ProjectSceneSeconds, label))
		labels.WriteString("[")
		labels.WriteString(label)
		labels.WriteString("]")
	}
	filters = append(filters, fmt.Sprintf("%sconcat=n=%d:v=1:a=0[outv]", labels.String(), ProjectSceneCount))
	// The output settings preserve the current 1080x1920, CRF 20 result while
	// preventing one combine from expanding filter and x264 worker threads to
	// all available CPUs and their associated frame buffers.
	args = append(args, "-filter_complex", strings.Join(filters, ";"), "-map", "[outv]", "-an", "-c:v", "libx264", "-threads", ffmpegWorkerThreadLimit, "-x264-params", "threads="+ffmpegWorkerThreadLimit, "-preset", "medium", "-crf", "20", "-pix_fmt", "yuv420p", "-movflags", "+faststart", "-y", tempPath)
	if err := runner(ctx, "ffmpeg", args...); err != nil {
		_ = os.Remove(tempPath)
		return 0, fmt.Errorf("combine scenes: %w", err)
	}
	file, err := os.OpenFile(tempPath, os.O_RDWR, 0)
	if err != nil {
		_ = os.Remove(tempPath)
		return 0, err
	}
	info, statErr := file.Stat()
	syncErr := file.Sync()
	closeErr := file.Close()
	if statErr != nil || syncErr != nil || closeErr != nil {
		_ = os.Remove(tempPath)
		return 0, errors.Join(statErr, syncErr, closeErr)
	}
	if info.Size() == 0 {
		_ = os.Remove(tempPath)
		return 0, errors.New("FFmpeg created an empty final video")
	}
	if err := os.Rename(tempPath, finalPath); err != nil {
		_ = os.Remove(tempPath)
		return 0, err
	}
	return info.Size(), nil
}
