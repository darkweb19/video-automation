package app

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestParseOpenRouterErrorRetryHints(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	reset := now.Add(90*time.Second + 237*time.Millisecond)
	for _, test := range []struct {
		name    string
		body    string
		headers http.Header
		want    time.Time
		delta   time.Duration
	}{
		{name: "delta seconds", headers: http.Header{"Retry-After": {"60"}}, delta: 60 * time.Second},
		{name: "HTTP date", headers: http.Header{"Retry-After": {now.Add(time.Minute).Format(http.TimeFormat)}}, want: now.Add(time.Minute)},
		{name: "epoch milliseconds", headers: http.Header{"X-RateLimit-Reset": {strconv.FormatInt(reset.UnixMilli(), 10)}}, want: reset},
		{name: "epoch seconds compatibility", headers: http.Header{"X-RateLimit-Reset": {strconv.FormatInt(now.Add(time.Minute).Unix(), 10)}}, want: now.Add(time.Minute)},
		{name: "later reset", headers: http.Header{"Retry-After": {"30"}, "X-RateLimit-Reset": {strconv.FormatInt(reset.UnixMilli(), 10)}}, want: reset},
		{name: "later retry after", headers: http.Header{"Retry-After": {"120"}, "X-RateLimit-Reset": {strconv.FormatInt(reset.UnixMilli(), 10)}}, delta: 120 * time.Second},
		{name: "case insensitive HTTP header", headers: http.Header{"rEtRy-AfTeR": {"60"}}, delta: 60 * time.Second},
		{name: "case insensitive metadata header", body: fmt.Sprintf(`{"error":{"metadata":{"headers":{"x-ratelimit-reset":%d}}}}`, reset.UnixMilli()), want: reset},
		{name: "later metadata hint", body: fmt.Sprintf(`{"error":{"metadata":{"headers":{"Retry-After":"10","X-RateLimit-Reset":"%d"}}}}`, reset.UnixMilli()), headers: http.Header{"Retry-After": {"20"}}, want: reset},
		{name: "multiple hints", headers: http.Header{"Retry-After": {"10", "60"}}, delta: 60 * time.Second},
		{name: "zero delta", headers: http.Header{"Retry-After": {"0"}}},
		{name: "elapsed HTTP date", headers: http.Header{"Retry-After": {now.Add(-time.Minute).Format(http.TimeFormat)}}, want: now.Add(-time.Minute)},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := test.body
			if body == "" {
				body = `{"error":{"message":"Rate limit exceeded"}}`
			}
			before := time.Now()
			err := parseOpenRouterError([]byte(body), http.StatusTooManyRequests, test.headers)
			after := time.Now()
			var upstream *upstreamError
			if !errors.As(err, &upstream) {
				t.Fatalf("error = %v, want upstream error", err)
			}
			if !test.want.IsZero() {
				if !upstream.RetryAt.Equal(test.want) {
					t.Fatalf("retry at = %s, want %s", upstream.RetryAt, test.want)
				}
			} else if upstream.RetryAt.Before(before.Add(test.delta)) || upstream.RetryAt.After(after.Add(test.delta)) {
				t.Fatalf("retry at = %s, want between %s and %s", upstream.RetryAt, before.Add(test.delta), after.Add(test.delta))
			}
		})
	}
}

