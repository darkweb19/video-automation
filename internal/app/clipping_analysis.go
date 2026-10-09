package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	clippingAnalysisArtifactType    = "clipping.analysis"
	clippingAnalysisSchemaVersion   = "2"
	clippingAnalysisSourceVersion   = "framevault.analysis.source.v1"
	clippingAnalysisSelectorVersion = "content-aware-events-v1"
	clippingMaxTimelineRecords      = 20000
	clippingMaxCandidateInspections = 10
	clippingMaxInspectionEvents     = 32
	clippingMaxInspectionFrames     = 360
	clippingInspectionSampleRateHz  = 2
	clippingDefaultPipelineRevision = "framevault-m3-v1"
)

var clippingEditorialCue = regexp.MustCompile(`(?i)(\?|!|\b(?:wait|but|because|actually|imagine|never|finally|secret|truth|why|what if|turns out|didn.t know|you won.t believe)\b|क्यों|लेकिन|तर|किन्तु|फेरि|सुनो)`)
var clippingPipelineRevisionPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
var clippingEmotionCue = regexp.MustCompile(`(?i)(\b(?:love|hate|fear|amazing|terrible|impossible|crazy|incredible|angry|cry|laugh|laughter|wow|oh no|surprise|shocked)\b|😂|🤣|😱|😢|वाह|हासो|रोयो|डर)`)
var clippingComedyCue = regexp.MustCompile(`(?i)(\b(?:laugh|laughter|joke|funny|punchline|ridiculous|absurd|kidding)\b|😂|🤣|हासो|मजाक)`)
var clippingQuestionCue = regexp.MustCompile(`(?i)(\?|\b(?:why|how|what|when|where|who|which|क्यों|कसरी|किन)\b)`)
var clippingComedySetupCue = regexp.MustCompile(`(?i)\b(?:why|what if|because|i thought|they told me|one day|so i|you know what|setup)\b`)
var clippingComedyPayoffCue = regexp.MustCompile(`(?i)\b(?:but|actually|turns out|punchline|joke|funny|ridiculous|absurd|kidding|laugh|laughter|haha|lol|no way)\b|😂|🤣`)
var clippingComedyReactionCue = regexp.MustCompile(`(?i)\b(?:laugh|laughter|haha|lol|gasp|wow)\b|\[(?:laughter|laughing|gasp)\]|😂|🤣|😱`)
var clippingPodcastIntroCue = regexp.MustCompile(`(?i)\b(?:today|topic|question|let.s talk|we.re going to discuss|guest|welcome back|the subject is|our first point)\b`)
var clippingPodcastPayoffCue = regexp.MustCompile(`(?i)\b(?:answer|reason|because|result|what happened|in the end|finally|turns out|that.s why|the takeaway|conclusion)\b`)
var clippingGamingActionCue = regexp.MustCompile(`(?i)\b(?:win|won|kill|killed|knock|clutch|score|goal|round|boss|respawn|headshot|champion|victory|defeated|overtime)\b`)

type clippingAnalysisCore struct {
	AnalysisVersion      string                        `json:"analysis_version"`
	PipelineRevision     string                        `json:"pipeline_revision"`
	SourceSHA256         string                        `json:"source_sha256"`
	SourceDurationMS     int64                         `json:"source_duration_ms"`
	ModelVersions        map[string]any                `json:"model_versions"`
	AlgorithmVersions    map[string]any                `json:"algorithm_versions"`
	RuntimeVersions      map[string]any                `json:"runtime_versions"`
	PromptVersions       map[string]any                `json:"prompt_versions"`
	Language             clippingLanguage              `json:"language"`
	Transcript           clippingTranscript            `json:"transcript"`
	Context              clippingContext               `json:"context"`
	MediaStreams         clippingMediaStreams          `json:"media_streams"`
	AudioAnalysisStatus  string                        `json:"audio_analysis_status"`
	AudioEvents          []clippingSignalEvent         `json:"audio_events"`
	VisualEvents         []clippingSignalEvent         `json:"visual_events"`
	CandidateInspections []clippingCandidateInspection `json:"candidate_inspections"`
	Coverage             clippingAnalysisCoverage      `json:"coverage"`
}

type clippingMediaStreams struct {
	HasAudio bool `json:"has_audio"`
	HasVideo bool `json:"has_video"`
}

type clippingAnalysisCoverage struct {
	AudioScannedDurationMS  int64 `json:"audio_scanned_duration_ms"`
	VisualScannedDurationMS int64 `json:"visual_scanned_duration_ms"`
}

type clippingCandidateInspection struct {
	AnchorMS      int64                 `json:"anchor_ms"`
	StartMS       int64                 `json:"start_ms"`
	EndMS         int64                 `json:"end_ms"`
	AnchorKind    string                `json:"anchor_kind"`
	AnchorScore   float64               `json:"anchor_score"`
	SampleRateHz  int                   `json:"sample_rate_hz"`
	SampledFrames int                   `json:"sampled_frames"`
	VisualEvents  []clippingSignalEvent `json:"visual_events"`
}

type clippingLanguage struct {
	Code        string   `json:"code"`
	Probability *float64 `json:"probability"`
	Script      string   `json:"script"`
}

type clippingTranscript struct {
	Available              bool                        `json:"available"`
	Status                 string                      `json:"status"`
	OriginalScript         bool                        `json:"original_script"`
	SpeakerLabelsAvailable bool                        `json:"speaker_labels_available"`
	Segments               []clippingTranscriptSegment `json:"segments"`
}

type clippingTranscriptSegment struct {
	StartMS    int64    `json:"start_ms"`
	EndMS      int64    `json:"end_ms"`
	Text       string   `json:"text"`
	Confidence *float64 `json:"confidence"`
	SpeakerID  *string  `json:"speaker_id"`
}

