package main

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

func TestPendingProjectsUsesLeanWorkerLoadAndProjectAPIKeepsAuditDetails(t *testing.T) {
	store := newTestStore(t)
	project, err := store.InsertProjectWithProviderConfig("lean worker load", string(VideoProviderModal), "provider_snapshot", "test/model")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveStoryPlan(project.ID, validStoryPlan()); err != nil {
		t.Fatal(err)
	}
	rawResponse := strings.Repeat("raw script response ", 48*1024)
	trace := TextGenerationTrace{
		RouterModel:    ScriptModel,
		ActualModel:    "test/actual-model",
		SystemPrompt:   "system prompt retained for the API",
		UserPrompt:     "user prompt retained for the API",
		ResponseSchema: `{"type":"object"}`,
		RawResponse:    rawResponse,
		Status:         "completed",
	}
	if err := store.SaveTextGenerationTrace(project.ID, trace); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendPipelineEvent(project.ID, "text_generation", "completed", "large trace persisted", 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendPipelineEvent(project.ID, "scene_poll", "processing", "scene state persisted", 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkSceneSubmitting(project.ID, 1); err != nil {
		t.Fatal(err)
	}
	progress := 42
	if err := store.SetSceneGeneration(project.ID, 1, Generation{ID: "worker_scene_1", Status: "processing", Progress: &progress, CostUSD: "0.12"}); err != nil {
		t.Fatal(err)
	}
	readyPath := store.SceneVideoPath(project.ID, 2)
	if err := store.MarkSceneVideoReady(project.ID, 2, readyPath, 1234); err != nil {
		t.Fatal(err)
	}

	pending, err := store.PendingProjects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending projects = %d, want 1", len(pending))
	}
	workerProject := pending[0]
	if workerProject.ID != project.ID || workerProject.Status != "generating" || workerProject.Topic != "lean worker load" || workerProject.Model != "test/model" || workerProject.VideoProvider != string(VideoProviderModal) || workerProject.ProviderConfigID != "provider_snapshot" || workerProject.Title == "" || workerProject.Story == "" || workerProject.Script == "" || workerProject.Continuity == "" || len(workerProject.Scenes) != ProjectSceneCount {
		t.Fatalf("worker core project = %+v", workerProject)
	}
	if scene := workerProject.Scenes[0]; scene.Prompt == "" || scene.Status != "processing" || scene.ProviderGenerationID != "worker_scene_1" || scene.Progress != progress || scene.CostUSD != "0.12" {
		t.Fatalf("worker scene state was not retained: %+v", scene)
	}
	if scene := workerProject.Scenes[1]; !scene.VideoReady || scene.VideoPath != readyPath || scene.SizeBytes != 1234 || scene.Status != "completed" {
		t.Fatalf("worker completed scene/file state was not retained: %+v", scene)
	}
	if workerProject.Progress != 32 || workerProject.TotalCostUSD != "0.12" {
		t.Fatalf("worker summary = progress %d cost %q", workerProject.Progress, workerProject.TotalCostUSD)
	}
	if workerProject.TextGeneration.RawResponse != "" || workerProject.TextGeneration.Status != "" || len(workerProject.PipelineEvents) != 0 {
		t.Fatalf("worker load included API-only audit data: trace status=%q raw bytes=%d events=%d", workerProject.TextGeneration.Status, len(workerProject.TextGeneration.RawResponse), len(workerProject.PipelineEvents))
	}

	fullProject, err := store.Project(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fullProject.TextGeneration.RawResponse != rawResponse || fullProject.TextGeneration.ActualModel != trace.ActualModel || len(fullProject.PipelineEvents) < 3 {
		t.Fatalf("full project lost persisted audit data: raw bytes=%d model=%q events=%d", len(fullProject.TextGeneration.RawResponse), fullProject.TextGeneration.ActualModel, len(fullProject.PipelineEvents))
	}

	app := &dashboardApp{store: store, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/projects/"+project.ID, nil)
	request.SetPathValue("id", project.ID)
	app.project(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("project API status = %d: %s", recorder.Code, recorder.Body.String())
	}
	var apiProject VideoProject
	if err := json.NewDecoder(recorder.Body).Decode(&apiProject); err != nil {
		t.Fatal(err)
	}
	if apiProject.TextGeneration.RawResponse != rawResponse || len(apiProject.PipelineEvents) < 3 {
		t.Fatalf("project API omitted persisted audit data: raw bytes=%d events=%d", len(apiProject.TextGeneration.RawResponse), len(apiProject.PipelineEvents))
	}
}
