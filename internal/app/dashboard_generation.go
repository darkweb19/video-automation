package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

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

type generationSubmission struct {
	Prompt         string `json:"prompt"`
	Model          string `json:"model"`
	Duration       int    `json:"duration,omitempty"`
	Resolution     string `json:"resolution,omitempty"`
	AspectRatio    string `json:"aspect_ratio,omitempty"`
	GenerateAudio  *bool  `json:"generate_audio,omitempty"`
	ModalAccountID string `json:"modal_account_id,omitempty"`
}

func (s generationSubmission) providerRequest() GenerateRequest {
	return GenerateRequest{Prompt: s.Prompt, Model: s.Model, Duration: s.Duration, Resolution: s.Resolution, AspectRatio: s.AspectRatio, GenerateAudio: s.GenerateAudio}
}

func (a *dashboardApp) selectedSubmissionProvider(modalAccountID string) (VideoProviderID, error) {
	providerID, err := a.selectedVideoProvider()
	if err != nil {
		return "", err
	}
	if providerID == VideoProviderModal && !safeID(modalAccountID) {
		return "", errors.New("select a Modal account for this generation")
	}
	if providerID != VideoProviderModal && modalAccountID != "" {
		return "", errors.New("a Modal account can only be used with the Modal provider")
	}
	return providerID, nil
}

