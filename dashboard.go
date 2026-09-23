package main

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	apiKeySetting            = "openrouter_api_key"
	videoProviderSetting     = "video_provider"
	modalVideoBaseURLSetting = "modal_video_base_url"
	modalVideoAPIKeySetting  = "modal_video_api_key"
)

type dashboardApp struct {
	store           *Store
	security        *Security
	logger          *slog.Logger
	baseURL         string
	limiter         *loginThrottle
	recoveryLimiter *loginThrottle
}

func NewDashboardHandler(store *Store, security *Security, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	app := &dashboardApp{store: store, security: security, logger: logger, limiter: newLoginThrottle(), recoveryLimiter: newLoginThrottle()}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", app.index)
	mux.HandleFunc("GET /static/app.js", app.static("static/app.js", "application/javascript; charset=utf-8"))
	mux.HandleFunc("GET /static/styles.css", app.static("static/styles.css", "text/css; charset=utf-8"))
	mux.HandleFunc("GET /static/model-picker.css", app.static("static/model-picker.css", "text/css; charset=utf-8"))
	mux.HandleFunc("GET /static/project.css", app.static("static/project.css", "text/css; charset=utf-8"))
	mux.HandleFunc("GET /health", app.health)
	mux.HandleFunc("POST /api/login", app.login)
	mux.HandleFunc("POST /api/password/recover", app.recoverPassword)
	mux.HandleFunc("GET /api/session", app.session)
	mux.Handle("POST /api/logout", app.requireAuth(http.HandlerFunc(app.logout)))
	mux.Handle("GET /models", app.requirePasswordChanged(http.HandlerFunc(app.models)))
	mux.Handle("GET /api/video-models", app.requirePasswordChanged(http.HandlerFunc(app.models)))
	mux.Handle("POST /generate", app.requirePasswordChanged(http.HandlerFunc(app.generate)))
	mux.Handle("GET /status", app.requirePasswordChanged(http.HandlerFunc(app.status)))
	mux.Handle("GET /api/generations", app.requirePasswordChanged(http.HandlerFunc(app.history)))
	mux.Handle("POST /api/projects", app.requirePasswordChanged(http.HandlerFunc(app.createProject)))
	mux.Handle("GET /api/projects", app.requirePasswordChanged(http.HandlerFunc(app.projects)))
	mux.Handle("GET /api/projects/{id}", app.requirePasswordChanged(http.HandlerFunc(app.project)))
	mux.Handle("POST /api/projects/{id}/retry", app.requirePasswordChanged(http.HandlerFunc(app.retryProject)))
	mux.Handle("POST /api/projects/{id}/scenes/{scene}/retry", app.requirePasswordChanged(http.HandlerFunc(app.retryProjectScene)))
	mux.Handle("GET /api/projects/{id}/video", app.requirePasswordChanged(http.HandlerFunc(app.projectVideo)))
	mux.Handle("DELETE /api/generations/{id}", app.requirePasswordChanged(http.HandlerFunc(app.deleteGeneration)))
	mux.Handle("GET /video", app.requirePasswordChanged(http.HandlerFunc(app.video)))
	mux.Handle("GET /api/settings", app.requirePasswordChanged(http.HandlerFunc(app.settings)))
	mux.Handle("PUT /api/settings/api-key", app.requirePasswordChanged(http.HandlerFunc(app.updateAPIKey)))
	mux.Handle("PUT /api/settings/video-provider", app.requirePasswordChanged(http.HandlerFunc(app.updateVideoProvider)))
	mux.Handle("PUT /api/settings/modal", app.requirePasswordChanged(http.HandlerFunc(app.updateModalSettings)))
	mux.Handle("PUT /api/settings/video-model", app.requirePasswordChanged(http.HandlerFunc(app.updateVideoModel)))
	mux.Handle("POST /api/settings/video-provider/test", app.requirePasswordChanged(http.HandlerFunc(app.testVideoProvider)))
	mux.Handle("PUT /api/settings/password", app.requireAuth(http.HandlerFunc(app.updatePassword)))
	return securityHeaders(mux)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; media-src 'self'; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

func (a *dashboardApp) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	a.static("static/index.html", "text/html; charset=utf-8")(w, r)
}

