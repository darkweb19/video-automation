package app

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	videoCallbackBaseURLEnv = "VIDEO_CALLBACK_BASE_URL"
	videoCallbackPath       = "/api/video-callbacks/"
	callbackJobLifetime     = 8 * time.Hour
)

var (
	errCallbackWorkerUnsupported  = errors.New("Modal worker does not support idempotent callback jobs; deploy the updated Modal worker")
	errCallbackSubmissionRedirect = errors.New("Modal callback submission was redirected; update the configured endpoint to the final HTTPS URL")
)

// VideoCallbackSubmission contains the per-job capability sent only to Modal.
// It is never serialized into a browser response or persisted without encryption.
type VideoCallbackSubmission struct {
	JobID         string `json:"job_id"`
	CallbackURL   string `json:"callback_url"`
	CallbackToken string `json:"callback_token"`
}

// CallbackVideoProvider supports durable, client-assigned Modal jobs. Providers
// that do not implement it keep using the normal status polling contract.
type CallbackVideoProvider interface {
	SubmitVideo(context.Context, GenerateRequest, VideoCallbackSubmission) (*Generation, error)
}

type videoCallback struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	Sequence  int    `json:"sequence"`
	Progress  int    `json:"progress"`
	ErrorCode string `json:"error_code,omitempty"`
	Stage     string `json:"stage,omitempty"`
	CostUSD   string `json:"cost_usd,omitempty"`
}

type callbackJob struct {
	ID, GenerationID, ProjectID, ProviderConfigID string
	SceneNumber                                   int
	Request                                       GenerateRequest
	CallbackURL, EncryptedToken, TokenHash        string
	State                                         string
	Sequence, Attempts                            int
	NextAttemptAt, Deadline                       int64
}

func configuredVideoCallbackBaseURL() string {
	if configured := strings.TrimSpace(os.Getenv(videoCallbackBaseURLEnv)); configured != "" {
		return configured
	}
	// Preserve existing deployments while they migrate from the old callback
	// implementation. VIDEO_CALLBACK_BASE_URL remains the documented setting.
	return configuredCallbackBaseURL()
}

