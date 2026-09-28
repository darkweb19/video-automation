package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCommandErrorTailKeepsOnlyRecentOutput(t *testing.T) {
	tail := commandErrorTail{limit: 8}
	if _, err := tail.Write([]byte("first-")); err != nil {
		t.Fatal(err)
	}
	if _, err := tail.Write([]byte("last")); err != nil {
		t.Fatal(err)
	}
	if got, want := tail.String(), "rst-last"; got != want {
		t.Fatalf("tail = %q, want %q", got, want)
	}
	if _, err := tail.Write([]byte("0123456789")); err != nil {
		t.Fatal(err)
	}
	if got, want := tail.String(), "23456789"; got != want {
		t.Fatalf("tail after large write = %q, want %q", got, want)
	}
}

func TestRunCommandBoundsFailedCommandOutput(t *testing.T) {
	if os.Getenv("GO_WANT_COMMAND_OUTPUT_HELPER") == "1" {
		_, _ = fmt.Fprint(os.Stderr, strings.Repeat("x", commandErrorTailLimit*2)+" final-marker")
		os.Exit(3)
	}
	t.Setenv("GO_WANT_COMMAND_OUTPUT_HELPER", "1")
	err := runCommand(context.Background(), os.Args[0], "-test.run=^TestRunCommandBoundsFailedCommandOutput$", "--")
	if err == nil {
		t.Fatal("expected helper process failure")
	}
	if !strings.Contains(err.Error(), "final-marker") {
		t.Fatalf("error lost final diagnostics: %q", err)
	}
	if len(err.Error()) > commandErrorTailLimit+100 {
		t.Fatalf("error retained %d bytes, limit is %d", len(err.Error()), commandErrorTailLimit)
	}
}

func TestCombineProjectVideoUsesBoundedThreadsAndNormalizesOutput(t *testing.T) {
	dir := t.TempDir()
	inputs := make([]string, ProjectSceneCount)
	for index := range inputs {
		inputs[index] = filepath.Join(dir, fmt.Sprintf("scene-%d.mp4", index+1))
		if err := os.WriteFile(inputs[index], []byte("clip"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	finalPath := filepath.Join(dir, "final.mp4")
	var captured []string
	runner := func(_ context.Context, name string, args ...string) error {
		if name != "ffmpeg" {
			t.Fatalf("runner = %s", name)
		}
		captured = append([]string(nil), args...)
		return os.WriteFile(args[len(args)-1], []byte("final-video"), 0o600)
	}
	if _, err := combineProjectVideo(context.Background(), runner, inputs, finalPath); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(captured, " ")
	for _, expected := range []string{
		"-nostdin",
		"-filter_threads " + ffmpegFilterThreadLimit,
		"-filter_complex_threads " + ffmpegFilterThreadLimit,
		"scale=1080:1920",
		"setsar=1",
		"trim=duration=6",
		"concat=n=5:v=1:a=0",
		"-c:v libx264",
		"-x264-params threads=" + ffmpegWorkerThreadLimit,
		"-crf 20",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("missing %q in FFmpeg args: %s", expected, joined)
		}
	}
	if got, want := countArg(captured, "-threads"), ProjectSceneCount+1; got != want {
		t.Fatalf("-threads count = %d, want %d", got, want)
	}
	for index, arg := range captured {
		if arg != "-threads" {
			continue
		}
		if index+1 >= len(captured) {
			t.Fatalf("missing thread value after position %d", index)
		}
		if captured[index+1] != ffmpegWorkerThreadLimit {
			t.Fatalf("thread argument after position %d = %q, want %q", index, captured[index+1], ffmpegWorkerThreadLimit)
		}
	}
}

func TestCombineProjectVideoRemovesPartialOutputAfterError(t *testing.T) {
	dir := t.TempDir()
	inputs := make([]string, ProjectSceneCount)
	for index := range inputs {
		inputs[index] = filepath.Join(dir, fmt.Sprintf("scene-%d.mp4", index+1))
	}
	finalPath := filepath.Join(dir, "final.mp4")
	runner := func(_ context.Context, _ string, args ...string) error {
		if err := os.WriteFile(args[len(args)-1], []byte("partial"), 0o600); err != nil {
			return err
		}
		return errors.New("FFmpeg failed")
	}
	if _, err := combineProjectVideo(context.Background(), runner, inputs, finalPath); err == nil {
		t.Fatal("expected combine failure")
	}
	for _, path := range []string{finalPath, filepath.Join(dir, "final.part.mp4")} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("partial output remains at %s: %v", path, err)
		}
	}
}

