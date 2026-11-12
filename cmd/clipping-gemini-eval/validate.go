package main

import (
	"encoding/hex"
	"math"
	"regexp"
	"strings"

	"video-automation/internal/app"
)

var (
	evaluationIDPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	evaluationTokenPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	evaluationLanguageCode = regexp.MustCompile(`^[A-Za-z]{2,8}(-[A-Za-z0-9]{1,8})*$`)
)

func validateDataset(data Dataset) error {
	if data.Version != evaluationInputVersion || data.Corpus == nil || data.Runs == nil ||
		len(data.Corpus) > maxEvaluationSources || len(data.Runs) > maxEvaluationRuns ||
		len(data.SourceReviews) > maxEvaluationReviews || len(data.RunReviews) > maxEvaluationReviews {
		return errInvalidDataset
	}
	sources := make(map[string]CorpusSource, len(data.Corpus))
	for _, source := range data.Corpus {
		if err := validateCorpusSource(source); err != nil {
			return errInvalidDataset
		}
		if _, exists := sources[source.ID]; exists {
			return errInvalidDataset
		}
		sources[source.ID] = source
	}

	runs := make(map[string]SavedRun, len(data.Runs))
	analyses := make(map[string]app.ClippingGeminiAnalysis, len(data.Runs))
	for _, run := range data.Runs {
		if err := validateSavedRun(run); err != nil {
			return errInvalidDataset
		}
		if _, exists := sources[run.SourceID]; !exists {
			return errInvalidDataset
		}
		if _, exists := runs[run.ID]; exists {
			return errInvalidDataset
		}
		analysis, err := app.DecodeClippingGeminiAnalysis(run.Analysis, run.Options)
		if err != nil {
			return errInvalidDataset
		}
		runs[run.ID] = run
		analyses[run.ID] = analysis
	}

	sourceReviews := make(map[string]SourceReview, len(data.SourceReviews))
	totalEvents := 0
	for _, review := range data.SourceReviews {
		source, exists := sources[review.SourceID]
		if !exists || validateSourceReview(review, source) != nil {
			return errInvalidDataset
		}
		if _, exists := sourceReviews[review.SourceID]; exists {
			return errInvalidDataset
		}
		totalEvents += len(review.Opportunities)
		if totalEvents > maxEvaluationEvents {
			return errInvalidDataset
		}
		sourceReviews[review.SourceID] = review
	}

	seenRunReviews := make(map[string]struct{}, len(data.RunReviews))
	for _, review := range data.RunReviews {
		run, exists := runs[review.RunID]
		analysis, analysisExists := analyses[review.RunID]
		if !exists || !analysisExists || validateRunReview(review, analysis, sourceReviews[run.SourceID]) != nil {
			return errInvalidDataset
		}
		if _, exists := seenRunReviews[review.RunID]; exists {
			return errInvalidDataset
		}
		seenRunReviews[review.RunID] = struct{}{}
	}
	return nil
}

func validateCorpusSource(source CorpusSource) error {
	if !validEvaluationID(source.ID) || !oneOf(source.Status, "synthetic", "pending", "ready", "excluded") ||
		!oneOf(source.RightsStatus, "unknown", "confirmed", "denied") ||
		!validContentType(source.ContentType) || !validLanguageCodes(source.LanguageCodes, 8) {
		return errInvalidDataset
	}
	if source.Status == "synthetic" && source.RightsStatus != "unknown" {
		return errInvalidDataset
	}
	if source.HumanDurationMS != nil && (*source.HumanDurationMS < 1 || *source.HumanDurationMS > app.ClippingGeminiMaxSourceDurationMS) {
		return errInvalidDataset
	}
	if source.MediaSHA256 != "" {
		if !isLowerHex(source.MediaSHA256, 32) || !source.MediaSHA256HumanVerified {
			return errInvalidDataset
		}
	} else if source.MediaSHA256HumanVerified {
		return errInvalidDataset
	}
	return nil
}

