package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func clippingM3VideoOnlyCoreForSource(source ClippingSource) clippingAnalysisCore {
	core := clippingM3CoreForSource(source)
	core.ModelVersions["transcription"] = "not-used-no-audio-stream"
	core.ModelVersions["model"] = "not-used-no-audio-stream"
	core.ModelVersions["model_snapshot"] = strings.Repeat("c", 40)
	core.ModelVersions["model_weights_sha256"] = "not-used-no-audio-stream"
	core.ModelVersions["ctranslate2_version"] = "not-used-no-audio-stream"
	core.AlgorithmVersions["audio_events"] = "not-run-no-audio-stream-v1"
	core.AlgorithmVersions["ffmpeg_configuration_sha256"] = strings.Repeat("d", 64)
	core.AlgorithmVersions["ffmpeg_executable_sha256"] = strings.Repeat("e", 64)
	core.AlgorithmVersions["python_distributions_sha256"] = strings.Repeat("f", 64)
	core.AlgorithmVersions["python_distributions_count"] = "17"
	core.AlgorithmVersions["os_release_sha256"] = strings.Repeat("a", 64)
	core.AlgorithmVersions["runtime_manifest_sha256"] = strings.Repeat("b", 64)
	core.MediaStreams = clippingMediaStreams{HasAudio: false, HasVideo: true}
	core.Transcript = clippingTranscript{
		Available: false, Status: "unavailable_no_audio_stream", OriginalScript: true,
		SpeakerLabelsAvailable: false, Segments: nil,
	}
	core.Context = clippingContext{}
	core.AudioAnalysisStatus = "skipped_no_audio_stream"
	core.AudioEvents = nil
	core.VisualEvents = []clippingSignalEvent{{
		StartMS: 70_000, EndMS: 82_000, Kind: "scene_motion", Score: 0.88,
		Evidence: json.RawMessage(`{"visual_evidence_strength":"weak"}`),
	}}
	core.CandidateInspections = []clippingCandidateInspection{{
		AnchorMS: 76_000, StartMS: 64_000, EndMS: 94_000,
		AnchorKind: "visual_scene_motion", AnchorScore: 0.88,
		SampleRateHz: 2, SampledFrames: 60,
		VisualEvents: []clippingSignalEvent{{
			StartMS: 74_000, EndMS: 78_000, Kind: "dense_motion_change", Score: 0.82,
			Evidence: json.RawMessage(`{"visual_evidence_strength":"weak","sample_rate_hz":2}`),
		}},
	}}
	core.Coverage = clippingAnalysisCoverage{
		AudioScannedDurationMS: 0, VisualScannedDurationMS: source.DurationMS,
	}
	return core
}

