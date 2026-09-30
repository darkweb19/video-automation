package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"
)

type failingDownloadProvider struct {
	*mockProvider
	calls int
}

func (p *failingDownloadProvider) GetVideoContent(context.Context, string, string) (*http.Response, error) {
	p.calls++
	return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: http.NoBody}, nil
}

func TestSingleVideoDownloadStopsAfterFiveAttemptsWithActionableError(t *testing.T) {
	store := newTestStore(t)
	record := GenerationRecord{ID: "single-download", Prompt: "clip", Model: "model", Status: "downloading"}
	if err := store.InsertGeneration(record); err != nil {
		t.Fatal(err)
	}
	provider := &failingDownloadProvider{mockProvider: &mockProvider{}}
	processor := NewProcessor(store, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	for range maxVideoDownloadAttempts {
		processor.download(context.Background(), provider, record)
	}
	if provider.calls != maxVideoDownloadAttempts {
		t.Fatalf("content calls = %d, want %d", provider.calls, maxVideoDownloadAttempts)
	}
	loaded, err := store.Generation(record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != "failed" || loaded.DownloadAttempts != maxVideoDownloadAttempts || loaded.NextDownloadAt != 0 {
		t.Fatalf("terminal generation state = status:%s attempts:%d next:%d", loaded.Status, loaded.DownloadAttempts, loaded.NextDownloadAt)
	}
	if !strings.Contains(loaded.Error, "5 attempts") || !strings.Contains(loaded.Error, "submit the clip again") {
		t.Fatalf("terminal error is not actionable: %q", loaded.Error)
	}
	if err := store.ScheduleDownloadRetry(record.ID); err != sql.ErrNoRows {
		t.Fatalf("terminal download scheduled another retry: %v", err)
	}
	if pending, err := store.PendingGenerations(context.Background()); err != nil || len(pending) != 0 {
		t.Fatalf("terminal generation remains pending: %#v, %v", pending, err)
	}
	events, err := store.GenerationEvents(record.ID)
	if err != nil || len(events) == 0 || events[len(events)-1].Stage != "download" || events[len(events)-1].Status != "failed" {
		t.Fatalf("terminal download event = %#v, %v", events, err)
	}
}

func TestProjectSceneDownloadFailureRollsUpAndCanBeRetried(t *testing.T) {
	store := newTestStore(t)
	project, err := store.InsertProject("retry project download", "model")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveStoryPlan(project.ID, validStoryPlan()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE project_scenes SET status='downloading',provider_generation_id='remote-scene' WHERE project_id=? AND scene_number=1`, project.ID); err != nil {
		t.Fatal(err)
	}
	for number := 2; number <= ProjectSceneCount; number++ {
		if err := store.MarkSceneVideoReady(project.ID, number, fmt.Sprintf("scene-%d.mp4", number), 1); err != nil {
			t.Fatal(err)
		}
	}

	for attempt := 1; attempt <= maxVideoDownloadAttempts; attempt++ {
		if err := store.ScheduleSceneDownloadRetry(project.ID, 1); err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		scene, err := store.Project(project.ID)
		if err != nil {
			t.Fatal(err)
		}
		got := scene.Scenes[0]
		if got.DownloadAttempts != attempt {
			t.Fatalf("attempt %d persisted as %d", attempt, got.DownloadAttempts)
		}
		if attempt < maxVideoDownloadAttempts && (got.Status != "download_failed" || got.NextAttemptAt <= time.Now().Unix()) {
			t.Fatalf("attempt %d should schedule an automatic retry: %#v", attempt, got)
		}
	}

	project, err = store.Project(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	failedScene := project.Scenes[0]
	if failedScene.Status != "failed" || failedScene.NextAttemptAt != 0 || !strings.Contains(failedScene.Error, "retry this scene") {
		t.Fatalf("terminal scene state = %#v", failedScene)
	}
	if err := store.ScheduleSceneDownloadRetry(project.ID, 1); err != sql.ErrNoRows {
		t.Fatalf("terminal scene scheduled another download retry: %v", err)
	}

	processor := NewProcessor(store, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	processor.processProjectScenes(context.Background(), nil, project)
	project, err = store.Project(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if project.Status != "failed" || !strings.Contains(project.Error, "retry it") {
		t.Fatalf("project failure did not roll up actionable scene error: status=%s error=%q", project.Status, project.Error)
	}
	if err := store.RetryScene(project.ID, 1); err != nil {
		t.Fatal(err)
	}
	project, err = store.Project(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if project.Status != "generating" || project.Scenes[0].Status != "pending" || project.Scenes[0].DownloadAttempts != 0 || project.Scenes[0].Error != "" {
		t.Fatalf("scene retry did not clear terminal download state: %#v", project.Scenes[0])
	}
}