func validateVideoCallbackBaseURL(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("Set VIDEO_CALLBACK_BASE_URL to this dashboard's public HTTPS origin before generating with Modal (for example https://video.example.com).")
	}
	host := strings.ToLower(parsed.Hostname())
	ip := net.ParseIP(host)
	if host == "localhost" || !strings.Contains(host, ".") || (ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsLinkLocalUnicast())) {
		return "", errors.New("VIDEO_CALLBACK_BASE_URL must be reachable from Modal; use a public HTTPS dashboard address or HTTPS tunnel.")
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func (a *dashboardApp) videoCallbackURL(jobID string) (string, error) {
	if !safeID(jobID) {
		return "", errors.New("callback job id is invalid")
	}
	baseURL, err := validateVideoCallbackBaseURL(a.videoCallbackBaseURL)
	if err != nil {
		return "", err
	}
	return baseURL + videoCallbackPath + jobID, nil
}

func newVideoCallbackToken() (string, error) {
	// Match the Modal worker's 32-byte URL-safe capability format.
	return newModalCallbackToken()
}

func validVideoCallbackToken(token string) bool {
	return validModalCallbackToken(token)
}

func (a *dashboardApp) prepareCallbackJob(request GenerateRequest, configID string) (callbackJob, error) {
	rawID := make([]byte, 16)
	if _, err := rand.Read(rawID); err != nil {
		return callbackJob{}, err
	}
	id := "gen_" + hex.EncodeToString(rawID)
	callbackURL, err := a.videoCallbackURL(id)
	if err != nil {
		return callbackJob{}, err
	}
	if a.security == nil {
		return callbackJob{}, errors.New("secure callback storage is unavailable")
	}
	token, err := newVideoCallbackToken()
	if err != nil {
		return callbackJob{}, err
	}
	encryptedToken, err := a.security.EncryptSetting("video_callback."+id, token)
	if err != nil {
		return callbackJob{}, err
	}
	return callbackJob{
		ID: id, ProviderConfigID: configID, Request: request, CallbackURL: callbackURL,
		EncryptedToken: encryptedToken, TokenHash: tokenHash(token), State: "submitting",
		Deadline: time.Now().Add(callbackJobLifetime).Unix(),
	}, nil
}

func (s *Store) migrateCallbackJobs() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS video_callback_jobs (
		id TEXT PRIMARY KEY,
		generation_id TEXT REFERENCES generations(id) ON DELETE CASCADE,
		project_id TEXT REFERENCES video_projects(id) ON DELETE CASCADE,
		scene_number INTEGER NOT NULL DEFAULT 0,
		provider_config_id TEXT NOT NULL,
		request_json TEXT NOT NULL,
		callback_url TEXT NOT NULL,
		encrypted_token TEXT NOT NULL,
		token_hash TEXT NOT NULL,
		state TEXT NOT NULL DEFAULT 'submitting',
		sequence INTEGER NOT NULL DEFAULT 0,
		attempts INTEGER NOT NULL DEFAULT 0,
		next_attempt_at INTEGER NOT NULL DEFAULT 0,
		deadline INTEGER NOT NULL,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL,
		CHECK ((generation_id IS NOT NULL AND project_id IS NULL AND scene_number=0) OR
		       (generation_id IS NULL AND project_id IS NOT NULL AND scene_number BETWEEN 1 AND 5))
	);
	CREATE INDEX IF NOT EXISTS video_callback_jobs_pending ON video_callback_jobs(state,next_attempt_at);`)
	return err
}

func insertCallbackJob(tx *sql.Tx, job callbackJob) error {
	request, err := json.Marshal(job.Request)
	if err != nil {
		return err
	}
	var generationID, projectID any
	if job.GenerationID != "" {
		generationID = job.GenerationID
	} else {
		projectID = job.ProjectID
	}
	_, err = tx.Exec(`INSERT INTO video_callback_jobs(id,generation_id,project_id,scene_number,provider_config_id,request_json,callback_url,encrypted_token,token_hash,state,deadline,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,unixepoch(),unixepoch())`,
		job.ID, generationID, projectID, job.SceneNumber, job.ProviderConfigID, string(request), job.CallbackURL, job.EncryptedToken, job.TokenHash, job.State, job.Deadline)
	return err
}

func (s *Store) insertCallbackGeneration(job callbackJob, estimatedCost string) error {
	if !safeID(job.ID) || job.GenerationID != "" || job.ProjectID != "" || job.SceneNumber != 0 || job.ProviderConfigID == "" || !validVideoCallbackTokenHash(job.TokenHash) {
		return errors.New("callback generation job is incomplete")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	_, err = tx.Exec(`INSERT INTO generations(id,video_provider,provider_config_id,prompt,model,duration,aspect_ratio,status,progress,estimated_cost_usd,created_at,updated_at) VALUES(?,'modal',?,?,?,?,?,'queued',0,?,?,?)`,
		job.ID, job.ProviderConfigID, job.Request.Prompt, job.Request.Model, job.Request.Duration, job.Request.AspectRatio, estimatedCost, now, now)
	if err != nil {
		return err
	}
	job.GenerationID = job.ID
	if err := insertCallbackJob(tx, job); err != nil {
		return err
	}
	_ = appendGenerationEvent(tx, job.ID, "submission", "queued", "Generation queued for callback delivery", 0)
	return tx.Commit()
}

func (s *Store) insertCallbackScene(job callbackJob) error {
	if !safeID(job.ID) || job.GenerationID != "" || !safeID(job.ProjectID) || job.SceneNumber < 1 || job.SceneNumber > ProjectSceneCount || job.ProviderConfigID == "" || !validVideoCallbackTokenHash(job.TokenHash) {
		return errors.New("callback scene job is incomplete")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var provider, projectStatus, providerConfigID string
	if err := tx.QueryRow(`SELECT video_provider,status,provider_config_id FROM video_projects WHERE id=?`, job.ProjectID).Scan(&provider, &projectStatus, &providerConfigID); err != nil {
		return err
	}
	if provider != string(VideoProviderModal) || projectStatus != "generating" || providerConfigID != job.ProviderConfigID {
		return sql.ErrNoRows
	}
	result, err := tx.Exec(`UPDATE project_scenes SET status='queued',progress=MAX(progress,10),provider_generation_id=?,attempts=attempts+1,error='',next_attempt_at=0,updated_at=unixepoch() WHERE project_id=? AND scene_number=? AND status='pending'`, job.ID, job.ProjectID, job.SceneNumber)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return sql.ErrNoRows
	}
	if err := insertCallbackJob(tx, job); err != nil {
		return err
	}
	if err := appendPipelineEvent(tx, job.ProjectID, "scene_submission", "queued", "Scene queued for callback delivery.", job.SceneNumber, 0, time.Now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

func validVideoCallbackTokenHash(value string) bool {
	return len(value) == 43
}

const callbackColumns = `id,COALESCE(generation_id,''),COALESCE(project_id,''),scene_number,provider_config_id,request_json,callback_url,encrypted_token,token_hash,state,sequence,attempts,next_attempt_at,deadline`

func scanCallbackJob(row interface{ Scan(...any) error }) (callbackJob, error) {
	var job callbackJob
	var request string
	err := row.Scan(&job.ID, &job.GenerationID, &job.ProjectID, &job.SceneNumber, &job.ProviderConfigID, &request, &job.CallbackURL, &job.EncryptedToken, &job.TokenHash, &job.State, &job.Sequence, &job.Attempts, &job.NextAttemptAt, &job.Deadline)
	if err == nil {
		err = json.Unmarshal([]byte(request), &job.Request)
	}
	return job, err
}

func (s *Store) callbackJob(id string) (callbackJob, error) {
	return scanCallbackJob(s.db.QueryRow(`SELECT `+callbackColumns+` FROM video_callback_jobs WHERE id=?`, id))
}

func (s *Store) hasCallbackJob(id string) (bool, error) {
	var found bool
	err := s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM video_callback_jobs WHERE id=?)`, id).Scan(&found)
	return found, err
}

