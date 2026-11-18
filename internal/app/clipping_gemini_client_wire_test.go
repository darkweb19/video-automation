package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cloud.google.com/go/auth"
	"cloud.google.com/go/auth/httptransport"
	"google.golang.org/genai"
)

const clippingGeminiWireFakeToken = "wire-test-token"

type clippingGeminiWireTokenProvider struct {
	calls atomic.Int32
	token func(context.Context) (*auth.Token, error)
}

func (p *clippingGeminiWireTokenProvider) Token(ctx context.Context) (*auth.Token, error) {
	p.calls.Add(1)
	if p.token != nil {
		return p.token(ctx)
	}
	return &auth.Token{Value: clippingGeminiWireFakeToken}, nil
}

type clippingGeminiWireRequest struct {
	method string
	url    string
	header http.Header
	body   []byte
}

type clippingGeminiWireTransport struct {
	mu       sync.Mutex
	requests []clippingGeminiWireRequest
	reply    func(*http.Request) (*http.Response, error)
}

func (t *clippingGeminiWireTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	_ = req.Body.Close()
	t.mu.Lock()
	t.requests = append(t.requests, clippingGeminiWireRequest{
		method: req.Method, url: req.URL.String(), header: req.Header.Clone(), body: body,
	})
	t.mu.Unlock()
	return t.reply(req)
}

func (t *clippingGeminiWireTransport) captured() []clippingGeminiWireRequest {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]clippingGeminiWireRequest(nil), t.requests...)
}

func newClippingGeminiWireClient(t *testing.T, config ClippingGeminiConfig, transport http.RoundTripper, provider *clippingGeminiWireTokenProvider) *clippingGeminiClient {
	t.Helper()
	client, err := newClippingGeminiClientWithDependencies(context.Background(), config, clippingGeminiClientDependencies{
		BaseTransport: transport,
		Authorize: func(config *genai.ClientConfig) error {
			config.Credentials = auth.NewCredentials(&auth.CredentialsOptions{TokenProvider: provider})
			// This is the same middleware installed by SDK UseDefaultCredentials,
			// but the synthetic TokenProvider never consults ADC or the network.
			return httptransport.AddAuthorizationMiddleware(config.HTTPClient, config.Credentials)
		},
	})
	if err != nil {
		t.Fatalf("construct wire client: %v", err)
	}
	return client
}

func clippingGeminiWireHTTPResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode:    status,
		Header:        http.Header{"Content-Type": []string{"application/json"}},
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
	}
}

type clippingGeminiWireTrackedBody struct {
	io.ReadCloser
	closed atomic.Bool
}

func (b *clippingGeminiWireTrackedBody) Close() error {
	b.closed.Store(true)
	return b.ReadCloser.Close()
}

func clippingGeminiWireEnvelope(t *testing.T, finishReason, text string) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"candidates": []any{map[string]any{
			"content":      map[string]any{"role": "model", "parts": []any{map[string]any{"text": text}}},
			"finishReason": finishReason,
		}},
		"modelVersion": "gemini-test-version",
		"responseId":   "response-test-id",
		"usageMetadata": map[string]any{
			"promptTokenCount": 100, "candidatesTokenCount": 20, "thoughtsTokenCount": 4,
			"cachedContentTokenCount": 8, "toolUsePromptTokenCount": 3, "totalTokenCount": 127,
		},
	})
	if err != nil {
		t.Fatalf("marshal response envelope: %v", err)
	}
	return string(payload)
}

