package app

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const clippingM3FixtureDurationMS int64 = 6 * 60 * 1000

func clippingM3CoreForSource(source ClippingSource) clippingAnalysisCore {
	segments := make([]clippingTranscriptSegment, 0, clippingM3FixtureDurationMS/10_000)
	for start := int64(0); start < source.DurationMS; start += 10_000 {
		end := min64(source.DurationMS, start+10_000)
		segments = append(segments, clippingTranscriptSegment{
			StartMS: start,
			EndMS:   end,
			Text:    "The story takes an unexpected turn, and everyone asks why it happened.",
			// A nil confidence is a supported worker result and must survive storage.
			Confidence: nil,
		})
	}
	topics := []clippingContextSpan{
		{StartMS: 0, EndMS: 60_000, Label: "opening", Summary: "The speaker introduces the story.", Keywords: []string{"opening", "story"}},
		{StartMS: 60_000, EndMS: 120_000, Label: "setup", Summary: "The speaker explains the setup.", Keywords: []string{"setup", "context"}},
		{StartMS: 120_000, EndMS: 180_000, Label: "turn", Summary: "The story changes direction.", Keywords: []string{"turn", "surprise"}},
		{StartMS: 180_000, EndMS: 240_000, Label: "reaction", Summary: "The audience reacts to the reveal.", Keywords: []string{"reaction", "reveal"}},
		{StartMS: 240_000, EndMS: source.DurationMS, Label: "payoff", Summary: "The late story payoff lands.", Keywords: []string{"payoff", "ending"}},
	}
	return clippingAnalysisCore{
		AnalysisVersion:  clippingAnalysisSourceVersion,
		PipelineRevision: "framevault-m3-v1",
		SourceSHA256:     source.SHA256,
		SourceDurationMS: source.DurationMS,
		ModelVersions: map[string]any{
			"transcription":        clippingWorkerTranscriberVersion,
			"model":                clippingWorkerModel,
			"language_mode":        "auto",
			"model_repository":     "Systran/faster-whisper-large-v3",
			"model_snapshot":       "0123456789abcdef0123456789abcdef01234567",
			"model_weights_sha256": strings.Repeat("e", 64),
			"ctranslate2_version":  "4.6.0",
		},
		AlgorithmVersions: map[string]any{
			"audio_events":                clippingWorkerAudioEventsVersion,
			"visual_events":               clippingWorkerVisualEventsVersion,
			"context_timeline":            clippingWorkerContextVersion,
			"candidate_inspection":        "ffmpeg-dense-candidate-window-v2-profile-pool",
			"ffmpeg_version":              "ffmpeg version 7.0 fixture",
			"ffmpeg_configuration_sha256": strings.Repeat("f", 64),
			"ffmpeg_executable_sha256":    strings.Repeat("b", 64),
			"python_distributions_sha256": strings.Repeat("c", 64),
			"python_distributions_count":  "42",
			"os_release_sha256":           strings.Repeat("d", 64),
			"runtime_manifest_sha256":     strings.Repeat("a", 64),
			"pipeline_revision":           "framevault-m3-v1",
		},
		PromptVersions:  map[string]any{"analysis": clippingWorkerPromptVersion},
		RuntimeVersions: map[string]any{"python": "3.11.9", "modal": "1.1.4"},
		Language:        clippingLanguage{Code: "en", Probability: nil, Script: "Latin"},
		Transcript: clippingTranscript{
			Available:              true,
			Status:                 "complete",
			OriginalScript:         true,
			SpeakerLabelsAvailable: false,
			Segments:               segments,
		},
		Context: clippingContext{
			Summary:  "A complete story develops from an introduction through a late reveal and payoff.",
			Topics:   topics,
			Timeline: append([]clippingContextSpan(nil), topics...),
		},
		MediaStreams:        clippingMediaStreams{HasAudio: true, HasVideo: true},
		AudioAnalysisStatus: "complete",
		AudioEvents: []clippingSignalEvent{
			{StartMS: 72_000, EndMS: 74_000, Kind: "non_speech_audio_transient", Score: 0.72, Evidence: json.RawMessage(`{"overlaps_speech":false,"semantic_label":null}`)},
			{StartMS: 326_000, EndMS: 330_000, Kind: "non_speech_audio_transient", Score: 0.91, Evidence: json.RawMessage(`{"overlaps_speech":false,"semantic_label":null}`)},
		},
		VisualEvents: []clippingSignalEvent{
			{StartMS: 322_000, EndMS: 334_000, Kind: "scene_motion", Score: 0.88, Evidence: json.RawMessage(`{"visual_evidence_strength":"weak"}`)},
		},
		CandidateInspections: []clippingCandidateInspection{{
			AnchorMS: 326_000, StartMS: 320_000, EndMS: 350_000,
			AnchorKind: "audio_non_speech_audio_transient", AnchorScore: 0.91,
			SampleRateHz: 2, SampledFrames: 60,
			VisualEvents: []clippingSignalEvent{{
				StartMS: 328_000, EndMS: 328_500, Kind: "dense_motion_change", Score: 0.86,
				Evidence: json.RawMessage(`{"visual_evidence_strength":"weak","sample_rate_hz":2}`),
			}},
		}},
		Coverage: clippingAnalysisCoverage{
			AudioScannedDurationMS: source.DurationMS, VisualScannedDurationMS: source.DurationMS,
		},
	}
}

