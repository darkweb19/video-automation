package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

const MaxScriptRawResponseBytes = 1 << 20

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

func newTextGenerationTrace(topic string) (TextGenerationTrace, error) {
	topic = strings.TrimSpace(topic)
	if topic == "" {
		return TextGenerationTrace{}, errors.New("topic is required")
	}
	schema, err := json.Marshal(storyPlanSchema())
	if err != nil {
		return TextGenerationTrace{}, fmt.Errorf("encode story plan schema: %w", err)
	}
	return TextGenerationTrace{RouterModel: ScriptModel, SystemPrompt: scriptSystemPrompt, UserPrompt: "Create the video plan for this topic or story idea:\n\n" + topic, ResponseSchema: string(schema), Status: "request_building", UpdatedAt: time.Now().Unix()}, nil
}

// GenerateStoryPlan returns the normalized plan and auditable provider
// artifacts. Validation and continuity are separate processor stages.
func (c *OpenRouterClient) GenerateStoryPlan(ctx context.Context, topic string) (StoryPlan, TextGenerationTrace, error) {
	trace, err := newTextGenerationTrace(topic)
	if err != nil {
		return StoryPlan{}, trace, err
	}
	request := map[string]any{
		"model":           ScriptModel,
		"messages":        []map[string]string{{"role": "system", "content": trace.SystemPrompt}, {"role": "user", "content": trace.UserPrompt}},
		"temperature":     0.7,
		"response_format": map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "short_video_plan", "strict": true, "schema": storyPlanSchema()}},
		"provider":        map[string]any{"require_parameters": true},
	}
	trace.Status = "started"
	trace.StartedAt, trace.UpdatedAt = time.Now().Unix(), time.Now().Unix()
	fail := func(message string) (StoryPlan, TextGenerationTrace, error) {
		trace.Status, trace.Error = "failed", message
		trace.CompletedAt, trace.UpdatedAt = time.Now().Unix(), time.Now().Unix()
		return StoryPlan{}, trace, errors.New(message)
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return fail(fmt.Sprintf("encode OpenRouter request: %v", err))
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL()+"/chat/completions", bytes.NewReader(encoded))
	if err != nil {
		return fail(err.Error())
	}
	httpRequest.Header.Set("Authorization", "Bearer "+c.APIKey)
	httpRequest.Header.Set("Accept", "application/json")
	httpRequest.Header.Set("Content-Type", "application/json")
	httpResponse, err := c.client().Do(httpRequest)
	if err != nil {
		return fail(fmt.Sprintf("OpenRouter request: %v", err))
	}
	defer httpResponse.Body.Close()
	if httpResponse.StatusCode < 200 || httpResponse.StatusCode >= 300 {
		return fail(readUpstreamError(httpResponse).Error())
	}
	body, err := io.ReadAll(io.LimitReader(httpResponse.Body, MaxScriptRawResponseBytes+1))
	if err != nil {
		return fail(fmt.Sprintf("read OpenRouter response: %v", err))
	}
	if len(body) > MaxScriptRawResponseBytes {
		return fail(fmt.Sprintf("OpenRouter script response exceeds %d bytes", MaxScriptRawResponseBytes))
	}
	var response struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return fail(fmt.Sprintf("decode OpenRouter response: %v", err))
	}
	trace.ActualModel = response.Model
	if len(response.Choices) == 0 || strings.TrimSpace(response.Choices[0].Message.Content) == "" {
		return fail("OpenRouter returned an empty script")
	}
	trace.RawResponse = response.Choices[0].Message.Content
	if len(trace.RawResponse) > MaxScriptRawResponseBytes {
		return fail(fmt.Sprintf("OpenRouter raw script response exceeds %d bytes", MaxScriptRawResponseBytes))
	}
	var plan StoryPlan
	if err := json.Unmarshal([]byte(trace.RawResponse), &plan); err != nil {
		return fail(fmt.Sprintf("decode generated script: %v", err))
	}
	trace.Status = "completed"
	trace.CompletedAt, trace.UpdatedAt = time.Now().Unix(), time.Now().Unix()
	return plan, trace, nil
}
