# ADR-002: Haiku story planning and single-prompt fallback

**Status:** Accepted by explicit user request

**Date:** 2026-10-02

## Context

Free OpenRouter text models can time out, return incomplete output, or exhaust shared free-tier quotas. The user requested Claude Haiku 4.5 for 30-second project text and as automatic fallback for single-clip random prompts. This supersedes the former free-only story/random policy. OpenRouter remains the text provider; existing encrypted Settings credentials remain in use.

## Decision

- Project stories and project random ideas use `anthropic/claude-haiku-4.5` directly.
- Single random prompts try one eligible shuffled free model, then Haiku once on any failure. A cancelled caller never starts fallback. The total remains 110 seconds with at most 35 seconds spent on the free attempt.
- Free quotas and free-model cooldowns are isolated from account-wide limits. Known free exhaustion skips directly to Haiku; generic account limits keep their reset behavior.
- Haiku uses strict `response_format` JSON schemas and provider `require_parameters`. All model output also passes Go validation. Random output is exactly `{prompt:string}` internally, with the existing browser response preserved. Invalid output produces a clear failure rather than starting video work.
- Persist the optional project category before asynchronous planning. Legacy projects with no category use general story instructions. Preserve five six-second silent 480p scenes, continuity, and final 1080×1920 assembly.
- YouTube metadata continues to use free text models only.

## Alternatives

Keeping free-only text would retain $0 text inference charges but does not meet the requested story reliability or fallback behavior. Adding a direct Anthropic client would require another credential and duplicate provider handling. Repeatedly trying all four free models would consume the fallback deadline. The selected approach reuses the existing client and credential.

## Cost and operational consequences

Haiku is paid. The catalog on 2026-10-02 reports $1 per million input tokens and $5 per million output tokens. Cost is `(input_tokens × 1 + output_tokens × 5) / 1,000,000` USD per request, excluding optional provider features. A story can make two bounded requests. A project random idea makes one Haiku request; a single random prompt makes at most one Haiku fallback request. Actual token counts and future workload are unknown, so no monthly or three-year spend estimate is asserted. Text charges are currently separate from video totals.

Rollback is a code change to the prior text policy; additive category storage remains compatible with old projects. No database or media deletion is required. No Modal worker changes are needed for this text-only feature. No deployment or paid request is part of verification.

## Prompt contracts and verification

Production prompt contracts live in `internal/app/video_prompt_contract.go`, with explicit version identifiers and change notes. Categories guide animation, suspense, nature, and non-explicit adult visuals. Story instructions prescribe hook, setup, development, payoff, and resolution with standalone shots and immutable continuity. Topic input is treated as data and cannot override format or shot constraints.

Mock HTTP tests verify model selection, timeout fallback, cancellation, refusal, malformed output, quotas, schema requests, and caller deadlines. Category tests verify migration, restart, worker loading, and the actual planning request. These are contract regressions; they do not establish semantic story quality. Live A/B comparisons against the prior model and prompt need separately budgeted provider calls. Test content is synthetic fixture data.

Sources: [OpenRouter model catalog](https://openrouter.ai/api/v1/models), [structured outputs](https://openrouter.ai/docs/guides/features/structured-outputs), [Haiku pricing](https://openrouter.ai/anthropic/claude-haiku-4.5/api).