func TestCombineProjectVideoRemovesUnopenableTemporaryOutput(t *testing.T) {
	dir := t.TempDir()
	inputs := make([]string, ProjectSceneCount)
	for index := range inputs {
		inputs[index] = filepath.Join(dir, fmt.Sprintf("scene-%d.mp4", index+1))
	}
	finalPath := filepath.Join(dir, "final.mp4")
	tempPath := filepath.Join(dir, "final.part.mp4")
	runner := func(_ context.Context, _ string, args ...string) error {
		return os.Mkdir(args[len(args)-1], 0o700)
	}
	if _, err := combineProjectVideo(context.Background(), runner, inputs, finalPath); err == nil {
		t.Fatal("expected temporary output open failure")
	}
	if _, err := os.Stat(tempPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unopenable temporary output remains: %v", err)
	}
}

func TestCombineTaskUsesOneSlotWithoutBlockingProjectTasks(t *testing.T) {
	processor := &Processor{
		combineSem: make(chan struct{}, 1),
		projectSem: make(chan struct{}, 1),
		inFlight:   make(map[string]struct{}),
	}
	combineStarted := make(chan struct{})
	releaseCombine := make(chan struct{})
	combineDone := make(chan struct{})
	processor.startCombineTask(context.Background(), "one:combine", func(context.Context) {
		close(combineStarted)
		<-releaseCombine
		close(combineDone)
	})
	waitForSignal(t, combineStarted, "first combine did not start")
	secondStarted := make(chan struct{})
	processor.startCombineTask(context.Background(), "two:combine", func(context.Context) {
		close(secondStarted)
	})
	select {
	case <-secondStarted:
		t.Fatal("second combine started while the first held the single combine slot")
	default:
	}

	projectStarted := make(chan struct{})
	processor.startProjectTask(context.Background(), "project:submit", func(context.Context) {
		close(projectStarted)
	})
	waitForSignal(t, projectStarted, "project task was blocked by active combine")

	close(releaseCombine)
	waitForSignal(t, combineDone, "first combine did not finish")
	waitForCombineSlot(t, processor)
	processor.startCombineTask(context.Background(), "two:combine", func(context.Context) {
		close(secondStarted)
	})
	waitForSignal(t, secondStarted, "combine slot did not reopen after the first task finished")
}

