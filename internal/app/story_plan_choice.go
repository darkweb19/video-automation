package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

const storyPlanToolName = "submit_story_plan"

// parseStoryPlanChoice accepts the function call used by models that support
// tools, while retaining the plain-text fallback for compatible responses.
func parseStoryPlanChoice(message json.RawMessage) (StoryPlan, string, error) {
	var choice struct {
		Content   json.RawMessage `json:"content"`
		Refusal   json.RawMessage `json:"refusal"`
		ToolCalls []struct {
			Function struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
	}
	if err := json.Unmarshal(message, &choice); err != nil {
		return StoryPlan{}, "", fmt.Errorf("decode OpenRouter script message: %w", err)
	}
	if openRouterErrorString(choice.Refusal) != "" {
		return StoryPlan{}, "", &upstreamError{StatusCode: 403, ErrorType: "refusal"}
	}
	var blocks []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(choice.Content, &blocks) == nil {
		for _, block := range blocks {
			if block.Type == "refusal" {
				return StoryPlan{}, "", &upstreamError{StatusCode: 403, ErrorType: "refusal"}
			}
		}
	}
	var raw string
	var lastErr error
	for _, call := range choice.ToolCalls {
		if call.Function.Name != storyPlanToolName {
			continue
		}
		arguments := bytes.TrimSpace(call.Function.Arguments)
		var candidate string
		if len(arguments) > 0 && arguments[0] == '"' {
			if err := json.Unmarshal(arguments, &candidate); err != nil {
				candidate = string(arguments)
			}
		} else {
			candidate = string(arguments)
		}
		if len(candidate) > MaxScriptRawResponseBytes {
			lastErr = fmt.Errorf("OpenRouter raw script response exceeds %d bytes", MaxScriptRawResponseBytes)
			continue
		}
		if strings.TrimSpace(candidate) != "" {
			raw = candidate
			plan, err := parseStoryPlanText(raw)
			if err == nil {
				return plan, raw, nil
			}
			lastErr = err
		}
	}
	if content := openRouterMessageText(choice.Content); strings.TrimSpace(content) != "" {
		if len(content) > MaxScriptRawResponseBytes {
			return StoryPlan{}, "", fmt.Errorf("OpenRouter raw script response exceeds %d bytes", MaxScriptRawResponseBytes)
		}
		if plan, err := parseStoryPlanText(content); err == nil {
			return plan, content, nil
		} else {
			lastErr = err
		}
		raw = content
	}
	if lastErr != nil {
		return StoryPlan{}, raw, lastErr
	}
	if strings.TrimSpace(raw) == "" {
		return StoryPlan{}, "", fmt.Errorf("OpenRouter returned an empty script")
	}
	return StoryPlan{}, raw, errStoryPlanUnusable
}
