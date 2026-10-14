#!/usr/bin/env python3
"""Check the pinned yt-dlp metadata contract with synthetic InfoJSON only."""

from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import tempfile
import threading
from pathlib import Path


VIDEO_ID = "abcdefghijk"
CANONICAL_URL = f"https://www.youtube.com/watch?v={VIDEO_ID}"
MEDIA_URLS = {
    "mp4_1080": "https://r1---runtime-check.googlevideo.com/videoplayback?sig=synthetic-mp4-1080",
    "mp4_720": "https://r1---runtime-check.googlevideo.com/videoplayback?sig=synthetic-mp4-720",
    "webm_1440": "https://r1---runtime-check.googlevideo.com/videoplayback?sig=synthetic-webm-1440",
    "video_original": "https://r1---runtime-check.googlevideo.com/videoplayback?sig=synthetic-video-original",
    "audio_original": "https://r1---runtime-check.googlevideo.com/videoplayback?sig=synthetic-audio-original",
    "audio_autodub": "https://r1---runtime-check.googlevideo.com/videoplayback?sig=synthetic-audio-autodub",
}
USER_AGENT = "SyntheticRuntimeCheck/1.0"
EXPECTED_HTTP_HEADERS = {
    "Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
    "Accept-Language": "en-us,en;q=0.5",
    "Referer": CANONICAL_URL,
    "Sec-Fetch-Mode": "navigate",
    "User-Agent": USER_AGENT,
}
RUNTIME_SPEC = "deno:/usr/bin/deno"
MAX_METADATA_BYTES = 4 << 20
PRODUCTION_ARG_SHAPE = [
    "--ignore-config",
    "--no-plugin-dirs",
    "--no-cache-dir",
    "--no-cookies",
    "--no-playlist",
    "--no-warnings",
    "--no-progress",
    "--quiet",
    "--skip-download",
    "--no-simulate",
    "--dump-single-json",
    "--no-remote-components",
    "--js-runtimes",
    "runtimeSpec",
    "canonicalURL.String()",
]
EXPECTED = {
    "yt-dlp": ("2026.8.19", "1d57897e94c6665a0a6f9bc54b34e584284e32c034ffab3a7df25d8f7b24eedf"),
    "yt-dlp-ejs": ("0.8.0", "79300e5fca7f937a1eeede11f0456862c1b41107ce1d726871e0207424f4bdb4"),
}
EXPECTED_DENO_APK = "2.7.4-r2"
EXPECTED_DENO_APK_COMMIT = "bec8b026686323b496365b825ad14fdf4473adf2"
SCRIPT_PATH = Path(__file__).resolve()
REPO_ROOT = SCRIPT_PATH.parents[2]
REPO_LOCK = REPO_ROOT / "tools/clipping/requirements-youtube.lock"
IMAGE_LOCK = SCRIPT_PATH.with_name("youtube-runtime.lock")
LOCK_PATH = IMAGE_LOCK if IMAGE_LOCK.exists() else REPO_LOCK
DOCKERFILE = REPO_ROOT / "Dockerfile"


