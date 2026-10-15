#!/usr/bin/env python3
"""Check pinned yt-dlp native-selection flags against synthetic local metadata."""

from __future__ import annotations

import importlib.metadata
import json
import os
import re
import shutil
import subprocess
import tempfile
import threading
from functools import partial
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


RUNTIME_SPEC = "deno:/usr/bin/deno"
MATCH_FILTER = "availability=public & duration <= 14400 & live_status~=^(not_live|was_live)$"
EXPECTED_PINS = {
    "yt-dlp": ("2026.8.19", "1d57897e94c6665a0a6f9bc54b34e584284e32c034ffab3a7df25d8f7b24eedf"),
    "yt-dlp-ejs": ("0.8.0", "79300e5fca7f937a1eeede11f0456862c1b41107ce1d726871e0207424f4bdb4"),
}
EXPECTED_DENO_APK = "2.7.4-r2"
EXPECTED_DENO_APK_COMMIT = "bec8b026686323b496365b825ad14fdf4473adf2"
VIDEO_ID = "abcdefghijk"
CANONICAL_URL = f"https://www.youtube.com/watch?v={VIDEO_ID}"
SCRIPT_PATH = Path(__file__).resolve()
SOURCE_ROOT = SCRIPT_PATH.parents[2]
SOURCE_LOCK = SOURCE_ROOT / "tools/clipping/requirements-youtube.lock"
INSTALLED_RUNTIME = not SOURCE_LOCK.is_file()
REPO_ROOT = None if INSTALLED_RUNTIME else SOURCE_ROOT
LOCK_PATH = SCRIPT_PATH.with_name("youtube-runtime.lock") if INSTALLED_RUNTIME else SOURCE_LOCK
DOCKERFILE = None if INSTALLED_RUNTIME else SOURCE_ROOT / "Dockerfile"
BACKEND_SOURCE = None if INSTALLED_RUNTIME else SOURCE_ROOT / "internal/app/clipping_youtube.go"


def check_pins() -> None:
    lock_text = LOCK_PATH.read_text(encoding="utf-8")
    logical_text = lock_text.replace("\\\n", " ")
    for package, (version, digest) in EXPECTED_PINS.items():
        pin = rf"(?m)^{re.escape(package)}=={re.escape(version)}\s+--hash=sha256:{digest}$"
        if not re.search(pin, logical_text) or logical_text.count(f"{package}==") != 1:
            raise SystemExit(f"{package} version/hash does not match the runtime pin")
    if f"deno={EXPECTED_DENO_APK}" not in lock_text:
        raise SystemExit("runtime lock does not record the Alpine Deno package revision")
    provenance = (
        f"build commit {EXPECTED_DENO_APK_COMMIT}",
        "does not publish a whole-APK SHA-256",
        "signed APKINDEX",
        "C field is SHA-1 of the control gzip stream",
        "That authenticated control metadata contains .PKGINFO datahash, a SHA-256 of the payload tarball",
        "https://pkgs.alpinelinux.org/package/v3.24/community/x86_64/deno",
        "https://wiki.alpinelinux.org/wiki/Apk_spec",
    )
    for evidence in provenance:
        if evidence not in lock_text:
            raise SystemExit(f"runtime lock is missing precise Alpine Deno provenance: {evidence}")
    if re.search(r"(?im)^.*deno[^\n]*--hash=sha256:", lock_text):
        raise SystemExit("do not invent a whole-APK SHA-256 absent from Alpine's package page/index")

    for package, (version, _) in EXPECTED_PINS.items():
        try:
            installed = importlib.metadata.version(package)
        except importlib.metadata.PackageNotFoundError:
            raise SystemExit(f"pinned runtime package {package} is not installed") from None
        if installed != version:
            raise SystemExit(f"installed {package} version {installed} does not match {version}")

    if INSTALLED_RUNTIME:
        return
    dockerfile = DOCKERFILE.read_text(encoding="utf-8")
    required_docker = (
        "COPY tools/clipping/requirements-youtube.lock /usr/local/share/video-automation/youtube-runtime.lock",
        "COPY tools/clipping/check_youtube_runtime_contract.py /usr/local/share/video-automation/check_youtube_runtime_contract.py",
        "RUN apk add --no-cache ffmpeg su-exec python3 py3-pip deno=2.7.4-r2",
        "--only-binary=:all: --require-hashes --target=/opt/yt-dlp",
        "test -r /opt/yt-dlp/yt_dlp_ejs/yt/solver/core.min.js",
        "test ! -w /opt/yt-dlp",
        "test -x /usr/local/bin/yt-dlp",
        "test -x /usr/bin/deno",
        "test -w /tmp",
        'test "$(su-exec app:app /usr/local/bin/yt-dlp --version)" = "2026.08.19"',
        'test "$(command -v yt-dlp)" = "/usr/local/bin/yt-dlp"',
        'test "$(command -v deno)" = "/usr/bin/deno"',
        'test "$(yt-dlp --version)" = "2026.08.19"',
        "yt-dlp --help | grep -q -- '--no-remote-components'",
        "yt-dlp --help | grep -q -- '--js-runtimes'",
        "yt-dlp --help | grep -q -- '--match-filter'",
        "yt-dlp --help | grep -q -- '--max-filesize'",
        "yt-dlp --help | grep -q -- '--merge-output-format'",
        "yt-dlp --help | grep -q -- '--remux-video'",
        "deno --version | grep -q -E '^deno 2\\.7\\.4( |$)'",
        "PYTHONPATH=/opt/yt-dlp",
        "/usr/local/share/video-automation/check_youtube_runtime_contract.py",
    )
    for fragment in required_docker:
        if fragment not in dockerfile:
            raise SystemExit(f"Dockerfile is missing the expected runtime pin/check: {fragment}")
    if "/nonexistent/.cache/deno" in dockerfile:
        raise SystemExit("Deno cache must use the private temporary DENO_DIR")