func validateSavedRun(run SavedRun) error {
	if !validEvaluationID(run.ID) || !validEvaluationID(run.SourceID) || len(run.Analysis) == 0 ||
		len(run.Analysis) > app.ClippingGeminiMaxAnalysisBytes || app.ValidateClippingGeminiOptions(run.Options) != nil {
		return errInvalidDataset
	}
	snapshot := run.ProviderSnapshot
	if snapshot.Provider != "gemini_enterprise_agent_platform" || !validSafeToken(snapshot.Model) ||
		snapshot.APIVersion != "v1" || !validSafeToken(snapshot.PromptVersion) ||
		!isLowerHex(snapshot.PromptSHA256, 32) || !validSafeToken(snapshot.SchemaVersion) ||
		!isLowerHex(snapshot.SchemaSHA256, 32) || snapshot.ProcessingMode != "STATIC" {
		return errInvalidDataset
	}
	if run.Usage != nil {
		usage := run.Usage
		if !oneOf(usage.Status, "reported", "unavailable", "unknown") ||
			!validOptionalCount(usage.PromptTokens) || !validOptionalCount(usage.OutputTokens) || !validOptionalCount(usage.TotalTokens) {
			return errInvalidDataset
		}
		countPresent := usage.PromptTokens != nil || usage.OutputTokens != nil || usage.TotalTokens != nil
		if (usage.Status == "reported") != countPresent {
			return errInvalidDataset
		}
	}
	if run.ActualCostUSD != nil {
		if math.IsNaN(*run.ActualCostUSD) || math.IsInf(*run.ActualCostUSD, 0) || *run.ActualCostUSD < 0 || !validEvaluationID(run.ActualCostEvidenceID) {
			return errInvalidDataset
		}
	} else if run.ActualCostEvidenceID != "" {
		return errInvalidDataset
	}
	return nil
}

func validateSourceReview(review SourceReview, source CorpusSource) error {
	if !validEvaluationID(review.SourceID) || !oneOf(review.CoverageStatus, "complete", "partial", "not_reviewed") ||
		!oneOf(review.AdjudicationStatus, "complete", "pending", "not_started", "not_required") ||
		!validReviewerIDs(review.ReviewerIDs) || len(review.Opportunities) > maxEvaluationEvents {
		return errInvalidDataset
	}
	if review.CoverageStatus == "not_reviewed" {
		if review.ReviewedStartMS != nil || review.ReviewedEndMS != nil || len(review.ReviewerIDs) != 0 ||
			len(review.Opportunities) != 0 || review.AdjudicationStatus != "not_started" {
			return errInvalidDataset
		}
		return nil
	}
	if len(review.ReviewerIDs) == 0 || review.AdjudicationStatus == "not_started" ||
		review.ReviewedStartMS == nil || review.ReviewedEndMS == nil {
		return errInvalidDataset
	}
	if *review.ReviewedStartMS < 0 || *review.ReviewedEndMS <= *review.ReviewedStartMS ||
		*review.ReviewedEndMS > app.ClippingGeminiMaxSourceDurationMS {
		return errInvalidDataset
	}
	if source.HumanDurationMS != nil && *review.ReviewedEndMS > *source.HumanDurationMS {
		return errInvalidDataset
	}
	if review.CoverageStatus == "complete" && (source.HumanDurationMS == nil || *review.ReviewedStartMS != 0 || *review.ReviewedEndMS != *source.HumanDurationMS) {
		return errInvalidDataset
	}
	seen := make(map[string]struct{}, len(review.Opportunities))
	for _, opportunity := range review.Opportunities {
		if err := validateOpportunity(opportunity, source); err != nil || opportunity.StartMS < *review.ReviewedStartMS || opportunity.EndMS > *review.ReviewedEndMS {
			return errInvalidDataset
		}
		if _, exists := seen[opportunity.ID]; exists {
			return errInvalidDataset
		}
		seen[opportunity.ID] = struct{}{}
	}
	return nil
}

