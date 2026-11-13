package main

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"video-automation/internal/app"
)

func TestEvaluateDatasetUsesHumanMatchesAndPreservesModelRanks(t *testing.T) {
	dataset := completeEvaluationDatasetForTest(t)
	if err := validateDataset(dataset); err != nil {
		t.Fatalf("validateDataset(): %v", err)
	}
	report := evaluateDataset(dataset)
	if report.Corpus.SourceCount != 1 || report.Corpus.EligibleSourceCount != 1 || report.Corpus.CompleteSourceReviewCount != 1 || report.Corpus.EvidenceStatus != "descriptive_only" {
		t.Fatalf("corpus summary = %#v", report.Corpus)
	}
	if report.Corpus.SourceStatusCounts["ready"] != 1 || report.Corpus.RightsStatusCounts["confirmed"] != 1 ||
		report.Corpus.CoverageStatusCounts["complete"] != 1 || report.Corpus.CoverageStatusCounts["not_reviewed"] != 0 {
		t.Fatalf("corpus lifecycle and coverage counts = %#v", report.Corpus)
	}
	if report.Corpus.OpportunityCount != 2 || report.Corpus.ExportableOpportunityCount != 2 ||
		report.Corpus.OpportunityKindCounts["speech"] != 2 || report.Corpus.OpportunityLanguageCounts["en"] != 2 {
		t.Fatalf("human opportunity summary = %#v", report.Corpus)
	}
	if len(report.Runs) != 1 {
		t.Fatalf("run summaries = %#v", report.Runs)
	}
	run := report.Runs[0]
	if run.HumanReviewStatus != "complete" || run.AnalysisStatus != "valid" || run.CandidateCount != 2 {
		t.Fatalf("run review state = %#v", run)
	}
	if run.CandidateReviews.Returned != 2 || run.CandidateReviews.Reviewed != 2 || run.CandidateReviews.Uncertain != 0 || run.CandidateReviews.Missing != 0 {
		t.Fatalf("candidate review coverage = %#v", run.CandidateReviews)
	}
	if run.DurationComparisonStatus != "within_1s" || run.DurationTargetStatus != "within_40_min_target_unverified" ||
		run.ContentTypeComparisonStatus != "matches_declared_content_type" || run.LanguageComparisonStatus != "matches_operator_metadata" {
		t.Fatalf("source metadata comparisons = %#v", run)
	}
	if len(run.RanksAsReturned) != 2 || run.RanksAsReturned[0] != 1 || run.RanksAsReturned[1] != 2 {
		t.Fatalf("ranks as returned = %v", run.RanksAsReturned)
	}
	if run.TopK.ReviewedOutputs != 2 || run.TopK.ReturnedOutputs != 2 || run.TopK.UsableOutputs != 1 || run.TopK.MatchedUsable != 1 || run.TopK.Precision == nil || run.TopK.Recall == nil {
		t.Fatalf("top-k summary = %#v", run.TopK)
	}
	if math.Abs(*run.TopK.Precision-0.5) > 1e-9 || math.Abs(*run.TopK.Recall-0.5) > 1e-9 || run.TopK.PrecisionStatus != "descriptive_only" || run.TopK.RecallStatus != "descriptive_only" {
		t.Fatalf("top-k values = %#v", run.TopK)
	}
	if run.Rubric.Status != "complete" || run.Rubric.RatedCandidateCount != 2 || run.Rubric.DimensionRatingCounts["hook"] != 2 || run.Rubric.DimensionMeans["hook"] != 4 {
		t.Fatalf("rubric summary = %#v", run.Rubric)
	}
	if run.LateEventStatus != "review_complete" || run.LateOpportunityCount != 1 || run.LateMatchedOpportunityCount != 0 {
		t.Fatalf("late-event summary = %#v", run)
	}
	if run.UsageStatus != "reported" || run.PromptTokens == nil || *run.PromptTokens != 120 || run.OutputTokens == nil || *run.OutputTokens != 45 || run.TotalTokens == nil || *run.TotalTokens != 165 {
		t.Fatalf("usage summary = %#v", run)
	}
	if run.CostStatus != "unknown" || run.ActualCostUSD != nil || report.ExternalReadiness.ActualCost != "unknown" {
		t.Fatalf("unknown cost was changed into an estimate: %#v / %#v", run, report.ExternalReadiness)
	}
	if report.ExternalReadiness.ProviderAvailability != "unverified_live" || report.ExternalReadiness.LiveAuthentication != "unverified" || report.ExternalReadiness.LongVideoSupport != "preview_and_40_minute_gate_unverified" || report.ExternalReadiness.DeploymentBackup != "not_verified_by_offline_tool" {
		t.Fatalf("external readiness = %#v", report.ExternalReadiness)
	}
	if gate := readinessGateForTest(t, report, "human_quality"); gate.Status != "descriptive_only" || gate.ReasonCode != "human_labels_are_not_a_release_pass" {
		t.Fatalf("human quality gate = %#v", gate)
	}
	if gate := readinessGateForTest(t, report, "corpus_rights"); gate.Status != "unverified" || gate.ReasonCode != "operator_attestation_not_independently_verified" {
		t.Fatalf("rights gate = %#v", gate)
	}
	if gate := readinessGateForTest(t, report, "provider_usage"); gate.Status != "unverified" || gate.ReasonCode != "provider_reported_counts_not_independently_verified" {
		t.Fatalf("usage gate = %#v", gate)
	}
	if gate := readinessGateForTest(t, report, "actual_cost"); gate.Status != "insufficient_evidence" || gate.ReasonCode != "actual_cost_missing_or_partial" {
		t.Fatalf("cost gate = %#v", gate)
	}
	if len(report.Strata) != 2 || report.Strata[0].Kind != "content_type" || report.Strata[1].Kind != "language" {
		t.Fatalf("strata = %#v", report.Strata)
	}
	if len(report.RankSummary) != 2 || report.RankSummary[0].Rank != 1 || report.RankSummary[1].Rank != 2 {
		t.Fatalf("rank summary order = %#v", report.RankSummary)
	}
	if report.RankSummary[0].UsabilityRate == nil || *report.RankSummary[0].UsabilityRate != 1 || report.RankSummary[0].MatchedUsable != 1 ||
		report.RankSummary[1].UsabilityRate == nil || *report.RankSummary[1].UsabilityRate != 0 || report.RankSummary[1].MatchedUsable != 0 {
		t.Fatalf("rank evidence = %#v", report.RankSummary)
	}
	for _, stratum := range report.Strata {
		if stratum.SourceCount != 1 || stratum.ReviewCompleteCount != 1 || stratum.OperatorRightsAttestedCount != 1 || stratum.EvidenceStatus != "descriptive_only" {
			t.Fatalf("stratum = %#v", stratum)
		}
	}

	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	for _, forbidden := range []string{
		"PRIVATE_MODEL_SUMMARY", "PRIVATE_MODEL_HOOK", "PRIVATE_MODEL_RATIONALE", "PRIVATE_MODEL_CONTEXT",
		"PRIVATE_MODEL_EVIDENCE", "PRIVATE_PROMPT_VERSION", "PRIVATE_SCHEMA_VERSION", "https://", "api_key_",
	} {
		if strings.Contains(string(encoded), forbidden) {
			t.Errorf("report echoed private input marker %q", forbidden)
		}
	}
}

