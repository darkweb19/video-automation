"""SkyReels V2 text-to-video worker for the FrameVault Modal provider.

Adapted from https://github.com/darkweb19/skyreel-modal at commit
a7676dc46a7f4084552cdbe80bc93de6c02a9add. The upstream repository did not
include a license file. This worker keeps its SkyReels inference setup while
exposing the provider-neutral `/api/v1/videos` API consumed by this project.
"""

import json
import logging
import os
import re
import secrets
import subprocess
import time
import uuid
from datetime import datetime, timezone
from pathlib import Path
from typing import Any
from urllib import error as urllib_error
from urllib import request as urllib_request
from urllib.parse import urlparse

import modal


# ---------------------------------------------------------------------------
# Deployment and model configuration
# ---------------------------------------------------------------------------

APP_NAME = "skyreels-v2-video-generation"

MODEL_ID = "modal/skyreels-v2-t2v-14b"
MODEL_NAME = "SkyReels V2 T2V 14B"
HF_MODEL_ID = "Skywork/SkyReels-V2-T2V-14B-720P-Diffusers"

MODEL_CACHE = "/models"
JOBS_DIR = "/jobs"

KEEP_WARM_SECONDS = 120
FPS = 24

MIN_DURATION = 1
MAX_DURATION = 6

# SkyReels' documented/recommended text-to-video settings.
NUM_INFERENCE_STEPS = 50
GUIDANCE_SCALE = 6.0
FLOW_SHIFT = 8.0

STATUS_POLL_RETRY_SECONDS = 2
MAX_JSON_BODY_BYTES = 32 << 10
MAX_PROMPT_LENGTH = 4000
MAX_ERROR_LENGTH = 1000
MAX_CALLBACK_URL_LENGTH = 2048

# Callback delivery is bounded. The dashboard has a sparse, persisted recovery
# check for the exceptional case where every delivery attempt fails.
CALLBACK_DELIVERY_MAX_ATTEMPTS = 5
CALLBACK_DELIVERY_TIMEOUT_SECONDS = 10
CALLBACK_DELIVERY_MAX_RETRY_AFTER_SECONDS = 15

SUPPORTED_RESOLUTIONS = ["480p", "720p"]
SUPPORTED_ASPECT_RATIOS = ["9:16", "16:9"]

SIZE_MAP = {
    ("480p", "9:16"): (480, 832),
    ("480p", "16:9"): (832, 480),
    ("720p", "9:16"): (720, 1280),
    ("720p", "16:9"): (1280, 720),
}

SAFE_JOB_ID = re.compile(r"^[A-Za-z0-9_-]{1,200}$")
CREATE_VIDEO_FIELDS = {
    "model",
    "prompt",
    "duration",
    "resolution",
    "aspect_ratio",
    "generate_audio",
    "callback_url",
    "callback_token",
    # Seed is an intentionally supported Modal extension. The Go application
    # does not currently send it, but retaining it preserves upstream use.
    "seed",
}


# ---------------------------------------------------------------------------
# Modal resources
# ---------------------------------------------------------------------------

app = modal.App(APP_NAME)

model_volume = modal.Volume.from_name(
    "skyreels-v2-model-cache",
    create_if_missing=True,
)
jobs_volume = modal.Volume.from_name(
    "skyreels-v2-video-generation-jobs",
    create_if_missing=True,
)
api_secret = modal.Secret.from_name(
    "video-api-secret",
    required_keys=["MODAL_VIDEO_API_KEY"],
)

gpu_image = (
    modal.Image.debian_slim(python_version="3.11")
    .apt_install("ffmpeg", "git")
    .pip_install(
        "torch==2.6.0",
        "torchvision==0.21.0",
        "diffusers>=0.35.0",
        "transformers>=4.49.0",
        "accelerate>=1.2.0",
        "huggingface_hub>=0.29.0",
        "safetensors",
        "sentencepiece",
        "protobuf",
        "numpy",
        "pillow",
        "imageio",
        "imageio-ffmpeg",
        "ftfy",
    )
)

# The API is deliberately a separate CPU-only image. GPU containers are used
# exclusively by VideoGenerator.
api_image = modal.Image.debian_slim(python_version="3.11").pip_install(
    "fastapi[standard]",
)


# ---------------------------------------------------------------------------
# Durable job metadata
# ---------------------------------------------------------------------------

def utc_now() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="seconds")


def log_event(event: str, **fields: object) -> None:
    """Write compact operational logs without prompts or credentials."""

    pieces = [utc_now(), event]
    for key, value in fields.items():
        if value is not None:
            pieces.append(f"{key}={value}")
    print(" | ".join(pieces), flush=True)


