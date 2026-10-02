package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	youtubeUploadScope       = "https://www.googleapis.com/auth/youtube.upload"
	youtubeReadOnlyScope     = "https://www.googleapis.com/auth/youtube.readonly"
	youtubeOAuthCallbackPath = "/api/youtube/oauth/callback"
	youtubeOAuthCookie       = "video_youtube_oauth"
	youtubeOAuthFlowLifetime = 10 * time.Minute
	maxYouTubeOAuthBody      = 1 << 20
)

const (
	youtubeOAuthTokenEndpoint = "https://oauth2.googleapis.com/token"
	youtubeDataAPIOrigin      = "https://www.googleapis.com"
)

type youtubeOAuthToken struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
	Error        string `json:"error"`
}

type youtubeChannelResponse struct {
	Items []struct {
		ID      string `json:"id"`
		Snippet struct {
			Title string `json:"title"`
		} `json:"snippet"`
	} `json:"items"`
}

type youtubeStatusResponse struct {
	Configured bool `json:"configured"`
	Connected  bool `json:"connected"`
	Channel    struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	} `json:"channel"`
	RedirectURI     string `json:"redirect_uri"`
	ClientID        string `json:"client_id"`
	BaseURL         string `json:"base_url"`
	HasClientSecret bool   `json:"has_client_secret"`
}

func youtubeHTTPClient(a *dashboardApp) *http.Client {
	if a.youtubeHTTPClient != nil {
		return a.youtubeHTTPClient
	}
	return defaultYouTubeHTTPClient
}

func (a *dashboardApp) youtubeOAuthTokenURL() string {
	if a.youtubeOAuthTokenEndpoint != "" {
		return a.youtubeOAuthTokenEndpoint
	}
	return youtubeOAuthTokenEndpoint
}

func (a *dashboardApp) youtubeAPIOrigin() string {
	if a.youtubeDataAPIOrigin != "" {
		return strings.TrimRight(a.youtubeDataAPIOrigin, "/")
	}
	return youtubeDataAPIOrigin
}

