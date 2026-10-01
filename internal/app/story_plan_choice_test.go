package app

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func validChoicePlan() StoryPlan {
	plan := StoryPlan{Title: "Title", Story: "Story", Script: "Script", Continuity: "Blue coat"}
	for i := 1; i <= ProjectSceneCount; i++ {
		plan.Scenes = append(plan.Scenes, StoryPlanScene{Number: i, Title: "Scene", Script: "Action", VideoPrompt: "Visible action"})
	}
	return plan
}

func TestParseStoryPlanChoice(t *testing.T) {
	planJSON, err := json.Marshal(validChoicePlan())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		message any
	}{
		{"tool arguments string", map[string]any{"content": nil, "tool_calls": []any{map[string]any{"function": map[string]any{"name": storyPlanToolName, "arguments": string(planJSON)}}}}},
		{"tool arguments object", map[string]any{"content": nil, "tool_calls": []any{map[string]any{"function": map[string]any{"name": storyPlanToolName, "arguments": json.RawMessage(planJSON)}}}}},
		{"invalid tool arguments with valid text fallback", map[string]any{"content": string(planJSON), "tool_calls": []any{map[string]any{"function": map[string]any{"name": storyPlanToolName, "arguments": "not a plan"}}}}},
		{"later matching tool call", map[string]any{"content": nil, "tool_calls": []any{map[string]any{"function": map[string]any{"name": storyPlanToolName, "arguments": "not a plan"}}, map[string]any{"function": map[string]any{"name": storyPlanToolName, "arguments": string(planJSON)}}}}},
		{"text blocks", map[string]any{"content": []any{map[string]string{"type": "image", "image_url": "ignored"}, map[string]string{"type": "text", "text": string(planJSON[:len(planJSON)/2])}, map[string]string{"type": "text", "text": string(planJSON[len(planJSON)/2:])}}}},
		{"text fallback", map[string]any{"content": "Here is the plan:\n```json\n" + string(planJSON) + "\n```"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			message, err := json.Marshal(tc.message)
			if err != nil {
				t.Fatal(err)
			}
			got, raw, err := parseStoryPlanChoice(message)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Scenes) != ProjectSceneCount || raw == "" {
				t.Fatalf("unexpected plan: scenes=%d raw=%q", len(got.Scenes), raw)
			}
		})
	}
}

func TestParseStoryPlanChoiceRejectsRefusalWithValidContent(t *testing.T) {
	planJSON, _ := json.Marshal(validChoicePlan())
	for _, message := range []map[string]any{
		{"content": string(planJSON), "refusal": "Declined"},
		{"content": []any{map[string]string{"type": "text", "text": string(planJSON)}, map[string]string{"type": "refusal", "refusal": "Declined"}}},
	} {
		encoded, _ := json.Marshal(message)
		_, raw, err := parseStoryPlanChoice(encoded)
		var upstream *upstreamError
		if !errors.As(err, &upstream) || upstream.StatusCode != 403 || upstream.ErrorType != "refusal" || raw != "" {
			t.Fatalf("refusal accepted as a valid story: raw=%q err=%v", raw, err)
		}
	}
}

func TestParseStoryPlanChoiceRejectsInvalidPlan(t *testing.T) {
	message := json.RawMessage(`{"content":null,"tool_calls":[{"function":{"name":"submit_story_plan","arguments":"{\"title\":\"incomplete\"}"}}]}`)
	_, raw, err := parseStoryPlanChoice(message)
	if err == nil || !strings.Contains(err.Error(), "valid JSON story plan") || raw == "" {
		t.Fatalf("expected actionable invalid-plan error and raw trace, got raw=%q err=%v", raw, err)
	}
}