def public_error_message(value: object, fallback: str = "Generation failed") -> str:
    """Keep API/job errors useful without persisting unbounded exception text."""

    if not isinstance(value, str):
        return fallback
    message = " ".join(value.split())
    return message[:MAX_ERROR_LENGTH] if message else fallback


def safe_job_id(value: object) -> bool:
    return isinstance(value, str) and SAFE_JOB_ID.fullmatch(value) is not None


def callback_url_value(value: object) -> str | None:
    """Return a safe, normalized callback URL or ``None``.

    The dashboard creates this value from its configured public origin. The
    worker still validates it because the callback capability must never be
    redirected to a different destination.
    """

    if not isinstance(value, str):
        return None
    callback_url = value.strip()
    if not callback_url or len(callback_url) > MAX_CALLBACK_URL_LENGTH:
        return None
    try:
        parsed = urlparse(callback_url)
        _ = parsed.port
    except (TypeError, ValueError):
        return None
    if (
        parsed.scheme != "https"
        or not parsed.hostname
        or parsed.username is not None
        or parsed.password is not None
        or parsed.query
        or parsed.fragment
    ):
        return None
    return callback_url


def valid_callback_token(value: object) -> bool:
    return (
        isinstance(value, str)
        and 43 <= len(value) <= 128
        and all(
            character.isascii()
            and (character.isalnum() or character in "-_")
            for character in value
        )
    )


def detect_runtime_gpu_name() -> str:
    """Return the actual GPU supplied by Modal's fallback allocation."""

    try:
        names = subprocess.check_output(
            [
                "nvidia-smi",
                "--query-gpu=name",
                "--format=csv,noheader",
            ],
            text=True,
            stderr=subprocess.DEVNULL,
            timeout=3,
        ).strip().splitlines()
    except Exception:
        return "unknown"

    if not names:
        return "unknown"
    name = names[0].strip()
    if "L40S" in name:
        return "L40S"
    if "A100" in name:
        return "A100-80GB"
    if "H100" in name:
        return "H100"
    return name


def job_json_path(job_id: str) -> Path:
    if not safe_job_id(job_id):
        raise ValueError("invalid job id")
    return Path(JOBS_DIR, f"{job_id}.json")


def job_video_path(job_id: str) -> Path:
    if not safe_job_id(job_id):
        raise ValueError("invalid job id")
    return Path(JOBS_DIR, f"{job_id}.mp4")


def job_call_path(job_id: str) -> Path:
    if not safe_job_id(job_id):
        raise ValueError("invalid job id")
    return Path(JOBS_DIR, f"{job_id}.call.json")


def _write_json_file(path: Path, data: dict[str, Any]) -> None:
    """Atomically replace a metadata file before committing the Modal volume."""

    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(f".{path.name}.{uuid.uuid4().hex}.tmp")
    try:
        temporary.write_text(
            json.dumps(data, ensure_ascii=False, indent=2),
            encoding="utf-8",
        )
        os.replace(temporary, path)
    finally:
        # A failed write leaves no stable partial record. Unique temporary
        # names also avoid collisions between API and worker processes.
        try:
            temporary.unlink(missing_ok=True)
        except OSError:
            pass


def _write_job_file(job_id: str, data: dict[str, Any]) -> None:
    _write_json_file(job_json_path(job_id), data)


def write_job(job_id: str, data: dict[str, Any]) -> None:
    _write_job_file(job_id, data)
    jobs_volume.commit()


async def write_job_async(job_id: str, data: dict[str, Any]) -> None:
    _write_job_file(job_id, data)
    await jobs_volume.commit.aio()


async def write_modal_call_id_async(job_id: str, call_id: str) -> None:
    """Persist dispatch metadata without ever rewriting the job status file."""

    if not safe_job_id(job_id) or not isinstance(call_id, str) or not call_id:
        raise ValueError("invalid dispatch metadata")
    _write_json_file(job_call_path(job_id), {"modal_call_id": call_id})
    await jobs_volume.commit.aio()


def read_job(job_id: str) -> dict[str, Any] | None:
    if not safe_job_id(job_id):
        raise ValueError("invalid job id")
    jobs_volume.reload()
    path = job_json_path(job_id)
    if not path.is_file():
        return None
    payload = json.loads(path.read_text(encoding="utf-8"))
    return payload if isinstance(payload, dict) else None


async def read_job_async(job_id: str) -> dict[str, Any] | None:
    if not safe_job_id(job_id):
        raise ValueError("invalid job id")
    await jobs_volume.reload.aio()
    path = job_json_path(job_id)
    if not path.is_file():
        return None
    payload = json.loads(path.read_text(encoding="utf-8"))
    return payload if isinstance(payload, dict) else None


