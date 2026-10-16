package app

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	clippingProtocolVersionV1       = "framevault.clipping.v1"
	clippingProtocolVersion         = "framevault.clipping.v2"
	defaultClippingRetentionDays    = 30
	maxClippingCallbackBodyBytes    = (4 << 20) + (32 << 10)
	clippingWorkerLeaseDuration     = 12 * time.Hour
	clippingCallbackCapabilityGrace = 24 * time.Hour
	clippingCapabilityMedia         = "media"
	clippingCapabilityCallback      = "callback"
	clippingCapabilityLease         = "lease"
)

type clippingSourceView struct {
	ID             string                     `json:"id"`
	Kind           ClippingSourceKind         `json:"kind"`
	Status         ClippingSourceStatus       `json:"status"`
	RightsAttested bool                       `json:"rights_attested"`
	OriginalName   string                     `json:"original_name"`
	MediaType      string                     `json:"media_type"`
	SizeBytes      int64                      `json:"size_bytes"`
	DurationMS     int64                      `json:"duration_ms"`
	SHA256         string                     `json:"sha256,omitempty"`
	Progress       int                        `json:"progress"`
	Error          string                     `json:"error,omitempty"`
	ErrorMessage   string                     `json:"error_message,omitempty"`
	Diagnostics    []ClippingSourceDiagnostic `json:"diagnostics,omitempty"`
	MediaURL       string                     `json:"media_url,omitempty"`
	CreatedAt      int64                      `json:"created_at"`
	UpdatedAt      int64                      `json:"updated_at"`
}

type clippingStageView struct {
	JobID          string              `json:"job_id"`
	Name           string              `json:"name"`
	Status         ClippingStageStatus `json:"status"`
	AttemptID      string              `json:"attempt_id,omitempty"`
	Attempt        int                 `json:"attempt"`
	LeaseExpiresAt int64               `json:"lease_expires_at,omitempty"`
	Error          string              `json:"error,omitempty"`
	CreatedAt      int64               `json:"created_at"`
	UpdatedAt      int64               `json:"updated_at"`
}

type clippingJobView struct {
	ID              string              `json:"id"`
	SourceID        string              `json:"source_id"`
	ContentType     string              `json:"content_type"`
	MinClipSeconds  int                 `json:"min_clip_seconds"`
	MaxClipSeconds  int                 `json:"max_clip_seconds"`
	CandidateLimit  int                 `json:"candidate_limit"`
	Status          ClippingJobStatus   `json:"status"`
	AnalysisDeleted bool                `json:"analysis_deleted,omitempty"`
	Progress        int                 `json:"progress"`
	Error           string              `json:"error,omitempty"`
	Stages          []clippingStageView `json:"stages"`
	Artifacts       []ClippingArtifact  `json:"artifacts,omitempty"`
	CreatedAt       int64               `json:"created_at"`
	UpdatedAt       int64               `json:"updated_at"`
}

func (a *dashboardApp) retentionDeadline(now time.Time) int64 {
	return now.Add(defaultClippingRetentionDays * 24 * time.Hour).Unix()
}

func clippingSourceSummary(source ClippingSource) clippingSourceView {
	progress := 0
	if source.Status == ClippingSourceReady {
		progress = 100
	}
	mediaURL := ""
	if source.Status == ClippingSourceReady {
		mediaURL = "/api/clipping/sources/" + source.ID + "/media"
	}
	return clippingSourceView{
		ID: source.ID, Kind: source.Kind, Status: source.Status, RightsAttested: source.RightsAttested, OriginalName: source.OriginalName,
		MediaType: source.MediaType, SizeBytes: source.SizeBytes,
		DurationMS: source.DurationMS, SHA256: source.SHA256,
		Progress: progress, Error: source.Failure, ErrorMessage: clippingSourceErrorMessage(source.Failure), Diagnostics: source.Diagnostics, MediaURL: mediaURL,
		CreatedAt: source.CreatedAt, UpdatedAt: source.UpdatedAt,
	}
}

func clippingSourceErrorMessage(code string) string {
	switch strings.TrimSpace(code) {
	case "invalid_media":
		return "This YouTube source is not a supported video with a valid duration and dimensions."
	case "unsafe_dns_answer":
		return "The YouTube link resolved to an unsafe destination. Check the link and try again."
	case "source_import_timed_out", "extractor_exit_failure":
		return "YouTube could not download this public video. Retry once, then choose another public video."
	case "source_import_canceled":
		return "The YouTube import was interrupted. Try importing it again."
	case "legacy_source_import_unsupported":
		return "This source came from a removed import workflow. Import a public YouTube video to continue."
	case "track_transfer_aborted":
		return "A YouTube media stream stopped before the source was complete. Retry the import."
	case "":
		return ""
	default:
		return "The YouTube import failed. Check the public link and retry."
	}
}

func clippingJobProgress(stages []ClippingStage, status ClippingJobStatus) int {
	if status == ClippingJobCompleted {
		return 100
	}
	if len(stages) == 0 {
		return 0
	}
	completed := 0
	for _, stage := range stages {
		if stage.Status == ClippingStageCompleted {
			completed++
		}
	}
	return completed * 100 / len(stages)
}

