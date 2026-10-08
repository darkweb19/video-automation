package app

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestSceneDownloadSkipsStaleCompletedSnapshot(t *testing.T) {
	store := newTestStore(t)
	project, err := store.InsertProject("stale scene download", "provider/model")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveStoryPlan(project.ID, validStoryPlan()); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkSceneSubmitting(project.ID, 1); err != nil {
		t.Fatal(err)
	}
	const generationID = "scene_current_generation"
	if err := store.SetSceneGeneration(project.ID, 1, Generation{ID: generationID, Status: "queued"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateScene(project.ID, 1, "downloading", "", ""); err != nil {
		t.Fatal(err)
	}
	stale, err := store.projectForWorker(context.Background(), project.ID)
	if err != nil {
		t.Fatal(err)
	}
	staleScene := stale.Scenes[0]
	if staleScene.Status != "downloading" || staleScene.ProviderGenerationID != generationID {
		t.Fatalf("expected a downloading snapshot, got %+v", staleScene)
	}

	if err := store.MarkSceneVideoReady(project.ID, 1, store.SceneVideoPath(project.ID, 1), 4); err != nil {
		t.Fatal(err)
	}
	provider := &mockProvider{content: &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("stale scene bytes")),
	}}
	processor := NewProcessor(store, nil, nil)
	processor.startSceneDownload(context.Background(), provider, staleScene)
	key := fmt.Sprintf("%s:download:%d", project.ID, staleScene.Number)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		processor.mu.Lock()
		_, active := processor.inFlight[key]
		processor.mu.Unlock()
		if !active {
			break
		}
		time.Sleep(time.Millisecond)
	}
	processor.mu.Lock()
	_, stillActive := processor.inFlight[key]
	processor.mu.Unlock()
	if stillActive {
		t.Fatal("scene download task did not finish")
	}
	if provider.contentID != "" {
		t.Fatalf("stale scene snapshot fetched provider generation %q", provider.contentID)
	}
	current, err := store.Project(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Scenes[0].Status != "completed" || current.Scenes[0].ProviderGenerationID != generationID {
		t.Fatalf("stale task changed the completed scene: %+v", current.Scenes[0])
	}
}