def check_backend_contract() -> None:
    if INSTALLED_RUNTIME:
        return
    text = BACKEND_SOURCE.read_text(encoding="utf-8")
    runtime = re.search(r'(?m)^\s*clippingYouTubeRuntime\s*=\s*"([^"]+)"', text)
    if runtime is None or runtime.group(1) != RUNTIME_SPEC:
        raise SystemExit("backend JavaScript runtime differs from the pinned Deno runtime")
    match = re.search(
        r"func youtubeDownloaderArgs\([^\n]+\) \[\]string \{(?P<body>.*?)\n\}",
        text,
        re.DOTALL,
    )
    if match is None:
        raise SystemExit("native yt-dlp argv function could not be located")
    body = match.group("body")
    required = (
        '"--ignore-config"',
        '"--no-plugin-dirs"',
        '"--no-cache-dir"',
        '"--no-cookies"',
        '"--no-playlist"',
        '"--no-simulate"',
        '"--no-remote-components"',
        '"--js-runtimes"',
        '"--match-filter"',
        '"--max-filesize"',
        '"--merge-output-format", "mp4"',
        '"--remux-video", "mp4"',
        '"source.%(ext)s"',
        '"--print"',
    )
    for fragment in required:
        if fragment not in body:
            raise SystemExit(f"backend native yt-dlp argv is missing {fragment}")
    if re.search(r'"--format"\s*,', body) or '"--proxy"' in body or '"--cookies"' in body:
        raise SystemExit("backend must use yt-dlp's native selection and ambient transport configuration")
    for invariant in (
        "command.Stderr = io.Discard",
        "configureYouTubeSubprocess(command)",
        "clippingYouTubeFileLimitLauncher",
        "youtubeWorkspaceUsage(workDir)",
        "--no-cookies",
        "--no-playlist",
    ):
        if invariant not in text:
            raise SystemExit(f"backend is missing downloader safety invariant: {invariant}")
    for obsolete in ("youtubeProxy", "startYouTubeProxy", "extractYouTubeMedia", "importYouTubeSeparateTracks"):
        if obsolete in text:
            raise SystemExit(f"obsolete application downloader pipeline remains: {obsolete}")


def make_fixture() -> dict[str, object]:
    return {
        "_type": "video",
        "id": VIDEO_ID,
        "title": "Local synthetic native-selection fixture",
        "extractor": "youtube",
        "extractor_key": "Youtube",
        "webpage_url": CANONICAL_URL,
        "original_url": CANONICAL_URL,
        "duration": 73.5,
        "availability": "public",
        "live_status": "not_live",
        "is_live": False,
        "formats": [
            {
                "format_id": "video-original",
                "url": "https://runtime-check.invalid/video?sig=synthetic-video",
                "ext": "mp4",
                "protocol": "https",
                "vcodec": "avc1.64001f",
                "acodec": "none",
                "width": 1280,
                "height": 720,
                "fps": 30,
                "tbr": 2400,
                "quality": 5,
                "filesize": 3_000_000,
            },
            {
                "format_id": "audio-original",
                "url": "https://runtime-check.invalid/audio?sig=synthetic-original",
                "ext": "m4a",
                "protocol": "https",
                "vcodec": "none",
                "acodec": "mp4a.40.2",
                "abr": 128,
                "asr": 48000,
                "audio_channels": 2,
                "language": "en",
                "language_preference": 10,
                "format_note": "Original",
                "filesize": 500_000,
            },
            {
                "format_id": "audio-autodub-higher-bitrate",
                "url": "https://runtime-check.invalid/audio?sig=synthetic-dub",
                "ext": "m4a",
                "protocol": "https",
                "vcodec": "none",
                "acodec": "mp4a.40.2",
                "abr": 192,
                "asr": 48000,
                "audio_channels": 2,
                "language": "en",
                "language_preference": 2,
                "format_note": "English (Auto-dubbed)",
                "filesize": 700_000,
            },
        ],
    }


