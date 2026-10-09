# FrameVault Video Automation

A self-hosted dashboard for generating video with a selectable Modal or OpenRouter provider, tracking jobs after the browser closes, and storing completed MP4 files locally in a Docker-managed volume. It uses SQLite for metadata and no external storage service. OpenRouter remains the separate provider for story and script generation.

The dashboard supports a single video and a 30-second project. For a project, Claude Haiku 4.5 (`anthropic/claude-haiku-4.5`) through OpenRouter creates a JSON schema constrained plan. The app validates the story, script, continuity bible, and five scene prompts before video work starts. The selected video provider generates five independent six-second 480p clips in 9:16, and FFmpeg joins them into a silent 1080×1920 MP4. The same providers support single-clip generation.

The random-prompt helper uses Haiku directly for 30-second project ideas. It targets 160–220 characters and validates the complete JSON response against the 280-character limit. An invalid, incomplete, or temporary failure gets one more Haiku attempt within the same 110-second total deadline; invalid output receives a concise correction instruction. Authentication, billing, invalid-request, and explicit refusal failures stop immediately. For a single clip it selects one available model from the existing free list, then automatically switches to Haiku if that attempt times out or returns any error or unusable output. The free attempt lasts at most 35 seconds, leaving time for Haiku within the 110-second total deadline. Caller cancellation stops the operation. Both routes request a JSON object containing only `prompt`; the server validates it and returns the same `{ "prompt": "..." }` browser API.

Known free-model cooldowns skip the free attempt and use Haiku. Shared free-tier quotas do not block paid text requests. General OpenRouter account limits still respect reset metadata; switching models cannot repair an invalid API key, insufficient credits, or an account-wide limit. The dashboard and project processor share cooldown state in memory until the app restarts. See OpenRouter's [limit documentation](https://openrouter.ai/docs/api_reference/limits).

Project story planning stays on Haiku. It allows up to two three-minute attempts within a six-minute, five-second total deadline. A recoverable failure is retried with the same schema contract. The plan must pass validation before scene generation starts. Text traces record the model, prompts, response schema, attempt count, final response, and safe error messages.

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
docker compose exec --user app video-automation /app/video-automation recovery-code
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
4. If Modal is selected, choose the Modal account for this submission. Submit the project. OpenRouter's configured `ScriptModel` generates a plan; the application parses and validates it before video generation starts. Video generation uses the selected provider and its provider-specific model.
5. Follow overall progress and each scene independently. A failed scene can be retried without regenerating successful scenes.
6. When all five scene files are stored, FFmpeg creates the final video automatically.

Projects, scene prompts, provider job IDs, statuses, errors, costs, and file paths are stored in SQLite. The background worker resumes unfinished planning, submission, polling, downloading, and combining work after a restart. Submitted jobs keep an encrypted snapshot of their selected provider credentials, so later account edits or default changes do not alter the account used by an existing job. New Modal callback submissions can safely retry the same stable job ID against the updated worker, which reuses the accepted durable job. An interrupted OpenRouter or legacy submission may have reached the provider before its acknowledgment was saved; check provider activity before manually retrying that scene.

Project submissions support an optional `request_id`. Repeating the same ID and options returns the original project, including its current failed or completed status, without starting another generation. Reusing an ID with different options returns a conflict. Submission records survive app restarts and project deletion, so delayed retries cannot recreate a deleted job. `GET /api/projects?request_id=...` finds the exact visible project even when it is older than the History list; normal authentication and Vault rules apply. Older API clients that omit the ID still create a new project for every accepted POST.

The dashboard saves a project request ID and its exact topic/options in the current tab's `sessionStorage` before sending the POST. Refreshing that tab looks up the same ID, so an accepted project is restored without starting generation again, even if it is older than the 24-project History list. If the lookup returns no project or the connection fails while the original POST may still be in flight, the dashboard keeps the original request and ID; retrying Generate sends the same request, and the server either returns the accepted project or creates it once. Editing the topic or options starts a new request ID. Closing the tab clears its saved draft; an accepted project remains in SQLite and in the normal latest-24 History results while it falls within that window. On a fresh page, the dashboard selects an active project first, otherwise the newest saved result. An app restart can resume unfinished work when the production `/data` volume is preserved. Random-idea generation is a separate text request rather than a saved job: refreshing before its response arrives can lose that idea, and the existing topic stays in place unless an idea was returned and applied.