var defaultYouTubeHTTPClient = &http.Client{
	Timeout: 30 * time.Second,
	CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

func validateYouTubeAppOrigin(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Opaque != "" || parsed.Host == "" || parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("Enter this dashboard's public origin only, such as https://video.example.com")
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" || strings.ContainsAny(parsed.Host, "\r\n\t ") {
		return "", errors.New("Enter a valid dashboard origin")
	}
	ip := net.ParseIP(host)
	loopback := host == "localhost" || ip != nil && ip.IsLoopback()
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && loopback) {
		return "", errors.New("Use an HTTPS dashboard origin; HTTP is allowed only for localhost")
	}
	if !loopback && (ip != nil && (ip.IsPrivate() || ip.IsUnspecified() || ip.IsLinkLocalUnicast()) || !strings.Contains(host, ".")) {
		return "", errors.New("Use a public dashboard hostname for YouTube OAuth")
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func youtubeRedirectURI(origin string) (string, error) {
	validated, err := validateYouTubeAppOrigin(origin)
	if err != nil {
		return "", err
	}
	return validated + youtubeOAuthCallbackPath, nil
}

func newYouTubeOpaqueToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func pkceChallenge(verifier string) string {
	digest := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func (a *dashboardApp) youtubeStatus(w http.ResponseWriter, _ *http.Request) {
	config, err := a.store.youtubeConfig()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to load YouTube settings")
		return
	}
	redirectURI, _ := youtubeRedirectURI(config.BaseURL)
	response := youtubeStatusResponse{
		Configured:      config.ClientID != "" && config.EncryptedClientSecret != "" && config.BaseURL != "",
		Connected:       config.ChannelID != "" && config.EncryptedRefreshToken != "" && !config.ReconnectRequired,
		RedirectURI:     redirectURI,
		ClientID:        config.ClientID,
		BaseURL:         config.BaseURL,
		HasClientSecret: config.EncryptedClientSecret != "",
	}
	response.Channel.ID = config.ChannelID
	response.Channel.Title = config.ChannelTitle
	writeJSON(w, http.StatusOK, response)
}

func (a *dashboardApp) updateYouTubeConfig(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) {
		return
	}
	if !isJSON(r.Header.Get("Content-Type")) {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	var input struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		BaseURL      string `json:"base_url"`
	}
	if decodeJSONBody(w, r, &input) != nil {
		return
	}
	input.ClientID = strings.TrimSpace(input.ClientID)
	if input.ClientID == "" || len(input.ClientID) > 512 || strings.ContainsAny(input.ClientID, "\r\n\t ") {
		writeError(w, http.StatusBadRequest, "Enter a valid Google OAuth client ID")
		return
	}
	if input.ClientSecret != "" && (len(input.ClientSecret) > 1024 || strings.ContainsAny(input.ClientSecret, "\r\n")) {
		writeError(w, http.StatusBadRequest, "Enter a valid Google OAuth client secret")
		return
	}
	baseURL, err := validateYouTubeAppOrigin(input.BaseURL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	a.store.youtubeMu.Lock()
	defer a.store.youtubeMu.Unlock()
	current, err := a.store.youtubeConfig()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to load YouTube settings")
		return
	}
	secret := input.ClientSecret
	if secret == "" {
		if current.EncryptedClientSecret == "" {
			writeError(w, http.StatusBadRequest, "Enter the Google OAuth client secret")
			return
		}
		secret, err = a.security.DecryptSetting("youtube_client_secret", current.EncryptedClientSecret)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "unable to read saved YouTube settings")
			return
		}
	}
	changedOAuthClient := input.ClientID != current.ClientID
	if current.EncryptedClientSecret != "" {
		oldSecret, decryptErr := a.security.DecryptSetting("youtube_client_secret", current.EncryptedClientSecret)
		if decryptErr != nil {
			writeError(w, http.StatusInternalServerError, "unable to read saved YouTube settings")
			return
		}
		changedOAuthClient = changedOAuthClient || secret != oldSecret
	}
	if current.ChannelID != "" && changedOAuthClient {
		writeError(w, http.StatusConflict, "Disconnect YouTube before changing the OAuth client ID or secret")
		return
	}
	if input.ClientSecret == "" && input.ClientID != current.ClientID {
		writeError(w, http.StatusBadRequest, "Enter the client secret that belongs to the new Google OAuth client ID")
		return
	}
	if changedOAuthClient {
		active, countErr := a.store.activeYouTubeUploadCount()
		if countErr != nil {
			writeError(w, http.StatusInternalServerError, "unable to check YouTube uploads")
			return
		}
		if active != 0 {
			writeError(w, http.StatusConflict, "Finish or cancel outstanding uploads before changing the OAuth client")
			return
		}
	}
	encryptedSecret := current.EncryptedClientSecret
	if current.EncryptedClientSecret == "" || input.ClientSecret != "" && changedOAuthClient {
		encryptedSecret, err = a.security.EncryptSetting("youtube_client_secret", secret)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "unable to save YouTube settings")
			return
		}
	}
	changed := changedOAuthClient || baseURL != current.BaseURL
	version := current.ConfigVersion
	if changed {
		version++
	}
	if err := a.store.saveYouTubeSettings(input.ClientID, encryptedSecret, baseURL, version); err != nil {
		writeError(w, http.StatusInternalServerError, "unable to save YouTube settings")
		return
	}
	if changed {
		_ = a.store.clearYouTubeOAuthFlows()
	}
	writeJSON(w, http.StatusOK, map[string]any{"configured": true, "redirect_uri": baseURL + youtubeOAuthCallbackPath, "client_id": input.ClientID, "base_url": baseURL, "has_client_secret": true})
}

