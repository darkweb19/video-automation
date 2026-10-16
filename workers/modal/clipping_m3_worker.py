"""Modal worker for FrameVault M3 whole-source clipping analysis.

The public ASGI endpoint authenticates an operator dispatch and enqueues the
analysis function. The worker downloads scoped media to one private temporary
file, verifies its source hash, performs bounded analysis, and posts a
canonical callback with the separate callback capability. No source media is
written to the Modal volume; only the protocol's bounded replay ledger is.
"""

from __future__ import annotations

import hashlib
import hmac
import http.client
import ipaddress
import json
import logging
import math
import os
import selectors
import re
import signal
import socket
import ssl
import tempfile
import threading
import time
from pathlib import Path
from typing import Any, Callable, Mapping
from urllib import error as urllib_error
from urllib import request as urllib_request

try:  # Works both as a Modal script and as a local test module.
    from .clipping_analysis import (
        MEDIA_FORMAT_WHITELIST,
        MEDIA_PROTOCOL_WHITELIST,
        AnalysisError,
        WorkCancelled,
        _configured_model_revision,
        analyze_source,
        prewarm_faster_whisper_snapshot,
    )
    from .clipping_protocol import (
        MAX_ARTIFACT_BYTES,
        MAX_CALLBACK_OVERHEAD_BYTES,
        MAX_COMPUTE_SECONDS,
        MAX_SOURCE_BYTES,
        PROTOCOL_VERSION,
        SUPPORTED_MODEL,
        DispatchLedger,
        DuplicateAttempt,
        ProtocolError,
        canonical_callback,
        SQLiteDispatchLedger,
        StageDispatch,
        StageResult,
        WorkerUnavailable,
        callback_headers,
        canonical_failure_callback,
        execute_stage,
        media_headers,
        trace_worker,
    )
except ImportError:  # pragma: no cover - used by `modal deploy workers/modal/...`.
    from clipping_analysis import (
        MEDIA_FORMAT_WHITELIST,
        MEDIA_PROTOCOL_WHITELIST,
        AnalysisError,
        WorkCancelled,
        _configured_model_revision,
        analyze_source,
        prewarm_faster_whisper_snapshot,
    )
    from clipping_protocol import (
        MAX_ARTIFACT_BYTES,
        MAX_CALLBACK_OVERHEAD_BYTES,
        MAX_COMPUTE_SECONDS,
        MAX_SOURCE_BYTES,
        PROTOCOL_VERSION,
        SUPPORTED_MODEL,
        DispatchLedger,
        DuplicateAttempt,
        ProtocolError,
        canonical_callback,
        SQLiteDispatchLedger,
        StageDispatch,
        StageResult,
        WorkerUnavailable,
        callback_headers,
        canonical_failure_callback,
        execute_stage,
        media_headers,
        trace_worker,
    )


APP_NAME = "framevault-clipping-analysis"
DISPATCH_API_SECRET_NAME = "framevault-clipping-dispatch-api-secret"
RUNTIME_SECRET_NAME = "framevault-clipping-runtime-secret"
MODEL_PREWARM_SECRET_NAME = "framevault-clipping-model-prewarm-secret"
WORKER_SECRET_KEY = "FRAMEVAULT_CLIPPING_WORKER_SHARED_SECRET"
WORKER_ORIGIN_KEY = "FRAMEVAULT_CALLBACK_ORIGIN"
WORKER_PIPELINE_REVISION_KEY = "FRAMEVAULT_PIPELINE_REVISION"
WORKER_MODEL_REVISION_KEY = "FRAMEVAULT_WHISPER_MODEL_REVISION"
MODEL_VOLUME_NAME = "framevault-clipping-model-cache"
LEDGER_VOLUME_NAME = "framevault-clipping-worker-ledger"
MODEL_CACHE_DIR = "/model-cache"
LEDGER_DIR = "/worker-state"
LEDGER_PATH = f"{LEDGER_DIR}/clipping-dispatches.sqlite3"
# Keep the analysis image pins in sync with requirements-clipping.txt. Modal
# imports this module inside deployed containers from /root, where the source
# tree is intentionally flattened and repository-relative paths do not exist.
CLIPPING_IMAGE_PACKAGES = (
    "modal==1.3.1",
    "fastapi==0.115.12",
    "faster-whisper==1.1.1",
    "ctranslate2==4.6.0",
    "av==14.2.0",
    "numpy==1.26.4",
    "huggingface-hub==0.30.2",
    "tokenizers==0.21.1",
)
# CTranslate2 4.6 requires CUDA 12 and cuDNN 9 for GPU transcription.
GPU_CUDA_IMAGE = "nvidia/cuda:12.3.2-cudnn9-runtime-ubuntu22.04"
MAX_DISPATCH_BODY_BYTES = 64 * 1024
MAX_CALLBACK_BODY_BYTES = MAX_ARTIFACT_BYTES + MAX_CALLBACK_OVERHEAD_BYTES
MAX_MEDIA_STREAM_START_SKEW_MS = 50
DOWNLOAD_CHUNK_BYTES = 1024 * 1024
DOWNLOAD_TIMEOUT_SECONDS = 30
HTTP_TIMEOUT_SECONDS = 15
MEDIA_PROBE_RETRY_DELAYS_SECONDS = (0.0, 0.25, 0.5)
CALLBACK_DELAYS_SECONDS = (0, 1, 2, 4, 8)
COMPUTE_COST_BASIS = "operator_declared_worker_second_rate_estimate"
_SHA256 = re.compile(r"^[a-f0-9]{64}$")
_LOGGER = logging.getLogger("framevault.clipping")
# Emit existing INFO phase markers in Modal without changing global SDK logging.
_LOGGER.setLevel(logging.INFO)
_GLOBAL_IPV6_UNICAST = ipaddress.ip_network("2000::/3")
_SPECIAL_IP_NETWORKS = tuple(ipaddress.ip_network(network) for network in (
    "0.0.0.0/8",
    "100.64.0.0/10",
    "192.0.0.0/24",
    "192.0.2.0/24",
    "192.88.99.0/24",
    "198.18.0.0/15",
    "198.51.100.0/24",
    "203.0.113.0/24",
    "224.0.0.0/4",
    "240.0.0.0/4",
    "64:ff9b::/96",
    "64:ff9b:1::/48",
    "100::/64",
    "2001::/23",
    "2001:db8::/32",
    "2002::/16",
    "3fff::/20",
))


class ComputeDeadlineExceeded(WorkCancelled):
    """The dispatch compute budget ended before source analysis completed."""


class MediaProbeCancelled(WorkCancelled):
    """Only allow fixed, non-sensitive reason codes and HTTP status in worker logs."""

    def __init__(self, reason_code: str, http_status: int | None = None):
        super().__init__(reason_code)
        self.reason_code = reason_code
        self.http_status = http_status


def _media_probe_http_category(status: int) -> tuple[str, bool]:
    """Return safe reason and whether this response merits a short retry."""
    if status in (401, 403):
        return "media_capability_rejected", False
    if status == 404:
        return "media_source_not_found", False
    if status == 410:
        return "media_attempt_expired_or_removed", False
    if status in (408, 429) or 500 <= status <= 599:
        return "media_probe_transient_http", True
    return "media_probe_unexpected_http", False


class NoRedirectHandler(urllib_request.HTTPRedirectHandler):
    """Never forward a scoped bearer capability to a redirect target."""

    def redirect_request(self, req: Any, fp: Any, code: int, msg: str, headers: Any, newurl: str) -> None:
        return None


class PinnedConnectionError(OSError):
    """DNS did not resolve exclusively to a safe public address."""


class PinnedHTTPSConnection(http.client.HTTPSConnection):
    """HTTPS connection pinned to a validated IP while retaining host TLS."""

    def __init__(self, host: str, *, pinned_ip: str, **kwargs: Any):
        super().__init__(host, **kwargs)
        self._pinned_ip = pinned_ip

    def connect(self) -> None:
        if self._tunnel_host:
            raise PinnedConnectionError("HTTPS tunnels are disabled for capability requests")
        sock = socket.create_connection((self._pinned_ip, self.port), self.timeout, self.source_address)
        try:
            self.sock = self._context.wrap_socket(sock, server_hostname=self.host)
        except Exception:
            sock.close()
            raise


class PinnedHTTPSHandler(urllib_request.HTTPSHandler):
    """Resolve once, reject unsafe DNS sets, then connect to one pinned IP."""

    def __init__(self, resolver: Callable[..., Any] | None = None):
        context = ssl.create_default_context()
        super().__init__(context=context, check_hostname=True)
        self._resolver = resolver or socket.getaddrinfo

    def _public_addresses(self, hostname: str, port: int) -> list[str]:
        try:
            answers = self._resolver(hostname, port, type=socket.SOCK_STREAM)
        except Exception as error:
            raise PinnedConnectionError("trusted origin DNS lookup failed") from error
        if not isinstance(answers, (list, tuple)) or not answers or len(answers) > 64:
            raise PinnedConnectionError("trusted origin DNS answer set is invalid")
        addresses: set[str] = set()
        for answer in answers:
            if not isinstance(answer, (list, tuple)) or len(answer) < 5:
                raise PinnedConnectionError("trusted origin DNS answer is malformed")
            family, _socktype, _proto, _canonname, sockaddr = answer[:5]
            if family not in (socket.AF_INET, socket.AF_INET6) or not isinstance(sockaddr, (list, tuple)) or not sockaddr:
                raise PinnedConnectionError("trusted origin returned a non-IP DNS answer")
            try:
                parsed = ipaddress.ip_address(sockaddr[0])
            except (TypeError, ValueError) as error:
                raise PinnedConnectionError("trusted origin returned an invalid IP address") from error
            if not _is_global_unicast(parsed):
                raise PinnedConnectionError("trusted origin DNS answer is not global unicast")
            addresses.add(str(parsed))
        if not addresses:
            raise PinnedConnectionError("trusted origin has no global unicast address")
        return sorted(addresses, key=lambda address: (ipaddress.ip_address(address).version, int(ipaddress.ip_address(address))))

    def https_open(self, req: Any) -> Any:
        from urllib.parse import urlsplit

        try:
            parsed = urlsplit(req.full_url)
            port = parsed.port or 443
        except ValueError as error:
            raise PinnedConnectionError("trusted origin URL is invalid") from error
        if parsed.scheme != "https" or not parsed.hostname or port != 443:
            raise PinnedConnectionError("capability requests require HTTPS on port 443")
        pinned_ip = self._public_addresses(parsed.hostname, port)[0]

        def pinned_connection(host: str, **kwargs: Any) -> PinnedHTTPSConnection:
            return PinnedHTTPSConnection(host, pinned_ip=pinned_ip, context=self._context, **kwargs)

        return self.do_open(pinned_connection, req)