func validateOpportunity(opportunity Opportunity, source CorpusSource) error {
	if !validEvaluationID(opportunity.ID) || opportunity.StartMS < 0 || opportunity.EndMS <= opportunity.StartMS ||
		opportunity.EndMS > app.ClippingGeminiMaxSourceDurationMS || !validEnumSet(opportunity.Kinds, "speech", "non_speech_audio", "visual_only") ||
		!validLanguageCodes(opportunity.LanguageCodes, 8) || !oneOf(opportunity.Humor, "present", "absent", "unknown") ||
		!oneOf(opportunity.Emotion, "present", "absent", "unknown") ||
		!oneOf(opportunity.Context, "self_contained", "needs_setup", "needs_prior_context", "unknown") {
		return errInvalidDataset
	}
	if source.HumanDurationMS != nil && opportunity.EndMS > *source.HumanDurationMS {
		return errInvalidDataset
	}
	return nil
}

func validateRunReview(review RunReview, analysis app.ClippingGeminiAnalysis, sourceReview SourceReview) error {
	if !validEvaluationID(review.RunID) || !oneOf(review.AdjudicationStatus, "complete", "pending", "not_started", "not_required") ||
		!validReviewerIDs(review.ReviewerIDs) || len(review.CandidateReviews) > maxEvaluationCandidates {
		return errInvalidDataset
	}
	if len(review.CandidateReviews) > 0 && (len(review.ReviewerIDs) == 0 || review.AdjudicationStatus == "not_started") {
		return errInvalidDataset
	}
	byRank := make(map[int]app.ClippingGeminiCandidate, len(analysis.Candidates))
	for _, candidate := range analysis.Candidates {
		byRank[candidate.Rank] = candidate
	}
	byOpportunity := make(map[string]Opportunity, len(sourceReview.Opportunities))
	for _, opportunity := range sourceReview.Opportunities {
		byOpportunity[opportunity.ID] = opportunity
	}
	seenRanks := make(map[int]struct{}, len(review.CandidateReviews))
	seenOpportunities := make(map[string]struct{}, len(review.CandidateReviews))
	for _, candidateReview := range review.CandidateReviews {
		candidate, exists := byRank[candidateReview.Rank]
		if !exists || validateCandidateReview(candidateReview) != nil {
			return errInvalidDataset
		}
		if _, exists := seenRanks[candidateReview.Rank]; exists {
			return errInvalidDataset
		}
		seenRanks[candidateReview.Rank] = struct{}{}
		if candidateReview.MatchedOpportunityID == "" {
			if candidateReview.SameMomentConfirmed {
				return errInvalidDataset
			}
			continue
		}
		opportunity, exists := byOpportunity[candidateReview.MatchedOpportunityID]
		if !exists || candidateReview.Status != "reviewed" || !candidateReview.SameMomentConfirmed ||
			intervalIoU(candidate.StartMS, candidate.EndMS, opportunity.StartMS, opportunity.EndMS) < 0.5 {
			return errInvalidDataset
		}
		if _, exists := seenOpportunities[opportunity.ID]; exists {
			return errInvalidDataset
		}
		seenOpportunities[opportunity.ID] = struct{}{}
	}
	return nil
}

