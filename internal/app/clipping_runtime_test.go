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

func TestLegacyWorkerDispatchContractBoundsCallbackExpiry(t *testing.T) {
	now := time.Now().Unix()
	input := clippingStageDispatch{
		ProtocolVersion: clippingProtocolVersion, JobID: "clip_job_01", BatchID: "clip_batch_01", SourceID: "clip_source_01",
		Stage: ClippingStageAnalysis, DispatchID: "clip_job_01:analysis", AttemptID: "attempt_01",
		MediaURL: "https://framevault.example/media", CallbackURL: "https://framevault.example/callback",
		SourceDurationMS: 60_000, ExpiresAt: now + 3_600,
		CallbackExpiresAt: now + 3_600 + int64(clippingCallbackCapabilityGrace.Seconds()),
		SourceSHA256:      strings.Repeat("a", 64), ContentType: "general", Model: clippingWorkerModel,
		MinClipSeconds: 15, MaxClipSeconds: 180, CandidateLimit: 10, PipelineRevision: "m3.2.1-operator",
	}
	wire, err := buildClippingWorkerDispatchV2(input, clippingWorkerProtocolCompatibilityRate, 120)
	if err != nil || wire.RateMicroUSDPerSecond != 1 || wire.MaxComputeSeconds != 120 || wire.PipelineRevision != input.PipelineRevision || wire.CallbackExpiresAt != input.CallbackExpiresAt {
		t.Fatalf("historical v2 contract=%+v error=%v", wire, err)
	}
	input.CallbackExpiresAt++
	if _, err := buildClippingWorkerDispatchV2(input, clippingWorkerProtocolCompatibilityRate, 120); err == nil {
		t.Fatal("accepted callback expiry beyond bounded capability window")
	}
	input.CallbackExpiresAt = now
	if _, err := buildClippingWorkerDispatchV2(input, clippingWorkerProtocolCompatibilityRate, 120); err == nil {
		t.Fatal("accepted an expired callback window")
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
