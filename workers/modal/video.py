import asyncio
import hashlib
import ipaddress
import json
import logging
import os
import re
import secrets
import subprocess
import sys
import threading
import time
import uuid
import warnings
from datetime import datetime, timezone
from pathlib import Path
from urllib import error as urllib_error
from urllib import request as urllib_request
from urllib.parse import urlsplit

import modal


# ============================================================
# CONFIG
# ============================================================

APP_NAME = "video-generation"

MODEL_ID = "modal/wan2.2-lightning-a14b"
MODEL_NAME = "Wan 2.2 Lightning A14B"

BASE_MODEL_REPO = "Wan-AI/Wan2.2-T2V-A14B"
LIGHTNING_REPO = "lightx2v/Wan2.2-Lightning"

MODEL_CACHE = "/models"
JOBS_DIR = "/jobs"

WAN_REPO = "/opt/wan-lightning"

TASK = "t2v-A14B"

FPS = 16

MIN_DURATION = 1
MAX_DURATION = 15

KEEP_WARM_SECONDS = 120

# Keep model components resident on the A100 between requests.
# Set this to True only if you run into CUDA OOM errors.
OFFLOAD_MODEL = True
MODEL_DOWNLOAD_SENTINEL = f"{MODEL_CACHE}/.wan22_download_complete"

# Runtime telemetry cadence while a job is active.
TELEMETRY_INTERVAL_SECONDS = 10

# Modal public A100 80 GB rate as of 2026-09-23.
# usage.cost is calculated from measured generation runtime.
# Modal workspace billing is aggregated and is not available as an exact
# per-request live invoice amount at completion time.
A100_80GB_USD_PER_SECOND = 0.000694
STATUS_POLL_RETRY_SECONDS = 2

# Callback delivery uses short bounded HTTP attempts; a Modal CPU supervisor
# owns durable result replay after transient delivery or control-plane errors.
CALLBACK_DELIVERY_MAX_ATTEMPTS = 5
CALLBACK_DELIVERY_TIMEOUT_SECONDS = 10
CALLBACK_DELIVERY_MAX_RETRY_AFTER_SECONDS = 15
CALLBACK_RETRY_DELAYS_SECONDS = (0, 1, 2, 4, 8)
CALLBACK_JOB_ID = re.compile(r"^[A-Za-z0-9_-]{1,200}$")
CALLBACK_TOKEN = re.compile(r"^[A-Za-z0-9_-]{43,128}$")
CLAIMS_DICT_NAME = "video-generation-job-claims"
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


# ============================================================
# REQUEST OPTIONS
# ============================================================

SUPPORTED_RESOLUTIONS = [
    "480p",
    "720p",
]

SUPPORTED_ASPECT_RATIOS = [
    "9:16",
    "16:9",
]

# Wan uses width * height.
SIZE_MAP = {
    ("480p", "9:16"): "480*832",
    ("480p", "16:9"): "832*480",

    ("720p", "9:16"): "720*1280",
    ("720p", "16:9"): "1280*720",
}

CREATE_VIDEO_FIELDS = {
    "job_id",
    "model",
    "prompt",
    "duration",
    "resolution",
    "aspect_ratio",
    "seed",
    "generate_audio",
    "callback_url",
    "callback_token",
}


# ============================================================
# MODAL
# ============================================================

app = modal.App(APP_NAME)

model_volume = modal.Volume.from_name(
    "wan22-model-cache",
    create_if_missing=True,
)

jobs_volume = modal.Volume.from_name(
    "video-generation-jobs",
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


# ============================================================
# IMAGE
# ============================================================

FLASH_ATTN_WHEEL = (
    "https://github.com/Dao-AILab/flash-attention/releases/download/"
    "v2.7.4.post1/"
    "flash_attn-2.7.4.post1+cu12torch2.6cxx11abiFALSE-"
    "cp311-cp311-linux_x86_64.whl"
)

gpu_image = (
    modal.Image.debian_slim(
        python_version="3.11",
    )
    .apt_install(
        "git",
        "ffmpeg",
    )
    .pip_install(
        "torch==2.6.0",
        "torchvision==0.21.0",
        "numpy==2.2.4",
        "diffusers>=0.33,<0.36",
        "transformers>=4.49.0,<=4.51.3",
        "accelerate>=1.1.1",
        "safetensors",
        "huggingface_hub",
        "sentencepiece",
        "protobuf",
        "einops",
        "ftfy",
        "easydict",
        "dashscope",
        "tqdm",
        "imageio",
        "imageio-ffmpeg",
        "psutil",
        FLASH_ATTN_WHEEL,
    )
    .run_commands(
        f"git clone "
        f"https://github.com/ModelTC/"
        f"LightX2V-Wan2.2-Lightning.git "
        f"{WAN_REPO}"
    )
)

api_image = modal.Image.debian_slim(python_version="3.11").pip_install(
    "fastapi[standard]",
)


# ============================================================
# HELPERS
# ============================================================

# This warning originates inside the upstream Wan VAE implementation and
# does not affect generation. Hide only this specific deprecation warning
# so application logs stay readable.
warnings.filterwarnings(
    "ignore",
    message=r".*torch\.cuda\.amp\.autocast.*is deprecated.*",
    category=FutureWarning,
)


def utc_now() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="seconds")


def log_event(level: str, event: str, **fields):
    """Emit one compact, searchable application log line."""
    pieces = [
        utc_now(),
        level.upper(),
        event,
    ]

    for key, value in fields.items():
        if value is None:
            continue
        pieces.append(f"{key}={value}")

    print(" | ".join(pieces), flush=True)


def collect_runtime_metrics() -> dict:
    """Best-effort CPU, RAM and NVIDIA GPU telemetry."""
    metrics = {
        "cpu_percent": None,
        "cpu_cores_percent": [],
        "cpu_temp_c": None,
        "ram_used_gb": None,
        "ram_total_gb": None,
        "ram_percent": None,
        "gpu_util_percent": None,
        "gpu_mem_used_mb": None,
        "gpu_mem_total_mb": None,
        "gpu_mem_percent": None,
        "gpu_temp_c": None,
        "gpu_power_w": None,
    }

    try:
        import psutil

        metrics["cpu_percent"] = round(psutil.cpu_percent(interval=None), 1)
        metrics["cpu_cores_percent"] = [
            round(value, 1)
            for value in psutil.cpu_percent(interval=None, percpu=True)
        ]

        memory = psutil.virtual_memory()
        metrics["ram_used_gb"] = round(memory.used / (1024 ** 3), 2)
        metrics["ram_total_gb"] = round(memory.total / (1024 ** 3), 2)
        metrics["ram_percent"] = round(memory.percent, 1)

        # Cloud containers often do not expose host CPU thermal sensors.
        try:
            temperatures = psutil.sensors_temperatures()
            readings = [
                reading.current
                for entries in temperatures.values()
                for reading in entries
                if reading.current is not None
            ]
            if readings:
                metrics["cpu_temp_c"] = round(max(readings), 1)
        except (AttributeError, OSError):
            pass

    except Exception:
        pass

    try:
        query = [
            "nvidia-smi",
            "--query-gpu=utilization.gpu,memory.used,memory.total,temperature.gpu,power.draw",
            "--format=csv,noheader,nounits",
        ]
        raw = subprocess.check_output(
            query,
            text=True,
            stderr=subprocess.DEVNULL,
            timeout=3,
        ).strip().splitlines()[0]

        values = [part.strip() for part in raw.split(",")]
        gpu_util, mem_used, mem_total, gpu_temp, gpu_power = map(float, values)

        metrics["gpu_util_percent"] = round(gpu_util, 1)
        metrics["gpu_mem_used_mb"] = round(mem_used, 1)
        metrics["gpu_mem_total_mb"] = round(mem_total, 1)
        metrics["gpu_mem_percent"] = round(
            100.0 * mem_used / mem_total,
            1,
        ) if mem_total else None
        metrics["gpu_temp_c"] = round(gpu_temp, 1)
        metrics["gpu_power_w"] = round(gpu_power, 1)
    except Exception:
        pass

    return metrics


