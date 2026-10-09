package app

import (
	"bytes"
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	clippingWorkerConfigSetting                  = "clipping_worker_config_v1"
	clippingWorkerProvider                       = "modal_faster_whisper"
	clippingWorkerModel                          = "large-v3"
	clippingWorkerReadiness                      = "configured_not_live_checked"
	clippingWorkerBillingBasis                   = "operator_declared_worker_second_rate_estimate"
	clippingWorkerMaxAttemptComputeSeconds int64 = 12 * 60 * 60
	clippingWorkerSubmitTimeout                  = 10 * time.Second
	clippingWorkerResponseHeaderTimeout          = 8 * time.Second
	maxClippingWorkerConfigBodyBytes             = 8 << 10
	maxClippingWorkerDispatchBodyBytes           = 32 << 10
	maxClippingWorkerResponseBodyBytes           = 64 << 10
	maxClippingWorkerTokenBytes                  = 4096
	clippingWorkerUserAgent                      = "FrameVault-Clipping/2.0"
	clippingWorkerMediaCapabilityHeader          = "X-FrameVault-Media-Capability"
	clippingWorkerCallbackCapabilityHeader       = "X-FrameVault-Callback-Capability"
	clippingWorkerConfigVersion                  = 2
)

var (
	errInvalidClippingWorkerConfig = errors.New("clipping worker configuration is invalid")
	clippingWorkerTokenPattern     = regexp.MustCompile(`^[A-Za-z0-9._~+/-]+=*$`)
	clippingWorkerBearerPattern    = regexp.MustCompile(`^[a-f0-9]{64}$`)
	clippingWorkerSHA256Pattern    = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

// clippingWorkerConfig contains operator-controlled credentials and stays
// encrypted as a single settings value. Its JSON representation is never
// returned to the browser.
type clippingWorkerConfig struct {
	Enabled               bool   `json:"enabled"`
	Endpoint              string `json:"endpoint"`
	BearerToken           string `json:"bearer_token"`
	RateMicroUSDPerSecond int64  `json:"rate_micro_usd_per_second"`
	PipelineRevision      string `json:"pipeline_revision"`
}

type clippingWorkerStoredConfig struct {
	Version int                  `json:"version"`
	Config  clippingWorkerConfig `json:"config"`
}

// clippingWorkerConfigInput is the exact PUT /api/clipping/config/worker
// request contract. Blank bearer_token and pipeline_revision values preserve
// their existing encrypted values when a valid configuration is already stored.
type clippingWorkerConfigInput struct {
	Enabled               bool   `json:"enabled"`
	Endpoint              string `json:"endpoint"`
	BearerToken           string `json:"bearer_token"`
	RateMicroUSDPerSecond int64  `json:"rate_micro_usd_per_second"`
	PipelineRevision      string `json:"pipeline_revision"`
}

// clippingWorkerConfigView is deliberately secret-free and reports only the
// validated hostname for the operator's Modal endpoint.
type clippingWorkerConfigView struct {
	Configured               bool   `json:"configured"`
	Enabled                  bool   `json:"enabled"`
	EndpointHost             string `json:"endpoint_host"`
	Provider                 string `json:"provider"`
	Model                    string `json:"model"`
	RateMicroUSDPerSecond    int64  `json:"rate_micro_usd_per_second"`
	PipelineRevision         string `json:"pipeline_revision"`
	MaxAttemptComputeSeconds int64  `json:"max_attempt_compute_seconds"`
	Readiness                string `json:"readiness"`
	Capabilities             struct {
		FullTimelineASR    bool `json:"full_timeline_asr"`
		OriginalScript     bool `json:"original_script"`
		AudioEvents        bool `json:"audio_events"`
		SceneMotionEvents  bool `json:"scene_motion_events"`
		CandidateSelection bool `json:"candidate_selection"`
	} `json:"capabilities"`
	BillingBasis string `json:"billing_basis"`
}

// clippingWorkerDispatchV2 is the canonical Modal submission body. The
// estimate rate is explicitly declared by the operator and is not an invoice
// rate or a claim about Modal charges.
type clippingWorkerDispatchV2 struct {
	ProtocolVersion       string `json:"protocol_version"`
	Model                 string `json:"model"`
	JobID                 string `json:"job_id"`
	BatchID               string `json:"batch_id"`
	SourceID              string `json:"source_id"`
	Stage                 string `json:"stage"`
	DispatchID            string `json:"dispatch_id"`
	AttemptID             string `json:"attempt_id"`
	MediaURL              string `json:"media_url"`
	CallbackURL           string `json:"callback_url"`
	SourceDurationMS      int64  `json:"source_duration_ms"`
	ExpiresAt             int64  `json:"expires_at"`
	CallbackExpiresAt     int64  `json:"callback_expires_at"`
	MaxComputeSeconds     int64  `json:"max_compute_seconds"`
	RateMicroUSDPerSecond int64  `json:"rate_micro_usd_per_second"`
	PipelineRevision      string `json:"pipeline_revision"`
	SourceSHA256          string `json:"source_sha256"`
	ContentType           string `json:"content_type"`
	MinClipSeconds        int    `json:"min_clip_seconds"`
	MaxClipSeconds        int    `json:"max_clip_seconds"`
	CandidateLimit        int    `json:"candidate_limit"`
}

type clippingWorkerDialContext func(context.Context, string, string) (net.Conn, error)

// clippingWorkerDispatchOutcomeError records whether the outbound HTTP call
// began. Callers may release a reservation only when the dispatch is known to
// have remained on the pre-submit side of that boundary.
type clippingWorkerDispatchOutcomeError struct {
	submissionAttempted bool
	err                 error
}

func (e *clippingWorkerDispatchOutcomeError) Error() string {
	return "clipping worker dispatch failed"
}

func (e *clippingWorkerDispatchOutcomeError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func clippingWorkerDispatchFailure(err error, submissionAttempted bool) error {
	if err == nil {
		return nil
	}
	var existing *clippingWorkerDispatchOutcomeError
	if errors.As(err, &existing) {
		return err
	}
	return &clippingWorkerDispatchOutcomeError{submissionAttempted: submissionAttempted, err: err}
}

// clippingWorkerDispatchDefinitelyNotSubmitted is true only for a typed
// dispatcher failure that occurred before http.Client.Do began.
func clippingWorkerDispatchDefinitelyNotSubmitted(err error) bool {
	var outcome *clippingWorkerDispatchOutcomeError
	return errors.As(err, &outcome) && !outcome.submissionAttempted
}

// clippingWorkerDispatcher resolves Modal workers for each submission and
// connects only to the pinned public addresses returned by that lookup.
// Injected HTTP clients are intended for deterministic local tests.
type clippingWorkerDispatcher struct {
	resolver     clippingIPResolver
	dial         clippingWorkerDialContext
	client       *http.Client
	timeout      time.Duration
	beforeSubmit func(context.Context) error
}

func newClippingWorkerDispatcher(resolver clippingIPResolver, dial clippingWorkerDialContext, client *http.Client) *clippingWorkerDispatcher {
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	if dial == nil {
		dialer := &net.Dialer{Timeout: clippingWorkerSubmitTimeout, KeepAlive: 30 * time.Second}
		dial = dialer.DialContext
	}
	return &clippingWorkerDispatcher{resolver: resolver, dial: dial, client: client, timeout: clippingWorkerSubmitTimeout}
}

func (a *dashboardApp) clippingWorkerConfig(w http.ResponseWriter, _ *http.Request) {
	config, configured, err := loadClippingWorkerConfig(a.store, a.security)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load clipping worker configuration")
		return
	}
	writeJSON(w, http.StatusOK, safeClippingWorkerConfigView(config, configured))
}

func (a *dashboardApp) updateClippingWorkerConfig(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) {
		return
	}
	if !isJSON(r.Header.Get("Content-Type")) {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	input, err := decodeClippingWorkerConfigInput(w, r)
	if err != nil {
		return
	}
	config, err := saveClippingWorkerConfig(a.store, a.security, input)
	if err != nil {
		if errors.Is(err, errInvalidClippingWorkerConfig) {
			writeError(w, http.StatusBadRequest, "worker endpoint, bearer token, and positive rate must be valid")
		} else {
			writeError(w, http.StatusInternalServerError, "could not save clipping worker configuration")
		}
		return
	}
	writeJSON(w, http.StatusOK, safeClippingWorkerConfigView(config, true))
}

// dispatchPreparedClippingStage loads the current encrypted operator settings
// for each attempt, so disabling or changing the worker takes effect without
// restarting the application.
func (a *dashboardApp) dispatchPreparedClippingStage(ctx context.Context, prepared clippingPreparedDispatch, maxComputeSeconds int64) error {
	return a.dispatchPreparedClippingStageWithDispatcher(ctx, prepared, maxComputeSeconds, nil)
}

// dispatchPreparedClippingStageWithDispatcher keeps the final current-attempt
// check immediately adjacent to the outbound call. The injected dispatcher is
// used by local tests to exercise cancellation between durable setup and send.
func (a *dashboardApp) dispatchPreparedClippingStageWithDispatcher(ctx context.Context, prepared clippingPreparedDispatch, maxComputeSeconds int64, dispatcher *clippingWorkerDispatcher) error {
	if a == nil || a.store == nil || a.security == nil {
		return clippingWorkerDispatchFailure(errors.New("clipping worker configuration is unavailable"), false)
	}
	config, configured, err := loadClippingWorkerConfig(a.store, a.security)
	if err != nil || !configured || !config.Enabled {
		return clippingWorkerDispatchFailure(errors.New("clipping worker is not configured for dispatch"), false)
	}
	config, err = validateClippingWorkerConfig(config)
	if err != nil {
		return clippingWorkerDispatchFailure(errors.New("clipping worker configuration is invalid"), false)
	}
	prepared.Payload.PipelineRevision = config.PipelineRevision
	if _, err := buildClippingWorkerDispatchV2(prepared.Payload, config.RateMicroUSDPerSecond, maxComputeSeconds); err != nil {
		return clippingWorkerDispatchFailure(err, false)
	}
	leaseToken, err := a.clippingCapability(prepared.Payload.JobID, prepared.Payload.AttemptID, clippingCapabilityLease)
	if err != nil {
		return clippingWorkerDispatchFailure(errors.New("clipping worker lease capability is unavailable"), false)
	}
	if err := a.store.SetClippingAttemptPipelineRevision(
		prepared.Payload.JobID, prepared.Payload.Stage, prepared.Payload.AttemptID, leaseToken, config.PipelineRevision,
	); err != nil {
		return clippingWorkerDispatchFailure(errors.New("clipping worker pipeline revision could not be persisted"), false)
	}
	if err := a.store.SetClippingAttemptEstimateTerms(
		prepared.Payload.JobID, prepared.Payload.Stage, prepared.Payload.AttemptID,
		leaseToken, config.RateMicroUSDPerSecond, maxComputeSeconds,
	); err != nil {
		return clippingWorkerDispatchFailure(errors.New("clipping worker estimate terms could not be persisted"), false)
	}
	if dispatcher == nil {
		dispatcher = newClippingWorkerDispatcher(nil, nil, nil)
	}
	dispatcher.beforeSubmit = func(checkCtx context.Context) error {
		if err := checkCtx.Err(); err != nil {
			return errors.New("clipping worker dispatch context ended before submission")
		}
		return a.checkClippingWorkerAttemptCurrent(
			checkCtx, prepared.Payload, leaseToken, config.RateMicroUSDPerSecond, maxComputeSeconds, config.PipelineRevision,
		)
	}
	return dispatcher.dispatch(ctx, config, prepared, maxComputeSeconds)
}

func (a *dashboardApp) checkClippingWorkerAttemptCurrent(ctx context.Context, dispatch clippingStageDispatch, leaseToken string, rateMicroUSDPerSecond, maxComputeSeconds int64, pipelineRevision string) error {
	if a == nil || a.store == nil || a.store.db == nil || ctx == nil {
		return errors.New("clipping worker attempt state is unavailable")
	}
	tx, err := a.store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return errors.New("clipping worker attempt state could not be checked")
	}
	defer tx.Rollback()
	job, err := scanClippingJob(tx.QueryRow(`SELECT `+clippingJobColumns()+` FROM clipping_jobs WHERE id=?`, dispatch.JobID))
	if err != nil || job.Status != ClippingJobRunning || job.BatchID != dispatch.BatchID || job.SourceID != dispatch.SourceID {
		return errors.New("clipping worker attempt is no longer current")
	}
	stage, err := readClippingStage(tx, dispatch.JobID, dispatch.Stage)
	if err != nil || stage.Status != ClippingStageRunning || stage.AttemptID != dispatch.AttemptID ||
		stage.IdempotencyKey != dispatch.DispatchID || stage.LeaseExpiresAt != dispatch.ExpiresAt {
		return errors.New("clipping worker attempt is no longer current")
	}
	attempt, err := readClippingAttempt(tx, dispatch.JobID, dispatch.Stage, dispatch.AttemptID)
	if err != nil || attempt.Status != "running" || attempt.SettledAt != 0 ||
		attempt.LeaseExpiresAt != dispatch.ExpiresAt || !validClippingLease(leaseToken, attempt.TokenHash) ||
		attempt.PipelineRevision != pipelineRevision || attempt.OperatorRateMicroUSDPerSecond != rateMicroUSDPerSecond ||
		attempt.MaxComputeSeconds != maxComputeSeconds {
		return errors.New("clipping worker attempt is no longer current")
	}
	var sourceID, sourceSHA256, claimRevision, claimAttemptID string
	if err := tx.QueryRow(`SELECT source_id,source_sha256,pipeline_key,attempt_id FROM clipping_analysis_claims WHERE job_id=?`, dispatch.JobID).
		Scan(&sourceID, &sourceSHA256, &claimRevision, &claimAttemptID); err != nil ||
		sourceID != dispatch.SourceID || sourceSHA256 != dispatch.SourceSHA256 ||
		claimRevision != pipelineRevision || claimAttemptID != dispatch.AttemptID {
		return errors.New("clipping worker attempt is no longer current")
	}
	if err := tx.Commit(); err != nil {
		return errors.New("clipping worker attempt state could not be checked")
	}
	return nil
}