func clippingJobSummary(job ClippingJob, stages []ClippingStage, artifacts []ClippingArtifact) clippingJobView {
	stageViews := make([]clippingStageView, 0, len(stages))
	for _, stage := range stages {
		stageViews = append(stageViews, clippingStageView{
			JobID: stage.JobID, Name: stage.Name, Status: stage.Status, AttemptID: stage.AttemptID, Attempt: stage.Attempt,
			LeaseExpiresAt: stage.LeaseExpiresAt, Error: stage.Error, CreatedAt: stage.CreatedAt,
			UpdatedAt: stage.UpdatedAt,
		})
	}
	return clippingJobView{
		ID: job.ID, SourceID: job.SourceID, ContentType: job.ContentType,
		MinClipSeconds: job.MinClipSeconds, MaxClipSeconds: job.MaxClipSeconds, CandidateLimit: job.CandidateLimit, Status: job.Status,
		Progress: clippingJobProgress(stages, job.Status),
		Error:    job.Error, AnalysisDeleted: job.AnalysisDeleted, Stages: stageViews, Artifacts: artifacts, CreatedAt: job.CreatedAt, UpdatedAt: job.UpdatedAt,
	}
}

func (a *dashboardApp) clippingJobResponse(job ClippingJob, includeArtifacts bool) (clippingJobView, error) {
	stages, err := a.store.ClippingStages(job.ID)
	if err != nil {
		return clippingJobView{}, err
	}
	var artifacts []ClippingArtifact
	if includeArtifacts {
		artifacts, err = a.store.ClippingArtifacts(job.ID)
		if err != nil {
			return clippingJobView{}, err
		}
		for index := range artifacts {
			artifacts[index] = sanitizeClippingArtifactForPublic(artifacts[index])
		}
	}
	return clippingJobSummary(job, stages, artifacts), nil
}

func sanitizeClippingArtifactForPublic(artifact ClippingArtifact) ClippingArtifact {
	var payload map[string]json.RawMessage
	if json.Unmarshal([]byte(artifact.PayloadJSON), &payload) != nil {
		return artifact
	}
	for _, key := range []string{
		"cost_basis", "cost_estimate_micro_usd", "rate_micro_usd_per_second", "actual_cost_micro_usd",
		"actual_cost_status", "reserved_micro_usd", "estimated_micro_usd", "spent_micro_usd",
	} {
		delete(payload, key)
	}
	encoded, err := json.Marshal(payload)
	if err == nil {
		artifact.PayloadJSON = string(encoded)
	}
	return artifact
}

func (a *dashboardApp) clippingConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"worker_available":     false,
		"worker_state":         "unavailable",
		"max_source_bytes":     MaxClippingSourceBytes,
		"max_duration_ms":      MaxClippingSourceDurationMS,
		"allowed_import_kinds": []ClippingSourceKind{ClippingSourceYouTube},
	})
}

func (a *dashboardApp) clippingSources(w http.ResponseWriter, _ *http.Request) {
	sources, err := a.store.ClippingSources()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load clipping sources")
		return
	}
	views := make([]clippingSourceView, 0, len(sources))
	for _, source := range sources {
		if source.Status == ClippingSourceDeleted {
			continue
		}
		views = append(views, clippingSourceSummary(source))
	}
	writeJSON(w, http.StatusOK, map[string]any{"sources": views})
}

func (a *dashboardApp) clippingSource(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !safeID(id) {
		writeError(w, http.StatusBadRequest, "invalid clipping source id")
		return
	}
	source, err := a.store.ClippingSource(id)
	if err != nil || source.Status == ClippingSourceDeleted {
		writeError(w, http.StatusNotFound, "clipping source not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"source": clippingSourceSummary(source)})
}

func (a *dashboardApp) deleteClippingSource(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) {
		return
	}
	id := r.PathValue("id")
	if !safeID(id) {
		writeError(w, http.StatusBadRequest, "invalid clipping source id")
		return
	}
	jobIDs, cleanupPending, err := a.clippingAcquisition.DeleteSource(r.Context(), id)
	for _, jobID := range jobIDs {
		a.deleteClippingCapabilities(jobID)
		a.store.PublishClippingJob(jobID)
	}
	a.store.PublishClippingSource(id)
	if err != nil {
		writeError(w, clippingErrorStatus(err), safeClippingClientError(err, "could not delete clipping source"))
		return
	}
	if cleanupPending {
		writeJSON(w, http.StatusAccepted, map[string]any{"cleanup_pending": true})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *dashboardApp) createClippingImport(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) {
		return
	}
	if !isJSON(r.Header.Get("Content-Type")) {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	var input struct {
		URL            string `json:"url"`
		RightsAttested bool   `json:"rights_attested"`
	}
	if decodeJSONBody(w, r, &input) != nil {
		return
	}
	if !input.RightsAttested {
		writeError(w, http.StatusUnprocessableEntity, "confirm that you have permission to reuse and process this source")
		return
	}
	source, err := a.clippingAcquisition.CreatePublicImport(r.Context(), input.RightsAttested, input.URL, a.retentionDeadline(time.Now()))
	if err != nil {
		writeError(w, clippingErrorStatus(err), safeClippingClientError(err, "this source cannot be imported from its public link"))
		return
	}
	a.store.PublishClippingSource(source.ID)
	writeJSON(w, http.StatusAccepted, map[string]any{"source": clippingSourceSummary(source)})
}

func (a *dashboardApp) clippingSourceMedia(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !safeID(id) {
		writeError(w, http.StatusBadRequest, "invalid clipping source id")
		return
	}
	source, err := a.store.ClippingSource(id)
	if err != nil || source.Status != ClippingSourceReady {
		writeError(w, http.StatusNotFound, "clipping source media not found")
		return
	}
	a.serveClippingSource(w, r, source)
}

func (a *dashboardApp) clippingJobs(w http.ResponseWriter, _ *http.Request) {
	jobs, err := a.store.ClippingJobs()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load clipping jobs")
		return
	}
	views := make([]clippingJobView, 0, len(jobs))
	for _, job := range jobs {
		if job.AnalysisDeleted {
			continue
		}
		view, err := a.clippingJobResponse(job, false)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not load clipping jobs")
			return
		}
		views = append(views, view)
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": views})
}

func (a *dashboardApp) clippingJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !safeID(id) {
		writeError(w, http.StatusBadRequest, "invalid clipping job id")
		return
	}
	job, err := a.store.ClippingJob(id)
	if err != nil || job.AnalysisDeleted {
		writeError(w, http.StatusNotFound, "clipping job not found")
		return
	}
	view, err := a.clippingJobResponse(job, true)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load clipping job details")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"job": view})
}

