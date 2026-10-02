package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
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
	randomPromptTimeout        = 110 * time.Second
	randomPromptMaxAttemptTime = 35 * time.Second
	randomPromptMinAttemptTime = 12 * time.Second
	randomPromptRetryPause     = 100 * time.Millisecond
)

// Keep the fallback list explicitly free so random prompt generation cannot
// route to a paid text model. Availability can change; failed variants are
// skipped within the request budget.
var randomPromptModels = [...]string{
	"nvidia/nemotron-3.5-lightning:free",
	"qwen/qwen3.8-27b:free",
	"nvidia/nemotron-3-nano-omni-30b-a3b-reasoning:free",
	"liquid/lfm-2.5-2.6b:free",
}

var errRandomPromptUnusable = errors.New("OpenRouter returned unusable random prompt content")

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
	operationCtx, cancelOperation := context.WithTimeout(ctx, randomPromptTimeout)
	defer cancelOperation()
	state := c.promptState
	if state == nil {
		// Keep direct/test clients independent without mutating a shared client.
		state = &randomPromptState{}
	}
	stateKey := randomPromptCredentialScope(c.APIKey, c.baseURL())
	if err := waitForRandomPromptPlatformCooldown(operationCtx, state, stateKey); err != nil {
		return "", err
	}
	cooldowns := state.cooldownSnapshot(stateKey, time.Now())

	models := append([]string(nil), randomPromptModels[:]...)
	rand.Shuffle(len(models), func(i, j int) {
		models[i], models[j] = models[j], models[i]
	})

	var lastErr error
	var rateLimitedModels int
	var earliestRateLimit time.Time
	var remainingModels []string
	for _, model := range models {
		if cooldownUntil := randomPromptModelCooldownUntil(cooldowns.models, model); cooldownUntil.After(time.Now()) {
			rateLimitedModels++
			if earliestRateLimit.IsZero() || cooldownUntil.Before(earliestRateLimit) {
				earliestRateLimit = cooldownUntil
			}
			continue
		}
		remainingModels = append(remainingModels, model)
	}
	if len(remainingModels) == 0 {
		if rateLimitedModels > 0 {
			return "", allRandomPromptModelsRateLimited(earliestRateLimit)
		}
		return "", errors.New("OpenRouter free text models are unavailable")
	}

	for modelIndex, model := range remainingModels {
		if err := operationCtx.Err(); err != nil {
			return "", err
		}
		platformRetries := 0
		for {
			if err := waitForRandomPromptPlatformCooldown(operationCtx, state, stateKey); err != nil {
				return "", err
			}
			latestCooldowns := state.cooldownSnapshot(stateKey, time.Now())
			if cooldownUntil := randomPromptModelCooldownUntil(latestCooldowns.models, model); cooldownUntil.After(time.Now()) {
				rateLimitedModels++
				if earliestRateLimit.IsZero() || cooldownUntil.Before(earliestRateLimit) {
					earliestRateLimit = cooldownUntil
				}
				break
			}
			attemptTimeout := randomPromptAttemptBudget(operationCtx, len(remainingModels)-modelIndex)
			if attemptTimeout <= 0 {
				if err := operationCtx.Err(); err != nil {
					return "", err
				}
				return "", context.DeadlineExceeded
			}
			attemptCtx, cancelAttempt := context.WithTimeout(operationCtx, attemptTimeout)
			prompt, err := c.generateRandomPromptAttempt(attemptCtx, input, model, systemPrompt, userPrompt, maxTokens)
			cancelAttempt()
			if err == nil {
				return prompt, nil
			}
			if err := operationCtx.Err(); err != nil {
				return "", err
			}
			lastErr = err

			var upstream *upstreamError
			if errors.As(err, &upstream) && upstream.StatusCode == http.StatusTooManyRequests {
				if upstream.RateLimitScope == "platform" {
					state.setPlatformCooldown(stateKey, upstream, time.Now())
					platformRetries++
					if platformRetries == 1 && upstream.RetryAt.After(time.Now()) {
						if waitErr := waitForRandomPromptPlatformCooldown(operationCtx, state, stateKey); waitErr == nil {
							continue
						} else {
							return "", waitErr
						}
					}
					return "", err
				}

				rateLimitedModels++
				cooldownKey := randomPromptModelCooldownKey(model, upstream)
				cooldownUntil := upstream.RetryAt
				if !cooldownUntil.After(time.Now()) {
					cooldownUntil = time.Now().Add(defaultModelCooldown)
				}
				state.setModelCooldown(stateKey, cooldownKey, cooldownUntil, time.Now())
				cooldowns.models[cooldownKey] = cooldownUntil
				if earliestRateLimit.IsZero() || cooldownUntil.Before(earliestRateLimit) {
					earliestRateLimit = cooldownUntil
				}
			}
			if !randomPromptShouldRetry(err) {
				return "", err
			}
			if randomPromptShouldPauseBeforeRetry(err) {
				if waitErr := waitRandomPromptRetryPause(operationCtx); waitErr != nil {
					return "", waitErr
				}
			}
			break
		}
	}
	if rateLimitedModels >= len(models) {
		return "", allRandomPromptModelsRateLimited(earliestRateLimit)
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", errors.New("OpenRouter free text models are unavailable")
}

