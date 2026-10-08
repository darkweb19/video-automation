"""Provider-neutral protocol primitives for FrameVault clipping stages.

This module intentionally does not implement transcription or video analysis.
It validates versioned dispatches and makes an injected stage client safe to
retry after an uncertain worker response. The SQLite ledger belongs to the
worker runtime and must use worker-owned durable storage; it must never point
at the Go application's database or media volume.
"""

from __future__ import annotations

import hashlib
import json
import re
import sqlite3
import threading
import time
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Callable, Mapping, Protocol
from urllib.parse import urlsplit


PROTOCOL_VERSION = "framevault.clipping.v1"
SUPPORTED_STAGE = "analysis"
MAX_SOURCE_DURATION_MS = 4 * 60 * 60 * 1000
MAX_ARTIFACT_BYTES = 4 * 1024 * 1024
_SAFE_ID = re.compile(r"^[A-Za-z0-9_-]{1,200}$")


class ProtocolError(ValueError):
    """A dispatch or result does not meet the versioned protocol."""


class DuplicateAttempt(RuntimeError):
    """The same attempt is already running or ended without a saved result."""


class WorkerUnavailable(RuntimeError):
    """No stage implementation is configured for this protocol stage."""


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

    @classmethod
    def parse(cls, payload: Mapping[str, Any], now: int | None = None) -> "StageDispatch":
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
        if not isinstance(dispatch_id, str) or not re.fullmatch(r"^[A-Za-z0-9_-]{1,200}:[A-Za-z0-9_-]{1,64}$", dispatch_id):
            raise ProtocolError("invalid dispatch_id")
        media_url = _https_url(payload.get("media_url"), "media_url")
        callback_url = _https_url(payload.get("callback_url"), "callback_url")
        duration = payload.get("source_duration_ms")
        expires = payload.get("expires_at")
        if isinstance(duration, bool) or not isinstance(duration, int) or not 0 < duration <= MAX_SOURCE_DURATION_MS:
            raise ProtocolError("invalid source duration")
        if isinstance(expires, bool) or not isinstance(expires, int):
            raise ProtocolError("invalid dispatch expiry")
        current_time = int(time.time()) if now is None else now
        if expires <= current_time:
            raise ProtocolError("dispatch has expired")
        if expires > current_time + 24 * 60 * 60:
            raise ProtocolError("dispatch expiry is too far in the future")
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
        )

    def fingerprint(self) -> str:
        encoded = json.dumps(self.as_dict(), sort_keys=True, separators=(",", ":")).encode("utf-8")
        return hashlib.sha256(encoded).hexdigest()

    def as_dict(self) -> dict[str, Any]:
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
        }


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


def media_headers(token: str) -> dict[str, str]:
    if not token or "\r" in token or "\n" in token:
        raise ProtocolError("media capability is missing or invalid")
    return {"Authorization": f"Bearer {token}", "User-Agent": "FrameVault-Clipping/1.0"}


def callback_headers(token: str) -> dict[str, str]:
    if not token or "\r" in token or "\n" in token:
        raise ProtocolError("callback capability is missing or invalid")
    return {"Authorization": f"Bearer {token}", "User-Agent": "FrameVault-Clipping/1.0"}


class DispatchLedger(Protocol):
    def claim(self, dispatch: StageDispatch) -> tuple[bool, dict[str, Any] | None]: ...

    def complete(self, dispatch: StageDispatch, result: dict[str, Any]) -> None: ...


class SQLiteDispatchLedger:
    """Durable per-attempt idempotency ledger for worker-owned storage."""

    def __init__(self, path: str | Path):
        self._lock = threading.Lock()
        self._db = sqlite3.connect(str(path), timeout=30, isolation_level=None, check_same_thread=False)
        self._db.execute("PRAGMA busy_timeout=30000")
        self._db.execute("PRAGMA journal_mode=WAL")
        self._db.execute(
            """CREATE TABLE IF NOT EXISTS clipping_dispatches (
                dispatch_id TEXT NOT NULL,
                attempt_id TEXT NOT NULL,
                request_hash TEXT NOT NULL,
                status TEXT NOT NULL CHECK(status IN ('running','completed')),
                result_json TEXT NOT NULL DEFAULT '',
                created_at INTEGER NOT NULL,
                updated_at INTEGER NOT NULL,
                PRIMARY KEY(dispatch_id, attempt_id)
            )"""
        )

    def claim(self, dispatch: StageDispatch) -> tuple[bool, dict[str, Any] | None]:
        request_hash = dispatch.fingerprint()
        key = (dispatch.dispatch_id, dispatch.attempt_id)
        with self._lock:
            self._db.execute("BEGIN IMMEDIATE")
            try:
                row = self._db.execute(
                    "SELECT request_hash,status,result_json FROM clipping_dispatches WHERE dispatch_id=? AND attempt_id=?",
                    key,
                ).fetchone()
                if row is None:
                    now = int(time.time())
                    self._db.execute(
                        "INSERT INTO clipping_dispatches(dispatch_id,attempt_id,request_hash,status,created_at,updated_at) VALUES(?,?,?,'running',?,?)",
                        (*key, request_hash, now, now),
                    )
                    self._db.execute("COMMIT")
                    return True, None
                saved_hash, status, result_json = row
                if saved_hash != request_hash:
                    raise ProtocolError("attempt identifiers cannot be reused for a different dispatch")
                if status == "completed":
                    self._db.execute("COMMIT")
                    return False, json.loads(result_json)
                raise DuplicateAttempt("attempt is already recorded as running; do not execute it again")
            except Exception:
                if self._db.in_transaction:
                    self._db.execute("ROLLBACK")
                raise

    def complete(self, dispatch: StageDispatch, result: dict[str, Any]) -> None:
        encoded = json.dumps(result, sort_keys=True, separators=(",", ":"))
        if len(encoded.encode("utf-8")) > MAX_ARTIFACT_BYTES + 32 * 1024:
            raise ProtocolError("stage result exceeds the protocol size limit")
        with self._lock:
            cursor = self._db.execute(
                "UPDATE clipping_dispatches SET status='completed',result_json=?,updated_at=? WHERE dispatch_id=? AND attempt_id=? AND request_hash=? AND status='running'",
                (encoded, int(time.time()), dispatch.dispatch_id, dispatch.attempt_id, dispatch.fingerprint()),
            )
            if cursor.rowcount != 1:
                raise DuplicateAttempt("dispatch ledger no longer owns this attempt")

    def close(self) -> None:
        with self._lock:
            self._db.close()


