"""Deterministic, full-source audio/video analysis for M3 clipping.

All event labels describe signals the code measures. The module does not call
an LLM, infer semantic objects/actions from pixels, or invent speaker labels.
The default transcription adapter is faster-whisper large-v3; tests inject a
small fixture transcriber and event scanners.
"""

from __future__ import annotations

import array
import contextlib
import hashlib
import importlib.metadata
import json
import math
import os
import platform
import re
import selectors
import signal
import shutil
import statistics
import subprocess
import threading
import unicodedata
from bisect import bisect_left, bisect_right
from collections import Counter, deque
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Callable, Iterable, Mapping


SUPPORTED_MODEL = "large-v3"
TRANSCRIBER_VERSION = "faster-whisper==1.1.1/large-v3"
AUDIO_ALGORITHM_VERSION = "ffmpeg-stream-energy-v1"
VISUAL_ALGORITHM_VERSION = "ffmpeg-scene-motion-v1"
CONTEXT_ALGORITHM_VERSION = "extractive-lexical-context-v1"
CANDIDATE_INSPECTION_ALGORITHM_VERSION = "ffmpeg-dense-candidate-window-v2-profile-pool"
MEDIA_PROTOCOL_WHITELIST = "file,crypto,data"
MEDIA_FORMAT_WHITELIST = "mov,matroska,webm"
_PIPELINE_REVISION_PATTERN = re.compile(r"^[A-Za-z0-9._-]{1,64}$")
_HF_COMMIT_PATTERN = re.compile(r"^[a-f0-9]{40}$")
_MODEL_REPOSITORY = "Systran/faster-whisper-large-v3"
_MODEL_CACHE_DEFAULT = "/tmp/framevault-whisper-cache"
_MODEL_READY_MARKER_PREFIX = ".framevault-model-ready-"
_MODEL_READY_MARKER_MAX_BYTES = 4096
MAX_TRANSCRIPT_SEGMENTS = 20_000
MAX_AUDIO_EVENTS = 20_000
MAX_VISUAL_EVENTS = 20_000
MAX_CANDIDATE_INSPECTIONS = 10
MIN_PROFILE_INSPECTIONS = 5
MAX_CANDIDATE_INSPECTION_EVENTS = 32
MAX_CANDIDATE_INSPECTION_FRAMES = 360
_DENSE_VISUAL_SAMPLE_RATE_HZ = 2
_DENSE_VISUAL_WIDTH = 96
_DENSE_VISUAL_HEIGHT = 54
_WORD = re.compile(r"[^\W_]+(?:['’][^\W_]+)?", re.UNICODE)
_QUESTION_MARKERS = {"why", "how", "what", "when", "where", "who", "क्यों", "कैसे", "क्या"}
_EMOTION_MARKERS = {
    "amazing", "angry", "beautiful", "crazy", "funny", "incredible", "love", "no way",
    "sad", "shocking", "surprise", "wow", "वाह", "अरे", "मज़ेदार", "कमाल",
}
_COMEDY_MARKERS = {"joke", "laugh", "laughing", "funny", "comedy", "punchline", "lol", "haha"}
_PODCAST_MARKERS = {"podcast", "episode", "interview", "guest", "welcome back", "subscribe"}
_GAMING_MARKERS = {"game", "gaming", "level", "round", "respawn", "quest", "boss", "player", "gg"}
_MOVIE_MARKERS = {"scene", "character", "previously", "chapter", "director", "screenplay"}
_SUPPORTED_CONTENT_PROFILES = ("general", "podcast", "comedy", "gaming", "movie")
_COMEDY_SETUP_PHRASES = (
    "guess what", "what if", "so here's", "so here is", "the thing is", "you know what",
    "one day", "i thought", "they told me", "क्यों", "क्या हुआ",
)
_COMEDY_PUNCHLINE_PHRASES = (
    "turns out", "but actually", "the punchline", "that was the joke", "और फिर",
)
_COMEDY_REACTION_PHRASES = ("no way", "are you kidding", "that is hilarious", "i can't believe")
_PODCAST_TOPIC_PHRASES = (
    "today we're talking", "today we are talking", "let's talk about", "lets talk about",
    "we're going to discuss", "we are going to discuss", "the topic is", "our guest",
)
_PODCAST_PAYOFF_PHRASES = (
    "the takeaway", "the answer is", "what we learned", "in the end", "that is why",
    "that's why", "the conclusion", "the result is", "turns out",
)
_STOP_WORDS = {
    "about", "after", "again", "against", "also", "because", "before", "being", "between",
    "could", "from", "have", "here", "into", "just", "more", "most", "other", "over",
    "really", "said", "some", "than", "that", "their", "them", "then", "there", "these",
    "they", "this", "those", "through", "very", "were", "what", "when", "where", "which",
    "with", "would", "your", "you", "और", "एक", "का", "की", "के", "को", "में", "पर",
    "है", "हैं", "था", "थी", "थे", "ये", "वह", "जो", "तो", "भी", "नहीं", "क्या",
}


class AnalysisError(RuntimeError):
    """The source could not be analyzed within its declared bounds."""


class WorkCancelled(RuntimeError):
    """The scoped media capability was revoked or its lease ended."""


@dataclass(frozen=True)
class TranscriptSegment:
    start_ms: int
    end_ms: int
    text: str
    speaker_id: str | None = None

    def as_dict(self) -> dict[str, Any]:
        return {
            "start_ms": self.start_ms,
            "end_ms": self.end_ms,
            "text": self.text,
            "speaker_id": self.speaker_id,
        }


def transcribe_faster_whisper(
    media_path: str | Path,
    *,
    model_name: str = SUPPORTED_MODEL,
    cancel_check: Callable[[], None] = lambda: None,
) -> dict[str, Any]:
    """Transcribe once in the source language, preserving source-time segments."""

    if model_name != SUPPORTED_MODEL:
        raise AnalysisError("unsupported transcription model")
    try:
        from faster_whisper import WhisperModel
    except ImportError as error:
        raise AnalysisError("faster-whisper runtime is unavailable") from error

    cancel_check()
    model = _whisper_model(model_name, WhisperModel, cancel_check=cancel_check)
    cancel_check()
    try:
        segments: list[dict[str, Any]] = []
        with _restricted_pyav_inputs():
            segment_iter, info = model.transcribe(
                str(media_path),
                task="transcribe",
                language=None,
                beam_size=5,
                word_timestamps=False,
                vad_filter=False,
                condition_on_previous_text=True,
            )
            for item in segment_iter:
                cancel_check()
                segments.append(
                    {
                        "start_ms": int(round(float(item.start) * 1000)),
                        "end_ms": int(round(float(item.end) * 1000)),
                        "text": str(item.text),
                        # faster-whisper does not diarize; null is truthful.
                        "speaker_id": None,
                    }
                )
                if len(segments) > MAX_TRANSCRIPT_SEGMENTS:
                    raise AnalysisError("transcript has too many segments")
        probability = getattr(info, "language_probability", None)
        if probability is not None and not math.isfinite(float(probability)):
            probability = None
        return {
            "segments": segments,
            "language": getattr(info, "language", None),
            "language_probability": float(probability) if probability is not None else None,
            "language_detector": "faster-whisper",
            "speaker_labels_available": False,
        }
    except WorkCancelled:
        raise
    except AnalysisError:
        raise
    except Exception as error:
        # Model/provider exception text may include local paths; keep it out of
        # callback payloads and logs at the caller.
        raise AnalysisError("transcription failed") from error


@contextlib.contextmanager
def _restricted_pyav_inputs():
    """Apply the FFmpeg protocol/container allowlists to PyAV opens."""

    try:
        import av
    except ImportError as error:
        raise AnalysisError("PyAV runtime is unavailable") from error
    with _PYAV_OPEN_LOCK:
        original_open = av.open

        def restricted_open(*args: Any, **kwargs: Any) -> Any:
            options = dict(kwargs.get("options") or {})
            options["protocol_whitelist"] = MEDIA_PROTOCOL_WHITELIST
            options["format_whitelist"] = MEDIA_FORMAT_WHITELIST
            kwargs["options"] = options
            return original_open(*args, **kwargs)

        av.open = restricted_open
        try:
            yield
        finally:
            av.open = original_open


_MODEL_CACHE: dict[str, Any] = {}
_MODEL_MANIFEST_CACHE: dict[str, tuple[str, str]] = {}
_RUNTIME_MANIFEST_CACHE: dict[str, dict[str, str]] = {}
_MANIFEST_LOCK = threading.Lock()
_PYAV_OPEN_LOCK = threading.RLock()


