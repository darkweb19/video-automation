package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type clippingApifyLoopbackDoer struct {
	client   *http.Client
	loopback *url.URL
}

func (d clippingApifyLoopbackDoer) Do(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	target := *request.URL
	target.Scheme = d.loopback.Scheme
	target.Host = d.loopback.Host
	clone.URL = &target
	clone.Host = target.Host
	return d.client.Do(clone)
}

func TestClippingApifyConfigUsesOnlyVerifiedActorAndBounds(t *testing.T) {
	base := map[string]string{
		"APIFY_API_TOKEN":         "test-token",
		"APIFY_YOUTUBE_ACTOR_ID":  "thenetaji/youtube-video-downloader",
		"APIFY_REQUEST_TIMEOUT":   "45s",
		"APIFY_MAX_POLL_DURATION": "2h",
	}
	get := func(values map[string]string) func(string) string {
		return func(key string) string { return values[key] }
	}

	client := &clippingApifyClient{}
	if err := client.loadConfig(get(base)); err != nil {
		t.Fatalf("load verified actor configuration: %v", err)
	}
	if client.actorID != clippingApifyActorID || client.requestTimeout != 45*time.Second || client.maxPoll != 2*time.Hour {
		t.Fatalf("unexpected normalized Apify config: actor=%q request=%s poll=%s", client.actorID, client.requestTimeout, client.maxPoll)
	}

	for _, actor := range []string{"somebody/other-actor", "thenetaji~other-actor", ""} {
		t.Run("actor="+actor, func(t *testing.T) {
			values := map[string]string{}
			for key, value := range base {
				values[key] = value
			}
			values["APIFY_YOUTUBE_ACTOR_ID"] = actor
			invalid := &clippingApifyClient{}
			if err := invalid.loadConfig(get(values)); err == nil {
				t.Fatal("unverified actor configuration was accepted")
			}
		})
	}

	for _, invalidDuration := range []string{"0s", "6m", "nonsense"} {
		t.Run("request_timeout="+invalidDuration, func(t *testing.T) {
			values := map[string]string{}
			for key, value := range base {
				values[key] = value
			}
			values["APIFY_REQUEST_TIMEOUT"] = invalidDuration
			invalid := &clippingApifyClient{}
			if err := invalid.loadConfig(get(values)); err == nil {
				t.Fatal("out-of-range request timeout was accepted")
			}
		})
	}
}

func TestValidateClippingApifyDatasetFailsClosedOnMismatchedOrUnusableRows(t *testing.T) {
	const canonical = "https://www.youtube.com/watch?v=abcdefghijk"
	const expectedID = "abcdefghijk"
	var good []clippingApifyDatasetItem
	if err := decodeApifyJSON(strings.NewReader(`[{"id":"abcdefghijk","url":"https://www.youtube.com/watch?v=abcdefghijk","durationSeconds":900,"isLive":false,"savedFile":{"url":"https://media.example/video","contentType":"video/mp4","bytes":1234}}]`), &good); err != nil {
		t.Fatalf("decode test fixture: %v", err)
	}
	artifact, err := validateClippingApifyDataset(good, expectedID, canonical, 4096)
	if err != nil || !artifact.HasBytes || artifact.Bytes != 1234 || artifact.URL != "https://media.example/video" {
		t.Fatalf("valid actor result rejected or misread: artifact=%+v err=%v", artifact, err)
	}

	tests := []struct {
		name string
		json string
	}{
		{name: "no rows", json: `[]`},
		{name: "multiple rows", json: `[{"savedFile":{"url":"https://media.example/one"}},{"savedFile":{"url":"https://media.example/two"}}]`},
		{name: "different video id", json: `[{"id":"zyxwvutsrqp","savedFile":{"url":"https://media.example/video"}}]`},
		{name: "different video URL", json: `[{"url":"https://www.youtube.com/watch?v=zyxwvutsrqp","savedFile":{"url":"https://media.example/video"}}]`},
		{name: "live video", json: `[{"isLive":true,"savedFile":{"url":"https://media.example/video"}}]`},
		{name: "duration beyond limit", json: `[{"durationSeconds":14401,"savedFile":{"url":"https://media.example/video"}}]`},
		{name: "metadata only", json: `[{"id":"abcdefghijk"}]`},
		{name: "zero bytes", json: `[{"savedFile":{"url":"https://media.example/video","bytes":0}}]`},
		{name: "oversize artifact", json: `[{"savedFile":{"url":"https://media.example/video","bytes":4097}}]`},
		{name: "non-video artifact", json: `[{"savedFile":{"url":"https://media.example/video","contentType":"text/html"}}]`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var items []clippingApifyDatasetItem
			if err := decodeApifyJSON(strings.NewReader(test.json), &items); err != nil {
				t.Fatalf("decode fixture: %v", err)
			}
			if _, err := validateClippingApifyDataset(items, expectedID, canonical, 4096); err == nil {
				t.Fatal("invalid actor result was accepted")
			}
		})
	}
}

