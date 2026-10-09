#!/usr/bin/env python3
"""Exercise the pinned yt-dlp CLI/output contract with synthetic local metadata only."""

from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import tempfile
from pathlib import Path


VIDEO_ID = "abcdefghijk"
MEDIA_URL = "https://r1---runtime-check.googlevideo.com/videoplayback?sig=synthetic"
REFERER_URL = "https://www.youtube.com/watch?v=abcdefghijk"
USER_AGENT = "SyntheticRuntimeCheck/1.0"
EXPECTED_HTTP_HEADERS = {
    "Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
    "Accept-Language": "en-us,en;q=0.5",
    "Referer": REFERER_URL,
    "Sec-Fetch-Mode": "navigate",
    "User-Agent": USER_AGENT,
}
FORMAT = "best[acodec!=none][vcodec!=none][protocol=https][ext=mp4]/best[acodec!=none][vcodec!=none][protocol=https][ext=webm]"
RUNTIME_SPEC = "deno:/usr/bin/deno"
SYNTHETIC_PROXY = "http://127.0.0.1:1"
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
    "--format",
    "clippingYouTubeMuxedFormat",
    "--no-remote-components",
    "--js-runtimes",
    "runtimeSpec",
    "--proxy",
    "proxy.AuthenticatedURL()",
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
        'FORMAT = "best[acodec!=none][vcodec!=none][protocol=https][ext=mp4]/best[acodec!=none][vcodec!=none][protocol=https][ext=webm]"',
        '"--format"',
        '"--no-remote-components"',
        '"--js-runtimes"',
        '"deno:/usr/bin/deno"',
        '"--proxy"',
        '"--load-info-json"',
    ):
        if option not in contract_text:
            raise SystemExit(f"synthetic CLI contract is missing expected argument {option}")
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
        match = re.search(r'(?m)^\s*clippingYouTubeMuxedFormat\s*=\s*"([^"]+)"', backend_text)
        if match is None or match.group(1) != FORMAT:
            raise SystemExit("synthetic CLI selector differs from the production backend selector")
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
            raise SystemExit(f"production yt-dlp argv differs from the offline contract: {backend_args!r}")


def make_fixture() -> dict[str, object]:
    return {
        "_type": "video",
        "id": VIDEO_ID,
        "title": "Local synthetic extractor fixture",
        "extractor": "youtube",
        "extractor_key": "Youtube",
        "webpage_url": "https://runtime-fixture.invalid/video",
        "original_url": "https://runtime-fixture.invalid/video",
        "duration": 73.5,
        "filesize": 1_234_567,
        "formats": [
            {
                "format_id": "video-only",
                "url": "https://runtime-fixture.invalid/video-only",
                "ext": "mp4",
                "protocol": "https",
                "width": 1280,
                "height": 720,
                "vcodec": "avc1.64001f",
                "acodec": "none",
                "filesize": 900_000,
            },
            {
                "format_id": "audio-only",
                "url": "https://runtime-fixture.invalid/audio-only",
                "ext": "m4a",
                "protocol": "https",
                "vcodec": "none",
                "acodec": "mp4a.40.2",
                "filesize": 200_000,
            },
            {
                "format_id": "high-quality-hls-muxed",
                "url": "https://runtime-fixture.invalid/hls-manifest.m3u8",
                "ext": "mp4",
                "protocol": "m3u8_native",
                "width": 3840,
                "height": 2160,
                "vcodec": "avc1.640033",
                "acodec": "mp4a.40.2",
                "filesize": 10_234_567,
                "tbr": 10_500.0,
            },
            {
                "format_id": "high-quality-dash-muxed",
                "url": "https://runtime-fixture.invalid/dash-manifest.mpd",
                "ext": "mp4",
                "protocol": "http_dash_segments",
                "width": 2560,
                "height": 1440,
                "vcodec": "avc1.640032",
                "acodec": "mp4a.40.2",
                "filesize": 8_234_567,
                "tbr": 8_500.0,
            },
            {
                "format_id": "progressive-mp4",
                "url": MEDIA_URL,
                "ext": "mp4",
                "protocol": "https",
                "width": 1280,
                "height": 720,
                "vcodec": "avc1.64001f",
                "acodec": "mp4a.40.2",
                "filesize": 1_234_567,
                "tbr": 1_500.0,
                "http_headers": {
                    "User-Agent": USER_AGENT,
                    "Referer": REFERER_URL,
                },
            },
            {
                "format_id": "progressive-webm",
                "url": "https://runtime-fixture.invalid/progressive.webm",
                "ext": "webm",
                "protocol": "https",
                "width": 640,
                "height": 360,
                "vcodec": "vp9",
                "acodec": "opus",
                "filesize": 534_567,
                "tbr": 700.0,
                "http_headers": {
                    "User-Agent": USER_AGENT,
                    "Referer": REFERER_URL,
                },
            },
        ],
    }


