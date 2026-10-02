package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	youtubeUploadChunkSize       int64 = 8 << 20
	maxYouTubeAPIResponse              = 2 << 20
	youtubeProcessingCheckLimit        = 288
	youtubeTransientAttemptLimit       = 8
	youtubeProcessingMaxDelay          = 5 * time.Minute
)

var (
	errYouTubeReconnect = errors.New("YouTube connection needs to be refreshed")
	errYouTubeTransient = errors.New("temporary YouTube request failure")
)

type youtubeVideoResource struct {
	ID     string `json:"id"`
	Status struct {
		PrivacyStatus           string `json:"privacyStatus"`
		UploadStatus            string `json:"uploadStatus"`
		SelfDeclaredMadeForKids bool   `json:"selfDeclaredMadeForKids"`
		ContainsSyntheticMedia  bool   `json:"containsSyntheticMedia"`
	} `json:"status"`
	ProcessingDetails struct {
		ProcessingStatus string `json:"processingStatus"`
		RejectionReason  string `json:"rejectionReason"`
		FailureReason    string `json:"failureReason"`
	} `json:"processingDetails"`
}

type youtubeVideosResponse struct {
	Items []youtubeVideoResource `json:"items"`
}

type youtubeInitiateRequest struct {
	Snippet struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		CategoryID  string `json:"categoryId"`
	} `json:"snippet"`
	Status struct {
		PrivacyStatus           string `json:"privacyStatus"`
		SelfDeclaredMadeForKids bool   `json:"selfDeclaredMadeForKids"`
		ContainsSyntheticMedia  bool   `json:"containsSyntheticMedia"`
	} `json:"status"`
}

type youtubeTokenRefreshError struct {
	reconnect bool
}

func (e youtubeTokenRefreshError) Error() string { return errYouTubeTransient.Error() }

func (p *Processor) processYouTubeUploads(ctx context.Context) {
	if p.app == nil || p.app.store == nil || p.app.security == nil {
		return
	}
	uploads, err := p.app.store.pendingYouTubeUploads(time.Now().Unix(), 8)
	if err != nil {
		p.logger.Warn("load pending YouTube uploads failed")
		return
	}
	for _, upload := range uploads {
		if ctx.Err() != nil {
			return
		}
		p.startYouTubeTask(ctx, upload.ID, func(taskCtx context.Context) {
			p.processYouTubeUpload(taskCtx, upload)
		})
	}
}

func (p *Processor) startYouTubeTask(ctx context.Context, id string, task func(context.Context)) {
	key := "youtube:" + id
	p.mu.Lock()
	if _, exists := p.inFlight[key]; exists {
		p.mu.Unlock()
		return
	}
	p.mu.Unlock()
	select {
	case p.youtubeSem <- struct{}{}:
	default:
		return
	}
	p.mu.Lock()
	if _, exists := p.inFlight[key]; exists {
		p.mu.Unlock()
		<-p.youtubeSem
		return
	}
	p.inFlight[key] = struct{}{}
	p.mu.Unlock()
	go func() {
		defer func() {
			<-p.youtubeSem
			p.mu.Lock()
			delete(p.inFlight, key)
			p.mu.Unlock()
		}()
		task(ctx)
	}()
}

func (p *Processor) processYouTubeUpload(ctx context.Context, upload YouTubeUpload) {
	if upload.YouTubeVideoID != "" {
		if upload.Status == youtubeUploadSending {
			if err := p.app.store.setYouTubeUploadProcessing(upload.ID, upload.YouTubeVideoID, upload.PrivacyStatus, "uploaded", time.Now().Unix(), time.Now().Unix()); err != nil {
				p.logger.Warn("recover YouTube video state failed", "upload_id", upload.ID)
				return
			}
			upload.Status = youtubeUploadProcessing
		}
		if upload.Status == youtubeUploadProcessing {
			p.pollYouTubeProcessing(ctx, upload, false)
		}
		return
	}
	if upload.Status == youtubeUploadProcessing {
		_ = p.app.store.unknownYouTubeUploadOutcome(upload.ID, "YouTube processing state is missing its video ID. Check YouTube Studio before restarting this upload.", time.Now().Unix())
		return
	}
	if upload.Status != youtubeUploadQueued && upload.Status != youtubeUploadSending {
		return
	}
	file, err := youtubeUploadSourceFile(p.app.store, upload)
	if err != nil {
		if upload.Status == youtubeUploadSending {
			_ = p.app.store.unknownYouTubeUploadOutcome(upload.ID, "The source file changed during an upload. Check YouTube Studio before restarting this upload.", time.Now().Unix())
		} else {
			_ = p.app.store.failYouTubeUpload(upload.ID, youtubeUploadFailed, "The completed source video is missing or changed. Reopen the source and create a new upload.", time.Now().Unix())
		}
		return
	}
	defer file.Close()
	token, err := p.app.youtubeAccessToken(ctx, upload, false)
	if err != nil {
		p.handleYouTubeAuthError(upload, err)
		return
	}
	if upload.Status == youtubeUploadQueued {
		p.initiateYouTubeUpload(ctx, upload, file, token)
		return
	}
	if upload.EncryptedSessionURL == "" {
		_ = p.app.store.unknownYouTubeUploadOutcome(upload.ID, "The upload session is missing after an interrupted transfer. Check YouTube Studio before restarting this upload.", time.Now().Unix())
		return
	}
	sessionURL, err := p.app.security.DecryptSetting("youtube_upload_session_"+upload.ID, upload.EncryptedSessionURL)
	if err != nil || !p.app.validYouTubeUploadSessionURL(sessionURL) {
		_ = p.app.store.unknownYouTubeUploadOutcome(upload.ID, "The saved YouTube upload session is invalid. Check YouTube Studio before restarting this upload.", time.Now().Unix())
		return
	}
	p.resumeYouTubeUpload(ctx, upload, file, token, sessionURL)
}

