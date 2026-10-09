"""Versioned, provider-neutral primitives for the FrameVault clipping worker.

The durable SQLite database is worker-owned and contains only an idempotency
record. A completed callback body may remain available for replay for at most
the exact callback capability expiry supplied in the dispatch; it is cleared
after the callback endpoint acknowledges it. Source media is never stored in
this database.
"""

from __future__ import annotations

import hashlib
import ipaddress
import json
import os
import re
import sqlite3
import threading
import time
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Callable, Mapping, Protocol
from urllib.parse import urlsplit


PROTOCOL_VERSION = "framevault.clipping.v2"
SUPPORTED_STAGE = "analysis"
SUPPORTED_MODEL = "large-v3"
SUPPORTED_CONTENT_TYPES = {"general", "podcast", "comedy", "gaming", "movie"}
MAX_SOURCE_DURATION_MS = 4 * 60 * 60 * 1000
MAX_ARTIFACT_BYTES = 3 * 1024 * 1024
MAX_CALLBACK_OVERHEAD_BYTES = 32 * 1024
MAX_SOURCE_BYTES = 20 * 1024 * 1024 * 1024
MAX_COMPUTE_SECONDS = 12 * 60 * 60
MAX_CALLBACK_GRACE_SECONDS = 24 * 60 * 60
MAX_CLIP_SECONDS = 180
MIN_CLIP_SECONDS = 15
MAX_RATE_MICRO_USD_PER_SECOND = (1 << 63) - 1
RESULT_CACHE_BYTES = 16 * 1024 * 1024
MAX_LEDGER_ROWS = 10_000
_SAFE_ID = re.compile(r"^[A-Za-z0-9_-]{1,200}$")
_SAFE_PIPELINE_REVISION = re.compile(r"^[A-Za-z0-9._-]{1,64}$")
_SHA256 = re.compile(r"^[a-f0-9]{64}$")
_CAPABILITY = re.compile(r"^[A-Za-z0-9_-]{32,256}$")
_COST_BASIS = "operator_declared_worker_second_rate_estimate"


class ProtocolError(ValueError):
    """A dispatch or result does not meet the versioned protocol."""


class DuplicateAttempt(RuntimeError):
    """The same attempt is already running or ended without a saved result."""


class WorkerUnavailable(RuntimeError):
    """The worker ledger cannot safely accept another dispatch."""


