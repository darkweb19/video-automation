package app

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"sync"
)

var (
	errClippingGeminiInvalidDestination = errors.New("invalid Gemini provider destination")
	errClippingGeminiResponseTooLarge   = errors.New("Gemini provider response exceeds size limit")
	errClippingGeminiInvalidResponse    = errors.New("invalid Gemini provider response")
)

// newClippingGeminiBoundedTransport accepts only an origin selected by the
// fixed location mapping. It limits the body delivered to the SDK, after any
// transparent decompression performed by the underlying HTTP transport.
func newClippingGeminiBoundedTransport(base http.RoundTripper, endpoint string) (http.RoundTripper, error) {
	var host string
	for _, location := range [...]string{"global", "us", "eu"} {
		allowed, _ := clippingGeminiEndpoint(location)
		if endpoint == allowed {
			parsed, err := url.Parse(allowed)
			if err != nil {
				return nil, errClippingGeminiInvalidDestination
			}
			host = parsed.Host
			break
		}
	}
	if host == "" {
		return nil, errClippingGeminiInvalidDestination
	}
	if base == nil {
		base = http.DefaultTransport
	}
	return &clippingGeminiBoundedTransport{base: base, host: host}, nil
}

type clippingGeminiBoundedTransport struct {
	base http.RoundTripper
	host string
}

func (t *clippingGeminiBoundedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !t.validDestination(req) {
		// RoundTripper owns the request body even when it rejects a request.
		if req != nil && req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, errClippingGeminiInvalidDestination
	}

	response, err := t.base.RoundTrip(req)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return nil, err
	}
	if response == nil {
		return nil, errClippingGeminiInvalidResponse
	}
	if response.ContentLength > clippingGeminiMaxProviderResponseLen {
		if response.Body != nil {
			_ = response.Body.Close()
		}
		return nil, errClippingGeminiResponseTooLarge
	}
	if response.Body == nil || response.ContentLength < -1 {
		if response.Body != nil {
			_ = response.Body.Close()
		}
		return nil, errClippingGeminiInvalidResponse
	}

	// Preserve the delegate's response object while replacing only our copy's
	// body. Even an incorrectly small Content-Length cannot bypass the limit.
	bounded := *response
	bounded.Body = &clippingGeminiBoundedBody{
		body:      response.Body,
		remaining: clippingGeminiMaxProviderResponseLen,
	}
	return &bounded, nil
}

func (t *clippingGeminiBoundedTransport) validDestination(req *http.Request) bool {
	if req == nil || req.URL == nil {
		return false
	}
	u := req.URL
	return u.Scheme == "https" && u.Host == t.host && u.Opaque == "" &&
		u.User == nil && u.Fragment == "" && u.RawFragment == "" &&
		(req.Host == "" || req.Host == t.host)
}

// clippingGeminiBoundedBody never returns more than remaining bytes. It probes
// one extra byte when necessary to distinguish an exact-sized body from an
// oversized body, and closes immediately when an overflow is detected.
type clippingGeminiBoundedBody struct {
	body      io.ReadCloser
	remaining int64
	failure   error
	closeOnce sync.Once
	closeErr  error
}

func (b *clippingGeminiBoundedBody) Read(p []byte) (int, error) {
	if b.failure != nil {
		return 0, b.failure
	}
	if len(p) == 0 {
		return 0, nil
	}

	readLen := len(p)
	if int64(readLen) > b.remaining+1 {
		readLen = int(b.remaining + 1)
	}
	n, err := b.body.Read(p[:readLen])
	if n < 0 || n > readLen {
		b.failure = errClippingGeminiInvalidResponse
		_ = b.Close()
		return 0, b.failure
	}
	if int64(n) > b.remaining {
		allowed := int(b.remaining)
		b.remaining = 0
		b.failure = errClippingGeminiResponseTooLarge
		_ = b.Close()
		return allowed, b.failure
	}
	b.remaining -= int64(n)
	return n, err
}

func (b *clippingGeminiBoundedBody) Close() error {
	b.closeOnce.Do(func() {
		b.closeErr = b.body.Close()
	})
	return b.closeErr
}