func (a *dashboardApp) connectYouTube(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) {
		return
	}
	if a.security == nil {
		writeError(w, http.StatusServiceUnavailable, "secure YouTube connection storage is unavailable")
		return
	}
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	identity, ok := a.security.Session(r)
	if !ok || identity.MustChangePassword {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	a.store.youtubeMu.Lock()
	defer a.store.youtubeMu.Unlock()
	config, err := a.store.youtubeConfig()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to load YouTube settings")
		return
	}
	if config.ClientID == "" || config.EncryptedClientSecret == "" || config.BaseURL == "" {
		writeError(w, http.StatusConflict, "Save YouTube OAuth settings before connecting a channel")
		return
	}
	redirectURI, err := youtubeRedirectURI(config.BaseURL)
	if err != nil {
		writeError(w, http.StatusConflict, "Save a valid public HTTPS dashboard origin before connecting YouTube")
		return
	}
	state, err := newYouTubeOpaqueToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to start YouTube authorization")
		return
	}
	binding, err := newYouTubeOpaqueToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to start YouTube authorization")
		return
	}
	verifier, err := newYouTubeOpaqueToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to start YouTube authorization")
		return
	}
	stateHash := tokenHash(state)
	encryptedVerifier, err := a.security.EncryptSetting("youtube_oauth_verifier_"+stateHash, verifier)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to start YouTube authorization")
		return
	}
	flow := YouTubeOAuthFlow{StateHash: stateHash, SessionHash: tokenHash(cookie.Value), BindingHash: tokenHash(binding), RedirectURI: redirectURI, EncryptedVerifier: encryptedVerifier, ConfigVersion: config.ConfigVersion, ExpiresAt: time.Now().Add(youtubeOAuthFlowLifetime).Unix()}
	if err := a.store.insertYouTubeOAuthFlow(flow, time.Now().Unix()); err != nil {
		writeError(w, http.StatusInternalServerError, "unable to start YouTube authorization")
		return
	}
	parsed, _ := url.Parse("https://accounts.google.com/o/oauth2/v2/auth")
	query := parsed.Query()
	query.Set("client_id", config.ClientID)
	query.Set("redirect_uri", redirectURI)
	query.Set("response_type", "code")
	query.Set("scope", youtubeUploadScope+" "+youtubeReadOnlyScope)
	query.Set("state", state)
	query.Set("access_type", "offline")
	query.Set("include_granted_scopes", "true")
	query.Set("prompt", "consent")
	query.Set("code_challenge", pkceChallenge(verifier))
	query.Set("code_challenge_method", "S256")
	parsed.RawQuery = query.Encode()
	secureCookie := strings.HasPrefix(redirectURI, "https://")
	http.SetCookie(w, &http.Cookie{Name: youtubeOAuthCookie, Value: binding, Path: youtubeOAuthCallbackPath, Expires: time.Unix(flow.ExpiresAt, 0), MaxAge: int(youtubeOAuthFlowLifetime.Seconds()), HttpOnly: true, Secure: secureCookie, SameSite: http.SameSiteLaxMode})
	writeJSON(w, http.StatusOK, map[string]string{"authorization_url": parsed.String()})
}

