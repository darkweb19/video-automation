package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func validStoryPlan() StoryPlan {
	plan := StoryPlan{Title: "A small rescue", Story: "A fox helps a bird return home.", Script: "Five connected silent scenes.", Continuity: "One red fox, one blue bird, soft clay animation, dawn forest."}
	for number := 1; number <= ProjectSceneCount; number++ {
		plan.Scenes = append(plan.Scenes, StoryPlanScene{Number: number, Title: "Scene", Script: "The characters move the story forward.", VideoPrompt: "Vertical 9:16 clay animation. One red fox and one blue bird in the same dawn forest."})
	}
	return plan
}

func TestGenerateStoryPlanUsesFreeTextModel(t *testing.T) {
	plan := validStoryPlan()
	content, _ := json.Marshal(plan)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" || r.Method != http.MethodPost {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != ScriptModel {
			t.Fatalf("model = %v", body["model"])
		}
		if body["max_completion_tokens"] != float64(storyPlanCompletionTokens) {
			t.Fatalf("max completion tokens = %v", body["max_completion_tokens"])
		}
		messages := body["messages"].([]any)
		userMessage := messages[1].(map[string]any)["content"].(string)
		if !strings.Contains(userMessage, `"video_prompt"`) || !strings.Contains(userMessage, "a fox rescue") {
			t.Fatalf("user prompt is missing topic or schema: %q", userMessage)
		}
		if _, ok := body["response_format"]; ok {
			t.Fatal("free-text route must not require structured output")
		}
		if _, ok := body["provider"]; ok {
			t.Fatal("free-text route must not require provider parameters")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "acme/free-actual", "choices": []any{map[string]any{"message": map[string]any{"content": "Here is the plan:\n```json\n" + string(content) + "\n```"}}}})
	}))
	defer server.Close()
	client := NewOpenRouterClient("test-key")
	client.BaseURL = server.URL
	generated, trace, err := client.GenerateStoryPlan(context.Background(), "a fox rescue")
	if err != nil {
		t.Fatal(err)
	}
	if len(generated.Scenes) != ProjectSceneCount || generated.Title != plan.Title {
		t.Fatalf("unexpected plan: %+v", generated)
	}
	if trace.RouterModel != ScriptModel || trace.ActualModel != "acme/free-actual" || !strings.Contains(trace.RawResponse, "```json") || trace.SystemPrompt != scriptSystemPrompt || !strings.Contains(trace.ResponseSchema, `"video_prompt"`) {
		t.Fatalf("unexpected trace: %+v", trace)
	}
}

func TestFreeTextStoryPlanRejectsMissingScene(t *testing.T) {
	plan := validStoryPlan()
	plan.Scenes = plan.Scenes[:4]
	content, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": string(content)}}}})
	}))
	defer server.Close()
	client := NewOpenRouterClient("test-key")
	client.BaseURL = server.URL
	_, trace, err := client.GenerateStoryPlan(context.Background(), "four scene story")
	if err == nil || !strings.Contains(err.Error(), "exactly 5 scenes") || trace.Status != "failed" {
		t.Fatalf("result = %v, trace = %+v", err, trace)
	}
}

