# OpenRouter Video Automation

A self-hosted dashboard for submitting OpenRouter video jobs, tracking their status after the browser closes, and storing completed MP4 files locally in a Docker-managed volume. It uses SQLite for metadata and no external storage service.

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

Change this password immediately in **Settings**. Then add and test the OpenRouter API key in **Settings**. No API-key environment variable or `.env` file is required.

The container is configured with `restart: unless-stopped`; it resumes outstanding jobs after a restart. Check its logs with:

```bash
docker compose logs -f video-automation
```

Stop it without deleting data:

```bash
docker compose down
```

The app is deliberately bound to `127.0.0.1` (localhost), not all network interfaces. To use a different local port, set `APP_PORT` for the command, for example `APP_PORT=9000 docker compose up -d`. On PowerShell use `$env:APP_PORT = "9000"` first.

## Data persistence and backups

The named Docker volume `video_automation_data` is the only persistent storage location. The app does not use browser local storage for application data, a host bind mount, or an external object store.

Within that volume:

| Path | Contents |
| --- | --- |
| `/data/app.db` | SQLite users, encrypted API-key setting, jobs, statuses, and cost records |
| `/data/secret.key` | Encryption key for the API-key setting |
| `/data/videos/` | Downloaded completed MP4 files |

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

## Dashboard behavior

The model list includes OpenRouter-provided per-second pricing where available, such as `$0.50/sec`; otherwise it shows `From …/sec` or `Price unavailable`. Before submission the dashboard may show an estimated price based on selected duration and published pricing. Once a job completes, History stores and displays OpenRouter's final reported `usage.cost`; that actual charge can differ from the estimate.

Jobs are stored before provider polling begins. The background worker keeps polling and downloads completed video into `/data/videos`, so closing the dashboard does not cancel a job. History includes the prompt, model, date, duration, job status, cost, local playback/download, and manual deletion. Deletion removes the corresponding database record and local video file. Files are retained indefinitely until deleted in History.

## Verification

Confirm the image, container health, and protected dashboard:

```bash
docker compose up -d --build
docker compose ps
docker compose exec video-automation wget -q -O - http://127.0.0.1:8080/health
```

`docker compose ps` should show the service as healthy. Open the dashboard in a browser, sign in, change the initial password, save and test an OpenRouter key, and create a low-cost test generation. Provider calls are paid and model availability/pricing can change.

For non-Docker development, set `DATA_DIR` to a writable directory and run `go run .`; the app listens on port 8080.
