package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/genai"
)

func geminiResponseTestOptions() ClippingGeminiOptions {
	return ClippingGeminiOptions{ContentType: "general", MinClipMS: 15_000, MaxClipMS: 180_000, CandidateLimit: 2}
}

func geminiResponseTestFixture(t *testing.T, name string) string {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join("..", "..", "testdata", "clipping_gemini", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return string(payload)
}

func geminiResponseTestValid(payload string) *genai.GenerateContentResponse {
	return &genai.GenerateContentResponse{
		ModelVersion: "gemini-test-version",
		ResponseID:   "response-test-id",
		UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount: 100, CandidatesTokenCount: 20, ThoughtsTokenCount: 4,
			CachedContentTokenCount: 8, ToolUsePromptTokenCount: 3, TotalTokenCount: 127,
		},
		Candidates: []*genai.Candidate{{
			FinishReason: genai.FinishReasonStop,
			Content:      &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{Text: payload}}},
		}},
	}
}

func assertGeminiResponseMetadata(t *testing.T, result clippingGeminiResponse) {
	t.Helper()
	if result.ModelVersion != "gemini-test-version" || result.ResponseID != "response-test-id" ||
		!result.UsageAvailable || result.PromptTokens != 100 || result.OutputTokens != 20 ||
		result.ThoughtsTokens != 4 || result.CachedTokens != 8 || result.ToolUsePromptTokens != 3 || result.TotalTokens != 127 {
		t.Fatal("provider metadata was not preserved")
	}
}

func TestDecodeClippingGeminiResponseValid(t *testing.T) {
	for _, fixture := range []string{"ranked.json", "shortfall.json", "empty.json"} {
		t.Run(fixture, func(t *testing.T) {
			payload := geminiResponseTestFixture(t, fixture)
			want, err := DecodeClippingGeminiAnalysis([]byte(payload), geminiResponseTestOptions())
			if err != nil {
				t.Fatalf("fixture: %v", err)
			}
			response := geminiResponseTestValid(payload)
			mid := len(payload) / 2
			response.Candidates[0].Content.Parts = []*genai.Part{
				{Text: payload[:mid]},
				{Text: "private reasoning that is not part of the result", Thought: true},
				{Text: payload[mid:]},
			}
			got, err := decodeClippingGeminiResponse(response, geminiResponseTestOptions())
			if err != nil {
				t.Fatalf("valid response: %v", err)
			}
			if !reflect.DeepEqual(got.Analysis, want) {
				t.Fatal("analysis was not preserved in model-provided order and content")
			}
			assertGeminiResponseMetadata(t, got)
		})
	}
}

func TestDecodeClippingGeminiResponseNeutralPromptFeedback(t *testing.T) {
	payload := geminiResponseTestFixture(t, "empty.json")
	response := geminiResponseTestValid(payload)
	response.PromptFeedback = &genai.GenerateContentResponsePromptFeedback{
		BlockReason:   genai.BlockedReasonUnspecified,
		SafetyRatings: []*genai.SafetyRating{{Blocked: false}},
	}
	result, err := decodeClippingGeminiResponse(response, geminiResponseTestOptions())
	if err != nil || result.Analysis.Version != ClippingGeminiAnalysisVersion {
		t.Fatal("neutral prompt feedback rejected a completed valid response")
	}
	assertGeminiResponseMetadata(t, result)
}

func TestDecodeClippingGeminiResponseUsagePresence(t *testing.T) {
	payload := geminiResponseTestFixture(t, "empty.json")
	for _, available := range []bool{false, true} {
		t.Run(fmt.Sprint(available), func(t *testing.T) {
			response := geminiResponseTestValid(payload)
			response.UsageMetadata = nil
			if available {
				response.UsageMetadata = &genai.GenerateContentResponseUsageMetadata{}
			}
			result, err := decodeClippingGeminiResponse(response, geminiResponseTestOptions())
			if err != nil || result.UsageAvailable != available {
				t.Fatal("usage object presence was not preserved")
			}
			if result.PromptTokens != 0 || result.OutputTokens != 0 || result.ThoughtsTokens != 0 ||
				result.CachedTokens != 0 || result.ToolUsePromptTokens != 0 || result.TotalTokens != 0 {
				t.Fatal("zero-valued usage fields changed")
			}
		})
	}
}