func validateCandidateReview(review CandidateReview) error {
	if review.Rank < 1 || !oneOf(review.Status, "reviewed", "uncertain", "not_reviewed") ||
		!oneOf(review.Usability, "usable", "not_usable", "uncertain", "not_rated") ||
		!oneOf(review.StartBoundaryFit, "within", "outside", "unknown") ||
		!oneOf(review.EndBoundaryFit, "within", "outside", "unknown") || !validDimensions(review.Dimensions) {
		return errInvalidDataset
	}
	if review.Status == "not_reviewed" && (review.Usability != "not_rated" || review.MatchedOpportunityID != "" ||
		review.SameMomentConfirmed || review.StartBoundaryFit != "unknown" || review.EndBoundaryFit != "unknown" || hasDimensions(review.Dimensions)) {
		return errInvalidDataset
	}
	if review.Status == "reviewed" && review.Usability != "usable" && review.Usability != "not_usable" {
		return errInvalidDataset
	}
	if review.Status == "reviewed" && (!allDimensionsPresent(review.Dimensions) || review.StartBoundaryFit == "unknown" || review.EndBoundaryFit == "unknown") {
		return errInvalidDataset
	}
	if review.Status == "uncertain" && review.Usability != "uncertain" && review.Usability != "not_rated" {
		return errInvalidDataset
	}
	if review.Status != "reviewed" && hasDimensions(review.Dimensions) {
		return errInvalidDataset
	}
	return nil
}

func allDimensionsPresent(dimensions ReviewDimensions) bool {
	return dimensions.Completeness != nil && dimensions.Context != nil && dimensions.Hook != nil && dimensions.Emotion != nil &&
		dimensions.Humor != nil && dimensions.VisualEvidence != nil && dimensions.Standalone != nil
}

func validDimensions(dimensions ReviewDimensions) bool {
	for _, score := range []*int{dimensions.Completeness, dimensions.Context, dimensions.Hook, dimensions.Emotion, dimensions.Humor, dimensions.VisualEvidence, dimensions.Standalone} {
		if score != nil && (*score < 1 || *score > 5) {
			return false
		}
	}
	return true
}

func hasDimensions(dimensions ReviewDimensions) bool {
	return dimensions.Completeness != nil || dimensions.Context != nil || dimensions.Hook != nil || dimensions.Emotion != nil ||
		dimensions.Humor != nil || dimensions.VisualEvidence != nil || dimensions.Standalone != nil
}

// intervalIoU is used only as a plausibility guard after a reviewer has
// explicitly confirmed that two ranges refer to the same moment. It never
// creates or chooses a match itself.
func intervalIoU(startA, endA, startB, endB int64) float64 {
	intersectionStart := max(startA, startB)
	intersectionEnd := min(endA, endB)
	if intersectionEnd <= intersectionStart {
		return 0
	}
	intersection := intersectionEnd - intersectionStart
	union := max(endA, endB) - min(startA, startB)
	if union <= 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

func validReviewerIDs(ids []string) bool {
	if len(ids) > maxEvaluationLabels {
		return false
	}
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if !validEvaluationID(id) {
			return false
		}
		if _, exists := seen[id]; exists {
			return false
		}
		seen[id] = struct{}{}
	}
	return true
}

func validOptionalCount(value *int64) bool {
	return value == nil || *value >= 0
}

func validLanguageCodes(codes []string, maximum int) bool {
	if len(codes) > maximum {
		return false
	}
	seen := make(map[string]struct{}, len(codes))
	for _, code := range codes {
		if len(code) > 35 || !evaluationLanguageCode.MatchString(code) {
			return false
		}
		key := strings.ToLower(code)
		if _, exists := seen[key]; exists {
			return false
		}
		seen[key] = struct{}{}
	}
	return true
}

func validContentType(contentType string) bool {
	return oneOf(contentType, "general", "podcast", "comedy", "gaming", "movie")
}

func validEnumSet(values []string, allowed ...string) bool {
	if len(values) == 0 || len(values) > len(allowed) {
		return false
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !oneOf(value, allowed...) {
			return false
		}
		if _, exists := seen[value]; exists {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func validEvaluationID(value string) bool {
	return evaluationIDPattern.MatchString(value)
}

func validSafeToken(value string) bool {
	return evaluationTokenPattern.MatchString(value)
}

func isLowerHex(value string, byteLength int) bool {
	if len(value) != byteLength*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
