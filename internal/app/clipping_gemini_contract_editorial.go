package app

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

var clippingGeminiLanguageCodePattern = regexp.MustCompile(`^[A-Za-z]{2,8}(-[A-Za-z0-9]{1,8})*$`)

// validateClippingGeminiEditorial checks bounded model output without
// rewriting text or recomputing scores. The limits here are UTF-8 bytes.
func validateClippingGeminiEditorial(analysis ClippingGeminiAnalysis, options ClippingGeminiOptions) error {
	if !validClippingGeminiText(analysis.Summary, 4096) || len(analysis.LanguageCodes) < 1 || len(analysis.LanguageCodes) > 8 || len(analysis.Limitations) > 8 {
		return fmt.Errorf("%w: summary, languages or limitations outside bounds", ErrClippingGeminiInvalidAnalysis)
	}
	for _, code := range analysis.LanguageCodes {
		if len(code) > 35 || !clippingGeminiLanguageCodePattern.MatchString(code) {
			return fmt.Errorf("%w: invalid language code", ErrClippingGeminiInvalidAnalysis)
		}
	}
	for _, limitation := range analysis.Limitations {
		if !validClippingGeminiText(limitation, 1024) {
			return fmt.Errorf("%w: invalid limitation", ErrClippingGeminiInvalidAnalysis)
		}
	}
	if len(analysis.FewerCandidatesReason) > 2048 || (len(analysis.Candidates) < options.CandidateLimit && !validClippingGeminiText(analysis.FewerCandidatesReason, 2048)) {
		return fmt.Errorf("%w: fewer candidates need a bounded explanation", ErrClippingGeminiInvalidAnalysis)
	}
	for _, candidate := range analysis.Candidates {
		if len(candidate.TitleVariants) < 1 || len(candidate.TitleVariants) > 3 ||
			!validClippingGeminiText(candidate.Hook, 480) ||
			!validClippingGeminiText(candidate.Rationale, 2048) ||
			!validClippingGeminiText(candidate.BoundaryRationale, 1024) ||
			!validClippingGeminiText(candidate.Context, 2048) {
			return fmt.Errorf("%w: candidate editorial text outside bounds", ErrClippingGeminiInvalidAnalysis)
		}
		for _, title := range candidate.TitleVariants {
			if !validClippingGeminiText(title, 180) {
				return fmt.Errorf("%w: candidate title outside bounds", ErrClippingGeminiInvalidAnalysis)
			}
		}
		if !validClippingGeminiScore(candidate.EditorialScore) || !validClippingGeminiScore(candidate.Scores.Hook) ||
			!validClippingGeminiScore(candidate.Scores.EmotionalImpact) || !validClippingGeminiScore(candidate.Scores.RetentionPotential) ||
			!validClippingGeminiScore(candidate.Scores.Shareability) || !validClippingGeminiScore(candidate.Scores.StandaloneContext) {
			return fmt.Errorf("%w: candidate score outside bounds", ErrClippingGeminiInvalidAnalysis)
		}
		if len(candidate.Evidence) < 1 || len(candidate.Evidence) > 8 {
			return fmt.Errorf("%w: candidate evidence count outside bounds", ErrClippingGeminiInvalidAnalysis)
		}
		for _, evidence := range candidate.Evidence {
			if (evidence.Kind != "audio" && evidence.Kind != "visual" && evidence.Kind != "speech") ||
				!validClippingGeminiText(evidence.Detail, 1024) || evidence.StartMS < 0 || evidence.EndMS <= evidence.StartMS ||
				evidence.EndMS > analysis.SourceDurationMS || evidence.StartMS >= candidate.EndMS || evidence.EndMS <= candidate.StartMS {
				return fmt.Errorf("%w: candidate evidence is invalid", ErrClippingGeminiInvalidAnalysis)
			}
		}
	}
	return nil
}

func validClippingGeminiText(text string, maxBytes int) bool {
	return len(text) <= maxBytes && utf8.ValidString(text) && strings.TrimSpace(text) != ""
}

func validClippingGeminiScore(score int) bool {
	return score >= 0 && score <= 100
}
