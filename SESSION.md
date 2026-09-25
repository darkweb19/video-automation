# Session handoff — 2026-09-25

## What was done
- Set the OpenRouter script model for random prompts and 30-second projects to `inclusionai/ling-3.0-flash-fin:free`.
- Requested the five-scene plan through a forced tool call, with plain-text parsing as a fallback; kept the requested and actual model in the project trace.
- Bounded generated continuity and scene text, preserved the six-second 9:16 shot instructions, and avoided duplicate continuity in final prompts.
- Kept incompatible video models disabled after project submission and surfaced a specific continuity-length failure.
- Updated README and added regression coverage for model routing, tool responses, prompt length, and failure messages.
- Verified `go test ./...`, `go vet ./...`, `node --check static/app.js`, and `git diff --check`.
- Committed changes as `d40a0a6` and `fa2d393` on `feat/modal-llm-implementation`.

## Decisions locked
- OpenRouter handles random prompts and story/script generation; the selected persisted video provider remains behind `VideoService`.
- The 30-second project remains five six-second, 480p, 9:16 scenes, with compatibility determined from runtime `VideoModel` data.
- Provider credentials remain encrypted at rest and absent from browser settings responses.
- Paid video generation is not a routine verification step.

## Open questions
1. Sujan: after deployment, does a new 30-second project complete? If it fails, share its Pipeline and Text generation status and error, without credentials.
2. Sujan: rotate the OpenRouter key noted as previously exposed in the prior handoff before live testing.

## Next steps
1. Deploy the pushed `feat/modal-llm-implementation` branch through the normal release path.
2. Retry a 30-second project and inspect its saved pipeline if it fails.

## Gotchas
- The local SQLite database has no project records, so the exact reported production failure stage could not be confirmed.
- No paid provider request or live generation was made during verification.
- Docker was unavailable locally; the Go tests exercise the changed request and processor paths with mocks.