def log_runtime_metrics(job_id: str, stage: str, metrics: dict):
    cores = metrics.get("cpu_cores_percent") or []
    core_text = "[" + ",".join(f"{value:.0f}" for value in cores) + "]%"

    gpu_memory = "n/a"
    if metrics.get("gpu_mem_used_mb") is not None:
        gpu_memory = (
            f"{metrics['gpu_mem_used_mb']:.0f}/"
            f"{metrics['gpu_mem_total_mb']:.0f}MB"
            f"({metrics['gpu_mem_percent']:.1f}%)"
        )

    log_event(
        "INFO",
        "telemetry",
        job=job_id,
        stage=stage,
        gpu=f"{metrics.get('gpu_util_percent')}%",
        vram=gpu_memory,
        gpu_temp=f"{metrics.get('gpu_temp_c')}C",
        gpu_power=f"{metrics.get('gpu_power_w')}W",
        cpu=f"{metrics.get('cpu_percent')}%",
        cores=core_text,
        cpu_temp=(
            f"{metrics.get('cpu_temp_c')}C"
            if metrics.get("cpu_temp_c") is not None
            else "n/a"
        ),
        ram=(
            f"{metrics.get('ram_used_gb')}/"
            f"{metrics.get('ram_total_gb')}GB"
            f"({metrics.get('ram_percent')}%)"
        ),
    )


def duration_to_frames(
    duration: int,
    fps: int = FPS,
) -> int:
    """
    Wan requires frame_num = 4n + 1.
    """

    target_frames = duration * fps

    n = round(
        (target_frames - 1) / 4
    )

    return 4 * n + 1


def job_json_path(
    job_id: str,
) -> Path:

    return Path(
        JOBS_DIR,
        f"{job_id}.json",
    )


def job_video_path(
    job_id: str,
) -> Path:
    """
    Generated filename ALWAYS matches generation ID.

    Example:

    gen_abc123
        ->
    gen_abc123.mp4
    """

    return Path(
        JOBS_DIR,
        f"{job_id}.mp4",
    )


def job_call_path(job_id: str) -> Path:
    if not isinstance(job_id, str) or not CALLBACK_JOB_ID.fullmatch(job_id):
        raise ValueError("invalid job id")
    return Path(JOBS_DIR, f"{job_id}.call.json")


def _write_job_file(job_id: str, data: dict):
    path = job_json_path(job_id)
    temp = Path(f"{path}.tmp")
    temp.write_text(
        json.dumps(data, indent=2),
        encoding="utf-8",
    )
    os.replace(temp, path)


def write_job(job_id: str, data: dict):
    """Synchronous worker-side job persistence."""
    _write_job_file(job_id, data)
    jobs_volume.commit()


async def write_job_async(job_id: str, data: dict):
    """Async API-side job persistence; never blocks FastAPI's event loop."""
    _write_job_file(job_id, data)
    await jobs_volume.commit.aio()


async def write_modal_dispatch_state_async(job_id: str, state: str, call_id: str = ""):
    """Persist dispatch state separately from the mutable GPU job record."""
    if state not in {"dispatching", "dispatched", "dispatch_failed"}:
        raise ValueError("invalid GPU dispatch state")
    if call_id and not isinstance(call_id, str):
        raise ValueError("invalid Modal call ID")
    path = job_call_path(job_id)
    temporary = Path(f"{path}.{uuid.uuid4().hex}.tmp")
    try:
        temporary.write_text(
            json.dumps({"dispatch_state": state, "modal_call_id": call_id}),
            encoding="utf-8",
        )
        os.replace(temporary, path)
        await jobs_volume.commit.aio()
    finally:
        try:
            temporary.unlink(missing_ok=True)
        except OSError:
            pass


async def read_modal_dispatch_state_async(job_id: str) -> dict:
    await jobs_volume.reload.aio()
    path = job_call_path(job_id)
    if not path.is_file():
        return {}
    value = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(value, dict):
        return {}
    return {
        "dispatch_state": str(value.get("dispatch_state", "")),
        "modal_call_id": str(value.get("modal_call_id", "")),
    }


def _read_job_file(job_id: str):
    path = job_json_path(job_id)
    if not path.exists():
        return None
    return json.loads(path.read_text(encoding="utf-8"))


def read_job(job_id: str):
    """Synchronous worker-side read."""
    jobs_volume.reload()
    return _read_job_file(job_id)


async def read_job_async(job_id: str):
    """Async API-side read; fixes Modal AsyncUsageWarning."""
    await jobs_volume.reload.aio()
    return _read_job_file(job_id)


class WorkerDispatchError(Exception):
    """Modal definitively rejected the GPU invocation."""


class WorkerDispatchInterrupted(WorkerDispatchError):
    """A claimed dispatch cannot be recovered without risking duplication."""


class CallbackDeliveryError(RuntimeError):
    """A durable terminal event could not be delivered after bounded retries."""


class SupervisorStateError(RuntimeError):
    """Durable coordination state is missing or temporarily unreadable."""


class CallbackWaitDeferredError(RuntimeError):
    """Modal has not confirmed a child result yet; retry the same supervisor."""


def definitive_modal_rejection(error: Exception) -> bool:
    return isinstance(error, DEFINITIVE_MODAL_REJECTIONS)


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
    if not isinstance(job_id, str) or not CALLBACK_JOB_ID.fullmatch(job_id):
        raise ValueError("job_id must be a safe generation ID")
    if not isinstance(callback_url, str) or not callback_url or len(callback_url) > 2048:
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
    if not isinstance(job_id, str) or not CALLBACK_JOB_ID.fullmatch(job_id):
        raise ValueError("job_id must be a safe generation ID")
    if not isinstance(callback_token, str) or not CALLBACK_TOKEN.fullmatch(callback_token):
        raise ValueError("callback_token must be a 32-byte URL-safe token")
    validate_callback_target(job_id, callback_url)


def validate_legacy_callback_submission(callback_url: object, callback_token: object) -> None:
    if not isinstance(callback_token, str) or not CALLBACK_TOKEN.fullmatch(callback_token):
        raise ValueError("callback_token is invalid")
    if not isinstance(callback_url, str) or len(callback_url) > 2048:
        raise ValueError("callback_url is invalid")
    try:
        parsed = urlsplit(callback_url)
        _ = parsed.port
    except (TypeError, ValueError) as error:
        raise ValueError("callback_url is invalid") from error
    if (
        parsed.scheme != "https"
        or not parsed.hostname
        or not _public_callback_host(parsed.hostname)
        or parsed.username is not None
        or parsed.password is not None
        or parsed.query
        or parsed.fragment
        or not parsed.path.endswith("/api/provider-callbacks/modal")
    ):
        raise ValueError("callback_url must use the legacy public HTTPS endpoint")