async def read_modal_call_id_async(job_id: str) -> str | None:
    if not safe_job_id(job_id):
        raise ValueError("invalid job id")
    await jobs_volume.reload.aio()
    path = job_call_path(job_id)
    if not path.is_file():
        return None
    payload = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(payload, dict):
        return None
    call_id = payload.get("modal_call_id")
    return call_id if isinstance(call_id, str) and call_id else None


async def reconcile_modal_call(job: dict[str, Any]) -> dict[str, Any]:
    """Turn failures before `generate` starts into a durable failed status.

    The GPU class can fail before the method body runs (scheduling, snapshot
    restore, model download, or `@modal.enter`). A separate immutable dispatch
    metadata file stores the FunctionCall id after spawn. Keeping it out of the
    mutable job record avoids an API write replacing newer worker progress.
    """

    if job.get("status") in {"completed", "failed"}:
        return job
    job_id = job.get("id")
    if not safe_job_id(job_id):
        return job
    call_id = await read_modal_call_id_async(job_id)
    if call_id is None:
        return job

    try:
        call = modal.FunctionCall.from_id(call_id)
        await call.get.aio(timeout=0)
    except TimeoutError:
        return job
    except (
        modal.exception.ClientClosed,
        modal.exception.ConnectionError,
        modal.exception.InternalError,
        modal.exception.InternalFailure,
        modal.exception.ResourceExhaustedError,
        modal.exception.ServiceError,
        OSError,
    ) as error:
        # A status poll must not convert a transient SDK/control-plane failure
        # into a permanent generation failure. The durable job stays pending
        # and the next normal processor poll tries again.
        log_event(
            "modal_call_status_deferred",
            job=job_id,
            error_type=type(error).__name__,
        )
        return job
    except Exception as error:
        # Reload before writing so a completed job is never replaced by a
        # delayed lifecycle check from a separate API request.
        current = await read_job_async(job_id)
        if current is not None and current.get("status") in {"completed", "failed"}:
            return current
        failed = current if current is not None else job
        failed.update(
            {
                "status": "failed",
                "stage": "worker_failed",
                "progress": 0,
                "error": "Video generation worker failed. Check Modal logs for details.",
                "completed_at": int(time.time()),
            }
        )
        await write_job_async(job_id, failed)
        queue_terminal_callback(
            job_id,
            "failed",
            str(failed.get("model") or MODEL_ID),
            failed.get("callback_url"),
            failed.get("callback_token"),
        )
        log_event(
            "modal_worker_failed",
            job=job_id,
            call_id=call_id,
            error_type=type(error).__name__,
        )
        return failed

    return job


def terminal_callback_payload(
    job_id: str,
    status: str,
    model: str,
    cost: float | None = None,
) -> dict[str, Any]:
    """Build the small terminal callback contract without credentials."""

    payload: dict[str, Any] = {"id": job_id, "status": status, "model": model}
    if status == "completed" and cost is not None:
        payload["cost_usd"] = f"{cost:.6f}"
    elif status != "completed":
        # Keep GPU exceptions in Modal logs. The dashboard maps this stable
        # message to the job without persisting worker internals.
        payload["error"] = "Video generation failed"
    return payload


class CallbackNoRedirectHandler(urllib_request.HTTPRedirectHandler):
    """Keep the callback capability bound to its configured endpoint."""

    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def open_callback_request(request, timeout):
    return urllib_request.build_opener(CallbackNoRedirectHandler()).open(
        request,
        timeout=timeout,
    )


