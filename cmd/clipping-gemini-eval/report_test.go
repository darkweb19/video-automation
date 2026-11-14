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
		report.Corpus.ReadyRightsSourceCount != 1 || report.Corpus.CoverageStatusCounts["complete"] != 1 || report.Corpus.CoverageStatusCounts["ineligible"] != 0 {
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
		run.ModelDurationTargetStatus != "within_40_min_target_unverified" ||
		run.ContentTypeComparisonStatus != "matches_declared_content_type" || run.LanguageComparisonStatus != "matches_operator_metadata" {
		t.Fatalf("source metadata comparisons = %#v", run)
	}
	if run.SourceEligibilityStatus != "eligible_for_candidate_review" {
		t.Fatalf("source eligibility = %q", run.SourceEligibilityStatus)
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
	if len(report.Strata) != 4 || report.Strata[0].Kind != "content_type" || report.Strata[1].Kind != "duration" ||
		report.Strata[2].Kind != "language" || report.Strata[3].Kind != "language_by_content_type" {
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
		if stratum.SourceCount != 1 || stratum.EligibleSourceCount != 1 || stratum.ReviewCompleteCount != 1 ||
			stratum.OperatorRightsAttestedCount != 1 || stratum.EvidenceStatus != "descriptive_only" {
			t.Fatalf("stratum = %#v", stratum)
		}
		if stratum.Kind == "duration" && stratum.Key != "under_15m" {
			t.Fatalf("duration bucket = %#v", stratum)
		}
		if stratum.Kind == "language_by_content_type" && stratum.Key != "comedy/en" {
			t.Fatalf("language/content bucket = %#v", stratum)
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
	dataset.RunReviews[0].CandidateReviews[1].StartBoundaryFit = "unknown"
	dataset.RunReviews[0].CandidateReviews[1].EndBoundaryFit = "unknown"
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
	if run.TopK.Precision != nil || run.TopK.PrecisionStatus != "insufficient_evidence" {
		t.Fatalf("source-partial panel contributed to quality precision: %#v", run.TopK)
	}
	if run.LateEventStatus != "insufficient_evidence" || run.LateOpportunityCount != 0 || run.LateMatchedOpportunityCount != 0 {
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
	if run.Rubric.Status != "insufficient_evidence" || run.Rubric.RatedCandidateCount != 0 || len(run.Rubric.DimensionMeans) != 0 {
		t.Fatalf("uncertain candidate label contributed to rubric metrics: %#v", run.Rubric)
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

func TestSyntheticHumanLabelsDoNotContributeToQualityEvidence(t *testing.T) {
	dataset := completeEvaluationDatasetForTest(t)
	dataset.Corpus[0].Status = "synthetic"
	dataset.Corpus[0].RightsStatus = "unknown"
	dataset.Runs[0].Usage = &UsageRecord{Status: "reported", PromptTokens: int64Pointer(10), OutputTokens: int64Pointer(5), TotalTokens: int64Pointer(15)}
	syntheticCost := 0.01
	dataset.Runs[0].ActualCostUSD = &syntheticCost
	dataset.Runs[0].ActualCostEvidenceID = "synthetic-cost-record"
	if err := validateDataset(dataset); err != nil {
		t.Fatalf("validate labeled synthetic dataset: %v", err)
	}
	report := evaluateDataset(dataset)
	run := report.Runs[0]
	if run.SourceEligibilityStatus != "synthetic_source" || run.HumanReviewStatus != "ineligible_source" {
		t.Fatalf("synthetic source eligibility/review status = %#v", run)
	}
	if run.CandidateReviews.Reviewed != 2 {
		t.Fatalf("synthetic annotation coverage was hidden: %#v", run.CandidateReviews)
	}
	if run.UsageStatus != "ineligible_source" || run.PromptTokens != nil || run.CostStatus != "ineligible_source" || run.ActualCostUSD != nil {
		t.Fatalf("synthetic provider metadata contributed to readiness: %#v", run)
	}
	if run.TopK.Precision != nil || run.TopK.Recall != nil || run.TopK.PrecisionStatus != "ineligible_source" || run.TopK.RecallStatus != "ineligible_source" {
		t.Fatalf("synthetic labels produced quality metrics: %#v", run.TopK)
	}
	if run.Rubric.RatedCandidateCount != 0 || run.Rubric.Status != "ineligible_source" || len(report.RankSummary) != 0 {
		t.Fatalf("synthetic labels contributed to aggregate quality evidence: rubric=%#v ranks=%#v", run.Rubric, report.RankSummary)
	}
	if report.Corpus.EligibleSourceCount != 0 || report.Corpus.OpportunityCount != 0 || report.ExternalReadiness.HumanQuality != "insufficient_evidence" {
		t.Fatalf("synthetic labels changed quality readiness: corpus=%#v external=%#v", report.Corpus, report.ExternalReadiness)
	}
	if report.Corpus.CoverageStatusCounts["ineligible"] != 1 || report.Corpus.CoverageStatusCounts["complete"] != 0 || report.ExternalReadiness.ActualCost != "unknown" {
		t.Fatalf("synthetic labels or costs entered eligible aggregates: corpus=%#v external=%#v", report.Corpus, report.ExternalReadiness)
	}
	if gate := readinessGateForTest(t, report, "human_quality"); gate.Status != "insufficient_evidence" {
		t.Fatalf("synthetic labels passed the human-quality gate: %#v", gate)
	}
	for _, gateName := range []string{"provider_usage", "actual_cost"} {
		if gate := readinessGateForTest(t, report, gateName); gate.Status != "insufficient_evidence" {
			t.Fatalf("synthetic data affected %s readiness: %#v", gateName, gate)
		}
	}
	for _, stratum := range report.Strata {
		if stratum.SourceCount != 1 || stratum.EligibleSourceCount != 0 || stratum.ReviewCompleteCount != 0 || stratum.OpportunityCount != 0 {
			t.Fatalf("synthetic source entered eligible stratum coverage: %#v", stratum)
		}
	}
}

func TestHumanQualityGateRequiresEveryEligibleRunToHaveCompleteReviews(t *testing.T) {
	dataset := completeEvaluationDatasetForTest(t)
	secondRun := dataset.Runs[0]
	secondRun.ID = "run-test-2"
	dataset.Runs = append(dataset.Runs, secondRun)
	secondReview := dataset.RunReviews[0]
	secondReview.RunID = "run-test-2"
	secondReview.AdjudicationStatus = "pending"
	dataset.RunReviews = append(dataset.RunReviews, secondReview)
	if err := validateDataset(dataset); err != nil {
		t.Fatalf("validate mixed complete/incomplete eligible runs: %v", err)
	}
	report := evaluateDataset(dataset)
	if report.ExternalReadiness.HumanQuality != "insufficient_evidence" {
		t.Fatalf("human quality status = %q", report.ExternalReadiness.HumanQuality)
	}
	if gate := readinessGateForTest(t, report, "human_quality"); gate.Status != "insufficient_evidence" || gate.ReasonCode != "eligible_source_review_panels_incomplete" {
		t.Fatalf("incomplete eligible corpus passed human-quality gate: %#v", gate)
	}
}

func TestReadinessRightsGateUsesCorpusWhenThereAreNoRuns(t *testing.T) {
	dataset := Dataset{
		Version: evaluationInputVersion,
		Corpus:  []CorpusSource{{ID: "source-rights-only", Status: "ready", RightsStatus: "confirmed", ContentType: "general", LanguageCodes: []string{}}},
		Runs:    []SavedRun{},
	}
	if err := validateDataset(dataset); err != nil {
		t.Fatalf("validate corpus without runs: %v", err)
	}
	gate := readinessGateForTest(t, evaluateDataset(dataset), "corpus_rights")
	if gate.Status != "unverified" || gate.ReasonCode != "operator_attestation_not_independently_verified" {
		t.Fatalf("rights gate ignored recorded attestation: %#v", gate)
	}
}

func TestUnmatchedReviewedCandidateCountsBoundaryFitsAsUnknown(t *testing.T) {
	dataset := completeEvaluationDatasetForTest(t)
	review := &dataset.RunReviews[0].CandidateReviews[0]
	review.Usability = "not_usable"
	review.MatchedOpportunityID = ""
	review.SameMomentConfirmed = false
	review.StartBoundaryFit = "unknown"
	review.EndBoundaryFit = "unknown"
	if err := validateDataset(dataset); err != nil {
		t.Fatalf("validate unmatched candidate review: %v", err)
	}
	run := evaluateDataset(dataset).Runs[0]
	if run.Rubric.BoundaryFitCounts["start_unknown"] != 1 || run.Rubric.BoundaryFitCounts["end_unknown"] != 1 {
		t.Fatalf("unknown boundary judgments were not reported: %#v", run.Rubric.BoundaryFitCounts)
	}
}

func TestModelDurationTargetIsCheckedSeparatelyFromOperatorDuration(t *testing.T) {
	dataset := completeEvaluationDatasetForTest(t)
	dataset.SourceReviews = nil
	dataset.RunReviews = nil
	humanDuration := int64(60_000)
	dataset.Corpus[0].HumanDurationMS = &humanDuration
	var analysis app.ClippingGeminiAnalysis
	if err := json.Unmarshal(dataset.Runs[0].Analysis, &analysis); err != nil {
		t.Fatalf("decode fixture analysis: %v", err)
	}
	analysis.SourceDurationMS = 41 * 60 * 1000
	dataset.Runs[0].Analysis, _ = json.Marshal(analysis)
	if err := validateDataset(dataset); err != nil {
		t.Fatalf("validate model-duration fixture: %v", err)
	}
	run := evaluateDataset(dataset).Runs[0]
	if run.DurationTargetStatus != "within_40_min_target_unverified" || run.ModelDurationTargetStatus != "above_40_min_target" {
		t.Fatalf("operator/model target flags = %q / %q", run.DurationTargetStatus, run.ModelDurationTargetStatus)
	}
}

func TestModelDurationTargetIsFlaggedWhenHumanDurationIsUnknown(t *testing.T) {
	dataset := completeEvaluationDatasetForTest(t)
	dataset.SourceReviews = nil
	dataset.RunReviews = nil
	dataset.Corpus[0].HumanDurationMS = nil
	var analysis app.ClippingGeminiAnalysis
	if err := json.Unmarshal(dataset.Runs[0].Analysis, &analysis); err != nil {
		t.Fatalf("decode fixture analysis: %v", err)
	}
	analysis.SourceDurationMS = 41 * 60 * 1000
	dataset.Runs[0].Analysis, _ = json.Marshal(analysis)
	if err := validateDataset(dataset); err != nil {
		t.Fatalf("validate model-duration fixture: %v", err)
	}
	run := evaluateDataset(dataset).Runs[0]
	if run.DurationTargetStatus != "operator_duration_unknown" || run.ModelDurationTargetStatus != "above_40_min_target" {
		t.Fatalf("unknown operator/model target flags = %q / %q", run.DurationTargetStatus, run.ModelDurationTargetStatus)
	}
}

func TestRecallIsNotComparableWhenRunOptionsExcludeExportableOpportunities(t *testing.T) {
	dataset := completeEvaluationDatasetForTest(t)
	dataset.Runs[0].Options.MinClipMS = 17_000
	if err := validateDataset(dataset); err != nil {
		t.Fatalf("validate narrower-options fixture: %v", err)
	}
	run := evaluateDataset(dataset).Runs[0]
	if run.TopK.Precision == nil || run.TopK.Recall != nil || run.TopK.RecallStatus != "not_comparable" {
		t.Fatalf("option-incompatible opportunities were used for recall: %#v", run.TopK)
	}
}

func TestCompleteEmptyCandidatePanelReportsZeroRecall(t *testing.T) {
	dataset := completeEvaluationDatasetForTest(t)
	dataset.SourceReviews[0].Opportunities = dataset.SourceReviews[0].Opportunities[:1]
	dataset.RunReviews[0].CandidateReviews = []CandidateReview{}
	var analysis app.ClippingGeminiAnalysis
	if err := json.Unmarshal(dataset.Runs[0].Analysis, &analysis); err != nil {
		t.Fatalf("decode fixture analysis: %v", err)
	}
	analysis.Candidates = []app.ClippingGeminiCandidate{}
	analysis.FewerCandidatesReason = "No candidate met the requested editorial criteria."
	dataset.Runs[0].Analysis, _ = json.Marshal(analysis)
	payload, err := json.Marshal(dataset)
	if err != nil {
		t.Fatalf("marshal complete empty panel: %v", err)
	}
	decoded, err := decodeDataset(strings.NewReader(string(payload)))
	if err != nil {
		t.Fatalf("decode complete empty panel: %v", err)
	}
	report := evaluateDataset(decoded)
	run := report.Runs[0]
	if run.HumanReviewStatus != "complete" || run.CandidateReviews.Returned != 0 || run.Rubric.Status != "complete" {
		t.Fatalf("complete empty panel was not preserved: %#v", run)
	}
	if run.TopK.ReturnedOutputs != 0 || run.TopK.Precision != nil || run.TopK.PrecisionStatus != "not_applicable" {
		t.Fatalf("precision was reported for an empty output set: %#v", run.TopK)
	}
	if run.TopK.Recall == nil || *run.TopK.Recall != 0 || run.TopK.RecallStatus != "descriptive_only" {
		t.Fatalf("zero-output recall was withheld or changed: %#v", run.TopK)
	}
	if gate := readinessGateForTest(t, report, "human_quality"); gate.Status != "insufficient_evidence" {
		t.Fatalf("empty candidate set passed the human-quality readiness gate: %#v", gate)
	}

	missingPanel := dataset
	missingPanel.RunReviews = nil
	if err := validateDataset(missingPanel); err != nil {
		t.Fatalf("validate empty candidate run without a panel: %v", err)
	}
	missingRun := evaluateDataset(missingPanel).Runs[0]
	if missingRun.TopK.Recall != nil || missingRun.TopK.RecallStatus != "insufficient_evidence" || missingRun.HumanReviewStatus != "partial" {
		t.Fatalf("missing empty candidate panel was treated as a completed review: %#v", missingRun)
	}
}

func TestReportedPartialUsageDoesNotPassUsageReadiness(t *testing.T) {
	dataset := completeEvaluationDatasetForTest(t)
	dataset.Runs[0].Usage = &UsageRecord{Status: "reported", PromptTokens: int64Pointer(120)}
	if err := validateDataset(dataset); err != nil {
		t.Fatalf("validate partial usage fixture: %v", err)
	}
	report := evaluateDataset(dataset)
	if gate := readinessGateForTest(t, report, "provider_usage"); gate.Status != "insufficient_evidence" || gate.ReasonCode != "provider_usage_missing_or_partial" {
		t.Fatalf("partial reported usage passed readiness: %#v", gate)
	}
	if report.Runs[0].PromptTokens == nil || report.Runs[0].OutputTokens != nil || report.Runs[0].TotalTokens != nil {
		t.Fatalf("partial usage detail was lost or fabricated: %#v", report.Runs[0])
	}
}

func TestIneligibleRightsAttestationDoesNotUnblockCorpusRightsGate(t *testing.T) {
	dataset := completeEvaluationDatasetForTest(t)
	dataset.Corpus[0].Status = "pending"
	if err := validateDataset(dataset); err != nil {
		t.Fatalf("validate pending attestation fixture: %v", err)
	}
	report := evaluateDataset(dataset)
	gate := readinessGateForTest(t, report, "corpus_rights")
	if report.Corpus.OperatorRightsAttestedCount != 1 || report.Corpus.ReadyRightsSourceCount != 0 || gate.Status != "blocked" || gate.ReasonCode != "no_ready_rights_sources" {
		t.Fatalf("ineligible attestation affected rights readiness: corpus=%#v gate=%#v", report.Corpus, gate)
	}
	if report.Corpus.CoverageStatusCounts["ineligible"] != 1 || report.Corpus.CoverageStatusCounts["complete"] != 0 || report.Corpus.OpportunityCount != 0 {
		t.Fatalf("ineligible review entered human coverage totals: %#v", report.Corpus)
	}
}

func TestReportHashesUntrustedIDsAndLanguageTagExtensions(t *testing.T) {
	dataset := completeEvaluationDatasetForTest(t)
	markers := []string{
		"AIzaSyFakeSourceKey123456789012345678901234",
		"sk-test-FAKEKEY1",
		"AIzaSyFakeReviewerKey12345678901234567890",
		"AIzaSyFakeOpportunityKey123456789012345678",
		"AIzaSyFakeCostKey123456789012345678901",
	}
	sourceID, runID, reviewerID, opportunityID, costID := markers[0], markers[1], markers[2], markers[3], markers[4]
	dataset.Corpus[0].ID = sourceID
	dataset.Corpus[0].LanguageCodes = []string{"sk-test-FAKEKEY1"}
	dataset.Runs[0].ID = runID
	dataset.Runs[0].SourceID = sourceID
	dataset.Runs[0].ActualCostEvidenceID = costID
	actualCost := 0.01
	dataset.Runs[0].ActualCostUSD = &actualCost
	var analysis app.ClippingGeminiAnalysis
	if err := json.Unmarshal(dataset.Runs[0].Analysis, &analysis); err != nil {
		t.Fatalf("decode fixture analysis: %v", err)
	}
	analysis.LanguageCodes = []string{"sk-test-FAKEKEY1"}
	dataset.Runs[0].Analysis, _ = json.Marshal(analysis)
	dataset.SourceReviews[0].SourceID = sourceID
	dataset.SourceReviews[0].ReviewerIDs = []string{reviewerID, "reviewer-2"}
	dataset.SourceReviews[0].Opportunities[0].ID = opportunityID
	dataset.SourceReviews[0].Opportunities[0].LanguageCodes = []string{"sk-test-FAKEKEY1"}
	dataset.RunReviews[0].RunID = runID
	dataset.RunReviews[0].ReviewerIDs = []string{reviewerID, "reviewer-2"}
	dataset.RunReviews[0].CandidateReviews[0].MatchedOpportunityID = opportunityID
	if err := validateDataset(dataset); err != nil {
		t.Fatalf("validate key-shaped marker fixture: %v", err)
	}
	report := evaluateDataset(dataset)
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	for _, marker := range markers {
		if strings.Contains(string(encoded), marker) {
			t.Fatalf("report echoed untrusted identifier/language marker %q", marker)
		}
	}
	if report.Runs[0].RunID != opaqueReportID("run", runID) || report.Runs[0].SourceID != opaqueReportID("source", sourceID) {
		t.Fatalf("report IDs were not stable opaque digests: %#v", report.Runs[0])
	}
	if len(report.Runs[0].ModelReportedLanguageCodes) != 1 || !strings.HasPrefix(report.Runs[0].ModelReportedLanguageCodes[0], "other~tag_") {
		t.Fatalf("untrusted language tag was not redacted: %v", report.Runs[0].ModelReportedLanguageCodes)
	}
}

func TestLanguageComparisonUsesCaseFoldedUniqueSets(t *testing.T) {
	if !sameLanguageSet([]string{"en"}, []string{"en", "EN"}) {
		t.Fatal("duplicate case variant did not collapse into one language code")
	}
	if compareLanguages([]string{"en"}, []string{"en", "EN"}) != "matches_operator_metadata" {
		t.Fatal("case-folded unique language sets did not match")
	}
	if compareLanguages([]string{"en", "hi"}, []string{"en", "EN"}) == "matches_operator_metadata" {
		t.Fatal("duplicate case variant masked a missing language code")
	}
	if !sameLanguageSet([]string{"en", "hi"}, []string{"hi", "EN"}) {
		t.Fatal("case-insensitive language set comparison failed")
	}
}

func TestLanguageStrataNormalizeCaseVariants(t *testing.T) {
	sources := []CorpusSource{
		{ID: "source-1", Status: "ready", RightsStatus: "confirmed", ContentType: "general", LanguageCodes: []string{"en"}},
		{ID: "source-2", Status: "ready", RightsStatus: "confirmed", ContentType: "general", LanguageCodes: []string{"EN"}},
	}
	strata := summarizeStrata(sources, map[string]SourceReview{})
	var language *StratumSummary
	for i := range strata {
		if strata[i].Kind == "language" {
			if language != nil {
				t.Fatalf("case variants produced separate strata: %#v", strata)
			}
			language = &strata[i]
		}
	}
	if language == nil || language.Key != "en" || language.SourceCount != 2 || language.EligibleSourceCount != 2 {
		t.Fatalf("normalized language stratum = %#v", language)
	}
}

func TestDurationStrataUseFixedBucketsAndUnknownDenominator(t *testing.T) {
	cases := []struct {
		name     string
		duration *int64
		want     string
	}{
		{name: "unknown", duration: nil, want: "unknown"},
		{name: "under fifteen minutes", duration: int64Pointer(shortDurationBucketMS - 1), want: "under_15m"},
		{name: "fifteen minutes", duration: int64Pointer(shortDurationBucketMS), want: "15m_to_40m"},
		{name: "forty minutes", duration: int64Pointer(maxGeminiTargetDurationMS), want: "15m_to_40m"},
		{name: "over forty minutes", duration: int64Pointer(maxGeminiTargetDurationMS + 1), want: "over_40m"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := durationStratumKey(test.duration); got != test.want {
				t.Fatalf("durationStratumKey() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestRankUsabilityRequiresCompleteAdjudicatedPanels(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*Dataset)
	}{
		{
			name: "one reviewer pending",
			setup: func(dataset *Dataset) {
				dataset.RunReviews[0].ReviewerIDs = []string{"reviewer-1"}
				dataset.RunReviews[0].AdjudicationStatus = "pending"
			},
		},
		{
			name: "missing rank",
			setup: func(dataset *Dataset) {
				dataset.RunReviews[0].AdjudicationStatus = "pending"
				dataset.RunReviews[0].CandidateReviews = dataset.RunReviews[0].CandidateReviews[:1]
			},
		},
		{
			name: "uncertain rank",
			setup: func(dataset *Dataset) {
				dataset.RunReviews[0].AdjudicationStatus = "pending"
				dataset.RunReviews[0].CandidateReviews[1] = CandidateReview{
					Rank: 2, Status: "uncertain", Usability: "uncertain",
					StartBoundaryFit: "unknown", EndBoundaryFit: "unknown",
				}
			},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			dataset := completeEvaluationDatasetForTest(t)
			test.setup(&dataset)
			if err := validateDataset(dataset); err != nil {
				t.Fatalf("validate partial panel: %v", err)
			}
			report := evaluateDataset(dataset)
			if len(report.RankSummary) != 0 {
				t.Fatalf("incomplete panel entered per-rank metrics: %#v", report.RankSummary)
			}
			run := report.Runs[0]
			if run.TopK.Precision != nil || run.TopK.UsableOutputs != 0 || run.Rubric.RatedCandidateCount != 0 || len(run.Rubric.DimensionMeans) != 0 {
				t.Fatalf("incomplete panel entered run-level quality metrics: %#v", run)
			}
		})
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