func (a *dashboardApp) createClippingJob(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) {
		return
	}
	if !isJSON(r.Header.Get("Content-Type")) {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	var input struct {
		SourceID       string `json:"source_id"`
		ContentType    string `json:"content_type"`
		MinClipSeconds int    `json:"min_clip_seconds"`
		MaxClipSeconds int    `json:"max_clip_seconds"`
		CandidateLimit int    `json:"candidate_limit"`
	}
	if decodeJSONBody(w, r, &input) != nil {
		return
	}
	key, ok := clippingIdempotencyKey(w, r)
	if !ok {
		return
	}
	selection, selectionErr := normalizeClippingSelection(ClippingJobCreate{
		SourceID:    input.SourceID,
		ContentType: input.ContentType, MinClipSeconds: input.MinClipSeconds,
		MaxClipSeconds: input.MaxClipSeconds, CandidateLimit: input.CandidateLimit,
	})
	if selectionErr != nil {
		writeError(w, http.StatusBadRequest, selectionErr.Error())
		return
	}
	job, duplicate, err := a.store.CreateClippingJob(selection, key)
	if err != nil {
		writeError(w, clippingErrorStatus(err), safeClippingClientError(err, "could not queue clipping job"))
		return
	}
	view, err := a.clippingJobResponse(job, false)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "clipping job was saved but could not be reloaded")
		return
	}
	a.store.PublishClippingJob(job.ID)
	status := http.StatusCreated
	if duplicate {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"job": view, "duplicate": duplicate})
}

func (a *dashboardApp) clippingJobResults(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !safeID(id) {
		writeError(w, http.StatusBadRequest, "invalid clipping job id")
		return
	}
	job, err := a.store.ClippingJob(id)
	if err != nil || job.AnalysisDeleted {
		writeError(w, http.StatusNotFound, "clipping job not found")
		return
	}
	artifacts, err := a.store.ClippingArtifacts(job.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load clipping job results")
		return
	}
	for index := range artifacts {
		artifacts[index] = sanitizeClippingArtifactForPublic(artifacts[index])
	}
	writeJSON(w, http.StatusOK, map[string]any{"job_id": job.ID, "source_id": job.SourceID, "artifacts": artifacts})
}

func (a *dashboardApp) deleteClippingAnalysis(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) {
		return
	}
	id := r.PathValue("id")
	if !safeID(id) {
		writeError(w, http.StatusBadRequest, "invalid clipping job id")
		return
	}
	if err := a.store.DeleteClippingAnalysis(id); err != nil {
		writeError(w, clippingErrorStatus(err), safeClippingClientError(err, "could not delete clipping analysis"))
		return
	}
	job, err := a.store.ClippingJob(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "analysis was deleted but its job could not be reloaded")
		return
	}
	a.deleteClippingCapabilities(job.ID)
	a.store.PublishClippingJob(job.ID)
	w.WriteHeader(http.StatusNoContent)
}

func (a *dashboardApp) cancelClippingJob(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) {
		return
	}
	id := r.PathValue("id")
	if !safeID(id) {
		writeError(w, http.StatusBadRequest, "invalid clipping job id")
		return
	}
	if err := a.store.CancelClippingJob(id); err != nil {
		writeError(w, clippingErrorStatus(err), safeClippingClientError(err, "could not cancel clipping job"))
		return
	}
	job, err := a.store.ClippingJob(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "job was canceled but could not be reloaded")
		return
	}
	a.deleteClippingCapabilities(job.ID)
	a.store.PublishClippingJob(job.ID)
	writeJSON(w, http.StatusOK, map[string]any{"job": clippingJobSummary(job, nil, nil)})
}

