package app

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type clippingApifyHTTPTestDoer struct {
	mu       sync.Mutex
	requests []*http.Request
	respond  func(*http.Request, int) (*http.Response, error)
}

func (d *clippingApifyHTTPTestDoer) Do(request *http.Request) (*http.Response, error) {
	d.mu.Lock()
	d.requests = append(d.requests, request)
	index := len(d.requests) - 1
	d.mu.Unlock()
	return d.respond(request, index)
}

func (d *clippingApifyHTTPTestDoer) requestCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.requests)
}

func (d *clippingApifyHTTPTestDoer) requestAt(index int) *http.Request {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.requests[index]
}

type clippingApifyHTTPTestResolver struct {
	answers map[string][]net.IPAddr
	err     error
	mu      sync.Mutex
	calls   []string
}

type clippingApifyHTTPTestDelayedBody struct {
	ctx         context.Context
	ready       chan struct{}
	release     chan struct{}
	releaseOnce sync.Once
	reader      io.Reader
}

func (b *clippingApifyHTTPTestDelayedBody) Read(p []byte) (int, error) {
	select {
	case <-b.ready:
	default:
		close(b.ready)
	}
	select {
	case <-b.release:
		return b.reader.Read(p)
	case <-b.ctx.Done():
		return 0, b.ctx.Err()
	}
}

func (b *clippingApifyHTTPTestDelayedBody) Unblock() {
	b.releaseOnce.Do(func() { close(b.release) })
}

func (b *clippingApifyHTTPTestDelayedBody) Close() error {
	b.Unblock()
	return nil
}

type clippingApifyHTTPTestCloseErrorBody struct {
	io.Reader
	err error
}

func (b clippingApifyHTTPTestCloseErrorBody) Close() error { return b.err }

func (r *clippingApifyHTTPTestResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, host)
	if r.err != nil {
		return nil, r.err
	}
	answers := r.answers[host]
	return append([]net.IPAddr(nil), answers...), nil
}

func (r *clippingApifyHTTPTestResolver) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func clippingApifyTestResponse(status int, headers http.Header, body string) *http.Response {
	if headers == nil {
		headers = make(http.Header)
	}
	return &http.Response{
		StatusCode:    status,
		Header:        headers,
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
	}
}

func clippingApifyTestMediaResponse(status int, headers http.Header, body string, contentLength int64) *http.Response {
	response := clippingApifyTestResponse(status, headers, body)
	response.ContentLength = contentLength
	return response
}

func clippingApifyTestClient() *clippingApifyClient {
	return &clippingApifyClient{
		token:          "apify-test-token",
		actorID:        clippingApifyActorID,
		region:         clippingApifyRegion,
		requestTimeout: time.Minute,
		maxPoll:        time.Hour,
		apiBaseURL:     clippingApifyAPIBaseURL,
	}
}

func clippingApifyTestPublicIP(value string) net.IPAddr {
	return net.IPAddr{IP: net.ParseIP(value)}
}

func TestValidateApifyMediaURLRejectsMalformedOrNonPublicShapes(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "empty", raw: ""},
		{name: "leading whitespace", raw: " https://media.example/video.mp4"},
		{name: "HTTP", raw: "http://media.example/video.mp4"},
		{name: "non-443 port", raw: "https://media.example:8443/video.mp4"},
		{name: "userinfo", raw: "https://user:password@media.example/video.mp4"},
		{name: "IPv4 literal", raw: "https://8.8.8.8/video.mp4"},
		{name: "IPv6 literal", raw: "https://[2606:4700:4700::1111]/video.mp4"},
		{name: "fragment", raw: "https://media.example/video.mp4#download"},
		{name: "malformed escape", raw: "https://media.example/%zz"},
		{name: "opaque URL", raw: "https:media.example/video.mp4"},
		{name: "trailing-dot host", raw: "https://media.example./video.mp4"},
		{name: "host percent escape", raw: "https://media%2eexample/video.mp4"},
		{name: "control character", raw: "https://media.example/video.mp4\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if parsed, err := validateApifyMediaURL(test.raw); err == nil {
				t.Fatalf("validateApifyMediaURL(%q) = %v, want rejection", test.raw, parsed)
			}
		})
	}

	valid, err := validateApifyMediaURL("https://Media.Example:443/video.mp4?signature=private-capability")
	if err != nil {
		t.Fatalf("valid HTTPS artifact URL rejected: %v", err)
	}
	if valid.Hostname() != "media.example" || valid.Port() != "443" || valid.Fragment != "" {
		t.Fatalf("valid artifact URL was not normalized safely: host=%q port=%q fragment=%q", valid.Hostname(), valid.Port(), valid.Fragment)
	}
}

