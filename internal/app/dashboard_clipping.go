package app

import (
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
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
	clippingRetentionSetting        = "clipping_retention_days"
	clippingProtocolVersionV1       = "framevault.clipping.v1"
	clippingProtocolVersion         = "framevault.clipping.v2"
	defaultClippingRetentionDays    = 30
	minClippingRetentionDays        = 1
	maxClippingRetentionDays        = 365
	maxClippingUploadChunkBytes     = 16 << 20
	maxClippingCallbackBodyBytes    = (4 << 20) + (32 << 10)
	clippingWorkerLeaseDuration     = 12 * time.Hour
	clippingCallbackAccountingGrace = 24 * time.Hour
	clippingCapabilityMedia         = "media"
	clippingCapabilityCallback      = "callback"
	clippingCapabilityLease         = "lease"
)

type clippingSourceView struct {
	ID                string               `json:"id"`
	Kind              ClippingSourceKind   `json:"kind"`
	Status            ClippingSourceStatus `json:"status"`
	RightsAttested    bool                 `json:"rights_attested"`
	OriginalName      string               `json:"original_name"`
	MediaType         string               `json:"media_type"`
	DeclaredSizeBytes int64                `json:"declared_size_bytes"`
	SizeBytes         int64                `json:"size_bytes"`
	UploadOffset      int64                `json:"upload_offset"`
	DurationMS        int64                `json:"duration_ms"`
	SHA256            string               `json:"sha256,omitempty"`
	RetainUntil       int64                `json:"retain_until"`
	Progress          int                  `json:"progress"`
	Error             string               `json:"error,omitempty"`
	MediaURL          string               `json:"media_url,omitempty"`
	CreatedAt         int64                `json:"created_at"`
	UpdatedAt         int64                `json:"updated_at"`
}

type clippingStageView struct {
	JobID             string              `json:"job_id"`
	Name              string              `json:"name"`
	Status            ClippingStageStatus `json:"status"`
	AttemptID         string              `json:"attempt_id,omitempty"`
	Attempt           int                 `json:"attempt"`
	LeaseExpiresAt    int64               `json:"lease_expires_at,omitempty"`
	ReservedMicroUSD  int64               `json:"reserved_micro_usd"`
	ActualMicroUSD    int64               `json:"actual_micro_usd"`
	EstimatedMicroUSD int64               `json:"estimated_micro_usd,omitempty"`
	CostReconciled    bool                `json:"cost_reconciled"`
	Error             string              `json:"error,omitempty"`
	CreatedAt         int64               `json:"created_at"`
	UpdatedAt         int64               `json:"updated_at"`
}

type clippingJobView struct {
	ID               string              `json:"id"`
	BatchID          string              `json:"batch_id"`
	SourceID         string              `json:"source_id"`
	ContentType      string              `json:"content_type"`
	MinClipSeconds   int                 `json:"min_clip_seconds"`
	MaxClipSeconds   int                 `json:"max_clip_seconds"`
	CandidateLimit   int                 `json:"candidate_limit"`
	Status           ClippingJobStatus   `json:"status"`
	BudgetMicroUSD   int64               `json:"budget_micro_usd"`
	ReservedMicroUSD int64               `json:"reserved_micro_usd"`
	SpentMicroUSD    int64               `json:"spent_micro_usd"`
	Progress         int                 `json:"progress"`
	Error            string              `json:"error,omitempty"`
	Stages           []clippingStageView `json:"stages"`
	Artifacts        []ClippingArtifact  `json:"artifacts,omitempty"`
	CreatedAt        int64               `json:"created_at"`
	UpdatedAt        int64               `json:"updated_at"`
}

type clippingBatchView struct {
	ID               string            `json:"id"`
	Status           ClippingJobStatus `json:"status"`
	BudgetMicroUSD   int64             `json:"budget_micro_usd"`
	ReservedMicroUSD int64             `json:"reserved_micro_usd"`
	SpentMicroUSD    int64             `json:"spent_micro_usd"`
	Progress         int               `json:"progress"`
	SourceIDs        []string          `json:"source_ids"`
	JobIDs           []string          `json:"job_ids"`
	CreatedAt        int64             `json:"created_at"`
	UpdatedAt        int64             `json:"updated_at"`
}

