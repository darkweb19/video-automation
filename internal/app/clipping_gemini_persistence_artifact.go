package app

import "fmt"

// clippingGeminiArtifactFromPayload preserves Gemini's rank/order, exact text
// and source-time boundaries. Its duration is explicitly model-reported by the
// v3 payload; it is not copied onto the source or treated as probed media.
func clippingGeminiArtifactFromPayload(jobID string, payload []byte, options ClippingGeminiOptions) (ClippingArtifact, error) {
	if !safeID(jobID) {
		return ClippingArtifact{}, ErrClippingInvalidArtifact
	}
	analysis, err := DecodeClippingGeminiAnalysis(payload, options)
	if err != nil {
		return ClippingArtifact{}, err
	}
	if analysis.SourceDurationMS > clippingGeminiMaxRequestDurationMS {
		return ClippingArtifact{}, errClippingGeminiSourceTooLong
	}
	ranges := make([]ClippingTimeRange, 0, len(analysis.Candidates))
	for _, candidate := range analysis.Candidates {
		ranges = append(ranges, ClippingTimeRange{StartMS: candidate.StartMS, EndMS: candidate.EndMS})
	}
	artifact := ClippingArtifact{JobID: jobID, Type: clippingAnalysisArtifactType, SchemaVersion: ClippingGeminiAnalysisVersion, Version: 1, SourceDurationMS: analysis.SourceDurationMS, TimeRanges: ranges, PayloadJSON: string(payload)}
	if _, err := validateClippingArtifact(artifact, jobID, analysis.SourceDurationMS); err != nil {
		return ClippingArtifact{}, err
	}
	return artifact, nil
}

// decodeClippingGeminiArtifact checks the envelope as well as the v3 payload;
// mismatched or legacy artifacts are never reinterpreted as Gemini output.
func decodeClippingGeminiArtifact(artifact ClippingArtifact, options ClippingGeminiOptions) (ClippingGeminiAnalysis, error) {
	if artifact.Type != clippingAnalysisArtifactType || artifact.SchemaVersion != ClippingGeminiAnalysisVersion || artifact.Version != 1 {
		return ClippingGeminiAnalysis{}, ErrClippingInvalidArtifact
	}
	analysis, err := DecodeClippingGeminiAnalysis([]byte(artifact.PayloadJSON), options)
	if err != nil {
		return ClippingGeminiAnalysis{}, err
	}
	if analysis.SourceDurationMS > clippingGeminiMaxRequestDurationMS {
		return ClippingGeminiAnalysis{}, errClippingGeminiSourceTooLong
	}
	if _, err := validateClippingArtifact(artifact, artifact.JobID, analysis.SourceDurationMS); err != nil {
		return ClippingGeminiAnalysis{}, err
	}
	if len(artifact.TimeRanges) != len(analysis.Candidates) {
		return ClippingGeminiAnalysis{}, fmt.Errorf("%w: Gemini candidate ranges disagree", ErrClippingInvalidArtifact)
	}
	for i, candidate := range analysis.Candidates {
		if artifact.TimeRanges[i].StartMS != candidate.StartMS || artifact.TimeRanges[i].EndMS != candidate.EndMS {
			return ClippingGeminiAnalysis{}, fmt.Errorf("%w: Gemini candidate ranges disagree", ErrClippingInvalidArtifact)
		}
	}
	return analysis, nil
}
