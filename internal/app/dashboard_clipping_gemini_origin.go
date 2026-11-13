package app

import (
	"net/http"
	"net/url"
	"strings"
)

// Preserve the dashboard's host-based origin check for TLS-terminating
// proxies, while requiring an Origin header to be a single serialized origin.
func clippingGeminiMutationAllowed(w http.ResponseWriter, r *http.Request) bool {
	if !mutationAllowed(w, r) {
		return false
	}
	origins := r.Header.Values("Origin")
	if len(origins) == 0 {
		return true
	}
	if len(origins) == 1 {
		origin, err := url.Parse(origins[0])
		if err == nil && (origin.Scheme == "http" || origin.Scheme == "https") && origin.Host != "" &&
			origin.User == nil && origin.Opaque == "" && origin.Path == "" && !strings.ContainsAny(origins[0], "?#") {
			return true
		}
	}
	writeError(w, http.StatusForbidden, "cross-origin requests are not allowed")
	return false
}
