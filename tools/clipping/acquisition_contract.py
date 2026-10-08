"""Offline-only reference validator for the C1.1 acquisition contract.

This module deliberately performs no DNS lookup, HTTP request, file write, media
probe, or provider operation. Tests supply simulated DNS answers, redirect hops,
response headers, bytes, and probe results. It is a contract/probe fixture, not
a production downloader.
"""

from __future__ import annotations

import hashlib
import ipaddress
import re
from urllib.parse import parse_qs, urljoin, urlsplit, urlunsplit

SOURCE_VERSION = "ai-clip.source.v1"
MAX_DURATION_MS = 4 * 60 * 60 * 1000
MAX_SOURCE_BYTES = 16 * 1024**3
MAX_DERIVED_BYTES = 8 * 1024**3
MIN_VOLUME_FREE_BYTES = 4 * 1024**3
MAX_REDIRECTS = 5
SOURCE_ID_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$")

REDIRECT_CODES = {301, 302, 303, 307, 308}
GOOGLE_HOSTS = {
    "drive.google.com",
    "docs.google.com",
    "drive.usercontent.google.com",
    "googleusercontent.com",
}
DROPBOX_HOSTS = {"dropbox.com", "dropboxusercontent.com"}
YOUTUBE_HOSTS = {
    "youtube.com",
    "www.youtube.com",
    "m.youtube.com",
    "youtu.be",
    "www.youtu.be",
    "www.youtube-nocookie.com",
    "youtube-nocookie.com",
}
ALLOWED_CONTAINERS = {"mp4", "mov", "mkv", "webm"}


def _failure(status: str, reason: str) -> dict:
    return {"status": status, "reason": reason, "version": SOURCE_VERSION}


def _source_id_ok(source_id: object) -> bool:
    return isinstance(source_id, str) and SOURCE_ID_RE.fullmatch(source_id) is not None


def _parse_https_url(raw_url: object) -> tuple[str | None, str | None]:
    """Return canonical URL and a stable failure code, without resolving DNS."""
    if not isinstance(raw_url, str) or not raw_url or raw_url != raw_url.strip():
        return None, "invalid_url"
    if any(ord(char) < 0x20 or ord(char) == 0x7F for char in raw_url) or "#" in raw_url:
        return None, "invalid_url"
    try:
        parsed = urlsplit(raw_url)
        host = parsed.hostname
        port = parsed.port
    except ValueError:
        return None, "invalid_url"
    if parsed.scheme.lower() != "https" or not host:
        return None, "https_required"
    if parsed.username is not None or parsed.password is not None:
        return None, "url_credentials_forbidden"
    if port not in (None, 443):
        return None, "port_forbidden"
    if parsed.netloc.endswith(".") or "%" in host:
        return None, "invalid_host"
    try:
        host = host.encode("idna").decode("ascii").lower()
    except UnicodeError:
        return None, "invalid_host"
    try:
        ipaddress.ip_address(host.strip("[]"))
    except ValueError:
        pass
    else:
        return None, "ip_literal_forbidden"
    canonical = urlunsplit(("https", host, parsed.path or "/", parsed.query, ""))
    return canonical, None


def _provider_for_host(host: str) -> str | None:
    if host in YOUTUBE_HOSTS:
        return "youtube"
    if host in GOOGLE_HOSTS or host.endswith(".googleusercontent.com"):
        return "google_drive_public"
    if host in DROPBOX_HOSTS or host.endswith(".dropbox.com") or host.endswith(".dropboxusercontent.com"):
        return "dropbox_public"
    return None


def _host_matches_provider(host: str, provider: str) -> bool:
    return _provider_for_host(host) == provider


