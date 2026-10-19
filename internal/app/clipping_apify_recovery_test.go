package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

const clippingApifyRecoveryMediaBody = "synthetic MP4 fixture bytes"

func newClippingApifyRecoveryTestClient(api, media *clippingApifyHTTPTestDoer) *clippingApifyClient {
	client := clippingApifyTestClient()
	client.requestTimeout = time.Second
	client.maxPoll = time.Hour
	client.apiDoer = api
	client.mediaDoer = media
	client.resolver = &clippingApifyHTTPTestResolver{answers: map[string][]net.IPAddr{
		"media.example": {clippingApifyTestPublicIP("8.8.8.8")},
	}}
	return client
}

func clippingApifyRecoveryAPIDoer() *clippingApifyHTTPTestDoer {
	return &clippingApifyHTTPTestDoer{respond: func(request *http.Request, _ int) (*http.Response, error) {
		switch {
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/runs"):
			return clippingApifyTestResponse(http.StatusCreated, nil, `{"data":{"id":"run_recovery"}}`), nil
		case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/actor-runs/run_recovery"):
			return clippingApifyTestResponse(http.StatusOK, nil, `{"data":{"id":"run_recovery","status":"SUCCEEDED","defaultDatasetId":"dataset_recovery"}}`), nil
		case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/datasets/dataset_recovery/items"):
			body := fmt.Sprintf(`[{"id":"abcdefghijk","url":"https://www.youtube.com/watch?v=abcdefghijk","durationSeconds":2,"isLive":false,"savedFile":{"url":"https://media.example/video.mp4","bytes":%d,"contentType":"video/mp4"}}]`, len(clippingApifyRecoveryMediaBody))
			return clippingApifyTestResponse(http.StatusOK, nil, body), nil
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/actor-runs/run_recovery/abort"):
			return clippingApifyTestResponse(http.StatusNoContent, nil, ""), nil
		default:
			return nil, fmt.Errorf("unexpected Apify request %s %s", request.Method, request.URL.Path)
		}
	}}
}

func clippingApifyRecoveryProbe(context.Context, string) (clippingMediaProbeResult, error) {
	return clippingMediaProbeResult{
		FormatName:       "mov,mp4,m4a,3gp,3g2,mj2",
		DurationMS:       2_000,
		Width:            320,
		Height:           240,
		AudioStreams:     1,
		VideoCodec:       "h264",
		AudioCodec:       "aac",
		VideoDurationMS:  2_000,
		AudioDurationMS:  2_000,
		VideoTimingValid: true,
		AudioTimingValid: true,
	}, nil
}

func countClippingApifyRecoveryRequests(doer *clippingApifyHTTPTestDoer, method, pathSuffix string) int {
	count := 0
	for index := 0; index < doer.requestCount(); index++ {
		request := doer.requestAt(index)
		if request.Method == method && strings.HasSuffix(request.URL.Path, pathSuffix) {
			count++
		}
	}
	return count
}