func TestClippingGeminiWireRequestRoutingAndSerialization(t *testing.T) {
	// These are deliberately hostile defaults, not real credentials. The actual
	// SDK must use only the caller's project/location and fixed Google origin.
	for key, value := range map[string]string{
		"GOOGLE_API_KEY":              "ambient-google-key",
		"GEMINI_API_KEY":              "ambient-gemini-key",
		"GOOGLE_GENAI_USE_ENTERPRISE": "false",
		"GOOGLE_GENAI_USE_VERTEXAI":   "false",
		"GOOGLE_CLOUD_PROJECT":        "ambient-project",
		"GOOGLE_CLOUD_LOCATION":       "ambient-location",
		"GOOGLE_CLOUD_REGION":         "ambient-region",
		"GOOGLE_VERTEX_BASE_URL":      "https://ambient-vertex.invalid/",
		"GOOGLE_GEMINI_BASE_URL":      "https://ambient-gemini.invalid/",
	} {
		t.Setenv(key, value)
	}
	for _, location := range []string{"global", "us", "eu"} {
		t.Run(location, func(t *testing.T) {
			config := testClippingGeminiConfig()
			config.Location = location
			provider := &clippingGeminiWireTokenProvider{}
			transport := &clippingGeminiWireTransport{reply: func(*http.Request) (*http.Response, error) {
				return clippingGeminiWireHTTPResponse(http.StatusOK, clippingGeminiWireEnvelope(t, "STOP", geminiResponseTestFixture(t, "empty.json"))), nil
			}}
			client := newClippingGeminiWireClient(t, config, transport, provider)
			sdkConfig := client.sdk.ClientConfig()
			endpoint, _ := clippingGeminiEndpoint(location)
			// The SDK internally normalizes BackendEnterprise to BackendVertexAI.
			if sdkConfig.Backend != genai.BackendVertexAI || sdkConfig.Project != config.ProjectID ||
				sdkConfig.Location != location || sdkConfig.APIKey != "" || sdkConfig.HTTPOptions.BaseURL != endpoint {
				t.Fatalf("SDK did not retain explicit backend, project, location, and origin")
			}
			request := testClippingGeminiRequest("https://youtu.be/abcdefghijk?feature=share")
			got, err := client.Analyze(context.Background(), request)
			if err != nil {
				t.Fatalf("Analyze: %v", err)
			}
			want, err := DecodeClippingGeminiAnalysis([]byte(geminiResponseTestFixture(t, "empty.json")), request.Options)
			if err != nil || !reflect.DeepEqual(got.Analysis, want) {
				t.Fatalf("analysis did not preserve valid model result: error=%v", err)
			}
			assertGeminiResponseMetadata(t, got)
			requests := transport.captured()
			if len(requests) != 1 || provider.calls.Load() != 1 {
				t.Fatalf("requests = %d, token calls = %d; want one of each", len(requests), provider.calls.Load())
			}
			wire := requests[0]
			wantURL := fmt.Sprintf("%sv1/projects/%s/locations/%s/publishers/google/models/%s:generateContent", endpoint, config.ProjectID, location, clippingGeminiModel)
			if wire.method != http.MethodPost || wire.url != wantURL {
				t.Errorf("wire request = %s %s, want POST %s", wire.method, wire.url, wantURL)
			}
			if got := wire.header.Get("Authorization"); got != "Bearer "+clippingGeminiWireFakeToken {
				t.Errorf("authorization header = %q, want synthetic bearer token", got)
			}
			if got := wire.header.Get("X-Goog-Api-Key"); got != "" {
				t.Errorf("unexpected API-key header %q", got)
			}
			if containsAny(wire.url+string(wire.body)+fmt.Sprint(wire.header), "ambient-google-key", "ambient-gemini-key", "ambient-project", "ambient-location", "ambient-region", "ambient-vertex.invalid", "ambient-gemini.invalid") {
				t.Error("wire request included ambient routing or API-key settings")
			}
			var body map[string]any
			if err := json.Unmarshal(wire.body, &body); err != nil {
				t.Fatalf("decode SDK JSON: %v", err)
			}
			contents, ok := body["contents"].([]any)
			if !ok || len(contents) != 1 {
				t.Fatalf("SDK contents = %#v, want one user content", body["contents"])
			}
			content, ok := contents[0].(map[string]any)
			if !ok || content["role"] != "user" {
				t.Fatalf("SDK content = %#v, want user role", contents[0])
			}
			parts, ok := content["parts"].([]any)
			if !ok || len(parts) != 2 {
				t.Fatalf("SDK parts = %#v, want video and prompt", content["parts"])
			}
			video, ok := parts[0].(map[string]any)
			if !ok {
				t.Fatalf("SDK video part = %#v", parts[0])
			}
			file, ok := video["fileData"].(map[string]any)
			if !ok || file["fileUri"] != "https://www.youtube.com/watch?v=abcdefghijk" || file["mimeType"] != "video/mp4" || video["mediaProcessing"] != "STATIC" {
				t.Errorf("SDK video part = %#v, want canonical public YouTube video/mp4 STATIC", video)
			}
			prompt, ok := parts[1].(map[string]any)
			_, wantPrompt, err := clippingGeminiPromptSnapshot(request.Options)
			if err != nil || !ok || prompt["text"] != wantPrompt {
				t.Errorf("SDK prompt was not the selected versioned prompt: %v", err)
			}
			generation, ok := body["generationConfig"].(map[string]any)
			if !ok || generation["responseMimeType"] != "application/json" || generation["responseSchema"] == nil || generation["maxOutputTokens"] != float64(clippingGeminiMaxOutputTokens) {
				t.Errorf("SDK generation config = %#v", body["generationConfig"])
			}
			if _, exists := generation["candidateCount"]; exists {
				t.Error("SDK sent unsupported candidateCount")
			}
			// AutomaticContinuation is a local SDK control, not an API field.
			if _, exists := generation["automaticContinuation"]; exists {
				t.Error("SDK serialized its local automaticContinuation setting")
			}
		})
	}
}

