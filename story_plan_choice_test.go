package main

import (
	"encoding/json"
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

func TestParseStoryPlanChoiceRejectsInvalidPlan(t *testing.T) {
	message := json.RawMessage(`{"content":null,"tool_calls":[{"function":{"name":"submit_story_plan","arguments":"{\"title\":\"incomplete\"}"}}]}`)
	_, raw, err := parseStoryPlanChoice(message)
	if err == nil || !strings.Contains(err.Error(), "valid JSON story plan") || raw == "" {
		t.Fatalf("expected actionable invalid-plan error and raw trace, got raw=%q err=%v", raw, err)
	}
}