func decodeClippingWorkerConfigInput(w http.ResponseWriter, r *http.Request) (clippingWorkerConfigInput, error) {
	if r.ContentLength > maxClippingWorkerConfigBodyBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "worker configuration request is too large")
		return clippingWorkerConfigInput{}, errors.New("oversized worker configuration request")
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxClippingWorkerConfigBodyBytes)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "worker configuration request is too large")
			return clippingWorkerConfigInput{}, err
		}
		writeError(w, http.StatusBadRequest, "invalid worker configuration request")
		return clippingWorkerConfigInput{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	start, err := decoder.Token()
	startDelim, startIsDelim := start.(json.Delim)
	if err != nil || !startIsDelim || startDelim != '{' {
		writeError(w, http.StatusBadRequest, "worker configuration must contain the required fields")
		return clippingWorkerConfigInput{}, errors.New("invalid worker configuration fields")
	}
	fields := make(map[string]json.RawMessage, 5)
	for decoder.More() {
		keyToken, keyErr := decoder.Token()
		name, ok := keyToken.(string)
		if keyErr != nil || !ok {
			writeError(w, http.StatusBadRequest, "invalid worker configuration request")
			return clippingWorkerConfigInput{}, errors.New("invalid worker configuration field name")
		}
		if _, duplicate := fields[name]; duplicate {
			writeError(w, http.StatusBadRequest, "worker configuration contains a duplicate field")
			return clippingWorkerConfigInput{}, errors.New("duplicate worker configuration field")
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			writeError(w, http.StatusBadRequest, "invalid worker configuration request")
			return clippingWorkerConfigInput{}, err
		}
		fields[name] = raw
	}
	end, endErr := decoder.Token()
	endDelim, endIsDelim := end.(json.Delim)
	trailingErr := decoder.Decode(&struct{}{})
	if endErr != nil || !endIsDelim || endDelim != '}' || !errors.Is(trailingErr, io.EOF) || len(fields) != 5 {
		writeError(w, http.StatusBadRequest, "worker configuration must contain the required fields")
		return clippingWorkerConfigInput{}, errors.New("invalid worker configuration fields")
	}
	for _, name := range []string{"enabled", "endpoint", "bearer_token", "rate_micro_usd_per_second", "pipeline_revision"} {
		if _, ok := fields[name]; !ok {
			writeError(w, http.StatusBadRequest, "worker configuration must contain the required fields")
			return clippingWorkerConfigInput{}, errors.New("missing worker configuration field")
		}
	}
	var input clippingWorkerConfigInput
	if !isJSONBoolean(fields["enabled"]) || !isJSONString(fields["endpoint"]) || !isJSONString(fields["bearer_token"]) ||
		!isJSONInteger(fields["rate_micro_usd_per_second"]) || !isJSONString(fields["pipeline_revision"]) {
		writeError(w, http.StatusBadRequest, "invalid worker configuration request")
		return clippingWorkerConfigInput{}, errors.New("worker configuration field has the wrong type")
	}
	if err := json.Unmarshal(fields["enabled"], &input.Enabled); err != nil {
		writeError(w, http.StatusBadRequest, "invalid worker configuration request")
		return clippingWorkerConfigInput{}, err
	}
	if err := json.Unmarshal(fields["endpoint"], &input.Endpoint); err != nil {
		writeError(w, http.StatusBadRequest, "invalid worker configuration request")
		return clippingWorkerConfigInput{}, err
	}
	if err := json.Unmarshal(fields["bearer_token"], &input.BearerToken); err != nil {
		writeError(w, http.StatusBadRequest, "invalid worker configuration request")
		return clippingWorkerConfigInput{}, err
	}
	if err := json.Unmarshal(fields["rate_micro_usd_per_second"], &input.RateMicroUSDPerSecond); err != nil {
		writeError(w, http.StatusBadRequest, "invalid worker configuration request")
		return clippingWorkerConfigInput{}, err
	}
	if err := json.Unmarshal(fields["pipeline_revision"], &input.PipelineRevision); err != nil {
		writeError(w, http.StatusBadRequest, "invalid worker configuration request")
		return clippingWorkerConfigInput{}, err
	}
	return input, nil
}

