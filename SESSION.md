# Session handoff ? 2026-10-01

## What was done
- Committed OpenRouter prompt/story reliability fixes as `ccb8f0f` on `feat/video-callbacks-and-random-prompts`.
- Random prompts retain five shuffled free models, with adaptive attempts capped at 35 seconds inside a 110-second request deadline. Unusable and incomplete responses move to another model.
- Added shared credential-scoped cooldowns, OpenRouter/provider limit classification, retry/reset parsing, HTTP-200 error handling, and safe public errors with retry guidance.
- Project planning stays on ScriptModel: two three-minute attempts within six minutes and five seconds; invalid output or unsupported tool routing retries with plain JSON. Five-scene validation remains required.
- Preserved bounded invalid story output for diagnosis with credential redaction; attempt counts persist through an additive SQLite migration and detail/live APIs.
- Committed Modal supervisor cap/test alignment as `14a30d0`; both workers now use the supported maximum of 10 retries.
- Go tests, vet, build, dashboard UI tests, JavaScript syntax, Wan callback checks, SkyReels offline tests, and diff checks passed. Final agent review found no material issues.

## Decisions locked
- User chose OpenRouter only: no local/template generator and no paid fallback.
- There is no application usage quota. OpenRouter's shared quota cannot be removed by model/key rotation; known reset times are respected. Missing reset hints receive a 30-second internal suppression without inventing a public reset time.
- Random-prompt deadline changes from 70/12 seconds to 110/adaptive seconds are intentional and documented in AGENTS.md and README.md.
- Deploy callback-capable Modal workers before the Go callback receiver; keep authenticated callbacks and legacy compatibility.

## Open questions
1. Live rollout verification remains pending; no deployment or live provider generation was run.

## Next steps
1. Deploy the reviewed feature branch when authorized, following the worker-first callback rollout in README.md.
2. Verify random prompts and project story planning against live OpenRouter after deployment; inspect saved trace attempts and invalid output if a provider still fails.

## Gotchas
- OpenRouter free-tier account limits still apply across models/API keys; long resets return actionable errors rather than waiting indefinitely.
- Cooldowns are shared by the HTTP dashboard and processor through Store and reset on app restart.
- Local Go checks used the writable TEMP build cache. Race-detector checks were not run (no C toolchain); no live API calls were used in tests.
- Pre-existing untracked `workers/modal/__pycache__/` was left untouched and excluded from commits.
- Keep `/data/secret.key` with backups and preserve callback worker-first deployment ordering.