def check_pins() -> None:
    lock_text = LOCK_PATH.read_text(encoding="utf-8")
    logical_text = lock_text.replace("\\\n", " ")
    for package, (version, digest) in EXPECTED.items():
        pin = rf"(?m)^{re.escape(package)}=={re.escape(version)}\s+--hash=sha256:{digest}$"
        if not re.search(pin, logical_text) or logical_text.count(f"{package}==") != 1:
            raise SystemExit(f"{package} version/hash does not match the runtime pin")
    if f"deno={EXPECTED_DENO_APK}" not in lock_text:
        raise SystemExit("runtime lock does not record the Alpine Deno package revision")
    # Alpine's official package page and signed APKINDEX do not publish a whole-APK SHA-256.
    # Keep the evidence precise: C authenticates the control gzip stream; datahash covers the payload tarball.
    for evidence in (
        f"build commit {EXPECTED_DENO_APK_COMMIT}",
        "does not publish a whole-APK SHA-256",
        "signed APKINDEX",
        "C field is SHA-1 of the control gzip stream",
        "That authenticated control metadata contains .PKGINFO datahash, a SHA-256 of the payload tarball",
        "https://pkgs.alpinelinux.org/package/v3.24/community/x86_64/deno",
        "https://wiki.alpinelinux.org/wiki/Apk_spec",
    ):
        if evidence not in lock_text:
            raise SystemExit(f"runtime lock is missing precise Alpine Deno integrity provenance: {evidence}")
    if re.search(r"(?im)^.*deno[^\n]*--hash=sha256:", lock_text):
        raise SystemExit("do not invent a whole-APK SHA-256 absent from Alpine's package page/index")

    contract_text = SCRIPT_PATH.read_text(encoding="utf-8")
    for option in (
        '"--no-plugin-dirs"',
        '"--no-cookies"',
        '"--skip-download"',
        '"--no-simulate"',
        '"--dump-single-json"',
        '"--no-remote-components"',
        '"--js-runtimes"',
        '"deno:/usr/bin/deno"',
        '"--load-info-json"',
    ):
        if option not in contract_text:
            raise SystemExit(f"synthetic CLI contract is missing expected argument {option}")
    if "--format" in PRODUCTION_ARG_SHAPE or "clippingYouTubeMuxedFormat" in PRODUCTION_ARG_SHAPE:
        raise SystemExit("synthetic CLI contract must request full metadata without a format selector")
    if re.search(r'(?m)^\s+"--no-netrc",\s*$', contract_text):
        raise SystemExit("yt-dlp 2026.8.19 does not support --no-netrc; rely on config isolation and omit --netrc")

    if DOCKERFILE.is_file():
        dockerfile = DOCKERFILE.read_text(encoding="utf-8")
        required = (
            "COPY tools/clipping/requirements-youtube.lock /usr/local/share/video-automation/youtube-runtime.lock",
            "COPY tools/clipping/check_youtube_runtime_contract.py /usr/local/share/video-automation/check_youtube_runtime_contract.py",
            "RUN apk add --no-cache ffmpeg su-exec python3 py3-pip deno=2.7.4-r2",
            "--only-binary=:all: --require-hashes --target=/opt/yt-dlp",
            "chmod -R a+rX /opt/yt-dlp",
            "test -r /opt/yt-dlp/yt_dlp_ejs/yt/solver/core.min.js",
            "test ! -w /opt/yt-dlp",
            "test ! -w /opt/yt-dlp/yt_dlp/YoutubeDL.py",
            "test ! -w /opt/yt-dlp/yt_dlp_ejs/yt/solver/core.min.js",
            "test -x /usr/local/bin/yt-dlp",
            "test -x /usr/bin/deno",
            "test -w /tmp",
            'test "$(su-exec app:app /usr/local/bin/yt-dlp --version)" = "2026.08.19"',
            'DENO_DIR="$1" /usr/bin/deno info > "$1/info.txt" && grep -F "$1" "$1/info.txt" >/dev/null',
            "test \"$(command -v yt-dlp)\" = \"/usr/local/bin/yt-dlp\"",
            "test \"$(command -v deno)\" = \"/usr/bin/deno\"",
            "test \"$(yt-dlp --version)\" = \"2026.08.19\"",
            "yt-dlp --help | grep -q -- '--no-remote-components'",
            "yt-dlp --help | grep -q -- '--js-runtimes'",
            "deno --version | grep -q -E '^deno 2\\.7\\.4( |$)'",
            "su-exec app:app env HOME=/nonexistent PYTHONDONTWRITEBYTECODE=1 /usr/bin/python3 /usr/local/share/video-automation/check_youtube_runtime_contract.py",
        )
        for fragment in required:
            if fragment not in dockerfile:
                raise SystemExit(f"Dockerfile is missing the expected runtime pin/check: {fragment}")
        if "/nonexistent/.cache/deno" in dockerfile:
            raise SystemExit("Deno cache must use the per-import temporary DENO_DIR, not a persistent HOME cache")

    backend_source = REPO_ROOT / "internal/app/clipping_youtube.go"
    if backend_source.is_file():
        backend_text = backend_source.read_text(encoding="utf-8")
        runtime_match = re.search(r'(?m)^\s*clippingYouTubeRuntime\s*=\s*"([^"]+)"', backend_text)
        if runtime_match is None or runtime_match.group(1) != RUNTIME_SPEC:
            raise SystemExit("synthetic CLI runtime differs from the production backend runtime")
        args_match = re.search(r"args := \[\]string\{\s*(.*?)\n\s*\}", backend_text, re.DOTALL)
        if args_match is None:
            raise SystemExit("production backend yt-dlp argv could not be located")
        backend_args = []
        for item in args_match.group(1).split(","):
            item = item.strip()
            if not item:
                continue
            backend_args.append(json.loads(item) if item.startswith('"') else item)
        if backend_args != PRODUCTION_ARG_SHAPE:
            raise SystemExit("production yt-dlp argv differs from the bounded full-metadata offline contract")
        if "--format" in backend_args or "clippingYouTubeMuxedFormat" in backend_args:
            raise SystemExit("production yt-dlp argv must not constrain the metadata format list")


