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

Before submitting a SkyReels job, set the dashboard's `PUBLIC_BASE_URL` to its externally reachable HTTPS origin. The dashboard sends a fresh `callback_url` and `callback_token` with every video request. SkyReels requires both fields, writes its terminal job state first, then sends a bounded-retry HTTPS callback for completion or failure. This keeps Modal jobs progressing after the browser closes without frequent provider polling. Do not put callback tokens in a Modal secret, browser setting, proxy log, or source file.

## API and model limits

The worker implements the same authenticated video routes used by the existing Modal client: `GET /videos/models`, `POST /videos`, `GET /videos/{id}`, and `GET /videos/{id}/content?index=0`, relative to the configured `/api/v1` endpoint. `POST /videos` requires a public HTTPS callback URL and an opaque callback token supplied by the dashboard; these private fields are never included in status responses. Content requests support byte ranges so the dashboard can stream and resume MP4 playback.

Model capabilities are returned at runtime. SkyReels V2 advertises durations from 1 to 6 seconds, 480p and 720p, 9:16 and 16:9, and no generated audio. The 30-second project workflow requires five six-second 480p 9:16 scenes, which this model supports. The model does not publish catalog `pricing_skus`, so the dashboard cannot show a pre-generation estimate. Completed jobs report GPU runtime telemetry, not a USD cost; Modal's final workspace bill is calculated separately.

## Local route tests

Run the offline API tests without deploying or loading model weights:

```bash
python -B -m unittest discover -s workers/modal -p "test_skyreels.py"
```

The tests use Python's standard `unittest` plus the Modal SDK, FastAPI, and HTTPX. They exercise the local ASGI app and callback retry/redirect handling without deploying or running video generation.
