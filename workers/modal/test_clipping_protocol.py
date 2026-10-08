"""Local protocol fixtures; no Modal, provider, or network access is used."""

import json
import tempfile
import unittest
from pathlib import Path

from clipping_protocol import (
    MAX_ARTIFACT_BYTES,
    PROTOCOL_VERSION,
    DuplicateAttempt,
    ProtocolError,
    SQLiteDispatchLedger,
    StageDispatch,
    StageResult,
    WorkerUnavailable,
    execute_stage,
    media_headers,
)


def dispatch_payload(**overrides):
    payload = {
        "protocol_version": PROTOCOL_VERSION,
        "job_id": "clip_job_01",
        "batch_id": "clip_batch_01",
        "source_id": "clip_source_01",
        "stage": "analysis",
        "dispatch_id": "clip_job_01:analysis",
        "attempt_id": "attempt_01",
        "media_url": "https://framevault.example/api/clipping/worker-media/clip_job_01/attempt_01",
        "callback_url": "https://framevault.example/api/clipping/worker-callbacks/clip_job_01/attempt_01",
        "source_duration_ms": 60_000,
        "expires_at": 200,
    }
    payload.update(overrides)
    return payload


def valid_artifact(**overrides):
    artifact = {
        "type": "clipping.analysis",
        "schema_version": "1",
        "version": 1,
        "source_duration_ms": 60_000,
        "time_ranges": [{"start_ms": 1000, "end_ms": 2000}],
        "payload": {"fixture": True, "note": "Injected local fixture only."},
    }
    artifact.update(overrides)
    return artifact


