package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
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
		BearerToken: workerBearer, RateMicroUSDPerSecond: 2_500, PipelineRevision: "m3.2.0-build_7",
	}
	config, err := saveClippingWorkerConfig(store, security, input)
	if err != nil {
		t.Fatal(err)
	}
	if config.Endpoint != "https://fn-worker.modal.run:443/dispatch" {
		t.Fatalf("endpoint not normalized: %q", config.Endpoint)
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
	for _, secret := range []string{workerBearer, "/dispatch", "https://"} {
		if strings.Contains(response, secret) {
			t.Fatalf("safe config response leaked %q: %s", secret, response)
		}
	}
	if !strings.Contains(response, `"endpoint_host":"fn-worker.modal.run"`) ||
		!strings.Contains(response, `"pipeline_revision":"m3.2.0-build_7"`) ||
		!strings.Contains(response, `"readiness":"configured_not_live_checked"`) ||
		!strings.Contains(response, `"billing_basis":"operator_declared_worker_second_rate_estimate"`) ||
		!strings.Contains(response, `"max_attempt_compute_seconds":43200`) {
		t.Fatalf("safe config response is missing the declared view fields: %s", response)
	}

	updated, err := saveClippingWorkerConfig(store, security, clippingWorkerConfigInput{
		Enabled: false, Endpoint: "https://other-worker.modal.run:443/dispatch", RateMicroUSDPerSecond: 3_000,
	})
	if err != nil || updated.Enabled || updated.BearerToken != input.BearerToken || updated.PipelineRevision != input.PipelineRevision {
		t.Fatalf("blank-token/revision update did not preserve stored values while disabling: config=%+v error=%v", updated, err)
	}
	loaded, configured, err = loadClippingWorkerConfig(store, security)
	if err != nil || !configured || loaded.Enabled || loaded.BearerToken != input.BearerToken || loaded.PipelineRevision != input.PipelineRevision {
		t.Fatalf("disabled config roundtrip=%+v configured=%v error=%v", loaded, configured, err)
	}

	updated, err = saveClippingWorkerConfig(store, security, clippingWorkerConfigInput{
		Enabled: true, Endpoint: "https://new-worker.modal.run:443/dispatch", RateMicroUSDPerSecond: 1, PipelineRevision: "revision-8",
	})
	if err != nil || !updated.Enabled || updated.PipelineRevision != "revision-8" || updated.BearerToken != input.BearerToken {
		t.Fatal("an existing valid credential should be preservable on a blank-token update:", err)
	}
	if marker, err := store.Setting(clippingAnalysisPipelineRevisionSetting); err != nil || marker != updated.PipelineRevision {
		t.Fatalf("updated pipeline revision marker was not persisted: marker=%q error=%v", marker, err)
	}
}

