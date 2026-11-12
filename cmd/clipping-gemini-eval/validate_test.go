package main

import "testing"

func TestReviewedCandidateRequiresCompleteRubricAndKnownBoundaries(t *testing.T) {
	valid := CandidateReview{
		Rank: 1, Status: "reviewed", Usability: "usable",
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
		t.Fatal("reviewed candidate with unknown end boundary was accepted")
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

func intPointer(value int) *int { return &value }
