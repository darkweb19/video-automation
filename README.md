# FrameVault Video Automation

A self-hosted dashboard for generating video with a selectable Modal or OpenRouter provider, tracking jobs after the browser closes, and storing completed MP4 files locally in a Docker-managed volume. It uses SQLite for metadata and no external storage service. OpenRouter remains the separate provider for story and script generation.

The dashboard supports a single video and a 30-second project. For a project, OpenRouter's `inclusionai/ling-3.0-flash-fin:free` model creates a structured plan with a plain-text parsing fallback. The app validates the story, script, continuity bible, and five scene prompts. The selected video provider generates five independent six-second 480p clips in 9:16, and FFmpeg joins them into a silent 1080×1920 MP4. The same providers support single-clip generation.

The random-prompt helper uses five explicitly free OpenRouter text models in shuffled order, with no application usage quota. Requests have a 110-second safety deadline; each model gets an adaptive share of the remaining time, up to 35 seconds, so slow models leave time for the others. Empty, refused, malformed, or truncated responses move to the next model. Authentication and invalid-request errors stop immediately, and retries never use a paid model.

Model rate limits trigger a temporary cooldown while other free models are tried. OpenRouter-wide limits respect `Retry-After` and reset times instead of sending the same blocked request to every model. Short resets are retried automatically; longer resets return the time to retry. The dashboard and project processor share these cooldowns in memory until the app restarts. OpenRouter's shared free-tier quota still applies across models and API keys; see its [limit documentation](https://openrouter.ai/docs/api_reference/limits). This app uses OpenRouter only, so it cannot generate while that shared quota is exhausted.

Project story planning stays on `ScriptModel`. It allows up to two three-minute attempts within a six-minute, five-second total deadline. A recoverable failure is retried; an empty or invalid five-scene response retries with a plain JSON request instead of requiring a function call. The plan must still pass validation before scene generation starts. Text traces record the attempt count, final response, and safe error messages.

## Docker quick start

Install Docker Desktop (or Docker Engine with Compose), then run:

```bash
docker compose up -d --build
docker compose ps
```

Open <http://localhost:8080> and sign in with the temporary first-run account:

```text
Username: sujanshrestha
Password: Sujan@123
```

Change this password immediately in **Settings**. Add and test the OpenRouter API key there; it remains necessary for story and script generation even when Modal is selected for video. In **Video generation**, choose the active video provider. In **Modal accounts**, add named endpoints and encrypted API keys, and choose a default. These settings persist in SQLite, so changing providers or Modal accounts does not require rebuilding the Go application. No API-key environment variable or `.env` file is required for normal setup.

Before submitting a new Modal job, set the dashboard's public HTTPS origin for callbacks. It must be reachable by Modal, so `localhost` and the Compose loopback port are not sufficient:

```bash
VIDEO_CALLBACK_BASE_URL="https://video.example.com" docker compose up -d --build
```

OpenRouter-only use and local dashboard development do not need this variable. `PUBLIC_BASE_URL` is a compatibility fallback for older deployments; configure `VIDEO_CALLBACK_BASE_URL` for new Modal jobs. When neither is set, new Modal submissions fail before dispatch.

The container is configured with `restart: unless-stopped`; it resumes outstanding jobs after a restart. Check its logs with:

```bash
docker compose logs -f video-automation
```

Stop it without deleting data:

```bash
docker compose down
```

## Password recovery

If you forget the password, generate a one-time recovery code from the same data directory as the running app. For Docker:

```bash
docker compose exec video-automation /app/video-automation recovery-code
```

For a local development server:

```bash
go run ./cmd/video-automation recovery-code
```

Open **Forgot password?** on the sign-in card, then enter the code and a new password. The code expires after 15 minutes, works once, and generating a replacement invalidates the previous code. A successful reset signs out every existing session. Recovery codes are stored only as hashes.

The Go server listens on `0.0.0.0:8080` and does not read an `APP_PORT` setting. Docker Compose publishes the host side on `127.0.0.1:8080`; its optional `APP_PORT` variable changes that host-side port only, while the container port remains 8080. For example, `APP_PORT=9000 docker compose up -d` publishes on `127.0.0.1:9000`.

## Data persistence and backups

The named Docker volume `video_automation_data` is the only persistent storage location. The app does not use browser local storage for application data, a host bind mount, or an external object store.

Within that volume:

