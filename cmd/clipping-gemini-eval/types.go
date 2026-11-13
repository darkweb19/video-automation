package main

import (
	"encoding/json"

	"video-automation/internal/app"
)

const (
	evaluationInputVersion   = "framevault.clipping.gemini.evaluation.v1"
	evaluationReportVersion  = "framevault.clipping.gemini.evaluation-report.v1"
	maxEvaluationInputBytes  = 8 << 20
	maxEvaluationReportBytes = 4 << 20
	maxEvaluationSources     = 256
	maxEvaluationRuns        = 512
	maxEvaluationReviews     = 512
	maxEvaluationLabels      = 64
	minimumReviewers         = 2
	maxEvaluationEvents      = 20_000
	maxEvaluationCandidates  = 10
	topK                     = 10
)

const (
	maxGeminiTargetDurationMS int64 = 40 * 60 * 1000
	durationMatchToleranceMS  int64 = 1000
)

// Dataset contains saved analyses and human annotations. It has no URL,
// credential, API-key, or source-media fields. Source references remain with
// the operator; an optional media digest requires a human-verification marker
// and is never independently verified by this tool.
type Dataset struct {
	Version       string         `json:"version"`
	Corpus        []CorpusSource `json:"corpus"`
	Runs          []SavedRun     `json:"runs"`
	SourceReviews []SourceReview `json:"source_reviews,omitempty"`
	RunReviews    []RunReview    `json:"run_reviews,omitempty"`
}

type CorpusSource struct {
	ID                       string   `json:"id"`
	Status                   string   `json:"status"`
	RightsStatus             string   `json:"rights_status"`
	ContentType              string   `json:"content_type"`
	LanguageCodes            []string `json:"language_codes"`
	HumanDurationMS          *int64   `json:"human_duration_ms,omitempty"`
	MediaSHA256              string   `json:"media_sha256,omitempty"`
	MediaSHA256HumanVerified bool     `json:"media_sha256_human_verified,omitempty"`
}

type SavedRun struct {
	ID                   string                    `json:"id"`
	SourceID             string                    `json:"source_id"`
	Options              app.ClippingGeminiOptions `json:"options"`
	ProviderSnapshot     ProviderSnapshot          `json:"provider_snapshot"`
	Analysis             json.RawMessage           `json:"analysis"`
	Usage                *UsageRecord              `json:"usage,omitempty"`
	ActualCostUSD        *float64                  `json:"actual_cost_usd,omitempty"`
	ActualCostEvidenceID string                    `json:"actual_cost_evidence_id,omitempty"`
}

type ProviderSnapshot struct {
	Provider       string `json:"provider"`
	Model          string `json:"model"`
	APIVersion     string `json:"api_version"`
	PromptVersion  string `json:"prompt_version"`
	PromptSHA256   string `json:"prompt_sha256"`
	SchemaVersion  string `json:"schema_version"`
	SchemaSHA256   string `json:"schema_sha256"`
	ProcessingMode string `json:"processing_mode"`
}

type UsageRecord struct {
	Status       string `json:"status"`
	PromptTokens *int64 `json:"prompt_tokens,omitempty"`
	OutputTokens *int64 `json:"output_tokens,omitempty"`
	TotalTokens  *int64 `json:"total_tokens,omitempty"`
}

type SourceReview struct {
	SourceID           string        `json:"source_id"`
	CoverageStatus     string        `json:"coverage_status"`
	ReviewedStartMS    *int64        `json:"reviewed_start_ms,omitempty"`
	ReviewedEndMS      *int64        `json:"reviewed_end_ms,omitempty"`
	ReviewerIDs        []string      `json:"reviewer_ids"`
	AdjudicationStatus string        `json:"adjudication_status"`
	Opportunities      []Opportunity `json:"opportunities"`
}

