package main

import "testing"

func TestReviewedCandidateRequiresCompleteRubricAndContextualBoundaryFits(t *testing.T) {
	valid := CandidateReview{
		Rank: 1, Status: "reviewed", Usability: "usable",
		MatchedOpportunityID: "opportunity-1", SameMomentConfirmed: true,
		StartBoundaryFit: "within", EndBoundaryFit: "outside",
		Dimensions: ReviewDimensions{
			Completeness: intPointer(4), Context: intPointer(5), Hook: intPointer(3), Emotion: intPointer(4),
			Humor: intPointer(2), VisualEvidence: intPointer(4), Standalone: intPointer(5),
		},
	}
	if err := validateCandidateReview(valid); err != nil {
		t.Fatalf("complete candidate review rejected: %v", err)
	}

	missingDimension := valid
	missingDimension.Dimensions.VisualEvidence = nil
	if err := validateCandidateReview(missingDimension); err == nil {
		t.Fatal("reviewed candidate with missing visual-evidence rating was accepted")
	}

	unknownBoundary := valid
	unknownBoundary.EndBoundaryFit = "unknown"
	if err := validateCandidateReview(unknownBoundary); err == nil {
		t.Fatal("matched candidate with unknown end boundary was accepted")
	}

	unmatched := valid
	unmatched.MatchedOpportunityID = ""
	unmatched.SameMomentConfirmed = false
	unmatched.StartBoundaryFit = "unknown"
	unmatched.EndBoundaryFit = "unknown"
	if err := validateCandidateReview(unmatched); err != nil {
		t.Fatalf("unmatched candidate with unknown boundaries rejected: %v", err)
	}
	unmatched.StartBoundaryFit = "within"
	if err := validateCandidateReview(unmatched); err == nil {
		t.Fatal("unmatched candidate with an invented boundary fit was accepted")
	}
}

func TestUncertainAndUnreviewedCandidatesCannotCarryRubricScores(t *testing.T) {
	scored := ReviewDimensions{Hook: intPointer(4)}
	cases := []CandidateReview{
		{Rank: 1, Status: "uncertain", Usability: "uncertain", StartBoundaryFit: "unknown", EndBoundaryFit: "unknown", Dimensions: scored},
		{Rank: 1, Status: "not_reviewed", Usability: "not_rated", StartBoundaryFit: "unknown", EndBoundaryFit: "unknown", Dimensions: scored},
	}
	for _, candidate := range cases {
		if err := validateCandidateReview(candidate); err == nil {
			t.Fatalf("candidate status %q accepted ratings without a completed review", candidate.Status)
		}
	}
	reviewedButUncertain := CandidateReview{
		Rank: 1, Status: "reviewed", Usability: "uncertain", StartBoundaryFit: "within", EndBoundaryFit: "within",
		Dimensions: ReviewDimensions{
			Completeness: intPointer(3), Context: intPointer(3), Hook: intPointer(3), Emotion: intPointer(3),
			Humor: intPointer(3), VisualEvidence: intPointer(3), Standalone: intPointer(3),
		},
	}
	if err := validateCandidateReview(reviewedButUncertain); err == nil {
		t.Fatal("candidate with uncertain usability was allowed as a completed binary review")
	}
}

func TestSyntheticCorpusCannotClaimRightsAttestation(t *testing.T) {
	source := CorpusSource{
		ID: "fixture", Status: "synthetic", RightsStatus: "unknown", ContentType: "comedy", LanguageCodes: []string{"en"},
	}
	if err := validateCorpusSource(source); err != nil {
		t.Fatalf("clearly labeled synthetic source rejected: %v", err)
	}
	source.RightsStatus = "confirmed"
	if err := validateCorpusSource(source); err == nil {
		t.Fatal("synthetic fixture was allowed to claim operator-confirmed source rights")
	}
}

func TestPartialSourceReviewCannotLabelOutsideItsReviewedRange(t *testing.T) {
	duration, start, reviewedEnd := int64(120_000), int64(0), int64(60_000)
	source := CorpusSource{ID: "source-1", Status: "pending", RightsStatus: "unknown", ContentType: "general", HumanDurationMS: &duration}
	review := SourceReview{
		SourceID: source.ID, CoverageStatus: "partial", ReviewedStartMS: &start, ReviewedEndMS: &reviewedEnd,
		ReviewerIDs: []string{"reviewer-1"}, AdjudicationStatus: "pending",
		Opportunities: []Opportunity{{
			ID: "outside-range", StartMS: 101_000, EndMS: 117_000, Kinds: []string{"visual_only"}, LanguageCodes: []string{},
			Exportable: true, Humor: "unknown", Emotion: "unknown", Context: "unknown",
		}},
	}
	if err := validateSourceReview(review, source); err == nil {
		t.Fatal("partial review accepted an opportunity outside its reviewed interval")
	}
}

func TestExportableOpportunityMustFitGlobalClipDurationBounds(t *testing.T) {
	source := CorpusSource{ID: "source-1", Status: "ready", RightsStatus: "confirmed", ContentType: "general"}
	base := Opportunity{
		ID: "opportunity-1", StartMS: 10_000, EndMS: 25_000, Kinds: []string{"speech"}, LanguageCodes: []string{"en"},
		Exportable: true, Humor: "unknown", Emotion: "unknown", Context: "unknown",
	}
	cases := []struct {
		name      string
		duration  int64
		wantValid bool
	}{
		{name: "minimum inclusive", duration: minExportableOpportunityMS, wantValid: true},
		{name: "below minimum", duration: minExportableOpportunityMS - 1},
		{name: "maximum inclusive", duration: maxExportableOpportunityMS, wantValid: true},
		{name: "above maximum", duration: maxExportableOpportunityMS + 1},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			opportunity := base
			opportunity.EndMS = opportunity.StartMS + test.duration
			err := validateOpportunity(opportunity, source)
			if test.wantValid && err != nil {
				t.Fatalf("valid exportable interval rejected: %v", err)
			}
			if !test.wantValid && err == nil {
				t.Fatal("exportable opportunity outside global clip bounds was accepted")
			}
		})
	}
	base.Exportable = false
	base.EndMS = base.StartMS + maxExportableOpportunityMS + 1
	if err := validateOpportunity(base, source); err != nil {
		t.Fatalf("non-exportable observation outside clip bounds was rejected: %v", err)
	}
}

func TestCompleteAdjudicationRequiresTwoReviewersAndAllCandidates(t *testing.T) {
	cases := map[string]func(*Dataset){
		"source panel has one reviewer": func(data *Dataset) {
			data.SourceReviews[0].ReviewerIDs = []string{"reviewer-1"}
		},
		"candidate panel has one reviewer": func(data *Dataset) {
			data.RunReviews[0].ReviewerIDs = []string{"reviewer-1"}
		},
		"candidate review omits a returned rank": func(data *Dataset) {
			data.RunReviews[0].CandidateReviews = data.RunReviews[0].CandidateReviews[:1]
		},
		"complete review leaves a candidate unreviewed": func(data *Dataset) {
			data.RunReviews[0].CandidateReviews[0] = CandidateReview{
				Rank: 1, Status: "not_reviewed", Usability: "not_rated",
				StartBoundaryFit: "unknown", EndBoundaryFit: "unknown",
			}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			data := completeEvaluationDatasetForTest(t)
			mutate(&data)
			if err := validateDataset(data); err == nil {
				t.Fatal("incomplete review panel was accepted as fully adjudicated")
			}
		})
	}
}

func intPointer(value int) *int { return &value }