func TestEvaluateDatasetWithholdsRecallWhenSourceCoverageIsPartial(t *testing.T) {
	dataset := completeEvaluationDatasetForTest(t)
	end := int64(60_000)
	dataset.SourceReviews[0].CoverageStatus = "partial"
	dataset.SourceReviews[0].ReviewedEndMS = &end
	dataset.SourceReviews[0].Opportunities = dataset.SourceReviews[0].Opportunities[:1]
	dataset.RunReviews[0].CandidateReviews[1].MatchedOpportunityID = ""
	dataset.RunReviews[0].CandidateReviews[1].SameMomentConfirmed = false
	if err := validateDataset(dataset); err != nil {
		t.Fatalf("validate partial dataset: %v", err)
	}
	result := evaluateDataset(dataset)
	run := result.Runs[0]
	if result.Corpus.OpportunityCount != 1 || result.Corpus.CoverageStatusCounts["partial"] != 1 {
		t.Fatalf("partial coverage denominator was hidden: %#v", result.Corpus)
	}
	if run.HumanReviewStatus != "partial" || run.TopK.Recall != nil || run.TopK.RecallStatus != "insufficient_evidence" {
		t.Fatalf("partial source coverage was treated as complete: %#v", run)
	}
	if run.TopK.Precision == nil || run.TopK.PrecisionStatus != "descriptive_only" {
		t.Fatalf("fully reviewed candidate outputs lost their descriptive precision: %#v", run.TopK)
	}
	if run.LateEventStatus != "unknown" || run.LateOpportunityCount != 0 || run.LateMatchedOpportunityCount != 0 {
		t.Fatalf("late-source events were inferred from partial coverage: %#v", run)
	}
}