func (a *dashboardApp) retryClippingJob(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) {
		return
	}
	if !isJSON(r.Header.Get("Content-Type")) {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	id := r.PathValue("id")
	if !safeID(id) {
		writeError(w, http.StatusBadRequest, "invalid clipping job id")
		return
	}
	var input struct {
		StageName string `json:"stage_name"`
	}
	if decodeJSONBody(w, r, &input) != nil {
		return
	}
	if input.StageName == "" {
		input.StageName = ClippingStageAnalysis
	}
	stage, err := a.store.RetryClippingStage(id, input.StageName)
	if err != nil {
		writeError(w, clippingErrorStatus(err), safeClippingClientError(err, "clipping stage cannot be retried"))
		return
	}
	job, err := a.store.ClippingJob(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "stage was queued but job could not be reloaded")
		return
	}
	_ = stage
	view, err := a.clippingJobResponse(job, false)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "stage was queued but job could not be reloaded")
		return
	}
	a.store.PublishClippingJob(id)
	writeJSON(w, http.StatusAccepted, map[string]any{"job": view})
}

func clippingIdempotencyKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(key) < 16 || len(key) > 128 || strings.ContainsAny(key, "\r\n\x00") {
		writeError(w, http.StatusBadRequest, "Idempotency-Key must contain 16 to 128 characters")
		return "", false
	}
	return key, true
}

func clippingErrorStatus(err error) int {
	switch {
	case errors.Is(err, sql.ErrNoRows), errors.Is(err, ErrClippingSourceNotFound), errors.Is(err, ErrClippingJobNotFound), errors.Is(err, ErrClippingStageNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrClippingIdempotencyConflict), errors.Is(err, ErrClippingInvalidState), errors.Is(err, ErrClippingStaleAttempt), errors.Is(err, ErrClippingJobTerminal):
		return http.StatusConflict
	case errors.Is(err, ErrYouTubeImportUnavailable), errors.Is(err, ErrClippingRightsNotAttested):
		return http.StatusUnprocessableEntity
	default:
		return http.StatusBadRequest
	}
}

func safeClippingClientError(err error, fallback string) string {
	message := strings.TrimSpace(err.Error())
	if message == "" || len(message) > 240 || strings.ContainsAny(message, "\r\n\x00") || strings.Contains(message, "http://") || strings.Contains(message, "https://") || strings.Contains(filepath.Clean(message), string(filepath.Separator)) {
		return fallback
	}
	return message
}

func safeClippingMediaType(mediaType string) string {
	mediaType, _, err := mime.ParseMediaType(mediaType)
	if err != nil {
		return "application/octet-stream"
	}
	switch strings.ToLower(mediaType) {
	case "video/mp4", "video/quicktime", "video/x-matroska", "video/webm", "video/x-msvideo", "video/mpeg":
		return strings.ToLower(mediaType)
	default:
		return "application/octet-stream"
	}
}

func (a *dashboardApp) serveClippingSource(w http.ResponseWriter, r *http.Request, source ClippingSource) {
	path, err := a.store.ClippingSourcePath(source.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, "clipping source media not found")
		return
	}
	a.store.clippingMu.Lock()
	file, err := os.Open(path)
	a.store.clippingMu.Unlock()
	if err != nil {
		writeError(w, http.StatusNotFound, "clipping source media not found")
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		writeError(w, http.StatusNotFound, "clipping source media not found")
		return
	}
	w.Header().Set("Content-Type", safeClippingMediaType(source.MediaType))
	w.Header().Set("Content-Disposition", "inline; filename=source-media")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, "source-media", time.Unix(source.UpdatedAt, 0), file)
}

func (a *dashboardApp) persistClippingCapability(jobID, attemptID, scope, token string) error {
	if a.security == nil || !safeID(jobID) || !safeID(attemptID) || token == "" {
		return errors.New("clipping capability cannot be persisted")
	}
	key := clippingCapabilitySettingKey(jobID, attemptID, scope)
	ciphertext, err := a.security.EncryptSetting(key, token)
	if err != nil {
		return err
	}
	return a.store.SetSetting(key, ciphertext)
}

func clippingCapabilitySettingKey(jobID, attemptID, scope string) string {
	return "clipping_cap_" + jobID + "_" + attemptID + "_" + scope
}

func (a *dashboardApp) persistClippingCapabilityExpiry(jobID, attemptID, scope string, expiry int64) error {
	key := clippingCapabilitySettingKey(jobID, attemptID, scope+"_expiry")
	ciphertext, err := a.security.EncryptSetting(key, strconv.FormatInt(expiry, 10))
	if err != nil {
		return err
	}
	return a.store.SetSetting(key, ciphertext)
}

func (a *dashboardApp) clippingCapabilityExpiry(jobID, attemptID, scope string) (int64, error) {
	if a.security == nil {
		return 0, errors.New("clipping security is unavailable")
	}
	key := clippingCapabilitySettingKey(jobID, attemptID, scope+"_expiry")
	ciphertext, err := a.store.Setting(key)
	if err != nil {
		return 0, err
	}
	plain, err := a.security.DecryptSetting(key, ciphertext)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(plain, 10, 64)
}