def _is_global_unicast(address: ipaddress.IPv4Address | ipaddress.IPv6Address) -> bool:
    if (
        not address.is_global
        or address.is_multicast
        or address.is_unspecified
        or address.is_loopback
        or address.is_link_local
        or address.is_private
        or address.is_reserved
    ):
        return False
    if address.version == 6 and address not in _GLOBAL_IPV6_UNICAST:
        return False
    return not any(address.version == network.version and address in network for network in _SPECIAL_IP_NETWORKS)


def _opener(resolver: Callable[..., Any] | None = None) -> Any:
    # An explicit empty ProxyHandler prevents urllib from honoring ambient
    # HTTP(S)_PROXY variables that could otherwise see bearer capabilities.
    return urllib_request.build_opener(
        urllib_request.ProxyHandler({}),
        NoRedirectHandler(),
        PinnedHTTPSHandler(resolver=resolver),
    )


class DispatchBodyTooLarge(ValueError):
    """The dispatch body exceeded its streaming byte cap."""


async def read_limited_body(chunks: Any, declared_content_length: str | None = None) -> bytes:
    """Read an ASGI body incrementally and enforce the cap before extending."""

    if declared_content_length is not None:
        if not declared_content_length.isascii() or not declared_content_length.isdecimal():
            raise ValueError("invalid content length")
        if int(declared_content_length) > MAX_DISPATCH_BODY_BYTES:
            raise DispatchBodyTooLarge("dispatch exceeds size limit")
    body = bytearray()
    async for block in chunks:
        if not isinstance(block, (bytes, bytearray, memoryview)):
            raise ValueError("dispatch stream returned a non-byte block")
        if len(block) > MAX_DISPATCH_BODY_BYTES - len(body):
            raise DispatchBodyTooLarge("dispatch exceeds size limit")
        body.extend(block)
    return bytes(body)


class WorkGuard:
    """Check lease, compute deadline and the live scoped media capability."""

    def __init__(
        self,
        dispatch: StageDispatch,
        media_token: str,
        *,
        wall_clock: Callable[[], float] = time.time,
        monotonic: Callable[[], float] = time.monotonic,
        active_probe: Callable[[StageDispatch, str], None] | None = None,
        probe_interval_seconds: float = 15.0,
    ):
        self.dispatch = dispatch
        self.media_token = media_token
        self.wall_clock = wall_clock
        self.monotonic = monotonic
        self.started = monotonic()
        self._default_active_probe = active_probe is None
        self.active_probe = active_probe or probe_media_capability
        self.probe_interval_seconds = probe_interval_seconds
        self.last_probe: float | None = None
        self.probe_count = 0

    def __call__(self) -> None:
        self._check_deadline()
        if self.last_probe is None or self.monotonic() - self.last_probe >= self.probe_interval_seconds:
            if self.probe_count == 0:
                trace_worker("media_probe_first_begin", job=self.dispatch.job_id, attempt=self.dispatch.attempt_id)
            try:
                if self._default_active_probe:
                    remaining = min(
                        self.dispatch.expires_at - self.wall_clock(),
                        self.dispatch.callback_expires_at - self.wall_clock(),
                        self.dispatch.max_compute_seconds - (self.monotonic() - self.started),
                    )
                    timeout = max(0.01, min(float(HTTP_TIMEOUT_SECONDS), remaining))
                    self.active_probe(self.dispatch, self.media_token, timeout=timeout)
                else:
                    self.active_probe(self.dispatch, self.media_token)
            except MediaProbeCancelled as error:
                trace_worker("media_probe_failure", job=self.dispatch.job_id, attempt=self.dispatch.attempt_id,
                             reason=error.reason_code, http_status=error.http_status or 0)
                _safe_log_failure("clipping_media_probe_rejected", self.dispatch, error)
                raise
            self._check_deadline()
            self.last_probe = self.monotonic()
            self.probe_count += 1
            if self.probe_count == 1 or self.probe_count % 4 == 0:
                trace_worker("media_probe_ok", job=self.dispatch.job_id, attempt=self.dispatch.attempt_id,
                             checks=self.probe_count, elapsed_ms=int((self.monotonic()-self.started)*1000))
                _LOGGER.info(
                    "clipping_media_probe_ok job=%s attempt=%s checks=%d elapsed_ms=%d",
                    self.dispatch.job_id, self.dispatch.attempt_id, self.probe_count,
                    int((self.monotonic() - self.started) * 1000),
                )

    def _check_deadline(self) -> None:
        if self.wall_clock() >= self.dispatch.expires_at:
            raise WorkCancelled("dispatch lease expired")
        if self.wall_clock() >= self.dispatch.callback_expires_at:
            raise WorkCancelled("callback capability expired")
        if self.monotonic() - self.started >= self.dispatch.max_compute_seconds:
            raise ComputeDeadlineExceeded("dispatch compute deadline expired")

    def compute_seconds(self) -> int:
        self()
        elapsed = max(0.0, self.monotonic() - self.started)
        seconds = max(1, math.ceil(elapsed))
        if seconds > self.dispatch.max_compute_seconds:
            raise ComputeDeadlineExceeded("dispatch compute deadline expired")
        return seconds


def create_private_temp_file(directory: str | Path | None = None) -> Path:
    """Create a random source-media file with owner-only permissions."""

    fd, name = tempfile.mkstemp(prefix="framevault-clipping-", suffix=".media", dir=directory)
    try:
        os.fchmod(fd, 0o600)
    finally:
        os.close(fd)
    return Path(name)


def download_media(
    dispatch: StageDispatch,
    media_token: str,
    destination: str | Path,
    *,
    cancel_check: Callable[[], None] = lambda: None,
    opener_factory: Callable[[], Any] = _opener,
    timeout: int = DOWNLOAD_TIMEOUT_SECONDS,
    now: Callable[[], float] = time.time,
) -> str:
    """Stream scoped source media to disk under the size, expiry and hash bounds."""

    headers = media_headers(media_token)
    request = urllib_request.Request(dispatch.media_url, headers=headers, method="GET")
    trace_worker("media_download_connect_begin", job=dispatch.job_id, attempt=dispatch.attempt_id)
    hasher = hashlib.sha256()
    written = 0
    try:
        with opener_factory().open(request, timeout=timeout) as response:
            status = response.getcode()
            if status != 200:
                trace_worker("media_download_bad_status", job=dispatch.job_id, attempt=dispatch.attempt_id,
                             http_status=status if isinstance(status,int) else 0)
                raise AnalysisError("scoped media endpoint did not return a complete source")
            trace_worker("media_download_http_ok", job=dispatch.job_id, attempt=dispatch.attempt_id, http_status=200)
            content_length = response.headers.get("Content-Length")
            if content_length:
                try:
                    declared = int(content_length)
                except (TypeError, ValueError) as error:
                    raise AnalysisError("media content length is invalid") from error
                if declared < 0 or declared > MAX_SOURCE_BYTES:
                    raise AnalysisError("source exceeds the 20 GiB worker limit")
            last_progress = time.monotonic()
            next_progress_bytes = 64 * 1024 * 1024
            with Path(destination).open("wb") as output:
                os.chmod(destination, 0o600)
                while True:
                    if int(now()) >= min(dispatch.expires_at, dispatch.callback_expires_at):
                        raise WorkCancelled("dispatch capability expired during source download")
                    cancel_check()
                    block = response.read(DOWNLOAD_CHUNK_BYTES)
                    if not block:
                        break
                    written += len(block)
                    if written >= next_progress_bytes or time.monotonic() - last_progress >= 30:
                        trace_worker("media_download_progress", job=dispatch.job_id, attempt=dispatch.attempt_id, bytes=written)
                        next_progress_bytes = written + 64 * 1024 * 1024
                        last_progress = time.monotonic()
                    if written > MAX_SOURCE_BYTES:
                        raise AnalysisError("source exceeds the 20 GiB worker limit")
                    output.write(block)
                    hasher.update(block)
                output.flush()
                os.fsync(output.fileno())
        if int(now()) >= min(dispatch.expires_at, dispatch.callback_expires_at):
            raise WorkCancelled("dispatch capability expired after source download")
        if written == 0:
            raise AnalysisError("source media is empty")
        trace_worker("media_download_bytes_complete", job=dispatch.job_id, attempt=dispatch.attempt_id, bytes=written)
        digest = hasher.hexdigest()
        if not hmac.compare_digest(digest, dispatch.source_sha256):
            raise AnalysisError("source SHA-256 does not match the dispatch")
        trace_worker("media_download_hash_verified", job=dispatch.job_id, attempt=dispatch.attempt_id)
        return digest
    except (AnalysisError, WorkCancelled):
        raise
    except Exception as error:
        # urllib exception strings contain signed URLs; never pass them on.
        raise AnalysisError("scoped media download failed") from error