func (s *Store) ApplyVideoCallback(callback videoCallback, token string) error {
	if !validVideoCallbackToken(token) {
		return ErrModalCallbackUnauthorized
	}
	return s.applyVideoCallback(callback, tokenHash(token))
}

func (s *Store) applySystemVideoCallback(callback videoCallback) error {
	job, err := s.callbackJob(callback.ID)
	if err != nil {
		return err
	}
	return s.applyVideoCallback(callback, job.TokenHash)
}

func (s *Store) applyVideoCallback(callback videoCallback, credentialHash string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	job, err := scanCallbackJob(tx.QueryRow(`SELECT `+callbackColumns+` FROM video_callback_jobs WHERE id=?`, callback.ID))
	if err != nil {
		return err
	}
	if len(credentialHash) != len(job.TokenHash) || subtle.ConstantTimeCompare([]byte(credentialHash), []byte(job.TokenHash)) != 1 {
		return ErrModalCallbackUnauthorized
	}
	if job.State == "completed" || job.State == "failed" || callback.Sequence <= job.Sequence {
		return nil
	}
	status, message := callback.Status, ""
	if status == "completed" {
		status = "downloading"
		callback.Progress = 100
	}
	if status == "failed" {
		message = callbackFailure(callback.ErrorCode)
	}
	cost := callback.CostUSD
	if !validCallbackCost(cost) {
		return errors.New("invalid callback cost")
	}
	if job.GenerationID != "" {
		result, updateErr := tx.Exec(`UPDATE generations SET status=?,progress=MAX(progress,?),cost_usd=CASE WHEN ?='' THEN cost_usd ELSE ? END,error=?,updated_at=unixepoch() WHERE id=? AND video_provider='modal' AND status IN ('queued','processing')`, status, callback.Progress, cost, cost, message, job.GenerationID)
		if updateErr != nil {
			return updateErr
		}
		if count, _ := result.RowsAffected(); count == 1 {
			if err := appendGenerationEvent(tx, job.GenerationID, "callback", status, callbackEventMessage(status, message), callback.Progress); err != nil {
				return err
			}
		} else if err := tx.QueryRow(`SELECT status FROM generations WHERE id=?`, job.GenerationID).Scan(new(string)); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrModalCallbackUnavailable
			}
			return err
		}
	} else {
		result, updateErr := tx.Exec(`UPDATE project_scenes SET status=?,progress=MAX(progress,?),cost_usd=CASE WHEN ?='' THEN cost_usd ELSE ? END,error=?,updated_at=unixepoch() WHERE project_id=? AND scene_number=? AND provider_generation_id=? AND status IN ('queued','processing')`, status, callback.Progress, cost, cost, message, job.ProjectID, job.SceneNumber, job.ID)
		if updateErr != nil {
			return updateErr
		}
		if count, _ := result.RowsAffected(); count == 1 {
			if err := appendPipelineEvent(tx, job.ProjectID, "scene_callback", status, callbackEventMessage(status, message), job.SceneNumber, 0, time.Now().Unix()); err != nil {
				return err
			}
		}
	}
	_, err = tx.Exec(`UPDATE video_callback_jobs SET state=?,sequence=?,updated_at=unixepoch() WHERE id=? AND sequence<?`, callback.Status, callback.Sequence, callback.ID, callback.Sequence)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func callbackEventMessage(status, failure string) string {
	switch status {
	case "processing":
		return "Modal is generating video."
	case "downloading":
		return "Modal completed generation; downloading video."
	case "failed":
		return failure
	default:
		return "Modal callback updated generation."
	}
}