func TestEvaluateDatasetLeavesMissingHumanEvidenceUnknown(t *testing.T) {
	dataset := completeEvaluationDatasetForTest(t)
	dataset.SourceReviews = nil
	dataset.RunReviews = nil
	if err := validateDataset(dataset); err != nil {
		t.Fatalf("validate unreviewed dataset: %v", err)
	}
	result := evaluateDataset(dataset)
	run := result.Runs[0]
	if run.HumanReviewStatus != "not_reviewed" || run.TopK.Precision != nil || run.TopK.Recall != nil || run.TopK.PrecisionStatus != "insufficient_evidence" || run.TopK.RecallStatus != "insufficient_evidence" {
		t.Fatalf("missing human labels produced a metric: %#v", run)
	}
	if run.Rubric.Status != "insufficient_evidence" || result.Corpus.EligibleSourceCount != 0 || result.Corpus.EvidenceStatus != "insufficient_evidence" {
		t.Fatalf("missing coverage was treated as evidence: %#v / %#v", run.Rubric, result.Corpus)
	}
	if gate := readinessGateForTest(t, result, "human_quality"); gate.Status != "insufficient_evidence" {
		t.Fatalf("missing human labels produced readiness: %#v", gate)
	}
}

func TestEvaluateDatasetWithholdsMetricsForUncertainCandidateLabels(t *testing.T) {
	dataset := completeEvaluationDatasetForTest(t)
	dataset.RunReviews[0].CandidateReviews[0] = CandidateReview{
		Rank: 1, Status: "uncertain", Usability: "uncertain",
		StartBoundaryFit: "unknown", EndBoundaryFit: "unknown",
	}
	if err := validateDataset(dataset); err != nil {
		t.Fatalf("validate fully covered but uncertain review: %v", err)
	}
	result := evaluateDataset(dataset)
	run := result.Runs[0]
	if run.HumanReviewStatus != "partial" || run.TopK.Precision != nil || run.TopK.Recall != nil ||
		run.TopK.PrecisionStatus != "insufficient_evidence" || run.TopK.RecallStatus != "insufficient_evidence" {
		t.Fatalf("uncertain candidate label contributed to top-k metrics: %#v", run)
	}
	if run.Rubric.Status != "partial" || run.Rubric.RatedCandidateCount != 1 {
		t.Fatalf("uncertain candidate label contributed to rubric completion: %#v", run.Rubric)
	}
	if run.CandidateReviews.Returned != 2 || run.CandidateReviews.Reviewed != 1 || run.CandidateReviews.Uncertain != 1 || run.CandidateReviews.Missing != 0 {
		t.Fatalf("uncertain candidate review count = %#v", run.CandidateReviews)
	}
	if gate := readinessGateForTest(t, result, "human_quality"); gate.Status != "insufficient_evidence" {
		t.Fatalf("uncertain label passed human quality gate: %#v", gate)
	}
}

func TestEvaluateDatasetCountsMissingCandidateReviewsExplicitly(t *testing.T) {
	dataset := completeEvaluationDatasetForTest(t)
	dataset.RunReviews[0].AdjudicationStatus = "pending"
	dataset.RunReviews[0].CandidateReviews = dataset.RunReviews[0].CandidateReviews[:1]
	if err := validateDataset(dataset); err != nil {
		t.Fatalf("validate partial candidate reviews: %v", err)
	}
	run := evaluateDataset(dataset).Runs[0]
	if run.CandidateReviews.Returned != 2 || run.CandidateReviews.Reviewed != 1 || run.CandidateReviews.Missing != 1 || run.CandidateReviews.Uncertain != 0 {
		t.Fatalf("missing candidate review was hidden: %#v", run.CandidateReviews)
	}
}

