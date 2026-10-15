package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type clippingRuntimeResolver struct {
	answers  []net.IPAddr
	err      error
	host     string
	onLookup func(string)
}

func (r *clippingRuntimeResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	r.host = host
	if r.onLookup != nil {
		r.onLookup(host)
	}
	return r.answers, r.err
}

type clippingRuntimeRoundTripper func(*http.Request) (*http.Response, error)

func (f clippingRuntimeRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestClippingWorkerConfigValidationAndEncryptionRoundTrip(t *testing.T) {
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}

	invalidEndpoints := []string{
		"http://worker.modal.run:443/dispatch",
		"https://worker.modal.run:/dispatch",
		"https://worker.modal.run:444/dispatch",
		"https://worker.modal.run:443/other",
		"https://worker.modal.run:443/%64ispatch",
		"https://user:secret@worker.modal.run:443/dispatch",
		"https://worker.modal.run:443/dispatch?key=secret",
		"https://worker.modal.run:443/dispatch#fragment",
		"https://127.0.0.1:443/dispatch",
		"https://worker.example:443/dispatch",
		"https://-worker.modal.run:443/dispatch",
	}
	for _, endpoint := range invalidEndpoints {
		t.Run(endpoint, func(t *testing.T) {
			if _, _, err := normalizeClippingWorkerEndpoint(endpoint); err == nil {
				t.Fatalf("accepted unsafe endpoint %q", endpoint)
			}
		})
	}

	workerBearer := strings.Repeat("a", 64)
	input := clippingWorkerConfigInput{
		Enabled: true, Endpoint: "HTTPS://Fn-Worker.Modal.Run/dispatch",
		BearerToken: workerBearer, MaxComputeSeconds: 43_200, PipelineRevision: "m3.2.0-build_7",
	}
	config, err := saveClippingWorkerConfig(store, security, input)
	if err != nil {
		t.Fatal(err)
	}
	if config.Endpoint != "https://fn-worker.modal.run:443/dispatch" {
		t.Fatalf("endpoint not normalized: %q", config.Endpoint)
	}
	if config.MaxComputeSeconds != 43_200 {
		t.Fatalf("configured compute cap=%d, want 43200", config.MaxComputeSeconds)
	}
	if normalized, _, err := normalizeClippingWorkerEndpoint("https://fn-worker.modal.run:443/dispatch"); err != nil || normalized != config.Endpoint {
		t.Fatalf("explicit default port was not accepted: normalized=%q error=%v", normalized, err)
	}
	ciphertext, err := store.Setting(clippingWorkerConfigSetting)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ciphertext, workerBearer) || strings.Contains(ciphertext, "/dispatch") || !strings.HasPrefix(ciphertext, "v1:") {
		t.Fatal("stored worker settings were not encrypted")
	}
	if strings.Contains(ciphertext, input.PipelineRevision) {
		t.Fatal("stored pipeline revision was not encrypted")
	}
	if marker, err := store.Setting(clippingAnalysisPipelineRevisionSetting); err != nil || marker != input.PipelineRevision {
		t.Fatalf("pipeline revision marker was not persisted with config: marker=%q error=%v", marker, err)
	}
	loaded, configured, err := loadClippingWorkerConfig(store, security)
	if err != nil || !configured || loaded != config {
		t.Fatalf("config roundtrip=%+v configured=%v error=%v", loaded, configured, err)
	}

	view := safeClippingWorkerConfigView(loaded, configured)
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	response := string(encoded)
	for _, forbidden := range []string{
		workerBearer, "/dispatch", "https://", "rate_micro_usd_per_second", "billing_basis", "budget_micro_usd",
	} {
		if strings.Contains(response, forbidden) {
			t.Fatalf("safe config response contains %q: %s", forbidden, response)
		}
	}
	for _, expected := range []string{
		`"endpoint_host":"fn-worker.modal.run"`,
		`"pipeline_revision":"m3.2.0-build_7"`,
		`"readiness":"configured_not_live_checked"`,
		`"max_compute_seconds":43200`,
	} {
		if !strings.Contains(response, expected) {
			t.Fatalf("safe config response is missing %s: %s", expected, response)
		}
	}

	updated, err := saveClippingWorkerConfig(store, security, clippingWorkerConfigInput{
		Enabled: false, Endpoint: "https://other-worker.modal.run:443/dispatch", MaxComputeSeconds: 120,
	})
	if err != nil || updated.Enabled || updated.BearerToken != input.BearerToken || updated.PipelineRevision != input.PipelineRevision {
		t.Fatalf("blank-token/revision update did not preserve stored values while disabling: config=%+v error=%v", updated, err)
	}
	loaded, configured, err = loadClippingWorkerConfig(store, security)
	if err != nil || !configured || loaded.Enabled || loaded.BearerToken != input.BearerToken || loaded.PipelineRevision != input.PipelineRevision {
		t.Fatalf("disabled config roundtrip=%+v configured=%v error=%v", loaded, configured, err)
	}

	updated, err = saveClippingWorkerConfig(store, security, clippingWorkerConfigInput{
		Enabled: true, Endpoint: "https://new-worker.modal.run:443/dispatch", MaxComputeSeconds: 1,
	})
	if err != nil || !updated.Enabled || updated.MaxComputeSeconds != 1 || updated.PipelineRevision != input.PipelineRevision || updated.BearerToken != input.BearerToken {
		t.Fatalf("existing credentials/revision could not be preserved at the minimum compute cap: config=%+v error=%v", updated, err)
	}
}

