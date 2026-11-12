package app

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ClippingGeminiJobCreate selects either an existing ready YouTube source or
// a new rights-attested public URL. Retention applies only to a new reference.
type ClippingGeminiJobCreate struct {
	SourceID       string
	YouTubeURL     string
	RightsAttested bool
	RetainUntil    int64
	Config         ClippingGeminiConfig
	Options        ClippingGeminiOptions
}

// ClippingGeminiSnapshot is immutable request intent, with no source URL or
// credentials. Before sending, reprepare against the decrypted source URL and
// require matching fingerprint and prompt/schema digests.
type ClippingGeminiSnapshot struct {
	JobID              string                `json:"job_id"`
	FingerprintVersion string                `json:"fingerprint_version"`
	Fingerprint        string                `json:"fingerprint"`
	Backend            string                `json:"backend"`
	Config             ClippingGeminiConfig  `json:"config"`
	Model              string                `json:"model"`
	APIVersion         string                `json:"api_version"`
	GenerationMode     string                `json:"generation_mode"`
	VideoProcessing    string                `json:"video_processing_mode"`
	PromptVersion      string                `json:"prompt_version"`
	PromptSHA256       string                `json:"prompt_sha256"`
	SchemaVersion      string                `json:"schema_version"`
	SchemaSHA256       string                `json:"schema_sha256"`
	Options            ClippingGeminiOptions `json:"options"`
	CreatedAt          int64                 `json:"created_at"`
}