func TestDecodeClippingGeminiResponseRejection(t *testing.T) {
	payload := geminiResponseTestFixture(t, "ranked.json")
	tests := []struct {
		name   string
		mutate func(*genai.GenerateContentResponse)
		want   error
	}{
		{"prompt block", func(r *genai.GenerateContentResponse) {
			r.PromptFeedback = &genai.GenerateContentResponsePromptFeedback{BlockReason: genai.BlockedReasonSafety, BlockReasonMessage: "private-marker"}
		}, errClippingGeminiResponseBlocked},
		{"unknown prompt block", func(r *genai.GenerateContentResponse) {
			r.PromptFeedback = &genai.GenerateContentResponsePromptFeedback{BlockReason: "NEW_BLOCK_REASON"}
		}, errClippingGeminiResponseBlocked},
		{"block message without reason", func(r *genai.GenerateContentResponse) {
			r.PromptFeedback = &genai.GenerateContentResponsePromptFeedback{BlockReasonMessage: "private-marker"}
		}, errClippingGeminiInvalidResponse},
		{"blocked prompt rating", func(r *genai.GenerateContentResponse) {
			r.PromptFeedback = &genai.GenerateContentResponsePromptFeedback{SafetyRatings: []*genai.SafetyRating{{Blocked: true}}}
		}, errClippingGeminiResponseBlocked},
		{"unspecified reason with blocked rating", func(r *genai.GenerateContentResponse) {
			r.PromptFeedback = &genai.GenerateContentResponsePromptFeedback{BlockReason: genai.BlockedReasonUnspecified, SafetyRatings: []*genai.SafetyRating{{Blocked: true}}}
		}, errClippingGeminiResponseBlocked},
		{"blocked candidate rating", func(r *genai.GenerateContentResponse) {
			r.Candidates[0].SafetyRatings = []*genai.SafetyRating{{Blocked: true}}
		}, errClippingGeminiResponseBlocked},
		{"safety finish", func(r *genai.GenerateContentResponse) { r.Candidates[0].FinishReason = genai.FinishReasonSafety }, errClippingGeminiResponseBlocked},
		{"recitation finish", func(r *genai.GenerateContentResponse) { r.Candidates[0].FinishReason = genai.FinishReasonRecitation }, errClippingGeminiResponseBlocked},
		{"max tokens", func(r *genai.GenerateContentResponse) { r.Candidates[0].FinishReason = genai.FinishReasonMaxTokens }, errClippingGeminiResponseTruncated},
		{"continuation", func(r *genai.GenerateContentResponse) {
			r.Candidates[0].FinishReason = genai.FinishReasonContinuation
			r.Candidates[0].ContinuationToken = []byte("private-marker")
		}, errClippingGeminiResponseTruncated},
		{"unexpected continuation token", func(r *genai.GenerateContentResponse) { r.Candidates[0].ContinuationToken = []byte("private-marker") }, errClippingGeminiInvalidResponse},
		{"unfinished", func(r *genai.GenerateContentResponse) { r.Candidates[0].FinishReason = "" }, errClippingGeminiInvalidResponse},
		{"unknown finish", func(r *genai.GenerateContentResponse) { r.Candidates[0].FinishReason = "NEW_FINISH_REASON" }, errClippingGeminiInvalidResponse},
		{"tool finish", func(r *genai.GenerateContentResponse) {
			r.Candidates[0].FinishReason = genai.FinishReasonUnexpectedToolCall
		}, errClippingGeminiInvalidResponse},
		{"no candidate", func(r *genai.GenerateContentResponse) { r.Candidates = nil }, errClippingGeminiInvalidResponse},
		{"multiple candidates", func(r *genai.GenerateContentResponse) { r.Candidates = append(r.Candidates, r.Candidates[0]) }, errClippingGeminiInvalidResponse},
		{"nil candidate", func(r *genai.GenerateContentResponse) { r.Candidates[0] = nil }, errClippingGeminiInvalidResponse},
		{"nil content", func(r *genai.GenerateContentResponse) { r.Candidates[0].Content = nil }, errClippingGeminiInvalidResponse},
		{"user role", func(r *genai.GenerateContentResponse) { r.Candidates[0].Content.Role = genai.RoleUser }, errClippingGeminiInvalidResponse},
		{"no parts", func(r *genai.GenerateContentResponse) { r.Candidates[0].Content.Parts = nil }, errClippingGeminiInvalidResponse},
		{"nil part", func(r *genai.GenerateContentResponse) { r.Candidates[0].Content.Parts = []*genai.Part{nil} }, errClippingGeminiInvalidResponse},
		{"empty text", func(r *genai.GenerateContentResponse) { r.Candidates[0].Content.Parts = []*genai.Part{{Text: ""}} }, errClippingGeminiInvalidResponse},
		{"thought only", func(r *genai.GenerateContentResponse) {
			r.Candidates[0].Content.Parts = []*genai.Part{{Text: "private-marker", Thought: true}}
		}, errClippingGeminiInvalidResponse},
		{"invalid JSON", func(r *genai.GenerateContentResponse) { r.Candidates[0].Content.Parts[0].Text = "private-marker" }, errClippingGeminiInvalidResponse},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := geminiResponseTestValid(payload)
			tt.mutate(response)
			result, err := decodeClippingGeminiResponse(response, geminiResponseTestOptions())
			if !errors.Is(err, tt.want) {
				t.Fatalf("wrong response classification: %v", err)
			}
			if !reflect.DeepEqual(result.Analysis, ClippingGeminiAnalysis{}) {
				t.Fatal("failure returned a partial analysis")
			}
			assertGeminiResponseMetadata(t, result)
			if strings.Contains(err.Error(), "private-marker") {
				t.Fatal("error exposed provider text")
			}
		})
	}
	result, err := decodeClippingGeminiResponse(nil, geminiResponseTestOptions())
	if !errors.Is(err, errClippingGeminiInvalidResponse) || !reflect.DeepEqual(result, clippingGeminiResponse{}) {
		t.Fatal("nil response was not rejected safely")
	}
}

