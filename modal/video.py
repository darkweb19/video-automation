import json
import os
import secrets
import sys
import time
import traceback
import uuid
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

KEEP_WARM_SECONDS = 600


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


def write_job(
    job_id: str,
    data: dict,
):
    """
    Write job metadata atomically and commit it
    so other Modal containers can see it.
    """

    path = job_json_path(
        job_id
    )

    temp = Path(
        f"{path}.tmp"
    )

    temp.write_text(
        json.dumps(
            data,
            indent=2,
        ),
        encoding="utf-8",
    )

    os.replace(
        temp,
        path,
    )

    jobs_volume.commit()


def read_job(
    job_id: str,
):
    """
    Reload the shared volume before reading because
    generation may be happening in another container.
    """

    jobs_volume.reload()

    path = job_json_path(
        job_id
    )

    if not path.exists():
        return None

    return json.loads(
        path.read_text(
            encoding="utf-8"
        )
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

    # Only one A100 worker for now.
    max_containers=1,

    # Keep model alive for 10 minutes after last job.
    scaledown_window=KEEP_WARM_SECONDS,

    timeout=3600,

    startup_timeout=1800,
)
class VideoGenerator:

    # ========================================================
    # LOAD MODEL ONCE
    # ========================================================

    @modal.enter()
    def load_model(self):

        from huggingface_hub import snapshot_download

        print(
            "=" * 70,
            flush=True,
        )

        print(
            "[STARTUP] Wan2.2 worker starting",
            flush=True,
        )

        print(
            "=" * 70,
            flush=True,
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
        # Download/cache base model
        # ----------------------------------------------------

        print(
            "[STARTUP] Checking base model...",
            flush=True,
        )

        snapshot_download(
            repo_id=BASE_MODEL_REPO,
            local_dir=self.base_model_path,
        )

        # ----------------------------------------------------
        # Download/cache Lightning LoRA
        # ----------------------------------------------------

        print(
            "[STARTUP] Checking Lightning model...",
            flush=True,
        )

        snapshot_download(
            repo_id=LIGHTNING_REPO,
            local_dir=self.lightning_path,
        )

        model_volume.commit()

        if not os.path.exists(
            self.lora_path
        ):
            raise RuntimeError(
                f"Lightning LoRA not found: "
                f"{self.lora_path}"
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

        print(
            "[STARTUP] Loading WanT2V pipeline...",
            flush=True,
        )

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

        print(
            f"[STARTUP] Model ready in "
            f"{time.time() - start:.1f}s",
            flush=True,
        )

        print(
            "[STARTUP] Worker ready",
            flush=True,
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
            "model": model,
            "progress": 0,
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

            print(
                "=" * 70,
                flush=True,
            )

            print(
                f"[JOB] ID: {job_id}",
                flush=True,
            )

            print(
                f"[JOB] Prompt: {prompt}",
                flush=True,
            )

            print(
                f"[JOB] Duration: "
                f"{duration}s",
                flush=True,
            )

            print(
                f"[JOB] Frames: "
                f"{frame_count}",
                flush=True,
            )

            print(
                f"[JOB] Resolution: "
                f"{resolution}",
                flush=True,
            )

            print(
                f"[JOB] Aspect ratio: "
                f"{aspect_ratio}",
                flush=True,
            )

            print(
                f"[JOB] Wan size: "
                f"{size_name}",
                flush=True,
            )

            print(
                f"[JOB] Seed: {seed}",
                flush=True,
            )

            print(
                "[JOB] Using persistent pipeline",
                flush=True,
            )

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

                offload_model=True,
            )

            self.torch.cuda.synchronize()

            inference_seconds = (
                time.time()
                - generation_start
            )

            print(
                f"[JOB] Inference: "
                f"{inference_seconds:.1f}s",
                flush=True,
            )

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

            del video

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

            # ------------------------------------------------
            # COMPLETE
            # ------------------------------------------------

            job.update(
                {
                    "status": "completed",
                    "progress": 100,

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

            print(
                f"[SUCCESS] {job_id}",
                flush=True,
            )

            print(
                f"[SUCCESS] File: "
                f"{output_path.name}",
                flush=True,
            )

            print(
                f"[SUCCESS] Size: "
                f"{file_size / 1024 / 1024:.2f} MB",
                flush=True,
            )

            print(
                f"[SUCCESS] Total: "
                f"{total_seconds:.1f}s",
                flush=True,
            )

            print(
                "=" * 70,
                flush=True,
            )

        except Exception as error:

            traceback.print_exc()

            job.update(
                {
                    "status":
                        "failed",

                    "progress":
                        0,

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
    )

    from fastapi.responses import (
        FileResponse,
    )

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

        write_job(
            job_id,
            job,
        )

        # ----------------------------------------------------
        # START GPU WORK
        # ----------------------------------------------------

        try:

            (
                VideoGenerator()
                .generate
                .spawn(
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

            write_job(
                job_id,
                job,
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

            "model":
                model,

            "progress":
                0,
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
    ):

        authenticate(
            request
        )

        job = read_job(
            job_id
        )

        if job is None:
            raise HTTPException(
                status_code=404,
                detail=(
                    "Generation not found"
                ),
            )

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
        }

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
                "cost": 0
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

        job = read_job(
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

        jobs_volume.reload()

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