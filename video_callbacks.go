package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Callback jobs are durable before any billable request leaves this process.
// Each scene attempt gets its own id and credential; a late previous attempt
// cannot update a retried scene. Tokens are encrypted with the application's
// existing at-rest key and are never included in browser responses.
const callbackJobLifetime = 8 * time.Hour

var errCallbackWorkerUnsupported = errors.New("Modal worker does not support idempotent callback jobs; deploy the updated Modal worker")
var errCallbackSubmissionRedirect = errors.New("Modal callback submission was redirected; update the configured endpoint to the final HTTPS URL")

type VideoCallbackSubmission struct {
	JobID         string `json:"job_id"`
	CallbackURL   string `json:"callback_url"`
	CallbackToken string `json:"callback_token"`
}

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
	Deadline                                      int64
}

func callbackBaseURL() (string, error) {
	raw := strings.TrimRight(strings.TrimSpace(os.Getenv("VIDEO_CALLBACK_BASE_URL")), "/")
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return "", errors.New("Set VIDEO_CALLBACK_BASE_URL to this dashboard's public HTTPS origin before generating with Modal (for example https://video.example.com).")
	}
	host := strings.ToLower(u.Hostname())
	ip := net.ParseIP(host)
	if host == "localhost" || !strings.Contains(host, ".") || (ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsLinkLocalUnicast())) {
		return "", errors.New("VIDEO_CALLBACK_BASE_URL must be reachable from Modal; use a public HTTPS dashboard address or HTTPS tunnel.")
	}
	return raw, nil
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

func (a *dashboardApp) prepareCallbackJob(request GenerateRequest, configID string) (callbackJob, error) {
	base, err := callbackBaseURL()
	if err != nil {
		return callbackJob{}, err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return callbackJob{}, err
	}
	id := "gen_" + hex.EncodeToString(raw[:16])
	token := hex.EncodeToString(raw)
	ciphertext, err := a.security.EncryptSetting("video_callback."+id, token)
	if err != nil {
		return callbackJob{}, err
	}
	return callbackJob{ID: id, Request: request, ProviderConfigID: configID, CallbackURL: base + "/api/video-callbacks/" + id, EncryptedToken: ciphertext, TokenHash: tokenHash(token), State: "submitting", Deadline: time.Now().Add(callbackJobLifetime).Unix()}, nil
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
	_, err = tx.Exec(`INSERT INTO video_callback_jobs(id,generation_id,project_id,scene_number,provider_config_id,request_json,callback_url,encrypted_token,token_hash,deadline,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,unixepoch(),unixepoch())`, job.ID, generationID, projectID, job.SceneNumber, job.ProviderConfigID, string(request), job.CallbackURL, job.EncryptedToken, job.TokenHash, job.Deadline)
	return err
}