func (a *dashboardApp) clippingCapability(jobID, attemptID, scope string) (string, error) {
	if a.security == nil || !safeID(jobID) || !safeID(attemptID) {
		return "", errors.New("clipping capability unavailable")
	}
	key := clippingCapabilitySettingKey(jobID, attemptID, scope)
	ciphertext, err := a.store.Setting(key)
	if err != nil {
		return "", err
	}
	return a.security.DecryptSetting(key, ciphertext)
}

func newClippingCapability() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

// persistClippingDispatchCapabilities saves every bearer capability encrypted
// before an asynchronous worker dispatch can be attempted.
func (a *dashboardApp) persistClippingDispatchCapabilities(stage ClippingStage) (mediaToken, callbackToken string, err error) {
	return a.persistClippingDispatchCapabilitiesUntil(stage, stage.LeaseExpiresAt+int64(clippingCallbackCapabilityGrace.Seconds()))
}

func (a *dashboardApp) persistClippingDispatchCapabilitiesUntil(stage ClippingStage, callbackExpiresAt int64) (mediaToken, callbackToken string, err error) {
	if stage.JobID == "" || stage.AttemptID == "" || stage.LeaseToken == "" || stage.LeaseExpiresAt <= time.Now().Unix() {
		return "", "", errors.New("invalid clipping stage lease")
	}
	now := time.Now().Unix()
	if callbackExpiresAt <= now || callbackExpiresAt > stage.LeaseExpiresAt+int64(clippingCallbackCapabilityGrace.Seconds()) {
		return "", "", errors.New("invalid clipping callback expiry")
	}
	mediaToken, err = newClippingCapability()
	if err != nil {
		return "", "", err
	}
	callbackToken, err = newClippingCapability()
	if err != nil {
		return "", "", err
	}
	if err = a.store.SetClippingCallbackCapabilityExpiryIndex(stage.JobID, stage.AttemptID, callbackExpiresAt); err != nil {
		return "", "", errors.New("unable to persist clipping worker callback expiry index")
	}
	for scope, token := range map[string]string{clippingCapabilityMedia: mediaToken, clippingCapabilityCallback: callbackToken, clippingCapabilityLease: stage.LeaseToken} {
		if err = a.persistClippingCapability(stage.JobID, stage.AttemptID, scope, token); err != nil {
			a.deleteClippingCapabilitiesForAttempt(stage.JobID, stage.AttemptID)
			return "", "", errors.New("unable to persist clipping worker capabilities")
		}
	}
	if err = a.persistClippingCapabilityExpiry(stage.JobID, stage.AttemptID, clippingCapabilityMedia, stage.LeaseExpiresAt); err != nil {
		a.deleteClippingCapabilitiesForAttempt(stage.JobID, stage.AttemptID)
		return "", "", errors.New("unable to persist clipping worker capability expiry")
	}
	if err = a.persistClippingCapabilityExpiry(stage.JobID, stage.AttemptID, clippingCapabilityCallback, callbackExpiresAt); err != nil {
		a.deleteClippingCapabilitiesForAttempt(stage.JobID, stage.AttemptID)
		return "", "", errors.New("unable to persist clipping worker capability expiry")
	}
	return mediaToken, callbackToken, nil
}

func (a *dashboardApp) deleteClippingCapabilitiesForAttempt(jobID, attemptID string) {
	_ = a.store.DeleteClippingCapabilitiesForAttempt(jobID, attemptID)
}

func clippingCallbackExpirySettingIDs(key string) (string, string, bool) {
	const prefix = "clipping_cap_"
	const suffix = "_callback_expiry"
	if !strings.HasPrefix(key, prefix) || !strings.HasSuffix(key, suffix) {
		return "", "", false
	}
	body := strings.TrimSuffix(strings.TrimPrefix(key, prefix), suffix)
	separator := strings.Index(body, "_clipatt_")
	if separator <= 0 {
		return "", "", false
	}
	jobID := body[:separator]
	attemptID := "clipatt_" + body[separator+len("_clipatt_"):]
	if len(jobID) != len("clipjob_")+32 || len(attemptID) != len("clipatt_")+32 ||
		!safeID(jobID) || !strings.HasPrefix(jobID, "clipjob_") || !safeID(attemptID) || !strings.HasPrefix(attemptID, "clipatt_") {
		return "", "", false
	}
	return jobID, attemptID, true
}

// cleanupExpiredClippingCapabilities first rebuilds bounded missing index rows
// from legacy encrypted expiry settings, then atomically removes due settings.
// If a legacy value cannot be decrypted, the attempt lease plus the full
// callback grace period is used as a conservative upper bound.
func (a *dashboardApp) cleanupExpiredClippingCapabilities(now int64, limit int) (int, error) {
	if a == nil || a.store == nil {
		return 0, ErrClippingInvalidState
	}
	if now <= 0 {
		now = time.Now().Unix()
	}
	if limit <= 0 || limit > maxClippingCapabilityCleanupBatch {
		limit = maxClippingCapabilityCleanupBatch
	}
	legacy, err := a.store.LegacyClippingCallbackExpirySettings(limit)
	if err != nil {
		return 0, err
	}
	for _, setting := range legacy {
		jobID, attemptID, validKey := clippingCallbackExpirySettingIDs(setting.Key)
		if !validKey {
			return 0, errors.New("legacy clipping callback expiry key could not be validated")
		}
		expiry := int64(0)
		if a.security != nil {
			if plaintext, decryptErr := a.security.DecryptSetting(setting.Key, setting.Ciphertext); decryptErr == nil {
				expiry, _ = strconv.ParseInt(strings.TrimSpace(plaintext), 10, 64)
			}
		}
		if expiry <= 0 {
			expiry, err = a.store.conservativeClippingCallbackExpiry(jobID, attemptID)
			if err != nil || expiry <= 0 {
				return 0, errors.New("legacy clipping callback expiry could not be safely recovered")
			}
		}
		if err := a.store.SetClippingCallbackCapabilityExpiryIndex(jobID, attemptID, expiry); err != nil {
			return 0, err
		}
	}
	return a.store.CleanupExpiredClippingCapabilities(now, limit)
}

