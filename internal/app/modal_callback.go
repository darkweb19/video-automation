package app

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"math"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	modalCallbackBaseURLEnv           = "PUBLIC_BASE_URL"
	modalCallbackPath                 = "/api/provider-callbacks/modal"
	modalCallbackInitialRecoveryDelay = 15 * time.Minute
	modalCallbackMaxRecoveryDelay     = 6 * time.Hour
)

var (
	ErrModalCallbackPending       = errors.New("Modal callback is awaiting generation persistence")
	ErrModalCallbackUnauthorized  = errors.New("Modal callback authentication failed")
	ErrModalCallbackUnavailable   = errors.New("Modal callback target is unavailable")
	ErrModalCallbackInvalidTarget = errors.New("Modal callback target is invalid")
)

// modalCompletionCallback is deliberately smaller than a provider status
// response. Modal only sends terminal state to this endpoint.
type modalCompletionCallback struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Model   string `json:"model,omitempty"`
	CostUSD string `json:"cost_usd,omitempty"`
	Error   string `json:"error,omitempty"`
}

type modalCallbackOutcome struct {
	GenerationID string
	ProjectID    string
	Ignored      bool
}

func configuredCallbackBaseURL() string {
	return strings.TrimSpace(os.Getenv(modalCallbackBaseURLEnv))
}

func validateModalCallbackBaseURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("PUBLIC_BASE_URL must be an HTTPS URL without query or fragment")
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func (a *dashboardApp) modalCallbackURL() (string, error) {
	baseURL, err := validateModalCallbackBaseURL(a.callbackBaseURL)
	if err != nil {
		return "", errors.New("Modal callbacks require PUBLIC_BASE_URL to be an HTTPS URL reachable from Modal")
	}
	return baseURL + modalCallbackPath, nil
}

func newModalCallbackToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func validModalCallbackToken(token string) bool {
	if len(token) < 43 || len(token) > 128 {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil && len(raw) >= 32
}

// modalCallbackTokenHash is the only callback credential persisted by the
// dashboard. The raw bearer-equivalent token exists only in the request to
// Modal and its private job record.
func modalCallbackTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func validModalCallbackCost(value string) bool {
	if value == "" {
		return true
	}
	if len(value) > 32 {
		return false
	}
	parsed, err := strconv.ParseFloat(value, 64)
	return err == nil && parsed >= 0 && !math.IsInf(parsed, 0) && !math.IsNaN(parsed)
}

func modalCallbackRecoveryAt(now time.Time, attempts int) int64 {
	if attempts < 0 {
		attempts = 0
	}
	delay := modalCallbackInitialRecoveryDelay * time.Duration(1<<min(attempts, 5))
	if delay > modalCallbackMaxRecoveryDelay {
		delay = modalCallbackMaxRecoveryDelay
	}
	return now.Add(delay).Unix()
}
