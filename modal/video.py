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
OFFLOAD_MODEL = False
MODEL_DOWNLOAD_SENTINEL = f"{MODEL_CACHE}/.wan22_download_complete"

# Runtime telemetry cadence while a job is active.
TELEMETRY_INTERVAL_SECONDS = 10

# Modal public A100 80 GB rate as of 2026-09-23.
# usage.cost is calculated from measured generation runtime.
# Modal workspace billing is aggregated and is not available as an exact
# per-request live invoice amount at completion time.
A100_80GB_USD_PER_SECOND = 0.000694
STATUS_POLL_RETRY_SECONDS = 2
CALLBACK_REQUEST_TIMEOUT_SECONDS = 5
CALLBACK_RETRY_DELAYS_SECONDS = (0, 0.25, 0.75, 1.5)
CALLBACK_JOB_ID = re.compile(r"^gen_[a-f0-9]{32}$")
CALLBACK_TOKEN = re.compile(r"^[a-fA-F0-9]{64}$")
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

api_image = (
    modal.Image.debian_slim(
        python_version="3.11",
    )
    .pip_install(
        "fastapi[standard]",
        "httpx",
    )
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


def job_call_path(job_id: str) -> Path:
    """Separate dispatch metadata from the mutable public job status record."""
    if not isinstance(job_id, str) or not CALLBACK_JOB_ID.fullmatch(job_id):
        raise ValueError("invalid callback job id")
    return Path(JOBS_DIR, f"{job_id}.call.json")


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
    """Mirror only dispatch state and Modal FunctionCall ID into the jobs volume."""
    if state not in {"dispatching", "dispatched", "dispatch_failed"}:
        raise ValueError("invalid GPU dispatch state")
    if call_id and not isinstance(call_id, str):
        raise ValueError("invalid Modal call ID")
    path = job_call_path(job_id)
    temp = Path(f"{path}.{uuid.uuid4().hex}.tmp")
    try:
        temp.write_text(
            json.dumps({"dispatch_state": state, "modal_call_id": call_id}),
            encoding="utf-8",
        )
        os.replace(temp, path)
        await jobs_volume.commit.aio()
    finally:
        try:
            temp.unlink(missing_ok=True)
        except OSError:
            pass


async def read_modal_dispatch_state_async(job_id: str) -> dict:
    """Read the callback-safe call-ID mirror; no callback credentials are stored."""
    path = job_call_path(job_id)
    await jobs_volume.reload.aio()
    if not path.is_file():
        return {}
    data = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(data, dict):
        return {}
    return {
        "dispatch_state": str(data.get("dispatch_state", "")),
        "modal_call_id": str(data.get("modal_call_id", "")),
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
    """The GPU method could not be queued by Modal."""


class WorkerDispatchInterrupted(WorkerDispatchError):
    """A claimed dispatch could not be recovered without risking a duplicate."""


class CallbackDeliveryError(RuntimeError):
    """A terminal callback could not be delivered after bounded HTTP retries."""


class SupervisorStateError(RuntimeError):
    """Persisted job state is missing or temporarily unreadable."""


class CallbackWaitDeferredError(RuntimeError):
    """The Modal control plane could not yet confirm the child GPU result."""


def definitive_modal_rejection(error):
    """Only classify SDK errors known to reject a function before enqueue."""
    return isinstance(error, DEFINITIVE_MODAL_REJECTIONS)


def validate_callback_submission(job_id, callback_url, callback_token):
    """Validate Go's client-assigned ID and narrowly scoped callback target."""
    if not isinstance(job_id, str) or not CALLBACK_JOB_ID.fullmatch(job_id):
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
        or "." not in hostname
        or hostname == "localhost"
        or hostname.endswith((".localhost", ".local"))
        or (address is not None and not address.is_global)
        or parsed.username is not None
        or parsed.password is not None
        or parsed.query
        or parsed.fragment
        or "?" in callback_url
        or "#" in callback_url
        or parsed.path != f"/api/video-callbacks/{job_id}"
    ):
        raise ValueError("callback_url must be the HTTPS callback endpoint for this job")
    if not isinstance(callback_token, str) or not CALLBACK_TOKEN.fullmatch(callback_token):
        raise ValueError("callback_token must be a 32-byte hex bearer token")


def callback_submission_from_body(body):
    """Return callback fields when all are present; preserve old no-callback clients."""
    fields = ("job_id", "callback_url", "callback_token")
    supplied = [field in body for field in fields]
    if not any(supplied):
        return None
    if not all(supplied):
        raise ValueError("job_id, callback_url, and callback_token must be provided together")
    job_id, callback_url, callback_token = (body[field] for field in fields)
    validate_callback_submission(job_id, callback_url, callback_token)
    return job_id, callback_url, callback_token


def callback_request_fingerprint(
    prompt,
    model,
    duration,
    resolution,
    aspect_ratio,
    callback_url,
    callback_token,
    seed=None,
    seed_was_supplied=False,
):
    """Hash normalized request identity without retaining callback credentials."""
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


async def get_claim(key):
    return await job_claims.get.aio(key)


async def put_claim(key, value, skip_if_exists=False):
    return await job_claims.put.aio(key, value, skip_if_exists=skip_if_exists)


def terminal_callback_event(job_id, job):
    """Rebuild a terminal notification from persisted state for safe redelivery."""
    status = job.get("status")
    if status == "completed":
        return _callback_event(job_id, "completed", 2, 100, "completed", job=job)
    if status == "failed":
        code = classify_worker_failure(RuntimeError("persisted failure"), job)
        progress = min(99, max(5, int(job.get("progress", 5) or 5)))
        stage = str(job.get("failure_stage") or job.get("stage") or "failed")
        return _callback_event(job_id, "failed", 2, progress, stage, error_code=code)
    return None


def request_matches_job(job, request, compare_seed=True):
    """Check a repeated submission without storing callback credentials."""
    fields = ("prompt", "model", "duration", "resolution", "aspect_ratio")
    if any(job.get(field) != request.get(field) for field in fields):
        return False
    return not compare_seed or job.get("seed") == request.get("seed")


def classify_worker_failure(error, job=None):
    """Map worker failures to the stable, non-sensitive callback vocabulary."""
    job = job or {}
    existing = job.get("error_code")
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
    if existing in allowed:
        return existing
    if isinstance(error, WorkerDispatchInterrupted):
        return "dispatch_interrupted"
    if isinstance(error, WorkerDispatchError):
        return "dispatch_failed"
    if isinstance(error, asyncio.CancelledError):
        return "dispatch_interrupted"

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


async def post_video_callback_once(callback_url, callback_token, payload):
    """Make one bounded callback request; redirects are deliberately disabled."""
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


async def deliver_video_callback(
    callback_url,
    callback_token,
    payload,
    post_once=None,
    sleep=asyncio.sleep,
):
    """Retry transient delivery failures only, without leaking target or token."""
    post_once = post_once or post_video_callback_once
    for delay in CALLBACK_RETRY_DELAYS_SECONDS:
        if delay:
            await sleep(delay)
        try:
            status = await post_once(callback_url, callback_token, payload)
        except Exception:
            status = None
        if isinstance(status, int) and 200 <= status < 300:
            return True
        if status is not None and status not in {408, 425, 429, 500, 502, 503, 504}:
            return False
    return False


def _callback_event(job_id, status, sequence, progress, stage, error_code="", job=None):
    event = {
        "id": job_id,
        "status": status,
        "sequence": sequence,
        "progress": progress,
        "stage": stage,
    }
    if error_code:
        event["error_code"] = error_code
    if status == "completed" and job and job.get("usage_cost_usd") is not None:
        event["cost_usd"] = str(job["usage_cost_usd"])
    return event


async def run_callback_supervisor(
    job_id,
    callback_url,
    callback_token,
    run_generation,
    read_job_fn,
    write_job_fn,
    post_once=None,
    sleep=asyncio.sleep,
):
    """Await GPU startup/runtime, persist terminal state, then notify Go.

    A Modal function retry re-enters here with the same inputs. Terminal results
    are read first and only their callback is retried; generation is never
    dispatched again after a result has been persisted.
    """

    try:
        job = await read_job_fn(job_id)
    except Exception as error:
        raise SupervisorStateError("Persisted video state could not be read") from error
    if not isinstance(job, dict):
        raise SupervisorStateError("Persisted video state is not available yet")

    event = terminal_callback_event(job_id, job)
    if event is not None:
        if not await deliver_video_callback(
            callback_url,
            callback_token,
            event,
            post_once=post_once,
            sleep=sleep,
        ):
            raise CallbackDeliveryError("Persisted terminal callback delivery failed")
        return event

    await deliver_video_callback(
        callback_url,
        callback_token,
        _callback_event(job_id, "processing", 1, 5, "starting"),
        post_once=post_once,
        sleep=sleep,
    )

    caught_error = None
    try:
        await run_generation()
    except asyncio.CancelledError:
        # Modal may stop this supervisor at its execution deadline while the
        # separately spawned GPU call is still running. Leave the job pending
        # and let Modal retry this same input; it will reattach by FunctionCall
        # ID instead of publishing a false terminal failure.
        raise
    except CallbackWaitDeferredError:
        # A transient Modal RPC error says nothing about the child GPU result.
        # Leave the persisted state unchanged so a managed function retry can
        # reattach to the exact same FunctionCall.
        raise
    except SupervisorStateError:
        # Missing/unreadable coordination state is not a generation outcome.
        raise
    except Exception as error:
        caught_error = error

    try:
        job = await read_job_fn(job_id)
    except Exception as error:
        raise SupervisorStateError("Persisted video state could not be reloaded") from error
    if not isinstance(job, dict):
        raise SupervisorStateError("Persisted video state disappeared during generation")

    # The GPU worker may have committed a terminal outcome before its FunctionCall
    # result was observed (for example, if the supervisor was interrupted). The
    # persisted terminal result is authoritative over a later transport error.
    event = terminal_callback_event(job_id, job)
    if event is None:
        if caught_error is None:
            caught_error = RuntimeError("GPU worker ended without a terminal result")

        error_code = classify_worker_failure(caught_error, job)
        failure_stage = str(job.get("failure_stage") or job.get("stage") or "failed")
        failed_job = dict(job)
        failed_job.update(
            {
                "id": job_id,
                "status": "failed",
                "stage": "failed",
                "failure_stage": failure_stage,
                "error_code": error_code,
                "error": "Video generation failed; check the worker logs for this job.",
                "completed_at": int(time.time()),
            }
        )
        try:
            await write_job_fn(job_id, failed_job)
        except Exception as error:
            log_event("ERROR", "callback_job_persist_failed", job=job_id, code="storage_failed")
            raise SupervisorStateError("Terminal video failure could not be persisted") from error

        event = terminal_callback_event(job_id, failed_job)

    if not await deliver_video_callback(
        callback_url,
        callback_token,
        event,
        post_once=post_once,
        sleep=sleep,
    ):
        raise CallbackDeliveryError("Persisted terminal callback delivery failed")

    if event["status"] == "failed":
        log_event("WARNING", "callback_job_failed", job=job_id, code=event["error_code"])
    return event


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
    ):

        started_at = time.time()

        job = {
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
            "created_at": int(
                started_at
            ),
            "error": "",
        }

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
                            error_type=type(telemetry_error).__name__,
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

            failed_stage = str(job.get("stage") or "unknown")
            error_code = classify_worker_failure(
                error,
                {"failure_stage": failed_stage},
            )

            if "telemetry_stop" in locals():
                telemetry_stop.set()
            if "telemetry_thread" in locals():
                telemetry_thread.join(timeout=2)

            log_event(
                "ERROR",
                "job_failed",
                job=job_id,
                stage=failed_stage,
                code=error_code,
                error_type=type(error).__name__,
            )

            job.update(
                {
                    "status":
                        "failed",

                    "stage":
                        "failed",

                    "failure_stage":
                        failed_stage,

                    "error_code":
                        error_code,

                    "progress":
                        job.get("progress", 0),

                    "telemetry":
                        collect_runtime_metrics(),

                    "error":
                        str(error),

                    "completed_at":
                        int(time.time()),
                }
            )

            write_job(
                job_id,
                job,
            )

            raise