func TestDecodeClippingGeminiResponseRejectsNonTextParts(t *testing.T) {
	payload := geminiResponseTestFixture(t, "empty.json")
	tests := []struct {
		name string
		part *genai.Part
	}{
		{"function call", &genai.Part{FunctionCall: &genai.FunctionCall{}}},
		{"function response", &genai.Part{FunctionResponse: &genai.FunctionResponse{}}},
		{"server tool call", &genai.Part{ToolCall: &genai.ToolCall{}}},
		{"server tool response", &genai.Part{ToolResponse: &genai.ToolResponse{}}},
		{"inline media", &genai.Part{InlineData: &genai.Blob{}}},
		{"file media", &genai.Part{FileData: &genai.FileData{}}},
		{"executable code", &genai.Part{ExecutableCode: &genai.ExecutableCode{}}},
		{"code result", &genai.Part{CodeExecutionResult: &genai.CodeExecutionResult{}}},
		{"transcription", &genai.Part{AudioTranscription: &genai.Transcription{}}},
		{"video metadata", &genai.Part{VideoMetadata: &genai.VideoMetadata{}}},
		{"thought with tool call", &genai.Part{Text: "private-marker", Thought: true, FunctionCall: &genai.FunctionCall{}}},
		{"text mixed with media", &genai.Part{Text: payload, InlineData: &genai.Blob{}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := geminiResponseTestValid(payload)
			response.Candidates[0].Content.Parts = append(response.Candidates[0].Content.Parts, tt.part)
			result, err := decodeClippingGeminiResponse(response, geminiResponseTestOptions())
			if !errors.Is(err, errClippingGeminiInvalidResponse) || !reflect.DeepEqual(result.Analysis, ClippingGeminiAnalysis{}) {
				t.Fatal("non-text part was accepted")
			}
			assertGeminiResponseMetadata(t, result)
		})
	}
}

func TestDecodeClippingGeminiResponseTextBounds(t *testing.T) {
	payload := geminiResponseTestFixture(t, "empty.json")
	for _, size := range []int{ClippingGeminiMaxAnalysisBytes, ClippingGeminiMaxAnalysisBytes + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			response := geminiResponseTestValid(payload)
			padding := strings.Repeat(" ", size-len(payload))
			response.Candidates[0].Content.Parts = []*genai.Part{{Text: payload}, {Text: padding}}
			result, err := decodeClippingGeminiResponse(response, geminiResponseTestOptions())
			if size == ClippingGeminiMaxAnalysisBytes {
				if err != nil || result.Analysis.Version != ClippingGeminiAnalysisVersion {
					t.Fatal("exact-limit valid text was rejected")
				}
			} else if !errors.Is(err, errClippingGeminiInvalidResponse) || !reflect.DeepEqual(result.Analysis, ClippingGeminiAnalysis{}) {
				t.Fatal("oversized text was accepted")
			}
			assertGeminiResponseMetadata(t, result)
		})
	}
}

