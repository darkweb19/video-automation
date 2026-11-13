package main

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"sort"
	"strings"

	"video-automation/internal/app"
)

func evaluateDataset(data Dataset) EvaluationReport {
	sources := make(map[string]CorpusSource, len(data.Corpus))
	for _, source := range data.Corpus {
		sources[source.ID] = source
	}
	sourceReviews := make(map[string]SourceReview, len(data.SourceReviews))
	for _, review := range data.SourceReviews {
		sourceReviews[review.SourceID] = review
	}
	runReviews := make(map[string]RunReview, len(data.RunReviews))
	for _, review := range data.RunReviews {
		runReviews[review.RunID] = review
	}

	report := EvaluationReport{
		Version:     evaluationReportVersion,
		LocalStatus: "offline_validation_complete",
		Runs:        make([]RunSummary, 0, len(data.Runs)),
		RankSummary: make([]RankSummary, 0),
		Corpus: CorpusSummary{
			SourceStatusCounts:   emptyCounts("synthetic", "pending", "ready", "excluded"),
			RightsStatusCounts:   emptyCounts("unknown", "confirmed", "denied"),
			CoverageStatusCounts: emptyCounts("not_reviewed", "partial", "complete"),
		},
		ExternalReadiness: ExternalReadiness{
			ProviderAvailability: "unverified_live",
			LiveAuthentication:   "unverified",
			SourceRights:         "operator_attestation_not_independently_verified",
			HumanQuality:         "insufficient_evidence",
			ActualCost:           "unknown",
			LongVideoSupport:     "preview_and_40_minute_gate_unverified",
			DeploymentBackup:     "not_verified_by_offline_tool",
		},
	}
	for _, source := range data.Corpus {
		report.Corpus.SourceStatusCounts[source.Status]++
		report.Corpus.RightsStatusCounts[source.RightsStatus]++
		if source.RightsStatus == "confirmed" {
			report.Corpus.OperatorRightsAttestedCount++
		}
		if source.HumanDurationMS != nil {
			report.Corpus.HumanDurationCount++
		}
		review, hasReview := sourceReviews[source.ID]
		coverageStatus := "not_reviewed"
		if hasReview {
			coverageStatus = review.CoverageStatus
		}
		report.Corpus.CoverageStatusCounts[coverageStatus]++
		if hasReview && sourceReviewComplete(review, source) {
			report.Corpus.CompleteSourceReviewCount++
			if source.Status == "ready" && source.RightsStatus == "confirmed" && source.HumanDurationMS != nil {
				report.Corpus.EligibleSourceCount++
			}
		}
	}
	report.Corpus.SourceCount = len(data.Corpus)
	report.Corpus.EvidenceStatus = corpusEvidenceStatus(report.Corpus.EligibleSourceCount)

	completeHumanReviews := 0
	partialHumanReviews := 0
	completeCostRecords := 0
	rankSummaries := make(map[int]*RankSummary)
	for _, run := range data.Runs {
		analysis, _ := app.DecodeClippingGeminiAnalysis(run.Analysis, run.Options)
		source := sources[run.SourceID]
		sourceReview, hasSourceReview := sourceReviews[run.SourceID]
		runReview, hasRunReview := runReviews[run.ID]
		addRunRankEvidence(rankSummaries, analysis, sourceReview, hasSourceReview, runReview, hasRunReview)
		summary := summarizeRun(run, analysis, source, sourceReview, hasSourceReview, runReview, hasRunReview)
		report.Runs = append(report.Runs, summary)
		if run.ActualCostUSD != nil {
			completeCostRecords++
		}
		if summary.HumanReviewStatus == "complete" {
			completeHumanReviews++
		} else if summary.HumanReviewStatus == "partial" {
			partialHumanReviews++
		}
	}
	for _, rank := range finalizeRankEvidence(rankSummaries) {
		report.RankSummary = append(report.RankSummary, rank)
	}
	if completeHumanReviews == len(data.Runs) && len(data.Runs) > 0 && hasAnyRubricRatings(report.Runs) {
		report.ExternalReadiness.HumanQuality = "human_review_metrics_are_descriptive_only"
	} else if completeHumanReviews > 0 || partialHumanReviews > 0 {
		report.ExternalReadiness.HumanQuality = "partial_review_metrics_are_descriptive_only"
	}
	if completeCostRecords == len(data.Runs) && len(data.Runs) > 0 {
		report.ExternalReadiness.ActualCost = "operator_recorded_not_independently_verified"
	} else if completeCostRecords > 0 {
		report.ExternalReadiness.ActualCost = "partial_operator_records_other_runs_unknown"
	}
	report.Strata = summarizeStrata(data.Corpus, sourceReviews)
	report.ReadinessGates = summarizeReadinessGates(data, report)
	return report
}