func (a *dashboardApp) generate(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) {
		return
	}
	if !isJSON(r.Header.Get("Content-Type")) {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	var input generationSubmission
	if decodeJSONBody(w, r, &input) != nil {
		return
	}
	input.ModalAccountID = strings.TrimSpace(input.ModalAccountID)
	request := input.providerRequest()
	request.Prompt, request.Model = strings.TrimSpace(request.Prompt), strings.TrimSpace(request.Model)
	if request.Prompt == "" || len([]rune(request.Prompt)) > MaxPromptLength {
		writeError(w, http.StatusBadRequest, "a valid prompt is required")
		return
	}
	providerID, err := a.selectedSubmissionProvider(input.ModalAccountID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	provider, providerID, providerConfigID, err := a.videoProviderSnapshotForAccount(providerID, input.ModalAccountID)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	persisted := false
	defer func() {
		if !persisted {
			_ = a.store.DeleteProviderConfigIfUnused(providerConfigID)
		}
	}()
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
	if _, ok := provider.(CallbackVideoProvider); ok {
		job, prepareErr := a.prepareCallbackJob(request, providerConfigID)
		if prepareErr != nil {
			writeError(w, http.StatusUnprocessableEntity, callbackConfigurationError(prepareErr))
			return
		}
		if err := a.store.insertCallbackGeneration(job, estimateCost(request.Duration, model.PricePerSecond)); err != nil {
			writeError(w, http.StatusInternalServerError, "unable to save callback generation")
			return
		}
		persisted = true
		record, err := a.store.Generation(job.ID)
		if err != nil {
			a.logger.Error("reload saved callback generation failed", "generation_id", job.ID)
			writeError(w, http.StatusInternalServerError, "unable to load saved generation")
			return
		}
		a.store.PublishGeneration(job.ID)
		writeJSON(w, http.StatusAccepted, record)
		return
	}
	callbackToken := ""
	callbackRegistered := false
	if providerID == VideoProviderModal {
		callbackURL, callbackErr := a.modalCallbackURL()
		if callbackErr != nil {
			writeError(w, http.StatusUnprocessableEntity, callbackErr.Error())
			return
		}
		callbackToken, err = newModalCallbackToken()
		if err == nil {
			err = a.store.RegisterModalCallbackToken(callbackToken, "", 0)
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "unable to prepare secure Modal callback")
			return
		}
		callbackRegistered = true
		request.ModalCallbackURL = callbackURL
		request.ModalCallbackToken = callbackToken
		defer func() {
			if callbackRegistered {
				a.store.DeleteModalCallbackToken(callbackToken)
			}
		}()
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
	initialProgress := 0
	if generation.Progress != nil {
		initialProgress = *generation.Progress
	}
	record := GenerationRecord{
		ID:               generation.ID,
		VideoProvider:    string(providerID),
		ProviderConfigID: providerConfigID,
		Prompt:           request.Prompt,
		Model:            request.Model,
		Duration:         request.Duration,
		AspectRatio:      request.AspectRatio,
		Status:           generation.Status,
		Progress:         initialProgress,
		CostUSD:          generation.CostUSD,
		EstimatedCostUSD: estimateCost(request.Duration, model.PricePerSecond),
	}
	if generation.Status == "failed" {
		record.Error = sanitizeProviderFailure(generation.Error)
		if record.Error == "" {
			record.Error = "Video generation failed"
		}
	}
	if providerID == VideoProviderModal {
		err = a.store.InsertGenerationWithModalCallback(record, callbackToken)
		if err == nil {
			callbackRegistered = false // token is now bound to the durable record.
		}
	} else {
		err = a.store.InsertGeneration(record)
	}
	if err != nil {
		a.logger.Error("save generation failed", "generation_id", generation.ID)
		writeError(w, http.StatusInternalServerError, "unable to save generation")
		return
	}
	persisted = true
	record, err = a.store.Generation(record.ID)
	if err != nil {
		a.logger.Error("reload submitted generation failed", "generation_id", generation.ID)
		writeError(w, http.StatusInternalServerError, "unable to load saved generation")
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
	if record.InVault {
		writeError(w, http.StatusNotFound, "generation not found")
		return
	}
	writeJSON(w, http.StatusOK, record)
}

// modalCompletionCallback is deliberately outside browser authentication.
// Each callback has an opaque, per-job capability token registered before
// Modal is asked to create the job. It accepts terminal state only.
func (a *dashboardApp) modalCompletionCallback(w http.ResponseWriter, r *http.Request) {
	if !isJSON(r.Header.Get("Content-Type")) {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	tokens := r.Header.Values("X-Modal-Callback-Token")
	if len(tokens) != 1 || !validModalCallbackToken(tokens[0]) {
		writeError(w, http.StatusUnauthorized, "invalid callback authentication")
		return
	}
	var callback modalCompletionCallback
	if decodeJSONBody(w, r, &callback) != nil {
		return
	}
	callback.ID = strings.TrimSpace(callback.ID)
	callback.Status = strings.ToLower(strings.TrimSpace(callback.Status))
	callback.Model = strings.TrimSpace(callback.Model)
	callback.CostUSD = strings.TrimSpace(callback.CostUSD)
	if !safeID(callback.ID) || (callback.Status != "completed" && callback.Status != "failed") || len(callback.Model) > 300 || !validModalCallbackCost(callback.CostUSD) {
		writeError(w, http.StatusBadRequest, "a terminal callback id and status are required")
		return
	}

	outcome, err := a.store.ApplyModalCompletionCallback(callback, tokens[0])
	switch {
	case errors.Is(err, ErrModalCallbackPending):
		// Modal retries this short-lived race after the server has committed the
		// provider response and token binding.
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusServiceUnavailable, "callback target is not ready")
		return
	case errors.Is(err, ErrModalCallbackUnauthorized):
		writeError(w, http.StatusUnauthorized, "invalid callback authentication")
		return
	case errors.Is(err, ErrModalCallbackInvalidTarget):
		writeError(w, http.StatusBadRequest, "invalid callback target")
		return
	case errors.Is(err, ErrModalCallbackUnavailable):
		// A valid callback for a deleted job is intentionally terminal from
		// Modal's point of view. Do not reveal whether an ID ever existed.
		w.WriteHeader(http.StatusNoContent)
		return
	case err != nil:
		a.logger.Error("Modal completion callback persistence failed")
		w.Header().Set("Retry-After", "2")
		writeError(w, http.StatusServiceUnavailable, "callback persistence is temporarily unavailable")
		return
	}
	if outcome.GenerationID != "" {
		a.store.PublishGeneration(outcome.GenerationID)
	}
	if outcome.ProjectID != "" {
		a.store.PublishProject(outcome.ProjectID)
	}
	w.WriteHeader(http.StatusNoContent)
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

// randomPrompt creates text only. The browser never receives the OpenRouter
// credential or any provider metadata, and the result is not persisted until
// the user explicitly submits it as a generation or project.
func (a *dashboardApp) randomPrompt(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) {
		return
	}
	if !isJSON(r.Header.Get("Content-Type")) {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	var input RandomPromptRequest
	if decodeJSONBody(w, r, &input) != nil {
		return
	}
	input.Category = strings.TrimSpace(input.Category)
	input.Mode = RandomPromptMode(strings.TrimSpace(string(input.Mode)))
	if !validRandomPromptCategory(input.Category) || !validRandomPromptMode(input.Mode) {
		writeError(w, http.StatusBadRequest, "a supported category and mode are required")
		return
	}
	provider, err := a.openRouterTextProvider()
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "OpenRouter text generation is not configured")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), randomPromptTimeout)
	defer cancel()
	prompt, err := provider.GenerateRandomPrompt(ctx, input)
	if err != nil {
		a.logger.Warn("random prompt generation failed")
		status := http.StatusBadGateway
		var upstream *upstreamError
		if errors.As(err, &upstream) && upstream.StatusCode == http.StatusTooManyRequests {
			status = http.StatusTooManyRequests
			if delay := time.Until(upstream.RetryAt); delay > 0 {
				w.Header().Set("Retry-After", strconv.FormatInt(int64((delay+time.Second-1)/time.Second), 10))
			}
		} else if errors.Is(err, context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
		}
		writeError(w, status, safeRandomPromptFailure(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"prompt": prompt})
}

func safeRandomPromptFailure(err error) string {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "OpenRouter prompt generation timed out. Try again shortly."
	}
	var upstream *upstreamError
	if errors.As(err, &upstream) {
		if strings.EqualFold(upstream.ErrorType, "content_policy_violation") || strings.EqualFold(upstream.ErrorType, "refusal") {
			return "The prompt model declined this request. Try another category."
		}
		switch upstream.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return "OpenRouter rejected the saved API key. Update it in Settings."
		case http.StatusPaymentRequired:
			return "OpenRouter rejected the request because of an account credit or key limit. Review your OpenRouter account limits."
		case http.StatusTooManyRequests:
			if upstream.RateLimitScope == "platform" {
				if !upstream.RetryAt.IsZero() {
					return "OpenRouter's account limit is reached. Try again after " + upstream.RetryAt.UTC().Format("2006-01-02 15:04:05 UTC") + "."
				}
				return "OpenRouter's account limit is reached. Wait for the quota to reset, or review your OpenRouter account limits."
			}
			return "OpenRouter's prompt model is rate-limited right now. Try again shortly."
		default:
			return fmt.Sprintf("OpenRouter prompt generation failed with HTTP %d. Try again.", upstream.StatusCode)
		}
	}
	return "The prompt model did not return a complete, valid prompt. Try again shortly."
}