func (a *dashboardApp) static(name, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		body, err := staticFiles.ReadFile(name)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "static asset unavailable")
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(body)
	}
}

func (a *dashboardApp) health(w http.ResponseWriter, _ *http.Request) {
	if err := a.store.db.Ping(); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
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
	a.security.Logout(w, r)
	writeJSON(w, http.StatusOK, map[string]string{"status": "password reset"})
}

func (a *dashboardApp) logout(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) {
		return
	}
	a.security.Logout(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (a *dashboardApp) openRouterTextProvider() (*OpenRouterClient, error) {
	encrypted, err := a.store.Setting(apiKeySetting)
	if err != nil {
		return nil, errors.New("OpenRouter API key is not configured")
	}
	key, err := a.security.DecryptSetting(apiKeySetting, encrypted)
	if err != nil {
		return nil, err
	}
	client := NewOpenRouterClient(key)
	if a.baseURL != "" {
		client.BaseURL = a.baseURL
	}
	return client, nil
}

func (a *dashboardApp) selectedVideoProvider() (VideoProviderID, error) {
	value, err := a.store.Setting(videoProviderSetting)
	if errors.Is(err, sql.ErrNoRows) {
		return VideoProviderOpenRouter, nil
	}
	if err != nil {
		return "", err
	}
	provider := VideoProviderID(value)
	if !validVideoProvider(provider) {
		return "", errors.New("unsupported video provider")
	}
	return provider, nil
}

func (a *dashboardApp) videoProvider(provider VideoProviderID) (VideoService, error) {
	switch provider {
	case VideoProviderOpenRouter:
		return a.openRouterTextProvider()
	case VideoProviderModal:
		baseURL, err := a.store.Setting(modalVideoBaseURLSetting)
		if err != nil {
			return nil, errors.New("Modal video base URL is not configured")
		}
		key, err := a.store.Setting(modalVideoAPIKeySetting)
		if err != nil {
			return nil, errors.New("Modal video API key is not configured")
		}
		plain, err := a.security.DecryptSetting(modalVideoAPIKeySetting, key)
		if err != nil {
			return nil, err
		}
		return NewModalVideoClient(baseURL, plain), nil
	default:
		return nil, errors.New("unsupported video provider")
	}
}

func (a *dashboardApp) activeVideoProvider() (VideoService, VideoProviderID, error) {
	provider, err := a.selectedVideoProvider()
	if err != nil {
		return nil, "", err
	}
	service, err := a.videoProvider(provider)
	return service, provider, err
}

func (a *dashboardApp) models(w http.ResponseWriter, r *http.Request) {
	requested := VideoProviderID(r.URL.Query().Get("provider"))
	var err error
	if requested == "" {
		requested, err = a.selectedVideoProvider()
	} else if !validVideoProvider(requested) {
		writeError(w, http.StatusBadRequest, "unsupported video provider")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to load video provider")
		return
	}
	provider, err := a.videoProvider(requested)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	models, err := provider.ListVideoModels(r.Context())
	if err != nil {
		a.logger.Error("models request failed")
		writeError(w, http.StatusBadGateway, "unable to load models")
		return
	}
	if models == nil {
		models = []VideoModel{}
	}
	writeJSON(w, http.StatusOK, struct {
		Models          []VideoModel `json:"models"`
		MaxPromptLength int          `json:"max_prompt_length"`
		Provider        string       `json:"provider"`
		SelectedModel   string       `json:"selected_model,omitempty"`
	}{models, MaxPromptLength, string(requested), a.selectedVideoModel(requested)})
}

func (a *dashboardApp) selectedVideoModel(provider VideoProviderID) string {
	model, err := a.store.Setting("video_model." + string(provider))
	if err != nil {
		return ""
	}
	return model
}

func estimateCost(duration int, price string) string {
	if duration <= 0 || price == "" {
		return ""
	}
	value, err := strconv.ParseFloat(price, 64)
	if err != nil {
		return ""
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.6f", value*float64(duration)), "0"), ".")
}

func (a *dashboardApp) generate(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) {
		return
	}
	if !isJSON(r.Header.Get("Content-Type")) {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	var request GenerateRequest
	if decodeJSONBody(w, r, &request) != nil {
		return
	}
	request.Prompt, request.Model = strings.TrimSpace(request.Prompt), strings.TrimSpace(request.Model)
	if request.Prompt == "" || len([]rune(request.Prompt)) > MaxPromptLength {
		writeError(w, http.StatusBadRequest, "a valid prompt is required")
		return
	}
	provider, providerID, err := a.activeVideoProvider()
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	models, err := provider.ListVideoModels(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "unable to validate model")
		return
	}
	model, ok := findModel(models, request.Model)
	if !ok {
		writeError(w, http.StatusBadRequest, "selected model is unavailable")
		return
	}
	if err := validateOptions(request, model); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	generation, err := provider.GenerateVideo(r.Context(), request)
	if err != nil {
		a.logger.Error("generation submission failed", "model", request.Model)
		writeError(w, http.StatusBadGateway, "video provider rejected the generation request")
		return
	}
	if generation == nil || !safeID(generation.ID) {
		writeError(w, http.StatusBadGateway, "video provider returned an invalid generation response")
		return
	}
	if generation.Status == "" {
		generation.Status = "queued"
	}
	// A provider is allowed to return a completed job at submission time. Keep
	// that record eligible for the durable downloader instead of stranding it in
	// a completed state with no local video file.
	if generation.Status == "completed" {
		generation.Status = "downloading"
	}
	record := GenerationRecord{
		ID:               generation.ID,
		VideoProvider:    string(providerID),
		Prompt:           request.Prompt,
		Model:            request.Model,
		Duration:         request.Duration,
		AspectRatio:      request.AspectRatio,
		Status:           generation.Status,
		CostUSD:          generation.CostUSD,
		EstimatedCostUSD: estimateCost(request.Duration, model.PricePerSecond),
	}
	if err := a.store.InsertGeneration(record); err != nil {
		a.logger.Error("save generation failed", "generation_id", generation.ID)
		writeError(w, http.StatusInternalServerError, "unable to save generation")
		return
	}
	writeJSON(w, http.StatusAccepted, record)
}

func (a *dashboardApp) status(w http.ResponseWriter, r *http.Request) {
	id, ok := oneSafeID(w, r)
	if !ok {
		return
	}
	record, err := a.store.Generation(id)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "generation not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to load generation")
		return
	}
	writeJSON(w, http.StatusOK, record)
}