func summarizeReadinessGates(data Dataset, report EvaluationReport) []ReadinessGate {
	runs := len(data.Runs)
	if runs == 0 {
		return []ReadinessGate{
			{Name: "provider_availability", Status: "unverified", ReasonCode: "live_inference_not_run"},
			{Name: "live_authentication", Status: "unverified", ReasonCode: "live_authentication_not_run"},
			{Name: "corpus_rights", Status: "blocked", ReasonCode: "no_operator_rights_attestations"},
			{Name: "human_quality", Status: "not_run", ReasonCode: "no_saved_runs"},
			{Name: "provider_usage", Status: "not_run", ReasonCode: "no_saved_runs"},
			{Name: "actual_cost", Status: "not_run", ReasonCode: "no_saved_runs"},
			{Name: "long_video_support", Status: "unverified", ReasonCode: "preview_40_minute_gate_not_run"},
			{Name: "deployment_backup", Status: "not_run", ReasonCode: "offline_tool_does_not_verify_operations"},
		}
	}

	gate := []ReadinessGate{
		{Name: "provider_availability", Status: "unverified", ReasonCode: "live_inference_not_run"},
		{Name: "live_authentication", Status: "unverified", ReasonCode: "live_authentication_not_run"},
		{Name: "long_video_support", Status: "unverified", ReasonCode: "preview_40_minute_gate_not_run"},
		{Name: "deployment_backup", Status: "not_run", ReasonCode: "offline_tool_does_not_verify_operations"},
	}
	if report.Corpus.OperatorRightsAttestedCount == 0 {
		gate = append(gate, ReadinessGate{Name: "corpus_rights", Status: "blocked", ReasonCode: "no_operator_rights_attestations"})
	} else {
		gate = append(gate, ReadinessGate{Name: "corpus_rights", Status: "unverified", ReasonCode: "operator_attestation_not_independently_verified"})
	}

	completeHumanReviews := 0
	completeUsageRecords := 0
	completeCostRecords := 0
	for _, run := range report.Runs {
		if run.HumanReviewStatus == "complete" {
			completeHumanReviews++
		}
		if run.UsageStatus == "reported" {
			completeUsageRecords++
		}
		if run.CostStatus == "operator_recorded_not_independently_verified" {
			completeCostRecords++
		}
	}
	if completeHumanReviews == runs && hasAnyRubricRatings(report.Runs) {
		gate = append(gate, ReadinessGate{Name: "human_quality", Status: "descriptive_only", ReasonCode: "human_labels_are_not_a_release_pass"})
	} else {
		gate = append(gate, ReadinessGate{Name: "human_quality", Status: "insufficient_evidence", ReasonCode: "complete_adjudicated_reviews_missing"})
	}
	if completeUsageRecords == runs {
		gate = append(gate, ReadinessGate{Name: "provider_usage", Status: "unverified", ReasonCode: "provider_reported_counts_not_independently_verified"})
	} else {
		gate = append(gate, ReadinessGate{Name: "provider_usage", Status: "insufficient_evidence", ReasonCode: "provider_usage_missing_or_partial"})
	}
	if completeCostRecords == runs {
		gate = append(gate, ReadinessGate{Name: "actual_cost", Status: "unverified", ReasonCode: "operator_cost_records_not_invoice_verified"})
	} else {
		gate = append(gate, ReadinessGate{Name: "actual_cost", Status: "insufficient_evidence", ReasonCode: "actual_cost_missing_or_partial"})
	}
	return gate
}

