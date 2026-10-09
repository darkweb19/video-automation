package app

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This fixture models the M2 clipping tables before M3 added selection,
// estimate, and callback-accounting columns. The records intentionally exist
// before OpenStore runs so the test covers SQLite's additive-column defaults.
const clippingM2SchemaWithoutM3Columns = `
CREATE TABLE clipping_sources (
	id TEXT PRIMARY KEY,
	kind TEXT NOT NULL CHECK(kind IN ('upload','youtube_original_file','google_drive_public','dropbox_public')),
	status TEXT NOT NULL CHECK(status IN ('uploading','importing','ready','failed','deleted')),
	original_name TEXT NOT NULL DEFAULT '',
	media_type TEXT NOT NULL DEFAULT '',
	source_url TEXT NOT NULL DEFAULT '',
	rights_attested INTEGER NOT NULL DEFAULT 0,
	storage_path TEXT NOT NULL UNIQUE,
	failure TEXT NOT NULL DEFAULT '',
	declared_size_bytes INTEGER NOT NULL,
	reserved_size_bytes INTEGER NOT NULL DEFAULT 0,
	size_bytes INTEGER NOT NULL DEFAULT 0,
	upload_offset INTEGER NOT NULL DEFAULT 0,
	duration_ms INTEGER NOT NULL DEFAULT 0,
	sha256 TEXT NOT NULL DEFAULT '',
	retain_until INTEGER NOT NULL DEFAULT 0,
	import_attempts INTEGER NOT NULL DEFAULT 0,
	next_import_at INTEGER NOT NULL DEFAULT 0,
	import_lease_until INTEGER NOT NULL DEFAULT 0,
	media_lease_until INTEGER NOT NULL DEFAULT 0,
	cleanup_complete INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	deleted_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE clipping_batches (
	id TEXT PRIMARY KEY,
	idempotency_key TEXT NOT NULL UNIQUE,
	request_hash TEXT NOT NULL,
	status TEXT NOT NULL,
	budget_limit_micro_usd INTEGER NOT NULL,
	reserved_micro_usd INTEGER NOT NULL DEFAULT 0,
	spent_micro_usd INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);
CREATE TABLE clipping_jobs (
	id TEXT PRIMARY KEY,
	batch_id TEXT NOT NULL REFERENCES clipping_batches(id) ON DELETE RESTRICT,
	source_id TEXT NOT NULL REFERENCES clipping_sources(id) ON DELETE RESTRICT,
	status TEXT NOT NULL,
	budget_limit_micro_usd INTEGER NOT NULL,
	reserved_micro_usd INTEGER NOT NULL DEFAULT 0,
	spent_micro_usd INTEGER NOT NULL DEFAULT 0,
	error TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);
CREATE TABLE clipping_stages (
	job_id TEXT NOT NULL REFERENCES clipping_jobs(id) ON DELETE RESTRICT,
	name TEXT NOT NULL,
	status TEXT NOT NULL,
	idempotency_key TEXT NOT NULL UNIQUE,
	attempt_id TEXT NOT NULL DEFAULT '',
	attempt INTEGER NOT NULL DEFAULT 0,
	lease_expires_at INTEGER NOT NULL DEFAULT 0,
	reserved_micro_usd INTEGER NOT NULL DEFAULT 0,
	actual_micro_usd INTEGER NOT NULL DEFAULT 0,
	error TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	PRIMARY KEY(job_id,name)
);
CREATE TABLE clipping_stage_attempts (
	attempt_id TEXT PRIMARY KEY,
	job_id TEXT NOT NULL,
	stage_name TEXT NOT NULL,
	attempt_no INTEGER NOT NULL,
	idempotency_key TEXT NOT NULL,
	token_hash TEXT NOT NULL,
	status TEXT NOT NULL,
	lease_expires_at INTEGER NOT NULL,
	reserved_micro_usd INTEGER NOT NULL,
	actual_micro_usd INTEGER NOT NULL DEFAULT 0,
	started_at INTEGER NOT NULL,
	settled_at INTEGER NOT NULL DEFAULT 0,
	error TEXT NOT NULL DEFAULT '',
	FOREIGN KEY(job_id,stage_name) REFERENCES clipping_stages(job_id,name) ON DELETE RESTRICT,
	UNIQUE(job_id,stage_name,attempt_no)
);
CREATE TABLE clipping_artifacts (
	job_id TEXT NOT NULL REFERENCES clipping_jobs(id) ON DELETE RESTRICT,
	artifact_type TEXT NOT NULL,
	schema_version TEXT NOT NULL,
	version INTEGER NOT NULL,
	source_duration_ms INTEGER NOT NULL,
	time_ranges_json TEXT NOT NULL,
	payload_json TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	PRIMARY KEY(job_id,artifact_type,version)
);
`

