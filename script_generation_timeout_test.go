package main

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

type deadlineCaptureTransport struct {
	deadline time.Time
	ok       bool
}

func (transport *deadlineCaptureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.deadline, transport.ok = request.Context().Deadline()
	return nil, errors.New("stop after capturing request deadline")
}

func TestGenerateStoryPlanAllowsFreeModelQueueTime(t *testing.T) {
	transport := &deadlineCaptureTransport{}
	client := NewOpenRouterClient("test-key")
	client.HTTPClient = &http.Client{Transport: transport}

	_, _, _ = client.GenerateStoryPlan(context.Background(), "a five-scene rescue story")

	if !transport.ok {
		t.Fatal("story request context has no deadline")
	}
	remaining := time.Until(transport.deadline)
	if remaining < storyGenerationTimeout-time.Second || remaining > storyGenerationTimeout {
		t.Fatalf("story request deadline remaining = %s, want approximately %s", remaining, storyGenerationTimeout)
	}
	if defaultOpenRouterStoryHTTPClient.Timeout <= storyGenerationTimeout {
		t.Fatalf("story HTTP timeout = %s, must exceed story context timeout %s", defaultOpenRouterStoryHTTPClient.Timeout, storyGenerationTimeout)
	}
	if defaultOpenRouterHTTPClient.Timeout != 45*time.Second {
		t.Fatalf("ordinary OpenRouter timeout changed to %s", defaultOpenRouterHTTPClient.Timeout)
	}
}

func TestGenerateStoryPlanKeepsEarlierCallerDeadline(t *testing.T) {
	transport := &deadlineCaptureTransport{}
	client := NewOpenRouterClient("test-key")
	client.HTTPClient = &http.Client{Transport: transport}
	callerTimeout := 10 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), callerTimeout)
	defer cancel()

	_, _, _ = client.GenerateStoryPlan(ctx, "a five-scene rescue story")

	remaining := time.Until(transport.deadline)
	if !transport.ok || remaining < callerTimeout-time.Second || remaining > callerTimeout {
		t.Fatalf("story request did not preserve caller deadline: ok=%v remaining=%s", transport.ok, remaining)
	}
}
