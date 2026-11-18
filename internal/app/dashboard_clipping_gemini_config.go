package app

// clippingGeminiConfigView describes local configuration only. It does not
// discover credentials or probe the provider, model, or YouTube access.
type clippingGeminiConfigView struct {
	Configured   bool                       `json:"configured"`
	Enabled      bool                       `json:"enabled"`
	Verification string                     `json:"verification"`
	Limits       clippingGeminiConfigLimits `json:"limits"`
}

type clippingGeminiConfigLimits struct {
	MinClipMS           int64 `json:"min_clip_ms"`
	MaxClipMS           int64 `json:"max_clip_ms"`
	MaxCandidates       int   `json:"max_candidates"`
	MaxSourceDurationMS int64 `json:"max_source_duration_ms"`
}

func clippingGeminiDescriptor(getenv func(string) string) clippingGeminiConfigView {
	view := clippingGeminiConfigView{
		Verification: "unverified",
		Limits: clippingGeminiConfigLimits{
			MinClipMS:           ClippingGeminiMinClipMS,
			MaxClipMS:           ClippingGeminiMaxClipMS,
			MaxCandidates:       ClippingGeminiMaxCandidates,
			MaxSourceDurationMS: clippingGeminiMaxRequestDurationMS,
		},
	}
	if getenv == nil {
		return view
	}
	view.Enabled = clippingGeminiRuntimeEnabled(getenv(clippingGeminiEnabledEnv))
	view.Configured = ValidateClippingGeminiConfig(ClippingGeminiConfig{
		ProjectID: getenv(clippingGeminiProjectEnv),
		Location:  getenv(clippingGeminiLocationEnv),
	}) == nil
	return view
}