func TestClippingApifyPollUsesAttachedRunAndLongPollQuery(t *testing.T) {
	api := &clippingApifyHTTPTestDoer{respond: func(request *http.Request, _ int) (*http.Response, error) {
		if request.Method != http.MethodGet || request.URL.Path != "/v2/actor-runs/run_123" {
			return nil, errors.New("unexpected poll request")
		}
		if request.URL.Query().Get("waitForFinish") != "30" {
			return nil, errors.New("poll did not use the documented long-poll interval")
		}
		return clippingApifyTestResponse(http.StatusOK, nil, `{"data":{"id":"run_123","status":"SUCCEEDED","defaultDatasetId":"dataset_123"}}`), nil
	}}
	client := clippingApifyTestClient()
	client.apiDoer = api
	state := clippingApifyRunState{RunID: "run_123", DeadlineAt: time.Now().Add(time.Hour).Unix()}
	datasetID, err := client.waitForRun(context.Background(), state)
	if err != nil || datasetID != "dataset_123" {
		t.Fatalf("poll result dataset=%q err=%v", datasetID, err)
	}
	if api.requestCount() != 1 {
		t.Fatalf("poll made %d requests for immediate successful run, want 1", api.requestCount())
	}
}

func TestClippingApifyPollMapsTerminalAndAuthorizationFailures(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		status     string
		want       string
	}{
		{name: "actor failed", statusCode: http.StatusOK, status: "FAILED", want: "apify_actor_failed"},
		{name: "actor timed out", statusCode: http.StatusOK, status: "TIMED-OUT", want: "apify_actor_failed"},
		{name: "auth rejected", statusCode: http.StatusUnauthorized, want: "apify_auth_failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			api := &clippingApifyHTTPTestDoer{respond: func(_ *http.Request, _ int) (*http.Response, error) {
				if test.statusCode != http.StatusOK {
					return clippingApifyTestResponse(test.statusCode, nil, `{}`), nil
				}
				return clippingApifyTestResponse(http.StatusOK, nil, `{"data":{"id":"run_123","status":"`+test.status+`"}}`), nil
			}}
			client := clippingApifyTestClient()
			client.apiDoer = api
			state := clippingApifyRunState{RunID: "run_123", DeadlineAt: time.Now().Add(time.Hour).Unix()}
			if _, err := client.waitForRun(context.Background(), state); err == nil || safeClippingFailure(err) != test.want {
				t.Fatalf("poll failure=%v safe=%q, want %q", err, safeClippingFailure(err), test.want)
			}
			if api.requestCount() != 1 {
				t.Fatalf("terminal poll made %d API calls, want one", api.requestCount())
			}
		})
	}
}