def media_format(
    name: str,
    url: str,
    *,
    ext: str,
    protocol: str = "https",
    vcodec: str = "none",
    acodec: str = "none",
    width: int | None = None,
    height: int | None = None,
    fps: float | None = None,
    tbr: float | None = None,
    quality: int | None = None,
    abr: float | None = None,
    asr: int | None = None,
    audio_channels: int | None = None,
    language: str | None = None,
    language_preference: int | None = None,
    format_note: str | None = None,
    filesize: int | None = None,
    filesize_approx: int | None = None,
) -> dict[str, object]:
    # Format IDs are synthetic labels here; the importer must select from metadata fields.
    item: dict[str, object] = {
        "format_id": name,
        "url": url,
        "ext": ext,
        "protocol": protocol,
        "vcodec": vcodec,
        "acodec": acodec,
        "http_headers": {
            "User-Agent": USER_AGENT,
            "Referer": CANONICAL_URL,
        },
    }
    for key, value in (
        ("width", width),
        ("height", height),
        ("fps", fps),
        ("tbr", tbr),
        ("quality", quality),
        ("abr", abr),
        ("asr", asr),
        ("audio_channels", audio_channels),
        ("language", language),
        ("language_preference", language_preference),
        ("format_note", format_note),
        ("filesize", filesize),
        ("filesize_approx", filesize_approx),
    ):
        if value is not None:
            item[key] = value
    return item


def make_fixture(*, separate_only: bool) -> dict[str, object]:
    formats = [
        media_format(
            "video-only", MEDIA_URLS["video_original"],
            ext="mp4", vcodec="avc1.640033", width=2560, height=1440,
            fps=30, tbr=5200, quality=9, filesize_approx=8_900_000,
        ),
        media_format(
            "audio-original-128k", MEDIA_URLS["audio_original"],
            ext="m4a", acodec="mp4a.40.2", tbr=160, abr=128,
            asr=48000, audio_channels=2, language="en",
            language_preference=10, format_note="Original", filesize_approx=540_000,
        ),
        media_format(
            "audio-autodub-192k", MEDIA_URLS["audio_autodub"],
            ext="m4a", acodec="mp4a.40.2", tbr=224, abr=192,
            asr=48000, audio_channels=2, language="en",
            language_preference=2, format_note="English (Auto-dubbed)", filesize=750_000,
        ),
        media_format(
            "high-quality-hls-muxed", "https://runtime-check.invalid/hls-manifest.m3u8",
            ext="mp4", protocol="m3u8_native", vcodec="avc1.640033",
            acodec="mp4a.40.2", width=3840, height=2160, fps=60,
            tbr=10500, quality=10, filesize=10_234_567,
        ),
        media_format(
            "high-quality-dash-muxed", "https://runtime-check.invalid/dash-manifest.mpd",
            ext="mp4", protocol="http_dash_segments", vcodec="avc1.640032",
            acodec="mp4a.40.2", width=2560, height=1440, fps=60,
            tbr=8500, quality=9, filesize=8_234_567,
        ),
    ]
    if not separate_only:
        formats.extend(
            [
                media_format(
                    "progressive-mp4-1080", MEDIA_URLS["mp4_1080"],
                    ext="mp4", vcodec="avc1.640028", acodec="mp4a.40.2",
                    width=1920, height=1080, fps=30, tbr=4100, quality=8,
                    abr=128, asr=48000, audio_channels=2, filesize=4_234_567,
                ),
                media_format(
                    "progressive-mp4-720", MEDIA_URLS["mp4_720"],
                    ext="mp4", vcodec="avc1.64001f", acodec="mp4a.40.2",
                    width=1280, height=720, fps=30, tbr=1800, quality=7,
                    abr=128, asr=48000, audio_channels=2, filesize=1_234_567,
                ),
                media_format(
                    "progressive-webm-1440", MEDIA_URLS["webm_1440"],
                    ext="webm", vcodec="vp9", acodec="opus",
                    width=2560, height=1440, fps=60, tbr=7100, quality=9,
                    abr=160, asr=48000, audio_channels=2, filesize=7_234_567,
                ),
            ]
        )
    return {
        "_type": "video",
        "id": VIDEO_ID,
        "title": "Local synthetic extractor fixture",
        "extractor": "youtube",
        "extractor_key": "Youtube",
        "webpage_url": CANONICAL_URL,
        "original_url": CANONICAL_URL,
        "duration": 73.5,
        "formats": formats,
    }