func (a *dashboardApp) clippingRetention() int {
	value, err := a.store.Setting(clippingRetentionSetting)
	if err == nil {
		if days, parseErr := strconv.Atoi(value); parseErr == nil && days >= minClippingRetentionDays && days <= maxClippingRetentionDays {
			return days
		}
	}
	return defaultClippingRetentionDays
}

func (a *dashboardApp) retentionDeadline(now time.Time) int64 {
	return now.Add(time.Duration(a.clippingRetention()) * 24 * time.Hour).Unix()
}

func clippingSourceSummary(source ClippingSource) clippingSourceView {
	progress := 0
	switch source.Status {
	case ClippingSourceReady:
		progress = 100
	case ClippingSourceUploading:
		if source.DeclaredSizeBytes > 0 {
			progress = int(source.UploadOffset * 100 / source.DeclaredSizeBytes)
		}
	}
	mediaURL := ""
	if source.Status == ClippingSourceReady {
		mediaURL = "/api/clipping/sources/" + source.ID + "/media"
	}
	return clippingSourceView{
		ID: source.ID, Kind: source.Kind, Status: source.Status, RightsAttested: source.RightsAttested, OriginalName: source.OriginalName,
		MediaType: source.MediaType, DeclaredSizeBytes: source.DeclaredSizeBytes, SizeBytes: source.SizeBytes,
		UploadOffset: source.UploadOffset, DurationMS: source.DurationMS, SHA256: source.SHA256,
		RetainUntil: source.RetainUntil, Progress: progress, Error: source.Failure, MediaURL: mediaURL,
		CreatedAt: source.CreatedAt, UpdatedAt: source.UpdatedAt,
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
			LeaseExpiresAt: stage.LeaseExpiresAt, ReservedMicroUSD: stage.ReservedMicroUSD,
			ActualMicroUSD: stage.ActualMicroUSD, EstimatedMicroUSD: stage.EstimatedMicroUSD, CostReconciled: stage.CostReconciled, Error: stage.Error, CreatedAt: stage.CreatedAt,
			UpdatedAt: stage.UpdatedAt,
		})
	}
	return clippingJobView{
		ID: job.ID, BatchID: job.BatchID, SourceID: job.SourceID, ContentType: job.ContentType,
		MinClipSeconds: job.MinClipSeconds, MaxClipSeconds: job.MaxClipSeconds, CandidateLimit: job.CandidateLimit, Status: job.Status,
		BudgetMicroUSD: job.BudgetLimitMicroUSD, ReservedMicroUSD: job.ReservedMicroUSD,
		SpentMicroUSD: job.SpentMicroUSD, Progress: clippingJobProgress(stages, job.Status),
		Error: job.Error, Stages: stageViews, Artifacts: artifacts, CreatedAt: job.CreatedAt, UpdatedAt: job.UpdatedAt,
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
	}
	return clippingJobSummary(job, stages, artifacts), nil
}

func (a *dashboardApp) clippingBatchResponse(batch ClippingBatch) (clippingBatchView, error) {
	jobs, err := a.store.ClippingJobs(batch.ID)
	if err != nil {
		return clippingBatchView{}, err
	}
	view := clippingBatchView{
		ID: batch.ID, Status: batch.Status, BudgetMicroUSD: batch.BudgetLimitMicroUSD,
		ReservedMicroUSD: batch.ReservedMicroUSD, SpentMicroUSD: batch.SpentMicroUSD,
		CreatedAt: batch.CreatedAt, UpdatedAt: batch.UpdatedAt,
		SourceIDs: make([]string, 0, len(jobs)), JobIDs: make([]string, 0, len(jobs)),
	}
	totalProgress := 0
	for _, job := range jobs {
		stages, stageErr := a.store.ClippingStages(job.ID)
		if stageErr != nil {
			return clippingBatchView{}, stageErr
		}
		view.SourceIDs = append(view.SourceIDs, job.SourceID)
		view.JobIDs = append(view.JobIDs, job.ID)
		totalProgress += clippingJobProgress(stages, job.Status)
	}
	if len(jobs) > 0 {
		view.Progress = totalProgress / len(jobs)
	}
	return view, nil
}

