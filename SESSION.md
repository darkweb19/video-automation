# Session handoff — 2026-09-28

## What was done
- Added `workers/modal/skyreels.py`, adapted from `darkweb19/skyreel-modal` source commit `a7676dc46a7f4084552cdbe80bc93de6c02a9add`; the source repository was read only.
- Matched the existing Go Modal client: authenticated model discovery, asynchronous submission, durable polling, backend-readable errors, and MP4 content with byte ranges.
- Added atomic job metadata and separate invocation-reference files; startup failures surface through polling, transient transport failures stay pending, and successful dispatch remains accepted if reference storage fails.
- Added worker deployment documentation and nine offline API regression tests.
- Verified nine Python tests, Python syntax, real Modal SDK import/decorators, `go test ./...`, `go vet ./...`, `node --check static/app.js`, and diff checks.
- Used Terra and Luna at max effort for implementation and tests; the main thread guided and reviewed their work.

## Decisions locked
- All changes belong to `video-automation`; preserve the upstream repository and the existing `modal/video.py` worker.
- Configure the new deployment through Settings > Modal accounts with a base URL ending in `/api/v1`; no backend or dashboard changes are needed.
- Keep story/script/random-prompt generation on OpenRouter. The upstream file hosts SkyReels video inference, with no separate text LLM endpoint.
- Keep the project preset at five six-second 480p 9:16 scenes; SkyReels advertises compatible capabilities and no audio.
- Report measured GPU runtime only; leave USD cost and catalog pricing unavailable rather than inventing a per-job price.

## Open questions
- None for the code scope.

## Next steps
1. Run deployment preflight, then deploy `workers/modal/skyreels.py` in the intended Modal account using `workers/modal/README.md`.
2. Add its `/api/v1` endpoint and matching key in Modal accounts, test the connection, and refresh the model list.
3. After checking credits, verify GPU inference and playback with an explicitly authorized live generation.

## Gotchas
- No cloud deployment or paid inference ran; API tests use local ASGI requests and fake Modal dispatch/storage calls.
- SkyReels uses separate app/cache/jobs volume names; its secret is `video-api-secret` with `MODAL_VIDEO_API_KEY`.
- Local Go checks need `GOCACHE` set to the workspace `.gocache` because the default cache was inaccessible.
- The pre-existing untracked `modal/__pycache__/` directory was left untouched.
- The shared checkout changed externally to `feat/apple-monochrome-dashboard` during commit creation; adapter commits are on that branch, and unrelated dashboard edits were excluded.
- Never read or print `.env` contents or credentials; credentials remain encrypted in existing application settings.