func (s *Store) insertCallbackGeneration(job callbackJob, estimatedCost string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO generations(id,video_provider,provider_config_id,prompt,model,duration,aspect_ratio,status,estimated_cost_usd,created_at,updated_at) VALUES(?,'modal',?,?,?,?,?,'queued',?,unixepoch(),unixepoch())`, job.ID, job.ProviderConfigID, job.Request.Prompt, job.Request.Model, job.Request.Duration, job.Request.AspectRatio, estimatedCost)
	if err != nil {
		return err
	}
	job.GenerationID = job.ID
	if err := insertCallbackJob(tx, job); err != nil {
		return err
	}
	if err := appendGenerationEvent(tx, job.ID, "submission", "queued", "Generation queued for callback delivery", 0); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) insertCallbackScene(job callbackJob) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.Exec(`UPDATE project_scenes SET status='queued',provider_generation_id=?,attempts=attempts+1,error='',updated_at=unixepoch() WHERE project_id=? AND scene_number=? AND status='pending'`, job.ID, job.ProjectID, job.SceneNumber)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
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

const callbackColumns = `id,COALESCE(generation_id,''),COALESCE(project_id,''),scene_number,provider_config_id,request_json,callback_url,encrypted_token,token_hash,state,sequence,attempts,deadline`

func scanCallbackJob(row interface{ Scan(...any) error }) (callbackJob, error) {
	var job callbackJob
	var request string
	err := row.Scan(&job.ID, &job.GenerationID, &job.ProjectID, &job.SceneNumber, &job.ProviderConfigID, &request, &job.CallbackURL, &job.EncryptedToken, &job.TokenHash, &job.State, &job.Sequence, &job.Attempts, &job.Deadline)
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

func (a *dashboardApp) videoCallback(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !safeID(id) {
		writeError(w, http.StatusNotFound, "callback job not found")
		return
	}
	job, err := a.store.callbackJob(id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "callback job not found")
		} else {
			a.logger.Error("load callback job failed")
			writeError(w, http.StatusServiceUnavailable, "callback job could not be loaded; retry delivery")
		}
		return
	}
	authorization := r.Header.Get("Authorization")
	token := strings.TrimPrefix(authorization, "Bearer ")
	if token == authorization || len(token) != 64 || subtle.ConstantTimeCompare([]byte(tokenHash(token)), []byte(job.TokenHash)) != 1 {
		writeError(w, http.StatusUnauthorized, "invalid callback credential")
		return
	}
	if !isJSON(r.Header.Get("Content-Type")) {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	var callback videoCallback
	if decodeJSONBody(w, r, &callback) != nil {
		return
	}
	if callback.ID != id || callback.Sequence < 1 || callback.Sequence > 1000000 || callback.Progress < 0 || callback.Progress > 100 || (callback.Status != "processing" && callback.Status != "completed" && callback.Status != "failed") {
		writeError(w, http.StatusBadRequest, "invalid callback payload")
		return
	}
	if err := a.store.applyVideoCallback(callback); err != nil {
		writeError(w, http.StatusInternalServerError, "unable to persist callback; retry delivery")
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
		return "Modal rejected this callback job. Redeploy the updated worker and check VIDEO_CALLBACK_BASE_URL on both services, then retry."
	case "provider_unavailable":
		return "The saved Modal account configuration is unavailable. Check Settings before retrying."
	default:
		return "Modal video generation failed. Check the worker logs for this job, then retry."
	}
}

func (s *Store) applyVideoCallback(callback videoCallback) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	job, err := scanCallbackJob(tx.QueryRow(`SELECT `+callbackColumns+` FROM video_callback_jobs WHERE id=?`, callback.ID))
	if err != nil {
		return err
	}
	// First terminal state wins. Delivery retries and older progress must not
	// regress a downloaded video, a timeout, or a new scene attempt.
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
		cost = ""
	}
	if job.GenerationID != "" {
		_, err = tx.Exec(`UPDATE generations SET status=?,progress=MAX(progress,?),cost_usd=CASE WHEN ?='' THEN cost_usd ELSE ? END,error=?,updated_at=unixepoch() WHERE id=? AND status IN ('queued','processing')`, status, callback.Progress, cost, cost, message, job.GenerationID)
		if err == nil {
			err = appendGenerationEvent(tx, job.GenerationID, "callback", status, message, callback.Progress)
		}
	} else {
		result, updateErr := tx.Exec(`UPDATE project_scenes SET status=?,progress=MAX(progress,?),cost_usd=CASE WHEN ?='' THEN cost_usd ELSE ? END,error=?,updated_at=unixepoch() WHERE project_id=? AND scene_number=? AND provider_generation_id=? AND status IN ('queued','processing')`, status, callback.Progress, cost, cost, message, job.ProjectID, job.SceneNumber, job.ID)
		err = updateErr
		if err == nil {
			if n, _ := result.RowsAffected(); n == 1 {
				err = appendPipelineEvent(tx, job.ProjectID, "scene_callback", status, message, job.SceneNumber, 0, time.Now().Unix())
			}
		}
	}
	if err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE video_callback_jobs SET state=?,sequence=?,updated_at=unixepoch() WHERE id=?`, callback.Status, callback.Sequence, callback.ID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func validCallbackCost(value string) bool {
	if len(value) > 24 {
		return false
	}
	dots := 0
	for _, c := range value {
		if c == '.' {
			dots++
			if dots > 1 {
				return false
			}
		} else if c < '0' || c > '9' {
			return false
		}
	}
	return value != "."
}

func (p *Processor) processCallbackJobs(ctx context.Context) {
	rows, err := p.app.store.db.QueryContext(ctx, `SELECT `+callbackColumns+` FROM video_callback_jobs WHERE state NOT IN ('completed','failed') AND (deadline<=unixepoch() OR (state='submitting' AND next_attempt_at<=unixepoch())) ORDER BY created_at`)
	if err != nil {
		p.logger.Error("load callback jobs failed")
		return
	}
	var jobs []callbackJob
	for rows.Next() {
		job, err := scanCallbackJob(rows)
		if err != nil {
			rows.Close()
			return
		}
		jobs = append(jobs, job)
	}
	readErr := rows.Err()
	rows.Close()
	if readErr != nil {
		return
	}
	for _, job := range jobs {
		if job.Deadline <= time.Now().Unix() {
			_ = p.app.store.applyVideoCallback(videoCallback{ID: job.ID, Status: "failed", Sequence: job.Sequence + 1, ErrorCode: "callback_timeout"})
			continue
		}
		jobCopy := job
		p.startProjectTask(ctx, "callback-submit:"+job.ID, func(taskCtx context.Context) { p.submitCallbackJob(taskCtx, jobCopy) })
	}
}

func (p *Processor) submitCallbackJob(ctx context.Context, job callbackJob) {
	service, err := p.app.videoProviderForSnapshot(job.ProviderConfigID, VideoProviderModal)
	provider, ok := service.(CallbackVideoProvider)
	if err != nil || !ok {
		_ = p.app.store.applyVideoCallback(videoCallback{ID: job.ID, Status: "failed", Sequence: job.Sequence + 1, ErrorCode: "provider_unavailable"})
		return
	}
	token, err := p.app.security.DecryptSetting("video_callback."+job.ID, job.EncryptedToken)
	if err != nil {
		_ = p.app.store.applyVideoCallback(videoCallback{ID: job.ID, Status: "failed", Sequence: job.Sequence + 1, ErrorCode: "provider_unavailable"})
		return
	}
	requestCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	generation, err := provider.SubmitVideo(requestCtx, job.Request, VideoCallbackSubmission{JobID: job.ID, CallbackURL: job.CallbackURL, CallbackToken: token})
	if err == nil && (generation == nil || generation.ID != job.ID) {
		err = errCallbackWorkerUnsupported
	}
	if err != nil {
		if errors.Is(err, errCallbackSubmissionRedirect) {
			_ = p.app.store.applyVideoCallback(videoCallback{ID: job.ID, Status: "failed", Sequence: job.Sequence + 1, ErrorCode: "submission_redirect"})
			return
		}
		if errors.Is(err, errCallbackWorkerUnsupported) {
			_ = p.app.store.applyVideoCallback(videoCallback{ID: job.ID, Status: "failed", Sequence: job.Sequence + 1, ErrorCode: "callback_unsupported"})
			return
		}
		var upstream *upstreamError
		if errors.As(err, &upstream) && upstream.StatusCode >= 400 && upstream.StatusCode < 500 && upstream.StatusCode != 429 && upstream.StatusCode != 408 {
			code := "submission_rejected"
			if upstream.StatusCode == 401 || upstream.StatusCode == 403 {
				code = "submission_auth_failed"
			}
			_ = p.app.store.applyVideoCallback(videoCallback{ID: job.ID, Status: "failed", Sequence: job.Sequence + 1, ErrorCode: code})
			return
		}
		// A lost HTTP acknowledgment is ambiguous. Retry the same idempotent job,
		// never create another generation. A callback may already have arrived.
		delay := time.Second * time.Duration(1<<min(job.Attempts+1, 8))
		_, _ = p.app.store.db.Exec(`UPDATE video_callback_jobs SET attempts=attempts+1,next_attempt_at=?,updated_at=unixepoch() WHERE id=? AND state='submitting'`, time.Now().Add(delay).Unix(), job.ID)
		return
	}
	_, _ = p.app.store.db.Exec(`UPDATE video_callback_jobs SET state='accepted',attempts=attempts+1,updated_at=unixepoch() WHERE id=? AND state='submitting'`, job.ID)
}

func callbackConfigurationError(err error) string {
	if err != nil && strings.Contains(err.Error(), "VIDEO_CALLBACK_BASE_URL") {
		return err.Error()
	}
	return fmt.Sprint("Unable to persist the Modal callback job. Retry after checking the application storage.")
}