def run_offline_native_selection() -> None:
    executable = shutil.which("yt-dlp")
    if executable is None:
        raise SystemExit("pinned yt-dlp executable was not found on PATH")
    try:
        version = subprocess.run(
            [executable, "--version"],
            check=True,
            capture_output=True,
            text=True,
            timeout=10,
            shell=False,
        ).stdout.strip()
    except (OSError, subprocess.SubprocessError):
        raise SystemExit("pinned yt-dlp version could not be read") from None
    if version != "2026.08.19":
        raise SystemExit(f"yt-dlp CLI version {version} does not match the runtime pin")

    with tempfile.TemporaryDirectory(prefix="youtube-runtime-contract-") as temporary:
        root = Path(temporary)
        fixture_path = root / "synthetic.info.json"
        deno_dir = root / "deno-cache"
        deno_dir.mkdir(mode=0o700)
        fixture_path.write_text(json.dumps(make_fixture()), encoding="utf-8")
        args = [
            executable,
            "--ignore-config",
            "--no-plugin-dirs",
            "--no-cache-dir",
            "--no-cookies",
            "--no-playlist",
            "--no-warnings",
            "--no-progress",
            "--quiet",
            "--simulate",
            "--no-remote-components",
            "--js-runtimes",
            RUNTIME_SPEC,
            "--match-filter",
            MATCH_FILTER,
            "--max-filesize",
            "10000000",
            "--merge-output-format",
            "mp4",
            "--remux-video",
            "mp4",
            "--paths",
            f"temp:{root}",
            "--output",
            str(root / "source.%(ext)s"),
            "--load-info-json",
            str(fixture_path),
            "--print",
            "%(format_id)s",
        ]
        environment = dict(os.environ)
        environment.update(
            {
                "HOME": "/nonexistent",
                "DENO_DIR": str(deno_dir),
                "PYTHONDONTWRITEBYTECODE": "1",
            }
        )
        try:
            process = subprocess.run(
                args,
                capture_output=True,
                text=True,
                timeout=15,
                check=False,
                shell=False,
                env=environment,
            )
        except (OSError, subprocess.SubprocessError):
            raise SystemExit("pinned yt-dlp offline InfoJSON selection check failed to run") from None
    if process.returncode != 0:
        raise SystemExit("pinned yt-dlp rejected the synthetic offline InfoJSON fixture")
    selected = process.stdout.strip().splitlines()
    if selected != ["video-original+audio-original"]:
        raise SystemExit(f"native yt-dlp did not prefer the original audio format: {selected!r}")
    if "synthetic-dub" in process.stdout or "runtime-check.invalid" in process.stdout:
        raise SystemExit("offline selector output unexpectedly exposed synthetic media URLs")


