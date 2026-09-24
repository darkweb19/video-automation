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

const (
	MaxScriptRawResponseBytes  = 1 << 20
	maxRandomPromptResponse    = 32 << 10
	maxRandomProjectTopicRunes = 280
	storyPlanCompletionTokens  = 6000
	storyGenerationTimeout     = 3 * time.Minute
	randomProjectTokens        = 1024
	randomSingleTokens         = 2048
)

const scriptSystemPrompt = `You are a short-form visual storyteller and video prompt director. Create a complete silent 30-second vertical video plan from the user's topic. The final video has exactly five sequential scenes, each exactly six seconds. There is no narration, dialogue, subtitles, music, logos, or on-screen text. Tell the story only through visible action.

Create one immutable continuity bible covering every recurring character's exact physical appearance and wardrobe, the visual style, color palette, lighting, camera language, time of day, and environment. Repeat the relevant continuity details verbatim inside every scene's video_prompt so each clip can be generated independently. Each video_prompt must describe only its six-second shot, start state, motion, camera movement, end state, vertical 9:16 composition, and continuity details. Avoid transitions that require footage from another scene.

Return a single JSON object with exactly these fields: title, story, script, continuity, scenes. Scenes must be an array of exactly five objects, numbered 1 through 5, each with number, title, script, and video_prompt. Return no commentary or Markdown.`

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
	return TextGenerationTrace{
		RouterModel:    ScriptModel,
		SystemPrompt:   scriptSystemPrompt,
		UserPrompt:     "Create the video plan for this topic or story idea:\n\n" + topic + "\n\nThe response must match this JSON schema:\n" + string(schema),
		ResponseSchema: string(schema),
		Status:         "request_building",
		UpdatedAt:      time.Now().Unix(),
	}, nil
}

// GenerateStoryPlan returns the normalized plan and auditable provider
// artifacts. Validation and continuity are separate processor stages.
func (c *OpenRouterClient) GenerateStoryPlan(ctx context.Context, topic string) (StoryPlan, TextGenerationTrace, error) {
	trace, err := newTextGenerationTrace(topic)
	if err != nil {
		return StoryPlan{}, trace, err
	}
	// Free routed models can spend significant time queued before producing a
	// complete five-scene plan. Keep the operation bounded, but do not apply the
	// shorter timeout used by ordinary OpenRouter metadata requests.
	requestContext, cancel := context.WithTimeout(ctx, storyGenerationTimeout)
	defer cancel()
	request := map[string]any{
		"model":                 ScriptModel,
		"messages":              []map[string]string{{"role": "system", "content": trace.SystemPrompt}, {"role": "user", "content": trace.UserPrompt}},
		"temperature":           0.4,
		"max_completion_tokens": storyPlanCompletionTokens,
	}
	trace.Status = "started"
	trace.StartedAt, trace.UpdatedAt = time.Now().Unix(), time.Now().Unix()
	fail := func(err error) (StoryPlan, TextGenerationTrace, error) {
		trace.Status, trace.Error = "failed", err.Error()
		trace.CompletedAt, trace.UpdatedAt = time.Now().Unix(), time.Now().Unix()
		return StoryPlan{}, trace, err
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return fail(fmt.Errorf("encode OpenRouter request: %w", err))
	}
	httpRequest, err := http.NewRequestWithContext(requestContext, http.MethodPost, c.baseURL()+"/chat/completions", bytes.NewReader(encoded))
	if err != nil {
		return fail(err)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+c.APIKey)
	httpRequest.Header.Set("Accept", "application/json")
	httpRequest.Header.Set("Content-Type", "application/json")
	httpResponse, err := c.storyClient().Do(httpRequest)
	if err != nil {
		return fail(fmt.Errorf("OpenRouter request: %w", err))
	}
	defer httpResponse.Body.Close()
	if httpResponse.StatusCode < 200 || httpResponse.StatusCode >= 300 {
		upstreamErr := readUpstreamError(httpResponse)
		trace.Status, trace.Error = "failed", upstreamErr.Error()
		trace.CompletedAt, trace.UpdatedAt = time.Now().Unix(), time.Now().Unix()
		return StoryPlan{}, trace, upstreamErr
	}
	body, err := io.ReadAll(io.LimitReader(httpResponse.Body, MaxScriptRawResponseBytes+1))
	if err != nil {
		return fail(fmt.Errorf("read OpenRouter response: %w", err))
	}
	if len(body) > MaxScriptRawResponseBytes {
		return fail(fmt.Errorf("OpenRouter script response exceeds %d bytes", MaxScriptRawResponseBytes))
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
		return fail(fmt.Errorf("decode OpenRouter response: %w", err))
	}
	trace.ActualModel = response.Model
	if len(response.Choices) == 0 || strings.TrimSpace(response.Choices[0].Message.Content) == "" {
		return fail(errors.New("OpenRouter returned an empty script"))
	}
	trace.RawResponse = response.Choices[0].Message.Content
	if len(trace.RawResponse) > MaxScriptRawResponseBytes {
		return fail(fmt.Errorf("OpenRouter raw script response exceeds %d bytes", MaxScriptRawResponseBytes))
	}
	plan, err := parseStoryPlanText(trace.RawResponse)
	if err != nil {
		return fail(err)
	}
	trace.Status = "completed"
	trace.CompletedAt, trace.UpdatedAt = time.Now().Unix(), time.Now().Unix()
	return plan, trace, nil
}