func (a *dashboardApp) history(w http.ResponseWriter, r *http.Request) {
	limit := 25
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 100")
			return
		}
		limit = parsed
	}
	var beforeCreated int64
	var beforeID string
	if cursor := r.URL.Query().Get("before"); cursor != "" {
		timestamp, id, found := strings.Cut(cursor, ":")
		parsed, err := strconv.ParseInt(timestamp, 10, 64)
		if !found || err != nil || !safeID(id) {
			writeError(w, http.StatusBadRequest, "invalid page cursor")
			return
		}
		beforeCreated, beforeID = parsed, id
	}
	records, err := a.store.GenerationsPage(limit, beforeCreated, beforeID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to load history")
		return
	}
	stats, err := a.store.GenerationStats()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to load history statistics")
		return
	}
	next := ""
	if len(records) == limit {
		last := records[len(records)-1]
		next = strconv.FormatInt(last.CreatedAt, 10) + ":" + last.ID
	}
	writeJSON(w, http.StatusOK, map[string]any{"generations": records, "stats": stats, "next_page": next})
}

func compatibleProjectModel(model VideoModel) bool {
	return containsInt(model.Durations, ProjectSceneSeconds) && containsString(model.AspectRatios, ProjectAspectRatio)
}

func preferredProjectModel(models []VideoModel) (VideoModel, bool) {
	for _, model := range models {
		if compatibleProjectModel(model) {
			return model, true
		}
	}
	return VideoModel{}, false
}