type RunReview struct {
	RunID              string            `json:"run_id"`
	ReviewerIDs        []string          `json:"reviewer_ids"`
	AdjudicationStatus string            `json:"adjudication_status"`
	CandidateReviews   []CandidateReview `json:"candidate_reviews"`
}

type Opportunity struct {
	ID            string   `json:"id"`
	StartMS       int64    `json:"start_ms"`
	EndMS         int64    `json:"end_ms"`
	Kinds         []string `json:"kinds"`
	LanguageCodes []string `json:"language_codes"`
	Exportable    bool     `json:"exportable"`
	Humor         string   `json:"humor"`
	Emotion       string   `json:"emotion"`
	Context       string   `json:"context"`
}

type CandidateReview struct {
	Rank                 int              `json:"rank"`
	Status               string           `json:"status"`
	Usability            string           `json:"usability"`
	MatchedOpportunityID string           `json:"matched_opportunity_id,omitempty"`
	SameMomentConfirmed  bool             `json:"same_moment_confirmed,omitempty"`
	StartBoundaryFit     string           `json:"start_boundary_fit"`
	EndBoundaryFit       string           `json:"end_boundary_fit"`
	Dimensions           ReviewDimensions `json:"dimensions"`
}

type ReviewDimensions struct {
	Completeness   *int `json:"completeness,omitempty"`
	Context        *int `json:"context,omitempty"`
	Hook           *int `json:"hook,omitempty"`
	Emotion        *int `json:"emotion,omitempty"`
	Humor          *int `json:"humor,omitempty"`
	VisualEvidence *int `json:"visual_evidence,omitempty"`
	Standalone     *int `json:"standalone,omitempty"`
}

type EvaluationReport struct {
	Version           string            `json:"version"`
	LocalStatus       string            `json:"local_status"`
	Corpus            CorpusSummary     `json:"corpus"`
	Runs              []RunSummary      `json:"runs"`
	RankSummary       []RankSummary     `json:"rank_summary"`
	Strata            []StratumSummary  `json:"strata"`
	ReadinessGates    []ReadinessGate   `json:"readiness_gates"`
	ExternalReadiness ExternalReadiness `json:"external_readiness"`
}

// ReadinessGate names one release prerequisite and records only a bounded
// status plus a stable reason code. No gate in an offline report can claim a
// live provider, rights, cost, quality, long-video, or deployment pass.
type ReadinessGate struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	ReasonCode string `json:"reason_code"`
}

type RankSummary struct {
	Rank            int      `json:"rank"`
	ReturnedOutputs int      `json:"returned_outputs"`
	ReviewedOutputs int      `json:"reviewed_outputs"`
	UsableOutputs   int      `json:"usable_outputs"`
	MatchedUsable   int      `json:"matched_usable"`
	UsabilityRate   *float64 `json:"usability_rate,omitempty"`
	EvidenceStatus  string   `json:"evidence_status"`
}

type CorpusSummary struct {
	SourceCount                 int            `json:"source_count"`
	SourceStatusCounts          map[string]int `json:"source_status_counts"`
	RightsStatusCounts          map[string]int `json:"rights_status_counts"`
	CoverageStatusCounts        map[string]int `json:"coverage_status_counts"`
	OperatorRightsAttestedCount int            `json:"operator_rights_attested_count"`
	HumanDurationCount          int            `json:"human_duration_count"`
	CompleteSourceReviewCount   int            `json:"complete_source_review_count"`
	EligibleSourceCount         int            `json:"eligible_source_count"`
	EvidenceStatus              string         `json:"evidence_status"`
}

