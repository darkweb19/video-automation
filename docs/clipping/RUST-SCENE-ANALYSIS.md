# Rust scene analyzer: M3 visual worker

## Scope

The new model-free scene engine lives under workers/modal/scene-analyzer.
It is a dependency-free Rust binary invoked by the existing Python
clipping worker. It decodes FFmpeg 2 FPS grayscale frames at 128x72
resolution and measures mean absolute frame difference and luminance
histogram divergence.

It detects shot-like discontinuities and visual activity. It does
NOT identify objects, actions, people, emotions, speech meaning, or virality,
and its frame difference is not optical flow. The worker still uses
faster-whisper large-v3 for audio transcription: the full pipeline
is NOT model-free.

The Rust engine is enabled in the Modal GPU image and is opt-in when
running Python locally. The frontend API, Go dispatch and callback
protocol, and existing read-only results are unchanged.

## Algorithm and limits

* Single full-source video pass at 2 FPS, 128x72 grayscale.
* Scene-cut heuristic combines luminance histogram distance and frame change.
* Motion heuristic measures average grayscale pixel change.
* At most one cut and one motion event per 15-second source interval;
  the strongest signal in each category is kept to preserve long-video
  coverage and bound the callback payload.
* Dense candidate pass remains 2 FPS, up to 180 seconds and 360 frames;
  returns the strongest 32 motion changes, with existing event kinds:
  scene_change, motion_change, dense_motion_change.
* All timestamps refer to the original source and the existing
  Go candidate-scoring code handles final clip selection.

## GPU decoding (and its limitations)

Rust invokes FFmpeg with -hwaccel cuda in auto mode. When FFmpeg and
the source codec support it, FFmpeg can use NVIDIA hardware decoding
(NVDEC). If CUDA decoding fails, auto mode retries in software.
The selected decoder is surfaced in safe Modal logs and output metadata.

The arithmetic metrics currently execute in Rust on the CPU, not as
custom CUDA kernels. GPU acceleration therefore applies to decoding only,
when supported. Benchmark decoding behavior on the deployed L4;
the Ubuntu FFmpeg package and media codec determine whether NVDEC works.

## Environment

The Modal analysis image builds the Rust source using cargo --offline
and installs the release executable in /usr/local/bin.

The image enables these variables:

    FRAMEVAULT_SCENE_ENGINE=rust
    FRAMEVAULT_SCENE_DECODE=auto

FRAMEVAULT_SCENE_ENGINE defaults to python outside the deployed image,
preserving existing offline fixtures and local workflows. For controlled
benchmarks FRAMEVAULT_SCENE_DECODE may be software, auto, or cuda.
FRAMEVAULT_SCENE_BINARY optionally overrides the executable path.

No credentials, media URLs, external packages, or model downloads are
handled by the Rust binary. It receives a private local media path.
The Python worker continuously checks the existing lease/deadline guard
and kills the Rust process group (including FFmpeg) on cancellation.
The JSON response is capped at 512 KiB, and verified before any
events are accepted.

## Deploy and cache revision

Update FRAMEVAULT_PIPELINE_REVISION to a NEW version in both Modal
secrets and the Go application's signed-in Modal worker settings.
Do not silently reuse cached source analysis after changing algorithms.
Deploy from the full repository checkout:

    modal deploy workers/modal/clipping_m3_worker.py

Follow M3-IMPLEMENTATION.md for the existing model prewarm,
least-privilege secret setup, endpoint configuration, callback
credentials, and source retention. Do not copy only the worker Python
file for deployment: the Rust source directory is required at image
build time.

To revert visual scanning temporarily in a development environment,
set FRAMEVAULT_SCENE_ENGINE=python. For a deployed worker, make an
explicit image configuration change and new revision instead of
silently changing in-flight jobs.

## Verification

    cargo test --offline --manifest-path workers/modal/scene-analyzer/Cargo.toml
    cargo build --offline --release --manifest-path workers/modal/scene-analyzer/Cargo.toml
    PYTHONPATH=workers/modal \
      FRAMEVAULT_SCENE_BINARY=./workers/modal/scene-analyzer/target/release/framevault-scene-analyzer \
      python3 -m unittest discover -s workers/modal -p test_clipping_rust_scene.py -v

GitHub Actions runs these checks against synthetic FFmpeg test media.
Live Modal image builds, L4/NVDEC execution, performance, real-video
quality, and Modal billed cost have not been tested in this change.
Test those before enabling the new pipeline revision in production.

The original Python scene scanner remains as a local fallback; the
Go backend and frontend are intentionally untouched.
