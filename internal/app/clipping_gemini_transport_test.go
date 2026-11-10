package app

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

type geminiTransportFunc func(*http.Request) (*http.Response, error)

func (f geminiTransportFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type geminiTrackingBody struct {
	reader    io.Reader
	reads     int
	bytesRead int
	closes    int
	closeErr  error
}

func (b *geminiTrackingBody) Read(p []byte) (int, error) {
	b.reads++
	n, err := b.reader.Read(p)
	if n > 0 {
		b.bytesRead += n
	}
	return n, err
}

func (b *geminiTrackingBody) Close() error {
	b.closes++
	return b.closeErr
}

func geminiTestTransport(t *testing.T, base http.RoundTripper) http.RoundTripper {
	t.Helper()
	endpoint, _ := clippingGeminiEndpoint("global")
	transport, err := newClippingGeminiBoundedTransport(base, endpoint)
	if err != nil {
		t.Fatalf("construct transport: %v", err)
	}
	return transport
}

func geminiTestRequest(t *testing.T) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "https://aiplatform.googleapis.com/v1/models:generateContent", nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func TestClippingGeminiTransportEndpoint(t *testing.T) {
	for _, location := range []string{"global", "us", "eu"} {
		t.Run(location, func(t *testing.T) {
			endpoint, _ := clippingGeminiEndpoint(location)
			transport, err := newClippingGeminiBoundedTransport(nil, endpoint)
			if err != nil {
				t.Fatalf("valid endpoint: %v", err)
			}
			if transport.(*clippingGeminiBoundedTransport).base != http.DefaultTransport {
				t.Fatal("nil base did not select the default transport")
			}
		})
	}
	invalid := []string{
		"", "http://aiplatform.googleapis.com/", "https://aiplatform.googleapis.com",
		"https://AIPLATFORM.googleapis.com/", "https://aiplatform.googleapis.com:443/",
		"https://private@aiplatform.googleapis.com/", "https://aiplatform.googleapis.com/private",
		"https://aiplatform.googleapis.com/?private", "https://aiplatform.googleapis.com/#private",
		"https://aiplatform.googleapis.com.evil.invalid/", "https://aiplatform.googleapis.com/ ",
	}
	for i, endpoint := range invalid {
		transport, err := newClippingGeminiBoundedTransport(nil, endpoint)
		if transport != nil || !errors.Is(err, errClippingGeminiInvalidDestination) {
			t.Errorf("invalid endpoint %d: did not reject with destination error", i)
		}
		if err != nil && strings.Contains(err.Error(), "private") {
			t.Errorf("invalid endpoint %d: error disclosed input", i)
		}
	}
}

func TestClippingGeminiTransportValidDestination(t *testing.T) {
	for _, location := range []string{"global", "us", "eu"} {
		t.Run(location, func(t *testing.T) {
			endpoint, _ := clippingGeminiEndpoint(location)
			parsed, _ := url.Parse(endpoint)
			var calls int
			body := &geminiTrackingBody{reader: strings.NewReader("ok")}
			response := &http.Response{StatusCode: http.StatusOK, Body: body, ContentLength: 2}
			var delegated *http.Request
			base := geminiTransportFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				delegated = req
				return response, nil
			})
			transport, err := newClippingGeminiBoundedTransport(base, endpoint)
			if err != nil {
				t.Fatal(err)
			}
			req, err := http.NewRequest(http.MethodPost, endpoint+"v1/models:generateContent", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Host = parsed.Host
			req.Header.Set("X-Test", "unchanged")
			originalURL := *req.URL
			got, err := transport.RoundTrip(req)
			if err != nil {
				t.Fatalf("valid request: %v", err)
			}
			if calls != 1 || delegated != req {
				t.Fatal("request was not delegated exactly once")
			}
			if *req.URL != originalURL || req.Host != parsed.Host || req.Header.Get("X-Test") != "unchanged" {
				t.Fatal("request was mutated")
			}
			if got == response || response.Body != body {
				t.Fatal("delegate response object was mutated")
			}
			data, err := io.ReadAll(got.Body)
			if err != nil || string(data) != "ok" {
				t.Fatalf("read body: %v", err)
			}
			if err := got.Body.Close(); err != nil || body.closes != 1 {
				t.Fatal("normal close was not propagated exactly once")
			}
		})
	}
}