func (s *Store) migrateClippingGeminiRequests() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS clipping_gemini_requests (
		job_id TEXT PRIMARY KEY REFERENCES clipping_jobs(id) ON DELETE RESTRICT,
		fingerprint_version TEXT NOT NULL, fingerprint TEXT NOT NULL CHECK(length(fingerprint)=64),
		backend TEXT NOT NULL, project_id TEXT NOT NULL, location TEXT NOT NULL, model TEXT NOT NULL,
		api_version TEXT NOT NULL, generation_mode TEXT NOT NULL, video_processing_mode TEXT NOT NULL,
		prompt_version TEXT NOT NULL, prompt_sha256 TEXT NOT NULL CHECK(length(prompt_sha256)=64),
		schema_version TEXT NOT NULL, schema_sha256 TEXT NOT NULL CHECK(length(schema_sha256)=64),
		options_json TEXT NOT NULL, created_at INTEGER NOT NULL
	);
	CREATE TRIGGER IF NOT EXISTS clipping_gemini_requests_immutable BEFORE UPDATE ON clipping_gemini_requests
	BEGIN SELECT RAISE(ABORT,'Gemini request snapshot is immutable'); END;
	CREATE TRIGGER IF NOT EXISTS clipping_gemini_requests_provider BEFORE INSERT ON clipping_gemini_requests
	WHEN NOT EXISTS(SELECT 1 FROM clipping_jobs WHERE id=NEW.job_id AND provider='gemini')
	BEGIN SELECT RAISE(ABORT,'Gemini request requires Gemini job'); END;`)
	return err
}

// CreateClippingGeminiJob atomically creates a source, job, stage and immutable
// request snapshot. An idempotent replay returns the original job and source;
// no provider work or local media acquisition is performed.
func (s *Store) CreateClippingGeminiJob(security *Security, input ClippingGeminiJobCreate, idempotencyKey string) (ClippingJob, bool, error) {
	if err := s.requireClippingGeminiPersistence(); err != nil {
		return ClippingJob{}, false, err
	}
	if security == nil || security.store != s {
		return ClippingJob{}, false, errors.New("source URL encryption is unavailable")
	}
	if strings.TrimSpace(idempotencyKey) == "" || len(idempotencyKey) > 128 || strings.ContainsAny(idempotencyKey, "\r\n\x00") {
		return ClippingJob{}, false, errors.New("clipping job idempotency key is required and must be at most 128 bytes")
	}
	// A caller may supply sensitive text as an idempotency key. Store only a
	// domain-separated digest so a URL can remain protected in the source row.
	keyDigest := sha256.Sum256([]byte("framevault.clipping.gemini.idempotency.v1\x00" + idempotencyKey))
	storedKey := "gemini-v1:" + hex.EncodeToString(keyDigest[:])
	if err := ValidateClippingGeminiConfig(input.Config); err != nil {
		return ClippingJob{}, false, err
	}
	if err := ValidateClippingGeminiOptions(input.Options); err != nil {
		return ClippingJob{}, false, err
	}
	newReference := input.SourceID == "" && input.YouTubeURL != ""
	if !newReference && (!safeClippingSourceID(input.SourceID) || input.YouTubeURL != "" || input.RetainUntil != 0) {
		return ClippingJob{}, false, ErrClippingGeminiInvalidRequest
	}
	var canonicalURL string
	var err error
	if newReference {
		if !input.RightsAttested {
			return ClippingJob{}, false, ErrClippingRightsNotAttested
		}
		canonicalURL, err = canonicalClippingGeminiYouTubeURL(input.YouTubeURL)
		if err != nil {
			return ClippingJob{}, false, err
		}
	}
	// The request hash is domain-separated from legacy jobs; only the digest,
	// never its URL-bearing input, is persisted in the compatibility batch.
	hashMaterial, err := json.Marshal(struct {
		Version, SourceID, URL string
		Config                 ClippingGeminiConfig
		Options                ClippingGeminiOptions
	}{"gemini-job-v1", input.SourceID, canonicalURL, input.Config, input.Options})
	if err != nil {
		return ClippingJob{}, false, err
	}
	digest := sha256.Sum256(hashMaterial)
	requestHash := hex.EncodeToString(digest[:])
	jobID, err := newClippingStorageID("clipjob_")
	if err != nil {
		return ClippingJob{}, false, err
	}
	batchID, err := newClippingStorageID("clipbatch_")
	if err != nil {
		return ClippingJob{}, false, err
	}
	sourceID := input.SourceID
	if newReference {
		sourceID, err = newClippingStorageID("clipsrc_")
		if err != nil {
			return ClippingJob{}, false, err
		}
	}
	now := time.Now().Unix()
	tx, err := s.db.Begin()
	if err != nil {
		return ClippingJob{}, false, err
	}
	defer tx.Rollback()
	var existingBatch, existingHash string
	err = tx.QueryRow(`SELECT id,request_hash FROM clipping_batches WHERE idempotency_key=?`, storedKey).Scan(&existingBatch, &existingHash)
	if err == nil {
		if existingHash != requestHash {
			return ClippingJob{}, false, ErrClippingIdempotencyConflict
		}
		var count int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM clipping_jobs WHERE batch_id=? AND provider='gemini'`, existingBatch).Scan(&count); err != nil || count != 1 {
			return ClippingJob{}, false, ErrClippingIdempotencyConflict
		}
		job, err := scanClippingJob(tx.QueryRow(`SELECT `+clippingJobColumns()+` FROM clipping_jobs WHERE batch_id=?`, existingBatch))
		if err != nil {
			return ClippingJob{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return ClippingJob{}, false, err
		}
		return job, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ClippingJob{}, false, err
	}
	if newReference {
		if input.RetainUntil < 0 || input.RetainUntil > 0 && input.RetainUntil <= now {
			return ClippingJob{}, false, errors.New("source retention must be in the future")
		}
		if _, err := insertClippingYouTubeReferenceTx(tx, security, sourceID, canonicalURL, input.RetainUntil, now); err != nil {
			return ClippingJob{}, false, err
		}
	} else {
		source, err := scanClippingSource(tx.QueryRow(`SELECT `+clippingSourceColumns()+` FROM clipping_sources WHERE id=?`, sourceID))
		if errors.Is(err, sql.ErrNoRows) {
			return ClippingJob{}, false, ErrClippingSourceNotFound
		}
		if err != nil {
			return ClippingJob{}, false, err
		}
		if (source.Kind != ClippingSourceYouTube && source.Kind != ClippingSourceYouTubeURL) || source.Status != ClippingSourceReady || !source.RightsAttested || source.RetainUntil > 0 && source.RetainUntil <= now {
			return ClippingJob{}, false, ErrClippingInvalidState
		}
		canonicalURL, err = security.DecryptSetting(clippingSourceURLSetting, source.SourceURL)
		if err != nil {
			return ClippingJob{}, false, errors.New("could not unprotect source URL")
		}
	}
	prepared, err := prepareClippingGeminiRequest(input.Config, ClippingGeminiRequest{YouTubeURL: canonicalURL, Options: input.Options})
	if err != nil {
		return ClippingJob{}, false, err
	}
	options, err := json.Marshal(input.Options)
	if err != nil {
		return ClippingJob{}, false, err
	}
	if _, err := tx.Exec(`INSERT INTO clipping_batches(id,idempotency_key,request_hash,status,budget_limit_micro_usd,created_at,updated_at) VALUES(?,?,?,'queued',1,?,?)`, batchID, storedKey, requestHash, now, now); err != nil {
		return ClippingJob{}, false, err
	}
	if _, err := tx.Exec(`INSERT INTO clipping_jobs(id,batch_id,source_id,provider,content_type,min_clip_seconds,max_clip_seconds,candidate_limit,status,budget_limit_micro_usd,created_at,updated_at) VALUES(?,?,?,'gemini',?,NULL,NULL,?,'queued',1,?,?)`, jobID, batchID, sourceID, input.Options.ContentType, input.Options.CandidateLimit, now, now); err != nil {
		return ClippingJob{}, false, err
	}
	if _, err := tx.Exec(`INSERT INTO clipping_gemini_requests(job_id,fingerprint_version,fingerprint,backend,project_id,location,model,api_version,generation_mode,video_processing_mode,prompt_version,prompt_sha256,schema_version,schema_sha256,options_json,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, jobID, clippingGeminiFingerprintVersion, prepared.Fingerprint, "gemini_enterprise_agent_platform", input.Config.ProjectID, input.Config.Location, prepared.Model, clippingGeminiAPIVersion, clippingGeminiGenerationMode, clippingGeminiVideoProcessingMode, prepared.PromptVersion, prepared.PromptSHA256, prepared.SchemaVersion, prepared.SchemaSHA256, string(options), now); err != nil {
		return ClippingJob{}, false, err
	}
	if _, err := tx.Exec(`INSERT INTO clipping_stages(job_id,name,status,idempotency_key,created_at,updated_at) VALUES(?,?,'queued',?,?,?)`, jobID, ClippingStageAnalysis, jobID+":"+ClippingStageAnalysis, now, now); err != nil {
		return ClippingJob{}, false, err
	}
	job, err := scanClippingJob(tx.QueryRow(`SELECT `+clippingJobColumns()+` FROM clipping_jobs WHERE id=?`, jobID))
	if err != nil {
		return ClippingJob{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return ClippingJob{}, false, err
	}
	return job, false, nil
}

func (s *Store) ClippingGeminiRequestSnapshot(jobID string) (ClippingGeminiSnapshot, error) {
	if err := s.requireClippingGeminiPersistence(); err != nil {
		return ClippingGeminiSnapshot{}, err
	}
	var snapshot ClippingGeminiSnapshot
	var options string
	err := s.db.QueryRow(`SELECT job_id,fingerprint_version,fingerprint,backend,project_id,location,model,api_version,generation_mode,video_processing_mode,prompt_version,prompt_sha256,schema_version,schema_sha256,options_json,created_at FROM clipping_gemini_requests WHERE job_id=?`, jobID).Scan(&snapshot.JobID, &snapshot.FingerprintVersion, &snapshot.Fingerprint, &snapshot.Backend, &snapshot.Config.ProjectID, &snapshot.Config.Location, &snapshot.Model, &snapshot.APIVersion, &snapshot.GenerationMode, &snapshot.VideoProcessing, &snapshot.PromptVersion, &snapshot.PromptSHA256, &snapshot.SchemaVersion, &snapshot.SchemaSHA256, &options, &snapshot.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ClippingGeminiSnapshot{}, ErrClippingJobNotFound
	}
	if err != nil {
		return ClippingGeminiSnapshot{}, err
	}
	if err := json.Unmarshal([]byte(options), &snapshot.Options); err != nil {
		return ClippingGeminiSnapshot{}, fmt.Errorf("invalid stored Gemini options: %w", err)
	}
	return snapshot, nil
}

// prepareClippingGeminiSnapshotRequest fails closed if code/configuration has
// changed since the durable request was created. The caller decrypts the
// source URL only immediately before use and must separately fence job/source
// state and its current attempt in its transaction.
func prepareClippingGeminiSnapshotRequest(snapshot ClippingGeminiSnapshot, sourceURL string) (*clippingGeminiPreparedRequest, error) {
	prepared, err := prepareClippingGeminiRequest(snapshot.Config, ClippingGeminiRequest{YouTubeURL: sourceURL, Options: snapshot.Options})
	if err != nil {
		return nil, ErrClippingGeminiInvalidRequest
	}
	if snapshot.FingerprintVersion != clippingGeminiFingerprintVersion || snapshot.Fingerprint != prepared.Fingerprint ||
		snapshot.Backend != "gemini_enterprise_agent_platform" || snapshot.Model != prepared.Model ||
		snapshot.APIVersion != clippingGeminiAPIVersion || snapshot.GenerationMode != clippingGeminiGenerationMode ||
		snapshot.VideoProcessing != clippingGeminiVideoProcessingMode || snapshot.PromptVersion != prepared.PromptVersion ||
		snapshot.PromptSHA256 != prepared.PromptSHA256 || snapshot.SchemaVersion != prepared.SchemaVersion ||
		snapshot.SchemaSHA256 != prepared.SchemaSHA256 {
		return nil, ErrClippingGeminiInvalidRequest
	}
	return prepared, nil
}
