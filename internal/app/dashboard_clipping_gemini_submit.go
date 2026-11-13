package app

import (
	"errors"
	"net/http"
	"time"
)

// createClippingGeminiJob queues durable intent only. The processor owns any
// later provider work; neither source acquisition nor an ADC check occurs here.
func (a *dashboardApp) createClippingGeminiJob(w http.ResponseWriter, r *http.Request) {
	if !clippingGeminiMutationAllowed(w, r) {
		return
	}
	if !isJSON(r.Header.Get("Content-Type")) {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	input, ok := decodeClippingGeminiJobInput(w, r)
	if !ok {
		return
	}
	key, ok := clippingIdempotencyKey(w, r)
	if !ok {
		return
	}
	runtime, err := currentClippingGeminiRuntimeConfig()
	if err != nil || !runtime.Enabled {
		writeError(w, http.StatusServiceUnavailable, "Gemini analysis is not configured or enabled")
		return
	}
	create := ClippingGeminiJobCreate{
		SourceID: input.SourceID, YouTubeURL: input.YouTubeURL, RightsAttested: input.RightsAttested,
		Options: input.Options, Config: runtime.Routing,
	}
	if input.YouTubeURL != "" {
		create.RetainUntil = a.retentionDeadline(time.Now())
	}
	job, duplicate, err := a.store.CreateClippingGeminiJob(a.security, create, key)
	if err != nil {
		status, message := clippingGeminiCreateError(err)
		writeError(w, status, message)
		return
	}
	// Publishing replays is safe: the event hub deduplicates identical
	// snapshots. It also lets a replay repair a previous post-commit read
	// failure that prevented a client from seeing the saved source or job.
	if input.YouTubeURL != "" {
		a.store.PublishClippingSource(job.SourceID)
	}
	a.store.PublishClippingJob(job.ID)
	view, err := a.clippingJobResponse(job, false)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "clipping job was saved but could not be reloaded")
		return
	}
	status := http.StatusCreated
	if duplicate {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"job": view, "duplicate": duplicate})
}

// These fixed messages never include source URLs, routing values, SDK text, or
// database details, including for malformed caller input.
func clippingGeminiCreateError(err error) (int, string) {
	switch {
	case errors.Is(err, ErrClippingGeminiInvalidRequest):
		return http.StatusBadRequest, "A supported public YouTube video or existing source is required"
	case errors.Is(err, ErrClippingGeminiInvalidOptions):
		return http.StatusBadRequest, "Invalid Gemini analysis options"
	case errors.Is(err, ErrClippingRightsNotAttested):
		return http.StatusUnprocessableEntity, "Permission to analyze this source must be confirmed"
	case errors.Is(err, ErrClippingSourceNotFound):
		return http.StatusNotFound, "clipping source not found"
	case errors.Is(err, ErrClippingIdempotencyConflict):
		return http.StatusConflict, "Idempotency-Key was already used for a different request"
	case errors.Is(err, ErrClippingInvalidState):
		return http.StatusConflict, "clipping source is not available for analysis"
	case errors.Is(err, ErrClippingGeminiPersistenceUnavailable), errors.Is(err, ErrClippingGeminiInvalidConfig):
		return http.StatusServiceUnavailable, "Gemini analysis is not configured or enabled"
	default:
		return http.StatusInternalServerError, "could not queue clipping job"
	}
}