def probe_media_capability(
    dispatch: StageDispatch,
    media_token: str,
    *,
    opener_factory: Callable[[], Any] = _opener,
    timeout: float = HTTP_TIMEOUT_SECONDS,
    sleep: Callable[[float], None] = time.sleep,
    monotonic: Callable[[], float] = time.monotonic,
) -> None:
    """Validate a one-byte media range; retry only transient faults within one deadline.

    Permanent authorization/absence failures stop immediately. Never log a
    bearer, URL, exception message, or response body from this capability call.
    """

    headers = media_headers(media_token)
    headers["Range"] = "bytes=0-0"
    request = urllib_request.Request(dispatch.media_url, headers=headers, method="GET")
    deadline = monotonic() + max(0.0, float(timeout))
    for attempt, delay in enumerate(MEDIA_PROBE_RETRY_DELAYS_SECONDS):
        if delay:
            remaining = deadline - monotonic()
            if remaining <= 0:
                raise MediaProbeCancelled("media_probe_timeout") from None
            sleep(min(delay, remaining))
        remaining = deadline - monotonic()
        if remaining <= 0:
            raise MediaProbeCancelled("media_probe_timeout") from None
        try:
            with opener_factory().open(request, timeout=remaining) as response:
                status = response.getcode()
                if status in (200, 206):
                    response.read(1)
                    return
        except urllib_error.HTTPError as error:
            status = error.code
            error.close()
        except PinnedConnectionError:
            # Invalid DNS/address sets are policy failures, not retryable outages.
            raise MediaProbeCancelled("media_probe_dns_validation_failed") from None
        except ssl.SSLError:
            raise MediaProbeCancelled("media_probe_tls_failed") from None
        except (urllib_error.URLError, OSError, TimeoutError):
            if attempt + 1 == len(MEDIA_PROBE_RETRY_DELAYS_SECONDS):
                raise MediaProbeCancelled("media_probe_transport_failed") from None
            continue
        except Exception:
            raise MediaProbeCancelled("media_probe_unexpected_failure") from None
        # HTTPError and non-success responses are handled identically.
        reason_code, retryable = _media_probe_http_category(status)
        if not retryable or attempt + 1 == len(MEDIA_PROBE_RETRY_DELAYS_SECONDS):
            raise MediaProbeCancelled(reason_code, status) from None
    raise MediaProbeCancelled("media_probe_transport_failed") from None


def verify_media_file(
    media_path: str | Path,
    expected_duration_ms: int,
    *,
    ffprobe: str = "ffprobe",
    timeout: int = 45,
    cancel_check: Callable[[], None] = lambda: None,
) -> dict[str, Any]:
    """Probe bounded media metadata and require audio/video starts within 50ms.

    faster-whisper and PCM event times use audio sample zero while visual event
    timestamps follow video PTS. Reject larger stream offsets rather than
    silently mixing those source-time domains.
    """

    cancel_check()
    command = [
        ffprobe,
        "-v",
        "error",
        "-show_entries",
        "format=duration:stream=codec_type,width,height,start_time",
        "-of",
        "json",
        "-protocol_whitelist",
        MEDIA_PROTOCOL_WHITELIST,
        "-format_whitelist",
        MEDIA_FORMAT_WHITELIST,
        str(media_path),
    ]
    try:
        completed = _run_bounded_process(
            command,
            timeout=timeout,
            output_limit=256 * 1024,
            cancel_check=cancel_check,
        )
    except (WorkCancelled, ComputeDeadlineExceeded):
        raise
    except Exception as error:
        raise AnalysisError("source media could not be probed") from error
    cancel_check()
    try:
        data = json.loads(completed)
        duration_seconds = float(data["format"]["duration"])
        streams = data.get("streams", [])
    except (KeyError, TypeError, ValueError, json.JSONDecodeError) as error:
        raise AnalysisError("source media probe returned invalid metadata") from error
    if not math.isfinite(duration_seconds) or duration_seconds <= 0:
        raise AnalysisError("source duration is invalid")
    probed_ms = int(round(duration_seconds * 1000))
    if abs(probed_ms - expected_duration_ms) > 1000:
        raise AnalysisError("source duration does not match the dispatch")
    if not isinstance(streams, list):
        raise AnalysisError("source media probe returned invalid stream metadata")
    video_streams = [stream for stream in streams if isinstance(stream, Mapping) and stream.get("codec_type") == "video"]
    audio_streams = [stream for stream in streams if isinstance(stream, Mapping) and stream.get("codec_type") == "audio"]
    if not video_streams:
        raise AnalysisError("source has no video stream")
    start_skew_ms: int | None = None
    if audio_streams:
        video_starts = [_stream_start_seconds(stream) for stream in video_streams]
        audio_starts = [_stream_start_seconds(stream) for stream in audio_streams]
        primary_video_start = video_starts[0]
        skew_values = [abs(start - primary_video_start) * 1000 for start in (*video_starts[1:], *audio_starts)]
        start_skew_ms = int(round(max(skew_values, default=0)))
        if start_skew_ms > MAX_MEDIA_STREAM_START_SKEW_MS:
            raise AnalysisError("audio and video stream starts differ by more than the 50ms alignment tolerance")
    return {
        "probed_duration_ms": probed_ms,
        "has_audio_stream": bool(audio_streams),
        "has_video_stream": True,
        "audio_video_start_skew_ms": start_skew_ms,
    }


def _stream_start_seconds(stream: Mapping[str, Any]) -> float:
    try:
        start_time = float(stream["start_time"])
    except (KeyError, TypeError, ValueError) as error:
        raise AnalysisError("audio/video stream start time is unavailable") from error
    if not math.isfinite(start_time):
        raise AnalysisError("audio/video stream start time is invalid")
    return start_time


def _run_bounded_process(
    command: list[str],
    *,
    timeout: int,
    output_limit: int,
    cancel_check: Callable[[], None] = lambda: None,
) -> bytes:
    import subprocess

    process = subprocess.Popen(command, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, start_new_session=True)
    assert process.stdout is not None
    selector = selectors.DefaultSelector()
    selector.register(process.stdout, selectors.EVENT_READ)
    output = bytearray()
    deadline = time.monotonic() + timeout
    success = False
    try:
        while True:
            cancel_check()
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise AnalysisError("media probe timed out")
            for key, _ in selector.select(min(0.5, remaining)):
                read_limit = min(64 * 1024, output_limit + 1 - len(output))
                if read_limit <= 0:
                    raise AnalysisError("media probe output exceeds its size bound")
                chunk = os.read(key.fd, read_limit)
                if not chunk:
                    selector.unregister(key.fileobj)
                    continue
                output.extend(chunk)
                if len(output) > output_limit:
                    raise AnalysisError("media probe output exceeds its size bound")
            return_code = process.poll()
            if return_code is not None and not selector.get_map():
                if return_code:
                    raise AnalysisError("media probe failed")
                success = True
                return bytes(output)
    finally:
        selector.close()
        process.stdout.close()
        if not success:
            _kill_process_group(process)
        process.wait()


def _kill_process_group(process: Any) -> None:
    """Stop ffprobe and inherited child processes when a work guard fires."""

    try:
        os.killpg(process.pid, signal.SIGKILL)
    except ProcessLookupError:
        pass
    except OSError:
        try:
            process.kill()
        except OSError:
            pass


