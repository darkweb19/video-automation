package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type legacyInflightFixture struct {
	store         *Store
	handler       http.Handler
	sourceID      string
	jobID         string
	attemptID     string
	mediaToken    string
	callbackToken string
	callback      clippingWorkerCallback
}

// Seed a previously submitted v2 attempt directly. These tests must keep
// working after the public and Store creation/dispatch paths are retired.
func newLegacyInflightFixture(t *testing.T) legacyInflightFixture {
	t.Helper()
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, 4)
	for _, prefix := range []string{"clipsrc_", "clipbatch_", "clipjob_", "clipatt_"} {
		id, err := newClippingStorageID(prefix)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	sourceID, batchID, jobID, attemptID := ids[0], ids[1], ids[2], ids[3]
	content := []byte("synthetic retained source")
	directory := filepath.Join(store.clippingDir, sourceID)
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "source.media")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("../../testdata/clipping_legacy/video_only_source_core.v2.json")
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	var core clippingAnalysisCore
	if err := json.Unmarshal(data, &core); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]any{
		"cost_basis": clippingWorkerProtocolCostBasis, "rate_micro_usd_per_second": int64(1),
		"compute_seconds": int64(10), "cost_estimate_micro_usd": int64(10),
	} {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		payload[key] = encoded
	}
	encodedPayload, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	leaseExpiry, callbackExpiry := now+600, now+3600
	leaseToken, mediaToken, callbackToken := strings.Repeat("a", 64), strings.Repeat("m", 43), strings.Repeat("c", 43)
	stageKey := jobID + ":analysis"
	for _, row := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO clipping_sources(id,kind,status,media_type,rights_attested,storage_path,declared_size_bytes,reserved_size_bytes,size_bytes,duration_ms,sha256,retain_until,created_at,updated_at)
			VALUES(?,'upload','ready','video/mp4',1,?,?,?,?,?,?,?, ?,?)`, []any{sourceID, path, len(content), len(content), len(content), core.SourceDurationMS, core.SourceSHA256, now + 7200, now, now}},
		{`INSERT INTO clipping_batches(id,idempotency_key,request_hash,status,budget_limit_micro_usd,created_at,updated_at) VALUES(?,?,?,'running',1,?,?)`, []any{batchID, "idempotency-" + jobID, "hash", now, now}},
		{`INSERT INTO clipping_jobs(id,batch_id,source_id,provider,content_type,min_clip_seconds,max_clip_seconds,candidate_limit,status,budget_limit_micro_usd,created_at,updated_at)
			VALUES(?,?,?,'modal_legacy','general',15,180,10,'running',1,?,?)`, []any{jobID, batchID, sourceID, now, now}},
		{`INSERT INTO clipping_stages(job_id,name,status,idempotency_key,attempt_id,attempt,lease_expires_at,created_at,updated_at)
			VALUES(?,'analysis','running',?,?,1,?,?,?)`, []any{jobID, stageKey, attemptID, leaseExpiry, now, now}},
		{`INSERT INTO clipping_stage_attempts(attempt_id,job_id,stage_name,attempt_no,idempotency_key,token_hash,status,lease_expires_at,reserved_micro_usd,pipeline_revision,max_compute_seconds,started_at)
			VALUES(?,?,'analysis',1,?,?,'running',?,0,?,120,?)`, []any{attemptID, jobID, stageKey, tokenHash(leaseToken), leaseExpiry, core.PipelineRevision, now}},
		{`INSERT INTO clipping_analysis_claims(source_id,source_sha256,pipeline_key,job_id,attempt_id,claimed_at) VALUES(?,?,?,?,?,?)`, []any{sourceID, core.SourceSHA256, core.PipelineRevision, jobID, attemptID, now}},
		{`INSERT INTO clipping_capability_expiry_index(job_id,attempt_id,expires_at) VALUES(?,?,?)`, []any{jobID, attemptID, callbackExpiry}},
	} {
		if _, err := store.db.Exec(row.query, row.args...); err != nil {
			t.Fatalf("seed legacy attempt: %v", err)
		}
	}
	for scope, value := range map[string]string{
		"lease": leaseToken, "media": mediaToken, "callback": callbackToken,
		"media_expiry": strconv.FormatInt(leaseExpiry, 10), "callback_expiry": strconv.FormatInt(callbackExpiry, 10),
	} {
		key := clippingCapabilitySettingKey(jobID, attemptID, scope)
		ciphertext, err := security.EncryptSetting(key, value)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.SetSetting(key, ciphertext); err != nil {
			t.Fatal(err)
		}
	}
	return legacyInflightFixture{
		store: store, handler: newDashboardHandler(&dashboardApp{store: store, security: security}), sourceID: sourceID,
		jobID: jobID, attemptID: attemptID, mediaToken: mediaToken, callbackToken: callbackToken,
		callback: clippingWorkerCallback{
			ProtocolVersion: clippingProtocolVersion, JobID: jobID, Stage: ClippingStageAnalysis,
			DispatchID: stageKey, AttemptID: attemptID, Status: "completed", CostEstimateMicroUSD: 10,
			CostBasis: clippingWorkerProtocolCostBasis,
			Artifact: clippingCallbackArtifact{Type: clippingAnalysisArtifactType, SchemaVersion: clippingAnalysisSchemaVersion,
				Version: 1, SourceDurationMS: core.SourceDurationMS, TimeRanges: []ClippingTimeRange{}, Payload: encodedPayload},
		},
	}
}

func (f legacyInflightFixture) sendCallback(t *testing.T, token string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(f.callback)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/clipping/worker-callbacks/"+f.jobID+"/"+f.attemptID, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, request)
	return response
}

func TestLegacyInflightMediaAndCallbackRemainAvailable(t *testing.T) {
	f := newLegacyInflightFixture(t)
	request := httptest.NewRequest(http.MethodGet, "/api/clipping/worker-media/"+f.jobID+"/"+f.attemptID, nil)
	request.Header.Set("Authorization", "Bearer "+f.mediaToken)
	request.Header.Set("Range", "bytes=0-8")
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, request)
	if response.Code != http.StatusPartialContent || response.Body.String() != "synthetic" {
		t.Fatalf("issued media capability: status=%d body=%q", response.Code, response.Body.String())
	}
	if response := f.sendCallback(t, "wrong-token"); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized callback status=%d", response.Code)
	}
	for i := 0; i < 2; i++ {
		if response := f.sendCallback(t, f.callbackToken); response.Code != http.StatusNoContent {
			t.Fatalf("callback %d status=%d body=%s", i, response.Code, response.Body.String())
		}
	}
	job, err := f.store.ClippingJob(f.jobID)
	if err != nil || job.Status != ClippingJobCompleted {
		t.Fatalf("settled legacy job=%+v error=%v", job, err)
	}
	artifacts, err := f.store.ClippingArtifacts(f.jobID)
	if err != nil || len(artifacts) != 1 {
		t.Fatalf("settled legacy artifacts=%+v error=%v", artifacts, err)
	}
	var result struct {
		Candidates []json.RawMessage `json:"candidates"`
	}
	if err := json.Unmarshal([]byte(artifacts[0].PayloadJSON), &result); err != nil || len(result.Candidates) == 0 {
		t.Fatalf("legacy v2 callback lost candidates: count=%d error=%v", len(result.Candidates), err)
	}
}

func TestLegacyInflightDeletedJobSettlesWithoutRepublishing(t *testing.T) {
	f := newLegacyInflightFixture(t)
	if err := f.store.DeleteClippingAnalysis(f.jobID); err != nil {
		t.Fatal(err)
	}
	if response := f.sendCallback(t, f.callbackToken); response.Code != http.StatusNoContent {
		t.Fatalf("late callback status=%d body=%s", response.Code, response.Body.String())
	}
	var status string
	var received int64
	if err := f.store.db.QueryRow(`SELECT status,callback_received_at FROM clipping_stage_attempts WHERE attempt_id=?`, f.attemptID).Scan(&status, &received); err != nil {
		t.Fatal(err)
	}
	artifacts, err := f.store.ClippingArtifacts(f.jobID)
	if err != nil || status != "settled_canceled" || received == 0 || len(artifacts) != 0 {
		t.Fatalf("deleted attempt status=%s received=%d artifacts=%d error=%v", status, received, len(artifacts), err)
	}
}

func TestLegacyInflightUncertainAttemptAcceptsIssuedLateCallback(t *testing.T) {
	f := newLegacyInflightFixture(t)
	expired := time.Now().Unix() - 10
	if _, err := f.store.db.Exec(`UPDATE clipping_stage_attempts SET status='expired',lease_expires_at=? WHERE attempt_id=?`, expired, f.attemptID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.db.Exec(`UPDATE clipping_stages SET status='uncertain',lease_expires_at=? WHERE job_id=?`, expired, f.jobID); err != nil {
		t.Fatal(err)
	}
	if response := f.sendCallback(t, f.callbackToken); response.Code != http.StatusNoContent {
		t.Fatalf("uncertain late callback status=%d body=%s", response.Code, response.Body.String())
	}
	job, err := f.store.ClippingJob(f.jobID)
	if err != nil || job.Status != ClippingJobCompleted {
		t.Fatalf("uncertain callback did not settle: job=%+v error=%v", job, err)
	}
}

func TestLegacyInflightCallbackRejectsGeminiProvider(t *testing.T) {
	f := newLegacyInflightFixture(t)
	// A malformed migrated record must not route Gemini work through the
	// legacy callback or candidate builder, even with a valid old capability.
	if _, err := f.store.db.Exec(`UPDATE clipping_jobs SET provider='gemini',min_clip_seconds=NULL,max_clip_seconds=NULL WHERE id=?`, f.jobID); err != nil {
		t.Fatal(err)
	}
	if response := f.sendCallback(t, f.callbackToken); response.Code != http.StatusConflict {
		t.Fatalf("Gemini callback through legacy receiver status=%d body=%s", response.Code, response.Body.String())
	}
	artifacts, err := f.store.ClippingArtifacts(f.jobID)
	if err != nil || len(artifacts) != 0 {
		t.Fatalf("Gemini job received legacy artifacts=%d error=%v", len(artifacts), err)
	}
}
