package main

import (
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	vaultCodeSetting    = "vault_code"
	vaultGrantLifetime  = sessionLifetime
	vaultAttemptLimit   = 3
	vaultAttemptWindow  = 15 * time.Minute
	vaultAttemptLockout = 15 * time.Minute
)

var (
	ErrVaultItemInVault    = errors.New("vault item is already in the vault")
	ErrVaultItemNotInVault = errors.New("vault item is not in the vault")
	ErrVaultItemNotReady   = errors.New("vault item is not a completed video")
)

type VaultItem struct {
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	Title     string `json:"title"`
	Model     string `json:"model"`
	Status    string `json:"status"`
	CreatedAt int64  `json:"created_at"`
	Duration  int    `json:"duration,omitempty"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
	CostUSD   string `json:"cost_usd,omitempty"`
}

type vaultGrant struct {
	username    string
	authSession string
	expiresAt   time.Time
}

type vaultAttempt struct {
	failures              int
	resetAt, blockedUntil time.Time
}

type vaultAttemptLimiter struct {
	mu       sync.Mutex
	attempts map[string]vaultAttempt
	now      func() time.Time
}

type vaultRuntime struct {
	mu        sync.Mutex
	verifyMu  sync.Mutex
	lockEpoch uint64
	grants    map[string]vaultGrant
	limiter   *vaultAttemptLimiter
	random    io.Reader
}

func newVaultRuntime() *vaultRuntime {
	return &vaultRuntime{
		grants:  make(map[string]vaultGrant),
		limiter: &vaultAttemptLimiter{attempts: make(map[string]vaultAttempt), now: time.Now},
		random:  rand.Reader,
	}
}

func (l *vaultAttemptLimiter) Allow(keys ...string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for key, attempt := range l.attempts {
		if !attempt.resetAt.After(now) && !attempt.blockedUntil.After(now) {
			delete(l.attempts, key)
		}
	}
	for _, key := range keys {
		if attempt, ok := l.attempts[key]; ok && attempt.blockedUntil.After(now) {
			return false
		}
	}
	return true
}

func (l *vaultAttemptLimiter) Failed(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for _, key := range keys {
		attempt := l.attempts[key]
		if !attempt.resetAt.After(now) {
			attempt = vaultAttempt{resetAt: now.Add(vaultAttemptWindow)}
		}
		attempt.failures++
		if attempt.failures >= vaultAttemptLimit {
			attempt.blockedUntil = now.Add(vaultAttemptLockout)
		}
		l.attempts[key] = attempt
	}
}

func (l *vaultAttemptLimiter) Succeeded(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, key := range keys {
		delete(l.attempts, key)
	}
}

func validVaultCode(code string) bool {
	if len(code) != 4 {
		return false
	}
	for _, digit := range code {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

func (a *dashboardApp) vaultCodeConfigured() bool {
	_, err := a.store.Setting(vaultCodeSetting)
	return err == nil
}

func (a *dashboardApp) vaultStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"configured": a.vaultCodeConfigured()})
}

func (a *dashboardApp) updateVaultCode(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) {
		return
	}
	if !isJSON(r.Header.Get("Content-Type")) {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	var input struct {
		CurrentCode string `json:"current_code"`
		NewCode     string `json:"new_code"`
	}
	if decodeJSONBody(w, r, &input) != nil {
		return
	}
	if !validVaultCode(input.NewCode) {
		writeError(w, http.StatusBadRequest, "Vault code must be exactly four digits")
		return
	}
	// Serialize verification and rotation so concurrent guesses cannot all pass
	// the limiter check before their failures are recorded, and an old-code
	// unlock cannot install a grant after a successful rotation clears grants.
	a.vault.verifyMu.Lock()
	defer a.vault.verifyMu.Unlock()

	encoded, err := a.store.Setting(vaultCodeSetting)
	configured := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "unable to load Vault code")
		return
	}
	if configured {
		identity, ok := a.security.Session(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		keys := []string{"user:" + identity.Username, "ip:" + clientIP(r)}
		if !a.vault.limiter.Allow(keys...) {
			writeError(w, http.StatusTooManyRequests, "Too many incorrect codes. Try again in 15 minutes.")
			return
		}
		if !validVaultCode(input.CurrentCode) {
			writeError(w, http.StatusBadRequest, "Enter the current four-digit Vault code")
			return
		}
		current, decryptErr := a.security.DecryptSetting(vaultCodeSetting, encoded)
		if decryptErr != nil {
			a.logger.Error("Vault code verification failed")
			writeError(w, http.StatusInternalServerError, "unable to verify the saved Vault code")
			return
		}
		if subtle.ConstantTimeCompare([]byte(current), []byte(input.CurrentCode)) != 1 {
			a.vault.limiter.Failed(keys...)
			writeError(w, http.StatusForbidden, "The current Vault code is incorrect")
			return
		}
		a.vault.limiter.Succeeded(keys...)
	} else if input.CurrentCode != "" {
		writeError(w, http.StatusBadRequest, "A current Vault code is not set")
		return
	}

	encrypted, err := a.security.EncryptSetting(vaultCodeSetting, input.NewCode)
	if err != nil || a.store.SetSetting(vaultCodeSetting, encrypted) != nil {
		writeError(w, http.StatusInternalServerError, "unable to save Vault code")
		return
	}
	a.clearVaultGrants()
	writeJSON(w, http.StatusOK, map[string]bool{"configured": true})
}

func (a *dashboardApp) unlockVault(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) {
		return
	}
	if !isJSON(r.Header.Get("Content-Type")) {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	var input struct {
		Code string `json:"code"`
	}
	if decodeJSONBody(w, r, &input) != nil {
		return
	}
	if !validVaultCode(input.Code) {
		writeError(w, http.StatusBadRequest, "Enter a four-digit Vault code")
		return
	}
	identity, ok := a.security.Session(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	a.vault.mu.Lock()
	requestEpoch := a.vault.lockEpoch
	a.vault.mu.Unlock()
	// Keep the limit check, code comparison, and grant issuance in one critical
	// section. Otherwise concurrent bad guesses can exceed the attempt limit.
	a.vault.verifyMu.Lock()
	defer a.vault.verifyMu.Unlock()
	keys := []string{"user:" + identity.Username, "ip:" + clientIP(r)}
	if !a.vault.limiter.Allow(keys...) {
		writeError(w, http.StatusTooManyRequests, "Too many incorrect codes. Try again in 15 minutes.")
		return
	}

	encoded, err := a.store.Setting(vaultCodeSetting)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusConflict, "Set a Vault code in Settings first")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to load Vault code")
		return
	}
	stored, err := a.security.DecryptSetting(vaultCodeSetting, encoded)
	if err != nil {
		a.logger.Error("Vault code verification failed")
		writeError(w, http.StatusInternalServerError, "unable to verify the saved Vault code")
		return
	}
	if subtle.ConstantTimeCompare([]byte(stored), []byte(input.Code)) != 1 {
		a.vault.limiter.Failed(keys...)
		writeError(w, http.StatusForbidden, "The Vault code is incorrect")
		return
	}
	a.vault.limiter.Succeeded(keys...)

	raw := make([]byte, 32)
	if _, err := io.ReadFull(a.vault.random, raw); err != nil {
		writeError(w, http.StatusInternalServerError, "unable to unlock Vault")
		return
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	grant := vaultGrant{username: identity.Username, authSession: tokenHash(cookie.Value), expiresAt: time.Now().Add(vaultGrantLifetime)}
	a.vault.mu.Lock()
	if requestEpoch != a.vault.lockEpoch {
		a.vault.mu.Unlock()
		writeError(w, http.StatusLocked, "Vault was locked before this unlock completed")
		return
	}
	a.pruneVaultGrantsLocked(time.Now())
	for key, existing := range a.vault.grants {
		if existing.authSession == grant.authSession {
			delete(a.vault.grants, key)
		}
	}
	a.vault.grants[tokenHash(token)] = grant
	a.vault.mu.Unlock()
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"token": token, "expires_in_seconds": int(vaultGrantLifetime.Seconds())})
}

func vaultTokenFromRequest(r *http.Request) string {
	scheme, token, found := strings.Cut(strings.TrimSpace(r.Header.Get("Authorization")), " ")
	if !found || scheme != "Vault" || len(token) < 32 || len(token) > 128 {
		return ""
	}
	return token
}

func (a *dashboardApp) requireVaultUnlock(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, ok := a.security.Session(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		cookie, err := r.Cookie(sessionCookie)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		token := vaultTokenFromRequest(r)
		if token == "" {
			writeError(w, http.StatusLocked, "Unlock the Vault to continue")
			return
		}
		key := tokenHash(token)
		now := time.Now()
		a.vault.mu.Lock()
		a.pruneVaultGrantsLocked(now)
		grant, found := a.vault.grants[key]
		if found && grant.expiresAt.After(now) && grant.username == identity.Username && grant.authSession == tokenHash(cookie.Value) {
			a.vault.mu.Unlock()
			next.ServeHTTP(w, r)
			return
		}
		if found && !grant.expiresAt.After(now) {
			delete(a.vault.grants, key)
		}
		a.vault.mu.Unlock()
		writeError(w, http.StatusLocked, "Vault is locked. Enter your code to unlock it.")
	})
}

func (a *dashboardApp) lockVault(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) {
		return
	}
	// Wait for any PIN verification/grant issuance already in progress, then
	// revoke it. Unlock requests that entered before this lock also carry the
	// prior epoch and cannot issue a grant afterward.
	a.vault.verifyMu.Lock()
	defer a.vault.verifyMu.Unlock()
	_, ok := a.security.Session(r)
	if ok {
		if cookie, err := r.Cookie(sessionCookie); err == nil {
			a.clearVaultGrantsForSession(tokenHash(cookie.Value))
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *dashboardApp) clearVaultGrantsForSession(session string) {
	a.vault.mu.Lock()
	defer a.vault.mu.Unlock()
	a.vault.lockEpoch++
	for key, grant := range a.vault.grants {
		if grant.authSession == session {
			delete(a.vault.grants, key)
		}
	}
}

func (a *dashboardApp) pruneVaultGrantsLocked(now time.Time) {
	for key, grant := range a.vault.grants {
		if !grant.expiresAt.After(now) {
			delete(a.vault.grants, key)
		}
	}
}

func (a *dashboardApp) clearVaultGrants() {
	a.vault.mu.Lock()
	defer a.vault.mu.Unlock()
	a.vault.lockEpoch++
	a.vault.grants = make(map[string]vaultGrant)
}

func (a *dashboardApp) vaultItems(w http.ResponseWriter, _ *http.Request) {
	items, err := a.store.VaultItems()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to load Vault")
		return
	}
	if items == nil {
		items = []VaultItem{}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *dashboardApp) moveToVault(w http.ResponseWriter, r *http.Request) {
	a.updateVaultMembership(w, r, true)
}

func (a *dashboardApp) restoreFromVault(w http.ResponseWriter, r *http.Request) {
	a.updateVaultMembership(w, r, false)
}

func (a *dashboardApp) updateVaultMembership(w http.ResponseWriter, r *http.Request, inVault bool) {
	if !mutationAllowed(w, r) {
		return
	}
	if inVault && !a.vaultCodeConfigured() {
		writeError(w, http.StatusConflict, "Set a Vault code in Settings first")
		return
	}
	kind, id := r.PathValue("kind"), r.PathValue("id")
	if !safeID(id) || (kind != "generation" && kind != "project") {
		writeError(w, http.StatusBadRequest, "invalid Vault item")
		return
	}
	var err error
	if kind == "generation" {
		err = a.store.SetGenerationVaulted(id, inVault)
	} else {
		err = a.store.SetProjectVaulted(id, inVault)
	}
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, "video not found")
		return
	case errors.Is(err, ErrVaultItemNotReady):
		writeError(w, http.StatusConflict, "Only completed videos can be moved to the Vault")
		return
	case errors.Is(err, ErrVaultItemInVault), errors.Is(err, ErrVaultItemNotInVault):
		writeError(w, http.StatusConflict, "Vault item has already changed")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "unable to update Vault")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *dashboardApp) vaultVideo(w http.ResponseWriter, r *http.Request) {
	kind, id := r.PathValue("kind"), r.PathValue("id")
	if !safeID(id) || (kind != "generation" && kind != "project") {
		writeError(w, http.StatusBadRequest, "invalid Vault item")
		return
	}
	var videoPath, baseDir string
	if kind == "generation" {
		record, err := a.store.Generation(id)
		if err != nil || !record.InVault || record.Status != "completed" || !record.VideoReady {
			writeError(w, http.StatusNotFound, "Vault video not found")
			return
		}
		videoPath, baseDir = record.VideoPath, a.store.videoDir
	} else {
		project, err := a.store.Project(id)
		if err != nil || !project.InVault || project.Status != "completed" || !project.FinalVideoReady {
			writeError(w, http.StatusNotFound, "Vault video not found")
			return
		}
		videoPath, baseDir = project.FinalVideoPath, a.store.projectDir
	}

	cleanPath := filepath.Clean(videoPath)
	rel, err := filepath.Rel(baseDir, cleanPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		writeError(w, http.StatusForbidden, "invalid video path")
		return
	}
	file, err := os.Open(cleanPath)
	if err != nil {
		writeError(w, http.StatusNotFound, "Vault video not found")
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to read Vault video")
		return
	}
	contentType := mime.TypeByExtension(filepath.Ext(cleanPath))
	if contentType == "" {
		contentType = "video/mp4"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-%s.mp4"`, kind, id))
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	http.ServeContent(w, r, filepath.Base(cleanPath), info.ModTime(), file)
}

