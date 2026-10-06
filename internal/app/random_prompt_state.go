package app

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

const (
	maxRandomPromptCooldownScopes = 128
	defaultModelCooldown          = 30 * time.Second
	defaultPlatformCooldown       = 30 * time.Second
)

// randomPromptState shares rate-limit cooldowns across dashboard requests.
// Credential scopes contain only a hash of the API key and are bounded so
// rotating credentials cannot grow this state without limit.
type randomPromptState struct {
	mu     sync.Mutex
	scopes map[string]*randomPromptCooldownScope
}

type randomPromptCooldownScope struct {
	platformUntil time.Time
	platformError upstreamError
	hasPlatform   bool
	freeUntil     time.Time
	freeError     upstreamError
	hasFree       bool
	models        map[string]time.Time
	lastUsed      time.Time
}

type randomPromptCooldownSnapshot struct {
	platformUntil time.Time
	platformError *upstreamError
	freeUntil     time.Time
	freeError     *upstreamError
	models        map[string]time.Time
}

func randomPromptCredentialScope(apiKey, baseURL string) string {
	keyHash := sha256.Sum256([]byte(apiKey))
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	scopeHash := sha256.Sum256([]byte(baseURL + "\x00" + hex.EncodeToString(keyHash[:])))
	return hex.EncodeToString(scopeHash[:])
}

func (s *randomPromptState) cooldownSnapshot(scopeKey string, now time.Time) randomPromptCooldownSnapshot {
	snapshot := randomPromptCooldownSnapshot{models: make(map[string]time.Time)}
	if s == nil {
		return snapshot
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	scope := s.getOrCreateScopeLocked(scopeKey, now)
	touchRandomPromptScope(scope, now)
	if !scope.platformUntil.IsZero() && !scope.platformUntil.After(now) {
		scope.platformUntil = time.Time{}
		scope.platformError = upstreamError{}
		scope.hasPlatform = false
	}
	if scope.hasPlatform {
		snapshot.platformUntil = scope.platformUntil
		platformError := scope.platformError
		snapshot.platformError = &platformError
	}
	if !scope.freeUntil.IsZero() && !scope.freeUntil.After(now) {
		scope.freeUntil = time.Time{}
		scope.freeError = upstreamError{}
		scope.hasFree = false
	}
	if scope.hasFree {
		snapshot.freeUntil = scope.freeUntil
		freeError := scope.freeError
		snapshot.freeError = &freeError
	}
	for model, until := range scope.models {
		if !until.After(now) {
			delete(scope.models, model)
			continue
		}
		snapshot.models[model] = until
	}
	return snapshot
}

func (s *randomPromptState) setModelCooldown(scopeKey, modelKey string, until, now time.Time) {
	if s == nil || modelKey == "" || !until.After(now) {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	scope := s.getOrCreateScopeLocked(scopeKey, now)
	touchRandomPromptScope(scope, now)
	if scope.models == nil {
		scope.models = make(map[string]time.Time)
	}
	if scope.models[modelKey].Before(until) {
		scope.models[modelKey] = until
	}
}

func (s *randomPromptState) setPlatformCooldown(scopeKey string, upstream *upstreamError, now time.Time) {
	if s == nil || upstream == nil {
		return
	}
	until := upstream.RetryAt
	if until.IsZero() {
		// Retain a short local suppression window without inventing an upstream
		// reset time for the error returned to the caller.
		until = now.Add(defaultPlatformCooldown)
	} else if !until.After(now) {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	scope := s.getOrCreateScopeLocked(scopeKey, now)
	touchRandomPromptScope(scope, now)
	if randomPromptIsFreeQuota(upstream) {
		if scope.freeUntil.After(until) {
			return
		}
		scope.freeUntil, scope.freeError, scope.hasFree = until, *upstream, true
		return
	}
	if scope.platformUntil.After(until) {
		return
	}
	scope.platformUntil = until
	scope.platformError = *upstream
	scope.hasPlatform = true
}

// Only explicit free-quota metadata or an anchored legacy quota message can
// exempt Haiku from a shared platform cooldown. Generic account limits and
// provider messages that merely mention a free quota retain their scope.
func randomPromptIsFreeQuota(upstream *upstreamError) bool {
	if upstream == nil || upstream.StatusCode != 429 || upstream.RateLimitScope != "platform" {
		return false
	}
	source := strings.ToLower(strings.TrimSpace(upstream.LimitSource))
	if source == "openrouter_free_models" || source == "openrouter_free_model" || source == "openrouter_free_models_per_min" || source == "openrouter_free_models_per_day" {
		return true
	}
	message := strings.TrimSpace(strings.ToLower(upstream.Message))
	if !strings.HasPrefix(message, "rate limit exceeded:") {
		return false
	}
	quota := strings.TrimSpace(strings.TrimPrefix(message, "rate limit exceeded:"))
	for _, prefix := range []string{"free-models-per-min", "free-models-per-day"} {
		if strings.HasPrefix(quota, prefix) {
			suffix := strings.TrimPrefix(quota, prefix)
			if suffix == "" || strings.ContainsAny(suffix[:1], "-. :\t\r\n") {
				return true
			}
		}
	}
	return false
}

func touchRandomPromptScope(scope *randomPromptCooldownScope, now time.Time) {
	if now.After(scope.lastUsed) {
		scope.lastUsed = now
	}
}

func (s *randomPromptState) getOrCreateScopeLocked(scopeKey string, now time.Time) *randomPromptCooldownScope {
	if s.scopes == nil {
		s.scopes = make(map[string]*randomPromptCooldownScope)
	}
	if scope, ok := s.scopes[scopeKey]; ok {
		return scope
	}
	if len(s.scopes) >= maxRandomPromptCooldownScopes {
		var oldestKey string
		var oldest time.Time
		for key, scope := range s.scopes {
			if oldestKey == "" || scope.lastUsed.Before(oldest) {
				oldestKey, oldest = key, scope.lastUsed
			}
		}
		delete(s.scopes, oldestKey)
	}
	scope := &randomPromptCooldownScope{
		models:   make(map[string]time.Time),
		lastUsed: now,
	}
	s.scopes[scopeKey] = scope
	return scope
}

func randomPromptModelCooldownKey(model string, upstream *upstreamError) string {
	if upstream != nil && strings.TrimSpace(upstream.ProviderName) != "" {
		provider, _, found := strings.Cut(model, "/")
		if found && strings.EqualFold(strings.TrimSpace(provider), strings.TrimSpace(upstream.ProviderName)) {
			return "provider:" + strings.ToLower(strings.TrimSpace(provider))
		}
	}
	return "model:" + model
}

func randomPromptModelCooldownKeys(model string) []string {
	keys := []string{"model:" + model}
	provider, _, found := strings.Cut(model, "/")
	if found && strings.TrimSpace(provider) != "" {
		keys = append(keys, "provider:"+strings.ToLower(strings.TrimSpace(provider)))
	}
	return keys
}