func (a *dashboardApp) revokeClippingMediaCapability(jobID, attemptID string) {
	_ = a.store.DeleteSetting(clippingCapabilitySettingKey(jobID, attemptID, clippingCapabilityMedia))
	_ = a.store.DeleteSetting(clippingCapabilitySettingKey(jobID, attemptID, clippingCapabilityMedia+"_expiry"))
}

func (a *dashboardApp) deleteClippingCapabilities(jobID string) {
	stages, err := a.store.ClippingStages(jobID)
	if err != nil {
		return
	}
	for _, stage := range stages {
		if stage.AttemptID != "" {
			a.revokeClippingMediaCapability(jobID, stage.AttemptID)
		}
	}
}

func (s *Store) PublishClippingSource(id string) {
	if s.events == nil || !s.events.hasSubscribers() {
		return
	}
	source, err := s.ClippingSource(id)
	if err != nil {
		return
	}
	view := clippingSourceSummary(source)
	fingerprint, err := snapshotFingerprint(view)
	if err == nil {
		s.events.publishSnapshot("clipping_source", id, fingerprint, map[string]any{"id": id, "source": view})
	}
}

func (s *Store) PublishClippingJob(id string) {
	if s.events == nil || !s.events.hasSubscribers() {
		return
	}
	job, err := s.ClippingJob(id)
	if err != nil {
		return
	}
	stages, err := s.ClippingStages(id)
	if err != nil {
		return
	}
	view := clippingJobSummary(job, stages, nil)
	fingerprint, err := snapshotFingerprint(view)
	if err == nil {
		s.events.publishSnapshot("clipping_job", id, fingerprint, map[string]any{"id": id, "job": view})
	}
}

type clippingCallbackArtifact struct {
	Type             string              `json:"type"`
	SchemaVersion    string              `json:"schema_version"`
	Version          int                 `json:"version"`
	SourceDurationMS int64               `json:"source_duration_ms"`
	TimeRanges       []ClippingTimeRange `json:"time_ranges"`
	Payload          json.RawMessage     `json:"payload"`
}

type clippingWorkerCallback struct {
	ProtocolVersion      string                   `json:"protocol_version"`
	JobID                string                   `json:"job_id"`
	Stage                string                   `json:"stage"`
	DispatchID           string                   `json:"dispatch_id"`
	AttemptID            string                   `json:"attempt_id"`
	Status               string                   `json:"status"`
	ActualCostMicroUSD   int64                    `json:"actual_cost_micro_usd"`
	CostEstimateMicroUSD int64                    `json:"cost_estimate_micro_usd,omitempty"`
	CostBasis            string                   `json:"cost_basis,omitempty"`
	Artifact             clippingCallbackArtifact `json:"artifact"`
	Error                string                   `json:"error,omitempty"`
	Retryable            bool                     `json:"retryable,omitempty"`
}

func (a *dashboardApp) clippingWorkerMedia(w http.ResponseWriter, r *http.Request) {
	jobID, attemptID := r.PathValue("jobID"), r.PathValue("attemptID")
	if !safeID(jobID) || !safeID(attemptID) || r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid worker media request")
		return
	}
	job, stage, source, ok := a.workerStage(jobID, attemptID)
	if !ok || source.Status != ClippingSourceReady || job.Status == ClippingJobCanceled || stage.AttemptID != attemptID || stage.LeaseExpiresAt <= time.Now().Unix() || stage.Status != ClippingStageRunning {
		a.revokeClippingMediaCapability(jobID, attemptID)
		writeError(w, http.StatusGone, "worker media capability expired")
		return
	}
	if !a.authorizeClippingBearer(w, r, jobID, attemptID, clippingCapabilityMedia) {
		return
	}
	if err := a.store.SetClippingSourceMediaLease(source.ID, stage.LeaseExpiresAt); err != nil {
		writeError(w, http.StatusGone, "source media is no longer available")
		return
	}
	a.serveClippingSource(w, r, source)
}

func (a *dashboardApp) workerStage(jobID, attemptID string) (ClippingJob, ClippingStage, ClippingSource, bool) {
	job, err := a.store.ClippingJob(jobID)
	if err != nil {
		return ClippingJob{}, ClippingStage{}, ClippingSource{}, false
	}
	stages, err := a.store.ClippingStages(jobID)
	if err != nil {
		return ClippingJob{}, ClippingStage{}, ClippingSource{}, false
	}
	var found ClippingStage
	for _, stage := range stages {
		if stage.Name == ClippingStageAnalysis {
			found = stage
			break
		}
	}
	if found.AttemptID == "" {
		return ClippingJob{}, ClippingStage{}, ClippingSource{}, false
	}
	source, err := a.store.ClippingSource(job.SourceID)
	if err != nil {
		return ClippingJob{}, ClippingStage{}, ClippingSource{}, false
	}
	return job, found, source, true
}