func assertClippingGeminiWireAttempt(t *testing.T, err error, outcome clippingGeminiAttemptOutcome, status int, reason error) {
	t.Helper()
	var attempt *clippingGeminiAttemptError
	if !errors.As(err, &attempt) || attempt.outcome != outcome || attempt.httpStatus != status {
		t.Fatalf("attempt = %#v (%v), want outcome %v and status %d", attempt, err, outcome, status)
	}
	if !errors.Is(err, clippingGeminiOutcomeSentinel(outcome)) || (reason != nil && !errors.Is(err, reason)) {
		t.Errorf("attempt %v does not expose expected safe outcome or cause %v", err, reason)
	}
	if containsAny(err.Error(), "wire-private", "secret-token", "redirect.invalid") {
		t.Errorf("attempt leaked private provider details: %q", err)
	}
	var sdkError genai.APIError
	if errors.As(err, &sdkError) {
		t.Error("attempt retained a raw SDK error")
	}
}

func TestClippingGeminiWireDoesNotFollowRedirectOrRetry(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		outcome clippingGeminiAttemptOutcome
	}{
		{"temporary redirect", http.StatusTemporaryRedirect, clippingGeminiAttemptUnknownPostSubmission},
		{"permanent redirect", http.StatusPermanentRedirect, clippingGeminiAttemptUnknownPostSubmission},
		{"rate limit", http.StatusTooManyRequests, clippingGeminiAttemptExplicitRejection},
		{"server error", http.StatusInternalServerError, clippingGeminiAttemptUnknownPostSubmission},
		{"bad gateway", http.StatusBadGateway, clippingGeminiAttemptUnknownPostSubmission},
		{"unavailable", http.StatusServiceUnavailable, clippingGeminiAttemptUnknownPostSubmission},
		{"gateway timeout", http.StatusGatewayTimeout, clippingGeminiAttemptUnknownPostSubmission},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &clippingGeminiWireTokenProvider{}
			transport := &clippingGeminiWireTransport{reply: func(*http.Request) (*http.Response, error) {
				body := fmt.Sprintf(`{"error":{"code":%d,"message":"wire-private provider detail"}}`, tc.status)
				response := clippingGeminiWireHTTPResponse(tc.status, body)
				// Same-origin as well as off-origin redirection must not be followed;
				// this destination would pass the fixed-origin transport guard.
				response.Header.Set("Location", "https://aiplatform.googleapis.com/redirect.invalid")
				return response, nil
			}}
			client := newClippingGeminiWireClient(t, testClippingGeminiConfig(), transport, provider)
			_, err := client.Analyze(context.Background(), testClippingGeminiRequest("https://www.youtube.com/watch?v=abcdefghijk"))
			assertClippingGeminiWireAttempt(t, err, tc.outcome, tc.status, nil)
			if got := len(transport.captured()); got != 1 {
				t.Fatalf("provider calls = %d, want exactly one and no redirect or retry", got)
			}
		})
	}
}

