"""SkyReels V2 text-to-video worker for the FrameVault Modal provider.

Adapted from https://github.com/darkweb19/skyreel-modal at commit
a7676dc46a7f4084552cdbe80bc93de6c02a9add. The upstream repository did not
include a license file. This worker keeps its SkyReels inference setup while
exposing the provider-neutral `/api/v1/videos` API consumed by this project.
"""

import asyncio
import hashlib
import ipaddress
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
MAX_CALLBACK_URL_LENGTH = 2048

# Callback delivery is bounded. The dashboard has a sparse, persisted recovery
# check for the exceptional case where every delivery attempt fails.
CALLBACK_DELIVERY_MAX_ATTEMPTS = 5
CALLBACK_DELIVERY_TIMEOUT_SECONDS = 10
CALLBACK_DELIVERY_MAX_RETRY_AFTER_SECONDS = 15
CALLBACK_RETRY_DELAYS_SECONDS = (0, 1, 2, 4, 8)
CALLBACK_JOB_ID = re.compile(r"^[A-Za-z0-9_-]{1,200}$")
CALLBACK_TOKEN = re.compile(r"^[A-Za-z0-9_-]{43,128}$")
CLAIMS_DICT_NAME = "skyreels-video-generation-job-claims"
CLAIM_RECOVERY_SECONDS = 60
TRANSIENT_MODAL_CALL_ERRORS = (
    modal.exception.ClientClosed,
    modal.exception.ConnectionError,
    modal.exception.InternalError,
    modal.exception.InternalFailure,
    modal.exception.ResourceExhaustedError,
    modal.exception.ServiceError,
    modal.exception.TimeoutError,
    TimeoutError,
    OSError,
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
CREATE_VIDEO_FIELDS = {
    "job_id",
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
job_claims = modal.Dict.from_name(
    CLAIMS_DICT_NAME,
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
        parsed = urlsplit(callback_url)
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


def _public_callback_host(hostname: str) -> bool:
    hostname = hostname.lower().rstrip(".")
    try:
        return ipaddress.ip_address(hostname).is_global
    except ValueError:
        return (
            "." in hostname
            and hostname != "localhost"
            and not hostname.endswith((".localhost", ".local", ".internal"))
        )


def validate_callback_target(job_id: str, callback_url: str, legacy: bool = False) -> None:
    if not safe_job_id(job_id):
        raise ValueError("job_id must be a safe generation ID")
    if not isinstance(callback_url, str) or not callback_url or len(callback_url) > MAX_CALLBACK_URL_LENGTH:
        raise ValueError("callback_url must be a valid HTTPS URL")
    try:
        parsed = urlsplit(callback_url)
        _ = parsed.port
    except (TypeError, ValueError) as error:
        raise ValueError("callback_url must be a valid HTTPS URL") from error
    suffix = "/api/provider-callbacks/modal" if legacy else f"/api/video-callbacks/{job_id}"
    if (
        parsed.scheme != "https"
        or not parsed.hostname
        or not _public_callback_host(parsed.hostname)
        or parsed.username is not None
        or parsed.password is not None
        or parsed.query
        or parsed.fragment
        or "?" in callback_url
        or "#" in callback_url
        or not parsed.path.endswith(suffix)
    ):
        raise ValueError("callback_url must be the public HTTPS callback endpoint for this job")


def validate_callback_submission(job_id: object, callback_url: object, callback_token: object) -> None:
    if not isinstance(job_id, str) or not safe_job_id(job_id):
        raise ValueError("job_id must be a safe generation ID")
    if not isinstance(callback_token, str) or not CALLBACK_TOKEN.fullmatch(callback_token):
        raise ValueError("callback_token must be a 32-byte URL-safe token")
    validate_callback_target(job_id, callback_url)


def validate_legacy_callback_submission(callback_url: object, callback_token: object) -> None:
    if not isinstance(callback_token, str) or not CALLBACK_TOKEN.fullmatch(callback_token):
        raise ValueError("callback_token is invalid")
    if not isinstance(callback_url, str):
        raise ValueError("callback_url is invalid")
    validate_callback_target("legacy", callback_url, legacy=True)


def callback_submission_from_body(body: dict[str, Any]) -> dict[str, Any] | None:
    """Accept stable callback jobs, the previous static callback, or no callback."""
    has_id = "job_id" in body
    has_url = "callback_url" in body
    has_token = "callback_token" in body
    if not has_id and not has_url and not has_token:
        return None
    if has_id:
        if not (has_url and has_token):
            raise ValueError("job_id, callback_url, and callback_token must be provided together")
        validate_callback_submission(body["job_id"], body["callback_url"], body["callback_token"])
        return {
            "mode": "callback",
            "job_id": body["job_id"],
            "callback_url": body["callback_url"],
            "callback_token": body["callback_token"],
        }
    if not (has_url and has_token):
        raise ValueError("callback_url and callback_token must be provided together")
    validate_legacy_callback_submission(body["callback_url"], body["callback_token"])
    return {
        "mode": "legacy",
        "callback_url": body["callback_url"],
        "callback_token": body["callback_token"],
    }


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
    _write_json_file(
        job_call_path(job_id),
        {"dispatch_state": "dispatched", "modal_call_id": call_id},
    )
    await jobs_volume.commit.aio()


async def write_modal_dispatch_state_async(
    job_id: str,
    state: str,
    call_id: str = "",
) -> None:
    """Persist dispatch lifecycle separately from mutable GPU job status."""
    if not safe_job_id(job_id) or state not in {"dispatching", "dispatched", "dispatch_failed"}:
        raise ValueError("invalid dispatch metadata")
    if call_id and (not isinstance(call_id, str) or len(call_id) > 500):
        raise ValueError("invalid Modal call ID")
    _write_json_file(
        job_call_path(job_id),
        {"dispatch_state": state, "modal_call_id": call_id},
    )
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


class WorkerDispatchError(Exception):
    """Modal definitively rejected the GPU invocation."""


class WorkerDispatchInterrupted(WorkerDispatchError):
    """A claimed dispatch cannot be recovered without risking duplication."""


class CallbackDeliveryError(RuntimeError):
    """A durable callback result could not be delivered after bounded retries."""


class SupervisorStateError(RuntimeError):
    """Durable coordination state is missing or temporarily unreadable."""


class CallbackWaitDeferredError(RuntimeError):
    """Modal has not confirmed a child result; retry the same supervisor."""


def definitive_modal_rejection(error: Exception) -> bool:
    return isinstance(error, DEFINITIVE_MODAL_REJECTIONS)


def callback_request_fingerprint(
    prompt: str,
    model: str,
    duration: int,
    resolution: str,
    aspect_ratio: str,
    callback_url: str,
    callback_token: str,
    seed: int | None = None,
    seed_was_supplied: bool = False,
) -> str:
    request: dict[str, Any] = {
        "prompt": prompt.strip(),
        "model": model,
        "duration": int(duration),
        "resolution": str(resolution).lower(),
        "aspect_ratio": str(aspect_ratio),
    }
    if seed_was_supplied:
        request["seed"] = int(seed)
    canonical = json.dumps(
        {
            "request": request,
            "callback_url": callback_url,
            "callback_token_hash": hashlib.sha256(callback_token.encode("ascii")).hexdigest(),
        },
        ensure_ascii=True,
        sort_keys=True,
        separators=(",", ":"),
    )
    return hashlib.sha256(canonical.encode("utf-8")).hexdigest()


async def get_claim(key: str) -> Any:
    return await job_claims.get.aio(key)


async def put_claim(key: str, value: dict[str, Any], skip_if_exists: bool = False) -> Any:
    return await job_claims.put.aio(key, value, skip_if_exists=skip_if_exists)


def classify_worker_failure(error: Exception, job: dict[str, Any] | None = None) -> str:
    job = job or {}
    allowed = {
        "out_of_memory", "worker_startup_failed", "generation_timeout", "generation_failed",
        "encoding_failed", "storage_failed", "dispatch_failed", "dispatch_interrupted",
        "callback_delivery_rejected",
    }
    existing = job.get("error_code")
    if existing in allowed:
        return existing
    if isinstance(error, WorkerDispatchInterrupted):
        return "dispatch_interrupted"
    if isinstance(error, WorkerDispatchError):
        return "dispatch_failed"
    if isinstance(error, asyncio.CancelledError):
        return "dispatch_interrupted"
    error_text = f"{type(error).__name__} {error}".lower()
    failed_stage = str(job.get("failure_stage") or job.get("stage") or "").lower()
    if any(term in error_text for term in ("out of memory", "cuda oom", "outofmemory")):
        return "out_of_memory"
    if any(term in error_text for term in ("timeout", "timed out", "deadline exceeded")):
        return "generation_timeout"
    if any(term in error_text for term in ("volume commit", "storage", "no space left", "disk full")) or isinstance(error, OSError):
        return "storage_failed"
    if failed_stage == "encoding":
        return "encoding_failed"
    if failed_stage in {"", "queued", "pending", "preparing", "startup", "starting", "dispatch"}:
        return "worker_startup_failed"
    return "generation_failed"


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
    except TRANSIENT_MODAL_CALL_ERRORS as error:
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
                "failure_stage": "worker_startup",
                "error_code": classify_worker_failure(error, {"stage": "worker_startup"}),
                "progress": 0,
                "error": "Video generation worker failed. Check Modal logs for details.",
                "completed_at": int(time.time()),
            }
        )
        await write_job_async(job_id, failed)
        callback_mode = failed.get("callback_mode")
        callback_url = failed.get("callback_url")
        callback_token = failed.get("callback_token")
        if not callback_mode and callback_url and callback_token:
            callback_mode = "legacy"
        if callback_mode == "legacy":
            queue_terminal_callback(
                job_id,
                "failed",
                str(failed.get("model") or MODEL_ID),
                callback_url,
                callback_token,
            )
        elif callback_mode == "callback" and callback_url and callback_token:
            event = terminal_callback_event(job_id, failed)
            if event is not None:
                await asyncio.to_thread(
                    deliver_video_callback,
                    callback_url,
                    callback_token,
                    event,
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
        callback_url: str = "",
        callback_token: str = "",
    ) -> None:
        """Generate, encode, and persist one MP4 without exposing it publicly."""

        from diffusers.utils import export_to_video

        if not safe_job_id(job_id):
            raise ValueError("invalid job id")

        started_at = time.time()
        previous = read_job(job_id) or {}
        callback_url = str(previous.get("callback_url") or callback_url or "")
        callback_token = str(previous.get("callback_token") or callback_token or "")
        callback_mode = previous.get("callback_mode")
        if not callback_mode and callback_url and callback_token:
            callback_mode = "legacy"
        job: dict[str, Any] = {
            **previous,
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
        if callback_url:
            job["callback_url"] = callback_url
        if callback_token:
            job["callback_token"] = callback_token
        if callback_mode:
            job["callback_mode"] = callback_mode
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
            if callback_mode == "legacy":
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
            failure_stage = str(job.get("stage") or "generation")
            job.update(
                {
                    "status": "failed",
                    "stage": "failed",
                    "failure_stage": failure_stage,
                    "error_code": classify_worker_failure(error, {"stage": failure_stage}),
                    "error": "Video generation failed. Check the Modal worker logs.",
                    "completed_at": int(time.time()),
                }
            )
            write_job(job_id, job)
            if callback_mode == "legacy":
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
# Durable per-job callback delivery
# ---------------------------------------------------------------------------

def _callback_event(job_id: str, status: str, sequence: int, progress: int,
                    stage: str, error_code: str = "") -> dict[str, Any]:
    event: dict[str, Any] = {
        "id": job_id,
        "status": status,
        "sequence": sequence,
        "progress": max(0, min(100, int(progress))),
        "stage": str(stage)[:100],
    }
    if error_code:
        event["error_code"] = error_code
    return event


def terminal_callback_event(job_id: str, job: dict[str, Any]) -> dict[str, Any] | None:
    status = job.get("status")
    if status == "completed":
        return _callback_event(job_id, "completed", 2, 100, "completed")
    if status == "failed":
        code = classify_worker_failure(RuntimeError("persisted failure"), job)
        progress = min(99, max(5, int(job.get("progress", 5) or 5)))
        stage = str(job.get("failure_stage") or job.get("stage") or "failed")
        return _callback_event(job_id, "failed", 2, progress, stage, error_code=code)
    return None


def post_video_callback_once(callback_url: str, callback_token: str, payload: dict[str, Any]) -> int:
    outbound = urllib_request.Request(
        callback_url,
        data=json.dumps(payload, separators=(",", ":")).encode("utf-8"),
        method="POST",
        headers={
            "Content-Type": "application/json",
            "Accept": "application/json",
            "Authorization": f"Bearer {callback_token}",
        },
    )
    try:
        with open_callback_request(outbound, CALLBACK_DELIVERY_TIMEOUT_SECONDS) as response:
            return response.getcode()
    except urllib_error.HTTPError as error:
        status = error.code
        try:
            error.close()
        except Exception:
            pass
        return status


def deliver_video_callback(callback_url: str, callback_token: str,
                           payload: dict[str, Any], post_once=None, sleep=time.sleep,
                           return_result: bool = False) -> bool | dict[str, Any]:
    """Send bearer callbacks with redirects disabled and bounded retries."""
    post_once = post_once or post_video_callback_once
    last_status = None
    for attempt, delay in enumerate(CALLBACK_RETRY_DELAYS_SECONDS, start=1):
        if delay:
            sleep(delay)
        try:
            status = post_once(callback_url, callback_token, payload)
        except Exception:
            status = None
        last_status = status if isinstance(status, int) else None
        if isinstance(status, int) and 200 <= status < 300:
            log_event("callback_delivered", job=payload.get("id"), attempt=attempt)
            outcome = {"state": "delivered", "code": "delivered", "status_code": status, "attempts": attempt}
            return outcome if return_result else True
        if status is not None and status not in {408, 425, 429, 500, 502, 503, 504}:
            log_event("callback_rejected", job=payload.get("id"), status=status)
            outcome = {"state": "rejected", "code": f"http_{status}", "status_code": status, "attempts": attempt}
            return outcome if return_result else False
    log_event("callback_delivery_exhausted", job=payload.get("id"),
              attempts=len(CALLBACK_RETRY_DELAYS_SECONDS))
    code = f"http_{last_status}" if last_status is not None else "network_error"
    outcome = {
        "state": "retry_exhausted",
        "code": code,
        "status_code": last_status,
        "attempts": len(CALLBACK_RETRY_DELAYS_SECONDS),
    }
    return outcome if return_result else False


def callback_delivery_summary(job: dict[str, Any]) -> dict[str, Any] | None:
    delivery = job.get("callback_delivery")
    if not isinstance(delivery, dict) or delivery.get("state") not in {"rejected", "retry_exhausted"}:
        return None
    code = delivery.get("code")
    if not isinstance(code, str) or not re.fullmatch(r"(?:http_[1-5][0-9]{2}|network_error)", code):
        code = "unknown_error"
    status_code = delivery.get("status_code")
    if type(status_code) is not int or status_code < 100 or status_code > 599:
        status_code = None
    attempts = delivery.get("attempts")
    if type(attempts) is not int or attempts < 1 or attempts > len(CALLBACK_RETRY_DELAYS_SECONDS):
        attempts = len(CALLBACK_RETRY_DELAYS_SECONDS)
    return {"state": delivery["state"], "code": code, "status_code": status_code, "attempts": attempts}


async def persist_callback_delivery(job_id: str, job: dict[str, Any], outcome: dict[str, Any], write_job_fn) -> None:
    saved_job = dict(job)
    if outcome["state"] == "delivered":
        if "callback_delivery" not in saved_job:
            return
        saved_job.pop("callback_delivery", None)
    else:
        saved_job["callback_delivery"] = {
            "state": outcome["state"],
            "code": outcome["code"],
            "status_code": outcome["status_code"],
            "attempts": outcome["attempts"],
        }
    try:
        await write_job_fn(job_id, saved_job)
    except Exception as error:
        log_event("callback_delivery_state_persist_failed", job=job_id, code="storage_failed")
        raise SupervisorStateError("Callback delivery result could not be persisted") from error


def callback_delivery_rejection_result(event: dict[str, Any], outcome: dict[str, Any]) -> dict[str, Any]:
    result = dict(event)
    result["delivery_rejected"] = True
    result["delivery_code"] = outcome["code"]
    result["delivery_status"] = outcome["status_code"]
    return result


async def run_callback_supervisor(job_id: str, callback_url: str, callback_token: str,
                                 run_generation, read_job_fn, write_job_fn,
                                 post_once=None, sleep=time.sleep) -> dict[str, Any]:
    """Persist/replay terminal state; supervisor retries never regenerate saved output."""
    try:
        job = await read_job_fn(job_id)
    except Exception as error:
        raise SupervisorStateError("Persisted video state could not be read") from error
    if not isinstance(job, dict):
        raise SupervisorStateError("Persisted video state is not available yet")
    event = terminal_callback_event(job_id, job)
    if event is not None:
        outcome = deliver_video_callback(
            callback_url,
            callback_token,
            event,
            post_once,
            sleep,
            return_result=True,
        )
        await persist_callback_delivery(job_id, job, outcome, write_job_fn)
        if outcome["state"] == "retry_exhausted":
            raise CallbackDeliveryError("Transient terminal callback delivery failed after bounded retries")
        if outcome["state"] == "rejected":
            log_event("callback_delivery_rejected_terminal", job=job_id, code=outcome["code"], status=outcome["status_code"])
            return callback_delivery_rejection_result(event, outcome)
        return event

    if job.get("status") == "pending":
        processing_job = dict(job)
        processing_job.update({
            "status": "in_progress",
            "stage": "starting",
            "progress": max(5, int(job.get("progress", 0) or 0)),
        })
        try:
            await write_job_fn(job_id, processing_job)
        except Exception as error:
            raise SupervisorStateError("Processing video state could not be persisted") from error
        job = processing_job

    # Stable bytes let the receiver acknowledge duplicate sequence 1 events.
    outcome = deliver_video_callback(
        callback_url,
        callback_token,
        _callback_event(job_id, "processing", 1, 5, "starting"),
        post_once,
        sleep,
        return_result=True,
    )
    if outcome["state"] == "retry_exhausted":
        await persist_callback_delivery(job_id, job, outcome, write_job_fn)
        raise CallbackDeliveryError("Transient initial callback delivery failed after bounded retries")
    if outcome["state"] == "rejected":
        rejected_job = dict(job)
        rejected_job.update({
            "id": job_id,
            "status": "failed",
            "stage": "failed",
            "failure_stage": "callback",
            "error_code": "callback_delivery_rejected",
            "error": "Callback endpoint rejected the worker's initial status update. Check VIDEO_CALLBACK_BASE_URL, the deployed receiver, and edge access rules.",
            "completed_at": int(time.time()),
        })
        await persist_callback_delivery(job_id, rejected_job, outcome, write_job_fn)
        event = terminal_callback_event(job_id, rejected_job)
        log_event("callback_delivery_rejected_before_generation", job=job_id, code=outcome["code"], status=outcome["status_code"])
        return callback_delivery_rejection_result(event, outcome)
    await persist_callback_delivery(job_id, job, outcome, write_job_fn)
    caught_error = None
    try:
        await run_generation()
    except asyncio.CancelledError:
        raise
    except (CallbackWaitDeferredError, SupervisorStateError):
        raise
    except Exception as error:
        caught_error = error

    try:
        job = await read_job_fn(job_id)
    except Exception as error:
        raise SupervisorStateError("Persisted video state could not be reloaded") from error
    if not isinstance(job, dict):
        raise SupervisorStateError("Persisted video state disappeared during generation")
    event = terminal_callback_event(job_id, job)
    if event is None:
        if caught_error is None:
            caught_error = RuntimeError("GPU worker ended without a terminal result")
        failure_stage = str(job.get("failure_stage") or job.get("stage") or "failed")
        failed_job = dict(job)
        failed_job.update({
            "id": job_id,
            "status": "failed",
            "stage": "failed",
            "failure_stage": failure_stage,
            "error_code": classify_worker_failure(caught_error, job),
            "error": "Video generation failed. Check the Modal worker logs.",
            "completed_at": int(time.time()),
        })
        try:
            await write_job_fn(job_id, failed_job)
        except Exception as error:
            log_event("callback_job_persist_failed", job=job_id, code="storage_failed")
            raise SupervisorStateError("Terminal video failure could not be persisted") from error
        job = failed_job
        event = terminal_callback_event(job_id, failed_job)
    outcome = deliver_video_callback(
        callback_url,
        callback_token,
        event,
        post_once,
        sleep,
        return_result=True,
    )
    await persist_callback_delivery(job_id, job, outcome, write_job_fn)
    if outcome["state"] == "retry_exhausted":
        raise CallbackDeliveryError("Transient terminal callback delivery failed after bounded retries")
    if outcome["state"] == "rejected":
        log_event("callback_delivery_rejected_terminal", job=job_id, code=outcome["code"], status=outcome["status_code"])
        event = callback_delivery_rejection_result(event, outcome)
    if event["status"] == "failed":
        log_event("callback_job_failed", job=job_id, code=event.get("error_code"))
    return event


@app.function(
    image=api_image,
    volumes={JOBS_DIR: jobs_volume},
    retries=modal.Retries(
        max_retries=10,
        backoff_coefficient=2.0,
        initial_delay=1.0,
        max_delay=60.0,
    ),
    timeout=7200,
)
async def callback_video_supervisor(job_id: str) -> dict[str, Any]:
    """Run/reattach to a single GPU call and replay the durable callback result.

    The callback capability is deliberately loaded from the private job record,
    not serialized as a Modal function argument or stored in the coordination
    Dict. The Dict contains only a request fingerprint and dispatch reference.
    """
    try:
        job = await read_job_async(job_id)
    except Exception as error:
        raise SupervisorStateError("Persisted video request could not be read") from error
    if not isinstance(job, dict):
        raise SupervisorStateError("Persisted video request is unavailable")
    callback_url = job.get("callback_url")
    callback_token = job.get("callback_token")
    fingerprint = job.get("request_fingerprint")
    try:
        validate_callback_submission(job_id, callback_url, callback_token)
    except ValueError as error:
        raise SupervisorStateError("Persisted callback capability is invalid") from error
    if not isinstance(fingerprint, str) or not re.fullmatch(r"[a-f0-9]{64}", fingerprint):
        raise SupervisorStateError("Persisted request fingerprint is invalid")

    async def run_generation() -> None:
        gpu_key = f"gpu:{job_id}"
        try:
            gpu_claim = await get_claim(gpu_key)
        except TRANSIENT_MODAL_CALL_ERRORS as error:
            raise CallbackWaitDeferredError("Modal GPU claim lookup will be retried") from error

        call = None
        if gpu_claim is None:
            new_claim = {
                "fingerprint": fingerprint,
                "state": "dispatching",
                "claimed_at": int(time.time()),
                "modal_call_id": "",
            }
            try:
                acquired = await put_claim(gpu_key, new_claim, skip_if_exists=True)
            except TRANSIENT_MODAL_CALL_ERRORS as error:
                raise CallbackWaitDeferredError("Modal GPU claim update will be retried") from error
            if acquired:
                try:
                    await write_modal_dispatch_state_async(job_id, "dispatching")
                except Exception as error:
                    try:
                        await put_claim(gpu_key, {**new_claim, "state": "dispatch_failed"})
                    except Exception:
                        pass
                    raise WorkerDispatchError("GPU dispatch state could not be persisted") from error
                try:
                    call = await VideoGenerator().generate.spawn.aio(
                        job_id=job_id,
                        prompt=job.get("prompt", ""),
                        model=job.get("model", MODEL_ID),
                        duration=job.get("duration", MAX_DURATION),
                        resolution=job.get("resolution", "480p"),
                        aspect_ratio=job.get("aspect_ratio", "9:16"),
                        seed=job.get("seed", 0),
                    )
                except Exception as error:
                    if definitive_modal_rejection(error):
                        try:
                            await put_claim(gpu_key, {**new_claim, "state": "dispatch_failed"})
                            await write_modal_dispatch_state_async(job_id, "dispatch_failed")
                        except Exception as persist_error:
                            log_event("gpu_dispatch_rejection_persist_failed", job=job_id,
                                      error_type=type(persist_error).__name__)
                        raise WorkerDispatchError("Modal rejected the GPU invocation") from error
                    log_event("gpu_dispatch_outcome_ambiguous", job=job_id,
                              error_type=type(error).__name__)
                    raise CallbackWaitDeferredError("Modal GPU dispatch outcome is not yet known") from error

                call_id = getattr(call, "object_id", "")
                if not isinstance(call_id, str) or not call_id:
                    log_event("gpu_call_reference_missing", job=job_id)
                    raise CallbackWaitDeferredError("Modal GPU call reference is not yet available")
                try:
                    await write_modal_dispatch_state_async(job_id, "dispatched", call_id)
                except Exception as error:
                    log_event("gpu_call_reference_save_failed", job=job_id,
                              error_type=type(error).__name__)
                gpu_claim = {**new_claim, "state": "dispatched", "modal_call_id": call_id}
                try:
                    await put_claim(gpu_key, gpu_claim)
                except Exception as error:
                    log_event("gpu_call_claim_save_failed", job=job_id,
                              error_type=type(error).__name__)
            else:
                try:
                    gpu_claim = await get_claim(gpu_key)
                except TRANSIENT_MODAL_CALL_ERRORS as error:
                    raise CallbackWaitDeferredError("Modal GPU claim reload will be retried") from error

        if not isinstance(gpu_claim, dict):
            raise WorkerDispatchInterrupted("GPU dispatch claim could not be recovered")
        if gpu_claim.get("fingerprint") != fingerprint:
            raise WorkerDispatchInterrupted("GPU dispatch claim does not match this request")
        if gpu_claim.get("state") == "dispatch_failed":
            raise WorkerDispatchError("Modal rejected the GPU invocation")

        call_id = gpu_claim.get("modal_call_id")
        if not call_id:
            try:
                dispatch = await read_modal_dispatch_state_async(job_id)
            except Exception as error:
                raise SupervisorStateError("GPU dispatch reference could not be read") from error
            call_id = dispatch.get("modal_call_id", "")

        deadline = time.monotonic() + CLAIM_RECOVERY_SECONDS
        while not call_id and gpu_claim.get("state") == "dispatching":
            if time.monotonic() >= deadline:
                raise CallbackWaitDeferredError("GPU dispatch is claimed but its call reference is unavailable")
            await asyncio.sleep(0.25)
            try:
                gpu_claim = await get_claim(gpu_key)
            except TRANSIENT_MODAL_CALL_ERRORS as error:
                raise CallbackWaitDeferredError("Modal GPU claim reload will be retried") from error
            if not isinstance(gpu_claim, dict):
                raise SupervisorStateError("GPU dispatch claim could not be read")
            if gpu_claim.get("fingerprint") != fingerprint:
                raise WorkerDispatchInterrupted("GPU dispatch claim does not match this request")
            if gpu_claim.get("state") == "dispatch_failed":
                raise WorkerDispatchError("Modal rejected the GPU invocation")
            try:
                persisted = await read_job_async(job_id)
            except Exception as error:
                raise SupervisorStateError("Persisted GPU state could not be reloaded") from error
            if isinstance(persisted, dict) and persisted.get("status") in {"completed", "failed"}:
                return
            call_id = gpu_claim.get("modal_call_id")
            if not call_id:
                try:
                    dispatch = await read_modal_dispatch_state_async(job_id)
                except Exception as error:
                    raise SupervisorStateError("GPU dispatch reference could not be reloaded") from error
                call_id = dispatch.get("modal_call_id", "")

        if not isinstance(call_id, str) or not call_id:
            raise CallbackWaitDeferredError("GPU dispatch still has no Modal call reference")
        try:
            if call is None:
                call = modal.FunctionCall.from_id(call_id)
            await call.get.aio()
        except asyncio.CancelledError:
            if call is not None:
                try:
                    await asyncio.shield(call.cancel.aio())
                except Exception as cancel_error:
                    log_event("gpu_cancel_propagation_failed", job=job_id,
                              error_type=type(cancel_error).__name__)
            raise
        except TRANSIENT_MODAL_CALL_ERRORS as error:
            raise CallbackWaitDeferredError("Modal could not confirm the GPU result yet") from error

    result = await run_callback_supervisor(
        job_id, callback_url, callback_token, run_generation,
        read_job_async, write_job_async,
    )
    try:
        request_claim = await get_claim(f"request:{job_id}")
        if isinstance(request_claim, dict):
            await put_claim(f"request:{job_id}", {**request_claim, "state": result["status"]})
    except Exception as error:
        log_event("request_claim_terminal_update_failed", job=job_id,
                  error_type=type(error).__name__)
    return result


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

    def validate_callback(body: dict[str, Any]) -> dict[str, Any] | None:
        try:
            return callback_submission_from_body(body)
        except ValueError as error:
            raise HTTPException(status_code=400, detail=str(error))

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
        callback_submission = validate_callback(body)
        options = _validated_create_options(body, HTTPException)
        callback_mode = callback_submission["mode"] if callback_submission else ""
        callback_url = callback_submission.get("callback_url", "") if callback_submission else ""
        callback_token = callback_submission.get("callback_token", "") if callback_submission else ""
        job_id = (
            callback_submission["job_id"]
            if callback_mode == "callback"
            else f"gen_{uuid.uuid4().hex}"
        )
        request_fingerprint = ""
        if callback_mode == "callback":
            request_fingerprint = callback_request_fingerprint(
                options["prompt"],
                options["model"],
                options["duration"],
                options["resolution"],
                options["aspect_ratio"],
                callback_url,
                callback_token,
                seed=body.get("seed"),
                seed_was_supplied=("seed" in body and body.get("seed") is not None),
            )
            try:
                request_claim = await get_claim(f"request:{job_id}")
                if request_claim is None:
                    request_claim = {
                        "fingerprint": request_fingerprint,
                        "state": "accepted",
                        "created_at": int(time.time()),
                    }
                    acquired = await put_claim(
                        f"request:{job_id}", request_claim, skip_if_exists=True
                    )
                    if not acquired:
                        request_claim = await get_claim(f"request:{job_id}")
            except Exception as error:
                log_event("request_claim_unavailable", job=job_id,
                          error_type=type(error).__name__)
                raise HTTPException(status_code=503, detail="generation request is temporarily unavailable")
            if not isinstance(request_claim, dict):
                raise HTTPException(status_code=503, detail="generation request is temporarily unavailable")
            if request_claim.get("fingerprint") != request_fingerprint:
                raise HTTPException(status_code=409, detail="job_id was already used for a different request")

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
        if callback_mode:
            job.update({
                "callback_url": callback_url,
                "callback_token": callback_token,
                "callback_mode": callback_mode,
            })
        if callback_mode == "callback":
            job["request_fingerprint"] = request_fingerprint
        if callback_mode == "callback":
            try:
                prior_job = await read_job_async(job_id)
            except Exception as error:
                log_event("job_state_read_failed", job=job_id,
                          error_type=type(error).__name__)
                raise HTTPException(status_code=503, detail="generation request is temporarily unavailable")
            if prior_job is None:
                await write_job_async(job_id, job)
            elif prior_job.get("request_fingerprint") != request_fingerprint:
                raise HTTPException(status_code=409, detail="job_id was already used for a different request")
            else:
                # The first accepted request owns its generated seed and
                # creation time; duplicates reuse the exact persisted job.
                job = prior_job
            try:
                supervisor_call = await callback_video_supervisor.spawn.aio(job_id=job_id)
                supervisor_call_id = getattr(supervisor_call, "object_id", "")
                if isinstance(supervisor_call_id, str) and supervisor_call_id:
                    log_event("callback_supervisor_accepted", job=job_id)
                else:
                    log_event("callback_supervisor_ack_missing", job=job_id)
            except Exception as error:
                if definitive_modal_rejection(error):
                    latest = await read_job_async(job_id) or job
                    if latest.get("status") not in {"completed", "failed"}:
                        latest.update({
                            "status": "failed",
                            "stage": "dispatch_failed",
                            "failure_stage": "dispatch",
                            "error_code": "dispatch_failed",
                            "progress": max(5, int(latest.get("progress", 0) or 0)),
                            "error": "Could not start video generation.",
                            "completed_at": int(time.time()),
                        })
                        await write_job_async(job_id, latest)
                    event = terminal_callback_event(job_id, latest)
                    if event is not None:
                        await asyncio.to_thread(
                            deliver_video_callback, callback_url, callback_token, event
                        )
                    try:
                        await put_claim(f"request:{job_id}", {
                            **request_claim, "state": "failed",
                        })
                    except Exception:
                        pass
                    log_event("callback_supervisor_rejected", job=job_id,
                              error_type=type(error).__name__)
                else:
                    # An uncertain CPU supervisor acknowledgement is accepted
                    # as pending. Retrying this stable ID can attach another
                    # supervisor, while the GPU Dict claim prevents duplicate
                    # inference.
                    log_event("callback_supervisor_outcome_ambiguous", job=job_id,
                              error_type=type(error).__name__)
        else:
            await write_job_async(job_id, job)
            try:
                await write_modal_dispatch_state_async(job_id, "dispatching")
                call = await VideoGenerator().generate.spawn.aio(
                    **options,
                    job_id=job_id,
                )
            except Exception as error:
                if definitive_modal_rejection(error):
                    failed = await read_job_async(job_id) or job
                    if failed.get("status") == "pending":
                        failed.update({
                            "status": "failed",
                            "stage": "dispatch_failed",
                            "failure_stage": "dispatch",
                            "error_code": "dispatch_failed",
                            "error": "Could not start video generation.",
                            "completed_at": int(time.time()),
                        })
                        await write_job_async(job_id, failed)
                        if callback_mode == "legacy":
                            queue_terminal_callback(
                                job_id, "failed", options["model"], callback_url, callback_token
                            )
                    log_event("gpu_dispatch_rejected", job=job_id,
                              error_type=type(error).__name__)
                    raise HTTPException(status_code=500, detail="could not start video generation")
                # Any non-definitive acknowledgement leaves the durable job
                # pending. Never turn an unknown spawn outcome into a failure.
                log_event("gpu_dispatch_outcome_ambiguous", job=job_id,
                          error_type=type(error).__name__)
                return {
                    "id": job_id,
                    "status": "pending",
                    "model": options["model"],
                    "progress": 0,
                    "poll_after_seconds": STATUS_POLL_RETRY_SECONDS,
                }

            call_id = getattr(call, "object_id", "")
            if isinstance(call_id, str) and call_id:
                try:
                    await write_modal_call_id_async(job_id, call_id)
                    log_event("gpu_job_dispatched", job=job_id, call_id=call_id)
                except Exception as error:
                    log_event("gpu_dispatch_reference_save_failed", job=job_id,
                              error_type=type(error).__name__)
            else:
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
        callback_delivery = callback_delivery_summary(job)
        if callback_delivery is not None:
            result["callback_delivery"] = callback_delivery
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
