# Video generation prompt contract v2

**Version:** 2.0.0

**Prompt IDs:** `framevault.story.v2`, `framevault.random.v2`

**Author:** Luna

**Date:** 2026-10-02

**Change note:** Add category-aware video direction, a complete five-scene story arc, and strict structured JSON contracts with application-side validation.

**Production source:** `internal/app/video_prompt_contract.go`

The story contract creates five ordered, silent six-second scenes in a vertical 9:16, 480p video. It prescribes hook and setup, development and obstacle, payoff, and resolution; standalone shots; achievable motion; and immutable visual continuity. Topic text is treated as untrusted source material and cannot override the schema or production constraints.

The random contract covers all six current categories: Kid Animation, Horror Story, Nature, Seduction, Mature Content, and Soft Corn. Adult categories require clearly adult subjects and tasteful, covered, non-explicit visuals. The exact response is one JSON object containing only a non-empty `prompt` string. Project ideas are capped at 280 Unicode characters; single-clip prompts are capped at the existing 4000-character prompt limit. Go rejects malformed, extra-field, refusal, analysis, and over-limit output without truncation.

Verification uses offline Go contract fixtures for each category, the story schema, malformed JSON, refusals, and rune limits. No live provider generations or semantic-quality claims are part of this prompt version. Any future behavior change receives a new immutable prompt ID and dated record; this file remains the v2 record.
