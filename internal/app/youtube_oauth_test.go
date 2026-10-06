package app

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testYouTubeOrigin       = "https://video.example.com"
	testYouTubeClientID     = "framevault-test.apps.googleusercontent.com"
	testYouTubeClientSecret = "google-client-secret-for-tests"
	testYouTubeAccessToken  = "google-access-token-for-tests"
	testYouTubeRefreshToken = "google-refresh-token-for-tests"
)

type youtubeOAuthTestAttempt struct {
	authorizationURL *url.URL
	state            string
	callbackCookie   *http.Cookie
	sessionCookie    *http.Cookie
}

type youtubeOAuthTestFixture struct {
	store         *Store
	security      *Security
	app           *dashboardApp
	server        *httptest.Server
	tokenResponse youtubeOAuthToken
	channelID     string
	channelTitle  string
	tokenCalls    int
	channelCalls  int
	channelAuth   string
	tokenForms    []url.Values
	beforeToken   func()
	beforeChannel func()
	clientSecret  string
}

func newYouTubeOAuthTestFixture(t *testing.T) *youtubeOAuthTestFixture {
	t.Helper()
	store := newTestStore(t)
	provisionTestUser(t, store)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &youtubeOAuthTestFixture{
		store:    store,
		security: security,
		tokenResponse: youtubeOAuthToken{
			AccessToken:  testYouTubeAccessToken,
			RefreshToken: testYouTubeRefreshToken,
			TokenType:    "Bearer",
			ExpiresIn:    3600,
			Scope:        youtubeUploadScope + " " + youtubeReadOnlyScope,
		},
		channelID:    "channel-framevault-test",
		channelTitle: "FrameVault Test Channel",
		clientSecret: testYouTubeClientSecret,
	}
	fixture.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			fixture.tokenCalls++
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse OAuth token form: %v", err)
				http.Error(w, "bad form", http.StatusBadRequest)
				return
			}
			fixture.tokenForms = append(fixture.tokenForms, r.PostForm)
			if fixture.beforeToken != nil {
				fixture.beforeToken()
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(fixture.tokenResponse)
		case "/youtube/v3/channels":
			fixture.channelCalls++
			fixture.channelAuth = r.Header.Get("Authorization")
			if fixture.beforeChannel != nil {
				fixture.beforeChannel()
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{
				map[string]any{"id": fixture.channelID, "snippet": map[string]string{"title": fixture.channelTitle}},
			}})
		default:
			t.Errorf("unexpected fake Google request: %s %s", r.Method, r.URL.String())
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fixture.server.Close)
	client := fixture.server.Client()
	client.Timeout = 3 * time.Second
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	fixture.app = &dashboardApp{
		store:                     store,
		security:                  security,
		logger:                    slog.New(slog.NewTextHandler(io.Discard, nil)),
		youtubeHTTPClient:         client,
		youtubeOAuthTokenEndpoint: fixture.server.URL + "/token",
		youtubeDataAPIOrigin:      fixture.server.URL,
	}
	secretCipher, err := security.EncryptSetting("youtube_client_secret", fixture.clientSecret)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.saveYouTubeSettings(testYouTubeClientID, secretCipher, testYouTubeOrigin, 1); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func (f *youtubeOAuthTestFixture) newSession(t *testing.T) *http.Cookie {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, testYouTubeOrigin+"/api/login", nil)
	if err := f.security.NewSession(recorder, request, "sujanshrestha"); err != nil {
		t.Fatal(err)
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != sessionCookie {
		t.Fatalf("session cookie = %#v", cookies)
	}
	return cookies[0]
}