func TestClippingApifyDeadlineAbortsKnownRunButShutdownCancellationPreservesIt(t *testing.T) {
	newAbortClient := func() (*clippingApifyClient, *clippingApifyHTTPTestDoer, *int) {
		abortCount := 0
		api := &clippingApifyHTTPTestDoer{respond: func(request *http.Request, _ int) (*http.Response, error) {
			if request.Method == http.MethodPost && request.URL.Path == "/v2/actor-runs/run_123/abort" {
				abortCount++
			}
			return clippingApifyTestResponse(http.StatusOK, nil, `{}`), nil
		}}
		client := clippingApifyTestClient()
		client.apiDoer = api
		return client, api, &abortCount
	}

	t.Run("persisted deadline expired", func(t *testing.T) {
		client, api, abortCount := newAbortClient()
		state := clippingApifyRunState{RunID: "run_123", DeadlineAt: time.Now().Add(-time.Second).Unix()}
		if _, err := client.waitForRun(context.Background(), state); err == nil || safeClippingFailure(err) != "apify_timeout" {
			t.Fatalf("expired persisted deadline failure=%v safe=%q", err, safeClippingFailure(err))
		}
		if *abortCount != 1 || api.requestCount() != 1 {
			t.Fatalf("expired run abort count=%d API calls=%d, want 1 each", *abortCount, api.requestCount())
		}
	})

	t.Run("outer import deadline expired", func(t *testing.T) {
		client, api, abortCount := newAbortClient()
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		state := clippingApifyRunState{RunID: "run_123", DeadlineAt: time.Now().Add(time.Hour).Unix()}
		if _, err := client.waitForRun(ctx, state); err == nil || safeClippingFailure(err) != "apify_timeout" {
			t.Fatalf("outer deadline failure=%v safe=%q", err, safeClippingFailure(err))
		}
		if *abortCount != 1 || api.requestCount() != 1 {
			t.Fatalf("outer deadline abort count=%d API calls=%d, want 1 each", *abortCount, api.requestCount())
		}
	})

	t.Run("app shutdown cancellation", func(t *testing.T) {
		client, api, abortCount := newAbortClient()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		state := clippingApifyRunState{RunID: "run_123", DeadlineAt: time.Now().Add(time.Hour).Unix()}
		if _, err := client.waitForRun(ctx, state); !errors.Is(err, context.Canceled) {
			t.Fatalf("shutdown returned %v, want context.Canceled", err)
		}
		if *abortCount != 0 || api.requestCount() != 0 {
			t.Fatalf("shutdown unexpectedly aborted run: abort count=%d API calls=%d", *abortCount, api.requestCount())
		}
	})
}