func isJSONString(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) >= 2 && trimmed[0] == '"' && json.Valid(trimmed)
}

func isJSONBoolean(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return bytes.Equal(trimmed, []byte("true")) || bytes.Equal(trimmed, []byte("false"))
}

func isJSONInteger(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return false
	}
	for index, char := range trimmed {
		if index == 0 && char == '-' {
			continue
		}
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func loadClippingWorkerConfig(store *Store, security *Security) (clippingWorkerConfig, bool, error) {
	if store == nil || security == nil {
		return clippingWorkerConfig{}, false, errors.New("clipping worker configuration storage is unavailable")
	}
	ciphertext, err := store.Setting(clippingWorkerConfigSetting)
	if errors.Is(err, sql.ErrNoRows) {
		return clippingWorkerConfig{}, false, nil
	}
	if err != nil {
		return clippingWorkerConfig{}, false, errors.New("clipping worker configuration could not be read")
	}
	plaintext, err := security.DecryptSetting(clippingWorkerConfigSetting, ciphertext)
	if err != nil {
		return clippingWorkerConfig{}, false, errors.New("clipping worker configuration could not be decrypted")
	}
	var stored clippingWorkerStoredConfig
	decoder := json.NewDecoder(strings.NewReader(plaintext))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&stored); err != nil || (stored.Version != 1 && stored.Version != clippingWorkerConfigVersion) {
		return clippingWorkerConfig{}, false, errors.New("clipping worker configuration is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return clippingWorkerConfig{}, false, errors.New("clipping worker configuration is invalid")
	}
	// Version 1 predated pipeline pinning. Preserve its encrypted endpoint and
	// bearer token for operator edits, but require an explicit re-enable after a
	// valid revision is supplied.
	if stored.Version == 1 {
		stored.Config.Enabled = false
	}
	config, err := validateClippingWorkerConfig(stored.Config)
	if err != nil {
		return clippingWorkerConfig{}, false, errors.New("clipping worker configuration is invalid")
	}
	return config, true, nil
}

func saveClippingWorkerConfig(store *Store, security *Security, input clippingWorkerConfigInput) (clippingWorkerConfig, error) {
	if store == nil || security == nil {
		return clippingWorkerConfig{}, errors.New("clipping worker configuration storage is unavailable")
	}
	input.Endpoint = strings.TrimSpace(input.Endpoint)
	token := input.BearerToken
	pipelineRevision := input.PipelineRevision
	if existing, configured, err := loadClippingWorkerConfig(store, security); err == nil && configured {
		if strings.TrimSpace(token) == "" {
			token = existing.BearerToken
		}
		if pipelineRevision == "" {
			pipelineRevision = existing.PipelineRevision
		}
	}
	config, err := validateClippingWorkerConfig(clippingWorkerConfig{
		Enabled: input.Enabled, Endpoint: input.Endpoint, BearerToken: token,
		RateMicroUSDPerSecond: input.RateMicroUSDPerSecond, PipelineRevision: pipelineRevision,
	})
	if err != nil {
		return clippingWorkerConfig{}, errInvalidClippingWorkerConfig
	}
	encoded, err := json.Marshal(clippingWorkerStoredConfig{Version: clippingWorkerConfigVersion, Config: config})
	if err != nil {
		return clippingWorkerConfig{}, errors.New("clipping worker configuration could not be encoded")
	}
	ciphertext, err := security.EncryptSetting(clippingWorkerConfigSetting, string(encoded))
	if err != nil {
		return clippingWorkerConfig{}, errors.New("clipping worker configuration could not be encrypted")
	}
	if err := persistClippingWorkerConfigSettings(store, ciphertext, config.PipelineRevision); err != nil {
		return clippingWorkerConfig{}, errors.New("clipping worker configuration could not be saved")
	}
	return config, nil
}

func validateClippingWorkerConfig(config clippingWorkerConfig) (clippingWorkerConfig, error) {
	normalizedEndpoint, _, err := normalizeClippingWorkerEndpoint(config.Endpoint)
	if err != nil || !validClippingWorkerBearer(config.BearerToken) || config.RateMicroUSDPerSecond <= 0 ||
		(config.PipelineRevision != "" && !validClippingWorkerPipelineRevision(config.PipelineRevision)) ||
		(config.Enabled && !validClippingWorkerPipelineRevision(config.PipelineRevision)) {
		return clippingWorkerConfig{}, errInvalidClippingWorkerConfig
	}
	if _, err := estimateClippingWorkerCost(config.RateMicroUSDPerSecond, clippingWorkerMaxAttemptComputeSeconds); err != nil {
		return clippingWorkerConfig{}, errInvalidClippingWorkerConfig
	}
	config.Endpoint = normalizedEndpoint
	return config, nil
}

func normalizeClippingWorkerEndpoint(raw string) (normalized, host string, err error) {
	if len(raw) == 0 || len(raw) > 2048 || strings.ContainsAny(raw, "?#") {
		return "", "", errInvalidClippingWorkerConfig
	}
	parsed, parseErr := url.Parse(raw)
	if parseErr != nil || parsed == nil || !strings.EqualFold(parsed.Scheme, "https") || parsed.Opaque != "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawFragment != "" ||
		parsed.Path != "/dispatch" || parsed.EscapedPath() != "/dispatch" || parsed.Hostname() == "" {
		return "", "", errInvalidClippingWorkerConfig
	}
	port := parsed.Port()
	if (port == "" && strings.HasSuffix(parsed.Host, ":")) || (port != "" && port != "443") {
		return "", "", errInvalidClippingWorkerConfig
	}
	host = strings.ToLower(parsed.Hostname())
	if net.ParseIP(host) != nil || !validModalDNSName(host) {
		return "", "", errInvalidClippingWorkerConfig
	}
	parsed.Scheme = "https"
	parsed.Host = net.JoinHostPort(host, "443")
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	parsed.RawFragment = ""
	return parsed.String(), host, nil
}

func validModalDNSName(host string) bool {
	if len(host) > 253 || !strings.HasSuffix(host, ".modal.run") || strings.HasSuffix(host, ".") {
		return false
	}
	labels := strings.Split(host, ".")
	if len(labels) < 3 {
		return false
	}
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, char := range label {
			if !(char >= 'a' && char <= 'z') && !(char >= '0' && char <= '9') && char != '-' {
				return false
			}
		}
	}
	return true
}