func TestClippingGeminiWireContinuationIsNotReplayed(t *testing.T) {
	const response = `{"candidates":[{"content":{"role":"model","parts":[{"text":"wire-private fragment"}]},"finishReason":"CONTINUATION","continuationToken":"c2VjcmV0LXRva2Vu"}],"modelVersion":"gemini-test-version","responseId":"response-test-id","usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":20,"thoughtsTokenCount":4,"cachedContentTokenCount":8,"toolUsePromptTokenCount":3,"totalTokenCount":127}}`
	provider := &clippingGeminiWireTokenProvider{}
	transport := &clippingGeminiWireTransport{reply: func(*http.Request) (*http.Response, error) {
		return clippingGeminiWireHTTPResponse(http.StatusOK, response), nil
	}}
	client := newClippingGeminiWireClient(t, testClippingGeminiConfig(), transport, provider)
	result, err := client.Analyze(context.Background(), testClippingGeminiRequest("https://www.youtube.com/watch?v=abcdefghijk"))
	assertClippingGeminiWireAttempt(t, err, clippingGeminiAttemptResponseReceived, http.StatusOK, errClippingGeminiResponseTruncated)
	assertGeminiResponseMetadata(t, result)
	if got := len(transport.captured()); got != 1 {
		t.Fatalf("provider calls = %d, continuation must not trigger another request", got)
	}
}

func TestClippingGeminiWireResponseFailuresPreserveOutcome(t *testing.T) {
	const invalidAnalysis = `{"version":"wire-private invalid analysis"}`
	const blocked = `{"promptFeedback":{"blockReason":"SAFETY","blockReasonMessage":"wire-private block"},"modelVersion":"gemini-test-version","responseId":"response-test-id","usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":20,"thoughtsTokenCount":4,"cachedContentTokenCount":8,"toolUsePromptTokenCount":3,"totalTokenCount":127}}`
	tests := []struct {
		name        string
		body        string
		unknownSize bool
		outcome     clippingGeminiAttemptOutcome
		reason      error
		metadata    bool
	}{
		{"malformed envelope", `{"candidates": ["wire-private`, false, clippingGeminiAttemptUnknownPostSubmission, nil, false},
		{"declared oversized envelope", strings.Repeat("x", clippingGeminiMaxProviderResponseLen+1), false, clippingGeminiAttemptUnknownPostSubmission, errClippingGeminiResponseTooLarge, false},
		{"streaming oversized envelope", strings.Repeat("x", clippingGeminiMaxProviderResponseLen+1), true, clippingGeminiAttemptUnknownPostSubmission, errClippingGeminiResponseTooLarge, false},
		{"valid envelope invalid analysis", clippingGeminiWireEnvelope(t, "STOP", invalidAnalysis), false, clippingGeminiAttemptResponseReceived, errClippingGeminiInvalidResponse, true},
		{"valid envelope blocked", blocked, false, clippingGeminiAttemptResponseReceived, errClippingGeminiResponseBlocked, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			provider := &clippingGeminiWireTokenProvider{}
			var body *clippingGeminiWireTrackedBody
			transport := &clippingGeminiWireTransport{reply: func(*http.Request) (*http.Response, error) {
				response := clippingGeminiWireHTTPResponse(http.StatusOK, tc.body)
				body = &clippingGeminiWireTrackedBody{ReadCloser: response.Body}
				response.Body = body
				if tc.unknownSize {
					response.ContentLength = -1
				}
				return response, nil
			}}
			client := newClippingGeminiWireClient(t, testClippingGeminiConfig(), transport, provider)
			result, err := client.Analyze(context.Background(), testClippingGeminiRequest("https://www.youtube.com/watch?v=abcdefghijk"))
			assertClippingGeminiWireAttempt(t, err, tc.outcome, http.StatusOK, tc.reason)
			if tc.metadata {
				assertGeminiResponseMetadata(t, result)
			}
			if got := len(transport.captured()); got != 1 {
				t.Fatalf("provider calls = %d, want exactly one", got)
			}
			if body == nil || !body.closed.Load() {
				t.Error("provider response body was not closed")
			}
		})
	}
}

