package app

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"google.golang.org/genai"
)

const clippingGeminiMaxResponseMetadataBytes = 256

var (
	errClippingGeminiResponseBlocked   = errors.New("Gemini clipping response was blocked")
	errClippingGeminiResponseTruncated = errors.New("Gemini clipping response was incomplete")
	errClippingGeminiSourceTooLong     = errors.New("Gemini clipping model reported a source beyond the request duration limit")
)

// clippingGeminiResponse retains provider metadata even when analysis fails.
// UsageAvailable only means that the SDK returned a usage object. The SDK's
// scalar counts cannot distinguish omitted fields from explicitly reported zero.
type clippingGeminiResponse struct {
	Analysis            ClippingGeminiAnalysis
	ModelVersion        string
	ResponseID          string
	UsageAvailable      bool
	PromptTokens        int32
	OutputTokens        int32
	ThoughtsTokens      int32
	CachedTokens        int32
	ToolUsePromptTokens int32
	TotalTokens         int32
}

// decodeClippingGeminiResponse accepts one completed text candidate, validates
// the versioned analysis, and preserves Gemini's decisions without rewriting
// them. Source duration remains a model report, not a measurement of media.
// Errors intentionally omit all provider-supplied text and metadata.
func decodeClippingGeminiResponse(response *genai.GenerateContentResponse, options ClippingGeminiOptions) (clippingGeminiResponse, error) {
	var result clippingGeminiResponse
	if response == nil {
		return result, errClippingGeminiInvalidResponse
	}
	if !clippingGeminiCopyResponseMetadata(&result, response) {
		return result, errClippingGeminiInvalidResponse
	}

	if feedback := response.PromptFeedback; feedback != nil {
		if (feedback.BlockReason != "" && feedback.BlockReason != genai.BlockedReasonUnspecified) || clippingGeminiHasBlockedRating(feedback.SafetyRatings) {
			return result, errClippingGeminiResponseBlocked
		}
		if feedback.BlockReasonMessage != "" {
			return result, errClippingGeminiInvalidResponse
		}
	}
	if len(response.Candidates) != 1 || response.Candidates[0] == nil {
		return result, errClippingGeminiInvalidResponse
	}
	candidate := response.Candidates[0]
	if clippingGeminiHasBlockedRating(candidate.SafetyRatings) {
		return result, errClippingGeminiResponseBlocked
	}
	switch candidate.FinishReason {
	case genai.FinishReasonStop:
		// Only a completed response can be decoded. A token on STOP would
		// contradict the single-response, no-continuation request.
		if len(candidate.ContinuationToken) != 0 {
			return result, errClippingGeminiInvalidResponse
		}
	case genai.FinishReasonMaxTokens, genai.FinishReasonContinuation:
		return result, errClippingGeminiResponseTruncated
	case genai.FinishReasonSafety, genai.FinishReasonRecitation,
		genai.FinishReasonBlocklist, genai.FinishReasonProhibitedContent,
		genai.FinishReasonSPII, genai.FinishReasonImageSafety,
		genai.FinishReasonImageProhibitedContent, genai.FinishReasonImageRecitation:
		return result, errClippingGeminiResponseBlocked
	default:
		return result, errClippingGeminiInvalidResponse
	}

	content := candidate.Content
	if content == nil || (content.Role != "" && content.Role != genai.RoleModel) || len(content.Parts) == 0 {
		return result, errClippingGeminiInvalidResponse
	}
	var text strings.Builder
	for _, part := range content.Parts {
		if !clippingGeminiTextOnlyPart(part) {
			return result, errClippingGeminiInvalidResponse
		}
		if part.Thought {
			continue
		}
		if len(part.Text) > ClippingGeminiMaxAnalysisBytes-text.Len() {
			return result, errClippingGeminiInvalidResponse
		}
		text.WriteString(part.Text)
	}
	if text.Len() == 0 {
		return result, errClippingGeminiInvalidResponse
	}
	analysis, err := DecodeClippingGeminiAnalysis([]byte(text.String()), options)
	if err != nil {
		return result, errClippingGeminiInvalidResponse
	}
	if analysis.SourceDurationMS > clippingGeminiMaxRequestDurationMS {
		return result, errClippingGeminiSourceTooLong
	}
	result.Analysis = analysis
	return result, nil
}

func clippingGeminiHasBlockedRating(ratings []*genai.SafetyRating) bool {
	for _, rating := range ratings {
		if rating != nil && rating.Blocked {
			return true
		}
	}
	return false
}

// A Part may contain exactly one kind of content. Reject other output types
// even when they are mixed with text or marked as a model thought.
func clippingGeminiTextOnlyPart(part *genai.Part) bool {
	return part != nil && part.Text != "" &&
		part.MediaResolution == nil && part.CodeExecutionResult == nil &&
		part.ExecutableCode == nil && part.FileData == nil &&
		part.FunctionCall == nil && part.FunctionResponse == nil &&
		part.InlineData == nil && part.VideoMetadata == nil &&
		part.ToolCall == nil && part.ToolResponse == nil &&
		len(part.PartMetadata) == 0 && part.AudioTranscription == nil &&
		part.MediaProcessing == "" && part.SpeechMetadata == nil
}

func clippingGeminiCopyResponseMetadata(result *clippingGeminiResponse, response *genai.GenerateContentResponse) bool {
	valid := true
	if clippingGeminiSafeMetadata(response.ModelVersion) {
		result.ModelVersion = response.ModelVersion
	} else {
		valid = false
	}
	if clippingGeminiSafeMetadata(response.ResponseID) {
		result.ResponseID = response.ResponseID
	} else {
		valid = false
	}
	if usage := response.UsageMetadata; usage != nil {
		if usage.PromptTokenCount < 0 || usage.CandidatesTokenCount < 0 ||
			usage.ThoughtsTokenCount < 0 || usage.CachedContentTokenCount < 0 ||
			usage.ToolUsePromptTokenCount < 0 || usage.TotalTokenCount < 0 {
			return false
		}
		result.UsageAvailable = true
		result.PromptTokens = usage.PromptTokenCount
		result.OutputTokens = usage.CandidatesTokenCount
		result.ThoughtsTokens = usage.ThoughtsTokenCount
		result.CachedTokens = usage.CachedContentTokenCount
		result.ToolUsePromptTokens = usage.ToolUsePromptTokenCount
		result.TotalTokens = usage.TotalTokenCount
	}
	return valid
}

func clippingGeminiSafeMetadata(value string) bool {
	if len(value) > clippingGeminiMaxResponseMetadataBytes || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}
