package app

import "fmt"

// validateClippingGeminiTimeline only checks bounds and consistency. It does
// not verify the model's reported duration, select windows, sort, or dedupe.
func validateClippingGeminiTimeline(analysis ClippingGeminiAnalysis, options ClippingGeminiOptions) error {
	if analysis.SourceDurationMS < 1 || analysis.SourceDurationMS > ClippingGeminiMaxSourceDurationMS {
		return fmt.Errorf("%w: model-reported duration outside bounds", ErrClippingGeminiInvalidAnalysis)
	}
	if len(analysis.Candidates) > options.CandidateLimit {
		return fmt.Errorf("%w: candidate count exceeds requested limit", ErrClippingGeminiInvalidAnalysis)
	}
	for index, candidate := range analysis.Candidates {
		if candidate.Rank != index+1 {
			return fmt.Errorf("%w: candidates must be in contiguous rank order", ErrClippingGeminiInvalidAnalysis)
		}
		if candidate.StartMS < 0 || candidate.EndMS <= candidate.StartMS || candidate.EndMS > analysis.SourceDurationMS {
			return fmt.Errorf("%w: candidate range outside reported duration", ErrClippingGeminiInvalidAnalysis)
		}
		durationMS := candidate.EndMS - candidate.StartMS
		if durationMS < options.MinClipMS || durationMS > options.MaxClipMS {
			return fmt.Errorf("%w: candidate duration outside requested bounds", ErrClippingGeminiInvalidAnalysis)
		}
	}
	return nil
}