func TestClippingGeminiWireCancellationTracksSubmission(t *testing.T) {
	t.Run("already canceled", func(t *testing.T) {
		provider := &clippingGeminiWireTokenProvider{}
		transport := &clippingGeminiWireTransport{reply: func(*http.Request) (*http.Response, error) {
			return nil, errors.New("unexpected provider call")
		}}
		client := newClippingGeminiWireClient(t, testClippingGeminiConfig(), transport, provider)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := client.Analyze(ctx, testClippingGeminiRequest("https://www.youtube.com/watch?v=abcdefghijk"))
		assertClippingGeminiWireAttempt(t, err, clippingGeminiAttemptNotSubmitted, 0, context.Canceled)
		if len(transport.captured()) != 0 || provider.calls.Load() != 0 {
			t.Fatal("canceled request reached token provider or transport")
		}
	})

	t.Run("token failure", func(t *testing.T) {
		provider := &clippingGeminiWireTokenProvider{token: func(context.Context) (*auth.Token, error) {
			return nil, errors.New("wire-private credential failure")
		}}
		transport := &clippingGeminiWireTransport{reply: func(*http.Request) (*http.Response, error) {
			return nil, errors.New("unexpected provider call")
		}}
		client := newClippingGeminiWireClient(t, testClippingGeminiConfig(), transport, provider)
		_, err := client.Analyze(context.Background(), testClippingGeminiRequest("https://www.youtube.com/watch?v=abcdefghijk"))
		assertClippingGeminiWireAttempt(t, err, clippingGeminiAttemptNotSubmitted, 0, errClippingGeminiCredentialsUnavailable)
		if len(transport.captured()) != 0 || provider.calls.Load() != 1 {
			t.Fatal("failed token must be requested once without submitting the request")
		}
	})

	for _, afterSubmission := range []bool{false, true} {
		name := "while obtaining token"
		if afterSubmission {
			name = "after transport submission"
		}
		t.Run(name, func(t *testing.T) {
			started := make(chan struct{}, 1)
			provider := &clippingGeminiWireTokenProvider{}
			if !afterSubmission {
				provider.token = func(ctx context.Context) (*auth.Token, error) {
					started <- struct{}{}
					<-ctx.Done()
					return nil, ctx.Err()
				}
			}
			transport := &clippingGeminiWireTransport{reply: func(req *http.Request) (*http.Response, error) {
				started <- struct{}{}
				<-req.Context().Done()
				return nil, req.Context().Err()
			}}
			client := newClippingGeminiWireClient(t, testClippingGeminiConfig(), transport, provider)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := client.Analyze(ctx, testClippingGeminiRequest("https://www.youtube.com/watch?v=abcdefghijk"))
				done <- err
			}()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("request did not reach expected cancellation point")
			}
			cancel()
			var err error
			select {
			case err = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("canceled request did not return")
			}
			wantOutcome := clippingGeminiAttemptNotSubmitted
			wantCalls := 0
			if afterSubmission {
				wantOutcome = clippingGeminiAttemptUnknownPostSubmission
				wantCalls = 1
			}
			assertClippingGeminiWireAttempt(t, err, wantOutcome, 0, context.Canceled)
			if got := len(transport.captured()); got != wantCalls {
				t.Fatalf("provider calls = %d, want %d", got, wantCalls)
			}
		})
	}
}