func youtubeUploadSourceFile(store *Store, upload YouTubeUpload) (*os.File, error) {
	baseDir := store.videoDir
	if upload.SourceKind == "project" {
		baseDir = store.projectDir
	} else if upload.SourceKind != "generation" {
		return nil, errors.New("invalid source kind")
	}
	clean := filepath.Clean(upload.SourcePath)
	baseAbs, err := filepath.Abs(baseDir)
	cleanAbs, absErr := filepath.Abs(clean)
	if err != nil || absErr != nil {
		return nil, errors.New("source path is unavailable")
	}
	rel, err := filepath.Rel(baseAbs, cleanAbs)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, errors.New("source path is outside the media directory")
	}
	// Reject symbolic-link components without resolving the directory through
	// the platform's reparse-point API. Some Windows accounts deny the latter
	// even for an application-owned temp/data directory.
	for current := cleanAbs; ; current = filepath.Dir(current) {
		info, statErr := os.Lstat(current)
		if statErr != nil {
			return nil, errors.New("source path is unavailable")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("source path is outside the media directory")
		}
		if current == baseAbs {
			break
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil, errors.New("source path is outside the media directory")
		}
	}
	file, err := os.Open(cleanAbs)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != upload.SourceSize || info.ModTime().UnixNano() != upload.SourceModTimeNS {
		_ = file.Close()
		return nil, errors.New("source file changed")
	}
	return file, nil
}

func (a *dashboardApp) youtubeAccessToken(ctx context.Context, upload YouTubeUpload, forceRefresh bool) (string, error) {
	if a.security == nil {
		return "", errYouTubeReconnect
	}
	store := a.store
	store.youtubeRefreshMu.Lock()
	defer store.youtubeRefreshMu.Unlock()
	store.youtubeMu.Lock()
	config, err := store.youtubeConfig()
	if err != nil {
		store.youtubeMu.Unlock()
		return "", errYouTubeTransient
	}
	if config.ChannelID != upload.ChannelID || config.ConnectionVersion != upload.ConnectionVersion || config.ReconnectRequired || config.EncryptedRefreshToken == "" {
		store.youtubeMu.Unlock()
		return "", errYouTubeReconnect
	}
	if !forceRefresh && config.AccessTokenExpiresAt > time.Now().Add(time.Minute).Unix() && config.EncryptedAccessToken != "" {
		access, decryptErr := a.security.DecryptSetting("youtube_access_token", config.EncryptedAccessToken)
		store.youtubeMu.Unlock()
		if decryptErr != nil {
			return "", errYouTubeReconnect
		}
		return access, nil
	}
	refreshToken, refreshErr := a.security.DecryptSetting("youtube_refresh_token", config.EncryptedRefreshToken)
	clientSecret, secretErr := a.security.DecryptSetting("youtube_client_secret", config.EncryptedClientSecret)
	if refreshErr != nil || secretErr != nil || config.ClientID == "" {
		_ = store.markYouTubeChannelNeedsReconnect(config.ChannelID, config.ConnectionVersion, config.EncryptedRefreshToken, "Reconnect the YouTube channel to continue this upload.", time.Now().Unix())
		store.youtubeMu.Unlock()
		return "", errYouTubeReconnect
	}
	connectionVersion, channelID, clientID, oldRefreshCipher := config.ConnectionVersion, config.ChannelID, config.ClientID, config.EncryptedRefreshToken
	store.youtubeMu.Unlock()

	refreshCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	refreshed, refreshErr := a.refreshYouTubeAccessToken(refreshCtx, refreshToken, clientID, clientSecret)
	cancel()
	if refreshErr != nil {
		var tokenErr youtubeTokenRefreshError
		if errors.As(refreshErr, &tokenErr) && tokenErr.reconnect {
			store.youtubeMu.Lock()
			_ = store.markYouTubeChannelNeedsReconnect(channelID, connectionVersion, oldRefreshCipher, "Reconnect the YouTube channel to continue this upload.", time.Now().Unix())
			store.youtubeMu.Unlock()
			return "", errYouTubeReconnect
		}
		return "", errYouTubeTransient
	}
	if refreshed.AccessToken == "" || refreshed.ExpiresIn <= 0 || refreshed.ExpiresIn > 86400 {
		return "", errYouTubeTransient
	}
	encryptedAccess, err := a.security.EncryptSetting("youtube_access_token", refreshed.AccessToken)
	if err != nil {
		return "", errYouTubeTransient
	}
	encryptedRefresh := oldRefreshCipher
	if refreshed.RefreshToken != "" {
		encryptedRefresh, err = a.security.EncryptSetting("youtube_refresh_token", refreshed.RefreshToken)
		if err != nil {
			return "", errYouTubeTransient
		}
	}
	store.youtubeMu.Lock()
	latest, loadErr := store.youtubeConfig()
	if loadErr != nil || latest.ConnectionVersion != connectionVersion || latest.ChannelID != channelID || latest.EncryptedRefreshToken != oldRefreshCipher {
		store.youtubeMu.Unlock()
		return "", errYouTubeReconnect
	}
	updated, updateErr := store.updateYouTubeTokens(connectionVersion, encryptedAccess, encryptedRefresh, time.Now().Add(time.Duration(refreshed.ExpiresIn)*time.Second).Unix())
	store.youtubeMu.Unlock()
	if updateErr != nil || !updated {
		return "", errYouTubeTransient
	}
	return refreshed.AccessToken, nil
}

