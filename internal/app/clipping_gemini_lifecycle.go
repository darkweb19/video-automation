package app

import (
	"database/sql"
	"errors"
	"time"
)

type ClippingGeminiAttemptPhase string
type clippingGeminiClaimState string

const (
	ClippingGeminiAttemptClaimed    ClippingGeminiAttemptPhase = "claimed"
	ClippingGeminiAttemptSubmitting ClippingGeminiAttemptPhase = "submitting"
	ClippingGeminiAttemptSettled    ClippingGeminiAttemptPhase = "settled"

	clippingGeminiClaimActive    clippingGeminiClaimState = "active"
	clippingGeminiClaimRetryWait clippingGeminiClaimState = "retry_wait"
	clippingGeminiClaimUnknown   clippingGeminiClaimState = "unknown"
)

// ClippingGeminiUsage contains provider-reported token counts. Missing usage
// stays distinct from a provider-reported zero, including on invalid output.
type ClippingGeminiUsage struct {
	Available        bool  `json:"available"`
	PromptTokens     int64 `json:"prompt_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	ThoughtsTokens   int64 `json:"thoughts_tokens"`
	CachedTokens     int64 `json:"cached_tokens"`
	ToolPromptTokens int64 `json:"tool_prompt_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

// ClippingGeminiAttempt is a safe durable view. The lease-token hash and
// provider URL are never part of this value.
type ClippingGeminiAttempt struct {
	JobID        string
	AttemptID    string
	Attempt      int
	Phase        ClippingGeminiAttemptPhase
	Outcome      clippingGeminiPersistedOutcome
	FailureCode  string
	HTTPStatus   int
	RetryAt      int64
	LeaseExpires int64
	Usage        ClippingGeminiUsage
	ModelVersion string
	ResponseID   string
	StartedAt    int64
	FinishedAt   int64
	UpdatedAt    int64
}

type clippingGeminiAttemptLease struct {
	Attempt    ClippingGeminiAttempt
	LeaseToken string
}

var errClippingGeminiFingerprintBusy = errors.New("Gemini request fingerprint has an active or uncertain attempt")

func (s *Store) migrateClippingGeminiLifecycle() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS clipping_gemini_attempts (
		attempt_id TEXT PRIMARY KEY,
		job_id TEXT NOT NULL REFERENCES clipping_jobs(id) ON DELETE RESTRICT,
		attempt_no INTEGER NOT NULL CHECK(attempt_no>0),
		phase TEXT NOT NULL CHECK(phase IN ('claimed','submitting','settled')),
		outcome TEXT NOT NULL CHECK(outcome IN ('not_submitted','explicit_rejection','response_received','unknown')),
		failure_code TEXT NOT NULL DEFAULT '',
		http_status INTEGER NOT NULL DEFAULT 0 CHECK(http_status BETWEEN 0 AND 599),
		retry_at INTEGER NOT NULL DEFAULT 0 CHECK(retry_at>=0),
		lease_token_hash TEXT NOT NULL CHECK(length(lease_token_hash)=43),
		lease_expires_at INTEGER NOT NULL CHECK(lease_expires_at>0),
		usage_available INTEGER NOT NULL DEFAULT 0 CHECK(usage_available IN (0,1)),
		prompt_tokens INTEGER NOT NULL DEFAULT 0 CHECK(prompt_tokens>=0),
		output_tokens INTEGER NOT NULL DEFAULT 0 CHECK(output_tokens>=0),
		thoughts_tokens INTEGER NOT NULL DEFAULT 0 CHECK(thoughts_tokens>=0),
		cached_tokens INTEGER NOT NULL DEFAULT 0 CHECK(cached_tokens>=0),
		tool_prompt_tokens INTEGER NOT NULL DEFAULT 0 CHECK(tool_prompt_tokens>=0),
		total_tokens INTEGER NOT NULL DEFAULT 0 CHECK(total_tokens>=0),
		model_version TEXT NOT NULL DEFAULT '',
		response_id TEXT NOT NULL DEFAULT '',
		started_at INTEGER NOT NULL CHECK(started_at>0),
		finished_at INTEGER NOT NULL DEFAULT 0 CHECK(finished_at>=0),
		updated_at INTEGER NOT NULL CHECK(updated_at>0),
		UNIQUE(job_id,attempt_no)
	);
	CREATE INDEX IF NOT EXISTS clipping_gemini_attempts_job ON clipping_gemini_attempts(job_id,attempt_no DESC);
	CREATE INDEX IF NOT EXISTS clipping_gemini_attempts_active ON clipping_gemini_attempts(phase,lease_expires_at);
	CREATE TABLE IF NOT EXISTS clipping_gemini_execution_claims (
		fingerprint TEXT PRIMARY KEY CHECK(length(fingerprint)=64),
		job_id TEXT NOT NULL REFERENCES clipping_jobs(id) ON DELETE RESTRICT,
		attempt_id TEXT NOT NULL REFERENCES clipping_gemini_attempts(attempt_id) ON DELETE RESTRICT,
		state TEXT NOT NULL CHECK(state IN ('active','retry_wait','unknown')),
		retry_at INTEGER NOT NULL DEFAULT 0 CHECK(retry_at>=0),
		lease_expires_at INTEGER NOT NULL CHECK(lease_expires_at>0),
		created_at INTEGER NOT NULL CHECK(created_at>0),
		updated_at INTEGER NOT NULL CHECK(updated_at>0)
	);
	CREATE INDEX IF NOT EXISTS clipping_gemini_claims_job ON clipping_gemini_execution_claims(job_id,state);
	CREATE TABLE IF NOT EXISTS clipping_gemini_cache_reuse_provenance (
		job_id TEXT PRIMARY KEY REFERENCES clipping_gemini_cache_reuses(job_id) ON DELETE RESTRICT,
		origin_created_at INTEGER NOT NULL CHECK(origin_created_at>0),
		origin_expires_at INTEGER NOT NULL CHECK(origin_expires_at>origin_created_at)
	);`)
	return err
}

func scanClippingGeminiAttempt(scanner interface{ Scan(...any) error }) (ClippingGeminiAttempt, error) {
	var attempt ClippingGeminiAttempt
	var available int
	err := scanner.Scan(
		&attempt.JobID, &attempt.AttemptID, &attempt.Attempt, &attempt.Phase, &attempt.Outcome,
		&attempt.FailureCode, &attempt.HTTPStatus, &attempt.RetryAt, &attempt.LeaseExpires,
		&available, &attempt.Usage.PromptTokens, &attempt.Usage.OutputTokens, &attempt.Usage.ThoughtsTokens,
		&attempt.Usage.CachedTokens, &attempt.Usage.ToolPromptTokens, &attempt.Usage.TotalTokens,
		&attempt.ModelVersion, &attempt.ResponseID, &attempt.StartedAt, &attempt.FinishedAt, &attempt.UpdatedAt,
	)
	if err != nil {
		return ClippingGeminiAttempt{}, err
	}
	if available != 0 && available != 1 {
		return ClippingGeminiAttempt{}, ErrClippingInvalidState
	}
	attempt.Usage.Available = available == 1
	return attempt, nil
}

const clippingGeminiAttemptColumns = `job_id,attempt_id,attempt_no,phase,outcome,failure_code,http_status,retry_at,lease_expires_at,
	usage_available,prompt_tokens,output_tokens,thoughts_tokens,cached_tokens,tool_prompt_tokens,total_tokens,
	model_version,response_id,started_at,finished_at,updated_at`

func (s *Store) ClippingGeminiAttempts(jobID string) ([]ClippingGeminiAttempt, error) {
	if s == nil || s.db == nil || !safeID(jobID) || len(jobID) < len("clipjob_") || jobID[:len("clipjob_")] != "clipjob_" {
		return nil, ErrClippingJobNotFound
	}
	if _, err := s.ClippingJob(jobID); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT `+clippingGeminiAttemptColumns+` FROM clipping_gemini_attempts WHERE job_id=? ORDER BY attempt_no`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	attempts := make([]ClippingGeminiAttempt, 0)
	for rows.Next() {
		attempt, err := scanClippingGeminiAttempt(rows)
		if err != nil {
			return nil, err
		}
		attempts = append(attempts, attempt)
	}
	return attempts, rows.Err()
}

func (s *Store) ClippingGeminiLatestAttempt(jobID string) (ClippingGeminiAttempt, error) {
	if s == nil || s.db == nil {
		return ClippingGeminiAttempt{}, ErrClippingInvalidState
	}
	attempt, err := scanClippingGeminiAttempt(s.db.QueryRow(`SELECT `+clippingGeminiAttemptColumns+` FROM clipping_gemini_attempts WHERE job_id=? ORDER BY attempt_no DESC LIMIT 1`, jobID))
	if errors.Is(err, sql.ErrNoRows) {
		return ClippingGeminiAttempt{}, ErrClippingStageNotFound
	}
	return attempt, err
}

func clippingGeminiNowUnix() int64 { return time.Now().Unix() }