func TestClippingApifyControlPlaneAgainstMockHTTPServer(t *testing.T) {
	apiRequests := make(chan string, 10)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		apiRequests <- request.Method + " " + request.URL.RequestURI()
		if request.Header.Get("Authorization") != "Bearer apify-test-token" {
			t.Errorf("control-plane request did not carry the configured API token")
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/v2/acts/thenetaji~youtube-video-downloader/runs":
			if request.URL.Query().Get("waitForFinish") != "0" {
				t.Errorf("start waitForFinish=%q, want 0", request.URL.Query().Get("waitForFinish"))
			}
			if request.URL.Query().Get("timeout") != "14400" {
				t.Errorf("start timeout=%q, want persisted 14400-second run bound", request.URL.Query().Get("timeout"))
			}
			var input map[string]any
			if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
				t.Errorf("decode actor input: %v", err)
			} else {
				want := map[string]any{
					"urls":              []any{map[string]any{"url": "https://www.youtube.com/watch?v=abcdefghijk"}},
					"region":            "US",
					"includeTranscript": false,
					"saveMedia":         true,
					"quality":           "best",
					"format":            "mp4",
				}
				if !reflect.DeepEqual(input, want) {
					t.Errorf("actor input=%v, want exactly %v", input, want)
				}
			}
			flusher, ok := w.(http.Flusher)
			if !ok {
				t.Error("mock server response does not support flush")
				_, _ = io.WriteString(w, `{"data":{"id":"run_123"}}`)
				return
			}
			_, _ = io.WriteString(w, `{"data":`)
			flusher.Flush()
			select {
			case <-request.Context().Done():
				t.Errorf("start request context was canceled before the response body completed: %v", request.Context().Err())
				return
			case <-time.After(10 * time.Millisecond):
			}
			_, _ = io.WriteString(w, `{"id":"run_123","status":"READY"}}`)
		case request.Method == http.MethodGet && request.URL.Path == "/v2/actor-runs/run_123":
			if request.URL.Query().Get("waitForFinish") != "30" {
				t.Errorf("poll waitForFinish=%q, want 30", request.URL.Query().Get("waitForFinish"))
			}
			_, _ = io.WriteString(w, `{"data":{"id":"run_123","status":"SUCCEEDED","defaultDatasetId":"dataset_123"}}`)
		case request.Method == http.MethodGet && request.URL.Path == "/v2/datasets/dataset_123/items":
			query := request.URL.Query()
			if query.Get("format") != "json" || query.Get("clean") != "true" {
				t.Errorf("dataset query=%v, want format=json&clean=true", query)
			}
			_, _ = io.WriteString(w, `[{"id":"abcdefghijk","url":"https://www.youtube.com/watch?v=abcdefghijk","savedFile":{"url":"https://media.example/video","contentType":"video/mp4","bytes":1234}}]`)
		default:
			t.Errorf("unexpected control-plane request: %s %s", request.Method, request.URL.RequestURI())
			http.NotFound(w, request)
		}
	}))
	defer server.Close()
	loopback, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}

	client := clippingApifyTestClient()
	client.apiDoer = clippingApifyLoopbackDoer{client: server.Client(), loopback: loopback}
	client.apiBaseURL = clippingApifyAPIBaseURL
	run, err := client.startRun(context.Background(), "https://www.youtube.com/watch?v=abcdefghijk", 14400)
	if err != nil || run.ID != "run_123" {
		t.Fatalf("start run=%+v err=%v", run, err)
	}
	state := clippingApifyRunState{RunID: run.ID, DeadlineAt: time.Now().Add(time.Hour).Unix()}
	datasetID, err := client.waitForRun(context.Background(), state)
	if err != nil || datasetID != "dataset_123" {
		t.Fatalf("poll dataset=%q err=%v", datasetID, err)
	}
	items, err := client.readDataset(context.Background(), datasetID)
	if err != nil {
		t.Fatalf("read dataset: %v", err)
	}
	artifact, err := validateClippingApifyDataset(items, "abcdefghijk", "https://www.youtube.com/watch?v=abcdefghijk", 4096)
	if err != nil || artifact.URL != "https://media.example/video" || artifact.Bytes != 1234 {
		t.Fatalf("validate mock actor result artifact=%+v err=%v", artifact, err)
	}
	if got := len(apiRequests); got != 3 {
		t.Fatalf("mock HTTP server handled %d control-plane requests, want 3", got)
	}
}

func TestClippingApifyRejectsIPv6SiteLocalDNSAnswers(t *testing.T) {
	for _, answers := range [][]net.IPAddr{
		{{IP: net.ParseIP("fec0::1")}},
		{{IP: net.ParseIP("8.8.8.8")}, {IP: net.ParseIP("fec0::1")}},
	} {
		client := clippingApifyTestClient()
		client.resolver = &clippingApifyHTTPTestResolver{answers: map[string][]net.IPAddr{"media.example": answers}}
		if _, err := client.resolveAndValidate(context.Background(), "media.example"); err == nil {
			t.Fatalf("IPv6 site-local DNS answer was accepted: %v", answers)
		}
	}
}

type clippingApifyTestCountingBody struct {
	reader io.Reader
	reads  int
}