func validClippingWorkerBearer(token string) bool {
	return len(token) == 64 && clippingWorkerBearerPattern.MatchString(token)
}

func validClippingWorkerCapabilityBearer(token string) bool {
	return len(token) > 0 && len(token) <= maxClippingWorkerTokenBytes && clippingWorkerTokenPattern.MatchString(token)
}

func validClippingWorkerPipelineRevision(revision string) bool {
	return validClippingPipelineRevision(revision)
}

func safeClippingWorkerConfigView(config clippingWorkerConfig, configured bool) clippingWorkerConfigView {
	view := clippingWorkerConfigView{
		Configured: configured, Enabled: configured && config.Enabled,
		Provider: clippingWorkerProvider, Model: clippingWorkerModel,
		MaxAttemptComputeSeconds: clippingWorkerMaxAttemptComputeSeconds,
		Readiness:                clippingWorkerReadiness, BillingBasis: clippingWorkerBillingBasis,
	}
	view.Capabilities.FullTimelineASR = true
	view.Capabilities.OriginalScript = true
	view.Capabilities.AudioEvents = true
	view.Capabilities.SceneMotionEvents = true
	view.Capabilities.CandidateSelection = true
	if !configured {
		return view
	}
	view.RateMicroUSDPerSecond = config.RateMicroUSDPerSecond
	view.PipelineRevision = config.PipelineRevision
	_, host, err := normalizeClippingWorkerEndpoint(config.Endpoint)
	if err == nil {
		view.EndpointHost = host
	}
	return view
}

