package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	youtubeMetadataOperationTimeout = 110 * time.Second
	youtubeMetadataAttemptTimeout   = 35 * time.Second
	maxYouTubeMetadataResponse      = 1 << 20
	maxYouTubeMetadataSource        = 6000
)

type youtubeMetadataResponse struct {
	Title       string `json:"title"`
	Description string `json:"description"`
}

func (c *OpenRouterClient) generateYouTubeMetadata(ctx context.Context, source youtubeSource) (youtubeMetadataResponse, error) {
	state := c.promptState
	if state == nil {
		state = &randomPromptState{}
	}
	stateKey := randomPromptCredentialScope(c.APIKey, c.baseURL())
	operationCtx, cancel := context.WithTimeout(ctx, youtubeMetadataOperationTimeout)
	defer cancel()
	if err := waitForRandomPromptPlatformCooldown(operationCtx, state, stateKey); err != nil {
		return youtubeMetadataResponse{}, err
	}
	models := append([]string(nil), randomPromptModels[:]...)
	rand.Shuffle(len(models), func(i, j int) { models[i], models[j] = models[j], models[i] })
	cooldowns := state.cooldownSnapshot(stateKey, time.Now())
	available := make([]string, 0, len(models))
	var earliest time.Time
	for _, model := range models {
		until := randomPromptModelCooldownUntil(cooldowns.models, model)
		if until.After(time.Now()) {
			if earliest.IsZero() || until.Before(earliest) {
				earliest = until
			}
			continue
		}
		available = append(available, model)
	}
	if len(available) == 0 {
		if !earliest.IsZero() {
			return youtubeMetadataResponse{}, allRandomPromptModelsRateLimited(earliest)
		}
		return youtubeMetadataResponse{}, errors.New("free OpenRouter text models are unavailable")
	}

	var lastErr error
	rateLimited := 0
	for index, model := range available {
		if err := operationCtx.Err(); err != nil {
			return youtubeMetadataResponse{}, err
		}
		if err := waitForRandomPromptPlatformCooldown(operationCtx, state, stateKey); err != nil {
			return youtubeMetadataResponse{}, err
		}
		budget := randomPromptAttemptBudget(operationCtx, len(available)-index)
		if budget > youtubeMetadataAttemptTimeout {
			budget = youtubeMetadataAttemptTimeout
		}
		if budget <= 0 {
			return youtubeMetadataResponse{}, context.DeadlineExceeded
		}
		attemptCtx, cancelAttempt := context.WithTimeout(operationCtx, budget)
		result, err := c.generateYouTubeMetadataAttempt(attemptCtx, source, model)
		cancelAttempt()
		if err == nil {
			return result, nil
		}
		lastErr = err
		var upstream *upstreamError
		if errors.As(err, &upstream) && upstream.StatusCode == http.StatusTooManyRequests {
			if upstream.RateLimitScope == "platform" {
				state.setPlatformCooldown(stateKey, upstream, time.Now())
				if upstream.RetryAt.IsZero() {
					return youtubeMetadataResponse{}, err
				}
				if waitErr := waitForRandomPromptPlatformCooldown(operationCtx, state, stateKey); waitErr != nil {
					return youtubeMetadataResponse{}, err
				}
				continue
			}
			rateLimited++
			until := upstream.RetryAt
			if !until.After(time.Now()) {
				until = time.Now().Add(defaultModelCooldown)
			}
			state.setModelCooldown(stateKey, randomPromptModelCooldownKey(model, upstream), until, time.Now())
		}
		if !randomPromptShouldRetry(err) {
			return youtubeMetadataResponse{}, err
		}
		if operationCtx.Err() != nil {
			return youtubeMetadataResponse{}, operationCtx.Err()
		}
		if index+1 < len(available) && randomPromptShouldPauseBeforeRetry(err) {
			if waitErr := waitRandomPromptRetryPause(operationCtx); waitErr != nil {
				return youtubeMetadataResponse{}, waitErr
			}
		}
	}
	if rateLimited == len(available) {
		return youtubeMetadataResponse{}, allRandomPromptModelsRateLimited(time.Time{})
	}
	if lastErr != nil {
		return youtubeMetadataResponse{}, lastErr
	}
	return youtubeMetadataResponse{}, errors.New("free OpenRouter text models are unavailable")
}