func TestClippingWorkerConfigRequiresTokenRevisionAndBoundedCompute(t *testing.T) {
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	base := clippingWorkerConfigInput{Endpoint: "https://worker.modal.run:443/dispatch", MaxComputeSeconds: 1}
	if _, err := saveClippingWorkerConfig(store, security, base); !errors.Is(err, errInvalidClippingWorkerConfig) {
		t.Fatalf("missing initial token error=%v", err)
	}
	base.BearerToken = strings.Repeat("a", 64)
	for _, seconds := range []int64{0, -1, 43_201, int64(^uint64(0) >> 1)} {
		input := base
		input.MaxComputeSeconds = seconds
		if _, err := saveClippingWorkerConfig(store, security, input); !errors.Is(err, errInvalidClippingWorkerConfig) {
			t.Errorf("accepted compute cap %d: error=%v", seconds, err)
		}
	}
	base.Enabled = true
	if _, err := saveClippingWorkerConfig(store, security, base); !errors.Is(err, errInvalidClippingWorkerConfig) {
		t.Fatalf("missing pipeline revision error=%v", err)
	}
	for _, token := range []string{"short", strings.Repeat("a", 63), strings.Repeat("a", 65), strings.Repeat("A", 64), strings.Repeat("g", 64)} {
		input := base
		input.PipelineRevision = "revision-1"
		input.BearerToken = token
		if _, err := saveClippingWorkerConfig(store, security, input); !errors.Is(err, errInvalidClippingWorkerConfig) {
			t.Errorf("accepted invalid worker bearer %q: error=%v", token, err)
		}
	}
	for _, revision := range []string{"bad/revision", "bad revision", strings.Repeat("a", 65)} {
		input := base
		input.PipelineRevision = revision
		if _, err := saveClippingWorkerConfig(store, security, input); !errors.Is(err, errInvalidClippingWorkerConfig) {
			t.Errorf("accepted malformed pipeline revision %q: error=%v", revision, err)
		}
	}
	base.PipelineRevision = strings.Repeat("r", 64)
	base.MaxComputeSeconds = 43_200
	if config, err := saveClippingWorkerConfig(store, security, base); err != nil || len(config.PipelineRevision) != 64 || config.MaxComputeSeconds != 43_200 {
		t.Fatalf("accepted maximum compute cap/revision did not roundtrip: config=%+v error=%v", config, err)
	}
}