def run_fixture(fixture: dict[str, object]) -> dict[str, object]:
    with tempfile.TemporaryDirectory(prefix="youtube-runtime-contract-") as temp_dir:
        info_path = Path(temp_dir) / "synthetic.info.json"
        deno_dir = Path(temp_dir) / "deno-cache"
        deno_dir.mkdir(mode=0o700)
        info_path.write_text(json.dumps(fixture), encoding="utf-8")
        # Match the production extractor argv. The local InfoJSON replaces the URL and
        # the inert loopback proxy value is never contacted by this metadata-only check.
        argv = ["yt-dlp"]
        for item in PRODUCTION_ARG_SHAPE:
            if item == "clippingYouTubeMuxedFormat":
                argv.append(FORMAT)
            elif item == "runtimeSpec":
                argv.append(RUNTIME_SPEC)
            elif item == "proxy.AuthenticatedURL()":
                argv.append(SYNTHETIC_PROXY)
            elif item == "canonicalURL.String()":
                argv.extend(["--load-info-json", str(info_path)])
            else:
                argv.append(item)
        result = subprocess.run(
            argv,
            check=False,
            capture_output=True,
            text=True,
            env={
                "PATH": os.environ.get("PATH", os.defpath),
                "HOME": "/nonexistent",
                "DENO_DIR": str(deno_dir),
                "LANG": "C.UTF-8",
                "LC_ALL": "C.UTF-8",
                "PYTHONDONTWRITEBYTECODE": "1",
            },
            timeout=15,
            shell=False,
        )
        if result.returncode != 0:
            raise SystemExit("pinned yt-dlp rejected the offline synthetic CLI contract")
        try:
            output = json.loads(result.stdout)
        except json.JSONDecodeError as exc:
            raise SystemExit("pinned yt-dlp did not emit one JSON result") from exc
    return output


def assert_selection(
    output: dict[str, object],
    expected_id: str,
    expected_url: str,
    expected_format: str,
    expected_ext: str,
    expected_size: int,
) -> None:
    if output.get("id") != expected_id or output.get("duration") != 73.5 or output.get("filesize") != expected_size:
        raise SystemExit("pinned yt-dlp did not preserve the synthetic ID/duration/size metadata")
    requested = output.get("requested_downloads")
    if not isinstance(requested, list) or len(requested) != 1:
        raise SystemExit("pinned yt-dlp did not select exactly one requested format")
    selected = requested[0]
    selected_headers = selected.get("http_headers")
    if (
        selected.get("format_id") != expected_format
        or selected.get("url") != expected_url
        or selected.get("protocol") != "https"
        or selected.get("ext") != expected_ext
        or selected.get("acodec") in (None, "none")
        or selected.get("vcodec") in (None, "none")
        or output.get("url") != expected_url
        or selected_headers != EXPECTED_HTTP_HEADERS
    ):
        raise SystemExit(
            "pinned yt-dlp selected an unexpected synthetic format: "
            f"format_id={selected.get('format_id')!r}, protocol={selected.get('protocol')!r}, "
            f"ext={selected.get('ext')!r}, filesize={selected.get('filesize')!r}, "
            f"top_filesize={output.get('filesize')!r}, url={selected.get('url')!r}, "
            f"http_headers={selected_headers!r}"
        )


def check_cli_contract() -> None:
    fixture = make_fixture()
    assert_selection(run_fixture(fixture), VIDEO_ID, MEDIA_URL, "progressive-mp4", "mp4", 1_234_567)

    webm_fallback_fixture = {
        **fixture,
        "filesize": 534_567,
        "formats": [
            item for item in fixture["formats"]
            if isinstance(item, dict) and item.get("format_id") != "progressive-mp4"
        ],
    }
    assert_selection(
        run_fixture(webm_fallback_fixture),
        VIDEO_ID,
        "https://runtime-fixture.invalid/progressive.webm",
        "progressive-webm",
        "webm",
        534_567,
    )
    print("Pinned yt-dlp offline CLI/result contract passed")


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
