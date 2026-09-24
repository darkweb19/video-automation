import json
import logging
import os
import secrets
import subprocess
import sys
import threading
import time
import traceback
import uuid
import warnings
from datetime import datetime, timezone
from pathlib import Path

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


async def reconcile_modal_call(job: dict) -> dict:
    """
    Reconcile a non-terminal job with its underlying Modal FunctionCall.

    This catches failures that happen before VideoGenerator.generate()
    begins, such as:
      - GPU scheduling/startup failures
      - snapshot restore failures
      - container bootstrap failures
      - @modal.enter() failures

    Without this, the job JSON can remain stuck in pending/dispatched
    forever even though the Modal worker has already failed.
    """

    if job.get("status") in {
        "completed",
        "failed",
    }:
        return job

    call_id = job.get(
        "modal_call_id"
    )

    if not call_id:
        return job

    try:
        call = modal.FunctionCall.from_id(
            call_id
        )

        # Non-blocking status check:
        # - TimeoutError => still queued/running
        # - return value => completed successfully
        # - other exception => worker/infrastructure failure
        await call.get.aio(
            timeout=0
        )

    except TimeoutError:
        return job

    except Exception as error:
        job.update(
            {
                "status":
                    "failed",

                "stage":
                    "worker_failed",

                "progress":
                    0,

                "error":
                    str(error),

                "completed_at":
                    int(time.time()),
            }
        )

        await write_job_async(
            job["id"],
            job,
        )

        log_event(
            "ERROR",
            "modal_worker_failed",
            job=job["id"],
            call_id=call_id,
            error=repr(error),
        )

    return job


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

            traceback.print_exc()
            log_event(
                "ERROR",
                "job_failed",
                job=job_id,
                stage=job.get("stage"),
                error=repr(error),
            )

            job.update(
                {
                    "status":
                        "failed",

                    "stage":
                        "failed",

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

        job_id = (
            "gen_"
            + uuid.uuid4().hex
        )

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

        await write_job_async(
            job_id,
            job,
        )

        # ----------------------------------------------------
        # START GPU WORK
        # ----------------------------------------------------

        try:

            log_event(
                "INFO",
                "gpu_dispatch_requested",
                job=job_id,
            )

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

            job[
                "modal_call_id"
            ] = call.object_id

            job[
                "stage"
            ] = "dispatched"

            await write_job_async(
                job_id,
                job,
            )

            log_event(
                "INFO",
                "gpu_job_dispatched",
                job=job_id,
                call_id=call.object_id,
            )

        except Exception as error:

            job.update(
                {
                    "status":
                        "failed",

                    "stage":
                        "dispatch_failed",

                    "error":
                        str(error),

                    "completed_at":
                        int(time.time()),
                }
            )

            await write_job_async(
                job_id,
                job,
            )

            log_event(
                "ERROR",
                "gpu_dispatch_failed",
                job=job_id,
                error=repr(error),
            )

            raise HTTPException(
                status_code=500,
                detail=(
                    "Could not start "
                    "video generation"
                ),
            )

        # ----------------------------------------------------
        # OpenRouter-style generation response
        # ----------------------------------------------------

        return {
            "id":
                job_id,

            "status":
                "pending",

            "stage":
                "dispatched",

            "model":
                model,

            "progress":
                0,

            "modal_call_id":
                call.object_id,

            "poll_after_seconds":
                STATUS_POLL_RETRY_SECONDS,
        }

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