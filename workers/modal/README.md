# SkyReels V2 Modal worker

This worker adapts the video-generation implementation from [`darkweb19/skyreel-modal`](https://github.com/darkweb19/skyreel-modal), source file `skyreel-model.py` at commit `a7676dc46a7f4084552cdbe80bc93de6c02a9add`, for this application's Modal provider contract. The source repository is unchanged.

The worker runs SkyReels V2 T2V 14B from `Skywork/SkyReels-V2-T2V-14B-720P-Diffusers`. It provides video generation only; it does not host a story or script LLM. The dashboard continues to use OpenRouter for story, script, and random-prompt generation.

## Deploy

Create a Modal secret named `video-api-secret` with a strong `MODAL_VIDEO_API_KEY`, then deploy from this repository's root:

```bash
modal secret create video-api-secret MODAL_VIDEO_API_KEY="<your-random-api-key>"
modal deploy workers/modal/skyreels.py
```

The app uses the dedicated `skyreels-v2-model-cache` and `skyreels-v2-video-generation-jobs` Modal volumes. The model cache persists downloaded weights, and the jobs volume persists accepted job state and generated files across worker restarts. The worker app is named `skyreels-v2-video-generation` and publishes the API under the `/api/v1` prefix.

In the dashboard's **Settings > Modal accounts**, add the deployed endpoint and the same API key. Use the `/api/v1` endpoint base, for example:

```text
https://<your-skyreels-v2-modal-endpoint>/api/v1
```

The API key is encrypted by the dashboard and is sent only from the Go server to Modal as a Bearer token.

## Asynchronous callbacks

Deploy the updated worker when using the dashboard's callback-based Modal workflow. Go submits its durable generation ID plus a callback URL and per-job bearer token. The CPU supervisor waits for the GPU call to finish, persists its terminal result to the Modal jobs volume, then sends an authenticated status callback to the dashboard. GPU startup failures are handled by the supervisor too. Modal retries the supervisor up to 240 times with exponential backoff capped at 60 seconds. Each callback request has a five-second timeout and up to four delivery attempts (0, 0.25, 0.75, and 1.5-second delays); only transient network errors and HTTP 408, 425, 429, and 5xx responses are retried. Callback jobs are idempotent: retrying the same ID replays saved terminal status and never queues another GPU run.

Set `VIDEO_CALLBACK_BASE_URL` in the Go server environment to the dashboard's publicly reachable HTTPS origin only (no path, query, fragment, or credentials). The server appends `/api/video-callbacks/{generationID}`. For Docker Compose, set it on the host before starting the app:

```bash
VIDEO_CALLBACK_BASE_URL=https://video.example.com docker compose up -d --build
```

For a local Go server, set the same process environment variable and use a public HTTPS tunnel or proxy. It is required only for Modal generation; OpenRouter and application startup do not depend on it. If a terminal callback cannot be delivered, the Modal supervisor retries the persisted outcome with bounded backoff; the Go server retries ambiguous submissions with that same ID. After eight hours without a callback, Go records a visible timeout error. Successful callbacks move the local generation to download, and completed project scenes are combined locally into the existing five-scene, 30-second output.

The worker uses the durable shared Modal Dict `video-generation-job-claims` to prevent repeated callback IDs from queuing duplicate GPU work. The `request:{generationID}` and `gpu:{generationID}` entries contain only request fingerprints, dispatch state, and Modal call IDs; callback tokens are not stored there or in worker job JSON/logs. If a spawn acknowledgment is ambiguous, the durable job stays pending; the worker will not redispatch that ID and its supervisor retries check for an eventual saved result. Go records an actionable missing-callback failure after eight hours. Legacy requests that omit callback fields continue to use the existing status-polling API.

## API and model limits

The worker implements the same authenticated video routes used by the existing Modal client: `GET /videos/models`, `POST /videos`, `GET /videos/{id}`, and `GET /videos/{id}/content?index=0`, relative to the configured `/api/v1` endpoint. Content requests support byte ranges so the dashboard can stream and resume MP4 playback.

Model capabilities are returned at runtime. SkyReels V2 advertises durations from 1 to 6 seconds, 480p and 720p, 9:16 and 16:9, and no generated audio. The 30-second project workflow requires five six-second 480p 9:16 scenes, which this model supports. The model does not publish catalog `pricing_skus`, so the dashboard cannot show a pre-generation estimate. Completed jobs report GPU runtime telemetry, not a USD cost; Modal's final workspace bill is calculated separately.

## Local route tests

Run the offline API tests without deploying or loading model weights:

```bash
python -B -m unittest discover -s workers/modal -p "test_skyreels.py"
```

The tests use Python's standard `unittest` plus the Modal SDK, FastAPI, and HTTPX. They exercise the local ASGI app and do not deploy or run video generation.