func (a *dashboardApp) clippingConfig(w http.ResponseWriter, _ *http.Request) {
	days := a.clippingRetention()
	writeJSON(w, http.StatusOK, map[string]any{
		"worker_available":       false,
		"worker_state":           "unavailable",
		"max_source_bytes":       MaxClippingSourceBytes,
		"max_duration_ms":        MaxClippingSourceDurationMS,
		"max_upload_chunk_bytes": maxClippingUploadChunkBytes,
		"default_retention_days": defaultClippingRetentionDays,
		"retention_days":         days,
		"retention_configurable": true,
		"allowed_import_kinds":   []ClippingSourceKind{ClippingSourceGoogleDrive, ClippingSourceDropbox, ClippingSourceYouTube},
	})
}

func (a *dashboardApp) updateClippingRetention(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) {
		return
	}
	if !isJSON(r.Header.Get("Content-Type")) {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	var input struct {
		RetentionDays int `json:"retention_days"`
	}
	if decodeJSONBody(w, r, &input) != nil {
		return
	}
	if input.RetentionDays < minClippingRetentionDays || input.RetentionDays > maxClippingRetentionDays {
		writeError(w, http.StatusBadRequest, "retention_days must be between 1 and 365")
		return
	}
	if err := a.store.SetSetting(clippingRetentionSetting, strconv.Itoa(input.RetentionDays)); err != nil {
		writeError(w, http.StatusInternalServerError, "could not save clipping retention")
		return
	}
	deadline := time.Now().Add(time.Duration(input.RetentionDays) * 24 * time.Hour).Unix()
	if err := a.store.ApplyClippingRetention(deadline); err != nil {
		writeError(w, http.StatusInternalServerError, "could not update source retention")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"retention_days": input.RetentionDays})
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

func (a *dashboardApp) createClippingUpload(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) {
		return
	}
	if !isJSON(r.Header.Get("Content-Type")) {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	var input struct {
		OriginalName   string `json:"original_name"`
		MediaType      string `json:"media_type"`
		SizeBytes      int64  `json:"size_bytes"`
		RightsAttested bool   `json:"rights_attested"`
	}
	if decodeJSONBody(w, r, &input) != nil {
		return
	}
	if !input.RightsAttested {
		writeError(w, http.StatusUnprocessableEntity, "confirm that you have permission to reuse and process this source")
		return
	}
	source, err := a.clippingAcquisition.CreateUpload(r.Context(), input.RightsAttested, input.OriginalName, input.MediaType, input.SizeBytes, a.retentionDeadline(time.Now()))
	if err != nil {
		writeError(w, clippingErrorStatus(err), safeClippingClientError(err, "could not start source upload"))
		return
	}
	a.store.PublishClippingSource(source.ID)
	writeJSON(w, http.StatusCreated, map[string]any{"source": clippingSourceSummary(source)})
}

func (a *dashboardApp) uploadClippingChunk(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) {
		return
	}
	id := r.PathValue("id")
	if !safeID(id) {
		writeError(w, http.StatusBadRequest, "invalid clipping source id")
		return
	}
	if r.ContentLength > maxClippingUploadChunkBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "upload chunks may not exceed 16 MiB")
		return
	}
	offsetHeader := r.Header.Get("Upload-Offset")
	expectedOffset, err := strconv.ParseInt(offsetHeader, 10, 64)
	if err != nil || expectedOffset < 0 {
		writeError(w, http.StatusBadRequest, "Upload-Offset must be a nonnegative byte offset")
		return
	}
	controller := http.NewResponseController(w)
	if err := controller.SetReadDeadline(time.Now().Add(5 * time.Minute)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		writeError(w, http.StatusRequestTimeout, "upload request could not be opened")
		return
	}
	defer func() { _ = controller.SetReadDeadline(time.Time{}) }()
	r.Body = http.MaxBytesReader(w, r.Body, maxClippingUploadChunkBytes)
	nextOffset, err := a.clippingAcquisition.UploadChunk(r.Context(), id, expectedOffset, r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "upload chunks may not exceed 16 MiB")
			return
		}
		writeError(w, clippingErrorStatus(err), safeClippingClientError(err, "could not save upload progress"))
		return
	}
	source, err := a.store.ClippingSource(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not reload source upload")
		return
	}
	source.UploadOffset = nextOffset
	a.store.PublishClippingSource(id)
	writeJSON(w, http.StatusOK, map[string]any{"source": clippingSourceSummary(source)})
}

