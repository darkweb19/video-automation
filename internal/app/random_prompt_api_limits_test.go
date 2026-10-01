package app

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRandomPromptAPIPlatformLimitSharesCooldownAndKeepsErrorsSafe(t *testing.T) {
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	saveKey := func(key string) {
		t.Helper()
		encrypted, err := security.EncryptSetting(apiKeySetting, key)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.SetSetting(apiKeySetting, encrypted); err != nil {
			t.Fatal(err)
		}
	}
	saveKey("first-test-key")
	var calls atomic.Int32
	reset := time.Now().Add(2 * time.Hour)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") == "Bearer replacement-test-key" {
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"A firefly crosses a moonlit forest."}}]}`)
			return
		}
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset.UnixMilli(), 10))
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = fmt.Fprint(w, `{"error":{"code":429,"message":"private upstream error first-test-key","metadata":{"limit_source":"openrouter_free_models"}}}`)
	}))
	defer upstream.Close()
	app := &dashboardApp{store: store, security: security, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), baseURL: upstream.URL}
	generate := func() *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/api/prompts/random", strings.NewReader(`{"category":"Nature","mode":"single"}`))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		app.randomPrompt(response, request)
		return response
	}
	for attempt := 0; attempt < 2; attempt++ {
		response := generate()
		if response.Code != http.StatusTooManyRequests || !strings.Contains(response.Body.String(), "shared free-model limit") {
			t.Fatalf("limit response = %d: %s", response.Code, response.Body.String())
		}
		seconds, err := strconv.Atoi(response.Header().Get("Retry-After"))
		if err != nil || seconds < 7100 || seconds > 7201 {
			t.Fatalf("Retry-After = %q", response.Header().Get("Retry-After"))
		}
		if strings.Contains(response.Body.String(), "first-test-key") || strings.Contains(response.Body.String(), "private upstream") {
			t.Fatal("provider data leaked in public error")
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("sent %d upstream calls during shared cooldown, want one", calls.Load())
	}
	// Changing the saved credential creates a different cooldown scope.
	saveKey("replacement-test-key")
	response := generate()
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "A firefly crosses") || calls.Load() != 2 {
		t.Fatalf("replacement credential response = %d: %s (calls=%d)", response.Code, response.Body.String(), calls.Load())
	}
}

func TestSafeRandomPromptFailureDistinguishesPlatformLimitsAndCreditErrors(t *testing.T) {
	reset := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		err  *upstreamError
		want string
	}{
		{&upstreamError{StatusCode: 429, RateLimitScope: "platform", RetryAt: reset}, "2026-10-02 00:00:00 UTC"},
		{&upstreamError{StatusCode: 429, RateLimitScope: "platform"}, "quota to reset"},
		{&upstreamError{StatusCode: 402}, "account credit or key limit"},
	} {
		if message := safeRandomPromptFailure(test.err); !strings.Contains(message, test.want) {
			t.Fatalf("message = %q, want %q", message, test.want)
		}
	}
}
