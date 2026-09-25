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
	"unicode"
	"unicode/utf8"
)

const (
	MaxScriptRawResponseBytes  = 1 << 20
	maxRandomPromptResponse    = 32 << 10
	maxRandomProjectTopicRunes = 280
	storyPlanCompletionTokens  = 6000
	storyGenerationTimeout     = 3 * time.Minute
	maxGeneratedContinuity     = 900
	maxGeneratedScenePrompt    = 2200
	randomProjectTokens        = 1024
	randomSingleTokens         = 2048
	randomPromptTimeout        = 105 * time.Second
)

const scriptSystemPrompt = `You are a short-form visual storyteller and video prompt director. Create a complete silent 30-second vertical video plan from the user's topic. The final video has exactly five sequential scenes, each exactly six seconds. There is no narration, dialogue, subtitles, music, logos, or on-screen text. Tell the story only through visible action.

Create one immutable continuity bible covering every recurring character's exact physical appearance and wardrobe, the visual style, color palette, lighting, camera language, time of day, and environment. Keep the continuity bible within 900 characters. Repeat the relevant continuity details verbatim inside every scene's video_prompt so each clip can be generated independently. Keep each video_prompt within 2200 characters. Each video_prompt must describe only its six-second shot, start state, visible motion, slow forward camera movement, end state, vertical 9:16 composition, and continuity details. Avoid transitions that require footage from another scene.

Return a single JSON object with exactly these fields: title, story, script, continuity, scenes. Scenes must be an array of exactly five objects, numbered 1 through 5, each with number, title, script, and video_prompt. Return no commentary or Markdown.`

func storyPlanSchema() map[string]any {
	scene := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"number":       map[string]any{"type": "integer", "minimum": 1, "maximum": ProjectSceneCount},
			"title":        map[string]any{"type": "string"},
			"script":       map[string]any{"type": "string", "description": "Visible action during this six-second scene."},
			"video_prompt": map[string]any{"type": "string", "maxLength": maxGeneratedScenePrompt, "description": "Standalone six-second vertical 9:16 video prompt with visible motion, slow forward camera movement, start and end states, and repeated continuity details."},
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
			"continuity": map[string]any{"type": "string", "maxLength": maxGeneratedContinuity, "description": "Immutable character, wardrobe, environment, palette, lighting, style, and camera bible."},
			"scenes": map[string]any{
				"type": "array", "minItems": ProjectSceneCount, "maxItems": ProjectSceneCount, "items": scene,
			},
		},
		"required":             []string{"title", "story", "script", "continuity", "scenes"},
		"additionalProperties": false,
	}
}

const continuityPromptSuffix = "\n\nContinuity bible - apply exactly in this scene:\n"
const shotContractSuffix = "\n\nShot requirements: exactly six seconds; vertical 9:16 framing. Motion: show visible action progressing from the described start state to the end state. Camera movement: make a slow forward push toward the action throughout the shot."

const minSceneActionRunes = 40

// appendContinuityBible normalizes scene prompts at the storage boundary. The
// provider response is not the only possible source of a plan, so persisted
// prompts must always carry the same continuity bible and remain within the
// provider's prompt limit.
func appendContinuityBible(plan *StoryPlan) error {
	continuity := strings.TrimSpace(plan.Continuity)
	if continuity == "" {
		return errors.New("script plan is missing continuity rules")
	}
	maxSceneRunes := MaxPromptLength - utf8.RuneCountInString(shotContractSuffix+continuityPromptSuffix+continuity)
	for index := range plan.Scenes {
		prompt := strings.TrimSpace(plan.Scenes[index].VideoPrompt)
		prompt = strings.TrimSpace(strings.ReplaceAll(prompt, shotContractSuffix, ""))
		if marker := strings.Index(prompt, "\n\nContinuity bible"); marker >= 0 {
			prompt = strings.TrimSpace(prompt[:marker])
		}
		if maxSceneRunes < minSceneActionRunes {
			return fmt.Errorf("scene %d prompt exceeds %d characters after continuity rules: continuity bible leaves insufficient room for scene action", plan.Scenes[index].Number, MaxPromptLength)
		}
		prompt = strings.TrimSpace(strings.ReplaceAll(prompt, continuity, ""))
		if prompt == "" {
			return fmt.Errorf("scene %d prompt has no action after continuity rules", plan.Scenes[index].Number)
		}
		plan.Scenes[index].VideoPrompt = fitSceneAction(prompt, maxSceneRunes) + shotContractSuffix + continuityPromptSuffix + continuity
	}
	return nil
}