func (f *youtubeOAuthTestFixture) begin(t *testing.T) youtubeOAuthTestAttempt {
	t.Helper()
	session := f.newSession(t)
	request := httptest.NewRequest(http.MethodPost, testYouTubeOrigin+"/api/youtube/connect", nil)
	request.Header.Set("Origin", testYouTubeOrigin)
	request.AddCookie(session)
	recorder := httptest.NewRecorder()
	securityHeaders(http.HandlerFunc(f.app.connectYouTube)).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("connect status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("connect cache policy=%q", recorder.Header().Get("Cache-Control"))
	}
	var response struct {
		AuthorizationURL string `json:"authorization_url"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	authorizationURL, err := url.Parse(response.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	cookieJar := recorder.Result().Cookies()
	var callbackCookie *http.Cookie
	for _, cookie := range cookieJar {
		if cookie.Name == youtubeOAuthCookie {
			callbackCookie = cookie
		}
	}
	if callbackCookie == nil {
		t.Fatalf("callback binding cookie missing: %#v", cookieJar)
	}
	return youtubeOAuthTestAttempt{
		authorizationURL: authorizationURL,
		state:            authorizationURL.Query().Get("state"),
		callbackCookie:   callbackCookie,
		sessionCookie:    session,
	}
}

func (f *youtubeOAuthTestFixture) callback(t *testing.T, attempt youtubeOAuthTestAttempt, code string) (*httptest.ResponseRecorder, *http.Request) {
	t.Helper()
	query := url.Values{"state": {attempt.state}}
	if code != "" {
		query.Set("code", code)
	}
	request := httptest.NewRequest(http.MethodGet, testYouTubeOrigin+youtubeOAuthCallbackPath+"?"+query.Encode(), nil)
	request.AddCookie(attempt.callbackCookie)
	recorder := httptest.NewRecorder()
	securityHeaders(http.HandlerFunc(f.app.youtubeOAuthCallback)).ServeHTTP(recorder, request)
	return recorder, request
}

func (f *youtubeOAuthTestFixture) storedFlow(t *testing.T, state string) YouTubeOAuthFlow {
	t.Helper()
	var flow YouTubeOAuthFlow
	err := f.store.db.QueryRow(`SELECT state_hash,session_hash,binding_hash,redirect_uri,encrypted_verifier,config_version,expires_at FROM youtube_oauth_flows WHERE state_hash=?`, tokenHash(state)).Scan(
		&flow.StateHash, &flow.SessionHash, &flow.BindingHash, &flow.RedirectURI, &flow.EncryptedVerifier, &flow.ConfigVersion, &flow.ExpiresAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	return flow
}

func (f *youtubeOAuthTestFixture) oauthFlowCount(t *testing.T, state string) int {
	t.Helper()
	var count int
	if err := f.store.db.QueryRow(`SELECT COUNT(*) FROM youtube_oauth_flows WHERE state_hash=?`, tokenHash(state)).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func assertYouTubeOAuthCallbackHeaders(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("OAuth callback status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Cache-Control") != "no-store" || recorder.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("OAuth callback privacy headers=%v", recorder.Header())
	}
	var cleared *http.Cookie
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == youtubeOAuthCookie {
			cleared = cookie
		}
	}
	if cleared == nil || cleared.Value != "" || cleared.MaxAge >= 0 || cleared.Path != youtubeOAuthCallbackPath || !cleared.HttpOnly || !cleared.Secure || cleared.SameSite != http.SameSiteLaxMode {
		t.Fatalf("callback binding cookie was not safely cleared: %#v", cleared)
	}
}

func assertYouTubeOAuthError(t *testing.T, recorder *httptest.ResponseRecorder, wantReason string, secrets ...string) {
	t.Helper()
	assertYouTubeOAuthCallbackHeaders(t, recorder)
	location := recorder.Header().Get("Location")
	parsed, err := url.Parse(location)
	if err != nil || parsed.Query().Get("youtube") != "error" || parsed.Query().Get("youtube_reason") != wantReason {
		t.Fatalf("OAuth error redirect=%q parseErr=%v, want reason %q", location, err, wantReason)
	}
	for _, secret := range secrets {
		if secret != "" && (strings.Contains(location, secret) || strings.Contains(recorder.Body.String(), secret)) {
			t.Fatalf("OAuth error response exposed a secret: %q", secret)
		}
	}
}

func TestYouTubeOAuthLaxCallbackBridgeUsesPKCEAndConsumesStateOnce(t *testing.T) {
	f := newYouTubeOAuthTestFixture(t)
	attempt := f.begin(t)
	query := attempt.authorizationURL.Query()
	if attempt.authorizationURL.Host != "accounts.google.com" || attempt.authorizationURL.Path != "/o/oauth2/v2/auth" {
		t.Fatalf("authorization URL=%q", attempt.authorizationURL)
	}
	if query.Get("client_id") != testYouTubeClientID || query.Get("redirect_uri") != testYouTubeOrigin+youtubeOAuthCallbackPath {
		t.Fatalf("authorization client or redirect URI=%v", query)
	}
	if query.Get("response_type") != "code" || query.Get("access_type") != "offline" || query.Get("prompt") != "consent" || query.Get("code_challenge_method") != "S256" {
		t.Fatalf("authorization security parameters=%v", query)
	}
	if query.Get("scope") != youtubeUploadScope+" "+youtubeReadOnlyScope || attempt.state == "" || len(attempt.state) < 32 {
		t.Fatalf("authorization scope/state=%v", query)
	}
	if !attempt.callbackCookie.HttpOnly || !attempt.callbackCookie.Secure || attempt.callbackCookie.SameSite != http.SameSiteLaxMode || attempt.callbackCookie.Path != youtubeOAuthCallbackPath || attempt.callbackCookie.MaxAge != int(youtubeOAuthFlowLifetime.Seconds()) {
		t.Fatalf("unsafe OAuth bridge cookie: %#v", attempt.callbackCookie)
	}
	if !attempt.sessionCookie.HttpOnly || attempt.sessionCookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("dashboard session cookie lost Strict policy: %#v", attempt.sessionCookie)
	}
	if !strings.Contains(attempt.authorizationURL.String(), "code_challenge=") || strings.Contains(attempt.authorizationURL.String(), "code_verifier=") || strings.Contains(attempt.authorizationURL.String(), f.clientSecret) {
		t.Fatalf("authorization URL contains a secret or lacks a PKCE challenge: %q", attempt.authorizationURL)
	}

	flow := f.storedFlow(t, attempt.state)
	if flow.StateHash == attempt.state || flow.SessionHash != tokenHash(attempt.sessionCookie.Value) || flow.BindingHash != tokenHash(attempt.callbackCookie.Value) || flow.RedirectURI != testYouTubeOrigin+youtubeOAuthCallbackPath || flow.ConfigVersion != 1 || flow.ExpiresAt <= time.Now().Unix() {
		t.Fatalf("OAuth flow stored unsafe or mismatched bindings: %#v", flow)
	}
	verifier, err := f.security.DecryptSetting("youtube_oauth_verifier_"+flow.StateHash, flow.EncryptedVerifier)
	if err != nil || verifier == flow.EncryptedVerifier || pkceChallenge(verifier) != query.Get("code_challenge") {
		t.Fatalf("PKCE verifier binding mismatch: verifier=%q err=%v", verifier, err)
	}
	if strings.Contains(flow.EncryptedVerifier, verifier) {
		t.Fatal("PKCE verifier was stored in plaintext")
	}

	recorder, callbackRequest := f.callback(t, attempt, "one-use-google-code")
	if _, err := callbackRequest.Cookie(sessionCookie); err == nil {
		t.Fatal("test callback unexpectedly sent the Strict dashboard session cookie")
	}
	if _, ok := f.security.Session(callbackRequest); ok {
		t.Fatal("cross-site callback unexpectedly authenticated with the dashboard cookie")
	}
	assertYouTubeOAuthCallbackHeaders(t, recorder)
	if recorder.Header().Get("Location") != "/?youtube=connected" || strings.Contains(recorder.Body.String(), attempt.state) || strings.Contains(recorder.Body.String(), verifier) || strings.Contains(recorder.Body.String(), f.clientSecret) {
		t.Fatalf("successful callback response location=%q body=%q", recorder.Header().Get("Location"), recorder.Body.String())
	}
	if f.tokenCalls != 1 || f.channelCalls != 1 || f.channelAuth != "Bearer "+testYouTubeAccessToken {
		t.Fatalf("OAuth exchange calls token=%d channel=%d auth=%q", f.tokenCalls, f.channelCalls, f.channelAuth)
	}
	if len(f.tokenForms) != 1 || f.tokenForms[0].Get("code_verifier") != verifier || f.tokenForms[0].Get("redirect_uri") != flow.RedirectURI || f.tokenForms[0].Get("client_secret") != f.clientSecret {
		t.Fatalf("token exchange form did not bind PKCE/config: %#v", f.tokenForms)
	}
	var remainingFlows int
	if err := f.store.db.QueryRow(`SELECT COUNT(*) FROM youtube_oauth_flows WHERE state_hash=?`, flow.StateHash).Scan(&remainingFlows); err != nil || remainingFlows != 0 {
		t.Fatalf("consumed OAuth state count=%d err=%v", remainingFlows, err)
	}

	config, err := f.store.youtubeConfig()
	if err != nil || config.ChannelID != f.channelID || config.ChannelTitle != f.channelTitle || config.EncryptedAccessToken == testYouTubeAccessToken || config.EncryptedRefreshToken == testYouTubeRefreshToken || config.EncryptedClientSecret == f.clientSecret {
		t.Fatalf("connected config leaked or lost credentials: %#v err=%v", config, err)
	}
	access, err := f.security.DecryptSetting("youtube_access_token", config.EncryptedAccessToken)
	if err != nil || access != testYouTubeAccessToken {
		t.Fatalf("access token encryption/decryption failed: %q err=%v", access, err)
	}
	refresh, err := f.security.DecryptSetting("youtube_refresh_token", config.EncryptedRefreshToken)
	if err != nil || refresh != testYouTubeRefreshToken {
		t.Fatalf("refresh token encryption/decryption failed: %q err=%v", refresh, err)
	}
	secret, err := f.security.DecryptSetting("youtube_client_secret", config.EncryptedClientSecret)
	if err != nil || secret != f.clientSecret {
		t.Fatalf("client secret encryption/decryption failed: %q err=%v", secret, err)
	}

	statusRequest := httptest.NewRequest(http.MethodGet, testYouTubeOrigin+"/api/youtube/status", nil)
	statusRequest.AddCookie(attempt.sessionCookie)
	statusRecorder := httptest.NewRecorder()
	statusHandler := securityHeaders(f.app.requirePasswordChanged(http.HandlerFunc(f.app.youtubeStatus)))
	statusHandler.ServeHTTP(statusRecorder, statusRequest)
	statusBody := statusRecorder.Body.String()
	for _, secret := range []string{f.clientSecret, testYouTubeAccessToken, testYouTubeRefreshToken, verifier, attempt.callbackCookie.Value} {
		if strings.Contains(statusBody, secret) {
			t.Fatalf("browser status response exposed a secret: %q", secret)
		}
	}
	if statusRecorder.Header().Get("Cache-Control") != "no-store" || !strings.Contains(statusBody, `"connected":true`) {
		t.Fatalf("status response cache or connection state is unsafe: headers=%v body=%s", statusRecorder.Header(), statusBody)
	}

	replay, _ := f.callback(t, attempt, "one-use-google-code")
	assertYouTubeOAuthError(t, replay, "state_invalid", f.clientSecret, testYouTubeAccessToken, testYouTubeRefreshToken)
	if f.tokenCalls != 1 || f.channelCalls != 1 {
		t.Fatalf("OAuth state replay repeated external calls: token=%d channel=%d", f.tokenCalls, f.channelCalls)
	}
}

func TestYouTubeOAuthCallbackRequiresTheOriginalSessionToRemainLive(t *testing.T) {
	tests := []struct {
		name       string
		invalidate func(*testing.T, *youtubeOAuthTestFixture, youtubeOAuthTestAttempt)
		wantReason string
		wantToken  int
	}{
		{
			name: "expired before callback",
			invalidate: func(t *testing.T, f *youtubeOAuthTestFixture, attempt youtubeOAuthTestAttempt) {
				_, err := f.store.db.Exec(`UPDATE sessions SET expires_at=? WHERE token_hash=?`, time.Now().Add(-time.Second).Unix(), tokenHash(attempt.sessionCookie.Value))
				if err != nil {
					t.Fatal(err)
				}
			},
			wantReason: "state_invalid",
		},
		{
			name: "logged out before callback",
			invalidate: func(t *testing.T, f *youtubeOAuthTestFixture, attempt youtubeOAuthTestAttempt) {
				request := httptest.NewRequest(http.MethodPost, testYouTubeOrigin+"/api/logout", nil)
				request.AddCookie(attempt.sessionCookie)
				f.security.Logout(httptest.NewRecorder(), request)
			},
			wantReason: "state_invalid",
		},
		{
			name: "session expires before atomic connection",
			invalidate: func(_ *testing.T, f *youtubeOAuthTestFixture, attempt youtubeOAuthTestAttempt) {
				f.beforeChannel = func() {
					_, _ = f.store.db.Exec(`DELETE FROM sessions WHERE token_hash=?`, tokenHash(attempt.sessionCookie.Value))
				}
			},
			wantReason: "session_expired",
			wantToken:  1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newYouTubeOAuthTestFixture(t)
			attempt := f.begin(t)
			test.invalidate(t, f, attempt)
			recorder, callbackRequest := f.callback(t, attempt, "session-bound-code")
			assertYouTubeOAuthError(t, recorder, test.wantReason, f.clientSecret, testYouTubeAccessToken, testYouTubeRefreshToken)
			if _, err := callbackRequest.Cookie(sessionCookie); err == nil {
				t.Fatal("cross-site callback received the Strict dashboard session cookie")
			}
			if f.tokenCalls != test.wantToken {
				t.Fatalf("token exchange count=%d, want %d", f.tokenCalls, test.wantToken)
			}
			if got := f.oauthFlowCount(t, attempt.state); got != 0 {
				t.Fatalf("rejected callback retained its one-use flow: count=%d", got)
			}
			config, err := f.store.youtubeConfig()
			if err != nil || config.ChannelID != "" || config.EncryptedRefreshToken != "" {
				t.Fatalf("OAuth connected after its initiating session ended: %#v err=%v", config, err)
			}
		})
	}
}

func TestYouTubeOAuthDisconnectInvalidatesCallbackAlreadyExchangingCode(t *testing.T) {
	f := newYouTubeOAuthTestFixture(t)
	attempt := f.begin(t)
	tokenEntered := make(chan struct{})
	releaseToken := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseToken) }) }
	defer release()
	f.beforeToken = func() {
		close(tokenEntered)
		<-releaseToken
	}
	callbackDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		recorder, _ := f.callback(t, attempt, "disconnect-race-code")
		callbackDone <- recorder
	}()
	select {
	case <-tokenEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("OAuth callback did not reach token exchange")
	}
	if got := f.oauthFlowCount(t, attempt.state); got != 0 {
		t.Fatalf("callback did not consume state before token exchange: count=%d", got)
	}
	disconnect := httptest.NewRequest(http.MethodDelete, testYouTubeOrigin+"/api/youtube/connection", nil)
	disconnect.Header.Set("Origin", testYouTubeOrigin)
	disconnect.AddCookie(attempt.sessionCookie)
	disconnectRecorder := httptest.NewRecorder()
	securityHeaders(http.HandlerFunc(f.app.disconnectYouTube)).ServeHTTP(disconnectRecorder, disconnect)
	if disconnectRecorder.Code != http.StatusNoContent {
		t.Fatalf("disconnect status=%d body=%s", disconnectRecorder.Code, disconnectRecorder.Body.String())
	}
	release()
	var callback *httptest.ResponseRecorder
	select {
	case callback = <-callbackDone:
	case <-time.After(3 * time.Second):
		t.Fatal("OAuth callback did not finish after token response")
	}
	assertYouTubeOAuthError(t, callback, "settings_changed", f.clientSecret, testYouTubeAccessToken, testYouTubeRefreshToken)
	if f.tokenCalls != 1 || f.channelCalls != 1 {
		t.Fatalf("disconnected callback continued exchange token=%d channel=%d", f.tokenCalls, f.channelCalls)
	}
	config, err := f.store.youtubeConfig()
	if err != nil || config.ChannelID != "" || config.EncryptedAccessToken != "" || config.EncryptedRefreshToken != "" || config.ConfigVersion != 2 {
		t.Fatalf("in-flight callback reconnected after disconnect: %#v err=%v", config, err)
	}
}

func TestYouTubeOAuthDisconnectInvalidatesPendingFlow(t *testing.T) {
	f := newYouTubeOAuthTestFixture(t)
	attempt := f.begin(t)
	disconnect := httptest.NewRequest(http.MethodDelete, testYouTubeOrigin+"/api/youtube/connection", nil)
	disconnect.Header.Set("Origin", testYouTubeOrigin)
	disconnect.AddCookie(attempt.sessionCookie)
	disconnectRecorder := httptest.NewRecorder()
	securityHeaders(http.HandlerFunc(f.app.disconnectYouTube)).ServeHTTP(disconnectRecorder, disconnect)
	if disconnectRecorder.Code != http.StatusNoContent {
		t.Fatalf("disconnect status=%d body=%s", disconnectRecorder.Code, disconnectRecorder.Body.String())
	}
	if got := f.oauthFlowCount(t, attempt.state); got != 0 {
		t.Fatalf("disconnect retained pending OAuth flow: count=%d", got)
	}
	callback, _ := f.callback(t, attempt, "pending-disconnect-code")
	assertYouTubeOAuthError(t, callback, "state_invalid", f.clientSecret, testYouTubeAccessToken, testYouTubeRefreshToken)
	if f.tokenCalls != 0 || f.channelCalls != 0 {
		t.Fatalf("callback after disconnect reached Google token=%d channel=%d", f.tokenCalls, f.channelCalls)
	}
}

func TestYouTubeOAuthRequiresRefreshTokenAndBothScopes(t *testing.T) {
	tests := []struct {
		name       string
		token      func(*youtubeOAuthTestFixture)
		wantReason string
	}{
		{
			name:       "offline refresh token missing",
			token:      func(f *youtubeOAuthTestFixture) { f.tokenResponse.RefreshToken = "" },
			wantReason: "offline_access_missing",
		},
		{
			name:       "upload permission missing",
			token:      func(f *youtubeOAuthTestFixture) { f.tokenResponse.Scope = youtubeReadOnlyScope },
			wantReason: "missing_scopes",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newYouTubeOAuthTestFixture(t)
			test.token(f)
			attempt := f.begin(t)
			recorder, _ := f.callback(t, attempt, "scope-check-code")
			assertYouTubeOAuthError(t, recorder, test.wantReason, f.clientSecret, testYouTubeAccessToken, testYouTubeRefreshToken)
			if f.tokenCalls != 1 || f.channelCalls != 0 {
				t.Fatalf("invalid grants continued to channel lookup: token=%d channel=%d", f.tokenCalls, f.channelCalls)
			}
			config, err := f.store.youtubeConfig()
			if err != nil || config.ChannelID != "" || config.EncryptedAccessToken != "" || config.EncryptedRefreshToken != "" {
				t.Fatalf("incomplete OAuth grant was persisted: %#v err=%v", config, err)
			}
		})
	}
}

func TestYouTubeOAuthRejectsChangedConfigAndWrongActiveChannel(t *testing.T) {
	t.Run("settings version changed", func(t *testing.T) {
		f := newYouTubeOAuthTestFixture(t)
		attempt := f.begin(t)
		config, err := f.store.youtubeConfig()
		if err != nil {
			t.Fatal(err)
		}
		if err := f.store.saveYouTubeSettings(config.ClientID, config.EncryptedClientSecret, "https://new-video.example.com", config.ConfigVersion+1); err != nil {
			t.Fatal(err)
		}
		recorder, _ := f.callback(t, attempt, "stale-config-code")
		assertYouTubeOAuthError(t, recorder, "settings_changed", f.clientSecret, testYouTubeAccessToken, testYouTubeRefreshToken)
		if f.tokenCalls != 0 || f.channelCalls != 0 {
			t.Fatalf("stale OAuth flow reached Google: token=%d channel=%d", f.tokenCalls, f.channelCalls)
		}
		config, err = f.store.youtubeConfig()
		if err != nil || config.ChannelID != "" || config.ConfigVersion != 2 {
			t.Fatalf("settings mismatch changed connected state: %#v err=%v", config, err)
		}
	})

	t.Run("active upload belongs to previous channel", func(t *testing.T) {
		f := newYouTubeOAuthTestFixture(t)
		oldAccess, err := f.security.EncryptSetting("youtube_access_token", "old-channel-access")
		if err != nil {
			t.Fatal(err)
		}
		oldRefresh, err := f.security.EncryptSetting("youtube_refresh_token", "old-channel-refresh")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.db.Exec(`UPDATE youtube_config SET encrypted_access_token=?,encrypted_refresh_token=?,channel_id='channel-old',channel_title='Old Channel',connection_version=7 WHERE id=1`, oldAccess, oldRefresh); err != nil {
			t.Fatal(err)
		}
		now := time.Now().Unix()
		_, err = f.store.db.Exec(`INSERT INTO youtube_uploads(id,source_kind,source_id,channel_id,connection_version,title,requested_privacy_status,privacy_status,made_for_kids,contains_synthetic_media,status,source_path,source_size,source_mtime_ns,created_at,updated_at) VALUES('youtube_active_old','generation','generation_old','channel-old',7,'Active upload','private','private',0,0,'uploading','/tmp/source.mp4',12,1,?,?)`, now, now)
		if err != nil {
			t.Fatal(err)
		}
		attempt := f.begin(t)
		recorder, _ := f.callback(t, attempt, "wrong-channel-code")
		assertYouTubeOAuthError(t, recorder, "channel_busy", f.clientSecret, testYouTubeAccessToken, testYouTubeRefreshToken)
		if f.tokenCalls != 1 || f.channelCalls != 1 {
			t.Fatalf("channel switch lookup calls token=%d channel=%d", f.tokenCalls, f.channelCalls)
		}
		config, err := f.store.youtubeConfig()
		if err != nil || config.ChannelID != "channel-old" || config.ChannelTitle != "Old Channel" || config.ConnectionVersion != 7 || config.EncryptedAccessToken != oldAccess || config.EncryptedRefreshToken != oldRefresh {
			t.Fatalf("wrong-channel OAuth replaced the active connection: %#v err=%v", config, err)
		}
		var status string
		if err := f.store.db.QueryRow(`SELECT status FROM youtube_uploads WHERE id='youtube_active_old'`).Scan(&status); err != nil || status != "uploading" {
			t.Fatalf("wrong-channel OAuth changed active upload status=%q err=%v", status, err)
		}
	})
}

func TestYouTubeConfigBrowserResponsesDoNotRevealOAuthCredentials(t *testing.T) {
	f := newYouTubeOAuthTestFixture(t)
	session := f.newSession(t)
	newSecret := "replacement-oauth-client-secret"
	input, err := json.Marshal(map[string]string{
		"client_id":     "replacement-client.apps.googleusercontent.com",
		"client_secret": newSecret,
		"base_url":      testYouTubeOrigin,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPut, testYouTubeOrigin+"/api/youtube/config", strings.NewReader(string(input)))
	request.Header.Set("Origin", testYouTubeOrigin)
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(session)
	recorder := httptest.NewRecorder()
	handler := securityHeaders(f.app.requirePasswordChanged(http.HandlerFunc(f.app.updateYouTubeConfig)))
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("config update status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), newSecret) || strings.Contains(recorder.Body.String(), f.clientSecret) || strings.Contains(recorder.Body.String(), testYouTubeAccessToken) || strings.Contains(recorder.Body.String(), testYouTubeRefreshToken) {
		t.Fatalf("config update response exposed OAuth credentials: %s", recorder.Body.String())
	}
	config, err := f.store.youtubeConfig()
	if err != nil || config.EncryptedClientSecret == newSecret {
		t.Fatalf("updated OAuth secret was not encrypted: %#v err=%v", config, err)
	}
	decrypted, err := f.security.DecryptSetting("youtube_client_secret", config.EncryptedClientSecret)
	if err != nil || decrypted != newSecret {
		t.Fatalf("updated OAuth secret could not be decrypted: %q err=%v", decrypted, err)
	}
}
