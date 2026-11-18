package app

const (
	ClippingGeminiAnalysisVersion       = "framevault.clipping.analysis.v3"
	ClippingGeminiModelReportedDuration = "model_reported"
	// This is a defensive timestamp ceiling, not an assertion that a single
	// Gemini request supports a video of this duration.
	ClippingGeminiMaxSourceDurationMS int64 = 4 * 60 * 60 * 1000
)

// ClippingGeminiAnalysis contains the model's editorial decisions. Duration
// and timeline values are model-reported estimates, not independently probed
// media metadata. There is deliberately no source-media hash in this result.
type ClippingGeminiAnalysis struct {
	Version                  string                    `json:"version"`
	SourceDurationMS         int64                     `json:"source_duration_ms"`
	SourceDurationProvenance string                    `json:"source_duration_provenance"`
	Summary                  string                    `json:"summary"`
	LanguageCodes            []string                  `json:"language_codes"`
	Limitations              []string                  `json:"limitations"`
	Candidates               []ClippingGeminiCandidate `json:"candidates"`
	FewerCandidatesReason    string                    `json:"fewer_candidates_reason"`
}

// ClippingGeminiCandidate represents a model-selected source-time window.
// Rank, boundaries, titles, hook and scores must be preserved as returned.
// The first title is the primary title. Hook is an editorial suggestion, not
// a claim that the words were spoken in the video.
type ClippingGeminiCandidate struct {
	Rank              int                      `json:"rank"`
	StartMS           int64                    `json:"start_ms"`
	EndMS             int64                    `json:"end_ms"`
	TitleVariants     []string                 `json:"title_variants"`
	Hook              string                   `json:"hook"`
	Rationale         string                   `json:"rationale"`
	BoundaryRationale string                   `json:"boundary_rationale"`
	Context           string                   `json:"context"`
	EditorialScore    int                      `json:"editorial_score"`
	Scores            ClippingGeminiScores     `json:"scores"`
	Evidence          []ClippingGeminiEvidence `json:"evidence"`
}

// ClippingGeminiScores are model editorial estimates on a 0–100 scale. They
// are not calibrated probabilities or guarantees of audience response.
type ClippingGeminiScores struct {
	Hook               int `json:"hook"`
	EmotionalImpact    int `json:"emotional_impact"`
	RetentionPotential int `json:"retention_potential"`
	Shareability       int `json:"shareability"`
	StandaloneContext  int `json:"standalone_context"`
}

// ClippingGeminiEvidence is a model observation over a half-open source-time
// range; it is not independent verification or a verified transcript.
type ClippingGeminiEvidence struct {
	Kind    string `json:"kind"`
	StartMS int64  `json:"start_ms"`
	EndMS   int64  `json:"end_ms"`
	Detail  string `json:"detail"`
}