def build_source_core_payload(
    analysis: Mapping[str, Any],
    *,
    compute_seconds: int,
    rate_micro_usd_per_second: int,
) -> dict[str, Any]:
    """Map internal analysis into the canonical schema-2 source-core payload."""

    if compute_seconds <= 0 or rate_micro_usd_per_second <= 0:
        raise ProtocolError("operator-rate cost estimate is unavailable")
    estimate = compute_seconds * rate_micro_usd_per_second
    if estimate <= 0 or estimate > (1 << 63) - 1:
        raise ProtocolError("operator-rate cost estimate exceeds int64 bounds")
    transcript_source = analysis.get("transcript")
    if not isinstance(transcript_source, Mapping):
        raise AnalysisError("transcript is missing from source analysis")
    transcript_segments = transcript_source.get("segments", [])
    segments = []
    for segment in transcript_segments:
        if not isinstance(segment, Mapping):
            continue
        segments.append({
            "start_ms": segment["start_ms"],
            "end_ms": segment["end_ms"],
            "text": segment["text"],
            # faster-whisper does not expose calibrated utterance confidence.
            "confidence": None,
            "speaker_id": segment.get("speaker_id"),
        })

    language = transcript_source.get("language")
    language = language if isinstance(language, str) and language else "und"
    script_meta = transcript_source.get("original_script")
    script_name = script_meta.get("name", "unknown") if isinstance(script_meta, Mapping) else "unknown"
    timeline_source = analysis.get("context_timeline", [])
    timeline: list[dict[str, Any]] = []
    topic_counts: dict[str, int] = {}
    for item in timeline_source if isinstance(timeline_source, list) else []:
        if not isinstance(item, Mapping):
            continue
        keywords = item.get("terms", [])
        keywords = [str(keyword) for keyword in keywords if isinstance(keyword, str)][:8]
        for keyword in keywords:
            topic_counts[keyword] = topic_counts.get(keyword, 0) + 1
        opening = str(item.get("opening_excerpt", ""))
        closing = str(item.get("closing_excerpt", ""))
        timeline.append({
            "start_ms": item["start_ms"],
            "end_ms": item["end_ms"],
            "summary": _context_excerpt(opening, closing),
            "keywords": keywords,
        })
    global_keywords = sorted(topic_counts, key=lambda keyword: (-topic_counts[keyword], keyword))[:24]
    duration = analysis.get("source_duration_ms")
    topics = [
        {
            "start_ms": 0,
            "end_ms": duration,
            "label": "Lexical terms: " + ", ".join(global_keywords[:5]),
            "keywords": global_keywords,
        }
    ] if global_keywords else []
    transcript_text = str(transcript_source.get("text", ""))
    summary = _context_excerpt(
        transcript_text[:240],
        transcript_text[-240:] if len(transcript_text) > 240 else "",
    )
    audio_events = [_event_contract(item, "audio") for item in analysis.get("audio_events", [])]
    visual_events = [_event_contract(item, "visual") for item in analysis.get("visual_events", [])]
    versions = analysis.get("analysis_versions", {})
    return {
        "analysis_version": "framevault.analysis.source.v1",
        "pipeline_revision": str(analysis.get("pipeline_revision", "unknown")),
        "source_sha256": analysis.get("source_sha256"),
        "source_duration_ms": duration,
        "model_versions": {
            "transcription": str(versions.get("transcriber", "unknown")),
            "model": str(versions.get("model", SUPPORTED_MODEL)),
            "language_mode": str(versions.get("language_mode", "auto")),
            "model_repository": str(versions.get("model_repository", "unknown")),
            "model_snapshot": str(versions.get("model_snapshot", "unknown")),
            "model_weights_sha256": str(versions.get("model_weights_sha256", "unknown")),
            "ctranslate2_version": str(versions.get("ctranslate2_version", "unknown")),
        },
        "algorithm_versions": {
            "audio_events": str(versions.get("audio_events", "unknown")),
            "visual_events": str(versions.get("visual_events", "unknown")),
            "context_timeline": str(versions.get("context_timeline", "unknown")),
            "candidate_inspection": str(versions.get("candidate_inspection", "unknown")),
            "ffmpeg_version": str(versions.get("ffmpeg_version", "unknown")),
            "ffmpeg_configuration_sha256": str(versions.get("ffmpeg_configuration_sha256", "unknown")),
            "ffmpeg_executable_sha256": str(versions.get("ffmpeg_executable_sha256", "unknown")),
            "python_distributions_sha256": str(versions.get("python_distributions_sha256", "unknown")),
            "python_distributions_count": str(versions.get("python_distributions_count", "unknown")),
            "os_release_sha256": str(versions.get("os_release_sha256", "unknown")),
            "runtime_manifest_sha256": str(versions.get("runtime_manifest_sha256", "unknown")),
            "pipeline_revision": str(analysis.get("pipeline_revision", "unknown")),
        },
        "runtime_versions": {
            "python": str(versions.get("python_version", "unknown")),
            "modal": str(versions.get("modal_version", "unknown")),
        },
        "prompt_versions": {"analysis": str(versions.get("prompt_version", "none-extractive-no-llm-v1"))},
        "language": {
            "code": language,
            "probability": transcript_source.get("language_probability"),
            "script": script_name,
        },
        "transcript": {
            "available": bool(transcript_source.get("available", True)),
            "status": str(transcript_source.get("status", "complete")),
            "original_script": True,
            "speaker_labels_available": bool(transcript_source.get("speaker_labels_available", False)),
            "segments": segments,
        },
        "context": {"summary": summary, "topics": topics, "timeline": timeline},
        "media_streams": analysis.get("media_streams", {"has_audio": True, "has_video": True}),
        "audio_analysis_status": (
            "complete" if analysis.get("media_streams", {}).get("has_audio", True)
            else "skipped_no_audio_stream"
        ),
        "audio_events": audio_events,
        "visual_events": visual_events,
        "candidate_inspections": analysis.get("candidate_inspections", []),
        "cost_basis": COMPUTE_COST_BASIS,
        "rate_micro_usd_per_second": rate_micro_usd_per_second,
        "compute_seconds": compute_seconds,
        "cost_estimate_micro_usd": estimate,
        "coverage": {
            "audio_scanned_duration_ms": _scan_coverage(analysis.get("audio_scan"), duration),
            "visual_scanned_duration_ms": _scan_coverage(analysis.get("visual_scan"), duration),
        },
    }


def _event_contract(event: Mapping[str, Any], modality: str) -> dict[str, Any]:
    kind = str(event.get("kind", "unclassified_signal"))
    evidence = {key: value for key, value in event.items() if key not in {"kind", "start_ms", "end_ms"}}
    if modality == "visual":
        detector_value = event.get("mean_absolute_difference")
        score = round(float(detector_value) / 255.0, 4) if isinstance(detector_value, (int, float)) else 0.3
        evidence["visual_evidence_strength"] = "weak"
    else:
        detector_value = event.get("rms", event.get("peak", event.get("threshold_rms")))
        score = round(float(detector_value), 4) if isinstance(detector_value, (int, float)) else None
        evidence.setdefault("semantic_label", None)
    return {
        "start_ms": event["start_ms"],
        "end_ms": event["end_ms"],
        "kind": kind,
        "score": score,
        "evidence": evidence,
    }


def _context_excerpt(opening: str, closing: str) -> str:
    opening = " ".join(opening.split())
    closing = " ".join(closing.split())
    if not opening:
        return closing[:480]
    if not closing or opening == closing:
        return opening[:480]
    return (opening[:235] + " … " + closing[:235])[:480]


def _scan_coverage(scan: Any, duration_ms: Any) -> int:
    if isinstance(scan, Mapping):
        value = scan.get("covered_duration_ms")
        if isinstance(value, int) and not isinstance(value, bool):
            return max(0, min(value, duration_ms if isinstance(duration_ms, int) else value))
    return 0


def analyze_dispatch(
    dispatch: StageDispatch,
    media_token: str,
    *,
    transcriber: Callable[..., Mapping[str, Any]] | None = None,
    audio_scanner: Callable[..., Mapping[str, Any]] | None = None,
    visual_scanner: Callable[..., Mapping[str, Any]] | None = None,
    candidate_inspector: Callable[..., Mapping[str, Any]] | None = None,
    runtime_manifest: Mapping[str, str] | None = None,
    active_probe: Callable[[StageDispatch, str], None] | None = None,
    temp_directory: str | Path | None = None,
    opener_factory: Callable[[], Any] = _opener,
    monotonic: Callable[[], float] = time.monotonic,
    wall_clock: Callable[[], float] = time.time,
    media_probe: Callable[..., Mapping[str, Any]] = verify_media_file,
) -> StageResult:
    """Download, hash, scan and transcribe one source inside a private temp file."""

    guard = WorkGuard(
        dispatch,
        media_token,
        wall_clock=wall_clock,
        monotonic=monotonic,
        active_probe=active_probe,
    )
    trace_worker("analysis_media_temp_created", job=dispatch.job_id, attempt=dispatch.attempt_id)
    source_path = create_private_temp_file(temp_directory)
    phase = "media_preflight"
    phase_started = time.monotonic()
    _LOGGER.info("clipping_worker_phase_start job=%s attempt=%s phase=%s",
                 dispatch.job_id, dispatch.attempt_id, phase)
    try:
        guard()
        trace_worker("analysis_preflight_success", job=dispatch.job_id, attempt=dispatch.attempt_id)
        _LOGGER.info("clipping_worker_phase_complete job=%s attempt=%s phase=%s elapsed_ms=%d",
                     dispatch.job_id, dispatch.attempt_id, phase,
                     int((time.monotonic() - phase_started) * 1000))
        phase = "source_download"
        phase_started = time.monotonic()
        _LOGGER.info("clipping_worker_phase_start job=%s attempt=%s phase=%s",
                     dispatch.job_id, dispatch.attempt_id, phase)
        trace_worker("analysis_download_begin", job=dispatch.job_id, attempt=dispatch.attempt_id)
        digest = download_media(
            dispatch,
            media_token,
            source_path,
            cancel_check=guard,
            opener_factory=opener_factory,
            now=wall_clock,
        )
        if not hmac.compare_digest(digest, dispatch.source_sha256):
            raise AnalysisError("source SHA-256 does not match the dispatch")
        _LOGGER.info("clipping_worker_phase_complete job=%s attempt=%s phase=%s elapsed_ms=%d bytes=%d",
                     dispatch.job_id, dispatch.attempt_id, phase,
                     int((time.monotonic() - phase_started) * 1000),
                     source_path.stat().st_size)
        trace_worker("analysis_download_verified", job=dispatch.job_id, attempt=dispatch.attempt_id)
        phase = "media_metadata_probe"
        phase_started = time.monotonic()
        _LOGGER.info("clipping_worker_phase_start job=%s attempt=%s phase=%s",
                     dispatch.job_id, dispatch.attempt_id, phase)
        trace_worker("analysis_ffprobe_begin", job=dispatch.job_id, attempt=dispatch.attempt_id)
        media_info = media_probe(source_path, dispatch.source_duration_ms, cancel_check=guard)
        if not isinstance(media_info, Mapping) or not isinstance(media_info.get("has_audio_stream"), bool):
            raise AnalysisError("media stream metadata is invalid")
        if media_info.get("has_video_stream") is not True:
            raise AnalysisError("source has no video stream")
        _LOGGER.info("clipping_worker_phase_complete job=%s attempt=%s phase=%s elapsed_ms=%d has_audio=%s",
                     dispatch.job_id, dispatch.attempt_id, phase,
                     int((time.monotonic() - phase_started) * 1000), media_info["has_audio_stream"])
        trace_worker("analysis_ffprobe_complete", job=dispatch.job_id, attempt=dispatch.attempt_id,
                     has_audio=media_info["has_audio_stream"])
        phase = "source_analysis"
        phase_started = time.monotonic()
        _LOGGER.info("clipping_worker_phase_start job=%s attempt=%s phase=%s",
                     dispatch.job_id, dispatch.attempt_id, phase)
        guard()
        trace_worker("analysis_pipeline_begin", job=dispatch.job_id, attempt=dispatch.attempt_id)
        analysis = analyze_source(
            source_path,
            source_duration_ms=dispatch.source_duration_ms,
            source_sha256=digest,
            model_name=dispatch.model,
            requested_content_type=dispatch.content_type,
            pipeline_revision=dispatch.pipeline_revision,
            min_clip_seconds=dispatch.min_clip_seconds,
            max_clip_seconds=dispatch.max_clip_seconds,
            candidate_limit=dispatch.candidate_limit,
            has_audio_stream=media_info["has_audio_stream"],
            transcriber=transcriber,
            audio_scanner=audio_scanner,
            visual_scanner=visual_scanner,
            candidate_inspector=candidate_inspector,
            runtime_manifest=runtime_manifest,
            cancel_check=guard,
        )
        _LOGGER.info("clipping_worker_phase_complete job=%s attempt=%s phase=%s elapsed_ms=%d",
                     dispatch.job_id, dispatch.attempt_id, phase,
                     int((time.monotonic() - phase_started) * 1000))
        trace_worker("analysis_pipeline_complete", job=dispatch.job_id, attempt=dispatch.attempt_id)
        phase = "result_serialization"
        phase_started = time.monotonic()
        _LOGGER.info("clipping_worker_phase_start job=%s attempt=%s phase=%s",
                     dispatch.job_id, dispatch.attempt_id, phase)
        compute_seconds = guard.compute_seconds()
        payload = build_source_core_payload(
            analysis,
            compute_seconds=compute_seconds,
            rate_micro_usd_per_second=dispatch.rate_micro_usd_per_second,
        )
        artifact = {
            "type": "clipping.analysis",
            "schema_version": "2",
            "version": 1,
            "source_duration_ms": dispatch.source_duration_ms,
            "time_ranges": [],
            "payload": payload,
        }
        _LOGGER.info("clipping_worker_phase_complete job=%s attempt=%s phase=%s elapsed_ms=%d compute_seconds=%d",
                     dispatch.job_id, dispatch.attempt_id, phase,
                     int((time.monotonic() - phase_started) * 1000), compute_seconds)
        trace_worker("analysis_result_ready", job=dispatch.job_id, attempt=dispatch.attempt_id, elapsed_ms=compute_seconds*1000)
        return StageResult(artifact, payload["cost_estimate_micro_usd"], COMPUTE_COST_BASIS)
    except Exception as error:
        trace_worker("analysis_phase_failure", job=dispatch.job_id, attempt=dispatch.attempt_id,
                     phase=phase, reason=type(error).__name__)
        _safe_log_failure("clipping_worker_phase_failed", dispatch, error, phase=phase)
        raise
    finally:
        try:
            source_path.unlink(missing_ok=True)
        except OSError:
            _LOGGER.warning("temporary_media_cleanup_failed")