Each project also keeps a permanent process/audit trace. In **Generation details**, expandable sections retain the exact text-generation system prompt, user prompt, JSON schema, raw tool arguments or assistant response, requested model, actual model when returned, normalized story/script/continuity, and each scene's final video prompt. The **Pipeline** timeline records stages, statuses, timestamps, errors, and attempts/retries. This is an operational audit trace, not hidden chain-of-thought or private reasoning. Raw responses are capped at 1 MiB.

## Single-clip workflow

1. Open **Generate** and choose **Single clip**.
2. Enter a prompt or use the random-prompt helper in single mode. The helper returns one standalone text-to-video prompt; the project mode returns a short topic instead.
3. Choose an available model and settings supported by its runtime-reported duration, resolution, aspect-ratio, and audio capabilities. Choose a Modal account when Modal is selected, then submit.
4. Follow the job in the dashboard or History. Provider errors remain attached to the job. Modal terminal callbacks start the local download; OpenRouter video jobs use status polling.

## YouTube manual upload

FrameVault uploads only when you choose **Upload to YouTube**. It does not upload automatically or schedule videos. The action is available for completed single clips and projects from Generate results, Overview, History, and an unlocked Vault. All entry points open the same preview and editable metadata dialog. See [YouTube upload UI behavior](docs/youtube-upload-ui.md) for state and recovery details.

### Configure Google OAuth