func TestEvaluateDatasetFlagsDurationAndMetadataDifferencesWithoutPassingLongVideo(t *testing.T) {
	dataset := completeEvaluationDatasetForTest(t)
	dataset.SourceReviews = nil
	dataset.RunReviews = nil
	duration := int64(41 * 60 * 1000)
	dataset.Corpus[0].HumanDurationMS = &duration
	dataset.Runs[0].Options.ContentType = "gaming"
	var analysis app.ClippingGeminiAnalysis
	if err := json.Unmarshal(dataset.Runs[0].Analysis, &analysis); err != nil {
		t.Fatalf("decode fixture analysis: %v", err)
	}
	analysis.LanguageCodes = []string{"hi"}
	encoded, err := json.Marshal(analysis)
	if err != nil {
		t.Fatalf("encode fixture analysis: %v", err)
	}
	dataset.Runs[0].Analysis = encoded
	if err := validateDataset(dataset); err != nil {
		t.Fatalf("validate metadata comparison fixture: %v", err)
	}

	report := evaluateDataset(dataset)
	run := report.Runs[0]
	if run.DurationComparisonStatus != "differs_over_1s" || run.DurationTargetStatus != "above_40_min_target" ||
		run.ContentTypeComparisonStatus != "differs_from_declared_content_type" || run.LanguageComparisonStatus != "differs_from_operator_metadata" {
		t.Fatalf("metadata mismatch diagnostics = %#v", run)
	}
	if run.OperatorHumanDurationMS == nil || *run.OperatorHumanDurationMS != duration || len(run.ModelReportedLanguageCodes) != 1 || run.ModelReportedLanguageCodes[0] != "hi" {
		t.Fatalf("duration/language evidence missing: %#v", run)
	}
	if report.ExternalReadiness.LongVideoSupport != "preview_and_40_minute_gate_unverified" || readinessGateForTest(t, report, "long_video_support").Status != "unverified" {
		t.Fatalf("reported duration was treated as long-video verification: %#v", report.ExternalReadiness)
	}
}

func TestReadinessGatesKeepEmptyAndSyntheticDataOutOfReleaseClaims(t *testing.T) {
	empty, err := decodeDataset(strings.NewReader(validEmptyDataset))
	if err != nil {
		t.Fatalf("decode empty dataset: %v", err)
	}
	emptyReport := evaluateDataset(empty)
	for _, name := range []string{"human_quality", "provider_usage", "actual_cost", "deployment_backup"} {
		if gate := readinessGateForTest(t, emptyReport, name); gate.Status != "not_run" {
			t.Errorf("empty dataset %s gate = %#v", name, gate)
		}
	}
	if gate := readinessGateForTest(t, emptyReport, "corpus_rights"); gate.Status != "blocked" {
		t.Errorf("empty corpus rights gate = %#v", gate)
	}
	for _, gate := range emptyReport.ReadinessGates {
		if gate.Status == "passed" || gate.Status == "ready" || gate.Status == "verified" {
			t.Fatalf("empty dataset received success-shaped gate: %#v", gate)
		}
	}

	synthetic := completeEvaluationDatasetForTest(t)
	synthetic.Corpus[0].Status = "synthetic"
	synthetic.Corpus[0].RightsStatus = "unknown"
	if err := validateDataset(synthetic); err != nil {
		t.Fatalf("validate synthetic dataset: %v", err)
	}
	syntheticReport := evaluateDataset(synthetic)
	if gate := readinessGateForTest(t, syntheticReport, "corpus_rights"); gate.Status != "blocked" {
		t.Fatalf("synthetic fixture was treated as approved corpus: %#v", gate)
	}
	if syntheticReport.Corpus.EligibleSourceCount != 0 {
		t.Fatalf("synthetic fixture counted as eligible: %#v", syntheticReport.Corpus)
	}
	if syntheticReport.Corpus.SourceStatusCounts["synthetic"] != 1 || syntheticReport.Corpus.RightsStatusCounts["unknown"] != 1 {
		t.Fatalf("synthetic and unknown-rights status disappeared: %#v", syntheticReport.Corpus)
	}
}

func readinessGateForTest(t *testing.T, report EvaluationReport, name string) ReadinessGate {
	t.Helper()
	for _, gate := range report.ReadinessGates {
		if gate.Name == name {
			return gate
		}
	}
	t.Fatalf("readiness gate %q missing from %#v", name, report.ReadinessGates)
	return ReadinessGate{}
}

