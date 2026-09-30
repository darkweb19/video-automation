# Session handoff — 2026-09-30

## What was done
- Ported random-prompt generation to five shuffled OpenRouter `:free` models, with 12-second per-model attempts and a 70-second total budget.
- Added prompt-generation tests for model fallback, retry classes, deadlines, free-only routing, and output validation.
- Ported durable Modal callback jobs into the current `internal/app` layout: state is saved before submission, callback tokens are encrypted, and single clips and five-scene projects receive ordered authenticated completion or error events without normal provider polling.
- Updated both Modal workers, `README.md`, `AGENTS.md`, and worker operations guidance for the new callback protocol and staged worker-first rollout.
- Local verification passed: Go tests, vet, build, dashboard UI tests, Wan callback delivery checks, SkyReels offline tests, JavaScript syntax, and `git diff --check`.

## Decisions locked
- Random prompts never fall back to a paid model; auth and invalid-request failures stop immediately.
- New Modal callback setup uses `VIDEO_CALLBACK_BASE_URL`; `PUBLIC_BASE_URL` remains a compatibility fallback.
- Deploy callback-capable Modal workers with legacy support before the Go callback receiver; submit new Modal jobs after both sides are updated.

## Open questions
1. Live rollout validation remains pending; it needs a publicly reachable HTTPS dashboard and explicit authorization for a deployment or provider generation.

## Next steps
1. Push the reviewed feature branch and open the pull request.
2. Deploy callback-capable workers, configure `VIDEO_CALLBACK_BASE_URL`, then deploy the matching Go receiver before submitting new Modal jobs.
3. Verify live generation only after the user explicitly authorizes a deployment and paid provider call.

## Gotchas
- Two Modal apps are supplied: Wan 2.2 in `workers/modal/video.py` and SkyReels V2 in `workers/modal/skyreels.py`; each endpoint must be configured separately.
- Legacy callback jobs use the old route during migration; new jobs use stable IDs at `/api/video-callbacks/{job_id}` with bearer authentication.
- No live Modal job, paid provider generation, or deployment was run.
