package app

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestClippingApifyDeleteBetweenStartAndAttachAbortsAcknowledgedRun(t *testing.T) {
	_, store, _, acquisition := newClippingAcquisitionFixture(t)
	source := createYouTubeImport(t, acquisition)
	claimed, err := store.BeginClippingImport(source.ID, 0, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	canonicalURL, _, err := normalizeClippingImportURL("https://www.youtube.com/watch?v=abcdefghijk")
	if err != nil {
		t.Fatal(err)
	}
	tempPath, err := store.ClippingSourceTempPath(source.ID)
	if err != nil {
		t.Fatal(err)
	}

	const acknowledgedRunID = "run_attach_race"
	startCtx, cancelStart := context.WithCancel(context.Background())
	defer cancelStart()
	api := &clippingApifyHTTPTestDoer{respond: func(request *http.Request, _ int) (*http.Response, error) {
		switch {
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/runs"):
			// Commit the deletion after Apify has accepted the run but before
			// acquire can persist the acknowledged run ID.
			if _, err := store.DeleteClippingSource(source.ID); err != nil {
				return nil, err
			}
			cancelStart() // Model the import cancellation caused by user deletion.
			return clippingApifyTestResponse(http.StatusCreated, nil, `{"data":{"id":"`+acknowledgedRunID+`"}}`), nil
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/actor-runs/"+acknowledgedRunID+"/abort"):
			if request.Context().Err() != nil {
				return nil, errors.New("abort inherited the canceled import context")
			}
			deadline, ok := request.Context().Deadline()
			if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 5*time.Second {
				return nil, errors.New("abort did not use a fresh context bounded to five seconds")
			}
			return clippingApifyTestResponse(http.StatusNoContent, nil, ""), nil
		default:
			return nil, errors.New("unexpected Apify request " + request.Method + " " + request.URL.Path)
		}
	}}
	client := clippingApifyTestClient()
	client.requestTimeout = time.Second
	client.apiDoer = api

	acquireErr := client.acquire(startCtx, store, claimed, canonicalURL, tempPath, MaxClippingSourceBytes, nil)
	if acquireErr == nil {
		t.Fatal("Apify run ID unexpectedly attached after source deletion")
	}
	deleted, err := store.ClippingSource(source.ID)
	if err != nil || deleted.Status != ClippingSourceDeleted {
		t.Fatalf("source is not durably deleted: source=%+v error=%v", deleted, err)
	}
	if err := store.MarkClippingSourceImportReady(source.ID, claimed.ImportAttempts, 1, 2_000, strings.Repeat("a", 64)); !errors.Is(err, ErrClippingInvalidState) {
		t.Fatalf("deleted source could transition to Ready: %v", err)
	}
	state, found, err := store.GetClippingApifyRunState(source.ID)
	if err != nil || !found || state.StartState != clippingApifyStartUnknown || state.RunID != "" {
		t.Fatalf("deleted source unexpectedly attached a run: found=%v state=%+v error=%v", found, state, err)
	}

	startRequests, abortRequests := 0, 0
	for index := 0; index < api.requestCount(); index++ {
		request := api.requestAt(index)
		if request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/runs") {
			startRequests++
		}
		if request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/actor-runs/"+acknowledgedRunID+"/abort") {
			abortRequests++
		}
	}
	if startRequests != 1 || abortRequests != 1 {
		t.Fatalf("start requests=%d, abort requests=%d; want one paid start and one abort", startRequests, abortRequests)
	}
}
