package main

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

//go:embed static/index.html static/app.js static/styles.css static/model-picker.css
var staticFiles embed.FS

type handler struct {
	provider VideoProvider
	logger   *slog.Logger
}

// NewHandler returns the complete local HTTP application.
func NewHandler(provider VideoProvider, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	h := &handler{provider: provider, logger: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", h.index)
	mux.HandleFunc("GET /static/app.js", h.static("static/app.js", "application/javascript; charset=utf-8"))
	mux.HandleFunc("GET /static/styles.css", h.static("static/styles.css", "text/css; charset=utf-8"))
	mux.HandleFunc("GET /static/model-picker.css", h.static("static/model-picker.css", "text/css; charset=utf-8"))
	mux.HandleFunc("GET /health", h.health)
	mux.HandleFunc("GET /models", h.models)
	mux.HandleFunc("POST /generate", h.generate)
	mux.HandleFunc("GET /status", h.status)
	mux.HandleFunc("GET /video", h.video)
	return mux
}

func (h *handler) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	h.static("static/index.html", "text/html; charset=utf-8")(w, r)
}

func (h *handler) static(name, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		body, err := staticFiles.ReadFile(name)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "static asset unavailable")
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}
}

func (h *handler) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *handler) models(w http.ResponseWriter, r *http.Request) {
	models, err := h.provider.ListVideoModels(r.Context())
	if err != nil {
		h.logger.Error("models request failed")
		writeError(w, http.StatusBadGateway, "unable to load models")
		return
	}
	h.logger.Info("models loaded", "count", len(models))
	if models == nil {
		models = []VideoModel{}
	}
	writeJSON(w, http.StatusOK, struct {
		Models          []VideoModel `json:"models"`
		MaxPromptLength int          `json:"max_prompt_length"`
	}{Models: models, MaxPromptLength: MaxPromptLength})
}

func (h *handler) generate(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross-origin requests are not allowed")
		return
	}
	if !isJSON(r.Header.Get("Content-Type")) {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}

	var request GenerateRequest
	if err := decodeJSONBody(w, r, &request); err != nil {
		return
	}
	request.Prompt = strings.TrimSpace(request.Prompt)
	request.Model = strings.TrimSpace(request.Model)
	if request.Prompt == "" {
		writeError(w, http.StatusBadRequest, "prompt is required")
		return
	}
	if utf8.RuneCountInString(request.Prompt) > MaxPromptLength {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("prompt must be at most %d characters", MaxPromptLength))
		return
	}
	if request.Model == "" {
		writeError(w, http.StatusBadRequest, "model is required")
		return
	}

	models, err := h.provider.ListVideoModels(r.Context())
	if err != nil {
		h.logger.Error("model validation failed")
		writeError(w, http.StatusBadGateway, "unable to validate model")
		return
	}
	model, ok := findModel(models, request.Model)
	if !ok {
		writeError(w, http.StatusBadRequest, "selected model is not available for video generation")
		return
	}
	if err := validateOptions(request, model); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	generation, err := h.provider.GenerateVideo(r.Context(), request)
	if err != nil {
		h.logger.Error("generation submission failed", "model", request.Model)
		writeError(w, http.StatusBadGateway, "OpenRouter rejected the generation request")
		return
	}
	if generation == nil || generation.ID == "" {
		h.logger.Error("generation submission returned no id", "model", request.Model)
		writeError(w, http.StatusBadGateway, "OpenRouter returned an invalid generation response")
		return
	}
	if generation.Status == "" {
		generation.Status = "queued"
	}
	if generation.Model == "" {
		generation.Model = request.Model
	}
	h.logger.Info("generation submitted", "generation_id", generation.ID, "model", request.Model)
	writeJSON(w, http.StatusAccepted, generation)
}

func (h *handler) status(w http.ResponseWriter, r *http.Request) {
	id, ok := oneSafeID(w, r)
	if !ok {
		return
	}
	generation, err := h.provider.GetGeneration(r.Context(), id)
	if err != nil {
		h.logger.Error("generation status failed", "generation_id", id)
		writeError(w, http.StatusBadGateway, "unable to retrieve generation status")
		return
	}
	if generation == nil {
		writeError(w, http.StatusBadGateway, "OpenRouter returned an invalid generation response")
		return
	}
	if generation.ID == "" {
		generation.ID = id
	}
	writeJSON(w, http.StatusOK, generation)
}

func (h *handler) video(w http.ResponseWriter, r *http.Request) {
	id, ok := oneSafeID(w, r)
	if !ok {
		return
	}
	contentProvider, ok := h.provider.(VideoContentProvider)
	if !ok {
		writeError(w, http.StatusNotImplemented, "video proxy is unavailable")
		return
	}
	response, err := contentProvider.GetVideoContent(r.Context(), id, r.Header.Get("Range"))
	if err != nil {
		h.logger.Error("video content request failed", "generation_id", id)
		writeError(w, http.StatusBadGateway, "unable to retrieve video")
		return
	}
	defer response.Body.Close()
	// Streaming duration depends on the generated file and the browser's
	// connection. Clear the server-wide write deadline for this response only.
	if err := http.NewResponseController(w).SetWriteDeadline(time.Time{}); err != nil {
		h.logger.Debug("unable to clear video write deadline")
	}
	for _, header := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "Cache-Control"} {
		if value := response.Header.Get(header); value != "" {
			w.Header().Set(header, value)
		}
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, response.Body)
}

func validateOptions(request GenerateRequest, model VideoModel) error {
	if request.Duration < 0 {
		return errors.New("duration must be positive")
	}
	if request.Duration > 0 {
		if len(model.Durations) == 0 {
			return errors.New("the selected model does not advertise supported durations")
		}
		if !containsInt(model.Durations, request.Duration) {
			return errors.New("duration is not supported by the selected model")
		}
	}
	if request.AspectRatio != "" {
		if len(model.AspectRatios) == 0 {
			return errors.New("the selected model does not advertise supported aspect ratios")
		}
		if !containsString(model.AspectRatios, request.AspectRatio) {
			return errors.New("aspect_ratio is not supported by the selected model")
		}
	}
	return nil
}

func findModel(models []VideoModel, id string) (VideoModel, bool) {
	for _, model := range models {
		if model.ID == id {
			return model, true
		}
	}
	return VideoModel{}, false
}

func containsInt(values []int, target int) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func decodeJSONBody(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, MaxJSONBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body is too large")
		} else {
			writeError(w, http.StatusBadRequest, "invalid JSON request body")
		}
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body is too large")
		} else {
			writeError(w, http.StatusBadRequest, "request body must contain a single JSON object")
		}
		return errors.New("request body contains trailing JSON")
	}
	return nil
}

func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host == r.Host
}

func isJSON(contentType string) bool {
	mediaType := strings.TrimSpace(strings.Split(contentType, ";")[0])
	return strings.EqualFold(mediaType, "application/json")
}

func oneSafeID(w http.ResponseWriter, r *http.Request) (string, bool) {
	values, present := r.URL.Query()["id"]
	if !present || len(values) != 1 || !safeID(values[0]) {
		writeError(w, http.StatusBadRequest, "a single valid generation id is required")
		return "", false
	}
	return values[0], true
}

func safeID(value string) bool {
	if len(value) == 0 || len(value) > 200 {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '_' || character == '-') {
			return false
		}
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