func TestSceneProgressEventsAreDurableAndDeduplicated(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.InsertProject("progress story", "provider/model")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveStoryPlan(project.ID, validStoryPlan()); err != nil {
		t.Fatal(err)
	}
	for _, value := range []int{24, 25, 26, 61, 61, 49} {
		if err := store.SetSceneProgress(project.ID, 1, value); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := store.Project(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Scenes[0].Progress != 61 || loaded.Progress <= 10 {
		t.Fatalf("progress = %d, scene = %d", loaded.Progress, loaded.Scenes[0].Progress)
	}
	var messages []string
	for _, event := range loaded.PipelineEvents {
		if event.Stage == "scene_progress" {
			messages = append(messages, event.Message)
		}
	}
	if len(messages) != 2 || !strings.Contains(messages[0], "25%") || !strings.Contains(messages[1], "50%") {
		t.Fatalf("progress events = %v", messages)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reloaded, err := reopened.Project(project.ID)
	if err != nil || reloaded.Scenes[0].Progress != 61 || len(reloaded.PipelineEvents) != len(loaded.PipelineEvents) {
		t.Fatalf("progress did not survive restart: %+v / %v", reloaded, err)
	}
}

func TestProjectFailureCardPersistsSceneNumberWithoutProviderPayload(t *testing.T) {
	store := newTestStore(t)
	project, err := store.InsertProject("failure story", "provider/model")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveStoryPlan(project.ID, validStoryPlan()); err != nil {
		t.Fatal(err)
	}
	for number := 1; number <= ProjectSceneCount; number++ {
		if number == 2 {
			if err := store.MarkSceneSubmitting(project.ID, number); err != nil {
				t.Fatal(err)
			}
			if err := store.SetSceneGeneration(project.ID, number, Generation{ID: "failed_scene", Status: "failed", Error: "provider secret payload"}); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := store.MarkSceneVideoReady(project.ID, number, "saved-path", 1); err != nil {
			t.Fatal(err)
		}
	}
	project, err = store.Project(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	processor := NewProcessor(store, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	processor.processProjectScenes(context.Background(), &mockProvider{}, project)
	loaded, err := store.Project(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != "failed" || !strings.Contains(loaded.Error, "Scene 2") || strings.Contains(loaded.Error, "secret") || strings.Contains(loaded.Scenes[1].Error, "secret") {
		t.Fatalf("unsafe failure card = %+v", loaded)
	}
}

func TestProjectPollNormalizesProviderFailure(t *testing.T) {
	store := newTestStore(t)
	project, err := store.InsertProject("poll failure", "provider/model")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveStoryPlan(project.ID, validStoryPlan()); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkSceneSubmitting(project.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSceneGeneration(project.ID, 1, Generation{ID: "poll_scene", Status: "processing"}); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Project(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	provider := &mockProvider{status: &Generation{ID: "poll_scene", Status: "failed", Error: "secret raw provider response"}}
	processor := NewProcessor(store, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	processor.pollScene(context.Background(), provider, loaded.Scenes[0])
	loaded, err = store.Project(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Scenes[0].Status != "failed" || !strings.Contains(loaded.Scenes[0].Error, "provider reported") || strings.Contains(loaded.Scenes[0].Error, "secret") {
		t.Fatalf("unsafe scene failure = %+v", loaded.Scenes[0])
	}
}

func TestProjectTraceAndPipelineEventsPersistAndAreAPIVisible(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.InsertProject("auditable fox story", "minimax/k3-pro")
	if err != nil {
		t.Fatal(err)
	}
	trace := TextGenerationTrace{
		RouterModel:    ScriptModel,
		ActualModel:    "provider/free-model",
		SystemPrompt:   "exact system prompt",
		UserPrompt:     "exact user prompt",
		ResponseSchema: `{"type":"object"}`,
		RawResponse:    `{"title":"raw"}`,
		Status:         "completed",
		StartedAt:      10,
		CompletedAt:    11,
	}
	if err := store.SaveTextGenerationTrace(project.ID, trace); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendPipelineEvent(project.ID, "text_generation", "completed", "Structured response received.", 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendPipelineEventOnce(project.ID, "scene_poll", "failed", "Temporary poll failure.", 2, 0); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendPipelineEventOnce(project.ID, "scene_poll", "failed", "Temporary poll failure.", 2, 0); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Project(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.TextGeneration.ActualModel != trace.ActualModel || loaded.TextGeneration.RawResponse != trace.RawResponse || len(loaded.PipelineEvents) != 3 {
		t.Fatalf("trace/events = %+v / %+v", loaded.TextGeneration, loaded.PipelineEvents)
	}
	if loaded.PipelineEvents[0].Stage != "project" || loaded.PipelineEvents[2].SceneNumber != 2 {
		t.Fatalf("unexpected event order: %+v", loaded.PipelineEvents)
	}
	app := &dashboardApp{store: store, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/projects/"+project.ID, nil)
	request.SetPathValue("id", project.ID)
	app.project(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"text_generation"`) || !strings.Contains(recorder.Body.String(), `"pipeline_events"`) || strings.Contains(recorder.Body.String(), "Authorization") {
		t.Fatalf("project API = %d %s", recorder.Code, recorder.Body.String())
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reloaded, err := reopened.Project(project.ID)
	if err != nil || reloaded.TextGeneration.SystemPrompt != trace.SystemPrompt || len(reloaded.PipelineEvents) != 3 {
		t.Fatalf("durable trace/events = %+v / %+v / %v", reloaded.TextGeneration, reloaded.PipelineEvents, err)
	}
}

func TestTraceMigrationAddsTablesToExistingProjectDatabase(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.InsertProject("migration project", "minimax/k3-pro")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DROP TABLE project_pipeline_events; DROP TABLE project_text_generation`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.SaveTextGenerationTrace(project.ID, TextGenerationTrace{RouterModel: ScriptModel, Status: "started"}); err != nil {
		t.Fatalf("migration did not recreate trace table: %v", err)
	}
	if err := reopened.AppendPipelineEvent(project.ID, "text_request", "started", "Request built.", 0, 0); err != nil {
		t.Fatalf("migration did not recreate event table: %v", err)
	}
}

func TestGenerateStoryPlanRejectsOversizedRawResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "provider/free", "choices": []any{map[string]any{"message": map[string]any{"content": strings.Repeat("x", MaxScriptRawResponseBytes)}}}})
	}))
	defer server.Close()
	client := NewOpenRouterClient("test-key")
	client.BaseURL = server.URL
	_, trace, err := client.GenerateStoryPlan(context.Background(), "oversized")
	if err == nil || trace.Status != "failed" || !strings.Contains(trace.Error, "exceeds") || trace.RawResponse != "" {
		t.Fatalf("oversized trace/error = %+v / %v", trace, err)
	}
}

func TestGenerateStoryPlanPreservesDeadlineForSafeFailureMessage(t *testing.T) {
	client := NewOpenRouterClient("test-key")
	client.BaseURL = "http://127.0.0.1:1"
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	_, trace, err := client.GenerateStoryPlan(ctx, "deadline story")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("GenerateStoryPlan error = %v, want wrapped deadline", err)
	}
	if trace.Status != "failed" || !strings.Contains(trace.Error, context.DeadlineExceeded.Error()) {
		t.Fatalf("deadline trace = %#v", trace)
	}
	if message := safeTextGenerationFailure(err); !strings.Contains(message, "timed out") {
		t.Fatalf("safe failure message = %q", message)
	}
}

func TestGeneratingProjectFailsWhenProviderSnapshotIsUnavailable(t *testing.T) {
	store := newTestStore(t)
	project, err := store.InsertProjectForProvider("missing provider snapshot", string(VideoProviderOpenRouter), "video/model")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveStoryPlan(project.ID, validStoryPlan()); err != nil {
		t.Fatal(err)
	}

	processor := NewProcessor(store, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	processor.process(context.Background())

	loaded, err := store.Project(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != "failed" || !strings.Contains(loaded.Error, "provider configuration") {
		t.Fatalf("project should fail clearly instead of waiting forever: %#v", loaded)
	}
	found := false
	for _, event := range loaded.PipelineEvents {
		if event.Stage == "video_provider" && event.Status == "failed" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing provider failure event: %#v", loaded.PipelineEvents)
	}
}

func TestFailedScriptGenerationPersistsTraceAndFailureEvent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream unavailable"}}`))
	}))
	defer server.Close()
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := security.Encrypt("test-openrouter-key")
	if err != nil || store.SetSetting(apiKeySetting, encrypted) != nil {
		t.Fatal("save test key")
	}
	project, err := store.InsertProject("failure trace", "minimax/k3-pro")
	if err != nil {
		t.Fatal(err)
	}
	processor := NewProcessor(store, security, slog.New(slog.NewTextHandler(io.Discard, nil)))
	processor.app.baseURL = server.URL
	processor.process(context.Background())
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		loaded, loadErr := store.Project(project.ID)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if loaded.Status != "failed" {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		if loaded.TextGeneration.RouterModel != ScriptModel || loaded.TextGeneration.Status != "failed" || loaded.TextGeneration.UserPrompt == "" {
			t.Fatalf("failed trace = %+v", loaded.TextGeneration)
		}
		if !strings.Contains(loaded.Error, "HTTP 502") || strings.Contains(loaded.Error, "upstream unavailable") {
			t.Fatalf("failure card = %q", loaded.Error)
		}
		for _, event := range loaded.PipelineEvents {
			if event.Stage == "text_generation" && event.Status == "failed" {
				return
			}
		}
		t.Fatalf("script failure event missing: %+v", loaded.PipelineEvents)
	}
	t.Fatalf("script failure was not persisted")
}

func TestProjectStoragePersistsAndRetriesOnlyFailedScene(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.InsertProject("fox story", "minimax/k3-pro")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveStoryPlan(project.ID, validStoryPlan()); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkSceneSubmitting(project.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSceneGeneration(project.ID, 1, Generation{ID: "scene_one", Status: "queued"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateScene(project.ID, 1, "failed", "1.25", "provider failed"); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkSceneSubmitting(project.ID, 2); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSceneGeneration(project.ID, 2, Generation{ID: "scene_two", Status: "processing"}); err != nil {
		t.Fatal(err)
	}
	if err := store.RetryScene(project.ID, 1); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Project(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Scenes[0].Status != "pending" || loaded.Scenes[0].ProviderGenerationID != "" {
		t.Fatalf("scene one was not reset: %+v", loaded.Scenes[0])
	}
	if loaded.Scenes[1].Status != "processing" || loaded.Scenes[1].ProviderGenerationID != "scene_two" {
		t.Fatalf("scene two changed during retry: %+v", loaded.Scenes[1])
	}
	var sawSubmission, sawRetry bool
	for _, event := range loaded.PipelineEvents {
		sawSubmission = sawSubmission || (event.Stage == "scene_submission" && event.SceneNumber == 1)
		sawRetry = sawRetry || (event.Stage == "scene_retry" && event.SceneNumber == 1)
	}
	if !sawSubmission || !sawRetry {
		t.Fatalf("scene transition events missing: %+v", loaded.PipelineEvents)
	}
	if err := store.MarkSceneSubmitting(project.ID, 3); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	loaded, err = reopened.Project(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Scenes[2].Status != "failed" || !strings.Contains(loaded.Scenes[2].Error, "interrupted") {
		t.Fatalf("interrupted submission was not recovered safely: %+v", loaded.Scenes[2])
	}
}

func TestSaveStoryPlanPersistsContinuityInEveryScenePromptAndProgress(t *testing.T) {
	store := newTestStore(t)
	project, err := store.InsertProject("fox story", "minimax/k3-pro")
	if err != nil {
		t.Fatal(err)
	}
	plan := validStoryPlan()
	for index := range plan.Scenes {
		plan.Scenes[index].VideoPrompt = "A standalone shot."
	}
	if err := store.SaveStoryPlan(project.ID, plan); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkSceneSubmitting(project.ID, 1); err != nil {
		t.Fatal(err)
	}
	progress := 67
	if err := store.SetSceneGeneration(project.ID, 1, Generation{ID: "scene_one", Status: "processing", Progress: &progress}); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Project(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, scene := range loaded.Scenes {
		if !strings.Contains(scene.Prompt, "Continuity bible - apply exactly in this scene:") || !strings.Contains(scene.Prompt, plan.Continuity) {
			t.Fatalf("scene %d prompt lost continuity bible: %q", scene.Number, scene.Prompt)
		}
	}
	if loaded.Scenes[0].Progress != progress {
		t.Fatalf("provider progress = %d, want %d", loaded.Scenes[0].Progress, progress)
	}
}

func TestSaveStoryPlanRejectsPromptOverLimitAfterContinuity(t *testing.T) {
	store := newTestStore(t)
	project, err := store.InsertProject("long story", "minimax/k3-pro")
	if err != nil {
		t.Fatal(err)
	}
	plan := validStoryPlan()
	plan.Scenes[0].VideoPrompt = strings.Repeat("x", MaxPromptLength)
	if err := store.SaveStoryPlan(project.ID, plan); err == nil || !strings.Contains(err.Error(), "after continuity rules") {
		t.Fatalf("expected post-continuity prompt limit error, got %v", err)
	}
}

func TestProcessorCombinesWithoutOpenRouterKey(t *testing.T) {
	store := newTestStore(t)
	project, err := store.InsertProject("offline combine", "minimax/k3-pro")
	if err != nil {
		t.Fatal(err)
	}
	plan := validStoryPlan()
	if err := store.SaveStoryPlan(project.ID, plan); err != nil {
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
		if err := store.MarkSceneVideoReady(project.ID, number, path, 4); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.UpdateProjectStatus(project.ID, "combining", ""); err != nil {
		t.Fatal(err)
	}
	processor := NewProcessor(store, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	processor.combineRunner = func(_ context.Context, name string, args ...string) error {
		if name != "ffmpeg" {
			t.Fatalf("runner = %s", name)
		}
		return os.WriteFile(args[len(args)-1], []byte("final"), 0o600)
	}
	processor.process(context.Background())
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		loaded, loadErr := store.Project(project.ID)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if loaded.Status == "completed" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	loaded, _ := store.Project(project.ID)
	t.Fatalf("combine-only project did not complete without API key: status=%s error=%s", loaded.Status, loaded.Error)
}

func TestCombineProjectVideoBuildsNormalizedThirtySecondOutput(t *testing.T) {
	dir := t.TempDir()
	inputs := make([]string, ProjectSceneCount)
	for index := range inputs {
		inputs[index] = filepath.Join(dir, "scene-"+string(rune('1'+index))+".mp4")
		if err := os.WriteFile(inputs[index], []byte("clip"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	finalPath := filepath.Join(dir, "final.mp4")
	runner := func(_ context.Context, name string, args ...string) error {
		if name != "ffmpeg" {
			t.Fatalf("runner = %s", name)
		}
		joined := strings.Join(args, " ")
		for _, expected := range []string{"scale=1080:1920", "setsar=1", "trim=duration=6", "concat=n=5:v=1:a=0", "-an", "libx264"} {
			if !strings.Contains(joined, expected) {
				t.Fatalf("missing %q in FFmpeg args", expected)
			}
		}
		return os.WriteFile(args[len(args)-1], []byte("final-video"), 0o600)
	}
	size, err := combineProjectVideo(context.Background(), runner, inputs, finalPath)
	if err != nil {
		t.Fatal(err)
	}
	if size != int64(len("final-video")) {
		t.Fatalf("size = %d", size)
	}
	if _, err := os.Stat(finalPath); err != nil {
		t.Fatal(err)
	}
}

func TestPreferredProjectModelRequiresVerticalSixSeconds(t *testing.T) {
	models := []VideoModel{
		{ID: "other/vertical", Name: "Other", Durations: []int{6}, Resolutions: []string{"480p"}, AspectRatios: []string{"9:16"}},
		{ID: "minimax/k3-pro", Name: "MiniMax K3 Pro", Durations: []int{6}, Resolutions: []string{"480p"}, AspectRatios: []string{"9:16"}},
	}
	model, ok := preferredProjectModel(models)
	if !ok || model.ID != "other/vertical" {
		t.Fatalf("unexpected preferred model: %+v, %v", model, ok)
	}
}

func TestProjectModelRequiresConfiguredResolution(t *testing.T) {
	model := VideoModel{ID: "provider/no-resolution", Durations: []int{ProjectSceneSeconds}, AspectRatios: []string{ProjectAspectRatio}}
	if compatibleProjectModel(model) {
		t.Fatal("project model without 480p capability must be rejected")
	}
}

func TestProjectSceneSendsFixedResolution(t *testing.T) {
	store := newTestStore(t)
	project, err := store.InsertProject("topic", "provider/model")
	if err != nil {
		t.Fatal(err)
	}
	plan := StoryPlan{Title: "title", Story: "story", Script: "script", Continuity: "continuity"}
	for number := 1; number <= ProjectSceneCount; number++ {
		plan.Scenes = append(plan.Scenes, StoryPlanScene{Number: number, Title: "scene", Script: "script", VideoPrompt: "prompt"})
	}
	if err := store.SaveStoryPlan(project.ID, plan); err != nil {
		t.Fatal(err)
	}
	project, err = store.Project(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	provider := &mockProvider{generation: &Generation{ID: "gen_resolution", Status: "queued"}}
	processor := NewProcessor(store, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	processor.submitScene(context.Background(), provider, project, project.Scenes[0])
	if provider.request.Resolution != ProjectResolution || provider.request.Duration != ProjectSceneSeconds || provider.request.AspectRatio != ProjectAspectRatio {
		t.Fatalf("request = %#v", provider.request)
	}
}

func TestCreateProjectDefaultsToFirstCompatibleModel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/videos/models" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"provider/compatible","name":"Compatible","supported_durations":[6],"supported_resolutions":["480p"],"supported_aspect_ratios":["9:16"]},{"id":"minimax/k3-pro","name":"MiniMax K3 Pro","supported_durations":[6],"supported_resolutions":["480p"],"supported_aspect_ratios":["9:16"]}]}`))
	}))
	defer server.Close()
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := security.Encrypt("test-openrouter-key")
	if err != nil || store.SetSetting(apiKeySetting, encrypted) != nil {
		t.Fatal("save test key")
	}
	app := &dashboardApp{store: store, security: security, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), baseURL: server.URL}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(`{"topic":"A fox finds a lost star","model":""}`))
	request.Header.Set("Content-Type", "application/json")
	app.createProject(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var project VideoProject
	if err := json.NewDecoder(recorder.Body).Decode(&project); err != nil {
		t.Fatal(err)
	}
	if project.Model != "provider/compatible" || project.Status != "planning" {
		t.Fatalf("project = %+v", project)
	}
}

func TestCreateProjectCompatibilityErrorIncludesFixedResolution(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/videos/models" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"provider/no-480p","name":"No 480p","supported_durations":[6],"supported_resolutions":["720p"],"supported_aspect_ratios":["9:16"]}]}`))
	}))
	defer server.Close()
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := security.EncryptSetting(apiKeySetting, "test-openrouter-key")
	if err != nil || store.SetSetting(apiKeySetting, encrypted) != nil {
		t.Fatal("save test key")
	}
	app := &dashboardApp{store: store, security: security, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), baseURL: server.URL}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(`{"topic":"A fox finds a star"}`))
	request.Header.Set("Content-Type", "application/json")
	app.createProject(recorder, request)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "480p") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestProjectFinalVideoIsServedFromProjectStorage(t *testing.T) {
	store := newTestStore(t)
	project, err := store.InsertProject("topic", "minimax/k3-pro")
	if err != nil {
		t.Fatal(err)
	}
	path := store.ProjectFinalPath(project.ID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	content := []byte("final-video")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkProjectReady(project.ID, path, int64(len(content))); err != nil {
		t.Fatal(err)
	}
	app := &dashboardApp{store: store, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/projects/"+project.ID+"/video?download=1", nil)
	request.SetPathValue("id", project.ID)
	app.projectVideo(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Body.String() != string(content) {
		t.Fatalf("status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Header().Get("Content-Disposition"), project.ID) {
		t.Fatalf("content disposition = %q", recorder.Header().Get("Content-Disposition"))
	}
}