def classify_source(
    *, source_id: str, url: str | None, rights_attested: bool, upload: bool = False
) -> dict:
    """Classify an input and return the only permitted offline route proposal."""
    if not _source_id_ok(source_id):
        return _failure("unsupported", "invalid_source_id")
    if rights_attested is not True:
        return _failure("authorization_needed", "reuse_rights_not_attested")
    if upload:
        if url is not None:
            return _failure("unsupported", "upload_must_not_include_remote_url")
        return {
            "status": "probe_required",
            "reason": "local_upload_route_candidate",
            "source_id": source_id,
            "source_kind": "upload",
            "route": "local_upload",
            "version": SOURCE_VERSION,
            "simulated": True,
        }
    canonical, error = _parse_https_url(url)
    if error:
        return _failure("unsupported", error or "invalid_url")
    assert canonical is not None
    host = urlsplit(canonical).hostname or ""
    provider = _provider_for_host(host)
    if provider == "youtube":
        parts = urlsplit(canonical)
        video_id = ""
        if host == "youtu.be" or host == "www.youtu.be":
            video_id = parts.path.strip("/").split("/", 1)[0]
        elif parts.path == "/watch":
            video_id = parse_qs(parts.query).get("v", [""])[0]
        elif parts.path.startswith(("/shorts/", "/embed/", "/live/")):
            video_id = parts.path.strip("/").split("/", 1)[1]
        if not re.fullmatch(r"[A-Za-z0-9_-]{11}", video_id):
            return _failure("unsupported", "unsupported_youtube_url_shape")
        return {
            "status": "original_file_required",
            "reason": "youtube_has_no_approved_media_fetch_route",
            "source_id": source_id,
            "source_kind": "youtube_original_file",
            "route": "youtube_original_file_upload",
            "version": SOURCE_VERSION,
            "simulated": True,
        }
    if provider == "google_drive_public":
        return {
            "status": "probe_required",
            "reason": "public_content_url_probe_required",
            "source_id": source_id,
            "source_kind": "google_drive_public",
            "route": "drive_public_content_url",
            "version": SOURCE_VERSION,
            "simulated": True,
        }
    if provider == "dropbox_public":
        parsed = urlsplit(canonical)
        if not (parsed.path.startswith("/s/") or parsed.path.startswith("/scl/fi/")):
            return _failure("unsupported", "unsupported_dropbox_link_shape")
        return {
            "status": "probe_required",
            "reason": "public_dropbox_download_probe_required",
            "source_id": source_id,
            "source_kind": "dropbox_public",
            "route": "dropbox_public_dl1",
            "query_normalization": "dl=1",
            "version": SOURCE_VERSION,
            "simulated": True,
        }
    return _failure("unsupported", "unsupported_source_host")


def _public_ip_set(values: object) -> bool:
    if not isinstance(values, list) or not values:
        return False
    try:
        addresses = [ipaddress.ip_address(value) for value in values]
    except (ValueError, TypeError):
        return False
    # Reject the whole DNS answer if even one result is non-global. The caller
    # must pin the eventual connection to one of these validated addresses.
    return all(address.is_global and not address.is_multicast for address in addresses)


def _container_from_prefix(prefix: bytes) -> str | None:
    if len(prefix) >= 12 and prefix[4:8] == b"ftyp":
        return "mov" if prefix[8:12] == b"qt  " else "mp4"
    if prefix.startswith(b"\x1a\x45\xdf\xa3"):
        return "webm" if b"webm" in prefix[:64].lower() else "mkv"
    return None


def _normalise_streams(value: object, *, video: bool) -> list[dict] | None:
    if not isinstance(value, list):
        return None
    normalized: list[dict] = []
    for stream in value:
        if not isinstance(stream, dict) or not isinstance(stream.get("codec"), str):
            return None
        entry = {"codec": stream["codec"]}
        if video:
            for field in ("width", "height", "frame_rate_num", "frame_rate_den"):
                value = stream.get(field)
                if type(value) is not int or value <= 0:
                    return None
                entry[field] = value
        else:
            for field in ("sample_rate_hz", "channels"):
                value = stream.get(field)
                if type(value) is not int or value <= 0:
                    return None
                entry[field] = value
        normalized.append(entry)
    return normalized


