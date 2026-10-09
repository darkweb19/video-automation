package app

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"video-automation/internal/webui"
)

func TestDashboardClippingStaticAssets(t *testing.T) {
	handler := NewDashboardHandler(newTestStore(t), nil, nil)
	tests := []struct {
		name        string
		asset       string
		target      string
		contentType string
	}{
		{
			name:        "javascript",
			asset:       "static/clipping.js",
			target:      "/static/clipping.js?v=clipper-ui-test",
			contentType: "application/javascript; charset=utf-8",
		},
		{
			name:        "stylesheet",
			asset:       "static/clipping.css",
			target:      "/static/clipping.css?v=clipper-ui-test",
			contentType: "text/css; charset=utf-8",
		},
	}

	const csp = "default-src 'self'; img-src 'self' data:; media-src 'self' blob:; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			want, err := webui.Files.ReadFile(test.asset)
			if err != nil {
				t.Fatalf("read embedded %s: %v", test.asset, err)
			}

			response := request(handler, http.MethodGet, test.target, "")
			if response.Code != http.StatusOK {
				t.Fatalf("GET %s = %d: %s", test.target, response.Code, response.Body.String())
			}
			if got := response.Header().Get("Content-Type"); got != test.contentType {
				t.Errorf("Content-Type = %q, want %q", got, test.contentType)
			}
			if got := response.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", got)
			}
			if got := response.Header().Get("Content-Security-Policy"); got != csp {
				t.Errorf("Content-Security-Policy = %q, want dashboard policy", got)
			}
			if got := response.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
			}

			body := response.Body.Bytes()
			if len(body) == 0 || !bytes.Equal(body, want) {
				t.Errorf("response body does not match embedded %s (%d response bytes, %d embedded bytes)", test.asset, len(body), len(want))
			}
			lowerBody := strings.ToLower(string(body))
			if strings.HasPrefix(strings.TrimSpace(lowerBody), "<!doctype html") || strings.Contains(lowerBody, "404 page not found") {
				t.Errorf("response for %s contains HTML or a 404 body", test.target)
			}
		})
	}
}
