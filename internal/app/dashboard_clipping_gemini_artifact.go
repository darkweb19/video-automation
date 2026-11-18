package app

// clippingGeminiPublicArtifacts verifies the persisted envelope and strict v3
// payload before returning it unchanged. This avoids changing model-provided
// text, rank, or boundaries when an authenticated client reads results.
func clippingGeminiPublicArtifacts(jobID string, options ClippingGeminiOptions, artifacts []ClippingArtifact) ([]ClippingArtifact, error) {
	for _, artifact := range artifacts {
		if artifact.JobID != jobID {
			return nil, ErrClippingInvalidArtifact
		}
		if _, err := decodeClippingGeminiArtifact(artifact, options); err != nil {
			return nil, err
		}
	}
	return artifacts, nil
}
