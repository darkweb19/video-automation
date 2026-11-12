package app

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"cloud.google.com/go/auth"
	"google.golang.org/genai"
)

func TestClippingGeminiAttemptClassificationIsSafeAndConservative(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		want       clippingGeminiAttemptOutcome
		wantStatus int
	}{
		{name: "bad request", status: http.StatusBadRequest, want: clippingGeminiAttemptExplicitRejection, wantStatus: http.StatusBadRequest},
		{name: "unauthorized", status: http.StatusUnauthorized, want: clippingGeminiAttemptExplicitRejection, wantStatus: http.StatusUnauthorized},
		{name: "forbidden", status: http.StatusForbidden, want: clippingGeminiAttemptExplicitRejection, wantStatus: http.StatusForbidden},
		{name: "rate limited", status: http.StatusTooManyRequests, want: clippingGeminiAttemptExplicitRejection, wantStatus: http.StatusTooManyRequests},
		{name: "request timeout", status: http.StatusRequestTimeout, want: clippingGeminiAttemptUnknownPostSubmission, wantStatus: http.StatusRequestTimeout},
		{name: "client closed request", status: 499, want: clippingGeminiAttemptUnknownPostSubmission, wantStatus: 499},
		{name: "server error", status: http.StatusInternalServerError, want: clippingGeminiAttemptUnknownPostSubmission, wantStatus: http.StatusInternalServerError},
		{name: "bad gateway", status: http.StatusBadGateway, want: clippingGeminiAttemptUnknownPostSubmission, wantStatus: http.StatusBadGateway},
		{name: "temporary redirect", status: http.StatusTemporaryRedirect, want: clippingGeminiAttemptUnknownPostSubmission, wantStatus: http.StatusTemporaryRedirect},
		{name: "permanent redirect", status: http.StatusPermanentRedirect, want: clippingGeminiAttemptUnknownPostSubmission, wantStatus: http.StatusPermanentRedirect},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tracker := &clippingGeminiAttemptTracker{}
			tracker.submitted.Store(true)
			tracker.httpStatus.Store(int32(test.status))
			const providerText = "private provider body and https://private.example/request"
			got := clippingGeminiClassifyCallError(genai.APIError{Code: test.status, Message: providerText}, tracker, nil)
			if got.outcome != test.want || got.httpStatus != test.wantStatus {
				t.Fatalf("classification = (%v, %d), want (%v, %d)", got.outcome, got.httpStatus, test.want, test.wantStatus)
			}
			if strings := got.Error(); containsAny(strings, providerText, "private.example") {
				t.Errorf("safe error leaked provider details: %q", strings)
			}
			var leaked genai.APIError
			if errors.As(got, &leaked) {
				t.Errorf("attempt error unwraps raw SDK API error: %#v", leaked)
			}
		})
	}
}

func TestClippingGeminiAttemptClassificationTracksSubmissionAndContext(t *testing.T) {
	t.Run("pre-submission context cancellation", func(t *testing.T) {
		got := clippingGeminiClassifyCallError(context.Canceled, &clippingGeminiAttemptTracker{}, context.Canceled)
		if got.outcome != clippingGeminiAttemptNotSubmitted || !errors.Is(got, context.Canceled) {
			t.Fatalf("classification = (%v, %v), want not-submitted with context.Canceled", got.outcome, got)
		}
	})

	t.Run("post-submission deadline", func(t *testing.T) {
		tracker := &clippingGeminiAttemptTracker{}
		tracker.submitted.Store(true)
		got := clippingGeminiClassifyCallError(context.DeadlineExceeded, tracker, context.DeadlineExceeded)
		if got.outcome != clippingGeminiAttemptUnknownPostSubmission || !errors.Is(got, context.DeadlineExceeded) {
			t.Fatalf("classification = (%v, %v), want unknown with context deadline", got.outcome, got)
		}
	})

	t.Run("invalid fixed destination", func(t *testing.T) {
		tracker := &clippingGeminiAttemptTracker{}
		got := clippingGeminiClassifyCallError(errClippingGeminiInvalidDestination, tracker, nil)
		if got.outcome != clippingGeminiAttemptNotSubmitted || !errors.Is(got, errClippingGeminiInvalidDestination) {
			t.Fatalf("classification = (%v, %v), want not-submitted invalid destination", got.outcome, got)
		}
	})
}

