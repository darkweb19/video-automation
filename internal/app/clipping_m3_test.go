package app

import (
	"encoding/json"
	"errors"
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

func clippingM3Selection(sourceID, contentType string, minSeconds, maxSeconds int) ClippingJobCreate {
	return ClippingJobCreate{
		SourceID: sourceID, BudgetLimitMicroUSD: 10_000,
		ContentType: contentType, MinClipSeconds: minSeconds,
		MaxClipSeconds: maxSeconds, CandidateLimit: 10,
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
		Selection   clippingSelection   `json:"selection"`
		ContentType string              `json:"content_type"`
		Reused      bool                `json:"analysis_reused"`
		Candidates  []clippingCandidate `json:"candidates"`
	}
	if err := json.Unmarshal([]byte(artifact.PayloadJSON), &payload); err != nil {
		t.Fatalf("decode analysis payload: %v", err)
	}
	return payload.Candidates
}

func TestClippingM3EstimateInvoiceCacheRestartAndSelectionEdits(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	source := makeReadyClippingSource(t, store, clippingM3FixtureDurationMS)
	firstSelection := clippingM3Selection(source.ID, "general", 15, 30)
	secondSelection := clippingM3Selection(source.ID, "comedy", 100, 180)
	batch, jobs, duplicate, err := store.CreateClippingBatch(ClippingBatchCreate{
		IdempotencyKey: "m3-estimate-cache-restart", BudgetLimitMicroUSD: 20_000,
		Jobs: []ClippingJobCreate{firstSelection, secondSelection},
	})
	if err != nil || duplicate || len(jobs) != 2 {
		t.Fatalf("create M3 jobs: batch=%+v jobs=%+v duplicate=%v error=%v", batch, jobs, duplicate, err)
	}
	firstJob, cachedJob := jobs[0], jobs[1]

	claimed, ok, err := store.ClaimClippingStage(firstJob.ID, ClippingStageAnalysis, 2_000, time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim analysis: stage=%+v claimed=%v error=%v", claimed, ok, err)
	}
	if err := store.SetClippingAttemptEstimateTerms(firstJob.ID, ClippingStageAnalysis, claimed.AttemptID, claimed.LeaseToken, 25, 20); err != nil {
		t.Fatalf("record bounded estimate terms: %v", err)
	}
	if err := store.SetClippingAttemptEstimateTerms(firstJob.ID, ClippingStageAnalysis, claimed.AttemptID, claimed.LeaseToken, 25, 20); err != nil {
		t.Fatalf("identical estimate terms should be idempotent: %v", err)
	}
	if err := store.SetClippingAttemptEstimateTerms(firstJob.ID, ClippingStageAnalysis, claimed.AttemptID, claimed.LeaseToken, 26, 20); !errors.Is(err, ErrClippingInvalidState) {
		t.Fatalf("changed estimate terms error=%v, want invalid state", err)
	}

	core := clippingM3CoreForSource(source)
	stage, isDuplicate, published, err := store.CompleteClippingStageEstimate(
		firstJob.ID, ClippingStageAnalysis, claimed.AttemptID, claimed.LeaseToken,
		250, 10, clippingM3CallbackArtifact(firstJob.ID, source.DurationMS), core,
	)
	if err != nil || isDuplicate || !published || stage.Status != ClippingStageCompleted || stage.EstimatedMicroUSD != 250 || stage.CostReconciled {
		t.Fatalf("estimated callback: stage=%+v duplicate=%v published=%v error=%v", stage, isDuplicate, published, err)
	}
	firstArtifacts, err := store.ClippingArtifacts(firstJob.ID)
	if err != nil || len(firstArtifacts) != 1 {
		t.Fatalf("published first analysis artifacts=%+v error=%v", firstArtifacts, err)
	}
	var firstPayload struct {
		Selection   clippingSelection `json:"selection"`
		ContentType string            `json:"content_type"`
	}
	if err := json.Unmarshal([]byte(firstArtifacts[0].PayloadJSON), &firstPayload); err != nil {
		t.Fatal(err)
	}
	firstCandidates := clippingM3Candidates(t, firstArtifacts[0])
	if firstPayload.Selection.MinClipSeconds != 15 || firstPayload.Selection.MaxClipSeconds != 30 || firstPayload.ContentType != "general" || len(firstCandidates) == 0 {
		t.Fatalf("initial 15–30 second selection payload=%+v candidates=%d", firstPayload, len(firstCandidates))
	}
	for _, candidate := range firstCandidates {
		if candidate.DurationMS < 15_000 || candidate.DurationMS > 30_000 {
			t.Fatalf("short selection returned out-of-range candidate: %+v", candidate)
		}
	}
	var sawLateAudio, sawLateVisual bool
	for _, candidate := range firstCandidates {
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
		t.Fatalf("full-timeline selector lost late event evidence: audio=%v visual=%v candidates=%+v", sawLateAudio, sawLateVisual, firstCandidates)
	}

	jobBeforeInvoice, err := store.ClippingJob(firstJob.ID)
	if err != nil || jobBeforeInvoice.ReservedMicroUSD != 2_000 || jobBeforeInvoice.SpentMicroUSD != 0 {
		t.Fatalf("estimate was settled as actual or released early: job=%+v error=%v", jobBeforeInvoice, err)
	}
	batchBeforeInvoice, err := store.ClippingBatch(batch.ID)
	if err != nil || batchBeforeInvoice.ReservedMicroUSD != 2_000 || batchBeforeInvoice.SpentMicroUSD != 0 {
		t.Fatalf("batch estimate accounting before invoice=%+v error=%v", batchBeforeInvoice, err)
	}
	stage, isDuplicate, published, err = store.CompleteClippingStageEstimate(
		firstJob.ID, ClippingStageAnalysis, claimed.AttemptID, claimed.LeaseToken,
		250, 10, clippingM3CallbackArtifact(firstJob.ID, source.DurationMS), core,
	)
	if err != nil || !isDuplicate || !published || stage.Status != ClippingStageCompleted {
		t.Fatalf("identical estimate callback should be idempotent: stage=%+v duplicate=%v published=%v error=%v", stage, isDuplicate, published, err)
	}
	if _, isDuplicate, _, err := store.CompleteClippingStageEstimate(
		firstJob.ID, ClippingStageAnalysis, claimed.AttemptID, claimed.LeaseToken,
		275, 11, clippingM3CallbackArtifact(firstJob.ID, source.DurationMS), core,
	); !errors.Is(err, ErrClippingStaleAttempt) || isDuplicate {
		t.Fatalf("changed duplicate callback should be rejected: duplicate=%v error=%v", isDuplicate, err)
	}

	updatedJob, updatedArtifact, err := store.UpdateClippingJobSelection(firstJob.ID, clippingM3Selection(source.ID, "comedy", 100, 180))
	if err != nil || updatedJob.MinClipSeconds != 100 || updatedJob.MaxClipSeconds != 180 || updatedJob.ContentType != "comedy" || updatedArtifact.Version != 2 {
		t.Fatalf("cached selector range/profile edit: job=%+v artifact=%+v error=%v", updatedJob, updatedArtifact, err)
	}
	var updatedPayload struct {
		Selection   clippingSelection   `json:"selection"`
		ContentType string              `json:"content_type"`
		Candidates  []clippingCandidate `json:"candidates"`
	}
	if err := json.Unmarshal([]byte(updatedArtifact.PayloadJSON), &updatedPayload); err != nil {
		t.Fatal(err)
	}
	if updatedPayload.Selection.MinClipSeconds != 100 || updatedPayload.Selection.MaxClipSeconds != 180 || updatedPayload.ContentType != "comedy" || len(updatedPayload.Candidates) == 0 {
		t.Fatalf("long comedy selection payload=%+v", updatedPayload)
	}
	var sawLongCandidate bool
	for _, candidate := range updatedPayload.Candidates {
		if candidate.DurationMS < 100_000 || candidate.DurationMS > 180_000 {
			t.Fatalf("long selection returned out-of-range candidate: %+v", candidate)
		}
		if candidate.DurationMS >= 100_000 {
			sawLongCandidate = true
		}
	}
	if !sawLongCandidate {
		t.Fatalf("100–180 second selection produced no candidate in range; count=%d", len(updatedPayload.Candidates))
	}

	longerJob, longerArtifact, err := store.UpdateClippingJobSelection(firstJob.ID, clippingM3Selection(source.ID, "podcast", 110, 180))
	if err != nil || longerJob.MinClipSeconds != 110 || longerJob.MaxClipSeconds != 180 || longerJob.ContentType != "podcast" {
		t.Fatalf("selection above 100 seconds: job=%+v error=%v", longerJob, err)
	}
	var longerPayload struct {
		Candidates []clippingCandidate `json:"candidates"`
	}
	if err := json.Unmarshal([]byte(longerArtifact.PayloadJSON), &longerPayload); err != nil {
		t.Fatal(err)
	}
	var sawOver100SecondCandidate bool
	longerDurations := make([]int64, 0, len(longerPayload.Candidates))
	for _, candidate := range longerPayload.Candidates {
		longerDurations = append(longerDurations, candidate.DurationMS)
		if candidate.DurationMS < 110_000 || candidate.DurationMS > 180_000 {
			t.Fatalf("110–180 second selection returned out-of-range candidate: %+v", candidate)
		}
		if candidate.DurationMS > 100_000 {
			sawOver100SecondCandidate = true
		}
	}
	if !sawOver100SecondCandidate {
		t.Fatalf("110–180 second selection returned no clip over 100 seconds: durations=%v", longerDurations)
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
		t.Fatalf("analysis cache after Store reopen: found=%v segments=%d error=%v", found, len(loadedCore.Transcript.Segments), err)
	}
	if loadedCore.Language.Probability != nil || loadedCore.Transcript.Segments[0].Confidence != nil || loadedCore.Context.Summary == "" || len(loadedCore.Context.Timeline) == 0 {
		t.Fatalf("cached core lost nullable ASR confidence or global context: language=%+v transcript=%+v context=%+v", loadedCore.Language, loadedCore.Transcript, loadedCore.Context)
	}
	completedFromCache, err := reopened.TryCompleteClippingJobFromAnalysisCache(cachedJob.ID)
	if err != nil || !completedFromCache {
		t.Fatalf("complete second job from persisted cache: completed=%v error=%v", completedFromCache, err)
	}
	cachedCompletedJob, err := reopened.ClippingJob(cachedJob.ID)
	if err != nil || cachedCompletedJob.Status != ClippingJobCompleted || cachedCompletedJob.ReservedMicroUSD != 0 || cachedCompletedJob.SpentMicroUSD != 0 {
		t.Fatalf("cache reuse unexpectedly dispatched/billed work: job=%+v error=%v", cachedCompletedJob, err)
	}
	cachedStages, err := reopened.ClippingStages(cachedJob.ID)
	if err != nil || len(cachedStages) != 1 || cachedStages[0].Status != ClippingStageCompleted || cachedStages[0].AttemptID != "" || cachedStages[0].EstimatedMicroUSD != 0 {
		t.Fatalf("cached completion should have no worker attempt: stages=%+v error=%v", cachedStages, err)
	}
	cachedArtifacts, err := reopened.ClippingArtifacts(cachedJob.ID)
	if err != nil || len(cachedArtifacts) != 1 {
		t.Fatalf("cached job artifacts=%+v error=%v", cachedArtifacts, err)
	}
	var cachedPayload struct {
		Reused      bool                `json:"analysis_reused"`
		Selection   clippingSelection   `json:"selection"`
		ContentType string              `json:"content_type"`
		Candidates  []clippingCandidate `json:"candidates"`
	}
	if err := json.Unmarshal([]byte(cachedArtifacts[0].PayloadJSON), &cachedPayload); err != nil {
		t.Fatal(err)
	}
	if !cachedPayload.Reused || cachedPayload.Selection.MinClipSeconds != 100 || cachedPayload.Selection.MaxClipSeconds != 180 || cachedPayload.ContentType != "comedy" || len(cachedPayload.Candidates) == 0 {
		t.Fatalf("restart cache route did not preserve source analysis and requested selector: %+v", cachedPayload)
	}
	_, over100CachedArtifact, err := reopened.UpdateClippingJobSelection(cachedJob.ID, clippingM3Selection(source.ID, "podcast", 110, 180))
	if err != nil {
		t.Fatalf("cached selector edit above 100 seconds after restart: %v", err)
	}
	var over100CachedPayload struct {
		Selection   clippingSelection   `json:"selection"`
		ContentType string              `json:"content_type"`
		Candidates  []clippingCandidate `json:"candidates"`
	}
	if err := json.Unmarshal([]byte(over100CachedArtifact.PayloadJSON), &over100CachedPayload); err != nil {
		t.Fatal(err)
	}
	if over100CachedPayload.Selection.MinClipSeconds != 110 || over100CachedPayload.Selection.MaxClipSeconds != 180 || over100CachedPayload.ContentType != "podcast" || len(over100CachedPayload.Candidates) == 0 || over100CachedPayload.Candidates[0].DurationMS <= 100_000 {
		t.Fatalf("cache did not return an over-100-second podcast candidate: %+v", over100CachedPayload)
	}
	_, editedCachedArtifact, err := reopened.UpdateClippingJobSelection(cachedJob.ID, clippingM3Selection(source.ID, "general", 15, 30))
	if err != nil {
		t.Fatalf("second cached selector edit after restart: %v", err)
	}
	var editedCachedPayload struct {
		Selection   clippingSelection   `json:"selection"`
		ContentType string              `json:"content_type"`
		Candidates  []clippingCandidate `json:"candidates"`
	}
	if err := json.Unmarshal([]byte(editedCachedArtifact.PayloadJSON), &editedCachedPayload); err != nil {
		t.Fatal(err)
	}
	if editedCachedPayload.Selection.MinClipSeconds != 15 || editedCachedPayload.Selection.MaxClipSeconds != 30 || editedCachedPayload.ContentType != "general" || len(editedCachedPayload.Candidates) == 0 {
		t.Fatalf("short selection edit after restart=%+v", editedCachedPayload)
	}

	stage, isDuplicate, err = reopened.ReconcileClippingStageCost(firstJob.ID, claimed.AttemptID, 312, "modal-invoice-test-2026-10")
	if err != nil || isDuplicate || !stage.CostReconciled || stage.ActualMicroUSD != 312 || stage.ReservedMicroUSD != 0 {
		t.Fatalf("invoice reconciliation: stage=%+v duplicate=%v error=%v", stage, isDuplicate, err)
	}
	stage, isDuplicate, err = reopened.ReconcileClippingStageCost(firstJob.ID, claimed.AttemptID, 312, "modal-invoice-test-2026-10")
	if err != nil || !isDuplicate || stage.ActualMicroUSD != 312 {
		t.Fatalf("identical invoice reconciliation should be idempotent: stage=%+v duplicate=%v error=%v", stage, isDuplicate, err)
	}
	if _, _, err := reopened.ReconcileClippingStageCost(firstJob.ID, claimed.AttemptID, 313, "different-invoice"); !errors.Is(err, ErrClippingInvalidState) {
		t.Fatalf("changed invoice reconciliation error=%v, want invalid state", err)
	}
	settledJob, err := reopened.ClippingJob(firstJob.ID)
	if err != nil || settledJob.ReservedMicroUSD != 0 || settledJob.SpentMicroUSD != 312 {
		t.Fatalf("settled job accounting=%+v error=%v", settledJob, err)
	}
	settledBatch, err := reopened.ClippingBatch(batch.ID)
	if err != nil || settledBatch.ReservedMicroUSD != 0 || settledBatch.SpentMicroUSD != 312 {
		t.Fatalf("settled batch accounting=%+v error=%v", settledBatch, err)
	}
}

func TestClippingM3CanceledLateCallbackRetainsHoldWithoutPublication(t *testing.T) {
	store := newTestStore(t)
	source := makeReadyClippingSource(t, store, clippingM3FixtureDurationMS)
	batch, jobs := makeClippingBatch(t, store, []string{source.ID}, 10_000, 10_000, "m3-canceled-late")
	job := jobs[0]
	claimed, ok, err := store.ClaimClippingStage(job.ID, ClippingStageAnalysis, 2_000, time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim analysis: stage=%+v claimed=%v error=%v", claimed, ok, err)
	}
	if err := store.SetClippingAttemptEstimateTerms(job.ID, ClippingStageAnalysis, claimed.AttemptID, claimed.LeaseToken, 25, 20); err != nil {
		t.Fatal(err)
	}
	if err := store.CancelClippingJob(job.ID); err != nil {
		t.Fatal(err)
	}
	stage, duplicate, published, err := store.CompleteClippingStageEstimate(
		job.ID, ClippingStageAnalysis, claimed.AttemptID, claimed.LeaseToken,
		125, 5, clippingM3CallbackArtifact(job.ID, source.DurationMS), clippingM3CoreForSource(source),
	)
	if err != nil || duplicate || published || stage.Status != ClippingStageCanceled || stage.CostReconciled || stage.ReservedMicroUSD != 2_000 {
		t.Fatalf("canceled late callback must remain unpublished and reserved: stage=%+v duplicate=%v published=%v error=%v", stage, duplicate, published, err)
	}
	canceledJob, err := store.ClippingJob(job.ID)
	if err != nil || canceledJob.Status != ClippingJobCanceled || canceledJob.ReservedMicroUSD != 2_000 || canceledJob.SpentMicroUSD != 0 {
		t.Fatalf("late callback changed canceled job accounting: job=%+v error=%v", canceledJob, err)
	}
	canceledBatch, err := store.ClippingBatch(batch.ID)
	if err != nil || canceledBatch.ReservedMicroUSD != 2_000 || canceledBatch.SpentMicroUSD != 0 {
		t.Fatalf("late callback changed canceled batch accounting: batch=%+v error=%v", canceledBatch, err)
	}
	artifacts, err := store.ClippingArtifacts(job.ID)
	if err != nil || len(artifacts) != 0 {
		t.Fatalf("canceled late callback published artifacts: %+v error=%v", artifacts, err)
	}
	if _, found, err := store.LoadClippingAnalysisCache(source.ID); err != nil || found {
		t.Fatalf("canceled late callback populated shared cache: found=%v error=%v", found, err)
	}
	_, duplicate, err = store.ReconcileClippingStageCost(job.ID, claimed.AttemptID, 131, "modal-canceled-invoice")
	if err != nil || duplicate {
		t.Fatalf("reconcile canceled worker invoice: duplicate=%v error=%v", duplicate, err)
	}
	canceledJob, err = store.ClippingJob(job.ID)
	if err != nil || canceledJob.Status != ClippingJobCanceled || canceledJob.ReservedMicroUSD != 0 || canceledJob.SpentMicroUSD != 131 {
		t.Fatalf("invoice reconciliation did not release canceled hold: job=%+v error=%v", canceledJob, err)
	}
}

func TestClippingM3CanceledAttemptTombstonePreservesLateCallbackSettlement(t *testing.T) {
	store, security, _, _ := clippingDashboardFixture(t)
	source := makeReadyClippingSource(t, store, clippingM3FixtureDurationMS)
	batch, jobs := makeClippingBatch(t, store, []string{source.ID}, 10_000, 10_000, "m3-canceled-tombstone-late-callback")
	job := jobs[0]
	stage := clippingM3ClaimAttempt(t, store, job.ID, true)
	app := &dashboardApp{store: store, security: security}
	currentJob, err := store.ClippingJob(job.ID)
	if err != nil {
		t.Fatalf("load claimed job: %v", err)
	}
	payload, err := buildClippingStageDispatch(currentJob, stage, source, "https://framevault.dev", time.Now())
	if err != nil {
		t.Fatalf("build prepared M3 dispatch: %v", err)
	}
	revision, err := store.ClippingAnalysisPipelineRevision()
	if err != nil {
		t.Fatal(err)
	}
	payload.PipelineRevision = revision
	mediaBearer, callbackBearer, err := app.persistClippingDispatchCapabilitiesUntil(stage, payload.CallbackExpiresAt)
	if err != nil {
		t.Fatalf("persist prepared dispatch capabilities: %v", err)
	}
	prepared := clippingPreparedDispatch{Payload: payload, MediaBearer: mediaBearer, CallbackBearer: callbackBearer}
	callbackExpiry := prepared.Payload.CallbackExpiresAt
	if callbackExpiry <= time.Now().Unix() {
		t.Fatalf("prepared callback expiry is not in the future: %d", callbackExpiry)
	}
	var indexedExpiry int64
	if err := store.db.QueryRow(`SELECT expires_at FROM clipping_capability_expiry_index WHERE job_id=? AND attempt_id=?`, job.ID, stage.AttemptID).Scan(&indexedExpiry); err != nil || indexedExpiry != callbackExpiry {
		t.Fatalf("persisted callback expiry index=%d want=%d error=%v", indexedExpiry, callbackExpiry, err)
	}
	if err := store.CancelClippingJob(job.ID); err != nil {
		t.Fatalf("cancel prepared job: %v", err)
	}
	if err := store.SetClippingSourceMediaLease(source.ID, time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatalf("set active media lease: %v", err)
	}
	if err := store.TombstoneClippingSource(source.ID); err == nil || !strings.Contains(strings.ToLower(err.Error()), "media lease") {
		t.Fatalf("active media lease did not block tombstone: %v", err)
	}
	if err := store.SetClippingSourceMediaLease(source.ID, 0); err != nil {
		t.Fatalf("clear active media lease: %v", err)
	}
	if err := store.TombstoneClippingSource(source.ID); err != nil {
		t.Fatalf("terminal canceled job should tombstone before callback expiry: %v", err)
	}
	deletedSource, err := store.ClippingSource(source.ID)
	if err != nil || deletedSource.Status != ClippingSourceDeleted || deletedSource.SHA256 != source.SHA256 || deletedSource.DurationMS != source.DurationMS {
		t.Fatalf("tombstone did not preserve callback validation metadata: source=%+v error=%v", deletedSource, err)
	}
	for _, scope := range []string{clippingCapabilityMedia, clippingCapabilityCallback, clippingCapabilityLease} {
		decrypted, err := app.clippingCapability(job.ID, stage.AttemptID, scope)
		want := map[string]string{
			clippingCapabilityMedia:    prepared.MediaBearer,
			clippingCapabilityCallback: prepared.CallbackBearer,
			clippingCapabilityLease:    stage.LeaseToken,
		}[scope]
		if err != nil || decrypted != want {
			t.Fatalf("tombstone changed unexpired %s capability: token=%q error=%v", scope, decrypted, err)
		}
	}
	var preservedIndex int64
	if err := store.db.QueryRow(`SELECT expires_at FROM clipping_capability_expiry_index WHERE job_id=? AND attempt_id=?`, job.ID, stage.AttemptID).Scan(&preservedIndex); err != nil || preservedIndex != callbackExpiry {
		t.Fatalf("tombstone changed callback expiry index=%d want=%d error=%v", preservedIndex, callbackExpiry, err)
	}

	core := clippingM3CoreForSource(source)
	core.PipelineRevision = revision
	core.AlgorithmVersions["pipeline_revision"] = revision
	callbackArtifact := clippingM3CallbackArtifact(job.ID, source.DurationMS)
	lateStage, duplicate, published, err := store.CompleteClippingStageEstimate(
		job.ID, ClippingStageAnalysis, stage.AttemptID, stage.LeaseToken, 125, 5, callbackArtifact, core,
	)
	if err != nil || duplicate || published || lateStage.Status != ClippingStageCanceled {
		t.Fatalf("late callback after tombstone should settle without publishing: stage=%+v duplicate=%v published=%v error=%v", lateStage, duplicate, published, err)
	}
	if _, duplicate, err := store.ReconcileClippingStageCost(job.ID, stage.AttemptID, 131, "modal-canceled-tombstone-invoice"); err != nil || duplicate {
		t.Fatalf("reconcile canceled late callback invoice: duplicate=%v error=%v", duplicate, err)
	}
	settledJob, err := store.ClippingJob(job.ID)
	if err != nil || settledJob.Status != ClippingJobCanceled || settledJob.ReservedMicroUSD != 0 || settledJob.SpentMicroUSD != 131 {
		t.Fatalf("late callback invoice changed canceled job accounting: job=%+v error=%v", settledJob, err)
	}
	settledBatch, err := store.ClippingBatch(batch.ID)
	if err != nil || settledBatch.ReservedMicroUSD != 0 || settledBatch.SpentMicroUSD != 131 {
		t.Fatalf("late callback invoice changed batch accounting: batch=%+v error=%v", settledBatch, err)
	}
	var attemptActual, reconciled, callbackReceived, settledAt int64
	var reference string
	if err := store.db.QueryRow(`SELECT actual_micro_usd,cost_reconciled,callback_received_at,settled_at,reconciliation_reference
		FROM clipping_stage_attempts WHERE attempt_id=?`, stage.AttemptID).Scan(&attemptActual, &reconciled, &callbackReceived, &settledAt, &reference); err != nil {
		t.Fatal(err)
	}
	if attemptActual != 131 || reconciled != 1 || callbackReceived == 0 || settledAt == 0 || reference != "modal-canceled-tombstone-invoice" {
		t.Fatalf("late callback audit settlement=%d/%d callback=%d settled=%d reference=%q", attemptActual, reconciled, callbackReceived, settledAt, reference)
	}
	if artifacts, err := store.ClippingArtifacts(job.ID); err != nil || len(artifacts) != 0 {
		t.Fatalf("late callback after tombstone published artifacts: %+v error=%v", artifacts, err)
	}
	if _, found, err := store.LoadClippingAnalysisCache(source.ID); err != nil || found {
		t.Fatalf("late callback after tombstone populated cache: found=%v error=%v", found, err)
	}
	var claims int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM clipping_analysis_claims WHERE source_id=?`, source.ID).Scan(&claims); err != nil || claims != 0 {
		t.Fatalf("tombstone retained source claim: claims=%d error=%v", claims, err)
	}
	if callback, err := app.clippingCapability(job.ID, stage.AttemptID, clippingCapabilityCallback); err != nil || callback != prepared.CallbackBearer {
		t.Fatalf("late callback/reconciliation removed unexpired callback capability: token=%q error=%v", callback, err)
	}
	for _, scope := range []string{
		clippingCapabilityMedia,
		clippingCapabilityCallback,
		clippingCapabilityLease,
		clippingCapabilityMedia + "_expiry",
		clippingCapabilityCallback + "_expiry",
	} {
		value, err := store.Setting(clippingCapabilitySettingKey(job.ID, stage.AttemptID, scope))
		if err != nil || value == "" {
			t.Fatalf("late callback/reconciliation removed unexpired capability setting %q: value=%q error=%v", scope, value, err)
		}
	}
	if err := store.db.QueryRow(`SELECT expires_at FROM clipping_capability_expiry_index WHERE job_id=? AND attempt_id=?`, job.ID, stage.AttemptID).Scan(&preservedIndex); err != nil || preservedIndex != callbackExpiry {
		t.Fatalf("late callback/reconciliation removed expiry index=%d want=%d error=%v", preservedIndex, callbackExpiry, err)
	}
}

func TestClippingM3SourceTombstoneDeletesAnalysisCache(t *testing.T) {
	store := newTestStore(t)
	source := makeReadyClippingSource(t, store, clippingM3FixtureDurationMS)
	_, jobs := makeClippingBatch(t, store, []string{source.ID}, 10_000, 10_000, "m3-source-tombstone")
	job := jobs[0]
	claimed, ok, err := store.ClaimClippingStage(job.ID, ClippingStageAnalysis, 2_000, time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim analysis: stage=%+v claimed=%v error=%v", claimed, ok, err)
	}
	if err := store.SetClippingAttemptEstimateTerms(job.ID, ClippingStageAnalysis, claimed.AttemptID, claimed.LeaseToken, 25, 20); err != nil {
		t.Fatal(err)
	}
	_, _, published, err := store.CompleteClippingStageEstimate(
		job.ID, ClippingStageAnalysis, claimed.AttemptID, claimed.LeaseToken,
		125, 5, clippingM3CallbackArtifact(job.ID, source.DurationMS), clippingM3CoreForSource(source),
	)
	if err != nil || !published {
		t.Fatalf("publish valid analysis before source cleanup: published=%v error=%v", published, err)
	}
	if _, found, err := store.LoadClippingAnalysisCache(source.ID); err != nil || !found {
		t.Fatalf("valid analysis cache missing before tombstone: found=%v error=%v", found, err)
	}
	if err := store.TombstoneClippingSource(source.ID); err != nil {
		t.Fatal(err)
	}
	deletedSource, err := store.ClippingSource(source.ID)
	if err != nil || deletedSource.Status != ClippingSourceDeleted {
		t.Fatalf("tombstoned source=%+v error=%v", deletedSource, err)
	}
	if _, found, err := store.LoadClippingAnalysisCache(source.ID); err != nil || found {
		t.Fatalf("source tombstone retained analysis cache: found=%v error=%v", found, err)
	}
	artifacts, err := store.ClippingArtifacts(job.ID)
	if err != nil || len(artifacts) != 0 {
		t.Fatalf("source tombstone retained derived analysis artifact: artifacts=%+v error=%v", artifacts, err)
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
		t.Fatalf("fixture does not satisfy canonical worker core validation: %v", err)
	}
	if len(core.Transcript.Segments) != int(source.DurationMS/10_000) || core.Transcript.Segments[0].StartMS != 0 || core.Transcript.Segments[len(core.Transcript.Segments)-1].EndMS != source.DurationMS {
		t.Fatalf("fixture transcript does not cover the full source: segments=%d last=%+v", len(core.Transcript.Segments), core.Transcript.Segments[len(core.Transcript.Segments)-1])
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
