"""Local v2 protocol fixtures; no Modal, model, provider, or network is used."""

import json
import os
import tempfile
import unittest
from pathlib import Path

from clipping_protocol import (
    MAX_ARTIFACT_BYTES,
    MAX_CALLBACK_GRACE_SECONDS,
    MAX_CALLBACK_OVERHEAD_BYTES,
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


MEDIA_CAP = "m" * 43
CALLBACK_CAP = "c" * 43
os.environ["FRAMEVAULT_CALLBACK_ORIGIN"] = "https://framevault.dev"
os.environ["FRAMEVAULT_PIPELINE_REVISION"] = "framevault_m3_worker_v1"


def dispatch_payload(**overrides):
    payload = {
        "protocol_version": PROTOCOL_VERSION,
        "job_id": "clip_job_01",
        "batch_id": "clip_batch_01",
        "source_id": "clip_source_01",
        "stage": "analysis",
        "dispatch_id": "clip_job_01:analysis",
        "attempt_id": "attempt_01",
        "media_url": "https://framevault.dev/api/clipping/worker-media/clip_job_01/attempt_01",
        "callback_url": "https://framevault.dev/api/clipping/worker-callbacks/clip_job_01/attempt_01",
        "source_duration_ms": 60_000,
        "expires_at": 200,
        "callback_expires_at": 200 + MAX_CALLBACK_GRACE_SECONDS,
        "source_sha256": "a" * 64,
        "content_type": "general",
        "rate_micro_usd_per_second": 50,
        "model": "large-v3",
        "max_compute_seconds": 90,
        "min_clip_seconds": 15,
        "max_clip_seconds": 180,
        "candidate_limit": 7,
        "pipeline_revision": "framevault_m3_worker_v1",
    }
    payload.update(overrides)
    if "media_url" not in overrides:
        payload["media_url"] = f"https://framevault.dev/api/clipping/worker-media/{payload['job_id']}/{payload['attempt_id']}"
    if "callback_url" not in overrides:
        payload["callback_url"] = f"https://framevault.dev/api/clipping/worker-callbacks/{payload['job_id']}/{payload['attempt_id']}"
    return payload


def valid_artifact(**overrides):
    artifact = {
        "type": "clipping.analysis",
        "schema_version": "2",
        "version": 1,
        "source_duration_ms": 60_000,
        "time_ranges": [],
        "payload": {
            "analysis_version": "framevault.analysis.source.v1",
            "pipeline_revision": "framevault_m3_worker_v1",
            "source_sha256": "a" * 64,
            "source_duration_ms": 60_000,
            "model_versions": {"transcription": "faster-whisper==1.1.1/large-v3", "model": "large-v3"},
            "algorithm_versions": {"audio_events": "audio-v1", "visual_events": "visual-v1"},
            "prompt_versions": {"analysis": "none"},
            "language": {"code": "en", "probability": 0.9, "script": "latin"},
            "transcript": {"original_script": True, "speaker_labels_available": False, "segments": []},
            "context": {"summary": "Extractive fixture.", "topics": [], "timeline": []},
            "audio_events": [],
            "visual_events": [],
            "cost_basis": "operator_declared_worker_second_rate_estimate",
            "rate_micro_usd_per_second": 50,
            "compute_seconds": 2,
            "cost_estimate_micro_usd": 100,
        },
    }
    artifact.update(overrides)
    return artifact


def valid_result(**overrides):
    result = StageResult(valid_artifact(), 100)
    return StageResult(overrides.get("artifact", result.artifact), overrides.get("estimate", result.cost_estimate_micro_usd))


class ClippingProtocolTests(unittest.TestCase):
    def test_v2_dispatch_is_exact_and_checks_hash_rate_profile_and_deadline(self):
        parsed = StageDispatch.parse(dispatch_payload(), now=100)
        self.assertEqual(parsed.protocol_version, "framevault.clipping.v2")
        self.assertEqual(parsed.model, "large-v3")
        for change in (
            {"media_url": "https://user:password@framevault.example/media"},
            {"callback_url": "https://framevault.example/callback?token=secret"},
            {"media_url": "https://attacker.dev/api/clipping/worker-media/clip_job_01/attempt_01"},
            {"media_url": "https://127.0.0.1/api/clipping/worker-media/clip_job_01/attempt_01"},
            {"callback_url": "https://framevault.dev/api/clipping/worker-callbacks/other_job/attempt_01"},
            {"expires_at": 100},
            {"source_sha256": "bad"},
            {"content_type": "unrecognized"},
            {"model": "some-other-model"},
            {"max_compute_seconds": 43_201},
            {"candidate_limit": 11},
            {"pipeline_revision": "untrusted_revision"},
            {"unexpected": "field"},
        ):
            with self.subTest(change=change), self.assertRaises(ProtocolError):
                StageDispatch.parse(dispatch_payload(**change), now=100)

    def test_queue_delay_is_allowed_and_worker_checks_absolute_lease(self):
        parsed = StageDispatch.parse(dispatch_payload(), now=150)
        self.assertEqual(parsed.expires_at, 200)
        self.assertEqual(parsed.max_compute_seconds, 90)

    def test_headers_keep_capabilities_outside_payload_and_result(self):
        headers = media_headers(MEDIA_CAP)
        self.assertEqual(headers["Authorization"], f"Bearer {MEDIA_CAP}")
        with tempfile.TemporaryDirectory() as directory:
            ledger = SQLiteDispatchLedger(Path(directory) / "worker-ledger.sqlite3")
            try:
                result = execute_stage(
                    dispatch_payload(),
                    media_token=MEDIA_CAP,
                    callback_token=CALLBACK_CAP,
                    ledger=ledger,
                    client=lambda _dispatch, _token: valid_result(),
                    now=100,
                )
            finally:
                ledger.close()
        encoded = json.dumps(result)
        self.assertNotIn(MEDIA_CAP, encoded)
        self.assertNotIn(CALLBACK_CAP, encoded)
        self.assertEqual(result["protocol_version"], PROTOCOL_VERSION)
        self.assertEqual(result["cost_estimate_micro_usd"], 100)
        self.assertEqual(result["cost_basis"], "operator_declared_worker_second_rate_estimate")
        self.assertNotIn("actual_cost_micro_usd", result)
        self.assertEqual(result["artifact"]["time_ranges"], [])
        self.assertEqual(result["artifact"]["schema_version"], "2")

    def test_completed_dispatch_replays_after_restart_without_inference(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "worker-ledger.sqlite3"
            calls = []
            first = SQLiteDispatchLedger(path)
            try:
                result = execute_stage(
                    dispatch_payload(), media_token=MEDIA_CAP, callback_token=CALLBACK_CAP,
                    ledger=first,
                    client=lambda _dispatch, _token: calls.append("inference") or valid_result(),
                    now=100,
                )
            finally:
                first.close()
            second = SQLiteDispatchLedger(path)
            try:
                replay = execute_stage(
                    dispatch_payload(), media_token=MEDIA_CAP, callback_token=CALLBACK_CAP,
                    ledger=second,
                    client=lambda _dispatch, _token: calls.append("incorrect-repeat") or valid_result(),
                    now=100,
                )
            finally:
                second.close()
        self.assertEqual(calls, ["inference"])
        self.assertEqual(replay, result)

    def test_acknowledgement_clears_callback_body_but_keeps_no_reinference_tombstone(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "worker-ledger.sqlite3"
            first = SQLiteDispatchLedger(path)
            dispatch = StageDispatch.parse(dispatch_payload(), now=100)
            try:
                execute_stage(
                    dispatch_payload(), media_token=MEDIA_CAP, callback_token=CALLBACK_CAP,
                    ledger=first, client=lambda _d, _t: valid_result(), now=100,
                )
                first.mark_delivered(dispatch)
            finally:
                first.close()
            restarted = SQLiteDispatchLedger(path)
            calls = []
            try:
                replay = execute_stage(
                    dispatch_payload(), media_token=MEDIA_CAP, callback_token=CALLBACK_CAP,
                    ledger=restarted,
                    client=lambda _d, _t: calls.append("incorrect-repeat") or valid_result(),
                    now=100,
                )
            finally:
                restarted.close()
        self.assertIsNone(replay)
        self.assertEqual(calls, [])

    def test_uncertain_running_dispatch_never_repeats_after_restart(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "worker-ledger.sqlite3"
            first = SQLiteDispatchLedger(path)
            dispatch = StageDispatch.parse(dispatch_payload(), now=100)
            self.assertEqual(first.claim(dispatch, now=100), (True, None))
            first.close()
            restarted = SQLiteDispatchLedger(path)
            calls = []
            try:
                with self.assertRaises(DuplicateAttempt):
                    execute_stage(
                        dispatch_payload(), media_token=MEDIA_CAP, callback_token=CALLBACK_CAP,
                        ledger=restarted,
                        client=lambda _d, _t: calls.append("incorrect-repeat") or valid_result(),
                        now=100,
                    )
            finally:
                restarted.close()
        self.assertEqual(calls, [])

    def test_uncertain_running_record_expires_at_media_lease(self):
        with tempfile.TemporaryDirectory() as directory:
            ledger = SQLiteDispatchLedger(Path(directory) / "worker-ledger.sqlite3")
            dispatch = StageDispatch.parse(dispatch_payload(), now=100)
            try:
                ledger.claim(dispatch, now=100)
                ledger.prune(now=200)
                self.assertEqual(ledger.claim(dispatch, now=200), (False, None))
            finally:
                ledger.close()

    def test_completed_result_replays_through_callback_grace_then_expires(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "worker-ledger.sqlite3"
            calls = []
            first = SQLiteDispatchLedger(path)
            try:
                completed = execute_stage(
                    dispatch_payload(), media_token=MEDIA_CAP, callback_token=CALLBACK_CAP,
                    ledger=first,
                    client=lambda _d, _t: calls.append("inference") or valid_result(),
                    now=100,
                )
            finally:
                first.close()
            restarted = SQLiteDispatchLedger(path)
            try:
                replay = execute_stage(
                    dispatch_payload(), media_token=MEDIA_CAP, callback_token=CALLBACK_CAP,
                    ledger=restarted,
                    client=lambda _d, _t: calls.append("incorrect-repeat") or valid_result(),
                    now=200,
                )
                final_grace_second = execute_stage(
                    dispatch_payload(), media_token=MEDIA_CAP, callback_token=CALLBACK_CAP,
                    ledger=restarted,
                    client=lambda _d, _t: calls.append("incorrect-repeat") or valid_result(),
                    now=200 + MAX_CALLBACK_GRACE_SECONDS - 1,
                )
                with self.assertRaises(ProtocolError):
                    execute_stage(
                        dispatch_payload(), media_token=MEDIA_CAP, callback_token=CALLBACK_CAP,
                        ledger=restarted,
                        client=lambda _d, _t: calls.append("incorrect-repeat") or valid_result(),
                        now=200 + MAX_CALLBACK_GRACE_SECONDS,
                    )
                dispatch = StageDispatch.parse(dispatch_payload(), now=100)
                after_callback_grace = restarted.claim(dispatch, now=200 + MAX_CALLBACK_GRACE_SECONDS)
            finally:
                restarted.close()
        self.assertEqual(calls, ["inference"])
        self.assertEqual(replay, completed)
        self.assertEqual(final_grace_second, completed)
        self.assertEqual(after_callback_grace, (False, None))

    def test_callback_retention_can_end_before_lease_and_exact_expiry_never_replays_or_reinfers(self):
        payload = dispatch_payload(callback_expires_at=150)
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "worker-ledger.sqlite3"
            calls = []
            ledger = SQLiteDispatchLedger(path)
            try:
                completed = execute_stage(
                    payload, media_token=MEDIA_CAP, callback_token=CALLBACK_CAP,
                    ledger=ledger,
                    client=lambda _d, _t: calls.append("inference") or valid_result(),
                    now=100,
                )
                dispatch = StageDispatch.parse(payload, now=100)
                at_retention_expiry = ledger.claim(dispatch, now=150)
                self.assertEqual(at_retention_expiry, (False, None))
                self.assertEqual(calls, ["inference"])
                with self.assertRaises(ProtocolError):
                    StageDispatch.parse(payload, now=150)
            finally:
                ledger.close()
        self.assertEqual(completed["attempt_id"], "attempt_01")

    def test_callback_expiry_must_be_future_and_no_later_than_lease_plus_grace(self):
        for value in (100, 200 + MAX_CALLBACK_GRACE_SECONDS + 1):
            with self.subTest(value=value), self.assertRaises(ProtocolError):
                StageDispatch.parse(dispatch_payload(callback_expires_at=value), now=100)

    def test_operator_rate_estimate_must_reconcile_and_never_claim_actual(self):
        with tempfile.TemporaryDirectory() as directory:
            ledger = SQLiteDispatchLedger(Path(directory) / "worker-ledger.sqlite3")
            try:
                with self.assertRaises(ProtocolError):
                    execute_stage(
                        dispatch_payload(), media_token=MEDIA_CAP, callback_token=CALLBACK_CAP,
                        ledger=ledger,
                        client=lambda _dispatch, _token: StageResult(valid_artifact(), 0),
                        now=100,
                    )
            finally:
                ledger.close()

    def test_running_attempt_can_be_retried_only_with_new_attempt_id(self):
        with tempfile.TemporaryDirectory() as directory:
            ledger = SQLiteDispatchLedger(Path(directory) / "worker-ledger.sqlite3")
            calls = []
            try:
                for attempt in ("attempt_01", "attempt_02"):
                    result = execute_stage(
                        dispatch_payload(attempt_id=attempt),
                        media_token=MEDIA_CAP,
                        callback_token=CALLBACK_CAP,
                        ledger=ledger,
                        client=lambda _dispatch, _token: calls.append("called") or valid_result(),
                        now=100,
                    )
                    self.assertEqual(result["attempt_id"], attempt)
            finally:
                ledger.close()
        self.assertEqual(len(calls), 2)

    def test_no_client_leaves_no_ledger_claim(self):
        with tempfile.TemporaryDirectory() as directory:
            ledger = SQLiteDispatchLedger(Path(directory) / "worker-ledger.sqlite3")
            try:
                with self.assertRaises(WorkerUnavailable):
                    execute_stage(
                        dispatch_payload(), media_token=MEDIA_CAP, callback_token=CALLBACK_CAP,
                        ledger=ledger, now=100,
                    )
                self.assertEqual(ledger.claim(StageDispatch.parse(dispatch_payload(), now=100), now=100), (True, None))
            finally:
                ledger.close()

    def test_source_core_contract_and_three_mib_bound_are_enforced(self):
        with tempfile.TemporaryDirectory() as directory:
            ledger = SQLiteDispatchLedger(Path(directory) / "worker-ledger.sqlite3")
            try:
                for attempt, change in (
                    ("attempt_bad_range", {"time_ranges": [{"start_ms": 100, "end_ms": 200}]}),
                    ("attempt_bad_hash", {"payload": {**valid_artifact()["payload"], "source_sha256": "b" * 64}}),
                    ("attempt_bad_duration", {"payload": {**valid_artifact()["payload"], "source_duration_ms": 59_999}}),
                    ("attempt_bad_rate", {"payload": {**valid_artifact()["payload"], "rate_micro_usd_per_second": True}}),
                    ("attempt_bad_estimate", {"payload": {**valid_artifact()["payload"], "cost_estimate_micro_usd": 99}}),
                ):
                    with self.subTest(attempt=attempt), self.assertRaises(ProtocolError):
                        execute_stage(
                            dispatch_payload(attempt_id=attempt),
                            media_token=MEDIA_CAP, callback_token=CALLBACK_CAP,
                            ledger=ledger,
                            client=lambda _d, _t, c=change: StageResult(valid_artifact(**c), 100),
                            now=100,
                        )
                def artifact_at_size(target_size):
                    artifact = valid_artifact(payload={**valid_artifact()["payload"], "large": ""})
                    base_size = len(json.dumps(artifact, ensure_ascii=False, separators=(",", ":")).encode("utf-8"))
                    artifact["payload"]["large"] = "x" * (target_size - base_size)
                    return artifact

                near_limit = artifact_at_size(int(2.9 * 1024 * 1024))
                near_limit_size = len(json.dumps(near_limit, ensure_ascii=False, separators=(",", ":")).encode("utf-8"))
                self.assertEqual(near_limit_size, int(2.9 * 1024 * 1024))
                accepted = execute_stage(
                    dispatch_payload(attempt_id="attempt_29mb"),
                    media_token=MEDIA_CAP,
                    callback_token=CALLBACK_CAP,
                    ledger=ledger,
                    client=lambda _d, _t: StageResult(near_limit, 100),
                    now=100,
                )
                self.assertEqual(accepted["artifact"]["payload"]["large"], near_limit["payload"]["large"])

                large = artifact_at_size(MAX_ARTIFACT_BYTES + 1)
                large_size = len(json.dumps(large, ensure_ascii=False, separators=(",", ":")).encode("utf-8"))
                self.assertEqual(large_size, MAX_ARTIFACT_BYTES + 1)
                with self.assertRaises(ProtocolError):
                    execute_stage(
                        dispatch_payload(attempt_id="attempt_over_3mb"),
                        media_token=MEDIA_CAP, callback_token=CALLBACK_CAP,
                        ledger=ledger,
                        client=lambda _d, _t: StageResult(large, 100),
                        now=100,
                    )
                self.assertEqual(MAX_CALLBACK_OVERHEAD_BYTES, 32 * 1024)
                overhead_dispatch = StageDispatch.parse(dispatch_payload(attempt_id="attempt_overhead"), now=100)
                self.assertEqual(ledger.claim(overhead_dispatch, now=100), (True, None))
                oversized_callback = {"result": "x" * (MAX_ARTIFACT_BYTES + MAX_CALLBACK_OVERHEAD_BYTES)}
                with self.assertRaises(ProtocolError):
                    ledger.complete(overhead_dispatch, oversized_callback, now=100)
            finally:
                ledger.close()


if __name__ == "__main__":
    unittest.main()