func TestParseOpenRouterErrorLimitScope(t *testing.T) {
	for _, test := range []struct {
		name    string
		body    string
		headers http.Header
		want    string
	}{
		{name: "platform source", body: `{"error":{"metadata":{"limit_source":"openrouter_free_models_per_min"}}}`, want: "platform"},
		{name: "provider source", body: `{"error":{"metadata":{"limit_source":"upstream_provider_shared_pool"}}}`, want: "provider"},
		{name: "named provider", body: `{"error":{"metadata":{"provider_name":"Chutes"}}}`, want: "provider"},
		{name: "provider code", body: `{"error":{"metadata":{"provider_code":"rate_limited"}}}`, want: "provider"},
		{name: "numeric provider code", body: `{"error":{"metadata":{"provider_code":429}}}`, want: "provider"},
		{name: "HTTP platform header", body: `{"error":{"metadata":{"provider_name":"Chutes"}}}`, headers: http.Header{"X-RateLimit-Remaining": {"0"}}, want: "platform"},
		{name: "metadata platform header", body: `{"error":{"metadata":{"headers":{"X-RateLimit-Limit":"20"},"provider_name":null}}}`, want: "platform"},
		{name: "provider echoed header", body: `{"error":{"metadata":{"headers":{"X-RateLimit-Limit":"20"},"provider_name":"Example"}}}`, want: "provider"},
		{name: "explicit provider source wins", body: `{"error":{"metadata":{"limit_source":"upstream_provider_byok"}}}`, headers: http.Header{"X-RateLimit-Remaining": {"0"}}, want: "provider"},
		{name: "legacy minute quota", body: `{"error":{"message":"Rate limit exceeded: free-models-per-min. Please retry."}}`, want: "platform"},
		{name: "legacy daily quota", body: `{"error":{"message":"Rate limit exceeded: free-models-per-day-high-balance."}}`, want: "platform"},
		{name: "generic unknown", body: `{"error":{"message":"Rate limit exceeded"}}`},
		{name: "retry after is ambiguous", body: `{"error":{"message":"Rate limit exceeded"}}`, headers: http.Header{"Retry-After": {"60"}}},
		{name: "unrecognized source", body: `{"error":{"metadata":{"limit_source":"new_scope"}}}`},
		{name: "message mentions quota", body: `{"error":{"message":"Provider error mentions free-models-per-min"}}`},
		{name: "near match is unknown", body: `{"error":{"message":"Rate limit exceeded: free-models-per-minor."}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := parseOpenRouterError([]byte(test.body), http.StatusTooManyRequests, test.headers)
			var upstream *upstreamError
			if !errors.As(err, &upstream) || upstream.RateLimitScope != test.want {
				t.Fatalf("error = %#v, want scope %q", err, test.want)
			}
		})
	}
}

func TestParseOpenRouterErrorEmbeddedStatus(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		want int
	}{
		{name: "numeric code", body: `{"error":{"code":429,"message":"limited"}}`, want: http.StatusTooManyRequests},
		{name: "string code", body: `{"error":{"code":"429","message":"limited"}}`, want: http.StatusTooManyRequests},
		{name: "integral number", body: `{"error":{"code":429.0}}`, want: http.StatusTooManyRequests},
		{name: "status field", body: `{"error":{"status":"503"}}`, want: http.StatusServiceUnavailable},
		{name: "missing code", body: `{"error":{"message":"failed"}}`, want: http.StatusBadGateway},
		{name: "success code in error", body: `{"error":{"code":200}}`, want: http.StatusBadGateway},
		{name: "fractional code", body: `{"error":{"code":429.5}}`, want: http.StatusBadGateway},
		{name: "malformed code", body: `{"error":{"code":{"raw":"secret"}}}`, want: http.StatusBadGateway},
		{name: "normal choices", body: `{"choices":[{"message":{"content":"A forest"}}]}`},
		{name: "null error", body: `{"error":null,"choices":[]}`},
		{name: "empty string error", body: `{"error":"","status":"completed"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := parseOpenRouterError([]byte(test.body), http.StatusOK, nil)
			if test.want == 0 {
				if err != nil {
					t.Fatalf("normal response error = %v", err)
				}
				return
			}
			var upstream *upstreamError
			if !errors.As(err, &upstream) || upstream.StatusCode != test.want {
				t.Fatalf("error = %#v, want HTTP %d", err, test.want)
			}
		})
	}
	var upstream *upstreamError
	err := parseOpenRouterError([]byte(`{"error":{"code":"429"}}`), http.StatusUnauthorized, nil)
	if !errors.As(err, &upstream) || upstream.StatusCode != http.StatusUnauthorized {
		t.Fatalf("non-2xx HTTP status must remain authoritative: %v", err)
	}
}

func TestParseOpenRouterErrorMalformedFieldsAndSecretHandling(t *testing.T) {
	for _, body := range []string{
		`{"error":{"code":429,"message":{},"metadata":{"raw":"Bearer test-secret","provider_name":{},"limit_source":12,"headers":{"Authorization":"Bearer test-secret","Retry-After":-1,"X-RateLimit-Reset":"NaN"}}}}`,
		`{"error":{"metadata":"test-secret"}}`,
		`{"error":{"metadata":{"raw":"test-secret","headers":["test-secret"]}}}`,
		`{"error":{"metadata":{"raw":"test-secret","headers":{"Retry-After":"9223372036854775807","X-RateLimit-Reset":"1e300"}}}}`,
		`{"raw":"test-secret"}`,
		`<html>test-secret</html>`,
	} {
		err := parseOpenRouterError([]byte(body), http.StatusTooManyRequests, nil)
		var upstream *upstreamError
		if !errors.As(err, &upstream) || upstream.StatusCode != http.StatusTooManyRequests || !upstream.RetryAt.IsZero() || upstream.RateLimitScope != "" {
			t.Fatalf("malformed optional metadata changed error classification: %#v", err)
		}
		if strings.Contains(err.Error(), "test-secret") || strings.Contains(err.Error(), "raw") || strings.Contains(err.Error(), "Bearer") {
			t.Fatalf("error exposed raw provider data: %v", err)
		}
	}
}

func TestReadUpstreamErrorPreservesSelectedMetadata(t *testing.T) {
	err := readUpstreamError(&http.Response{
		StatusCode: http.StatusForbidden,
		Header:     http.Header{"Retry-After": {"60"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"Blocked","metadata":{"error_type":"refusal","provider_name":"Example","limit_source":"upstream_provider_byok","raw":"test-secret"}}}`)),
	})
	var upstream *upstreamError
	if !errors.As(err, &upstream) || upstream.ErrorType != "refusal" || upstream.ProviderName != "Example" || upstream.LimitSource != "upstream_provider_byok" || upstream.RetryAt.IsZero() || upstream.RateLimitScope != "" {
		t.Fatalf("error = %#v", err)
	}
	if strings.Contains(err.Error(), "test-secret") {
		t.Fatalf("error exposed raw metadata: %v", err)
	}
}