func buildClippingWorkerDispatchV2(dispatch clippingStageDispatch, rateMicroUSDPerSecond, maxComputeSeconds int64) (clippingWorkerDispatchV2, error) {
	now := time.Now().Unix()
	if dispatch.ProtocolVersion != clippingProtocolVersion || !safeID(dispatch.JobID) || !safeID(dispatch.BatchID) ||
		!safeID(dispatch.SourceID) || !safeID(dispatch.AttemptID) || dispatch.Stage != ClippingStageAnalysis || dispatch.Model != clippingWorkerModel ||
		!validClippingWorkerPipelineRevision(dispatch.PipelineRevision) ||
		dispatch.DispatchID != dispatch.JobID+":"+dispatch.Stage || dispatch.SourceDurationMS < 1 ||
		dispatch.SourceDurationMS > MaxClippingSourceDurationMS || dispatch.ExpiresAt <= now || dispatch.ExpiresAt > now+int64(clippingWorkerMaxAttemptComputeSeconds) ||
		dispatch.CallbackExpiresAt <= now || dispatch.CallbackExpiresAt > dispatch.ExpiresAt+int64(clippingCallbackAccountingGrace.Seconds()) ||
		!clippingWorkerSHA256Pattern.MatchString(dispatch.SourceSHA256) || !validClippingContentType(dispatch.ContentType) ||
		dispatch.MinClipSeconds < ClippingDefaultMinClipSeconds || dispatch.MaxClipSeconds > ClippingDefaultMaxClipSeconds ||
		dispatch.MinClipSeconds > dispatch.MaxClipSeconds || dispatch.CandidateLimit < ClippingMinCandidateLimit ||
		dispatch.CandidateLimit > ClippingMaxCandidateLimit ||
		!validClippingWorkerTransferURL(dispatch.MediaURL) || !validClippingWorkerTransferURL(dispatch.CallbackURL) ||
		maxComputeSeconds < 1 || maxComputeSeconds > clippingWorkerMaxAttemptComputeSeconds {
		return clippingWorkerDispatchV2{}, errors.New("clipping worker dispatch is invalid")
	}
	if _, err := estimateClippingWorkerCost(rateMicroUSDPerSecond, maxComputeSeconds); err != nil {
		return clippingWorkerDispatchV2{}, errors.New("clipping worker dispatch is invalid")
	}
	return clippingWorkerDispatchV2{
		ProtocolVersion: "framevault.clipping.v2", Model: dispatch.Model, JobID: dispatch.JobID, BatchID: dispatch.BatchID,
		SourceID: dispatch.SourceID, Stage: dispatch.Stage, DispatchID: dispatch.DispatchID,
		AttemptID: dispatch.AttemptID, MediaURL: dispatch.MediaURL, CallbackURL: dispatch.CallbackURL,
		SourceDurationMS: dispatch.SourceDurationMS, ExpiresAt: dispatch.ExpiresAt, CallbackExpiresAt: dispatch.CallbackExpiresAt,
		MaxComputeSeconds: maxComputeSeconds, RateMicroUSDPerSecond: rateMicroUSDPerSecond,
		PipelineRevision: dispatch.PipelineRevision,
		SourceSHA256:     dispatch.SourceSHA256, ContentType: dispatch.ContentType,
		MinClipSeconds: dispatch.MinClipSeconds, MaxClipSeconds: dispatch.MaxClipSeconds,
		CandidateLimit: dispatch.CandidateLimit,
	}, nil
}