func addRunRankEvidence(summaries map[int]*RankSummary, analysis app.ClippingGeminiAnalysis, sourceReview SourceReview, hasSourceReview bool, runReview RunReview, hasRunReview bool) {
	opportunities := make(map[string]Opportunity)
	if hasSourceReview {
		for _, opportunity := range sourceReview.Opportunities {
			opportunities[opportunity.ID] = opportunity
		}
	}
	reviews := make(map[int]CandidateReview)
	if hasRunReview {
		for _, candidateReview := range runReview.CandidateReviews {
			reviews[candidateReview.Rank] = candidateReview
		}
	}
	for _, candidate := range analysis.Candidates {
		summary := summaries[candidate.Rank]
		if summary == nil {
			summary = &RankSummary{Rank: candidate.Rank, EvidenceStatus: "insufficient_evidence"}
			summaries[candidate.Rank] = summary
		}
		summary.ReturnedOutputs++
		candidateReview, exists := reviews[candidate.Rank]
		if !exists || candidateReview.Status != "reviewed" {
			continue
		}
		summary.ReviewedOutputs++
		if candidateReview.Usability == "usable" {
			summary.UsableOutputs++
			if opportunity, ok := opportunities[candidateReview.MatchedOpportunityID]; ok && opportunity.Exportable && candidateReview.SameMomentConfirmed {
				summary.MatchedUsable++
			}
		}
	}
}

func finalizeRankEvidence(summaries map[int]*RankSummary) []RankSummary {
	ranks := make([]int, 0, len(summaries))
	for rank := range summaries {
		ranks = append(ranks, rank)
	}
	sort.Ints(ranks)
	result := make([]RankSummary, 0, len(ranks))
	for _, rank := range ranks {
		summary := *summaries[rank]
		if summary.ReturnedOutputs > 0 && summary.ReviewedOutputs == summary.ReturnedOutputs {
			rate := float64(summary.UsableOutputs) / float64(summary.ReturnedOutputs)
			summary.UsabilityRate = &rate
			summary.EvidenceStatus = "descriptive_only"
		}
		result = append(result, summary)
	}
	return result
}

// summarizeRun emits only safe identifiers, counts, digests, enums, and
// numeric usage/cost evidence. Model text and provider configuration values
// never appear in the report.
func summarizeRun(run SavedRun, analysis app.ClippingGeminiAnalysis, source CorpusSource, sourceReview SourceReview, hasSourceReview bool, runReview RunReview, hasRunReview bool) RunSummary {
	digest := sha256.Sum256(run.Analysis)
	summary := RunSummary{
		RunID:                       run.ID,
		SourceID:                    run.SourceID,
		AnalysisPayloadSHA256:       hex.EncodeToString(digest[:]),
		AnalysisStatus:              "valid",
		ModelReportedDurationMS:     analysis.SourceDurationMS,
		OperatorHumanDurationMS:     cloneInt64(source.HumanDurationMS),
		DurationComparisonStatus:    compareDurations(source.HumanDurationMS, analysis.SourceDurationMS),
		DurationTargetStatus:        geminiDurationTargetStatus(source.HumanDurationMS),
		DeclaredContentType:         source.ContentType,
		RequestedContentType:        run.Options.ContentType,
		ContentTypeComparisonStatus: compareContentTypes(source.ContentType, run.Options.ContentType),
		ModelReportedLanguageCodes:  append([]string{}, analysis.LanguageCodes...),
		LanguageComparisonStatus:    compareLanguages(source.LanguageCodes, analysis.LanguageCodes),
		CandidateCount:              len(analysis.Candidates),
		CandidateReviews:            summarizeCandidateReviewCounts(analysis, runReview, hasRunReview),
		RanksAsReturned:             make([]int, 0, len(analysis.Candidates)),
		UsageStatus:                 "unknown",
		CostStatus:                  "unknown",
		HumanReviewStatus:           "not_reviewed",
		LateEventStatus:             "unknown",
		TopK: TopKSummary{
			K:               topK,
			PrecisionStatus: "insufficient_evidence",
			RecallStatus:    "insufficient_evidence",
		},
		Rubric: RubricSummary{Status: "insufficient_evidence", BoundaryFitCounts: map[string]int{
			"start_within": 0, "start_outside": 0, "start_unknown": 0,
			"end_within": 0, "end_outside": 0, "end_unknown": 0,
		}},
	}
	for _, candidate := range analysis.Candidates {
		// Keep the model's returned order and rank intact. This CLI does not
		// sort, score, repair, or choose candidates.
		summary.RanksAsReturned = append(summary.RanksAsReturned, candidate.Rank)
	}
	if run.Usage != nil {
		summary.UsageStatus = run.Usage.Status
		summary.PromptTokens = cloneInt64(run.Usage.PromptTokens)
		summary.OutputTokens = cloneInt64(run.Usage.OutputTokens)
		summary.TotalTokens = cloneInt64(run.Usage.TotalTokens)
	}
	if run.ActualCostUSD != nil {
		summary.CostStatus = "operator_recorded_not_independently_verified"
		actual := *run.ActualCostUSD
		summary.ActualCostUSD = &actual
	}

	if hasSourceReview {
		summary.ReviewedOpportunityCount = len(sourceReview.Opportunities)
		for _, opportunity := range sourceReview.Opportunities {
			if opportunity.Exportable {
				summary.UsableOpportunityCount++
			}
		}
		if sourceReview.CoverageStatus != "not_reviewed" {
			summary.HumanReviewStatus = "partial"
		}
		if sourceReviewComplete(sourceReview, source) {
			summary.HumanReviewStatus = "complete"
			summary.LateEventStatus = "source_coverage_complete_candidate_review_missing"
			lateThreshold := (*source.HumanDurationMS * 4) / 5
			for _, opportunity := range sourceReview.Opportunities {
				if opportunity.Exportable && opportunity.StartMS >= lateThreshold {
					summary.LateOpportunityCount++
				}
			}
		}
	}

	summarizeRunReview(&summary, analysis, sourceReview, hasSourceReview, runReview, hasRunReview)
	if hasRunReview && len(runReview.CandidateReviews) > 0 {
		summary.Rubric.Status = "partial"
		if runCandidateReviewsComplete(analysis, runReview, true) {
			summary.Rubric.Status = "complete"
		}
	}
	candidateReviewComplete := runCandidateReviewsComplete(analysis, runReview, hasRunReview)
	sourceReviewIsComplete := hasSourceReview && sourceReviewComplete(sourceReview, source)
	if summary.HumanReviewStatus == "complete" && !candidateReviewComplete {
		summary.HumanReviewStatus = "partial"
	}
	if candidateReviewComplete && sourceReviewIsComplete {
		summary.HumanReviewStatus = "complete"
		summary.LateEventStatus = "review_complete"
		summary.LateMatchedOpportunityCount = countLateMatchedUsable(sourceReview, runReview, *source.HumanDurationMS)
	} else if !sourceReviewIsComplete && hasRunReview && len(runReview.CandidateReviews) > 0 && summary.HumanReviewStatus == "not_reviewed" {
		summary.HumanReviewStatus = "partial"
	}
	return summary
}

