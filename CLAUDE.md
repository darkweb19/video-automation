# FrameVault Video Automation

FrameVault is a Go HTTP application with an embedded dashboard. SQLite holds settings, jobs, projects, and workflow history. Generated clips and final videos live on persistent local storage. OpenRouter generates story plans and scripts; the selected video provider generates clips. The optional Modal worker is deployed separately.

`AGENTS.md` is the authoritative detailed guide for architecture, storage and cost decisions, security invariants, operations, and routine checks. Keep its file map aligned with this guide when files or responsibilities change.

## Repository structure and key files

Application source is in the repository root. `static/` contains the embedded browser dashboard, `modal/` contains the separate Python worker, and `*_test.go` contains Go unit, HTTP, storage, security, and workflow tests.

| File or folder | Responsibility |
| --- | --- |
| `main.go` | Opens the data store and security state, serves HTTP on port 8080, and starts the background processor. |
| `dashboard.go` | Dashboard app shell, shared route registration, health endpoint, and embedded static files. |
| `dashboard_auth.go` | Authentication guards, sessions, password handling, and request throttles. |
| `dashboard_providers.go` | Provider setup, model capabilities, and immutable credential snapshots. |
| `dashboard_generation.go` | Generation submission, status, callback, history, and random-prompt routes. |
| `dashboard_projects.go` | Project lifecycle, scene retry, and project API routes. |
| `dashboard_media.go` | Local media playback, downloads, caching, and deletion routes. |
| `dashboard_settings.go` | Settings and account-management routes. |
| `handlers.go` | Shared provider-facing generation validation and streamed video responses. |
| `models.go` | Core generation types, `VideoService` interfaces, provider identifiers, model capabilities, and workflow constants. |
| `openrouter.go`, `modal.go` | Provider-specific HTTP requests, response normalization, and content fetching. |
| `script_generation.go`, `story_plan_choice.go` | OpenRouter story/script requests, parsing, and story-plan validation. |
| `processor.go` | Processor setup, five-second work loop, job/project scans, status polling, and Modal generation recovery. |
| `processor_projects.go` | Project story planning, scene submission and recovery, and coordination of final assembly. |
| `processor_downloads.go` | Streaming scene and generation downloads, atomic file storage, and retry handling. |
| `processor_tasks.go` | Bounded background task coordination for project work and FFmpeg assembly. |
| `storage.go`, `project_storage.go` | SQLite schema, additive migrations, settings, state transitions, worker reads, paths, retries, and pipeline events. |
| `combiner.go` | FFmpeg command construction and file-based final assembly. |
| `events.go` | Authenticated server-sent events for job and project updates. |
| `security.go`, `vault.go`, `modal_callback.go` | Encrypted settings and sessions, Vault authorization, and authenticated Modal callbacks. |
| `static/` | Browser assets; `app.js` calls the same-origin authenticated API. |
| `modal/video.py` | Deployable Modal generation worker and callback delivery. |
| `Dockerfile`, `docker-compose.yml` | Production container build and local Compose stack. |
| `README.md` | Setup, provider configuration, storage, backup, and deployment instructions. |

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

- Build Railway deployments from the repository root with `Dockerfile`. Attach a persistent volume at `/data` for SQLite, `secret.key`, downloaded clips, and project output. Run one replica against that local state.
- The application binds fixed port `8080`; configure the service target port accordingly. The app does not read Railway's `PORT` or Compose's host-side `APP_PORT`.
- Set `PUBLIC_BASE_URL` to the public HTTPS dashboard origin when using Modal callbacks. Check deployment health at `GET /health`.
- Start local Compose with `docker compose up -d --build`. For native Go development, set `DATA_DIR` to a writable directory and run `go run .`; FFmpeg must be installed.
- `OPENROUTER_API_KEY` is only a first-start bootstrap option. Configure normal provider settings through the authenticated Settings UI.

## Project rules and verification

- Keep media on the persistent application volume. Do not add object storage, change output quality, or alter provider/workflow semantics without a product decision.
- Keep idle processor work cheap and Modal callback recovery sparse. Do not replace callback completion with frequent polling.
- Tests must not submit paid provider generations. Before handing off changes, run `go test ./...`, `node --check static/app.js`, `python3 modal/callback_delivery_test.py`, and `git diff --check`.
- The main thread is the CTO/lead: decide architecture, delegate implementation, and review results; it does not directly implement code. Use Terra Max for large or complex engineering tasks and Luna Max for focused, straightforward changes.