| Path | Contents |
| --- | --- |
| `/data/app.db` | SQLite users, selected video provider, named Modal accounts and encrypted credentials, encrypted Vault code and membership, per-job provider credential snapshots and callback capabilities, provider-scoped video models, jobs, normalized events, progress, errors, and cost records |
| `/data/secret.key` | Encryption key for stored API credentials |
| `/data/videos/` | Downloaded completed MP4 files |
| `/data/projects/` | Per-project scene clips and final 30-second MP4 files |

## Thirty-second project workflow

1. Open **Generate** and leave **30-second project** selected.
2. Enter a topic or story idea.
3. Select a model that supports six-second clips, 480p resolution, and the 9:16 aspect ratio. The model list comes from the selected video provider and its capabilities determine which requests are accepted.
4. If Modal is selected, choose the Modal account for this submission. Submit the project. OpenRouter's `inclusionai/ling-3.0-flash-fin:free` model generates a plan; the application parses and validates it before video generation starts. Video generation uses the selected provider and its provider-specific model.
5. Follow overall progress and each scene independently. A failed scene can be retried without regenerating successful scenes.
6. When all five scene files are stored, FFmpeg creates the final video automatically.

Projects, scene prompts, provider job IDs, statuses, errors, costs, and file paths are stored in SQLite. The background worker resumes unfinished planning, submission, polling, downloading, and combining work after a restart. Submitted jobs keep an encrypted snapshot of their selected provider credentials, so later account edits or default changes do not alter the account used by an existing job. New Modal callback submissions can safely retry the same stable job ID against the updated worker, which reuses the accepted durable job. An interrupted OpenRouter or legacy submission may have reached the provider before its acknowledgment was saved; check provider activity before manually retrying that scene.

Each project also keeps a permanent process/audit trace. In **Generation details**, expandable sections retain the exact text-generation system prompt, user prompt, JSON schema, raw tool arguments or assistant response, requested model (`inclusionai/ling-3.0-flash-fin:free`), actual model when returned, normalized story/script/continuity, and each scene's final video prompt. The **Pipeline** timeline records stages, statuses, timestamps, errors, and attempts/retries. This is an operational audit trace, not hidden chain-of-thought or private reasoning. Raw responses are capped at 1 MiB.

## Single-clip workflow

1. Open **Generate** and choose **Single clip**.
2. Enter a prompt or use the random-prompt helper in single mode. The helper returns one standalone text-to-video prompt; the project mode returns a short topic instead.
3. Choose an available model and settings supported by its runtime-reported duration, resolution, aspect-ratio, and audio capabilities. Choose a Modal account when Modal is selected, then submit.
4. Follow the job in the dashboard or History. Provider errors remain attached to the job. Modal terminal callbacks start the local download; OpenRouter video jobs use status polling.

## Text generation

Project story plans use the configured `ScriptModel`, currently `inclusionai/ling-3.0-flash-fin:free`. Random prompts use these pinned free variants: `inclusionai/ling-3.0-flash:free`, `nvidia/nemotron-3.5-lightning:free`, `qwen/qwen3.8-27b:free`, `nvidia/nemotron-3-nano-omni-30b-a3b-reasoning:free`, and `liquid/lfm-2.5-2.6b:free`. A project topic is capped at 280 characters after generation; a single-clip prompt is capped at 4,000 characters. OpenRouter availability can change, but random-prompt retries never select a paid model.

The displayed project estimate covers five video generations when the selected provider publishes pricing. Modal does not return per-generation pricing through this API, and its compute charges are billed separately by Modal. Provider pricing and capabilities can change, so confirm any available estimate and account balance before starting a project.

Back up the whole volume, including `secret.key`; the database cannot decrypt a stored API key without it. This command writes a portable archive to the current directory:

```bash
docker run --rm -v video_automation_data:/data -v "$PWD":/backup alpine:3.24 tar czf /backup/video-automation-backup.tgz -C /data .
```

Restore into an empty or intentionally replaced volume only (the command overwrites files in the volume):

```bash
docker run --rm -v video_automation_data:/data -v "$PWD":/backup alpine:3.24 sh -c 'rm -rf /data/* /data/.[!.]* /data/..?*; tar xzf /backup/video-automation-backup.tgz -C /data'
```

Stop the application before backup or restore for a consistent SQLite snapshot:

```bash
docker compose stop video-automation
```

Start it again with `docker compose start video-automation`.

## Security and credentials