func (a *dashboardApp) youtubeOAuthCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	configForCookie, _ := a.store.youtubeConfig()
	clearYouTubeOAuthCookie(w, r, strings.HasPrefix(configForCookie.BaseURL, "https://"))
	state := singleQueryValue(r, "state")
	binding, cookieErr := r.Cookie(youtubeOAuthCookie)
	if state == "" || cookieErr != nil || len(state) < 32 || len(state) > 128 {
		redirectYouTubeOAuth(w, r, "state_invalid")
		return
	}
	flow, err := a.store.consumeYouTubeOAuthFlow(tokenHash(state), tokenHash(binding.Value), time.Now().Unix())
	if err != nil {
		redirectYouTubeOAuth(w, r, "state_invalid")
		return
	}
	if oauthError := singleQueryValue(r, "error"); oauthError != "" {
		if oauthError == "access_denied" {
			redirectYouTubeOAuth(w, r, "consent_denied")
		} else {
			redirectYouTubeOAuth(w, r, "provider_error")
		}
		return
	}
	code := singleQueryValue(r, "code")
	if code == "" {
		redirectYouTubeOAuth(w, r, "provider_error")
		return
	}
	verifier, err := a.security.DecryptSetting("youtube_oauth_verifier_"+flow.StateHash, flow.EncryptedVerifier)
	if err != nil {
		redirectYouTubeOAuth(w, r, "provider_error")
		return
	}
	a.store.youtubeMu.Lock()
	config, err := a.store.youtubeConfig()
	a.store.youtubeMu.Unlock()
	if err != nil || config.ConfigVersion != flow.ConfigVersion {
		redirectYouTubeOAuth(w, r, "settings_changed")
		return
	}
	secret, err := a.security.DecryptSetting("youtube_client_secret", config.EncryptedClientSecret)
	if err != nil {
		redirectYouTubeOAuth(w, r, "settings_changed")
		return
	}
	token, err := a.exchangeYouTubeAuthorizationCode(r.Context(), code, verifier, flow.RedirectURI, config.ClientID, secret)
	if err != nil || token.AccessToken == "" || token.ExpiresIn <= 0 || token.ExpiresIn > 86400 {
		redirectYouTubeOAuth(w, r, "provider_error")
		return
	}
	if token.RefreshToken == "" {
		redirectYouTubeOAuth(w, r, "offline_access_missing")
		return
	}
	if !youtubeScopesGranted(token.Scope) {
		redirectYouTubeOAuth(w, r, "missing_scopes")
		return
	}
	channelID, channelTitle, err := a.youtubeChannel(r.Context(), token.AccessToken)
	if err != nil || channelID == "" || channelTitle == "" {
		redirectYouTubeOAuth(w, r, "channel_unavailable")
		return
	}
	encryptedAccess, err := a.security.EncryptSetting("youtube_access_token", token.AccessToken)
	if err != nil {
		redirectYouTubeOAuth(w, r, "provider_error")
		return
	}
	encryptedRefresh, err := a.security.EncryptSetting("youtube_refresh_token", token.RefreshToken)
	if err != nil {
		redirectYouTubeOAuth(w, r, "provider_error")
		return
	}
	config.EncryptedAccessToken = encryptedAccess
	config.EncryptedRefreshToken = encryptedRefresh
	config.AccessTokenExpiresAt = time.Now().Add(time.Duration(token.ExpiresIn) * time.Second).Unix()
	config.ChannelID, config.ChannelTitle = channelID, channelTitle
	a.store.youtubeMu.Lock()
	current, loadErr := a.store.youtubeConfig()
	if loadErr == nil {
		config.ConfigVersion = current.ConfigVersion
		if current.ConfigVersion != flow.ConfigVersion {
			loadErr = errors.New("YouTube settings changed during authorization")
		} else {
			loadErr = a.store.connectYouTube(config, current.ChannelID, flow.SessionHash, time.Now().Unix())
		}
	}
	a.store.youtubeMu.Unlock()
	if loadErr != nil {
		if strings.Contains(loadErr.Error(), "different channel") {
			redirectYouTubeOAuth(w, r, "channel_busy")
		} else if strings.Contains(loadErr.Error(), "settings changed") {
			redirectYouTubeOAuth(w, r, "settings_changed")
		} else if strings.Contains(loadErr.Error(), "session expired") {
			redirectYouTubeOAuth(w, r, "session_expired")
		} else {
			redirectYouTubeOAuth(w, r, "provider_error")
		}
		return
	}
	http.Redirect(w, r, "/?youtube=connected", http.StatusSeeOther)
}

func clearYouTubeOAuthCookie(w http.ResponseWriter, _ *http.Request, secure bool) {
	http.SetCookie(w, &http.Cookie{Name: youtubeOAuthCookie, Value: "", Path: youtubeOAuthCallbackPath, MaxAge: -1, HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode})
}