func TestClippingGeminiTransportRejectsInvalidDestination(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*http.Request)
	}{
		{"missing URL", func(r *http.Request) { r.URL = nil }},
		{"wrong protocol", func(r *http.Request) { r.URL.Scheme = "http" }},
		{"wrong host", func(r *http.Request) { r.URL.Host = "example.invalid" }},
		{"host suffix", func(r *http.Request) { r.URL.Host += ".evil.invalid" }},
		{"explicit default port", func(r *http.Request) { r.URL.Host += ":443" }},
		{"nondefault port", func(r *http.Request) { r.URL.Host += ":8443" }},
		{"credentials", func(r *http.Request) { r.URL.User = url.UserPassword("private", "private") }},
		{"opaque", func(r *http.Request) { r.URL.Opaque = "//example.invalid/" }},
		{"fragment", func(r *http.Request) { r.URL.Fragment = "private" }},
		{"raw fragment", func(r *http.Request) { r.URL.RawFragment = "private" }},
		{"host override", func(r *http.Request) { r.Host = "example.invalid" }},
		{"host override port", func(r *http.Request) { r.Host += ":443" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls int
			transport := geminiTestTransport(t, geminiTransportFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return nil, nil
			}))
			req := geminiTestRequest(t)
			body := &geminiTrackingBody{reader: strings.NewReader("input")}
			req.Body = body
			tt.mutate(req)
			response, err := transport.RoundTrip(req)
			if response != nil || !errors.Is(err, errClippingGeminiInvalidDestination) || calls != 0 {
				t.Fatal("invalid destination reached the delegate or was not rejected")
			}
			if body.closes != 1 {
				t.Fatal("rejected request body was not closed")
			}
			if strings.Contains(err.Error(), "private") {
				t.Fatal("destination error disclosed request data")
			}
		})
	}
	transport := geminiTestTransport(t, geminiTransportFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("nil request reached the delegate")
		return nil, nil
	}))
	if _, err := transport.RoundTrip(nil); !errors.Is(err, errClippingGeminiInvalidDestination) {
		t.Fatal("nil request was not rejected")
	}
}

func TestClippingGeminiTransportResponseBounds(t *testing.T) {
	const limit = clippingGeminiMaxProviderResponseLen
	tests := []struct {
		name          string
		size          int
		contentLength int64
		tooLarge      bool
		early         bool
	}{
		{"empty", 0, 0, false, false},
		{"below limit", limit - 1, -1, false, false},
		{"exact declared", limit, limit, false, false},
		{"exact streamed", limit, -1, false, false},
		{"too large declared", limit + 1, limit + 1, true, true},
		{"too large streamed", limit + 1, -1, true, false},
		{"too large underreported", limit + 1, 1, true, false},
		{"much too large streamed", limit * 2, -1, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := &geminiTrackingBody{reader: bytes.NewReader(bytes.Repeat([]byte{'x'}, tt.size))}
			var calls int
			transport := geminiTestTransport(t, geminiTransportFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: http.StatusOK, ContentLength: tt.contentLength, Body: body}, nil
			}))
			response, err := transport.RoundTrip(geminiTestRequest(t))
			if calls != 1 {
				t.Fatal("delegate was not called exactly once")
			}
			if tt.early {
				if response != nil || !errors.Is(err, errClippingGeminiResponseTooLarge) || body.reads != 0 || body.closes != 1 {
					t.Fatal("oversized declared response was not rejected and closed before reading")
				}
				return
			}
			if err != nil {
				t.Fatalf("round trip: %v", err)
			}
			data, err := io.ReadAll(response.Body)
			if tt.tooLarge {
				if !errors.Is(err, errClippingGeminiResponseTooLarge) || len(data) != limit || body.closes != 1 {
					t.Fatal("streamed overflow did not stop at the limit and close promptly")
				}
				readsAtOverflow := body.reads
				if _, err := response.Body.Read(make([]byte, 1)); !errors.Is(err, errClippingGeminiResponseTooLarge) || body.reads != readsAtOverflow {
					t.Fatal("overflow error was not stable on a subsequent read")
				}
			} else if err != nil || len(data) != tt.size {
				t.Fatalf("body size or error mismatch: bytes=%d error=%v", len(data), err)
			}
			if err := response.Body.Close(); err != nil || body.closes != 1 {
				t.Fatal("body was not closed exactly once")
			}
		})
	}
}

