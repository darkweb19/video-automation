package app

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

const clippingGeminiCacheTTLSeconds = int64((24 * time.Hour) / time.Second)

func (s *Store) migrateClippingGeminiCache() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS clipping_gemini_cache (
		source_id TEXT NOT NULL REFERENCES clipping_sources(id) ON DELETE RESTRICT,
		fingerprint TEXT NOT NULL CHECK(length(fingerprint)=64),
		origin_job_id TEXT NOT NULL REFERENCES clipping_jobs(id) ON DELETE RESTRICT,
		created_at INTEGER NOT NULL CHECK(created_at>0),
		expires_at INTEGER NOT NULL CHECK(expires_at>created_at AND expires_at<=created_at+86400),
		PRIMARY KEY(source_id,fingerprint)
	); CREATE INDEX IF NOT EXISTS clipping_gemini_cache_origin ON clipping_gemini_cache(origin_job_id);
	CREATE TABLE IF NOT EXISTS clipping_gemini_cache_reuses (
		job_id TEXT PRIMARY KEY REFERENCES clipping_jobs(id) ON DELETE RESTRICT,
		origin_job_id TEXT NOT NULL REFERENCES clipping_jobs(id) ON DELETE RESTRICT,
		created_at INTEGER NOT NULL CHECK(created_at>0)
	);`)
	return err
}

// clippingGeminiCacheIntentTx validates the persisted intent and source without
// consulting mutable configuration or exposing a URL or media identity.
func clippingGeminiCacheIntentTx(tx *sql.Tx, jobID string, now int64) (ClippingJob, ClippingGeminiOptions, string, int64, error) {
	job, err := scanClippingJob(tx.QueryRow(`SELECT `+clippingJobColumns()+` FROM clipping_jobs WHERE id=?`, jobID))
	if err != nil {
		return ClippingJob{}, ClippingGeminiOptions{}, "", 0, err
	}
	if job.Provider != ClippingProviderGemini || job.AnalysisDeleted {
		return ClippingJob{}, ClippingGeminiOptions{}, "", 0, ErrClippingInvalidState
	}
	var fingerprint, optionsJSON, kind, status string
	var rights int
	var retention int64
	if err := tx.QueryRow(`SELECT request.fingerprint,request.options_json,source.kind,source.status,source.rights_attested,source.retain_until
		FROM clipping_gemini_requests AS request JOIN clipping_sources AS source ON source.id=? WHERE request.job_id=?`, job.SourceID, job.ID).Scan(&fingerprint, &optionsJSON, &kind, &status, &rights, &retention); err != nil {
		return ClippingJob{}, ClippingGeminiOptions{}, "", 0, err
	}
	if !validLowerHexDigest(fingerprint) || (ClippingSourceKind(kind) != ClippingSourceYouTubeURL && ClippingSourceKind(kind) != ClippingSourceYouTube) || status != string(ClippingSourceReady) || rights != 1 || retention > 0 && retention <= now {
		return ClippingJob{}, ClippingGeminiOptions{}, "", 0, ErrClippingInvalidState
	}
	var options ClippingGeminiOptions
	if err := json.Unmarshal([]byte(optionsJSON), &options); err != nil || ValidateClippingGeminiOptions(options) != nil {
		return ClippingJob{}, ClippingGeminiOptions{}, "", 0, ErrClippingInvalidState
	}
	return job, options, fingerprint, retention, nil
}

// storeClippingGeminiCacheTx records only an already-completed origin job's
// validated v3 artifact. Call inside the same transaction that completes it.
func storeClippingGeminiCacheTx(tx *sql.Tx, jobID string, now int64) error {
	if now <= 0 {
		return ErrClippingInvalidState
	}
	job, options, fingerprint, retention, err := clippingGeminiCacheIntentTx(tx, jobID, now)
	if err != nil {
		return err
	}
	if job.Status != ClippingJobCompleted {
		return ErrClippingInvalidState
	}
	var reused int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM clipping_gemini_cache_reuses WHERE job_id=?`, job.ID).Scan(&reused); err != nil {
		return err
	}
	if reused != 0 {
		return ErrClippingInvalidState
	}
	artifact, err := clippingGeminiOriginArtifactTx(tx, job.ID)
	if err != nil {
		return err
	}
	if _, err := decodeClippingGeminiArtifact(artifact, options); err != nil {
		return err
	}
	// The artifact timestamp is the original provider result time; reindexing
	// a completed job cannot refresh the lifetime of an old result.
	if artifact.CreatedAt <= 0 || artifact.CreatedAt > now {
		return ErrClippingInvalidState
	}
	expires := artifact.CreatedAt + clippingGeminiCacheTTLSeconds
	if expires <= now {
		return ErrClippingInvalidState
	}
	if retention > 0 && retention < expires {
		expires = retention
	}
	if expires <= now {
		return ErrClippingInvalidState
	}
	_, err = tx.Exec(`INSERT INTO clipping_gemini_cache(source_id,fingerprint,origin_job_id,created_at,expires_at) VALUES(?,?,?,?,?)
		ON CONFLICT(source_id,fingerprint) DO UPDATE SET origin_job_id=excluded.origin_job_id,created_at=excluded.created_at,
		expires_at=CASE WHEN clipping_gemini_cache.origin_job_id=excluded.origin_job_id THEN MIN(clipping_gemini_cache.expires_at,excluded.expires_at) ELSE excluded.expires_at END`, job.SourceID, fingerprint, job.ID, artifact.CreatedAt, expires)
	return err
}