func randomPromptAttemptBudget(ctx context.Context, remainingAttempts int) time.Duration {
	if remainingAttempts < 1 {
		return 0
	}
	remaining := randomPromptTimeout
	if deadline, ok := ctx.Deadline(); ok {
		remaining = time.Until(deadline)
	}
	if remaining <= 0 {
		return 0
	}
	budget := remaining / time.Duration(remainingAttempts)
	if budget > randomPromptMaxAttemptTime {
		budget = randomPromptMaxAttemptTime
	}
	if budget < randomPromptMinAttemptTime && remaining > randomPromptMinAttemptTime {
		// Preserve a useful queue window when the caller supplied a slightly
		// shorter deadline than the normal 110-second operation budget.
		budget = randomPromptMinAttemptTime
	}
	if budget > remaining {
		return remaining
	}
	return budget
}

func randomPromptModelCooldownUntil(cooldowns map[string]time.Time, model string) time.Time {
	var latest time.Time
	for _, key := range randomPromptModelCooldownKeys(model) {
		if until := cooldowns[key]; until.After(latest) {
			latest = until
		}
	}
	return latest
}

func waitForRandomPromptPlatformLimit(ctx context.Context, retryAt time.Time, minAttempt time.Duration) error {
	if retryAt.IsZero() || !retryAt.After(time.Now()) {
		return errors.New("OpenRouter platform rate limit did not include a future reset time")
	}
	deadline, ok := ctx.Deadline()
	if ok && retryAt.Add(minAttempt).After(deadline) {
		return context.DeadlineExceeded
	}
	delay := time.Until(retryAt)
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func waitForRandomPromptPlatformCooldown(ctx context.Context, state *randomPromptState, scopeKey string) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		snapshot := state.cooldownSnapshot(scopeKey, time.Now())
		if snapshot.platformUntil.IsZero() {
			return nil
		}
		if snapshot.platformError != nil && snapshot.platformError.RetryAt.IsZero() {
			return snapshot.platformError
		}
		if err := waitForRandomPromptPlatformLimit(ctx, snapshot.platformUntil, randomPromptMinAttemptTime); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if snapshot.platformError != nil {
				return snapshot.platformError
			}
			return err
		}
		// Another request may have extended the shared reset while this request
		// was waiting, so refresh the state before sending any model request.
	}
}

func allRandomPromptModelsRateLimited(retryAt time.Time) error {
	return &upstreamError{
		StatusCode:     http.StatusTooManyRequests,
		Message:        "all configured free OpenRouter text models are temporarily rate-limited",
		RetryAt:        retryAt,
		RateLimitScope: "provider",
		LimitSource:    "free model availability",
	}
}