@app.function(image=api_image, timeout=120)
def deliver_terminal_callback(
    callback_url: str,
    callback_token: str,
    payload: dict[str, Any],
) -> None:
    """POST a terminal result with bounded retries and no secret logging."""

    body = json.dumps(payload, separators=(",", ":")).encode("utf-8")
    for attempt in range(1, CALLBACK_DELIVERY_MAX_ATTEMPTS + 1):
        retry_after = None
        try:
            outbound = urllib_request.Request(
                callback_url,
                data=body,
                method="POST",
                headers={
                    "Content-Type": "application/json",
                    "Accept": "application/json",
                    "X-Modal-Callback-Token": callback_token,
                },
            )
            with open_callback_request(
                outbound,
                timeout=CALLBACK_DELIVERY_TIMEOUT_SECONDS,
            ) as response:
                status_code = response.getcode()
                retry_after = response.headers.get("Retry-After")

            if 200 <= status_code < 300:
                log_event("callback_delivered", job=payload.get("id"), attempt=attempt)
                return
            if 300 <= status_code < 500 and status_code != 429:
                log_event("callback_rejected", job=payload.get("id"), status=status_code)
                return
        except urllib_error.HTTPError as error:
            status_code = error.code
            retry_after = error.headers.get("Retry-After") if error.headers else None
            try:
                error.close()
            except Exception:
                pass
            if 300 <= status_code < 500 and status_code != 429:
                log_event("callback_rejected", job=payload.get("id"), status=status_code)
                return
        except Exception:
            # Network details can contain callback credentials, so log only
            # the terminal outcome after retries are exhausted.
            pass

        if attempt == CALLBACK_DELIVERY_MAX_ATTEMPTS:
            break
        delay_seconds = min(2 ** (attempt - 1), CALLBACK_DELIVERY_MAX_RETRY_AFTER_SECONDS)
        try:
            if retry_after is not None:
                delay_seconds = min(
                    max(1, int(retry_after)),
                    CALLBACK_DELIVERY_MAX_RETRY_AFTER_SECONDS,
                )
        except (TypeError, ValueError):
            pass
        time.sleep(delay_seconds)

    log_event(
        "callback_delivery_exhausted",
        job=payload.get("id"),
        attempts=CALLBACK_DELIVERY_MAX_ATTEMPTS,
    )


def schedule_terminal_callback(
    job_id: str,
    callback_url: str,
    callback_token: str,
    payload: dict[str, Any],
) -> None:
    """Queue callback delivery only after the terminal job record is durable."""

    try:
        deliver_terminal_callback.spawn(callback_url, callback_token, payload)
        log_event("callback_delivery_scheduled", job=job_id)
    except Exception:
        # The dashboard's persisted recovery check handles an unscheduled
        # callback. Never include the URL or token in logs.
        log_event("callback_delivery_schedule_failed", job=job_id)


def queue_terminal_callback(
    job_id: str,
    status: str,
    model: str,
    callback_url: object,
    callback_token: object,
    cost: float | None = None,
) -> None:
    """Validate private callback credentials and schedule one terminal event."""

    normalized_url = callback_url_value(callback_url)
    if normalized_url is None or not valid_callback_token(callback_token):
        log_event("callback_delivery_unavailable", job=job_id)
        return
    schedule_terminal_callback(
        job_id,
        normalized_url,
        callback_token,
        terminal_callback_payload(job_id, status, model, cost),
    )


def duration_to_frames(duration: int) -> int:
    """Return a 4n+1 SkyReels/Wan VAE-compatible frame count at 24 FPS."""

    target = duration * FPS
    frames = 4 * round((target - 1) / 4) + 1
    max_compatible_frames = 4 * round((MAX_DURATION * FPS - 1) / 4) + 1
    return max(25, min(frames, max_compatible_frames))


# ---------------------------------------------------------------------------
# GPU inference worker
# ---------------------------------------------------------------------------