func callbackFailure(code string) string {
	switch code {
	case "out_of_memory":
		return "The Modal GPU ran out of memory. Try a lower resolution or a larger GPU."
	case "worker_startup_failed":
		return "The Modal worker could not start or load its model. Check the deployment and model access, then retry."
	case "generation_timeout":
		return "The Modal video job exceeded its generation deadline. Check the worker logs before retrying."
	case "generation_cancelled":
		return "The Modal video job was cancelled. Retry when the worker is available."
	case "generation_failed":
		return "Modal could not generate this video. Check the worker logs for the job, then retry."
	case "encoding_failed":
		return "Modal could not encode the generated video. Check the worker logs, then retry."
	case "storage_failed":
		return "Modal could not save the generated video. Check its jobs volume, then retry."
	case "dispatch_failed":
		return "Modal could not queue the video worker. Check the deployment and account capacity, then retry."
	case "dispatch_interrupted":
		return "Modal submission was interrupted before its worker reference was saved. Check Modal jobs before retrying."
	case "callback_timeout":
		return "No completion callback arrived from Modal before the job deadline. Check VIDEO_CALLBACK_BASE_URL, dashboard reachability, and Modal logs before retrying."
	case "submission_auth_failed":
		return "Modal rejected the saved API key. Update the account in Settings, then retry."
	case "submission_conflict":
		return "Modal received this generation ID with different request details. Retry with a new generation."
	case "callback_unsupported":
		return "This Modal deployment does not support callback jobs yet. Deploy the updated Modal worker, then retry."
	case "submission_redirect":
		return "The Modal endpoint redirected a callback submission. Update the Modal endpoint in Settings to its final HTTPS URL, then retry."
	case "submission_rejected":
		return "Modal rejected this callback job. Redeploy the updated worker and check callback configuration, then retry."
	case "provider_unavailable":
		return "The saved Modal account configuration is unavailable. Check Settings before retrying."
	default:
		return "Modal video generation failed. Check the worker logs for this job, then retry."
	}
}

func validCallbackCost(value string) bool {
	if len(value) > 24 {
		return false
	}
	dots := 0
	for _, character := range value {
		if character == '.' {
			dots++
			if dots > 1 {
				return false
			}
		} else if character < '0' || character > '9' {
			return false
		}
	}
	return value != "."
}