func (a *dashboardApp) finalizeClippingUpload(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) {
		return
	}
	id := r.PathValue("id")
	if !safeID(id) {
		writeError(w, http.StatusBadRequest, "invalid clipping source id")
		return
	}
	source, err := a.clippingAcquisition.FinalizeUpload(r.Context(), id)
	if err != nil {
		writeError(w, clippingErrorStatus(err), safeClippingClientError(err, "could not validate uploaded video"))
		return
	}
	a.store.PublishClippingSource(id)
	writeJSON(w, http.StatusOK, map[string]any{"source": clippingSourceSummary(source)})
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

func (a *dashboardApp) deleteClippingSource(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) {
		return
	}
	id := r.PathValue("id")
	if !safeID(id) {
		writeError(w, http.StatusBadRequest, "invalid clipping source id")
		return
	}
	if err := a.clippingAcquisition.CancelSource(r.Context(), id); err != nil {
		writeError(w, clippingErrorStatus(err), safeClippingClientError(err, "could not remove clipping source"))
		return
	}
	if err := a.store.TombstoneClippingSource(id); err != nil {
		writeError(w, clippingErrorStatus(err), safeClippingClientError(err, "could not remove clipping source"))
		return
	}
	a.store.PublishClippingSource(id)
	writeJSON(w, http.StatusNoContent, nil)
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
	jobs, err := a.store.ClippingJobs("")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load clipping jobs")
		return
	}
	views := make([]clippingJobView, 0, len(jobs))
	for _, job := range jobs {
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
	if err != nil {
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
		BudgetMicroUSD int64  `json:"budget_micro_usd"`
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
	if input.BudgetMicroUSD <= 0 || input.BudgetMicroUSD > 4_000_000 {
		writeError(w, http.StatusBadRequest, "budget_micro_usd must be between $0.000001 and $4.00")
		return
	}
	selection, selectionErr := normalizeClippingSelection(ClippingJobCreate{
		SourceID: input.SourceID, BudgetLimitMicroUSD: input.BudgetMicroUSD,
		ContentType: input.ContentType, MinClipSeconds: input.MinClipSeconds,
		MaxClipSeconds: input.MaxClipSeconds, CandidateLimit: input.CandidateLimit,
	})
	if selectionErr != nil {
		writeError(w, http.StatusBadRequest, selectionErr.Error())
		return
	}
	_, jobs, duplicate, err := a.store.CreateClippingBatch(ClippingBatchCreate{
		IdempotencyKey: key, BudgetLimitMicroUSD: input.BudgetMicroUSD,
		Jobs: []ClippingJobCreate{selection},
	})
	if err != nil {
		writeError(w, clippingErrorStatus(err), safeClippingClientError(err, "could not queue clipping job"))
		return
	}
	job := jobs[0]
	view, err := a.clippingJobResponse(job, false)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "clipping job was saved but could not be reloaded")
		return
	}
	a.store.PublishClippingJob(job.ID)
	a.publishClippingBatch(job.BatchID)
	status := http.StatusCreated
	if duplicate {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"job": view, "duplicate": duplicate})
}

