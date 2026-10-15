package app

import (
	"strings"
	"testing"
)

func TestYouTubeDiagnosticStderrKeepsBoundedTail(t *testing.T) {
	var captured youtubeDiagnosticStderr
	secret := "https://signed.example/video?token=do-not-log"
	if _, err := captured.Write([]byte(secret + strings.Repeat("x", youtubeDiagnosticStderrLimit))); err != nil {
		t.Fatal(err)
	}
	if len(captured.tail) != youtubeDiagnosticStderrLimit {
		t.Fatalf("captured %d bytes; want at most %d", len(captured.tail), youtubeDiagnosticStderrLimit)
	}
	if strings.Contains(string(captured.tail), secret) {
		t.Fatal("bounded buffer failed to discard old text")
	}
	if _, err := captured.Write([]byte("\nERROR: HTTP Error 403: Forbidden")); err != nil {
		t.Fatal(err)
	}
	if category := classifyYouTubeDownloaderStderr(captured.tail); category != "upstream_http_403" {
		t.Fatalf("category = %q; want upstream_http_403", category)
	}
}

func TestYouTubeDiagnosticStderrSafeFixedCategories(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		expected string
	}{
		{"empty", "", "no_stderr"},
		{"signed URL", "ERROR: https://media.example/v?sig=SUPER_SECRET HTTP Error 403: Forbidden", "upstream_http_403"},
		{"proxy", "ERROR: Tunnel connection failed: 403 Forbidden", "proxy_failure"},
		{"proxy HTTP CONNECT", "Proxy returned HTTP CONNECT 403", "proxy_failure"},
		{"rate limit", "HTTP Error 429: Too Many Requests", "upstream_rate_limited"},
		{"bot", "Sign in to confirm you're not a bot", "bot_challenge"},
		{"geo block", "This video is not available in your country", "geo_blocked"},
		{"format", "Requested format is not available", "format_unavailable"},
		{"unknown", "unexpected message TOP_SECRET", "unclassified_extractor_error"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			actual := classifyYouTubeDownloaderStderr([]byte(test.input))
			if actual != test.expected {
				t.Fatalf("category = %q; want %q", actual, test.expected)
			}
			if strings.Contains(actual, "SECRET") || strings.Contains(actual, "media.example") {
				t.Fatalf("upstream text was exposed in category: %q", actual)
			}
		})
	}
}

func TestYouTubeDownloaderExitCode(t *testing.T) {
	if got := youtubeDownloaderExitCode(nil); got != 0 {
		t.Fatalf("successful exit should be 0: %d", got)
	}
}
