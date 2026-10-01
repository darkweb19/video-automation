package app

import "testing"

func TestStoryTraceAttemptsPersistAndMigrateExistingDatabase(t *testing.T) {
	store := newTestStore(t)
	project, err := store.InsertProject("test topic", "test/video-model")
	if err != nil {
		t.Fatal(err)
	}
	trace := TextGenerationTrace{RouterModel: ScriptModel, Status: "completed", Attempts: 2, RawResponse: "stored story audit"}
	if err := store.SaveTextGenerationTrace(project.ID, trace); err != nil {
		t.Fatal(err)
	}
	full, err := store.Project(project.ID)
	if err != nil || full.TextGeneration.Attempts != 2 {
		t.Fatalf("full trace attempts = %d, error = %v", full.TextGeneration.Attempts, err)
	}
	live, err := store.projectLiveSnapshot(project.ID)
	if err != nil || live.TextGeneration.Attempts != 2 || live.TextGeneration.RawResponse != "" {
		t.Fatalf("live trace = %+v, error = %v", live.TextGeneration, err)
	}
	// Simulate an existing installation predating the additive attempts column.
	if _, err := store.db.Exec(`ALTER TABLE project_text_generation DROP COLUMN attempts`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(store.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	full, err = reopened.Project(project.ID)
	if err != nil || full.TextGeneration.Attempts != 0 || full.TextGeneration.RawResponse != trace.RawResponse {
		t.Fatalf("migrated trace = %+v, error = %v", full.TextGeneration, err)
	}
	if err := reopened.SaveTextGenerationTrace(project.ID, trace); err != nil {
		t.Fatal(err)
	}
	full, err = reopened.Project(project.ID)
	if err != nil || full.TextGeneration.Attempts != 2 {
		t.Fatalf("updated trace attempts = %d, error = %v", full.TextGeneration.Attempts, err)
	}
}