func (b *clippingApifyTestCountingBody) Read(p []byte) (int, error) {
	b.reads++
	return b.reader.Read(p)
}

func (*clippingApifyTestCountingBody) Close() error { return nil }

func TestClippingApifyDownloadRejectsChunkedNonVideoBeforeReadingBody(t *testing.T) {
	body := &clippingApifyTestCountingBody{reader: strings.NewReader("an unbounded HTML error page")}
	media := &clippingApifyHTTPTestDoer{respond: func(_ *http.Request, _ int) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Header:        http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
			Body:          body,
			ContentLength: -1,
		}, nil
	}}
	client := clippingApifyTestClient()
	client.mediaDoer = media
	client.resolver = &clippingApifyHTTPTestResolver{answers: map[string][]net.IPAddr{"media.example": {clippingApifyTestPublicIP("8.8.8.8")}}}
	destination := filepath.Join(t.TempDir(), "source.mp4")
	err := client.downloadMedia(context.Background(), "https://media.example/source.mp4", destination, 1024)
	if err == nil || safeClippingFailure(err) != "apify_invalid_response" {
		t.Fatalf("chunked non-video response failure=%v, want apify_invalid_response", err)
	}
	if body.reads != 0 {
		t.Fatalf("download read %d bytes from a non-video chunked response", body.reads)
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected response left a destination file: %v", err)
	}
}

func TestClippingApifyDownloadTreatsEmptyUnknownLengthBodyAsRetryable(t *testing.T) {
	media := &clippingApifyHTTPTestDoer{respond: func(_ *http.Request, _ int) (*http.Response, error) {
		return clippingApifyTestMediaResponse(http.StatusOK, http.Header{"Content-Type": []string{"video/mp4"}}, "", -1), nil
	}}
	client := clippingApifyTestClient()
	client.mediaDoer = media
	client.resolver = &clippingApifyHTTPTestResolver{answers: map[string][]net.IPAddr{"media.example": {clippingApifyTestPublicIP("8.8.8.8")}}}
	destination := filepath.Join(t.TempDir(), "source.mp4")
	err := client.downloadMedia(context.Background(), "https://media.example/source.mp4", destination, 1024)
	if err == nil || safeClippingFailure(err) != "apify_download_failed" {
		t.Fatalf("empty chunked download failure=%v, want apify_download_failed", err)
	}
	if !errors.Is(err, errClippingImportRetryable) || isPermanentClippingImportError(err) {
		t.Fatalf("empty chunked download error=%v, want retryable and non-permanent", err)
	}
	if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("empty download left a partial file: stat error=%v", statErr)
	}
}

func TestClippingApifyDownloadPreservesProgressCallbackFailure(t *testing.T) {
	media := &clippingApifyHTTPTestDoer{respond: func(_ *http.Request, _ int) (*http.Response, error) {
		return clippingApifyTestMediaResponse(http.StatusOK, http.Header{"Content-Type": []string{"video/mp4"}}, "video", -1), nil
	}}
	client := clippingApifyTestClient()
	client.mediaDoer = media
	client.resolver = &clippingApifyHTTPTestResolver{answers: map[string][]net.IPAddr{"media.example": {clippingApifyTestPublicIP("8.8.8.8")}}}
	destination := filepath.Join(t.TempDir(), "source.mp4")
	want := apifyPermanentFailure("storage_reservation_unavailable", "Storage reservation was lost.")
	err := client.downloadMediaWithProgress(
		context.Background(),
		"https://media.example/source.mp4",
		destination,
		1024,
		0,
		false,
		func(int64) error { return want },
	)
	if err == nil || safeClippingFailure(err) != "storage_reservation_unavailable" || !isPermanentClippingImportError(err) {
		t.Fatalf("progress callback failure was rewritten: err=%v safe=%q", err, safeClippingFailure(err))
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("callback failure left a partial media file: %v", err)
	}
}