def callback_submission_from_body(body: dict) -> dict | None:
    """Accept the stable per-job contract, the legacy Go contract, or neither."""
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
    request = {
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


async def get_claim(key: str):
    return await job_claims.get.aio(key)


async def put_claim(key: str, value: dict, skip_if_exists: bool = False):
    return await job_claims.put.aio(key, value, skip_if_exists=skip_if_exists)


def request_matches_job(job: dict, request: dict, compare_seed: bool = True) -> bool:
    for field in ("prompt", "model", "duration", "resolution", "aspect_ratio"):
        if job.get(field) != request.get(field):
            return False
    return not compare_seed or job.get("seed") == request.get("seed")


def classify_worker_failure(error: Exception, job: dict | None = None) -> str:
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


async def reconcile_modal_call(job: dict) -> dict:
    """Surface GPU startup errors without replacing a newer/terminal job record."""
    if job.get("status") in {"completed", "failed"}:
        return job
    job_id = job.get("id")
    if not isinstance(job_id, str) or not CALLBACK_JOB_ID.fullmatch(job_id):
        return job
    call_id = job.get("modal_call_id")
    if not call_id:
        try:
            dispatch = await read_modal_dispatch_state_async(job_id)
        except Exception:
            return job
        call_id = dispatch.get("modal_call_id")
    if not call_id:
        return job
    try:
        call = modal.FunctionCall.from_id(call_id)
        await call.get.aio(timeout=0)
    except TimeoutError:
        return job
    except TRANSIENT_MODAL_CALL_ERRORS as error:
        log_event("WARNING", "modal_call_status_deferred", job=job_id, error_type=type(error).__name__)
        return job
    except Exception as error:
        try:
            current = await read_job_async(job_id)
        except Exception:
            return job
        if not isinstance(current, dict) or current.get("status") in {"completed", "failed"}:
            return current if isinstance(current, dict) else job
        failed = dict(current)
        failed.update(
            {
                "status": "failed",
                "stage": "worker_failed",
                "failure_stage": "worker_startup",
                "error_code": classify_worker_failure(error, {"stage": "worker_startup"}),
                "progress": max(0, int(current.get("progress", 0) or 0)),
                "error": "Video generation worker failed. Check Modal logs for details.",
                "completed_at": int(time.time()),
            }
        )
        await write_job_async(job_id, failed)
        callback_url = failed.get("callback_url")
        callback_token = failed.get("callback_token")
        callback_mode = failed.get("callback_mode")
        if callback_mode == "legacy" and callback_url and callback_token:
            schedule_terminal_callback(
                job_id,
                callback_url,
                callback_token,
                terminal_callback_payload(job_id, "failed", str(failed.get("model") or MODEL_ID)),
            )
        elif callback_mode == "callback" and callback_url and callback_token:
            event = terminal_callback_event(job_id, failed)
            await asyncio.to_thread(deliver_video_callback, callback_url, callback_token, event)
        log_event("ERROR", "modal_worker_failed", job=job_id, call_id=call_id, error_type=type(error).__name__)
        return failed
    return job


# ============================================================
# TERMINAL CALLBACK DELIVERY
# ============================================================

def terminal_callback_payload(job_id: str, status: str, model: str, cost=None) -> dict:
    """Return the small, credential-free terminal callback contract."""
    payload = {
        "id": job_id,
        "status": status,
        "model": model,
    }

    if status == "completed":
        payload["cost_usd"] = (
            f"{float(cost):.6f}"
            if cost is not None
            else ""
        )
    else:
        # Do not copy a worker exception into an external callback. The
        # dashboard already turns provider failure into a safe operator message.
        payload["error"] = "Video generation failed"

    return payload


class CallbackNoRedirectHandler(urllib_request.HTTPRedirectHandler):
    """Keep the per-job callback capability bound to its original endpoint."""

    def redirect_request(self, req, fp, code, msg, headers, newurl):
        # urllib otherwise copies request headers, including the callback token,
        # onto the redirect target. The dashboard callback URL is configured
        # before submission and does not need redirect support.
        return None


def open_callback_request(request, timeout):
    return urllib_request.build_opener(
        CallbackNoRedirectHandler(),
    ).open(
        request,
        timeout=timeout,
    )


def _callback_event(
    job_id: str,
    status: str,
    sequence: int,
    progress: int,
    stage: str,
    error_code: str = "",
    job: dict | None = None,
) -> dict:
    event = {
        "id": job_id,
        "status": status,
        "sequence": sequence,
        "progress": max(0, min(100, int(progress))),
        "stage": str(stage)[:100],
    }
    if error_code:
        event["error_code"] = error_code
    if status == "completed" and job and job.get("usage_cost_usd") is not None:
        event["cost_usd"] = f"{float(job['usage_cost_usd']):.6f}"
    return event


def terminal_callback_event(job_id: str, job: dict) -> dict | None:
    status = job.get("status")
    if status == "completed":
        return _callback_event(job_id, "completed", 2, 100, "completed", job=job)
    if status == "failed":
        code = classify_worker_failure(RuntimeError("persisted failure"), job)
        progress = min(99, max(5, int(job.get("progress", 5) or 5)))
        stage = str(job.get("failure_stage") or job.get("stage") or "failed")
        return _callback_event(job_id, "failed", 2, progress, stage, error_code=code)
    return None


def post_video_callback_once(callback_url: str, callback_token: str, payload: dict) -> int:
    outbound = urllib_request.Request(
        callback_url,
        data=json.dumps(payload, separators=(",", ":")).encode("utf-8"),
        method="POST",
        headers={
            "Content-Type": "application/json",
            "Accept": "application/json",
            "User-Agent": "FrameVault-Modal-Callback/1.0",
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


def deliver_video_callback(
    callback_url: str,
    callback_token: str,
    payload: dict,
    post_once=None,
    sleep=time.sleep,
    return_result: bool = False,
) -> bool | dict:
    """Send a per-job bearer callback with redirects disabled and bounded retries."""
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
            outcome = {"state": "delivered", "code": "delivered", "status_code": status, "attempts": attempt}
            return outcome if return_result else True
        if status is not None and status not in {408, 425, 429, 500, 502, 503, 504}:
            log_event("WARNING", "callback_rejected", job=payload.get("id"), status=status)
            outcome = {"state": "rejected", "code": f"http_{status}", "status_code": status, "attempts": attempt}
            return outcome if return_result else False
    log_event(
        "ERROR",
        "callback_delivery_exhausted",
        job=payload.get("id"),
        attempts=len(CALLBACK_RETRY_DELAYS_SECONDS),
    )
    code = f"http_{last_status}" if last_status is not None else "network_error"
    outcome = {
        "state": "retry_exhausted",
        "code": code,
        "status_code": last_status,
        "attempts": len(CALLBACK_RETRY_DELAYS_SECONDS),
    }
    return outcome if return_result else False


def callback_delivery_summary(job: dict) -> dict | None:
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
    return {
        "state": delivery["state"],
        "code": code,
        "status_code": status_code,
        "attempts": attempts,
    }


async def persist_callback_delivery(job_id: str, job: dict, outcome: dict, write_job_fn) -> None:
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
        log_event("ERROR", "callback_delivery_state_persist_failed", job=job_id, code="storage_failed")
        raise SupervisorStateError("Callback delivery result could not be persisted") from error


def callback_delivery_rejection_result(event: dict, outcome: dict) -> dict:
    result = dict(event)
    result["delivery_rejected"] = True
    result["delivery_code"] = outcome["code"]
    result["delivery_status"] = outcome["status_code"]
    return result


async def run_callback_supervisor(
    job_id: str,
    callback_url: str,
    callback_token: str,
    run_generation,
    read_job_fn,
    write_job_fn,
    post_once=None,
    sleep=time.sleep,
) -> dict:
    """Persist and replay terminal outcomes; retries never regenerate a saved result."""
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
            post_once=post_once,
            sleep=sleep,
            return_result=True,
        )
        await persist_callback_delivery(job_id, job, outcome, write_job_fn)
        if outcome["state"] == "retry_exhausted":
            raise CallbackDeliveryError("Transient terminal callback delivery failed after bounded retries")
        if outcome["state"] == "rejected":
            log_event("WARNING", "callback_delivery_rejected_terminal", job=job_id, code=outcome["code"], status=outcome["status_code"])
            return callback_delivery_rejection_result(event, outcome)
        return event

    outcome = deliver_video_callback(
        callback_url,
        callback_token,
        _callback_event(
            job_id,
            "processing",
            1,
            max(5, int(job.get("progress", 5) or 5)),
            str(job.get("stage") or "starting"),
        ),
        post_once=post_once,
        sleep=sleep,
        return_result=True,
    )
    if outcome["state"] == "retry_exhausted":
        await persist_callback_delivery(job_id, job, outcome, write_job_fn)
        raise CallbackDeliveryError("Transient initial callback delivery failed after bounded retries")
    if outcome["state"] == "rejected":
        rejected_job = dict(job)
        rejected_job.update(
            {
                "id": job_id,
                "status": "failed",
                "stage": "failed",
                "failure_stage": "callback",
                "error_code": "callback_delivery_rejected",
                "error": "Callback endpoint rejected the worker's initial status update. Check VIDEO_CALLBACK_BASE_URL, the deployed receiver, and edge access rules.",
                "completed_at": int(time.time()),
            }
        )
        await persist_callback_delivery(job_id, rejected_job, outcome, write_job_fn)
        event = terminal_callback_event(job_id, rejected_job)
        log_event("WARNING", "callback_delivery_rejected_before_generation", job=job_id, code=outcome["code"], status=outcome["status_code"])
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
        failed_job.update(
            {
                "id": job_id,
                "status": "failed",
                "stage": "failed",
                "failure_stage": failure_stage,
                "error_code": classify_worker_failure(caught_error, job),
                "error": "Video generation failed. Check the Modal worker logs.",
                "completed_at": int(time.time()),
            }
        )
        try:
            await write_job_fn(job_id, failed_job)
        except Exception as error:
            log_event("ERROR", "callback_job_persist_failed", job=job_id, code="storage_failed")
            raise SupervisorStateError("Terminal video failure could not be persisted") from error
        job = failed_job
        event = terminal_callback_event(job_id, failed_job)

    outcome = deliver_video_callback(
        callback_url,
        callback_token,
        event,
        post_once=post_once,
        sleep=sleep,
        return_result=True,
    )
    await persist_callback_delivery(job_id, job, outcome, write_job_fn)
    if outcome["state"] == "retry_exhausted":
        raise CallbackDeliveryError("Transient terminal callback delivery failed after bounded retries")
    if outcome["state"] == "rejected":
        log_event("WARNING", "callback_delivery_rejected_terminal", job=job_id, code=outcome["code"], status=outcome["status_code"])
        event = callback_delivery_rejection_result(event, outcome)
    if event["status"] == "failed":
        log_event("WARNING", "callback_job_failed", job=job_id, code=event.get("error_code"))
    return event


@app.function(
    image=api_image,
    timeout=120,
)
def deliver_terminal_callback(
    callback_url: str,
    callback_token: str,
    payload: dict,
):
    """POST a terminal result with bounded retries and no secret logging."""
    body = json.dumps(
        payload,
        separators=(",", ":"),
    ).encode("utf-8")

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
                    "User-Agent": "FrameVault-Modal-Callback/1.0",
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
                log_event(
                    "INFO",
                    "callback_delivered",
                    job=payload.get("id"),
                    attempt=attempt,
                )
                return

            if 300 <= status_code < 500 and status_code != 429:
                log_event(
                    "WARNING",
                    "callback_rejected",
                    job=payload.get("id"),
                    status=status_code,
                )
                return

        except urllib_error.HTTPError as error:
            status_code = error.code
            retry_after = error.headers.get("Retry-After")
            if 300 <= status_code < 500 and status_code != 429:
                log_event(
                    "WARNING",
                    "callback_rejected",
                    job=payload.get("id"),
                    status=status_code,
                )
                return

        except Exception:
            # Network errors are intentionally summarized without URL/token
            # material. The next bounded attempt may use a fresh route.
            pass

        if attempt == CALLBACK_DELIVERY_MAX_ATTEMPTS:
            break

        delay_seconds = min(
            2 ** (attempt - 1),
            CALLBACK_DELIVERY_MAX_RETRY_AFTER_SECONDS,
        )
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
        "ERROR",
        "callback_delivery_exhausted",
        job=payload.get("id"),
        attempts=CALLBACK_DELIVERY_MAX_ATTEMPTS,
    )


def schedule_terminal_callback(
    job_id: str,
    callback_url: str,
    callback_token: str,
    payload: dict,
):
    """Queue CPU-side delivery after the terminal job record is durable."""
    try:
        deliver_terminal_callback.spawn(
            callback_url,
            callback_token,
            payload,
        )
        log_event(
            "INFO",
            "callback_delivery_scheduled",
            job=job_id,
        )
    except Exception:
        # The dashboard's delayed recovery query handles an invocation that
        # cannot be scheduled at all. Do not include callback credentials here.
        log_event(
            "ERROR",
            "callback_delivery_schedule_failed",
            job=job_id,
        )


# ============================================================
# PERSISTENT GPU WORKER
# ============================================================

@app.cls(
    image=gpu_image,

    gpu="A100-80GB",

    volumes={
        MODEL_CACHE: model_volume,
        JOBS_DIR: jobs_volume,
    },

    # Scale fully to zero: no A100 is kept running permanently.
    min_containers=0,
    max_containers=1,

    # Keep the live GPU only 2 minutes after the last request.
    scaledown_window=KEEP_WARM_SECONDS,

    # Snapshot the initialized Python/CUDA state so future cold starts can
    # restore rather than reconstruct the full Wan pipeline from scratch.
    enable_memory_snapshot=True,
    experimental_options={
        "enable_gpu_snapshot": True,
    },

    timeout=3600,

    startup_timeout=1800,
)
class VideoGenerator:

    # ========================================================
    # LOAD MODEL ONCE
    # ========================================================

    @modal.enter(snap=True)
    def load_model(self):

        from huggingface_hub import snapshot_download

        startup_started = time.time()
        log_event(
            "INFO",
            "worker_starting",
            model=MODEL_NAME,
            gpu="A100-80GB",
            gpu_snapshot=True,
            offload_model=OFFLOAD_MODEL,
        )

        self.base_model_path = os.path.join(
            MODEL_CACHE,
            "Wan2.2-T2V-A14B",
        )

        self.lightning_path = os.path.join(
            MODEL_CACHE,
            "Wan2.2-Lightning",
        )

        self.lora_path = os.path.join(
            self.lightning_path,
            "Wan2.2-T2V-A14B-4steps-lora-rank64-Seko-V1",
        )

        # ----------------------------------------------------
        # Download/cache model files once
        # ----------------------------------------------------

        if os.path.exists(MODEL_DOWNLOAD_SENTINEL):
            log_event("INFO", "model_cache_hit", path=MODEL_CACHE)
        else:
            log_event("INFO", "model_cache_miss", component="base_model")
            snapshot_download(
                repo_id=BASE_MODEL_REPO,
                local_dir=self.base_model_path,
            )

            log_event("INFO", "model_cache_miss", component="lightning_lora")
            snapshot_download(
                repo_id=LIGHTNING_REPO,
                local_dir=self.lightning_path,
            )

            if not os.path.exists(self.lora_path):
                raise RuntimeError(
                    f"Lightning LoRA not found: {self.lora_path}"
                )

            Path(MODEL_DOWNLOAD_SENTINEL).write_text(
                "download complete\n",
                encoding="utf-8",
            )
            model_volume.commit()

        if not os.path.exists(self.lora_path):
            raise RuntimeError(
                f"Lightning LoRA not found: {self.lora_path}"
            )

        # ----------------------------------------------------
        # Import Lightning Wan repo
        # ----------------------------------------------------

        if WAN_REPO not in sys.path:
            sys.path.insert(
                0,
                WAN_REPO,
            )

        import torch
        import wan

        from wan.configs import (
            SIZE_CONFIGS,
            SUPPORTED_SIZES,
            WAN_CONFIGS,
        )

        from wan.utils.utils import (
            save_video,
        )

        self.torch = torch
        self.SIZE_CONFIGS = SIZE_CONFIGS
        self.SUPPORTED_SIZES = SUPPORTED_SIZES
        self.save_video = save_video

        self.cfg = WAN_CONFIGS[
            TASK
        ]

        # ----------------------------------------------------
        # Persistent model
        # ----------------------------------------------------

        start = time.time()

        log_event("INFO", "model_loading", component="WanT2V")

        self.pipe = wan.WanT2V(
            config=self.cfg,

            checkpoint_dir=(
                self.base_model_path
            ),

            lora_dir=(
                self.lora_path
            ),

            device_id=0,

            rank=0,

            t5_fsdp=False,

            dit_fsdp=False,

            use_sp=False,

            t5_cpu=False,

            convert_model_dtype=False,
        )

        model_load_seconds = time.time() - start
        metrics = collect_runtime_metrics()
        log_event(
            "INFO",
            "model_ready",
            model_load_s=f"{model_load_seconds:.1f}",
            startup_total_s=f"{time.time() - startup_started:.1f}",
            vram_mb=(
                f"{metrics.get('gpu_mem_used_mb')}/"
                f"{metrics.get('gpu_mem_total_mb')}"
            ),
            gpu_temp_c=metrics.get("gpu_temp_c"),
        )

    # ========================================================
    # GENERATION JOB
    # ========================================================

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
    ):

        started_at = time.time()
        previous = read_job(job_id) or {}
        callback_url = str(previous.get("callback_url") or callback_url or "")
        callback_token = str(previous.get("callback_token") or callback_token or "")
        callback_mode = previous.get("callback_mode")
        if not callback_mode and callback_url and callback_token:
            callback_mode = "legacy"

        job = {
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
            "created_at": previous.get("created_at", int(started_at)),
            "error": "",
        }
        if callback_url:
            job["callback_url"] = callback_url
        if callback_token:
            job["callback_token"] = callback_token
        if callback_mode:
            job["callback_mode"] = callback_mode

        write_job(
            job_id,
            job,
        )

        try:

            # ------------------------------------------------
            # Convert OpenRouter-style settings to Wan
            # ------------------------------------------------

            frame_count = (
                duration_to_frames(
                    duration
                )
            )

            size_name = SIZE_MAP[
                (
                    resolution,
                    aspect_ratio,
                )
            ]

            if size_name not in (
                self.SUPPORTED_SIZES[
                    TASK
                ]
            ):
                raise ValueError(
                    f"Size {size_name} "
                    f"is not supported by {TASK}"
                )

            size = self.SIZE_CONFIGS[
                size_name
            ]

            output_path = (
                job_video_path(
                    job_id
                )
            )

            log_event(
                "INFO",
                "job_started",
                job=job_id,
                duration=f"{duration}s",
                frames=frame_count,
                resolution=resolution,
                aspect=aspect_ratio,
                wan_size=size_name,
                seed=seed,
                persistent_pipeline=True,
                offload_model=OFFLOAD_MODEL,
            )

            job["stage"] = "inference"
            job["progress"] = 20
            job["telemetry"] = collect_runtime_metrics()
            write_job(job_id, job)

            telemetry_stop = threading.Event()

            def telemetry_loop():
                # Prime psutil's CPU counters, then report throughout inference.
                try:
                    import psutil
                    psutil.cpu_percent(interval=None)
                    psutil.cpu_percent(interval=None, percpu=True)
                except Exception:
                    pass

                while not telemetry_stop.wait(TELEMETRY_INTERVAL_SECONDS):
                    current = collect_runtime_metrics()
                    job["telemetry"] = current
                    log_runtime_metrics(job_id, job.get("stage", "unknown"), current)
                    try:
                        write_job(job_id, job)
                    except Exception as telemetry_error:
                        log_event(
                            "WARNING",
                            "telemetry_persist_failed",
                            job=job_id,
                            error=repr(telemetry_error),
                        )

            telemetry_thread = threading.Thread(
                target=telemetry_loop,
                name=f"telemetry-{job_id}",
                daemon=True,
            )
            telemetry_thread.start()

            log_runtime_metrics(job_id, "inference", job["telemetry"])

            generation_start = (
                time.time()
            )

            # ------------------------------------------------
            # WAN INFERENCE
            # ------------------------------------------------

            video = self.pipe.generate(
                prompt,

                size=size,

                frame_num=(
                    frame_count
                ),

                shift=(
                    self.cfg.sample_shift
                ),

                sample_solver="euler",

                sampling_steps=(
                    self.cfg.sample_steps
                ),

                guide_scale=(
                    self.cfg
                    .sample_guide_scale
                ),

                seed=seed,

                offload_model=OFFLOAD_MODEL,
            )

            self.torch.cuda.synchronize()

            inference_seconds = (
                time.time()
                - generation_start
            )

            job["stage"] = "encoding"
            job["progress"] = 90
            job["inference_seconds"] = inference_seconds
            job["telemetry"] = collect_runtime_metrics()
            write_job(job_id, job)

            log_event(
                "INFO",
                "inference_complete",
                job=job_id,
                inference_s=f"{inference_seconds:.1f}",
            )
            log_runtime_metrics(job_id, "encoding", job["telemetry"])

            # ------------------------------------------------
            # SAVE
            #
            # filename == generation/request ID
            # ------------------------------------------------

            encode_start = (
                time.time()
            )

            self.save_video(
                tensor=video[None],

                save_file=str(
                    output_path
                ),

                fps=self.cfg.sample_fps,

                nrow=1,

                normalize=True,

                value_range=(-1, 1),
            )

            encode_seconds = (
                time.time()
                - encode_start
            )

            telemetry_stop.set()
            telemetry_thread.join(timeout=2)

            del video

            if OFFLOAD_MODEL:
                self.torch.cuda.empty_cache()

            if not output_path.exists():
                raise RuntimeError(
                    "Video generation finished "
                    "but output file was not created"
                )

            file_size = (
                output_path
                .stat()
                .st_size
            )

            if file_size <= 0:
                raise RuntimeError(
                    "Generated video file is empty"
                )

            # Make MP4 visible to API containers.
            jobs_volume.commit()

            total_seconds = (
                time.time()
                - started_at
            )

            # Per-request GPU generation cost. This uses the measured time
            # spent inside generate() and Modal's current published A100-80GB
            # per-second rate. It intentionally does not claim to include
            # later warm-idle time or workspace-level credits/adjustments.
            usage_cost_usd = round(
                total_seconds * A100_80GB_USD_PER_SECOND,
                6,
            )

            # ------------------------------------------------
            # COMPLETE
            # ------------------------------------------------

            job.update(
                {
                    "status": "completed",
                    "stage": "completed",
                    "progress": 100,
                    "telemetry": collect_runtime_metrics(),

                    "filename":
                        output_path.name,

                    "size_bytes":
                        file_size,

                    "inference_seconds":
                        inference_seconds,

                    "encode_seconds":
                        encode_seconds,

                    "total_seconds":
                        total_seconds,

                    "usage_cost_usd":
                        usage_cost_usd,

                    "gpu_billed_seconds":
                        total_seconds,

                    "gpu_rate_usd_per_second":
                        A100_80GB_USD_PER_SECOND,

                    "completed_at":
                        int(time.time()),

                    "error":
                        "",
                }
            )

            write_job(
                job_id,
                job,
            )

            # The terminal state is committed before delivery is scheduled.
            # Pass the callback values directly through this invocation rather
            # than depending on a later API container's view of the job file.
            if job.get("callback_mode") == "legacy" and callback_url and callback_token:
                schedule_terminal_callback(
                    job_id,
                    callback_url,
                    callback_token,
                    terminal_callback_payload(
                        job_id,
                        "completed",
                        model,
                        usage_cost_usd,
                    ),
                )

            log_event(
                "INFO",
                "job_completed",
                job=job_id,
                file=output_path.name,
                size_mb=f"{file_size / 1024 / 1024:.2f}",
                inference_s=f"{inference_seconds:.1f}",
                encode_s=f"{encode_seconds:.1f}",
                total_s=f"{total_seconds:.1f}",
                cost_usd=f"{usage_cost_usd:.6f}",
            )
            log_runtime_metrics(job_id, "completed", job["telemetry"])

        except Exception as error:

            if "telemetry_stop" in locals():
                telemetry_stop.set()
            if "telemetry_thread" in locals():
                telemetry_thread.join(timeout=2)

            log_event(
                "ERROR",
                "job_failed",
                job=job_id,
                stage=job.get("stage"),
                error_type=type(error).__name__,
            )

            failure_stage = str(job.get("stage") or "unknown")

            job.update(
                {
                    "status":
                        "failed",

                    "stage":
                        "failed",

                    "failure_stage":
                        failure_stage,

                    "error_code":
                        classify_worker_failure(error, {"failure_stage": failure_stage}),

                    "progress":
                        job.get("progress", 0),

                    "telemetry":
                        collect_runtime_metrics(),

                    "error":
                        "Video generation failed. Check the Modal worker logs.",

                    "completed_at":
                        int(time.time()),
                }
            )

            write_job(
                job_id,
                job,
            )

            # Failure follows the same durable-write-then-deliver ordering as
            # success. The callback uses a generic error so worker internals
            # never cross the dashboard boundary.
            if job.get("callback_mode") == "legacy" and callback_url and callback_token:
                schedule_terminal_callback(
                    job_id,
                    callback_url,
                    callback_token,
                    terminal_callback_payload(
                        job_id,
                        "failed",
                        model,
                    ),
                )

            raise