@dataclass(frozen=True)
class StageDispatch:
    protocol_version: str
    job_id: str
    batch_id: str
    source_id: str
    stage: str
    dispatch_id: str
    attempt_id: str
    media_url: str
    callback_url: str
    source_duration_ms: int
    expires_at: int
    callback_expires_at: int
    source_sha256: str
    content_type: str
    rate_micro_usd_per_second: int
    model: str
    max_compute_seconds: int
    min_clip_seconds: int
    max_clip_seconds: int
    candidate_limit: int
    pipeline_revision: str

    @classmethod
    def parse(
        cls,
        payload: Mapping[str, Any],
        now: int | None = None,
        *,
        allow_expired: bool = False,
        expected_origin: str | None = None,
        expected_pipeline_revision: str | None = None,
    ) -> "StageDispatch":
        if not isinstance(payload, Mapping):
            raise ProtocolError("dispatch must be an object")
        expected = {
            "protocol_version",
            "job_id",
            "batch_id",
            "source_id",
            "stage",
            "dispatch_id",
            "attempt_id",
            "media_url",
            "callback_url",
            "source_duration_ms",
            "expires_at",
            "callback_expires_at",
            "source_sha256",
            "content_type",
            "rate_micro_usd_per_second",
            "model",
            "max_compute_seconds",
            "min_clip_seconds",
            "max_clip_seconds",
            "candidate_limit",
            "pipeline_revision",
        }
        if set(payload) != expected:
            raise ProtocolError("dispatch fields do not match protocol version")
        if payload.get("protocol_version") != PROTOCOL_VERSION:
            raise ProtocolError("unsupported clipping protocol version")
        if payload.get("stage") != SUPPORTED_STAGE:
            raise ProtocolError("unsupported clipping stage")
        for field in ("job_id", "batch_id", "source_id", "attempt_id"):
            value = payload.get(field)
            if not isinstance(value, str) or not _SAFE_ID.fullmatch(value):
                raise ProtocolError(f"invalid {field}")
        dispatch_id = payload.get("dispatch_id")
        if not isinstance(dispatch_id, str) or not re.fullmatch(
            r"^[A-Za-z0-9_-]{1,200}:[A-Za-z0-9_-]{1,64}$", dispatch_id
        ):
            raise ProtocolError("invalid dispatch_id")

        media_url = _https_url(payload.get("media_url"), "media_url")
        callback_url = _https_url(payload.get("callback_url"), "callback_url")
        if expected_origin is None:
            expected_origin = os.environ.get("FRAMEVAULT_CALLBACK_ORIGIN")
        _validate_worker_targets(media_url, callback_url, payload["job_id"], payload["attempt_id"], expected_origin)
        pipeline_revision = payload.get("pipeline_revision")
        if not isinstance(pipeline_revision, str) or not _SAFE_PIPELINE_REVISION.fullmatch(pipeline_revision):
            raise ProtocolError("invalid pipeline revision")
        if expected_pipeline_revision is None:
            expected_pipeline_revision = os.environ.get("FRAMEVAULT_PIPELINE_REVISION")
        if not expected_pipeline_revision or pipeline_revision != expected_pipeline_revision:
            raise ProtocolError("dispatch pipeline revision does not match the configured revision")
        duration = _positive_int(payload.get("source_duration_ms"), "source duration")
        if duration > MAX_SOURCE_DURATION_MS:
            raise ProtocolError("source duration exceeds four hours")
        expires = _positive_int(payload.get("expires_at"), "dispatch expiry")
        current_time = int(time.time()) if now is None else now
        if expires <= current_time and not allow_expired:
            raise ProtocolError("dispatch has expired")
        if expires > current_time + MAX_COMPUTE_SECONDS:
            raise ProtocolError("dispatch expiry exceeds the worker lease limit")
        callback_expires = _positive_int(payload.get("callback_expires_at"), "callback expiry")
        if callback_expires <= current_time:
            raise ProtocolError("callback capability has expired")
        if callback_expires > expires + MAX_CALLBACK_GRACE_SECONDS:
            raise ProtocolError("callback expiry exceeds the supported grace period")

        source_sha256 = payload.get("source_sha256")
        if not isinstance(source_sha256, str) or not _SHA256.fullmatch(source_sha256):
            raise ProtocolError("source_sha256 must be a lowercase SHA-256 digest")
        content_type = payload.get("content_type")
        if content_type not in SUPPORTED_CONTENT_TYPES:
            raise ProtocolError("unsupported content profile")
        rate = _positive_int(payload.get("rate_micro_usd_per_second"), "worker second rate")
        if rate > MAX_RATE_MICRO_USD_PER_SECOND:
            raise ProtocolError("worker second rate is outside int64 bounds")
        if payload.get("model") != SUPPORTED_MODEL:
            raise ProtocolError("unsupported transcription model")
        max_compute = _positive_int(payload.get("max_compute_seconds"), "compute deadline")
        if max_compute > MAX_COMPUTE_SECONDS:
            raise ProtocolError("compute deadline exceeds the worker limit")
        minimum = _positive_int(payload.get("min_clip_seconds"), "minimum clip duration")
        maximum = _positive_int(payload.get("max_clip_seconds"), "maximum clip duration")
        if not MIN_CLIP_SECONDS <= minimum <= maximum <= MAX_CLIP_SECONDS:
            raise ProtocolError("clip durations must be within 15–180 seconds")
        candidate_limit = _positive_int(payload.get("candidate_limit"), "candidate limit")
        if candidate_limit > 10:
            raise ProtocolError("candidate limit exceeds the supported maximum")

        return cls(
            protocol_version=PROTOCOL_VERSION,
            job_id=payload["job_id"],
            batch_id=payload["batch_id"],
            source_id=payload["source_id"],
            stage=SUPPORTED_STAGE,
            dispatch_id=dispatch_id,
            attempt_id=payload["attempt_id"],
            media_url=media_url,
            callback_url=callback_url,
            source_duration_ms=duration,
            expires_at=expires,
            callback_expires_at=callback_expires,
            source_sha256=source_sha256,
            content_type=content_type,
            rate_micro_usd_per_second=rate,
            model=SUPPORTED_MODEL,
            max_compute_seconds=max_compute,
            min_clip_seconds=minimum,
            max_clip_seconds=maximum,
            candidate_limit=candidate_limit,
            pipeline_revision=pipeline_revision,
        )

    def fingerprint(self) -> str:
        encoded = json.dumps(self.as_dict(), sort_keys=True, separators=(",", ":")).encode("utf-8")
        return hashlib.sha256(encoded).hexdigest()

    def as_dict(self) -> dict[str, Any]:
        """Return only public dispatch data; scoped bearer caps are never fields."""

        return {
            "protocol_version": self.protocol_version,
            "job_id": self.job_id,
            "batch_id": self.batch_id,
            "source_id": self.source_id,
            "stage": self.stage,
            "dispatch_id": self.dispatch_id,
            "attempt_id": self.attempt_id,
            "media_url": self.media_url,
            "callback_url": self.callback_url,
            "source_duration_ms": self.source_duration_ms,
            "expires_at": self.expires_at,
            "callback_expires_at": self.callback_expires_at,
            "source_sha256": self.source_sha256,
            "content_type": self.content_type,
            "rate_micro_usd_per_second": self.rate_micro_usd_per_second,
            "model": self.model,
            "max_compute_seconds": self.max_compute_seconds,
            "min_clip_seconds": self.min_clip_seconds,
            "max_clip_seconds": self.max_clip_seconds,
            "candidate_limit": self.candidate_limit,
            "pipeline_revision": self.pipeline_revision,
        }