func TestClippingApifyResolveAndValidateChecksEveryDNSAnswer(t *testing.T) {
	tests := []struct {
		name    string
		answers []net.IPAddr
		wantErr bool
	}{
		{name: "public IPv4", answers: []net.IPAddr{clippingApifyTestPublicIP("8.8.8.8")}},
		{name: "public IPv6", answers: []net.IPAddr{clippingApifyTestPublicIP("2606:4700:4700::1111")}},
		{name: "all public answers", answers: []net.IPAddr{clippingApifyTestPublicIP("8.8.8.8"), clippingApifyTestPublicIP("2606:4700:4700::1111")}},
		{name: "loopback", answers: []net.IPAddr{clippingApifyTestPublicIP("127.0.0.1")}, wantErr: true},
		{name: "private", answers: []net.IPAddr{clippingApifyTestPublicIP("10.10.0.7")}, wantErr: true},
		{name: "link local", answers: []net.IPAddr{clippingApifyTestPublicIP("169.254.1.8")}, wantErr: true},
		{name: "IPv6 unique local", answers: []net.IPAddr{clippingApifyTestPublicIP("fd00::7")}, wantErr: true},
		{name: "IPv4 mapped private", answers: []net.IPAddr{clippingApifyTestPublicIP("::ffff:127.0.0.1")}, wantErr: true},
		{name: "mixed public and private", answers: []net.IPAddr{clippingApifyTestPublicIP("8.8.8.8"), clippingApifyTestPublicIP("192.168.1.4")}, wantErr: true},
		{name: "IPv6 zone", answers: []net.IPAddr{{IP: net.ParseIP("2606:4700:4700::1111"), Zone: "eth0"}}, wantErr: true},
		{name: "empty DNS answer", answers: nil, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolver := &clippingApifyHTTPTestResolver{answers: map[string][]net.IPAddr{"media.example": test.answers}}
			client := clippingApifyTestClient()
			client.resolver = resolver
			addresses, err := client.resolveAndValidate(context.Background(), "media.example")
			if (err != nil) != test.wantErr {
				t.Fatalf("resolveAndValidate error=%v, wantErr=%v", err, test.wantErr)
			}
			if !test.wantErr && len(addresses) != len(test.answers) {
				t.Fatalf("validated addresses=%v, want %v", addresses, test.answers)
			}
		})
	}

	resolver := &clippingApifyHTTPTestResolver{err: errors.New("DNS failure")}
	client := clippingApifyTestClient()
	client.resolver = resolver
	if _, err := client.resolveAndValidate(context.Background(), "media.example"); err == nil {
		t.Fatal("DNS lookup error was accepted")
	}
}

func TestClippingApifyMediaRedirectRevalidatesEachHop(t *testing.T) {
	resolver := &clippingApifyHTTPTestResolver{answers: map[string][]net.IPAddr{
		"first.example":  {clippingApifyTestPublicIP("8.8.8.8")},
		"second.example": {clippingApifyTestPublicIP("1.1.1.1")},
		"mixed.example":  {clippingApifyTestPublicIP("1.1.1.1"), clippingApifyTestPublicIP("10.0.0.5")},
	}}
	media := &clippingApifyHTTPTestDoer{respond: func(_ *http.Request, index int) (*http.Response, error) {
		if index == 0 {
			return clippingApifyTestResponse(http.StatusFound, http.Header{"Location": []string{"https://second.example/video.mp4?sig=opaque"}}, ""), nil
		}
		return clippingApifyTestResponse(http.StatusFound, http.Header{"Location": []string{"https://mixed.example/video.mp4?sig=opaque"}}, ""), nil
	}}
	client := clippingApifyTestClient()
	client.resolver = resolver
	client.mediaDoer = media
	destination := filepath.Join(t.TempDir(), "source.mp4")
	err := client.downloadMedia(context.Background(), "https://first.example/video.mp4", destination, 1024)
	if err == nil {
		t.Fatal("redirect to a hostname with a private DNS answer was accepted")
	}
	if got := media.requestCount(); got != 2 {
		t.Fatalf("media requests=%d, want only the original and one validated redirect", got)
	}
	if got := resolver.callCount(); got != 3 {
		t.Fatalf("DNS validations=%d, want a fresh validation for every requested hop", got)
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsafe redirect left an artifact at destination: stat error=%v", err)
	}
}