func (a *dashboardApp) refreshYouTubeAccessToken(ctx context.Context, refreshToken, clientID, clientSecret string) (youtubeOAuthToken, error) {
	form := url.Values{"client_id": {clientID}, "client_secret": {clientSecret}, "refresh_token": {refreshToken}, "grant_type": {"refresh_token"}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, a.youtubeOAuthTokenURL(), strings.NewReader(form.Encode()))
	if err != nil {
		return youtubeOAuthToken{}, errYouTubeTransient
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := a.youTubeClient().Do(request)
	if err != nil {
		return youtubeOAuthToken{}, errYouTubeTransient
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, maxYouTubeOAuthBody+1))
	if readErr != nil || len(body) > maxYouTubeOAuthBody {
		return youtubeOAuthToken{}, errYouTubeTransient
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return youtubeOAuthToken{}, youtubeTokenRefreshError{reconnect: response.StatusCode == http.StatusBadRequest || response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden}
	}
	var token youtubeOAuthToken
	if json.Unmarshal(body, &token) != nil || token.Error != "" {
		return youtubeOAuthToken{}, errYouTubeTransient
	}
	return token, nil
}

func (a *dashboardApp) youTubeClient() *http.Client {
	client := *youtubeHTTPClient(a)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if client.Timeout == 0 {
		client.Timeout = 30 * time.Second
	}
	return &client
}

func (a *dashboardApp) validYouTubeUploadSessionURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || parsed.Fragment != "" || parsed.Path != "/upload/youtube/v3/videos" {
		return false
	}
	origin, err := url.Parse(a.youtubeAPIOrigin())
	if err != nil || !strings.EqualFold(parsed.Scheme, origin.Scheme) || !strings.EqualFold(parsed.Host, origin.Host) {
		return false
	}
	if origin.Scheme != "https" && !(origin.Scheme == "http" && isLoopbackHost(origin.Hostname())) {
		return false
	}
	query := parsed.Query()
	if (len(query) != 2 && len(query) != 3) || len(query["uploadType"]) != 1 || query.Get("uploadType") != "resumable" || len(query["upload_id"]) != 1 {
		return false
	}
	for name := range query {
		if name != "uploadType" && name != "upload_id" && name != "part" {
			return false
		}
	}
	if part, exists := query["part"]; exists {
		if len(part) != 1 || part[0] == "" {
			return false
		}
		allowedParts := map[string]struct{}{"snippet": {}, "status": {}, "contentDetails": {}}
		for _, name := range strings.Split(part[0], ",") {
			if _, ok := allowedParts[name]; !ok {
				return false
			}
		}
	}
	id := query.Get("upload_id")
	if id == "" || len(id) > 4096 || strings.ContainsAny(id, "\r\n\x00") {
		return false
	}
	return true
}

func isLoopbackHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func (a *dashboardApp) youtubeRequest(ctx context.Context, method, target, token string, body io.Reader, contentLength int64, headers http.Header) (*http.Response, error) {
	parsed, err := url.Parse(target)
	origin, originErr := url.Parse(a.youtubeAPIOrigin())
	if err != nil || originErr != nil || parsed.User != nil || parsed.Fragment != "" || !strings.EqualFold(parsed.Scheme, origin.Scheme) || !strings.EqualFold(parsed.Host, origin.Host) {
		return nil, errYouTubeTransient
	}
	request, err := http.NewRequestWithContext(ctx, method, parsed.String(), body)
	if err != nil {
		return nil, errYouTubeTransient
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
	if body != nil {
		contentType := headers.Get("Content-Type")
		if contentType == "" {
			contentType = "application/json"
		}
		request.Header.Set("Content-Type", contentType)
		if contentLength >= 0 {
			request.ContentLength = contentLength
		}
	} else if method == http.MethodPut {
		request.ContentLength = 0
	}
	for name, values := range headers {
		request.Header[http.CanonicalHeaderKey(name)] = append([]string(nil), values...)
	}
	response, err := a.youTubeClient().Do(request)
	if err != nil {
		// net/http errors include the complete request URL. A resumable URL is
		// a bearer capability, so discard the upstream error string entirely.
		return nil, errYouTubeTransient
	}
	return response, nil
}

func readYouTubeResponse(response *http.Response) ([]byte, error) {
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxYouTubeAPIResponse+1))
	if err != nil || len(body) > maxYouTubeAPIResponse {
		return nil, errYouTubeTransient
	}
	return body, nil
}