1. In Google Cloud Console, select a project and enable the [YouTube Data API v3](https://console.cloud.google.com/marketplace/product/google/youtube.googleapis.com).
2. Configure the OAuth consent screen and create an OAuth client whose type is **Web application**. FrameVault uses user OAuth; an API key alone cannot authorize video uploads.
3. In FrameVault **Settings > YouTube**, enter the OAuth client ID and secret and the dashboard's public HTTPS origin. For local development, `http://localhost`, `http://127.0.0.1`, and `http://[::1]` are accepted. Save the settings and copy the exact **Authorized redirect URI** displayed there into the Google OAuth client's Authorized redirect URIs. The URI ends in `/api/youtube/oauth/callback`; do not construct it from an internal hostname or proxy path.
4. Choose **Connect YouTube** and approve these requested scopes: `https://www.googleapis.com/auth/youtube.upload` and `https://www.googleapis.com/auth/youtube.readonly`. Google offline access is requested so the worker can continue a queued transfer after the browser closes. FrameVault stores the client secret and refresh token encrypted in `/data/app.db` using `/data/secret.key`; the browser never receives either secret. Back up both files together.

While the OAuth consent screen is in **Testing**, add every account that will connect as a test user. Google currently limits Testing audiences to 100 test users and expires their authorizations, including offline refresh tokens, after seven days; reconnect after expiry. See Google's [Testing publishing status guidance](https://support.google.com/cloud/answer/15549945?hl=en) and [OAuth verification requirements](https://support.google.com/cloud/answer/13463073?hl=en). OAuth consent/scope verification and YouTube API upload audit are separate Google reviews.

Google restricts uploads from API projects created after July 28, 2020 that have not completed the YouTube API compliance audit to private visibility. FrameVault selects **Private** by default, but the service can accept other visibility choices; Google's restriction can still override a request. Check the returned visibility in the upload status. Review the official [`videos.insert` documentation](https://developers.google.com/youtube/v3/docs/videos/insert) and [YouTube API quota and compliance audits](https://developers.google.com/youtube/v3/guides/quota_and_compliance_audits) before public rollout. Monitor the project's current allocation and usage in the [YouTube Data API quota console](https://console.cloud.google.com/apis/api/youtube.googleapis.com/quotas); quota allocation and policy can change.

### Upload and recover

The composer requires an explicit made-for-kids selection and starts the synthetic-content disclosure checked for generated media; both are editable. **Generate title and description** is optional, requires the OpenRouter API key configured in FrameVault Settings, and tries only the existing four explicitly free text models. It has no paid or local fallback. Suggestions fill a field only if you have not edited that field while generation was running, and a failure leaves manual entry available. Title and description are validated before submission. Vault requests carry the current Vault grant, and the service rechecks access. Moving a source to the Vault is blocked while an upload still needs its local media; YouTube processing or completion does not block the move.

Uploads use Google's [resumable upload protocol](https://developers.google.com/youtube/v3/guides/using_resumable_upload_protocol). When the transfer reaches YouTube, FrameVault continues to show **Processing** until YouTube confirms the video is ready; only **Ready on YouTube** means processing finished. Closing the browser does not start another upload; progress is checked while the composer is open. A safe cancellation may be submitted again. If Google may have accepted a video but FrameVault cannot confirm its ID, open YouTube Studio and verify the result before using the explicit **Start upload again** confirmation. The service probes its saved resumable session before restarting. Canceling or abandoning only stops FrameVault tracking; it never deletes a video from YouTube.

## Text generation

Project story plans and random project ideas use `anthropic/claude-haiku-4.5`. Single-clip random prompts first select an available free variant from `nvidia/nemotron-3.5-lightning:free`, `qwen/qwen3.8-27b:free`, `nvidia/nemotron-3-nano-omni-30b-a3b-reasoning:free`, and `liquid/lfm-2.5-2.6b:free`; errors fall back to Haiku once. Generated project ideas are capped at 280 characters and single-clip prompts at 4,000 characters. Empty, oversized, refused, truncated, and malformed outputs are rejected.

The existing authenticated Settings OpenRouter key handles Haiku; no separate Anthropic key is required. **OpenRouter credits are required for project text generation and single-clip fallback.** Model and schema support were verified against the [live OpenRouter catalog](https://openrouter.ai/api/v1/models) on 2026-10-02. Check current [Haiku pricing](https://openrouter.ai/anthropic/claude-haiku-4.5/api) before running a batch. Text charges are separate from the dashboard's video cost totals.

The selected category is saved with the project and used throughout planning and retries. Older API clients may omit `category`; their projects keep general story guidance. Category prompts cover family-friendly animation, restrained horror, natural behavior, and non-explicit adult fashion and cinematic posing. Each story follows five connected visual beats with a payoff, immutable continuity, one achievable action per six-second shot, and silent vertical composition. Prompt contracts and regression guidance are recorded in [the text routing decision](docs/architecture/002-haiku-text-routing.md).

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

### Railway persistence

In Railway, open the existing **video-automation Development** service and:

1. Create a persistent volume in the Development environment.
2. Attach/select that volume for the existing service and set its mount path to `/data`.
3. Set `DATA_DIR=/data` in the service variables. Railway supplies the actual volume mount metadata automatically; do not set a made-up mount-path variable.
4. Keep the root `Dockerfile` build, expose container port `8080`, and use `GET /health` as the health check.

Leave Railway's `RAILWAY_RUN_UID` unset so the entrypoint can initialize volume ownership as root and then start the service as `app`. If Railway forces a non-root run UID, the initializer is bypassed and a root-owned volume may prevent startup; remove that override.

The app validates the runtime mount before initializing storage. If the Development volume is missing or incorrectly mounted, startup fails instead of treating the container filesystem as persistent storage. The Settings page already stores provider credentials and other configuration in SQLite; after the volume is attached, they persist with the account and job history across pushes/redeploys, so they do not need to be entered again.

An empty volume starts with the documented first-run account and no existing settings, history, or media. To retain existing state, first make a consistent full backup and restore it to the volume before starting the app. Preserve `/data/app.db`, `/data/secret.key`, `/data/videos/`, and `/data/projects/` together. Stop the app before copying SQLite files so any WAL state is included consistently; do not copy only `app.db` from a live service. Losing or mismatching `secret.key` makes encrypted settings and credentials unreadable.

Keep Development and Production on separate service volumes. Do not attach the Production volume to Development or use one volume for both. Run one app replica per volume because SQLite state and generated media are local to that volume and are not shared across replicas. A redeploy using the same intact volume should retain the password, settings, history, and media; a new or empty volume is a new application state.

Railway service and volume configuration is managed in the Railway dashboard. This repository does not add deprecated `railway.toml` or `railway.json` configuration files.

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

If an edge proxy returns HTTP 403 for a callback, that is not a rejection status from the current Go callback handler. Go returns 401 for a bad per-job capability, 404 for an unknown job, 400 for an invalid callback body, 503 for a database failure, and 204 on success. A 403 therefore points first to the gateway, an incorrect callback origin, or an older deployment. Set `VIDEO_CALLBACK_BASE_URL` to the final public HTTPS origin of this Go dashboard, not a Modal or internal host, login URL, or redirecting proxy path. The reverse proxy must pass `POST /api/video-callbacks/*` and the `Authorization: Bearer ...` header to Go without a browser-login challenge or redirect. Keep Go's per-job capability check enabled. Compare the matching request/job in Go logs with edge logs to determine which layer produced the 403; do not log or share the bearer token.

Cloudflare can block Python's generic `urllib` user agent with browser-signature error 1010. Both workers identify callback requests as `FrameVault-Modal-Callback/1.0`; deploy both updated workers before relying on that identity. Railway Under Attack Mode presents a browser challenge and blocks machine requests to the service where it is enabled. A different hostname or user agent does not bypass that challenge, so any change to this protection remains an owner decision. If the production Railway origin is reachable without a browser challenge, it can be used directly for `VIDEO_CALLBACK_BASE_URL`, even while the dashboard uses its custom domain. Do not send production callbacks to a development service or another app with a separate volume.

The callback URL is saved with each job, so changing `VIDEO_CALLBACK_BASE_URL` affects new jobs and does not rewrite active jobs. For rollout, deploy both updated Wan and SkyReels workers with legacy callback support first, then deploy Go with the matching callback receiver and public origin. Verify health and callback delivery before submitting new Modal jobs. Completed media has bounded sparse recovery beginning after a 15-minute delay, with at most four checks before the eight-hour deadline; a permanent callback preflight 403 stops further GPU work. Do not assume an edge 403 is fixed or an existing job recovered until the matching deployment and job state have been verified.

For SkyReels V2 deployment and test details, see the [worker README](workers/modal/README.md).

## Dashboard behavior

For OpenRouter, the model list includes provider-supplied per-second pricing where available, such as `$0.50/sec`; otherwise it shows `From …/sec` or `Price unavailable`. The dashboard shows estimates only when the active provider supplies pricing. Modal's API returns no per-video price, so its compute cost is billed separately by Modal. History stores the cost value reported by the selected provider; confirm the provider's billing dashboard for actual charges.

Jobs are stored before asynchronous provider work begins. OpenRouter video jobs use background status polling. New Modal jobs transition through authenticated per-job callbacks, then the Go processor downloads completed video into `/data/videos`; the callback watchdog resumes idempotent submissions and reports callback timeouts. Closing the dashboard does not cancel a job. Live job views and History receive same-origin authenticated server-sent events and retain persisted, application-normalized event messages and progress for project and single-clip jobs. Provider, scene submission, and download failures remain visible on the job or project timeline; failed project scenes can be retried without rerunning successful scenes. History includes the prompt, model, date, duration, status, cost, local playback/download, and manual deletion. Deletion removes the corresponding database record and local video file. Files are retained until deleted in History.

### Using the dashboard

The main destinations are **Workspace**, **Create**, **History**, **Vault**, and **Settings**. Workspace shows a combined recent list of projects and individual clips. Create offers a 30-second project or a single clip. A project always uses OpenRouter to plan and validate its story, even when Modal is selected for video; the selected video provider then generates its five connected six-second scenes. A single clip goes directly to the selected video provider.

While a job runs, its current status and progress stay with that job. Project and clip details can be opened when you need the process log or generation trace. The connection indicator reports the current live-update state, including connecting, live, reconnecting, unavailable, or locked. A lost dashboard connection can delay visible updates, but closing the browser does not stop submitted work; reopen the dashboard to see persisted job state.

History search and status/format filters apply to the records currently loaded in the page. Use **Load more** to fetch older clips, then search or filter the expanded loaded set. The result count describes that loaded set; it does not imply that every saved record has been fetched. Projects and clips remain identifiable by format in the combined recent list and History.

The cost estimate uses prices supplied by the selected video provider. If no price is supplied, the dashboard cannot estimate that job's video cost; Modal infrastructure usage is billed separately by Modal. Project planning also uses OpenRouter, so a configured OpenRouter key is required for projects regardless of the selected video provider. Configure provider credentials and select a provider in **Settings**; a Modal account must also be configured and selected when Modal runs a job.

YouTube uploads are manual. Review the video and metadata in the upload dialog and submit when ready; FrameVault does not publish automatically.

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

## Clipping sources and analysis

The Clipping tab starts with a shared video-link field for public single-video YouTube, Drive, and Dropbox links, alongside drag-and-drop or file upload. Confirm permission to process the footage before importing it. Uploads are resumable; YouTube availability is best-effort and can fall back to an original-file upload. Operational controls for source management, batches, budgets, retention, and worker settings remain in collapsed disclosures; saved-analysis candidate review and invoice settlement remain available on demand from their detail views. Select ready sources to queue a job or batch with explicit per-job and total budgets.

Source analysis and candidate selection are available only after an operator separately deploys and configures the optional worker. It is not deployed by default, and submitting sources alone does not start analysis. Candidate selection does not yet include captions, framing, editing, or video exports. Source limits are 20 GiB and four hours, with a 100 GiB aggregate reservation bound and physical disk checks. Live provider acquisition and cost/quality feasibility remain unverified. See [M2 implementation and operations](docs/clipping/M2-IMPLEMENTATION.md), [M3 implementation and setup](docs/clipping/M3-IMPLEMENTATION.md), and the [tracker](docs/clipping/TRACKER.md) for protocols, recovery, verification, and authorization.