// Keep the start state and ending frame when a verbose model response needs
// shortening. Slicing runes avoids corrupting multi-byte scene descriptions.
func fitSceneAction(prompt string, maxRunes int) string {
	runes := []rune(prompt)
	if len(runes) <= maxRunes {
		return prompt
	}
	const separator = " … "
	available := maxRunes - len([]rune(separator))
	front := available * 2 / 3
	back := available - front
	return strings.TrimSpace(string(runes[:front])) + separator + strings.TrimSpace(string(runes[len(runes)-back:]))
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
	// Free models can spend significant time queued before producing a
	// complete five-scene plan. Keep the operation bounded, but do not apply the
	// shorter timeout used by ordinary OpenRouter metadata requests.
	requestContext, cancel := context.WithTimeout(ctx, storyGenerationTimeout)
	defer cancel()
	request := map[string]any{
		"model":                 ScriptModel,
		"messages":              []map[string]string{{"role": "system", "content": trace.SystemPrompt}, {"role": "user", "content": trace.UserPrompt}},
		"temperature":           0.4,
		"max_completion_tokens": storyPlanCompletionTokens,
		"tools": []map[string]any{{
			"type": "function",
			"function": map[string]any{
				"name":        storyPlanToolName,
				"description": "Submit the complete five-scene video plan.",
				"parameters":  storyPlanSchema(),
			},
		}},
		"tool_choice": map[string]any{"type": "function", "function": map[string]string{"name": storyPlanToolName}},
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
			Message json.RawMessage `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return fail(fmt.Errorf("decode OpenRouter response: %w", err))
	}
	trace.ActualModel = response.Model
	if len(response.Choices) == 0 {
		return fail(errors.New("OpenRouter returned an empty script"))
	}
	plan, rawResponse, err := parseStoryPlanChoice(response.Choices[0].Message)
	trace.RawResponse = rawResponse
	if len(trace.RawResponse) > MaxScriptRawResponseBytes {
		return fail(fmt.Errorf("OpenRouter raw script response exceeds %d bytes", MaxScriptRawResponseBytes))
	}
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
	// Free text models may queue on the first request. The ordinary metadata
	// client times out after 45 seconds, before this endpoint's request budget.
	httpResponse, err := c.storyClient().Do(httpRequest)
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
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return "", fmt.Errorf("decode OpenRouter random prompt response: %w", err)
	}
	var prompt string
	for _, choice := range response.Choices {
		prompt = strings.TrimSpace(openRouterMessageText(choice.Message.Content))
		if prompt != "" {
			break
		}
	}
	if prompt == "" {
		return "", errors.New("OpenRouter returned no text in its random prompt choices")
	}
	if len(prompt) >= 2 && prompt[0] == '"' && prompt[len(prompt)-1] == '"' {
		prompt = strings.TrimSpace(prompt[1 : len(prompt)-1])
	}
	if input.Mode == RandomPromptModeProject {
		prompt = fitRandomProjectTopic(prompt)
	} else if utf8.RuneCountInString(prompt) > MaxPromptLength {
		return "", fmt.Errorf("OpenRouter random prompt exceeds %d characters", MaxPromptLength)
	}
	return prompt, nil
}

// OpenRouter normally returns message.content as a string, but compatible
// providers may return an array of text blocks. Read both forms and ignore
// non-text blocks without exposing the raw upstream response to the browser.
func openRouterMessageText(content json.RawMessage) string {
	var text string
	if json.Unmarshal(content, &text) == nil {
		return text
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, block := range blocks {
		if block.Text != "" && (block.Type == "" || block.Type == "text") {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "")
}

// Keep a verbose free-model response usable as a project topic. Prefer a word
// boundary, while preserving the random project topic's 280-character limit.
func fitRandomProjectTopic(prompt string) string {
	runes := []rune(prompt)
	if len(runes) <= maxRandomProjectTopicRunes {
		return prompt
	}
	limit := maxRandomProjectTopicRunes - 1 // reserve one rune for the ellipsis
	cut := limit
	for cut > limit/2 && !unicode.IsSpace(runes[cut]) {
		cut--
	}
	if cut <= limit/2 {
		cut = limit
	}
	return strings.TrimSpace(string(runes[:cut])) + "…"
}

func randomPromptInstructions(mode RandomPromptMode, category string) (systemPrompt, userPrompt string, maxTokens int) {
	if category == RandomPromptCategoryMatureContent {
		if mode == RandomPromptModeProject {
			return "You create bold, sensual short-form video story ideas for an adult audience. Every person must be clearly 25 or older. Make the idea itself unmistakably sensual and visually revealing while remaining non-explicit: center it on a confident adult woman in a daring two-piece, bikini, or exotic lingerie look, with flirtatious posing, curves, cleavage, a striking bare back, or suggestive dancing such as a playful hip sway or twerk. Specify the outfit and sensual action in the idea so a later video script preserves them across scenes. Rotate the featured look and action; don't make every idea a robe, quiet glance, or generic elegant lounge scene. Keep breasts and buttocks covered by opaque clothing, with no nudity, visible nipples, genitalia, sexual activity, or fetish framing. Return only one concise topic or story idea in at most 200 characters, with no title, list, quotation marks, or explanation. It must suit a silent 30-second vertical video told in five connected six-second scenes. Do not include brand names, logos, subtitles, or on-screen text.",
				"Generate one original bold, sensual, non-explicit adult video idea in this category: " + category,
				randomProjectTokens
		}
		return "You are a production-ready text-to-video prompt writer for bold, sensual adult content. Every person must be clearly 25 or older. Write an unmistakably erotic, visually revealing but non-explicit prompt: favor a confident adult woman in a daring two-piece, bikini, or exotic lingerie, emphasizing her curves and fuller bust through opaque clothing, a striking bare back, teasing poses, flirtatious eye contact, and sensual movement such as a hip sway or twerk. Rotate settings, outfits, camera angles, and actions; avoid tame, generic scenes. Keep breasts and buttocks covered by opaque clothing, with no nudity, visible nipples, genitalia, sexual activity, or fetish framing. Return only one standalone prompt with setting, visual style, lighting, camera movement, six-second visible action, ending frame, and vertical 9:16 composition. Do not include brand names, logos, subtitles, or on-screen text.",
			"Generate one original bold, sensual, non-explicit adult single-clip prompt in this category: " + category,
			randomSingleTokens
	}
	if mode == RandomPromptModeProject {
		return "You create original short-form video ideas. Return only one concise topic or story idea in at most 200 characters, with no title, list, quotation marks, or explanation. It must be suitable for a silent 30-second vertical video with five connected six-second scenes. Keep every person clearly adult. Do not include brand names, logos, subtitles, or on-screen text.",
			"Generate one original topic in this category: " + category,
			randomProjectTokens
	}
	return "You are a production-ready text-to-video prompt writer. Return only one standalone prompt with no title, list, quotation marks, or explanation. Include a clearly adult subject, setting, visual style, lighting, camera movement, six-second visible action, ending frame, and vertical 9:16 composition. Do not include brand names, logos, subtitles, or on-screen text.",
		"Generate one original single-clip video prompt in this category: " + category,
		randomSingleTokens
}