def _positive_int(value: Any, field: str) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or value <= 0:
        raise ProtocolError(f"invalid {field}")
    return value


def _https_url(value: Any, field: str) -> str:
    if not isinstance(value, str) or len(value) > 2048:
        raise ProtocolError(f"invalid {field}")
    try:
        parsed = urlsplit(value)
        _ = parsed.port
    except ValueError as error:
        raise ProtocolError(f"invalid {field}") from error
    if (
        parsed.scheme != "https"
        or not parsed.hostname
        or parsed.username is not None
        or parsed.password is not None
        or parsed.query
        or parsed.fragment
    ):
        raise ProtocolError(f"{field} must be a credential-free HTTPS URL without query parameters")
    return value


def _validate_worker_targets(
    media_url: str,
    callback_url: str,
    job_id: str,
    attempt_id: str,
    expected_origin: str | None,
) -> None:
    """Bind scoped capabilities to one configured public FrameVault origin."""

    if not isinstance(expected_origin, str) or not expected_origin:
        raise ProtocolError("FrameVault callback origin is not configured")
    try:
        origin = urlsplit(expected_origin)
        origin_port = origin.port
    except ValueError as error:
        raise ProtocolError("configured FrameVault origin is invalid") from error
    host = (origin.hostname or "").lower()
    if (
        origin.scheme != "https"
        or not host
        or origin.username is not None
        or origin.password is not None
        or origin.path not in ("", "/")
        or origin.query
        or origin.fragment
        or origin_port not in (None, 443)
        or not _is_public_dns_host(host)
    ):
        raise ProtocolError("configured FrameVault origin must be a public HTTPS origin on port 443")

    expected_paths = {
        media_url: f"/api/clipping/worker-media/{job_id}/{attempt_id}",
        callback_url: f"/api/clipping/worker-callbacks/{job_id}/{attempt_id}",
    }
    for url, expected_path in expected_paths.items():
        parsed = urlsplit(url)
        try:
            port = parsed.port
        except ValueError as error:
            raise ProtocolError("worker target URL has an invalid port") from error
        if (
            parsed.scheme != "https"
            or (parsed.hostname or "").lower() != host
            or port not in (None, 443)
            or parsed.path != expected_path
        ):
            raise ProtocolError("worker media and callback URLs must match the configured FrameVault endpoints")