func validClippingWorkerTransferURL(raw string) bool {
	if len(raw) == 0 || len(raw) > 2048 || strings.ContainsAny(raw, "?#") {
		return false
	}
	parsed, err := url.Parse(raw)
	return err == nil && parsed != nil && parsed.Scheme == "https" && parsed.Hostname() != "" &&
		parsed.User == nil && parsed.RawQuery == "" && !parsed.ForceQuery && parsed.Fragment == "" && parsed.RawFragment == ""
}

func estimateClippingWorkerCost(rateMicroUSDPerSecond, computeSeconds int64) (int64, error) {
	if rateMicroUSDPerSecond <= 0 || computeSeconds < 0 ||
		(computeSeconds > 0 && rateMicroUSDPerSecond > math.MaxInt64/computeSeconds) {
		return 0, errors.New("clipping worker cost estimate is out of range")
	}
	return rateMicroUSDPerSecond * computeSeconds, nil
}

// clippingWorkerComputeSecondsForReservation converts the held integer
// microUSD budget into a bounded worker compute ceiling using only the
// operator-declared estimate rate. It does not estimate or settle an invoice.
func clippingWorkerComputeSecondsForReservation(reservedMicroUSD, rateMicroUSDPerSecond int64) (int64, error) {
	if reservedMicroUSD <= 0 || rateMicroUSDPerSecond <= 0 {
		return 0, errors.New("clipping worker reservation cannot fund a compute second")
	}
	seconds := reservedMicroUSD / rateMicroUSDPerSecond
	if seconds < 1 {
		return 0, errors.New("clipping worker reservation cannot fund a compute second")
	}
	if seconds > clippingWorkerMaxAttemptComputeSeconds {
		seconds = clippingWorkerMaxAttemptComputeSeconds
	}
	return seconds, nil
}