func summarizeCandidateReviewCounts(analysis app.ClippingGeminiAnalysis, review RunReview, hasReview bool) CandidateReviewCounts {
	counts := CandidateReviewCounts{Returned: len(analysis.Candidates)}
	reviews := make(map[int]CandidateReview, len(review.CandidateReviews))
	if hasReview {
		for _, candidateReview := range review.CandidateReviews {
			reviews[candidateReview.Rank] = candidateReview
		}
	}
	for _, candidate := range analysis.Candidates {
		candidateReview, exists := reviews[candidate.Rank]
		if !exists {
			counts.Missing++
			continue
		}
		switch candidateReview.Status {
		case "reviewed":
			counts.Reviewed++
		case "uncertain":
			counts.Uncertain++
		case "not_reviewed":
			counts.NotReviewed++
		}
	}
	return counts
}

func emptyCounts(keys ...string) map[string]int {
	counts := make(map[string]int, len(keys))
	for _, key := range keys {
		counts[key] = 0
	}
	return counts
}

func summarizeRunReview(summary *RunSummary, analysis app.ClippingGeminiAnalysis, sourceReview SourceReview, hasSourceReview bool, review RunReview, hasRunReview bool) {
	opportunityByID := make(map[string]Opportunity)
	if hasSourceReview {
		for _, opportunity := range sourceReview.Opportunities {
			opportunityByID[opportunity.ID] = opportunity
		}
	}
	matchedUsableIDs := make(map[string]struct{})
	usableOutputs := 0
	matchedUsableCount := 0
	for _, candidateReview := range review.CandidateReviews {
		if !hasRunReview {
			break
		}
		if candidateReview.Rank <= topK && candidateReview.Status == "reviewed" {
			summary.TopK.ReviewedOutputs++
			if candidateReview.Usability == "usable" {
				usableOutputs++
			}
			if candidateReview.Usability == "usable" && candidateReview.MatchedOpportunityID != "" {
				if opportunity, ok := opportunityByID[candidateReview.MatchedOpportunityID]; ok && opportunity.Exportable {
					matchedUsableCount++
					matchedUsableIDs[candidateReview.MatchedOpportunityID] = struct{}{}
				}
			}
		}
		if candidateReview.Status == "reviewed" {
			summarizeDimensions(summary, candidateReview.Dimensions)
			for _, fit := range []struct{ side, value string }{
				{"start", candidateReview.StartBoundaryFit}, {"end", candidateReview.EndBoundaryFit},
			} {
				key := fit.side + "_" + fit.value
				summary.Rubric.BoundaryFitCounts[key]++
			}
		}
	}
	finishDimensionMeans(summary)
	summary.TopK.ReturnedOutputs = min(len(analysis.Candidates), topK)
	summary.TopK.UsableOutputs = usableOutputs
	summary.TopK.MatchedUsable = matchedUsableCount
	if hasRunReview && runCandidateReviewsComplete(analysis, review, true) && summary.TopK.ReturnedOutputs > 0 && review.AdjudicationStatus == "complete" {
		value := float64(usableOutputs) / float64(summary.TopK.ReturnedOutputs)
		summary.TopK.Precision = &value
		summary.TopK.PrecisionStatus = "descriptive_only"
	}
	if hasSourceReview && sourceReviewComplete(sourceReview, CorpusSource{HumanDurationMS: sourceReview.ReviewedEndMS}) && runCandidateReviewsComplete(analysis, review, hasRunReview) {
		usableOpportunities := 0
		for _, opportunity := range sourceReview.Opportunities {
			if opportunity.Exportable {
				usableOpportunities++
			}
		}
		if usableOpportunities > 0 {
			value := float64(len(matchedUsableIDs)) / float64(usableOpportunities)
			summary.TopK.Recall = &value
			summary.TopK.RecallStatus = "descriptive_only"
		}
	}
}