def _is_public_dns_host(host: str) -> bool:
    if not host or host.endswith(".") or "." not in host:
        return False
    try:
        ipaddress.ip_address(host.strip("[]"))
        return False
    except ValueError:
        pass
    if any(host == suffix or host.endswith("." + suffix) for suffix in (
        "localhost", "local", "internal", "invalid", "test", "example", "onion",
    )):
        return False
    return True


def media_headers(token: str) -> dict[str, str]:
    _validate_capability(token, "media")
    return {"Authorization": f"Bearer {token}", "User-Agent": "FrameVault-Clipping/2.0"}


def callback_headers(token: str) -> dict[str, str]:
    _validate_capability(token, "callback")
    return {"Authorization": f"Bearer {token}", "User-Agent": "FrameVault-Clipping/2.0"}


def _validate_capability(token: str, name: str) -> None:
    if not isinstance(token, str) or not _CAPABILITY.fullmatch(token):
        raise ProtocolError(f"{name} capability is missing or invalid")


class DispatchLedger(Protocol):
    def claim(
        self,
        dispatch: StageDispatch,
        now: int | None = None,
        *,
        phase: str = "worker",
    ) -> tuple[bool, dict[str, Any] | None]: ...

    def complete(self, dispatch: StageDispatch, result: dict[str, Any]) -> None: ...

    def mark_delivered(self, dispatch: StageDispatch) -> None: ...