func (d *clippingWorkerDispatcher) dispatch(ctx context.Context, config clippingWorkerConfig, prepared clippingPreparedDispatch, maxComputeSeconds int64) (dispatchErr error) {
	submissionAttempted := false
	defer func() {
		if dispatchErr != nil {
			dispatchErr = clippingWorkerDispatchFailure(dispatchErr, submissionAttempted)
		}
	}()
	if d == nil || d.resolver == nil || d.dial == nil || ctx == nil {
		return errors.New("clipping worker dispatcher is unavailable")
	}
	config, err := validateClippingWorkerConfig(config)
	if err != nil || !config.Enabled {
		return errors.New("clipping worker is not configured for dispatch")
	}
	prepared.Payload.PipelineRevision = config.PipelineRevision
	if !validClippingWorkerCapabilityBearer(prepared.MediaBearer) || !validClippingWorkerCapabilityBearer(prepared.CallbackBearer) {
		return errors.New("clipping worker capabilities are unavailable")
	}
	dispatch, err := buildClippingWorkerDispatchV2(prepared.Payload, config.RateMicroUSDPerSecond, maxComputeSeconds)
	if err != nil {
		return err
	}
	body, err := json.Marshal(dispatch)
	if err != nil || len(body) > maxClippingWorkerDispatchBodyBytes {
		return errors.New("clipping worker dispatch could not be encoded")
	}
	endpoint, _, err := normalizeClippingWorkerEndpoint(config.Endpoint)
	if err != nil {
		return errors.New("clipping worker endpoint is invalid")
	}
	parsed, _ := url.Parse(endpoint)

	timeout := d.timeout
	if timeout <= 0 || timeout > clippingWorkerSubmitTimeout {
		timeout = clippingWorkerSubmitTimeout
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	addresses, err := resolveClippingWorkerAddresses(requestCtx, d.resolver, parsed.Hostname())
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return errors.New("clipping worker request could not be created")
	}
	request.Header.Set("Authorization", "Bearer "+config.BearerToken)
	request.Header.Set(clippingWorkerMediaCapabilityHeader, prepared.MediaBearer)
	request.Header.Set(clippingWorkerCallbackCapabilityHeader, prepared.CallbackBearer)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", clippingWorkerUserAgent)

	client, cleanup := d.httpClientFor(parsed, addresses)
	defer cleanup()
	if d.beforeSubmit != nil {
		if err := d.beforeSubmit(requestCtx); err != nil {
			return err
		}
	}
	if err := requestCtx.Err(); err != nil {
		return errors.New("clipping worker dispatch context ended before submission")
	}
	submissionAttempted = true
	response, err := client.Do(request)
	if err != nil {
		return errors.New("clipping worker submission failed")
	}
	if response == nil || response.Body == nil {
		return errors.New("clipping worker acceptance response is invalid")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		return errors.New("clipping worker did not accept the dispatch")
	}
	if err := parseClippingWorkerAcceptedResponse(response.Body, dispatch); err != nil {
		return err
	}
	return nil
}

