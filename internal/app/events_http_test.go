package app

import (
	"bufio"
	"context"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestEventsStreamsOverHTTP1AndHTTP2(t *testing.T) {
	for _, test := range []struct {
		name        string
		enableHTTP2 bool
		wantProto   int
	}{
		{name: "http1", wantProto: 1},
		{name: "http2", enableHTTP2: true, wantProto: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newTestStore(t)
			security, err := NewSecurity(store)
			if err != nil {
				t.Fatal(err)
			}
			passwordHash, err := hashPassword("a sufficiently long test password")
			if err != nil {
				t.Fatal(err)
			}
			if err := store.EnsureUser("events-user", passwordHash); err != nil {
				t.Fatal(err)
			}
			if err := store.ChangePassword("events-user", passwordHash); err != nil {
				t.Fatal(err)
			}

			server := httptest.NewUnstartedServer(NewDashboardHandler(store, security, nil))
			server.EnableHTTP2 = test.enableHTTP2
			// Prove that the per-frame deadline reset lets an SSE stream outlive
			// the ordinary response timeout before another event arrives.
			server.Config.WriteTimeout = time.Second
			server.StartTLS()
			defer server.Close()

			client := server.Client()
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()

			unauthorizedRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/events", nil)
			if err != nil {
				t.Fatal(err)
			}
			unauthorizedResponse, err := client.Do(unauthorizedRequest)
			if err != nil {
				t.Fatal(err)
			}
			_ = unauthorizedResponse.Body.Close()
			if unauthorizedResponse.StatusCode != http.StatusUnauthorized {
				t.Fatalf("unauthenticated stream status = %d, want %d", unauthorizedResponse.StatusCode, http.StatusUnauthorized)
			}

			sessionRecorder := httptest.NewRecorder()
			if err := security.NewSession(sessionRecorder, httptest.NewRequest(http.MethodGet, "/", nil), "events-user"); err != nil {
				t.Fatal(err)
			}
			cookies := sessionRecorder.Result().Cookies()
			if len(cookies) != 1 {
				t.Fatalf("session cookies = %d, want 1", len(cookies))
			}
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/events", nil)
			if err != nil {
				t.Fatal(err)
			}
			request.AddCookie(cookies[0])
			response, err := client.Do(request)
			if err != nil {
				t.Fatalf("open authenticated stream: %v", err)
			}
			defer response.Body.Close()
			if response.ProtoMajor != test.wantProto {
				t.Fatalf("stream protocol = %s, want HTTP/%d", response.Proto, test.wantProto)
			}
			if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "text/event-stream" {
				t.Fatalf("stream response = %d %v", response.StatusCode, response.Header)
			}
			if got := response.Header.Get("Connection"); got != "" {
				t.Fatalf("connection-specific response header = %q, want none", got)
			}
			if response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("X-Accel-Buffering") != "no" {
				t.Fatalf("stream proxy/cache headers = %v", response.Header)
			}

			reader := bufio.NewReader(response.Body)
			if got := readSSEFrame(t, reader); !strings.Contains(strings.Join(got, "\n"), "retry: 3000") {
				t.Fatalf("initial retry frame = %q", got)
			}
			if got := strings.Join(readSSEFrame(t, reader), "\n"); got != "event: ready\ndata: {\"retry_ms\":3000}" {
				t.Fatalf("ready frame = %q", got)
			}

			// The stream should remain writable after the server's normal
			// response deadline has elapsed because each frame resets it.
			time.Sleep(1100 * time.Millisecond)
			store.events.publishSnapshot("generation", "http_stream_snapshot", sha256.Sum256([]byte("snapshot")), map[string]string{"id": "http_stream_snapshot"})
			got := strings.Join(readSSEFrame(t, reader), "\n")
			if !strings.Contains(got, "event: generation") || !strings.Contains(got, `"id":"http_stream_snapshot"`) {
				t.Fatalf("live snapshot frame = %q", got)
			}
		})
	}
}

func readSSEFrame(t *testing.T, reader *bufio.Reader) []string {
	t.Helper()
	var lines []string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read SSE frame: %v", err)
		}
		line = strings.TrimSuffix(line, "\n")
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			if len(lines) == 0 {
				continue
			}
			return lines
		}
		lines = append(lines, line)
	}
}