func (a *dashboardApp) createClippingBatch(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) {
		return
	}
	if !isJSON(r.Header.Get("Content-Type")) {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	var input struct {
		SourceIDs            []string `json:"source_ids"`
		PerJobBudgetMicroUSD int64    `json:"per_job_budget_micro_usd"`
		BudgetMicroUSD       int64    `json:"budget_micro_usd"`
		ContentType          string   `json:"content_type"`
		MinClipSeconds       int      `json:"min_clip_seconds"`
		MaxClipSeconds       int      `json:"max_clip_seconds"`
		CandidateLimit       int      `json:"candidate_limit"`
	}
	if decodeJSONBody(w, r, &input) != nil {
		return
	}
	key, ok := clippingIdempotencyKey(w, r)
	if !ok {
		return
	}
	if len(input.SourceIDs) == 0 || len(input.SourceIDs) > MaxClippingBatchJobs || input.PerJobBudgetMicroUSD <= 0 || input.BudgetMicroUSD <= 0 {
		writeError(w, http.StatusBadRequest, "batch requires source_ids and positive per-job and aggregate budgets")
		return
	}
	if input.PerJobBudgetMicroUSD > 4_000_000 || input.BudgetMicroUSD > 400_000_000 {
		writeError(w, http.StatusBadRequest, "batch budget is outside the supported range")
		return
	}
	if input.PerJobBudgetMicroUSD > input.BudgetMicroUSD/int64(len(input.SourceIDs)) {
		writeError(w, http.StatusBadRequest, "aggregate budget must cover each job's budget")
		return
	}
	selection, selectionErr := normalizeClippingSelection(ClippingJobCreate{
		ContentType: input.ContentType, MinClipSeconds: input.MinClipSeconds,
		MaxClipSeconds: input.MaxClipSeconds, CandidateLimit: input.CandidateLimit,
	})
	if selectionErr != nil {
		writeError(w, http.StatusBadRequest, selectionErr.Error())
		return
	}
	jobs := make([]ClippingJobCreate, 0, len(input.SourceIDs))
	seen := make(map[string]struct{}, len(input.SourceIDs))
	for _, sourceID := range input.SourceIDs {
		if !safeID(sourceID) {
			writeError(w, http.StatusBadRequest, "batch contains an invalid source id")
			return
		}
		if _, exists := seen[sourceID]; exists {
			writeError(w, http.StatusBadRequest, "batch cannot include the same source more than once")
			return
		}
		seen[sourceID] = struct{}{}
		selection.SourceID = sourceID
		selection.BudgetLimitMicroUSD = input.PerJobBudgetMicroUSD
		jobs = append(jobs, selection)
	}
	batch, createdJobs, duplicate, err := a.store.CreateClippingBatch(ClippingBatchCreate{
		IdempotencyKey: key, BudgetLimitMicroUSD: input.BudgetMicroUSD, Jobs: jobs,
	})
	if err != nil {
		writeError(w, clippingErrorStatus(err), safeClippingClientError(err, "could not queue clipping batch"))
		return
	}
	for _, job := range createdJobs {
		a.store.PublishClippingJob(job.ID)
	}
	view, err := a.clippingBatchResponse(batch)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "clipping batch was saved but could not be reloaded")
		return
	}
	a.store.PublishClippingBatch(batch.ID)
	status := http.StatusCreated
	if duplicate {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"batch": view, "duplicate": duplicate})
}

func (a *dashboardApp) clippingBatches(w http.ResponseWriter, _ *http.Request) {
	batches, err := a.store.ClippingBatches()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load clipping batches")
		return
	}
	views := make([]clippingBatchView, 0, len(batches))
	for _, batch := range batches {
		view, err := a.clippingBatchResponse(batch)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not load clipping batches")
			return
		}
		views = append(views, view)
	}
	writeJSON(w, http.StatusOK, map[string]any{"batches": views})
}

func (a *dashboardApp) clippingBatch(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !safeID(id) {
		writeError(w, http.StatusBadRequest, "invalid clipping batch id")
		return
	}
	batch, err := a.store.ClippingBatch(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "clipping batch not found")
		return
	}
	view, err := a.clippingBatchResponse(batch)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load clipping batch details")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"batch": view})
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
	a.publishClippingBatch(job.BatchID)
	writeJSON(w, http.StatusOK, map[string]any{"job": clippingJobSummary(job, nil, nil)})
}

func (a *dashboardApp) cancelClippingBatch(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) {
		return
	}
	id := r.PathValue("id")
	if !safeID(id) {
		writeError(w, http.StatusBadRequest, "invalid clipping batch id")
		return
	}
	if err := a.store.CancelClippingBatch(id); err != nil {
		writeError(w, clippingErrorStatus(err), safeClippingClientError(err, "could not cancel clipping batch"))
		return
	}
	jobs, _ := a.store.ClippingJobs(id)
	for _, job := range jobs {
		a.deleteClippingCapabilities(job.ID)
		a.store.PublishClippingJob(job.ID)
	}
	a.publishClippingBatch(id)
	batch, err := a.store.ClippingBatch(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "batch was canceled but could not be reloaded")
		return
	}
	view, err := a.clippingBatchResponse(batch)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "batch was canceled but could not be reloaded")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"batch": view})
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
	a.publishClippingBatch(job.BatchID)
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
	case errors.Is(err, sql.ErrNoRows), errors.Is(err, ErrClippingSourceNotFound), errors.Is(err, ErrClippingBatchNotFound), errors.Is(err, ErrClippingJobNotFound), errors.Is(err, ErrClippingStageNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrClippingIdempotencyConflict), errors.Is(err, ErrClippingInvalidState), errors.Is(err, ErrClippingStaleAttempt), errors.Is(err, ErrClippingJobTerminal):
		return http.StatusConflict
	case errors.Is(err, ErrYouTubeImportUnavailable), errors.Is(err, ErrClippingRightsNotAttested):
		return http.StatusUnprocessableEntity
	case errors.Is(err, ErrClippingBudgetExceeded):
		return http.StatusPaymentRequired
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
	return a.persistClippingDispatchCapabilitiesUntil(stage, stage.LeaseExpiresAt+int64(clippingCallbackAccountingGrace.Seconds()))
}

