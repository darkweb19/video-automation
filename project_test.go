package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validStoryPlan() StoryPlan {
	plan := StoryPlan{Title: "A small rescue", Story: "A fox helps a bird return home.", Script: "Five connected silent scenes.", Continuity: "One red fox, one blue bird, soft clay animation, dawn forest."}
	for number := 1; number <= ProjectSceneCount; number++ {
		plan.Scenes = append(plan.Scenes, StoryPlanScene{Number: number, Title: "Scene", Script: "The characters move the story forward.", VideoPrompt: "Vertical 9:16 clay animation. One red fox and one blue bird in the same dawn forest."})
	}
	return plan
}

func TestGenerateStoryPlanUsesFreeStructuredModel(t *testing.T) {
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
		format := body["response_format"].(map[string]any)
		if format["type"] != "json_schema" {
			t.Fatalf("response format = %v", format["type"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": string(content)}}}})
	}))
	defer server.Close()
	client := NewOpenRouterClient("test-key")
	client.BaseURL = server.URL
	generated, err := client.GenerateStoryPlan(context.Background(), "a fox rescue")
	if err != nil {
		t.Fatal(err)
	}
	if len(generated.Scenes) != ProjectSceneCount || generated.Title != plan.Title {
		t.Fatalf("unexpected plan: %+v", generated)
	}
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
		for _, expected := range []string{"scale=1080:1920", "trim=duration=6", "concat=n=5:v=1:a=0", "-an", "libx264"} {
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
		{ID: "minimax/k3-pro", Name: "MiniMax K3 Pro", Durations: []int{5}, AspectRatios: []string{"9:16"}},
		{ID: "other/vertical", Name: "Other", Durations: []int{6}, AspectRatios: []string{"9:16"}},
	}
	model, ok := preferredProjectModel(models)
	if !ok || model.ID != "other/vertical" {
		t.Fatalf("unexpected preferred model: %+v, %v", model, ok)
	}
}