func clippingGeminiOriginArtifactTx(tx *sql.Tx, jobID string) (ClippingArtifact, error) {
	var artifact ClippingArtifact
	var ranges string
	artifact.JobID = jobID
	err := tx.QueryRow(`SELECT artifact_type,schema_version,version,source_duration_ms,time_ranges_json,payload_json,created_at FROM clipping_artifacts WHERE job_id=? AND artifact_type=? AND version=1`, jobID, clippingAnalysisArtifactType).Scan(&artifact.Type, &artifact.SchemaVersion, &artifact.Version, &artifact.SourceDurationMS, &ranges, &artifact.PayloadJSON, &artifact.CreatedAt)
	if err != nil {
		return ClippingArtifact{}, err
	}
	if err := json.Unmarshal([]byte(ranges), &artifact.TimeRanges); err != nil {
		return ClippingArtifact{}, ErrClippingInvalidArtifact
	}
	return artifact, nil
}

// clippingGeminiCacheHit records the original fresh-inference job and expiry;
// a reused result must never be presented as a new provider inference.
type clippingGeminiCacheHit struct {
	OriginJobID string
	ExpiresAt   int64
	Artifact    ClippingArtifact
}

// loadClippingGeminiCacheTx records reuse in the caller's transaction before
// returning a validated artifact. Commit only with the target's publication;
// rollback removes the reuse record. A reused job cannot refresh the cache TTL.
func loadClippingGeminiCacheTx(tx *sql.Tx, jobID string, now int64) (clippingGeminiCacheHit, bool, error) {
	if now <= 0 {
		return clippingGeminiCacheHit{}, false, ErrClippingInvalidState
	}
	job, options, fingerprint, _, err := clippingGeminiCacheIntentTx(tx, jobID, now)
	if err != nil {
		return clippingGeminiCacheHit{}, false, err
	}
	if job.Status != ClippingJobQueued && job.Status != ClippingJobRunning {
		return clippingGeminiCacheHit{}, false, ErrClippingInvalidState
	}
	var hit clippingGeminiCacheHit
	err = tx.QueryRow(`SELECT cache.origin_job_id,cache.expires_at FROM clipping_gemini_cache AS cache
		JOIN clipping_jobs AS origin ON origin.id=cache.origin_job_id
		JOIN clipping_gemini_requests AS request ON request.job_id=origin.id
		WHERE cache.source_id=? AND cache.fingerprint=? AND cache.created_at<=? AND cache.expires_at>? AND cache.expires_at<=cache.created_at+86400
		AND origin.source_id=cache.source_id AND origin.provider='gemini' AND origin.status='completed' AND origin.analysis_deleted=0 AND request.fingerprint=cache.fingerprint`, job.SourceID, fingerprint, now, now).Scan(&hit.OriginJobID, &hit.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return clippingGeminiCacheHit{}, false, nil
	}
	if err != nil {
		return clippingGeminiCacheHit{}, false, err
	}
	artifact, err := clippingGeminiOriginArtifactTx(tx, hit.OriginJobID)
	if err != nil {
		return clippingGeminiCacheHit{}, false, err
	}
	if _, err := decodeClippingGeminiArtifact(artifact, options); err != nil {
		return clippingGeminiCacheHit{}, false, err
	}
	if _, err := tx.Exec(`INSERT INTO clipping_gemini_cache_reuses(job_id,origin_job_id,created_at) VALUES(?,?,?) ON CONFLICT(job_id) DO NOTHING`, job.ID, hit.OriginJobID, now); err != nil {
		return clippingGeminiCacheHit{}, false, err
	}
	var recordedOrigin string
	if err := tx.QueryRow(`SELECT origin_job_id FROM clipping_gemini_cache_reuses WHERE job_id=?`, job.ID).Scan(&recordedOrigin); err != nil || recordedOrigin != hit.OriginJobID {
		return clippingGeminiCacheHit{}, false, ErrClippingInvalidState
	}
	artifact.JobID, artifact.CreatedAt = jobID, 0
	hit.Artifact = artifact
	return hit, true, nil
}
