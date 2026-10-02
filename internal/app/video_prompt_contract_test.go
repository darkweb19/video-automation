package app

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestVideoPromptContractMetadataHasVersionedAssets(t *testing.T) {
	if storyPromptContractID != "framevault.story.v2" || randomPromptContractID != "framevault.random.v2" {
		t.Fatalf("prompt contract identifiers are not versioned: story=%q random=%q", storyPromptContractID, randomPromptContractID)
	}
	if videoPromptContractVersion == "" || videoPromptContractAuthor == "" || videoPromptContractChange == "" {
		t.Fatal("prompt contract metadata must include a version, author, and change note")
	}
}

func TestStoryPromptContractCoversAllCategoriesAndFixedStoryArc(t *testing.T) {
	cases := []struct {
		category string
		contains string
	}{
		{RandomPromptCategoryKidAnimation, "family-safe animation for children"},
		{RandomPromptCategoryHorrorStory, "restrained, atmospheric suspense"},
		{RandomPromptCategoryNature, "plausible for the real world"},
		{RandomPromptCategorySeduction, "tasteful, non-explicit adult fashion"},
		{RandomPromptCategoryMatureContent, "adult-audience dramatic tone"},
		{RandomPromptCategorySoftCorn, "tasteful, non-explicit adult romance or glamour only"},
	}
	for _, test := range cases {
		t.Run(test.category, func(t *testing.T) {
			prompt := storySystemPromptForCategory(test.category)
			if !strings.Contains(prompt, quotedJSON(test.category)) || !strings.Contains(prompt, test.contains) {
				t.Fatalf("story prompt omits category label or guidance: %q", prompt)
			}
			if !strings.Contains(prompt, "hook and setup") || !strings.Contains(prompt, "develop the action") || !strings.Contains(prompt, "decisive payoff") || !strings.Contains(prompt, "resolves the story") {
				t.Fatal("story prompt omits a required beat in the five-scene story arc")
			}
			if !strings.Contains(prompt, "untrusted source material") || !strings.Contains(prompt, "exactly five objects ordered and numbered 1 through 5") {
				t.Fatal("story prompt does not protect the schema from topic instructions")
			}
			if !strings.Contains(prompt, "exactly six seconds") || !strings.Contains(prompt, "480p in 9:16") || !strings.Contains(prompt, "no generated audio") || !strings.Contains(prompt, "slow forward camera movement") {
				t.Fatal("story prompt omits a fixed production constraint")
			}
			adultPrompt := strings.ToLower(prompt)
			if adultCategory(test.category) && (!strings.Contains(adultPrompt, "non-explicit") || !strings.Contains(adultPrompt, "do not create nudity") || !strings.Contains(adultPrompt, "sexual activity") || !strings.Contains(adultPrompt, "fetish framing") || !strings.Contains(adultPrompt, "eroticized camera focus")) {
				t.Fatal("adult category must require covered, non-explicit adult visuals")
			}
		})
	}
	if got := storySystemPromptForCategory("unknown"); got != scriptSystemPrompt {
		t.Fatal("unknown category must not inject arbitrary category instructions")
	}
}

func TestStoryPlanSchemaIsProviderCompatibleAndStrict(t *testing.T) {
	schema := storyPlanSchema()
	encoded, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("marshal story schema: %v", err)
	}
	var roundTrip map[string]any
	if err := json.Unmarshal(encoded, &roundTrip); err != nil {
		t.Fatalf("unmarshal story schema: %v", err)
	}
	if roundTrip["type"] != "object" || roundTrip["additionalProperties"] != false {
		t.Fatalf("story schema must be a closed JSON object: %#v", roundTrip)
	}
	properties := roundTrip["properties"].(map[string]any)
	for _, field := range []string{"title", "story", "script", "continuity", "scenes"} {
		if _, ok := properties[field]; !ok {
			t.Errorf("story schema is missing %q", field)
		}
	}
	if got, ok := properties["scenes"].(map[string]any); !ok || got["type"] != "array" || !strings.Contains(got["description"].(string), "Exactly 5 ordered scene objects") {
		t.Fatalf("story schema must describe an exact five-scene array: %#v", properties["scenes"])
	}
	required := roundTrip["required"].([]any)
	if len(required) != 5 {
		t.Fatalf("story schema required %d fields, want 5", len(required))
	}
	if strings.Contains(string(encoded), "maxLength") || strings.Contains(string(encoded), "minLength") || strings.Contains(string(encoded), "minItems") || strings.Contains(string(encoded), "maxItems") {
		t.Fatal("story schema should use provider-compatible structural constraints and enforce lengths/counts in Go")
	}
	if !strings.Contains(string(encoded), "at most 900 characters") || !strings.Contains(string(encoded), "at most 2200 characters") {
		t.Fatal("story schema descriptions must communicate runtime length limits")
	}
}