func TestClippingApifyMediaRejectsUnsafeRedirectTargets(t *testing.T) {
	unsafeTargets := []struct {
		name   string
		value  string
		answer []net.IPAddr
	}{
		{name: "HTTP", value: "http://second.example/video.mp4"},
		{name: "non-443 port", value: "https://second.example:8443/video.mp4"},
		{name: "userinfo", value: "https://user@second.example/video.mp4"},
		{name: "IP literal", value: "https://127.0.0.1/video.mp4"},
		{name: "fragment", value: "https://second.example/video.mp4#fragment"},
		{name: "private DNS", value: "https://second.example/video.mp4", answer: []net.IPAddr{clippingApifyTestPublicIP("127.0.0.1")}},
	}
	for _, test := range unsafeTargets {
		t.Run(test.name, func(t *testing.T) {
			resolver := &clippingApifyHTTPTestResolver{answers: map[string][]net.IPAddr{
				"first.example":  {clippingApifyTestPublicIP("8.8.8.8")},
				"second.example": test.answer,
			}}
			media := &clippingApifyHTTPTestDoer{respond: func(_ *http.Request, _ int) (*http.Response, error) {
				return clippingApifyTestResponse(http.StatusFound, http.Header{"Location": []string{test.value}}, ""), nil
			}}
			client := clippingApifyTestClient()
			client.resolver = resolver
			client.mediaDoer = media
			destination := filepath.Join(t.TempDir(), "source.mp4")
			if err := client.downloadMedia(context.Background(), "https://first.example/video.mp4", destination, 1024); err == nil {
				t.Fatal("unsafe redirect target was accepted")
			}
			if got := media.requestCount(); got != 1 {
				t.Fatalf("media requests=%d, want redirect target rejected before request", got)
			}
			if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unsafe redirect left destination: stat error=%v", err)
			}
		})
	}
}

func TestClippingApifyMediaRedirectCountIsBounded(t *testing.T) {
	resolver := &clippingApifyHTTPTestResolver{answers: map[string][]net.IPAddr{"media.example": {clippingApifyTestPublicIP("8.8.8.8")}}}
	media := &clippingApifyHTTPTestDoer{respond: func(_ *http.Request, index int) (*http.Response, error) {
		return clippingApifyTestResponse(http.StatusTemporaryRedirect, http.Header{"Location": []string{"/hop/" + strconv.Itoa(index+1)}}, ""), nil
	}}
	client := clippingApifyTestClient()
	client.resolver = resolver
	client.mediaDoer = media
	destination := filepath.Join(t.TempDir(), "source.mp4")
	if err := client.downloadMedia(context.Background(), "https://media.example/video.mp4", destination, 1024); err == nil {
		t.Fatal("redirect chain exceeded the configured bound without error")
	}
	wantRequests := clippingApifyMaxRedirects + 1
	if got := media.requestCount(); got != wantRequests {
		t.Fatalf("media requests=%d, want %d (initial request plus bounded redirects)", got, wantRequests)
	}
	if got := resolver.callCount(); got != wantRequests {
		t.Fatalf("DNS validations=%d, want one for each attempted hop", got)
	}
}

