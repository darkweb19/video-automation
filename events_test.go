package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type deadlineEventResponseWriter struct {
	header    http.Header
	body      strings.Builder
	writeErr  error
	flushErr  error
	flushes   int
	deadlines []time.Time
}

func (w *deadlineEventResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *deadlineEventResponseWriter) Write(data []byte) (int, error) {
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	return w.body.Write(data)
}

func (w *deadlineEventResponseWriter) WriteHeader(int) {}

func (w *deadlineEventResponseWriter) Flush() {}

func (w *deadlineEventResponseWriter) FlushError() error {
	w.flushes++
	return w.flushErr
}

func (w *deadlineEventResponseWriter) SetWriteDeadline(deadline time.Time) error {
	w.deadlines = append(w.deadlines, deadline)
	return nil
}

func assertEventFrameDeadlineReset(t *testing.T, writer *deadlineEventResponseWriter, started time.Time) {
	t.Helper()
	if len(writer.deadlines) != 2 {
		t.Fatalf("write deadline calls = %d, want set and reset", len(writer.deadlines))
	}
	if deadline := writer.deadlines[0]; !deadline.After(started) || deadline.After(started.Add(eventFrameWriteTimeout+time.Second)) {
		t.Fatalf("frame deadline = %s, want approximately %s after start", deadline.Sub(started), eventFrameWriteTimeout)
	}
	if !writer.deadlines[1].IsZero() {
		t.Fatalf("frame deadline was not reset: %s", writer.deadlines[1])
	}
}

func TestEventsEndpointRequiresSessionAndSendsReadyContract(t *testing.T) {
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
	handler := NewDashboardHandler(store, security, nil)

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/events", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d body=%s", unauthorized.Code, unauthorized.Body.String())
	}

	sessionRecorder := httptest.NewRecorder()
	if err := security.NewSession(sessionRecorder, httptest.NewRequest(http.MethodGet, "/", nil), "events-user"); err != nil {
		t.Fatal(err)
	}
	cookies := sessionRecorder.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("session cookies=%d", len(cookies))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequest(http.MethodGet, "/api/events", nil).WithContext(ctx)
	request.AddCookie(cookies[0])
	stream := httptest.NewRecorder()
	handler.ServeHTTP(stream, request)
	if stream.Code != http.StatusOK || stream.Header().Get("Content-Type") != "text/event-stream" || !strings.Contains(stream.Body.String(), "event: ready\ndata: {\"retry_ms\":3000}") {
		t.Fatalf("SSE contract status=%d headers=%v body=%q", stream.Code, stream.Header(), stream.Body.String())
	}
}

func TestWriteEventFrameSetsAndResetsDeadline(t *testing.T) {
	writer := &deadlineEventResponseWriter{}
	started := time.Now()
	if err := writeEventFrame(writer, http.NewResponseController(writer), "event: ping\ndata: {}\n\n"); err != nil {
		t.Fatal(err)
	}
	assertEventFrameDeadlineReset(t, writer, started)
	if got := writer.body.String(); got != "event: ping\ndata: {}\n\n" || writer.flushes != 1 {
		t.Fatalf("frame body/flushes = %q/%d", got, writer.flushes)
	}
}

func TestEventsFrameErrorsUnsubscribe(t *testing.T) {
	writeFailure := errors.New("write failed")
	flushFailure := errors.New("flush failed")
	for _, test := range []struct {
		name        string
		writeErr    error
		flushErr    error
		wantFlushes int
	}{
		{name: "write", writeErr: writeFailure},
		{name: "flush", flushErr: flushFailure, wantFlushes: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			hub := newEventHub()
			app := &dashboardApp{store: &Store{events: hub}}
			writer := &deadlineEventResponseWriter{writeErr: test.writeErr, flushErr: test.flushErr}
			started := time.Now()
			app.events(writer, httptest.NewRequest(http.MethodGet, "/api/events", nil))

			assertEventFrameDeadlineReset(t, writer, started)
			if writer.flushes != test.wantFlushes {
				t.Fatalf("flushes = %d, want %d", writer.flushes, test.wantFlushes)
			}
			if got := len(hub.clients); got != 0 {
				t.Fatalf("writer error kept SSE subscriber: %d", got)
			}
		})
	}
}
