# Session handoff - 2026-10-02

## What was done
- Implemented Haiku text generation on local `feat/haiku-text-generation`, based on the existing YouTube feature branch/PR #6. This branch has not been pushed or deployed.
- Project story plans and random project ideas use `anthropic/claude-haiku-4.5` through the existing OpenRouter client and encrypted Settings key.
- Single random prompts try one available shuffled free model, then Haiku on failure, reserving at least 60 seconds of the 110-second operation budget for fallback.
- Added strict JSON schema requests, local validation, clear terminal errors, and separate free/account/paid-model cooldown handling. YouTube metadata stays free-only.
- Persisted optional project category with an additive migration; browser submission, worker loading, restart, and planning preserve category guidance.
- Added category-aware video prompt contracts, immutable v1/v2 records under `docs/prompts/`, routing ADR-002, updated README/AGENTS, and mocked regression tests. Terra and Luna worked at MAX; independent review findings were resolved.

## Decisions locked
- User explicitly superseded the former free-only story/random policy with Haiku for project text and single-prompt fallback.
- OpenRouter remains the only text integration; no additional Anthropic key or provider is required. General account limits are respected; free-tier limits do not block Haiku.
- Story planning retains two three-minute attempts within six minutes and five seconds. Invalid output never starts video work; five ordered six-second silent 480p scenes and final 1080x1920 assembly remain required.
- Categories guide family animation, suspense, plausible nature, and non-explicit adult visuals. Legacy projects without a category use general guidance.
- No paid provider calls, deployment, live upload, or push was performed. Text charges are separate from dashboard video cost totals.

## Open questions
- None for local implementation. Live Haiku output quality and production account credits remain unverified.

## Next steps
1. Review the local Haiku feature branch; it currently includes the underlying YouTube PR #6 changes, so account for that dependency before opening/merging a separate PR.
2. Ensure the saved OpenRouter key has credits, deploy the Go service, and run a budgeted live smoke test for project text and single-prompt fallback when authorized.
3. For the underlying YouTube/Modal rollout, deploy callback-compatible workers before the Go callback receiver and verify the public callback origin.

## Gotchas
- Verification passed: `go test ./... -count=1`, `go build ./...`, `go vet ./...`, `node --check internal/webui/static/app.js`, 28 dashboard tests, callback delivery checks, and `git diff --check`.
- Go's default AppData build cache is inaccessible in the sandbox. Set `GOCACHE` to this repo's ignored `.gocache` and `GOMODCACHE` to `.gomodcache` for local checks.
- Tests use synthetic fixtures and mock HTTP providers; contract regressions do not establish semantic story quality. No live A/B evaluation was performed.
- Existing untracked `workers/modal/__pycache__/` was left untouched and must not be staged.
- Never read or print `.env` or credentials; keep `/data/secret.key` with production backups. Manual browser integration and external gateway behavior remain unverified.