func compareDurations(operatorDuration *int64, modelDuration int64) string {
	if operatorDuration == nil {
		return "operator_duration_unknown"
	}
	difference := *operatorDuration - modelDuration
	if difference < 0 {
		difference = -difference
	}
	if difference <= durationMatchToleranceMS {
		return "within_1s"
	}
	return "differs_over_1s"
}

func geminiDurationTargetStatus(operatorDuration *int64) string {
	if operatorDuration == nil {
		return "operator_duration_unknown"
	}
	if *operatorDuration > maxGeminiTargetDurationMS {
		return "above_40_min_target"
	}
	return "within_40_min_target_unverified"
}

func compareContentTypes(declared, requested string) string {
	if declared == requested {
		return "matches_declared_content_type"
	}
	return "differs_from_declared_content_type"
}

func compareLanguages(operatorLanguages, modelLanguages []string) string {
	if len(operatorLanguages) == 0 {
		return "operator_language_metadata_missing"
	}
	if len(modelLanguages) == 0 {
		return "model_reported_language_unknown"
	}
	if sameLanguageSet(operatorLanguages, modelLanguages) {
		return "matches_operator_metadata"
	}
	return "differs_from_operator_metadata"
}

func sameLanguageSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	values := make(map[string]struct{}, len(left))
	for _, value := range left {
		values[strings.ToLower(value)] = struct{}{}
	}
	for _, value := range right {
		if _, exists := values[strings.ToLower(value)]; !exists {
			return false
		}
	}
	return true
}

func summarizeDimensions(summary *RunSummary, dimensions ReviewDimensions) {
	values := map[string]*int{
		"completeness": dimensions.Completeness, "context": dimensions.Context,
		"hook": dimensions.Hook, "emotion": dimensions.Emotion, "humor": dimensions.Humor,
		"visual_evidence": dimensions.VisualEvidence, "standalone": dimensions.Standalone,
	}
	if summary.Rubric.DimensionMeans == nil {
		summary.Rubric.DimensionMeans = make(map[string]float64)
		summary.Rubric.DimensionRatingCounts = make(map[string]int)
	}
	anyRated := false
	for name, score := range values {
		if score == nil {
			continue
		}
		anyRated = true
		// Store scaled sums temporarily in the map, then replace them with means
		// after all candidate reviews have been read.
		summary.Rubric.DimensionMeans[name] += float64(*score)
		summary.Rubric.DimensionRatingCounts[name]++
	}
	if anyRated {
		summary.Rubric.RatedCandidateCount++
	}
}

func finishDimensionMeans(summary *RunSummary) {
	if len(summary.Rubric.DimensionRatingCounts) == 0 {
		return
	}
	for name, count := range summary.Rubric.DimensionRatingCounts {
		if count == 0 {
			delete(summary.Rubric.DimensionMeans, name)
			continue
		}
		mean := math.Round(summary.Rubric.DimensionMeans[name]/float64(count)*100) / 100
		summary.Rubric.DimensionMeans[name] = mean
	}
}