func completeEvaluationDatasetForTest(t *testing.T) Dataset {
	t.Helper()
	options := app.ClippingGeminiOptions{ContentType: "comedy", MinClipMS: 15_000, MaxClipMS: 180_000, CandidateLimit: 2}
	analysis := app.ClippingGeminiAnalysis{
		Version: app.ClippingGeminiAnalysisVersion, SourceDurationMS: 120_000,
		SourceDurationProvenance: app.ClippingGeminiModelReportedDuration,
		Summary:                  "PRIVATE_MODEL_SUMMARY_DO_NOT_ECHO", LanguageCodes: []string{"en"}, Limitations: []string{"synthetic test input"},
		Candidates: []app.ClippingGeminiCandidate{
			candidateForTest(1, 10_000, 30_000, "PRIVATE_MODEL_HOOK_ONE", 4),
			candidateForTest(2, 100_000, 118_000, "PRIVATE_MODEL_HOOK_TWO", 5),
		},
	}
	analysisJSON, err := json.Marshal(analysis)
	if err != nil {
		t.Fatal(err)
	}
	duration := int64(120_000)
	start := int64(0)
	end := duration
	return Dataset{
		Version: evaluationInputVersion,
		Corpus: []CorpusSource{{
			ID: "source-test-1", Status: "ready", RightsStatus: "confirmed", ContentType: "comedy",
			LanguageCodes: []string{"en"}, HumanDurationMS: &duration,
		}},
		Runs: []SavedRun{{
			ID: "run-test-1", SourceID: "source-test-1", Options: options,
			ProviderSnapshot: ProviderSnapshot{
				Provider: "gemini_enterprise_agent_platform", Model: "snapshot-model-private", APIVersion: "v1",
				PromptVersion: "PRIVATE_PROMPT_VERSION", PromptSHA256: strings.Repeat("a", 64),
				SchemaVersion: "PRIVATE_SCHEMA_VERSION", SchemaSHA256: strings.Repeat("b", 64), ProcessingMode: "STATIC",
			},
			Analysis: analysisJSON,
			Usage:    &UsageRecord{Status: "reported", PromptTokens: int64Pointer(120), OutputTokens: int64Pointer(45), TotalTokens: int64Pointer(165)},
		}},
		SourceReviews: []SourceReview{{
			SourceID: "source-test-1", CoverageStatus: "complete", ReviewedStartMS: &start, ReviewedEndMS: &end,
			ReviewerIDs: []string{"reviewer-1", "reviewer-2"}, AdjudicationStatus: "complete",
			Opportunities: []Opportunity{
				{ID: "opportunity-1", StartMS: 12_000, EndMS: 28_000, Kinds: []string{"speech"}, LanguageCodes: []string{"en"}, Exportable: true, Humor: "present", Emotion: "present", Context: "self_contained"},
				{ID: "opportunity-2", StartMS: 101_000, EndMS: 117_000, Kinds: []string{"speech"}, LanguageCodes: []string{"en"}, Exportable: true, Humor: "present", Emotion: "present", Context: "needs_setup"},
			},
		}},
		RunReviews: []RunReview{{
			RunID: "run-test-1", ReviewerIDs: []string{"reviewer-1", "reviewer-2"}, AdjudicationStatus: "complete",
			CandidateReviews: []CandidateReview{
				candidateReviewForTest(1, "usable", "opportunity-1", "within", "outside", 4),
				candidateReviewForTest(2, "not_usable", "opportunity-2", "outside", "within", 4),
			},
		}},
	}
}

func candidateForTest(rank int, startMS, endMS int64, hook string, score int) app.ClippingGeminiCandidate {
	return app.ClippingGeminiCandidate{
		Rank: rank, StartMS: startMS, EndMS: endMS,
		TitleVariants: []string{"Synthetic title"}, Hook: hook, Rationale: "PRIVATE_MODEL_RATIONALE",
		BoundaryRationale: "PRIVATE_MODEL_BOUNDARY", Context: "PRIVATE_MODEL_CONTEXT",
		EditorialScore: 70, Scores: app.ClippingGeminiScores{Hook: score, EmotionalImpact: score, RetentionPotential: score, Shareability: score, StandaloneContext: score},
		Evidence: []app.ClippingGeminiEvidence{{Kind: "speech", StartMS: startMS + 1_000, EndMS: startMS + 2_000, Detail: "PRIVATE_MODEL_EVIDENCE"}},
	}
}

func candidateReviewForTest(rank int, usability, opportunity, startFit, endFit string, score int) CandidateReview {
	return CandidateReview{
		Rank: rank, Status: "reviewed", Usability: usability, MatchedOpportunityID: opportunity, SameMomentConfirmed: true,
		StartBoundaryFit: startFit, EndBoundaryFit: endFit,
		Dimensions: ReviewDimensions{
			Completeness: intPointer(score), Context: intPointer(score), Hook: intPointer(score), Emotion: intPointer(score),
			Humor: intPointer(score), VisualEvidence: intPointer(score), Standalone: intPointer(score),
		},
	}
}

func int64Pointer(value int64) *int64 { return &value }