def run_local_native_merge() -> None:
    ffmpeg = shutil.which("ffmpeg")
    ffprobe = shutil.which("ffprobe")
    deno = Path("/usr/bin/deno")
    if ffmpeg is None or ffprobe is None or INSTALLED_RUNTIME and not deno.is_file():
        if INSTALLED_RUNTIME:
            raise SystemExit("installed runtime is missing FFmpeg, ffprobe, or Deno")
        print("Skipping local native merge check: FFmpeg or ffprobe is unavailable")
        return

    with tempfile.TemporaryDirectory(prefix="youtube-runtime-merge-") as temporary:
        root = Path(temporary)
        web_root = root / "media"
        web_root.mkdir(mode=0o700)
        work_dir = root / "download"
        work_dir.mkdir(mode=0o700)
        video_path = web_root / "video.mp4"
        audio_path = web_root / "audio.m4a"
        try:
            subprocess.run(
                [
                    ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i",
                    "color=c=black:s=320x240:d=1", "-an", "-c:v", "mpeg4", "-pix_fmt",
                    "yuv420p", "-movflags", "+faststart", "-y", str(video_path),
                ],
                check=True, capture_output=True, timeout=20, shell=False,
            )
            subprocess.run(
                [
                    ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i",
                    "sine=frequency=1000:duration=1", "-vn", "-c:a", "aac", "-y", str(audio_path),
                ],
                check=True, capture_output=True, timeout=20, shell=False,
            )
        except (OSError, subprocess.SubprocessError):
            raise SystemExit("local FFmpeg media fixtures could not be generated") from None

        server = ThreadingHTTPServer(
            ("127.0.0.1", 0),
            partial(SimpleHTTPRequestHandler, directory=str(web_root)),
        )
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        fixture = make_fixture()
        formats = fixture["formats"]
        assert isinstance(formats, list)
        video_format, audio_format = formats[0], formats[1]
        video_format.update(
            {
                "url": f"http://127.0.0.1:{server.server_port}/video.mp4",
                "protocol": "http",
                "filesize": video_path.stat().st_size,
            }
        )
        audio_format.update(
            {
                "url": f"http://127.0.0.1:{server.server_port}/audio.m4a",
                "protocol": "http",
                "filesize": audio_path.stat().st_size,
            }
        )
        fixture_path = root / "local.info.json"
        fixture_path.write_text(json.dumps(fixture), encoding="utf-8")
        deno_dir = root / "deno-cache"
        deno_dir.mkdir(mode=0o700)
        args = [
            shutil.which("yt-dlp") or "yt-dlp",
            "--ignore-config", "--no-plugin-dirs", "--no-cache-dir", "--no-cookies",
            "--no-playlist", "--no-warnings", "--no-progress", "--quiet", "--no-simulate",
            "--no-remote-components", "--js-runtimes", RUNTIME_SPEC, "--match-filter", MATCH_FILTER,
            "--max-filesize", "10000000", "--merge-output-format", "mp4", "--remux-video", "mp4",
            "--paths", f"temp:{work_dir}", "--output", str(work_dir / "source.%(ext)s"),
            "--load-info-json", str(fixture_path),
            "--print", "before_dl:__FRAMEVAULT_FORMAT__%(format_id)s",
            "--print", "after_move:__FRAMEVAULT_OUTPUT__%(filepath)s",
        ]
        environment = dict(os.environ)
        environment.update(
            {
                "HOME": "/nonexistent",
                "DENO_DIR": str(deno_dir),
                "PYTHONDONTWRITEBYTECODE": "1",
            }
        )
        for name in ("NO_PROXY", "no_proxy"):
            local_exclusions = ["127.0.0.1", "localhost"]
            inherited = environment.get(name, "")
            if inherited:
                local_exclusions.extend(part.strip() for part in inherited.split(",") if part.strip())
            environment[name] = ",".join(dict.fromkeys(local_exclusions))
        try:
            process = subprocess.run(
                args, capture_output=True, text=True, timeout=30, check=False, shell=False, env=environment,
            )
        except (OSError, subprocess.SubprocessError):
            server.shutdown()
            server.server_close()
            raise SystemExit("pinned yt-dlp local merge check failed to run") from None
        finally:
            server.shutdown()
            server.server_close()
            thread.join(timeout=2)

        if process.returncode != 0:
            raise SystemExit("pinned yt-dlp failed to download and merge synthetic local media")
        output = work_dir / "source.mp4"
        if not output.is_file() or output.stat().st_size <= 0:
            raise SystemExit("yt-dlp did not produce the fixed MP4 output path")
        lines = process.stdout.strip().splitlines()
        expected_markers = [
            "__FRAMEVAULT_FORMAT__video-original+audio-original",
            "__FRAMEVAULT_OUTPUT__" + str(output),
        ]
        if lines != expected_markers:
            raise SystemExit(f"yt-dlp output markers differ from the importer contract: {lines!r}")
        try:
            probe = subprocess.run(
                [ffprobe, "-v", "error", "-show_entries", "format=format_name", "-show_entries", "stream=codec_type", "-of", "json", str(output)],
                check=True, capture_output=True, text=True, timeout=10, shell=False,
            )
            media = json.loads(probe.stdout)
        except (OSError, subprocess.SubprocessError, json.JSONDecodeError):
            raise SystemExit("ffprobe could not validate the native yt-dlp MP4 result") from None
        format_name = str(media.get("format", {}).get("format_name", ""))
        stream_types = {stream.get("codec_type") for stream in media.get("streams", [])}
        if "mp4" not in format_name.split(",") or not {"video", "audio"}.issubset(stream_types):
            raise SystemExit("native yt-dlp output is not an MP4 with video and audio")


def main() -> None:
    check_pins()
    check_backend_contract()
    run_offline_native_selection()
    run_local_native_merge()
    print("Pinned yt-dlp offline native-selection contract passed")


if __name__ == "__main__":
    main()
