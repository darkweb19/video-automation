package app

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProjectCategorySurvivesRestartAndWorkerLoad(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.InsertProjectWithCategory("A fox returns a lost star", RandomPromptCategoryKidAnimation, "openrouter", "", "fixture/video")
	if err != nil {
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
	loaded, err := reopened.Project(project.ID)
	if err != nil || loaded.Category != RandomPromptCategoryKidAnimation {
		t.Fatalf("loaded category=%q error=%v", loaded.Category, err)
	}
	pending, err := reopened.PendingProjects(context.Background())
	if err != nil || len(pending) != 1 || pending[0].Category != RandomPromptCategoryKidAnimation {
		t.Fatalf("pending=%+v error=%v", pending, err)
	}
	trace, err := newTextGenerationTraceForCategory(pending[0].Topic, pending[0].Category)
	if err != nil || !strings.Contains(trace.SystemPrompt, RandomPromptCategoryKidAnimation) {
		t.Fatalf("category absent from planner prompt: %q error=%v", trace.SystemPrompt, err)
	}
}

func TestProjectCategoryMigrationKeepsExistingProjects(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.InsertProject("Existing topic", "fixture/video")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`ALTER TABLE video_projects DROP COLUMN category`); err != nil {
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
	loaded, err := reopened.Project(project.ID)
	if err != nil || loaded.Category != "" || loaded.Topic != project.Topic {
		t.Fatalf("migrated=%+v error=%v", loaded, err)
	}
	newProject, err := reopened.InsertProjectWithCategory("New topic", RandomPromptCategoryNature, "openrouter", "", "fixture/video")
	if err != nil || newProject.Category != RandomPromptCategoryNature {
		t.Fatalf("new category=%q error=%v", newProject.Category, err)
	}
}

func TestCreateProjectRejectsInvalidCategoryBeforeProviderWork(t *testing.T) {
	app := &dashboardApp{}
	request := httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(`{"topic":"Valid topic","category":"unknown"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	app.createProject(recorder, request)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "category") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestCreateProjectPersistsSelectedCategory(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/videos/models" {
			t.Fatalf("unexpected provider path %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"fixture/video","name":"Fixture","supported_durations":[6],"supported_resolutions":["480p"],"supported_aspect_ratios":["9:16"]}]}`)
	}))
	defer server.Close()
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := security.EncryptSetting(apiKeySetting, "fixture-key")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(apiKeySetting, encrypted); err != nil {
		t.Fatal(err)
	}
	app := &dashboardApp{store: store, security: security, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), baseURL: server.URL}
	request := httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(`{"topic":"A seed opens at dawn","category":"Nature"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	app.createProject(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var project VideoProject
	if err := json.NewDecoder(recorder.Body).Decode(&project); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Project(project.ID)
	if err != nil || loaded.Category != RandomPromptCategoryNature || project.Category != RandomPromptCategoryNature {
		t.Fatalf("category=%q response category=%q error=%v", loaded.Category, project.Category, err)
	}
}

func TestStoryRequestUsesPersistedCategory(t *testing.T) {
	content, _ := json.Marshal(validStoryPlan())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if len(request.Messages) < 2 || !strings.Contains(request.Messages[0].Content, RandomPromptCategoryNature) {
			t.Fatalf("category missing: %+v", request.Messages)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": ScriptModel, "choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": string(content)}}}})
	}))
	defer server.Close()
	client := NewOpenRouterClient("fixture-key")
	client.BaseURL = server.URL
	_, trace, err := client.GenerateStoryPlanForCategory(context.Background(), "A seed opens at dawn", RandomPromptCategoryNature)
	if err != nil || trace.RouterModel != ScriptModel {
		t.Fatalf("trace=%+v error=%v", trace, err)
	}
}