func (s *Store) SetGenerationVaulted(id string, inVault bool) error {
	value := 0
	if inVault {
		value = 1
	}
	result, err := s.db.Exec(`UPDATE generations SET in_vault=? WHERE id=? AND status='completed' AND video_path<>'' AND in_vault<>?`, value, id, value)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 1 {
		return nil
	}
	var status, videoPath string
	var current bool
	err = s.db.QueryRow(`SELECT status,video_path,in_vault FROM generations WHERE id=?`, id).Scan(&status, &videoPath, &current)
	if err != nil {
		return err
	}
	if status != "completed" || videoPath == "" {
		return ErrVaultItemNotReady
	}
	if current {
		return ErrVaultItemInVault
	}
	return ErrVaultItemNotInVault
}

func (s *Store) SetProjectVaulted(id string, inVault bool) error {
	value := 0
	if inVault {
		value = 1
	}
	result, err := s.db.Exec(`UPDATE video_projects SET in_vault=? WHERE id=? AND status='completed' AND final_video_path<>'' AND in_vault<>?`, value, id, value)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 1 {
		return nil
	}
	var status, videoPath string
	var current bool
	err = s.db.QueryRow(`SELECT status,final_video_path,in_vault FROM video_projects WHERE id=?`, id).Scan(&status, &videoPath, &current)
	if err != nil {
		return err
	}
	if status != "completed" || videoPath == "" {
		return ErrVaultItemNotReady
	}
	if current {
		return ErrVaultItemInVault
	}
	return ErrVaultItemNotInVault
}

func (s *Store) VaultItems() ([]VaultItem, error) {
	rows, err := s.db.Query(`
		SELECT 'generation',id,prompt,model,status,created_at,duration,size_bytes,cost_usd
		FROM generations WHERE in_vault=1 AND status='completed' AND video_path<>''
		UNION ALL
		SELECT 'project',id,COALESCE(NULLIF(title,''),topic),model,status,created_at,30,final_size_bytes,''
		FROM video_projects WHERE in_vault=1 AND status='completed' AND final_video_path<>''
		ORDER BY created_at DESC,id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]VaultItem, 0)
	for rows.Next() {
		var item VaultItem
		if err := rows.Scan(&item.Kind, &item.ID, &item.Title, &item.Model, &item.Status, &item.CreatedAt, &item.Duration, &item.SizeBytes, &item.CostUSD); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