func TestClippingWorkerConfigRequiresTokenSafeRateAndPipelineRevision(t *testing.T) {
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	base := clippingWorkerConfigInput{Endpoint: "https://worker.modal.run:443/dispatch", RateMicroUSDPerSecond: 1}
	if _, err := saveClippingWorkerConfig(store, security, base); !errors.Is(err, errInvalidClippingWorkerConfig) {
		t.Fatalf("missing initial token error=%v", err)
	}
	for _, rate := range []int64{0, -1, int64(^uint64(0) >> 1)} {
		input := base
		input.BearerToken = strings.Repeat("a", 64)
		input.RateMicroUSDPerSecond = rate
		if _, err := saveClippingWorkerConfig(store, security, input); !errors.Is(err, errInvalidClippingWorkerConfig) {
			t.Errorf("accepted unsafe rate %d: error=%v", rate, err)
		}
	}
	base.Enabled = true
	base.BearerToken = strings.Repeat("a", 64)
	if _, err := saveClippingWorkerConfig(store, security, base); !errors.Is(err, errInvalidClippingWorkerConfig) {
		t.Fatalf("missing pipeline revision error=%v", err)
	}
	for _, token := range []string{"short", strings.Repeat("a", 63), strings.Repeat("a", 65), strings.Repeat("A", 64), strings.Repeat("g", 64)} {
		input := base
		input.PipelineRevision = "revision-1"
		input.BearerToken = token
		if _, err := saveClippingWorkerConfig(store, security, input); !errors.Is(err, errInvalidClippingWorkerConfig) {
			t.Errorf("accepted invalid shared worker bearer length or alphabet %q: error=%v", token, err)
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
	if config, err := saveClippingWorkerConfig(store, security, base); err != nil || len(config.PipelineRevision) != 64 {
		t.Fatalf("accepted 64-character pipeline revision did not roundtrip: config=%+v error=%v", config, err)
	}
}

func TestClippingWorkerConfigV1MigrationRequiresOperatorRevision(t *testing.T) {
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
	if err != nil || !configured || config.Enabled || config.BearerToken != legacyToken {
		t.Fatalf("legacy config was not safely retained disabled: config=%+v configured=%v error=%v", config, configured, err)
	}
	updated, err := saveClippingWorkerConfig(store, security, clippingWorkerConfigInput{
		Enabled: true, Endpoint: config.Endpoint, RateMicroUSDPerSecond: config.RateMicroUSDPerSecond,
		PipelineRevision: "revision-1",
	})
	if err != nil || !updated.Enabled || updated.BearerToken != legacyToken || updated.PipelineRevision != "revision-1" {
		t.Fatalf("legacy credential/revision update failed: config=%+v error=%v", updated, err)
	}
}

func TestClippingWorkerConfigRequestRequiresExactTypedFields(t *testing.T) {
	valid := `{"enabled":false,"endpoint":"https://worker.modal.run:443/dispatch","bearer_token":"","rate_micro_usd_per_second":10,"pipeline_revision":""}`
	request := func(body string) (clippingWorkerConfigInput, int) {
		r := httptest.NewRequest(http.MethodPut, "/api/clipping/config/worker", strings.NewReader(body))
		w := httptest.NewRecorder()
		input, err := decodeClippingWorkerConfigInput(w, r)
		if err != nil {
			return clippingWorkerConfigInput{}, w.Code
		}
		return input, w.Code
	}
	if input, status := request(valid); status != 200 || input.Enabled || input.RateMicroUSDPerSecond != 10 || input.PipelineRevision != "" {
		t.Fatalf("valid request was not decoded: input=%+v status=%d", input, status)
	}
	for _, body := range []string{
		`{"enabled":false,"endpoint":"https://worker.modal.run:443/dispatch","bearer_token":"","rate_micro_usd_per_second":10}`,
		strings.Replace(valid, `"enabled":false`, `"enabled":null`, 1),
		strings.Replace(valid, `"bearer_token":""`, `"bearer_token":null`, 1),
		strings.Replace(valid, `"rate_micro_usd_per_second":10`, `"rate_micro_usd_per_second":10.5`, 1),
		strings.Replace(valid, `"pipeline_revision":""`, `"pipeline_revision":null`, 1),
		strings.TrimSuffix(valid, "}") + `,"extra":true}`,
		strings.TrimSuffix(valid, "}") + `,"enabled":true}`,
		strings.TrimSuffix(valid, "}") + `,"pipeline_revision":"other"}`,
		valid + ` {}`,
	} {
		if _, status := request(body); status < 400 {
			t.Errorf("accepted invalid config JSON: %s", body)
		}
	}
}

func TestClippingWorkerDispatcherSendsCanonicalV2AndCapabilityHeaders(t *testing.T) {
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
		Enabled: true, Endpoint: "https://fn-worker.modal.run:443/dispatch", BearerToken: workerBearer, RateMicroUSDPerSecond: 3_500,
		PipelineRevision: "m3.2.1-operator",
	}
	prepared := clippingPreparedDispatch{
		Payload: clippingStageDispatch{
			ProtocolVersion: clippingProtocolVersion, JobID: "clip_job_01", BatchID: "clip_batch_01", SourceID: "clip_source_01",
			Stage: ClippingStageAnalysis, DispatchID: "clip_job_01:analysis", AttemptID: "attempt_01",
			MediaURL:         "https://framevault.example/api/clipping/worker-media/clip_job_01/attempt_01",
			CallbackURL:      "https://framevault.example/api/clipping/worker-callbacks/clip_job_01/attempt_01",
			SourceDurationMS: 60_000, ExpiresAt: dispatchNow + 3_600,
			CallbackExpiresAt: dispatchNow + 3_600 + int64(clippingCallbackAccountingGrace.Seconds()),
			SourceSHA256:      strings.Repeat("a", 64),
			ContentType:       "general", Model: clippingWorkerModel, MinClipSeconds: 15, MaxClipSeconds: 180, CandidateLimit: 10,
		},
		MediaBearer: "media-capability-123", CallbackBearer: "callback-capability-456",
	}
	if err := dispatcher.dispatch(context.Background(), config, prepared, 120); err != nil {
		t.Fatal(err)
	}
	invalidCallbackExpiry := prepared.Payload
	invalidCallbackExpiry.PipelineRevision = config.PipelineRevision
	invalidCallbackExpiry.CallbackExpiresAt = invalidCallbackExpiry.ExpiresAt + int64(clippingCallbackAccountingGrace.Seconds()) + 1
	if _, err := buildClippingWorkerDispatchV2(invalidCallbackExpiry, config.RateMicroUSDPerSecond, 120); err == nil {
		t.Fatal("dispatch accepted callback expiry beyond the 24-hour accounting window")
	}
	invalidCallbackExpiry.CallbackExpiresAt = time.Now().Unix()
	if _, err := buildClippingWorkerDispatchV2(invalidCallbackExpiry, config.RateMicroUSDPerSecond, 120); err == nil {
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
	if payload["protocol_version"] != "framevault.clipping.v2" || payload["model"] != clippingWorkerModel || payload["max_compute_seconds"] != float64(120) ||
		payload["rate_micro_usd_per_second"] != float64(3_500) || payload["pipeline_revision"] != config.PipelineRevision ||
		payload["callback_expires_at"] != float64(prepared.Payload.CallbackExpiresAt) {
		t.Fatalf("v2 body is missing bounded estimate fields: %s", body)
	}
	for _, secret := range []string{config.BearerToken, prepared.MediaBearer, prepared.CallbackBearer} {
		if strings.Contains(string(body), secret) {
			t.Fatalf("dispatch JSON leaked a header capability: %s", body)
		}
	}
}

func TestClippingWorkerDispatcherRejectsUnsafeDNSResponsesAndUnacceptedResponses(t *testing.T) {
	dispatchNow := time.Now().Unix()
	prepared := clippingPreparedDispatch{
		Payload: clippingStageDispatch{
			ProtocolVersion: clippingProtocolVersion, JobID: "clip_job_01", BatchID: "clip_batch_01", SourceID: "clip_source_01",
			Stage: ClippingStageAnalysis, DispatchID: "clip_job_01:analysis", AttemptID: "attempt_01",
			MediaURL: "https://framevault.example/media", CallbackURL: "https://framevault.example/callback",
			SourceDurationMS: 60_000, ExpiresAt: dispatchNow + 3_600,
			CallbackExpiresAt: dispatchNow + 3_600 + int64(clippingCallbackAccountingGrace.Seconds()),
			SourceSHA256:      strings.Repeat("b", 64),
			ContentType:       "podcast", Model: clippingWorkerModel, MinClipSeconds: 20, MaxClipSeconds: 150, CandidateLimit: 8,
		}, MediaBearer: "media-capability", CallbackBearer: "callback-capability",
	}
	config := clippingWorkerConfig{
		Enabled: true, Endpoint: "https://fn-worker.modal.run:443/dispatch", BearerToken: strings.Repeat("b", 64), RateMicroUSDPerSecond: 1,
		PipelineRevision: "m3.2.0",
	}
	acknowledgement := `{"accepted":true,"job_id":"clip_job_01","attempt_id":"attempt_01","dispatch_id":"clip_job_01:analysis"}`
	disabledResolver := &clippingRuntimeResolver{answers: []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}}
	disabledCalled := false
	disabledConfig := config
	disabledConfig.Enabled = false
	disabledDispatcher := newClippingWorkerDispatcher(disabledResolver, nil, &http.Client{Transport: clippingRuntimeRoundTripper(func(request *http.Request) (*http.Response, error) {
		disabledCalled = true
		return clippingRuntimeResponse(request, http.StatusAccepted, `{}`), nil
	})})
	if err := disabledDispatcher.dispatch(context.Background(), disabledConfig, prepared, 60); err == nil || !clippingWorkerDispatchDefinitelyNotSubmitted(err) || disabledCalled || disabledResolver.host != "" {
		t.Fatalf("disabled worker dispatched work: called=%v resolved=%q error=%v", disabledCalled, disabledResolver.host, err)
	}

	unsafeResolver := &clippingRuntimeResolver{answers: []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}, {IP: net.ParseIP("127.0.0.1")}}}
	called := false
	unsafeDispatcher := newClippingWorkerDispatcher(unsafeResolver, nil, &http.Client{Transport: clippingRuntimeRoundTripper(func(request *http.Request) (*http.Response, error) {
		called = true
		return clippingRuntimeResponse(request, http.StatusAccepted, `{}`), nil
	})})
	if err := unsafeDispatcher.dispatch(context.Background(), config, prepared, 60); err == nil || !clippingWorkerDispatchDefinitelyNotSubmitted(err) || called {
		t.Fatalf("unsafe mixed DNS response was dispatched: called=%v error=%v", called, err)
	}

	validResolver := &clippingRuntimeResolver{answers: []net.IPAddr{{IP: net.ParseIP("8.8.4.4")}}}
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
		if err := dispatcher.dispatch(context.Background(), config, prepared, 60); err == nil {
			t.Errorf("accepted status=%d body_length=%d", response.status, len(response.body))
		} else if clippingWorkerDispatchDefinitelyNotSubmitted(err) {
			t.Errorf("failure after client.Do was marked as definitely unsubmitted: status=%d body_length=%d", response.status, len(response.body))
		}
	}
}

