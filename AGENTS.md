# FrameVault Video Automation — agent context

Read the root `SESSION.md` at the start of a task when it exists. Use `README.md` for user-facing setup and workflows. Never read or print `.env` contents or credentials.

## Architecture

- This is a Go HTTP service. `cmd/video-automation/main.go` calls `internal/app.Run()`; keep routes, providers, processing, persistence, and security in the cohesive `internal/app` package while they share application types and lifecycle.
- `internal/webui/` embeds the dashboard assets from `internal/webui/static/`; preserve the existing `/static/...` paths and keep browser API calls same-origin and authenticated.
- `workers/modal/video.py` (Wan) and `workers/modal/skyreels.py` (SkyReels) are separate Modal apps. OpenRouter remains responsible for project story plans and random text prompts.
- `internal/app/storage.go` and `project_storage.go` own SQLite state and additive migrations. `docs/architecture/` records accepted architecture decisions.

## Workflow and provider invariants

- Single clips use the selected video provider. A project is always five sequential six-second scenes at 480p, 9:16, with no generated audio; OpenRouter creates and validates the story plan regardless of video provider. FFmpeg joins the five scenes into the final silent 1080×1920 video.
- Treat video capabilities and prices as provider runtime data. Validate duration, resolution, aspect ratio, and audio against `VideoModel`; do not hard-code provider model lists or pricing into browser code.
- Story planning and random project ideas use catalog-verified `anthropic/claude-haiku-4.5` through OpenRouter. Story planning allows at most two three-minute attempts within six minutes and five seconds, retaining strict schema and five-scene validation on retries. Single-clip random prompts try one available model from the shuffled explicit free list for at most 35 seconds, then fall back to Haiku on any error within the 110-second total budget. Caller cancellation stops all attempts. Free-model quota cooldowns must not block paid Haiku; account-wide limits still apply. Persist the optional selected project category and apply category guidance to planning. Verify models against the live catalog before changing pins; do not add other paid models or a local fallback. YouTube metadata remains free-only.
- Persist state before asynchronous provider work. Keep immutable provider credential snapshots, retry counts, clear terminal failures, and project scene retries that preserve successful scenes.
- OpenRouter video uses status polling. New Modal jobs use stable job IDs and authenticated callbacks at `/api/video-callbacks/{job_id}`; persist callback state before dispatch and let the watchdog retry idempotent submissions or report a callback timeout. Keep legacy callback and sparse bounded recovery for migration and active jobs; classify permanent worker callback rejections without weakening bearer authentication or repeating inference, and do not add frequent Modal polling.
- Roll out a worker version with legacy and new callback support before deploying the matching Go callback receiver. Do not submit new Modal jobs until both are deployed.
- Keep provider-specific API payloads and response rules in provider clients, not the dashboard or processor.

## State, media, and updates

- Production state and media require one persistent volume mounted at `/data`. Keep SQLite, `secret.key`, scene clips, and finished videos together; independent local volumes do not share job state.
- Stream media to files, sync and atomically rename downloads, and pass stored paths to FFmpeg. Serve media from files with range support; authorize before evaluating conditional requests. Do not buffer whole videos in Go memory.
- Keep the five-second processor scan cheap when queues are empty. Project worker scans use `projectForWorker`; full text traces and pipeline audit records belong in `Store.Project` and detail APIs.
- The authenticated EventSource is the live update channel. Skip publish work when no client is connected, omit large raw script responses from list/live snapshots, and keep meaningful status, cost, error, trace, and pipeline changes.
- Preserve the current local-media cache and Vault authorization rules. Vault media is `no-store`; CSP must allow `blob:` for Vault player downloads. Media remains on the app volume; object storage is not part of the selected architecture.
- Keep final assembly at 1080×1920, medium preset, CRF 20. Limit concurrent project combining and FFmpeg threads as already implemented; do not change output quality as an unapproved cost shortcut.

## Security and operations

- API keys and callback bearer tokens are encrypted at rest and never returned to the browser or logged. Keep `/data/secret.key` with backups. Authenticate same-origin browser routes; never disable authorization to fix a route.
- `OPENROUTER_API_KEY` is an optional first-start bootstrap. Configure normal provider credentials in authenticated Settings. Set `VIDEO_CALLBACK_BASE_URL` to a public HTTPS origin for new Modal callbacks; `PUBLIC_BASE_URL` is only a compatibility fallback.
- Build the container from the root `Dockerfile`; it builds `./cmd/video-automation` and listens on fixed container port `8080`. Compose `APP_PORT` affects only the host-side binding. Health check: `GET /health`.
- Do not run paid provider generations in tests. Routine local checks: `go test ./...`, `node --check internal/webui/static/app.js`, `python3 workers/modal/callback_delivery_test.py`, and `git diff --check`.