def run_fixture(fixture: dict[str, object]) -> dict[str, object]:
    with tempfile.TemporaryDirectory(prefix="youtube-runtime-contract-") as temp_dir:
        info_path = Path(temp_dir) / "synthetic.info.json"
        deno_dir = Path(temp_dir) / "deno-cache"
        deno_dir.mkdir(mode=0o700)
        info_path.write_text(json.dumps(fixture), encoding="utf-8")
        # Local InfoJSON replaces the URL; no media download, model call,
        # or external network connection is made.
        argv = ["yt-dlp"]
        for item in PRODUCTION_ARG_SHAPE:
            if item == "runtimeSpec":
                argv.append(RUNTIME_SPEC)
            elif item == "canonicalURL.String()":
                argv.extend(["--load-info-json", str(info_path)])
            else:
                argv.append(item)
        try:
            process = subprocess.Popen(
                argv,
                stdout=subprocess.PIPE,
                stderr=subprocess.DEVNULL,
                env={
                    "PATH": os.environ.get("PATH", os.defpath),
                    "HOME": "/nonexistent",
                    "DENO_DIR": str(deno_dir),
                    "LANG": "C.UTF-8",
                    "LC_ALL": "C.UTF-8",
                    "PYTHONDONTWRITEBYTECODE": "1",
                },
                shell=False,
            )
        except OSError:
            raise SystemExit("pinned yt-dlp could not be started for the offline synthetic CLI check") from None
        if process.stdout is None:
            process.kill()
            process.wait()
            raise SystemExit("pinned yt-dlp stdout could not be read for the offline synthetic CLI check")
        output_bytes = bytearray()
        output_overflow = threading.Event()
        output_read_failed = threading.Event()

        def collect_bounded_stdout() -> None:
            try:
                while True:
                    chunk = process.stdout.read(64 << 10)
                    if not chunk:
                        return
                    remaining = MAX_METADATA_BYTES + 1 - len(output_bytes)
                    output_bytes.extend(chunk[:remaining])
                    if len(output_bytes) > MAX_METADATA_BYTES:
                        output_overflow.set()
                        try:
                            process.kill()
                        except OSError:
                            pass
                        return
            except OSError:
                output_read_failed.set()

        reader = threading.Thread(target=collect_bounded_stdout, daemon=True)
        reader.start()
        try:
            return_code = process.wait(timeout=15)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait()
            reader.join(timeout=2)
            process.stdout.close()
            raise SystemExit("pinned yt-dlp exceeded the offline synthetic CLI time bound") from None
        reader.join(timeout=2)
        if reader.is_alive():
            process.kill()
            process.wait()
            process.stdout.close()
            raise SystemExit("pinned yt-dlp stdout reader exceeded the offline synthetic CLI time bound")
        process.stdout.close()
        if output_overflow.is_set():
            raise SystemExit("pinned yt-dlp synthetic metadata exceeded the production JSON bound")
        if output_read_failed.is_set():
            raise SystemExit("pinned yt-dlp synthetic metadata could not be read")
        if return_code != 0:
            raise SystemExit("pinned yt-dlp rejected the offline synthetic CLI contract")
        try:
            output_text = output_bytes.decode("utf-8")
            output = json.loads(output_text)
        except (UnicodeDecodeError, json.JSONDecodeError):
            raise SystemExit("pinned yt-dlp did not emit one valid UTF-8 JSON result") from None
    if not isinstance(output, dict):
        raise SystemExit("pinned yt-dlp result is not a metadata object")
    return output


def metadata_formats(output: dict[str, object]) -> list[dict[str, object]]:
    if output.get("id") != VIDEO_ID or output.get("duration") != 73.5:
        raise SystemExit("pinned yt-dlp did not preserve the synthetic video ID and duration")
    formats = output.get("formats")
    if not isinstance(formats, list) or not formats or any(not isinstance(item, dict) for item in formats):
        raise SystemExit("pinned yt-dlp did not return the full formats array")
    return formats