func TestClippingWorkerCancelAfterTermsPersistencePreventsSubmission(t *testing.T) {
	store, security, _, _ := clippingDashboardFixture(t, true)
	source := makeReadyClippingSource(t, store, 60_000)
	_, jobs := makeClippingBatch(t, store, []string{source.ID}, 1_000_000, 1_000_000, "worker-cancel-before-submit")
	job := jobs[0]
	config, err := saveClippingWorkerConfig(store, security, clippingWorkerConfigInput{
		Enabled: true, Endpoint: "https://fn-worker.modal.run/dispatch", BearerToken: strings.Repeat("c", 64),
		RateMicroUSDPerSecond: 100, PipelineRevision: "cancel-race-v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	claimedSource, err := store.ClaimClippingAnalysisSource(job.ID)
	if err != nil || !claimedSource {
		t.Fatalf("analysis source claim=%v error=%v", claimedSource, err)
	}
	app := &dashboardApp{store: store, security: security}
	prepared, claimed, err := app.prepareClippingStageDispatch(job.ID, 50_000, "https://framevault.dev")
	if err != nil || !claimed {
		t.Fatalf("prepared dispatch=%+v claimed=%v error=%v", prepared, claimed, err)
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
		var persistedRate, persistedMaxCompute int64
		if err := store.db.QueryRow(`SELECT pipeline_revision,operator_rate_micro_usd_per_second,max_compute_seconds FROM clipping_stage_attempts WHERE attempt_id=?`, prepared.Payload.AttemptID).
			Scan(&persistedRevision, &persistedRate, &persistedMaxCompute); err != nil {
			t.Errorf("read persisted dispatch terms: %v", err)
		} else if persistedRevision != config.PipelineRevision || persistedRate != config.RateMicroUSDPerSecond || persistedMaxCompute != 120 {
			t.Errorf("dispatch terms were not persisted before DNS resolution: revision=%q rate=%d max=%d", persistedRevision, persistedRate, persistedMaxCompute)
		}
		if err := store.CancelClippingJob(job.ID); err != nil {
			t.Errorf("cancel job at the pre-submit boundary: %v", err)
			return
		}
		app.deleteClippingCapabilities(job.ID)
	}
	client := &http.Client{Transport: clippingRuntimeRoundTripper(func(request *http.Request) (*http.Response, error) {
		roundTripCalled = true
		return clippingRuntimeResponse(request, http.StatusAccepted, `{"accepted":true,"job_id":"`+job.ID+`","attempt_id":"`+prepared.Payload.AttemptID+`","dispatch_id":"`+prepared.Payload.DispatchID+`"}`), nil
	})}
	dispatcher := newClippingWorkerDispatcher(resolver, nil, client)
	err = app.dispatchPreparedClippingStageWithDispatcher(context.Background(), prepared, 120, dispatcher)
	if err == nil || !clippingWorkerDispatchDefinitelyNotSubmitted(err) {
		t.Fatalf("canceled attempt did not produce a known-zero outcome: error=%v", err)
	}
	if roundTripCalled {
		t.Fatal("network dispatch occurred after the attempt was canceled")
	}
	canceled, err := store.ClippingJob(job.ID)
	if err != nil || canceled.Status != ClippingJobCanceled {
		t.Fatalf("job status after pre-submit cancellation=%q error=%v", canceled.Status, err)
	}
}

func TestClippingWorkerDispatchTransportErrorIsAmbiguous(t *testing.T) {
	now := time.Now().Unix()
	prepared := clippingPreparedDispatch{
		Payload: clippingStageDispatch{
			ProtocolVersion: clippingProtocolVersion, JobID: "clip_job_01", BatchID: "clip_batch_01", SourceID: "clip_source_01",
			Stage: ClippingStageAnalysis, DispatchID: "clip_job_01:analysis", AttemptID: "attempt_01",
			MediaURL: "https://framevault.example/media", CallbackURL: "https://framevault.example/callback",
			SourceDurationMS: 60_000, ExpiresAt: now + 3_600, CallbackExpiresAt: now + 3_600 + int64(clippingCallbackAccountingGrace.Seconds()),
			SourceSHA256: strings.Repeat("c", 64), ContentType: "general", Model: clippingWorkerModel,
			MinClipSeconds: 15, MaxClipSeconds: 180, CandidateLimit: 10,
		},
		MediaBearer: "media-capability", CallbackBearer: "callback-capability",
	}
	config := clippingWorkerConfig{
		Enabled: true, Endpoint: "https://fn-worker.modal.run/dispatch", BearerToken: strings.Repeat("d", 64),
		RateMicroUSDPerSecond: 1, PipelineRevision: "worker-outcome-v1",
	}
	dispatcher := newClippingWorkerDispatcher(
		&clippingRuntimeResolver{answers: []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}}, nil,
		&http.Client{Transport: clippingRuntimeRoundTripper(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("connection reset")
		})},
	)
	err := dispatcher.dispatch(context.Background(), config, prepared, 60)
	if err == nil || clippingWorkerDispatchDefinitelyNotSubmitted(err) {
		t.Fatalf("client.Do transport error was not marked ambiguous: %v", err)
	}
	var outcome *clippingWorkerDispatchOutcomeError
	if !errors.As(err, &outcome) || !outcome.submissionAttempted {
		t.Fatalf("dispatch outcome did not record that client.Do began: %#v", outcome)
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

func TestClippingWorkerEstimateMathIsBounded(t *testing.T) {
	got, err := estimateClippingWorkerCost(2_500, 120)
	if err != nil || got != 300_000 {
		t.Fatalf("estimate=%d error=%v", got, err)
	}
	if _, err := estimateClippingWorkerCost(2, math.MaxInt64); err == nil {
		t.Fatal("overflowing compute cost was accepted")
	}
	if seconds, err := clippingWorkerComputeSecondsForReservation(1_000, 100); err != nil || seconds != 10 {
		t.Fatalf("reservation compute limit=%d error=%v", seconds, err)
	}
	if seconds, err := clippingWorkerComputeSecondsForReservation(math.MaxInt64, 1); err != nil || seconds != clippingWorkerMaxAttemptComputeSeconds {
		t.Fatalf("reservation compute limit was not capped: seconds=%d error=%v", seconds, err)
	}
	if _, err := clippingWorkerComputeSecondsForReservation(99, 100); err == nil {
		t.Fatal("reservation that cannot fund one second was accepted")
	}
}

func clippingRuntimeResponse(request *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request,
	}
}