func clippingM3CallbackArtifact(jobID string, durationMS int64) ClippingArtifact {
	return ClippingArtifact{
		JobID: jobID, Type: clippingAnalysisArtifactType, SchemaVersion: clippingAnalysisSchemaVersion,
		Version: 1, SourceDurationMS: durationMS, TimeRanges: []ClippingTimeRange{}, PayloadJSON: `{}`,
	}
}

func clippingM3Candidates(t *testing.T, artifact ClippingArtifact) []clippingCandidate {
	t.Helper()
	var payload struct {
		ContentType string              `json:"content_type"`
		Reused      bool                `json:"analysis_reused"`
		Candidates  []clippingCandidate `json:"candidates"`
	}
	if err := json.Unmarshal([]byte(artifact.PayloadJSON), &payload); err != nil {
		t.Fatalf("decode analysis payload: %v", err)
	}
	return payload.Candidates
}

// Keep lifecycle fixtures on the same one-job storage API as the dashboard.
func clippingM3CreateJob(t *testing.T, store *Store, sourceID, key string) ClippingJob {
	t.Helper()
	job, duplicate, err := store.CreateClippingJob(ClippingJobCreate{
		SourceID: sourceID, ContentType: "general", MinClipSeconds: 15,
		MaxClipSeconds: 180, CandidateLimit: 10,
	}, key)
	if err != nil || duplicate {
		t.Fatalf("create single analysis job: job=%+v duplicate=%v error=%v", job, duplicate, err)
	}
	return job
}

func clippingM3ClaimAttempt(t *testing.T, store *Store, jobID string) ClippingStage {
	t.Helper()
	if claimed, err := store.ClaimClippingAnalysisSource(jobID); err != nil || !claimed {
		t.Fatalf("claim source analysis for %s: claimed=%v error=%v", jobID, claimed, err)
	}
	stage, claimed, err := store.ClaimClippingStage(jobID, ClippingStageAnalysis, 0, time.Minute)
	if err != nil || !claimed {
		t.Fatalf("claim analysis stage for %s: stage=%+v claimed=%v error=%v", jobID, stage, claimed, err)
	}
	if err := store.SetClippingAnalysisClaimAttempt(jobID, stage.AttemptID); err != nil {
		t.Fatalf("bind analysis claim for %s: %v", jobID, err)
	}
	revision, err := store.ClippingAnalysisPipelineRevision()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetClippingAttemptPipelineRevision(jobID, ClippingStageAnalysis, stage.AttemptID, stage.LeaseToken, revision); err != nil {
		t.Fatalf("pin pipeline revision for %s: %v", jobID, err)
	}
	if err := store.SetClippingAttemptComputeLimit(jobID, ClippingStageAnalysis, stage.AttemptID, stage.LeaseToken, 120); err != nil {
		t.Fatalf("set callback compute limit for %s: %v", jobID, err)
	}
	return stage
}

