# FrameVault Video Automation

A self-hosted dashboard for generating video with a selectable Modal or OpenRouter provider, tracking jobs after the browser closes, and storing completed MP4 files locally in a Docker-managed volume. It uses SQLite for metadata and no external storage service. OpenRouter remains the separate provider for story and script generation.

The default dashboard workflow creates a complete 30-second YouTube Short from a topic or story idea. OpenRouter's `inclusionai/ling-3.0-flash-fin:free` model generates the plan through a tool call, with plain-text parsing as a fallback. The application validates the plan, including its story, script, continuity bible, and five scene prompts. The selected video provider generates five independent six-second 480p clips in 9:16. FFmpeg normalizes and joins them into one silent 1080×1920 MP4. The original single-clip workflow remains available.

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
go run . recovery-code
```

Open **Forgot password?** on the sign-in card, then enter the code and a new password. The code expires after 15 minutes, works once, and generating a replacement invalidates the previous code. A successful reset signs out every existing session. Recovery codes are stored only as hashes.

The Go server listens on `0.0.0.0:8080` and does not read an `APP_PORT` setting. Docker Compose publishes the host side on `127.0.0.1:8080`; its optional `APP_PORT` variable changes that host-side port only, while the container port remains 8080. For example, `APP_PORT=9000 docker compose up -d` publishes on `127.0.0.1:9000`.

## Data persistence and backups

The named Docker volume `video_automation_data` is the only persistent storage location. The app does not use browser local storage for application data, a host bind mount, or an external object store.

Within that volume:

| Path | Contents |
| --- | --- |
| `/data/app.db` | SQLite users, selected video provider, named Modal accounts and encrypted credentials, encrypted Vault code and membership, per-job provider credential snapshots, provider-scoped video models, jobs, normalized events, progress, errors, and cost records |
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

Projects, scene prompts, provider job IDs, statuses, errors, costs, and file paths are stored in SQLite. The background worker resumes unfinished planning, polling, downloading, and combining work after a restart. Submitted jobs keep an encrypted snapshot of their selected provider credentials, so later account edits or default changes do not alter the account used by an existing job. If the process stops while a paid scene submission is in flight, that scene is marked failed instead of being silently resubmitted; check the selected video provider's activity before using **Retry scene**, because the interrupted request may already have been accepted upstream.

Each project also keeps a permanent process/audit trace. In **Generation details**, expandable sections retain the exact text-generation system prompt, user prompt, JSON schema, raw tool arguments or assistant response, requested model (`inclusionai/ling-3.0-flash-fin:free`), actual model when returned, normalized story/script/continuity, and each scene's final video prompt. The **Pipeline** timeline records stages, statuses, timestamps, errors, and attempts/retries. This is an operational audit trace, not hidden chain-of-thought or private reasoning. Raw responses are capped at 1 MiB.

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

## Networking and HTTPS

The included Compose file publishes port 8080 only on `127.0.0.1` for local use. Docker does not add HTTPS. For public deployment, keep that loopback binding and put an HTTPS reverse proxy (for example Caddy, Nginx, or Traefik) on the host in front of `http://127.0.0.1:8080`; expose only the proxy's TLS ports (normally 443/80). A minimal Caddy site is:

```caddyfile
video.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

Point DNS at the host and let Caddy obtain the certificate. Configure TLS before using real credentials or API keys. Do not publish the dashboard directly to a public interface.

## OpenRouter key bootstrap (optional)

The normal path is adding the key in the authenticated Settings page. For unattended initial deployment only, the Compose file accepts an optional `OPENROUTER_API_KEY` environment value. On first start it is encrypted and persisted in the Docker volume:

```bash
OPENROUTER_API_KEY="your_key" docker compose up -d --build
```

Do not place secrets in source control, logs, images, or a committed `.env` file. After it is saved, remove that environment variable and restart the container.

## Video providers and Modal setup

Choose **Modal** or **OpenRouter** in **Settings > Video generation**. The provider choice and each provider's selected video model are stored in the app database. Model choices and the duration, resolution, aspect-ratio, and audio controls are based on the selected model's reported capabilities. The 30-second project remains a fixed preset of five six-second scenes at 480p in 9:16; models must report support for all three values to run that workflow.

OpenRouter's API key is shared between story/script generation and OpenRouter video generation. It remains configured when Modal is selected, because story/script generation continues to use OpenRouter.

For Modal video, deploy the supplied `video.py` service in the Modal account that will run generation. Run these commands from the directory containing `video.py`. Create the Modal secret named `video-api-secret` with its required `MODAL_VIDEO_API_KEY`, then deploy the service:

```bash
modal secret create video-api-secret MODAL_VIDEO_API_KEY="your-secret-value"
modal deploy video.py
```

In **Settings > Modal accounts**, add an account name, its deployment endpoint, and the matching API key. The endpoint should end in `/api/v1`, for example:

```text
https://<your-modal-endpoint>/api/v1
```

Each Modal deployment has its own endpoint and key. The key is encrypted at rest and is never returned to the browser. Add, edit, or delete named accounts in Settings, and mark one account as the default; the Generate screen lets you choose the account separately for each 30-second project or single clip. Existing jobs retain the encrypted credential snapshot captured at submission. The supplied Modal service currently exposes one static model, `modal/wan2.2-lightning-a14b`, with durations from 1 to 15 seconds, 480p/720p, 9:16/16:9, and no generated audio. Its API does not report a video usage charge; Modal infrastructure usage is billed separately by Modal.

## Dashboard behavior

For OpenRouter, the model list includes provider-supplied per-second pricing where available, such as `$0.50/sec`; otherwise it shows `From …/sec` or `Price unavailable`. The dashboard shows estimates only when the active provider supplies pricing. Modal's API returns no per-video price, so its compute cost is billed separately by Modal. History stores the cost value reported by the selected provider; confirm the provider's billing dashboard for actual charges.

Jobs are stored before provider polling begins. The background worker keeps polling and downloads completed video into `/data/videos`, so closing the dashboard does not cancel a job. Live job views and History show persisted, application-normalized event messages and progress for project and single-clip jobs. Failure errors are stored with the job and remain visible on its History card. History also includes the prompt, model, date, duration, status, cost, local playback/download, and manual deletion. Deletion removes the corresponding database record and local video file. Files are retained indefinitely until deleted in History.

Completed videos can be moved from History into the separate Vault tab; this hides them from regular History without moving or copying their files. Set the four-digit Vault code in Settings first. Enter the code to list, play, or download Vault videos. The Vault locks when you refresh the page or choose **Lock Vault**. Change the code in Settings with the current code; keep it safe, because a forgotten code cannot be recovered in this version. Returning a Vault video restores it to History.

## Verification

Confirm the image, container health, and protected dashboard:

```bash
docker compose up -d --build
docker compose ps
docker compose exec video-automation wget -q -O - http://127.0.0.1:8080/health
```

`docker compose ps` should show the service as healthy. Open the dashboard in a browser, sign in, change the initial password, save and test the OpenRouter key, choose a video provider, and test its connection. Add a Modal account before selecting one for a submission. OpenRouter video calls may be paid; Modal infrastructure usage is billed separately, and model availability/pricing can change.

For non-Docker development, set `DATA_DIR` to a writable directory and run `go run .`; the server listens on `0.0.0.0:8080`. The Go server has no port override.
