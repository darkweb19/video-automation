# Session handoff — 2026-10-05

## What was done
- Fixed 30-second idea failures by retrying incomplete, invalid, and transient Haiku responses once within the existing request deadline; strict JSON and character validation remain enforced.
- Added durable, idempotent project submissions keyed by optional `request_id`, including exact lookup, replay conflict handling, and deletion tombstones.
- The dashboard saves the exact project request in the current tab and restores accepted work by ID after refresh; uncertain retries reuse the same request.
- Documented refresh, tab-close, History, random-idea, and `/data` restart behavior in README; added the versioned random-project prompt v3 asset.
- Created 15 focused conventional commits and pushed them to `feat/youtube-upload-and-generation-reliability`.

## Decisions locked
- Project ideas use catalog-verified Haiku through OpenRouter; invalid or temporary output gets one correction attempt, while fatal account errors and refusals stop.
- An optional request ID replays the same project for the same options; changed options conflict. Exact lookup remains authenticated and respects Vault visibility.
- Same-tab refresh recovery uses session storage. App restart recovery requires the persistent `/data` volume.
- Projects remain five ordered six-second silent 480p scenes assembled to silent 1080×1920 video.
- No live paid generation or deployment was performed.

## Open questions
1. Live Haiku output quality and production account credits remain unverified; run a budgeted smoke test only when authorized.

## Next steps
1. Review the updated branch in PR #6.
2. After merge and authorization, deploy and run a budgeted live smoke test for project ideas and refresh recovery.
3. For Modal callback rollout, deploy callback-compatible workers before the Go callback receiver and verify the public callback origin.

## Gotchas
- Verification passed: Go tests, build, vet, callback delivery, JavaScript syntax, 43/43 dashboard UI tests, and `git diff --check`; independent review reported no blocking findings.
- Go's default AppData build cache is inaccessible in the sandbox; use repository-local `.gocache` and `.gomodcache` for local checks.
- Tests use synthetic prompt fixtures and mocked providers; they do not establish live Haiku quality or external gateway behavior.
- `workers/modal/__pycache__/` is untracked and was left untouched; do not stage it.
- Never read or print `.env` or credentials. Keep `/data/secret.key` with production backups.