type RunSummary struct {
	RunID                       string                `json:"run_id"`
	SourceID                    string                `json:"source_id"`
	AnalysisPayloadSHA256       string                `json:"analysis_payload_sha256"`
	AnalysisStatus              string                `json:"analysis_status"`
	ModelReportedDurationMS     int64                 `json:"model_reported_duration_ms,omitempty"`
	OperatorHumanDurationMS     *int64                `json:"operator_human_duration_ms,omitempty"`
	DurationComparisonStatus    string                `json:"duration_comparison_status"`
	DurationTargetStatus        string                `json:"duration_target_status"`
	DeclaredContentType         string                `json:"declared_content_type"`
	RequestedContentType        string                `json:"requested_content_type"`
	ContentTypeComparisonStatus string                `json:"content_type_comparison_status"`
	ModelReportedLanguageCodes  []string              `json:"model_reported_language_codes"`
	LanguageComparisonStatus    string                `json:"language_comparison_status"`
	CandidateCount              int                   `json:"candidate_count"`
	CandidateReviews            CandidateReviewCounts `json:"candidate_reviews"`
	RanksAsReturned             []int                 `json:"ranks_as_returned"`
	UsageStatus                 string                `json:"usage_status"`
	PromptTokens                *int64                `json:"prompt_tokens,omitempty"`
	OutputTokens                *int64                `json:"output_tokens,omitempty"`
	TotalTokens                 *int64                `json:"total_tokens,omitempty"`
	CostStatus                  string                `json:"cost_status"`
	ActualCostUSD               *float64              `json:"actual_cost_usd,omitempty"`
	HumanReviewStatus           string                `json:"human_review_status"`
	ReviewedOpportunityCount    int                   `json:"reviewed_opportunity_count"`
	UsableOpportunityCount      int                   `json:"usable_opportunity_count"`
	LateEventStatus             string                `json:"late_event_status"`
	LateOpportunityCount        int                   `json:"late_opportunity_count"`
	LateMatchedOpportunityCount int                   `json:"late_matched_opportunity_count"`
	TopK                        TopKSummary           `json:"top_k"`
	Rubric                      RubricSummary         `json:"rubric"`
}

type CandidateReviewCounts struct {
	Returned    int `json:"returned"`
	Reviewed    int `json:"reviewed"`
	Uncertain   int `json:"uncertain"`
	NotReviewed int `json:"not_reviewed"`
	Missing     int `json:"missing"`
}

type TopKSummary struct {
	K               int      `json:"k"`
	ReviewedOutputs int      `json:"reviewed_outputs"`
	ReturnedOutputs int      `json:"returned_outputs"`
	UsableOutputs   int      `json:"usable_outputs"`
	MatchedUsable   int      `json:"matched_usable"`
	Precision       *float64 `json:"precision,omitempty"`
	Recall          *float64 `json:"recall,omitempty"`
	PrecisionStatus string   `json:"precision_status"`
	RecallStatus    string   `json:"recall_status"`
}

type RubricSummary struct {
	Status                string             `json:"status"`
	RatedCandidateCount   int                `json:"rated_candidate_count"`
	DimensionMeans        map[string]float64 `json:"dimension_means,omitempty"`
	DimensionRatingCounts map[string]int     `json:"dimension_rating_counts,omitempty"`
	BoundaryFitCounts     map[string]int     `json:"boundary_fit_counts"`
}

type StratumSummary struct {
	Kind                        string   `json:"kind"`
	Key                         string   `json:"key"`
	SourceIDs                   []string `json:"source_ids"`
	SourceCount                 int      `json:"source_count"`
	OperatorRightsAttestedCount int      `json:"operator_rights_attested_count"`
	ReviewCompleteCount         int      `json:"review_complete_count"`
	PartialReviewCount          int      `json:"partial_review_count"`
	OpportunityCount            int      `json:"opportunity_count"`
	EvidenceStatus              string   `json:"evidence_status"`
}

type ExternalReadiness struct {
	ProviderAvailability string `json:"provider_availability"`
	LiveAuthentication   string `json:"live_authentication"`
	SourceRights         string `json:"source_rights"`
	HumanQuality         string `json:"human_quality"`
	ActualCost           string `json:"actual_cost"`
	LongVideoSupport     string `json:"long_video_support"`
	DeploymentBackup     string `json:"deployment_backup"`
}