func TestClippingGeminiTransportBoundsDecodedGzipBody(t *testing.T) {
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(bytes.Repeat([]byte{'a'}, clippingGeminiMaxProviderResponseLen+1)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	decoded, err := gzip.NewReader(bytes.NewReader(compressed.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer decoded.Close()
	body := &geminiTrackingBody{reader: decoded}
	transport := geminiTestTransport(t, geminiTransportFunc(func(*http.Request) (*http.Response, error) {
		// A standard Go Transport transparently decodes gzip and sets these fields.
		return &http.Response{StatusCode: http.StatusOK, Uncompressed: true, ContentLength: -1, Body: body}, nil
	}))
	response, err := transport.RoundTrip(geminiTestRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(response.Body)
	if !errors.Is(err, errClippingGeminiResponseTooLarge) || len(data) != clippingGeminiMaxProviderResponseLen || body.closes != 1 {
		t.Fatal("decoded gzip overflow was not bounded and closed")
	}
	if err := response.Body.Close(); err != nil || body.closes != 1 {
		t.Fatal("decoded body close was not idempotent")
	}
}

type geminiErrorReader struct{ err error }

func (r geminiErrorReader) Read([]byte) (int, error) { return 0, r.err }

type geminiInvalidReader struct{}

func (geminiInvalidReader) Read(p []byte) (int, error) { return len(p) + 1, nil }

func TestClippingGeminiTransportMalformedAndErrors(t *testing.T) {
	closeErr := errors.New("close failed")
	tests := []struct {
		name        string
		response    *http.Response
		delegateErr error
		want        error
		body        *geminiTrackingBody
	}{
		{name: "nil response", want: errClippingGeminiInvalidResponse},
		{name: "nil body", response: &http.Response{}, want: errClippingGeminiInvalidResponse},
		{name: "invalid content length", response: &http.Response{ContentLength: -2}, want: errClippingGeminiInvalidResponse, body: &geminiTrackingBody{reader: strings.NewReader("")}},
		{name: "delegate error", delegateErr: context.Canceled, want: context.Canceled, body: &geminiTrackingBody{reader: strings.NewReader("")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.body != nil {
				if tt.response == nil {
					tt.response = &http.Response{}
				}
				tt.response.Body = tt.body
			}
			var calls int
			transport := geminiTestTransport(t, geminiTransportFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return tt.response, tt.delegateErr
			}))
			response, err := transport.RoundTrip(geminiTestRequest(t))
			if response != nil || !errors.Is(err, tt.want) || calls != 1 {
				t.Fatal("malformed or failed response was not rejected as expected")
			}
			if tt.body != nil && tt.body.closes != 1 {
				t.Fatal("failed response body was not closed")
			}
		})
	}

	t.Run("read and close errors", func(t *testing.T) {
		body := &geminiTrackingBody{reader: geminiErrorReader{err: context.DeadlineExceeded}, closeErr: closeErr}
		transport := geminiTestTransport(t, geminiTransportFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{Body: body, ContentLength: -1}, nil
		}))
		response, err := transport.RoundTrip(geminiTestRequest(t))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.ReadAll(response.Body); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("body read error was not preserved")
		}
		if err := response.Body.Close(); err != closeErr {
			t.Fatal("body close error was not preserved")
		}
		if err := response.Body.Close(); err != closeErr || body.closes != 1 {
			t.Fatal("repeated close did not preserve the original error and close once")
		}
	})

	t.Run("invalid reader count", func(t *testing.T) {
		body := &geminiTrackingBody{reader: geminiInvalidReader{}}
		transport := geminiTestTransport(t, geminiTransportFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{Body: body, ContentLength: -1}, nil
		}))
		response, err := transport.RoundTrip(geminiTestRequest(t))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.ReadAll(response.Body); !errors.Is(err, errClippingGeminiInvalidResponse) || body.closes != 1 {
			t.Fatal("invalid reader count was not rejected and closed")
		}
	})
}

// A redirect is returned as a response; following it is an http.Client policy.
func TestClippingGeminiTransportDoesNotFollowRedirect(t *testing.T) {
	var calls int
	transport := geminiTestTransport(t, geminiTransportFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {"https://example.invalid/"}}, Body: http.NoBody}, nil
	}))
	response, err := transport.RoundTrip(geminiTestRequest(t))
	if err != nil || calls != 1 || response.StatusCode != http.StatusFound {
		t.Fatal("redirect was not returned after exactly one delegation")
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal("redirect response body did not close")
	}
}

func TestClippingGeminiTransportCloseInterruptsRead(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	transport := geminiTestTransport(t, geminiTransportFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{Body: reader, ContentLength: -1}, nil
	}))
	response, err := transport.RoundTrip(geminiTestRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		close(started)
		_, err := response.Body.Read(make([]byte, 1))
		result <- err
	}()
	<-started
	if err := response.Body.Close(); err != nil {
		t.Fatal("close failed")
	}
	select {
	case err := <-result:
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatal("closing did not interrupt the body read")
		}
	case <-time.After(time.Second):
		t.Fatal("closing did not unblock the body read")
	}
}