func TestClippingApifyAPIOriginBearerAndResponseContextLifetime(t *testing.T) {
	var requestContext context.Context
	var delayedBody *clippingApifyHTTPTestDelayedBody
	api := &clippingApifyHTTPTestDoer{respond: func(request *http.Request, _ int) (*http.Response, error) {
		requestContext = request.Context()
		delayedBody = &clippingApifyHTTPTestDelayedBody{
			ctx:     request.Context(),
			ready:   make(chan struct{}),
			release: make(chan struct{}),
			reader:  strings.NewReader(`{"data":{"id":"run_123"}}`),
		}
		return &http.Response{
			StatusCode:    http.StatusOK,
			Header:        make(http.Header),
			Body:          delayedBody,
			ContentLength: int64(len(`{"data":{"id":"run_123"}}`)),
		}, nil
	}}
	client := clippingApifyTestClient()
	client.apiDoer = api
	query := url.Values{"waitForFinish": {"0"}}
	response, err := client.doAPI(context.Background(), http.MethodGet, "/actor-runs/run_123", query, nil)
	if err != nil {
		t.Fatalf("doAPI: %v", err)
	}
	request := api.requestAt(0)
	if request.URL.Scheme != "https" || request.URL.Host != "api.apify.com" {
		t.Fatalf("API request used origin %q, want the fixed HTTPS Apify API origin", request.URL.Scheme+"://"+request.URL.Host)
	}
	if request.URL.Path != "/v2/actor-runs/run_123" {
		t.Fatalf("API path=%q, want exactly one /v2 prefix", request.URL.Path)
	}
	if request.Header.Get("Authorization") != "Bearer apify-test-token" {
		t.Fatal("Apify bearer was not attached to the fixed API request")
	}
	if request.URL.Query().Get("waitForFinish") != "0" {
		t.Fatalf("API query=%s", request.URL.RawQuery)
	}
	if requestContext == nil || requestContext.Err() != nil {
		t.Fatalf("response request context was canceled before body read: %v", requestContext)
	}
	defer delayedBody.Unblock()
	readResult := make(chan struct {
		body []byte
		err  error
	}, 1)
	go func() {
		body, err := io.ReadAll(response.Body)
		readResult <- struct {
			body []byte
			err  error
		}{body: body, err: err}
	}()
	<-delayedBody.ready
	if requestContext.Err() != nil {
		t.Fatalf("response request context canceled during delayed body read: %v", requestContext.Err())
	}
	delayedBody.Unblock()
	read := <-readResult
	if read.err != nil || string(read.body) != `{"data":{"id":"run_123"}}` {
		t.Fatalf("read API response body=%q error=%v", read.body, read.err)
	}
	if requestContext.Err() != nil {
		t.Fatalf("response request context canceled before Body.Close: %v", requestContext.Err())
	}
	if err := response.Body.Close(); err != nil {
		t.Fatalf("close API response: %v", err)
	}
	if requestContext.Err() == nil {
		t.Fatal("API response context remained live after Body.Close")
	}
}

func TestClippingApifyAPIResponseCloseErrorCancelsContext(t *testing.T) {
	var requestContext context.Context
	closeFailure := errors.New("fake body close error")
	api := &clippingApifyHTTPTestDoer{respond: func(request *http.Request, _ int) (*http.Response, error) {
		requestContext = request.Context()
		response := clippingApifyTestResponse(http.StatusOK, nil, "body")
		response.Body = clippingApifyHTTPTestCloseErrorBody{Reader: strings.NewReader("body"), err: closeFailure}
		return response, nil
	}}
	client := clippingApifyTestClient()
	client.apiDoer = api
	response, err := client.doAPI(context.Background(), http.MethodGet, "/actor-runs/run_123", nil, nil)
	if err != nil {
		t.Fatalf("doAPI: %v", err)
	}
	if err := response.Body.Close(); !errors.Is(err, closeFailure) {
		t.Fatalf("response close error=%v, want %v", err, closeFailure)
	}
	if requestContext.Err() == nil {
		t.Fatal("request context remained live after a failed Body.Close")
	}
}

func TestClippingApifyAPICancellationAndErrorCleanup(t *testing.T) {
	started := make(chan context.Context, 1)
	api := &clippingApifyHTTPTestDoer{respond: func(request *http.Request, _ int) (*http.Response, error) {
		started <- request.Context()
		<-request.Context().Done()
		return nil, request.Context().Err()
	}}
	client := clippingApifyTestClient()
	client.apiDoer = api
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := client.doAPI(ctx, http.MethodGet, "/actor-runs/run_123", nil, nil)
		result <- err
	}()
	requestContext := <-started
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("doAPI cancellation error=%v", err)
	}
	if requestContext.Err() == nil {
		t.Fatal("request context was not canceled after Do returned an error")
	}
}

