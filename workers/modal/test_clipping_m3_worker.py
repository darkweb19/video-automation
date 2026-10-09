"""Offline M3 worker transport and callback-cache fixtures."""

import asyncio
import ast
import hashlib
import importlib.util
import ipaddress
import json
import os
import shutil
import socket
import sys
import subprocess
import tempfile
import threading
import time
from types import SimpleNamespace
import unittest
from pathlib import Path
from unittest.mock import patch
from urllib import request as urllib_request

from clipping_analysis import AnalysisError, WorkCancelled, analyze_source
from clipping_m3_worker import (
    COMPUTE_COST_BASIS,
    DISPATCH_API_SECRET_NAME,
    MODEL_PREWARM_SECRET_NAME,
    RUNTIME_SECRET_NAME,
    WORKER_MODEL_REVISION_KEY,
    WORKER_ORIGIN_KEY,
    WORKER_PIPELINE_REVISION_KEY,
    WORKER_SECRET_KEY,
    ComputeDeadlineExceeded,
    DispatchBodyTooLarge,
    MAX_DISPATCH_BODY_BYTES,
    NoRedirectHandler,
    PinnedConnectionError,
    PinnedHTTPSConnection,
    PinnedHTTPSHandler,
    RemoteDispatchLedger,
    WorkGuard,
    _run_bounded_process,
    _opener,
    _valid_dispatch_shared_secret,
    analyze_dispatch,
    build_source_core_payload,
    create_private_temp_file,
    deliver_callback,
    dispatch_admission,
    dispatch_ack,
    download_media,
    ledger_rpc_operation,
    read_limited_body,
    replay_cached_callback,
    run_ledger_rpc,
    clipping_analysis_task,
    clipping_callback_replay_task,
    clipping_dispatch_api,
    clipping_ledger_owner,
    clipping_model_prewarm_task,
    process_dispatch,
    verify_media_file,
)
from clipping_protocol import (
    MAX_CALLBACK_GRACE_SECONDS,
    PROTOCOL_VERSION,
    SQLiteDispatchLedger,
    StageDispatch,
    StageResult,
    canonical_callback,
)


MEDIA_CAP = "m" * 43
CALLBACK_CAP = "c" * 43
os.environ["FRAMEVAULT_CALLBACK_ORIGIN"] = "https://framevault.dev"
os.environ["FRAMEVAULT_PIPELINE_REVISION"] = "framevault_m3_worker_v1"
MEDIA_BYTES = b"synthetic fixture media bytes"


def video_only_source_core_wire_payload():
    duration_ms = 6 * 60 * 1000
    runtime_manifest = {
        "model_repository": "Systran/faster-whisper-large-v3",
        "model_snapshot": "0123456789abcdef0123456789abcdef01234567",
        "model_weights_sha256": "not-used-no-audio-stream",
        "ctranslate2_version": "not-used-no-audio-stream",
        "language_mode": "auto",
        "ffmpeg_version": "ffmpeg version 7.0 fixture",
        "ffmpeg_configuration_sha256": "b" * 64,
        "ffmpeg_executable_sha256": "c" * 64,
        "python_distributions_sha256": "d" * 64,
        "python_distributions_count": "17",
        "os_release_sha256": "e" * 64,
        "python_version": "3.11.9",
        "modal_version": "1.1.4",
        "runtime_manifest_sha256": "f" * 64,
    }

    def unexpected_audio_stage(*_args, **_kwargs):
        raise AssertionError("video-only source must skip audio processing")

    def visual_scanner(_path, duration, *, cancel_check):
        cancel_check()
        return {
            "covered_duration_ms": duration,
            "events": [{
                "kind": "motion_change",
                "start_ms": 70_000,
                "end_ms": 82_000,
                "detector": "mean_absolute_gray_frame_difference",
                "mean_absolute_difference": 204.0,
                "visual_evidence_strength": "weak",
            }],
        }

    analysis = analyze_source(
        "fixture-video-only.media",
        source_duration_ms=duration_ms,
        source_sha256="a" * 64,
        requested_content_type="general",
        pipeline_revision="framevault_m3_worker_v1",
        min_clip_seconds=15,
        max_clip_seconds=30,
        candidate_limit=10,
        has_audio_stream=False,
        transcriber=unexpected_audio_stage,
        audio_scanner=unexpected_audio_stage,
        visual_scanner=visual_scanner,
        candidate_inspector=lambda _path, _start, _end, *, cancel_check: (
            cancel_check(),
            {"sample_rate_hz": 2, "sampled_frames": 60, "visual_events": []},
        )[1],
        runtime_manifest=runtime_manifest,
    )
    return build_source_core_payload(
        analysis, compute_seconds=10, rate_micro_usd_per_second=25
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
        "media_url": "https://framevault.dev/api/clipping/worker-media/clip_job_01/attempt_01",
        "callback_url": "https://framevault.dev/api/clipping/worker-callbacks/clip_job_01/attempt_01",
        "source_duration_ms": 60_000,
        "expires_at": 200,
        "callback_expires_at": 200 + MAX_CALLBACK_GRACE_SECONDS,
        "source_sha256": hashlib.sha256(MEDIA_BYTES).hexdigest(),
        "content_type": "podcast",
        "rate_micro_usd_per_second": 125,
        "model": "large-v3",
        "max_compute_seconds": 90,
        "min_clip_seconds": 15,
        "max_clip_seconds": 180,
        "candidate_limit": 7,
        "pipeline_revision": "framevault_m3_worker_v1",
    }
    payload.update(overrides)
    return payload


class MemoryResponse:
    def __init__(self, body: bytes, *, status=200, declared_length=None):
        self.body = body
        self.status = status
        self.offset = 0
        self.read_calls = 0
        self.headers = {"Content-Length": str(len(body) if declared_length is None else declared_length)}

    def getcode(self):
        return self.status

    def read(self, size):
        self.read_calls += 1
        block = self.body[self.offset : self.offset + size]
        self.offset += len(block)
        return block

    def __enter__(self):
        return self

    def __exit__(self, *_args):
        return None


class MemoryOpener:
    def __init__(self, response):
        self.response = response
        self.request = None

    def open(self, request, timeout):
        self.request = request
        return self.response


def source_analysis(dispatch):
    return {
        "source_sha256": dispatch.source_sha256,
        "source_duration_ms": dispatch.source_duration_ms,
        "transcript": {
            "text": "A source transcript.",
            "segments": [{"start_ms": 100, "end_ms": 2000, "text": "A source transcript.", "speaker_id": None}],
            "language": "en",
            "language_probability": 0.86,
            "original_script": {"name": "latin"},
            "speaker_labels_available": False,
        },
        "context_timeline": [{
            "start_ms": 0,
            "end_ms": dispatch.source_duration_ms,
            "terms": ["source", "transcript"],
            "opening_excerpt": "A source transcript.",
            "closing_excerpt": "A source transcript.",
        }],
        "audio_events": [{
            "kind": "non_speech_audio_transient",
            "start_ms": 30_000,
            "end_ms": 31_000,
            "detector": "energy_outside_transcript_timing",
            "semantic_label": None,
            "rms": 0.2,
        }],
        "visual_events": [{
            "kind": "scene_change",
            "start_ms": 40_000,
            "end_ms": 40_001,
            "detector": "ffmpeg_scene_score_threshold_0_30",
            "visual_evidence_strength": "weak",
        }],
        "audio_scan": {"covered_duration_ms": dispatch.source_duration_ms},
        "visual_scan": {"covered_duration_ms": dispatch.source_duration_ms},
        "analysis_versions": {
            "transcriber": "faster-whisper==1.1.1/large-v3",
            "audio_events": "audio-v1",
            "visual_events": "visual-v1",
            "context_timeline": "context-v1",
            "candidate_inspection": "dense-v1",
            "model_repository": "Systran/faster-whisper-large-v3",
            "model_snapshot": "0123456789abcdef0123456789abcdef01234567",
            "model_weights_sha256": "e" * 64,
            "ctranslate2_version": "4.6.0",
            "language_mode": "auto",
            "ffmpeg_version": "ffmpeg version 7.0 fixture",
            "ffmpeg_configuration_sha256": "f" * 64,
            "runtime_manifest_sha256": "a" * 64,
            "python_version": "3.11.9",
            "modal_version": "1.1.4",
            "prompt_version": "none-extractive-no-llm-v1",
        },
        "pipeline_revision": "framevault_m3_worker_v1",
        "candidate_inspections": [{
            "anchor_ms": 30_000,
            "start_ms": 0,
            "end_ms": 60_000,
            "anchor_kind": "audio_non_speech_audio_transient",
            "anchor_score": 0.9,
            "sample_rate_hz": 2,
            "sampled_frames": 120,
            "visual_events": [{
                "start_ms": 40_000,
                "end_ms": 40_500,
                "kind": "dense_motion_change",
                "score": 0.4,
                "evidence": {"visual_evidence_strength": "weak"},
            }],
        }],
    }