def deliver_callback(
    callback_url: str,
    callback_token: str,
    payload: Mapping[str, Any],
    *,
    opener_factory: Callable[[], Any] = _opener,
    sleep: Callable[[float], None] = time.sleep,
    timeout: int = HTTP_TIMEOUT_SECONDS,
    callback_expires_at: int | None = None,
    wall_clock: Callable[[], float] = time.time,
) -> bool:
    """Retry the canonical callback without following redirects or logging secrets."""

    headers = callback_headers(callback_token)
    headers["Content-Type"] = "application/json"
    encoded = json.dumps(payload, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
    if len(encoded) > MAX_CALLBACK_BODY_BYTES:
        raise ProtocolError("callback exceeds the source-core plus callback-overhead limit")
    request = urllib_request.Request(callback_url, data=encoded, headers=headers, method="POST")
    trace_worker("callback_delivery_begin", status=payload.get("status","unknown"))
    for attempt, delay in enumerate(CALLBACK_DELAYS_SECONDS):
        if delay:
            remaining = callback_expires_at - wall_clock() if callback_expires_at is not None else delay
            if remaining <= 0:
                return False
            sleep(min(delay, remaining))
        if callback_expires_at is not None:
            remaining = callback_expires_at - wall_clock()
            if remaining <= 0:
                return False
            request_timeout = max(0.01, min(float(timeout), remaining))
        else:
            request_timeout = timeout
        trace_worker("callback_delivery_attempt", attempt_number=attempt+1)
        try:
            with opener_factory().open(request, timeout=request_timeout) as response:
                status = response.getcode()
                if 200 <= status < 300:
                    trace_worker("callback_delivery_http_ack", http_status=status)
                    return callback_expires_at is None or wall_clock() < callback_expires_at
                if status < 500 and status not in (408, 429):
                    trace_worker("callback_delivery_http_rejected", http_status=status)
                    return False
        except urllib_error.HTTPError as error:
            status = error.code
            error.close()
            if status < 500 and status not in (408, 429):
                trace_worker("callback_delivery_http_rejected", http_status=status)
                return False
        except Exception:
            # Exception text can contain the callback URL; keep it private.
            pass
        if attempt + 1 < len(CALLBACK_DELAYS_SECONDS):
            continue
    trace_worker("callback_delivery_exhausted")
    return False


def process_dispatch(
    payload: Mapping[str, Any],
    media_token: str,
    callback_token: str,
    *,
    ledger: DispatchLedger,
    commit_ledger: Callable[[], None] = lambda: None,
    analysis_client: Callable[[StageDispatch, str], StageResult] | None = None,
    callback_sender: Callable[[str, str, Mapping[str, Any]], bool] = deliver_callback,
    now: int | None = None,
    expected_origin: str | None = None,
    expected_pipeline_revision: str | None = None,
) -> bool:
    """Use a cached completed result on replay, then clear it after callback ack."""

    dispatch = StageDispatch.parse(
        payload,
        now=now,
        allow_expired=True,
        expected_origin=expected_origin,
        expected_pipeline_revision=expected_pipeline_revision,
    )
    media_headers(media_token)
    callback_headers(callback_token)
    client = analysis_client or analyze_dispatch
    _LOGGER.info("clipping_worker_attempt_start job=%s attempt=%s",
                 dispatch.job_id, dispatch.attempt_id)
    trace_worker("worker_execute_stage_begin", job=dispatch.job_id, attempt=dispatch.attempt_id)
    callback = execute_stage(
        payload,
        media_token=media_token,
        callback_token=callback_token,
        ledger=ledger,
        client=client,
        now=now,
        expected_origin=expected_origin,
        expected_pipeline_revision=expected_pipeline_revision,
        on_claim=commit_ledger,
        on_complete=commit_ledger,
    )
    trace_worker("worker_execute_stage_returned", job=dispatch.job_id, attempt=dispatch.attempt_id,
                 status=callback["status"] if callback is not None else "cached")
    if callback is None:
        # The callback was acknowledged on an earlier run; the ledger's
        # completed tombstone prevents a second inference.
        _LOGGER.info("clipping_worker_attempt_cached job=%s attempt=%s callback_required=false",
                     dispatch.job_id, dispatch.attempt_id)
        return True
    _LOGGER.info("clipping_worker_callback_start job=%s attempt=%s status=%s",
                 dispatch.job_id, dispatch.attempt_id, callback["status"])
    trace_worker("worker_callback_begin", job=dispatch.job_id, attempt=dispatch.attempt_id,
                 status=callback["status"])
    if callback_sender is deliver_callback:
        acknowledged = deliver_callback(
            dispatch.callback_url,
            callback_token,
            callback,
            callback_expires_at=dispatch.callback_expires_at,
        )
    else:
        acknowledged = callback_sender(dispatch.callback_url, callback_token, callback)
    trace_worker("worker_callback_result", job=dispatch.job_id, attempt=dispatch.attempt_id,
                 acknowledged=acknowledged)
    if acknowledged:
        trace_worker("worker_ledger_mark_delivered_begin", job=dispatch.job_id, attempt=dispatch.attempt_id)
        ledger.mark_delivered(dispatch)
        commit_ledger()
    _LOGGER.info("clipping_worker_callback_complete job=%s attempt=%s status=%s acknowledged=%s",
                 dispatch.job_id, dispatch.attempt_id, callback["status"], acknowledged)
    return acknowledged


def ledger_rpc_operation(
    ledger: SQLiteDispatchLedger,
    operation: str,
    request: Mapping[str, Any],
    *,
    expected_origin: str | None = None,
    expected_pipeline_revision: str | None = None,
) -> dict[str, Any]:
    """Execute one validated ledger operation in the authoritative owner."""

    if not isinstance(request, Mapping):
        raise ProtocolError("ledger RPC request must be an object")
    if operation == "prune":
        if set(request) != {"now"}:
            raise ProtocolError("invalid prune RPC fields")
        now = request["now"]
        if now is not None and (isinstance(now, bool) or not isinstance(now, int) or now < 0):
            raise ProtocolError("invalid prune timestamp")
        ledger.prune(now=now)
        return {"pruned": True}

    expected_fields = {
        "claim": {"dispatch", "now", "phase"},
        "complete": {"dispatch", "result", "now"},
        "mark_delivered": {"dispatch"},
    }
    if operation not in expected_fields or set(request) != expected_fields[operation]:
        raise ProtocolError("invalid ledger RPC operation or fields")
    now = request.get("now")
    if operation in ("claim", "complete") and now is not None:
        if isinstance(now, bool) or not isinstance(now, int) or now < 0:
            raise ProtocolError("invalid ledger RPC timestamp")
    dispatch = StageDispatch.parse(
        request["dispatch"],
        now=now if operation in ("claim", "complete") else None,
        allow_expired=True,
        expected_origin=expected_origin,
        expected_pipeline_revision=expected_pipeline_revision,
    )
    if operation == "claim":
        phase = request["phase"]
        if phase not in ("admit", "worker"):
            raise ProtocolError("invalid dispatch claim phase")
        try:
            claimed, cached = ledger.claim(dispatch, now=now, phase=phase)
        except DuplicateAttempt:
            return {"claimed": False, "cached": None}
        return {"claimed": claimed, "cached": cached}
    if operation == "complete":
        result = request["result"]
        if not isinstance(result, dict):
            raise ProtocolError("ledger result must be an object")
        ledger.complete(dispatch, result, now=now)
        return {"completed": True}
    ledger.mark_delivered(dispatch)
    return {"delivered": True}


def run_ledger_rpc(
    operation: str,
    request: Mapping[str, Any],
    *,
    ledger_factory: Callable[[], SQLiteDispatchLedger],
    reload_volume: Callable[[], None],
    commit_volume: Callable[[], None],
    expected_origin: str | None = None,
    expected_pipeline_revision: str | None = None,
) -> dict[str, Any]:
    """Reload, handle one RPC, close SQLite, and commit its sole-owner volume."""

    if operation != "prune":
        dispatch_id = request.get("dispatch") if isinstance(request, Mapping) else None
        trace_worker("ledger_rpc_started", operation=operation,
                     job=dispatch_id.get("job_id", "-") if isinstance(dispatch_id, Mapping) else "-",
                     attempt=dispatch_id.get("attempt_id", "-") if isinstance(dispatch_id, Mapping) else "-")
    reload_volume()
    if operation != "prune":
        trace_worker("ledger_volume_reloaded", operation=operation)
    ledger = ledger_factory()
    try:
        outcome = ledger_rpc_operation(
            ledger,
            operation,
            request,
            expected_origin=expected_origin,
            expected_pipeline_revision=expected_pipeline_revision,
        )
        if operation != "prune":
            trace_worker("ledger_rpc_operation_complete", operation=operation)
        return outcome
    finally:
        try:
            ledger.close()
        finally:
            commit_volume()
            if operation != "prune":
                trace_worker("ledger_volume_committed", operation=operation)


class RemoteDispatchLedger:
    """Client for the single CPU Modal function that owns the ledger volume."""

    def __init__(self, rpc: Callable[[str, Mapping[str, Any]], Any]):
        self._rpc = rpc

    def claim(
        self,
        dispatch: StageDispatch,
        now: int | None = None,
        *,
        phase: str = "worker",
    ) -> tuple[bool, dict[str, Any] | None]:
        response = self._rpc("claim", {"dispatch": dispatch.as_dict(), "now": now, "phase": phase})
        if (
            not isinstance(response, Mapping)
            or set(response) != {"claimed", "cached"}
            or not isinstance(response["claimed"], bool)
            or (response["cached"] is not None and not isinstance(response["cached"], dict))
        ):
            raise WorkerUnavailable("ledger owner returned an invalid claim response")
        return response["claimed"], response["cached"]

    def complete(
        self,
        dispatch: StageDispatch,
        result: dict[str, Any],
        now: int | None = None,
    ) -> None:
        response = self._rpc("complete", {"dispatch": dispatch.as_dict(), "result": result, "now": now})
        if not isinstance(response, Mapping) or dict(response) != {"completed": True}:
            raise WorkerUnavailable("ledger owner did not confirm result completion")

    def mark_delivered(self, dispatch: StageDispatch) -> None:
        response = self._rpc("mark_delivered", {"dispatch": dispatch.as_dict()})
        if not isinstance(response, Mapping) or dict(response) != {"delivered": True}:
            raise WorkerUnavailable("ledger owner did not confirm callback delivery")


def dispatch_admission(
    dispatch: StageDispatch,
    ledger_rpc: Callable[[str, Mapping[str, Any]], Any],
) -> tuple[str, dict[str, Any] | None]:
    """Claim at the HTTP edge and separate inference from cached replay."""

    response = ledger_rpc("claim", {"dispatch": dispatch.as_dict(), "now": None, "phase": "admit"})
    if (
        not isinstance(response, Mapping)
        or set(response) != {"claimed", "cached"}
        or not isinstance(response["claimed"], bool)
        or (response["cached"] is not None and not isinstance(response["cached"], dict))
    ):
        raise WorkerUnavailable("ledger owner returned an invalid dispatch admission")
    if response["claimed"]:
        return "analysis", None
    # A cached callback must be posted again, while an in-progress or already
    # acknowledged attempt is accepted without scheduling another task.
    if response["cached"] is not None:
        return "replay", response["cached"]
    return "duplicate", None


def replay_cached_callback(
    dispatch_payload: Mapping[str, Any],
    callback_token: str,
    cached_callback: Mapping[str, Any],
    *,
    ledger_rpc: Callable[[str, Mapping[str, Any]], Any],
    callback_sender: Callable[[str, str, Mapping[str, Any]], bool] = deliver_callback,
    now: int | None = None,
    expected_origin: str | None = None,
    expected_pipeline_revision: str | None = None,
) -> bool:
    """Post a validated cached callback on CPU and clear it through the owner."""

    dispatch = StageDispatch.parse(
        dispatch_payload,
        now=now,
        allow_expired=True,
        expected_origin=expected_origin,
        expected_pipeline_revision=expected_pipeline_revision,
    )
    callback_headers(callback_token)
    validated = canonical_callback(
        dispatch,
        StageResult(
            cached_callback.get("artifact"),
            cached_callback.get("cost_estimate_micro_usd"),
            cached_callback.get("cost_basis"),
        ) if isinstance(cached_callback, Mapping) else None,
    )
    if dict(validated) != dict(cached_callback):
        raise ProtocolError("cached callback does not match its dispatch")
    if callback_sender is deliver_callback:
        acknowledged = deliver_callback(
            dispatch.callback_url,
            callback_token,
            validated,
            callback_expires_at=dispatch.callback_expires_at,
        )
    else:
        acknowledged = callback_sender(dispatch.callback_url, callback_token, validated)
    if acknowledged:
        RemoteDispatchLedger(ledger_rpc).mark_delivered(dispatch)
    return acknowledged


def _clear_delivered_worker_callback(dispatch: StageDispatch) -> None:
    """Discard cached result content after Go acknowledges a failure callback."""

    try:
        RemoteDispatchLedger(
            lambda operation, request: _MODAL_FUNCTIONS["ledger_owner"].remote(
                operation,
                dict(request),
            )
        ).mark_delivered(dispatch)
    except Exception as error:
        # The Go failure callback is already acknowledged. Keep any existing
        # ledger tombstone; a cleanup error must not trigger inference again.
        _safe_log_failure("clipping_callback_cache_cleanup_failed", dispatch, error)


def _persist_worker_failure_callback(dispatch: StageDispatch) -> None:
    """Cache a canonical failure before delivery so retries can replay it."""

    ledger = RemoteDispatchLedger(
        lambda operation, request: _MODAL_FUNCTIONS["ledger_owner"].remote(
            operation,
            dict(request),
        )
    )
    failure = canonical_failure_callback(dispatch)
    try:
        claimed, cached = ledger.claim(dispatch)
        if claimed:
            ledger.complete(dispatch, failure)
        elif cached is None:
            # This invocation may have already claimed the worker row before
            # a setup/volume error. Completion is safe only for that running
            # attempt; the owner rejects completed or mismatched rows.
            ledger.complete(dispatch, failure)
    except Exception as error:
        _safe_log_failure("clipping_failure_callback_cache_failed", dispatch, error)


def dispatch_ack(dispatch: StageDispatch) -> dict[str, Any]:
    """Sanitized 202 body tied to the exact job attempt accepted for enqueue."""

    return {
        "accepted": True,
        "job_id": dispatch.job_id,
        "attempt_id": dispatch.attempt_id,
        "dispatch_id": dispatch.dispatch_id,
    }


def _valid_dispatch_shared_secret(value: Any) -> bool:
    """Require the operator dispatch bearer secret to be 64 lowercase hex chars."""

    return isinstance(value, str) and _SHA256.fullmatch(value) is not None


def _safe_log_failure(
    event: str, dispatch: StageDispatch | None, error: Exception, *, phase: str = "unknown",
) -> None:
    # Log controlled categories only. Untrusted network errors may contain
    # capability-bearing URLs, so their exception text must never be emitted.
    safe_deadline_codes = {
        "dispatch lease expired": "dispatch_lease_expired",
        "callback capability expired": "callback_capability_expired",
        "dispatch compute deadline expired": "compute_deadline_expired",
        "dispatch capability expired during source download": "download_lease_expired",
        "dispatch capability expired after source download": "download_lease_expired",
    }
    category = "not_applicable"
    http_status: int | str = "-"
    if isinstance(error, MediaProbeCancelled):
        category = error.reason_code
        if error.http_status is not None:
            http_status = error.http_status
    elif isinstance(error, WorkCancelled):
        category = safe_deadline_codes.get(str(error), "unspecified_cancellation")
    _LOGGER.warning(
        "%s job=%s attempt=%s phase=%s reason=%s category=%s http_status=%s",
        event,
        dispatch.job_id if dispatch else "unknown",
        dispatch.attempt_id if dispatch else "unknown",
        phase,
        type(error).__name__,
        category,
        http_status,
    )


_MODAL_FUNCTIONS: dict[str, Any] = {}
_MODAL_MODEL_VOLUME: Any = None
_MODAL_LEDGER_VOLUME: Any = None


# Keep each registered handler at module scope. Modal App.function
# rejects nested local handlers by default unless they use serialized=True.
def clipping_ledger_owner(
    operation: str = "prune",
    request: dict[str, Any] | None = None,
) -> dict[str, Any]:
    # Scheduled pruning is routine housekeeping, not a job lifecycle event.
    # Preserve failure visibility through the existing exception handling.
    if operation != "prune":
        trace_worker("ledger_owner_entered", operation=operation)
    if _MODAL_LEDGER_VOLUME is None:
        raise RuntimeError("ledger volume is not configured")
    try:
        return run_ledger_rpc(
            operation,
            {"now": None} if request is None else request,
            ledger_factory=lambda: SQLiteDispatchLedger(LEDGER_PATH),
            reload_volume=_MODAL_LEDGER_VOLUME.reload,
            commit_volume=_MODAL_LEDGER_VOLUME.commit,
            expected_origin=os.environ.get(WORKER_ORIGIN_KEY),
            expected_pipeline_revision=os.environ.get(WORKER_PIPELINE_REVISION_KEY),
        )
    except Exception:
        raise RuntimeError("ledger operation failed") from None


def clipping_callback_replay_task(
    dispatch_payload: dict[str, Any],
    callback_capability: str,
    cached_callback: dict[str, Any],
) -> bool:
    dispatch: StageDispatch | None = None
    expected_origin = os.environ.get(WORKER_ORIGIN_KEY)
    expected_pipeline_revision = os.environ.get(WORKER_PIPELINE_REVISION_KEY)
    try:
        dispatch = StageDispatch.parse(
            dispatch_payload,
            allow_expired=True,
            expected_origin=expected_origin,
            expected_pipeline_revision=expected_pipeline_revision,
        )
        acknowledged = replay_cached_callback(
            dispatch_payload,
            callback_capability,
            cached_callback,
            ledger_rpc=lambda operation, request: _MODAL_FUNCTIONS["ledger_owner"].remote(
                operation,
                dict(request),
            ),
            expected_origin=expected_origin,
            expected_pipeline_revision=expected_pipeline_revision,
        )
        if acknowledged:
            return True
        _LOGGER.warning("clipping_callback_replay_delivery_pending job=%s attempt=%s", dispatch.job_id, dispatch.attempt_id)
        accepted = deliver_callback(
            dispatch.callback_url,
            callback_capability,
            canonical_failure_callback(dispatch),
            callback_expires_at=dispatch.callback_expires_at,
        )
        if accepted:
            _LOGGER.warning("clipping_failure_callback_acknowledged job=%s attempt=%s", dispatch.job_id, dispatch.attempt_id)
            _clear_delivered_worker_callback(dispatch)
        return accepted
    except Exception as error:
        _safe_log_failure("clipping_callback_replay_failed", dispatch, error)
        if dispatch is not None:
            try:
                accepted = deliver_callback(
                    dispatch.callback_url,
                    callback_capability,
                    canonical_failure_callback(dispatch),
                    callback_expires_at=dispatch.callback_expires_at,
                )
                if accepted:
                    _LOGGER.warning("clipping_failure_callback_acknowledged job=%s attempt=%s", dispatch.job_id, dispatch.attempt_id)
                    _clear_delivered_worker_callback(dispatch)
                    return True
            except Exception as callback_error:
                _safe_log_failure("clipping_failure_callback_delivery_failed", dispatch, callback_error)
        raise RuntimeError("clipping callback replay failed") from None


def clipping_analysis_task(
    dispatch_payload: dict[str, Any],
    media_capability: str,
    callback_capability: str,
) -> bool:
    trace_worker("gpu_function_entered", job=dispatch_payload.get("job_id","-"), attempt=dispatch_payload.get("attempt_id","-"))
    started_at = time.monotonic()
    heartbeat_stopped = threading.Event()

    def heartbeat() -> None:
        while not heartbeat_stopped.wait(30):
            trace_worker(
                "gpu_task_heartbeat",
                job=dispatch_payload.get("job_id", "-"),
                attempt=dispatch_payload.get("attempt_id", "-"),
                elapsed_ms=int((time.monotonic() - started_at) * 1000),
            )
    threading.Thread(target=heartbeat, name="clipping-task-heartbeat", daemon=True).start()
    dispatch: StageDispatch | None = None
    expected_origin = os.environ.get(WORKER_ORIGIN_KEY)
    expected_pipeline_revision = os.environ.get(WORKER_PIPELINE_REVISION_KEY)
    try:
        dispatch = StageDispatch.parse(
            dispatch_payload,
            allow_expired=True,
            expected_origin=expected_origin,
            expected_pipeline_revision=expected_pipeline_revision,
        )
        _LOGGER.info("clipping_gpu_task_started job=%s attempt=%s source_duration_ms=%d",
                     dispatch.job_id, dispatch.attempt_id, dispatch.source_duration_ms)
        trace_worker("gpu_model_revision_check_begin", job=dispatch.job_id, attempt=dispatch.attempt_id)
        _configured_model_revision()
        trace_worker("gpu_model_revision_check_complete", job=dispatch.job_id, attempt=dispatch.attempt_id)
        if _MODAL_MODEL_VOLUME is None:
            raise AnalysisError("worker model volume is unavailable")
        # CPU prewarm is the sole persistent writer of model files. GPU
        # containers reload the committed snapshot and never commit it.
        _LOGGER.info("clipping_gpu_model_volume_reload_start job=%s attempt=%s",
                     dispatch.job_id, dispatch.attempt_id)
        trace_worker("gpu_volume_reload_begin", job=dispatch.job_id, attempt=dispatch.attempt_id)
        _MODAL_MODEL_VOLUME.reload()
        trace_worker("gpu_volume_reload_complete", job=dispatch.job_id, attempt=dispatch.attempt_id)
        _LOGGER.info("clipping_gpu_model_volume_reload_complete job=%s attempt=%s",
                     dispatch.job_id, dispatch.attempt_id)
        trace_worker("gpu_dispatch_execution_begin", job=dispatch.job_id, attempt=dispatch.attempt_id)
        accepted = process_dispatch(
            dispatch_payload,
            media_capability,
            callback_capability,
            ledger=RemoteDispatchLedger(
                lambda operation, request: _MODAL_FUNCTIONS["ledger_owner"].remote(
                    operation,
                    dict(request),
                )
            ),
            expected_origin=expected_origin,
            expected_pipeline_revision=expected_pipeline_revision,
        )
        trace_worker("gpu_dispatch_execution_complete", job=dispatch.job_id, attempt=dispatch.attempt_id,
                     acknowledged=accepted)
        if accepted:
            _LOGGER.info("clipping_callback_acknowledged job=%s attempt=%s", dispatch.job_id, dispatch.attempt_id)
        else:
            _LOGGER.warning("clipping_callback_delivery_pending job=%s attempt=%s", dispatch.job_id, dispatch.attempt_id)
            # A completed callback may have been rejected or lost. A bounded
            # failure callback gives Go a terminal event if the first body
            # could not be committed; replay remains guarded by the ledger.
            accepted = deliver_callback(
                dispatch.callback_url,
                callback_capability,
                canonical_failure_callback(dispatch),
                callback_expires_at=dispatch.callback_expires_at,
            )
            if accepted:
                _LOGGER.warning("clipping_failure_callback_acknowledged job=%s attempt=%s", dispatch.job_id, dispatch.attempt_id)
                _clear_delivered_worker_callback(dispatch)
        return accepted
    except Exception as error:
        trace_worker("gpu_function_failure", job=dispatch.job_id if dispatch else "-",
                     attempt=dispatch.attempt_id if dispatch else "-", reason=type(error).__name__)
        _safe_log_failure("clipping_worker_task_failed", dispatch, error)
        if dispatch is not None:
            try:
                _persist_worker_failure_callback(dispatch)
                accepted = deliver_callback(
                    dispatch.callback_url,
                    callback_capability,
                    canonical_failure_callback(dispatch),
                    callback_expires_at=dispatch.callback_expires_at,
                )
                if accepted:
                    _LOGGER.warning("clipping_failure_callback_acknowledged job=%s attempt=%s", dispatch.job_id, dispatch.attempt_id)
                    _clear_delivered_worker_callback(dispatch)
                    return True
            except Exception as callback_error:
                _safe_log_failure("clipping_failure_callback_delivery_failed", dispatch, callback_error)
        raise RuntimeError("clipping worker task failed") from None
    finally:
        heartbeat_stopped.set()
        trace_worker(
            "gpu_task_finished",
            job=dispatch_payload.get("job_id", "-"),
            attempt=dispatch_payload.get("attempt_id", "-"),
            elapsed_ms=int((time.monotonic() - started_at) * 1000),
        )


def clipping_model_prewarm_task() -> dict[str, Any]:
    """Manually prewarm the pinned model on CPU before enabling dispatch.

    This operation has no job payload, media URL, or scoped capability. It is
    intentionally unscheduled because the first invocation downloads pinned
    weights. The separately configured hard timeout bounds that operation.
    """

    if _MODAL_MODEL_VOLUME is None:
        raise RuntimeError("model volume is not configured")
    try:
        _MODAL_MODEL_VOLUME.reload()
        manifest = prewarm_faster_whisper_snapshot()
        _MODAL_MODEL_VOLUME.commit()
        return {"ready": True, "model_snapshot": manifest["model_snapshot"]}
    except Exception as error:
        raise RuntimeError("model prewarm failed") from None


def clipping_dispatch_api() -> Any:
    from fastapi import FastAPI, HTTPException, Request
    from fastapi.responses import JSONResponse
    from starlette.concurrency import run_in_threadpool

    api = FastAPI()

    async def dispatch_endpoint(request: Request) -> Any:
        shared_secret = os.environ.get(WORKER_SECRET_KEY, "")
        expected_origin = os.environ.get(WORKER_ORIGIN_KEY)
        expected_pipeline_revision = os.environ.get(WORKER_PIPELINE_REVISION_KEY)
        if not _valid_dispatch_shared_secret(shared_secret):
            raise HTTPException(status_code=503, detail="worker shared secret is unavailable")
        parts = request.headers.get("authorization", "").split(" ", 1)
        if (
            len(parts) != 2
            or parts[0].lower() != "bearer"
            or not hmac.compare_digest(parts[1], shared_secret)
        ):
            raise HTTPException(status_code=401, detail="unauthorized")
        try:
            _configured_model_revision()
        except AnalysisError:
            raise HTTPException(status_code=503, detail="worker model revision is not configured") from None
        if not expected_origin:
            raise HTTPException(status_code=503, detail="worker origin is not configured")
        if not expected_pipeline_revision:
            raise HTTPException(status_code=503, detail="worker pipeline revision is not configured")
        if request.headers.get("content-type", "").split(";", 1)[0].strip().lower() != "application/json":
            raise HTTPException(status_code=415, detail="dispatch must be JSON")
        try:
            body = await read_limited_body(
                request.stream(),
                request.headers.get("content-length"),
            )
        except DispatchBodyTooLarge:
            raise HTTPException(status_code=413, detail="dispatch exceeds size limit") from None
        except ValueError:
            raise HTTPException(status_code=400, detail="invalid content length") from None
        if not body:
            raise HTTPException(status_code=400, detail="invalid dispatch")
        try:
            payload = json.loads(bytes(body))
            dispatch = StageDispatch.parse(
                payload,
                expected_origin=expected_origin,
                expected_pipeline_revision=expected_pipeline_revision,
            )
        except (ValueError, TypeError):
            raise HTTPException(status_code=400, detail="invalid dispatch") from None
        media_capability = request.headers.get("x-framevault-media-capability", "")
        callback_capability = request.headers.get("x-framevault-callback-capability", "")
        try:
            media_headers(media_capability)
            callback_headers(callback_capability)
        except ProtocolError:
            raise HTTPException(status_code=400, detail="invalid worker capabilities") from None
        trace_worker("dispatch_http_validated", job=dispatch.job_id, attempt=dispatch.attempt_id)
        # Dispatch admission is a synchronous ledger operation. Run it and
        # its Modal spawn in a worker thread rather than blocking the ASGI loop.
        # Keep admission and enqueue together to preserve idempotency behavior.
        def admit_and_enqueue() -> None:
            owner_rpc = lambda operation, ledger_request: _MODAL_FUNCTIONS["ledger_owner"].remote(
                operation,
                dict(ledger_request),
            )
            trace_worker("dispatch_ledger_claim_begin", job=dispatch.job_id, attempt=dispatch.attempt_id)
            action, cached_callback = dispatch_admission(dispatch, owner_rpc)
            trace_worker("dispatch_ledger_claim_complete", job=dispatch.job_id, attempt=dispatch.attempt_id, action=action)
            if action == "analysis":
                trace_worker("dispatch_gpu_spawn_begin", job=dispatch.job_id, attempt=dispatch.attempt_id)
                call = _MODAL_FUNCTIONS["analysis"].spawn(payload, media_capability, callback_capability)
                trace_worker("dispatch_gpu_spawn_submitted", job=dispatch.job_id, attempt=dispatch.attempt_id,
                             call_id=getattr(call, "object_id", "-"))
            elif action == "replay":
                trace_worker("dispatch_replay_spawn_begin", job=dispatch.job_id, attempt=dispatch.attempt_id)
                call = _MODAL_FUNCTIONS["callback_replay"].spawn(payload, callback_capability, cached_callback)
                trace_worker("dispatch_replay_spawn_submitted", job=dispatch.job_id, attempt=dispatch.attempt_id,
                             call_id=getattr(call, "object_id", "-"))
            elif action == "duplicate":
                trace_worker("dispatch_duplicate_no_spawn", job=dispatch.job_id, attempt=dispatch.attempt_id)
            _LOGGER.info("clipping_dispatch_accepted job=%s attempt=%s action=%s",
                         dispatch.job_id, dispatch.attempt_id, action)

        try:
            await run_in_threadpool(admit_and_enqueue)
        except Exception as error:
            trace_worker("dispatch_enqueue_failed", job=dispatch.job_id, attempt=dispatch.attempt_id,
                         reason=type(error).__name__)
            _safe_log_failure("clipping_dispatch_enqueue_failed", dispatch, error)
            raise HTTPException(status_code=503, detail="worker temporarily unavailable") from None
        trace_worker("dispatch_http_202_returned", job=dispatch.job_id, attempt=dispatch.attempt_id)
        return JSONResponse(status_code=202, content=dispatch_ack(dispatch))

    # With postponed annotations, the factory-local Request name would be
    # evaluated as a query parameter if registered as a forward reference.
    # Bind the actual FastAPI Request type before registering the route.
    dispatch_endpoint.__annotations__["request"] = Request
    api.add_api_route("/dispatch", dispatch_endpoint, methods=["POST"])
    return api


def _add_worker_source_files(image: Any) -> Any:
    """Mount sibling imports into /root for every registered worker image."""

    source_dir = Path(__file__).resolve().parent
    return (
        image.add_local_file(
            str(source_dir / "clipping_analysis.py"),
            remote_path="/root/clipping_analysis.py",
        )
        .add_local_file(
            str(source_dir / "clipping_protocol.py"),
            remote_path="/root/clipping_protocol.py",
        )
    )


def _add_rust_scene_image(image: Any) -> Any:
    """Compile the self-contained Rust analyzer in the GPU image at deployment.

    When Modal imports the flattened worker inside a running container the
    repository source tree may be absent; the image is already built then.
    """
    rust_dir = Path(__file__).resolve().parent / "scene-analyzer"
    if not rust_dir.is_dir():
        return image
    return (
        image.add_local_dir(
            str(rust_dir),
            remote_path="/build/framevault-scene-analyzer",
            copy=True,
        )
        .run_commands(
            "cd /build/framevault-scene-analyzer && cargo build --offline --release",
            "install -m 0755 /build/framevault-scene-analyzer/target/release/framevault-scene-analyzer /usr/local/bin/framevault-scene-analyzer",
        )
    )


def _install_modal_app() -> Any:
    """Create Modal registrations using only module-global handlers."""

    try:
        import modal
    except ImportError:
        return None

    global _MODAL_LEDGER_VOLUME, _MODAL_MODEL_VOLUME
    app = modal.App(APP_NAME)
    _MODAL_MODEL_VOLUME = modal.Volume.from_name(MODEL_VOLUME_NAME, create_if_missing=True)
    _MODAL_LEDGER_VOLUME = modal.Volume.from_name(LEDGER_VOLUME_NAME, create_if_missing=True)
    # Modal's required_keys validates presence but does not scope a Secret's
    # contents. Provision these three distinct names with exactly their
    # declared variables; only the dispatch API secret contains the bearer.
    dispatch_api_secret = modal.Secret.from_name(
        DISPATCH_API_SECRET_NAME,
        required_keys=[WORKER_SECRET_KEY, WORKER_ORIGIN_KEY, WORKER_PIPELINE_REVISION_KEY, WORKER_MODEL_REVISION_KEY],
    )
    runtime_secret = modal.Secret.from_name(
        RUNTIME_SECRET_NAME,
        required_keys=[WORKER_ORIGIN_KEY, WORKER_PIPELINE_REVISION_KEY, WORKER_MODEL_REVISION_KEY],
    )
    prewarm_secret = modal.Secret.from_name(
        MODEL_PREWARM_SECRET_NAME,
        required_keys=[WORKER_MODEL_REVISION_KEY],
    )
    gpu_image = _add_worker_source_files(
        _add_rust_scene_image(
            modal.Image.from_registry(GPU_CUDA_IMAGE, add_python="3.11")
            .entrypoint([])
            .apt_install("ffmpeg", "rustc", "cargo")
            .pip_install(*CLIPPING_IMAGE_PACKAGES)
        ).env({
            "FRAMEVAULT_WHISPER_DEVICE": "cuda",
            "FRAMEVAULT_WHISPER_CACHE": MODEL_CACHE_DIR,
            "FRAMEVAULT_SCENE_ENGINE": "rust",
            "FRAMEVAULT_SCENE_DECODE": "auto",
        })
    )
    prewarm_image = _add_worker_source_files(
        modal.Image.debian_slim(python_version="3.11").pip_install(
            "modal==1.3.1",
            "huggingface-hub==0.30.2",
        ).env({"FRAMEVAULT_WHISPER_CACHE": MODEL_CACHE_DIR})
    )
    api_image = _add_worker_source_files(
        modal.Image.debian_slim(python_version="3.11").pip_install(
            "modal==1.3.1",
            "fastapi==0.115.12",
        )
    )

    # Modal App.function requires globally addressable functions unless
    # serialized=True. Keep these implementations module-global and register
    # them directly, avoiding serialization of captured resource handles.
    _MODAL_FUNCTIONS.clear()
    _MODAL_FUNCTIONS["model_prewarm"] = app.function(
        image=prewarm_image,
        cpu=1,
        timeout=3600,
        volumes={MODEL_CACHE_DIR: _MODAL_MODEL_VOLUME},
        secrets=[prewarm_secret],
        max_containers=1,
        retries=modal.Retries(max_retries=0),
    )(clipping_model_prewarm_task)
    _MODAL_FUNCTIONS["ledger_owner"] = app.function(
        image=api_image,
        cpu=0.5,
        timeout=60,
        volumes={LEDGER_DIR: _MODAL_LEDGER_VOLUME},
        secrets=[runtime_secret],
        schedule=modal.Period(seconds=60),
        # Keep one ledger owner container at a time; retire it after each
        # input so subsequent invocations reload the authoritative volume.
        max_containers=1,
        single_use_containers=True,
        retries=modal.Retries(max_retries=0),
    )(clipping_ledger_owner)
    _MODAL_FUNCTIONS["callback_replay"] = app.function(
        image=api_image,
        timeout=60,
        secrets=[runtime_secret],
        max_containers=10,
        retries=modal.Retries(max_retries=0),
    )(clipping_callback_replay_task)
    _MODAL_FUNCTIONS["analysis"] = app.function(
        image=gpu_image,
        gpu="L4",
        timeout=MAX_COMPUTE_SECONDS + 300,
        ephemeral_disk=524_288,
        volumes={MODEL_CACHE_DIR: _MODAL_MODEL_VOLUME},
        secrets=[runtime_secret],
        max_containers=1,
        retries=modal.Retries(max_retries=0),
    )(clipping_analysis_task)
    asgi_api = modal.asgi_app()(clipping_dispatch_api)
    _MODAL_FUNCTIONS["dispatch_api"] = app.function(
        image=api_image,
        secrets=[dispatch_api_secret],
        timeout=30,
        max_containers=100,
    )(asgi_api)
    return app


app = _install_modal_app()
