# Session handoff — 2026-09-24

## What was done
- Fixed OpenRouter prompt and 30-second story generation in `script_generation.go` using `max_completion_tokens`, larger completion budgets, and the JSON schema inside the free-text prompt.
- Preserved `openrouter/free` free-text parsing; no structured-output requirement was added.
- Preserved the 30-second preset: exactly five 6-second scenes at 480p and 9:16.
- Preserved wrapped timeout/cancellation errors so project failures show actionable messages.
- Added safe prompt/story errors for timeout, invalid key, rate limiting, and upstream HTTP failures.
- Fixed project polling and background refresh so active projects continue updating across mode/navigation changes.
- Changed projects with missing persisted provider snapshots from waiting forever to a clear failed state.
- Added focused regression tests for OpenRouter requests, timeout propagation, prompt errors, and missing provider snapshots.
- Verification passed: `go test ./...`, `go vet ./...`, `node --check static/app.js`, and `git diff --check`.
- Committed the workflow fixes as `ba69d8e`, `1b4b893`, and `799cd91`.

## Decisions locked
- Story and random-prompt generation use OpenRouter `openrouter/free` with free-text parsing.
- Provider-specific video handling remains behind `VideoService` and persisted provider snapshots.
- Runtime `VideoModel` capabilities determine compatibility; the project preset remains five 6-second 480p 9:16 scenes.
- Credentials stay encrypted at rest and are never returned to the browser or logged.
- Paid/live provider generation is not part of routine verification.

## Open questions
1. Sujan: rotate the previously exposed OpenRouter API key before any live provider testing.
2. Sujan: confirm visual behavior in the dashboard once an in-app browser is available.

## Next steps
1. Review the three workflow-fix commits and deploy through the project's normal release path.
2. After key rotation, optionally run a controlled live prompt/project test; do not run paid generation casually.
3. Confirm visual behavior in the dashboard when an in-app browser is available.

## Gotchas
- No paid or live provider request was made during this fix.
- The in-app browser was unavailable, so visual/runtime browser QA remains pending.
- Local Go verification needs a writable `GOCACHE`; the temporary workspace cache was removed after checks passed.
- Git may warn that the user-level global ignore file is inaccessible; it did not affect verification.