func clippingM3Complete(t *testing.T, store *Store, job ClippingJob, stage ClippingStage, source ClippingSource, core clippingAnalysisCore, digest string) (ClippingStage, bool, bool, error) {
	t.Helper()
	return store.CompleteClippingAnalysisStage(
		job.ID, ClippingStageAnalysis, stage.AttemptID, stage.LeaseToken, digest,
		clippingM3CallbackArtifact(job.ID, source.DurationMS), core, 10,
	)
}

func clippingM3CoreForRevision(t *testing.T, store *Store, source ClippingSource) clippingAnalysisCore {
	t.Helper()
	core := clippingM3CoreForSource(source)
	revision, err := store.ClippingAnalysisPipelineRevision()
	if err != nil {
		t.Fatal(err)
	}
	core.PipelineRevision = revision
	core.AlgorithmVersions["pipeline_revision"] = revision
	return core
}

func TestClippingAnalysisCacheSurvivesRestartAndCompletesSecondJob(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	source := makeReadyClippingSource(t, store, clippingM3FixtureDurationMS)
	firstJob := clippingM3CreateJob(t, store, source.ID, "simple-analysis-cache-first")
	cachedJob := clippingM3CreateJob(t, store, source.ID, "simple-analysis-cache-second")
	stage := clippingM3ClaimAttempt(t, store, firstJob.ID)
	core := clippingM3CoreForRevision(t, store, source)
	const callbackDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	completed, duplicate, published, err := clippingM3Complete(t, store, firstJob, stage, source, core, callbackDigest)
	if err != nil || duplicate || !published || completed.Status != ClippingStageCompleted {
		t.Fatalf("complete analysis callback: stage=%+v duplicate=%v published=%v error=%v", completed, duplicate, published, err)
	}
	artifacts, err := store.ClippingArtifacts(firstJob.ID)
	if err != nil || len(artifacts) != 1 {
		t.Fatalf("published analysis artifacts=%+v error=%v", artifacts, err)
	}
	candidates := clippingM3Candidates(t, artifacts[0])
	if len(candidates) == 0 {
		t.Fatal("analysis artifact contains no candidate output")
	}
	for _, candidate := range candidates {
		if candidate.DurationMS < 15_000 || candidate.DurationMS > 180_000 {
			t.Fatalf("candidate duration outside job bounds: %+v", candidate)
		}
	}
	var sawLateAudio, sawLateVisual bool
	for _, candidate := range candidates {
		for _, evidence := range candidate.Evidence {
			if evidence.Kind == "audio_non_speech_audio_transient" && evidence.StartMS >= 320_000 {
				sawLateAudio = true
			}
			if evidence.Kind == "visual_scene_motion" && evidence.StartMS >= 320_000 {
				sawLateVisual = true
			}
		}
	}
	if !sawLateAudio || !sawLateVisual {
		t.Fatalf("analysis lost late source-timeline evidence: audio=%v visual=%v", sawLateAudio, sawLateVisual)
	}
	if _, duplicate, published, err := clippingM3Complete(t, store, firstJob, stage, source, core, callbackDigest); err != nil || !duplicate || !published {
		t.Fatalf("matching callback replay: duplicate=%v published=%v error=%v", duplicate, published, err)
	}
	if _, duplicate, _, err := clippingM3Complete(t, store, firstJob, stage, source, core, strings.Repeat("b", 64)); err == nil || duplicate {
		t.Fatalf("callback with a changed digest was accepted: duplicate=%v error=%v", duplicate, err)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	loadedCore, found, err := reopened.LoadClippingAnalysisCache(source.ID)
	if err != nil || !found || loadedCore.SourceSHA256 != source.SHA256 || loadedCore.SourceDurationMS != source.DurationMS || len(loadedCore.Transcript.Segments) != len(core.Transcript.Segments) {
		t.Fatalf("analysis cache after restart: found=%v segments=%d error=%v", found, len(loadedCore.Transcript.Segments), err)
	}
	if loadedCore.Language.Probability != nil || loadedCore.Transcript.Segments[0].Confidence != nil || loadedCore.Context.Summary == "" || len(loadedCore.Context.Timeline) == 0 {
		t.Fatalf("cache lost nullable confidence or context provenance: language=%+v transcript=%+v context=%+v", loadedCore.Language, loadedCore.Transcript, loadedCore.Context)
	}
	fromCache, err := reopened.TryCompleteClippingJobFromAnalysisCache(cachedJob.ID)
	if err != nil || !fromCache {
		t.Fatalf("complete second job from source analysis cache: completed=%v error=%v", fromCache, err)
	}
	completedJob, err := reopened.ClippingJob(cachedJob.ID)
	if err != nil || completedJob.Status != ClippingJobCompleted {
		t.Fatalf("cached job status=%+v error=%v", completedJob, err)
	}
	stages, err := reopened.ClippingStages(cachedJob.ID)
	if err != nil || len(stages) != 1 || stages[0].Status != ClippingStageCompleted || stages[0].AttemptID != "" {
		t.Fatalf("cached job unexpectedly has a worker attempt: stages=%+v error=%v", stages, err)
	}
	cachedArtifacts, err := reopened.ClippingArtifacts(cachedJob.ID)
	if err != nil || len(cachedArtifacts) != 1 {
		t.Fatalf("cached job result=%+v error=%v", cachedArtifacts, err)
	}
	var cachedPayload struct {
		Reused      bool                `json:"analysis_reused"`
		ContentType string              `json:"content_type"`
		Candidates  []clippingCandidate `json:"candidates"`
	}
	if err := json.Unmarshal([]byte(cachedArtifacts[0].PayloadJSON), &cachedPayload); err != nil {
		t.Fatal(err)
	}
	if !cachedPayload.Reused || cachedPayload.ContentType != "general" || len(cachedPayload.Candidates) == 0 {
		t.Fatalf("cache reuse lost provenance or candidate output: %+v", cachedPayload)
	}
}

func TestClippingCanceledLateCallbackIsIgnoredAndIdempotent(t *testing.T) {
	store := newTestStore(t)
	source := makeReadyClippingSource(t, store, clippingM3FixtureDurationMS)
	job := clippingM3CreateJob(t, store, source.ID, "simple-canceled-late-callback")
	stage := clippingM3ClaimAttempt(t, store, job.ID)
	if err := store.CancelClippingJob(job.ID); err != nil {
		t.Fatal(err)
	}
	core := clippingM3CoreForRevision(t, store, source)
	const callbackDigest = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	completed, duplicate, published, err := clippingM3Complete(t, store, job, stage, source, core, callbackDigest)
	if err != nil || duplicate || published || completed.Status != ClippingStageCanceled {
		t.Fatalf("canceled callback should be acknowledged without publication: stage=%+v duplicate=%v published=%v error=%v", completed, duplicate, published, err)
	}
	if artifacts, err := store.ClippingArtifacts(job.ID); err != nil || len(artifacts) != 0 {
		t.Fatalf("canceled callback published artifacts: artifacts=%+v error=%v", artifacts, err)
	}
	if _, found, err := store.LoadClippingAnalysisCache(source.ID); err != nil || found {
		t.Fatalf("canceled callback populated the source cache: found=%v error=%v", found, err)
	}
	if _, duplicate, published, err := clippingM3Complete(t, store, job, stage, source, core, callbackDigest); err != nil || !duplicate || published {
		t.Fatalf("canceled callback replay was not safely idempotent: duplicate=%v published=%v error=%v", duplicate, published, err)
	}
}

func TestClippingM3CanceledAttemptTombstoneKeepsLateCallbackNonPublishing(t *testing.T) {
	store, security, _, _ := clippingDashboardFixture(t)
	source := makeReadyClippingSource(t, store, clippingM3FixtureDurationMS)
	job := clippingM3CreateJob(t, store, source.ID, "simple-tombstone-late-callback")
	stage := clippingM3ClaimAttempt(t, store, job.ID)
	app := &dashboardApp{store: store, security: security}
	callbackExpiry := stage.LeaseExpiresAt + int64(clippingCallbackCapabilityGrace.Seconds())
	mediaBearer, callbackBearer, err := app.persistClippingDispatchCapabilitiesUntil(stage, callbackExpiry)
	if err != nil {
		t.Fatalf("persist callback capabilities: %v", err)
	}
	if err := store.CancelClippingJob(job.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.TombstoneClippingSource(source.ID); err != nil {
		t.Fatalf("tombstone canceled source: %v", err)
	}
	deleted, err := store.ClippingSource(source.ID)
	if err != nil || deleted.Status != ClippingSourceDeleted || deleted.SHA256 != source.SHA256 || deleted.DurationMS != source.DurationMS {
		t.Fatalf("tombstone lost callback identity metadata: source=%+v error=%v", deleted, err)
	}
	if token, err := app.clippingCapability(job.ID, stage.AttemptID, clippingCapabilityCallback); err != nil || token != callbackBearer {
		t.Fatalf("tombstone removed unexpired callback capability: token=%q error=%v", token, err)
	}
	if token, err := app.clippingCapability(job.ID, stage.AttemptID, clippingCapabilityMedia); err != nil || token != mediaBearer {
		t.Fatalf("tombstone removed unexpired media capability: token=%q error=%v", token, err)
	}
	core := clippingM3CoreForRevision(t, store, source)
	const callbackDigest = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	completed, duplicate, published, err := clippingM3Complete(t, store, job, stage, source, core, callbackDigest)
	if err != nil || duplicate || published || completed.Status != ClippingStageCanceled {
		t.Fatalf("late callback after tombstone should remain nonpublishing: stage=%+v duplicate=%v published=%v error=%v", completed, duplicate, published, err)
	}
	if _, duplicate, published, err := clippingM3Complete(t, store, job, stage, source, core, callbackDigest); err != nil || !duplicate || published {
		t.Fatalf("late tombstone callback replay: duplicate=%v published=%v error=%v", duplicate, published, err)
	}
	if artifacts, err := store.ClippingArtifacts(job.ID); err != nil || len(artifacts) != 0 {
		t.Fatalf("late callback after tombstone published artifacts: %+v error=%v", artifacts, err)
	}
	if _, found, err := store.LoadClippingAnalysisCache(source.ID); err != nil || found {
		t.Fatalf("late callback after tombstone populated cache: found=%v error=%v", found, err)
	}
}

func TestClippingM3LateCallbackUsesPersistedExpiryAfterRetentionShortens(t *testing.T) {
	store, security, _, _ := clippingDashboardFixture(t)
	source := makeReadyClippingSource(t, store, clippingM3FixtureDurationMS)
	job := clippingM3CreateJob(t, store, source.ID, "simple-indexed-callback-expiry")
	stage := clippingM3ClaimAttempt(t, store, job.ID)
	app := &dashboardApp{store: store, security: security}
	callbackExpiry := stage.LeaseExpiresAt + int64(clippingCallbackCapabilityGrace.Seconds())
	if _, _, err := app.persistClippingDispatchCapabilitiesUntil(stage, callbackExpiry); err != nil {
		t.Fatalf("persist dispatch callback expiry: %v", err)
	}
	var indexedExpiry int64
	if err := store.db.QueryRow(`SELECT expires_at FROM clipping_capability_expiry_index WHERE job_id=? AND attempt_id=?`, job.ID, stage.AttemptID).Scan(&indexedExpiry); err != nil || indexedExpiry != callbackExpiry {
		t.Fatalf("persisted callback expiry index=%d want %d error=%v", indexedExpiry, callbackExpiry, err)
	}

	// Move the lease past its deadline while the issued callback capability is
	// still valid. Recovery should retain the claim until the indexed expiry.
	now := time.Now().Unix()
	expiredLease := now - 1
	if _, err := store.db.Exec(`UPDATE clipping_stage_attempts SET lease_expires_at=? WHERE attempt_id=?`, expiredLease, stage.AttemptID); err != nil {
		t.Fatalf("expire callback attempt lease: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE clipping_stages SET lease_expires_at=? WHERE job_id=? AND name=?`, expiredLease, job.ID, ClippingStageAnalysis); err != nil {
		t.Fatalf("expire callback stage lease: %v", err)
	}
	if err := store.RecoverExpiredClippingStages(now); err != nil {
		t.Fatalf("recover expired dispatch: %v", err)
	}
	stages, err := store.ClippingStages(job.ID)
	if err != nil || len(stages) != 1 || stages[0].Status != ClippingStageUncertain {
		t.Fatalf("stage should await its issued callback window: stages=%+v error=%v", stages, err)
	}

	// Retention may be shortened after dispatch. The callback expiry index is
	// the durable record of the capability already issued to the worker.
	if _, err := store.db.Exec(`UPDATE clipping_sources SET retain_until=?,updated_at=? WHERE id=?`, now-1, now, source.ID); err != nil {
		t.Fatalf("shorten source retention fixture: %v", err)
	}
	shortened, err := store.ClippingSource(source.ID)
	if err != nil || shortened.RetainUntil > now {
		t.Fatalf("source retention was not shortened: retain_until=%d error=%v", shortened.RetainUntil, err)
	}

	core := clippingM3CoreForRevision(t, store, source)
	const callbackDigest = "abababababababababababababababababababababababababababababababab"
	completed, duplicate, published, err := clippingM3Complete(t, store, job, stage, source, core, callbackDigest)
	if err != nil || duplicate || !published || completed.Status != ClippingStageCompleted {
		t.Fatalf("valid late callback was not published through indexed expiry: stage=%+v duplicate=%v published=%v error=%v", completed, duplicate, published, err)
	}
	if cached, found, err := store.LoadClippingAnalysisCache(source.ID); err != nil || !found || cached.SourceSHA256 != source.SHA256 {
		t.Fatalf("late callback did not populate analysis cache: found=%v cached_sha=%q error=%v", found, cached.SourceSHA256, err)
	}
	artifacts, err := store.ClippingArtifacts(job.ID)
	if err != nil || len(artifacts) != 1 {
		t.Fatalf("late callback result artifacts=%+v error=%v", artifacts, err)
	}
	if _, duplicate, published, err := clippingM3Complete(t, store, job, stage, source, core, callbackDigest); err != nil || !duplicate || !published {
		t.Fatalf("identical late callback replay: duplicate=%v published=%v error=%v", duplicate, published, err)
	}
}

func TestClippingM3SourceTombstoneDeletesAnalysisCache(t *testing.T) {
	store := newTestStore(t)
	source := makeReadyClippingSource(t, store, clippingM3FixtureDurationMS)
	job := clippingM3CreateJob(t, store, source.ID, "simple-source-tombstone")
	stage := clippingM3ClaimAttempt(t, store, job.ID)
	core := clippingM3CoreForRevision(t, store, source)
	const callbackDigest = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	if _, duplicate, published, err := clippingM3Complete(t, store, job, stage, source, core, callbackDigest); err != nil || duplicate || !published {
		t.Fatalf("publish valid analysis before tombstone: duplicate=%v published=%v error=%v", duplicate, published, err)
	}
	if _, found, err := store.LoadClippingAnalysisCache(source.ID); err != nil || !found {
		t.Fatalf("valid analysis cache missing before tombstone: found=%v error=%v", found, err)
	}
	if err := store.TombstoneClippingSource(source.ID); err != nil {
		t.Fatal(err)
	}
	deleted, err := store.ClippingSource(source.ID)
	if err != nil || deleted.Status != ClippingSourceDeleted {
		t.Fatalf("tombstoned source=%+v error=%v", deleted, err)
	}
	if _, found, err := store.LoadClippingAnalysisCache(source.ID); err != nil || found {
		t.Fatalf("source tombstone retained analysis cache: found=%v error=%v", found, err)
	}
	artifacts, err := store.ClippingArtifacts(job.ID)
	if err != nil || len(artifacts) != 0 {
		t.Fatalf("source tombstone retained derived artifacts: artifacts=%+v error=%v", artifacts, err)
	}
}

func TestClippingM3FixtureTracksWorkerVersionsAndFullTimeline(t *testing.T) {
	store := newTestStore(t)
	source := makeReadyClippingSource(t, store, clippingM3FixtureDurationMS)
	core := clippingM3CoreForSource(source)
	if core.ModelVersions["transcription"] != clippingWorkerTranscriberVersion || core.ModelVersions["model"] != clippingWorkerModel ||
		core.AlgorithmVersions["audio_events"] != clippingWorkerAudioEventsVersion || core.AlgorithmVersions["visual_events"] != clippingWorkerVisualEventsVersion ||
		core.AlgorithmVersions["context_timeline"] != clippingWorkerContextVersion || core.PromptVersions["analysis"] != clippingWorkerPromptVersion {
		t.Fatalf("fixture version pins drifted from the worker contract: %+v", core)
	}
	if _, err := decodeClippingAnalysisCore(mustJSONClippingM3(t, core), source); err != nil {
		t.Fatalf("fixture does not satisfy worker core validation: %v", err)
	}
	if len(core.Transcript.Segments) != int(source.DurationMS/10_000) || core.Transcript.Segments[0].StartMS != 0 || core.Transcript.Segments[len(core.Transcript.Segments)-1].EndMS != source.DurationMS {
		t.Fatalf("fixture transcript does not cover the full source: segments=%d last=%+v", len(core.Transcript.Segments), core.Transcript.Segments[len(core.Transcript.Segments)-1])
	}
}

func TestClippingM3RustSceneProvenanceVersionPair(t *testing.T) {
	store := newTestStore(t)
	source := makeReadyClippingSource(t, store, clippingM3FixtureDurationMS)
	for _, tc := range []struct {
		name, visual, inspection string
		accepted bool
	}{
		{"rust pair", clippingWorkerRustVisualEventsVersion, clippingWorkerRustCandidateInspectionVersion, true},
		{"legacy ffmpeg pair", clippingWorkerVisualEventsVersion, clippingWorkerFFmpegCandidateInspectionVersion, true},
		{"rust full with ffmpeg dense", clippingWorkerRustVisualEventsVersion, clippingWorkerFFmpegCandidateInspectionVersion, false},
		{"ffmpeg full with rust dense", clippingWorkerVisualEventsVersion, clippingWorkerRustCandidateInspectionVersion, false},
		{"unknown engine", "rust-scene-unpinned", clippingWorkerRustCandidateInspectionVersion, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			core := clippingM3CoreForSource(source)
			core.AlgorithmVersions["visual_events"] = tc.visual
			core.AlgorithmVersions["candidate_inspection"] = tc.inspection
			_, err := decodeClippingAnalysisCore(mustJSONClippingM3(t, core), source)
			if (err == nil) != tc.accepted {
				t.Fatalf("provenance accepted=%v want=%v error=%v", err == nil, tc.accepted, err)
			}
		})
	}
}

func mustJSONClippingM3(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal M3 fixture: %v", err)
	}
	return string(encoded)
}