def _whisper_model(
    model_name: str,
    model_class: Any,
    *,
    cancel_check: Callable[[], None] = lambda: None,
) -> Any:
    revision = _configured_model_revision()
    cache_key = f"{model_name}:{revision}"
    model = _MODEL_CACHE.get(cache_key)
    # The GPU worker never downloads model files. Operators run the separate
    # CPU prewarm operation first; this is local-only so job capabilities can
    # never accompany a Hugging Face request.
    if model is None:
        snapshot, marker = _require_prewarmed_snapshot(revision)
        cancel_check()
    if model is None:
        try:
            from huggingface_hub import snapshot_download
        except ImportError as error:
            raise AnalysisError("pinned Hugging Face model loader is unavailable") from error
        device = os.environ.get("FRAMEVAULT_WHISPER_DEVICE", "cuda")
        compute_type = "float16" if device == "cuda" else "int8"
        cache_root = Path(os.environ.get("FRAMEVAULT_WHISPER_CACHE", "/tmp/framevault-whisper-cache"))
        try:
            snapshot = Path(snapshot_download(
                repo_id="Systran/faster-whisper-large-v3",
                revision=revision,
                cache_dir=str(cache_root),
                local_files_only=True,
            ))
            resolved_root = cache_root.resolve()
            resolved_snapshot = snapshot.resolve()
            resolved_snapshot.relative_to(resolved_root)
        except Exception as error:
            raise AnalysisError("pinned faster-whisper model snapshot is unavailable") from error
        if snapshot.name != revision or not snapshot.is_dir():
            raise AnalysisError("faster-whisper snapshot does not match the configured immutable revision")
        cancel_check()
        weights_sha256, snapshot_revision = _model_snapshot_fingerprint(
            revision=revision,
            cancel_check=cancel_check,
        )
        if snapshot_revision != revision or weights_sha256 != marker["model_weights_sha256"]:
            raise AnalysisError("prewarmed faster-whisper model fingerprint does not match local snapshot")
        cancel_check()
        model = model_class(
            str(snapshot),
            device=device,
            compute_type=compute_type,
        )
        cancel_check()
        _MODEL_CACHE[cache_key] = model
    return model


def _configured_model_revision() -> str:
    revision = os.environ.get("FRAMEVAULT_WHISPER_MODEL_REVISION", "")
    if not _HF_COMMIT_PATTERN.fullmatch(revision):
        raise AnalysisError("an immutable 40-character faster-whisper model commit is required")
    return revision


def prewarm_faster_whisper_snapshot(
    *,
    revision: str | None = None,
    cache_dir: str | Path | None = None,
    snapshot_downloader: Callable[..., str] | None = None,
    cancel_check: Callable[[], None] = lambda: None,
) -> dict[str, str]:
    """Fetch and fingerprint the immutable model snapshot for CPU prewarm.

    This helper is called only by the separately invoked CPU Modal prewarm
    function. It receives no job media or scoped capabilities. GPU analysis
    uses `_require_prewarmed_snapshot` and local-only Hugging Face resolution.
    """

    revision = revision or _configured_model_revision()
    if not _HF_COMMIT_PATTERN.fullmatch(revision):
        raise AnalysisError("an immutable 40-character faster-whisper model commit is required")
    cache_root = Path(cache_dir or os.environ.get("FRAMEVAULT_WHISPER_CACHE", _MODEL_CACHE_DEFAULT))
    cache_root.mkdir(parents=True, exist_ok=True)
    cancel_check()
    downloader = snapshot_downloader
    if downloader is None:
        try:
            from huggingface_hub import snapshot_download
        except ImportError as error:
            raise AnalysisError("pinned Hugging Face model loader is unavailable") from error
        downloader = snapshot_download
    try:
        snapshot = Path(downloader(
            repo_id=_MODEL_REPOSITORY,
            revision=revision,
            cache_dir=str(cache_root),
            local_files_only=False,
        ))
        resolved_root = cache_root.resolve()
        resolved_snapshot = snapshot.resolve()
        resolved_snapshot.relative_to(resolved_root)
    except WorkCancelled:
        raise
    except Exception as error:
        raise AnalysisError("pinned faster-whisper model prewarm failed") from error
    cancel_check()
    if snapshot.name != revision or not snapshot.is_dir():
        raise AnalysisError("prewarmed faster-whisper snapshot does not match the configured immutable revision")
    weights_sha256, actual_revision = _model_snapshot_fingerprint(
        revision=revision,
        cancel_check=cancel_check,
    )
    if actual_revision != revision:
        raise AnalysisError("prewarmed faster-whisper snapshot revision is invalid")
    marker = {
        "model_repository": _MODEL_REPOSITORY,
        "model_snapshot": revision,
        "model_weights_sha256": weights_sha256,
    }
    marker_bytes = json.dumps(marker, sort_keys=True, separators=(",", ":")).encode("utf-8")
    marker_path = _model_ready_marker_path(cache_root, revision)
    temporary_path: Path | None = None
    try:
        import tempfile

        with tempfile.NamedTemporaryFile(
            mode="wb",
            dir=cache_root,
            prefix=".framevault-model-ready-tmp-",
            delete=False,
        ) as marker_file:
            temporary_path = Path(marker_file.name)
            os.chmod(temporary_path, 0o600)
            marker_file.write(marker_bytes)
            marker_file.flush()
            os.fsync(marker_file.fileno())
        os.replace(temporary_path, marker_path)
        temporary_path = None
    except OSError as error:
        raise AnalysisError("prewarmed faster-whisper ready marker could not be written") from error
    finally:
        if temporary_path is not None:
            temporary_path.unlink(missing_ok=True)
    cancel_check()
    return marker


def _require_prewarmed_snapshot(revision: str) -> tuple[Path, dict[str, str]]:
    cache_root = Path(os.environ.get("FRAMEVAULT_WHISPER_CACHE", _MODEL_CACHE_DEFAULT))
    snapshot = cache_root / "models--Systran--faster-whisper-large-v3" / "snapshots" / revision
    marker_path = _model_ready_marker_path(cache_root, revision)
    try:
        marker_info = marker_path.lstat()
        if not marker_path.is_file() or marker_info.st_size > _MODEL_READY_MARKER_MAX_BYTES:
            raise AnalysisError("prewarmed faster-whisper snapshot marker is invalid")
        marker = json.loads(marker_path.read_bytes())
    except AnalysisError:
        raise
    except (OSError, ValueError, json.JSONDecodeError) as error:
        raise AnalysisError("pinned faster-whisper snapshot has not been prewarmed") from error
    if (
        not isinstance(marker, dict)
        or marker.get("model_repository") != _MODEL_REPOSITORY
        or marker.get("model_snapshot") != revision
        or not isinstance(marker.get("model_weights_sha256"), str)
        or not re.fullmatch(r"[a-f0-9]{64}", marker["model_weights_sha256"])
        or not snapshot.is_dir()
    ):
        raise AnalysisError("pinned faster-whisper snapshot has not been prewarmed")
    try:
        snapshot.resolve().relative_to(cache_root.resolve())
    except (OSError, ValueError) as error:
        raise AnalysisError("prewarmed model snapshot escaped its configured cache") from error
    return snapshot, marker


def _model_ready_marker_path(cache_root: Path, revision: str) -> Path:
    return cache_root / f"{_MODEL_READY_MARKER_PREFIX}{revision}.json"


def analysis_runtime_manifest(
    *,
    include_model: bool = True,
    cancel_check: Callable[[], None] = lambda: None,
    ffmpeg: str = "ffmpeg",
    os_release_path: str | Path = "/etc/os-release",
    distribution_provider: Callable[[], Iterable[Any]] | None = None,
) -> dict[str, str]:
    """Return immutable runtime provenance used to key reusable source cores."""

    if include_model:
        model_revision = _configured_model_revision()
    else:
        try:
            model_revision = _configured_model_revision()
        except AnalysisError:
            model_revision = "not-configured"
    executable = shutil.which(ffmpeg) if not Path(ffmpeg).is_absolute() else ffmpeg
    if not executable:
        raise AnalysisError("FFmpeg executable is unavailable")
    try:
        resolved_ffmpeg = Path(executable).resolve(strict=True)
    except OSError as error:
        raise AnalysisError("FFmpeg executable is unavailable") from error
    cache_key = f"{include_model}:{model_revision}:{resolved_ffmpeg}"
    cacheable = distribution_provider is None and str(os_release_path) == "/etc/os-release"
    if cacheable:
        with _MANIFEST_LOCK:
            cached = _RUNTIME_MANIFEST_CACHE.get(cache_key)
        if cached is not None:
            return dict(cached)

    if include_model:
        model_sha256, snapshot = _model_snapshot_fingerprint(revision=model_revision, cancel_check=cancel_check)
        try:
            ctranslate2_version = importlib.metadata.version("ctranslate2")
        except importlib.metadata.PackageNotFoundError as error:
            raise AnalysisError("CTranslate2 runtime version is unavailable") from error
        model_snapshot = snapshot
    else:
        model_sha256 = "not-used-model-weights"
        ctranslate2_version = "not-used-injected-transcriber"
        model_snapshot = model_revision

    distribution_sha256, distribution_count = _python_distributions_fingerprint(distribution_provider)
    os_release_sha256 = _bounded_file_sha256(os_release_path, 16 * 1024, "operating-system release metadata")
    ffmpeg_sha256 = _file_sha256(resolved_ffmpeg, cancel_check)

    try:
        completed = subprocess.run(
            [str(resolved_ffmpeg), "-version"],
            check=True,
            capture_output=True,
            text=True,
            timeout=10,
        )
    except Exception as error:
        raise AnalysisError("FFmpeg build metadata is unavailable") from error
    lines = completed.stdout.splitlines()
    first_line = next((line.strip() for line in lines if line.startswith("ffmpeg version ")), "")
    configuration_line = next((line.strip() for line in lines if line.startswith("configuration: ")), "")
    if not first_line or not configuration_line:
        raise AnalysisError("FFmpeg build metadata is incomplete")
    manifest = {
        "model_repository": "Systran/faster-whisper-large-v3" if model_revision != "not-configured" else "not-used-model",
        "model_snapshot": model_snapshot,
        "model_weights_sha256": model_sha256,
        "ctranslate2_version": ctranslate2_version,
        "language_mode": "auto",
        "ffmpeg_version": first_line,
        "ffmpeg_configuration_sha256": hashlib.sha256(configuration_line.encode("utf-8")).hexdigest(),
        "ffmpeg_executable_sha256": ffmpeg_sha256,
        "python_distributions_sha256": distribution_sha256,
        "python_distributions_count": str(distribution_count),
        "os_release_sha256": os_release_sha256,
        "python_version": platform.python_version(),
        "modal_version": _package_version_or_uninstalled("modal"),
    }
    manifest["runtime_manifest_sha256"] = hashlib.sha256(
        json.dumps(manifest, sort_keys=True, separators=(",", ":")).encode("utf-8")
    ).hexdigest()
    if cacheable:
        with _MANIFEST_LOCK:
            return dict(_RUNTIME_MANIFEST_CACHE.setdefault(cache_key, manifest))
    return manifest