func TestClippingM2SchemaAdditivelyMigratesAndAcceptsV1Callback(t *testing.T) {
	dataDir := t.TempDir()
	legacyLease := strings.Repeat("d", 64)
	sourceID := "clipsrc_m2_migration"
	batchID := "clipbatch_m2_migration"
	jobID := "clipjob_m2_migration"
	stageName := ClippingStageAnalysis
	attemptID := "clipatt_m2_migration"
	const sourceDurationMS int64 = 60_000
	now := time.Now().Unix()
	mediaPath := filepath.Join(dataDir, "clipping", sourceID, "source.media")
	if err := os.MkdirAll(filepath.Dir(mediaPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mediaPath, []byte("legacy m2 source"), 0o600); err != nil {
		t.Fatal(err)
	}

	legacyDB, err := sql.Open("sqlite", filepath.Join(dataDir, "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	legacyDB.SetMaxOpenConns(1)
	if _, err := legacyDB.Exec(clippingM2SchemaWithoutM3Columns); err != nil {
		_ = legacyDB.Close()
		t.Fatalf("create M2 schema fixture: %v", err)
	}
	_, err = legacyDB.Exec(`INSERT INTO clipping_sources(
		id,kind,status,original_name,media_type,rights_attested,storage_path,declared_size_bytes,
		reserved_size_bytes,size_bytes,duration_ms,sha256,retain_until,created_at,updated_at
	) VALUES(?,?,?,?,?,1,?,?,?,?,?,?,?,?,?)`,
		sourceID, string(ClippingSourceUpload), string(ClippingSourceReady), "legacy.mp4", "video/mp4",
		mediaPath, int64(len("legacy m2 source")), int64(len("legacy m2 source")), int64(len("legacy m2 source")),
		sourceDurationMS, strings.Repeat("b", 64), now+86400, now, now,
	)
	if err != nil {
		_ = legacyDB.Close()
		t.Fatalf("seed M2 source: %v", err)
	}
	_, err = legacyDB.Exec(`INSERT INTO clipping_batches(
		id,idempotency_key,request_hash,status,budget_limit_micro_usd,reserved_micro_usd,created_at,updated_at
	) VALUES(?,?,?,?,?,?,?,?)`, batchID, "m2-migration-request", strings.Repeat("c", 64), "running", 5_000, 100, now, now)
	if err != nil {
		_ = legacyDB.Close()
		t.Fatalf("seed M2 batch: %v", err)
	}
	_, err = legacyDB.Exec(`INSERT INTO clipping_jobs(
		id,batch_id,source_id,status,budget_limit_micro_usd,reserved_micro_usd,created_at,updated_at
	) VALUES(?,?,?,?,?,?,?,?)`, jobID, batchID, sourceID, "running", 2_000, 100, now, now)
	if err != nil {
		_ = legacyDB.Close()
		t.Fatalf("seed M2 job: %v", err)
	}
	stageKey := jobID + ":" + stageName
	leaseUntil := now + 3600
	_, err = legacyDB.Exec(`INSERT INTO clipping_stages(
		job_id,name,status,idempotency_key,attempt_id,attempt,lease_expires_at,reserved_micro_usd,created_at,updated_at
	) VALUES(?,?,'running',?,?,1,?,100,?,?)`, jobID, stageName, stageKey, attemptID, leaseUntil, now, now)
	if err != nil {
		_ = legacyDB.Close()
		t.Fatalf("seed M2 stage: %v", err)
	}
	_, err = legacyDB.Exec(`INSERT INTO clipping_stage_attempts(
		attempt_id,job_id,stage_name,attempt_no,idempotency_key,token_hash,status,lease_expires_at,reserved_micro_usd,started_at
	) VALUES(?,?,?,?,?,?,'running',?,100,?)`,
		attemptID, jobID, stageName, 1, stageKey, tokenHash(legacyLease), leaseUntil, now,
	)
	if err != nil {
		_ = legacyDB.Close()
		t.Fatalf("seed M2 attempt: %v", err)
	}
	if err := legacyDB.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := OpenStore(dataDir)
	if err != nil {
		t.Fatalf("open M2 database through additive migration: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	job, err := store.ClippingJob(jobID)
	if err != nil || job.ContentType != "general" || job.MinClipSeconds != 15 || job.MaxClipSeconds != 180 || job.CandidateLimit != 10 {
		t.Fatalf("M3 selection defaults on old job: job=%+v error=%v", job, err)
	}
	stages, err := store.ClippingStages(jobID)
	if err != nil || len(stages) != 1 || stages[0].EstimatedMicroUSD != 0 || !stages[0].CostReconciled {
		t.Fatalf("M3 defaults on old stage: stages=%+v error=%v", stages, err)
	}
	var attemptEstimate, attemptReconciled, operatorRate, maxCompute, callbackReceived int64
	var reference, callbackDigest string
	if err := store.db.QueryRow(`SELECT estimated_micro_usd,cost_reconciled,operator_rate_micro_usd_per_second,
		max_compute_seconds,reconciliation_reference,callback_received_at,callback_digest
		FROM clipping_stage_attempts WHERE attempt_id=?`, attemptID).Scan(
		&attemptEstimate, &attemptReconciled, &operatorRate, &maxCompute, &reference, &callbackReceived, &callbackDigest,
	); err != nil {
		t.Fatal(err)
	}
	if attemptEstimate != 0 || attemptReconciled != 1 || operatorRate != 0 || maxCompute != 0 || reference != "" || callbackReceived != 0 || callbackDigest != "" {
		t.Fatalf("M3 defaults on old attempt: estimate=%d reconciled=%d rate=%d max=%d reference=%q callback_at=%d digest=%q",
			attemptEstimate, attemptReconciled, operatorRate, maxCompute, reference, callbackReceived, callbackDigest)
	}

	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	app := &dashboardApp{store: store, security: security}
	stage := stages[0]
	stage.LeaseToken = legacyLease
	_, callbackToken, err := app.persistClippingDispatchCapabilities(stage)
	if err != nil {
		t.Fatalf("persist callback capabilities for migrated attempt: %v", err)
	}
	handler := newDashboardHandler(app)
	callback := clippingWorkerCallback{
		ProtocolVersion:    clippingProtocolVersionV1,
		JobID:              jobID,
		Stage:              stageName,
		DispatchID:         stage.IdempotencyKey,
		AttemptID:          attemptID,
		Status:             "completed",
		ActualCostMicroUSD: 42,
		Artifact: clippingCallbackArtifact{
			Type: "clipping.analysis", SchemaVersion: "1", Version: 1,
			SourceDurationMS: sourceDurationMS,
			TimeRanges:       []ClippingTimeRange{{StartMS: 0, EndMS: sourceDurationMS}},
			Payload:          json.RawMessage(`{"fixture":"legacy-v1"}`),
		},
	}
	invalidLegacy := callback
	invalidLegacy.CostEstimateMicroUSD = 1
	if response := clippingM2CallbackRequest(handler, jobID, attemptID, callbackToken, invalidLegacy); response.Code != http.StatusBadRequest {
		t.Fatalf("v1 callback carrying an M3 estimate should be rejected: status=%d body=%s", response.Code, response.Body.String())
	}
	stillRunning, err := store.ClippingJob(jobID)
	if err != nil || stillRunning.Status != ClippingJobRunning || stillRunning.ReservedMicroUSD != 100 || stillRunning.SpentMicroUSD != 0 {
		t.Fatalf("rejected v1 payload changed the M2 attempt: job=%+v error=%v", stillRunning, err)
	}
	if response := clippingM2CallbackRequest(handler, jobID, attemptID, callbackToken, callback); response.Code != http.StatusNoContent {
		t.Fatalf("legacy v1 callback after additive migration: status=%d body=%s", response.Code, response.Body.String())
	}
	if response := clippingM2CallbackRequest(handler, jobID, attemptID, callbackToken, callback); response.Code != http.StatusNoContent {
		t.Fatalf("duplicate legacy v1 callback: status=%d body=%s", response.Code, response.Body.String())
	}
	settledJob, err := store.ClippingJob(jobID)
	if err != nil || settledJob.Status != ClippingJobCompleted || settledJob.ReservedMicroUSD != 0 || settledJob.SpentMicroUSD != 42 {
		t.Fatalf("legacy v1 settlement job=%+v error=%v", settledJob, err)
	}
	settledBatch, err := store.ClippingBatch(batchID)
	if err != nil || settledBatch.ReservedMicroUSD != 0 || settledBatch.SpentMicroUSD != 42 {
		t.Fatalf("legacy v1 settlement batch=%+v error=%v", settledBatch, err)
	}
	settledStages, err := store.ClippingStages(jobID)
	if err != nil || len(settledStages) != 1 || settledStages[0].Status != ClippingStageCompleted || settledStages[0].ActualMicroUSD != 42 {
		t.Fatalf("legacy v1 settlement stage=%+v error=%v", settledStages, err)
	}
	artifacts, err := store.ClippingArtifacts(jobID)
	if err != nil || len(artifacts) != 1 || artifacts[0].SchemaVersion != "1" || artifacts[0].PayloadJSON != `{"fixture":"legacy-v1"}` {
		t.Fatalf("legacy v1 artifact after migration=%+v error=%v", artifacts, err)
	}
}

func clippingM2CallbackRequest(handler http.Handler, jobID, attemptID, token string, callback clippingWorkerCallback) *httptest.ResponseRecorder {
	body, err := json.Marshal(callback)
	if err != nil {
		panic(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/clipping/worker-callbacks/"+jobID+"/"+attemptID, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