func redirectYouTubeOAuth(w http.ResponseWriter, r *http.Request, reason string) {
	allowed := map[string]struct{}{
		"state_invalid": {}, "consent_denied": {}, "missing_scopes": {}, "offline_access_missing": {},
		"channel_unavailable": {}, "channel_busy": {}, "settings_changed": {}, "session_expired": {}, "provider_error": {},
	}
	if _, ok := allowed[reason]; !ok {
		reason = "provider_error"
	}
	query := url.Values{"youtube": {"error"}, "youtube_reason": {reason}}
	http.Redirect(w, r, "/?"+query.Encode(), http.StatusSeeOther)
}

func singleQueryValue(r *http.Request, name string) string {
	values, ok := r.URL.Query()[name]
	if !ok || len(values) != 1 || len(values[0]) > 4096 {
		return ""
	}
	return values[0]
}

func youtubeScopesGranted(scope string) bool {
	granted := make(map[string]struct{})
	for _, item := range strings.Fields(scope) {
		granted[item] = struct{}{}
	}
	_, upload := granted[youtubeUploadScope]
	_, readonly := granted[youtubeReadOnlyScope]
	return upload && readonly
}

func (a *dashboardApp) exchangeYouTubeAuthorizationCode(ctx context.Context, code, verifier, redirectURI, clientID, clientSecret string) (youtubeOAuthToken, error) {
	form := url.Values{
		"code":          {code},
		"code_verifier": {verifier},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"redirect_uri":  {redirectURI},
		"grant_type":    {"authorization_code"},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, a.youtubeOAuthTokenURL(), strings.NewReader(form.Encode()))
	if err != nil {
		return youtubeOAuthToken{}, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := youtubeHTTPClient(a).Do(request)
	if err != nil {
		return youtubeOAuthToken{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return youtubeOAuthToken{}, fmt.Errorf("Google OAuth token exchange returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxYouTubeOAuthBody+1))
	if err != nil || len(data) > maxYouTubeOAuthBody {
		return youtubeOAuthToken{}, errors.New("Google OAuth response was too large or unreadable")
	}
	var token youtubeOAuthToken
	if err := json.Unmarshal(data, &token); err != nil {
		return youtubeOAuthToken{}, errors.New("Google OAuth response was invalid")
	}
	return token, nil
}

func (a *dashboardApp) youtubeChannel(ctx context.Context, accessToken string) (string, string, error) {
	endpoint := a.youtubeAPIOrigin() + "/youtube/v3/channels?part=id%2Csnippet&mine=true"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", "", err
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	response, err := youtubeHTTPClient(a).Do(request)
	if err != nil {
		return "", "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("YouTube channel lookup returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxYouTubeOAuthBody+1))
	if err != nil || len(data) > maxYouTubeOAuthBody {
		return "", "", errors.New("YouTube channel response was too large or unreadable")
	}
	var result youtubeChannelResponse
	if err := json.Unmarshal(data, &result); err != nil {
		return "", "", errors.New("YouTube channel response was invalid")
	}
	if len(result.Items) != 1 {
		return "", "", errors.New("YouTube authorization must resolve to exactly one channel")
	}
	return strings.TrimSpace(result.Items[0].ID), strings.TrimSpace(result.Items[0].Snippet.Title), nil
}

func (a *dashboardApp) disconnectYouTube(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) {
		return
	}
	a.store.youtubeMu.Lock()
	err := a.store.disconnectYouTube()
	a.store.youtubeMu.Unlock()
	if err != nil {
		if strings.Contains(err.Error(), "outstanding uploads") {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "unable to disconnect YouTube")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *dashboardApp) requireCurrentYouTubeConfig(ctx context.Context, expectedVersion int64) (YouTubeConfig, error) {
	select {
	case <-ctx.Done():
		return YouTubeConfig{}, ctx.Err()
	default:
	}
	config, err := a.store.youtubeConfig()
	if err != nil {
		return YouTubeConfig{}, err
	}
	if config.ConfigVersion != expectedVersion {
		return YouTubeConfig{}, sql.ErrNoRows
	}
	return config, nil
}