func TestClippingCapabilityCleanupUsesPersistedExpiryAfterRestart(t *testing.T) {
	store, security, _, _ := clippingDashboardFixture(t)
	source := makeReadyClippingSource(t, store, clippingM3FixtureDurationMS)
	job := clippingM3CreateJob(t, store, source.ID, "simple-capability-cleanup-restart")
	stage, claimed, err := store.ClaimClippingStage(job.ID, ClippingStageAnalysis, 0, time.Minute)
	if err != nil || !claimed {
		t.Fatalf("claim capability cleanup stage: stage=%+v claimed=%v error=%v", stage, claimed, err)
	}
	app := &dashboardApp{store: store, security: security}
	callbackExpiry := stage.LeaseExpiresAt + int64(clippingCallbackCapabilityGrace.Seconds())
	mediaToken, callbackToken, err := app.persistClippingDispatchCapabilitiesUntil(stage, callbackExpiry)
	if err != nil || mediaToken == "" || callbackToken == "" {
		t.Fatalf("persist encrypted attempt capabilities: media=%q callback=%q error=%v", mediaToken, callbackToken, err)
	}
	settings := []string{
		clippingCapabilityMedia,
		clippingCapabilityCallback,
		clippingCapabilityLease,
		clippingCapabilityMedia + "_expiry",
		clippingCapabilityCallback + "_expiry",
	}
	for _, scope := range settings {
		key := clippingCapabilitySettingKey(job.ID, stage.AttemptID, scope)
		value, err := store.Setting(key)
		if err != nil || value == "" {
			t.Fatalf("persisted capability setting %q missing: value=%q error=%v", scope, value, err)
		}
		if scope == clippingCapabilityCallback && value == callbackToken {
			t.Fatal("callback bearer was stored in plaintext")
		}
	}
	var indexedExpiry int64
	if err := store.db.QueryRow(`SELECT expires_at FROM clipping_capability_expiry_index WHERE job_id=? AND attempt_id=?`, job.ID, stage.AttemptID).Scan(&indexedExpiry); err != nil || indexedExpiry != callbackExpiry {
		t.Fatalf("persisted callback expiry index=%d want %d error=%v", indexedExpiry, callbackExpiry, err)
	}
	if cleaned, err := app.cleanupExpiredClippingCapabilities(time.Now().Unix(), 10); err != nil || cleaned != 0 {
		t.Fatalf("cleanup ran before capability expiry: cleaned=%d error=%v", cleaned, err)
	}
	if decrypted, err := app.clippingCapability(job.ID, stage.AttemptID, clippingCapabilityCallback); err != nil || decrypted != callbackToken {
		t.Fatalf("unexpired callback did not remain decryptable: token=%q error=%v", decrypted, err)
	}

	dataDir := store.dataDir
	if err := store.Close(); err != nil {
		t.Fatalf("close store before cleanup restart: %v", err)
	}
	reopened, err := OpenStore(dataDir)
	if err != nil {
		t.Fatalf("reopen store before capability cleanup: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	reopenedSecurity, err := NewSecurity(reopened)
	if err != nil {
		t.Fatalf("restore encryption key after restart: %v", err)
	}
	reopenedApp := &dashboardApp{store: reopened, security: reopenedSecurity}
	if decrypted, err := reopenedApp.clippingCapability(job.ID, stage.AttemptID, clippingCapabilityCallback); err != nil || decrypted != callbackToken {
		t.Fatalf("persisted callback capability did not survive restart: token=%q error=%v", decrypted, err)
	}
	cleaned, err := reopenedApp.cleanupExpiredClippingCapabilities(callbackExpiry+1, 10)
	if err != nil || cleaned != 1 {
		t.Fatalf("cleanup expired capability settings after restart: cleaned=%d error=%v", cleaned, err)
	}
	for _, scope := range settings {
		key := clippingCapabilitySettingKey(job.ID, stage.AttemptID, scope)
		var count int
		if err := reopened.db.QueryRow(`SELECT COUNT(*) FROM settings WHERE key=?`, key).Scan(&count); err != nil || count != 0 {
			t.Fatalf("expired capability setting %q remains: count=%d error=%v", scope, count, err)
		}
	}
	var retainedExpiry int64
	if err := reopened.db.QueryRow(`SELECT expires_at FROM clipping_capability_expiry_index WHERE job_id=? AND attempt_id=?`, job.ID, stage.AttemptID).Scan(&retainedExpiry); err != nil || retainedExpiry != callbackExpiry {
		t.Fatalf("cleanup discarded the issued expiry needed for recovery: expiry=%d want %d error=%v", retainedExpiry, callbackExpiry, err)
	}
	if _, err := reopenedApp.clippingCapability(job.ID, stage.AttemptID, clippingCapabilityCallback); err == nil {
		t.Fatal("expired callback capability remained decryptable after settings cleanup")
	}
	currentStages, err := reopened.ClippingStages(job.ID)
	if err != nil || len(currentStages) != 1 || currentStages[0].AttemptID != stage.AttemptID || currentStages[0].Status != ClippingStageRunning {
		t.Fatalf("settings cleanup changed the unsettled durable attempt: stages=%+v error=%v", currentStages, err)
	}
	if err := reopened.RecoverExpiredClippingStages(callbackExpiry + 1); err != nil {
		t.Fatalf("recover attempt after its persisted callback window: %v", err)
	}
	currentStages, err = reopened.ClippingStages(job.ID)
	if err != nil || len(currentStages) != 1 || currentStages[0].AttemptID != stage.AttemptID || currentStages[0].Status != ClippingStageFailed {
		t.Fatalf("expired unsettled attempt did not reach a terminal state after recovery: stages=%+v error=%v", currentStages, err)
	}
	var remainingIndex int
	if err := reopened.db.QueryRow(`SELECT COUNT(*) FROM clipping_capability_expiry_index WHERE job_id=? AND attempt_id=?`, job.ID, stage.AttemptID).Scan(&remainingIndex); err != nil || remainingIndex != 0 {
		t.Fatalf("recovered attempt retained expired callback index: count=%d error=%v", remainingIndex, err)
	}
	if cleanedAgain, err := reopenedApp.cleanupExpiredClippingCapabilities(callbackExpiry+1, 10); err != nil || cleanedAgain != 0 {
		t.Fatalf("capability cleanup was not idempotent after recovery: cleaned=%d error=%v", cleanedAgain, err)
	}
	if err := reopened.RecoverExpiredClippingStages(callbackExpiry + 1); err != nil {
		t.Fatalf("repeated recovery was not idempotent: %v", err)
	}
	currentStages, err = reopened.ClippingStages(job.ID)
	if err != nil || len(currentStages) != 1 || currentStages[0].Status != ClippingStageFailed {
		t.Fatalf("repeated recovery changed the terminal attempt: stages=%+v error=%v", currentStages, err)
	}
}

func TestClippingPreSubmitFailureReleasesSourceClaim(t *testing.T) {
	store := newTestStore(t)
	source := makeReadyClippingSource(t, store, clippingM3FixtureDurationMS)
	job := clippingM3CreateJob(t, store, source.ID, "simple-presubmit-claim-release-a")
	nextJob := clippingM3CreateJob(t, store, source.ID, "simple-presubmit-claim-release-b")
	stage := clippingM3ClaimAttempt(t, store, job.ID)
	failedStage, err := store.FailClippingStageBeforeSubmit(job.ID, ClippingStageAnalysis, stage.AttemptID, stage.LeaseToken, "dispatch preparation failed before submit")
	if err != nil || failedStage.Status != ClippingStageFailed {
		t.Fatalf("record pre-submit failure before worker submit: stage=%+v error=%v", failedStage, err)
	}
	if claimed, err := store.ClaimClippingAnalysisSource(nextJob.ID); err != nil || !claimed {
		t.Fatalf("next job could not claim after a known pre-submit failure: claimed=%v error=%v", claimed, err)
	}
}

func TestClippingLargeM3CoreEnrichesWithinArtifactLimitAndRejectsInvalidCores(t *testing.T) {
	store := newTestStore(t)
	source := makeReadyClippingSource(t, store, clippingM3FixtureDurationMS)
	core := clippingM3CoreForSource(source)
	largeText := strings.Repeat("w", 8_100)
	core.Transcript.Segments = make([]clippingTranscriptSegment, 0, 360)
	for i := 0; i < 360; i++ {
		start := int64(i) * 1_000
		core.Transcript.Segments = append(core.Transcript.Segments, clippingTranscriptSegment{
			StartMS: start, EndMS: start + 1_000, Text: largeText,
		})
	}
	coreJSON, err := json.Marshal(core)
	if err != nil {
		t.Fatal(err)
	}
	const minCoreBytes = 2_936_013 // 2.8 MiB
	const maxCoreBytes = 3_145_728 // 3 MiB
	if len(coreJSON) < minCoreBytes || len(coreJSON) > maxCoreBytes {
		t.Fatalf("fixture core size=%d bytes, want %d..%d", len(coreJSON), minCoreBytes, maxCoreBytes)
	}
	job := ClippingJob{
		ID: "clipjob_large_core", SourceID: source.ID, ContentType: "general",
		MinClipSeconds: 15, MaxClipSeconds: 180, CandidateLimit: 10,
	}
	artifact, err := buildClippingAnalysisArtifact(job, source, core, 1, 0, 0, 0)
	if err != nil {
		t.Fatalf("enrich near-limit core: %v", err)
	}
	if len(artifact.PayloadJSON) > maxClippingArtifactPayloadBytes {
		t.Fatalf("enriched analysis payload=%d bytes exceeds %d", len(artifact.PayloadJSON), maxClippingArtifactPayloadBytes)
	}
	if _, err := decodeClippingAnalysisCore("{malformed", source); !errors.Is(err, ErrClippingInvalidArtifact) {
		t.Fatalf("malformed core error=%v", err)
	}
	if _, err := decodeClippingAnalysisCore(strings.Repeat("x", maxClippingArtifactPayloadBytes+1), source); !errors.Is(err, ErrClippingInvalidArtifact) {
		t.Fatalf("oversized core error=%v", err)
	}
}

func TestClippingAnalysisCoreRejectsPythonTranscriptLimitOverflow(t *testing.T) {
	store := newTestStore(t)
	source := makeReadyClippingSource(t, store, clippingM3FixtureDurationMS)
	core := clippingM3CoreForSource(source)
	core.Transcript.Segments = make([]clippingTranscriptSegment, 20_001)
	for index := range core.Transcript.Segments {
		start := int64(index) * 16
		core.Transcript.Segments[index] = clippingTranscriptSegment{
			StartMS: start, EndMS: start + 16, Text: "x",
		}
	}
	encoded, err := json.Marshal(core)
	if err != nil {
		t.Fatal(err)
	}
	if len(core.Transcript.Segments) != 20_001 {
		t.Fatalf("cross-language overflow fixture has %d transcript segments, want 20001", len(core.Transcript.Segments))
	}
	if _, err := decodeClippingAnalysisCore(string(encoded), source); !errors.Is(err, ErrClippingInvalidArtifact) {
		t.Fatalf("Go accepted core above Python's 20000 transcript-segment limit: %v", err)
	}
}

func TestClippingProfileContinuityEvidenceDoesNotClaimFacecamDetection(t *testing.T) {
	store := newTestStore(t)
	source := makeReadyClippingSource(t, store, clippingM3FixtureDurationMS)
	tests := []struct {
		contentType string
		kind        string
		detailCue   string
		segments    []clippingTranscriptSegment
		audio       []clippingSignalEvent
	}{
		{
			contentType: "comedy", kind: "comedy_setup_punchline", detailCue: "lexical setup and later payoff",
			segments: []clippingTranscriptSegment{
				{StartMS: 0, EndMS: 3_000, Text: "The setup is that I thought this would be easy."},
				{StartMS: 3_000, EndMS: 6_000, Text: "But actually the joke is ridiculous; that is the punchline."},
				{StartMS: 6_000, EndMS: 8_000, Text: "Ha ha, laughter!"},
			},
		},
		{
			contentType: "podcast", kind: "podcast_intro_payoff", detailCue: "lexical topic-introduction and later payoff",
			segments: []clippingTranscriptSegment{
				{StartMS: 0, EndMS: 4_000, Text: "Welcome back. Today we are going to discuss the main question."},
				{StartMS: 20_000, EndMS: 24_000, Text: "Finally, the answer explains what happened and that is the takeaway."},
			},
		},
		{
			contentType: "gaming", kind: "gaming_action_with_commentary", detailCue: "measured audio or visual timeline signal",
			segments: []clippingTranscriptSegment{
				{StartMS: 20_000, EndMS: 24_000, Text: "I defeated the boss and won the round!"},
				{StartMS: 25_000, EndMS: 28_000, Text: "I did not expect that, what a clutch play."},
			},
			audio: []clippingSignalEvent{{StartMS: 27_000, EndMS: 29_000, Kind: "non_speech_audio_transient", Score: 0.82}},
		},
		{
			contentType: "movie", kind: "movie_dialogue_sequence", detailCue: "source-timed dialogue segments",
			segments: []clippingTranscriptSegment{
				{StartMS: 0, EndMS: 2_000, Text: "Are you sure we should go inside?"},
				{StartMS: 2_300, EndMS: 4_800, Text: "There is no turning back now."},
				{StartMS: 5_000, EndMS: 7_000, Text: "Then tell me what happened."},
			},
		},
	}
	for index, fixture := range tests {
		t.Run(fixture.contentType, func(t *testing.T) {
			core := clippingM3CoreForSource(source)
			core.Transcript.Segments = fixture.segments
			if len(fixture.audio) > 0 {
				core.AudioEvents = append(core.AudioEvents, fixture.audio...)
			}
			job := ClippingJob{
				ID: fmt.Sprintf("clipjob_profile_%d", index), SourceID: source.ID, ContentType: fixture.contentType,
				MinClipSeconds: 15, MaxClipSeconds: 30, CandidateLimit: 10,
			}
			artifact, err := buildClippingAnalysisArtifact(job, source, core, 1, 0, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			var payload struct {
				Candidates []clippingCandidate `json:"candidates"`
			}
			if err := json.Unmarshal([]byte(artifact.PayloadJSON), &payload); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, candidate := range payload.Candidates {
				for _, evidence := range candidate.Evidence {
					detail := strings.ToLower(evidence.Detail)
					if evidence.Kind == fixture.kind {
						found = found || strings.Contains(detail, fixture.detailCue)
					}
					for _, unsupported := range []string{"facecam detected", "facecam is visible", "facecam shows", "visual action detected", "camera shows"} {
						if strings.Contains(detail, unsupported) {
							t.Fatalf("profile evidence made an unsupported visual detection claim: %+v", evidence)
						}
					}
					if fixture.contentType == "gaming" && evidence.Kind == fixture.kind && strings.Contains(detail, fixture.detailCue) &&
						!strings.Contains(detail, "no facecam or visual action is identified") {
						t.Fatalf("gaming profile evidence does not limit its claim to measured signals: %+v", evidence)
					}
				}
			}
			if !found {
				t.Fatalf("profile %s did not preserve continuity evidence %s; candidates=%+v", fixture.contentType, fixture.kind, payload.Candidates)
			}
		})
	}
}

func TestClippingVideoOnlyAnalysisAcceptsNoAudioProvenanceAndSelectsVisualEvidence(t *testing.T) {
	store := newTestStore(t)
	source := makeReadyClippingSource(t, store, clippingM3FixtureDurationMS)
	core := clippingM3VideoOnlyCoreForSource(source)
	revision, err := store.ClippingAnalysisPipelineRevision()
	if err != nil {
		t.Fatal(err)
	}
	core.PipelineRevision = revision
	core.AlgorithmVersions["pipeline_revision"] = revision
	coreJSON, err := json.Marshal(core)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeClippingAnalysisCore(string(coreJSON), source); err != nil {
		t.Fatalf("decode valid video-only core: %v", err)
	}
	job := clippingM3CreateJob(t, store, source.ID, "simple-video-only-persist-callback")
	stage := clippingM3ClaimAttempt(t, store, job.ID)
	completed, callbackDuplicate, published, err := clippingM3Complete(t, store, job, stage, source, core, strings.Repeat("f", 64))
	if err != nil || callbackDuplicate || !published || completed.Status != ClippingStageCompleted {
		t.Fatalf("persist video-only callback result: stage=%+v duplicate=%v published=%v error=%v", completed, callbackDuplicate, published, err)
	}
	artifacts, err := store.ClippingArtifacts(job.ID)
	if err != nil || len(artifacts) != 1 {
		t.Fatalf("load persisted video-only artifact: artifacts=%+v error=%v", artifacts, err)
	}
	candidates := clippingM3Candidates(t, artifacts[0])
	if len(candidates) == 0 {
		t.Fatal("video-only analysis produced no visual candidates")
	}
	visualEvidenceFound := false
	for _, candidate := range candidates {
		for _, evidence := range candidate.Evidence {
			if evidence.Kind == "transcript" || strings.HasPrefix(evidence.Kind, "audio_") {
				t.Fatalf("video-only candidate contains unavailable audio evidence: %+v", evidence)
			}
			if evidence.Kind == "visual_scene_motion" {
				visualEvidenceFound = true
			}
		}
	}
	if !visualEvidenceFound {
		t.Fatalf("video-only candidates lack visual event evidence: %+v", candidates)
	}

	invalidMutations := []struct {
		name   string
		mutate func(*clippingAnalysisCore)
	}{
		{name: "transcript_available", mutate: func(mutated *clippingAnalysisCore) { mutated.Transcript.Available = true }},
		{name: "transcript_status", mutate: func(mutated *clippingAnalysisCore) { mutated.Transcript.Status = "complete" }},
		{name: "audio_status", mutate: func(mutated *clippingAnalysisCore) { mutated.AudioAnalysisStatus = "complete" }},
		{name: "audio_coverage", mutate: func(mutated *clippingAnalysisCore) { mutated.Coverage.AudioScannedDurationMS = source.DurationMS }},
	}
	for _, mutation := range invalidMutations {
		t.Run(mutation.name, func(t *testing.T) {
			mutated := core
			mutation.mutate(&mutated)
			payload, err := json.Marshal(mutated)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeClippingAnalysisCore(string(payload), source); !errors.Is(err, ErrClippingInvalidArtifact) {
				t.Fatalf("inconsistent video-only provenance error=%v", err)
			}
		})
	}
}

func TestClippingVideoOnlyPythonWireArtifactPassesGoCallback(t *testing.T) {
	fixture, err := os.ReadFile("../../workers/modal/testdata/video_only_source_core.v2.json")
	if err != nil {
		t.Fatal(err)
	}
	var core clippingAnalysisCore
	if err := json.Unmarshal(fixture, &core); err != nil {
		t.Fatalf("decode Python worker source-core fixture: %v", err)
	}
	if core.AlgorithmVersions["audio_events"] != "not-run-no-audio-stream-v1" {
		t.Fatalf("Python video-only audio provenance=%v", core.AlgorithmVersions["audio_events"])
	}
	if err := decodeVideoOnlyFixtureSource(t, core); err != nil {
		t.Fatalf("validate Python worker video-only core: %v", err)
	}
	store := newTestStore(t)
	if err := store.SetSetting(clippingAnalysisPipelineRevisionSetting, core.PipelineRevision); err != nil {
		t.Fatal(err)
	}
	source := makeReadyClippingSource(t, store, core.SourceDurationMS)
	if source.SHA256 != core.SourceSHA256 {
		t.Fatalf("fixture source digest=%s want=%s", core.SourceSHA256, source.SHA256)
	}
	job := clippingM3CreateJob(t, store, source.ID, "simple-python-video-only-callback")
	stage := clippingM3ClaimAttempt(t, store, job.ID)
	completed, duplicate, published, err := clippingM3Complete(t, store, job, stage, source, core, strings.Repeat("1", 64))
	if err != nil || duplicate || !published || completed.Status != ClippingStageCompleted {
		t.Fatalf("persist Python video-only callback: stage=%+v duplicate=%v published=%v error=%v", completed, duplicate, published, err)
	}
	artifacts, err := store.ClippingArtifacts(job.ID)
	if err != nil || len(artifacts) != 1 {
		t.Fatalf("load Python video-only callback artifact: artifacts=%+v error=%v", artifacts, err)
	}
	if candidates := clippingM3Candidates(t, artifacts[0]); len(candidates) == 0 {
		t.Fatal("Python video-only wire artifact produced no visual candidates")
	}
}

func decodeVideoOnlyFixtureSource(t *testing.T, core clippingAnalysisCore) error {
	t.Helper()
	source := ClippingSource{
		ID: "clipsrc_python_wire_fixture", Status: ClippingSourceReady, SHA256: strings.Repeat("a", 64),
		DurationMS: core.SourceDurationMS,
	}
	encoded, err := json.Marshal(core)
	if err != nil {
		return err
	}
	_, err = decodeClippingAnalysisCore(string(encoded), source)
	return err
}