@dataclass(frozen=True)
class StageResult:
    """Provider-neutral output with an explicit, integer microUSD settlement."""

    artifact: Mapping[str, Any]
    actual_cost_micro_usd: int


StageClient = Callable[[StageDispatch, str], StageResult]


def execute_stage(
    payload: Mapping[str, Any],
    *,
    media_token: str,
    callback_token: str,
    ledger: DispatchLedger,
    client: StageClient | None = None,
    now: int | None = None,
) -> dict[str, Any]:
    """Run an injected provider-neutral client once and persist its result.

    A running attempt found after a process restart is treated as uncertain and
    is never repeated automatically. A caller may retry only by receiving a
    new attempt_id from Go after an explicit retry decision.
    """

    dispatch = StageDispatch.parse(payload, now=now)
    media_headers(media_token)
    callback_headers(callback_token)
    # Do not poison the durable ledger when M2 has no client configured. The
    # same attempt may proceed later after an explicit provider configuration.
    if client is None:
        raise WorkerUnavailable("analysis is not implemented in this M2 protocol worker")
    claimed, cached = ledger.claim(dispatch)
    if not claimed:
        assert cached is not None
        return cached
    raw_result = client(dispatch, media_token)
    result = canonical_callback(dispatch, raw_result)
    ledger.complete(dispatch, result)
    return result


def canonical_callback(dispatch: StageDispatch, stage_result: StageResult) -> dict[str, Any]:
    if not isinstance(stage_result, StageResult):
        raise ProtocolError("stage client must return an artifact and actual microUSD cost")
    actual_cost = stage_result.actual_cost_micro_usd
    if isinstance(actual_cost, bool) or not isinstance(actual_cost, int) or actual_cost < 0 or actual_cost > (1 << 63) - 1:
        raise ProtocolError("actual_cost_micro_usd must be a nonnegative int64")
    raw_artifact = stage_result.artifact
    if not isinstance(raw_artifact, Mapping):
        raise ProtocolError("stage client must return an artifact object")
    expected = {"type", "schema_version", "version", "source_duration_ms", "time_ranges", "payload"}
    if set(raw_artifact) != expected:
        raise ProtocolError("artifact fields do not match canonical schema")
    if not isinstance(raw_artifact["type"], str) or not raw_artifact["type"]:
        raise ProtocolError("artifact type is required")
    schema_version = raw_artifact["schema_version"]
    if not isinstance(schema_version, str) or not schema_version or len(schema_version) > 128:
        raise ProtocolError("invalid artifact schema_version")
    for field in ("version", "source_duration_ms"):
        value = raw_artifact[field]
        if isinstance(value, bool) or not isinstance(value, int) or value < 1:
            raise ProtocolError(f"invalid artifact {field}")
    if raw_artifact["source_duration_ms"] != dispatch.source_duration_ms:
        raise ProtocolError("artifact source duration does not match dispatch")
    ranges = raw_artifact["time_ranges"]
    if not isinstance(ranges, list) or len(ranges) > 1000:
        raise ProtocolError("invalid artifact time ranges")
    for item in ranges:
        if not isinstance(item, Mapping) or set(item) != {"start_ms", "end_ms"}:
            raise ProtocolError("time ranges must contain start_ms and end_ms")
        start, end = item["start_ms"], item["end_ms"]
        if (
            isinstance(start, bool)
            or isinstance(end, bool)
            or not isinstance(start, int)
            or not isinstance(end, int)
            or start < 0
            or end <= start
            or end > dispatch.source_duration_ms
        ):
            raise ProtocolError("artifact time range is outside source bounds")
    try:
        artifact_json = json.dumps(raw_artifact, ensure_ascii=False, separators=(",", ":"))
    except (TypeError, ValueError) as error:
        raise ProtocolError("artifact is not valid JSON") from error
    if len(artifact_json.encode("utf-8")) > MAX_ARTIFACT_BYTES:
        raise ProtocolError("artifact exceeds 4 MiB")
    return {
        "protocol_version": PROTOCOL_VERSION,
        "job_id": dispatch.job_id,
        "stage": dispatch.stage,
        "dispatch_id": dispatch.dispatch_id,
        "attempt_id": dispatch.attempt_id,
        "status": "completed",
        "actual_cost_micro_usd": actual_cost,
        "artifact": json.loads(artifact_json),
    }