The first run provisions the `sujanshrestha` account shown in Quick start. Passwords are stored as hashes. OpenRouter and Modal API credentials, Vault secrets, and per-job callback bearer tokens are encrypted at rest using `/data/secret.key`; keep that file with the database in every backup. The authenticated dashboard never returns provider keys or callback tokens to the browser. Set a new password before adding real credentials, and use HTTPS before exposing the service outside a trusted local network.

## Networking and HTTPS

The included Compose file publishes port 8080 only on `127.0.0.1` for local use. Docker does not add HTTPS. For public deployment, keep that loopback binding and put an HTTPS reverse proxy (for example Caddy, Nginx, or Traefik) on the host in front of `http://127.0.0.1:8080`; expose only the proxy's TLS ports (normally 443/80). A minimal Caddy site is:

```caddyfile
video.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

Point DNS at the host and let Caddy obtain the certificate. Configure TLS before using real credentials or API keys. Do not publish the dashboard directly to a public interface.

For Railway, build from the repository root Dockerfile, attach a persistent volume at `/data`, and expose container port `8080`. The health check is `GET /health`. Keep one service replica per local data volume; SQLite state and generated media are not shared across independent volumes.

For new Modal jobs, set `VIDEO_CALLBACK_BASE_URL` to the externally reachable HTTPS origin of the dashboard, for example `https://video.example.com`. Set only the origin, without a path, query, fragment, or user information; local, loopback, link-local, and private hosts are rejected. The Go service builds callback URLs from this setting and never trusts an inbound `Host` or forwarding header. `PUBLIC_BASE_URL` is accepted only as a backward-compatible fallback when `VIDEO_CALLBACK_BASE_URL` is unset.

## OpenRouter key bootstrap (optional)

The normal path is adding the key in the authenticated Settings page. For unattended initial deployment only, the Compose file accepts an optional `OPENROUTER_API_KEY` environment value. On first start it is encrypted and persisted in the Docker volume:

```bash
OPENROUTER_API_KEY="your_key" docker compose up -d --build
```

Do not place secrets in source control, logs, images, or a committed `.env` file. After it is saved, remove that environment variable and restart the container.

## Video providers and Modal setup

Choose **Modal** or **OpenRouter** in **Settings > Video generation**. The provider choice and each provider's selected video model are stored in the app database. Model choices and the duration, resolution, aspect-ratio, and audio controls are based on the selected model's reported capabilities. The 30-second project remains a fixed preset of five six-second scenes at 480p in 9:16; models must report support for all three values to run that workflow.

OpenRouter's API key is shared between story/script generation and OpenRouter video generation. It remains configured when Modal is selected, because story/script generation continues to use OpenRouter.

Two Modal workers are supplied. `workers/modal/video.py` serves Wan 2.2 Lightning A14B (`modal/wan2.2-lightning-a14b`); `workers/modal/skyreels.py` serves SkyReels V2 T2V 14B (`modal/skyreels-v2-t2v-14b`). They are separate Modal apps with separate durable job/model volumes. Deploy one or both in the Modal workspace where you intend to run video generation. Create the `video-api-secret` secret with its `MODAL_VIDEO_API_KEY`, then deploy the selected worker(s) from the repository root:

```bash
modal secret create video-api-secret MODAL_VIDEO_API_KEY="your-secret-value"
modal deploy workers/modal/video.py
modal deploy workers/modal/skyreels.py
```

If the two apps run in different Modal workspaces, create the secret in each workspace. In **Settings > Modal accounts**, add a named account for each deployed endpoint you want to use, with the matching API key. The endpoint should end in `/api/v1`, for example:

```text
https://<your-modal-endpoint>/api/v1
```

Each Modal deployment has its own endpoint and key. The key is encrypted at rest and is never returned to the browser. Add, edit, or delete named accounts in Settings, and mark one account as the default; the Generate screen lets you choose the account separately for each 30-second project or single clip. Existing jobs retain the encrypted credential snapshot captured at submission. Capabilities are fetched at runtime: Wan supports 1–15 seconds and SkyReels 1–6 seconds; both advertise 480p/720p, 9:16/16:9, and no generated audio. Both support the project's six-second scenes. Neither worker reports a USD video price; Modal infrastructure usage is billed separately by Modal.

### Modal callback rollout

Roll out the worker before the Go callback receiver. The updated worker must preserve the legacy callback mode while existing jobs drain. Then deploy the Go service with `VIDEO_CALLBACK_BASE_URL` set to the public HTTPS origin, verify `GET /health`, and only then submit new Modal jobs. Neither the updated worker alone nor the new Go receiver alone enables the new callback flow.