func (d *clippingWorkerDispatcher) httpClientFor(endpoint *url.URL, addresses []net.IP) (*http.Client, func()) {
	var transport http.RoundTripper
	if d.client != nil && d.client.Transport != nil {
		transport = d.client.Transport
	} else {
		transport = newPinnedClippingWorkerTransport(endpoint, addresses, d.dial)
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   clippingWorkerSubmitTimeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	if d.client != nil && d.client.Timeout > 0 && d.client.Timeout < client.Timeout {
		client.Timeout = d.client.Timeout
	}
	return client, func() {
		if closer, ok := transport.(interface{ CloseIdleConnections() }); ok {
			closer.CloseIdleConnections()
		}
	}
}

func resolveClippingWorkerAddresses(ctx context.Context, resolver clippingIPResolver, host string) ([]net.IP, error) {
	resolved, err := resolver.LookupIPAddr(ctx, host)
	if err != nil || len(resolved) == 0 {
		return nil, errors.New("clipping worker DNS lookup failed")
	}
	addresses := make([]net.IP, 0, len(resolved))
	for _, answer := range resolved {
		if answer.Zone != "" || !isPublicUnicastIP(answer.IP) {
			return nil, errors.New("clipping worker DNS returned a non-public address")
		}
		addresses = append(addresses, append(net.IP(nil), answer.IP...))
	}
	return addresses, nil
}

func newPinnedClippingWorkerTransport(endpoint *url.URL, addresses []net.IP, dial clippingWorkerDialContext) *http.Transport {
	port := "443"
	host := strings.ToLower(endpoint.Hostname())
	validated := make([]net.IP, 0, len(addresses))
	for _, address := range addresses {
		if address != nil && isPublicUnicastIP(address) {
			validated = append(validated, append(net.IP(nil), address...))
		}
	}
	return &http.Transport{
		Proxy:                 nil,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   clippingWorkerSubmitTimeout,
		ResponseHeaderTimeout: clippingWorkerResponseHeaderTimeout,
		DisableCompression:    true,
		DisableKeepAlives:     true,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			requestedHost, requestedPort, splitErr := net.SplitHostPort(address)
			if splitErr != nil || strings.ToLower(strings.TrimSuffix(requestedHost, ".")) != host || requestedPort != port || len(validated) == 0 {
				return nil, errors.New("clipping worker dial target is not validated")
			}
			var lastErr error
			for _, ip := range validated {
				if !isPublicUnicastIP(ip) {
					continue
				}
				connection, dialErr := dial(ctx, network, net.JoinHostPort(ip.String(), port))
				if dialErr == nil {
					return connection, nil
				}
				lastErr = dialErr
			}
			if lastErr == nil {
				return nil, errors.New("no validated public worker address")
			}
			return nil, errors.New("validated clipping worker connection failed")
		},
	}
}

func parseClippingWorkerAcceptedResponse(body io.Reader, expected clippingWorkerDispatchV2) error {
	encoded, err := io.ReadAll(io.LimitReader(body, maxClippingWorkerResponseBodyBytes+1))
	if err != nil || len(encoded) > maxClippingWorkerResponseBodyBytes {
		return errors.New("clipping worker acceptance response is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	start, err := decoder.Token()
	startDelim, startIsDelim := start.(json.Delim)
	if err != nil || !startIsDelim || startDelim != '{' {
		return errors.New("clipping worker acceptance response is invalid")
	}
	var response map[string]json.RawMessage
	response = make(map[string]json.RawMessage, 4)
	for decoder.More() {
		keyToken, keyErr := decoder.Token()
		name, ok := keyToken.(string)
		if keyErr != nil || !ok {
			return errors.New("clipping worker acceptance response is invalid")
		}
		if _, duplicate := response[name]; duplicate {
			return errors.New("clipping worker acceptance response is invalid")
		}
		switch name {
		case "accepted", "job_id", "attempt_id", "dispatch_id":
		default:
			return errors.New("clipping worker acceptance response is invalid")
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return errors.New("clipping worker acceptance response is invalid")
		}
		response[name] = value
	}
	end, endErr := decoder.Token()
	endDelim, endIsDelim := end.(json.Delim)
	if endErr != nil || !endIsDelim || endDelim != '}' || len(response) != 4 {
		return errors.New("clipping worker acceptance response is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("clipping worker acceptance response is invalid")
	}
	var accepted bool
	var jobID, attemptID, dispatchID string
	if json.Unmarshal(response["accepted"], &accepted) != nil || !accepted ||
		json.Unmarshal(response["job_id"], &jobID) != nil || jobID != expected.JobID ||
		json.Unmarshal(response["attempt_id"], &attemptID) != nil || attemptID != expected.AttemptID ||
		json.Unmarshal(response["dispatch_id"], &dispatchID) != nil || dispatchID != expected.DispatchID {
		return errors.New("clipping worker acknowledgement does not match the dispatch")
	}
	return nil
}
