# Modal video workers

FrameVault supplies two separately deployed Modal video-generation providers and an opt-in Modal worker for AI clipping analysis. The video providers are:

| Worker | Source | Modal app | Endpoint base | Persistent volumes |
| --- | --- | --- | --- | --- |
| Wan 2.2 Lightning T2V | `video.py` | `video-generation` | `/api/v1` | `wan22-model-cache`, `video-generation-jobs` |
| SkyReels V2 T2V 14B | `skyreels.py` | `skyreels-v2-video-generation` | `/api/v1` | `skyreels-v2-model-cache`, `skyreels-v2-video-generation-jobs` |

Each video provider has its own Modal app, model cache, and job volume. Both use the Modal secret `video-api-secret` with `MODAL_VIDEO_API_KEY`. SkyReels adapts [`darkweb19/skyreel-modal`](https://github.com/darkweb19/skyreel-modal), source file `skyreel-model.py` at commit `a7676dc46a7f4084552cdbe80bc93de6c02a9add`; that upstream source is unchanged.

## Python module inventory

These are the non-test Python modules in this directory:

| File | Role |
| --- | --- |
| `video.py` | Deployable Wan 2.2 Lightning text-to-video Modal app. |
| `skyreels.py` | Deployable SkyReels V2 text-to-video Modal app. |
| `clipping_m3_worker.py` | Deployable, opt-in M3 clipping Modal app. It handles versioned dispatch, scoped source download, analysis execution, and callback delivery/replay. |
| `clipping_analysis.py` | Analysis library used by the M3 worker. It transcribes audio with faster-whisper and derives audio/visual events and profile-aware regions for denser inspection; Go performs persisted candidate selection and ranking. |
| `clipping_protocol.py` | Provider-neutral v2 dispatch validation, idempotency ledger, and callback primitives used by the M3 worker; it performs no model inference and is not a separate Modal app. |

The M3 clipping worker uses a pinned faster-whisper ASR model plus deterministic media analysis. It does not call an LLM; OpenRouter story and script generation is a separate application workflow. For the clipping architecture and operating limits, see [ADR-004](../../docs/architecture/004-ai-clipping.md), [M2 implementation](../../docs/clipping/M2-IMPLEMENTATION.md), [M3 setup and limitations](../../docs/clipping/M3-IMPLEMENTATION.md), and [YouTube source import](../../docs/clipping/YOUTUBE-IMPORT.md).

## Deploy

From the repository root, create the shared API secret once, then deploy the provider app or apps you intend to use:

```bash
modal secret create video-api-secret MODAL_VIDEO_API_KEY="<your-random-api-key>"
modal deploy workers/modal/video.py
modal deploy workers/modal/skyreels.py
```

In the dashboard's **Settings > Modal accounts**, register each deployed endpoint with the same API key. Use the `/api/v1` endpoint base, for example `https://<your-modal-endpoint>/api/v1`. The dashboard encrypts the API key and sends it only from the Go server to Modal as a Bearer token.

Deploy the worker version before the Go callback receiver. Then configure the Go service's `VIDEO_CALLBACK_BASE_URL` to its externally reachable HTTPS origin (without a path); `PUBLIC_BASE_URL` remains a compatibility fallback when `VIDEO_CALLBACK_BASE_URL` is unset. The Go app constructs each job's callback endpoint and sends the URL and per-job token to the worker. No callback base URL or callback secret belongs in Modal environment variables. This worker-first order keeps the previous static callback contract available during rollout.

## Callback and compatibility behavior

For new submissions, the Go app sends `job_id`, `callback_url`, and `callback_token` with the generation request. The worker validates the HTTPS callback target and token, reserves the client-assigned ID idempotently, and returns that same ID with `202 Accepted`. Reusing an ID with the same request does not create a second GPU generation; changing the request for a reserved ID is rejected.

Callbacks are sent to `POST /api/video-callbacks/{job_id}` with `Authorization: Bearer <callback_token>` and a JSON event containing `id`, `status`, `sequence`, `progress`, and `stage`, plus `error_code` for failures. The worker sends `processing` at sequence 1 and a persisted `completed` or `failed` result at sequence 2. The token remains in the worker's private durable job record; it is not included in status responses, callback bodies, Modal Dict claims, or worker logs. Redirects are refused so the bearer token cannot be forwarded to another host. Each HTTP delivery uses at most five bounded attempts, and the CPU supervisor reattaches/replays durable outcomes with up to 240 Modal retries and a maximum 60-second backoff delay.

The workers also continue accepting the previous Go submission shape with only `callback_url` and `callback_token`; it uses the legacy `/api/provider-callbacks/modal` endpoint and `X-Modal-Callback-Token` header. Requests with no callback fields remain supported for existing clients that poll status. Legacy callbacks are terminal-only; new jobs should use the stable `job_id` contract.

## API and model limits

Both workers implement authenticated `GET /videos/models`, `POST /videos`, `GET /videos/{id}`, and `GET /videos/{id}/content?index=0` routes relative to the configured `/api/v1` base. Job metadata and generated files persist in the app-specific Modal jobs volume. Status responses select only public job fields, and content supports byte ranges for streaming and resume.

Both providers support five six-second 480p 9:16 scenes for the default 30-second project workflow. Wan advertises durations from 1 to 15 seconds; SkyReels advertises 1 to 6 seconds, 480p/720p, and 9:16/16:9, with no generated audio. Neither Modal worker publishes a catalog `pricing_skus`; do not present a fabricated pre-generation estimate. Modal account billing is separate from measured per-job GPU runtime telemetry.

## AI clipping worker (M3)

The M3 app in `clipping_m3_worker.py` is separate from the two video-generation apps above. It is an opt-in analysis worker and is not needed to generate videos. Follow [M3 operator setup](../../docs/clipping/M3-IMPLEMENTATION.md) for its dedicated Modal secrets, immutable Hugging Face model revision, matching `FRAMEVAULT_PIPELINE_REVISION`, model-cache setup and signed-in **Clipping > Worker settings** configuration. The operator must prewarm the pinned model snapshot before analysis; prewarming downloads model weights. The analysis worker loads the prewarmed local snapshot and does not fetch model files during a job.

Transcription, event detection, and candidate inspection are heuristic inputs to the Go selection flow. They do not provide semantic video understanding or an LLM-generated clip judgment. The M3 tests use local fixtures and mocks; they do not deploy the app, download model weights, or make hosted inference calls.

## Offline checks

Run these from the repository root. They do not deploy an app, load model weights, or submit a paid generation:

```bash
python -B workers/modal/callback_delivery_test.py
python -B -m unittest discover -s workers/modal -p "test_skyreels.py"
python -B -m unittest discover -s workers/modal -p "test_clipping_*.py"
```

The dependency-free callback delivery check uses the Python standard library. The SkyReels ASGI suite additionally uses the local Modal SDK, FastAPI, and HTTPX. The M3 suites use the worker dependencies in [`requirements-clipping.txt`](requirements-clipping.txt); FFmpeg/ffprobe fixture checks are skipped when those binaries are unavailable.