func TestRandomPromptInstructionsAndSchemaCoverEachCategory(t *testing.T) {
	categories := []string{
		RandomPromptCategoryKidAnimation,
		RandomPromptCategoryHorrorStory,
		RandomPromptCategoryNature,
		RandomPromptCategorySeduction,
		RandomPromptCategoryMatureContent,
		RandomPromptCategorySoftCorn,
	}
	for _, category := range categories {
		for _, mode := range []RandomPromptMode{RandomPromptModeProject, RandomPromptModeSingle} {
			t.Run(string(mode)+"/"+category, func(t *testing.T) {
				system, user, tokens := randomPromptInstructions(mode, category)
				if !strings.Contains(system, quotedJSON(category)) || !strings.Contains(system, storyCategoryGuidance(category)) {
					t.Fatal("random prompt instructions omit the selected category or its guidance")
				}
				if !strings.Contains(system, "exactly one JSON object") || !strings.Contains(system, "exactly one key, `prompt`") || !strings.Contains(user, "one JSON object with exactly one field") {
					t.Fatal("random prompt instructions do not require the exact response object")
				}
				if !strings.Contains(system, "physically achievable") || !strings.Contains(system, "vertical 9:16") {
					t.Fatal("random prompt instructions omit video production constraints")
				}
				if mode == RandomPromptModeProject && (!strings.Contains(system, "30-second") || !strings.Contains(system, "280 Unicode characters") || tokens != randomProjectTokens) {
					t.Fatal("project instructions omit the 30-second story or output-length contract")
				}
				if mode == RandomPromptModeSingle && (!strings.Contains(system, "standalone, silent six-second shot") || !strings.Contains(system, "4000 Unicode characters") || tokens != randomSingleTokens) {
					t.Fatal("single-clip instructions omit the single-shot or output-length contract")
				}
				schema := randomPromptSchema(RandomPromptRequest{Category: category, Mode: mode})
				if schema["additionalProperties"] != false || schema["type"] != "object" {
					t.Fatalf("random schema is not a closed object: %#v", schema)
				}
				props := schema["properties"].(map[string]any)
				if len(props) != 1 || props["prompt"].(map[string]any)["type"] != "string" {
					t.Fatalf("random schema must contain exactly the prompt string: %#v", props)
				}
				serialized, err := json.Marshal(schema)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(serialized), "maxLength") || strings.Contains(string(serialized), "minLength") {
					t.Fatal("random schema length constraints belong in Go validation for provider compatibility")
				}
			})
		}
	}
}

func TestParseRandomPromptContentAcceptsOnlyCompleteContract(t *testing.T) {
	input := RandomPromptRequest{Category: RandomPromptCategoryNature, Mode: RandomPromptModeProject}
	valid := `{"prompt":"A fox follows a glowing leaf through a dawn forest, then guides it home."}`
	got, err := parseRandomPromptContent(valid, input)
	if err != nil || got != "A fox follows a glowing leaf through a dawn forest, then guides it home." {
		t.Fatalf("parsed prompt = %q, error = %v", got, err)
	}

	bad := []struct {
		name    string
		content string
	}{
		{"empty", ""},
		{"malformed JSON", `{"prompt":`},
		{"markdown fence", "```json\n" + valid + "\n```"},
		{"prose before object", "Here is the prompt: " + valid},
		{"trailing data", valid + ` {"prompt":"second"}`},
		{"unknown property", `{"prompt":"A fox crosses a stream.","title":"Extra"}`},
		{"duplicate property", `{"prompt":"first","prompt":"second"}`},
		{"null object", `null`},
		{"null prompt", `{"prompt":null}`},
		{"missing prompt", `{}`},
		{"empty prompt", `{"prompt":"  "}`},
		{"non-string prompt", `{"prompt":42}`},
		{"refusal", `{"prompt":"I cannot help with this request."}`},
		{"curly apostrophe refusal", `{"prompt":"Sorry, I can’t assist with that."}`},
		{"analysis", `{"prompt":"Analysis: first I should reason through the request."}`},
		{"nested JSON value", `{"prompt":"{\"foo\":\"bar\"}"}`},
		{"nested list value", `{"prompt":"[\"first\",\"second\"]"}`},
		{"markdown value", "{\"prompt\":\"```A fox crosses a stream.```\"}"},
	}
	for _, test := range bad {
		t.Run(test.name, func(t *testing.T) {
			if value, err := parseRandomPromptContent(test.content, input); err == nil {
				t.Fatalf("accepted invalid response %q as %q", test.content, value)
			}
		})
	}
	if _, err := parseRandomPromptContent(valid, RandomPromptRequest{Category: input.Category, Mode: "other"}); err == nil {
		t.Fatal("accepted unsupported mode")
	}
	if _, err := parseRandomPromptContent(valid, RandomPromptRequest{Category: "other", Mode: input.Mode}); err == nil {
		t.Fatal("accepted unsupported category")
	}
}

func TestParseRandomPromptContentEnforcesRuneLimitsWithoutTruncation(t *testing.T) {
	cases := []struct {
		name  string
		mode  RandomPromptMode
		limit int
	}{
		{"project topic", RandomPromptModeProject, maxRandomProjectTopicRunes},
		{"single clip", RandomPromptModeSingle, MaxPromptLength},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			input := RandomPromptRequest{Category: RandomPromptCategoryNature, Mode: test.mode}
			within := strings.Repeat("🌿", test.limit)
			encoded, err := json.Marshal(map[string]string{"prompt": within})
			if err != nil {
				t.Fatal(err)
			}
			got, err := parseRandomPromptContent(string(encoded), input)
			if err != nil || utf8.RuneCountInString(got) != test.limit {
				t.Fatalf("limit-sized output has %d runes, error=%v", utf8.RuneCountInString(got), err)
			}
			over, err := json.Marshal(map[string]string{"prompt": within + "x"})
			if err != nil {
				t.Fatal(err)
			}
			if got, err := parseRandomPromptContent(string(over), input); err == nil {
				t.Fatalf("over-limit prompt was accepted or truncated to %d runes", utf8.RuneCountInString(got))
			}
		})
	}
	if _, err := parseRandomPromptContent(strings.Repeat("x", maxRandomPromptResponse+1), RandomPromptRequest{Category: RandomPromptCategoryNature, Mode: RandomPromptModeSingle}); err == nil {
		t.Fatal("accepted response larger than the raw response limit")
	}
}

func adultCategory(category string) bool {
	return category == RandomPromptCategorySeduction || category == RandomPromptCategoryMatureContent || category == RandomPromptCategorySoftCorn
}