# ============================================================
# CALLBACK CPU SUPERVISOR
# ============================================================

@app.function(
    image=api_image,
    volumes={JOBS_DIR: jobs_volume},
    secrets=[api_secret],
    timeout=7200,
    retries=modal.Retries(
        max_retries=10,
        backoff_coefficient=2.0,
        initial_delay=1.0,
        max_delay=60.0,
    ),
)
async def callback_video_supervisor(
    job_id: str,
):
    """CPU orchestration observes GPU startup and replays saved callback outcomes."""
    try:
        request_job = await read_job_async(job_id)
    except Exception as error:
        raise SupervisorStateError("Persisted video request could not be read") from error
    if not isinstance(request_job, dict):
        raise SupervisorStateError("Persisted video request is unavailable")
    callback_url = request_job.get("callback_url")
    callback_token = request_job.get("callback_token")
    request_fingerprint = request_job.get("request_fingerprint")
    try:
        validate_callback_submission(job_id, callback_url, callback_token)
    except ValueError as error:
        raise SupervisorStateError("Persisted callback capability is invalid") from error
    if not isinstance(request_fingerprint, str) or not re.fullmatch(r"[a-f0-9]{64}", request_fingerprint):
        raise SupervisorStateError("Persisted request fingerprint is invalid")
    prompt = request_job.get("prompt", "")
    model = request_job.get("model", MODEL_ID)
    duration = request_job.get("duration", MAX_DURATION)
    resolution = request_job.get("resolution", "480p")
    aspect_ratio = request_job.get("aspect_ratio", "9:16")
    seed = request_job.get("seed", 0)

    async def run_generation():
        gpu_key = f"gpu:{job_id}"
        try:
            gpu_claim = await get_claim(gpu_key)
        except TRANSIENT_MODAL_CALL_ERRORS as error:
            raise CallbackWaitDeferredError("Modal GPU claim lookup will be retried") from error
        call = None
        if gpu_claim is None:
            new_claim = {
                "fingerprint": request_fingerprint,
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
                    call = await (
                        VideoGenerator()
                        .generate
                        .spawn.aio(
                            job_id=job_id,
                            prompt=prompt,
                            model=model,
                            duration=duration,
                            resolution=resolution,
                            aspect_ratio=aspect_ratio,
                            seed=seed,
                        )
                    )
                except Exception as error:
                    if definitive_modal_rejection(error):
                        try:
                            await put_claim(gpu_key, {**new_claim, "state": "dispatch_failed"})
                            await write_modal_dispatch_state_async(job_id, "dispatch_failed")
                        except Exception as persist_error:
                            log_event("ERROR", "gpu_dispatch_rejection_persist_failed", job=job_id, error_type=type(persist_error).__name__)
                        raise WorkerDispatchError("Modal rejected the GPU invocation") from error
                    log_event("WARNING", "gpu_dispatch_outcome_ambiguous", job=job_id, error_type=type(error).__name__)
                    raise CallbackWaitDeferredError("Modal GPU dispatch outcome is not yet known") from error

                call_id = getattr(call, "object_id", "")
                if not isinstance(call_id, str) or not call_id:
                    log_event("WARNING", "gpu_call_reference_missing", job=job_id)
                    raise CallbackWaitDeferredError("Modal GPU call reference is not yet available")
                try:
                    await write_modal_dispatch_state_async(job_id, "dispatched", call_id)
                except Exception as error:
                    log_event("WARNING", "gpu_call_reference_save_failed", job=job_id, error_type=type(error).__name__)
                gpu_claim = {**new_claim, "state": "dispatched", "modal_call_id": call_id}
                try:
                    await put_claim(gpu_key, gpu_claim)
                except Exception as error:
                    # The in-memory reference remains usable. A retry can use
                    # the sidecar, and terminal GPU state remains authoritative.
                    log_event("WARNING", "gpu_call_claim_save_failed", job=job_id, error_type=type(error).__name__)
            else:
                try:
                    gpu_claim = await get_claim(gpu_key)
                except TRANSIENT_MODAL_CALL_ERRORS as error:
                    raise CallbackWaitDeferredError("Modal GPU claim reload will be retried") from error

        if not isinstance(gpu_claim, dict):
            raise WorkerDispatchInterrupted("GPU dispatch claim could not be recovered")
        if gpu_claim.get("fingerprint") != request_fingerprint:
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
                raise CallbackWaitDeferredError("GPU dispatch is claimed but its call reference is not available")
            await asyncio.sleep(0.25)
            try:
                gpu_claim = await get_claim(gpu_key)
            except TRANSIENT_MODAL_CALL_ERRORS as error:
                raise CallbackWaitDeferredError("Modal GPU claim reload will be retried") from error
            if not isinstance(gpu_claim, dict):
                raise SupervisorStateError("GPU dispatch claim could not be read")
            if gpu_claim.get("fingerprint") != request_fingerprint:
                raise WorkerDispatchInterrupted("GPU dispatch claim does not match this request")
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
                    log_event("WARNING", "gpu_cancel_propagation_failed", job=job_id, error_type=type(cancel_error).__name__)
            raise
        except TRANSIENT_MODAL_CALL_ERRORS as error:
            raise CallbackWaitDeferredError("Modal could not confirm the GPU result yet") from error

    result = await run_callback_supervisor(
        job_id,
        callback_url,
        callback_token,
        run_generation,
        read_job_async,
        write_job_async,
    )
    try:
        request_claim = await get_claim(f"request:{job_id}")
        if isinstance(request_claim, dict):
            await put_claim(f"request:{job_id}", {**request_claim, "state": result["status"]})
    except Exception as error:
        log_event("WARNING", "request_claim_terminal_update_failed", job=job_id, error_type=type(error).__name__)
    return result


# ============================================================
# API
# ============================================================

@app.function(
    image=api_image,

    volumes={
        JOBS_DIR: jobs_volume,
    },

    secrets=[
        api_secret,
    ],

    timeout=300,
)
@modal.asgi_app(
    label="generate",
)
def api():

    from fastapi import (
        FastAPI,
        HTTPException,
        Request,
        Response,
    )

    from fastapi.responses import (
        FileResponse,
    )

    # Silence framework-level access logging where possible. Modal's own
    # edge/router request lines are platform logs and cannot be fully disabled
    # from inside FastAPI. The Retry-After response below lets pollers back off.
    logging.getLogger("uvicorn.access").disabled = True
    logging.getLogger("uvicorn.error").setLevel(logging.WARNING)
    logging.getLogger("fastapi").setLevel(logging.WARNING)

    api_app = FastAPI(
        title=(
            "Wan2.2 Video API"
        )
    )

    # ========================================================
    # AUTH
    # ========================================================

    def authenticate(
        request: Request,
    ):

        expected_key = os.environ[
            "MODAL_VIDEO_API_KEY"
        ]

        authorization = (
            request.headers.get(
                "Authorization",
                "",
            )
        )

        expected = (
            f"Bearer {expected_key}"
        )

        if not secrets.compare_digest(
            authorization,
            expected,
        ):

            raise HTTPException(
                status_code=401,
                detail="Invalid API key",
                headers={
                    "WWW-Authenticate":
                        "Bearer"
                },
            )

    def validate_callback(
        body: dict,
    ):
        try:
            return callback_submission_from_body(body)
        except ValueError as error:
            raise HTTPException(status_code=400, detail=str(error))

    # ========================================================
    # HEALTH
    # ========================================================

    @api_app.get(
        "/health"
    )
    async def health():

        return {
            "status": "ok",
            "model": MODEL_ID,
        }

    # ========================================================
    # MODELS
    #
    # GET /api/v1/videos/models
    # ========================================================

    @api_app.get(
        "/api/v1/videos/models"
    )
    async def list_models(
        request: Request,
    ):

        authenticate(
            request
        )

        return {
            "data": [
                {
                    "id":
                        MODEL_ID,

                    "name":
                        MODEL_NAME,

                    "supported_durations":
                        list(
                            range(
                                MIN_DURATION,
                                MAX_DURATION + 1,
                            )
                        ),

                    "supported_resolutions":
                        SUPPORTED_RESOLUTIONS,

                    "supported_aspect_ratios":
                        SUPPORTED_ASPECT_RATIOS,

                    "generate_audio":
                        False,

                    "pricing_skus":
                        {},
                }
            ]
        }

    # ========================================================
    # CREATE VIDEO
    #
    # POST /api/v1/videos
    # ========================================================

    @api_app.post(
        "/api/v1/videos",
        status_code=202,
    )
    async def create_video(
        request: Request,
    ):

        authenticate(
            request
        )

        try:
            body = (
                await request.json()
            )

        except Exception:
            raise HTTPException(
                status_code=400,
                detail="Invalid JSON",
            )

        if not isinstance(body, dict):
            raise HTTPException(
                status_code=400,
                detail="JSON body must be an object",
            )

        unsupported = set(body).difference(CREATE_VIDEO_FIELDS)
        if unsupported:
            raise HTTPException(status_code=400, detail="request contains an unsupported field")

        callback_submission = validate_callback(body)

        # ----------------------------------------------------
        # MODEL
        # ----------------------------------------------------

        model = body.get(
            "model",
            MODEL_ID,
        )

        if model != MODEL_ID:
            raise HTTPException(
                status_code=400,
                detail=(
                    f"Unsupported model: "
                    f"{model}"
                ),
            )

        # ----------------------------------------------------
        # PROMPT
        # ----------------------------------------------------

        prompt = body.get(
            "prompt"
        )

        if not isinstance(
            prompt,
            str,
        ):
            raise HTTPException(
                status_code=400,
                detail=(
                    "prompt must be a string"
                ),
            )

        prompt = prompt.strip()

        if not prompt:
            raise HTTPException(
                status_code=400,
                detail="prompt is required",
            )

        # ----------------------------------------------------
        # DURATION
        # ----------------------------------------------------

        duration = body.get("duration", 6)
        if type(duration) is not int:
            raise HTTPException(
                status_code=400,
                detail=(
                    "duration must be "
                    "an integer"
                ),
            )

        if (
            duration < MIN_DURATION
            or duration > MAX_DURATION
        ):
            raise HTTPException(
                status_code=400,
                detail=(
                    f"duration must be between "
                    f"{MIN_DURATION} and "
                    f"{MAX_DURATION} seconds"
                ),
            )

        # ----------------------------------------------------
        # RESOLUTION
        # ----------------------------------------------------

        resolution = body.get("resolution", "480p")
        if not isinstance(resolution, str):
            raise HTTPException(status_code=400, detail="resolution must be a string")
        resolution = resolution.strip().lower()

        if resolution not in (
            SUPPORTED_RESOLUTIONS
        ):
            raise HTTPException(
                status_code=400,
                detail=(
                    "resolution must be "
                    "'480p' or '720p'"
                ),
            )

        # ----------------------------------------------------
        # ASPECT RATIO
        # ----------------------------------------------------

        aspect_ratio = body.get("aspect_ratio", "9:16")
        if not isinstance(aspect_ratio, str):
            raise HTTPException(status_code=400, detail="aspect_ratio must be a string")
        aspect_ratio = aspect_ratio.strip()

        if aspect_ratio not in (
            SUPPORTED_ASPECT_RATIOS
        ):
            raise HTTPException(
                status_code=400,
                detail=(
                    "aspect_ratio must be "
                    "'9:16' or '16:9'"
                ),
            )

        # ----------------------------------------------------
        # SEED
        #
        # optional
        # ----------------------------------------------------

        raw_seed = body.get(
            "seed"
        )

        if raw_seed is None:

            seed = secrets.randbelow(
                sys.maxsize
            )

        else:
            if type(raw_seed) is not int:
                raise HTTPException(
                    status_code=400,
                    detail=(
                        "seed must be "
                        "an integer"
                    ),
                )

            seed = raw_seed
            if seed < 0:
                seed = secrets.randbelow(
                    sys.maxsize
                )

        # ----------------------------------------------------
        # AUDIO
        # ----------------------------------------------------

        generate_audio = body.get(
            "generate_audio",
            False,
        )

        if type(generate_audio) is not bool:
            raise HTTPException(status_code=400, detail="generate_audio must be a boolean")
        if generate_audio:
            raise HTTPException(
                status_code=400,
                detail=(
                    "Wan2.2 Lightning T2V "
                    "does not generate audio"
                ),
            )

        # ----------------------------------------------------
        # VALIDATE COMBINATION
        # ----------------------------------------------------

        combination = (
            resolution,
            aspect_ratio,
        )

        if combination not in SIZE_MAP:
            raise HTTPException(
                status_code=400,
                detail=(
                    "Unsupported resolution "
                    "and aspect ratio"
                ),
            )

        callback_mode = callback_submission["mode"] if callback_submission else ""
        callback_url = callback_submission.get("callback_url", "") if callback_submission else ""
        callback_token = callback_submission.get("callback_token", "") if callback_submission else ""
        job_id = callback_submission.get("job_id", "") if callback_mode == "callback" else ""
        if not job_id:
            job_id = "gen_" + uuid.uuid4().hex

        seed_was_supplied = type(raw_seed) is int and raw_seed >= 0
        request = {
            "prompt": prompt,
            "model": model,
            "duration": duration,
            "resolution": resolution,
            "aspect_ratio": aspect_ratio,
            "seed": seed,
        }
        request_fingerprint = ""
        if callback_mode == "callback":
            request_fingerprint = callback_request_fingerprint(
                prompt,
                model,
                duration,
                resolution,
                aspect_ratio,
                callback_url,
                callback_token,
                seed=seed,
                seed_was_supplied=seed_was_supplied,
            )
        job = {
            "id": job_id,
            "status": "pending",
            "stage": "queued",
            "model": model,
            "progress": 0,
            "prompt": prompt,
            "duration": duration,
            "resolution": resolution,
            "aspect_ratio": aspect_ratio,
            "seed": seed,
            "created_at": int(time.time()),
            "error": "",
        }
        if callback_mode:
            job.update(
                {
                    "callback_mode": callback_mode,
                    "callback_url": callback_url,
                    "callback_token": callback_token,
                }
            )
        if request_fingerprint:
            job["request_fingerprint"] = request_fingerprint

        def acknowledgement(current: dict, include_poll: bool) -> dict:
            status = current.get("status", "pending")
            if status == "in_progress":
                status = "processing"
            result = {
                "id": job_id,
                "status": status,
                "stage": current.get("stage", "queued"),
                "model": model,
                "progress": current.get("progress", 0),
            }
            if include_poll:
                result["poll_after_seconds"] = STATUS_POLL_RETRY_SECONDS
            return result

        # New dashboard requests carry a stable ID. The Dict claim prevents
        # duplicate API requests from enqueuing a second supervisor/GPU job.
        if callback_mode == "callback":
            request_claim_key = f"request:{job_id}"
            request_claim = {
                "fingerprint": request_fingerprint,
                "state": "claiming",
                "claimed_at": int(time.time()),
                "supervisor_call_id": "",
            }
            try:
                owns_request_claim = await put_claim(
                    request_claim_key,
                    request_claim,
                    skip_if_exists=True,
                )
            except Exception as error:
                log_event("WARNING", "callback_claim_unavailable", job=job_id, error_type=type(error).__name__)
                raise HTTPException(status_code=503, detail="The video request could not be claimed; retry shortly")

            if not owns_request_claim:
                try:
                    saved_claim = await get_claim(request_claim_key)
                except Exception as error:
                    log_event("WARNING", "callback_claim_read_failed", job=job_id, error_type=type(error).__name__)
                    raise HTTPException(status_code=503, detail="The video request state is temporarily unavailable")
                if not isinstance(saved_claim, dict):
                    raise HTTPException(status_code=503, detail="The video request state is temporarily unavailable")
                if saved_claim.get("fingerprint") != request_fingerprint:
                    raise HTTPException(status_code=409, detail="This job_id was already used for a different video request")
                try:
                    existing_job = await read_job_async(job_id)
                except Exception as error:
                    log_event("WARNING", "callback_job_read_failed", job=job_id, error_type=type(error).__name__)
                    raise HTTPException(status_code=503, detail="The saved video request is temporarily unavailable")
                if not isinstance(existing_job, dict):
                    raise HTTPException(
                        status_code=503,
                        detail="The video request is still being saved; retry this same job ID",
                        headers={"Retry-After": "2"},
                    )
                if not request_matches_job(existing_job, request, compare_seed=seed_was_supplied):
                    raise HTTPException(status_code=409, detail="This job_id was already used for a different video request")
                terminal_event = terminal_callback_event(job_id, existing_job)
                if terminal_event is not None:
                    try:
                        outcome = await asyncio.to_thread(
                            deliver_video_callback,
                            callback_url,
                            callback_token,
                            terminal_event,
                            return_result=True,
                        )
                        await persist_callback_delivery(job_id, existing_job, outcome, write_job_async)
                    except Exception:
                        pass
                return acknowledgement(existing_job, include_poll=False)

            try:
                existing_job = await read_job_async(job_id)
            except Exception as error:
                log_event("WARNING", "callback_job_read_failed", job=job_id, error_type=type(error).__name__)
                raise HTTPException(status_code=503, detail="The saved video request is temporarily unavailable")
            if isinstance(existing_job, dict):
                if not request_matches_job(existing_job, request, compare_seed=seed_was_supplied):
                    raise HTTPException(status_code=409, detail="This job_id was already used for a different video request")
                return acknowledgement(existing_job, include_poll=False)
            try:
                await write_job_async(job_id, job)
            except Exception as error:
                log_event("WARNING", "callback_job_write_failed", job=job_id, error_type=type(error).__name__)
                raise HTTPException(status_code=503, detail="The video request could not be saved; retry shortly")

            try:
                await put_claim(request_claim_key, {**request_claim, "state": "recorded"})
            except Exception as error:
                # The initial atomic claim still prevents duplicate GPU work.
                log_event("WARNING", "callback_claim_update_failed", job=job_id, error_type=type(error).__name__)

            try:
                supervisor_call = await callback_video_supervisor.spawn.aio(
                    job_id=job_id,
                )
            except Exception as error:
                if definitive_modal_rejection(error):
                    failed = dict(job)
                    failed.update(
                        {
                            "status": "failed",
                            "stage": "failed",
                            "failure_stage": "dispatch",
                            "error_code": "dispatch_failed",
                            "progress": 5,
                            "error": "Modal could not queue the video worker.",
                            "completed_at": int(time.time()),
                        }
                    )
                    try:
                        await write_job_async(job_id, failed)
                    except Exception as persist_error:
                        log_event("ERROR", "callback_job_persist_failed", job=job_id, code="storage_failed")
                        return acknowledgement(job, include_poll=False)
                    try:
                        await put_claim(request_claim_key, {**request_claim, "state": "failed"})
                    except Exception:
                        log_event("WARNING", "callback_claim_failure_update_failed", job=job_id)
                    terminal_event = terminal_callback_event(job_id, failed)
                    try:
                        await asyncio.to_thread(deliver_video_callback, callback_url, callback_token, terminal_event)
                    except Exception:
                        pass
                    log_event("ERROR", "callback_supervisor_dispatch_failed", job=job_id, error_type=type(error).__name__)
                    return acknowledgement(failed, include_poll=False)
                log_event("WARNING", "callback_supervisor_dispatch_ambiguous", job=job_id, error_type=type(error).__name__)
                return acknowledgement(job, include_poll=False)

            supervisor_call_id = getattr(supervisor_call, "object_id", "")
            if isinstance(supervisor_call_id, str) and supervisor_call_id:
                try:
                    await put_claim(
                        request_claim_key,
                        {**request_claim, "state": "supervisor_queued", "supervisor_call_id": supervisor_call_id},
                    )
                except Exception as error:
                    log_event("WARNING", "callback_supervisor_reference_save_failed", job=job_id, error_type=type(error).__name__)
            else:
                log_event("WARNING", "callback_supervisor_reference_missing", job=job_id)
            return acknowledgement(job, include_poll=False)

        # The static callback endpoint remains available while old Go services
        # and other pre-callback API clients finish their active requests.
        try:
            await write_job_async(job_id, job)
        except Exception as error:
            log_event("WARNING", "job_write_failed", job=job_id, error_type=type(error).__name__)
            raise HTTPException(status_code=503, detail="The video request could not be saved; retry shortly")

        try:
            log_event("INFO", "gpu_dispatch_requested", job=job_id)
            call = await (
                VideoGenerator()
                .generate
                .spawn.aio(
                    job_id=job_id,
                    prompt=prompt,
                    model=model,
                    duration=duration,
                    resolution=resolution,
                    aspect_ratio=aspect_ratio,
                    seed=seed,
                )
            )
        except Exception as error:
            if definitive_modal_rejection(error):
                job.update(
                    {
                        "status": "failed",
                        "stage": "dispatch_failed",
                        "failure_stage": "dispatch",
                        "error_code": "dispatch_failed",
                        "error": "Could not start video generation.",
                        "completed_at": int(time.time()),
                    }
                )
                try:
                    await write_job_async(job_id, job)
                except Exception as persist_error:
                    log_event("ERROR", "job_failure_persist_failed", job=job_id, error_type=type(persist_error).__name__)
                if callback_mode == "legacy" and callback_url and callback_token:
                    schedule_terminal_callback(
                        job_id,
                        callback_url,
                        callback_token,
                        terminal_callback_payload(job_id, "failed", model),
                    )
                log_event("ERROR", "gpu_dispatch_failed", job=job_id, error_type=type(error).__name__)
                return acknowledgement(job, include_poll=callback_mode != "legacy")
            log_event("WARNING", "gpu_dispatch_outcome_ambiguous", job=job_id, error_type=type(error).__name__)
            return acknowledgement(job, include_poll=callback_mode != "legacy")

        call_id = getattr(call, "object_id", "")
        if isinstance(call_id, str) and call_id:
            try:
                await write_modal_dispatch_state_async(job_id, "dispatched", call_id)
            except Exception as error:
                log_event("WARNING", "gpu_call_reference_save_failed", job=job_id, error_type=type(error).__name__)
        else:
            log_event("WARNING", "gpu_dispatch_reference_missing", job=job_id)
        ack = acknowledgement(job, include_poll=callback_mode != "legacy")
        if isinstance(call_id, str) and call_id:
            ack["modal_call_id"] = call_id
        return ack

    # ========================================================
    # POLL VIDEO
    #
    # GET /api/v1/videos/{id}
    # ========================================================

    @api_app.get(
        "/api/v1/videos/{job_id}"
    )
    async def get_video(
        job_id: str,
        request: Request,
        response: Response,
    ):

        authenticate(
            request
        )

        job = await read_job_async(
            job_id
        )

        if job is None:
            raise HTTPException(
                status_code=404,
                detail=(
                    "Generation not found"
                ),
            )

        job = await reconcile_modal_call(
            job
        )

        if job.get("status") not in {"completed", "failed"}:
            response.headers["Retry-After"] = str(STATUS_POLL_RETRY_SECONDS)

        response = {
            "id":
                job["id"],

            "status":
                job["status"],

            "model":
                job.get(
                    "model",
                    MODEL_ID,
                ),

            "progress":
                job.get(
                    "progress",
                    0,
                ),

            "stage":
                job.get(
                    "stage",
                    "unknown",
                ),

            "telemetry":
                job.get(
                    "telemetry",
                    {},
                ),
        }

        callback_delivery = callback_delivery_summary(job)
        if callback_delivery is not None:
            response["callback_delivery"] = callback_delivery

        if job.get("status") not in {"completed", "failed"}:
            response["poll_after_seconds"] = STATUS_POLL_RETRY_SECONDS

        # ----------------------------------------------------
        # COMPLETED
        # ----------------------------------------------------

        if (
            job["status"]
            == "completed"
        ):

            base_url = str(
                request.base_url
            ).rstrip("/")

            content_url = (
                f"{base_url}"
                f"/api/v1/videos/"
                f"{job_id}"
                f"/content?index=0"
            )

            response[
                "unsigned_urls"
            ] = [
                content_url
            ]

            response[
                "usage"
            ] = {
                "cost": job.get("usage_cost_usd", 0.0),
                "currency": "USD",
                "gpu_seconds": job.get("gpu_billed_seconds"),
                "gpu_rate_usd_per_second": job.get(
                    "gpu_rate_usd_per_second",
                    A100_80GB_USD_PER_SECOND,
                ),
                "basis": "measured_generation_runtime",
            }

            response["timings"] = {
                "inference_seconds": job.get("inference_seconds"),
                "encode_seconds": job.get("encode_seconds"),
                "total_seconds": job.get("total_seconds"),
            }

        # ----------------------------------------------------
        # FAILED
        # ----------------------------------------------------

        if (
            job["status"]
            == "failed"
        ):

            response[
                "error"
            ] = job.get(
                "error",
                "Generation failed",
            )

        return response

    # ========================================================
    # VIDEO CONTENT
    #
    # GET /api/v1/videos/{id}/content?index=0
    # ========================================================

    @api_app.get(
        "/api/v1/videos/{job_id}/content"
    )
    async def get_content(
        job_id: str,
        request: Request,
        index: int = 0,
    ):

        authenticate(
            request
        )

        if index != 0:
            raise HTTPException(
                status_code=404,
                detail=(
                    "Only output index 0 exists"
                ),
            )

        job = await read_job_async(
            job_id
        )

        if job is None:
            raise HTTPException(
                status_code=404,
                detail=(
                    "Generation not found"
                ),
            )

        if (
            job["status"]
            != "completed"
        ):
            raise HTTPException(
                status_code=409,
                detail=(
                    "Generation is not "
                    "completed yet"
                ),
            )

        path = job_video_path(
            job_id
        )

        if not path.exists():
            raise HTTPException(
                status_code=404,
                detail=(
                    "Generated video "
                    "file not found"
                ),
            )

        # ----------------------------------------------------
        # IMPORTANT:
        #
        # generation id:
        # gen_123abc
        #
        # filename:
        # gen_123abc.mp4
        #
        # FileResponse also avoids loading the entire
        # video into API-container RAM.
        # ----------------------------------------------------

        return FileResponse(
            path=str(path),

            media_type="video/mp4",

            filename=(
                f"{job_id}.mp4"
            ),
        )

    return api_app