class SQLiteDispatchLedger:
    """Bounded, lease-expiring idempotency ledger on worker-owned storage."""

    def __init__(self, path: str | Path):
        self.path = Path(path)
        self.path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
        self._lock = threading.Lock()
        self._db = sqlite3.connect(str(self.path), timeout=30, isolation_level=None, check_same_thread=False)
        os.chmod(self.path, 0o600)
        self._db.execute("PRAGMA busy_timeout=30000")
        self._db.execute("PRAGMA journal_mode=DELETE")
        self._db.execute("PRAGMA secure_delete=ON")
        self._db.execute(
            """CREATE TABLE IF NOT EXISTS clipping_dispatches (
                dispatch_id TEXT NOT NULL,
                attempt_id TEXT NOT NULL,
                request_hash TEXT NOT NULL,
                status TEXT NOT NULL CHECK(status IN ('running','completed')),
                result_json TEXT NOT NULL DEFAULT '',
                created_at INTEGER NOT NULL,
                updated_at INTEGER NOT NULL,
                expires_at INTEGER NOT NULL DEFAULT 0,
                callback_expires_at INTEGER NOT NULL DEFAULT 0,
                result_expires_at INTEGER NOT NULL DEFAULT 0,
                worker_started INTEGER NOT NULL DEFAULT 1 CHECK(worker_started IN (0,1)),
                PRIMARY KEY(dispatch_id, attempt_id)
            )"""
        )
        columns = {row[1] for row in self._db.execute("PRAGMA table_info(clipping_dispatches)")}
        if "expires_at" not in columns:
            self._db.execute("ALTER TABLE clipping_dispatches ADD COLUMN expires_at INTEGER NOT NULL DEFAULT 0")
        if "result_expires_at" not in columns:
            self._db.execute("ALTER TABLE clipping_dispatches ADD COLUMN result_expires_at INTEGER NOT NULL DEFAULT 0")
        if "callback_expires_at" not in columns:
            self._db.execute("ALTER TABLE clipping_dispatches ADD COLUMN callback_expires_at INTEGER NOT NULL DEFAULT 0")
            # Older ledger rows did not retain the exact capability expiry.
            # Discard their transcript-bearing results rather than extending
            # data retention based on an inferred grace period.
            self._db.execute("UPDATE clipping_dispatches SET result_json='',result_expires_at=0 WHERE status='completed'")
        if "worker_started" not in columns:
            # Existing running rows were claimed directly by a worker; never
            # let a new invocation take over one during schema migration.
            self._db.execute("ALTER TABLE clipping_dispatches ADD COLUMN worker_started INTEGER NOT NULL DEFAULT 1")
        os.chmod(self.path, 0o600)

    def claim(
        self,
        dispatch: StageDispatch,
        now: int | None = None,
        *,
        phase: str = "worker",
    ) -> tuple[bool, dict[str, Any] | None]:
        if phase not in ("admit", "worker"):
            raise ProtocolError("invalid dispatch claim phase")
        current = int(time.time()) if now is None else now
        request_hash = dispatch.fingerprint()
        key = (dispatch.dispatch_id, dispatch.attempt_id)
        with self._lock:
            self._db.execute(
                "UPDATE clipping_dispatches SET result_json='' WHERE status='completed' AND result_json<>'' AND result_expires_at<=?",
                (current,),
            )
            self._db.execute(
                "DELETE FROM clipping_dispatches WHERE (status='running' AND expires_at<=?) OR (status='completed' AND callback_expires_at<=?)",
                (current, current),
            )
            # A source retention policy can shorten callback grace below the
            # stage lease. Never start inference when its result cannot be
            # delivered and retained for the callback capability.
            if dispatch.callback_expires_at <= current:
                return False, None
            self._db.execute("BEGIN IMMEDIATE")
            try:
                row = self._db.execute(
                    "SELECT request_hash,status,result_json,worker_started FROM clipping_dispatches WHERE dispatch_id=? AND attempt_id=?",
                    key,
                ).fetchone()
                if row is None:
                    if dispatch.expires_at <= current:
                        # An expired replay cannot claim inference. Returning a
                        # tombstone-like miss is safe because both capabilities
                        # are already outside the lease and no new callback is
                        # possible from this request.
                        self._db.execute("COMMIT")
                        return False, None
                    count = self._db.execute("SELECT COUNT(*) FROM clipping_dispatches").fetchone()[0]
                    if count >= MAX_LEDGER_ROWS:
                        raise WorkerUnavailable("worker idempotency ledger is at its entry limit")
                    self._db.execute(
                        "INSERT INTO clipping_dispatches(dispatch_id,attempt_id,request_hash,status,created_at,updated_at,expires_at,callback_expires_at,worker_started) VALUES(?,?,?,'running',?,?,?,?,?)",
                        (
                            *key,
                            request_hash,
                            current,
                            current,
                            dispatch.expires_at,
                            dispatch.callback_expires_at,
                            int(phase == "worker"),
                        ),
                    )
                    self._db.execute("COMMIT")
                    return True, None
                saved_hash, status, result_json, worker_started = row
                if saved_hash != request_hash:
                    raise ProtocolError("attempt identifiers cannot be reused for a different dispatch")
                if status == "completed":
                    result_expiry = self._db.execute(
                        "SELECT result_expires_at FROM clipping_dispatches WHERE dispatch_id=? AND attempt_id=?",
                        key,
                    ).fetchone()[0]
                    if not result_json or result_expiry <= current:
                        self._db.execute(
                            "UPDATE clipping_dispatches SET result_json='' WHERE dispatch_id=? AND attempt_id=?",
                            key,
                        )
                        self._db.execute("COMMIT")
                        return False, None
                    self._db.execute("COMMIT")
                    return False, json.loads(result_json) if result_json else None
                if status == "running" and phase == "admit":
                    self._db.execute("COMMIT")
                    return False, None
                if status == "running" and phase == "worker" and not worker_started:
                    cursor = self._db.execute(
                        "UPDATE clipping_dispatches SET worker_started=1,updated_at=? WHERE dispatch_id=? AND attempt_id=? AND status='running' AND worker_started=0",
                        (current, *key),
                    )
                    if cursor.rowcount == 1:
                        self._db.execute("COMMIT")
                        return True, None
                raise DuplicateAttempt("attempt is already recorded as running; do not execute it again")
            except Exception:
                if self._db.in_transaction:
                    self._db.execute("ROLLBACK")
                raise

    def complete(self, dispatch: StageDispatch, result: dict[str, Any], now: int | None = None) -> None:
        encoded = json.dumps(result, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
        encoded_bytes = len(encoded.encode("utf-8"))
        if encoded_bytes > MAX_ARTIFACT_BYTES + MAX_CALLBACK_OVERHEAD_BYTES:
            raise ProtocolError("stage result exceeds the protocol size limit")
        current = int(time.time()) if now is None else now
        # Go binds callback capability expiry to source retention and stage
        # grace. Honor that exact timestamp; never extend source transcript
        # retention based on a worker-side approximation.
        result_expiry = dispatch.callback_expires_at
        if result_expiry <= current:
            raise ProtocolError("callback capability expired before result caching")
        with self._lock:
            self._db.execute("BEGIN IMMEDIATE")
            try:
                key = (dispatch.dispatch_id, dispatch.attempt_id, dispatch.fingerprint())
                exists = self._db.execute(
                    "SELECT status,worker_started FROM clipping_dispatches WHERE dispatch_id=? AND attempt_id=? AND request_hash=?",
                    key,
                ).fetchone()
                if exists is None or exists[0] != "running" or not exists[1]:
                    raise DuplicateAttempt("dispatch ledger no longer owns this attempt")
                total = sum(
                    len(saved.encode("utf-8"))
                    for (saved,) in self._db.execute(
                        "SELECT result_json FROM clipping_dispatches WHERE status='completed' AND result_json<>''"
                    )
                )
                if total + encoded_bytes > RESULT_CACHE_BYTES:
                    raise WorkerUnavailable("worker callback replay cache is at its byte limit")
                cursor = self._db.execute(
                    "UPDATE clipping_dispatches SET status='completed',result_json=?,updated_at=?,result_expires_at=? WHERE dispatch_id=? AND attempt_id=? AND request_hash=? AND status='running'",
                    (encoded, current, result_expiry, *key),
                )
                if cursor.rowcount != 1:
                    raise DuplicateAttempt("dispatch ledger no longer owns this attempt")
                self._db.execute("COMMIT")
            except Exception:
                if self._db.in_transaction:
                    self._db.execute("ROLLBACK")
                raise
        os.chmod(self.path, 0o600)

    def mark_delivered(self, dispatch: StageDispatch) -> None:
        """Drop transcript-bearing callback content after a receiver ack."""

        with self._lock:
            cursor = self._db.execute(
                "UPDATE clipping_dispatches SET result_json='',result_expires_at=?,updated_at=? WHERE dispatch_id=? AND attempt_id=? AND request_hash=? AND status='completed'",
                (int(time.time()), int(time.time()), dispatch.dispatch_id, dispatch.attempt_id, dispatch.fingerprint()),
            )
            if cursor.rowcount not in (0, 1):
                raise DuplicateAttempt("dispatch ledger could not mark the callback delivered")

    def prune(self, now: int | None = None) -> None:
        current = int(time.time()) if now is None else now
        with self._lock:
            self._db.execute(
                "UPDATE clipping_dispatches SET result_json='' WHERE status='completed' AND result_json<>'' AND result_expires_at<=?",
                (current,),
            )
            self._db.execute(
                "DELETE FROM clipping_dispatches WHERE (status='running' AND expires_at<=?) OR (status='completed' AND callback_expires_at<=?)",
                (current, current),
            )

    def close(self) -> None:
        with self._lock:
            self._db.close()


@dataclass(frozen=True)
class StageResult:
    """Analysis artifact with an operator-rate estimate, not Modal billing."""

    artifact: Mapping[str, Any]
    cost_estimate_micro_usd: int
    cost_basis: str = _COST_BASIS


StageClient = Callable[[StageDispatch, str], StageResult]


def execute_stage(
    payload: Mapping[str, Any],
    *,
    media_token: str,
    callback_token: str,
    ledger: DispatchLedger,
    client: StageClient | None = None,
    now: int | None = None,
    expected_origin: str | None = None,
    expected_pipeline_revision: str | None = None,
    on_claim: Callable[[], None] | None = None,
    on_complete: Callable[[], None] | None = None,
) -> dict[str, Any] | None:
    """Run one injected analysis and save its callback for restart-safe replay."""

    dispatch = StageDispatch.parse(
        payload,
        now=now,
        allow_expired=True,
        expected_origin=expected_origin,
        expected_pipeline_revision=expected_pipeline_revision,
    )
    media_headers(media_token)
    callback_headers(callback_token)
    # Preserve an unclaimed dispatch if no runtime client has been configured.
    if client is None:
        raise WorkerUnavailable("analysis runtime is not configured")
    claimed, cached = ledger.claim(dispatch, now=now)
    if not claimed:
        return cached
    if on_claim is not None:
        on_claim()
    stage_result = client(dispatch, media_token)
    result = canonical_callback(dispatch, stage_result)
    ledger.complete(dispatch, result, now=now)
    if on_complete is not None:
        on_complete()
    return result


def canonical_callback(dispatch: StageDispatch, stage_result: StageResult) -> dict[str, Any]:
    if not isinstance(stage_result, StageResult):
        raise ProtocolError("stage client must return an artifact and an operator-rate cost estimate")
    estimate = stage_result.cost_estimate_micro_usd
    if isinstance(estimate, bool) or not isinstance(estimate, int) or not 0 < estimate <= (1 << 63) - 1:
        raise ProtocolError("cost_estimate_micro_usd must be a positive int64")
    if stage_result.cost_basis != _COST_BASIS:
        raise ProtocolError("unsupported worker cost basis")
    raw_artifact = stage_result.artifact
    if not isinstance(raw_artifact, Mapping):
        raise ProtocolError("stage client must return an artifact object")
    expected = {"type", "schema_version", "version", "source_duration_ms", "time_ranges", "payload"}
    if set(raw_artifact) != expected:
        raise ProtocolError("artifact fields do not match canonical schema")
    if raw_artifact["type"] != "clipping.analysis" or raw_artifact["schema_version"] != "2" or raw_artifact["version"] != 1:
        raise ProtocolError("analysis artifact must use clipping.analysis schema 2")
    for field in ("version", "source_duration_ms"):
        value = raw_artifact[field]
        if isinstance(value, bool) or not isinstance(value, int) or value < 1:
            raise ProtocolError(f"invalid artifact {field}")
    if raw_artifact["source_duration_ms"] != dispatch.source_duration_ms:
        raise ProtocolError("artifact source duration does not match dispatch")
    ranges = raw_artifact["time_ranges"]
    if ranges != []:
        raise ProtocolError("source-core artifact time_ranges must be empty")
    payload = raw_artifact["payload"]
    if not isinstance(payload, Mapping):
        raise ProtocolError("analysis payload must be an object")
    if payload.get("source_sha256") != dispatch.source_sha256:
        raise ProtocolError("analysis source hash does not match the dispatch")
    if payload.get("source_duration_ms") != dispatch.source_duration_ms:
        raise ProtocolError("analysis source duration does not match the dispatch")
    if payload.get("pipeline_revision") != dispatch.pipeline_revision:
        raise ProtocolError("analysis pipeline revision does not match the dispatch")
    compute_seconds = payload.get("compute_seconds")
    rate = payload.get("rate_micro_usd_per_second")
    payload_estimate = payload.get("cost_estimate_micro_usd")
    if (
        isinstance(compute_seconds, bool)
        or not isinstance(compute_seconds, int)
        or not 1 <= compute_seconds <= dispatch.max_compute_seconds
        or isinstance(rate, bool)
        or not isinstance(rate, int)
        or rate != dispatch.rate_micro_usd_per_second
        or isinstance(payload_estimate, bool)
        or not isinstance(payload_estimate, int)
        or payload_estimate != estimate
    ):
        raise ProtocolError("analysis cost metadata does not match the dispatch")
    expected_estimate = compute_seconds * rate
    if expected_estimate != estimate or expected_estimate > (1 << 63) - 1:
        raise ProtocolError("operator-rate cost estimate cannot be reconciled")
    if payload.get("cost_basis") != _COST_BASIS:
        raise ProtocolError("analysis payload must state the operator-rate estimate basis")
    try:
        artifact_json = json.dumps(raw_artifact, ensure_ascii=False, separators=(",", ":"))
    except (TypeError, ValueError) as error:
        raise ProtocolError("artifact is not valid JSON") from error
    if len(artifact_json.encode("utf-8")) > MAX_ARTIFACT_BYTES:
            raise ProtocolError("worker source-core artifact exceeds 3 MiB")
    return {
        "protocol_version": PROTOCOL_VERSION,
        "job_id": dispatch.job_id,
        "stage": dispatch.stage,
        "dispatch_id": dispatch.dispatch_id,
        "attempt_id": dispatch.attempt_id,
        "status": "completed",
        "cost_estimate_micro_usd": estimate,
        "cost_basis": _COST_BASIS,
        "artifact": json.loads(artifact_json),
    }