func youtubeAPIReason(body []byte) string {
	var payload struct {
		Error struct {
			Errors []struct {
				Reason string `json:"reason"`
			} `json:"errors"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &payload) != nil || len(payload.Error.Errors) == 0 {
		return ""
	}
	reason := payload.Error.Errors[0].Reason
	switch reason {
	case "quotaExceeded", "userRateLimitExceeded", "rateLimitExceeded", "uploadLimitExceeded", "insufficientPermissions", "forbidden", "invalidValue", "processingFailure", "videoNotFound", "youtubeSignupRequired":
		return reason
	default:
		return ""
	}
}

func youtubeAPIErrorMessage(status int, reason string) string {
	switch reason {
	case "quotaExceeded", "userRateLimitExceeded", "rateLimitExceeded":
		return "YouTube API quota or rate limits were reached. Check the Google Cloud quota before retrying."
	case "uploadLimitExceeded":
		return "The connected YouTube channel reached its upload limit. Check YouTube Studio before retrying."
	case "insufficientPermissions", "youtubeSignupRequired":
		return "The connected Google account needs YouTube upload access. Reconnect the channel and check its YouTube status."
	case "invalidValue":
		return "YouTube rejected the upload metadata. Review the title and description before retrying."
	case "processingFailure":
		return "YouTube could not process this video. Check YouTube Studio for details."
	case "videoNotFound":
		return "YouTube could not find the uploaded video. Check YouTube Studio before creating another upload."
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return "YouTube rejected the connected account. Reconnect the channel and retry."
	}
	return "YouTube temporarily could not complete this request. The upload will resume automatically."
}

func scheduleYouTubeRetryDelay(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	delay := 5 * time.Second
	for i := 0; i < attempt && delay < youtubeProcessingMaxDelay; i++ {
		delay *= 2
	}
	if delay > youtubeProcessingMaxDelay {
		return youtubeProcessingMaxDelay
	}
	return delay
}

func youtubeProcessingDelay(checks int) time.Duration {
	delay := 10 * time.Second
	for i := 0; i < checks/2 && delay < youtubeProcessingMaxDelay; i++ {
		delay *= 2
	}
	if delay > youtubeProcessingMaxDelay {
		return youtubeProcessingMaxDelay
	}
	return delay
}

func parseYouTubeResumeOffset(rangeHeader string, total int64) (int64, bool) {
	if rangeHeader == "" {
		return 0, true
	}
	if !strings.HasPrefix(rangeHeader, "bytes=0-") {
		return 0, false
	}
	end, err := strconv.ParseInt(strings.TrimPrefix(rangeHeader, "bytes=0-"), 10, 64)
	if err != nil || end < 0 || total <= 0 || end >= total {
		return 0, false
	}
	return end + 1, true
}

func youtubeUploadPayload(upload YouTubeUpload) youtubeInitiateRequest {
	var payload youtubeInitiateRequest
	payload.Snippet.Title = upload.Title
	payload.Snippet.Description = upload.Description
	payload.Snippet.CategoryID = "22"
	payload.Status.PrivacyStatus = upload.RequestedPrivacyStatus
	payload.Status.SelfDeclaredMadeForKids = upload.MadeForKids
	payload.Status.ContainsSyntheticMedia = upload.ContainsSyntheticMedia
	return payload
}

func decodeYouTubeVideo(body []byte) (youtubeVideoResource, error) {
	var video youtubeVideoResource
	if err := json.Unmarshal(body, &video); err == nil && video.ID != "" {
		return video, nil
	}
	var response youtubeVideosResponse
	if err := json.Unmarshal(body, &response); err == nil && len(response.Items) == 1 && response.Items[0].ID != "" {
		return response.Items[0], nil
	}
	return youtubeVideoResource{}, errors.New("YouTube response did not include a video id")
}

func youtubeUploadLocation(a *dashboardApp, response *http.Response) (string, bool) {
	location := response.Header.Get("Location")
	return location, location != "" && a.validYouTubeUploadSessionURL(location)
}

func youtubeUploadInitiationURL(a *dashboardApp) string {
	query := url.Values{"part": {"snippet,status"}, "uploadType": {"resumable"}}
	return a.youtubeAPIOrigin() + "/upload/youtube/v3/videos?" + query.Encode()
}

func youtubeProcessingURL(a *dashboardApp, id string) string {
	query := url.Values{"id": {id}, "part": {"processingDetails,status"}}
	return a.youtubeAPIOrigin() + "/youtube/v3/videos?" + query.Encode()
}

func (a *dashboardApp) safeYouTubeAPIError(response *http.Response) string {
	body, err := readYouTubeResponse(response)
	if err != nil {
		return youtubeAPIErrorMessage(response.StatusCode, "")
	}
	return youtubeAPIErrorMessage(response.StatusCode, youtubeAPIReason(body))
}

func (p *Processor) handleYouTubeAuthError(upload YouTubeUpload, err error) {
	if errors.Is(err, errYouTubeReconnect) {
		_ = p.app.store.markYouTubeUploadNeedsReconnect(upload.ID, upload.ChannelID, upload.ConnectionVersion, "Reconnect the same YouTube channel to resume this upload.", time.Now().Unix())
		return
	}
	p.retryYouTubeLater(upload, 0)
}

func (p *Processor) markYouTubeReconnectForUpload(upload YouTubeUpload, message string) {
	p.app.store.youtubeMu.Lock()
	defer p.app.store.youtubeMu.Unlock()
	current, err := p.app.store.youtubeConfig()
	if err == nil && current.ChannelID == upload.ChannelID && current.ConnectionVersion == upload.ConnectionVersion {
		_ = p.app.store.markYouTubeChannelNeedsReconnect(upload.ChannelID, upload.ConnectionVersion, current.EncryptedRefreshToken, message, time.Now().Unix())
	}
}

func (p *Processor) initiateYouTubeUpload(ctx context.Context, upload YouTubeUpload, file *os.File, accessToken string) {
	now := time.Now()
	claimed, err := p.app.store.claimYouTubeUploadInitiation(upload.ID, now.Unix())
	if err != nil || !claimed {
		return
	}
	metadata, err := json.Marshal(youtubeUploadPayload(upload))
	if err != nil {
		_ = p.app.store.failYouTubeUpload(upload.ID, youtubeUploadFailed, "The upload metadata could not be encoded.", time.Now().Unix())
		return
	}
	headers := make(http.Header)
	headers.Set("X-Upload-Content-Length", strconv.FormatInt(upload.SourceSize, 10))
	headers.Set("X-Upload-Content-Type", "video/mp4")
	response, err := p.app.youtubeRequest(ctx, http.MethodPost, youtubeUploadInitiationURL(p.app), accessToken, bytes.NewReader(metadata), int64(len(metadata)), headers)
	if err != nil {
		_ = p.app.store.unknownYouTubeUploadOutcome(upload.ID, "YouTube may have started the upload, but its resumable session could not be saved. Check YouTube Studio before restarting.", time.Now().Unix())
		return
	}
	if response.StatusCode == http.StatusUnauthorized {
		response.Body.Close()
		requeued, requeueErr := p.app.store.requeueYouTubeInitiation(upload.ID, time.Now().Add(2*time.Second).Unix(), time.Now().Unix())
		if requeueErr != nil || !requeued {
			return
		}
		_, refreshErr := p.app.youtubeAccessToken(ctx, upload, true)
		if refreshErr != nil {
			p.handleYouTubeAuthError(upload, refreshErr)
			return
		}
		return
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if response.StatusCode >= 500 || response.StatusCode >= 300 && response.StatusCode < 400 {
			response.Body.Close()
			_ = p.app.store.unknownYouTubeUploadOutcome(upload.ID, "YouTube may have started the upload, but the response was incomplete. Check YouTube Studio before restarting.", time.Now().Unix())
			return
		}
		message := p.app.safeYouTubeAPIError(response)
		_ = p.app.store.failYouTubeUpload(upload.ID, youtubeUploadFailed, message, time.Now().Unix())
		return
	}
	location, valid := youtubeUploadLocation(p.app, response)
	response.Body.Close()
	if !valid {
		_ = p.app.store.unknownYouTubeUploadOutcome(upload.ID, "YouTube returned an invalid resumable session. Check YouTube Studio before restarting.", time.Now().Unix())
		return
	}
	ciphertext, err := p.app.security.EncryptSetting("youtube_upload_session_"+upload.ID, location)
	if err != nil {
		_ = p.app.store.unknownYouTubeUploadOutcome(upload.ID, "YouTube started an upload, but its secure resumable session could not be stored. Check YouTube Studio before restarting.", time.Now().Unix())
		return
	}
	if err := p.app.store.setYouTubeUploadSession(upload.ID, ciphertext, time.Now().Unix()); err != nil {
		_ = p.app.store.unknownYouTubeUploadOutcome(upload.ID, "YouTube started an upload, but its resumable session could not be stored. Check YouTube Studio before restarting.", time.Now().Unix())
	}
}

func (s *Store) requeueYouTubeInitiation(id string, nextAttempt, now int64) (bool, error) {
	result, err := s.db.Exec(`UPDATE youtube_uploads SET status=CASE WHEN cancel_requested=1 THEN 'canceled' ELSE 'queued' END,error='',outcome_uncertain=0,cancel_requested=0,next_attempt_at=CASE WHEN cancel_requested=1 THEN 0 ELSE ? END,updated_at=? WHERE id=? AND status='initiating'`, nextAttempt, now, id)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func (p *Processor) resumeYouTubeUpload(ctx context.Context, upload YouTubeUpload, file *os.File, accessToken, sessionURL string) {
	headers := make(http.Header)
	headers.Set("Content-Range", fmt.Sprintf("bytes */%d", upload.SourceSize))
	headers.Set("Content-Type", "video/mp4")
	response, err := p.app.youtubeRequest(ctx, http.MethodPut, sessionURL, accessToken, nil, 0, headers)
	if err != nil {
		p.retryYouTubeLater(upload, 0)
		return
	}
	if response.StatusCode == http.StatusUnauthorized {
		response.Body.Close()
		_, refreshErr := p.app.youtubeAccessToken(ctx, upload, true)
		if refreshErr != nil {
			p.handleYouTubeAuthError(upload, refreshErr)
			return
		}
		p.retryYouTubeLater(upload, 2*time.Second)
		return
	}
	if response.StatusCode == http.StatusOK || response.StatusCode == http.StatusCreated {
		p.completeYouTubeFromResponse(upload, response)
		return
	}
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusGone {
		response.Body.Close()
		p.handleExpiredYouTubeSession(upload)
		return
	}
	if response.StatusCode == http.StatusPermanentRedirect {
		offset, valid := parseYouTubeResumeOffset(response.Header.Get("Range"), upload.SourceSize)
		response.Body.Close()
		if !valid {
			_ = p.app.store.unknownYouTubeUploadOutcome(upload.ID, "YouTube returned an invalid resume offset. Check YouTube Studio before restarting.", time.Now().Unix())
			return
		}
		_ = p.app.store.consumeYouTubeRestartApproval(upload.ID, time.Now().Unix())
		if offset >= upload.SourceSize {
			p.retryYouTubeLater(upload, 10*time.Second)
			return
		}
		p.sendYouTubeChunks(ctx, upload, file, accessToken, sessionURL, offset)
		return
	}
	if response.StatusCode >= 500 || response.StatusCode == http.StatusTooManyRequests {
		response.Body.Close()
		p.retryYouTubeLater(upload, 0)
		return
	}
	message := p.app.safeYouTubeAPIError(response)
	_ = p.app.store.unknownYouTubeUploadOutcome(upload.ID, message+" Check YouTube Studio before restarting this upload.", time.Now().Unix())
}

func (p *Processor) sendYouTubeChunks(ctx context.Context, upload YouTubeUpload, file *os.File, accessToken, sessionURL string, offset int64) {
	for offset < upload.SourceSize {
		if ctx.Err() != nil {
			p.retryYouTubeLater(upload, 0)
			return
		}
		current, err := p.app.store.YouTubeUpload(upload.ID)
		if err != nil {
			return
		}
		if current.CancelRequested {
			_ = p.app.store.cancelIncompleteYouTubeUpload(upload.ID, time.Now().Unix())
			return
		}
		endExclusive := offset + youtubeUploadChunkSize
		if endExclusive > upload.SourceSize {
			endExclusive = upload.SourceSize
		}
		chunkLength := endExclusive - offset
		end := endExclusive - 1
		headers := make(http.Header)
		headers.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, end, upload.SourceSize))
		headers.Set("Content-Type", "video/mp4")
		reader := io.NewSectionReader(file, offset, chunkLength)
		response, err := p.app.youtubeRequest(ctx, http.MethodPut, sessionURL, accessToken, reader, chunkLength, headers)
		if err != nil {
			p.retryYouTubeLater(upload, 0)
			return
		}
		if response.StatusCode == http.StatusOK || response.StatusCode == http.StatusCreated {
			p.completeYouTubeFromResponse(upload, response)
			return
		}
		if response.StatusCode == http.StatusUnauthorized {
			response.Body.Close()
			_, refreshErr := p.app.youtubeAccessToken(ctx, upload, true)
			if refreshErr != nil {
				p.handleYouTubeAuthError(upload, refreshErr)
				return
			}
			p.retryYouTubeLater(upload, 2*time.Second)
			return
		}
		if response.StatusCode == http.StatusPermanentRedirect {
			newOffset, valid := parseYouTubeResumeOffset(response.Header.Get("Range"), upload.SourceSize)
			response.Body.Close()
			if !valid || newOffset < offset || newOffset > endExclusive {
				_ = p.app.store.unknownYouTubeUploadOutcome(upload.ID, "YouTube returned an invalid resume offset. Check YouTube Studio before restarting.", time.Now().Unix())
				return
			}
			_ = p.app.store.consumeYouTubeRestartApproval(upload.ID, time.Now().Unix())
			if newOffset == offset {
				p.retryYouTubeLater(upload, 5*time.Second)
				return
			}
			offset = newOffset
			progress := 0
			if upload.SourceSize > 0 {
				progress = int(float64(offset) / float64(upload.SourceSize) * 100)
			}
			_ = p.app.store.setYouTubeUploadProgress(upload.ID, progress, 0, false, time.Now().Unix())
			continue
		}
		if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusGone {
			response.Body.Close()
			p.handleExpiredYouTubeSession(upload)
			return
		}
		if response.StatusCode >= 500 || response.StatusCode == http.StatusTooManyRequests {
			response.Body.Close()
			p.retryYouTubeLater(upload, 0)
			return
		}
		message := p.app.safeYouTubeAPIError(response)
		_ = p.app.store.unknownYouTubeUploadOutcome(upload.ID, message+" Check YouTube Studio before restarting this upload.", time.Now().Unix())
		return
	}
}

func (p *Processor) handleExpiredYouTubeSession(upload YouTubeUpload) {
	current, err := p.app.store.YouTubeUpload(upload.ID)
	if err == nil && current.CancelRequested {
		_ = p.app.store.unknownYouTubeUploadOutcome(upload.ID, "The YouTube resume session expired while cancellation was requested. Check YouTube Studio before restarting.", time.Now().Unix())
		return
	}
	allowed, expireErr := p.app.store.expireYouTubeSession(upload.ID, time.Now().Unix())
	if expireErr == nil && allowed {
		return
	}
	_ = p.app.store.unknownYouTubeUploadOutcome(upload.ID, "The YouTube resume session expired with an unknown upload outcome. Check YouTube Studio before restarting.", time.Now().Unix())
}

func (p *Processor) completeYouTubeFromResponse(upload YouTubeUpload, response *http.Response) {
	body, err := readYouTubeResponse(response)
	if err != nil {
		_ = p.app.store.unknownYouTubeUploadOutcome(upload.ID, "YouTube finished receiving the video but returned an unreadable response. Check YouTube Studio before restarting.", time.Now().Unix())
		return
	}
	video, err := decodeYouTubeVideo(body)
	if err != nil {
		_ = p.app.store.unknownYouTubeUploadOutcome(upload.ID, "YouTube finished receiving the video but did not confirm its video ID. Check YouTube Studio before restarting.", time.Now().Unix())
		return
	}
	privacy := video.Status.PrivacyStatus
	if privacy != "private" && privacy != "unlisted" && privacy != "public" {
		privacy = upload.RequestedPrivacyStatus
	}
	if err := p.app.store.completeYouTubeUpload(upload.ID, video.ID, privacy, time.Now().Unix()); err != nil {
		p.logger.Warn("persist YouTube upload completion failed", "upload_id", upload.ID)
	}
}

func (p *Processor) retryYouTubeLater(upload YouTubeUpload, minimum time.Duration) {
	if upload.AttemptCount >= youtubeTransientAttemptLimit-1 {
		now := time.Now().Unix()
		if upload.YouTubeVideoID != "" {
			_ = p.app.store.updateYouTubeProcessing(upload.ID, youtubeUploadAttentionRequired, "status_check_unavailable", "YouTube has not confirmed this video's processing status. Check YouTube Studio before abandoning the upload.", upload.ProcessingCheckCount, 0, now)
		} else if upload.EncryptedSessionURL != "" || upload.Status == youtubeUploadSending || upload.Status == youtubeUploadInitiating {
			_ = p.app.store.unknownYouTubeUploadOutcome(upload.ID, "YouTube could not confirm the resumable upload after several checks. Check YouTube Studio before restarting.", now)
		} else {
			_ = p.app.store.failYouTubeUpload(upload.ID, youtubeUploadFailed, "YouTube could not be reached after several attempts. Retry after checking your connection.", now)
		}
		return
	}
	delay := scheduleYouTubeRetryDelay(upload.AttemptCount)
	if minimum > delay {
		delay = minimum
	}
	now := time.Now()
	_ = p.app.store.scheduleYouTubeUploadRetry(upload.ID, upload.ChannelID, upload.ConnectionVersion, now.Add(delay).Unix(), now.Unix())
}

func (p *Processor) retryYouTubeProcessingCheck(upload YouTubeUpload, status string) {
	checks := upload.ProcessingCheckCount + 1
	now := time.Now()
	if checks >= youtubeProcessingCheckLimit || upload.AttemptCount >= youtubeTransientAttemptLimit-1 {
		_ = p.app.store.updateYouTubeProcessing(upload.ID, youtubeUploadAttentionRequired, "status_check_unavailable", "YouTube has not confirmed this video's processing status. Check YouTube Studio before abandoning the upload.", checks, 0, now.Unix())
		return
	}
	_ = p.app.store.retryYouTubeProcessingCheck(upload.ID, status, checks, now.Add(scheduleYouTubeRetryDelay(upload.AttemptCount)).Unix(), now.Unix())
}

func (p *Processor) pollYouTubeProcessing(ctx context.Context, upload YouTubeUpload, refreshed bool) {
	if upload.ProcessingCheckCount >= youtubeProcessingCheckLimit {
		_ = p.app.store.updateYouTubeProcessing(upload.ID, youtubeUploadAttentionRequired, "processing_timeout", "YouTube has not finished processing this video. Check YouTube Studio before abandoning the upload.", upload.ProcessingCheckCount, 0, time.Now().Unix())
		return
	}
	token, err := p.app.youtubeAccessToken(ctx, upload, false)
	if err != nil {
		p.handleYouTubeAuthError(upload, err)
		return
	}
	response, err := p.app.youtubeRequest(ctx, http.MethodGet, youtubeProcessingURL(p.app, upload.YouTubeVideoID), token, nil, -1, nil)
	if err != nil {
		p.retryYouTubeProcessingCheck(upload, "checking")
		return
	}
	if response.StatusCode == http.StatusUnauthorized && !refreshed {
		response.Body.Close()
		_, refreshErr := p.app.youtubeAccessToken(ctx, upload, true)
		if refreshErr != nil {
			p.handleYouTubeAuthError(upload, refreshErr)
			return
		}
		p.pollYouTubeProcessing(ctx, upload, true)
		return
	}
	if response.StatusCode == http.StatusUnauthorized {
		response.Body.Close()
		p.markYouTubeReconnectForUpload(upload, "Reconnect the YouTube channel to check processing status.")
		return
	}
	if response.StatusCode >= 500 || response.StatusCode == http.StatusTooManyRequests {
		response.Body.Close()
		p.retryYouTubeProcessingCheck(upload, "checking")
		return
	}
	body, readErr := readYouTubeResponse(response)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if response.StatusCode == http.StatusForbidden && youtubeAPIReason(body) == "insufficientPermissions" {
			p.markYouTubeReconnectForUpload(upload, "Reconnect the YouTube channel to check processing status.")
			return
		}
		p.retryYouTubeProcessingCheck(upload, "checking")
		return
	}
	if readErr != nil {
		p.retryYouTubeProcessingCheck(upload, "checking")
		return
	}
	var result youtubeVideosResponse
	if json.Unmarshal(body, &result) != nil || len(result.Items) != 1 || result.Items[0].ID != upload.YouTubeVideoID {
		p.retryYouTubeProcessingCheck(upload, "checking")
		return
	}
	video := result.Items[0]
	_ = p.app.store.updateYouTubeEffectivePrivacy(upload.ID, video.Status.PrivacyStatus, time.Now().Unix())
	checks := upload.ProcessingCheckCount + 1
	status := strings.ToLower(strings.TrimSpace(video.Status.UploadStatus))
	processingStatus := strings.ToLower(strings.TrimSpace(video.ProcessingDetails.ProcessingStatus))
	if processingStatus == "" {
		processingStatus = status
	}
	if status == "processed" || processingStatus == "succeeded" {
		_ = p.app.store.updateYouTubeProcessing(upload.ID, youtubeUploadCompleted, "succeeded", "", checks, 0, time.Now().Unix())
		return
	}
	if status == "failed" || status == "rejected" || status == "deleted" || processingStatus == "failed" || processingStatus == "terminated" {
		reason := safeYouTubeProcessingReason(video.ProcessingDetails.RejectionReason, video.ProcessingDetails.FailureReason)
		message := "YouTube could not process this video. Check YouTube Studio for details."
		if reason != "" {
			message = "YouTube could not process this video (" + reason + "). Check YouTube Studio for details."
		}
		_ = p.app.store.updateYouTubeProcessing(upload.ID, youtubeUploadFailed, processingStatus, message, checks, 0, time.Now().Unix())
		return
	}
	if checks >= youtubeProcessingCheckLimit {
		_ = p.app.store.updateYouTubeProcessing(upload.ID, youtubeUploadAttentionRequired, "processing_timeout", "YouTube has not finished processing this video. Check YouTube Studio before abandoning the upload.", checks, 0, time.Now().Unix())
		return
	}
	_ = p.app.store.updateYouTubeProcessing(upload.ID, youtubeUploadProcessing, processingStatus, "", checks, time.Now().Add(youtubeProcessingDelay(checks)).Unix(), time.Now().Unix())
}

func safeYouTubeProcessingReason(rejection, failure string) string {
	allowed := map[string]struct{}{
		"copyright": {}, "termsOfUse": {}, "videoTooLong": {}, "videoTooShort": {}, "processingFailed": {},
		"corruptedFile": {}, "emptyFile": {}, "codec": {}, "conversion": {}, "uploadAborted": {},
	}
	for _, reason := range []string{rejection, failure} {
		if _, ok := allowed[reason]; ok {
			return reason
		}
	}
	return ""
}