func (c *OpenRouterClient) generateYouTubeMetadataAttempt(ctx context.Context, source youtubeSource, model string) (youtubeMetadataResponse, error) {
	sourceTitle := truncateYouTubeSource(source.Title, maxYouTubeMetadataSource)
	topic := truncateYouTubeSource(source.Topic, maxYouTubeMetadataSource)
	story := truncateYouTubeSource(source.Story, maxYouTubeMetadataSource)
	prompt := truncateYouTubeSource(source.Prompt, maxYouTubeMetadataSource)
	userPrompt, err := json.Marshal(map[string]string{
		"source_title":  sourceTitle,
		"topic":         topic,
		"visual_story":  story,
		"source_prompt": prompt,
	})
	if err != nil {
		return youtubeMetadataResponse{}, err
	}
	requestBody := map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": "Create a concise YouTube title and description based only on the source details. Do not invent claims, names, dates, or events. The title must be at most 100 Unicode characters. The description must be at most 5000 UTF-8 bytes. Do not use angle brackets, HTML, Markdown fences, or unsupported hashtags. Return one JSON object with exactly title and description string fields."},
			{"role": "user", "content": string(userPrompt)},
		},
		"temperature":           0.4,
		"max_completion_tokens": 700,
	}
	encoded, err := json.Marshal(requestBody)
	if err != nil {
		return youtubeMetadataResponse{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL()+"/chat/completions", bytes.NewReader(encoded))
	if err != nil {
		return youtubeMetadataResponse{}, err
	}
	request.Header.Set("Authorization", "Bearer "+c.APIKey)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	response, err := c.storyClient().Do(request)
	if err != nil {
		return youtubeMetadataResponse{}, errors.New("OpenRouter metadata request failed")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxYouTubeMetadataResponse+1))
	if err != nil || len(body) > maxYouTubeMetadataResponse {
		return youtubeMetadataResponse{}, errors.New("OpenRouter metadata response was unreadable")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return youtubeMetadataResponse{}, parseOpenRouterError(body, response.StatusCode, response.Header)
	}
	var completion struct {
		Choices []struct {
			Message struct {
				Content json.RawMessage `json:"content"`
				Refusal json.RawMessage `json:"refusal"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &completion); err != nil {
		return youtubeMetadataResponse{}, errors.New("OpenRouter metadata response was invalid")
	}
	for _, choice := range completion.Choices {
		if hasRandomPromptRefusal(choice.Message.Refusal) || randomPromptFinishIsUnusable(strings.ToLower(strings.TrimSpace(choice.FinishReason))) {
			continue
		}
		content := strings.TrimSpace(openRouterMessageText(choice.Message.Content))
		var result youtubeMetadataResponse
		decoder := json.NewDecoder(strings.NewReader(content))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&result) != nil {
			continue
		}
		if decoder.Decode(&struct{}{}) != io.EOF {
			continue
		}
		result.Title = strings.TrimSpace(result.Title)
		result.Description = strings.TrimSpace(result.Description)
		if validateYouTubeTitle(result.Title) != nil || validateYouTubeDescription(result.Description) != nil {
			continue
		}
		return result, nil
	}
	return youtubeMetadataResponse{}, errors.New("OpenRouter returned no usable YouTube metadata")
}

func truncateYouTubeSource(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) <= maxRunes {
		return value
	}
	runes := []rune(value)
	return string(runes[:maxRunes])
}

type youtubeMetadataRequest struct {
	SourceKind string `json:"source_kind"`
	SourceID   string `json:"source_id"`
}

func (a *dashboardApp) generateYouTubeMetadata(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) {
		return
	}
	if !isJSON(r.Header.Get("Content-Type")) {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	var input youtubeMetadataRequest
	if decodeJSONBody(w, r, &input) != nil {
		return
	}
	if input.SourceKind != "generation" && input.SourceKind != "project" || input.SourceID == "" {
		writeError(w, http.StatusBadRequest, "source_kind and source_id are required")
		return
	}
	unlock := a.lockYouTubeSourceAccess()
	source, ok := a.requireYouTubeSource(w, r, input.SourceKind, input.SourceID)
	if !ok {
		unlock()
		return
	}
	identity, sessionHash, ok := metadataSession(a.security, r)
	if !ok {
		unlock()
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	unlock()
	provider, err := a.openRouterTextProvider()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Configure an OpenRouter API key in Settings to generate metadata")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), youtubeMetadataOperationTimeout)
	metadata, err := provider.generateYouTubeMetadata(ctx, source)
	cancel()
	if err != nil {
		writeError(w, http.StatusBadGateway, "Free OpenRouter text models could not generate metadata. Enter a title and description manually.")
		return
	}
	unlock = a.lockYouTubeSourceAccess()
	defer unlock()
	currentIdentity, currentSessionHash, sessionOK := metadataSession(a.security, r)
	if !sessionOK || currentIdentity.Username != identity.Username || currentSessionHash != sessionHash {
		writeError(w, http.StatusUnauthorized, "Your dashboard session expired while metadata was generated")
		return
	}
	currentSource, ok := a.requireYouTubeSource(w, r, input.SourceKind, input.SourceID)
	if !ok {
		return
	}
	if currentSource.Path != source.Path || currentSource.InVault != source.InVault || currentSource.Size != source.Size || currentSource.ModTimeNS != source.ModTimeNS {
		writeError(w, http.StatusConflict, "The source video changed while metadata was generated. Reopen upload details and try again.")
		return
	}
	writeJSON(w, http.StatusOK, metadata)
}

func metadataSession(security *Security, r *http.Request) (SessionIdentity, string, bool) {
	if security == nil {
		return SessionIdentity{}, "", false
	}
	identity, ok := security.Session(r)
	if !ok || identity.MustChangePassword {
		return SessionIdentity{}, "", false
	}
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return SessionIdentity{}, "", false
	}
	return identity, tokenHash(cookie.Value), true
}
