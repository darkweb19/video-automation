package app

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"
)

func TestPublishSkipsDatabaseReadsWithoutSubscribers(t *testing.T) {
	// A nil database makes any accidental Generation or Project read panic.
	store := &Store{events: newEventHub()}
	store.PublishGeneration("generation_without_listener")
	store.PublishProject("project_without_listener")
}

func TestIdlePublishDoesNotAllocateFingerprintCache(t *testing.T) {
	hub := &eventHub{}
	store := &Store{events: hub}
	store.PublishGeneration("generation_without_listener")
	store.PublishProject("project_without_listener")
	if hub.fingerprints != nil || hub.fingerprintLRU != nil {
		t.Fatalf("idle publish initialized fingerprint cache: map=%v lru=%v", hub.fingerprints, hub.fingerprintLRU)
	}
}

func TestGenerationSnapshotsIgnoreUpdatedAtButKeepMeaningfulChanges(t *testing.T) {
	store := newTestStore(t)
	record := GenerationRecord{
		ID:       "generation_event_cost",
		Prompt:   "A short test prompt",
		Model:    "test/model",
		Status:   "queued",
		Progress: 4,
	}
	if err := store.InsertGeneration(record); err != nil {
		t.Fatal(err)
	}
	channel, unsubscribe := store.events.subscribe()
	defer unsubscribe()

	store.PublishGeneration(record.ID)
	first := nextDashboardEvent(t, channel, "generation")
	var firstPayload struct {
		ID         string           `json:"id"`
		Generation GenerationRecord `json:"generation"`
	}
	if err := json.Unmarshal(first.data, &firstPayload); err != nil {
		t.Fatal(err)
	}
	if firstPayload.ID != record.ID || firstPayload.Generation.Prompt != record.Prompt {
		t.Fatalf("initial full snapshot = %+v", firstPayload)
	}

	if _, err := store.db.Exec(`UPDATE generations SET updated_at=updated_at+100 WHERE id=?`, record.ID); err != nil {
		t.Fatal(err)
	}
	store.PublishGeneration(record.ID)
	assertNoDashboardEvent(t, channel)

	updates := []struct {
		name     string
		status   string
		cost     string
		message  string
		error    string
		progress int
	}{
		{name: "progress", status: "processing", progress: 27},
		{name: "cost without a progress change", status: "processing", cost: "0.42", progress: 27},
		{name: "error without a progress change", status: "processing", cost: "0.42", message: "provider is still working", error: "Video provider reported a generation failure", progress: 27},
		{name: "status without a progress change", status: "failed", cost: "0.42", message: "provider is still working", error: "Video provider reported a generation failure", progress: 27},
	}
	for _, update := range updates {
		t.Run(update.name, func(t *testing.T) {
			if err := store.UpdateGenerationProgress(record.ID, update.status, "test/model", update.cost, update.message, update.progress); err != nil {
				t.Fatal(err)
			}
			store.PublishGeneration(record.ID)
			event := nextDashboardEvent(t, channel, "generation")
			var payload struct {
				Generation GenerationRecord `json:"generation"`
			}
			if err := json.Unmarshal(event.data, &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Generation.Progress != update.progress || payload.Generation.Status != update.status || payload.Generation.CostUSD != update.cost || payload.Generation.Error != update.error {
				t.Fatalf("meaningful update was not preserved: %+v", payload.Generation)
			}
		})
	}
}

func TestProjectSnapshotsIgnorePollTimestampsButKeepPlanAndSceneChanges(t *testing.T) {
	store := newTestStore(t)
	project, err := store.InsertProject("cost test", "test/model")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveStoryPlan(project.ID, validStoryPlan()); err != nil {
		t.Fatal(err)
	}
	channel, unsubscribe := store.events.subscribe()
	defer unsubscribe()

	store.PublishProject(project.ID)
	initial := nextDashboardEvent(t, channel, "project")
	var initialPayload struct {
		ID      string       `json:"id"`
		Project VideoProject `json:"project"`
	}
	if err := json.Unmarshal(initial.data, &initialPayload); err != nil {
		t.Fatal(err)
	}
	if initialPayload.ID != project.ID || len(initialPayload.Project.Scenes) != ProjectSceneCount || len(initialPayload.Project.PipelineEvents) == 0 {
		t.Fatalf("initial full project snapshot = %+v", initialPayload)
	}

	if _, err := store.db.Exec(`UPDATE video_projects SET updated_at=updated_at+100 WHERE id=?`, project.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE project_scenes SET updated_at=updated_at+100 WHERE project_id=?`, project.ID); err != nil {
		t.Fatal(err)
	}
	store.PublishProject(project.ID)
	assertNoDashboardEvent(t, channel)

	// Cost and error change while status/progress stay fixed. Fingerprinting the
	// whole semantic snapshot must retain these fields rather than keying on
	// progress alone.
	if _, err := store.db.Exec(`UPDATE project_scenes SET cost_usd='0.75',error='scene provider issue' WHERE project_id=? AND scene_number=1`, project.ID); err != nil {
		t.Fatal(err)
	}
	store.PublishProject(project.ID)
	costAndError := nextDashboardEvent(t, channel, "project")
	var costPayload struct {
		Project VideoProject `json:"project"`
	}
	if err := json.Unmarshal(costAndError.data, &costPayload); err != nil {
		t.Fatal(err)
	}
	if got := costPayload.Project.Scenes[0]; got.CostUSD != "0.75" || got.Error != "scene provider issue" {
		t.Fatalf("scene cost/error were not preserved: %+v", got)
	}

	if _, err := store.db.Exec(`UPDATE project_scenes SET prompt='revised scene plan' WHERE project_id=? AND scene_number=1`, project.ID); err != nil {
		t.Fatal(err)
	}
	store.PublishProject(project.ID)
	planChange := nextDashboardEvent(t, channel, "project")
	var planPayload struct {
		Project VideoProject `json:"project"`
	}
	if err := json.Unmarshal(planChange.data, &planPayload); err != nil {
		t.Fatal(err)
	}
	if got := planPayload.Project.Scenes[0].Prompt; got != "revised scene plan" {
		t.Fatalf("updated scene prompt = %q", got)
	}

	if err := store.AppendPipelineEvent(project.ID, "test", "visible", "new pipeline event", 0, 0); err != nil {
		t.Fatal(err)
	}
	store.PublishProject(project.ID)
	pipelineChange := nextDashboardEvent(t, channel, "project")
	var pipelinePayload struct {
		Project VideoProject `json:"project"`
	}
	if err := json.Unmarshal(pipelineChange.data, &pipelinePayload); err != nil {
		t.Fatal(err)
	}
	events := pipelinePayload.Project.PipelineEvents
	if events[len(events)-1].Message != "new pipeline event" {
		t.Fatalf("pipeline event missing from snapshot: %+v", events)
	}
}

func TestGenerationVaultFilteringEvictsCachedSnapshot(t *testing.T) {
	store := newTestStore(t)
	record := GenerationRecord{ID: "generation_vault_filter", Prompt: "vault test", Model: "test/model", Status: "queued"}
	if err := store.InsertGeneration(record); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkVideoReady(record.ID, store.VideoPath(record.ID), 12); err != nil {
		t.Fatal(err)
	}
	channel, unsubscribe := store.events.subscribe()
	defer unsubscribe()

	store.PublishGeneration(record.ID)
	nextDashboardEvent(t, channel, "generation")
	if err := store.SetGenerationVaulted(record.ID, true); err != nil {
		t.Fatal(err)
	}
	store.PublishGeneration(record.ID)
	assertNoDashboardEvent(t, channel)
	if err := store.SetGenerationVaulted(record.ID, false); err != nil {
		t.Fatal(err)
	}
	store.PublishGeneration(record.ID)
	if event := nextDashboardEvent(t, channel, "generation"); event.name != "generation" {
		t.Fatalf("restored snapshot event = %q", event.name)
	}
}

func TestEventFingerprintCacheBoundedAndResetsAfterLastSubscriber(t *testing.T) {
	hub := newEventHub()
	channel, unsubscribe := hub.subscribe()
	lastID := ""
	lastHash := sha256.Sum256([]byte("last semantic snapshot"))
	for index := 0; index < eventFingerprintCacheLimit+8; index++ {
		id := fmt.Sprintf("generation_%d", index)
		hash := sha256.Sum256([]byte(id))
		hub.publishSnapshot("generation", id, hash, map[string]string{"id": id})
		nextDashboardEvent(t, channel, "generation")
		lastID, lastHash = id, hash
	}
	if got := len(hub.fingerprints); got != eventFingerprintCacheLimit {
		t.Fatalf("fingerprint cache entries = %d, want %d", got, eventFingerprintCacheLimit)
	}
	if hub.fingerprintLRU.Len() != eventFingerprintCacheLimit {
		t.Fatalf("fingerprint LRU entries = %d, want %d", hub.fingerprintLRU.Len(), eventFingerprintCacheLimit)
	}

	unsubscribe()
	if len(hub.fingerprints) != 0 || hub.fingerprintLRU.Len() != 0 {
		t.Fatalf("fingerprints survived last unsubscribe: cache=%d lru=%d", len(hub.fingerprints), hub.fingerprintLRU.Len())
	}

	reconnected, unsubscribeAgain := hub.subscribe()
	defer unsubscribeAgain()
	hub.publishSnapshot("generation", lastID, lastHash, map[string]string{"id": lastID, "state": "authoritative"})
	event := nextDashboardEvent(t, reconnected, "generation")
	var payload map[string]string
	if err := json.Unmarshal(event.data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["state"] != "authoritative" {
		t.Fatalf("reconnect did not receive current full snapshot: %v", payload)
	}
}

func TestEventQueueOverflowDrainsDisconnectsAndAllowsAuthoritativeReconnect(t *testing.T) {
	hub := newEventHub()
	channel, unsubscribe := hub.subscribe()
	for index := 0; index < eventClientBuffer; index++ {
		id := fmt.Sprintf("generation_queued_%d", index)
		hash := sha256.Sum256([]byte(id))
		hub.publishSnapshot("generation", id, hash, map[string]string{"id": id})
	}
	if got := len(channel); got != eventClientBuffer {
		t.Fatalf("queued events = %d, want %d before overflow", got, eventClientBuffer)
	}

	latestID := "generation_latest_after_overflow"
	latestHash := sha256.Sum256([]byte("latest state"))
	hub.publishSnapshot("generation", latestID, latestHash, map[string]string{"id": latestID})
	if got := len(channel); got != 0 {
		t.Fatalf("stale queued events after overflow = %d, want 0", got)
	}
	if _, open := <-channel; open {
		t.Fatal("overflowed subscriber channel remained open")
	}
	if len(hub.clients) != 0 || len(hub.fingerprints) != 0 {
		t.Fatalf("overflow did not clear disconnected client/cache: clients=%d fingerprints=%d", len(hub.clients), len(hub.fingerprints))
	}
	unsubscribe() // The deferred request cleanup must be safe after overflow.

	reconnected, unsubscribeAgain := hub.subscribe()
	defer unsubscribeAgain()
	hub.publishSnapshot("generation", latestID, latestHash, map[string]string{"id": latestID, "state": "latest"})
	latest := nextDashboardEvent(t, reconnected, "generation")
	var payload map[string]string
	if err := json.Unmarshal(latest.data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["id"] != latestID || payload["state"] != "latest" {
		t.Fatalf("reconnected event = %v", payload)
	}
}

func nextDashboardEvent(t *testing.T, channel <-chan dashboardEvent, wantName string) dashboardEvent {
	t.Helper()
	select {
	case event, open := <-channel:
		if !open {
			t.Fatal("event channel closed before expected snapshot")
		}
		if event.name != wantName {
			t.Fatalf("event name = %q, want %q", event.name, wantName)
		}
		return event
	default:
		t.Fatal("expected snapshot event was not queued")
		return dashboardEvent{}
	}
}

func assertNoDashboardEvent(t *testing.T, channel <-chan dashboardEvent) {
	t.Helper()
	select {
	case event, open := <-channel:
		if open {
			t.Fatalf("unexpected snapshot event: %+v", event)
		}
	default:
	}
}