func TestFailedCombineCanRetryAfterProcessorRestart(t *testing.T) {
	store := newTestStore(t)
	project, err := store.InsertProject("combine retry", "provider/model")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveStoryPlan(project.ID, validStoryPlan()); err != nil {
		t.Fatal(err)
	}
	for number := 1; number <= ProjectSceneCount; number++ {
		path := store.SceneVideoPath(project.ID, number)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("clip"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := store.MarkSceneVideoReady(project.ID, number, path, int64(len("clip"))); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.UpdateProjectStatus(project.ID, "combining", ""); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	failing := NewProcessor(store, nil, logger)
	failing.combineRunner = func(context.Context, string, ...string) error {
		return errors.New("FFmpeg failed")
	}
	failing.process(context.Background())
	waitForCombineProjectStatus(t, store, project.ID, "failed")
	if err := store.RetryProject(project.ID); err != nil {
		t.Fatalf("retry failed project: %v", err)
	}

	restarted := NewProcessor(store, nil, logger)
	restarted.combineRunner = func(_ context.Context, _ string, args ...string) error {
		return os.WriteFile(args[len(args)-1], []byte("final"), 0o600)
	}
	restarted.process(context.Background())
	completed := waitForCombineProjectStatus(t, store, project.ID, "completed")
	if completed.FinalSizeBytes != int64(len("final")) {
		t.Fatalf("final size = %d", completed.FinalSizeBytes)
	}
}

func TestCombineProjectVideoWithLocalFFmpeg(t *testing.T) {
	ffmpeg := localFFmpeg(t)
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	inputs := createSyntheticSceneClips(t, ctx, ffmpeg, dir)
	finalPath := filepath.Join(dir, "final.mp4")
	runner := func(ctx context.Context, _ string, args ...string) error {
		return runCommand(ctx, ffmpeg, args...)
	}
	if _, err := combineProjectVideo(ctx, runner, inputs, finalPath); err != nil {
		t.Fatalf("combine synthetic clips: %v", err)
	}
	width, height, duration := probeVideo(t, ctx, ffmpeg, finalPath)
	if width != 1080 || height != 1920 {
		t.Fatalf("output resolution = %dx%d, want 1080x1920", width, height)
	}
	if duration < 29.8 || duration > 30.2 {
		t.Fatalf("output duration = %.3fs, want 30s", duration)
	}
}

func TestFiveSceneWorkflowStreamsDownloadsAndCombinesWithFFmpeg(t *testing.T) {
	ffmpeg := localFFmpeg(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	store := newTestStore(t)
	project, err := store.InsertProject("streamed workflow", "provider/model")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveStoryPlan(project.ID, validStoryPlan()); err != nil {
		t.Fatal(err)
	}
	clipDir := t.TempDir()
	clipPaths := createSyntheticSceneClips(t, ctx, ffmpeg, clipDir)
	provider := &workflowVideoProvider{clips: make(map[string]string)}
	for index, path := range clipPaths {
		provider.clips[fmt.Sprintf("scene_%d", index+1)] = path
	}
	server := httptest.NewServer(http.HandlerFunc(provider.serveClip))
	defer server.Close()
	provider.baseURL = server.URL
	provider.client = server.Client()

	processor := NewProcessor(store, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	processor.combineRunner = func(runCtx context.Context, _ string, args ...string) error {
		return runCommand(runCtx, ffmpeg, args...)
	}
	project, err = store.Project(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, scene := range project.Scenes {
		processor.submitScene(ctx, provider, project, scene)
	}
	project = waitForProjectScenes(t, store, project.ID, "queued")
	for _, scene := range project.Scenes {
		processor.pollScene(ctx, provider, scene)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		project, err = store.Project(project.ID)
		if err != nil {
			t.Fatal(err)
		}
		if project.Status == "combining" {
			break
		}
		processor.processProjectScenes(ctx, provider, project)
		time.Sleep(10 * time.Millisecond)
	}
	if project.Status != "combining" {
		t.Fatalf("project did not reach combining: status=%s scenes=%+v", project.Status, project.Scenes)
	}
	processor.processCombiningProjects(ctx, []VideoProject{project})
	completed := waitForCombineProjectStatus(t, store, project.ID, "completed")
	if !completed.FinalVideoReady || completed.FinalSizeBytes == 0 {
		t.Fatalf("final project output = %+v", completed)
	}
	width, height, duration := probeVideo(t, ctx, ffmpeg, completed.FinalVideoPath)
	if width != 1080 || height != 1920 || duration < 29.8 || duration > 30.2 {
		t.Fatalf("final output = %dx%d %.3fs", width, height, duration)
	}
	for _, scene := range completed.Scenes {
		if scene.Status != "completed" || !scene.VideoReady || scene.SizeBytes == 0 {
			t.Fatalf("scene did not complete after streamed download: %+v", scene)
		}
		if _, err := os.Stat(scene.VideoPath + ".part"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("streamed download left temporary file for scene %d: %v", scene.Number, err)
		}
	}
	if got := provider.contentRequestCount(); got != ProjectSceneCount {
		t.Fatalf("content request count = %d, want %d", got, ProjectSceneCount)
	}
}

func countArg(args []string, target string) int {
	count := 0
	for _, arg := range args {
		if arg == target {
			count++
		}
	}
	return count
}

func waitForSignal(t *testing.T, signal <-chan struct{}, failure string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal(failure)
	}
}

func waitForCombineProjectStatus(t *testing.T, store *Store, projectID, want string) VideoProject {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		project, err := store.Project(projectID)
		if err != nil {
			t.Fatal(err)
		}
		if project.Status == want {
			return project
		}
		time.Sleep(10 * time.Millisecond)
	}
	project, err := store.Project(projectID)
	if err != nil {
		t.Fatal(err)
	}
	t.Fatalf("project status = %s, want %s", project.Status, want)
	return VideoProject{}
}

func waitForCombineSlot(t *testing.T, processor *Processor) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(processor.combineSem) == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("combine slot was not released")
}

func localFFmpeg(t *testing.T) string {
	t.Helper()
	if path, err := exec.LookPath("ffmpeg"); err == nil {
		return path
	}
	const homebrewFFmpeg = "/opt/homebrew/bin/ffmpeg"
	if _, err := os.Stat(homebrewFFmpeg); err == nil {
		return homebrewFFmpeg
	}
	t.Skip("ffmpeg is unavailable; skip local combine integration test")
	return ""
}

func createSyntheticSceneClips(t *testing.T, ctx context.Context, ffmpeg, dir string) []string {
	t.Helper()
	clips := make([]string, ProjectSceneCount)
	colors := []string{"red", "green", "blue", "yellow", "purple"}
	for index := range clips {
		clips[index] = filepath.Join(dir, fmt.Sprintf("scene-%d.mp4", index+1))
		if err := runCommand(ctx, ffmpeg,
			"-hide_banner", "-nostdin", "-loglevel", "error",
			"-f", "lavfi", "-i", "color=c="+colors[index]+":s=160x288:r=30",
			"-t", "6", "-an", "-c:v", "libx264", "-threads", "1", "-preset", "ultrafast", "-crf", "35", "-pix_fmt", "yuv420p", "-y", clips[index]); err != nil {
			t.Fatalf("create synthetic scene %d: %v", index+1, err)
		}
	}
	return clips
}

func waitForProjectScenes(t *testing.T, store *Store, projectID, status string) VideoProject {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		project, err := store.Project(projectID)
		if err != nil {
			t.Fatal(err)
		}
		ready := len(project.Scenes) == ProjectSceneCount
		for _, scene := range project.Scenes {
			ready = ready && scene.Status == status
		}
		if ready {
			return project
		}
		time.Sleep(10 * time.Millisecond)
	}
	project, err := store.Project(projectID)
	if err != nil {
		t.Fatal(err)
	}
	t.Fatalf("project scenes did not all reach %q: %+v", status, project.Scenes)
	return VideoProject{}
}

type workflowVideoProvider struct {
	baseURL string
	client  *http.Client
	clips   map[string]string

	mu              sync.Mutex
	nextGeneration  int
	contentRequests int
}

func (provider *workflowVideoProvider) GenerateVideo(_ context.Context, request GenerateRequest) (*Generation, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.nextGeneration++
	return &Generation{ID: fmt.Sprintf("scene_%d", provider.nextGeneration), Status: "queued", Model: request.Model}, nil
}

func (provider *workflowVideoProvider) GetGeneration(_ context.Context, id string) (*Generation, error) {
	progress := 100
	return &Generation{ID: id, Status: "completed", Progress: &progress, Model: "provider/model"}, nil
}

func (provider *workflowVideoProvider) GetVideoContent(ctx context.Context, id, _ string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, provider.baseURL+"/clips/"+id, nil)
	if err != nil {
		return nil, err
	}
	return provider.client.Do(request)
}