type clippingContext struct {
	Summary  string                `json:"summary"`
	Topics   []clippingContextSpan `json:"topics"`
	Timeline []clippingContextSpan `json:"timeline"`
}

type clippingContextSpan struct {
	StartMS  int64    `json:"start_ms"`
	EndMS    int64    `json:"end_ms"`
	Label    string   `json:"label,omitempty"`
	Summary  string   `json:"summary,omitempty"`
	Keywords []string `json:"keywords,omitempty"`
}

type clippingSignalEvent struct {
	StartMS  int64           `json:"start_ms"`
	EndMS    int64           `json:"end_ms"`
	Kind     string          `json:"kind"`
	Score    float64         `json:"score"`
	Evidence json.RawMessage `json:"evidence,omitempty"`
}

type clippingCandidateEvidence struct {
	Kind    string `json:"kind"`
	StartMS int64  `json:"start_ms"`
	EndMS   int64  `json:"end_ms"`
	Text    string `json:"text,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

type clippingCandidateScores struct {
	Hook               int `json:"hook"`
	EmotionalImpact    int `json:"emotional_impact"`
	RetentionPotential int `json:"retention_potential"`
	Shareability       int `json:"shareability"`
	StandaloneContext  int `json:"standalone_context"`
}

type clippingCandidate struct {
	ID              string                      `json:"id"`
	StartMS         int64                       `json:"start_ms"`
	EndMS           int64                       `json:"end_ms"`
	DurationMS      int64                       `json:"duration_ms"`
	TitleVariants   []string                    `json:"title_variants"`
	Hook            string                      `json:"hook"`
	EditorialScore  int                         `json:"editorial_score"`
	Scores          clippingCandidateScores     `json:"scores"`
	Confidence      float64                     `json:"confidence"`
	ConfidenceLabel string                      `json:"confidence_label"`
	Evidence        []clippingCandidateEvidence `json:"evidence"`
}

type clippingSelection struct {
	ContentType    string `json:"content_type"`
	MinClipSeconds int    `json:"min_clip_seconds"`
	MaxClipSeconds int    `json:"max_clip_seconds"`
	CandidateLimit int    `json:"candidate_limit"`
}

type clippingSeed struct {
	AtMS  int64
	Kind  string
	Score float64
	Text  string
}

type clippingProfileSequence struct {
	Kind     string
	StartMS  int64
	EndMS    int64
	AnchorMS int64
	Evidence string
}

// decodeClippingAnalysisCore validates the complete untrusted worker payload
// against the stored source identity before it can enter the retention-bound
// cache. Analysis artifacts remain data; nothing in them is executed.
func decodeClippingAnalysisCore(payload string, source ClippingSource) (clippingAnalysisCore, error) {
	var core clippingAnalysisCore
	if len(payload) == 0 || len(payload) > maxClippingArtifactPayloadBytes || !json.Valid([]byte(payload)) {
		return core, fmt.Errorf("%w: source analysis payload is invalid or oversized", ErrClippingInvalidArtifact)
	}
	if err := json.Unmarshal([]byte(payload), &core); err != nil {
		return core, fmt.Errorf("%w: source analysis payload has an unsupported shape", ErrClippingInvalidArtifact)
	}
	if core.AnalysisVersion != clippingAnalysisSourceVersion || !validClippingPipelineRevision(core.PipelineRevision) || core.SourceSHA256 == "" || core.SourceSHA256 != source.SHA256 || len(core.SourceSHA256) != 64 || core.SourceDurationMS != source.DurationMS || !validLowerHexDigest(core.SourceSHA256) {
		return core, fmt.Errorf("%w: source hash or duration does not match", ErrClippingInvalidArtifact)
	}
	if !validClippingAnalysisProvenance(core) {
		return core, fmt.Errorf("%w: worker pipeline provenance is incomplete or unsupported", ErrClippingInvalidArtifact)
	}
	if !validClippingAnalysisMediaTimeline(core, source.DurationMS) {
		return core, fmt.Errorf("%w: source audio/video coverage or availability is inconsistent", ErrClippingInvalidArtifact)
	}
	if !core.Transcript.OriginalScript || (core.Language.Probability != nil && !finiteRatio(*core.Language.Probability)) ||
		len(core.Language.Code) > 32 || len(core.Language.Script) > 64 || len(core.Context.Summary) > 8192 ||
		!utf8.ValidString(core.Context.Summary) || len(core.Transcript.Segments) > clippingMaxTimelineRecords ||
		len(core.Context.Topics) > clippingMaxTimelineRecords || len(core.Context.Timeline) > clippingMaxTimelineRecords ||
		len(core.AudioEvents) > clippingMaxTimelineRecords || len(core.VisualEvents) > clippingMaxTimelineRecords || len(core.CandidateInspections) > clippingMaxCandidateInspections {
		return core, fmt.Errorf("%w: source analysis exceeds its timeline bounds", ErrClippingInvalidArtifact)
	}
	for _, segment := range core.Transcript.Segments {
		if !validTimelineRange(segment.StartMS, segment.EndMS, source.DurationMS) || !utf8.ValidString(segment.Text) || len(segment.Text) > 8192 ||
			(segment.Confidence != nil && !finiteRatio(*segment.Confidence)) ||
			(segment.SpeakerID != nil && (len(*segment.SpeakerID) > 128 || strings.ContainsAny(*segment.SpeakerID, "\r\n\x00"))) {
			return core, fmt.Errorf("%w: transcript contains an invalid source-time segment", ErrClippingInvalidArtifact)
		}
	}
	for _, topic := range append(append([]clippingContextSpan{}, core.Context.Topics...), core.Context.Timeline...) {
		if !validTimelineRange(topic.StartMS, topic.EndMS, source.DurationMS) || len(topic.Label) > 512 || len(topic.Summary) > 2048 || len(topic.Keywords) > 64 {
			return core, fmt.Errorf("%w: global context contains an invalid source-time span", ErrClippingInvalidArtifact)
		}
		for _, keyword := range topic.Keywords {
			if !utf8.ValidString(keyword) || len(keyword) > 256 {
				return core, fmt.Errorf("%w: global context contains an invalid keyword", ErrClippingInvalidArtifact)
			}
		}
	}
	for _, event := range append(append([]clippingSignalEvent{}, core.AudioEvents...), core.VisualEvents...) {
		if !validTimelineRange(event.StartMS, event.EndMS, source.DurationMS) || len(event.Kind) == 0 || len(event.Kind) > 64 || !utf8.ValidString(event.Kind) || !finiteRatio(event.Score) || len(event.Evidence) > 2048 ||
			(len(event.Evidence) > 0 && !json.Valid(event.Evidence)) {
			return core, fmt.Errorf("%w: timeline event is invalid", ErrClippingInvalidArtifact)
		}
	}
	for _, inspection := range core.CandidateInspections {
		if !validTimelineRange(inspection.StartMS, inspection.EndMS, source.DurationMS) || inspection.AnchorMS < inspection.StartMS || inspection.AnchorMS > inspection.EndMS ||
			inspection.AnchorKind == "" || len(inspection.AnchorKind) > 64 || !utf8.ValidString(inspection.AnchorKind) || !finiteRatio(inspection.AnchorScore) ||
			inspection.SampleRateHz != clippingInspectionSampleRateHz || inspection.SampledFrames < 0 || inspection.SampledFrames > clippingMaxInspectionFrames || len(inspection.VisualEvents) > clippingMaxInspectionEvents {
			return core, fmt.Errorf("%w: finalist sequence inspection is invalid", ErrClippingInvalidArtifact)
		}
		for _, event := range inspection.VisualEvents {
			if !validTimelineRange(event.StartMS, event.EndMS, source.DurationMS) || event.StartMS < inspection.StartMS || event.EndMS > inspection.EndMS ||
				event.Kind == "" || len(event.Kind) > 64 || !utf8.ValidString(event.Kind) || !finiteRatio(event.Score) || len(event.Evidence) > 2048 ||
				(len(event.Evidence) > 0 && !json.Valid(event.Evidence)) {
				return core, fmt.Errorf("%w: finalist visual evidence is invalid", ErrClippingInvalidArtifact)
			}
		}
	}
	return core, nil
}

func validClippingAnalysisMediaTimeline(core clippingAnalysisCore, durationMS int64) bool {
	if !core.MediaStreams.HasVideo || core.Coverage.VisualScannedDurationMS != durationMS {
		return false
	}
	if core.MediaStreams.HasAudio {
		return core.AudioAnalysisStatus == "complete" && core.Transcript.Available &&
			core.Transcript.Status == "complete" && core.Coverage.AudioScannedDurationMS == durationMS
	}
	return core.AudioAnalysisStatus == "skipped_no_audio_stream" && !core.Transcript.Available &&
		core.Transcript.Status == "unavailable_no_audio_stream" && len(core.Transcript.Segments) == 0 &&
		len(core.AudioEvents) == 0 && core.Coverage.AudioScannedDurationMS == 0
}

func validClippingAnalysisProvenance(core clippingAnalysisCore) bool {
	transcriber, model := clippingWorkerTranscriberVersion, clippingWorkerModel
	ctranslate2Version := "4.6.0"
	if !core.MediaStreams.HasAudio {
		transcriber, model = "not-used-no-audio-stream", "not-used-no-audio-stream"
		ctranslate2Version = "not-used-no-audio-stream"
	}
	modelStrings := map[string]string{
		"transcription":       transcriber,
		"model":               model,
		"language_mode":       "auto",
		"model_repository":    "Systran/faster-whisper-large-v3",
		"ctranslate2_version": ctranslate2Version,
	}
	for key, want := range modelStrings {
		if got, ok := core.ModelVersions[key].(string); !ok || got != want {
			return false
		}
	}
	snapshot, ok := core.ModelVersions["model_snapshot"].(string)
	if !ok || len(snapshot) != 40 {
		return false
	}
	for _, char := range snapshot {
		if !((char >= 'a' && char <= 'f') || (char >= '0' && char <= '9')) {
			return false
		}
	}
	weights, ok := core.ModelVersions["model_weights_sha256"].(string)
	if !ok {
		return false
	}
	if core.MediaStreams.HasAudio {
		if !validLowerHexDigest(weights) {
			return false
		}
	} else if weights != "not-used-no-audio-stream" {
		return false
	}
	audioEventsVersion := clippingWorkerAudioEventsVersion
	if !core.MediaStreams.HasAudio {
		audioEventsVersion = "not-run-no-audio-stream-v1"
	}
	algorithmStrings := map[string]string{
		"audio_events":         audioEventsVersion,
		"visual_events":        clippingWorkerVisualEventsVersion,
		"context_timeline":     clippingWorkerContextVersion,
		"candidate_inspection": "ffmpeg-dense-candidate-window-v2-profile-pool",
		"pipeline_revision":    core.PipelineRevision,
	}
	for key, want := range algorithmStrings {
		if got, ok := core.AlgorithmVersions[key].(string); !ok || got != want {
			return false
		}
	}
	for _, key := range []string{
		"ffmpeg_configuration_sha256", "ffmpeg_executable_sha256", "python_distributions_sha256",
		"os_release_sha256", "runtime_manifest_sha256",
	} {
		value, ok := core.AlgorithmVersions[key].(string)
		if !ok || !validLowerHexDigest(value) {
			return false
		}
	}
	distributionCount, ok := core.AlgorithmVersions["python_distributions_count"].(string)
	if !ok || len(distributionCount) == 0 || len(distributionCount) > 4 {
		return false
	}
	for _, char := range distributionCount {
		if char < '0' || char > '9' {
			return false
		}
	}
	count := 0
	for _, char := range distributionCount {
		count = count*10 + int(char-'0')
	}
	if count < 1 || count > 8192 {
		return false
	}
	if version, ok := core.AlgorithmVersions["ffmpeg_version"].(string); !ok || len(version) == 0 || len(version) > 512 || !utf8.ValidString(version) {
		return false
	}
	if prompt, ok := core.PromptVersions["analysis"].(string); !ok || prompt != clippingWorkerPromptVersion {
		return false
	}
	for _, key := range []string{"python", "modal"} {
		value, ok := core.RuntimeVersions[key].(string)
		if !ok || len(value) == 0 || len(value) > 64 || !utf8.ValidString(value) {
			return false
		}
	}
	return true
}

func validClippingPipelineRevision(value string) bool {
	return clippingPipelineRevisionPattern.MatchString(value)
}

func validLowerHexDigest(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validTimelineRange(start, end, duration int64) bool {
	return start >= 0 && end > start && end <= duration
}

func finiteRatio(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
}

func clippingAnalysisPipelineKey(core clippingAnalysisCore) (string, error) {
	metadata := struct {
		AnalysisVersion  string         `json:"analysis_version"`
		PipelineRevision string         `json:"pipeline_revision"`
		Models           map[string]any `json:"model_versions"`
		Algorithms       map[string]any `json:"algorithm_versions"`
		Runtime          map[string]any `json:"runtime_versions"`
		Prompts          map[string]any `json:"prompt_versions"`
	}{core.AnalysisVersion, core.PipelineRevision, core.ModelVersions, core.AlgorithmVersions, core.RuntimeVersions, core.PromptVersions}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func clippingSelectionForJob(job ClippingJob) clippingSelection {
	return clippingSelection{
		ContentType: job.ContentType, MinClipSeconds: job.MinClipSeconds,
		MaxClipSeconds: job.MaxClipSeconds, CandidateLimit: job.CandidateLimit,
	}
}

func buildClippingAnalysisArtifact(job ClippingJob, source ClippingSource, core clippingAnalysisCore, version int, costEstimate, rateMicroUSDPerSecond, computeSeconds int64) (ClippingArtifact, error) {
	if version < 1 || costEstimate < 0 || rateMicroUSDPerSecond < 0 || computeSeconds < 0 ||
		((rateMicroUSDPerSecond == 0 || computeSeconds == 0) && (rateMicroUSDPerSecond != 0 || computeSeconds != 0 || costEstimate != 0)) ||
		(computeSeconds > 0 && rateMicroUSDPerSecond > math.MaxInt64/computeSeconds) ||
		(computeSeconds > 0 && rateMicroUSDPerSecond*computeSeconds != costEstimate) {
		return ClippingArtifact{}, ErrClippingInvalidArtifact
	}
	candidates := selectClippingCandidates(core, clippingSelectionForJob(job))
	if candidates == nil {
		candidates = []clippingCandidate{}
	}
	ranges := make([]ClippingTimeRange, 0, len(candidates))
	for _, candidate := range candidates {
		ranges = append(ranges, ClippingTimeRange{StartMS: candidate.StartMS, EndMS: candidate.EndMS})
	}
	var payload map[string]json.RawMessage
	encodedCore, err := json.Marshal(core)
	if err != nil {
		return ClippingArtifact{}, err
	}
	if err := json.Unmarshal(encodedCore, &payload); err != nil {
		return ClippingArtifact{}, err
	}
	costBasis := "operator_declared_worker_second_rate_estimate"
	if computeSeconds == 0 {
		costBasis = "reused_source_analysis_no_new_worker_charge"
	}
	fields := map[string]any{
		"analysis_version":          "framevault.clipping.analysis.v2",
		"content_type":              job.ContentType,
		"selection":                 clippingSelectionForJob(job),
		"selector_version":          clippingAnalysisSelectorVersion,
		"candidates":                candidates,
		"cost_basis":                costBasis,
		"cost_estimate_micro_usd":   costEstimate,
		"rate_micro_usd_per_second": rateMicroUSDPerSecond,
		"compute_seconds":           computeSeconds,
		"score_disclaimer":          "Editorial heuristic ranking; not a probability or guarantee of virality.",
		"confidence_disclaimer":     "Uncalibrated evidence confidence; not a statistical probability.",
	}
	for key, value := range fields {
		encoded, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			return ClippingArtifact{}, marshalErr
		}
		payload[key] = encoded
	}
	encodedPayload, err := json.Marshal(payload)
	if err != nil {
		return ClippingArtifact{}, err
	}
	artifact := ClippingArtifact{
		JobID: job.ID, Type: clippingAnalysisArtifactType, SchemaVersion: clippingAnalysisSchemaVersion,
		Version: version, SourceDurationMS: source.DurationMS, TimeRanges: ranges, PayloadJSON: string(encodedPayload),
	}
	if _, err := validateClippingArtifact(artifact, job.ID, source.DurationMS); err != nil {
		return ClippingArtifact{}, err
	}
	return artifact, nil
}

func selectClippingCandidates(core clippingAnalysisCore, selection clippingSelection) []clippingCandidate {
	normalized, err := normalizeClippingSelection(ClippingJobCreate{
		ContentType: selection.ContentType, MinClipSeconds: selection.MinClipSeconds,
		MaxClipSeconds: selection.MaxClipSeconds, CandidateLimit: selection.CandidateLimit,
	})
	if err == nil {
		selection = clippingSelection{ContentType: normalized.ContentType, MinClipSeconds: normalized.MinClipSeconds, MaxClipSeconds: normalized.MaxClipSeconds, CandidateLimit: normalized.CandidateLimit}
	}
	if err != nil || core.SourceDurationMS < int64(selection.MinClipSeconds)*1000 {
		return []clippingCandidate{}
	}
	maxMS := int64(selection.MaxClipSeconds) * 1000
	profileSequences := clippingProfileSequences(core, selection.ContentType, maxMS)
	seeds := clippingAnalysisSeeds(core, selection.ContentType, profileSequences)
	if len(seeds) == 0 {
		return []clippingCandidate{}
	}
	target := preferredClippingDuration(selection)
	candidates := make([]clippingCandidate, 0, len(seeds))
	seen := make(map[string]struct{}, len(seeds))
	for _, seed := range seeds {
		start, end := refineClippingWindow(core, seed.AtMS, target, selection, profileSequences)
		if end <= start {
			continue
		}
		key := fmt.Sprintf("%d:%d", start, end)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		candidate := scoreClippingCandidate(core, selection.ContentType, start, end, seed, profileSequences)
		// Video-only sources have no speech or audio transients to carry the
		// general threshold. Admit only strong, explicitly visual-only anchors
		// at a lower ranking floor; their confidence and evidence remain
		// uncalibrated and do not imply semantic action detection.
		visualOnlyCue := !core.MediaStreams.HasAudio && len(core.Transcript.Segments) == 0 &&
			strings.HasPrefix(seed.Kind, "visual_") && seed.Score >= 0.55
		if candidate.EditorialScore >= 42 || (visualOnlyCue && candidate.EditorialScore >= 30) {
			candidates = append(candidates, candidate)
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].EditorialScore != candidates[j].EditorialScore {
			return candidates[i].EditorialScore > candidates[j].EditorialScore
		}
		return candidates[i].StartMS < candidates[j].StartMS
	})
	selected := make([]clippingCandidate, 0, selection.CandidateLimit)
	for _, candidate := range candidates {
		duplicate := false
		for _, prior := range selected {
			intersection := min64(candidate.EndMS, prior.EndMS) - max64(candidate.StartMS, prior.StartMS)
			shorter := min64(candidate.DurationMS, prior.DurationMS)
			if intersection > 0 && (float64(intersection)/float64(shorter) >= 0.75 || float64(intersection)/float64(candidate.DurationMS+prior.DurationMS-intersection) >= 0.58) {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		selected = append(selected, candidate)
		if len(selected) >= selection.CandidateLimit {
			break
		}
	}
	return selected
}

func clippingProfileSequences(core clippingAnalysisCore, contentType string, maxDurationMS int64) []clippingProfileSequence {
	if maxDurationMS < int64(ClippingDefaultMinClipSeconds)*1000 {
		return nil
	}
	segments := append([]clippingTranscriptSegment(nil), core.Transcript.Segments...)
	sort.SliceStable(segments, func(i, j int) bool { return segments[i].StartMS < segments[j].StartMS })
	sequences := make([]clippingProfileSequence, 0, 64)
	appendSequence := func(kind string, start, end, anchor int64, evidence string) {
		if end > start && end-start <= maxDurationMS && len(sequences) < 256 {
			sequences = append(sequences, clippingProfileSequence{Kind: kind, StartMS: start, EndMS: end, AnchorMS: anchor, Evidence: evidence})
		}
	}
	switch contentType {
	case "comedy", "podcast":
		for i, opening := range segments {
			openingText := strings.TrimSpace(opening.Text)
			isSetup := contentType == "comedy" && clippingComedySetupCue.MatchString(openingText)
			isIntro := contentType == "podcast" && clippingPodcastIntroCue.MatchString(openingText)
			if !isSetup && !isIntro {
				continue
			}
			for j := i + 1; j < len(segments) && j <= i+96; j++ {
				payoff := segments[j]
				if payoff.StartMS-opening.StartMS > maxDurationMS {
					break
				}
				payoffText := strings.TrimSpace(payoff.Text)
				isPayoff := contentType == "comedy" && clippingComedyPayoffCue.MatchString(payoffText) ||
					contentType == "podcast" && clippingPodcastPayoffCue.MatchString(payoffText)
				if !isPayoff {
					continue
				}
				end := payoff.EndMS
				if contentType == "comedy" {
					for k := j + 1; k < len(segments) && k <= j+8 && segments[k].StartMS-payoff.EndMS <= 8_000; k++ {
						if clippingComedyReactionCue.MatchString(segments[k].Text) {
							end = segments[k].EndMS
							break
						}
					}
					appendSequence("comedy_setup_punchline", opening.StartMS, end, payoff.StartMS,
						"Lexical setup and later payoff cues are both inside this source-time window; reaction timing is included when present.")
				} else {
					appendSequence("podcast_intro_payoff", opening.StartMS, end,
						payoff.StartMS, "Lexical topic-introduction and later payoff cues are both inside this source-time window.")
				}
				break
			}
			if len(sequences) >= 256 {
				break
			}
		}
	case "gaming":
		const bucketMS = int64(10_000)
		bestSignalByBucket := make(map[int64]clippingSignalEvent)
		for _, event := range append(append([]clippingSignalEvent(nil), core.AudioEvents...), core.VisualEvents...) {
			bucket := event.StartMS / bucketMS
			if previous, exists := bestSignalByBucket[bucket]; !exists || event.Score > previous.Score {
				bestSignalByBucket[bucket] = event
			}
		}
		for _, segment := range segments {
			if !clippingGamingActionCue.MatchString(segment.Text) {
				continue
			}
			segmentBucket := segment.StartMS / bucketMS
			var nearest clippingSignalEvent
			nearestDistance := int64(1<<63 - 1)
			for bucket := segmentBucket - 1; bucket <= segmentBucket+1; bucket++ {
				event, exists := bestSignalByBucket[bucket]
				if !exists {
					continue
				}
				distance := event.StartMS - segment.StartMS
				if distance < 0 {
					distance = -distance
				}
				if distance <= 8_000 && distance < nearestDistance {
					nearest, nearestDistance = event, distance
				}
			}
			if nearestDistance == int64(1<<63-1) {
				continue
			}
			start, end := min64(segment.StartMS, nearest.StartMS), max64(segment.EndMS, nearest.EndMS)
			appendSequence("gaming_action_with_commentary", start, end, segment.StartMS,
				"A gameplay-action transcript cue is near a measured audio or visual timeline signal; no facecam or visual action is identified.")
			if len(sequences) >= 256 {
				break
			}
		}
	case "movie":
		if len(segments) == 0 {
			return nil
		}
		groupStart, groupEnd := segments[0].StartMS, segments[0].EndMS
		groupCount := 1
		for i := 1; i <= len(segments); i++ {
			if i < len(segments) && segments[i].StartMS-segments[i-1].EndMS <= 2_500 && segments[i].EndMS-groupStart <= maxDurationMS {
				groupEnd = segments[i].EndMS
				groupCount++
				continue
			}
			if groupCount >= 2 {
				appendSequence("movie_dialogue_sequence", groupStart, groupEnd, groupStart+(groupEnd-groupStart)/2,
					"Adjacent source-timed dialogue segments are kept together as one continuity sequence.")
			}
			if i < len(segments) {
				groupStart, groupEnd, groupCount = segments[i].StartMS, segments[i].EndMS, 1
			}
			if len(sequences) >= 256 {
				break
			}
		}
	}
	return sequences
}

func clippingAnalysisSeeds(core clippingAnalysisCore, contentType string, profileSequences []clippingProfileSequence) []clippingSeed {
	seeds := make([]clippingSeed, 0, 128)
	for _, segment := range core.Transcript.Segments {
		text := strings.TrimSpace(segment.Text)
		if text == "" {
			continue
		}
		lower := strings.ToLower(text)
		if clippingEditorialCue.MatchString(text) || clippingEmotionCue.MatchString(text) || strings.Contains(lower, "[laughter]") || strings.Contains(lower, "[laughing]") || strings.Contains(lower, "[music]") {
			seedScore := 0.5
			if segment.Confidence != nil {
				seedScore = *segment.Confidence
			}
			seeds = append(seeds, clippingSeed{AtMS: segment.StartMS, Kind: "transcript_cue", Score: seedScore, Text: text})
		}
	}
	for _, topic := range core.Context.Topics {
		if topic.Label != "" || len(topic.Keywords) > 0 {
			seeds = append(seeds, clippingSeed{AtMS: topic.StartMS, Kind: "topic_transition", Score: 0.45, Text: topic.Label})
		}
	}
	for _, event := range core.AudioEvents {
		if event.Score >= 0.18 {
			seeds = append(seeds, clippingSeed{AtMS: event.StartMS, Kind: "audio_" + event.Kind, Score: event.Score})
		}
	}
	for _, event := range core.VisualEvents {
		if event.Score >= 0.2 {
			seeds = append(seeds, clippingSeed{AtMS: event.StartMS, Kind: "visual_" + event.Kind, Score: event.Score})
		}
	}
	for _, inspection := range core.CandidateInspections {
		seeds = append(seeds, clippingSeed{
			AtMS: inspection.AnchorMS, Kind: "finalist_sequence_anchor_" + inspection.AnchorKind,
			Score: inspection.AnchorScore, Text: fmt.Sprintf("Dense finalist sequence inspection sampled %d frames at %d fps.", inspection.SampledFrames, inspection.SampleRateHz),
		})
		for _, event := range inspection.VisualEvents {
			seeds = append(seeds, clippingSeed{
				AtMS: event.StartMS, Kind: "finalist_sequence_" + event.Kind, Score: event.Score,
				Text: "Dense finalist sequence visual signal; weak evidence, not a semantic label.",
			})
		}
	}
	for _, sequence := range profileSequences {
		seeds = append(seeds, clippingSeed{
			AtMS: sequence.AnchorMS, Kind: sequence.Kind, Score: 0.78, Text: sequence.Evidence,
		})
	}
	sort.SliceStable(seeds, func(i, j int) bool {
		if seeds[i].AtMS != seeds[j].AtMS {
			return seeds[i].AtMS < seeds[j].AtMS
		}
		return seeds[i].Score > seeds[j].Score
	})
	if len(seeds) > 2000 {
		// Keep the timeline distributed: retain the strongest seed from each
		// source-time bucket, including the final bucket, rather than trimming
		// everything after an early event-heavy section.
		buckets := make(map[int64]clippingSeed)
		bucketMS := int64(30_000)
		for _, seed := range seeds {
			key := seed.AtMS / bucketMS
			prior, exists := buckets[key]
			if !exists || seed.Score > prior.Score {
				buckets[key] = seed
			}
		}
		seeds = seeds[:0]
		for _, seed := range buckets {
			seeds = append(seeds, seed)
		}
		sort.Slice(seeds, func(i, j int) bool { return seeds[i].AtMS < seeds[j].AtMS })
	}
	return seeds
}

func preferredClippingDuration(selection clippingSelection) int64 {
	preferred := 60
	switch selection.ContentType {
	case "podcast":
		preferred = 90
	case "comedy":
		preferred = 40
	case "gaming":
		preferred = 55
	case "movie":
		preferred = 75
	}
	if preferred < selection.MinClipSeconds {
		preferred = selection.MinClipSeconds
	}
	if preferred > selection.MaxClipSeconds {
		preferred = selection.MaxClipSeconds
	}
	return int64(preferred) * 1000
}

func refineClippingWindow(core clippingAnalysisCore, anchorMS, targetMS int64, selection clippingSelection, profileSequences []clippingProfileSequence) (int64, int64) {
	duration := core.SourceDurationMS
	minMS := int64(selection.MinClipSeconds) * 1000
	maxMS := int64(selection.MaxClipSeconds) * 1000
	if targetMS < minMS {
		targetMS = minMS
	}
	if targetMS > maxMS {
		targetMS = maxMS
	}
	if targetMS > duration {
		targetMS = duration
	}
	start := anchorMS - targetMS/3
	if start < 0 {
		start = 0
	}
	end := start + targetMS
	if end > duration {
		end = duration
		start = end - targetMS
	}
	if start < 0 {
		start = 0
	}
	// Refine to a nearby utterance edge when it remains inside the requested
	// duration range; audio/visual-only events retain their detected boundaries.
	startCandidate, endCandidate := start, end
	for _, segment := range core.Transcript.Segments {
		if segment.StartMS >= start-3000 && segment.StartMS <= start+3000 {
			startCandidate = segment.StartMS
			break
		}
	}
	for index := len(core.Transcript.Segments) - 1; index >= 0; index-- {
		segment := core.Transcript.Segments[index]
		if segment.EndMS >= end-3000 && segment.EndMS <= end+3000 {
			endCandidate = segment.EndMS
			break
		}
	}
	if startCandidate >= 0 && endCandidate <= duration && endCandidate-startCandidate >= minMS && endCandidate-startCandidate <= maxMS {
		start, end = startCandidate, endCandidate
	}
	if end-start < minMS {
		end = min64(duration, start+minMS)
		if end-start < minMS {
			start = max64(0, end-minMS)
		}
	}
	if end-start > maxMS {
		end = start + maxMS
	}
	for _, sequence := range profileSequences {
		if anchorMS < sequence.StartMS-5000 || anchorMS > sequence.EndMS+5000 {
			continue
		}
		sequenceStart := max64(0, sequence.StartMS-1500)
		sequenceEnd := min64(duration, sequence.EndMS+1500)
		span := sequenceEnd - sequenceStart
		if span > maxMS {
			continue
		}
		desired := max64(targetMS, max64(minMS, span))
		if desired > maxMS {
			desired = maxMS
		}
		start = sequenceStart
		end = start + desired
		if end < sequenceEnd {
			end = sequenceEnd
			start = end - desired
		}
		if start < 0 {
			start = 0
			end = min64(duration, desired)
		}
		if end > duration {
			end = duration
			start = max64(0, end-desired)
		}
		break
	}
	return start, end
}

func scoreClippingCandidate(core clippingAnalysisCore, contentType string, start, end int64, seed clippingSeed, profileSequences []clippingProfileSequence) clippingCandidate {
	duration := end - start
	transcriptText := make([]string, 0, 12)
	transcriptEvidence := make([]clippingCandidateEvidence, 0, 3)
	speechMS := int64(0)
	segmentCount := 0
	confidenceSum := 0.0
	turns := make(map[string]struct{})
	for _, segment := range core.Transcript.Segments {
		if segment.EndMS <= start || segment.StartMS >= end || strings.TrimSpace(segment.Text) == "" {
			continue
		}
		segmentCount++
		speechMS += min64(segment.EndMS, end) - max64(segment.StartMS, start)
		if segment.Confidence != nil {
			confidenceSum += *segment.Confidence
		}
		transcriptText = append(transcriptText, strings.TrimSpace(segment.Text))
		if segment.SpeakerID != nil {
			turns[*segment.SpeakerID] = struct{}{}
		}
		if len(transcriptEvidence) < 3 {
			transcriptEvidence = append(transcriptEvidence, clippingCandidateEvidence{Kind: "transcript", StartMS: segment.StartMS, EndMS: segment.EndMS, Text: truncateClippingText(segment.Text, 180), Detail: "Original-script ASR segment"})
		}
	}
	joined := strings.Join(transcriptText, " ")
	wordCount := len(strings.Fields(joined))
	cueCount := len(clippingEditorialCue.FindAllString(joined, 8))
	emotionCount := len(clippingEmotionCue.FindAllString(joined, 8))
	comedyCount := len(clippingComedyCue.FindAllString(joined, 8))
	questionCount := len(clippingQuestionCue.FindAllString(joined, 8))
	avgASR := 0.0
	if segmentCount > 0 {
		confidenceCount := 0
		for _, segment := range core.Transcript.Segments {
			if segment.StartMS < end && segment.EndMS > start && segment.Confidence != nil {
				confidenceCount++
			}
		}
		if confidenceCount > 0 {
			avgASR = confidenceSum / float64(confidenceCount)
		}
	}
	speechRatio := 0.0
	if duration > 0 {
		speechRatio = math.Min(1, float64(speechMS)/float64(duration))
	}
	maxAudio, maxVisual := 0.0, 0.0
	eventEvidence := make([]clippingCandidateEvidence, 0, 5)
	for _, event := range core.AudioEvents {
		if event.EndMS <= start || event.StartMS >= end {
			continue
		}
		if event.Score > maxAudio {
			maxAudio = event.Score
		}
		if len(eventEvidence) < 5 {
			eventEvidence = append(eventEvidence, clippingCandidateEvidence{Kind: "audio_" + event.Kind, StartMS: event.StartMS, EndMS: event.EndMS, Detail: clippingSignalEvidence(event.Evidence, event.Score)})
		}
	}
	for _, event := range core.VisualEvents {
		if event.EndMS <= start || event.StartMS >= end {
			continue
		}
		if event.Score > maxVisual {
			maxVisual = event.Score
		}
		if len(eventEvidence) < 5 {
			eventEvidence = append(eventEvidence, clippingCandidateEvidence{Kind: "visual_" + event.Kind, StartMS: event.StartMS, EndMS: event.EndMS, Detail: clippingSignalEvidence(event.Evidence, event.Score)})
		}
	}
	profileEvidence := make([]clippingCandidateEvidence, 0, 2)
	for _, sequence := range profileSequences {
		if sequence.StartMS >= end || sequence.EndMS <= start || sequence.StartMS < start-1500 || sequence.EndMS > end+1500 {
			continue
		}
		profileEvidence = append(profileEvidence, clippingCandidateEvidence{
			Kind: sequence.Kind, StartMS: sequence.StartMS, EndMS: sequence.EndMS, Detail: sequence.Evidence,
		})
		if len(profileEvidence) == cap(profileEvidence) {
			break
		}
	}
	for _, evidence := range profileEvidence {
		if len(eventEvidence) < 5 {
			eventEvidence = append(eventEvidence, evidence)
		}
	}
	if seed.Kind != "" && len(eventEvidence) < 5 {
		eventEvidence = append(eventEvidence, clippingCandidateEvidence{Kind: seed.Kind, StartMS: seed.AtMS, EndMS: seed.AtMS, Text: truncateClippingText(seed.Text, 180), Detail: "Timeline-wide event or narrative cue"})
	}
	cueStrength := math.Min(1, float64(cueCount+questionCount)/4)
	hook := scorePercent(34 + 35*cueStrength + 18*math.Max(maxAudio, maxVisual) + math.Min(13, float64(wordCount)/5))
	emotional := scorePercent(24 + 48*maxAudio + 20*math.Min(1, float64(emotionCount)/3) + 8*maxVisual)
	retention := scorePercent(25 + 34*speechRatio + 20*math.Min(1, float64(segmentCount)/12) + 13*maxAudio + 8*maxVisual)
	share := scorePercent(22 + 36*math.Min(1, float64(comedyCount+questionCount)/3) + 24*maxAudio + 18*maxVisual)
	standalone := scorePercent(22 + math.Min(25, float64(wordCount)/2) + math.Min(20, float64(len(turns))*8) + 18*math.Min(1, speechRatio) + 15*avgASR)
	if segmentCount > 0 && strings.HasSuffix(strings.TrimSpace(joined), ".") {
		standalone = minInt(100, standalone+5)
	}
	scores := clippingCandidateScores{Hook: hook, EmotionalImpact: emotional, RetentionPotential: retention, Shareability: share, StandaloneContext: standalone}
	weights := clippingCandidateScores{Hook: 25, EmotionalImpact: 20, RetentionPotential: 25, Shareability: 20, StandaloneContext: 10}
	switch contentType {
	case "podcast":
		weights = clippingCandidateScores{Hook: 20, EmotionalImpact: 15, RetentionPotential: 25, Shareability: 15, StandaloneContext: 25}
	case "comedy":
		weights = clippingCandidateScores{Hook: 20, EmotionalImpact: 20, RetentionPotential: 15, Shareability: 30, StandaloneContext: 15}
	case "gaming":
		weights = clippingCandidateScores{Hook: 20, EmotionalImpact: 20, RetentionPotential: 20, Shareability: 25, StandaloneContext: 15}
	case "movie":
		weights = clippingCandidateScores{Hook: 20, EmotionalImpact: 25, RetentionPotential: 20, Shareability: 20, StandaloneContext: 15}
	}
	editorial := (hook*weights.Hook + emotional*weights.EmotionalImpact + retention*weights.RetentionPotential + share*weights.Shareability + standalone*weights.StandaloneContext) / 100
	if len(profileEvidence) > 0 {
		editorial = minInt(100, editorial+8)
	}
	confidence := 0.12 + 0.28*avgASR
	if segmentCount > 0 {
		confidence += 0.20
	}
	if maxAudio > 0 {
		confidence += 0.20 * maxAudio
	}
	if maxVisual > 0 {
		confidence += 0.20 * maxVisual
	}
	confidence = math.Max(0, math.Min(0.9, confidence))
	confidenceLabel := "heuristic, uncalibrated"
	if !core.MediaStreams.HasAudio && len(core.Transcript.Segments) == 0 {
		confidenceLabel = "low; visual-only heuristic, uncalibrated"
	}
	titles := make([]string, 0, 3)
	for _, text := range transcriptText {
		candidateTitle := truncateClippingText(text, 88)
		if candidateTitle != "" {
			titles = append(titles, candidateTitle)
		}
		if len(titles) >= 3 {
			break
		}
	}
	if len(titles) == 0 {
		titles = append(titles, fmt.Sprintf("Detected moment at %s", clippingTimestamp(start)))
	}
	hookText := titles[0]
	if len(transcriptEvidence) == 0 && len(eventEvidence) > 0 {
		hookText = "A detected audio or visual event occurs near " + clippingTimestamp(seed.AtMS) + "."
	}
	hash := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%d:%d", core.SourceSHA256, contentType, start, end)))
	return clippingCandidate{
		ID: "candidate_" + hex.EncodeToString(hash[:8]), StartMS: start, EndMS: end,
		DurationMS: duration, TitleVariants: titles, Hook: truncateClippingText(hookText, 240),
		EditorialScore: editorial, Scores: scores, Confidence: confidence,
		ConfidenceLabel: confidenceLabel, Evidence: append(transcriptEvidence, eventEvidence...),
	}
}

func clippingSignalEvidence(raw json.RawMessage, score float64) string {
	if len(raw) == 0 || string(raw) == "null" {
		return fmt.Sprintf("Detector score %.2f", score)
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return truncateClippingText(text, 180)
	}
	var fields map[string]any
	if json.Unmarshal(raw, &fields) == nil {
		if value, ok := fields["detail"].(string); ok && value != "" {
			return truncateClippingText(value, 180)
		}
	}
	return fmt.Sprintf("Detector score %.2f", score)
}

func scorePercent(value float64) int {
	return int(math.Round(math.Max(0, math.Min(100, value))))
}

func truncateClippingText(value string, limit int) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if !utf8.ValidString(value) {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func clippingTimestamp(milliseconds int64) string {
	seconds := milliseconds / 1000
	return fmt.Sprintf("%02d:%02d:%02d", seconds/3600, (seconds%3600)/60, seconds%60)
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

var errClippingAnalysisUnavailable = errors.New("clipping source analysis is unavailable")