func TestClippingGeminiWireUnicodeBoundaries(t *testing.T) {
	fixture := geminiResponseTestFixture(t, "empty.json")
	const summary = "A brief indistinct visual sequence."
	validEnvelope := clippingGeminiWireEnvelope(t, "STOP", fixture)
	if !strings.Contains(validEnvelope, summary) {
		t.Fatal("expected summary was not present in the response fixture")
	}
	// Go's encoding/json normally replaces malformed UTF-8 and lone surrogates
	// before the SDK returns its decoded response. Keep these bytes in the
	// outer envelope so the actual client must reject them before that occurs.
	invalidUTF8 := strings.Replace(validEnvelope, summary, "A brief "+string([]byte{0xff})+" sequence.", 1)
	invalidOuterSurrogate := strings.Replace(validEnvelope, summary, `A brief \ud800 sequence.`, 1)
	innerSurrogate := strings.Replace(fixture, summary, `A brief \ud800 sequence.`, 1)
	validInnerPair := strings.Replace(fixture, summary, `A brief \ud834\udd1e and � sequence.`, 1)
	validOuterPair := strings.Replace(validEnvelope, summary, `A brief \ud834\udd1e sequence.`, 1)

	for _, tc := range []struct {
		name        string
		body        string
		outcome     clippingGeminiAttemptOutcome
		reason      error
		wantSummary string
		metadata    bool
	}{
		{"invalid UTF-8 envelope", invalidUTF8, clippingGeminiAttemptUnknownPostSubmission, errClippingGeminiInvalidResponse, "", false},
		{"unpaired surrogate envelope", invalidOuterSurrogate, clippingGeminiAttemptUnknownPostSubmission, errClippingGeminiInvalidResponse, "", false},
		{"unpaired surrogate analysis", clippingGeminiWireEnvelope(t, "STOP", innerSurrogate), clippingGeminiAttemptResponseReceived, errClippingGeminiInvalidResponse, "", true},
		{"valid escaped analysis pair and replacement character", clippingGeminiWireEnvelope(t, "STOP", validInnerPair), 0, nil, "A brief 𝄞 and � sequence.", true},
		{"valid escaped envelope pair", validOuterPair, 0, nil, "A brief 𝄞 sequence.", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &clippingGeminiWireTokenProvider{}
			transport := &clippingGeminiWireTransport{reply: func(*http.Request) (*http.Response, error) {
				return clippingGeminiWireHTTPResponse(http.StatusOK, tc.body), nil
			}}
			client := newClippingGeminiWireClient(t, testClippingGeminiConfig(), transport, provider)
			result, err := client.Analyze(context.Background(), testClippingGeminiRequest("https://www.youtube.com/watch?v=abcdefghijk"))
			if tc.reason != nil {
				assertClippingGeminiWireAttempt(t, err, tc.outcome, http.StatusOK, tc.reason)
				if !reflect.DeepEqual(result.Analysis, ClippingGeminiAnalysis{}) {
					t.Error("malformed Unicode produced an analysis")
				}
			} else if err != nil || result.Analysis.Summary != tc.wantSummary {
				t.Fatalf("valid Unicode summary = %q, error = %v; want %q", result.Analysis.Summary, err, tc.wantSummary)
			}
			if tc.metadata {
				assertGeminiResponseMetadata(t, result)
			}
			if got := len(transport.captured()); got != 1 {
				t.Fatalf("provider calls = %d, want exactly one", got)
			}
		})
	}
}

type clippingGeminiWireCanceledBody struct {
	ctx     context.Context
	started chan struct{}
	once    sync.Once
	closed  atomic.Bool
}

func (b *clippingGeminiWireCanceledBody) Read([]byte) (int, error) {
	b.once.Do(func() { close(b.started) })
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

func (b *clippingGeminiWireCanceledBody) Close() error {
	b.closed.Store(true)
	return nil
}

func TestClippingGeminiWireCancellationDuringResponse(t *testing.T) {
	started := make(chan struct{})
	var body *clippingGeminiWireCanceledBody
	provider := &clippingGeminiWireTokenProvider{}
	transport := &clippingGeminiWireTransport{reply: func(req *http.Request) (*http.Response, error) {
		body = &clippingGeminiWireCanceledBody{ctx: req.Context(), started: started}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body, ContentLength: -1}, nil
	}}
	client := newClippingGeminiWireClient(t, testClippingGeminiConfig(), transport, provider)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := client.Analyze(ctx, testClippingGeminiRequest("https://www.youtube.com/watch?v=abcdefghijk"))
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("response body was not read")
	}
	cancel()
	var err error
	select {
	case err = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("canceled response read did not return")
	}
	assertClippingGeminiWireAttempt(t, err, clippingGeminiAttemptUnknownPostSubmission, http.StatusOK, context.Canceled)
	if got := len(transport.captured()); got != 1 {
		t.Fatalf("provider calls = %d, want exactly one", got)
	}
	if body == nil || !body.closed.Load() {
		t.Error("canceled response body was not closed")
	}
}