func (a *dashboardApp) authorizeClippingBearer(w http.ResponseWriter, r *http.Request, jobID, attemptID, scope string) bool {
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "capabilities must not be supplied in a URL")
		return false
	}
	parts := strings.SplitN(r.Header.Get("Authorization"), " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		writeError(w, http.StatusUnauthorized, "worker capability required")
		return false
	}
	expected, err := a.clippingCapability(jobID, attemptID, scope)
	if err != nil || subtle.ConstantTimeCompare([]byte(expected), []byte(parts[1])) != 1 {
		writeError(w, http.StatusUnauthorized, "worker capability is invalid")
		return false
	}
	return true
}

func clippingWorkerCallbackDigest(input clippingWorkerCallback) (string, error) {
	encoded, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func clippingWorkerCallbackBodyDigest(body []byte) string {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

func (a *dashboardApp) failInvalidCurrentClippingCallback(w http.ResponseWriter, jobID, attemptID, leaseToken, digest string, failureCodes ...string) {
	_, duplicate, err := a.store.FailClippingAnalysisCallback(jobID, ClippingStageAnalysis, attemptID, leaseToken, digest, failureCodes...)
	if err != nil {
		if errors.Is(err, ErrClippingStaleAttempt) || errors.Is(err, ErrClippingJobTerminal) {
			a.revokeClippingMediaCapability(jobID, attemptID)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeError(w, http.StatusInternalServerError, "worker callback failure could not be recorded")
		return
	}
	_ = duplicate
	a.store.PublishClippingJob(jobID)
	a.revokeClippingMediaCapability(jobID, attemptID)
	w.WriteHeader(http.StatusNoContent)
}

// These codes describe validation of Go-owned contracts, never content from
// the untrusted callback. The worker still receives 204 after a durable
// terminal rejection so it will not re-run successful inference.
func clippingAnalysisCallbackValidationCode(err error) string {
	if err == nil {
		return "invalid_analysis_payload"
	}
	switch {
	case strings.Contains(err.Error(), "source hash or duration"):
		return "source_identity_mismatch"
	case strings.Contains(err.Error(), "pipeline provenance"):
		return "invalid_pipeline_provenance"
	case strings.Contains(err.Error(), "coverage or availability"):
		return "invalid_media_coverage"
	default:
		return "invalid_analysis_payload"
	}
}

func (a *dashboardApp) clippingWorkerCallback(w http.ResponseWriter, r *http.Request) {
	jobID, attemptID := r.PathValue("jobID"), r.PathValue("attemptID")
	if !safeID(jobID) || !safeID(attemptID) || r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid worker callback request")
		return
	}
	if !a.authorizeClippingBearer(w, r, jobID, attemptID, clippingCapabilityCallback) {
		return
	}
	_, stage, _, ok := a.workerStage(jobID, attemptID)
	if !ok || stage.AttemptID != attemptID {
		writeError(w, http.StatusConflict, "worker callback attempt is stale")
		return
	}
	expiry, expiryErr := a.clippingCapabilityExpiry(jobID, attemptID, clippingCapabilityCallback)
	if expiryErr != nil {
		writeError(w, http.StatusGone, "worker callback capability expiry could not be verified")
		return
	}
	if expiry <= time.Now().Unix() {
		a.deleteClippingCapabilitiesForAttempt(jobID, attemptID)
		writeError(w, http.StatusGone, "worker callback capability expired")
		return
	}
	leaseToken, err := a.clippingCapability(jobID, attemptID, clippingCapabilityLease)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "worker lease capability is unavailable")
		return
	}
	if r.ContentLength > maxClippingCallbackBodyBytes {
		digest := clippingWorkerCallbackBodyDigest([]byte("oversize:" + strconv.FormatInt(r.ContentLength, 10)))
		a.failInvalidCurrentClippingCallback(w, jobID, attemptID, leaseToken, digest, "callback_payload_too_large")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxClippingCallbackBodyBytes)
	body, readErr := io.ReadAll(r.Body)
	if readErr != nil {
		digest := clippingWorkerCallbackBodyDigest(append(body, []byte("\x00read-error")...))
		a.failInvalidCurrentClippingCallback(w, jobID, attemptID, leaseToken, digest, "invalid_callback_body")
		return
	}
	bodyDigest := clippingWorkerCallbackBodyDigest(body)
	if !isJSON(r.Header.Get("Content-Type")) {
		a.failInvalidCurrentClippingCallback(w, jobID, attemptID, leaseToken, bodyDigest)
		return
	}
	var input clippingWorkerCallback
	if err := decodeClippingWorkerCallback(body, &input); err != nil {
		a.failInvalidCurrentClippingCallback(w, jobID, attemptID, leaseToken, bodyDigest, "invalid_callback_body")
		return
	}
	// Capabilities are bound to one accepted attempt. A stale or misrouted body
	// must not terminate that attempt, and legacy protocol callbacks remain
	// rejected without changing their migrated job state.
	if input.ProtocolVersion != clippingProtocolVersion {
		writeError(w, http.StatusBadRequest, "worker callback protocol is unsupported")
		return
	}
	if input.JobID != jobID || input.Stage != ClippingStageAnalysis || input.AttemptID != attemptID {
		writeError(w, http.StatusBadRequest, "worker callback attempt identity does not match")
		return
	}
	if input.DispatchID == "" || input.DispatchID != stage.IdempotencyKey {
		writeError(w, http.StatusConflict, "worker callback dispatch identity is stale")
		return
	}
	if input.ActualCostMicroUSD != 0 {
		a.failInvalidCurrentClippingCallback(w, jobID, attemptID, leaseToken, bodyDigest)
		return
	}
	var source ClippingSource
	_, stage, source, ok = a.workerStage(jobID, attemptID)
	if !ok {
		writeError(w, http.StatusConflict, "worker callback attempt is stale")
		return
	}
	digest, err := clippingWorkerCallbackDigest(input)
	if err != nil {
		a.failInvalidCurrentClippingCallback(w, jobID, attemptID, leaseToken, bodyDigest)
		return
	}
	switch input.Status {
	case "completed":
		payload := strings.TrimSpace(string(input.Artifact.Payload))
		if input.CostEstimateMicroUSD <= 0 || input.CostBasis != clippingWorkerProtocolCostBasis ||
			input.Artifact.Type != clippingAnalysisArtifactType || input.Artifact.SchemaVersion != clippingAnalysisSchemaVersion || input.Artifact.Version != 1 || len(input.Artifact.TimeRanges) != 0 {
			a.failInvalidCurrentClippingCallback(w, jobID, attemptID, leaseToken, digest, "invalid_artifact_metadata")
			return
		}
		core, coreErr := decodeClippingAnalysisCore(payload, source)
		if coreErr != nil {
			a.failInvalidCurrentClippingCallback(w, jobID, attemptID, leaseToken, digest, clippingAnalysisCallbackValidationCode(coreErr))
			return
		}
		var protocol struct {
			Basis          string `json:"cost_basis"`
			Rate           int64  `json:"rate_micro_usd_per_second"`
			ComputeSeconds int64  `json:"compute_seconds"`
			Estimate       int64  `json:"cost_estimate_micro_usd"`
		}
		if err := json.Unmarshal(input.Artifact.Payload, &protocol); err != nil || protocol.Basis != clippingWorkerProtocolCostBasis ||
			protocol.Rate != clippingWorkerProtocolCompatibilityRate || protocol.ComputeSeconds <= 0 ||
			protocol.Estimate != input.CostEstimateMicroUSD || protocol.Estimate != protocol.ComputeSeconds {
			a.failInvalidCurrentClippingCallback(w, jobID, attemptID, leaseToken, digest, "invalid_cost_metadata")
			return
		}
		callbackArtifact := ClippingArtifact{
			JobID: jobID, Type: input.Artifact.Type, SchemaVersion: input.Artifact.SchemaVersion,
			Version: input.Artifact.Version, SourceDurationMS: input.Artifact.SourceDurationMS,
			TimeRanges: input.Artifact.TimeRanges, PayloadJSON: payload,
		}
		_, duplicate, published, err := a.store.CompleteClippingAnalysisStage(jobID, stage.Name, attemptID, leaseToken, digest, callbackArtifact, core, protocol.ComputeSeconds)
		if err != nil {
			if errors.Is(err, ErrClippingStaleAttempt) || errors.Is(err, ErrClippingJobTerminal) {
				a.revokeClippingMediaCapability(jobID, attemptID)
				w.WriteHeader(http.StatusNoContent)
				return
			}
			a.failInvalidCurrentClippingCallback(w, jobID, attemptID, leaseToken, digest, "artifact_persistence_failed")
			return
		}
		if published && !duplicate {
			a.store.PublishClippingJob(jobID)
		}
		a.revokeClippingMediaCapability(jobID, attemptID)
		w.WriteHeader(http.StatusNoContent)
	case "failed":
		if input.CostEstimateMicroUSD != 0 || input.CostBasis != "" {
			a.failInvalidCurrentClippingCallback(w, jobID, attemptID, leaseToken, digest)
			return
		}
		_, duplicate, err := a.store.FailClippingAnalysisCallback(jobID, stage.Name, attemptID, leaseToken, digest)
		if err != nil {
			if errors.Is(err, ErrClippingStaleAttempt) || errors.Is(err, ErrClippingJobTerminal) {
				a.revokeClippingMediaCapability(jobID, attemptID)
				w.WriteHeader(http.StatusNoContent)
				return
			}
			writeError(w, clippingErrorStatus(err), safeClippingClientError(err, "worker callback could not be applied"))
			return
		}
		if !duplicate {
			a.store.PublishClippingJob(jobID)
		}
		a.revokeClippingMediaCapability(jobID, attemptID)
		w.WriteHeader(http.StatusNoContent)
	default:
		a.failInvalidCurrentClippingCallback(w, jobID, attemptID, leaseToken, digest)
	}
}

func decodeClippingWorkerCallback(body []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("worker callback contains trailing JSON")
	}
	return nil
}