func TestClippingApifyRestartReusesAttachedRunWithoutStartingAnother(t *testing.T) {
	_, store, security, acquisition := newClippingAcquisitionFixture(t)
	source := createYouTubeImport(t, acquisition)
	claimed, err := store.BeginClippingImport(source.ID, 0, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PrepareClippingApifyRun(claimed.ID, claimed.ImportAttempts, clippingApifyActorID, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, mayDispatch, err := store.MarkClippingApifyStartUnknown(claimed.ID, claimed.ImportAttempts, clippingApifyActorID); err != nil || !mayDispatch {
		t.Fatalf("mark first actor start: mayDispatch=%v error=%v", mayDispatch, err)
	}
	if _, err := store.AttachClippingApifyRunID(claimed.ID, claimed.ImportAttempts, clippingApifyActorID, "run_recovery"); err != nil {
		t.Fatal(err)
	}
	if err := store.RetryClippingImport(claimed.ID, claimed.ImportAttempts, time.Now().Add(-time.Second).Unix(), "simulated process restart"); err != nil {
		t.Fatal(err)
	}

	api := clippingApifyRecoveryAPIDoer()
	media := &clippingApifyHTTPTestDoer{respond: func(_ *http.Request, _ int) (*http.Response, error) {
		return clippingApifyTestMediaResponse(http.StatusOK, http.Header{"Content-Type": []string{"video/mp4"}}, clippingApifyRecoveryMediaBody, int64(len(clippingApifyRecoveryMediaBody))), nil
	}}
	restarted := NewClippingAcquisition(store, security)
	restarted.apify = newClippingApifyRecoveryTestClient(api, media)
	restarted.diskAvailable = func(string) (int64, error) { return 1 << 40, nil }
	restarted.probe = clippingApifyRecoveryProbe
	worked, err := restarted.ProcessNextImport(context.Background())
	if err != nil || !worked {
		t.Fatalf("resume attached run worked=%v error=%v", worked, err)
	}
	ready, err := store.ClippingSource(source.ID)
	if err != nil || ready.Status != ClippingSourceReady || ready.SizeBytes != int64(len(clippingApifyRecoveryMediaBody)) {
		t.Fatalf("resumed import did not become Ready: source=%+v error=%v", ready, err)
	}
	state, found, err := store.GetClippingApifyRunState(source.ID)
	if err != nil || !found || state.RunID != "run_recovery" || state.StartState != clippingApifyRunAttached {
		t.Fatalf("run correlation changed during recovery: found=%v state=%+v error=%v", found, state, err)
	}
	if got := countClippingApifyRecoveryRequests(api, http.MethodPost, "/runs"); got != 0 {
		t.Fatalf("restart issued %d actor start requests; want none", got)
	}
	if got := countClippingApifyRecoveryRequests(api, http.MethodGet, "/actor-runs/run_recovery"); got != 1 {
		t.Fatalf("resumed run polls=%d, want 1", got)
	}
}

func TestClippingApifyLostStartResponseRemainsFencedAcrossRetry(t *testing.T) {
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
	api := &clippingApifyHTTPTestDoer{respond: func(_ *http.Request, _ int) (*http.Response, error) {
		return nil, errors.New("connection dropped after the actor accepted the request")
	}}
	firstClient := newClippingApifyRecoveryTestClient(api, nil)
	firstErr := firstClient.acquire(context.Background(), store, claimed, canonicalURL, tempPath, MaxClippingSourceBytes, nil)
	if safeClippingFailure(firstErr) != "apify_start_uncertain" {
		t.Fatalf("lost start response failure code=%q error=%v", safeClippingFailure(firstErr), firstErr)
	}
	state, found, err := store.GetClippingApifyRunState(source.ID)
	if err != nil || !found || state.StartState != clippingApifyStartUnknown || state.RunID != "" {
		t.Fatalf("lost response was not durably fenced as unknown: found=%v state=%+v error=%v", found, state, err)
	}
	if err := store.RetryClippingImport(claimed.ID, claimed.ImportAttempts, time.Now().Add(-time.Second).Unix(), "simulated retry"); err != nil {
		t.Fatal(err)
	}
	retried, err := store.BeginClippingImport(claimed.ID, claimed.ImportAttempts, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	secondClient := newClippingApifyRecoveryTestClient(api, nil)
	secondErr := secondClient.acquire(context.Background(), store, retried, canonicalURL, tempPath, MaxClippingSourceBytes, nil)
	if safeClippingFailure(secondErr) != "apify_start_uncertain" {
		t.Fatalf("retry did not fail closed on unknown start: failure=%q error=%v", safeClippingFailure(secondErr), secondErr)
	}
	if got := countClippingApifyRecoveryRequests(api, http.MethodPost, "/runs"); got != 1 {
		t.Fatalf("ambiguous start was submitted %d times across attempts; want exactly 1", got)
	}
	state, found, err = store.GetClippingApifyRunState(source.ID)
	if err != nil || !found || state.StartState != clippingApifyStartUnknown || state.ImportAttempt != claimed.ImportAttempts {
		t.Fatalf("retry changed ambiguous run state: found=%v state=%+v error=%v", found, state, err)
	}
}

func TestClippingApifyDeleteDuringDownloadCannotPublishReady(t *testing.T) {
	_, store, _, acquisition := newClippingAcquisitionFixture(t)
	api := clippingApifyRecoveryAPIDoer()
	mediaStarted := make(chan struct{})
	media := &clippingApifyHTTPTestDoer{respond: func(request *http.Request, _ int) (*http.Response, error) {
		close(mediaStarted)
		<-request.Context().Done()
		return nil, request.Context().Err()
	}}
	acquisition.apify = newClippingApifyRecoveryTestClient(api, media)
	acquisition.probe = clippingApifyRecoveryProbe
	source := createYouTubeImport(t, acquisition)
	type processResult struct {
		worked bool
		err    error
	}
	finished := make(chan processResult, 1)
	go func() {
		worked, err := acquisition.ProcessNextImport(context.Background())
		finished <- processResult{worked: worked, err: err}
	}()
	select {
	case <-mediaStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("Apify import never reached the media download")
	}
	if err := acquisition.CancelSource(context.Background(), source.ID); err != nil {
		t.Fatalf("cancel active source: %v", err)
	}
	select {
	case result := <-finished:
		if result.err != nil || !result.worked {
			t.Fatalf("canceled import worked=%v error=%v", result.worked, result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled import did not stop")
	}
	deleted, err := store.ClippingSource(source.ID)
	if err != nil || deleted.Status != ClippingSourceDeleted {
		t.Fatalf("canceled source was not durably deleted: source=%+v error=%v", deleted, err)
	}
	if err := store.MarkClippingSourceImportReady(source.ID, 1, int64(len(clippingApifyRecoveryMediaBody)), 2_000, strings.Repeat("a", 64)); !errors.Is(err, ErrClippingInvalidState) {
		t.Fatalf("late import result published Ready after deletion: %v", err)
	}
	if got := countClippingApifyRecoveryRequests(api, http.MethodPost, "/actor-runs/run_recovery/abort"); got != 1 {
		t.Fatalf("user delete issued %d actor abort requests, want exactly 1", got)
	}
}