func (a *dashboardApp) videoCallback(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !safeID(id) {
		writeError(w, http.StatusNotFound, "callback job not found")
		return
	}
	job, err := a.store.callbackJob(id)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "callback job not found")
		return
	}
	if err != nil {
		a.logger.Error("load Modal callback job failed")
		w.Header().Set("Retry-After", "2")
		writeError(w, http.StatusServiceUnavailable, "callback target could not be loaded; retry delivery")
		return
	}
	if !isJSON(r.Header.Get("Content-Type")) {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	authorization := r.Header.Values("Authorization")
	if len(authorization) != 1 {
		writeError(w, http.StatusUnauthorized, "invalid callback credential")
		return
	}
	parts := strings.Fields(authorization[0])
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || !validVideoCallbackToken(parts[1]) {
		writeError(w, http.StatusUnauthorized, "invalid callback credential")
		return
	}
	var callback videoCallback
	if decodeJSONBody(w, r, &callback) != nil {
		return
	}
	callback.ID = strings.TrimSpace(callback.ID)
	callback.Status = strings.ToLower(strings.TrimSpace(callback.Status))
	callback.ErrorCode = strings.ToLower(strings.TrimSpace(callback.ErrorCode))
	callback.Stage = strings.TrimSpace(callback.Stage)
	callback.CostUSD = strings.TrimSpace(callback.CostUSD)
	if callback.ID != id || callback.Sequence < 1 || callback.Sequence > 1000000 || callback.Progress < 0 || callback.Progress > 100 || (callback.Status != "processing" && callback.Status != "completed" && callback.Status != "failed") || len(callback.ErrorCode) > 64 || len(callback.Stage) > 64 || !validCallbackCost(callback.CostUSD) {
		writeError(w, http.StatusBadRequest, "invalid callback payload")
		return
	}
	if err := a.store.ApplyVideoCallback(callback, parts[1]); err != nil {
		if errors.Is(err, ErrModalCallbackUnauthorized) {
			writeError(w, http.StatusUnauthorized, "invalid callback credential")
			return
		}
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, ErrModalCallbackUnavailable) {
			// A valid callback for a deleted or superseded attempt is terminal to
			// the worker. Do not disclose whether an identifier still exists.
			w.WriteHeader(http.StatusNoContent)
			return
		}
		a.logger.Error("Modal callback persistence failed")
		w.Header().Set("Retry-After", "2")
		writeError(w, http.StatusServiceUnavailable, "callback persistence is temporarily unavailable; retry delivery")
		return
	}
	if job.GenerationID != "" {
		a.store.PublishGeneration(job.GenerationID)
	}
	if job.ProjectID != "" {
		a.store.PublishProject(job.ProjectID)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (p *Processor) processCallbackJobs(ctx context.Context) {
	rows, err := p.app.store.db.QueryContext(ctx, `SELECT `+callbackColumns+` FROM video_callback_jobs WHERE state NOT IN ('completed','failed') AND (deadline<=unixepoch() OR (state='submitting' AND next_attempt_at<=unixepoch())) ORDER BY created_at`)
	if err != nil {
		p.logger.Error("load callback jobs failed")
		return
	}
	var jobs []callbackJob
	for rows.Next() {
		job, scanErr := scanCallbackJob(rows)
		if scanErr != nil {
			_ = rows.Close()
			return
		}
		jobs = append(jobs, job)
	}
	readErr := rows.Err()
	_ = rows.Close()
	if readErr != nil {
		return
	}
	for _, job := range jobs {
		if job.Deadline <= time.Now().Unix() {
			p.failCallbackJob(job, "callback_timeout")
			continue
		}
		jobCopy := job
		p.startProjectTask(ctx, "callback-submit:"+job.ID, func(taskCtx context.Context) {
			p.submitCallbackJob(taskCtx, jobCopy)
		})
	}
}

func (p *Processor) submitCallbackJob(ctx context.Context, job callbackJob) {
	if p.app.security == nil {
		p.failCallbackJob(job, "provider_unavailable")
		return
	}
	service, err := p.app.videoProviderForSnapshot(job.ProviderConfigID, VideoProviderModal)
	provider, ok := service.(CallbackVideoProvider)
	if err != nil || !ok {
		p.failCallbackJob(job, "provider_unavailable")
		return
	}
	token, err := p.app.security.DecryptSetting("video_callback."+job.ID, job.EncryptedToken)
	if err != nil {
		p.failCallbackJob(job, "provider_unavailable")
		return
	}
	requestContext, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	generation, err := provider.SubmitVideo(requestContext, job.Request, VideoCallbackSubmission{JobID: job.ID, CallbackURL: job.CallbackURL, CallbackToken: token})
	if err == nil && (generation == nil || generation.ID != job.ID) {
		err = errCallbackWorkerUnsupported
	}
	if err != nil {
		if errors.Is(err, errCallbackSubmissionRedirect) {
			p.failCallbackJob(job, "submission_redirect")
			return
		}
		if errors.Is(err, errCallbackWorkerUnsupported) {
			p.failCallbackJob(job, "callback_unsupported")
			return
		}
		var upstream *upstreamError
		if errors.As(err, &upstream) && upstream.StatusCode >= 400 && upstream.StatusCode < 500 && upstream.StatusCode != 429 && upstream.StatusCode != 408 {
			code := "submission_rejected"
			if upstream.StatusCode == 401 || upstream.StatusCode == 403 {
				code = "submission_auth_failed"
			}
			p.failCallbackJob(job, code)
			return
		}
		// The request may have reached Modal without its acknowledgment reaching
		// this service. Retrying the same id and capability is safe and idempotent.
		delay := time.Second * time.Duration(1<<min(job.Attempts+1, 8))
		_, _ = p.app.store.db.Exec(`UPDATE video_callback_jobs SET attempts=attempts+1,next_attempt_at=?,updated_at=unixepoch() WHERE id=? AND state='submitting'`, time.Now().Add(delay).Unix(), job.ID)
		return
	}
	_, _ = p.app.store.db.Exec(`UPDATE video_callback_jobs SET state='accepted',attempts=attempts+1,updated_at=unixepoch() WHERE id=? AND state='submitting'`, job.ID)
}

func (p *Processor) failCallbackJob(job callbackJob, code string) {
	if err := p.app.store.applySystemVideoCallback(videoCallback{ID: job.ID, Status: "failed", Sequence: job.Sequence + 1, ErrorCode: code}); err != nil {
		p.logger.Error("persist Modal callback job failure", "job_id", job.ID)
		return
	}
	if job.GenerationID != "" {
		p.app.store.PublishGeneration(job.GenerationID)
	}
	if job.ProjectID != "" {
		p.app.store.PublishProject(job.ProjectID)
	}
}

func callbackConfigurationError(err error) string {
	if err != nil && strings.Contains(err.Error(), videoCallbackBaseURLEnv) {
		return err.Error()
	}
	return "Unable to persist the Modal callback job. Retry after checking the application storage."
}