def _python_distributions_fingerprint(
    provider: Callable[[], Iterable[Any]] | None = None,
) -> tuple[str, int]:
    source = importlib.metadata.distributions if provider is None else provider
    inventory: list[tuple[str, str]] = []
    for distribution in source():
        metadata = getattr(distribution, "metadata", None)
        name = metadata.get("Name") if metadata is not None and hasattr(metadata, "get") else None
        version = getattr(distribution, "version", None)
        if not isinstance(name, str) or not name.strip() or not isinstance(version, str) or not version:
            raise AnalysisError("installed Python distribution metadata is incomplete")
        normalized_name = re.sub(r"[-_.]+", "-", name.strip()).lower()
        if len(normalized_name) > 256 or len(version) > 256:
            raise AnalysisError("installed Python distribution metadata exceeds its bound")
        inventory.append((normalized_name, version))
        if len(inventory) > 8192:
            raise AnalysisError("installed Python distribution inventory exceeds its bound")
    inventory.sort()
    encoded = json.dumps(inventory, ensure_ascii=True, separators=(",", ":")).encode("ascii")
    return hashlib.sha256(encoded).hexdigest(), len(inventory)


def _bounded_file_sha256(path: str | Path, maximum_bytes: int, description: str) -> str:
    try:
        with Path(path).open("rb") as bounded_file:
            data = bounded_file.read(maximum_bytes + 1)
    except OSError as error:
        raise AnalysisError(f"{description} is unavailable") from error
    if len(data) > maximum_bytes:
        raise AnalysisError(f"{description} exceeds its size bound")
    return hashlib.sha256(data).hexdigest()


def _file_sha256(path: str | Path, cancel_check: Callable[[], None]) -> str:
    digest = hashlib.sha256()
    try:
        with Path(path).open("rb") as executable_file:
            while True:
                cancel_check()
                block = executable_file.read(1024 * 1024)
                if not block:
                    break
                digest.update(block)
    except OSError as error:
        raise AnalysisError("FFmpeg executable fingerprint is unavailable") from error
    return digest.hexdigest()


def _model_snapshot_fingerprint(
    *,
    revision: str | None = None,
    cancel_check: Callable[[], None],
) -> tuple[str, str]:
    revision = revision or _configured_model_revision()
    if not _HF_COMMIT_PATTERN.fullmatch(revision):
        raise AnalysisError("faster-whisper model snapshot revision is invalid")
    root = Path(os.environ.get("FRAMEVAULT_WHISPER_CACHE", "/tmp/framevault-whisper-cache"))
    repository = root / "models--Systran--faster-whisper-large-v3"
    snapshots = repository / "snapshots"
    snapshot = snapshots / revision
    if not snapshot.is_dir():
        raise AnalysisError("configured immutable faster-whisper large-v3 snapshot is unavailable")
    resolved_root = root.resolve()
    resolved_snapshot = snapshot.resolve()
    try:
        resolved_snapshot.relative_to(resolved_root)
    except ValueError as error:
        raise AnalysisError("cached model snapshot escaped its configured cache") from error
    snapshot_key = str(resolved_snapshot)
    with _MANIFEST_LOCK:
        cached = _MODEL_MANIFEST_CACHE.get(snapshot_key)
    if cached is not None:
        return cached[0], revision

    files = sorted(path for path in snapshot.rglob("*") if path.is_file())
    if not files:
        raise AnalysisError("cached faster-whisper model snapshot has no files")
    manifest_hash = hashlib.sha256()
    for path in files:
        cancel_check()
        resolved_path = path.resolve(strict=True)
        try:
            resolved_path.relative_to(resolved_root)
        except ValueError as error:
            raise AnalysisError("cached model file escaped its configured cache") from error
        relative_name = path.relative_to(snapshot).as_posix().encode("utf-8")
        file_hash = hashlib.sha256()
        file_size = 0
        with resolved_path.open("rb") as model_file:
            while True:
                cancel_check()
                chunk = model_file.read(8 * 1024 * 1024)
                if not chunk:
                    break
                file_size += len(chunk)
                file_hash.update(chunk)
        manifest_hash.update(len(relative_name).to_bytes(4, "big"))
        manifest_hash.update(relative_name)
        manifest_hash.update(file_size.to_bytes(8, "big"))
        manifest_hash.update(file_hash.digest())
    result = manifest_hash.hexdigest()
    with _MANIFEST_LOCK:
        _MODEL_MANIFEST_CACHE[snapshot_key] = (result, revision)
    return result, revision


def _package_version_or_uninstalled(package: str) -> str:
    try:
        return importlib.metadata.version(package)
    except importlib.metadata.PackageNotFoundError:
        return "not-installed"


def normalize_segments(raw_segments: Iterable[Any], duration_ms: int) -> list[TranscriptSegment]:
    """Normalize injected or model segments to bounded integer source time."""

    normalized: list[TranscriptSegment] = []
    for raw in raw_segments:
        if len(normalized) >= MAX_TRANSCRIPT_SEGMENTS:
            raise AnalysisError("transcript has too many segments")
        start = _field(raw, "start_ms", None)
        end = _field(raw, "end_ms", None)
        if start is None:
            start = int(round(float(_field(raw, "start", 0)) * 1000))
        if end is None:
            end = int(round(float(_field(raw, "end", 0)) * 1000))
        text = str(_field(raw, "text", "")).strip()
        if isinstance(start, bool) or isinstance(end, bool) or not isinstance(start, int) or not isinstance(end, int):
            raise AnalysisError("transcript timestamps must be integer milliseconds")
        if not text or end <= start or end <= 0 or start >= duration_ms:
            continue
        start = max(0, min(duration_ms, start))
        end = max(start + 1, min(duration_ms, end))
        speaker = _field(raw, "speaker_id", None)
        speaker_id = speaker if isinstance(speaker, str) and speaker.strip() else None
        normalized.append(TranscriptSegment(start, end, text, speaker_id))
    normalized.sort(key=lambda item: (item.start_ms, item.end_ms))
    return normalized