func (a *dashboardApp) createProject(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) || !isJSON(r.Header.Get("Content-Type")) {
		if !isJSON(r.Header.Get("Content-Type")) {
			writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		}
		return
	}
	var input ProjectRequest
	if decodeJSONBody(w, r, &input) != nil {
		return
	}
	input.Topic, input.Model = strings.TrimSpace(input.Topic), strings.TrimSpace(input.Model)
	if input.Topic == "" || len([]rune(input.Topic)) > MaxPromptLength {
		writeError(w, http.StatusBadRequest, "a topic or story idea of at most 4,000 characters is required")
		return
	}
	provider, providerID, err := a.activeVideoProvider()
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	models, err := provider.ListVideoModels(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "unable to validate video models")
		return
	}
	var model VideoModel
	var ok bool
	if input.Model == "" {
		if selected := a.selectedVideoModel(providerID); selected != "" {
			model, ok = findModel(models, selected)
			ok = ok && compatibleProjectModel(model)
		} else {
			model, ok = preferredProjectModel(models)
		}
	} else {
		model, ok = findModel(models, input.Model)
		ok = ok && compatibleProjectModel(model)
	}
	if !ok {
		writeError(w, http.StatusBadRequest, "select a video model that supports 6-second clips in 9:16")
		return
	}
	project, err := a.store.InsertProjectForProvider(input.Topic, string(providerID), model.ID)
	if err != nil {
		a.logger.Error("create project failed")
		writeError(w, http.StatusInternalServerError, "unable to create project")
		return
	}
	writeJSON(w, http.StatusAccepted, project)
}

func (a *dashboardApp) projects(w http.ResponseWriter, _ *http.Request) {
	projects, err := a.store.Projects(24)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to load projects")
		return
	}
	if projects == nil {
		projects = []VideoProject{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": projects})
}

func (a *dashboardApp) project(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !safeID(id) {
		writeError(w, http.StatusBadRequest, "invalid project id")
		return
	}
	project, err := a.store.Project(id)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to load project")
		return
	}
	writeJSON(w, http.StatusOK, project)
}

func (a *dashboardApp) retryProjectScene(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) {
		return
	}
	id := r.PathValue("id")
	number, err := strconv.Atoi(r.PathValue("scene"))
	if !safeID(id) || err != nil || number < 1 || number > ProjectSceneCount {
		writeError(w, http.StatusBadRequest, "invalid project or scene")
		return
	}
	if err := a.store.RetryScene(id, number); errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusConflict, "only a failed scene can be retried")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to retry scene")
		return
	}
	project, err := a.store.Project(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to load project")
		return
	}
	writeJSON(w, http.StatusAccepted, project)
}

func (a *dashboardApp) retryProject(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) {
		return
	}
	id := r.PathValue("id")
	if !safeID(id) {
		writeError(w, http.StatusBadRequest, "invalid project id")
		return
	}
	if err := a.store.RetryProject(id); errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusConflict, "only a failed project can be retried")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to retry project")
		return
	}
	project, _ := a.store.Project(id)
	writeJSON(w, http.StatusAccepted, project)
}

