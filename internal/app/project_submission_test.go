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
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func projectSubmissionFixture() projectSubmission {
	return projectSubmission{RequestID: "fixture-request-0001", Topic: "A seed opens at dawn", Category: RandomPromptCategoryNature, Model: "fixture/video"}
}

func submitProjectFixture(t *testing.T, app *dashboardApp, input projectSubmission) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	app.createProject(response, request)
	return response
}

func newProjectSubmissionApp(t *testing.T, store *Store, endpoint string) *dashboardApp {
	t.Helper()
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
	return &dashboardApp{store: store, security: security, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), baseURL: endpoint}
}

func projectSubmissionModels(w http.ResponseWriter) {
	_, _ = io.WriteString(w, `{"data":[{"id":"fixture/video","name":"Fixture","supported_durations":[6],"supported_resolutions":["480p"],"supported_aspect_ratios":["9:16"]}]}`)
}

type disconnectedProjectResponse struct{ header http.Header }

func (w disconnectedProjectResponse) Header() http.Header { return w.header }
func (w disconnectedProjectResponse) WriteHeader(int)     {}
func (w disconnectedProjectResponse) Write([]byte) (int, error) {
	return 0, io.ErrClosedPipe
}

func TestProjectSubmissionLostResponseSurvivesRestartWithoutProviderWork(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/videos/models" {
			t.Errorf("unexpected provider work during submission: %s", r.URL.Path)
		}
		projectSubmissionModels(w)
	}))
	defer server.Close()
	directory := t.TempDir()
	store, err := OpenStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	app := newProjectSubmissionApp(t, store, server.URL)
	input := projectSubmissionFixture()
	body, _ := json.Marshal(input)
	request := httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	app.createProject(disconnectedProjectResponse{header: make(http.Header)}, request)
	original, err := store.projectForSubmission(input.RequestID, input.requestHash())
	if err != nil || original.Status != "planning" {
		t.Fatalf("lost response lost queued project: project=%+v err=%v", original, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	// No security/provider is configured on this app: a successful replay must
	// use its durable submission before touching new provider configuration.
	app = &dashboardApp{store: reopened}
	response := submitProjectFixture(t, app, input)
	var replay VideoProject
	if response.Code != http.StatusAccepted || json.Unmarshal(response.Body.Bytes(), &replay) != nil || replay.ID != original.ID || replay.RequestID != input.RequestID {
		t.Fatalf("replay status=%d body=%s original=%s", response.Code, response.Body.String(), original.ID)
	}
	pending, err := reopened.PendingProjects(context.Background())
	if err != nil || len(pending) != 1 || pending[0].ID != original.ID || calls.Load() != 1 {
		t.Fatalf("pending=%+v calls=%d err=%v", pending, calls.Load(), err)
	}
	for _, changed := range []projectSubmission{
		{RequestID: input.RequestID, Topic: input.Topic + " later", Category: input.Category, Model: input.Model},
		{RequestID: input.RequestID, Topic: input.Topic, Category: RandomPromptCategoryKidAnimation, Model: input.Model},
		{RequestID: input.RequestID, Topic: input.Topic, Category: input.Category, Model: "other/video"},
		{RequestID: input.RequestID, Topic: input.Topic, Category: input.Category, Model: input.Model, ModalAccountID: "other-account"},
	} {
		if response := submitProjectFixture(t, app, changed); response.Code != http.StatusConflict {
			t.Fatalf("changed input replayed: status=%d body=%s", response.Code, response.Body.String())
		}
	}
	if err := reopened.UpdateProjectStatus(original.ID, "failed", "Synthetic planning failure"); err != nil {
		t.Fatal(err)
	}
	response = submitProjectFixture(t, app, input)
	if response.Code != http.StatusAccepted || json.Unmarshal(response.Body.Bytes(), &replay) != nil || replay.Status != "failed" {
		t.Fatalf("replay restarted a terminal project: status=%d body=%s", response.Code, response.Body.String())
	}
	if err := reopened.DeleteProject(original.ID); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	afterDeletion, err := OpenStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer afterDeletion.Close()
	response = submitProjectFixture(t, &dashboardApp{store: afterDeletion}, input)
	if response.Code != http.StatusConflict {
		t.Fatalf("restart lost deleted submission tombstone: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestProjectSubmissionConcurrentReplayCreatesOneProjectAndOneSnapshot(t *testing.T) {
	arrived := make(chan struct{}, 2)
	release := make(chan struct{})
	releaseRequests := sync.OnceFunc(func() { close(release) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		arrived <- struct{}{}
		<-release
		projectSubmissionModels(w)
	}))
	defer server.Close()
	defer releaseRequests()
	store := newTestStore(t)
	app := newProjectSubmissionApp(t, store, server.URL)
	input := projectSubmissionFixture()
	responses := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() { responses <- submitProjectFixture(t, app, input) }()
	}
	for range 2 {
		select {
		case <-arrived:
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent submissions did not reach model validation")
		}
	}
	releaseRequests()
	var id string
	for range 2 {
		response := <-responses
		var project VideoProject
		if response.Code != http.StatusAccepted || json.Unmarshal(response.Body.Bytes(), &project) != nil {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		if id != "" && id != project.ID {
			t.Fatalf("duplicate accepted projects: %s and %s", id, project.ID)
		}
		id = project.ID
	}
	for _, table := range []string{"video_projects", "project_submissions", "project_pipeline_events", "video_provider_configs"} {
		var count int
		if err := store.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("%s count=%d err=%v", table, count, err)
		}
	}
}

func TestProjectSubmissionConcurrentChangedInputConflictsAndNewKeyIsIndependent(t *testing.T) {
	store := newTestStore(t)
	var wait sync.WaitGroup
	errorsFound := make(chan error, 2)
	for _, topic := range []string{"First fixture", "Second fixture"} {
		wait.Add(1)
		go func(topic string) {
			defer wait.Done()
			input := projectSubmissionFixture()
			input.Topic = topic
			_, err := store.insertProjectSubmission(input.Topic, input.Category, "openrouter", "", input.Model, input.RequestID, input.requestHash())
			errorsFound <- err
		}(topic)
	}
	wait.Wait()
	close(errorsFound)
	accepted, conflicts := 0, 0
	for err := range errorsFound {
		if err == nil {
			accepted++
		} else if errors.Is(err, errProjectSubmissionConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if accepted != 1 || conflicts != 1 {
		t.Fatalf("accepted=%d conflicts=%d", accepted, conflicts)
	}
	input := projectSubmissionFixture()
	for _, requestID := range []string{"another-request-0002", "another-request-0003", "", ""} {
		if _, err := store.insertProjectSubmission(input.Topic, input.Category, "openrouter", "", input.Model, requestID, input.requestHash()); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM video_projects`).Scan(&count); err != nil || count != 5 {
		t.Fatalf("independent requests were deduplicated: count=%d err=%v", count, err)
	}
}