func (provider *workflowVideoProvider) ListVideoModels(context.Context) ([]VideoModel, error) {
	return nil, nil
}

func (provider *workflowVideoProvider) serveClip(writer http.ResponseWriter, request *http.Request) {
	id := strings.TrimPrefix(request.URL.Path, "/clips/")
	provider.mu.Lock()
	path, ok := provider.clips[id]
	if ok {
		provider.contentRequests++
	}
	provider.mu.Unlock()
	if !ok || request.Method != http.MethodGet {
		http.NotFound(writer, request)
		return
	}
	file, err := os.Open(path)
	if err != nil {
		http.Error(writer, "clip unavailable", http.StatusInternalServerError)
		return
	}
	defer file.Close()
	writer.Header().Set("Content-Type", "video/mp4")
	if _, err := io.Copy(writer, file); err != nil {
		return
	}
}

func (provider *workflowVideoProvider) contentRequestCount() int {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return provider.contentRequests
}

func probeVideo(t *testing.T, ctx context.Context, ffmpeg, path string) (int, int, float64) {
	t.Helper()
	ffprobe := filepath.Join(filepath.Dir(ffmpeg), "ffprobe")
	if _, err := os.Stat(ffprobe); err != nil {
		if path, lookErr := exec.LookPath("ffprobe"); lookErr == nil {
			ffprobe = path
		} else {
			t.Skip("ffprobe is unavailable; cannot verify local combine output")
		}
	}
	output, err := exec.CommandContext(ctx, ffprobe, "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=width,height:format=duration", "-of", "json", path).Output()
	if err != nil {
		t.Fatalf("probe final video: %v", err)
	}
	var result struct {
		Streams []struct {
			Width  int `json:"width"`
			Height int `json:"height"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("decode ffprobe response: %v", err)
	}
	if len(result.Streams) != 1 {
		t.Fatalf("video stream count = %d", len(result.Streams))
	}
	var duration float64
	if _, err := fmt.Sscanf(result.Format.Duration, "%f", &duration); err != nil {
		t.Fatalf("parse duration %q: %v", result.Format.Duration, err)
	}
	return result.Streams[0].Width, result.Streams[0].Height, duration
}