class ClippingM3WorkerTests(unittest.TestCase):
    def test_video_only_source_core_wire_fixture_matches_worker_output(self):
        payload = video_only_source_core_wire_payload()
        self.assertEqual(payload["algorithm_versions"]["audio_events"], "not-run-no-audio-stream-v1")
        fixture_path = Path(__file__).parent / "testdata" / "video_only_source_core.v2.json"
        fixture = json.loads(fixture_path.read_text(encoding="utf-8"))
        self.assertEqual(payload, fixture)

    def test_manual_model_prewarm_is_cpu_only_volume_writer_with_bounded_result(self):
        module = sys.modules[clipping_model_prewarm_task.__module__]
        previous_volume = module._MODAL_MODEL_VOLUME
        lifecycle = []

        class FakeVolume:
            def reload(self):
                lifecycle.append("reload")

            def commit(self):
                lifecycle.append("commit")

        module._MODAL_MODEL_VOLUME = FakeVolume()
        try:
            with patch.object(module, "prewarm_faster_whisper_snapshot", return_value={
                "model_repository": "Systran/faster-whisper-large-v3",
                "model_snapshot": "c" * 40,
                "model_weights_sha256": "a" * 64,
            }) as prewarm:
                response = clipping_model_prewarm_task()
            prewarm.assert_called_once_with()
        finally:
            module._MODAL_MODEL_VOLUME = previous_volume
        self.assertEqual(lifecycle, ["reload", "commit"])
        self.assertEqual(response, {"ready": True, "model_snapshot": "c" * 40})

    @staticmethod
    def _dns_answer(address):
        parsed = ipaddress.ip_address(address)
        family = socket.AF_INET6 if parsed.version == 6 else socket.AF_INET
        sockaddr = (str(parsed), 443, 0, 0) if parsed.version == 6 else (str(parsed), 443)
        return (family, socket.SOCK_STREAM, socket.IPPROTO_TCP, "", sockaddr)

    def test_dispatch_ack_is_bound_and_contains_no_capabilities_or_urls(self):
        dispatch = StageDispatch.parse(dispatch_payload(), now=100)
        ack = dispatch_ack(dispatch)
        self.assertEqual(ack, {
            "accepted": True,
            "job_id": dispatch.job_id,
            "attempt_id": dispatch.attempt_id,
            "dispatch_id": dispatch.dispatch_id,
        })
        encoded = json.dumps(ack)
        self.assertNotIn(dispatch.media_url, encoded)
        self.assertNotIn(dispatch.callback_url, encoded)
        self.assertNotIn(MEDIA_CAP, encoded)
        self.assertNotIn(CALLBACK_CAP, encoded)

    def test_dispatch_shared_secret_requires_exact_lowercase_sha256_hex(self):
        self.assertTrue(_valid_dispatch_shared_secret("a" * 64))
        invalid_values = ("a" * 63, "A" * 64, "g" * 64, "a" * 63 + "\n", "")
        for invalid in invalid_values:
            with self.subTest(length=len(invalid), uppercase=invalid.isupper()):
                self.assertFalse(_valid_dispatch_shared_secret(invalid))
        from fastapi import HTTPException

        api = clipping_dispatch_api()
        endpoint = next(route.endpoint for route in api.routes if getattr(route, "path", None) == "/dispatch")
        for invalid in invalid_values:
            with patch.dict(os.environ, {WORKER_SECRET_KEY: invalid}):
                with self.assertRaises(HTTPException) as raised:
                    asyncio.run(endpoint(SimpleNamespace(headers={})))
            self.assertEqual(raised.exception.status_code, 503)
            if invalid:
                self.assertNotIn(invalid, str(raised.exception.detail))

    def test_dispatch_body_is_stream_bounded_and_content_length_is_validated(self):
        async def chunks(*items):
            for item in items:
                yield item

        exact = asyncio.run(read_limited_body(
            chunks(b"x" * 32_000, b"y" * (MAX_DISPATCH_BODY_BYTES - 32_000)),
            str(MAX_DISPATCH_BODY_BYTES),
        ))
        self.assertEqual(len(exact), MAX_DISPATCH_BODY_BYTES)
        with self.assertRaises(DispatchBodyTooLarge):
            asyncio.run(read_limited_body(chunks(b"x"), str(MAX_DISPATCH_BODY_BYTES + 1)))
        with self.assertRaises(DispatchBodyTooLarge):
            asyncio.run(read_limited_body(chunks(b"x" * MAX_DISPATCH_BODY_BYTES, b"y")))
        for invalid in ("-1", "+1", "one", ""):
            with self.subTest(content_length=invalid), self.assertRaises(ValueError):
                asyncio.run(read_limited_body(chunks(b""), invalid))

    def test_download_streams_to_private_file_and_verifies_sha256(self):
        dispatch = StageDispatch.parse(dispatch_payload(), now=100)
        response = MemoryResponse(MEDIA_BYTES)
        opener = MemoryOpener(response)
        with tempfile.TemporaryDirectory() as directory:
            path = create_private_temp_file(directory)
            digest = download_media(
                dispatch,
                MEDIA_CAP,
                path,
                opener_factory=lambda: opener,
                now=lambda: 101,
            )
            self.assertEqual(path.read_bytes(), MEDIA_BYTES)
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)
            self.assertEqual(digest, hashlib.sha256(MEDIA_BYTES).hexdigest())
        headers = {key.lower(): value for key, value in opener.request.header_items()}
        self.assertEqual(headers["authorization"], f"Bearer {MEDIA_CAP}")
        self.assertNotIn(CALLBACK_CAP, json.dumps(headers))

    def test_source_sha_mismatch_fails_after_streaming(self):
        dispatch = StageDispatch.parse(dispatch_payload(source_sha256="0" * 64), now=100)
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "source.media"
            path.touch(mode=0o600)
            with self.assertRaisesRegex(AnalysisError, "SHA-256"):
                download_media(
                    dispatch,
                    MEDIA_CAP,
                    path,
                    opener_factory=lambda: MemoryOpener(MemoryResponse(MEDIA_BYTES)),
                    now=lambda: 101,
                )

    def test_oversize_content_length_is_rejected_before_read(self):
        from clipping_protocol import MAX_SOURCE_BYTES

        dispatch = StageDispatch.parse(dispatch_payload(), now=100)
        response = MemoryResponse(b"", declared_length=MAX_SOURCE_BYTES + 1)
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "source.media"
            path.touch(mode=0o600)
            with self.assertRaisesRegex(AnalysisError, "20 GiB"):
                download_media(
                    dispatch,
                    MEDIA_CAP,
                    path,
                    opener_factory=lambda: MemoryOpener(response),
                    now=lambda: 101,
                )
        self.assertEqual(response.read_calls, 0)

    def test_expiry_and_cancellation_stop_stream_before_next_block(self):
        dispatch = StageDispatch.parse(dispatch_payload(), now=100)
        response = MemoryResponse(MEDIA_BYTES)
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "source.media"
            path.touch(mode=0o600)
            with self.assertRaises(WorkCancelled):
                download_media(
                    dispatch,
                    MEDIA_CAP,
                    path,
                    opener_factory=lambda: MemoryOpener(response),
                    now=lambda: 200,
                )
        self.assertEqual(response.read_calls, 0)

        response = MemoryResponse(MEDIA_BYTES)
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "source.media"
            path.touch(mode=0o600)
            with self.assertRaises(WorkCancelled):
                download_media(
                    dispatch,
                    MEDIA_CAP,
                    path,
                    cancel_check=lambda: (_ for _ in ()).throw(WorkCancelled("cancelled")),
                    opener_factory=lambda: MemoryOpener(response),
                    now=lambda: 101,
                )
        self.assertEqual(response.read_calls, 0)

    def test_compute_guard_stops_at_deadline_and_checks_live_capability(self):
        dispatch = StageDispatch.parse(dispatch_payload(
            max_compute_seconds=10,
            expires_at=150,
            callback_expires_at=150 + MAX_CALLBACK_GRACE_SECONDS,
        ), now=100)
        clock = {"wall": 101, "mono": 1}
        probes = []
        guard = WorkGuard(
            dispatch,
            MEDIA_CAP,
            wall_clock=lambda: clock["wall"],
            monotonic=lambda: clock["mono"],
            active_probe=lambda _dispatch, _token: probes.append("probe"),
        )
        guard()
        self.assertEqual(probes, ["probe"])
        clock["mono"] = 11
        with self.assertRaises(ComputeDeadlineExceeded):
            guard()

    def test_work_guard_stops_at_retention_limited_callback_expiry(self):
        dispatch = StageDispatch.parse(dispatch_payload(callback_expires_at=105), now=100)
        guard = WorkGuard(
            dispatch,
            MEDIA_CAP,
            wall_clock=lambda: 105,
            monotonic=lambda: 1,
            active_probe=lambda *_args: self.fail("expired work must not probe or continue"),
        )
        with self.assertRaisesRegex(WorkCancelled, "callback capability expired"):
            guard()

    def test_callback_sender_refuses_to_post_at_exact_capability_expiry(self):
        opened = []

        class Opener:
            def open(self, *_args, **_kwargs):
                opened.append(True)
                raise AssertionError("expired callback must not be sent")

        acknowledged = deliver_callback(
            "https://framevault.dev/api/clipping/worker-callbacks/clip_job_01/attempt_01",
            CALLBACK_CAP,
            {"status": "completed"},
            opener_factory=Opener,
            callback_expires_at=100,
            wall_clock=lambda: 100,
        )
        self.assertFalse(acknowledged)
        self.assertEqual(opened, [])

    def test_bounded_process_cancellation_kills_its_process_group(self):
        with tempfile.TemporaryDirectory() as directory:
            pid_file = Path(directory) / "child.pid"
            script = (
                "import pathlib,subprocess,sys,time; "
                "child=subprocess.Popen([sys.executable,'-c','import time;time.sleep(30)']); "
                f"pathlib.Path({str(pid_file)!r}).write_text(str(child.pid)); "
                "print('ready',flush=True); time.sleep(30)"
            )
            checks = {"count": 0}

            def cancel_after_ready():
                checks["count"] += 1
                if checks["count"] > 1:
                    raise WorkCancelled("fixture cancelled")

            started = time.monotonic()
            with self.assertRaises(WorkCancelled):
                _run_bounded_process(
                    [sys.executable, "-c", script],
                    timeout=20,
                    output_limit=1024,
                    cancel_check=cancel_after_ready,
                )
            self.assertLess(time.monotonic() - started, 3)
            child_pid = int(pid_file.read_text())
            child_proc = Path(f"/proc/{child_pid}/stat")
            deadline = time.monotonic() + 1
            state = None
            while child_proc.exists() and time.monotonic() < deadline:
                state = child_proc.read_text().split()[2]
                if state == "Z":
                    break
                time.sleep(0.02)
            if child_proc.exists():
                self.assertEqual(state, "Z")

    def test_source_core_has_schema_fields_scores_and_operator_estimate(self):
        dispatch = StageDispatch.parse(dispatch_payload(), now=100)
        payload = build_source_core_payload(
            source_analysis(dispatch), compute_seconds=4, rate_micro_usd_per_second=125
        )
        self.assertEqual(payload["analysis_version"], "framevault.analysis.source.v1")
        self.assertEqual(payload["cost_estimate_micro_usd"], 500)
        self.assertEqual(payload["cost_basis"], COMPUTE_COST_BASIS)
        self.assertEqual(payload["coverage"]["audio_scanned_duration_ms"], dispatch.source_duration_ms)
        self.assertEqual(payload["model_versions"]["model_weights_sha256"], "e" * 64)
        self.assertEqual(payload["model_versions"]["ctranslate2_version"], "4.6.0")
        self.assertEqual(payload["algorithm_versions"]["ffmpeg_configuration_sha256"], "f" * 64)
        self.assertEqual(payload["algorithm_versions"]["runtime_manifest_sha256"], "a" * 64)
        self.assertEqual(payload["algorithm_versions"]["pipeline_revision"], "framevault_m3_worker_v1")
        self.assertEqual(payload["pipeline_revision"], "framevault_m3_worker_v1")
        self.assertEqual(payload["model_versions"]["language_mode"], "auto")
        self.assertEqual(payload["runtime_versions"], {"python": "3.11.9", "modal": "1.1.4"})
        self.assertEqual(payload["candidate_inspections"][0]["sampled_frames"], 120)
        self.assertEqual(payload["candidate_inspections"][0]["visual_events"][0]["kind"], "dense_motion_change")
        self.assertIsNone(payload["transcript"]["segments"][0]["confidence"])
        self.assertIsNone(payload["audio_events"][0]["evidence"]["semantic_label"])
        self.assertEqual(payload["visual_events"][0]["evidence"]["visual_evidence_strength"], "weak")

    def test_analyze_dispatch_runs_download_probe_core_and_private_cleanup_offline(self):
        dispatch = StageDispatch.parse(dispatch_payload(), now=100)
        response = MemoryResponse(MEDIA_BYTES)
        probes = []
        media_checks = []
        runtime_manifest = {
            "model_repository": "fixture-model",
            "model_snapshot": "0123456789abcdef0123456789abcdef01234567",
            "model_weights_sha256": "a" * 64,
            "ctranslate2_version": "fixture-ctranslate2",
            "language_mode": "auto",
            "ffmpeg_version": "fixture-ffmpeg",
            "ffmpeg_configuration_sha256": "b" * 64,
            "python_version": "3.11.9",
            "modal_version": "1.1.4",
            "runtime_manifest_sha256": "c" * 64,
        }

        def media_probe(path, duration, *, cancel_check):
            cancel_check()
            media_checks.append((Path(path).read_bytes(), duration, Path(path).stat().st_mode & 0o777))
            return {"probed_duration_ms": duration, "has_audio_stream": True, "has_video_stream": True}

        with tempfile.TemporaryDirectory() as directory:
            result = analyze_dispatch(
                dispatch,
                MEDIA_CAP,
                transcriber=lambda *_args, **_kwargs: {
                    "language": "en",
                    "language_probability": 0.8,
                    "segments": [{"start_ms": 1_000, "end_ms": 3_000, "text": "Why did this happen?"}],
                },
                audio_scanner=lambda _path, _segments, duration, **_kwargs: {
                    "events": [{"kind": "non_speech_audio_transient", "start_ms": 30_000, "end_ms": 31_000}],
                    "covered_duration_ms": duration,
                },
                visual_scanner=lambda _path, duration, **_kwargs: {
                    "events": [{"kind": "scene_change", "start_ms": 40_000, "end_ms": 40_001}],
                    "covered_duration_ms": duration,
                },
                candidate_inspector=lambda _path, start, end, *, cancel_check: (
                    cancel_check(),
                    {"sample_rate_hz": 2, "sampled_frames": (end - start) // 500, "visual_events": []},
                )[1],
                runtime_manifest=runtime_manifest,
                active_probe=lambda _dispatch, _token: probes.append("live-capability"),
                temp_directory=directory,
                opener_factory=lambda: MemoryOpener(response),
                monotonic=lambda: 10.0,
                wall_clock=lambda: 101,
                media_probe=media_probe,
            )
            leftovers = list(Path(directory).iterdir())

        self.assertEqual(probes, ["live-capability"])
        self.assertEqual(media_checks, [(MEDIA_BYTES, dispatch.source_duration_ms, 0o600)])
        self.assertEqual(leftovers, [])
        self.assertEqual(result.cost_estimate_micro_usd, dispatch.rate_micro_usd_per_second)
        self.assertEqual(result.artifact["payload"]["pipeline_revision"], dispatch.pipeline_revision)
        self.assertEqual(result.artifact["payload"]["candidate_inspections"][0]["sample_rate_hz"], 2)

    @unittest.skipUnless(shutil.which("ffmpeg") and shutil.which("ffprobe"), "FFmpeg/ffprobe are not installed")
    def test_video_only_media_skips_asr_and_audio_but_keeps_visual_analysis(self):
        with tempfile.TemporaryDirectory() as directory:
            media = Path(directory) / "video-only.mkv"
            subprocess.run(
                [
                    shutil.which("ffmpeg"), "-y", "-hide_banner", "-loglevel", "error",
                    "-f", "lavfi", "-i", "color=c=black:s=64x64:r=2:d=2",
                    "-f", "lavfi", "-i", "color=c=white:s=64x64:r=2:d=2",
                    "-filter_complex", "[0:v][1:v]concat=n=2:v=1:a=0[v]",
                    "-map", "[v]", "-an", "-c:v", "ffv1", str(media),
                ],
                check=True,
                capture_output=True,
                timeout=30,
            )
            media_bytes = media.read_bytes()
            dispatch = StageDispatch.parse(dispatch_payload(
                source_duration_ms=4_000,
                source_sha256=hashlib.sha256(media_bytes).hexdigest(),
            ), now=100)
            runtime_manifest = {
                "model_repository": "Systran/faster-whisper-large-v3",
                "model_snapshot": "c" * 40,
                "model_weights_sha256": "not-used-no-audio-stream",
                "ctranslate2_version": "not-used-no-audio-stream",
                "language_mode": "auto",
                "ffmpeg_version": "fixture-ffmpeg",
                "ffmpeg_configuration_sha256": "d" * 64,
                "ffmpeg_executable_sha256": "e" * 64,
                "python_distributions_sha256": "f" * 64,
                "python_distributions_count": "17",
                "os_release_sha256": "a" * 64,
                "python_version": "3.11.9",
                "modal_version": "1.1.4",
                "runtime_manifest_sha256": "b" * 64,
            }
            result = analyze_dispatch(
                dispatch,
                MEDIA_CAP,
                transcriber=lambda *_args, **_kwargs: self.fail("video-only source must skip ASR"),
                audio_scanner=lambda *_args, **_kwargs: self.fail("video-only source must skip audio FFmpeg"),
                runtime_manifest=runtime_manifest,
                active_probe=lambda *_args: None,
                temp_directory=directory,
                opener_factory=lambda: MemoryOpener(MemoryResponse(media_bytes)),
                monotonic=lambda: 10.0,
                wall_clock=lambda: 101,
            )

        payload = result.artifact["payload"]
        self.assertEqual(payload["media_streams"], {"has_audio": False, "has_video": True})
        self.assertEqual(payload["audio_analysis_status"], "skipped_no_audio_stream")
        self.assertEqual(payload["coverage"]["audio_scanned_duration_ms"], 0)
        self.assertEqual(payload["audio_events"], [])
        self.assertFalse(payload["transcript"]["available"])
        self.assertEqual(payload["transcript"]["status"], "unavailable_no_audio_stream")
        self.assertEqual(payload["transcript"]["segments"], [])
        self.assertEqual(payload["model_versions"]["transcription"], "not-used-no-audio-stream")
        self.assertEqual(payload["model_versions"]["model"], "not-used-no-audio-stream")
        self.assertEqual(payload["model_versions"]["model_weights_sha256"], "not-used-no-audio-stream")
        self.assertEqual(payload["model_versions"]["ctranslate2_version"], "not-used-no-audio-stream")
        self.assertTrue(payload["visual_events"])
        self.assertTrue(payload["candidate_inspections"])

    @unittest.skipUnless(shutil.which("ffmpeg") and shutil.which("ffprobe"), "FFmpeg/ffprobe are not installed")
    def test_real_ffmpeg_aligned_streams_pass_and_900ms_audio_offset_is_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            aligned = Path(directory) / "aligned.mkv"
            offset = Path(directory) / "offset.mkv"
            common = [
                shutil.which("ffmpeg"), "-y", "-hide_banner", "-loglevel", "error",
                "-f", "lavfi", "-i", "color=c=black:s=64x64:r=2:d=3",
            ]
            subprocess.run(
                common + [
                    "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100:duration=3",
                    "-map", "0:v", "-map", "1:a", "-c:v", "ffv1", "-c:a", "pcm_s16le", str(aligned),
                ],
                check=True,
                capture_output=True,
                timeout=30,
            )
            subprocess.run(
                common + [
                    "-itsoffset", "0.9", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100:duration=2",
                    "-map", "0:v", "-map", "1:a", "-c:v", "ffv1", "-c:a", "pcm_s16le", str(offset),
                ],
                check=True,
                capture_output=True,
                timeout=30,
            )
            aligned_info = verify_media_file(aligned, 3_000)
            self.assertTrue(aligned_info["has_audio_stream"])
            self.assertEqual(aligned_info["audio_video_start_skew_ms"], 0)
            with self.assertRaisesRegex(AnalysisError, "50ms alignment tolerance"):
                verify_media_file(offset, 3_000)

    def test_callback_result_replays_once_after_restart_without_reanalysis(self):
        payload = dispatch_payload()
        dispatch = StageDispatch.parse(payload, now=100)
        analysis_calls = []
        sent = []

        def analysis_client(_dispatch, _media_token):
            analysis_calls.append("analyzed")
            core = source_analysis(_dispatch)
            source_payload = build_source_core_payload(
                core,
                compute_seconds=2,
                rate_micro_usd_per_second=_dispatch.rate_micro_usd_per_second,
            )
            return StageResult({
                "type": "clipping.analysis",
                "schema_version": "2",
                "version": 1,
                "source_duration_ms": _dispatch.source_duration_ms,
                "time_ranges": [],
                "payload": source_payload,
            }, source_payload["cost_estimate_micro_usd"], COMPUTE_COST_BASIS)

        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "ledger.sqlite3"
            first = SQLiteDispatchLedger(path)
            try:
                delivered = process_dispatch(
                    payload, MEDIA_CAP, CALLBACK_CAP, ledger=first,
                    analysis_client=analysis_client,
                    callback_sender=lambda url, token, result: sent.append((url, token, result)) or False,
                    now=100,
                )
            finally:
                first.close()
            self.assertFalse(delivered)
            second = SQLiteDispatchLedger(path)
            try:
                delivered = process_dispatch(
                    payload, MEDIA_CAP, CALLBACK_CAP, ledger=second,
                    analysis_client=lambda *_args: self.fail("replay must not analyze again"),
                    callback_sender=lambda url, token, result: sent.append((url, token, result)) or True,
                    now=100,
                )
                self.assertTrue(delivered)
                third_delivery = process_dispatch(
                    payload, MEDIA_CAP, CALLBACK_CAP, ledger=second,
                    analysis_client=lambda *_args: self.fail("acknowledged result must not analyze again"),
                    callback_sender=lambda *_args: self.fail("acknowledged result must not post again"),
                    now=100,
                )
            finally:
                second.close()
        self.assertTrue(third_delivery)
        self.assertEqual(analysis_calls, ["analyzed"])
        self.assertEqual(len(sent), 2)
        self.assertEqual(sent[0][2], sent[1][2])
        serialized = json.dumps(sent[1][2])
        self.assertNotIn(MEDIA_CAP, serialized)
        self.assertNotIn(CALLBACK_CAP, serialized)
        self.assertNotIn("actual_cost_micro_usd", serialized)
        self.assertEqual(sent[1][2]["artifact"]["type"], "clipping.analysis")
        self.assertEqual(sent[1][2]["artifact"]["time_ranges"], [])

    def test_idle_prune_helper_removes_expired_callback_without_a_later_claim(self):
        payload = dispatch_payload(callback_expires_at=150)
        with tempfile.TemporaryDirectory() as directory:
            ledger = SQLiteDispatchLedger(Path(directory) / "ledger.sqlite3")
            try:
                result = process_dispatch(
                    payload,
                    MEDIA_CAP,
                    CALLBACK_CAP,
                    ledger=ledger,
                    analysis_client=lambda _dispatch, _token: StageResult(
                        {
                            "type": "clipping.analysis",
                            "schema_version": "2",
                            "version": 1,
                            "source_duration_ms": 60_000,
                            "time_ranges": [],
                            "payload": {
                                **build_source_core_payload(
                                    source_analysis(StageDispatch.parse(payload, now=100)),
                                    compute_seconds=2,
                                    rate_micro_usd_per_second=125,
                                ),
                            },
                        },
                        250,
                    ),
                    callback_sender=lambda *_args: False,
                    now=100,
                )
                self.assertFalse(result)
                stored = ledger._db.execute(
                    "SELECT result_json FROM clipping_dispatches WHERE dispatch_id=? AND attempt_id=?",
                    ("clip_job_01:analysis", "attempt_01"),
                ).fetchone()
                self.assertIsNotNone(stored)
                self.assertTrue(stored[0])
                ledger.close()
                lifecycle = []
                run_ledger_rpc(
                    "prune",
                    {"now": 150},
                    ledger_factory=lambda: SQLiteDispatchLedger(Path(directory) / "ledger.sqlite3"),
                    reload_volume=lambda: lifecycle.append("reload"),
                    commit_volume=lambda: lifecycle.append("commit"),
                )
                ledger = SQLiteDispatchLedger(Path(directory) / "ledger.sqlite3")
                after_prune = ledger._db.execute(
                    "SELECT result_json FROM clipping_dispatches WHERE dispatch_id=? AND attempt_id=?",
                    ("clip_job_01:analysis", "attempt_01"),
                ).fetchone()
            finally:
                ledger.close()
        self.assertIsNone(after_prune)
        self.assertEqual(lifecycle, ["reload", "commit"])

    def test_modal_worker_uses_one_serialized_cpu_volume_owner_for_prune_and_rpc(self):
        worker_path = Path(__file__).with_name("clipping_m3_worker.py")
        worker_source = worker_path.read_text(encoding="utf-8")
        tree = ast.parse(worker_source)
        handlers = {
            "clipping_model_prewarm_task": clipping_model_prewarm_task,
            "clipping_ledger_owner": clipping_ledger_owner,
            "clipping_callback_replay_task": clipping_callback_replay_task,
            "clipping_analysis_task": clipping_analysis_task,
            "clipping_dispatch_api": clipping_dispatch_api,
        }
        module = sys.modules[clipping_ledger_owner.__module__]
        for name, handler in handlers.items():
            self.assertEqual(handler.__qualname__, name)
            self.assertNotIn("<locals>", handler.__qualname__)
            self.assertIs(getattr(module, name), handler)

        factory = next(
            node
            for node in tree.body
            if isinstance(node, ast.FunctionDef) and node.name == "_install_modal_app"
        )
        constants = {
            node.targets[0].id: node.value.value
            for node in tree.body
            if isinstance(node, ast.Assign)
            and len(node.targets) == 1
            and isinstance(node.targets[0], ast.Name)
            and isinstance(node.value, ast.Constant)
            and isinstance(node.value.value, str)
        }
        secret_contracts = {}
        for node in factory.body:
            if not isinstance(node, ast.Assign) or len(node.targets) != 1 or not isinstance(node.targets[0], ast.Name):
                continue
            call = node.value
            if not isinstance(call, ast.Call) or not isinstance(call.func, ast.Attribute) or call.func.attr != "from_name":
                continue
            if not isinstance(call.func.value, ast.Attribute) or call.func.value.attr != "Secret":
                continue
            name_arg = call.args[0]
            required = next(keyword.value for keyword in call.keywords if keyword.arg == "required_keys")
            secret_contracts[node.targets[0].id] = {
                "name": constants[name_arg.id],
                "required_keys": {constants[item.id] for item in required.elts},
            }
        self.assertEqual(set(secret_contracts), {"dispatch_api_secret", "runtime_secret", "prewarm_secret"})
        self.assertEqual(len({secret["name"] for secret in secret_contracts.values()}), 3)
        self.assertEqual(secret_contracts["dispatch_api_secret"]["name"], DISPATCH_API_SECRET_NAME)
        self.assertEqual(secret_contracts["dispatch_api_secret"]["required_keys"], {
            WORKER_SECRET_KEY, WORKER_ORIGIN_KEY, WORKER_PIPELINE_REVISION_KEY, WORKER_MODEL_REVISION_KEY,
        })
        self.assertEqual(secret_contracts["runtime_secret"]["name"], RUNTIME_SECRET_NAME)
        self.assertEqual(secret_contracts["runtime_secret"]["required_keys"], {
            WORKER_ORIGIN_KEY, WORKER_PIPELINE_REVISION_KEY, WORKER_MODEL_REVISION_KEY,
        })
        self.assertEqual(secret_contracts["prewarm_secret"]["name"], MODEL_PREWARM_SECRET_NAME)
        self.assertEqual(secret_contracts["prewarm_secret"]["required_keys"], {WORKER_MODEL_REVISION_KEY})
        self.assertNotIn(WORKER_SECRET_KEY, secret_contracts["runtime_secret"]["required_keys"])
        self.assertNotIn(WORKER_SECRET_KEY, secret_contracts["prewarm_secret"]["required_keys"])

        function_options = {}
        for node in ast.walk(factory):
            if not isinstance(node, ast.Call) or not isinstance(node.func, ast.Call):
                continue
            app_function = node.func
            function = app_function.func
            if not (
                isinstance(function, ast.Attribute)
                and function.attr == "function"
                and isinstance(function.value, ast.Name)
                and function.value.id == "app"
            ):
                continue
            if len(node.args) != 1 or not isinstance(node.args[0], ast.Name):
                self.fail("Modal registrations must receive one module-global function")
            implementation_name = node.args[0].id
            if implementation_name == "asgi_api":
                implementation_name = "clipping_dispatch_api"
            function_options[implementation_name] = {
                keyword.arg: keyword.value for keyword in app_function.keywords
            }

        self.assertEqual(set(function_options), set(handlers))
        for options in function_options.values():
            self.assertNotIn("concurrency_limit", options)
        expected_secrets = {
            "clipping_dispatch_api": "dispatch_api_secret",
            "clipping_ledger_owner": "runtime_secret",
            "clipping_callback_replay_task": "runtime_secret",
            "clipping_analysis_task": "runtime_secret",
            "clipping_model_prewarm_task": "prewarm_secret",
        }
        for function_name, secret_name in expected_secrets.items():
            configured = function_options[function_name]["secrets"]
            self.assertEqual(len(configured.elts), 1)
            self.assertIsInstance(configured.elts[0], ast.Name)
            self.assertEqual(configured.elts[0].id, secret_name)
        self.assertIn("required_keys validates presence but does not scope a Secret's", worker_source)
        owner_options = function_options["clipping_ledger_owner"]
        options = owner_options
        self.assertIn("schedule", options)
        self.assertNotIn("gpu", options)
        self.assertNotIn("concurrency_limit", options)
        for name in ("max_containers", "max_inputs"):
            self.assertIsInstance(options[name], ast.Constant)
            self.assertEqual(options[name].value, 1)
        self.assertIsInstance(options["cpu"], ast.Constant)
        self.assertGreater(options["cpu"].value, 0)
        self.assertIn("modal==1.1.4", worker_source)
        self.assertIn("max_inputs=1 retires a container after one", worker_source)
        requirements = worker_path.with_name("requirements-clipping.txt").read_text(encoding="utf-8")
        self.assertIn("modal==1.1.4", requirements)
        for function_name, expected_limit in (
            ("clipping_analysis_task", 1),
            ("clipping_dispatch_api", 100),
            ("clipping_callback_replay_task", 10),
            ("clipping_model_prewarm_task", 1),
        ):
            handler_options = function_options[function_name]
            self.assertIsInstance(handler_options["max_containers"], ast.Constant)
            self.assertEqual(handler_options["max_containers"].value, expected_limit)

        analysis_options = function_options["clipping_analysis_task"]
        self.assertIsInstance(analysis_options["ephemeral_disk"], ast.Constant)
        self.assertEqual(analysis_options["ephemeral_disk"].value, 524_288)
        self.assertGreaterEqual(analysis_options["ephemeral_disk"].value, 524_288)
        self.assertLessEqual(analysis_options["ephemeral_disk"].value, 3_145_728)

        ledger_writers = []
        for name, options in function_options.items():
            volumes = options.get("volumes")
            if not isinstance(volumes, ast.Dict):
                continue
            for key, value in zip(volumes.keys, volumes.values):
                if (
                    isinstance(key, ast.Name)
                    and key.id == "LEDGER_DIR"
                    and isinstance(value, ast.Name)
                    and value.id == "_MODAL_LEDGER_VOLUME"
                ):
                    ledger_writers.append(name)
        self.assertEqual(len(ledger_writers), 1)
        self.assertEqual(ledger_writers, ["clipping_ledger_owner"])
        replay_options = function_options["clipping_callback_replay_task"]
        self.assertNotIn("gpu", replay_options)
        self.assertNotIn("volumes", replay_options)
        prewarm_options = function_options["clipping_model_prewarm_task"]
        self.assertNotIn("gpu", prewarm_options)
        self.assertNotIn("schedule", prewarm_options)
        self.assertIsInstance(prewarm_options["timeout"], ast.Constant)
        self.assertEqual(prewarm_options["timeout"].value, 3600)
        prewarm_volumes = prewarm_options.get("volumes")
        self.assertIsInstance(prewarm_volumes, ast.Dict)
        self.assertTrue(any(
            isinstance(key, ast.Name) and key.id == "MODEL_CACHE_DIR"
            and isinstance(value, ast.Name) and value.id == "_MODAL_MODEL_VOLUME"
            for key, value in zip(prewarm_volumes.keys, prewarm_volumes.values)
        ))
        analysis_function = next(node for node in tree.body if isinstance(node, ast.FunctionDef) and node.name == "clipping_analysis_task")
        self.assertFalse(any(
            isinstance(node, ast.Call)
            and isinstance(node.func, ast.Attribute)
            and node.func.attr == "commit"
            for node in ast.walk(analysis_function)
        ), "GPU analysis must not commit the prewarmed model volume")
        self.assertIn("Manually prewarm the pinned model on CPU before enabling dispatch", worker_source)
        endpoint = next(node for node in ast.walk(tree) if isinstance(node, ast.AsyncFunctionDef) and node.name == "dispatch_endpoint")
        secret_availability_checks = [
            node for node in ast.walk(endpoint)
            if isinstance(node, ast.Call)
            and isinstance(node.func, ast.Name)
            and node.func.id == "_valid_dispatch_shared_secret"
        ]
        self.assertEqual(len(secret_availability_checks), 1)
        admission_lines = [
            node.lineno
            for node in ast.walk(endpoint)
            if isinstance(node, ast.Call)
            and isinstance(node.func, ast.Name)
            and node.func.id == "dispatch_admission"
        ]
        spawn_calls = [
            node
            for node in ast.walk(endpoint)
            if isinstance(node, ast.Call)
            and isinstance(node.func, ast.Attribute)
            and node.func.attr == "spawn"
        ]
        self.assertEqual(len(admission_lines), 1)
        spawn_targets = set()
        for node in spawn_calls:
            function_reference = node.func.value
            if (
                isinstance(function_reference, ast.Subscript)
                and isinstance(function_reference.slice, ast.Constant)
            ):
                spawn_targets.add(function_reference.slice.value)
        self.assertEqual(spawn_targets, {"analysis", "callback_replay"})
        self.assertTrue(all(admission_lines[0] < node.lineno for node in spawn_calls))

    def test_every_registered_modal_image_mounts_worker_sibling_modules(self):
        worker_path = Path(__file__).with_name("clipping_m3_worker.py")
        worker_source = worker_path.read_text(encoding="utf-8")
        tree = ast.parse(worker_source)
        factory = next(
            node
            for node in tree.body
            if isinstance(node, ast.FunctionDef) and node.name == "_install_modal_app"
        )
        image_names = {"gpu_image", "prewarm_image", "api_image"}
        image_assignments = {
            node.targets[0].id: node.value
            for node in factory.body
            if isinstance(node, ast.Assign)
            and len(node.targets) == 1
            and isinstance(node.targets[0], ast.Name)
            and node.targets[0].id in image_names
        }
        self.assertEqual(set(image_assignments), image_names)
        for image_name, image_expression in image_assignments.items():
            self.assertIsInstance(image_expression, ast.Call, image_name)
            self.assertIsInstance(image_expression.func, ast.Name, image_name)
            self.assertEqual(image_expression.func.id, "_add_worker_source_files", image_name)

        helper = next(
            node
            for node in tree.body
            if isinstance(node, ast.FunctionDef) and node.name == "_add_worker_source_files"
        )
        source_mounts = [
            node
            for node in ast.walk(helper)
            if isinstance(node, ast.Call)
            and isinstance(node.func, ast.Attribute)
            and node.func.attr == "add_local_file"
        ]
        self.assertEqual(len(source_mounts), 2)
        mounted_remote_paths = {
            next(keyword.value.value for keyword in call.keywords if keyword.arg == "remote_path")
            for call in source_mounts
        }
        self.assertEqual(
            mounted_remote_paths,
            {"/root/clipping_analysis.py", "/root/clipping_protocol.py"},
        )
        local_module_names = set()
        for call in source_mounts:
            local_path = call.args[0]
            self.assertIsInstance(local_path, ast.Call)
            self.assertIsInstance(local_path.func, ast.Name)
            self.assertEqual(local_path.func.id, "str")
            path_expression = local_path.args[0]
            self.assertIsInstance(path_expression, ast.BinOp)
            self.assertIsInstance(path_expression.left, ast.Name)
            self.assertEqual(path_expression.left.id, "source_dir")
            self.assertIsInstance(path_expression.right, ast.Constant)
            local_module_names.add(path_expression.right.value)
        self.assertEqual(local_module_names, {"clipping_analysis.py", "clipping_protocol.py"})

        image_for_function = {
            "clipping_model_prewarm_task": "prewarm_image",
            "clipping_ledger_owner": "api_image",
            "clipping_callback_replay_task": "api_image",
            "clipping_analysis_task": "gpu_image",
            "clipping_dispatch_api": "api_image",
        }
        registrations = {}
        for node in ast.walk(factory):
            if not isinstance(node, ast.Call) or not isinstance(node.func, ast.Call):
                continue
            app_function = node.func
            if not (
                isinstance(app_function.func, ast.Attribute)
                and app_function.func.attr == "function"
                and isinstance(app_function.func.value, ast.Name)
                and app_function.func.value.id == "app"
            ):
                continue
            implementation = node.args[0].id
            if implementation == "asgi_api":
                implementation = "clipping_dispatch_api"
            registrations[implementation] = {
                keyword.arg: keyword.value for keyword in app_function.keywords
            }
        self.assertEqual(set(registrations), set(image_for_function))
        for function_name, image_name in image_for_function.items():
            configured_image = registrations[function_name]["image"]
            self.assertIsInstance(configured_image, ast.Name, function_name)
            self.assertEqual(configured_image.id, image_name, function_name)

    def test_analysis_image_pins_match_the_worker_requirements_file(self):
        worker_path = Path(__file__).with_name("clipping_m3_worker.py")
        worker_source = worker_path.read_text(encoding="utf-8")
        tree = ast.parse(worker_source)
        pin_assignment = next(
            node
            for node in tree.body
            if isinstance(node, ast.Assign)
            and len(node.targets) == 1
            and isinstance(node.targets[0], ast.Name)
            and node.targets[0].id == "CLIPPING_IMAGE_PACKAGES"
        )
        self.assertIsInstance(pin_assignment.value, ast.Tuple)
        image_pins = tuple(item.value for item in pin_assignment.value.elts)
        requirements_pins = tuple(
            line.strip()
            for line in worker_path.with_name("requirements-clipping.txt")
            .read_text(encoding="utf-8")
            .splitlines()
            if line.strip() and not line.lstrip().startswith("#")
        )
        self.assertEqual(image_pins, requirements_pins)
        self.assertNotIn("pip_install_from_requirements", worker_source)

    @unittest.skipUnless(importlib.util.find_spec("modal"), "pinned Modal SDK is not installed")
    def test_modal_registration_mounts_worker_modules_without_network_or_rpc(self):
        worker_path = Path(__file__).with_name("clipping_m3_worker.py").resolve()
        module_name = "_framevault_clipping_m3_worker_import_contract"
        spec = importlib.util.spec_from_file_location(module_name, worker_path)
        self.assertIsNotNone(spec)
        self.assertIsNotNone(spec.loader)
        worker_module = importlib.util.module_from_spec(spec)
        sys.modules[module_name] = worker_module

        def reject_network(*_args, **_kwargs):
            raise AssertionError("Modal registration must not make network or RPC calls")

        try:
            with (
                patch("socket.create_connection", side_effect=reject_network),
                patch("socket.socket.connect", side_effect=reject_network),
                patch("socket.getaddrinfo", side_effect=reject_network),
            ):
                spec.loader.exec_module(worker_module)

            self.assertIsNotNone(worker_module.app)
            self.assertEqual(
                set(worker_module._MODAL_FUNCTIONS),
                {"model_prewarm", "ledger_owner", "callback_replay", "analysis", "dispatch_api"},
            )
            expected_images = {
                "model_prewarm": "prewarm_image",
                "ledger_owner": "api_image",
                "callback_replay": "api_image",
                "analysis": "gpu_image",
                "dispatch_api": "api_image",
            }
            source_dir = worker_path.parent
            expected_mounts = {
                "/root/clipping_analysis.py": source_dir / "clipping_analysis.py",
                "/root/clipping_protocol.py": source_dir / "clipping_protocol.py",
            }
            for function_name, image_name in expected_images.items():
                function = worker_module._MODAL_FUNCTIONS[function_name]
                image = function.spec.image
                mount_map = {}
                pending = [image]
                visited = set()
                while pending:
                    current = pending.pop()
                    if id(current) in visited:
                        continue
                    visited.add(id(current))
                    dependencies = getattr(current, "_deps", None)
                    if not callable(dependencies):
                        continue
                    for dependency in dependencies():
                        entries = getattr(dependency, "entries", None)
                        if entries is not None:
                            for entry in entries:
                                if hasattr(entry, "remote_path") and hasattr(entry, "local_file"):
                                    mount_map[str(entry.remote_path)] = Path(entry.local_file).resolve()
                        else:
                            pending.append(dependency)
                for remote_path, local_path in expected_mounts.items():
                    self.assertEqual(
                        mount_map.get(remote_path),
                        local_path,
                        (function_name, image_name, remote_path),
                    )
        finally:
            sys.modules.pop(module_name, None)

    @unittest.skipUnless(importlib.util.find_spec("modal"), "pinned Modal SDK is not installed")
    def test_modal_registration_survives_flat_deployed_module_path(self):
        worker_path = Path(__file__).with_name("clipping_m3_worker.py").resolve()
        module_name = "_framevault_clipping_m3_worker_flat_import_contract"
        spec = importlib.util.spec_from_file_location(module_name, worker_path)
        self.assertIsNotNone(spec)
        self.assertIsNotNone(spec.loader)
        worker_module = importlib.util.module_from_spec(spec)
        sys.modules[module_name] = worker_module

        def reject_network(*_args, **_kwargs):
            raise AssertionError("Modal registration must not make network or RPC calls")

        try:
            with (
                patch("socket.create_connection", side_effect=reject_network),
                patch("socket.socket.connect", side_effect=reject_network),
                patch("socket.getaddrinfo", side_effect=reject_network),
            ):
                spec.loader.exec_module(worker_module)
                with (
                    patch.object(worker_module, "__file__", "/root/clipping_m3_worker.py"),
                    patch.object(
                        worker_module,
                        "_add_worker_source_files",
                        side_effect=lambda image: image,
                    ),
                ):
                    deployed_app = worker_module._install_modal_app()

            self.assertIsNotNone(deployed_app)
            self.assertEqual(
                set(worker_module._MODAL_FUNCTIONS),
                {"model_prewarm", "ledger_owner", "callback_replay", "analysis", "dispatch_api"},
            )
        finally:
            sys.modules.pop(module_name, None)

    def test_remote_ledger_rpc_replay_and_delivery_follow_serialized_operation_order(self):
        now = int(time.time())
        payload = dispatch_payload(
            expires_at=now + 3600,
            callback_expires_at=now + 3600 + MAX_CALLBACK_GRACE_SECONDS,
        )
        dispatch = StageDispatch.parse(payload, now=now)
        calls = []
        with tempfile.TemporaryDirectory() as directory:
            ledger = SQLiteDispatchLedger(Path(directory) / "ledger.sqlite3")
            try:
                def rpc(operation, request):
                    calls.append(operation)
                    return ledger_rpc_operation(
                        ledger,
                        operation,
                        request,
                        expected_origin="https://framevault.dev",
                        expected_pipeline_revision="framevault_m3_worker_v1",
                    )

                remote = RemoteDispatchLedger(rpc)
                claimed, cached = remote.claim(dispatch, now=now)
                self.assertTrue(claimed)
                self.assertIsNone(cached)
                remote.complete(dispatch, {"status": "completed", "artifact": {"type": "fixture"}})
                replay_claimed, replayed = remote.claim(dispatch, now=now + 1)
                self.assertFalse(replay_claimed)
                self.assertEqual(replayed["status"], "completed")
                remote.mark_delivered(dispatch)
                post_ack_claimed, post_ack_result = remote.claim(dispatch, now=now + 2)
                self.assertFalse(post_ack_claimed)
                self.assertIsNone(post_ack_result)
            finally:
                ledger.close()
        self.assertEqual(calls, ["claim", "complete", "claim", "mark_delivered", "claim"])

    def test_http_admission_claims_before_spawn_and_replays_only_cached_results(self):
        now = int(time.time())
        payload = dispatch_payload(
            expires_at=now + 3600,
            callback_expires_at=now + 3600 + MAX_CALLBACK_GRACE_SECONDS,
        )
        dispatch = StageDispatch.parse(payload, now=now)
        with tempfile.TemporaryDirectory() as directory:
            ledger = SQLiteDispatchLedger(Path(directory) / "ledger.sqlite3")
            claim_phases = []
            try:
                def rpc(operation, request):
                    if operation == "claim":
                        claim_phases.append(request["phase"])
                    return ledger_rpc_operation(
                        ledger,
                        operation,
                        request,
                        expected_origin="https://framevault.dev",
                        expected_pipeline_revision="framevault_m3_worker_v1",
                    )

                first_action, first_cache = dispatch_admission(dispatch, rpc)
                duplicate_action, duplicate_cache = dispatch_admission(dispatch, rpc)
                self.assertEqual(first_action, "analysis")
                self.assertIsNone(first_cache)
                self.assertEqual(duplicate_action, "duplicate")
                self.assertIsNone(duplicate_cache)

                worker_ledger = RemoteDispatchLedger(rpc)
                self.assertEqual(worker_ledger.claim(dispatch, now=now), (True, None))
                self.assertEqual(worker_ledger.claim(dispatch, now=now), (False, None))
                source_payload = build_source_core_payload(
                    source_analysis(dispatch),
                    compute_seconds=2,
                    rate_micro_usd_per_second=dispatch.rate_micro_usd_per_second,
                )
                artifact = {
                    "type": "clipping.analysis",
                    "schema_version": "2",
                    "version": 1,
                    "source_duration_ms": dispatch.source_duration_ms,
                    "time_ranges": [],
                    "payload": source_payload,
                }
                cached_callback = canonical_callback(
                    dispatch,
                    StageResult(artifact, source_payload["cost_estimate_micro_usd"], COMPUTE_COST_BASIS),
                )
                worker_ledger.complete(dispatch, cached_callback)
                replay_action, replay = dispatch_admission(dispatch, rpc)
                self.assertEqual(replay_action, "replay")
                sent = []
                acknowledged = replay_cached_callback(
                    payload,
                    CALLBACK_CAP,
                    replay,
                    ledger_rpc=rpc,
                    callback_sender=lambda url, token, body: sent.append((url, token, body)) or True,
                    now=now + 1,
                )
                self.assertTrue(acknowledged)
                self.assertEqual(len(sent), 1)
                self.assertEqual(sent[0][2], cached_callback)
                post_ack_action, post_ack_cache = dispatch_admission(dispatch, rpc)
                self.assertEqual(post_ack_action, "duplicate")
                self.assertIsNone(post_ack_cache)
            finally:
                ledger.close()
        self.assertEqual(claim_phases, ["admit", "admit", "worker", "worker", "admit", "admit"])

    def test_orphaned_admission_expires_without_a_late_worker_claim(self):
        payload = dispatch_payload(expires_at=200, callback_expires_at=200 + MAX_CALLBACK_GRACE_SECONDS)
        now = 100
        dispatch = StageDispatch.parse(payload, now=now)
        with tempfile.TemporaryDirectory() as directory:
            ledger = SQLiteDispatchLedger(Path(directory) / "ledger.sqlite3")
            try:
                admitted, cached = ledger.claim(dispatch, now=now, phase="admit")
                self.assertTrue(admitted)
                self.assertIsNone(cached)
                late_claim, late_result = ledger.claim(dispatch, now=200, phase="worker")
                self.assertFalse(late_claim)
                self.assertIsNone(late_result)
                row_count = ledger._db.execute("SELECT COUNT(*) FROM clipping_dispatches").fetchone()[0]
            finally:
                ledger.close()
        self.assertEqual(row_count, 0)

    def test_simulated_serial_modal_ledger_rpc_never_overlaps_volume_operations(self):
        now = int(time.time())
        serial_lock = threading.Lock()
        state_lock = threading.Lock()
        active = 0
        maximum_active = 0
        with tempfile.TemporaryDirectory() as directory:
            ledger_path = Path(directory) / "ledger.sqlite3"
            lifecycle = []

            def serialized_rpc(operation, request):
                nonlocal active, maximum_active
                with serial_lock:
                    with state_lock:
                        active += 1
                        maximum_active = max(maximum_active, active)
                    try:
                        time.sleep(0.002)
                        return run_ledger_rpc(
                            operation,
                            request,
                            ledger_factory=lambda: SQLiteDispatchLedger(ledger_path),
                            reload_volume=lambda: lifecycle.append("reload"),
                            commit_volume=lambda: lifecycle.append("commit"),
                            expected_origin="https://framevault.dev",
                            expected_pipeline_revision="framevault_m3_worker_v1",
                        )
                    finally:
                        with state_lock:
                            active -= 1

            remote = RemoteDispatchLedger(serialized_rpc)
            payloads = [
                dispatch_payload(
                    attempt_id=f"attempt_{index:02d}",
                    media_url=f"https://framevault.dev/api/clipping/worker-media/clip_job_01/attempt_{index:02d}",
                    callback_url=f"https://framevault.dev/api/clipping/worker-callbacks/clip_job_01/attempt_{index:02d}",
                    expires_at=now + 3600,
                    callback_expires_at=now + 3600 + MAX_CALLBACK_GRACE_SECONDS,
                )
                for index in range(2, 10)
            ]
            dispatches = [StageDispatch.parse(payload, now=now) for payload in payloads]
            failures = []

            def submit(dispatch):
                try:
                    return remote.claim(dispatch, now=now)[0]
                except Exception as error:  # collected for useful test diagnostics
                    failures.append(type(error).__name__)
                    return False

            threads = [threading.Thread(target=submit, args=(dispatch,)) for dispatch in dispatches]
            for thread in threads:
                thread.start()
            for thread in threads:
                thread.join(timeout=5)
            self.assertTrue(all(not thread.is_alive() for thread in threads))
        self.assertEqual(failures, [])
        self.assertEqual(maximum_active, 1)
        self.assertEqual(lifecycle.count("reload"), len(dispatches))
        self.assertEqual(lifecycle.count("commit"), len(dispatches))

    def test_redirect_handler_refuses_to_forward_capabilities(self):
        handler = NoRedirectHandler()
        self.assertIsNone(handler.redirect_request(None, None, 302, "Moved", {}, "https://other.example/"))

    def test_dns_rejects_private_and_mixed_public_private_answers(self):
        for answers in (
            [self._dns_answer("127.0.0.1")],
            [self._dns_answer("169.254.1.2")],
            [self._dns_answer("10.0.0.4")],
            [self._dns_answer("192.0.0.9")],
            [self._dns_answer("2001:db8::1")],
            [self._dns_answer("8.8.8.8"), self._dns_answer("192.168.1.8")],
        ):
            handler = PinnedHTTPSHandler(resolver=lambda *_args, answer_set=answers, **_kwargs: answer_set)
            with self.subTest(answers=answers), self.assertRaises(PinnedConnectionError):
                handler._public_addresses("framevault.dev", 443)

    def test_https_resolution_is_single_use_pinned_and_keeps_tls_hostname(self):
        resolutions = []

        def rebinding_resolver(host, port, *, type):
            resolutions.append((host, port, type))
            if len(resolutions) == 1:
                return [self._dns_answer("8.8.8.8")]
            return [self._dns_answer("127.0.0.1")]

        handler = PinnedHTTPSHandler(resolver=rebinding_resolver)
        captured = {}

        def capture_connection(factory, request):
            connection = factory(request.host, timeout=5)
            captured["connection"] = connection
            return "opened-without-network"

        handler.do_open = capture_connection
        response = handler.https_open(urllib_request.Request("https://framevault.dev/api/clipping/worker-media/job/attempt"))
        self.assertEqual(response, "opened-without-network")
        connection = captured["connection"]
        self.assertIsInstance(connection, PinnedHTTPSConnection)
        self.assertTrue(handler._context.check_hostname)
        tls = {}

        class FakeSocket:
            def close(self):
                pass

        class FakeTLSContext:
            check_hostname = True

            def wrap_socket(self, sock, *, server_hostname):
                tls["sock"] = sock
                tls["server_hostname"] = server_hostname
                return sock

        connection._context = FakeTLSContext()
        connected = []
        with patch("clipping_m3_worker.socket.create_connection", side_effect=lambda address, *_args: connected.append(address) or FakeSocket()):
            connection.connect()
        self.assertEqual(len(resolutions), 1)
        self.assertEqual(connected, [("8.8.8.8", 443)])
        self.assertEqual(tls["server_hostname"], "framevault.dev")
        self.assertTrue(connection._context.check_hostname)
        connection.sock = None

    def test_worker_opener_ignores_ambient_proxy_configuration(self):
        with patch.dict(os.environ, {"HTTPS_PROXY": "http://127.0.0.1:8080", "https_proxy": "http://127.0.0.1:8080"}):
            opener = _opener(resolver=lambda *_args, **_kwargs: [self._dns_answer("8.8.8.8")])
        proxies = [handler for handler in opener.handlers if isinstance(handler, urllib_request.ProxyHandler)]
        self.assertEqual(proxies, [])


if __name__ == "__main__":
    unittest.main()