@app.function(
    image=api_image,
    volumes={JOBS_DIR: jobs_volume},
    secrets=[api_secret],
    # VideoGenerator allows 30 minutes for class startup and 60 minutes for
    # generation; leave headroom so a healthy child can finish before retry.
    timeout=7200,
    retries=modal.Retries(
        max_retries=240,
        backoff_coefficient=2.0,
        initial_delay=1.0,
        max_delay=60.0,
    ),
)
async def callback_video_supervisor(
    job_id,
    prompt,
    model,
    duration,
    resolution,
    aspect_ratio,
    seed,
    callback_url,
    callback_token,
    request_fingerprint,
):
    """CPU orchestration keeps GPU cold-start/runtime errors observable."""

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
                    await put_claim(gpu_key, {**new_claim, "state": "dispatch_failed"})
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
                            await put_claim(
                                gpu_key,
                                {
                                    **new_claim,
                                    "state": "dispatch_failed",
                                },
                            )
                            await write_modal_dispatch_state_async(job_id, "dispatch_failed")
                        except Exception as persist_error:
                            log_event(
                                "ERROR",
                                "gpu_dispatch_rejection_persist_failed",
                                job=job_id,
                                error_type=type(persist_error).__name__,
                            )
                        raise WorkerDispatchError() from error
                    log_event("WARNING", "gpu_dispatch_outcome_ambiguous", job=job_id, error_type=type(error).__name__)
                    raise CallbackWaitDeferredError("Modal GPU dispatch outcome is not yet known") from error

                call_id = getattr(call, "object_id", "")
                if not isinstance(call_id, str) or not call_id:
                    log_event("WARNING", "gpu_call_reference_missing", job=job_id)
                    raise CallbackWaitDeferredError("Modal GPU call reference is not yet available")
                try:
                    await write_modal_dispatch_state_async(job_id, "dispatched", call_id)
                except Exception as error:
                    # The accepted call is still available in memory. Preserve
                    # it and await completion instead of publishing failure;
                    # the Dict claim remains the primary idempotency marker.
                    log_event(
                        "WARNING",
                        "gpu_call_reference_save_failed",
                        job=job_id,
                        error_type=type(error).__name__,
                    )
                gpu_claim = {
                    **new_claim,
                    "state": "dispatched",
                    "modal_call_id": call_id,
                }
                try:
                    await put_claim(gpu_key, gpu_claim)
                except Exception as error:
                    # The in-memory FunctionCall can still complete; a later
                    # Modal retry can fall back to the jobs-volume sidecar.
                    log_event(
                        "WARNING",
                        "gpu_call_claim_save_failed",
                        job=job_id,
                        error_type=type(error).__name__,
                    )
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
            raise WorkerDispatchError()

        call_id = gpu_claim.get("modal_call_id")
        if not call_id:
            try:
                dispatch_state = await read_modal_dispatch_state_async(job_id)
            except Exception as error:
                raise SupervisorStateError("GPU dispatch reference could not be read") from error
            call_id = dispatch_state.get("modal_call_id", "")
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
                persisted_job = await read_job_async(job_id)
            except Exception as error:
                raise SupervisorStateError("Persisted GPU state could not be reloaded") from error
            if isinstance(persisted_job, dict) and persisted_job.get("status") in {"completed", "failed"}:
                return
            call_id = gpu_claim.get("modal_call_id")
            if not call_id:
                try:
                    dispatch_state = await read_modal_dispatch_state_async(job_id)
                except Exception as error:
                    raise SupervisorStateError("GPU dispatch reference could not be reloaded") from error
                call_id = dispatch_state.get("modal_call_id", "")

        if not isinstance(call_id, str) or not call_id:
            raise CallbackWaitDeferredError("GPU dispatch still has no Modal call reference")
        try:
            if call is None:
                call = modal.FunctionCall.from_id(call_id)
            await call.get.aio()
        except TRANSIENT_MODAL_CALL_ERRORS as error:
            raise CallbackWaitDeferredError("Modal could not confirm the GPU result yet") from error

    result = await run_callback_supervisor(
        job_id=job_id,
        callback_url=callback_url,
        callback_token=callback_token,
        run_generation=run_generation,
        read_job_fn=read_job_async,
        write_job_fn=write_job_async,
    )
    request_claim_key = f"request:{job_id}"
    request_claim = await get_claim(request_claim_key)
    if isinstance(request_claim, dict):
        await put_claim(
            request_claim_key,
            {
                **request_claim,
                "state": result["status"],
            },
        )
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
                detail="JSON request body must be an object",
            )

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

        try:
            duration = int(
                body.get(
                    "duration",
                    6,
                )
            )

        except (
            TypeError,
            ValueError,
        ):
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

        resolution = str(
            body.get(
                "resolution",
                "480p",
            )
        ).lower()

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

        aspect_ratio = str(
            body.get(
                "aspect_ratio",
                "9:16",
            )
        )

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

            try:
                seed = int(
                    raw_seed
                )

            except (
                TypeError,
                ValueError,
            ):
                raise HTTPException(
                    status_code=400,
                    detail=(
                        "seed must be "
                        "an integer"
                    ),
                )

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

        # ----------------------------------------------------
        # GENERATION ID
        # ----------------------------------------------------

        try:
            callback_submission = callback_submission_from_body(body)
        except ValueError as error:
            raise HTTPException(status_code=400, detail=str(error))

        if callback_submission:
            job_id, callback_url, callback_token = callback_submission
        else:
            job_id = "gen_" + uuid.uuid4().hex
            callback_url = ""
            callback_token = ""

        job = {
            "id":
                job_id,

            "status":
                "pending",

            "stage":
                "queued",

            "model":
                model,

            "progress":
                0,

            "prompt":
                prompt,

            "duration":
                duration,

            "resolution":
                resolution,

            "aspect_ratio":
                aspect_ratio,

            "seed":
                seed,

            "created_at":
                int(time.time()),

            "error":
                "",
        }

        request_claim_key = ""
        request_fingerprint = ""
        request_claim = None
        owns_request_claim = False
        if callback_submission:
            request_fingerprint = callback_request_fingerprint(
                prompt,
                model,
                duration,
                resolution,
                aspect_ratio,
                callback_url,
                callback_token,
                seed=seed,
                seed_was_supplied=(raw_seed is not None and int(raw_seed) >= 0),
            )
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
                    request_claim = await get_claim(request_claim_key)
                except Exception as error:
                    log_event("WARNING", "callback_claim_read_failed", job=job_id, error_type=type(error).__name__)
                    raise HTTPException(status_code=503, detail="The video request state is temporarily unavailable")
                if not isinstance(request_claim, dict):
                    raise HTTPException(status_code=503, detail="The video request state is temporarily unavailable")
                if request_claim.get("fingerprint") != request_fingerprint:
                    raise HTTPException(
                        status_code=409,
                        detail="This job_id was already used for a different video request",
                    )

        try:
            existing_job = await read_job_async(job_id)
        except Exception as error:
            if callback_submission:
                log_event("WARNING", "callback_job_read_failed", job=job_id, error_type=type(error).__name__)
                raise HTTPException(status_code=503, detail="The saved video request is temporarily unavailable")
            raise HTTPException(status_code=503, detail="The saved video request is temporarily unavailable")

        if existing_job is not None:
            if not request_matches_job(
                existing_job,
                job,
                compare_seed=raw_seed is not None and int(raw_seed) >= 0,
            ):
                raise HTTPException(
                    status_code=409,
                    detail="This job_id was already used for a different video request",
                )
            if callback_submission:
                event = terminal_callback_event(job_id, existing_job)
                if event is None:
                    if not request_claim.get("supervisor_call_id") and request_claim.get("state") not in {
                        "supervisor_queued",
                        "completed",
                        "failed",
                    }:
                        # A queued supervisor reference may have been accepted
                        # even if saving its Dict metadata failed. Keep the job
                        # alive and let Go retry this same ID or receive its
                        # eventual callback; never terminalize a live child.
                        raise HTTPException(
                            status_code=503,
                            detail="The video supervisor is still being recorded; retry this request shortly",
                            headers={"Retry-After": "2"},
                        )
                    event = _callback_event(
                        job_id,
                        "processing",
                        1,
                        max(5, int(existing_job.get("progress", 5) or 5)),
                        str(existing_job.get("stage") or "starting"),
                    )
                delivered = await deliver_video_callback(callback_url, callback_token, event)
                if event["sequence"] == 2 and not delivered:
                    raise HTTPException(
                        status_code=503,
                        detail="The saved video result callback could not be delivered; retry this request",
                    )
            ack = {
                "id": job_id,
                "status": existing_job.get("status", "pending"),
                "stage": existing_job.get("stage", "queued"),
                "model": model,
                "progress": existing_job.get("progress", 0),
            }
            if callback_submission and event["sequence"] == 2 and event["status"] == "failed":
                ack.update({"status": "failed", "stage": "failed", "progress": event["progress"]})
            if ack["status"] == "in_progress":
                ack["status"] = "processing"
            if not callback_submission:
                ack["poll_after_seconds"] = STATUS_POLL_RETRY_SECONDS
            return ack

        if callback_submission and not owns_request_claim:
            claim_age = int(time.time()) - int(request_claim.get("claimed_at", 0))
            if claim_age < CLAIM_RECOVERY_SECONDS:
                raise HTTPException(
                    status_code=503,
                    detail="The video request is still being claimed; retry shortly",
                    headers={"Retry-After": "2"},
                )
            failed_job = dict(job)
            failed_job.update(
                {
                    "status": "failed",
                    "stage": "failed",
                    "failure_stage": "dispatch",
                    "error_code": "dispatch_interrupted",
                    "error": "The video worker request was interrupted before it could be recorded.",
                    "completed_at": int(time.time()),
                }
            )
            await write_job_async(job_id, failed_job)
            request_claim = {**request_claim, "state": "failed"}
            await put_claim(request_claim_key, request_claim)
            event = terminal_callback_event(job_id, failed_job)
            delivered = await deliver_video_callback(callback_url, callback_token, event)
            if not delivered:
                raise HTTPException(status_code=503, detail="The interrupted video request failure could not be delivered")
            return {
                "id": job_id,
                "status": "failed",
                "stage": "failed",
                "model": model,
                "progress": event["progress"],
            }

        try:
            await write_job_async(job_id, job)
        except Exception as error:
            if callback_submission:
                log_event("WARNING", "callback_job_write_failed", job=job_id, error_type=type(error).__name__)
            raise HTTPException(status_code=503, detail="The video request could not be saved; retry shortly")

        if callback_submission:
            request_claim = {**request_claim, "state": "recorded"}
            try:
                await put_claim(request_claim_key, request_claim)
            except Exception as error:
                log_event("WARNING", "callback_claim_update_failed", job=job_id, error_type=type(error).__name__)
                raise HTTPException(status_code=503, detail="The video request state could not be saved; retry shortly")

        # ----------------------------------------------------
        # START GPU WORK
        # ----------------------------------------------------

        if callback_submission:
            try:
                supervisor_call = await callback_video_supervisor.spawn.aio(
                    job_id=job_id,
                    prompt=prompt,
                    model=model,
                    duration=duration,
                    resolution=resolution,
                    aspect_ratio=aspect_ratio,
                    seed=seed,
                    callback_url=callback_url,
                    callback_token=callback_token,
                    request_fingerprint=request_fingerprint,
                )
            except Exception as error:
                if not definitive_modal_rejection(error):
                    # Modal may have accepted the supervisor input while its
                    # enqueue acknowledgement was lost. Keep the durable job
                    # pending; same-ID retries must not enqueue a second call.
                    log_event(
                        "WARNING",
                        "callback_supervisor_dispatch_ambiguous",
                        job=job_id,
                        error_type=type(error).__name__,
                    )
                    raise HTTPException(
                        status_code=503,
                        detail="The Modal submission outcome is not yet known; retry this same job ID",
                    )
                error_code = "dispatch_failed"
                job.update(
                    {
                        "status": "failed",
                        "stage": "failed",
                        "failure_stage": "dispatch",
                        "error_code": error_code,
                        "error": "Modal could not queue the video worker.",
                        "completed_at": int(time.time()),
                    }
                )
                await write_job_async(job_id, job)
                try:
                    request_claim = {**request_claim, "state": "failed"}
                    await put_claim(request_claim_key, request_claim)
                except Exception:
                    log_event("ERROR", "callback_claim_failure_update_failed", job=job_id, code=error_code)
                log_event(
                    "ERROR",
                    "callback_supervisor_dispatch_failed",
                    job=job_id,
                    error_type=type(error).__name__,
                )
                event = _callback_event(
                    job_id,
                    "failed",
                    2,
                    5,
                    "dispatch",
                    error_code=error_code,
                )
                delivered = await deliver_video_callback(callback_url, callback_token, event)
                if not delivered:
                    raise HTTPException(
                        status_code=503,
                        detail="The video worker could not be queued and its failure callback could not be delivered; retry this request",
                    )
                return {
                    "id": job_id,
                    "status": "failed",
                    "stage": "failed",
                    "model": model,
                    "progress": 5,
                }

            supervisor_call_id = getattr(supervisor_call, "object_id", "")
            if not isinstance(supervisor_call_id, str) or not supervisor_call_id:
                # The call may already be accepted even if the SDK failed to
                # return its reference. Do not publish a false terminal failure
                # or risk a second supervisor/GPU submission for this ID.
                log_event("WARNING", "callback_supervisor_reference_missing", job=job_id)
            else:
                request_claim = {
                    **request_claim,
                    "state": "supervisor_queued",
                    "supervisor_call_id": supervisor_call_id,
                }
                try:
                    await put_claim(request_claim_key, request_claim)
                except Exception as error:
                    # Enqueue succeeded. The supervisor still has its original
                    # inputs and its own retry policy; metadata-write failure
                    # must not overwrite a live job with dispatch_failed.
                    log_event(
                        "WARNING",
                        "callback_supervisor_reference_save_failed",
                        job=job_id,
                        error_type=type(error).__name__,
                    )
        else:
            try:
                await (
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
                job.update(
                    {
                        "status": "failed",
                        "error": str(error),
                    }
                )
                await write_job_async(job_id, job)
                raise HTTPException(
                    status_code=500,
                    detail="Could not start video generation",
                )

        # ----------------------------------------------------
        # OpenRouter-style generation response
        # ----------------------------------------------------

        response = {
            "id":
                job_id,

            "status":
                "pending",

            "stage":
                "queued",

            "model":
                model,

            "progress":
                0,

        }
        if not callback_submission:
            response["poll_after_seconds"] = STATUS_POLL_RETRY_SECONDS
        return response

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
