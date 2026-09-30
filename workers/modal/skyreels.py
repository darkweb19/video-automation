"""SkyReels V2 text-to-video worker for the FrameVault Modal provider.

Adapted from https://github.com/darkweb19/skyreel-modal at commit
a7676dc46a7f4084552cdbe80bc93de6c02a9add. The upstream repository did not
include a license file. This worker keeps its SkyReels inference setup while
exposing the provider-neutral `/api/v1/videos` API consumed by this project.
"""

import asyncio
import hashlib
import hmac
import ipaddress
import json
import logging
import os
import re
import secrets
import subprocess
import time
import uuid
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Any
from urllib.parse import urlsplit

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
CALLBACK_REQUEST_TIMEOUT_SECONDS = 5
CALLBACK_RETRY_DELAYS_SECONDS = (0, 0.25, 0.75, 1.5)
CALLBACK_CLAIM_STALE_SECONDS = 60
TRANSIENT_MODAL_CALL_ERRORS = (
    modal.exception.ClientClosed,
    modal.exception.ConnectionError,
    modal.exception.InternalError,
    modal.exception.InternalFailure,
    modal.exception.ResourceExhaustedError,
    modal.exception.ServiceError,
    OSError,
    TimeoutError,
)
DEFINITIVE_MODAL_REJECTIONS = (
    modal.exception.InvalidError,
    modal.exception.AuthError,
    modal.exception.PermissionDeniedError,
    modal.exception.NotFoundError,
    modal.exception.VersionError,
    modal.exception.UnimplementedError,
    modal.exception.RequestSizeError,
)

SUPPORTED_RESOLUTIONS = ["480p", "720p"]
SUPPORTED_ASPECT_RATIOS = ["9:16", "16:9"]

SIZE_MAP = {
    ("480p", "9:16"): (480, 832),
    ("480p", "16:9"): (832, 480),
    ("720p", "9:16"): (720, 1280),
    ("720p", "16:9"): (1280, 720),
}

