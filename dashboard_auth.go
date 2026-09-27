package main

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; media-src 'self' blob:; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

func (a *dashboardApp) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := a.security.SessionUser(r); !ok {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *dashboardApp) requirePasswordChanged(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, ok := a.security.Session(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		if identity.MustChangePassword {
			writeJSON(w, http.StatusPreconditionRequired, map[string]any{"error": "password change required", "must_change_password": true})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func mutationAllowed(w http.ResponseWriter, r *http.Request) bool {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross-origin requests are not allowed")
		return false
	}
	return true
}

func (a *dashboardApp) login(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) || !isJSON(r.Header.Get("Content-Type")) {
		if !isJSON(r.Header.Get("Content-Type")) {
			writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		}
		return
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if decodeJSONBody(w, r, &input) != nil {
		return
	}
	username := strings.TrimSpace(input.Username)
	if a.limiter == nil {
		a.limiter = newLoginThrottle()
	}
	if !a.limiter.Allow(clientIP(r)) {
		writeError(w, http.StatusTooManyRequests, "too many login attempts; try again later")
		return
	}
	hash, mustChange, err := a.store.UserAuthentication(username)
	if err != nil || !checkPassword(hash, input.Password) {
		a.limiter.Failed(clientIP(r))
		writeError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	a.limiter.Succeeded(clientIP(r))
	if err := a.security.NewSession(w, r, username); err != nil {
		a.logger.Error("create session failed")
		writeError(w, http.StatusInternalServerError, "unable to log in")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"username": username, "must_change_password": mustChange})
}

func (a *dashboardApp) session(w http.ResponseWriter, r *http.Request) {
	identity, ok := a.security.Session(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"username": identity.Username, "must_change_password": identity.MustChangePassword})
}

func (a *dashboardApp) recoverPassword(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) || !isJSON(r.Header.Get("Content-Type")) {
		if !isJSON(r.Header.Get("Content-Type")) {
			writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		}
		return
	}
	if a.recoveryLimiter == nil {
		a.recoveryLimiter = newLoginThrottle()
	}
	ip := clientIP(r)
	if !a.recoveryLimiter.Allow(ip) {
		writeError(w, http.StatusTooManyRequests, "too many recovery attempts; try again later")
		return
	}
	var input struct {
		Username    string `json:"username"`
		Code        string `json:"code"`
		NewPassword string `json:"new_password"`
	}
	if decodeJSONBody(w, r, &input) != nil {
		return
	}
	username := strings.TrimSpace(input.Username)
	code := normalizeRecoveryCode(input.Code)
	if len(input.NewPassword) < 10 || len(input.NewPassword) > 200 {
		writeError(w, http.StatusBadRequest, "new password must be 10–200 characters")
		return
	}
	if username == "" || len(code) != 16 {
		a.recoveryLimiter.Failed(ip)
		writeError(w, http.StatusBadRequest, "invalid or expired recovery code")
		return
	}
	newHash, err := hashPassword(input.NewPassword)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to reset password")
		return
	}
	ok, err := a.store.ConsumeRecoveryCode(username, recoveryCodeHash(code), newHash, time.Now().Unix())
	if err != nil {
		a.logger.Error("password recovery failed")
		writeError(w, http.StatusInternalServerError, "unable to reset password")
		return
	}
	if !ok {
		a.recoveryLimiter.Failed(ip)
		writeError(w, http.StatusBadRequest, "invalid or expired recovery code")
		return
	}
	a.recoveryLimiter.Succeeded(ip)
	a.clearVaultGrants()
	a.security.Logout(w, r)
	writeJSON(w, http.StatusOK, map[string]string{"status": "password reset"})
}

func (a *dashboardApp) logout(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) {
		return
	}
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		a.clearVaultGrantsForSession(tokenHash(cookie.Value))
	}
	a.security.Logout(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (a *dashboardApp) updatePassword(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) {
		return
	}
	if !isJSON(r.Header.Get("Content-Type")) {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	identity, _ := a.security.Session(r)
	var input struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if decodeJSONBody(w, r, &input) != nil {
		return
	}
	if len(input.NewPassword) < 10 || len(input.NewPassword) > 200 {
		writeError(w, http.StatusBadRequest, "new password must be 10–200 characters")
		return
	}
	currentHash, err := a.store.PasswordHash(identity.Username)
	if err != nil || !checkPassword(currentHash, input.CurrentPassword) {
		writeError(w, http.StatusUnauthorized, "current password is incorrect")
		return
	}
	newHash, err := hashPassword(input.NewPassword)
	if err != nil || a.store.ChangePassword(identity.Username, newHash) != nil {
		writeError(w, http.StatusInternalServerError, "unable to change password")
		return
	}
	a.clearVaultGrants()
	if err := a.security.NewSession(w, r, identity.Username); err != nil {
		writeError(w, http.StatusInternalServerError, "password changed; please log in again")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "password changed"})
}

const (
	loginFailureLimit = 5
	loginWindow       = 15 * time.Minute
	loginBlock        = 15 * time.Minute
)

type loginAttempt struct {
	failures              int
	resetAt, blockedUntil time.Time
}
type loginThrottle struct {
	mu       sync.Mutex
	attempts map[string]loginAttempt
	now      func() time.Time
}

func newLoginThrottle() *loginThrottle {
	return &loginThrottle{attempts: make(map[string]loginAttempt), now: time.Now}
}

func (t *loginThrottle) Allow(ip string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	t.cleanupLocked(now)
	attempt, ok := t.attempts[ip]
	return !ok || !attempt.blockedUntil.After(now)
}
func (t *loginThrottle) Failed(ip string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	t.cleanupLocked(now)
	attempt := t.attempts[ip]
	if attempt.resetAt.Before(now) {
		attempt = loginAttempt{resetAt: now.Add(loginWindow)}
	}
	attempt.failures++
	if attempt.failures >= loginFailureLimit {
		attempt.blockedUntil = now.Add(loginBlock)
	}
	t.attempts[ip] = attempt
}
func (t *loginThrottle) Succeeded(ip string) { t.mu.Lock(); delete(t.attempts, ip); t.mu.Unlock() }
func (t *loginThrottle) cleanupLocked(now time.Time) {
	for ip, attempt := range t.attempts {
		if !attempt.resetAt.After(now) && !attempt.blockedUntil.After(now) {
			delete(t.attempts, ip)
		}
	}
}
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	if r.RemoteAddr != "" {
		return r.RemoteAddr
	}
	return "unknown"
}