For a new job, Go persists its callback state before dispatch and assigns a stable `job_id`. It sends the worker `job_id`, `callback_url` (`VIDEO_CALLBACK_BASE_URL` plus `/api/video-callbacks/{job_id}`), and an opaque bearer token. The worker returns HTTP 202 with that same ID; an identical replay reuses the durable job, while a changed request with the same ID is rejected. The worker stores callback credentials in its private durable job record and never includes them in browser-facing status responses or logs.

The worker first persists terminal state, then sends an authenticated callback with a bounded retry schedule and redirects disabled. Go accepts callbacks at `POST /api/video-callbacks/{job_id}`, checks the bearer token and monotonic event sequence, persists progress or terminal status, then downloads completed output. Callback failure or download failure is shown in the job or project pipeline; failed project scenes can be retried without regenerating successful scenes. The watchdog safely retries callback submissions that have not received an acknowledgment and reports a clear failure if no callback arrives before the persisted deadline; new callback jobs do not use frequent provider polling. OpenRouter video jobs continue to use status polling. Legacy Modal jobs retain the prior callback and sparse recovery behavior while they drain.

For SkyReels V2 deployment and test details, see the [worker README](workers/modal/README.md).

## Dashboard behavior

For OpenRouter, the model list includes provider-supplied per-second pricing where available, such as `$0.50/sec`; otherwise it shows `From …/sec` or `Price unavailable`. The dashboard shows estimates only when the active provider supplies pricing. Modal's API returns no per-video price, so its compute cost is billed separately by Modal. History stores the cost value reported by the selected provider; confirm the provider's billing dashboard for actual charges.

Jobs are stored before asynchronous provider work begins. OpenRouter video jobs use background status polling. New Modal jobs transition through authenticated per-job callbacks, then the Go processor downloads completed video into `/data/videos`; the callback watchdog resumes idempotent submissions and reports callback timeouts. Closing the dashboard does not cancel a job. Live job views and History receive same-origin authenticated server-sent events and retain persisted, application-normalized event messages and progress for project and single-clip jobs. Provider, scene submission, and download failures remain visible on the job or project timeline; failed project scenes can be retried without rerunning successful scenes. History includes the prompt, model, date, duration, status, cost, local playback/download, and manual deletion. Deletion removes the corresponding database record and local video file. Files are retained until deleted in History.

Completed videos can be moved from History into the separate Vault tab; this hides them from regular History without moving or copying their files. Set the four-digit Vault code in Settings first. Enter the code to list, play, or download Vault videos. The Vault locks when you refresh the page or choose **Lock Vault**. Change the code in Settings with the current code; keep it safe, because a forgotten code cannot be recovered in this version. Returning a Vault video restores it to History.

## Code organization

The Go service layout keeps its executable entrypoint under `cmd/` and its private implementation under `internal/`:

- `cmd/video-automation/main.go` starts `internal/app.Run()`.
- `internal/app/` contains the cohesive backend package: HTTP routes, provider clients, generation and project processing, SQLite storage, security, and Go tests. Keep these related implementation files in one package while they share internal types and lifecycle; split into more packages only when a stable boundary exists, so implementation details do not become exported APIs.
- `internal/webui/embed.go` embeds `internal/webui/static/`; the dashboard continues to use the same `/static/...` URLs.
- `workers/modal/` contains the separately deployed Python worker and its dependency-free callback checks.
- `docs/architecture/` records architecture decisions.

Run the server natively with `DATA_DIR` set to a writable location; FFmpeg must be installed:

```bash
DATA_DIR=/path/to/video-data go run ./cmd/video-automation
```

## Verification

Run the routine local checks from the repository root:

```bash
go test ./...
node --check internal/webui/static/app.js
python3 workers/modal/callback_delivery_test.py
git diff --check
```

Confirm the image, container health, and protected dashboard:

```bash
docker compose up -d --build
docker compose ps
docker compose exec video-automation wget -q -O - http://127.0.0.1:8080/health
```

`docker compose ps` should show the service as healthy. Open the dashboard in a browser, sign in, change the initial password, save and test the OpenRouter key, choose a video provider, and test its connection. Add a Modal account before selecting one for a submission. OpenRouter video calls may be paid; Modal infrastructure usage is billed separately, and model availability/pricing can change.

For non-Docker development, set `DATA_DIR` to a writable directory and run `go run ./cmd/video-automation`; the server listens on `0.0.0.0:8080`. The Go server has no port override.