SAFE_JOB_ID = re.compile(r"^[A-Za-z0-9_-]{1,200}$")
CALLBACK_JOB_ID = re.compile(r"^gen_[a-f0-9]{32}$")
CALLBACK_TOKEN = re.compile(r"^[a-fA-F0-9]{64}$")
CREATE_VIDEO_FIELDS = {
    "model",
    "prompt",
    "duration",
    "resolution",
    "aspect_ratio",
    "generate_audio",
    # Seed is an intentionally supported Modal extension. The Go application
    # does not currently send it, but retaining it preserves upstream use.
    "seed",
    "job_id",
    "callback_url",
    "callback_token",
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
callback_submission_claims = modal.Dict.from_name(
    "video-generation-job-claims",
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


def validate_callback_submission(job_id: object, callback_url: object, callback_token: object) -> None:
    """Validate the narrowly scoped callback supplied by the Go server."""

    if not isinstance(job_id, str) or CALLBACK_JOB_ID.fullmatch(job_id) is None:
        raise ValueError("job_id must be a generated video ID")
    if not isinstance(callback_url, str) or len(callback_url) > 2048:
        raise ValueError("callback_url must be a valid HTTPS callback URL")
    try:
        parsed = urlsplit(callback_url)
        _ = parsed.port
    except ValueError as error:
        raise ValueError("callback_url must be a valid HTTPS callback URL") from error
    hostname = (parsed.hostname or "").lower()
    try:
        address = ipaddress.ip_address(hostname)
    except ValueError:
        address = None
    if (
        parsed.scheme != "https"
        or not hostname
        or parsed.username is not None
        or parsed.password is not None
        or parsed.query
        or parsed.fragment
        or "?" in callback_url
        or "#" in callback_url
        or parsed.path != f"/api/video-callbacks/{job_id}"
        or hostname == "localhost"
        or hostname.endswith((".localhost", ".local"))
        or (address is not None and not address.is_global)
        or (address is None and "." not in hostname)
    ):
        raise ValueError("callback_url must be a public HTTPS endpoint for this job")
    if not isinstance(callback_token, str) or CALLBACK_TOKEN.fullmatch(callback_token) is None:
        raise ValueError("callback_token must be a 32-byte hex bearer token")


def callback_submission_from_body(body: dict[str, Any]) -> tuple[str, str, str] | None:
    """Return all callback fields, while preserving old clients without them."""

    fields = ("job_id", "callback_url", "callback_token")
    supplied = [field in body for field in fields]
    if not any(supplied):
        return None
    if not all(supplied):
        raise ValueError("job_id, callback_url, and callback_token must be provided together")
    job_id, callback_url, callback_token = (body[field] for field in fields)
    validate_callback_submission(job_id, callback_url, callback_token)
    return job_id, callback_url, callback_token


def callback_token_hash(token: str) -> str:
    return hashlib.sha256(token.encode("ascii")).hexdigest()


def request_matches_job(job: dict[str, Any], options: dict[str, Any], compare_seed: bool) -> bool:
    fields = ("prompt", "model", "duration", "resolution", "aspect_ratio")
    if any(job.get(field) != options.get(field) for field in fields):
        return False
    return not compare_seed or job.get("seed") == options.get("seed")


def callback_request_fingerprint(
    options: dict[str, Any],
    callback_url: str,
    token_hash: str,
    compare_seed: bool,
) -> str:
    request = dict(options)
    if not compare_seed:
        request.pop("seed", None)
    serialized = json.dumps(
        {
            "request": request,
            "callback_url": callback_url,
            "callback_token_hash": token_hash,
        },
        ensure_ascii=True,
        sort_keys=True,
        separators=(",", ":"),
    )
    return hashlib.sha256(serialized.encode("utf-8")).hexdigest()


def classify_worker_failure(error: BaseException, job: dict[str, Any] | None = None) -> str:
    job = job or {}
    allowed = {
        "out_of_memory",
        "worker_startup_failed",
        "generation_timeout",
        "generation_failed",
        "encoding_failed",
        "storage_failed",
        "dispatch_failed",
        "dispatch_interrupted",
    }
    existing = job.get("error_code")
    if existing in allowed:
        return existing
    if isinstance(error, WorkerDispatchInterrupted):
        return "dispatch_interrupted"
    if isinstance(error, WorkerDispatchError):
        return "dispatch_failed"
    error_type = type(error).__name__.lower()
    message = str(error).lower()
    failed_stage = str(job.get("failure_stage") or job.get("stage") or "").lower()
    if any(term in error_type or term in message for term in ("out of memory", "cuda oom", "outofmemory")):
        return "out_of_memory"
    if any(term in error_type or term in message for term in ("timeout", "timed out", "deadline exceeded")):
        return "generation_timeout"
    if any(term in message for term in ("volume commit", "storage", "no space left", "disk full")) or isinstance(error, OSError):
        return "storage_failed"
    if failed_stage == "encoding":
        return "encoding_failed"
    if failed_stage in {"", "queued", "pending", "preparing", "startup", "starting"}:
        return "worker_startup_failed"
    return "generation_failed"


async def post_video_callback_once(callback_url: str, callback_token: str, payload: dict[str, Any]) -> int:
    """Post one bounded callback request; never follow a redirect with a bearer body."""

    import httpx

    async with httpx.AsyncClient(
        timeout=CALLBACK_REQUEST_TIMEOUT_SECONDS,
        follow_redirects=False,
    ) as client:
        response = await client.post(
            callback_url,
            headers={"Authorization": f"Bearer {callback_token}"},
            json=payload,
        )
        return response.status_code


@dataclass(frozen=True)
class CallbackDeliveryResult:
    delivered: bool
    retryable: bool
    status_code: int | None = None

    def __bool__(self) -> bool:
        return self.delivered


def callback_delivery_retryable(result: Any) -> bool:
    # Tests and callers may inject a boolean delivery stub; False represents a
    # transient failure in that simplified interface.
    if isinstance(result, CallbackDeliveryResult):
        return result.retryable
    return result is False


class CallbackWaitDeferredError(RuntimeError):
    """A transient Modal control-plane failure while awaiting the GPU call."""


def definitive_modal_rejection(error: BaseException) -> bool:
    return isinstance(error, DEFINITIVE_MODAL_REJECTIONS)


async def wait_for_callback_gpu_call(job_id: str, call: Any) -> None:
    """Wait for GPU completion, allowing Modal to retry transient SDK failures."""

    try:
        await call.get.aio()
    except TRANSIENT_MODAL_CALL_ERRORS as error:
        log_event(
            "callback_gpu_wait_deferred",
            job=job_id,
            error_type=type(error).__name__,
        )
        raise CallbackWaitDeferredError("Modal GPU call wait will be retried") from error


async def deliver_video_callback(
    callback_url: str,
    callback_token: str,
    payload: dict[str, Any],
    post_once: Any = None,
    sleep: Any = asyncio.sleep,
) -> CallbackDeliveryResult:
    """Retry transient delivery failures only; log no target, token, or body."""

    post_once = post_once or post_video_callback_once
    last_status: int | None = None
    for delay in CALLBACK_RETRY_DELAYS_SECONDS:
        if delay:
            await sleep(delay)
        try:
            status = await post_once(callback_url, callback_token, payload)
        except Exception:
            status = None
        if isinstance(status, int) and 200 <= status < 300:
            return CallbackDeliveryResult(True, False, status)
        last_status = status if isinstance(status, int) else None
        if status is not None and status not in {408, 425, 429, 500, 502, 503, 504}:
            return CallbackDeliveryResult(False, False, status)
    return CallbackDeliveryResult(False, True, last_status)


def callback_event(
    job_id: str,
    status: str,
    sequence: int,
    progress: int,
    stage: str,
    error_code: str = "",
) -> dict[str, Any]:
    event = {
        "id": job_id,
        "status": status,
        "sequence": sequence,
        "progress": progress,
        "stage": stage,
    }
    if error_code:
        event["error_code"] = error_code
    return event


async def run_callback_supervisor(
    job_id: str,
    callback_url: str,
    callback_token: str,
    run_generation: Any,
    read_job_fn: Any,
    write_job_fn: Any,
    post_once: Any = None,
    sleep: Any = asyncio.sleep,
) -> dict[str, Any]:
    """Persist the terminal worker outcome before notifying the Go server."""

    # Fail closed on a transient Volume read error. Treating it as a missing
    # job could dispatch a second paid GPU run or overwrite an existing result.
    previous = await read_job_fn(job_id)
    if previous is None:
        raise RuntimeError("durable worker job record is unavailable")
    if previous and previous.get("status") in {"completed", "failed"}:
        if previous.get("callback_sequence") != 2:
            previous["callback_sequence"] = 2
            await write_job_fn(job_id, previous)
        if previous.get("status") == "completed":
            event = callback_event(job_id, "completed", 2, 100, "completed")
        else:
            event = callback_event(
                job_id,
                "failed",
                2,
                int(previous.get("progress", 5)),
                str(previous.get("failure_stage") or "failed"),
                str(previous.get("error_code") or "generation_failed"),
            )
        delivered = await deliver_video_callback(
            callback_url,
            callback_token,
            event,
            post_once=post_once,
            sleep=sleep,
        )
        if not delivered:
            if callback_delivery_retryable(delivered):
                raise RuntimeError("terminal callback delivery failed")
            log_event("callback_delivery_rejected", job=job_id, status=delivered.status_code)
        return event

    previous["callback_sequence"] = 1
    await write_job_fn(job_id, previous)
    await deliver_video_callback(
        callback_url,
        callback_token,
        callback_event(job_id, "processing", 1, 5, "starting"),
        post_once=post_once,
        sleep=sleep,
    )

    caught_error: BaseException | None = None
    try:
        await run_generation()
    except asyncio.CancelledError:
        raise
    except CallbackWaitDeferredError:
        # The GPU dispatch claim and call id are durable. Let Modal's managed
        # retry reattach to that call instead of marking its still-running job
        # failed or submitting another GPU invocation.
        raise
    except BaseException as error:
        caught_error = error

    job = await read_job_fn(job_id)
    if job is None:
        raise RuntimeError("durable worker job record is unavailable after GPU execution")

    if caught_error is None and job and job.get("status") == "completed":
        job["callback_sequence"] = 2
        await write_job_fn(job_id, job)
        event = callback_event(job_id, "completed", 2, 100, "completed")
        delivered = await deliver_video_callback(
            callback_url,
            callback_token,
            event,
            post_once=post_once,
            sleep=sleep,
        )
        if not delivered:
            if callback_delivery_retryable(delivered):
                raise RuntimeError("terminal callback delivery failed")
            log_event("callback_delivery_rejected", job=job_id, status=delivered.status_code)
        return event

    if caught_error is None:
        caught_error = RuntimeError("GPU worker ended without a completed result")

    error_code = classify_worker_failure(caught_error, job)
    failure_stage = str((job or {}).get("failure_stage") or (job or {}).get("stage") or "failed")
    failed_job = dict(job or {"id": job_id, "model": MODEL_ID})
    failed_job.update(
        {
            "id": job_id,
            "status": "failed",
            "stage": "failed",
            "failure_stage": failure_stage,
            "error_code": error_code,
            "callback_sequence": 2,
            "error": "Video generation failed; check the worker logs for this job.",
            "completed_at": int(time.time()),
        }
    )
    await write_job_fn(job_id, failed_job)

    event = callback_event(job_id, "failed", 2, int((job or {}).get("progress", 5)), failure_stage, error_code)
    delivered = await deliver_video_callback(
        callback_url,
        callback_token,
        event,
        post_once=post_once,
        sleep=sleep,
    )
    log_event("callback_job_failed", job=job_id, code=error_code)
    if not delivered:
        if callback_delivery_retryable(delivered):
            raise RuntimeError("terminal callback delivery failed")
        log_event("callback_delivery_rejected", job=job_id, status=delivered.status_code)
    if isinstance(caught_error, asyncio.CancelledError):
        raise caught_error
    return event


async def persist_callback_dispatch_failure(
    job: dict[str, Any],
    callback_url: str,
    callback_token: str,
    error_code: str = "dispatch_failed",
) -> CallbackDeliveryResult:
    """Persist and deliver a pre-acceptance dispatch failure without exposing details."""

    failed = dict(job)
    failed.update(
        {
            "status": "failed",
            "stage": "failed",
            "failure_stage": "dispatch",
            "error_code": error_code,
            "callback_sequence": 2,
            "error": "Modal could not queue this video worker. Check deployment and account capacity, then retry.",
            "completed_at": int(time.time()),
        }
    )
    try:
        await write_job_async(str(job["id"]), failed)
    except Exception as error:
        log_event("callback_job_persist_failed", job=job.get("id"), error_type=type(error).__name__)
    event = callback_event(str(job["id"]), "failed", 2, 5, "dispatch", error_code)
    return await deliver_video_callback(callback_url, callback_token, event)


def safe_job_id(value: object) -> bool:
    return isinstance(value, str) and SAFE_JOB_ID.fullmatch(value) is not None


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


async def write_modal_dispatch_state_async(
    job_id: str,
    state: str,
    call_id: str = "",
) -> None:
    """Persist an idempotency marker before a billable GPU spawn."""

    if state not in {"dispatching", "dispatched"}:
        raise ValueError("invalid GPU dispatch state")
    if call_id and (not isinstance(call_id, str) or not call_id):
        raise ValueError("invalid Modal call id")
    _write_json_file(
        job_call_path(job_id),
        {"dispatch_state": state, "modal_call_id": call_id},
    )
    await jobs_volume.commit.aio()


async def read_modal_dispatch_state_async(job_id: str) -> dict[str, str]:
    if not safe_job_id(job_id):
        raise ValueError("invalid job id")
    await jobs_volume.reload.aio()
    path = job_call_path(job_id)
    if not path.is_file():
        return {}
    payload = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(payload, dict):
        return {}
    return {
        "dispatch_state": str(payload.get("dispatch_state", "")),
        "modal_call_id": str(payload.get("modal_call_id", "")),
    }


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

    if job.get("status") in {"completed", "failed"} or job.get("callback_token_hash"):
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
        log_event(
            "modal_worker_failed",
            job=job_id,
            call_id=call_id,
            error_type=type(error).__name__,
        )
        return failed

    return job


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
    ) -> None:
        """Generate, encode, and persist one MP4 without exposing it publicly."""

        from diffusers.utils import export_to_video

        if not safe_job_id(job_id):
            raise ValueError("invalid job id")

        started_at = time.time()
        previous = read_job(job_id) or {}
        if previous.get("status") in {"completed", "failed"}:
            # Modal may retry a supervisor after callback delivery failed. A
            # terminal job must never trigger another billable GPU run.
            return
        callback_metadata = {
            key: previous[key]
            for key in ("callback_url", "callback_token_hash", "callback_sequence")
            if key in previous
        }
        job: dict[str, Any] = {
            **callback_metadata,
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
            log_event(
                "generation_completed",
                job=job_id,
                inference_seconds=round(inference_seconds, 2),
                total_seconds=round(total_seconds, 2),
            )
        except Exception as error:
            failed_stage = str(job.get("stage") or "failed")
            error_code = classify_worker_failure(
                error,
                {**job, "failure_stage": failed_stage},
            )
            job.update(
                {
                    "status": "failed",
                    "stage": "failed",
                    "failure_stage": failed_stage,
                    "error_code": error_code,
                    "error": "Video generation failed. Check the Modal worker logs.",
                    "completed_at": int(time.time()),
                }
            )
            write_job(job_id, job)
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


class WorkerDispatchError(RuntimeError):
    """A GPU generation could not be queued by Modal."""


class WorkerDispatchInterrupted(RuntimeError):
    """The durable GPU call reference was lost; do not risk another spawn."""


async def run_callback_gpu_generation(
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
    """Spawn once or reattach by durable id; ambiguous acknowledgments defer."""

    gpu_claim_key = f"gpu:{job_id}"
    gpu_fingerprint = callback_request_fingerprint(
        {
            "prompt": prompt,
            "model": model,
            "duration": duration,
            "resolution": resolution,
            "aspect_ratio": aspect_ratio,
            "seed": seed,
        },
        callback_url,
        callback_token_hash(callback_token),
        compare_seed=True,
    )
    gpu_claim = {
        "fingerprint": gpu_fingerprint,
        "state": "dispatching",
        "claimed_at": int(time.time()),
        "modal_call_id": "",
    }
    try:
        acquired = await callback_submission_claims.put.aio(
            gpu_claim_key,
            gpu_claim,
            skip_if_exists=True,
        )
    except Exception as error:
        log_event("gpu_claim_write_deferred", job=job_id, error_type=type(error).__name__)
        raise CallbackWaitDeferredError("GPU dispatch claim write will be retried") from error

    if acquired:
        await write_modal_dispatch_state_async(job_id, "dispatching")
        try:
            call = await VideoGenerator().generate.spawn.aio(
                job_id=job_id,
                prompt=prompt,
                model=model,
                duration=duration,
                resolution=resolution,
                aspect_ratio=aspect_ratio,
                seed=seed,
            )
        except Exception as error:
            # Modal may have accepted the input before its acknowledgment was
            # lost. Keep the immutable claim and defer; never enqueue this id a
            # second time. A later supervisor retry checks the shared job
            # volume for a terminal child result before trying to reattach.
            if definitive_modal_rejection(error):
                raise WorkerDispatchError("Modal rejected the GPU worker submission") from error
            log_event("gpu_dispatch_ack_deferred", job=job_id, error_type=type(error).__name__)
            raise CallbackWaitDeferredError("GPU dispatch acknowledgment was ambiguous") from error

        call_id = getattr(call, "object_id", "")
        if not isinstance(call_id, str) or not call_id:
            log_event("gpu_dispatch_call_id_missing", job=job_id)
            raise CallbackWaitDeferredError("Modal GPU call id is not available yet")
        try:
            await write_modal_dispatch_state_async(job_id, "dispatched", call_id)
        except Exception as error:
            # Continue waiting on the in-memory FunctionCall. If this
            # supervisor is restarted, its dispatching marker prevents a
            # second GPU submission with the same immutable job id.
            log_event("gpu_call_reference_save_failed", job=job_id, error_type=type(error).__name__)
        gpu_claim.update({"state": "dispatched", "modal_call_id": call_id})
        try:
            await callback_submission_claims.put.aio(gpu_claim_key, gpu_claim)
        except Exception as error:
            # The sidecar and in-memory FunctionCall still let this attempt
            # finish; a supervisor retry can recover the sidecar or defer
            # without dispatching GPU twice.
            log_event("gpu_call_claim_save_failed", job=job_id, error_type=type(error).__name__)
    else:
        try:
            existing_claim = await callback_submission_claims.get.aio(gpu_claim_key)
        except Exception as error:
            log_event("gpu_claim_read_deferred", job=job_id, error_type=type(error).__name__)
            raise CallbackWaitDeferredError("GPU dispatch claim read will be retried") from error
        if not isinstance(existing_claim, dict) or existing_claim.get("fingerprint") != gpu_fingerprint:
            raise WorkerDispatchInterrupted("GPU dispatch claim does not match the durable job")
        call_id = str(existing_claim.get("modal_call_id", ""))
        if not call_id:
            try:
                dispatch = await read_modal_dispatch_state_async(job_id)
            except Exception as error:
                log_event("gpu_call_reference_read_deferred", job=job_id, error_type=type(error).__name__)
                raise CallbackWaitDeferredError("GPU call reference read will be retried") from error
            call_id = dispatch.get("modal_call_id", "")
        if not call_id:
            # An accepted spawn can outlive its lost acknowledgment. Repeated
            # supervisor attempts inspect the durable job at entry and never
            # create a second GPU call while its ID/result is not yet visible.
            raise CallbackWaitDeferredError("GPU call id is not available yet")
        try:
            call = modal.FunctionCall.from_id(call_id)
        except Exception as error:
            if definitive_modal_rejection(error):
                raise WorkerDispatchInterrupted("Modal no longer recognizes the saved GPU call id") from error
            log_event("gpu_call_attach_deferred", job=job_id, error_type=type(error).__name__)
            raise CallbackWaitDeferredError("GPU call attachment will be retried") from error
    await wait_for_callback_gpu_call(job_id, call)


@app.function(
    image=api_image,
    volumes={JOBS_DIR: jobs_volume},
    secrets=[api_secret],
    timeout=7200,
    # The terminal job is durable before delivery. Retries revisit that result
    # and resend sequence 2; they never repeat GPU inference for that id.
    # Capped Modal retries allow recovery from callback outages. A bounded
    # 240-attempt policy with a 60s cap remains within Go's eight-hour callback
    # deadline even after a full GPU startup + generation attempt.
    retries=modal.Retries(
        max_retries=240,
        backoff_coefficient=2.0,
        initial_delay=1.0,
        max_delay=60.0,
    ),
    max_containers=2,
)
async def callback_video_supervisor(
    job_id: str,
    prompt: str,
    model: str,
    duration: int,
    resolution: str,
    aspect_ratio: str,
    seed: int,
    callback_url: str,
    callback_token: str,
) -> dict[str, Any]:
    """Await the GPU function's full lifecycle and callback its terminal result."""

    async def run_generation() -> None:
        await run_callback_gpu_generation(
            job_id=job_id,
            prompt=prompt,
            model=model,
            duration=duration,
            resolution=resolution,
            aspect_ratio=aspect_ratio,
            seed=seed,
            callback_url=callback_url,
            callback_token=callback_token,
        )

    return await run_callback_supervisor(
        job_id=job_id,
        callback_url=callback_url,
        callback_token=callback_token,
        run_generation=run_generation,
        read_job_fn=read_job_async,
        write_job_fn=write_job_async,
    )


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
        options = _validated_create_options(body, HTTPException)

        try:
            callback_submission = callback_submission_from_body(body)
        except ValueError as error:
            raise HTTPException(status_code=400, detail=str(error))
        if callback_submission is None:
            job_id = f"gen_{uuid.uuid4().hex}"
            callback_url = ""
            callback_token = ""
            callback_fingerprint = ""
            request_claim_key = ""
        else:
            job_id, callback_url, callback_token = callback_submission
            callback_fingerprint = callback_request_fingerprint(
                options,
                callback_url,
                callback_token_hash(callback_token),
                compare_seed="seed" in body,
            )
            request_claim_key = f"request:{job_id}"
            request_claim = {
                "fingerprint": callback_fingerprint,
                "state": "claiming",
                "claimed_at": int(time.time()),
                "supervisor_call_id": "",
            }
            try:
                claim_created = await callback_submission_claims.put.aio(
                    request_claim_key,
                    request_claim,
                    skip_if_exists=True,
                )
                if not claim_created:
                    existing_claim = await callback_submission_claims.get.aio(request_claim_key)
                    if (
                        not isinstance(existing_claim, dict)
                        or existing_claim.get("fingerprint") != callback_fingerprint
                    ):
                        raise HTTPException(
                            status_code=409,
                            detail="This job_id was already claimed for a different video request",
                        )
                    request_claim = existing_claim
            except HTTPException:
                raise
            except Exception as error:
                log_event("callback_claim_unavailable", job=job_id, error_type=type(error).__name__)
                raise HTTPException(
                    status_code=503,
                    detail="The Modal callback submission could not be claimed safely; retry this request",
                )

        try:
            existing = await read_job_async(job_id)
        except Exception as error:
            log_event("callback_job_read_failed", job=job_id, error_type=type(error).__name__)
            raise HTTPException(
                status_code=503,
                detail="The durable Modal job state could not be read; retry this request",
            )
        if callback_submission is not None and existing is None and not claim_created:
            claimed_at = request_claim.get("claimed_at", 0)
            if type(claimed_at) is not int or time.time() - claimed_at < CALLBACK_CLAIM_STALE_SECONDS:
                raise HTTPException(
                    status_code=503,
                    detail="The accepted Modal job is still being recorded; retry this request shortly",
                )
            orphaned_job = {
                "id": job_id,
                "status": "pending",
                "stage": "queued",
                "model": options["model"],
                "progress": 5,
                "prompt": options["prompt"],
                "duration": options["duration"],
                "resolution": options["resolution"],
                "aspect_ratio": options["aspect_ratio"],
                "seed": options["seed"],
                "created_at": claimed_at,
                "callback_url": callback_url,
                "callback_token_hash": callback_token_hash(callback_token),
                "callback_sequence": 1,
            }
            delivered = await persist_callback_dispatch_failure(
                orphaned_job,
                callback_url,
                callback_token,
                error_code="dispatch_interrupted",
            )
            request_claim.update({"state": "failed", "completed_at": int(time.time())})
            try:
                await callback_submission_claims.put.aio(request_claim_key, request_claim)
            except Exception as error:
                log_event("callback_claim_update_failed", job=job_id, error_type=type(error).__name__)
            if not delivered:
                if callback_delivery_retryable(delivered):
                    raise HTTPException(
                        status_code=503,
                        detail="The interrupted Modal submission failure callback could not be delivered; retry this request",
                    )
                raise HTTPException(
                    status_code=400,
                    detail="The callback endpoint rejected delivery; check VIDEO_CALLBACK_BASE_URL and the dashboard callback route",
                )
            return {
                "id": job_id,
                "status": "failed",
                "stage": "failed",
                "model": options["model"],
                "progress": 5,
            }
        if existing is not None:
            if (
                callback_submission is None
                or not request_matches_job(existing, options, compare_seed="seed" in body)
                or existing.get("callback_url") != callback_url
                or not hmac.compare_digest(
                    str(existing.get("callback_token_hash", "")),
                    callback_token_hash(callback_token),
                )
            ):
                raise HTTPException(
                    status_code=409,
                    detail="This job_id was already used for a different video request",
                )
            if callback_submission is not None:
                request_claim.update({"state": "recorded" if existing.get("status") not in {"completed", "failed"} else existing.get("status")})
                try:
                    await callback_submission_claims.put.aio(request_claim_key, request_claim)
                except Exception as error:
                    log_event("callback_claim_update_failed", job=job_id, error_type=type(error).__name__)
            if existing.get("status") == "completed":
                event = callback_event(job_id, "completed", 2, 100, "completed")
                terminal = True
            elif existing.get("status") == "failed":
                code = str(existing.get("error_code") or "generation_failed")
                event = callback_event(
                    job_id,
                    "failed",
                    2,
                    int(existing.get("progress", 5)),
                    str(existing.get("failure_stage") or "failed"),
                    code,
                )
                terminal = True
            else:
                event = callback_event(job_id, "processing", 1, 5, "starting")
                terminal = False
            delivered = await deliver_video_callback(callback_url, callback_token, event)
            if terminal and not delivered:
                if callback_delivery_retryable(delivered):
                    raise HTTPException(
                        status_code=503,
                        detail="The saved video result callback could not be delivered; retry this request",
                    )
                raise HTTPException(
                    status_code=400,
                    detail="The callback endpoint rejected delivery; check VIDEO_CALLBACK_BASE_URL and the dashboard callback route",
                )
            ack = {
                "id": job_id,
                "status": existing.get("status", "pending"),
                "stage": existing.get("stage", "queued"),
                "model": options["model"],
                "progress": existing.get("progress", 0),
            }
            if not callback_submission:
                ack["poll_after_seconds"] = STATUS_POLL_RETRY_SECONDS
            return ack

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
            "created_at": int(time.time()),
            "error": "",
        }
        if callback_submission is not None:
            job.update(
                {
                    "callback_url": callback_url,
                    "callback_token_hash": callback_token_hash(callback_token),
                    "callback_sequence": 0,
                }
            )
        try:
            await write_job_async(job_id, job)
        except Exception as error:
            log_event("callback_job_record_failed", job=job_id, error_type=type(error).__name__)
            if callback_submission is None:
                raise HTTPException(status_code=503, detail="Could not persist the video job")
            delivered = await persist_callback_dispatch_failure(
                job,
                callback_url,
                callback_token,
                error_code="storage_failed",
            )
            request_claim.update({"state": "failed", "completed_at": int(time.time())})
            try:
                await callback_submission_claims.put.aio(request_claim_key, request_claim)
            except Exception as claim_error:
                log_event("callback_claim_update_failed", job=job_id, error_type=type(claim_error).__name__)
            if not delivered:
                if callback_delivery_retryable(delivered):
                    raise HTTPException(
                        status_code=503,
                        detail="The Modal job could not be stored and its failure callback could not be delivered; retry this request",
                    )
                raise HTTPException(
                    status_code=400,
                    detail="The callback endpoint rejected delivery; check VIDEO_CALLBACK_BASE_URL and the dashboard callback route",
                )
            return {
                "id": job_id,
                "status": "failed",
                "stage": "failed",
                "model": options["model"],
                "progress": 5,
            }

        if callback_submission is not None:
            request_claim.update({"state": "recorded"})
            try:
                await callback_submission_claims.put.aio(request_claim_key, request_claim)
            except Exception as error:
                log_event("callback_claim_update_failed", job=job_id, error_type=type(error).__name__)

        if callback_submission is not None:
            try:
                supervisor_call = await callback_video_supervisor.spawn.aio(
                    job_id=job_id,
                    prompt=options["prompt"],
                    model=options["model"],
                    duration=options["duration"],
                    resolution=options["resolution"],
                    aspect_ratio=options["aspect_ratio"],
                    seed=options["seed"],
                    callback_url=callback_url,
                    callback_token=callback_token,
                )
            except Exception as error:
                if not definitive_modal_rejection(error):
                    # The supervisor input may have been accepted before its
                    # acknowledgment was lost. Keep the durable job pending;
                    # a same-id retry must observe this record, never queue
                    # another supervisor or turn uncertain work into failure.
                    log_event("callback_supervisor_ack_deferred", job=job_id, error_type=type(error).__name__)
                    return {
                        "id": job_id,
                        "status": "pending",
                        "stage": "queued",
                        "model": options["model"],
                        "progress": 0,
                    }
                log_event("callback_supervisor_dispatch_failed", job=job_id, error_type=type(error).__name__)
                delivered = await persist_callback_dispatch_failure(
                    job,
                    callback_url,
                    callback_token,
                    error_code="dispatch_failed",
                )
                request_claim.update({"state": "failed", "completed_at": int(time.time())})
                try:
                    await callback_submission_claims.put.aio(request_claim_key, request_claim)
                except Exception as claim_error:
                    log_event("callback_claim_update_failed", job=job_id, error_type=type(claim_error).__name__)
                if not delivered:
                    if callback_delivery_retryable(delivered):
                        raise HTTPException(
                            status_code=503,
                            detail="The video worker could not be queued and its failure callback could not be delivered; retry this request",
                        )
                    raise HTTPException(
                        status_code=400,
                        detail="The callback endpoint rejected delivery; check VIDEO_CALLBACK_BASE_URL and the dashboard callback route",
                    )
                return {
                    "id": job_id,
                    "status": "failed",
                    "stage": "failed",
                    "model": options["model"],
                    "progress": 5,
                }
            request_claim.update(
                {
                    "state": "supervisor_queued",
                    "supervisor_call_id": str(getattr(supervisor_call, "object_id", "")),
                }
            )
            try:
                await callback_submission_claims.put.aio(request_claim_key, request_claim)
            except Exception as error:
                log_event("callback_claim_update_failed", job=job_id, error_type=type(error).__name__)
            return {
                "id": job_id,
                "status": "pending",
                "stage": "queued",
                "model": options["model"],
                "progress": 0,
            }

        try:
            call = await VideoGenerator().generate.spawn.aio(**options, job_id=job_id)
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
