package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type commandRunner func(context.Context, string, ...string) error

func runCommand(ctx context.Context, name string, args ...string) error {
	command := exec.CommandContext(ctx, name, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if len(message) > 1000 {
			message = message[len(message)-1000:]
		}
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
	args := make([]string, 0, len(inputs)*2+16)
	args = append(args, "-hide_banner", "-loglevel", "error")
	for _, input := range inputs {
		args = append(args, "-i", input)
	}
	filters := make([]string, 0, len(inputs)+1)
	labels := strings.Builder{}
	for index := range inputs {
		label := fmt.Sprintf("v%d", index)
		filters = append(filters, fmt.Sprintf("[%d:v]scale=1080:1920:force_original_aspect_ratio=decrease,pad=1080:1920:(ow-iw)/2:(oh-ih)/2,fps=30,tpad=stop_mode=clone:stop_duration=%d,trim=duration=%d,setpts=PTS-STARTPTS[%s]", index, ProjectSceneSeconds, ProjectSceneSeconds, label))
		labels.WriteString("[")
		labels.WriteString(label)
		labels.WriteString("]")
	}
	filters = append(filters, fmt.Sprintf("%sconcat=n=%d:v=1:a=0[outv]", labels.String(), ProjectSceneCount))
	args = append(args, "-filter_complex", strings.Join(filters, ";"), "-map", "[outv]", "-an", "-c:v", "libx264", "-preset", "medium", "-crf", "20", "-pix_fmt", "yuv420p", "-movflags", "+faststart", "-y", tempPath)
	if err := runner(ctx, "ffmpeg", args...); err != nil {
		_ = os.Remove(tempPath)
		return 0, fmt.Errorf("combine scenes: %w", err)
	}
	file, err := os.Open(tempPath)
	if err != nil {
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
