package main

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
	var raw string
	for _, call := range choice.ToolCalls {
		if call.Function.Name != storyPlanToolName {
			continue
		}
		arguments := bytes.TrimSpace(call.Function.Arguments)
		if len(arguments) > 0 && arguments[0] == '"' {
			if err := json.Unmarshal(arguments, &raw); err != nil {
				raw = string(arguments)
			}
		} else {
			raw = string(arguments)
		}
		break
	}
	if len(raw) > MaxScriptRawResponseBytes {
		return StoryPlan{}, "", fmt.Errorf("OpenRouter raw script response exceeds %d bytes", MaxScriptRawResponseBytes)
	}
	if strings.TrimSpace(raw) != "" {
		if plan, err := parseStoryPlanText(raw); err == nil {
			return plan, raw, nil
		}
	}
	if len(choice.Content) > 0 && string(choice.Content) != "null" {
		var content string
		if err := json.Unmarshal(choice.Content, &content); err == nil && strings.TrimSpace(content) != "" {
			if len(content) > MaxScriptRawResponseBytes {
				return StoryPlan{}, "", fmt.Errorf("OpenRouter raw script response exceeds %d bytes", MaxScriptRawResponseBytes)
			}
			if plan, err := parseStoryPlanText(content); err == nil {
				return plan, content, nil
			}
			if raw == "" {
				raw = content
			}
		}
	}
	if strings.TrimSpace(raw) == "" {
		return StoryPlan{}, "", fmt.Errorf("OpenRouter returned an empty script")
	}
	plan, err := parseStoryPlanText(raw)
	return plan, raw, err
}
