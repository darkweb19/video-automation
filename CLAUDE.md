# FrameVault Video Automation

FrameVault is a Go HTTP application with an embedded dashboard. SQLite holds settings, jobs, projects, and workflow history. Generated clips and final videos live on persistent local storage. OpenRouter generates story plans and scripts; the selected video provider generates clips. The optional Modal worker is deployed separately.

`AGENTS.md` is the authoritative detailed guide for architecture, storage and cost decisions, security invariants, operations, and routine checks. Keep its file map aligned with this guide when files or responsibilities change.

## Repository structure and key files

The executable entrypoint is under `cmd/`; the private backend package and Go tests are under `internal/app/`. `internal/webui/` embeds the browser dashboard, `workers/modal/` contains the separate Python worker, and `docs/` holds session notes and architecture decisions.

| File or folder | Responsibility |
| --- | --- |
| `cmd/video-automation/main.go` | Small executable entrypoint that calls `internal/app.Run()`. |
| `internal/app/run.go` | Opens the data store and security state, serves HTTP on port 8080, and starts the background processor. |
| `internal/app/dashboard.go` | Dashboard app shell, shared route registration, and health endpoint. |
| `internal/app/dashboard_auth.go` | Authentication guards, sessions, password handling, and request throttles. |
| `internal/app/dashboard_providers.go` | Provider setup, model capabilities, and immutable credential snapshots. |
| `internal/app/dashboard_generation.go` | Generation submission, status, callback, history, and random-prompt routes. |
| `internal/app/dashboard_projects.go` | Project lifecycle, scene retry, and project API routes. |
| `internal/app/dashboard_media.go` | Local media playback, downloads, caching, and deletion routes. |
| `internal/app/dashboard_settings.go` | Settings and account-management routes. |
| `internal/app/handlers.go` | Shared provider-facing generation validation and streamed video responses. |
| `internal/app/models.go` | Core generation types, `VideoService` interfaces, provider identifiers, model capabilities, and workflow constants. |
| `internal/app/openrouter.go`, `internal/app/modal.go` | Provider-specific HTTP requests, response normalization, and content fetching. |
| `internal/app/script_generation.go`, `internal/app/story_plan_choice.go` | OpenRouter story/script requests, parsing, and story-plan validation. |
| `internal/app/processor.go` | Processor setup, five-second work loop, job/project scans, status polling, and Modal generation recovery. |
| `internal/app/processor_projects.go` | Project story planning, scene submission and recovery, and coordination of final assembly. |
| `internal/app/processor_downloads.go` | Streaming scene and generation downloads, atomic file storage, and retry handling. |
| `internal/app/processor_tasks.go` | Bounded background task coordination for project work and FFmpeg assembly. |
| `internal/app/storage.go`, `internal/app/project_storage.go` | SQLite schema, additive migrations, settings, state transitions, worker reads, paths, retries, and pipeline events. |
| `internal/app/combiner.go` | FFmpeg command construction and file-based final assembly. |
| `internal/app/events.go` | Authenticated server-sent events for job and project updates. |
| `internal/app/security.go`, `internal/app/vault.go`, `internal/app/modal_callback.go` | Encrypted settings and sessions, Vault authorization, and authenticated Modal callbacks. |
| `internal/webui/embed.go`, `internal/webui/static/` | Embedded browser assets; `app.js` calls the same-origin authenticated API and keeps the existing `/static/...` paths. |
| `workers/modal/video.py` | Deployable Modal generation worker and callback delivery. |
| `workers/modal/callback_delivery_test.py` | Dependency-free regression checks for Modal callback delivery and redirect handling. |
| `internal/app/*_test.go` | Unit, HTTP handler, storage, security, workflow recovery, and provider-client tests. |
| `docs/architecture/001-go-service-layout.md`, `docs/SESSION.md` | Accepted Go service layout decision and project session notes. |
| `Dockerfile`, `docker-compose.yml` | Production container build and local Compose stack. |
| `README.md` | Setup, provider configuration, storage, backup, development, and deployment instructions. |

Keep the backend in one cohesive `internal/app` package while routes, providers, processor, storage, and security share application types and lifecycle. This keeps implementation details private and avoids exporting package internals just to divide files; split packages when a stable dependency boundary appears. The command package should remain a small startup wrapper.

There is no Railway manifest in the repository. Railway builds from the root `Dockerfile`.

## Component patterns

- Keep provider-specific API rules inside each provider client. Implement video providers behind `VideoService` and use the content-provider interface for streamed media.
- Treat model capabilities as runtime data. Validate duration, resolution, aspect ratio, audio, and pricing through `VideoModel` fields.
- The default project preset is five six-second scenes at 480p and 9:16. Story planning always uses OpenRouter, regardless of the selected video provider.
- Persist state before asynchronous provider work. The processor resumes polling, downloads, retries, and assembly from SQLite after restart. Preserve credential snapshots, attempt counters, terminal failures, and scene-level retry behavior.
- `PendingProjects` must use `projectForWorker` for processor-only columns. Keep full traces and pipeline history in `Store.Project` for detail/API callers.
- OpenRouter video jobs use processor status polling. Modal completion uses authenticated callbacks; delayed status checks only recover missed callbacks or restarts.
- Stream large provider responses to a temporary file, sync it, and atomically rename it. Pass stored paths to FFmpeg and serve local media with range support.
- Keep browser requests same-origin and authenticated. Check current access before evaluating media cache validators. Use the existing authenticated `EventSource` for live updates.
- Encrypt credentials at rest. Never send credentials to the browser or logs, and never read or print `.env` contents.

## Deployment and local development

- Build Railway deployments from the repository root with `Dockerfile`, which builds `./cmd/video-automation` and keeps the runtime binary at `/app/video-automation`. Attach a persistent volume at `/data` for SQLite, `secret.key`, downloaded clips, and project output. Run one replica against that local state.
- The application binds fixed port `8080`; configure the service target port accordingly. The app does not read Railway's `PORT` or Compose's host-side `APP_PORT`.
- Set `PUBLIC_BASE_URL` to the public HTTPS dashboard origin when using Modal callbacks. Check deployment health at `GET /health`.
- Start local Compose with `docker compose up -d --build`. For native Go development, set `DATA_DIR` to a writable directory and run `go run ./cmd/video-automation`; FFmpeg must be installed. Generate a local password recovery code with `go run ./cmd/video-automation recovery-code`.
- `OPENROUTER_API_KEY` is only a first-start bootstrap option. Configure normal provider settings through the authenticated Settings UI.

## Project rules and verification

- Keep media on the persistent application volume. Do not add object storage, change output quality, or alter provider/workflow semantics without a product decision.
- Keep idle processor work cheap and Modal callback recovery sparse. Do not replace callback completion with frequent polling.
- Tests must not submit paid provider generations. Before handing off changes, run `go test ./...`, `node --check internal/webui/static/app.js`, `python3 workers/modal/callback_delivery_test.py`, and `git diff --check`.
- The main thread is the CTO/lead: decide architecture, delegate implementation, and review results; it does not directly implement code. Use Terra Max for large or complex engineering tasks and Luna Max for focused, straightforward changes.