func TestClippingApifyMediaDoesNotForwardAPISecretToSameOriginArtifact(t *testing.T) {
	const artifactURL = "https://api.apify.com/v2/key-value-stores/key/records/video.mp4?signature=opaque"
	api := &clippingApifyHTTPTestDoer{respond: func(_ *http.Request, _ int) (*http.Response, error) {
		return clippingApifyTestResponse(http.StatusOK, nil, `{}`), nil
	}}
	media := &clippingApifyHTTPTestDoer{respond: func(_ *http.Request, _ int) (*http.Response, error) {
		return clippingApifyTestMediaResponse(http.StatusOK, http.Header{"Content-Type": []string{"video/mp4"}}, "video", 5), nil
	}}
	client := clippingApifyTestClient()
	client.apiDoer = api
	client.mediaDoer = media
	client.resolver = &clippingApifyHTTPTestResolver{answers: map[string][]net.IPAddr{"api.apify.com": {clippingApifyTestPublicIP("8.8.8.8")}}}
	response, err := client.doAPI(context.Background(), http.MethodGet, "/datasets/dataset/items", nil, nil)
	if err != nil {
		t.Fatalf("Apify API request: %v", err)
	}
	_ = response.Body.Close()

	destination := filepath.Join(t.TempDir(), "source.mp4")
	if err := client.downloadMedia(context.Background(), artifactURL, destination, 1024); err != nil {
		t.Fatalf("download same-origin artifact: %v", err)
	}
	apiRequest := api.requestAt(0)
	mediaRequest := media.requestAt(0)
	if apiRequest.Header.Get("Authorization") != "Bearer apify-test-token" {
		t.Fatal("Apify API bearer was not attached to the fixed API request")
	}
	if mediaRequest.URL.Host != apiRequest.URL.Host {
		t.Fatalf("fixture must exercise same-origin media: API=%s artifact=%s", apiRequest.URL.Host, mediaRequest.URL.Host)
	}
	if got := mediaRequest.Header.Get("Authorization"); got != "" {
		t.Fatal("Apify bearer forwarded to artifact URL")
	}
	if got := mediaRequest.Header.Get("Cookie"); got != "" {
		t.Fatal("cookie forwarded to artifact URL")
	}
}

func TestClippingApifyDownloadRejectsDeclaredLengthMismatchAndRemovesPartialFile(t *testing.T) {
	media := &clippingApifyHTTPTestDoer{respond: func(_ *http.Request, _ int) (*http.Response, error) {
		return clippingApifyTestMediaResponse(http.StatusOK, http.Header{"Content-Type": []string{"video/mp4"}}, "short", 100), nil
	}}
	client := clippingApifyTestClient()
	client.mediaDoer = media
	client.resolver = &clippingApifyHTTPTestResolver{answers: map[string][]net.IPAddr{"media.example": {clippingApifyTestPublicIP("8.8.8.8")}}}
	destination := filepath.Join(t.TempDir(), "source.mp4")
	err := client.downloadMedia(context.Background(), "https://media.example/source.mp4", destination, 1024)
	if err == nil {
		t.Fatal("truncated body was accepted despite declared Content-Length")
	}
	if got := safeClippingFailure(err); got != "apify_download_failed" {
		t.Fatalf("truncated body failure code=%q, want apify_download_failed (error %v)", got, err)
	}
	if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("truncated download left partial file: stat error=%v", statErr)
	}
}

func TestClippingApifyDownloadRejectsActorReportedLengthMismatch(t *testing.T) {
	media := &clippingApifyHTTPTestDoer{respond: func(_ *http.Request, _ int) (*http.Response, error) {
		return clippingApifyTestMediaResponse(http.StatusOK, http.Header{"Content-Type": []string{"video/mp4"}}, "short", -1), nil
	}}
	client := clippingApifyTestClient()
	client.mediaDoer = media
	client.resolver = &clippingApifyHTTPTestResolver{answers: map[string][]net.IPAddr{"media.example": {clippingApifyTestPublicIP("8.8.8.8")}}}
	destination := filepath.Join(t.TempDir(), "source.mp4")
	err := client.downloadMedia(context.Background(), "https://media.example/source.mp4", destination, 1024, 100)
	if err == nil {
		t.Fatal("download length mismatch was accepted despite Apify's savedFile.bytes metadata")
	}
	if got := safeClippingFailure(err); got != "apify_download_failed" {
		t.Fatalf("savedFile.bytes mismatch failure code=%q, want apify_download_failed (error %v)", got, err)
	}
	if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("mismatched download left partial file: stat error=%v", statErr)
	}
}

func TestDecodeApifyJSONEnforcesByteLimitIncludingTrailingWhitespace(t *testing.T) {
	var value map[string]any
	if err := decodeApifyJSON(strings.NewReader(`{"ok":true}`), &value); err != nil {
		t.Fatalf("valid JSON rejected: %v", err)
	}
	tooLarge := `{"ok":true}` + strings.Repeat(" ", clippingApifyMaxJSONBytes+1)
	if err := decodeApifyJSON(strings.NewReader(tooLarge), &value); err == nil {
		t.Fatal("JSON response larger than the 1 MiB limit was accepted when excess bytes were whitespace")
	}
	if err := decodeApifyJSON(strings.NewReader(`{"ok":true}{"second":true}`), &value); err == nil {
		t.Fatal("multiple JSON values were accepted")
	}
}