class ClippingProtocolTests(unittest.TestCase):
    def test_dispatch_rejects_credentials_in_urls_and_expiry(self):
        for change in (
            {"media_url": "https://user:password@framevault.example/media"},
            {"callback_url": "https://framevault.example/callback?token=secret"},
            {"expires_at": 100},
        ):
            with self.subTest(change=change), self.assertRaises(ProtocolError):
                StageDispatch.parse(dispatch_payload(**change), now=100)

    def test_headers_carry_bearer_capabilities_outside_payload(self):
        media = media_headers("media-capability-secret")
        self.assertEqual(media["Authorization"], "Bearer media-capability-secret")
        with tempfile.TemporaryDirectory() as directory:
            ledger = SQLiteDispatchLedger(Path(directory) / "worker-ledger.sqlite3")
            try:
                result = execute_stage(
                    dispatch_payload(),
                    media_token="media-capability-secret",
                    callback_token="callback-capability-secret",
                    ledger=ledger,
                    client=lambda _dispatch, _token: StageResult(valid_artifact(), 0),
                    now=100,
                )
            finally:
                ledger.close()
        encoded = json.dumps(result)
        self.assertNotIn("media-capability-secret", encoded)
        self.assertNotIn("callback-capability-secret", encoded)
        self.assertEqual(result["status"], "completed")
        self.assertEqual(result["artifact"]["time_ranges"], [{"start_ms": 1000, "end_ms": 2000}])

    def test_completed_dispatch_replay_after_restart_returns_cached_result(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "worker-ledger.sqlite3"
            calls = []
            first = SQLiteDispatchLedger(path)
            try:
                result = execute_stage(
                    dispatch_payload(),
                    media_token="media",
                    callback_token="callback",
                    ledger=first,
                    client=lambda _dispatch, _token: calls.append("called") or StageResult(valid_artifact(), 37),
                    now=100,
                )
            finally:
                first.close()
            second = SQLiteDispatchLedger(path)
            try:
                replay = execute_stage(
                    dispatch_payload(),
                    media_token="media",
                    callback_token="callback",
                    ledger=second,
                    client=lambda _dispatch, _token: calls.append("called-again") or StageResult(valid_artifact(), 37),
                    now=100,
                )
            finally:
                second.close()
        self.assertEqual(calls, ["called"])
        self.assertEqual(replay, result)
        self.assertEqual(replay["actual_cost_micro_usd"], 37)

    def test_stage_result_cost_must_be_nonnegative_int64(self):
        with tempfile.TemporaryDirectory() as directory:
            ledger = SQLiteDispatchLedger(Path(directory) / "worker-ledger.sqlite3")
            try:
                for attempt, value in enumerate((-1, True, 1 << 63), start=1):
                    with self.subTest(value=value), self.assertRaises(ProtocolError):
                        execute_stage(
                            dispatch_payload(attempt_id=f"attempt_cost_{attempt}"),
                            media_token="media",
                            callback_token="callback",
                            ledger=ledger,
                            client=lambda _dispatch, _token, cost=value: StageResult(valid_artifact(), cost),
                            now=100,
                        )
            finally:
                ledger.close()

    def test_running_dispatch_after_restart_is_uncertain_and_never_repeated(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "worker-ledger.sqlite3"
            first = SQLiteDispatchLedger(path)
            dispatch = StageDispatch.parse(dispatch_payload(), now=100)
            self.assertEqual(first.claim(dispatch), (True, None))
            first.close()

            calls = []
            restarted = SQLiteDispatchLedger(path)
            try:
                with self.assertRaises(DuplicateAttempt):
                    execute_stage(
                        dispatch_payload(),
                        media_token="media",
                        callback_token="callback",
                        ledger=restarted,
                        client=lambda _dispatch, _token: calls.append("incorrect-reexecution") or valid_artifact(),
                        now=100,
                    )
            finally:
                restarted.close()
        self.assertEqual(calls, [])

    def test_explicit_new_attempt_can_run_without_reusing_completed_attempt(self):
        with tempfile.TemporaryDirectory() as directory:
            ledger = SQLiteDispatchLedger(Path(directory) / "worker-ledger.sqlite3")
            calls = []
            try:
                for attempt in ("attempt_01", "attempt_02"):
                    result = execute_stage(
                        dispatch_payload(attempt_id=attempt),
                        media_token="media",
                        callback_token="callback",
                        ledger=ledger,
                        client=lambda _dispatch, _token: calls.append("called") or StageResult(valid_artifact(), 0),
                        now=100,
                    )
                    self.assertEqual(result["attempt_id"], attempt)
            finally:
                ledger.close()
        self.assertEqual(len(calls), 2)

    def test_worker_has_no_analysis_implementation_by_default(self):
        with tempfile.TemporaryDirectory() as directory:
            ledger = SQLiteDispatchLedger(Path(directory) / "worker-ledger.sqlite3")
            try:
                with self.assertRaises(WorkerUnavailable):
                    execute_stage(
                        dispatch_payload(),
                        media_token="media",
                        callback_token="callback",
                        ledger=ledger,
                        now=100,
                    )
                result = execute_stage(
                    dispatch_payload(),
                    media_token="media",
                    callback_token="callback",
                    ledger=ledger,
                    client=lambda _dispatch, _token: StageResult(valid_artifact(), 0),
                    now=100,
                )
                self.assertEqual(result["status"], "completed")
            finally:
                ledger.close()

    def test_artifact_bounds_and_size_are_enforced(self):
        with tempfile.TemporaryDirectory() as directory:
            ledger = SQLiteDispatchLedger(Path(directory) / "worker-ledger.sqlite3")
            try:
                with self.assertRaises(ProtocolError):
                    execute_stage(
                        dispatch_payload(attempt_id="attempt_bad_range"),
                        media_token="media",
                        callback_token="callback",
                        ledger=ledger,
                        client=lambda _dispatch, _token: StageResult(valid_artifact(
                            time_ranges=[{"start_ms": 59_000, "end_ms": 61_000}]
                        ), 0),
                        now=100,
                    )
                with self.assertRaises(ProtocolError):
                    execute_stage(
                        dispatch_payload(attempt_id="attempt_large"),
                        media_token="media",
                        callback_token="callback",
                        ledger=ledger,
                        client=lambda _dispatch, _token: StageResult(valid_artifact(payload={"text": "x" * MAX_ARTIFACT_BYTES}), 0),
                        now=100,
                    )
            finally:
                ledger.close()


if __name__ == "__main__":
    unittest.main()