func TestClippingWorkerConfigV1MigrationDisablesLegacyConfiguration(t *testing.T) {
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	legacyToken := strings.Repeat("a", 64)
	legacy := `{"version":1,"config":{"enabled":true,"endpoint":"https://worker.modal.run:443/dispatch","bearer_token":"` + legacyToken + `","rate_micro_usd_per_second":100}}`
	ciphertext, err := security.EncryptSetting(clippingWorkerConfigSetting, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(clippingWorkerConfigSetting, ciphertext); err != nil {
		t.Fatal(err)
	}
	config, configured, err := loadClippingWorkerConfig(store, security)
	if err != nil || !configured || config.Enabled || config.BearerToken != legacyToken || config.MaxComputeSeconds != 43_200 {
		t.Fatalf("legacy config was not safely retained disabled: config=%+v configured=%v error=%v", config, configured, err)
	}
	updated, err := saveClippingWorkerConfig(store, security, clippingWorkerConfigInput{
		Enabled: true, Endpoint: config.Endpoint, MaxComputeSeconds: config.MaxComputeSeconds,
		PipelineRevision: "revision-1",
	})
	if err != nil || !updated.Enabled || updated.BearerToken != legacyToken || updated.PipelineRevision != "revision-1" {
		t.Fatalf("legacy credential reconfiguration failed: config=%+v error=%v", updated, err)
	}
}

func TestClippingWorkerConfigRequestRequiresFiveExactFields(t *testing.T) {
	valid := `{"enabled":false,"endpoint":"https://worker.modal.run:443/dispatch","bearer_token":"","max_compute_seconds":43200,"pipeline_revision":""}`
	request := func(body string) (clippingWorkerConfigInput, int) {
		r := httptest.NewRequest(http.MethodPut, "/api/clipping/config/worker", strings.NewReader(body))
		w := httptest.NewRecorder()
		input, err := decodeClippingWorkerConfigInput(w, r)
		if err != nil {
			return clippingWorkerConfigInput{}, w.Code
		}
		return input, w.Code
	}
	if input, status := request(valid); status != http.StatusOK || input.Enabled || input.MaxComputeSeconds != 43_200 || input.PipelineRevision != "" {
		t.Fatalf("valid request was not decoded: input=%+v status=%d", input, status)
	}
	invalid := []string{
		`{"enabled":false,"endpoint":"https://worker.modal.run:443/dispatch","bearer_token":"","pipeline_revision":""}`,
		strings.Replace(valid, `"enabled":false`, `"enabled":null`, 1),
		strings.Replace(valid, `"bearer_token":""`, `"bearer_token":null`, 1),
		strings.Replace(valid, `"max_compute_seconds":43200`, `"max_compute_seconds":43200.5`, 1),
		strings.Replace(valid, `"pipeline_revision":""`, `"pipeline_revision":null`, 1),
		strings.TrimSuffix(valid, "}") + `,"rate_micro_usd_per_second":10}`,
		strings.TrimSuffix(valid, "}") + `,"extra":true}`,
		strings.TrimSuffix(valid, "}") + `,"enabled":true}`,
		valid + ` {}`,
	}
	for _, body := range invalid {
		if _, status := request(body); status < 400 {
			t.Errorf("accepted invalid worker configuration JSON: %s", body)
		}
	}
}

func TestClippingWorkerDispatcherUsesFixedV2CompatibilityRate(t *testing.T) {
	dispatchNow := time.Now().Unix()
	resolver := &clippingRuntimeResolver{answers: []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}}
	var sent *http.Request
	var body []byte
	acknowledgement := `{"accepted":true,"job_id":"clip_job_01","attempt_id":"attempt_01","dispatch_id":"clip_job_01:analysis"}`
	client := &http.Client{Transport: clippingRuntimeRoundTripper(func(request *http.Request) (*http.Response, error) {
		sent = request.Clone(request.Context())
		if request.Body != nil {
			body, _ = io.ReadAll(request.Body)
		}
		return clippingRuntimeResponse(request, http.StatusAccepted, acknowledgement), nil
	})}
	dispatcher := newClippingWorkerDispatcher(resolver, nil, client)
	workerBearer := strings.Repeat("f", 64)
	config := clippingWorkerConfig{
		Enabled: true, Endpoint: "https://fn-worker.modal.run:443/dispatch", BearerToken: workerBearer,
		MaxComputeSeconds: 120, PipelineRevision: "m3.2.1-operator",
	}
	prepared := clippingPreparedDispatch{
		Payload: clippingStageDispatch{
			ProtocolVersion: clippingProtocolVersion, JobID: "clip_job_01", BatchID: "clip_batch_01", SourceID: "clip_source_01",
			Stage: ClippingStageAnalysis, DispatchID: "clip_job_01:analysis", AttemptID: "attempt_01",
			MediaURL:         "https://framevault.example/api/clipping/worker-media/clip_job_01/attempt_01",
			CallbackURL:      "https://framevault.example/api/clipping/worker-callbacks/clip_job_01/attempt_01",
			SourceDurationMS: 60_000, ExpiresAt: dispatchNow + 3_600,
			CallbackExpiresAt: dispatchNow + 3_600 + int64(clippingCallbackCapabilityGrace.Seconds()),
			SourceSHA256:      strings.Repeat("a", 64),
			ContentType:       "general", Model: clippingWorkerModel, MinClipSeconds: 15, MaxClipSeconds: 180, CandidateLimit: 10,
		},
		MediaBearer: "media-capability-123", CallbackBearer: "callback-capability-456",
	}
	if err := dispatcher.dispatch(context.Background(), config, prepared); err != nil {
		t.Fatal(err)
	}
	invalidCallbackExpiry := prepared.Payload
	invalidCallbackExpiry.PipelineRevision = config.PipelineRevision
	invalidCallbackExpiry.CallbackExpiresAt = invalidCallbackExpiry.ExpiresAt + int64(clippingCallbackCapabilityGrace.Seconds()) + 1
	if _, err := buildClippingWorkerDispatchV2(invalidCallbackExpiry, clippingWorkerProtocolCompatibilityRate, config.MaxComputeSeconds); err == nil {
		t.Fatal("dispatch accepted callback expiry beyond the bounded callback window")
	}
	invalidCallbackExpiry.CallbackExpiresAt = time.Now().Unix()
	if _, err := buildClippingWorkerDispatchV2(invalidCallbackExpiry, clippingWorkerProtocolCompatibilityRate, config.MaxComputeSeconds); err == nil {
		t.Fatal("dispatch accepted an expired callback window")
	}
	if sent == nil || sent.URL.String() != config.Endpoint || resolver.host != "fn-worker.modal.run" {
		t.Fatalf("request URL/resolved host mismatch: request=%v resolver=%q", sent, resolver.host)
	}
	if sent.Header.Get("Authorization") != "Bearer "+workerBearer ||
		sent.Header.Get(clippingWorkerMediaCapabilityHeader) != prepared.MediaBearer ||
		sent.Header.Get(clippingWorkerCallbackCapabilityHeader) != prepared.CallbackBearer ||
		sent.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("dispatch capability headers are incomplete: %+v", sent.Header)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["protocol_version"] != "framevault.clipping.v2" || payload["model"] != clippingWorkerModel ||
		payload["max_compute_seconds"] != float64(120) || payload["rate_micro_usd_per_second"] != float64(1) ||
		payload["pipeline_revision"] != config.PipelineRevision || payload["callback_expires_at"] != float64(prepared.Payload.CallbackExpiresAt) {
		t.Fatalf("v2 body does not preserve the worker contract and fixed compatibility value: %s", body)
	}
	for _, secret := range []string{config.BearerToken, prepared.MediaBearer, prepared.CallbackBearer} {
		if strings.Contains(string(body), secret) {
			t.Fatalf("dispatch JSON leaked a header capability: %s", body)
		}
	}
}