@app.cls(
    image=gpu_image,
    gpu=["L40S", "A100-80GB", "H100"],
    volumes={MODEL_CACHE: model_volume, JOBS_DIR: jobs_volume},
    min_containers=0,
    max_containers=1,
    scaledown_window=KEEP_WARM_SECONDS,
    timeout=3600,
    startup_timeout=1800,
    enable_memory_snapshot=True,
    experimental_options={"enable_gpu_snapshot": True},
)
class VideoGenerator:
    @modal.enter(snap=True)
    def load_model(self) -> None:
        """Download/cache and load SkyReels once for the warm GPU container."""

        import torch
        from diffusers import AutoencoderKLWan, SkyReelsV2Pipeline, UniPCMultistepScheduler
        from huggingface_hub import snapshot_download

        startup_started = time.time()
        self.runtime_gpu_name = detect_runtime_gpu_name()
        log_event("worker_starting", model=MODEL_NAME, gpu=self.runtime_gpu_name)

        model_path = Path(MODEL_CACHE, "skyreels-v2-14b")
        sentinel = model_path / ".download_complete"
        if sentinel.exists():
            log_event("model_cache_hit")
        else:
            log_event("model_cache_miss")
            model_path.mkdir(parents=True, exist_ok=True)
            snapshot_download(repo_id=HF_MODEL_ID, local_dir=str(model_path))
            sentinel.write_text("complete", encoding="utf-8")
            model_volume.commit()

        log_event("model_loading", component="vae")
        vae = AutoencoderKLWan.from_pretrained(
            str(model_path),
            subfolder="vae",
            torch_dtype=torch.float32,
        )

        log_event("model_loading", component="pipeline")
        self.pipe = SkyReelsV2Pipeline.from_pretrained(
            str(model_path),
            vae=vae,
            torch_dtype=torch.bfloat16,
        )
        self.pipe.scheduler = UniPCMultistepScheduler.from_config(
            self.pipe.scheduler.config,
            flow_shift=FLOW_SHIFT,
        )

        # This is retained from the upstream deployment to make the 14B model
        # practical across the requested L40S/A100/H100 fallback choices.
        self.pipe.enable_model_cpu_offload()
        try:
            self.pipe.vae.enable_tiling()
        except Exception:
            pass

        self.torch = torch
        log_event("model_ready", seconds=round(time.time() - startup_started, 2))

    @modal.method()
    def generate(
        self,
        job_id: str,
        prompt: str,
        model: str,
        duration: int,
        resolution: str,
        aspect_ratio: str,
        seed: int,
        callback_url: str,
        callback_token: str,
    ) -> None:
        """Generate, encode, and persist one MP4 without exposing it publicly."""

        from diffusers.utils import export_to_video

        if not safe_job_id(job_id):
            raise ValueError("invalid job id")

        started_at = time.time()
        previous = read_job(job_id) or {}
        job: dict[str, Any] = {
            "id": job_id,
            "status": "in_progress",
            "stage": "preparing",
            "model": model,
            "progress": 5,
            "prompt": prompt,
            "duration": duration,
            "resolution": resolution,
            "aspect_ratio": aspect_ratio,
            "seed": seed,
            # Private worker metadata. The HTTP status response below selects
            # only public job fields, so callback credentials never leave this
            # provider boundary through the browser-facing dashboard.
            "callback_url": callback_url,
            "callback_token": callback_token,
            "created_at": previous.get("created_at", int(started_at)),
            "error": "",
        }
        write_job(job_id, job)

        try:
            width, height = SIZE_MAP[(resolution, aspect_ratio)]
            frame_count = duration_to_frames(duration)
            output_path = job_video_path(job_id)
            log_event(
                "generation_started",
                job=job_id,
                width=width,
                height=height,
                frames=frame_count,
                steps=NUM_INFERENCE_STEPS,
            )

            job.update({"stage": "inference", "progress": 20})
            write_job(job_id, job)

            generator = self.torch.Generator(device="cpu").manual_seed(seed)
            inference_started = time.time()
            result = self.pipe(
                prompt=prompt,
                height=height,
                width=width,
                num_frames=frame_count,
                num_inference_steps=NUM_INFERENCE_STEPS,
                guidance_scale=GUIDANCE_SCALE,
                generator=generator,
            )
            frames = result.frames[0]
            inference_seconds = time.time() - inference_started

            job.update(
                {
                    "stage": "encoding",
                    "progress": 90,
                    "inference_seconds": inference_seconds,
                }
            )
            write_job(job_id, job)

            encode_started = time.time()
            export_to_video(frames, str(output_path), fps=FPS, quality=8)
            encode_seconds = time.time() - encode_started
            del frames
            del result
            self.torch.cuda.empty_cache()

            if not output_path.is_file():
                raise RuntimeError("Generated video was not created")
            size_bytes = output_path.stat().st_size
            if size_bytes <= 0:
                raise RuntimeError("Generated MP4 is empty")

            # Commit the MP4 before exposing a completed job. The final
            # write_job commit includes the completed JSON record as well.
            jobs_volume.commit()
            total_seconds = time.time() - started_at
            job.update(
                {
                    "status": "completed",
                    "stage": "completed",
                    "progress": 100,
                    "filename": output_path.name,
                    "size_bytes": size_bytes,
                    "inference_seconds": inference_seconds,
                    "encode_seconds": encode_seconds,
                    "gpu_seconds": total_seconds,
                    "gpu_name": getattr(self, "runtime_gpu_name", "unknown"),
                    "completed_at": int(time.time()),
                    "error": "",
                }
            )
            write_job(job_id, job)
            queue_terminal_callback(
                job_id,
                "completed",
                model,
                callback_url,
                callback_token,
            )
            log_event(
                "generation_completed",
                job=job_id,
                inference_seconds=round(inference_seconds, 2),
                total_seconds=round(total_seconds, 2),
            )
        except Exception as error:
            job.update(
                {
                    "status": "failed",
                    "stage": "failed",
                    "error": "Video generation failed. Check the Modal worker logs.",
                    "completed_at": int(time.time()),
                }
            )
            write_job(job_id, job)
            queue_terminal_callback(
                job_id,
                "failed",
                model,
                callback_url,
                callback_token,
            )
            log_event(
                "generation_failed",
                job=job_id,
                error_type=type(error).__name__,
            )
            raise