def analyze_source(
    media_path: str | Path,
    *,
    source_duration_ms: int,
    source_sha256: str,
    model_name: str = SUPPORTED_MODEL,
    requested_content_type: str = "general",
    pipeline_revision: str = "",
    min_clip_seconds: int = 15,
    max_clip_seconds: int = 180,
    candidate_limit: int = 7,
    has_audio_stream: bool = True,
    transcriber: Callable[..., Mapping[str, Any]] | None = None,
    audio_scanner: Callable[..., Mapping[str, Any]] | None = None,
    visual_scanner: Callable[..., Mapping[str, Any]] | None = None,
    candidate_inspector: Callable[..., Mapping[str, Any]] | None = None,
    runtime_manifest: Mapping[str, str] | None = None,
    cancel_check: Callable[[], None] = lambda: None,
) -> dict[str, Any]:
    """Build bounded full-video analysis using injectable deterministic seams."""

    if not 0 < source_duration_ms <= 4 * 60 * 60 * 1000:
        raise AnalysisError("source duration is outside the supported range")
    if requested_content_type not in {"general", "podcast", "comedy", "gaming", "movie"}:
        raise AnalysisError("content type is unsupported")
    if not isinstance(pipeline_revision, str) or not _PIPELINE_REVISION_PATTERN.fullmatch(pipeline_revision):
        raise AnalysisError("pipeline revision is invalid")
    if not isinstance(has_audio_stream, bool):
        raise AnalysisError("audio stream metadata is invalid")
    cancel_check()
    use_default_transcriber = transcriber is None and has_audio_stream
    if has_audio_stream:
        transcriber = transcriber or transcribe_faster_whisper
        transcript_result = transcriber(media_path, model_name=model_name, cancel_check=cancel_check)
    else:
        transcript_result = {
            "segments": [],
            "language": None,
            "language_probability": None,
            "language_detector": "not-run-no-audio-stream",
            "speaker_labels_available": False,
        }
    if not isinstance(transcript_result, Mapping):
        raise AnalysisError("transcriber returned an invalid result")
    segments = normalize_segments(transcript_result.get("segments", []), source_duration_ms)
    cancel_check()
    if not has_audio_stream:
        audio_scan = {
            "events": [],
            "covered_duration_ms": 0,
            "algorithm_version": "not-run-no-audio-stream-v1",
        }
    elif audio_scanner is None:
        audio_scan = scan_audio_events(media_path, segments, source_duration_ms, cancel_check=cancel_check)
    else:
        audio_scan = audio_scanner(media_path, segments, source_duration_ms, cancel_check=cancel_check)
    cancel_check()
    if visual_scanner is None:
        visual_scan = scan_visual_events(media_path, source_duration_ms, cancel_check=cancel_check)
    else:
        visual_scan = visual_scanner(media_path, source_duration_ms, cancel_check=cancel_check)
    if not isinstance(audio_scan, Mapping) or not isinstance(visual_scan, Mapping):
        raise AnalysisError("event scanner returned an invalid result")

    audio_events = _bounded_events(audio_scan.get("events", []), source_duration_ms, MAX_AUDIO_EVENTS)
    visual_events = _bounded_events(visual_scan.get("events", []), source_duration_ms, MAX_VISUAL_EVENTS)
    candidate_inspections = inspect_candidate_windows(
        media_path,
        source_duration_ms=source_duration_ms,
        min_clip_seconds=min_clip_seconds,
        max_clip_seconds=max_clip_seconds,
        candidate_limit=candidate_limit,
        content_type=requested_content_type,
        transcript_segments=segments,
        audio_events=audio_events,
        visual_events=visual_events,
        inspector=candidate_inspector,
        cancel_check=cancel_check,
    )
    runtime_manifest = dict(runtime_manifest or analysis_runtime_manifest(
        include_model=use_default_transcriber,
        cancel_check=cancel_check,
    ))
    if not has_audio_stream:
        runtime_manifest.update({
            "model_repository": "Systran/faster-whisper-large-v3",
            "model_weights_sha256": "not-used-no-audio-stream",
            "ctranslate2_version": "not-used-no-audio-stream",
            "language_mode": "auto",
        })
    transcript_text = " ".join(segment.text for segment in segments).strip()
    language = transcript_result.get("language")
    language = language if isinstance(language, str) and language else None
    language_probability = transcript_result.get("language_probability")
    if isinstance(language_probability, bool) or not isinstance(language_probability, (int, float)) or not math.isfinite(float(language_probability)):
        language_probability = None

    return {
        "source_sha256": source_sha256,
        "source_duration_ms": source_duration_ms,
        "requested_content_type": requested_content_type,
        "pipeline_revision": pipeline_revision,
        "media_streams": {"has_audio": has_audio_stream, "has_video": True},
        "transcript": {
            "available": has_audio_stream,
            "status": "complete" if has_audio_stream else "unavailable_no_audio_stream",
            "text": transcript_text,
            "segments": [segment.as_dict() for segment in segments],
            "language": language,
            "language_probability": float(language_probability) if language_probability is not None else None,
            "language_detector": str(transcript_result.get("language_detector", "injected-transcriber")),
            "original_script": _script_metadata(transcript_text),
            "speaker_labels_available": bool(transcript_result.get("speaker_labels_available", False)),
        },
        "context_timeline": build_context_timeline(segments, source_duration_ms),
        "audio_events": audio_events,
        "audio_scan": {
            "algorithm_version": str(audio_scan.get("algorithm_version", AUDIO_ALGORITHM_VERSION)),
            "covered_duration_ms": min(source_duration_ms, _safe_int(audio_scan.get("covered_duration_ms"), source_duration_ms)),
        },
        "visual_events": visual_events,
        "visual_scan": {
            "algorithm_version": VISUAL_ALGORITHM_VERSION,
            "covered_duration_ms": min(source_duration_ms, _safe_int(visual_scan.get("covered_duration_ms"), source_duration_ms)),
        },
        "analysis_versions": {
            "transcriber": (
                TRANSCRIBER_VERSION if use_default_transcriber else (
                    "injected-transcriber" if has_audio_stream else "not-used-no-audio-stream"
                )
            ),
            "model": model_name if use_default_transcriber else ("injected-transcriber" if has_audio_stream else "not-used-no-audio-stream"),
            "audio_events": str(audio_scan.get("algorithm_version", AUDIO_ALGORITHM_VERSION)),
            "visual_events": VISUAL_ALGORITHM_VERSION,
            "context_timeline": CONTEXT_ALGORITHM_VERSION,
            "candidate_inspection": CANDIDATE_INSPECTION_ALGORITHM_VERSION,
            "pipeline_revision": pipeline_revision,
            "audio_available": str(has_audio_stream).lower(),
            "prompt_version": "none-extractive-no-llm-v1",
            **runtime_manifest,
        },
        "candidate_inspections": candidate_inspections,
    }