func (a *dashboardApp) persistClippingDispatchCapabilitiesUntil(stage ClippingStage, callbackExpiresAt int64) (mediaToken, callbackToken string, err error) {
	if stage.JobID == "" || stage.AttemptID == "" || stage.LeaseToken == "" || stage.LeaseExpiresAt <= time.Now().Unix() {
		return "", "", errors.New("invalid clipping stage lease")
	}
	now := time.Now().Unix()
	if callbackExpiresAt <= now || callbackExpiresAt > stage.LeaseExpiresAt+int64(clippingCallbackAccountingGrace.Seconds()) {
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

func (a *dashboardApp) publishClippingBatch(id string) {
	if a.store.events == nil || !a.store.events.hasSubscribers() {
		return
	}
	batch, err := a.store.ClippingBatch(id)
	if err != nil {
		return
	}
	view, err := a.clippingBatchResponse(batch)
	if err != nil {
		return
	}
	fingerprint, err := snapshotFingerprint(view)
	if err == nil {
		a.store.events.publishSnapshot("clipping_batch", id, fingerprint, map[string]any{"id": id, "batch": view})
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

func (s *Store) PublishClippingBatch(id string) {
	app := dashboardApp{store: s}
	app.publishClippingBatch(id)
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

func (a *dashboardApp) clippingWorkerCallback(w http.ResponseWriter, r *http.Request) {
	jobID, attemptID := r.PathValue("jobID"), r.PathValue("attemptID")
	if !safeID(jobID) || !safeID(attemptID) || r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid worker callback request")
		return
	}
	if r.ContentLength > maxClippingCallbackBodyBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "worker artifact exceeds 4 MiB")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxClippingCallbackBodyBytes)
	if !isJSON(r.Header.Get("Content-Type")) {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	if !a.authorizeClippingBearer(w, r, jobID, attemptID, clippingCapabilityCallback) {
		return
	}
	var input clippingWorkerCallback
	if err := decodeClippingWorkerCallback(w, r, &input); err != nil {
		return
	}
	versionV1 := input.ProtocolVersion == clippingProtocolVersionV1
	versionV2 := input.ProtocolVersion == clippingProtocolVersion
	if (!versionV1 && !versionV2) || input.JobID != jobID || input.Stage != ClippingStageAnalysis || input.AttemptID != attemptID || input.DispatchID == "" || input.ActualCostMicroUSD < 0 {
		writeError(w, http.StatusBadRequest, "worker callback does not match the clipping protocol")
		return
	}
	job, stage, source, ok := a.workerStage(jobID, attemptID)
	if !ok {
		writeError(w, http.StatusConflict, "worker callback attempt is stale")
		return
	}
	if input.DispatchID != stage.IdempotencyKey {
		writeError(w, http.StatusConflict, "worker callback dispatch id is stale")
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
	switch input.Status {
	case "completed":
		payload := strings.TrimSpace(string(input.Artifact.Payload))
		if versionV2 {
			if input.ActualCostMicroUSD != 0 || input.CostEstimateMicroUSD <= 0 || input.CostBasis != clippingWorkerBillingBasis ||
				input.Artifact.Type != clippingAnalysisArtifactType || input.Artifact.SchemaVersion != clippingAnalysisSchemaVersion || len(input.Artifact.TimeRanges) != 0 {
				writeError(w, http.StatusBadRequest, "worker callback estimate or source analysis artifact is invalid")
				return
			}
			core, coreErr := decodeClippingAnalysisCore(payload, source)
			if coreErr != nil {
				writeError(w, http.StatusBadRequest, "worker source analysis failed validation")
				return
			}
			var cost struct {
				Basis          string `json:"cost_basis"`
				Rate           int64  `json:"rate_micro_usd_per_second"`
				ComputeSeconds int64  `json:"compute_seconds"`
				Estimate       int64  `json:"cost_estimate_micro_usd"`
			}
			if err := json.Unmarshal(input.Artifact.Payload, &cost); err != nil || cost.Basis != input.CostBasis || cost.Estimate != input.CostEstimateMicroUSD || cost.Rate <= 0 || cost.ComputeSeconds <= 0 {
				writeError(w, http.StatusBadRequest, "worker callback estimate metadata is invalid")
				return
			}
			_, duplicate, _, err := a.store.CompleteClippingStageEstimate(jobID, stage.Name, attemptID, leaseToken, input.CostEstimateMicroUSD, cost.ComputeSeconds, ClippingArtifact{
				JobID: jobID, Type: input.Artifact.Type, SchemaVersion: input.Artifact.SchemaVersion,
				Version: input.Artifact.Version, SourceDurationMS: input.Artifact.SourceDurationMS,
				TimeRanges: input.Artifact.TimeRanges, PayloadJSON: payload,
			}, core)
			if err != nil {
				if errors.Is(err, ErrClippingStaleAttempt) || errors.Is(err, ErrClippingJobTerminal) {
					a.store.PublishClippingJob(jobID)
					a.publishClippingBatch(job.BatchID)
					a.revokeClippingMediaCapability(jobID, attemptID)
					w.WriteHeader(http.StatusNoContent)
					return
				}
				writeError(w, clippingErrorStatus(err), safeClippingClientError(err, "worker callback could not be applied"))
				return
			}
			if !duplicate {
				a.store.PublishClippingJob(jobID)
				a.publishClippingBatch(job.BatchID)
			}
			a.revokeClippingMediaCapability(jobID, attemptID)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if versionV1 && input.CostEstimateMicroUSD != 0 {
			writeError(w, http.StatusBadRequest, "legacy worker callback cannot contain an estimate")
			return
		}
		_, duplicate, err := a.store.CompleteClippingStage(jobID, stage.Name, attemptID, leaseToken, input.ActualCostMicroUSD, ClippingArtifact{
			JobID: jobID, Type: input.Artifact.Type, SchemaVersion: input.Artifact.SchemaVersion,
			Version: input.Artifact.Version, SourceDurationMS: input.Artifact.SourceDurationMS,
			TimeRanges: input.Artifact.TimeRanges, PayloadJSON: payload,
		})
		if err != nil {
			if errors.Is(err, ErrClippingStaleAttempt) || errors.Is(err, ErrClippingJobTerminal) || errors.Is(err, ErrClippingInvalidArtifact) {
				a.store.PublishClippingJob(jobID)
				a.publishClippingBatch(job.BatchID)
				a.revokeClippingMediaCapability(jobID, attemptID)
				if errors.Is(err, ErrClippingInvalidArtifact) {
					if strings.Contains(err.Error(), "4 MiB") {
						writeError(w, http.StatusRequestEntityTooLarge, "worker artifact exceeds 4 MiB; actual cost was reconciled")
					} else {
						writeError(w, http.StatusBadRequest, "worker artifact was invalid; actual cost was reconciled")
					}
					return
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}
			writeError(w, clippingErrorStatus(err), safeClippingClientError(err, "worker callback could not be applied"))
			return
		}
		if !duplicate {
			a.store.PublishClippingJob(jobID)
			a.publishClippingBatch(job.BatchID)
		}
		a.revokeClippingMediaCapability(jobID, attemptID)
		w.WriteHeader(http.StatusNoContent)
	case "failed":
		if versionV2 {
			writeError(w, http.StatusBadRequest, "v2 worker callbacks report analysis results only")
			return
		}
		// Worker error details can contain signed source URLs, capability values,
		// or other upstream secrets. Keep persisted job/stage/SSE errors fixed.
		message := "clipping analysis stage failed"
		_, err := a.store.FailClippingStage(jobID, stage.Name, attemptID, leaseToken, message, false, input.ActualCostMicroUSD)
		if err != nil {
			if errors.Is(err, ErrClippingStaleAttempt) || errors.Is(err, ErrClippingJobTerminal) {
				a.store.PublishClippingJob(jobID)
				a.publishClippingBatch(job.BatchID)
				a.revokeClippingMediaCapability(jobID, attemptID)
				w.WriteHeader(http.StatusNoContent)
				return
			}
			writeError(w, clippingErrorStatus(err), safeClippingClientError(err, "worker callback could not be applied"))
			return
		}
		a.store.PublishClippingJob(jobID)
		a.publishClippingBatch(job.BatchID)
		a.revokeClippingMediaCapability(jobID, attemptID)
		w.WriteHeader(http.StatusNoContent)
	default:
		writeError(w, http.StatusBadRequest, "worker callback status must be completed or failed")
	}
}

func decodeClippingWorkerCallback(w http.ResponseWriter, r *http.Request, target any) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "worker artifact exceeds 4 MiB")
		} else {
			writeError(w, http.StatusBadRequest, "invalid worker callback JSON")
		}
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "worker artifact exceeds 4 MiB")
		} else {
			writeError(w, http.StatusBadRequest, "worker callback must contain one JSON object")
		}
		return errors.New("worker callback contains trailing JSON")
	}
	return nil
}