func (c *OpenRouterClient) generateRandomPromptAttempt(ctx context.Context, input RandomPromptRequest, model, systemPrompt, userPrompt string, maxTokens int) (string, error) {
	request := map[string]any{
		"model":                 model,
		"messages":              []map[string]string{{"role": "system", "content": systemPrompt}, {"role": "user", "content": userPrompt}},
		"temperature":           1,
		"max_completion_tokens": maxTokens,
		"reasoning": map[string]any{
			"effort":  "minimal",
			"exclude": true,
		},
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
	body, err := io.ReadAll(io.LimitReader(httpResponse.Body, maxRandomPromptResponse+1))
	if err != nil {
		return "", fmt.Errorf("read OpenRouter random prompt response: %w", err)
	}
	if upstreamErr := parseOpenRouterError(body, httpResponse.StatusCode, httpResponse.Header); upstreamErr != nil {
		return "", upstreamErr
	}
	if len(body) > maxRandomPromptResponse {
		return "", errors.New("OpenRouter random prompt response exceeds the allowed size")
	}
	var response struct {
		Choices []struct {
			Message struct {
				Content   json.RawMessage `json:"content"`
				Refusal   json.RawMessage `json:"refusal"`
				Reasoning json.RawMessage `json:"reasoning"`
			} `json:"message"`
			FinishReason       string `json:"finish_reason"`
			NativeFinishReason string `json:"native_finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return "", fmt.Errorf("decode OpenRouter random prompt response: %w", err)
	}
	var prompt string
	for _, choice := range response.Choices {
		finishReason := strings.ToLower(strings.TrimSpace(choice.FinishReason))
		nativeFinishReason := strings.ToLower(strings.TrimSpace(choice.NativeFinishReason))
		if hasRandomPromptRefusal(choice.Message.Refusal) || randomPromptFinishIsUnusable(finishReason) || randomPromptFinishIsUnusable(nativeFinishReason) {
			continue
		}
		candidate := strings.TrimSpace(openRouterMessageText(choice.Message.Content))
		var reasoning string
		_ = json.Unmarshal(choice.Message.Reasoning, &reasoning)
		if reasoning = strings.TrimSpace(reasoning); reasoning != "" && candidate == reasoning {
			continue
		}
		if len(candidate) >= 2 && candidate[0] == '"' && candidate[len(candidate)-1] == '"' {
			candidate = strings.TrimSpace(candidate[1 : len(candidate)-1])
		}
		candidate = stripRandomPromptReasoning(candidate)
		if candidate == "" || isRandomPromptRefusal(candidate) || isMalformedRandomPrompt(candidate) {
			continue
		}
		prompt = candidate
		break
	}
	if prompt == "" {
		return "", fmt.Errorf("%w: no complete plain prompt in the response choices", errRandomPromptUnusable)
	}
	if input.Mode == RandomPromptModeProject {
		prompt = fitRandomProjectTopic(prompt)
	} else if utf8.RuneCountInString(prompt) > MaxPromptLength {
		return "", fmt.Errorf("%w: prompt exceeds %d characters", errRandomPromptUnusable, MaxPromptLength)
	}
	return prompt, nil
}

func randomPromptFinishIsUnusable(reason string) bool {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "length", "max_tokens", "token_limit", "content_filter", "error", "failed", "refusal", "content_policy_violation":
		return true
	default:
		return false
	}
}

func hasRandomPromptRefusal(refusal json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(refusal))
	return trimmed != "" && trimmed != "null" && trimmed != `""`
}

func isMalformedRandomPrompt(prompt string) bool {
	trimmed := strings.TrimSpace(prompt)
	return strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, "```")
}

func randomPromptShouldRetry(err error) bool {
	if errors.Is(err, errRandomPromptUnusable) {
		return true
	}
	var upstream *upstreamError
	if errors.As(err, &upstream) {
		if upstream.StatusCode == http.StatusTooManyRequests {
			return upstream.RateLimitScope != "platform"
		}
		if upstream.StatusCode == http.StatusForbidden && (strings.EqualFold(strings.TrimSpace(upstream.ErrorType), "content_policy_violation") || strings.EqualFold(strings.TrimSpace(upstream.ErrorType), "refusal")) {
			return true
		}
		switch status := upstream.StatusCode; {
		case status == http.StatusNotFound:
			// The model may have been removed since this free variant was listed.
			return true
		case status == http.StatusRequestTimeout,
			status == http.StatusConflict,
			status == http.StatusTooEarly,
			status == http.StatusTooManyRequests,
			status >= http.StatusInternalServerError:
			return true
		default:
			// Authentication and other client errors need a key or request fix.
			return false
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	// Transport, body-read, and malformed-response failures may be isolated to
	// one routed model, so move to the next explicitly free variant.
	return true
}

func randomPromptShouldPauseBeforeRetry(err error) bool {
	if errors.Is(err, errRandomPromptUnusable) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var upstream *upstreamError
	if errors.As(err, &upstream) && upstream.StatusCode == http.StatusTooManyRequests {
		return false
	}
	return true
}

func waitRandomPromptRetryPause(ctx context.Context) error {
	timer := time.NewTimer(randomPromptRetryPause)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func stripRandomPromptReasoning(prompt string) string {
	trimmed := strings.TrimSpace(prompt)
	for _, tag := range []string{"think", "analysis", "reasoning"} {
		lower := strings.ToLower(trimmed)
		openTag, closeTag := "<"+tag+">", "</"+tag+">"
		if start := strings.Index(lower, openTag); start >= 0 {
			end := strings.Index(lower[start+len(openTag):], closeTag)
			if end < 0 {
				return ""
			}
			trimmed = strings.TrimSpace(trimmed[start+len(openTag)+end+len(closeTag):])
		}
	}
	lower := strings.ToLower(trimmed)
	for _, prefix := range []string{"reasoning:", "analysis:", "let me think", "we need to", "the user asks"} {
		if strings.HasPrefix(lower, prefix) {
			return ""
		}
	}
	return trimmed
}

func isRandomPromptRefusal(prompt string) bool {
	lower := strings.ToLower(strings.TrimSpace(prompt))
	for _, prefix := range []string{
		"i cannot help",
		"i cannot assist",
		"i cannot provide",
		"i can't help",
		"i can't assist",
		"i can't provide",
		"i am unable to help",
		"i'm unable to help",
		"i am unable to provide",
		"i'm unable to provide",
		"i am unable to assist",
		"i'm unable to assist",
		"sorry, i cannot",
		"sorry, i can't",
		"sorry, i can’t",
		"sorry, i am unable",
		"sorry, i'm unable",
		"sorry, i’m unable",
	} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
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