func TestClippingWorkerDispatcherRejectsUnsafeDNSAndUnacceptedResponses(t *testing.T) {
	dispatchNow := time.Now().Unix()
	prepared := clippingPreparedDispatch{
		Payload: clippingStageDispatch{
			ProtocolVersion: clippingProtocolVersion, JobID: "clip_job_01", BatchID: "clip_batch_01", SourceID: "clip_source_01",
			Stage: ClippingStageAnalysis, DispatchID: "clip_job_01:analysis", AttemptID: "attempt_01",
			MediaURL: "https://framevault.example/media", CallbackURL: "https://framevault.example/callback",
			SourceDurationMS: 60_000, ExpiresAt: dispatchNow + 3_600,
			CallbackExpiresAt: dispatchNow + 3_600 + int64(clippingCallbackCapabilityGrace.Seconds()),
			SourceSHA256:      strings.Repeat("b", 64), ContentType: "podcast", Model: clippingWorkerModel,
			MinClipSeconds: 20, MaxClipSeconds: 150, CandidateLimit: 8,
		}, MediaBearer: "media-capability", CallbackBearer: "callback-capability",
	}
	config := clippingWorkerConfig{
		Enabled: true, Endpoint: "https://fn-worker.modal.run:443/dispatch", BearerToken: strings.Repeat("b", 64),
		MaxComputeSeconds: 60, PipelineRevision: "m3.2.0",
	}
	acknowledgement := `{"accepted":true,"job_id":"clip_job_01","attempt_id":"attempt_01","dispatch_id":"clip_job_01:analysis"}`
	validResolver := &clippingRuntimeResolver{answers: []net.IPAddr{{IP: net.ParseIP("8.8.4.4")}}}
	disabledCalled := false
	disabledConfig := config
	disabledConfig.Enabled = false
	disabledDispatcher := newClippingWorkerDispatcher(&clippingRuntimeResolver{answers: []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}}, nil, &http.Client{Transport: clippingRuntimeRoundTripper(func(request *http.Request) (*http.Response, error) {
		disabledCalled = true
		return clippingRuntimeResponse(request, http.StatusAccepted, `{}`), nil
	})})
	if err := disabledDispatcher.dispatch(context.Background(), disabledConfig, prepared); err == nil || disabledCalled {
		t.Fatalf("disabled worker dispatched work: called=%v error=%v", disabledCalled, err)
	}

	unsafeResolver := &clippingRuntimeResolver{answers: []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}, {IP: net.ParseIP("127.0.0.1")}}}
	called := false
	unsafeDispatcher := newClippingWorkerDispatcher(unsafeResolver, nil, &http.Client{Transport: clippingRuntimeRoundTripper(func(request *http.Request) (*http.Response, error) {
		called = true
		return clippingRuntimeResponse(request, http.StatusAccepted, `{}`), nil
	})})
	if err := unsafeDispatcher.dispatch(context.Background(), config, prepared); err == nil || called {
		t.Fatalf("unsafe mixed DNS response was dispatched: called=%v error=%v", called, err)
	}

	for _, response := range []struct {
		status int
		body   string
	}{
		{http.StatusOK, acknowledgement},
		{http.StatusFound, acknowledgement},
		{http.StatusAccepted, `{"accepted":false,"job_id":"clip_job_01","attempt_id":"attempt_01","dispatch_id":"clip_job_01:analysis"}`},
		{http.StatusAccepted, `{"accepted":true,"job_id":"clip_job_01","attempt_id":"other_attempt","dispatch_id":"clip_job_01:analysis"}`},
		{http.StatusAccepted, `{"accepted":true,"job_id":"clip_job_01","attempt_id":"attempt_01","dispatch_id":"other-dispatch"}`},
		{http.StatusAccepted, `{"accepted":true,"accepted":true,"job_id":"clip_job_01","attempt_id":"attempt_01","dispatch_id":"clip_job_01:analysis"}`},
		{http.StatusAccepted, `{"accepted":true,"job_id":"clip_job_01","attempt_id":"attempt_01","dispatch_id":"clip_job_01:analysis","extra":true}`},
		{http.StatusAccepted, ""},
		{http.StatusAccepted, `{}`},
		{http.StatusAccepted, `not-json`},
		{http.StatusAccepted, strings.Repeat("x", maxClippingWorkerResponseBodyBytes+1)},
	} {
		dispatcher := newClippingWorkerDispatcher(validResolver, nil, &http.Client{Transport: clippingRuntimeRoundTripper(func(request *http.Request) (*http.Response, error) {
			return clippingRuntimeResponse(request, response.status, response.body), nil
		})})
		if err := dispatcher.dispatch(context.Background(), config, prepared); err == nil {
			t.Errorf("accepted status=%d body_length=%d", response.status, len(response.body))
		}
	}
}