def simulate_probe(
    *,
    source_id: str,
    source_kind: str,
    route: str,
    start_url: str | None,
    rights_attested: bool,
    hops: list[dict],
    disk_free_bytes: int,
) -> dict:
    """Evaluate a caller-supplied fake fetch transcript; never contacts a host."""
    if not _source_id_ok(source_id):
        return _failure("unsupported", "invalid_source_id")
    if rights_attested is not True:
        return _failure("authorization_needed", "reuse_rights_not_attested")
    if not isinstance(source_kind, str) or source_kind not in {
        "upload",
        "youtube_original_file",
        "google_drive_public",
        "dropbox_public",
    }:
        return _failure("unsupported", "source_kind_has_no_media_probe_route")
    expected_route = {
        "upload": "local_upload",
        "youtube_original_file": "youtube_original_file_upload",
        "google_drive_public": "drive_public_content_url",
        "dropbox_public": "dropbox_public_dl1",
    }[source_kind]
    if not isinstance(route, str) or route != expected_route:
        return _failure("unsupported", "route_source_kind_mismatch")
    if type(disk_free_bytes) is not int or disk_free_bytes < 0:
        return _failure("unsupported", "invalid_storage_measurement")
    if not isinstance(hops, list) or not hops:
        return _failure("unsupported", "empty_probe_transcript")

    is_upload = source_kind in {"upload", "youtube_original_file"}
    if is_upload:
        if (
            start_url is not None
            or len(hops) != 1
            or not isinstance(hops[0], dict)
            or hops[0].get("url") is not None
        ):
            return _failure("unsupported", "invalid_upload_probe_transcript")
    else:
        canonical, error = _parse_https_url(start_url)
        if error:
            return _failure("unsupported", error or "invalid_url")
        first = hops[0]
        if not isinstance(first, dict):
            return _failure("unsupported", "invalid_probe_hop")
        first_canonical, first_error = _parse_https_url(first.get("url"))
        if first_error or canonical != first_canonical:
            return _failure("unsupported", "probe_start_url_mismatch")

    redirects = 0
    expected_next: str | None = None
    provider = source_kind
    final_response_seen = False
    for index, hop in enumerate(hops):
        if not isinstance(hop, dict):
            return _failure("unsupported", "invalid_probe_hop")
        if is_upload:
            if index != 0:
                return _failure("unsupported", "upload_redirect_forbidden")
        else:
            canonical, error = _parse_https_url(hop.get("url"))
            if error:
                return _failure("unsupported", error or "invalid_url")
            host = urlsplit(canonical or "").hostname or ""
            if not _host_matches_provider(host, provider):
                return _failure("unsupported", "redirect_provider_mismatch")
            if expected_next is not None and canonical != expected_next:
                return _failure("unsupported", "redirect_chain_mismatch")
            if not _public_ip_set(hop.get("resolved_ips")):
                return _failure("unsupported", "unsafe_dns_answer")

        status_code = hop.get("status")
        if type(status_code) is not int:
            return _failure("unsupported", "invalid_http_status")
        if status_code in REDIRECT_CODES:
            redirects += 1
            if redirects > MAX_REDIRECTS:
                return _failure("unsupported", "redirect_limit_exceeded")
            location = hop.get("location")
            if not isinstance(location, str) or not location:
                return _failure("unsupported", "redirect_location_missing")
            if is_upload:
                return _failure("unsupported", "upload_redirect_forbidden")
            expected_next, error = _parse_https_url(
                urljoin(canonical or "", location)
            )
            if error:
                return _failure("unsupported", "unsafe_redirect")
            next_host = urlsplit(expected_next or "").hostname or ""
            if not _host_matches_provider(next_host, provider):
                return _failure("unsupported", "redirect_provider_mismatch")
            continue
        if index != len(hops) - 1:
            return _failure("unsupported", "unexpected_hop_after_response")
        if status_code in (401, 403):
            return _failure("authorization_needed", "provider_denied_download")
        if status_code in (404, 410):
            return _failure("unsupported", "source_unavailable")
        if status_code == 206:
            return _failure("unsupported", "partial_response_not_accepted")
        if status_code != 200:
            return _failure("unsupported", "http_fetch_failed")
        final_response_seen = True

    if not final_response_seen:
        return _failure("unsupported", "redirect_chain_incomplete")

    final = hops[-1]
    headers = final.get("headers")
    if not isinstance(headers, dict):
        return _failure("unsupported", "response_headers_missing")
    headers = {str(key).lower(): str(value) for key, value in headers.items()}
    if "content-range" in headers:
        return _failure("unsupported", "partial_response_not_accepted")
    try:
        announced_bytes = int(headers["content-length"]) if "content-length" in headers else None
    except (TypeError, ValueError):
        return _failure("unsupported", "invalid_content_length")
    if announced_bytes is not None and announced_bytes < 0:
        return _failure("unsupported", "invalid_content_length")
    if announced_bytes is not None and announced_bytes > MAX_SOURCE_BYTES:
        return _failure("unsupported", "source_size_limit_exceeded")
    downloaded_bytes = final.get("downloaded_bytes")
    if type(downloaded_bytes) is not int or downloaded_bytes <= 0:
        return _failure("unsupported", "invalid_downloaded_byte_count")
    if downloaded_bytes > MAX_SOURCE_BYTES:
        return _failure("unsupported", "source_size_limit_exceeded")
    if announced_bytes is not None and announced_bytes != downloaded_bytes:
        return _failure("unsupported", "content_length_mismatch")

    reserve_source_bytes = announced_bytes if announced_bytes is not None else MAX_SOURCE_BYTES
    needed_free_bytes = reserve_source_bytes + MAX_DERIVED_BYTES + MIN_VOLUME_FREE_BYTES
    if disk_free_bytes < needed_free_bytes:
        return _failure("unsupported", "storage_reservation_unavailable")

    content_type = headers.get("content-type", "").split(";", 1)[0].strip().lower()
    body = final.get("body")
    if not isinstance(body, bytes) or len(body) != downloaded_bytes:
        return _failure("unsupported", "incomplete_probe_body")
    body_start = body[:256].lstrip(b"\xef\xbb\xbf \t\r\n").lower()
    if content_type == "text/html" or body_start.startswith((b"<!doctype html", b"<html", b"<head", b"<body")):
        if source_kind == "google_drive_public":
            return _failure("original_file_required", "drive_viewer_page_not_media")
        return _failure("unsupported", "non_media_response")
    container = _container_from_prefix(body[:128])
    if container not in ALLOWED_CONTAINERS:
        return _failure("unsupported", "unsupported_or_unrecognized_container")
    if content_type and content_type not in {
        "application/octet-stream",
        "video/mp4",
        "video/quicktime",
        "video/x-matroska",
        "video/webm",
    }:
        return _failure("unsupported", "non_media_response")

    probe = final.get("probe")
    if not isinstance(probe, dict):
        return _failure("unsupported", "media_probe_missing")
    if "container" in probe and probe["container"] != container:
        return _failure("unsupported", "container_probe_mismatch")
    duration_ms = probe.get("duration_ms")
    if type(duration_ms) is not int or duration_ms <= 0:
        return _failure("unsupported", "invalid_media_duration")
    if duration_ms > MAX_DURATION_MS:
        return _failure("unsupported", "duration_limit_exceeded")
    video_streams = _normalise_streams(probe.get("video_streams"), video=True)
    audio_streams = _normalise_streams(probe.get("audio_streams"), video=False)
    if not video_streams:
        return _failure("unsupported", "video_stream_missing_or_invalid")
    if audio_streams is None:
        return _failure("unsupported", "audio_stream_metadata_invalid")

    metadata = {
        "version": SOURCE_VERSION,
        "source_id": source_id,
        "source_kind": source_kind,
        "source_sha256": hashlib.sha256(body).hexdigest(),
        "source_bytes": downloaded_bytes,
        "duration_ms": duration_ms,
        "container": container,
        "video_streams": video_streams,
        "audio_streams": audio_streams,
        "acquisition_route": route,
        "authorization_state": "attested",
        "source_time_unit": "ms",
    }
    return {
        "status": "imported",
        "reason": "simulated_probe_validated",
        "metadata": metadata,
        "version": SOURCE_VERSION,
        "simulated": True,
    }