func TestNewClippingGeminiClientPinsDependenciesAndHidesAuthFailures(t *testing.T) {
	calls := 0
	client, err := newClippingGeminiClientWithDependencies(context.Background(), testClippingGeminiConfig(), clippingGeminiClientDependencies{
		BaseTransport: clippingGeminiRoundTripper(func(*http.Request) (*http.Response, error) {
			calls++
			return nil, errors.New("transport must not run during client construction")
		}),
		Authorize: func(config *genai.ClientConfig) error {
			if config.Backend != genai.BackendEnterprise || config.APIKey != "" || config.Project != "framevault-test-123" || config.Location != "global" {
				t.Errorf("SDK routing/auth config = %#v, want fixed Cloud ADC configuration", config)
			}
			if config.HTTPClient == nil || config.HTTPClient.Timeout != clippingGeminiRequestTimeout || config.HTTPClient.CheckRedirect == nil {
				t.Errorf("HTTP client = %#v, want bounded timeout and redirect denial", config.HTTPClient)
			}
			config.Credentials = &auth.Credentials{}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("newClippingGeminiClientWithDependencies() error = %v", err)
	}
	if client == nil || client.sdk == nil {
		t.Fatal("constructor returned an empty client")
	}
	if calls != 0 {
		t.Fatalf("transport calls during construction = %d, want 0", calls)
	}

	_, err = newClippingGeminiClientWithDependencies(context.Background(), ClippingGeminiConfig{ProjectID: "Invalid_Project", Location: "global"}, clippingGeminiClientDependencies{
		BaseTransport: clippingGeminiRoundTripper(func(*http.Request) (*http.Response, error) {
			calls++
			return nil, errors.New("transport must not run for invalid configuration")
		}),
		Authorize: func(*genai.ClientConfig) error {
			t.Error("authorization must not run for invalid configuration")
			return nil
		},
	})
	var invalidConfigAttempt *clippingGeminiAttemptError
	if !errors.As(err, &invalidConfigAttempt) || invalidConfigAttempt.outcome != clippingGeminiAttemptNotSubmitted || !errors.Is(err, ErrClippingGeminiInvalidConfig) {
		t.Fatalf("invalid configuration error = %#v, want safe not-submitted attempt", err)
	}
	if calls != 0 {
		t.Fatalf("transport calls after invalid configuration = %d, want 0", calls)
	}

	const authFailureDetails = "credential response body at /private/path"
	_, err = newClippingGeminiClientWithDependencies(context.Background(), testClippingGeminiConfig(), clippingGeminiClientDependencies{
		Authorize: func(*genai.ClientConfig) error { return errors.New(authFailureDetails) },
	})
	if err == nil {
		t.Fatal("constructor with failed authorization succeeded")
	}
	var attempt *clippingGeminiAttemptError
	if !errors.As(err, &attempt) || attempt.outcome != clippingGeminiAttemptNotSubmitted || !errors.Is(err, errClippingGeminiCredentialsUnavailable) {
		t.Fatalf("authorization error = %#v, want safe not-submitted attempt", err)
	}
	if containsAny(err.Error(), authFailureDetails, "/private/path") {
		t.Errorf("authorization error leaked details: %q", err)
	}
}

func TestClippingGeminiClientAnalyzeRejectsBeforeSubmission(t *testing.T) {
	var calls int
	client, err := newClippingGeminiClientWithDependencies(context.Background(), testClippingGeminiConfig(), clippingGeminiClientDependencies{
		BaseTransport: clippingGeminiRoundTripper(func(*http.Request) (*http.Response, error) {
			calls++
			return nil, errors.New("unexpected provider call")
		}),
		Authorize: func(config *genai.ClientConfig) error {
			config.Credentials = &auth.Credentials{}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("constructor error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = client.Analyze(ctx, testClippingGeminiRequest("https://www.youtube.com/watch?v=abcdefghijk"))
	var attempt *clippingGeminiAttemptError
	if !errors.As(err, &attempt) || attempt.outcome != clippingGeminiAttemptNotSubmitted || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Analyze() error = %#v, want not-submitted context.Canceled", err)
	}
	if calls != 0 {
		t.Fatalf("provider calls = %d, want 0", calls)
	}

	_, err = client.Analyze(context.Background(), testClippingGeminiRequest("https://attacker.example/path?secret=do-not-echo"))
	if !errors.As(err, &attempt) || attempt.outcome != clippingGeminiAttemptNotSubmitted || !errors.Is(err, ErrClippingGeminiInvalidRequest) {
		t.Fatalf("invalid request Analyze() error = %#v, want safe not-submitted error", err)
	}
	if containsAny(err.Error(), "attacker.example", "do-not-echo") {
		t.Errorf("invalid request error leaked URL data: %q", err)
	}
	if calls != 0 {
		t.Fatalf("provider calls after invalid input = %d, want 0", calls)
	}
}

type clippingGeminiRoundTripper func(*http.Request) (*http.Response, error)

func (f clippingGeminiRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func containsAny(value string, fragments ...string) bool {
	for _, fragment := range fragments {
		if fragment != "" && strings.Contains(value, fragment) {
			return true
		}
	}
	return false
}