def inspect_candidate_windows(
    media_path: str | Path,
    *,
    source_duration_ms: int,
    min_clip_seconds: int,
    max_clip_seconds: int,
    candidate_limit: int,
    content_type: str = "general",
    transcript_segments: list[TranscriptSegment],
    audio_events: list[dict[str, Any]],
    visual_events: list[dict[str, Any]],
    inspector: Callable[..., Mapping[str, Any]] | None = None,
    cancel_check: Callable[[], None] = lambda: None,
) -> list[dict[str, Any]]:
    """Inspect bounded, well-spaced cue windows with denser visual sampling."""

    if (
        isinstance(min_clip_seconds, bool)
        or isinstance(max_clip_seconds, bool)
        or not isinstance(min_clip_seconds, int)
        or not isinstance(max_clip_seconds, int)
        or not 15 <= min_clip_seconds <= max_clip_seconds <= 180
    ):
        raise AnalysisError("candidate inspection clip bounds are invalid")
    if isinstance(candidate_limit, bool) or not isinstance(candidate_limit, int) or not 1 <= candidate_limit <= MAX_CANDIDATE_INSPECTIONS:
        raise AnalysisError("candidate inspection count is outside its bound")
    if content_type not in _SUPPORTED_CONTENT_PROFILES:
        raise AnalysisError("candidate inspection content profile is unsupported")
    target_window_ms = min(source_duration_ms, min(max_clip_seconds, max(min_clip_seconds, 60)) * 1000)
    cues = _candidate_anchor_cues(
        transcript_segments,
        audio_events,
        visual_events,
        max_span_ms=target_window_ms,
        cancel_check=cancel_check,
    )
    # Keep one inspection opportunity for each known editorial profile even
    # when the current job asks for fewer clips. The source core is reused
    # after profile edits, so its bounded pool must carry cross-profile cues.
    pool_limit = min(MAX_CANDIDATE_INSPECTIONS, max(candidate_limit, MIN_PROFILE_INSPECTIONS))
    nominations = _select_candidate_anchors(
        cues,
        source_duration_ms,
        target_window_ms,
        pool_limit,
        preferred_profile=content_type,
    )
    scanner = inspector or inspect_candidate_window
    inspections: list[dict[str, Any]] = []
    for cue in nominations:
        cancel_check()
        anchor_ms = cue["anchor_ms"]
        start_ms = max(0, min(source_duration_ms - target_window_ms, anchor_ms - target_window_ms // 2))
        end_ms = start_ms + target_window_ms
        scan = scanner(media_path, start_ms, end_ms, cancel_check=cancel_check)
        if not isinstance(scan, Mapping):
            raise AnalysisError("candidate inspector returned an invalid result")
        sample_rate = scan.get("sample_rate_hz")
        sampled_frames = scan.get("sampled_frames")
        if (
            isinstance(sample_rate, bool)
            or sample_rate != _DENSE_VISUAL_SAMPLE_RATE_HZ
            or isinstance(sampled_frames, bool)
            or not isinstance(sampled_frames, int)
            or not 0 <= sampled_frames <= MAX_CANDIDATE_INSPECTION_FRAMES
        ):
            raise AnalysisError("candidate inspection sampling metadata is outside its bounds")
        dense_events = scan.get("visual_events", [])
        if not isinstance(dense_events, list) or len(dense_events) > MAX_CANDIDATE_INSPECTION_EVENTS:
            raise AnalysisError("candidate inspection event list exceeds its bound")
        valid_dense_events = _validate_dense_events(dense_events, source_duration_ms)
        for scene in visual_events:
            if scene.get("kind") != "scene_change" or not start_ms <= scene["start_ms"] < end_ms:
                continue
            valid_dense_events.append({
                "start_ms": scene["start_ms"],
                "end_ms": scene["end_ms"],
                "kind": "scene_change",
                "score": 0.30,
                "evidence": {
                    "detector": "ffmpeg_scene_score_threshold_0_30",
                    "visual_evidence_strength": "weak",
                },
            })
        valid_dense_events.sort(key=lambda item: (-item["score"], item["start_ms"], item["kind"]))
        valid_dense_events = valid_dense_events[:MAX_CANDIDATE_INSPECTION_EVENTS]
        valid_dense_events.sort(key=lambda item: (item["start_ms"], item["kind"]))
        inspections.append({
            "anchor_ms": anchor_ms,
            "start_ms": start_ms,
            "end_ms": end_ms,
            "anchor_kind": cue["anchor_kind"],
            "anchor_score": cue["anchor_score"],
            "sample_rate_hz": sample_rate,
            "sampled_frames": sampled_frames,
            "visual_events": valid_dense_events,
        })
    return inspections


def _candidate_anchor_cues(
    transcript_segments: list[TranscriptSegment],
    audio_events: list[dict[str, Any]],
    visual_events: list[dict[str, Any]],
    *,
    max_span_ms: int,
    cancel_check: Callable[[], None] = lambda: None,
) -> list[dict[str, Any]]:
    cues: list[dict[str, Any]] = []
    marker_groups = (_EMOTION_MARKERS, _COMEDY_MARKERS, _PODCAST_MARKERS, _GAMING_MARKERS, _MOVIE_MARKERS)
    all_markers = set().union(*marker_groups)
    for index, segment in enumerate(transcript_segments):
        if index % 256 == 0:
            cancel_check()
        text = segment.text.casefold()
        terms = set(_content_tokens(text))
        has_marker = any(marker in text for marker in all_markers)
        question = "?" in text or any(marker in terms for marker in _QUESTION_MARKERS)
        if question:
            score, kind = 0.82, "transcript_question_hook"
        elif has_marker:
            score, kind = 0.76, "transcript_editorial_cue"
        elif len(terms) >= 7:
            score, kind = 0.35, "transcript_context_segment"
        else:
            continue
        cues.append({
            "anchor_ms": (segment.start_ms + segment.end_ms) // 2,
            "anchor_kind": kind,
            "anchor_score": score,
            "profile": "general",
        })

    for event in audio_events:
        kind = event.get("kind")
        if kind == "repeated_non_speech_pulses":
            score = 0.98
        elif kind == "non_speech_audio_transient":
            score = 0.90
        elif kind == "audio_energy_transient":
            score = 0.60
        else:
            continue
        cues.append({
            "anchor_ms": (event["start_ms"] + event["end_ms"]) // 2,
            "anchor_kind": f"audio_{kind}",
            "anchor_score": score,
            "profile": "general",
        })

    for event in visual_events:
        kind = event.get("kind")
        if kind == "scene_change":
            score = 0.52
        elif kind == "motion_change":
            score = 0.43
        else:
            continue
        cues.append({
            "anchor_ms": (event["start_ms"] + event["end_ms"]) // 2,
            "anchor_kind": f"visual_{kind}",
            "anchor_score": score,
            "profile": "general",
        })
    cues.extend(_profile_candidate_anchor_cues(
        transcript_segments,
        audio_events,
        visual_events,
        max_span_ms=max_span_ms,
        cancel_check=cancel_check,
    ))
    return cues


def _profile_candidate_anchor_cues(
    transcript_segments: list[TranscriptSegment],
    audio_events: list[dict[str, Any]],
    visual_events: list[dict[str, Any]],
    *,
    max_span_ms: int,
    cancel_check: Callable[[], None],
) -> list[dict[str, Any]]:
    cues: list[dict[str, Any]] = []
    segments = sorted(transcript_segments, key=lambda item: (item.start_ms, item.end_ms))
    segment_starts = [item.start_ms for item in segments]

    def add(profile: str, kind: str, anchor_ms: int, score: float) -> None:
        if 0 <= anchor_ms:
            cues.append({
                "anchor_ms": anchor_ms,
                "anchor_kind": kind,
                "anchor_score": score,
                "profile": profile,
            })

    # Comedy nominations use observable lexical setup/payoff/reaction cues and
    # measured non-speech audio pulses. The labels remain heuristic.
    comedy_setups: list[TranscriptSegment] = []
    comedy_payoffs: list[TranscriptSegment] = []
    comedy_reactions: list[tuple[int, int, str]] = []
    for index, segment in enumerate(segments):
        if index % 256 == 0:
            cancel_check()
        text = segment.text.casefold()
        terms = set(_content_tokens(text))
        is_setup = "?" in text or bool(terms & _QUESTION_MARKERS) or any(phrase in text for phrase in _COMEDY_SETUP_PHRASES)
        is_payoff = any(marker in text for marker in _COMEDY_MARKERS) or any(phrase in text for phrase in _COMEDY_PUNCHLINE_PHRASES)
        is_reaction = any(phrase in text for phrase in _COMEDY_REACTION_PHRASES) or bool(terms & (_EMOTION_MARKERS | _COMEDY_MARKERS))
        if is_setup:
            comedy_setups.append(segment)
            add("comedy", "comedy_setup", (segment.start_ms + segment.end_ms) // 2, 0.80)
        if is_payoff:
            comedy_payoffs.append(segment)
            add("comedy", "comedy_punchline", (segment.start_ms + segment.end_ms) // 2, 0.86)
        if is_reaction:
            comedy_reactions.append((segment.start_ms, segment.end_ms, "transcript_reaction"))
            add("comedy", "comedy_reaction", (segment.start_ms + segment.end_ms) // 2, 0.84)
    for event in audio_events:
        if event.get("kind") in {"repeated_non_speech_pulses", "non_speech_audio_transient"}:
            comedy_reactions.append((event["start_ms"], event["end_ms"], "non_speech_audio_reaction"))
            add("comedy", "comedy_audio_reaction", (event["start_ms"] + event["end_ms"]) // 2, 0.91)
    payoff_starts = [item.start_ms for item in comedy_payoffs]
    reaction_starts = [item[0] for item in sorted(comedy_reactions)]
    sorted_reactions = sorted(comedy_reactions)
    for index, setup in enumerate(comedy_setups):
        if index % 128 == 0:
            cancel_check()
        payoff_index = bisect_left(payoff_starts, setup.end_ms)
        if payoff_index >= len(comedy_payoffs):
            continue
        payoff = comedy_payoffs[payoff_index]
        if payoff.start_ms - setup.start_ms > max_span_ms:
            continue
        reaction_index = bisect_left(reaction_starts, payoff.end_ms)
        reaction = None
        if reaction_index < len(sorted_reactions) and sorted_reactions[reaction_index][0] - payoff.end_ms <= min(12_000, max_span_ms):
            reaction = sorted_reactions[reaction_index]
        end_ms = reaction[1] if reaction is not None else payoff.end_ms
        kind = "comedy_setup_punchline_reaction" if reaction is not None else "comedy_setup_punchline"
        add("comedy", kind, (setup.start_ms + end_ms) // 2, 0.97 if reaction is not None else 0.92)

    # Podcast topics and payoffs are paired only when the measured transcript
    # cues fit inside the inspected source-time window.
    podcast_topics: list[TranscriptSegment] = []
    podcast_payoffs: list[TranscriptSegment] = []
    for index, segment in enumerate(segments):
        if index % 256 == 0:
            cancel_check()
        text = segment.text.casefold()
        is_topic = any(phrase in text for phrase in _PODCAST_TOPIC_PHRASES) or any(marker in text for marker in _PODCAST_MARKERS)
        is_payoff = any(phrase in text for phrase in _PODCAST_PAYOFF_PHRASES) or bool(set(_content_tokens(text)) & {"answer", "reason", "result", "finally", "conclusion", "takeaway"})
        if is_topic:
            podcast_topics.append(segment)
            add("podcast", "podcast_topic", (segment.start_ms + segment.end_ms) // 2, 0.82)
        if is_payoff:
            podcast_payoffs.append(segment)
            add("podcast", "podcast_payoff", (segment.start_ms + segment.end_ms) // 2, 0.84)
    podcast_payoff_starts = [item.start_ms for item in podcast_payoffs]
    for index, topic in enumerate(podcast_topics):
        if index % 128 == 0:
            cancel_check()
        payoff_index = bisect_left(podcast_payoff_starts, topic.end_ms)
        if payoff_index >= len(podcast_payoffs):
            continue
        payoff = podcast_payoffs[payoff_index]
        if payoff.start_ms - topic.start_ms <= max_span_ms:
            add("podcast", "podcast_topic_payoff", (topic.start_ms + payoff.end_ms) // 2, 0.93)

    # Gaming continuity requires a measured full-timeline motion event and
    # nearby transcript commentary; it does not label the visual action.
    for index, event in enumerate(visual_events):
        if index % 256 == 0:
            cancel_check()
        if event.get("kind") != "motion_change":
            continue
        event_ms = (event["start_ms"] + event["end_ms"]) // 2
        insertion = bisect_left(segment_starts, event_ms)
        nearby = [segments[position] for position in (insertion - 1, insertion) if 0 <= position < len(segments)]
        nearby = [item for item in nearby if abs((item.start_ms + item.end_ms) // 2 - event_ms) <= 15_000]
        if nearby:
            commentary = min(nearby, key=lambda item: (abs((item.start_ms + item.end_ms) // 2 - event_ms), item.start_ms))
            anchor = (event_ms + (commentary.start_ms + commentary.end_ms) // 2) // 2
            add("gaming", "gaming_motion_with_nearby_commentary", anchor, 0.91)
        else:
            add("gaming", "gaming_motion", event_ms, 0.70)

    # Movie continuity joins source-timed dialogue to measured scene changes;
    # adjacent transcript turns also provide dialogue-only continuity cues.
    for index, event in enumerate(visual_events):
        if index % 256 == 0:
            cancel_check()
        if event.get("kind") != "scene_change" or not segments:
            continue
        scene_ms = (event["start_ms"] + event["end_ms"]) // 2
        insertion = bisect_left(segment_starts, scene_ms)
        nearby = [segments[position] for position in (insertion - 1, insertion) if 0 <= position < len(segments)]
        nearby = [item for item in nearby if abs((item.start_ms + item.end_ms) // 2 - scene_ms) <= 15_000]
        if nearby:
            dialogue = min(nearby, key=lambda item: (abs((item.start_ms + item.end_ms) // 2 - scene_ms), item.start_ms))
            anchor = (scene_ms + (dialogue.start_ms + dialogue.end_ms) // 2) // 2
            add("movie", "movie_dialogue_scene_continuity", anchor, 0.90)
        else:
            add("movie", "movie_scene_change", scene_ms, 0.68)
    group: list[TranscriptSegment] = []
    for index, segment in enumerate(segments):
        if index % 256 == 0:
            cancel_check()
        if group and (segment.start_ms - group[-1].end_ms > 2_500 or segment.end_ms - group[0].start_ms > max_span_ms):
            if len(group) >= 2:
                add("movie", "movie_dialogue_continuity", (group[0].start_ms + group[-1].end_ms) // 2, 0.78)
            group = []
        group.append(segment)
    if len(group) >= 2:
        add("movie", "movie_dialogue_continuity", (group[0].start_ms + group[-1].end_ms) // 2, 0.78)
    return cues


def _select_candidate_anchors(
    cues: list[dict[str, Any]],
    source_duration_ms: int,
    window_ms: int,
    candidate_limit: int,
    *,
    preferred_profile: str = "general",
) -> list[dict[str, Any]]:
    if not cues:
        return []
    selected: list[dict[str, Any]] = []
    selected_windows: list[tuple[int, int]] = []

    def try_add(cue: dict[str, Any]) -> bool:
        start, end = _inspection_window_bounds(cue["anchor_ms"], source_duration_ms, window_ms)
        if any(start < prior_end and prior_start < end for prior_start, prior_end in selected_windows):
            return False
        selected.append(cue)
        selected_windows.append((start, end))
        return True

    def rank(items: Iterable[dict[str, Any]]) -> list[dict[str, Any]]:
        return sorted(items, key=lambda item: (-item["anchor_score"], item["anchor_ms"], item["anchor_kind"]))

    # Reserve a slot for each available profile before filling the rest of the
    # bounded pool with the strongest timeline-distributed generic/profile cues.
    profile_order = (
        ([preferred_profile] if preferred_profile != "general" else [])
        + [profile for profile in _SUPPORTED_CONTENT_PROFILES if profile not in {preferred_profile, "general"}]
        + ["general"]
    )
    for profile in profile_order:
        for cue in rank(item for item in cues if item.get("profile") == profile):
            if try_add(cue):
                break
        if len(selected) >= candidate_limit:
            return sorted(selected, key=lambda item: item["anchor_ms"])

    ranked = rank(cues)
    bucket_winners: dict[int, dict[str, Any]] = {}
    for cue in ranked:
        bucket = min(candidate_limit - 1, cue["anchor_ms"] * candidate_limit // source_duration_ms)
        bucket_winners.setdefault(bucket, cue)
    for bucket in sorted(bucket_winners):
        if len(selected) >= candidate_limit:
            break
        try_add(bucket_winners[bucket])
    if len(selected) < candidate_limit:
        for cue in ranked:
            if len(selected) >= candidate_limit:
                break
            try_add(cue)
    return sorted(selected[:candidate_limit], key=lambda item: item["anchor_ms"])


def _inspection_window_bounds(anchor_ms: int, duration_ms: int, window_ms: int) -> tuple[int, int]:
    start_ms = max(0, min(duration_ms - window_ms, anchor_ms - window_ms // 2))
    return start_ms, start_ms + window_ms


def inspect_candidate_window(
    media_path: str | Path,
    start_ms: int,
    end_ms: int,
    *,
    cancel_check: Callable[[], None] = lambda: None,
    ffmpeg: str = "ffmpeg",
) -> dict[str, Any]:
    """Measure frame-to-frame changes at 2 fps over one nominated source window."""

    if start_ms < 0 or end_ms <= start_ms:
        raise AnalysisError("candidate inspection window is invalid")
    frame_bytes = _DENSE_VISUAL_WIDTH * _DENSE_VISUAL_HEIGHT
    command = [
        ffmpeg,
        "-nostdin",
        "-hide_banner",
        "-loglevel",
        "error",
        "-ss",
        f"{start_ms / 1000:.3f}",
        "-protocol_whitelist",
        MEDIA_PROTOCOL_WHITELIST,
        "-format_whitelist",
        MEDIA_FORMAT_WHITELIST,
        "-i",
        str(media_path),
        "-t",
        f"{(end_ms - start_ms) / 1000:.3f}",
        "-an",
        "-vf",
        f"fps={_DENSE_VISUAL_SAMPLE_RATE_HZ},scale={_DENSE_VISUAL_WIDTH}:{_DENSE_VISUAL_HEIGHT}:flags=bilinear,format=gray",
        "-f",
        "rawvideo",
        "-pix_fmt",
        "gray",
        "pipe:1",
    ]
    process = subprocess.Popen(command, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, start_new_session=True)
    assert process.stdout is not None
    selector = selectors.DefaultSelector()
    selector.register(process.stdout, selectors.EVENT_READ)
    previous: bytes | None = None
    sampled_frames = 0
    events: list[dict[str, Any]] = []
    try:
        while True:
            frame = _read_exact_frame(selector, process.stdout.fileno(), frame_bytes, cancel_check)
            if frame is None:
                break
            sampled_frames += 1
            if sampled_frames > MAX_CANDIDATE_INSPECTION_FRAMES:
                raise AnalysisError("candidate inspection sampled more than 360 frames")
            if previous is not None:
                change = sum(abs(left - right) for left, right in zip(frame, previous)) / frame_bytes
                frame_start_ms = start_ms + int(round((sampled_frames - 1) * 1000 / _DENSE_VISUAL_SAMPLE_RATE_HZ))
                if change >= 16.0:
                    events.append({
                        "start_ms": frame_start_ms,
                        "end_ms": min(end_ms, frame_start_ms + 500),
                        "kind": "dense_motion_change",
                        "score": round(change / 255.0, 4),
                        "evidence": {
                            "detector": "mean_absolute_gray_frame_difference",
                            "mean_absolute_difference": round(change, 2),
                            "visual_evidence_strength": "weak",
                            "sample_rate_hz": _DENSE_VISUAL_SAMPLE_RATE_HZ,
                        },
                    })
            previous = frame
        cancel_check()
        return_code = process.wait()
        if return_code:
            raise AnalysisError("candidate visual scan failed")
        events.sort(key=lambda item: (-item["score"], item["start_ms"]))
        return {
            "sample_rate_hz": _DENSE_VISUAL_SAMPLE_RATE_HZ,
            "sampled_frames": sampled_frames,
            "visual_events": events[:MAX_CANDIDATE_INSPECTION_EVENTS],
        }
    except Exception:
        _kill_process_group(process)
        raise
    finally:
        selector.close()
        process.stdout.close()
        if process.poll() is None:
            process.wait()


def _read_exact_frame(selector: Any, file_descriptor: int, frame_bytes: int, cancel_check: Callable[[], None]) -> bytes | None:
    frame = bytearray()
    while len(frame) < frame_bytes:
        cancel_check()
        if not selector.select(0.25):
            continue
        chunk = os.read(file_descriptor, frame_bytes - len(frame))
        if not chunk:
            if frame:
                raise AnalysisError("candidate visual scan returned a partial frame")
            return None
        frame.extend(chunk)
    return bytes(frame)


def _read_pipe_chunk(selector: Any, file_descriptor: int, maximum_bytes: int, cancel_check: Callable[[], None]) -> bytes:
    """Wait for bounded pipe output while keeping cancellation/deadlines live."""

    while True:
        cancel_check()
        if selector.select(0.25):
            return os.read(file_descriptor, maximum_bytes)


def _validate_dense_events(events: list[Any], source_duration_ms: int) -> list[dict[str, Any]]:
    if len(events) > MAX_CANDIDATE_INSPECTION_EVENTS:
        raise AnalysisError("candidate inspection event list exceeds its bound")
    valid: list[dict[str, Any]] = []
    for event in events:
        if not isinstance(event, Mapping):
            raise AnalysisError("candidate inspection event is invalid")
        start, end, score, kind, evidence = (
            event.get("start_ms"), event.get("end_ms"), event.get("score"), event.get("kind"), event.get("evidence")
        )
        if (
            isinstance(start, bool)
            or not isinstance(start, int)
            or isinstance(end, bool)
            or not isinstance(end, int)
            or not 0 <= start < end <= source_duration_ms
            or not isinstance(score, (int, float))
            or isinstance(score, bool)
            or not math.isfinite(float(score))
            or not 0 <= score <= 1
            or not isinstance(kind, str)
            or not isinstance(evidence, Mapping)
        ):
            raise AnalysisError("candidate inspection event is outside its bounds")
        valid.append({
            "start_ms": start,
            "end_ms": end,
            "kind": kind,
            "score": float(score),
            "evidence": dict(evidence),
        })
    return valid


def build_context_timeline(segments: list[TranscriptSegment], duration_ms: int, window_ms: int = 60_000) -> list[dict[str, Any]]:
    """Create lexical topic/story windows across every source-time minute."""

    if duration_ms <= 0 or window_ms <= 0:
        return []
    grouped: dict[int, list[TranscriptSegment]] = {}
    for segment in segments:
        index = min(max(0, segment.start_ms // window_ms), (duration_ms - 1) // window_ms)
        grouped.setdefault(index, []).append(segment)
    global_counts: Counter[str] = Counter()
    for segment in segments:
        global_counts.update(_content_tokens(segment.text))
    timeline: list[dict[str, Any]] = []
    for index in range((duration_ms + window_ms - 1) // window_ms):
        start_ms = index * window_ms
        end_ms = min(duration_ms, start_ms + window_ms)
        items = grouped.get(index, [])
        if not items:
            timeline.append({
                "start_ms": start_ms,
                "end_ms": end_ms,
                "kind": "no_transcript_segments",
                "segment_count": 0,
                "terms": [],
                "opening_excerpt": "",
                "closing_excerpt": "",
            })
            continue
        counts: Counter[str] = Counter()
        for segment in items:
            counts.update(_content_tokens(segment.text))
        terms = sorted(counts, key=lambda term: (-counts[term], -_inverse_frequency(global_counts[term], len(segments)), term))[:6]
        timeline.append({
            "start_ms": start_ms,
            "end_ms": end_ms,
            "kind": "lexical_topic_window",
            "segment_count": len(items),
            "terms": terms,
            "opening_excerpt": _excerpt(items[0].text, 240),
            "closing_excerpt": _excerpt(items[-1].text, 240),
        })
    return timeline


def scan_audio_events(
    media_path: str | Path,
    transcript_segments: list[TranscriptSegment],
    duration_ms: int,
    *,
    cancel_check: Callable[[], None] = lambda: None,
    ffmpeg: str = "ffmpeg",
) -> dict[str, Any]:
    """Stream low-rate mono PCM and mark silence, energy changes, and pulses."""

    command = [
        ffmpeg,
        "-nostdin",
        "-hide_banner",
        "-loglevel",
        "error",
        "-protocol_whitelist",
        MEDIA_PROTOCOL_WHITELIST,
        "-format_whitelist",
        MEDIA_FORMAT_WHITELIST,
        "-i",
        str(media_path),
        "-vn",
        "-ac",
        "1",
        "-ar",
        "8000",
        "-f",
        "s16le",
        "pipe:1",
    ]
    process = subprocess.Popen(command, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, start_new_session=True)
    assert process.stdout is not None
    selector = selectors.DefaultSelector()
    selector.register(process.stdout, selectors.EVENT_READ)
    events: list[dict[str, Any]] = []
    history: deque[float] = deque(maxlen=25)
    frame_bytes = 3200  # 200 ms at 8 kHz, signed 16-bit mono.
    sample_index = 0
    silence_start: int | None = None
    previous_transient: dict[str, Any] | None = None
    try:
        while True:
            chunk = _read_pipe_chunk(selector, process.stdout.fileno(), frame_bytes, cancel_check)
            if not chunk:
                break
            usable = len(chunk) - (len(chunk) % 2)
            if usable <= 0:
                continue
            samples = array.array("h")
            samples.frombytes(chunk[:usable])
            if not samples:
                continue
            square_mean = sum(value * value for value in samples) / len(samples)
            rms = math.sqrt(square_mean) / 32768.0
            peak = max(abs(value) for value in samples) / 32768.0
            start_ms = int(round(sample_index * 1000 / 8000))
            sample_index += len(samples)
            end_ms = min(duration_ms, int(round(sample_index * 1000 / 8000)))
            baseline = statistics.median(history) if history else 0.0
            history.append(rms)

            if rms < 0.008:
                silence_start = start_ms if silence_start is None else silence_start
            elif silence_start is not None:
                if start_ms - silence_start >= 800:
                    events.append({"kind": "silence", "start_ms": silence_start, "end_ms": start_ms, "threshold_rms": 0.008})
                silence_start = None

            transient = rms >= max(0.075, baseline * 3.5) and peak >= max(0.15, baseline * 4.0)
            if transient and end_ms > start_ms:
                overlaps_speech = any(
                    segment.start_ms < end_ms and segment.end_ms > start_ms for segment in transcript_segments
                )
                if (
                    previous_transient
                    and start_ms - previous_transient["end_ms"] <= 100
                    and previous_transient["overlaps_speech"] == overlaps_speech
                ):
                    previous_transient["end_ms"] = end_ms
                    previous_transient["peak"] = max(previous_transient["peak"], round(peak, 4))
                    previous_transient["rms"] = max(previous_transient["rms"], round(rms, 4))
                    previous_transient["overlaps_speech"] = bool(previous_transient["overlaps_speech"] or overlaps_speech)
                else:
                    previous_transient = {
                        "kind": "audio_energy_transient",
                        "start_ms": start_ms,
                        "end_ms": end_ms,
                        "peak": round(peak, 4),
                        "rms": round(rms, 4),
                        "overlaps_speech": overlaps_speech,
                    }
                    events.append(previous_transient)
                    if not overlaps_speech:
                        events.append({
                            "kind": "non_speech_audio_transient",
                            "start_ms": start_ms,
                            "end_ms": end_ms,
                            "detector": "energy_outside_transcript_timing",
                            "peak": round(peak, 4),
                            "rms": round(rms, 4),
                            "semantic_label": None,
                        })
                if len(events) > MAX_AUDIO_EVENTS:
                    raise AnalysisError("audio event count exceeds the supported bound")
            elif previous_transient and start_ms - previous_transient["end_ms"] > 300:
                previous_transient = None

        if silence_start is not None and duration_ms - silence_start >= 800:
            events.append({"kind": "silence", "start_ms": silence_start, "end_ms": duration_ms, "threshold_rms": 0.008})
        cancel_check()
        return_code = process.wait()
        if return_code:
            raise AnalysisError("audio scan failed")
        return {
            "events": _mark_repeated_pulses(events, transcript_segments),
            "covered_duration_ms": min(duration_ms, sample_index * 1000 // 8000),
        }
    except Exception:
        _kill_process_group(process)
        raise
    finally:
        selector.close()
        process.stdout.close()
        if process.poll() is None:
            process.wait()


def _mark_repeated_pulses(events: list[dict[str, Any]], segments: list[TranscriptSegment]) -> list[dict[str, Any]]:
    pulses = [item for item in events if item.get("kind") == "non_speech_audio_transient"]
    if len(pulses) < 3:
        return events
    group: list[dict[str, Any]] = [pulses[0]]
    for pulse in pulses[1:]:
        gap = pulse["start_ms"] - group[-1]["end_ms"]
        if 80 <= gap <= 700:
            group.append(pulse)
        else:
            _append_pulse_pattern(events, group, segments)
            group = [pulse]
    _append_pulse_pattern(events, group, segments)
    events.sort(key=lambda item: (item.get("start_ms", 0), item.get("kind", "")))
    return events


def _append_pulse_pattern(events: list[dict[str, Any]], group: list[dict[str, Any]], segments: list[TranscriptSegment]) -> None:
    if len(group) < 3:
        return
    start, end = group[0]["start_ms"], group[-1]["end_ms"]
    if any(segment.start_ms < end and segment.end_ms > start for segment in segments):
        return
    events.append({
        "kind": "repeated_non_speech_pulses",
        "start_ms": start,
        "end_ms": end,
        "detector": "repeated_energy_transients_without_transcript_overlap",
        "pulse_count": len(group),
        "acoustic_pattern": "laughter_like_pulses",
        "semantic_label": None,
        "confidence_kind": "uncalibrated_signal_heuristic",
    })


def scan_visual_events(
    media_path: str | Path,
    duration_ms: int,
    *,
    cancel_check: Callable[[], None] = lambda: None,
    ffmpeg: str = "ffmpeg",
) -> dict[str, Any]:
    """Scan all frames for cuts and low-resolution frame-to-frame motion."""

    events = _scan_scene_changes(media_path, duration_ms, cancel_check=cancel_check, ffmpeg=ffmpeg)
    events.extend(_scan_motion(media_path, duration_ms, cancel_check=cancel_check, ffmpeg=ffmpeg))
    events = _bounded_events(events, duration_ms, MAX_VISUAL_EVENTS)
    events.sort(key=lambda item: (item["start_ms"], item["kind"]))
    return {"events": events, "covered_duration_ms": duration_ms}


def _scan_scene_changes(media_path: str | Path, duration_ms: int, *, cancel_check: Callable[[], None], ffmpeg: str) -> list[dict[str, Any]]:
    command = [
        ffmpeg,
        "-nostdin",
        "-hide_banner",
        "-loglevel",
        "info",
        "-stats_period",
        "2",
        "-progress",
        "pipe:2",
        "-protocol_whitelist",
        MEDIA_PROTOCOL_WHITELIST,
        "-format_whitelist",
        MEDIA_FORMAT_WHITELIST,
        "-i",
        str(media_path),
        "-an",
        "-vf",
        "select='gt(scene,0.30)',showinfo",
        "-f",
        "null",
        "-",
    ]
    process = subprocess.Popen(command, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, start_new_session=True)
    assert process.stderr is not None
    selector = selectors.DefaultSelector()
    selector.register(process.stderr, selectors.EVENT_READ)
    events: list[dict[str, Any]] = []
    previous_ms = -2000
    pending = bytearray()

    def consume_line(raw_line: bytes) -> None:
        nonlocal previous_ms
        match = re.search(rb"pts_time:([0-9]+(?:\.[0-9]+)?)", raw_line)
        if not match:
            return
        start_ms = int(float(match.group(1)) * 1000)
        if start_ms >= duration_ms or start_ms - previous_ms < 1000:
            return
        events.append({
            "kind": "scene_change",
            "start_ms": start_ms,
            "end_ms": min(duration_ms, start_ms + 1),
            "detector": "ffmpeg_scene_score_threshold_0_30",
            "visual_evidence_strength": "weak",
        })
        previous_ms = start_ms
        if len(events) > MAX_VISUAL_EVENTS:
            raise AnalysisError("scene event count exceeds the supported bound")

    try:
        eof = False
        while not eof:
            cancel_check()
            if not selector.select(0.25):
                continue
            chunk = os.read(process.stderr.fileno(), 4096)
            if not chunk:
                eof = True
            else:
                pending.extend(chunk)
                while True:
                    newline = pending.find(b"\n")
                    if newline < 0:
                        break
                    consume_line(bytes(pending[:newline]))
                    del pending[: newline + 1]
                if len(pending) > 64 * 1024:
                    raise AnalysisError("scene scanner output line exceeds its size bound")
        if pending:
            consume_line(bytes(pending))
        cancel_check()
        code = process.wait()
        if code:
            raise AnalysisError("scene scan failed")
        return events
    except Exception:
        _kill_process_group(process)
        raise
    finally:
        selector.close()
        process.stderr.close()
        if process.poll() is None:
            process.wait()


def _scan_motion(media_path: str | Path, duration_ms: int, *, cancel_check: Callable[[], None], ffmpeg: str) -> list[dict[str, Any]]:
    width, height = 64, 36
    frame_bytes = width * height
    command = [
        ffmpeg,
        "-nostdin",
        "-hide_banner",
        "-loglevel",
        "error",
        "-protocol_whitelist",
        MEDIA_PROTOCOL_WHITELIST,
        "-format_whitelist",
        MEDIA_FORMAT_WHITELIST,
        "-i",
        str(media_path),
        "-an",
        "-vf",
        f"fps=1,scale={width}:{height}:flags=bilinear,format=gray",
        "-f",
        "rawvideo",
        "-pix_fmt",
        "gray",
        "pipe:1",
    ]
    process = subprocess.Popen(command, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, start_new_session=True)
    assert process.stdout is not None
    selector = selectors.DefaultSelector()
    selector.register(process.stdout, selectors.EVENT_READ)
    events: list[dict[str, Any]] = []
    previous: bytes | None = None
    frame_index = 0
    last_event_ms = -5000
    try:
        while True:
            frame = _read_exact_frame(selector, process.stdout.fileno(), frame_bytes, cancel_check)
            if frame is None:
                break
            if previous is not None:
                change = sum(abs(left - right) for left, right in zip(frame, previous)) / frame_bytes
                start_ms = frame_index * 1000
                if change >= 22.0 and start_ms - last_event_ms >= 3_000:
                    events.append({
                        "kind": "motion_change",
                        "start_ms": min(duration_ms, start_ms),
                        "end_ms": min(duration_ms, start_ms + 1000),
                        "detector": "mean_absolute_gray_frame_difference",
                        "mean_absolute_difference": round(change, 2),
                        "visual_evidence_strength": "weak",
                    })
                    last_event_ms = start_ms
                    if len(events) > MAX_VISUAL_EVENTS:
                        raise AnalysisError("motion event count exceeds the supported bound")
            previous = frame
            frame_index += 1
        cancel_check()
        code = process.wait()
        if code:
            raise AnalysisError("motion scan failed")
        return events
    except Exception:
        _kill_process_group(process)
        raise
    finally:
        selector.close()
        process.stdout.close()
        if process.poll() is None:
            process.wait()


def _bounded_events(raw_events: Any, duration_ms: int, limit: int) -> list[dict[str, Any]]:
    if not isinstance(raw_events, list):
        raise AnalysisError("event list is invalid")
    if len(raw_events) > limit:
        raise AnalysisError("event list exceeds the supported bound")
    events: list[dict[str, Any]] = []
    for raw in raw_events:
        if not isinstance(raw, Mapping):
            continue
        start, end = raw.get("start_ms"), raw.get("end_ms")
        kind = raw.get("kind")
        if (
            isinstance(start, bool)
            or isinstance(end, bool)
            or not isinstance(start, int)
            or not isinstance(end, int)
            or not isinstance(kind, str)
            or start < 0
            or end <= start
            or end > duration_ms
        ):
            raise AnalysisError("event timestamp is outside source bounds")
        event = dict(raw)
        events.append(event)
    events.sort(key=lambda item: (item["start_ms"], item["kind"]))
    return events


def _kill_process_group(process: Any) -> None:
    """Stop FFmpeg and any descendants promptly when cancellation fires."""

    try:
        os.killpg(process.pid, signal.SIGKILL)
    except ProcessLookupError:
        pass
    except OSError:
        try:
            process.kill()
        except OSError:
            pass


def _field(value: Any, name: str, default: Any) -> Any:
    if isinstance(value, Mapping):
        return value.get(name, default)
    return getattr(value, name, default)


def _content_tokens(text: str) -> list[str]:
    return [token.casefold() for token in _WORD.findall(text) if len(token) > 2 and token.casefold() not in _STOP_WORDS]


def _inverse_frequency(count: int, segment_count: int) -> float:
    return math.log1p(max(0, segment_count) / max(1, count))


def _excerpt(text: str, max_chars: int) -> str:
    text = " ".join(text.split())
    return text if len(text) <= max_chars else text[: max_chars - 1].rstrip() + "…"


def _script_metadata(text: str) -> dict[str, Any]:
    counts: Counter[str] = Counter()
    for char in text:
        if char.isspace() or not char.isalpha():
            continue
        try:
            name = unicodedata.name(char)
        except ValueError:
            counts["other"] += 1
            continue
        if "LATIN" in name:
            counts["latin"] += 1
        elif "DEVANAGARI" in name:
            counts["devanagari"] += 1
        else:
            counts["other"] += 1
    active = [name for name, count in counts.items() if count]
    return {
        "name": active[0] if len(active) == 1 else "mixed" if active else "unknown",
        "character_counts": dict(sorted(counts.items())),
        "method": "unicode-script-count",
    }


def _safe_int(value: Any, default: int) -> int:
    if isinstance(value, bool) or not isinstance(value, int):
        return default
    return value
