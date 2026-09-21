package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

const scriptSystemPrompt = `You are a short-form visual storyteller and video prompt director. Create a complete silent 30-second vertical video plan from the user's topic. The final video has exactly five sequential scenes, each exactly six seconds. There is no narration, dialogue, subtitles, music, logos, or on-screen text. Tell the story only through visible action.

Create one immutable continuity bible covering every recurring character's exact physical appearance and wardrobe, the visual style, color palette, lighting, camera language, time of day, and environment. Repeat the relevant continuity details verbatim inside every scene's video_prompt so each clip can be generated independently. Each video_prompt must describe only its six-second shot, start state, motion, camera movement, end state, vertical 9:16 composition, and continuity details. Avoid transitions that require footage from another scene.`

func storyPlanSchema() map[string]any {
	scene := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"number":       map[string]any{"type": "integer", "minimum": 1, "maximum": ProjectSceneCount},
			"title":        map[string]any{"type": "string"},
			"script":       map[string]any{"type": "string", "description": "Visible action during this six-second scene."},
			"video_prompt": map[string]any{"type": "string", "description": "Standalone video-generation prompt with repeated continuity details."},
		},
		"required":             []string{"number", "title", "script", "video_prompt"},
		"additionalProperties": false,
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"title":      map[string]any{"type": "string"},
			"story":      map[string]any{"type": "string", "description": "Complete story synopsis."},
			"script":     map[string]any{"type": "string", "description": "Full 30-second visual script covering all five scenes."},
			"continuity": map[string]any{"type": "string", "description": "Immutable character, wardrobe, environment, palette, lighting, style, and camera bible."},
			"scenes": map[string]any{
				"type": "array", "minItems": ProjectSceneCount, "maxItems": ProjectSceneCount, "items": scene,
			},
		},
		"required":             []string{"title", "story", "script", "continuity", "scenes"},
		"additionalProperties": false,
	}
}

const continuityPromptSuffix = "\n\nContinuity bible - apply exactly in this scene:\n"

// appendContinuityBible normalizes scene prompts at the storage boundary. The
// provider response is not the only possible source of a plan, so persisted
// prompts must always carry the same continuity bible and remain within the
// provider's prompt limit.
func appendContinuityBible(plan *StoryPlan) error {
	continuity := strings.TrimSpace(plan.Continuity)
	if continuity == "" {
		return errors.New("script plan is missing continuity rules")
	}
	for index := range plan.Scenes {
		prompt := strings.TrimSpace(plan.Scenes[index].VideoPrompt)
		if marker := strings.Index(prompt, "\n\nContinuity bible"); marker >= 0 {
			prompt = strings.TrimSpace(prompt[:marker])
		}
		prompt += continuityPromptSuffix + continuity
		if utf8.RuneCountInString(prompt) > MaxPromptLength {
			return fmt.Errorf("scene %d prompt exceeds %d characters after continuity rules", plan.Scenes[index].Number, MaxPromptLength)
		}
		plan.Scenes[index].VideoPrompt = prompt
	}
	return nil
}

func (c *OpenRouterClient) GenerateStoryPlan(ctx context.Context, topic string) (StoryPlan, error) {
	topic = strings.TrimSpace(topic)
	if topic == "" {
		return StoryPlan{}, errors.New("topic is required")
	}
	request := map[string]any{
		"model": ScriptModel,
		"messages": []map[string]string{
			{"role": "system", "content": scriptSystemPrompt},
			{"role": "user", "content": "Create the video plan for this topic or story idea:\n\n" + topic},
		},
		"temperature": 0.7,
		"response_format": map[string]any{
			"type":        "json_schema",
			"json_schema": map[string]any{"name": "short_video_plan", "strict": true, "schema": storyPlanSchema()},
		},
		"provider": map[string]any{"require_parameters": true},
	}
	var response struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := c.doJSON(ctx, "POST", "/chat/completions", request, &response); err != nil {
		return StoryPlan{}, err
	}
	if len(response.Choices) == 0 || strings.TrimSpace(response.Choices[0].Message.Content) == "" {
		return StoryPlan{}, errors.New("OpenRouter returned an empty script")
	}
	var plan StoryPlan
	if err := json.Unmarshal([]byte(response.Choices[0].Message.Content), &plan); err != nil {
		return StoryPlan{}, fmt.Errorf("decode generated script: %w", err)
	}
	if err := validateStoryPlan(plan); err != nil {
		return StoryPlan{}, err
	}
	if utf8.RuneCountInString(plan.Script) > 12000 || utf8.RuneCountInString(plan.Continuity) > 8000 {
		return StoryPlan{}, errors.New("generated script exceeded the storage limit")
	}
	for index := range plan.Scenes {
		plan.Scenes[index].VideoPrompt = strings.TrimSpace(plan.Scenes[index].VideoPrompt) + "\n\nContinuity bible — apply exactly in this scene:\n" + strings.TrimSpace(plan.Continuity)
		if utf8.RuneCountInString(plan.Scenes[index].VideoPrompt) > MaxPromptLength {
			return StoryPlan{}, fmt.Errorf("scene %d prompt exceeds %d characters after continuity rules", plan.Scenes[index].Number, MaxPromptLength)
		}
	}
	if err := appendContinuityBible(&plan); err != nil {
		return StoryPlan{}, err
	}
	return plan, nil
}