func TestClippingWorkerDispatchTransportErrorRemainsAmbiguous(t *testing.T) {
	now := time.Now().Unix()
	prepared := clippingPreparedDispatch{
		Payload: clippingStageDispatch{
			ProtocolVersion: clippingProtocolVersion, JobID: "clip_job_01", BatchID: "clip_batch_01", SourceID: "clip_source_01",
			Stage: ClippingStageAnalysis, DispatchID: "clip_job_01:analysis", AttemptID: "attempt_01",
			MediaURL: "https://framevault.example/media", CallbackURL: "https://framevault.example/callback",
			SourceDurationMS: 60_000, ExpiresAt: now + 3_600, CallbackExpiresAt: now + 3_600 + int64(clippingCallbackCapabilityGrace.Seconds()),
			SourceSHA256: strings.Repeat("c", 64), ContentType: "general", Model: clippingWorkerModel,
			MinClipSeconds: 15, MaxClipSeconds: 180, CandidateLimit: 10,
		}, MediaBearer: "media-capability", CallbackBearer: "callback-capability",
	}
	config := clippingWorkerConfig{
		Enabled: true, Endpoint: "https://fn-worker.modal.run/dispatch", BearerToken: strings.Repeat("d", 64),
		MaxComputeSeconds: 60, PipelineRevision: "worker-outcome-v1",
	}
	dispatcher := newClippingWorkerDispatcher(
		&clippingRuntimeResolver{answers: []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}}, nil,
		&http.Client{Transport: clippingRuntimeRoundTripper(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("connection reset")
		})},
	)
	err := dispatcher.dispatch(context.Background(), config, prepared)
	if err == nil || clippingWorkerDispatchDefinitelyNotSubmitted(err) {
		t.Fatalf("client.Do transport error was not marked ambiguous: %v", err)
	}
	var outcome *clippingWorkerDispatchOutcomeError
	if !errors.As(err, &outcome) || !outcome.submissionAttempted {
		t.Fatalf("dispatch outcome did not record that client.Do began: %#v", outcome)
	}
}

