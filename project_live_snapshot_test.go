package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLiveProjectViewsOmitRawTraceButKeepAuditMetadata(t *testing.T) {
	store := newTestStore(t)
	project, err := store.InsertProject("live project projection", "test/model")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveStoryPlan(project.ID, validStoryPlan()); err != nil {
		t.Fatal(err)
	}
	rawResponse := strings.Repeat("SSE-LIVE-RAW-NEVER-SEND-", 32*1024)
	trace := TextGenerationTrace{
		RouterModel:    ScriptModel,
		ActualModel:    "test/actual-model",
		SystemPrompt:   "system prompt remains useful in live diagnostics",
		UserPrompt:     "user prompt remains useful in live diagnostics",
		ResponseSchema: `{"type":"object"}`,
		RawResponse:    rawResponse,
		Status:         "completed",
		StartedAt:      10,
		CompletedAt:    11,
		UpdatedAt:      12,
	}
	if err := store.SaveTextGenerationTrace(project.ID, trace); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendPipelineEvent(project.ID, "text_generation", "completed", "diagnostic remains visible", 0, 0); err != nil {
		t.Fatal(err)
	}

	live, err := store.projectLiveSnapshot(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if live.TextGeneration.RawResponse != "" || !live.TextGeneration.RawResponseOmitted || live.TextGeneration.SystemPrompt != trace.SystemPrompt || live.TextGeneration.ResponseSchema != trace.ResponseSchema || len(live.PipelineEvents) < 2 {
		t.Fatalf("live project did not keep compact audit data: raw bytes=%d omitted=%v prompt=%q schema=%q events=%d", len(live.TextGeneration.RawResponse), live.TextGeneration.RawResponseOmitted, live.TextGeneration.SystemPrompt, live.TextGeneration.ResponseSchema, len(live.PipelineEvents))
	}
	full, err := store.Project(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if full.TextGeneration.RawResponse != rawResponse || full.TextGeneration.RawResponseOmitted {
		t.Fatalf("full project lost raw trace: raw bytes=%d omitted=%v", len(full.TextGeneration.RawResponse), full.TextGeneration.RawResponseOmitted)
	}

	channel, unsubscribe := store.events.subscribe()
	defer unsubscribe()
	store.PublishProject(project.ID)
	select {
	case event, open := <-channel:
		if !open || event.name != "project" {
			t.Fatalf("live event = %#v open=%v", event, open)
		}
		if bytes.Contains(event.data, []byte(rawResponse)) {
			t.Fatal("SSE project payload included the raw script response")
		}
		var payload struct {
			Project VideoProject `json:"project"`
		}
		if err := json.Unmarshal(event.data, &payload); err != nil {
			t.Fatal(err)
		}
		if !payload.Project.TextGeneration.RawResponseOmitted || payload.Project.TextGeneration.RawResponse != "" || payload.Project.TextGeneration.UserPrompt != trace.UserPrompt || len(payload.Project.PipelineEvents) < 2 {
			t.Fatalf("SSE project lost compact diagnostics: %+v", payload.Project.TextGeneration)
		}
	default:
		t.Fatal("expected a project SSE snapshot")
	}

	updatedRawResponse := strings.Repeat("SSE-RAW-CHANGED-WITHOUT-LIVE-PAYLOAD-", 24*1024)
	trace.RawResponse = updatedRawResponse
	if err := store.SaveTextGenerationTrace(project.ID, trace); err != nil {
		t.Fatal(err)
	}
	store.PublishProject(project.ID)
	select {
	case event, open := <-channel:
		t.Fatalf("raw-only trace update emitted SSE snapshot: %#v open=%v", event, open)
	default:
	}

	listed, err := store.Projects(24)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || !listed[0].TextGeneration.RawResponseOmitted || listed[0].TextGeneration.RawResponse != "" || listed[0].TextGeneration.ActualModel != trace.ActualModel || len(listed[0].PipelineEvents) < 2 {
		t.Fatalf("project list did not use compact live view: %#v", listed)
	}

	provisionTestUser(t, store)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	app := NewDashboardHandler(store, security, slog.New(slog.NewTextHandler(io.Discard, nil)))
	login := httptest.NewRecorder()
	if err := security.NewSession(login, httptest.NewRequest(http.MethodGet, "/", nil), "sujanshrestha"); err != nil {
		t.Fatal(err)
	}
	cookie := login.Result().Cookies()[0]

	listRequest := httptest.NewRequest(http.MethodGet, "/api/projects", nil)
	listRequest.AddCookie(cookie)
	listResponse := httptest.NewRecorder()
	app.ServeHTTP(listResponse, listRequest)
	if listResponse.Code != http.StatusOK || bytes.Contains(listResponse.Body.Bytes(), []byte(updatedRawResponse)) {
		t.Fatalf("project list API status=%d raw response leaked=%v", listResponse.Code, bytes.Contains(listResponse.Body.Bytes(), []byte(updatedRawResponse)))
	}
	var listPayload struct {
		Projects []VideoProject `json:"projects"`
	}
	if err := json.NewDecoder(listResponse.Body).Decode(&listPayload); err != nil {
		t.Fatal(err)
	}
	if len(listPayload.Projects) != 1 || !listPayload.Projects[0].TextGeneration.RawResponseOmitted || len(listPayload.Projects[0].PipelineEvents) < 2 {
		t.Fatalf("project list API did not retain compact diagnostics: %#v", listPayload)
	}

	detailRequest := httptest.NewRequest(http.MethodGet, "/api/projects/"+project.ID, nil)
	detailRequest.SetPathValue("id", project.ID)
	detailRequest.AddCookie(cookie)
	detailResponse := httptest.NewRecorder()
	app.ServeHTTP(detailResponse, detailRequest)
	if detailResponse.Code != http.StatusOK || !bytes.Contains(detailResponse.Body.Bytes(), []byte(updatedRawResponse)) {
		t.Fatalf("full project API status=%d raw response present=%v", detailResponse.Code, bytes.Contains(detailResponse.Body.Bytes(), []byte(updatedRawResponse)))
	}
}
