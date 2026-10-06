package app

import (
	"log/slog"
	"net/http"

	"video-automation/internal/webui"
)

type dashboardApp struct {
	store                     *Store
	security                  *Security
	logger                    *slog.Logger
	vault                     *vaultRuntime
	baseURL                   string
	callbackBaseURL           string
	videoCallbackBaseURL      string
	youtubeHTTPClient         *http.Client
	youtubeOAuthTokenEndpoint string
	youtubeDataAPIOrigin      string
	limiter                   *loginThrottle
	recoveryLimiter           *loginThrottle
	legacySnapshotBackfillErr error
}

func NewDashboardHandler(store *Store, security *Security, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	app := &dashboardApp{store: store, security: security, logger: logger, vault: newVaultRuntime(), callbackBaseURL: configuredCallbackBaseURL(), videoCallbackBaseURL: configuredVideoCallbackBaseURL(), limiter: newLoginThrottle(), recoveryLimiter: newLoginThrottle()}
	if err := store.RecoverInterruptedProjectDeletes(); err != nil {
		logger.Error("recover interrupted project deletions failed")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", app.index)
	mux.HandleFunc("GET /static/app.js", app.static("static/app.js", "application/javascript; charset=utf-8"))
	mux.HandleFunc("GET /static/styles.css", app.static("static/styles.css", "text/css; charset=utf-8"))
	mux.HandleFunc("GET /static/model-picker.css", app.static("static/model-picker.css", "text/css; charset=utf-8"))
	mux.HandleFunc("GET /static/project.css", app.static("static/project.css", "text/css; charset=utf-8"))
	mux.HandleFunc("GET /static/history-settings.css", app.static("static/history-settings.css", "text/css; charset=utf-8"))
	mux.HandleFunc("GET /static/youtube.css", app.static("static/youtube.css", "text/css; charset=utf-8"))
	mux.HandleFunc("GET /health", app.health)
	// Modal is an external worker and does not have a browser session. Its
	// callback is authenticated by a per-job capability token instead.
	mux.HandleFunc("POST /api/provider-callbacks/modal", app.modalCompletionCallback)
	mux.HandleFunc("POST /api/video-callbacks/{id}", app.videoCallback)
	mux.HandleFunc("POST /api/login", app.login)
	mux.HandleFunc("POST /api/password/recover", app.recoverPassword)
	mux.HandleFunc("GET /api/session", app.session)
	mux.Handle("POST /api/logout", app.requireAuth(http.HandlerFunc(app.logout)))
	mux.Handle("GET /models", app.requirePasswordChanged(http.HandlerFunc(app.models)))
	mux.Handle("GET /api/video-models", app.requirePasswordChanged(http.HandlerFunc(app.models)))
	mux.Handle("POST /generate", app.requirePasswordChanged(http.HandlerFunc(app.generate)))
	mux.Handle("GET /status", app.requirePasswordChanged(http.HandlerFunc(app.status)))
	mux.Handle("GET /api/generations", app.requirePasswordChanged(http.HandlerFunc(app.history)))
	mux.Handle("GET /api/events", app.requirePasswordChanged(http.HandlerFunc(app.events)))
	mux.Handle("GET /api/vault/status", app.requirePasswordChanged(http.HandlerFunc(app.vaultStatus)))
	mux.Handle("POST /api/vault/unlock", app.requirePasswordChanged(http.HandlerFunc(app.unlockVault)))
	mux.Handle("POST /api/vault/lock", app.requirePasswordChanged(http.HandlerFunc(app.lockVault)))
	mux.Handle("GET /api/vault/items", app.requirePasswordChanged(app.requireVaultUnlock(http.HandlerFunc(app.vaultItems))))
	mux.Handle("GET /api/vault/items/{kind}/{id}/video", app.requirePasswordChanged(app.requireVaultUnlock(http.HandlerFunc(app.vaultVideo))))
	mux.Handle("POST /api/vault/items/{kind}/{id}/move", app.requirePasswordChanged(http.HandlerFunc(app.moveToVault)))
	mux.Handle("POST /api/vault/items/{kind}/{id}/restore", app.requirePasswordChanged(app.requireVaultUnlock(http.HandlerFunc(app.restoreFromVault))))
	mux.Handle("POST /api/prompts/random", app.requirePasswordChanged(http.HandlerFunc(app.randomPrompt)))
	mux.Handle("POST /api/projects", app.requirePasswordChanged(http.HandlerFunc(app.createProject)))
	mux.Handle("GET /api/projects", app.requirePasswordChanged(http.HandlerFunc(app.projects)))
	mux.Handle("GET /api/projects/{id}", app.requirePasswordChanged(http.HandlerFunc(app.project)))
	mux.Handle("DELETE /api/projects/{id}", app.requirePasswordChanged(http.HandlerFunc(app.deleteProject)))
	mux.Handle("POST /api/projects/{id}/retry", app.requirePasswordChanged(http.HandlerFunc(app.retryProject)))
	mux.Handle("POST /api/projects/{id}/scenes/{scene}/retry", app.requirePasswordChanged(http.HandlerFunc(app.retryProjectScene)))
	mux.Handle("GET /api/projects/{id}/video", app.requirePasswordChanged(http.HandlerFunc(app.projectVideo)))
	mux.Handle("DELETE /api/generations/{id}", app.requirePasswordChanged(http.HandlerFunc(app.deleteGeneration)))
	mux.Handle("GET /video", app.requirePasswordChanged(http.HandlerFunc(app.video)))
	mux.Handle("GET /api/settings", app.requirePasswordChanged(http.HandlerFunc(app.settings)))
	mux.Handle("PUT /api/settings/api-key", app.requirePasswordChanged(http.HandlerFunc(app.updateAPIKey)))
	mux.Handle("PUT /api/settings/video-provider", app.requirePasswordChanged(http.HandlerFunc(app.updateVideoProvider)))
	mux.Handle("PUT /api/settings/modal", app.requirePasswordChanged(http.HandlerFunc(app.updateModalSettings)))
	mux.Handle("GET /api/modal-accounts", app.requirePasswordChanged(http.HandlerFunc(app.modalAccounts)))
	mux.Handle("POST /api/modal-accounts", app.requirePasswordChanged(http.HandlerFunc(app.createModalAccount)))
	mux.Handle("PUT /api/modal-accounts/active", app.requirePasswordChanged(http.HandlerFunc(app.setActiveModalAccount)))
	mux.Handle("PUT /api/modal-accounts/{id}", app.requirePasswordChanged(http.HandlerFunc(app.updateModalAccount)))
	mux.Handle("DELETE /api/modal-accounts/{id}", app.requirePasswordChanged(http.HandlerFunc(app.deleteModalAccount)))
	mux.Handle("PUT /api/settings/video-model", app.requirePasswordChanged(http.HandlerFunc(app.updateVideoModel)))
	mux.Handle("POST /api/settings/video-provider/test", app.requirePasswordChanged(http.HandlerFunc(app.testVideoProvider)))
	mux.Handle("PUT /api/settings/password", app.requireAuth(http.HandlerFunc(app.updatePassword)))
	mux.Handle("PUT /api/settings/vault-code", app.requirePasswordChanged(http.HandlerFunc(app.updateVaultCode)))
	mux.Handle("GET /api/youtube/status", app.requirePasswordChanged(http.HandlerFunc(app.youtubeStatus)))
	mux.Handle("PUT /api/youtube/config", app.requirePasswordChanged(http.HandlerFunc(app.updateYouTubeConfig)))
	mux.Handle("POST /api/youtube/connect", app.requirePasswordChanged(http.HandlerFunc(app.connectYouTube)))
	mux.HandleFunc("GET /api/youtube/oauth/callback", app.youtubeOAuthCallback)
	mux.Handle("DELETE /api/youtube/connection", app.requirePasswordChanged(http.HandlerFunc(app.disconnectYouTube)))
	mux.Handle("GET /api/youtube/uploads", app.requirePasswordChanged(http.HandlerFunc(app.youtubeUploads)))
	mux.Handle("POST /api/youtube/uploads", app.requirePasswordChanged(http.HandlerFunc(app.createYouTubeUpload)))
	mux.Handle("GET /api/youtube/uploads/{id}", app.requirePasswordChanged(http.HandlerFunc(app.youtubeUpload)))
	mux.Handle("POST /api/youtube/uploads/{id}/retry", app.requirePasswordChanged(http.HandlerFunc(app.retryYouTubeUpload)))
	mux.Handle("POST /api/youtube/uploads/{id}/cancel", app.requirePasswordChanged(http.HandlerFunc(app.cancelYouTubeUpload)))
	mux.Handle("POST /api/youtube/uploads/{id}/restart", app.requirePasswordChanged(http.HandlerFunc(app.restartYouTubeUpload)))
	mux.Handle("POST /api/youtube/metadata", app.requirePasswordChanged(http.HandlerFunc(app.generateYouTubeMetadata)))
	if security != nil {
		if err := app.migrateLegacyModalAccount(); err != nil {
			logger.Warn("legacy Modal account migration failed")
		}
	}
	return securityHeaders(mux)
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
		body, err := webui.Files.ReadFile(name)
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
