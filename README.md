# FrameVault Video Automation

FrameVault is a self-hosted dashboard for generating videos, analyzing public YouTube videos with Gemini, and storing media locally. It is a Go service with a plain JavaScript dashboard, SQLite metadata, and a persistent `/data` volume.

Jump to [Quick start](#quick-start), [Create videos](#create-videos), [Clipping](#use-clipping), [YouTube uploads](#upload-to-youtube), [Data and security](#data-and-security), or [Development](#development). See the [documentation index](docs/README.md) for operator guides and architecture notes.

## What works today

- **Video generation:** create a single clip or a 30-second project. Projects use OpenRouter to plan five connected scenes; the selected OpenRouter or Modal video provider creates five six-second, 480p vertical clips, which FFmpeg joins into a silent 1080×1920 video.
- **YouTube uploads:** upload a completed video manually after connecting a YouTube account. FrameVault does not publish or schedule videos automatically.
- **Clipping:** submit a public YouTube URL directly to Google Cloud Gemini. Gemini selects and ranks proposed clips; the Go backend validates and saves the results without downloading the source video.

## Current clipping limits

Gemini analysis defaults off and requires an operator-configured Google Cloud project and Application Default Credentials (ADC). Public YouTube input is Preview and availability is best-effort. The initial source limit is 40 minutes, checked against model-reported duration; clip options span 15–180 seconds and 1–10 candidates. Gemini may return fewer candidates with an explanation.

Scores are editorial estimates and timestamps are proposed boundaries. Live provider access, semantic quality, language performance, latency and cost remain unmeasured by this migration. The page does not edit, extract, render, export or publish candidate videos; `youtube-extractor-rs` integration is a future phase.

## Gemini migration status

All six implementation phases are integrated and independently reviewed: provider contracts, compatible references, durable execution, dashboard/API flow, offline evaluation and legacy-runtime retirement. New analysis uses Gemini exclusively; old import/submission/retry routes and outbound Modal clipping dispatch are retired. Historical media/results and issued legacy callbacks remain supported. Live qualification, a real legacy drain and production rollout have separate gates. See the [migration plan](docs/clipping/GEMINI-MIGRATION.md), [dashboard/API guide](docs/clipping/GEMINI-DASHBOARD.md) and [legacy drain procedure](docs/clipping/LEGACY-RETIREMENT.md).

## Quick start

Install Docker with Compose, then start the app:

```bash
docker compose up -d --build
```

Open <http://localhost:8080> and sign in with the first-run account:

```text
Username: sujanshrestha
Password: Sujan@123
```

Change the password immediately in **Settings**. Add and test an OpenRouter key there; projects need it for story planning even when Modal is selected for video. Choose a video provider in **Settings > Video generation**. To use Modal for video generation, deploy a worker and add its endpoint and key under **Modal accounts**. New Modal jobs also need the dashboard's public HTTPS origin for callbacks:

```bash
VIDEO_CALLBACK_BASE_URL="https://video.example.com" docker compose up -d --build
```

Set the origin without a path. Deploy the updated Modal worker before the Go callback receiver, then verify health and callback delivery before submitting new Modal jobs. See the [worker setup guide](workers/modal/README.md). OpenRouter-only use does not need `VIDEO_CALLBACK_BASE_URL`.

The container restarts automatically and resumes saved jobs when its volume is preserved. Check status and logs with:

```bash
docker compose ps
docker compose logs -f video-automation
```

Stop the app without deleting its data with `docker compose down`.

## Create videos

In **Create**, choose **Single clip** for one prompt-driven video, or **30-second project** for a five-scene story. A project always uses OpenRouter for story planning, regardless of the selected video provider. The dashboard saves job state before work begins, so submitted jobs continue after the browser closes and unfinished work can resume after a restart when `/data` is persistent.

Provider capabilities and prices are read at runtime. Estimates appear only when a provider supplies a price; Modal infrastructure is billed separately by Modal. Check account billing before running paid work.

Project story planning and project ideas use OpenRouter's Haiku model. Single-clip random prompts try a free text model first and fall back to Haiku when needed. OpenRouter credits are required for project planning and that fallback; text costs are separate from video estimates.

## Use Clipping

Enter one public YouTube video URL in **Clipping**, confirm permission to process the footage, choose the profile and duration/candidate options, then select **Analyze video**. Gemini analyzes the URL directly. The saved results include ranked titles, hooks, proposed times, explanations, context and evidence. Private or sign-in-only videos are unsupported.

On a host with ADC supplied by its runtime identity, enable analysis with nonsecret configuration:

```bash
CLIPPING_GEMINI_ENABLED=true \
CLIPPING_GEMINI_PROJECT_ID=your-google-cloud-project \
CLIPPING_GEMINI_LOCATION=global \
docker compose up -d --build
```

The application uses ADC/service accounts, with no Google API-key input. Supported locations are `global`, `us` and `eu`. **Configured** reports local settings only; it does not verify credentials or video access. Analysis may incur Google Cloud charges. See the [operations runbook](docs/clipping/GEMINI-OPERATIONS.md), [provider](docs/clipping/GEMINI-PROVIDER.md) and [execution](docs/clipping/GEMINI-EXECUTION.md) guides before enablement.

An unresolved submission can be resent with the exact original body and key. Unknown provider outcomes are quarantined rather than automatically repeated. After a known terminal result, an explicit new analysis creates a separate job. Cancel/delete stop local publication but cannot guarantee Google stopped or did not charge. Existing saved YouTube sources can be analyzed remotely; legacy media and results remain readable.

## Upload to YouTube

Upload is manual from a completed video. Connect an account under **Settings > YouTube**, review the video and metadata, and submit from the upload dialog.

To connect YouTube:

1. In Google Cloud Console, enable the [YouTube Data API v3](https://console.cloud.google.com/marketplace/product/youtube.googleapis.com) and configure the OAuth consent screen.
2. Create an OAuth client with type **Web application**. While the consent screen is in **Testing**, add every account that will connect as a test user.
3. In FrameVault **Settings > YouTube**, enter the client ID, client secret, and the dashboard's public HTTPS origin (HTTP localhost is accepted for local development). Save, then copy the exact **OAuth redirect URI** shown there into the OAuth client's authorized redirect URIs. It ends in `/api/youtube/oauth/callback`; do not construct it from an internal host or proxy path.
4. Choose **Connect YouTube channel** and approve the requested `youtube.upload` and `youtube.readonly` scopes.

Google Testing authorizations, including offline refresh tokens, expire after seven days; reconnect after expiry. OAuth consent verification and YouTube API upload audit are separate reviews. Projects created after July 28, 2020 that have not completed the YouTube API compliance audit can be restricted to Private; check the returned upload status. See Google's [Testing publishing guidance](https://support.google.com/cloud/answer/15549945?hl=en), [OAuth verification requirements](https://support.google.com/cloud/answer/13463073?hl=en), and [YouTube API quota and compliance audits](https://developers.google.com/youtube/v3/guides/quota_and_compliance_audits). For upload recovery and dialog behavior, see [YouTube upload guidance](docs/youtube-upload-ui.md).

## Data and security

Docker Compose keeps application state in the named `video_automation_data` volume mounted at `/data`. It contains `/data/app.db`, the encryption key at `/data/secret.key`, generated videos in `/data/videos/` and `/data/projects/`, and legacy clipping media in `/data/clipping/`. New URL references and analysis results live in SQLite; no new source media is downloaded. Keep the database and key together in backups; without the key, saved credentials cannot be decrypted.

Create a portable backup while the app is stopped:

```bash
docker compose stop video-automation
docker run --rm -v video_automation_data:/data -v "$PWD":/backup alpine:3.24 tar czf /backup/video-automation-backup.tgz -C /data .
docker compose start video-automation
```

The default Compose binding is `127.0.0.1:8080`. For public access, put an HTTPS reverse proxy in front of it and keep the app bound to localhost. Change the initial password before adding credentials, and use HTTPS outside a trusted local network. See [Railway persistence](docs/architecture/003-railway-persistence.md) for the persistent-volume requirements on Railway.

For Docker, generate a one-time password recovery code with:

```bash
docker compose exec --user app video-automation /app/video-automation recovery-code
```

Enter it under **Forgot password?** on the sign-in screen. It expires after 15 minutes and can be used once.

## Development

The service entry point is cmd/video-automation/main.go; backend code and tests are in internal/app/, and the embedded dashboard is in internal/webui/static/. Modal workers are under workers/modal/. Native development needs Go 1.24 or newer and FFmpeg for generation and project assembly. Gemini analysis uses ADC and does not require a local video downloader. The clipping downloader, Deno/Python importer runtime, and Modal analysis worker are removed. Modal video-generation workers and FFmpeg assembly remain supported.

Run locally with a writable data directory:

```bash
DATA_DIR=/path/to/video-data go run ./cmd/video-automation
```

The server listens on port `8080`; `APP_PORT` changes only the Docker host-side binding. The health endpoint is `GET /health`.

Run the routine local checks from the repository root:

```bash
go test ./...
node --check internal/webui/static/app.js
python3 workers/modal/callback_delivery_test.py
git diff --check
```

Evaluate saved analyses and human annotations without credentials, network calls or media access:

```bash
go run ./cmd/clipping-gemini-eval --input docs/clipping/gemini-evaluation/synthetic-example.v1.json
```

The example is synthetic and proves no live quality or readiness. The evaluator withholds incomplete quality metrics, preserves model rank, hashes report identifiers and distinguishes operator evidence from verified facts. See the [Gemini evaluation protocol](docs/clipping/EVALUATION.md#g5-gemini-offline-evaluation).

For deeper implementation notes and decisions, see [docs/README.md](docs/README.md), [architecture decisions](docs/architecture/), and the [clipping tracker](docs/clipping/TRACKER.md).