func (a *dashboardApp) projectVideo(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !safeID(id) {
		writeError(w, http.StatusBadRequest, "invalid project id")
		return
	}
	project, err := a.store.Project(id)
	if err != nil || !project.FinalVideoReady {
		writeError(w, http.StatusNotFound, "final video not found")
		return
	}
	clean := filepath.Clean(project.FinalVideoPath)
	rel, err := filepath.Rel(a.store.projectDir, clean)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		writeError(w, http.StatusForbidden, "invalid final video path")
		return
	}
	file, err := os.Open(clean)
	if err != nil {
		writeError(w, http.StatusNotFound, "final video not found")
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to read final video")
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="project-%s.mp4"`, id))
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	http.ServeContent(w, r, filepath.Base(clean), info.ModTime(), file)
}

func (a *dashboardApp) deleteGeneration(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) {
		return
	}
	id := r.PathValue("id")
	if !safeID(id) {
		writeError(w, http.StatusBadRequest, "invalid generation id")
		return
	}
	record, err := a.store.BeginDelete(id)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "generation not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to delete generation")
		return
	}
	if record.VideoPath != "" {
		if err := a.removeVideo(record.VideoPath); err != nil {
			a.store.CancelDelete(id, record.Status)
			writeError(w, http.StatusInternalServerError, "unable to remove video file")
			return
		}
	}
	if err := a.store.FinishDelete(id); err != nil {
		a.store.CancelDelete(id, record.Status)
		writeError(w, http.StatusInternalServerError, "unable to delete generation")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *dashboardApp) removeVideo(path string) error {
	clean := filepath.Clean(path)
	rel, err := filepath.Rel(a.store.videoDir, clean)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("invalid video path")
	}
	err = os.Remove(clean)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (a *dashboardApp) video(w http.ResponseWriter, r *http.Request) {
	id, ok := oneSafeID(w, r)
	if !ok {
		return
	}
	record, err := a.store.Generation(id)
	if err != nil || record.VideoPath == "" {
		writeError(w, http.StatusNotFound, "video not found")
		return
	}
	cleanPath := filepath.Clean(record.VideoPath)
	rel, err := filepath.Rel(a.store.videoDir, cleanPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		writeError(w, http.StatusForbidden, "invalid video path")
		return
	}
	file, err := os.Open(cleanPath)
	if err != nil {
		writeError(w, http.StatusNotFound, "video not found")
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to read video")
		return
	}
	contentType := mime.TypeByExtension(filepath.Ext(cleanPath))
	if contentType == "" {
		contentType = "video/mp4"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="generation-%s.mp4"`, id))
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	http.ServeContent(w, r, filepath.Base(cleanPath), info.ModTime(), file)
}

func (a *dashboardApp) settings(w http.ResponseWriter, _ *http.Request) {
	provider, err := a.selectedVideoProvider()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to load settings")
		return
	}
	_, openRouterErr := a.store.Setting(apiKeySetting)
	modalBaseURL, _ := a.store.Setting(modalVideoBaseURLSetting)
	_, modalKeyErr := a.store.Setting(modalVideoAPIKeySetting)
	writeJSON(w, http.StatusOK, map[string]any{
		"video_provider":                 string(provider),
		"openrouter_api_key_configured":  openRouterErr == nil,
		"modal_video_base_url":           modalBaseURL,
		"modal_video_api_key_configured": modalKeyErr == nil,
		"video_models":                   map[string]string{"openrouter": a.selectedVideoModel(VideoProviderOpenRouter), "modal": a.selectedVideoModel(VideoProviderModal)},
	})
}

func (a *dashboardApp) updateAPIKey(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) {
		return
	}
	if !isJSON(r.Header.Get("Content-Type")) {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	var input struct {
		APIKey string `json:"api_key"`
	}
	if decodeJSONBody(w, r, &input) != nil {
		return
	}
	input.APIKey = strings.TrimSpace(input.APIKey)
	if len(input.APIKey) < 10 || len(input.APIKey) > 500 {
		writeError(w, http.StatusBadRequest, "enter a valid API key")
		return
	}
	client := NewOpenRouterClient(input.APIKey)
	if a.baseURL != "" {
		client.BaseURL = a.baseURL
	}
	if _, err := client.ListVideoModels(r.Context()); err != nil {
		var upstream *upstreamError
		if errors.As(err, &upstream) && (upstream.StatusCode == http.StatusUnauthorized || upstream.StatusCode == http.StatusForbidden) {
			writeError(w, http.StatusBadRequest, "API key is invalid or not authorized")
		} else {
			writeError(w, http.StatusBadGateway, "unable to test API key")
		}
		return
	}
	encrypted, err := a.security.EncryptSetting(apiKeySetting, input.APIKey)
	if err != nil || a.store.SetSetting(apiKeySetting, encrypted) != nil {
		writeError(w, http.StatusInternalServerError, "unable to save API key")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"openrouter_api_key_configured": true})
}