# ---------------------------------------------------------------------------
# CPU-only, provider-compatible HTTP API
# ---------------------------------------------------------------------------

def _error_envelope(message: str) -> dict[str, dict[str, str]]:
    return {"error": {"message": message}}


async def _decode_create_request(request: Any, http_exception: Any) -> dict[str, Any]:
    content_type = request.headers.get("content-type", "").split(";", 1)[0].strip().lower()
    if content_type != "application/json":
        raise http_exception(status_code=415, detail="Content-Type must be application/json")

    chunks: list[bytes] = []
    body_size = 0
    try:
        async for chunk in request.stream():
            body_size += len(chunk)
            if body_size > MAX_JSON_BODY_BYTES:
                raise http_exception(status_code=413, detail="request body is too large")
            chunks.append(chunk)
    except Exception as error:
        if isinstance(error, http_exception):
            raise
        raise http_exception(status_code=400, detail="invalid request body")
    raw_body = b"".join(chunks)
    try:
        body = json.loads(raw_body)
    except (TypeError, UnicodeDecodeError, json.JSONDecodeError):
        raise http_exception(status_code=400, detail="invalid JSON")
    if not isinstance(body, dict):
        raise http_exception(status_code=400, detail="request body must be a JSON object")
    unknown_fields = set(body).difference(CREATE_VIDEO_FIELDS)
    if unknown_fields:
        raise http_exception(status_code=400, detail="request contains an unsupported field")
    return body


def _validated_create_options(body: dict[str, Any], http_exception: Any) -> dict[str, Any]:
    model = body.get("model", MODEL_ID)
    if not isinstance(model, str) or model != MODEL_ID:
        raise http_exception(status_code=400, detail="unsupported model")

    prompt = body.get("prompt")
    if not isinstance(prompt, str):
        raise http_exception(status_code=400, detail="prompt must be a string")
    prompt = prompt.strip()
    if not prompt:
        raise http_exception(status_code=400, detail="prompt is required")
    if len(prompt) > MAX_PROMPT_LENGTH:
        raise http_exception(
            status_code=400,
            detail=f"prompt must be at most {MAX_PROMPT_LENGTH} characters",
        )

    duration = body.get("duration", MAX_DURATION)
    if type(duration) is not int:
        raise http_exception(status_code=400, detail="duration must be an integer")
    if duration < MIN_DURATION or duration > MAX_DURATION:
        raise http_exception(
            status_code=400,
            detail=f"duration must be between {MIN_DURATION} and {MAX_DURATION} seconds",
        )

    resolution = body.get("resolution", "480p")
    if not isinstance(resolution, str):
        raise http_exception(status_code=400, detail="resolution must be a string")
    resolution = resolution.strip().lower()
    if resolution not in SUPPORTED_RESOLUTIONS:
        raise http_exception(status_code=400, detail="resolution must be '480p' or '720p'")

    aspect_ratio = body.get("aspect_ratio", "9:16")
    if not isinstance(aspect_ratio, str):
        raise http_exception(status_code=400, detail="aspect_ratio must be a string")
    aspect_ratio = aspect_ratio.strip()
    if aspect_ratio not in SUPPORTED_ASPECT_RATIOS:
        raise http_exception(status_code=400, detail="aspect_ratio must be '9:16' or '16:9'")

    generate_audio = body.get("generate_audio", False)
    if type(generate_audio) is not bool:
        raise http_exception(status_code=400, detail="generate_audio must be a boolean")
    if generate_audio:
        raise http_exception(status_code=400, detail="SkyReels V2 T2V does not generate audio")

    raw_seed = body.get("seed")
    if raw_seed is None:
        seed = secrets.randbelow(2**31 - 1)
    elif type(raw_seed) is not int:
        raise http_exception(status_code=400, detail="seed must be an integer")
    elif raw_seed < 0:
        seed = secrets.randbelow(2**31 - 1)
    else:
        seed = raw_seed

    if (resolution, aspect_ratio) not in SIZE_MAP:
        raise http_exception(status_code=400, detail="unsupported resolution and aspect ratio")

    return {
        "model": model,
        "prompt": prompt,
        "duration": duration,
        "resolution": resolution,
        "aspect_ratio": aspect_ratio,
        "seed": seed,
    }