func TestClippingApifyStartStatusClassifiesAmbiguousAndDefinitiveResponses(t *testing.T) {
	tests := []struct {
		name                 string
		status               int
		wantCode             string
		wantDefinitiveReject bool
	}{
		{name: "auth rejected", status: http.StatusUnauthorized, wantCode: "apify_auth_failed", wantDefinitiveReject: true},
		{name: "forbidden", status: http.StatusForbidden, wantCode: "apify_auth_failed", wantDefinitiveReject: true},
		{name: "rate limited", status: http.StatusTooManyRequests, wantCode: "apify_rate_limited", wantDefinitiveReject: true},
		{name: "request timeout is ambiguous", status: http.StatusRequestTimeout, wantCode: "apify_start_uncertain"},
		{name: "redirect is ambiguous", status: http.StatusTemporaryRedirect, wantCode: "apify_start_uncertain"},
		{name: "server error is ambiguous", status: http.StatusBadGateway, wantCode: "apify_start_uncertain"},
		{name: "malformed request rejected", status: http.StatusBadRequest, wantCode: "apify_invalid_response", wantDefinitiveReject: true},
		{name: "unprocessable request rejected", status: http.StatusUnprocessableEntity, wantCode: "apify_invalid_response", wantDefinitiveReject: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			api := &clippingApifyHTTPTestDoer{respond: func(_ *http.Request, _ int) (*http.Response, error) {
				return clippingApifyTestResponse(test.status, nil, `{"message":"private provider diagnostic"}`), nil
			}}
			client := clippingApifyTestClient()
			client.apiDoer = api
			_, err := client.startRun(context.Background(), "https://www.youtube.com/watch?v=abcdefghijk")
			if err == nil {
				t.Fatal("non-success response was accepted")
			}
			if got := safeClippingFailure(err); got != test.wantCode {
				t.Fatalf("safe failure code=%q, want %q (error %v)", got, test.wantCode, err)
			}
			var apiErr clippingApifyAPIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("failure type=%T, want clippingApifyAPIError", err)
			}
			if apiErr.definitiveRejection != test.wantDefinitiveReject {
				t.Fatalf("definitiveRejection=%v, want %v", apiErr.definitiveRejection, test.wantDefinitiveReject)
			}
			if strings.Contains(err.Error(), "private provider diagnostic") {
				t.Fatal("raw provider response body escaped in the safe error")
			}
		})
	}
}

func TestClippingApifyStartMalformedSuccessIsAmbiguous(t *testing.T) {
	for _, body := range []string{
		"not-json",
		`{"message":"private provider diagnostic"}`,
	} {
		t.Run(body, func(t *testing.T) {
			api := &clippingApifyHTTPTestDoer{respond: func(_ *http.Request, _ int) (*http.Response, error) {
				return clippingApifyTestResponse(http.StatusOK, nil, body), nil
			}}
			client := clippingApifyTestClient()
			client.apiDoer = api
			_, err := client.startRun(context.Background(), "https://www.youtube.com/watch?v=abcdefghijk")
			if err == nil {
				t.Fatal("malformed successful start response was accepted")
			}
			if got := safeClippingFailure(err); got != "apify_start_uncertain" {
				t.Fatalf("safe failure code=%q, want apify_start_uncertain (error %v)", got, err)
			}
			if strings.Contains(err.Error(), "private provider diagnostic") {
				t.Fatal("raw provider response body escaped in the safe error")
			}
		})
	}
}

func TestClippingApifyStartTransportErrorIsAmbiguous(t *testing.T) {
	api := &clippingApifyHTTPTestDoer{respond: func(_ *http.Request, _ int) (*http.Response, error) {
		return nil, errors.New("private transport diagnostic")
	}}
	client := clippingApifyTestClient()
	client.apiDoer = api
	_, err := client.startRun(context.Background(), "https://www.youtube.com/watch?v=abcdefghijk")
	if err == nil {
		t.Fatal("transport failure was accepted")
	}
	if got := safeClippingFailure(err); got != "apify_start_uncertain" {
		t.Fatalf("safe failure code=%q, want apify_start_uncertain (error %v)", got, err)
	}
	if strings.Contains(err.Error(), "private transport diagnostic") {
		t.Fatal("raw transport diagnostic escaped in the safe error")
	}
}