func (a *dashboardApp) publishClippingEventsForBatch(batchID string) {
	jobs, err := a.store.ClippingJobs(batchID)
	if err != nil {
		return
	}
	for _, job := range jobs {
		a.store.PublishClippingJob(job.ID)
	}
	a.publishClippingBatch(batchID)
}

func (a *dashboardApp) updateClippingSelection(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) {
		return
	}
	jobID := r.PathValue("id")
	if !safeID(jobID) || r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid clipping job selection request")
		return
	}
	if !isJSON(r.Header.Get("Content-Type")) {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	type selectionRequest struct {
		ContentType    string `json:"content_type"`
		MinClipSeconds int    `json:"min_clip_seconds"`
		MaxClipSeconds int    `json:"max_clip_seconds"`
		CandidateLimit int    `json:"candidate_limit"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var input selectionRequest
	if err := decoder.Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid selection options")
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "selection request must contain one JSON object")
		return
	}
	job, _, err := a.store.UpdateClippingJobSelection(jobID, ClippingJobCreate{
		ContentType: input.ContentType, MinClipSeconds: input.MinClipSeconds,
		MaxClipSeconds: input.MaxClipSeconds, CandidateLimit: input.CandidateLimit,
	})
	if err != nil {
		writeError(w, clippingErrorStatus(err), safeClippingClientError(err, "saved analysis selection could not be updated"))
		return
	}
	a.store.PublishClippingJob(jobID)
	a.publishClippingBatch(job.BatchID)
	view, err := a.clippingJobResponse(job, true)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "selection was saved but its analysis could not be reloaded")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"job": view})
}

func (a *dashboardApp) reconcileClippingCost(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) {
		return
	}
	jobID := r.PathValue("id")
	if !safeID(jobID) || r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid clipping cost reconciliation request")
		return
	}
	if !isJSON(r.Header.Get("Content-Type")) {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	type reconciliationRequest struct {
		AttemptID               string `json:"attempt_id"`
		ActualCostMicroUSD      int64  `json:"actual_cost_micro_usd"`
		ReconciliationReference string `json:"reconciliation_reference"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var input reconciliationRequest
	if err := decoder.Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid clipping cost reconciliation")
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "reconciliation request must contain one JSON object")
		return
	}
	stage, duplicate, err := a.store.ReconcileClippingStageCost(jobID, input.AttemptID, input.ActualCostMicroUSD, input.ReconciliationReference)
	if err != nil {
		writeError(w, clippingErrorStatus(err), safeClippingClientError(err, "clipping invoice settlement could not be recorded"))
		return
	}
	job, err := a.store.ClippingJob(jobID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "invoice settlement was recorded but the job could not be reloaded")
		return
	}
	if !duplicate {
		a.store.PublishClippingJob(jobID)
		a.publishClippingBatch(job.BatchID)
	}
	view, err := a.clippingJobResponse(job, true)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "invoice settlement was recorded but the job could not be reloaded")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"job": view, "stage": stage, "duplicate": duplicate})
}