// Free text models can surround JSON with prose or a Markdown code fence.
// Decode complete JSON objects and accept only a valid five-scene plan.
func parseStoryPlanText(content string) (StoryPlan, error) {
	attempts := 0
	for offset, char := range content {
		if char != '{' {
			continue
		}
		attempts++
		if attempts > 32 {
			break
		}
		decoder := json.NewDecoder(strings.NewReader(content[offset:]))
		var plan StoryPlan
		if decoder.Decode(&plan) == nil && validateStoryPlan(plan) == nil {
			return plan, nil
		}
	}
	return StoryPlan{}, fmt.Errorf("OpenRouter did not return a valid JSON story plan with exactly %d scenes", ProjectSceneCount)
}

// GenerateRandomPrompt uses the same OpenRouter free-text route as project
// planning. It deliberately returns only usable browser text: the API key,
// routed model, and provider response remain server-side.
func (c *OpenRouterClient) GenerateRandomPrompt(ctx context.Context, input RandomPromptRequest) (string, error) {
	input.Category = strings.TrimSpace(input.Category)
	if !validRandomPromptCategory(input.Category) {
		return "", errors.New("unsupported prompt category")
	}
	if !validRandomPromptMode(input.Mode) {
		return "", errors.New("unsupported prompt mode")
	}

	systemPrompt, userPrompt, maxTokens := randomPromptInstructions(input.Mode, input.Category)
	request := map[string]any{
		"model":                 ScriptModel,
		"messages":              []map[string]string{{"role": "system", "content": systemPrompt}, {"role": "user", "content": userPrompt}},
		"temperature":           1,
		"max_completion_tokens": maxTokens,
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("encode OpenRouter random prompt request: %w", err)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL()+"/chat/completions", bytes.NewReader(encoded))
	if err != nil {
		return "", err
	}
	httpRequest.Header.Set("Authorization", "Bearer "+c.APIKey)
	httpRequest.Header.Set("Accept", "application/json")
	httpRequest.Header.Set("Content-Type", "application/json")
	httpResponse, err := c.client().Do(httpRequest)
	if err != nil {
		return "", fmt.Errorf("OpenRouter random prompt request: %w", err)
	}
	defer httpResponse.Body.Close()
	if httpResponse.StatusCode < 200 || httpResponse.StatusCode >= 300 {
		return "", readUpstreamError(httpResponse)
	}
	body, err := io.ReadAll(io.LimitReader(httpResponse.Body, maxRandomPromptResponse+1))
	if err != nil {
		return "", fmt.Errorf("read OpenRouter random prompt response: %w", err)
	}
	if len(body) > maxRandomPromptResponse {
		return "", errors.New("OpenRouter random prompt response exceeds the allowed size")
	}
	var response struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return "", fmt.Errorf("decode OpenRouter random prompt response: %w", err)
	}
	if len(response.Choices) == 0 {
		return "", errors.New("OpenRouter returned an empty random prompt")
	}
	prompt := strings.TrimSpace(response.Choices[0].Message.Content)
	if len(prompt) >= 2 && prompt[0] == '"' && prompt[len(prompt)-1] == '"' {
		prompt = strings.TrimSpace(prompt[1 : len(prompt)-1])
	}
	if prompt == "" {
		return "", errors.New("OpenRouter returned an empty random prompt")
	}
	if utf8.RuneCountInString(prompt) > MaxPromptLength {
		return "", fmt.Errorf("OpenRouter random prompt exceeds %d characters", MaxPromptLength)
	}
	if input.Mode == RandomPromptModeProject && utf8.RuneCountInString(prompt) > maxRandomProjectTopicRunes {
		return "", fmt.Errorf("OpenRouter random project topic exceeds %d characters", maxRandomProjectTopicRunes)
	}
	return prompt, nil
}

func randomPromptInstructions(mode RandomPromptMode, category string) (systemPrompt, userPrompt string, maxTokens int) {
	if mode == RandomPromptModeProject {
		return "You create original short-form video ideas. Return only one concise topic or story idea with no title, list, quotation marks, or explanation. It must be suitable for a silent 30-second vertical video with five connected six-second scenes. Keep every person clearly adult. Do not include brand names, logos, subtitles, or on-screen text.",
			"Generate one original topic in this category: " + category,
			randomProjectTokens
	}
	return "You are a production-ready text-to-video prompt writer. Return only one standalone prompt with no title, list, quotation marks, or explanation. Include a clearly adult subject, setting, visual style, lighting, camera movement, six-second visible action, ending frame, and vertical 9:16 composition. Do not include brand names, logos, subtitles, or on-screen text.",
		"Generate one original single-clip video prompt in this category: " + category,
		randomSingleTokens
}