def create_api() -> Any:
    """Build the FastAPI application; kept separate for lightweight ASGI tests."""

    from fastapi import FastAPI, HTTPException, Request, Response
    from fastapi.exceptions import RequestValidationError
    from fastapi.responses import FileResponse, JSONResponse
    from starlette.exceptions import HTTPException as StarletteHTTPException

    api_app = FastAPI(title="SkyReels V2 Video API")
    logger = logging.getLogger("skyreels.api")
    logging.getLogger("uvicorn.access").disabled = True

    @api_app.exception_handler(StarletteHTTPException)
    async def http_error_handler(_: Request, error: StarletteHTTPException) -> JSONResponse:
        message = public_error_message(error.detail, "request failed")
        return JSONResponse(
            status_code=error.status_code,
            content=_error_envelope(message),
            headers=error.headers,
        )

    @api_app.exception_handler(RequestValidationError)
    async def validation_error_handler(_: Request, __: RequestValidationError) -> JSONResponse:
        return JSONResponse(
            status_code=400,
            content=_error_envelope("invalid request"),
        )

    @api_app.exception_handler(Exception)
    async def unhandled_error_handler(_: Request, error: Exception) -> JSONResponse:
        # Do not serialize exception values because upstream clients and
        # platform errors may contain implementation-only details.
        logger.error("unhandled API error", extra={"error_type": type(error).__name__})
        return JSONResponse(
            status_code=500,
            content=_error_envelope("internal server error"),
        )

    def authenticate(request: Request) -> None:
        expected_key = os.environ.get("MODAL_VIDEO_API_KEY")
        if not expected_key:
            raise HTTPException(status_code=500, detail="server API key is not configured")
        authorization = request.headers.get("Authorization", "")
        try:
            authenticated = secrets.compare_digest(
                authorization.encode("utf-8"),
                f"Bearer {expected_key}".encode("utf-8"),
            )
        except UnicodeEncodeError:
            authenticated = False
        if not authenticated:
            raise HTTPException(
                status_code=401,
                detail="invalid API key",
                headers={"WWW-Authenticate": "Bearer"},
            )

    def require_safe_job_id(job_id: str) -> None:
        if not safe_job_id(job_id):
            raise HTTPException(status_code=400, detail="invalid generation id")

    def validate_callback(body: dict[str, Any]) -> tuple[str, str]:
        callback_url = callback_url_value(body.get("callback_url"))
        if callback_url is None:
            raise HTTPException(status_code=400, detail="callback_url must be a public HTTPS URL")
        callback_token = body.get("callback_token")
        if not valid_callback_token(callback_token):
            raise HTTPException(status_code=400, detail="callback_token is required")
        return callback_url, callback_token

    @api_app.get("/health")
    async def health() -> dict[str, str]:
        return {"status": "ok", "model": MODEL_ID}

    @api_app.get("/api/v1/videos/models")
    async def list_models(request: Request) -> dict[str, list[dict[str, Any]]]:
        authenticate(request)
        return {
            "data": [
                {
                    "id": MODEL_ID,
                    "name": MODEL_NAME,
                    "supported_durations": list(range(MIN_DURATION, MAX_DURATION + 1)),
                    "supported_resolutions": SUPPORTED_RESOLUTIONS,
                    "supported_aspect_ratios": SUPPORTED_ASPECT_RATIOS,
                    "generate_audio": False,
                    # Modal invoices are account-level. Do not fabricate a
                    # per-generation price for the dashboard.
                    "pricing_skus": {},
                }
            ]
        }

    @api_app.post("/api/v1/videos", status_code=202)
    async def create_video(request: Request) -> dict[str, Any]:
        authenticate(request)
        body = await _decode_create_request(request, HTTPException)
        callback_url, callback_token = validate_callback(body)
        options = _validated_create_options(body, HTTPException)

        job_id = f"gen_{uuid.uuid4().hex}"
        job: dict[str, Any] = {
            "id": job_id,
            "status": "pending",
            "stage": "queued",
            "model": options["model"],
            "progress": 0,
            "prompt": options["prompt"],
            "duration": options["duration"],
            "resolution": options["resolution"],
            "aspect_ratio": options["aspect_ratio"],
            "seed": options["seed"],
            "callback_url": callback_url,
            "callback_token": callback_token,
            "created_at": int(time.time()),
            "error": "",
        }
        await write_job_async(job_id, job)

        try:
            call = await VideoGenerator().generate.spawn.aio(
                **options,
                job_id=job_id,
                callback_url=callback_url,
                callback_token=callback_token,
            )
        except Exception as error:
            failed = await read_job_async(job_id) or job
            if failed.get("status") == "pending":
                failed.update(
                    {
                        "status": "failed",
                        "stage": "dispatch_failed",
                        "error": "Could not start video generation.",
                        "completed_at": int(time.time()),
                    }
                )
                await write_job_async(job_id, failed)
                queue_terminal_callback(
                    job_id,
                    "failed",
                    options["model"],
                    callback_url,
                    callback_token,
                )
            log_event("gpu_dispatch_failed", job=job_id, error_type=type(error).__name__)
            raise HTTPException(status_code=500, detail="could not start video generation")

        call_id = getattr(call, "object_id", "")
        if isinstance(call_id, str) and call_id:
            try:
                # This immutable sidecar avoids a stale API status write after
                # the GPU has already started updating the main job record.
                await write_modal_call_id_async(job_id, call_id)
                log_event("gpu_job_dispatched", job=job_id, call_id=call_id)
            except Exception as error:
                # The submission already succeeded. Returning 500 here would
                # encourage a duplicate paid submission, so retain the durable
                # job id and let normal worker progress continue.
                log_event(
                    "gpu_dispatch_reference_save_failed",
                    job=job_id,
                    error_type=type(error).__name__,
                )
        else:
            # A real Modal FunctionCall always has an id. Do not claim that a
            # successful spawn failed if a future SDK changes that shape.
            log_event("gpu_dispatch_missing_call_id", job=job_id)

        return {
            "id": job_id,
            "status": "pending",
            "model": options["model"],
            "progress": 0,
            "poll_after_seconds": STATUS_POLL_RETRY_SECONDS,
        }

    @api_app.get("/api/v1/videos/{job_id}")
    async def get_video(job_id: str, request: Request, response: Response) -> dict[str, Any]:
        authenticate(request)
        require_safe_job_id(job_id)
        job = await read_job_async(job_id)
        if job is None:
            raise HTTPException(status_code=404, detail="generation not found")
        job = await reconcile_modal_call(job)

        status = job.get("status")
        if status not in {"pending", "in_progress", "completed", "failed"}:
            raise HTTPException(status_code=500, detail="generation has an invalid status")

        progress = job.get("progress", 0)
        if type(progress) is not int or progress < 0 or progress > 100:
            progress = 0
        result: dict[str, Any] = {
            "id": job_id,
            "status": status,
            "model": job.get("model", MODEL_ID),
            "progress": progress,
        }
        if status not in {"completed", "failed"}:
            response.headers["Retry-After"] = str(STATUS_POLL_RETRY_SECONDS)
            result["poll_after_seconds"] = STATUS_POLL_RETRY_SECONDS
        elif status == "completed":
            base_url = str(request.base_url).rstrip("/")
            result["unsigned_urls"] = [
                f"{base_url}/api/v1/videos/{job_id}/content?index=0"
            ]
            gpu_seconds = job.get("gpu_seconds")
            if isinstance(gpu_seconds, (int, float)) and not isinstance(gpu_seconds, bool):
                result["usage"] = {
                    "gpu_seconds": gpu_seconds,
                    "gpu_name": job.get("gpu_name", "unknown"),
                    "basis": "measured_generation_runtime",
                }
        else:
            result["error"] = public_error_message(job.get("error"))
        return result

    @api_app.get("/api/v1/videos/{job_id}/content")
    async def get_content(job_id: str, request: Request, index: int = 0) -> Any:
        authenticate(request)
        require_safe_job_id(job_id)
        if index != 0:
            raise HTTPException(status_code=404, detail="only output index 0 exists")

        job = await read_job_async(job_id)
        if job is None:
            raise HTTPException(status_code=404, detail="generation not found")
        job = await reconcile_modal_call(job)
        if job.get("status") != "completed":
            if job.get("status") == "failed":
                raise HTTPException(
                    status_code=409,
                    detail=public_error_message(job.get("error")),
                )
            raise HTTPException(status_code=409, detail="generation is not completed yet")

        path = job_video_path(job_id)
        if not path.is_file():
            raise HTTPException(status_code=404, detail="generated video file not found")
        # Modal's default per-container input concurrency is one, and this
        # deployment uses one API container, so Volume reloads do not overlap
        # this FileResponse's Range-aware streaming lifecycle.
        return FileResponse(
            path=path,
            media_type="video/mp4",
            filename=f"{job_id}.mp4",
            headers={"Accept-Ranges": "bytes"},
        )

    return api_app


# One CPU API container uses Modal's default one input at a time. That avoids
# reloading a Volume while a FileResponse streams from the mounted Volume.
@app.function(
    image=api_image,
    volumes={JOBS_DIR: jobs_volume},
    secrets=[api_secret],
    timeout=300,
    max_containers=1,
)
@modal.asgi_app(label="skyreels-v2")
def api() -> Any:
    return create_api()