func TestDecodeClippingGeminiResponseModelReportedDuration(t *testing.T) {
	fixture := geminiResponseTestFixture(t, "empty.json")
	for _, duration := range []int64{clippingGeminiMaxRequestDurationMS, clippingGeminiMaxRequestDurationMS + 1} {
		t.Run(fmt.Sprint(duration), func(t *testing.T) {
			payload := strings.Replace(fixture, `"source_duration_ms": 8000`, fmt.Sprintf(`"source_duration_ms": %d`, duration), 1)
			if payload == fixture {
				t.Fatal("fixture did not contain the expected duration field")
			}
			result, err := decodeClippingGeminiResponse(geminiResponseTestValid(payload), geminiResponseTestOptions())
			if duration == clippingGeminiMaxRequestDurationMS {
				if err != nil || result.Analysis.SourceDurationMS != duration || len(result.Analysis.Candidates) != 0 {
					t.Fatal("exact limit with zero candidates was rejected")
				}
			} else if !errors.Is(err, errClippingGeminiSourceTooLong) || !reflect.DeepEqual(result.Analysis, ClippingGeminiAnalysis{}) {
				t.Fatal("over-limit model-reported duration with zero candidates was accepted")
			}
			assertGeminiResponseMetadata(t, result)
		})
	}
}

func TestDecodeClippingGeminiResponseInvalidMetadata(t *testing.T) {
	payload := geminiResponseTestFixture(t, "empty.json")
	invalidUsage := []struct {
		name   string
		mutate func(*genai.GenerateContentResponseUsageMetadata)
	}{
		{"prompt", func(u *genai.GenerateContentResponseUsageMetadata) { u.PromptTokenCount = -1 }},
		{"output", func(u *genai.GenerateContentResponseUsageMetadata) { u.CandidatesTokenCount = -1 }},
		{"thoughts", func(u *genai.GenerateContentResponseUsageMetadata) { u.ThoughtsTokenCount = -1 }},
		{"cached", func(u *genai.GenerateContentResponseUsageMetadata) { u.CachedContentTokenCount = -1 }},
		{"tool", func(u *genai.GenerateContentResponseUsageMetadata) { u.ToolUsePromptTokenCount = -1 }},
		{"total", func(u *genai.GenerateContentResponseUsageMetadata) { u.TotalTokenCount = -1 }},
	}
	for _, tt := range invalidUsage {
		t.Run("negative "+tt.name, func(t *testing.T) {
			response := geminiResponseTestValid(payload)
			tt.mutate(response.UsageMetadata)
			result, err := decodeClippingGeminiResponse(response, geminiResponseTestOptions())
			if !errors.Is(err, errClippingGeminiInvalidResponse) || result.UsageAvailable ||
				result.ModelVersion != response.ModelVersion || result.ResponseID != response.ResponseID ||
				!reflect.DeepEqual(result.Analysis, ClippingGeminiAnalysis{}) {
				t.Fatal("invalid usage was accepted or valid metadata was discarded")
			}
		})
	}
	for _, value := range []string{strings.Repeat("x", clippingGeminiMaxResponseMetadataBytes+1), "private-marker\n", string([]byte{0xff})} {
		t.Run("invalid ID", func(t *testing.T) {
			response := geminiResponseTestValid(payload)
			response.ResponseID = value
			result, err := decodeClippingGeminiResponse(response, geminiResponseTestOptions())
			if !errors.Is(err, errClippingGeminiInvalidResponse) || result.ResponseID != "" ||
				result.ModelVersion != response.ModelVersion || !result.UsageAvailable ||
				!reflect.DeepEqual(result.Analysis, ClippingGeminiAnalysis{}) {
				t.Fatal("invalid response metadata was accepted or valid metadata was discarded")
			}
			if strings.Contains(err.Error(), "private-marker") {
				t.Fatal("error exposed invalid metadata")
			}
		})
	}
}