func TestClippingWorkerCancelBeforeSubmitPreventsModalRequest(t *testing.T) {
	store, security, handler, cookie := clippingDashboardFixture(t)
	source := makeReadyClippingSource(t, store, clippingM3FixtureDurationMS)
	job, _ := clippingCreateJob(t, handler, cookie, source.ID, "cancel-before-modal-submit")
	config := clippingCreateWorkerConfigForTest(t, store, security)
	if claimed, err := store.ClaimClippingAnalysisSource(job.ID); err != nil || !claimed {
		t.Fatalf("claim analysis source=%v error=%v", claimed, err)
	}
	app := &dashboardApp{store: store, security: security}
	prepared, claimed, err := app.prepareClippingStageDispatch(job.ID, 0, "https://framevault.dev")
	if err != nil || !claimed {
		t.Fatalf("prepare dispatch=%+v claimed=%v error=%v", prepared, claimed, err)
	}
	if err := store.SetClippingAnalysisClaimAttempt(job.ID, prepared.Payload.AttemptID); err != nil {
		t.Fatal(err)
	}

	resolver := &clippingRuntimeResolver{answers: []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}}
	var roundTripCalled bool
	resolver.onLookup = func(host string) {
		if host != "fn-worker.modal.run" {
			t.Errorf("unexpected resolver host %q", host)
		}
		var persistedRevision string
		var persistedCompute int64
		if err := store.db.QueryRow(`SELECT pipeline_revision,max_compute_seconds FROM clipping_stage_attempts WHERE attempt_id=?`, prepared.Payload.AttemptID).
			Scan(&persistedRevision, &persistedCompute); err != nil {
			t.Errorf("read persisted dispatch constraints: %v", err)
		} else if persistedRevision != config.PipelineRevision || persistedCompute != config.MaxComputeSeconds {
			t.Errorf("dispatch constraints were not persisted before DNS: revision=%q compute=%d", persistedRevision, persistedCompute)
		}
		if err := store.CancelClippingJob(job.ID); err != nil {
			t.Errorf("cancel job at pre-submit boundary: %v", err)
		}
		app.deleteClippingCapabilities(job.ID)
	}
	client := &http.Client{Transport: clippingRuntimeRoundTripper(func(request *http.Request) (*http.Response, error) {
		roundTripCalled = true
		return clippingRuntimeResponse(request, http.StatusAccepted, `{"accepted":true,"job_id":"`+job.ID+`","attempt_id":"`+prepared.Payload.AttemptID+`","dispatch_id":"`+prepared.Payload.DispatchID+`"}`), nil
	})}
	dispatcher := newClippingWorkerDispatcher(resolver, nil, client)
	err = app.dispatchPreparedClippingStageWithDispatcher(context.Background(), prepared, dispatcher)
	if err == nil || !clippingWorkerDispatchDefinitelyNotSubmitted(err) {
		t.Fatalf("canceled attempt was not recognized as definitely unsubmitted: error=%v", err)
	}
	if roundTripCalled {
		t.Fatal("Modal dispatch occurred after the job was canceled")
	}
	canceled, err := store.ClippingJob(job.ID)
	if err != nil || canceled.Status != ClippingJobCanceled {
		t.Fatalf("job status after pre-submit cancellation=%q error=%v", canceled.Status, err)
	}
}

func TestClippingWorkerPinnedDialUsesValidatedIPAndRejectsOtherTargets(t *testing.T) {
	endpoint, err := url.Parse("https://fn-worker.modal.run:443/dispatch")
	if err != nil {
		t.Fatal(err)
	}
	var dialed []string
	clientSide, workerSide := net.Pipe()
	defer clientSide.Close()
	defer workerSide.Close()
	transport := newPinnedClippingWorkerTransport(endpoint, []net.IP{net.ParseIP("8.8.8.8")}, func(_ context.Context, network, address string) (net.Conn, error) {
		dialed = append(dialed, network+" "+address)
		return clientSide, nil
	})
	connection, err := transport.DialContext(context.Background(), "tcp", "fn-worker.modal.run:443")
	if err != nil {
		t.Fatal(err)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	if len(dialed) != 1 || !strings.Contains(dialed[0], "8.8.8.8:443") {
		t.Fatalf("worker dial did not use the pinned IP: %v", dialed)
	}
	if _, err := transport.DialContext(context.Background(), "tcp", "other.modal.run:443"); err == nil {
		t.Fatal("worker transport accepted a different host")
	}
}

func clippingRuntimeResponse(request *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request,
	}
}