def has_synthetic_format(
    formats: list[dict[str, object]],
    *,
    url: str,
    ext: str,
    protocol: str,
    vcodec: str,
    acodec: str,
    width: int | None = None,
    height: int | None = None,
    fps: float | None = None,
    tbr: float | None = None,
    quality: int | None = None,
    abr: float | None = None,
    asr: int | None = None,
    audio_channels: int | None = None,
    language_preference: int | None = None,
    language: str | None = None,
    format_note: str | None = None,
    filesize: int | None = None,
    filesize_approx: int | None = None,
) -> bool:
    expected: dict[str, object] = {
        "url": url,
        "ext": ext,
        "protocol": protocol,
        "vcodec": vcodec,
        "acodec": acodec,
    }
    for key, value in (
        ("width", width),
        ("height", height),
        ("fps", fps),
        ("tbr", tbr),
        ("quality", quality),
        ("abr", abr),
        ("asr", asr),
        ("audio_channels", audio_channels),
        ("language_preference", language_preference),
        ("language", language),
        ("format_note", format_note),
        ("filesize", filesize),
        ("filesize_approx", filesize_approx),
    ):
        if value is not None:
            expected[key] = value
    return any(
        all(item.get(key) == value for key, value in expected.items())
        and item.get("http_headers") == EXPECTED_HTTP_HEADERS
        for item in formats
    )


def check_metadata_shape(output: dict[str, object], *, separate_only: bool) -> None:
    formats = metadata_formats(output)
    if not has_synthetic_format(
        formats,
        url=MEDIA_URLS["audio_original"], ext="m4a", protocol="https",
        vcodec="none", acodec="mp4a.40.2", abr=128, asr=48000,
        audio_channels=2, language_preference=10, language="en",
        format_note="Original", filesize_approx=540_000,
    ):
        raise SystemExit("pinned yt-dlp did not preserve the original-audio metadata fields")
    if not has_synthetic_format(
        formats,
        url=MEDIA_URLS["audio_autodub"], ext="m4a", protocol="https",
        vcodec="none", acodec="mp4a.40.2", abr=192, language_preference=2,
        language="en", format_note="English (Auto-dubbed)",
        filesize=750_000,
    ):
        raise SystemExit("pinned yt-dlp did not preserve the synthetic alternate-audio metadata")
    if not has_synthetic_format(
        formats,
        url="https://runtime-check.invalid/hls-manifest.m3u8", ext="mp4",
        protocol="m3u8_native", vcodec="avc1.640033", acodec="mp4a.40.2",
        height=2160, fps=60, tbr=10500, quality=10,
    ):
        raise SystemExit("pinned yt-dlp did not preserve protocol and quality metadata for a manifest candidate")
    if not has_synthetic_format(
        formats,
        url="https://runtime-check.invalid/dash-manifest.mpd", ext="mp4",
        protocol="http_dash_segments", vcodec="avc1.640032", acodec="mp4a.40.2",
        height=1440, fps=60, tbr=8500, quality=9,
    ):
        raise SystemExit("pinned yt-dlp did not preserve protocol and quality metadata for a second manifest candidate")
    if separate_only:
        if not has_synthetic_format(
            formats,
            url=MEDIA_URLS["video_original"], ext="mp4", protocol="https",
            vcodec="avc1.640033", acodec="none", width=2560, height=1440,
            fps=30, tbr=5200, quality=9, filesize_approx=8_900_000,
        ):
            raise SystemExit("pinned yt-dlp did not preserve the separate-video metadata fields")
        return
    if not has_synthetic_format(
        formats,
        url=MEDIA_URLS["mp4_1080"], ext="mp4", protocol="https",
        vcodec="avc1.640028", acodec="mp4a.40.2", width=1920, height=1080,
        fps=30, tbr=4100, quality=8, filesize=4_234_567,
    ):
        raise SystemExit("pinned yt-dlp did not preserve the higher-quality combined MP4 metadata")
    if not has_synthetic_format(
        formats,
        url=MEDIA_URLS["webm_1440"], ext="webm", protocol="https",
        vcodec="vp9", acodec="opus", width=2560, height=1440, fps=60,
        tbr=7100, quality=9, filesize=7_234_567,
    ):
        raise SystemExit("pinned yt-dlp did not preserve the combined WebM metadata")


def check_cli_contract() -> None:
    combined_output = run_fixture(make_fixture(separate_only=False))
    check_metadata_shape(combined_output, separate_only=False)
    split_output = run_fixture(make_fixture(separate_only=True))
    check_metadata_shape(split_output, separate_only=True)
    print("Pinned yt-dlp offline full-metadata contract passed")


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--static-only", action="store_true", help="check source pin/config consistency only")
    args = parser.parse_args()
    check_pins()
    if args.static_only:
        print("YouTube runtime pins and Docker install checks are consistent")
        return
    check_cli_contract()


if __name__ == "__main__":
    main()