func runCandidateReviewsComplete(analysis app.ClippingGeminiAnalysis, review RunReview, hasReview bool) bool {
	if len(analysis.Candidates) == 0 {
		return true
	}
	if !hasReview || review.AdjudicationStatus != "complete" || len(review.ReviewerIDs) < minimumReviewers || len(review.CandidateReviews) != len(analysis.Candidates) {
		return false
	}
	for _, candidateReview := range review.CandidateReviews {
		if candidateReview.Status != "reviewed" {
			return false
		}
	}
	return true
}

func countLateMatchedUsable(sourceReview SourceReview, runReview RunReview, durationMS int64) int {
	threshold := (durationMS * 4) / 5
	opportunities := make(map[string]Opportunity, len(sourceReview.Opportunities))
	for _, opportunity := range sourceReview.Opportunities {
		if opportunity.Exportable && opportunity.StartMS >= threshold {
			opportunities[opportunity.ID] = opportunity
		}
	}
	if len(opportunities) == 0 {
		return 0
	}
	seen := make(map[string]struct{})
	for _, candidateReview := range runReview.CandidateReviews {
		if candidateReview.Status == "reviewed" && candidateReview.Usability == "usable" && candidateReview.SameMomentConfirmed {
			if _, ok := opportunities[candidateReview.MatchedOpportunityID]; ok {
				seen[candidateReview.MatchedOpportunityID] = struct{}{}
			}
		}
	}
	return len(seen)
}

func summarizeStrata(sources []CorpusSource, reviews map[string]SourceReview) []StratumSummary {
	groups := make(map[string]*StratumSummary)
	for _, source := range sources {
		contentKey := "content_type\x00" + source.ContentType
		addSourceToStratum(groups, contentKey, "content_type", source.ContentType, source, reviews[source.ID])
		languages := source.LanguageCodes
		if len(languages) == 0 {
			languages = []string{"unknown"}
		}
		for _, language := range languages {
			key := "language\x00" + language
			addSourceToStratum(groups, key, "language", language, source, reviews[source.ID])
		}
	}
	strata := make([]StratumSummary, 0, len(groups))
	for _, stratum := range groups {
		sort.Strings(stratum.SourceIDs)
		stratum.SourceCount = len(stratum.SourceIDs)
		if stratum.ReviewCompleteCount == 0 {
			stratum.EvidenceStatus = "insufficient_evidence"
		} else {
			stratum.EvidenceStatus = "descriptive_only"
		}
		strata = append(strata, *stratum)
	}
	sortStrata(strata)
	return strata
}

func addSourceToStratum(groups map[string]*StratumSummary, groupID, kind, key string, source CorpusSource, review SourceReview) {
	stratum := groups[groupID]
	if stratum == nil {
		stratum = &StratumSummary{Kind: kind, Key: key, SourceIDs: make([]string, 0)}
		groups[groupID] = stratum
	}
	stratum.SourceIDs = append(stratum.SourceIDs, source.ID)
	if source.RightsStatus == "confirmed" {
		stratum.OperatorRightsAttestedCount++
	}
	if review.SourceID == source.ID {
		stratum.OpportunityCount += len(review.Opportunities)
		if sourceReviewComplete(review, source) {
			stratum.ReviewCompleteCount++
		} else if review.CoverageStatus == "partial" || review.CoverageStatus == "complete" {
			stratum.PartialReviewCount++
		}
	}
}

func sourceReviewComplete(review SourceReview, source CorpusSource) bool {
	return review.CoverageStatus == "complete" && review.AdjudicationStatus == "complete" && len(review.ReviewerIDs) >= minimumReviewers &&
		source.HumanDurationMS != nil && review.ReviewedStartMS != nil && review.ReviewedEndMS != nil &&
		*review.ReviewedStartMS == 0 && *review.ReviewedEndMS == *source.HumanDurationMS
}

func corpusEvidenceStatus(eligibleSources int) string {
	if eligibleSources == 0 {
		return "insufficient_evidence"
	}
	return "descriptive_only"
}

func hasAnyRubricRatings(runs []RunSummary) bool {
	for _, run := range runs {
		if run.Rubric.RatedCandidateCount > 0 {
			return true
		}
	}
	return false
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func sortStrata(strata []StratumSummary) {
	sort.Slice(strata, func(i, j int) bool {
		if strata[i].Kind != strata[j].Kind {
			return strata[i].Kind < strata[j].Kind
		}
		return strata[i].Key < strata[j].Key
	})
}