func (a *dashboardApp) updateVideoProvider(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) || !isJSON(r.Header.Get("Content-Type")) {
		if !isJSON(r.Header.Get("Content-Type")) {
			writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		}
		return
	}
	var input struct {
		Provider VideoProviderID `json:"video_provider"`
	}
	if decodeJSONBody(w, r, &input) != nil {
		return
	}
	if !validVideoProvider(input.Provider) {
		writeError(w, http.StatusBadRequest, "unsupported video provider")
		return
	}
	if err := a.store.SetSetting(videoProviderSetting, string(input.Provider)); err != nil {
		writeError(w, http.StatusInternalServerError, "unable to save video provider")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"video_provider": string(input.Provider)})
}

func (a *dashboardApp) updateModalSettings(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) || !isJSON(r.Header.Get("Content-Type")) {
		if !isJSON(r.Header.Get("Content-Type")) {
			writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		}
		return
	}
	var input struct {
		BaseURL string `json:"base_url"`
		APIKey  string `json:"api_key"`
	}
	if decodeJSONBody(w, r, &input) != nil {
		return
	}
	baseURL, err := validateModalBaseURL(input.BaseURL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	key := strings.TrimSpace(input.APIKey)
	if key == "" {
		encrypted, settingErr := a.store.Setting(modalVideoAPIKeySetting)
		if settingErr != nil {
			writeError(w, http.StatusBadRequest, "enter a Modal video API key")
			return
		}
		key, err = a.security.DecryptSetting(modalVideoAPIKeySetting, encrypted)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "unable to load Modal API key")
			return
		}
	}
	if len(key) < 10 || len(key) > 500 {
		writeError(w, http.StatusBadRequest, "enter a valid Modal video API key")
		return
	}
	if _, err := NewModalVideoClient(baseURL, key).ListVideoModels(r.Context()); err != nil {
		writeError(w, http.StatusBadGateway, "unable to test Modal video connection")
		return
	}
	if err := a.store.SetSetting(modalVideoBaseURLSetting, baseURL); err != nil {
		writeError(w, http.StatusInternalServerError, "unable to save Modal settings")
		return
	}
	if strings.TrimSpace(input.APIKey) != "" {
		encrypted, err := a.security.EncryptSetting(modalVideoAPIKeySetting, key)
		if err != nil || a.store.SetSetting(modalVideoAPIKeySetting, encrypted) != nil {
			writeError(w, http.StatusInternalServerError, "unable to save Modal API key")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"modal_video_base_url": baseURL, "modal_video_api_key_configured": true})
}

func (a *dashboardApp) updateVideoModel(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) || !isJSON(r.Header.Get("Content-Type")) {
		if !isJSON(r.Header.Get("Content-Type")) {
			writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		}
		return
	}
	var input struct {
		Provider VideoProviderID `json:"provider"`
		Model    string          `json:"model"`
	}
	if decodeJSONBody(w, r, &input) != nil {
		return
	}
	if input.Provider == "" {
		input.Provider, _ = a.selectedVideoProvider()
	}
	if !validVideoProvider(input.Provider) || strings.TrimSpace(input.Model) == "" {
		writeError(w, http.StatusBadRequest, "a supported provider and model are required")
		return
	}
	service, err := a.videoProvider(input.Provider)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	models, err := service.ListVideoModels(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "unable to validate video model")
		return
	}
	if _, ok := findModel(models, strings.TrimSpace(input.Model)); !ok {
		writeError(w, http.StatusBadRequest, "selected model is unavailable")
		return
	}
	if err := a.store.SetSetting("video_model."+string(input.Provider), strings.TrimSpace(input.Model)); err != nil {
		writeError(w, http.StatusInternalServerError, "unable to save video model")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"provider": string(input.Provider), "model": strings.TrimSpace(input.Model)})
}

func (a *dashboardApp) testVideoProvider(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) {
		return
	}
	service, provider, err := a.activeVideoProvider()
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	models, err := service.ListVideoModels(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "unable to test video provider")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"provider": string(provider), "model_count": len(models)})
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
