"""Offline ASGI contract tests for the SkyReels Modal worker."""

from __future__ import annotations

import ast
import asyncio
import importlib.util
import json
import os
import sys
import tempfile
import types
import unittest
from email.message import Message
from pathlib import Path
from types import SimpleNamespace
from typing import Any
from urllib import error as urllib_error
from urllib import request as urllib_request
from unittest.mock import patch

import httpx


WORKER_PATH = Path(__file__).with_name("skyreels.py")
_SPEC = importlib.util.spec_from_file_location("skyreels_worker_tests", WORKER_PATH)
if _SPEC is None or _SPEC.loader is None:
    raise RuntimeError(f"unable to load worker module from {WORKER_PATH}")
WORKER = importlib.util.module_from_spec(_SPEC)
_SPEC.loader.exec_module(WORKER)


API_KEY = "offline-test-modal-key"
BASE_PATH = "/api/v1/videos"
CALLBACK_URL = "https://dashboard.example/api/provider-callbacks/modal"
CALLBACK_TOKEN = "a" * 43


class _FakeTime:
    def __init__(self) -> None:
        self.sleeps: list[int] = []

    def sleep(self, seconds: int) -> None:
        self.sleeps.append(seconds)


class _FakeResponse:
    def __init__(self, status: int, headers: Message | None = None) -> None:
        self.status = status
        self.headers = headers or Message()

    def getcode(self) -> int:
        return self.status

    def __enter__(self):
        return self

    def __exit__(self, *_):
        return False


def _extract_callback_helper(name: str, namespace: dict[str, Any]) -> Any:
    tree = ast.parse(WORKER_PATH.read_text(encoding="utf-8"))
    for node in tree.body:
        if not isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef, ast.ClassDef)) or node.name != name:
            continue
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)):
            node.decorator_list = []
        module = ast.Module(body=[node], type_ignores=[])
        ast.fix_missing_locations(module)
        exec(compile(module, f"<extracted {name}>", "exec"), namespace)
        return namespace[name]
    raise AssertionError(f"{name} is missing from skyreels.py")


def _extract_worker_class(name: str, namespace: dict[str, Any]) -> type:
    tree = ast.parse(WORKER_PATH.read_text(encoding="utf-8"))
    for node in tree.body:
        if not isinstance(node, ast.ClassDef) or node.name != name:
            continue
        node.decorator_list = []
        for member in node.body:
            if isinstance(member, (ast.FunctionDef, ast.AsyncFunctionDef)):
                member.decorator_list = []
        module = ast.Module(body=[node], type_ignores=[])
        ast.fix_missing_locations(module)
        exec(compile(module, f"<extracted {name}>", "exec"), namespace)
        return namespace[name]
    raise AssertionError(f"{name} is missing from skyreels.py")


def _callback_namespace() -> tuple[dict[str, Any], list[tuple[str, dict[str, Any]]], _FakeTime]:
    logs: list[tuple[str, dict[str, Any]]] = []
    fake_time = _FakeTime()
    namespace: dict[str, Any] = {
        "Any": Any,
        "json": json,
        "urllib_error": urllib_error,
        "urllib_request": urllib_request,
        "time": fake_time,
        "CALLBACK_DELIVERY_MAX_ATTEMPTS": 5,
        "CALLBACK_DELIVERY_TIMEOUT_SECONDS": 10,
        "CALLBACK_DELIVERY_MAX_RETRY_AFTER_SECONDS": 15,
        "log_event": lambda event, **fields: logs.append((event, fields)),
    }
    return namespace, logs, fake_time


class _VolumeMethod:
    def __call__(self, *_args, **_kwargs) -> None:
        return None

    async def aio(self, *_args, **_kwargs) -> None:
        return None


class _Volume:
    def __init__(self) -> None:
        self.commit = _VolumeMethod()
        self.reload = _VolumeMethod()


class _FailSecondCommitVolume(_Volume):
    def __init__(self) -> None:
        super().__init__()
        self.commit = self._Commit()

    class _Commit:
        def __init__(self) -> None:
            self.calls = 0

        def __call__(self, *_args, **_kwargs) -> None:
            return None

        async def aio(self, *_args, **_kwargs) -> None:
            self.calls += 1
            if self.calls == 2:
                    raise OSError("dispatch reference volume commit failed")


class _FailThirdCommitVolume(_Volume):
    def __init__(self) -> None:
        super().__init__()
        self.commit = self._Commit()

    class _Commit:
        def __init__(self) -> None:
            self.calls = 0

        def __call__(self, *_args, **_kwargs) -> None:
            return None

        async def aio(self, *_args, **_kwargs) -> None:
            self.calls += 1
            if self.calls == 3:
                raise OSError("dispatch reference volume commit failed")


class _Claims:
    def __init__(self) -> None:
        self.values: dict[str, Any] = {}

        async def get(key: str):
            return self.values.get(key)

        async def put(key: str, value: dict[str, Any], skip_if_exists: bool = False):
            if skip_if_exists and key in self.values:
                return False
            self.values[key] = value
            return True

        self.get = SimpleNamespace(aio=get)
        self.put = SimpleNamespace(aio=put)


class _Dispatch:
    def __init__(self) -> None:
        self.calls: list[dict[str, object]] = []
        self.error: Exception | None = None
        self.before_return = None

    async def aio(self, **kwargs):
        self.calls.append(kwargs)
        if self.before_return is not None:
            await self.before_return(kwargs)
        if self.error is not None:
            raise self.error
        return SimpleNamespace(object_id="fc-offline-test")


class _VideoGeneratorFactory:
    dispatch: _Dispatch

    def __call__(self):
        return SimpleNamespace(
            generate=SimpleNamespace(spawn=SimpleNamespace(aio=self.dispatch.aio))
        )


class SkyReelsCallbackDeliveryTests(unittest.TestCase):
    def test_redirects_are_not_followed_and_are_terminal(self) -> None:
        namespace, logs, fake_time = _callback_namespace()
        handler_type = _extract_callback_helper("CallbackNoRedirectHandler", namespace)
        original = urllib_request.Request(
            CALLBACK_URL,
            data=b"{}",
            method="POST",
            headers={"X-Modal-Callback-Token": CALLBACK_TOKEN},
        )
        self.assertIsNone(
            handler_type().redirect_request(
                original,
                None,
                302,
                "Found",
                Message(),
                "https://redirect-target.example/capture",
            )
        )

        _extract_callback_helper("open_callback_request", namespace)
        deliver = _extract_callback_helper("deliver_terminal_callback", namespace)
        calls: list[int] = []

        def redirected(_request, timeout):
            calls.append(timeout)
            raise urllib_error.HTTPError(CALLBACK_URL, 302, "Found", Message(), None)

        namespace["open_callback_request"] = redirected
        deliver(CALLBACK_URL, CALLBACK_TOKEN, {"id": "job", "status": "completed"})
        self.assertEqual(calls, [10])
        self.assertEqual(fake_time.sleeps, [])
        self.assertEqual(logs, [("callback_rejected", {"job": "job", "status": 302})])

    def test_delivery_retries_transient_errors_without_serializing_credentials(self) -> None:
        namespace, logs, fake_time = _callback_namespace()
        _extract_callback_helper("CallbackNoRedirectHandler", namespace)
        _extract_callback_helper("open_callback_request", namespace)
        deliver = _extract_callback_helper("deliver_terminal_callback", namespace)
        payload = {
            "id": "job",
            "status": "completed",
            "model": WORKER.MODEL_ID,
        }
        sent = []
        responses = [
            urllib_error.HTTPError(CALLBACK_URL, 503, "Unavailable", Message(), None),
            _FakeResponse(204),
        ]

        def transient_then_success(request, timeout):
            sent.append((request, timeout))
            response = responses.pop(0)
            if isinstance(response, Exception):
                raise response
            return response

        namespace["open_callback_request"] = transient_then_success
        deliver(CALLBACK_URL, CALLBACK_TOKEN, payload)
        self.assertEqual(len(sent), 2)
        self.assertEqual(fake_time.sleeps, [1])
        self.assertEqual(json.loads(sent[0][0].data), payload)
        self.assertNotIn(CALLBACK_TOKEN, sent[0][0].data.decode())
        self.assertEqual(sent[0][0].get_header("X-modal-callback-token"), CALLBACK_TOKEN)
        self.assertEqual(logs[-1], ("callback_delivered", {"job": "job", "attempt": 2}))

        logs.clear()
        fake_time.sleeps.clear()
        sent.clear()

        def rejected(request, timeout):
            sent.append((request, timeout))
            raise urllib_error.HTTPError(CALLBACK_URL, 401, "Unauthorized", Message(), None)

        namespace["open_callback_request"] = rejected
        deliver(CALLBACK_URL, CALLBACK_TOKEN, payload)
        self.assertEqual(len(sent), 1)
        self.assertEqual(fake_time.sleeps, [])
        self.assertEqual(logs, [("callback_rejected", {"job": "job", "status": 401})])

    def test_delivery_exhaustion_caps_retry_after_without_logging_credentials(self) -> None:
        namespace, logs, fake_time = _callback_namespace()
        _extract_callback_helper("CallbackNoRedirectHandler", namespace)
        _extract_callback_helper("open_callback_request", namespace)
        deliver = _extract_callback_helper("deliver_terminal_callback", namespace)
        calls: list[int] = []

        def throttled(_request, timeout):
            calls.append(timeout)
            headers = Message()
            headers["Retry-After"] = "99999"
            raise urllib_error.HTTPError(CALLBACK_URL, 429, "Too Many Requests", headers, None)

        namespace["open_callback_request"] = throttled
        deliver(CALLBACK_URL, CALLBACK_TOKEN, {"id": "job", "status": "completed"})
        self.assertEqual(calls, [10, 10, 10, 10, 10])
        self.assertEqual(fake_time.sleeps, [15, 15, 15, 15])
        self.assertEqual(
            logs,
            [("callback_delivery_exhausted", {"job": "job", "attempts": 5})],
        )
        self.assertNotIn(CALLBACK_TOKEN, repr(logs))

    def test_terminal_payload_is_credential_free_and_has_a_safe_failure(self) -> None:
        completed = WORKER.terminal_callback_payload("job", "completed", WORKER.MODEL_ID)
        self.assertEqual(completed, {"id": "job", "status": "completed", "model": WORKER.MODEL_ID})
        self.assertNotIn("callback_token", completed)
        failed = WORKER.terminal_callback_payload("job", "failed", WORKER.MODEL_ID)
        self.assertEqual(failed["error"], "Video generation failed")

    def test_new_callback_uses_bearer_auth_and_modal_retry_limit_is_supported(self) -> None:
        callback_url = "https://dashboard.example/api/video-callbacks/client-job-123"
        payload = {
            "id": "client-job-123",
            "status": "processing",
            "sequence": 1,
            "progress": 5,
            "stage": "starting",
        }
        request = WORKER.urllib_request.Request(
            callback_url,
            data=json.dumps(payload).encode("utf-8"),
            method="POST",
            headers={"Authorization": f"Bearer {CALLBACK_TOKEN}"},
        )
        self.assertEqual(request.get_header("Authorization"), f"Bearer {CALLBACK_TOKEN}")
        self.assertIsNone(
            _extract_callback_helper(
                "CallbackNoRedirectHandler", {"urllib_request": urllib_request}
            )().redirect_request(
                request,
                None,
                307,
                "Temporary Redirect",
                Message(),
                "https://attacker.example/capture",
            )
        )

        tree = ast.parse(WORKER_PATH.read_text(encoding="utf-8"))
        supervisor = next(
            node for node in tree.body
            if isinstance(node, ast.AsyncFunctionDef) and node.name == "callback_video_supervisor"
        )
        retry_call = next(
            node for decorator in supervisor.decorator_list for node in ast.walk(decorator)
            if isinstance(node, ast.Call)
            and isinstance(node.func, ast.Attribute)
            and node.func.attr == "Retries"
        )
        retry_options = {keyword.arg: ast.literal_eval(keyword.value) for keyword in retry_call.keywords}
        self.assertEqual(retry_options["max_retries"], 10)
        self.assertLessEqual(retry_options["max_delay"], 60)
        self.assertTrue(
            any(
                isinstance(handler.type, ast.Attribute)
                and handler.type.attr == "CancelledError"
                for node in ast.walk(supervisor)
                if isinstance(node, ast.Try)
                for handler in node.handlers
            )
        )
        self.assertTrue(
            any(
                isinstance(node, ast.Attribute) and node.attr == "cancel"
                for node in ast.walk(supervisor)
            )
        )


class SkyReelsSupervisorTests(unittest.IsolatedAsyncioTestCase):
    async def test_permanent_initial_callback_rejection_stops_before_generation(self) -> None:
        job_id = "client-job-initial-callback-rejected"
        stored = {"id": job_id, "status": "pending", "progress": 0}
        generation_calls = 0

        async def read_job(_job_id: str):
            return dict(stored)

        async def write_job(_job_id: str, value: dict[str, Any]):
            stored.clear()
            stored.update(value)

        async def run_generation():
            nonlocal generation_calls
            generation_calls += 1

        result = await WORKER.run_callback_supervisor(
            job_id,
            f"https://dashboard.example/api/video-callbacks/{job_id}",
            CALLBACK_TOKEN,
            run_generation,
            read_job,
            write_job,
            post_once=lambda *_args: 403,
            sleep=lambda _seconds: None,
        )

        self.assertEqual(generation_calls, 0)
        self.assertEqual(result["status"], "failed")
        self.assertEqual(result["error_code"], "callback_delivery_rejected")
        self.assertTrue(result["delivery_rejected"])
        self.assertEqual(stored["status"], "failed")
        self.assertEqual(stored["callback_delivery"]["status_code"], 403)
        self.assertNotIn(CALLBACK_TOKEN, repr(stored))

    async def test_transient_initial_callback_exhaustion_stays_retryable_before_generation(self) -> None:
        job_id = "client-job-initial-callback-transient"
        stored = {"id": job_id, "status": "pending", "progress": 0}
        generation_calls = 0

        async def read_job(_job_id: str):
            return dict(stored)

        async def write_job(_job_id: str, value: dict[str, Any]):
            stored.clear()
            stored.update(value)

        async def run_generation():
            nonlocal generation_calls
            generation_calls += 1

        with self.assertRaises(WORKER.CallbackDeliveryError):
            await WORKER.run_callback_supervisor(
                job_id,
                f"https://dashboard.example/api/video-callbacks/{job_id}",
                CALLBACK_TOKEN,
                run_generation,
                read_job,
                write_job,
                post_once=lambda *_args: 503,
                sleep=lambda _seconds: None,
            )

        self.assertEqual(generation_calls, 0)
        self.assertEqual(stored["status"], "in_progress")
        self.assertEqual(stored["callback_delivery"]["state"], "retry_exhausted")
        self.assertEqual(stored["callback_delivery"]["status_code"], 503)

    async def test_permanent_callback_rejection_is_durable_and_replay_skips_generation(self) -> None:
        job_id = "client-job-callback-rejected"
        stored = {
            "id": job_id,
            "status": "completed",
            "stage": "completed",
            "progress": 100,
            "model": WORKER.MODEL_ID,
        }
        writes: list[dict[str, Any]] = []
        callbacks: list[dict[str, Any]] = []
        generation_calls = 0

        async def read_job(_job_id: str):
            return dict(stored)

        async def write_job(_job_id: str, value: dict[str, Any]):
            nonlocal stored
            stored = dict(value)
            writes.append(dict(value))

        async def run_generation():
            nonlocal generation_calls
            generation_calls += 1

        def reject_callback(_url, _token, payload):
            callbacks.append(dict(payload))
            return 403

        for _ in range(2):
            result = await WORKER.run_callback_supervisor(
                job_id,
                f"https://dashboard.example/api/video-callbacks/{job_id}",
                CALLBACK_TOKEN,
                run_generation,
                read_job,
                write_job,
                post_once=reject_callback,
                sleep=lambda _seconds: None,
            )
            self.assertTrue(result["delivery_rejected"])
            self.assertEqual(result["delivery_status"], 403)

        self.assertEqual(generation_calls, 0)
        self.assertEqual(len(callbacks), 2)
        self.assertEqual(stored["status"], "completed")
        self.assertEqual(
            stored["callback_delivery"],
            {"state": "rejected", "code": "http_403", "status_code": 403, "attempts": 1},
        )
        self.assertNotIn(CALLBACK_TOKEN, repr(stored))
        self.assertEqual(WORKER.callback_delivery_summary(stored), stored["callback_delivery"])

    async def test_startup_error_is_persisted_then_sent_as_terminal_callback(self) -> None:
        job_id = "client-job-startup"
        stored = {
            "id": job_id,
            "status": "in_progress",
            "stage": "starting",
            "progress": 5,
            "model": WORKER.MODEL_ID,
        }
        writes: list[dict[str, Any]] = []
        callbacks: list[dict[str, Any]] = []

        async def read_job(_job_id: str):
            return dict(stored)

        async def write_job(_job_id: str, value: dict[str, Any]):
            nonlocal stored
            stored = dict(value)
            writes.append(dict(value))

        async def failed_startup():
            raise RuntimeError("private model startup detail")

        def accept_callback(_url, token, payload):
            callbacks.append(dict(payload))
            self.assertEqual(token, CALLBACK_TOKEN)
            return 204

        result = await WORKER.run_callback_supervisor(
            job_id,
            "https://dashboard.example/api/video-callbacks/client-job-startup",
            CALLBACK_TOKEN,
            failed_startup,
            read_job,
            write_job,
            post_once=accept_callback,
            sleep=lambda _seconds: None,
        )
        self.assertEqual(stored["status"], "failed")
        self.assertEqual(stored["error_code"], "worker_startup_failed")
        self.assertNotIn("private model startup detail", json.dumps(stored))
        self.assertEqual([item["sequence"] for item in callbacks], [1, 2])
        self.assertEqual(callbacks[-1]["status"], "failed")
        self.assertEqual(callbacks[-1]["error_code"], "worker_startup_failed")
        self.assertEqual(result["sequence"], 2)

    async def test_cancellation_is_not_converted_to_a_terminal_failure(self) -> None:
        job_id = "client-job-cancel"
        stored = {"id": job_id, "status": "in_progress", "stage": "inference", "progress": 20}
        writes: list[dict[str, Any]] = []

        async def read_job(_job_id: str):
            return dict(stored)

        async def write_job(_job_id: str, value: dict[str, Any]):
            writes.append(dict(value))

        async def cancelled():
            raise asyncio.CancelledError()

        with self.assertRaises(asyncio.CancelledError):
            await WORKER.run_callback_supervisor(
                job_id,
                "https://dashboard.example/api/video-callbacks/client-job-cancel",
                CALLBACK_TOKEN,
                cancelled,
                read_job,
                write_job,
                post_once=lambda *_args: 204,
                sleep=lambda _seconds: None,
            )
        self.assertEqual(writes, [])
        self.assertNotEqual(stored["status"], "failed")


class SkyReelsGenerateCallbackTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temp_dir = tempfile.TemporaryDirectory()
        self.jobs_dir = Path(self.temp_dir.name)
        self.logs: list[tuple[str, dict[str, Any]]] = []
        self.deliveries: list[dict[str, Any]] = []

        def capture_schedule(job_id, callback_url, callback_token, payload) -> None:
            self.deliveries.append(
                {
                    "job_id": job_id,
                    "callback_url": callback_url,
                    "callback_token": callback_token,
                    "payload": payload,
                    "job": WORKER.read_job(job_id),
                    "video_exists": WORKER.job_video_path(job_id).is_file(),
                }
            )

        self.patches = [
            patch.object(WORKER, "JOBS_DIR", str(self.jobs_dir)),
            patch.object(WORKER, "jobs_volume", _Volume()),
            patch.object(WORKER, "schedule_terminal_callback", capture_schedule),
        ]
        for active_patch in self.patches:
            active_patch.start()
        namespace: dict[str, Any] = {
            "Any": Any,
            "Path": Path,
            "time": WORKER.time,
            "jobs_volume": WORKER.jobs_volume,
            "safe_job_id": WORKER.safe_job_id,
            "read_job": WORKER.read_job,
            "write_job": WORKER.write_job,
            "job_video_path": WORKER.job_video_path,
            "SIZE_MAP": WORKER.SIZE_MAP,
            "duration_to_frames": WORKER.duration_to_frames,
            "NUM_INFERENCE_STEPS": WORKER.NUM_INFERENCE_STEPS,
            "GUIDANCE_SCALE": WORKER.GUIDANCE_SCALE,
            "FPS": WORKER.FPS,
            "queue_terminal_callback": WORKER.queue_terminal_callback,
            "classify_worker_failure": WORKER.classify_worker_failure,
            "log_event": lambda event, **fields: self.logs.append((event, fields)),
        }
        self.generator_type = _extract_worker_class("VideoGenerator", namespace)

    def tearDown(self) -> None:
        for active_patch in reversed(self.patches):
            active_patch.stop()
        self.temp_dir.cleanup()

    def run_generate(self, job_id: str, pipe) -> None:
        class FakeCUDA:
            def empty_cache(self) -> None:
                return None

        class FakeGenerator:
            def manual_seed(self, _seed: int):
                return self

        class FakeTorch:
            cuda = FakeCUDA()

            def Generator(self, device: str):
                self.device = device
                return FakeGenerator()

        def export_to_video(_frames, path, **_kwargs) -> None:
            Path(path).write_bytes(b"offline-mp4")

        fake_diffusers = types.ModuleType("diffusers")
        fake_utils = types.ModuleType("diffusers.utils")
        fake_utils.export_to_video = export_to_video
        fake_diffusers.utils = fake_utils
        generator = self.generator_type()
        generator.runtime_gpu_name = "L40S"
        generator.torch = FakeTorch()
        generator.pipe = pipe
        with patch.dict(sys.modules, {"diffusers": fake_diffusers, "diffusers.utils": fake_utils}):
            generator.generate(
                job_id=job_id,
                prompt="A small red kite over a Toronto park at sunrise.",
                model=WORKER.MODEL_ID,
                duration=6,
                resolution="480p",
                aspect_ratio="9:16",
                seed=7,
                callback_url=CALLBACK_URL,
                callback_token=CALLBACK_TOKEN,
            )

    def test_completed_job_and_video_are_durable_before_callback(self) -> None:
        class SuccessfulPipe:
            def __call__(self, **_kwargs):
                return SimpleNamespace(frames=[["frame"]])

        self.run_generate("gen_callback_complete", SuccessfulPipe())
        self.assertEqual(len(self.deliveries), 1)
        delivery = self.deliveries[0]
        self.assertEqual(delivery["job_id"], "gen_callback_complete")
        self.assertTrue(delivery["video_exists"])
        self.assertEqual(delivery["job"]["status"], "completed")
        self.assertEqual(delivery["job"]["progress"], 100)
        self.assertEqual(delivery["job"]["filename"], "gen_callback_complete.mp4")
        self.assertEqual(
            delivery["payload"],
            {
                "id": "gen_callback_complete",
                "status": "completed",
                "model": WORKER.MODEL_ID,
            },
        )
        self.assertNotIn("callback_url", delivery["payload"])
        self.assertNotIn("callback_token", delivery["payload"])
        self.assertNotIn(CALLBACK_TOKEN, repr(self.logs))

    def test_worker_failure_is_durable_and_callback_error_is_safe(self) -> None:
        secret = "worker-inference-secret"

        class FailingPipe:
            def __call__(self, **_kwargs):
                raise RuntimeError(secret)

        with self.assertRaisesRegex(RuntimeError, secret):
            self.run_generate("gen_callback_failed", FailingPipe())
        self.assertEqual(len(self.deliveries), 1)
        delivery = self.deliveries[0]
        self.assertEqual(delivery["job"]["status"], "failed")
        self.assertNotIn(secret, delivery["job"]["error"])
        self.assertEqual(
            delivery["payload"],
            {
                "id": "gen_callback_failed",
                "status": "failed",
                "model": WORKER.MODEL_ID,
                "error": "Video generation failed",
            },
        )
        self.assertNotIn(secret, json.dumps(delivery["payload"]))
        self.assertNotIn(CALLBACK_TOKEN, repr(self.logs))


class SkyReelsAPITests(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self) -> None:
        self.temp_dir = tempfile.TemporaryDirectory()
        self.jobs_dir = Path(self.temp_dir.name)
        self.dispatch = _Dispatch()
        self.supervisor_dispatch = _Dispatch()
        self.claims = _Claims()
        self.callback_deliveries: list[dict[str, object]] = []
        self.original_generator = WORKER.VideoGenerator
        self.original_reconcile = WORKER.reconcile_modal_call

        async def no_remote_reconcile(job):
            return job

        def capture_callback(job_id, callback_url, callback_token, payload) -> None:
            self.callback_deliveries.append(
                {
                    "job_id": job_id,
                    "callback_url": callback_url,
                    "callback_token": callback_token,
                    "payload": payload,
                }
            )

        self.patches = [
            patch.object(WORKER, "JOBS_DIR", str(self.jobs_dir)),
            patch.object(WORKER, "jobs_volume", _Volume()),
            patch.object(WORKER, "job_claims", self.claims),
            patch.object(WORKER, "reconcile_modal_call", no_remote_reconcile),
            patch.object(WORKER, "schedule_terminal_callback", capture_callback),
            patch.dict(os.environ, {"MODAL_VIDEO_API_KEY": API_KEY}),
        ]
        for active_patch in self.patches:
            active_patch.start()
        _VideoGeneratorFactory.dispatch = self.dispatch
        self.generator_patch = patch.object(WORKER, "VideoGenerator", _VideoGeneratorFactory())
        self.generator_patch.start()
        self.supervisor_patch = patch.object(
            WORKER,
            "callback_video_supervisor",
            SimpleNamespace(spawn=SimpleNamespace(aio=self.supervisor_dispatch.aio)),
        )
        self.supervisor_patch.start()

        app = WORKER.create_api()
        self.client = httpx.AsyncClient(
            transport=httpx.ASGITransport(app=app),
            base_url="http://worker.test",
        )

    async def asyncTearDown(self) -> None:
        await self.client.aclose()
        self.supervisor_patch.stop()
        self.generator_patch.stop()
        for active_patch in reversed(self.patches):
            active_patch.stop()
        self.temp_dir.cleanup()

    def auth(self) -> dict[str, str]:
        return {"Authorization": f"Bearer {API_KEY}"}

    def request_body(self, **updates) -> dict[str, object]:
        body: dict[str, object] = {
            "model": WORKER.MODEL_ID,
            "prompt": "A small red kite over a Toronto park at sunrise.",
            "duration": 6,
            "resolution": "480p",
            "aspect_ratio": "9:16",
            "generate_audio": False,
            "callback_url": CALLBACK_URL,
            "callback_token": CALLBACK_TOKEN,
        }
        body.update(updates)
        return body

    def stable_request_body(self, job_id: str = "client-job-123", **updates) -> dict[str, object]:
        body = self.request_body(
            job_id=job_id,
            callback_url=f"https://dashboard.example/api/video-callbacks/{job_id}",
        )
        body.update(updates)
        return body

    def assert_error_envelope(self, response: httpx.Response) -> str:
        try:
            payload = response.json()
        except ValueError as error:
            self.fail(f"error response was not JSON: {response.text!r} ({error})")
        self.assertEqual(set(payload), {"error"}, payload)
        self.assertIsInstance(payload["error"], dict)
        self.assertIsInstance(payload["error"].get("message"), str)
        return payload["error"]["message"]

    async def submit(self, **updates) -> tuple[str, httpx.Response]:
        response = await self.client.post(
            BASE_PATH,
            headers=self.auth(),
            json=self.request_body(**updates),
        )
        self.assertEqual(response.status_code, 202, response.text)
        job_id = response.json().get("id", "")
        self.assertTrue(WORKER.safe_job_id(job_id), response.json())
        return job_id, response

    async def test_models_advertise_the_project_combination_and_auth_is_required(self) -> None:
        health = await self.client.get("/health")
        self.assertEqual(health.status_code, 200)
        self.assertEqual(health.json()["status"], "ok")

        unauthorized = await self.client.get(f"{BASE_PATH}/models")
        self.assertEqual(unauthorized.status_code, 401)
        self.assertEqual(unauthorized.headers.get("www-authenticate"), "Bearer")
        self.assert_error_envelope(unauthorized)

        response = await self.client.get(f"{BASE_PATH}/models", headers=self.auth())
        self.assertEqual(response.status_code, 200, response.text)
        models = response.json().get("data")
        self.assertEqual(len(models), 1)
        model = models[0]
        self.assertEqual(model["id"], "modal/skyreels-v2-t2v-14b")
        self.assertEqual(model["name"], WORKER.MODEL_NAME)
        self.assertIn(6, model["supported_durations"])
        self.assertIn("480p", model["supported_resolutions"])
        self.assertIn("9:16", model["supported_aspect_ratios"])
        self.assertIs(model["generate_audio"], False)
        self.assertEqual(model["pricing_skus"], {})

    async def test_job_submission_polling_completion_and_range_download(self) -> None:
        job_id, accepted = await self.submit()
        self.assertEqual(accepted.json()["status"], "pending")
        self.assertEqual(accepted.json()["model"], WORKER.MODEL_ID)
        self.assertEqual(len(self.dispatch.calls), 1)
        self.assertEqual(self.dispatch.calls[0]["duration"], 6)
        self.assertEqual(self.dispatch.calls[0]["resolution"], "480p")
        self.assertEqual(self.dispatch.calls[0]["aspect_ratio"], "9:16")
        self.assertEqual(self.dispatch.calls[0]["prompt"], self.request_body()["prompt"])
        self.assertNotIn("callback_url", self.dispatch.calls[0])
        self.assertNotIn("callback_token", self.dispatch.calls[0])
        self.assertNotIn("callback_url", accepted.json())
        self.assertNotIn("callback_token", accepted.json())

        initial = json.loads((self.jobs_dir / f"{job_id}.json").read_text(encoding="utf-8"))
        self.assertEqual(initial["status"], "pending")
        self.assertNotIn("modal_call_id", initial)
        self.assertEqual(await WORKER.read_modal_call_id_async(job_id), "fc-offline-test")

        queued = await self.client.get(f"{BASE_PATH}/{job_id}", headers=self.auth())
        self.assertEqual(queued.status_code, 200, queued.text)
        self.assertEqual(queued.json()["status"], "pending")
        self.assertEqual(queued.headers.get("retry-after"), str(WORKER.STATUS_POLL_RETRY_SECONDS))
        self.assertNotIn("callback_url", queued.json())
        self.assertNotIn("callback_token", queued.json())

        completed_job = {
            **initial,
            "status": "completed",
            "progress": 100,
            "gpu_seconds": 72.5,
            "gpu_name": "L40S",
            "callback_delivery": {
                "state": "rejected",
                "code": "http_403",
                "status_code": 403,
                "attempts": 1,
            },
        }
        await WORKER.write_job_async(job_id, completed_job)
        video = b"0123456789-video-content"
        (self.jobs_dir / f"{job_id}.mp4").write_bytes(video)

        completed = await self.client.get(f"{BASE_PATH}/{job_id}", headers=self.auth())
        self.assertEqual(completed.status_code, 200, completed.text)
        payload = completed.json()
        self.assertEqual(payload["status"], "completed")
        self.assertEqual(payload["progress"], 100)
        self.assertEqual(payload["callback_delivery"], completed_job["callback_delivery"])
        self.assertNotIn(CALLBACK_TOKEN, json.dumps(payload))
        self.assertEqual(payload["unsigned_urls"], [
            f"http://worker.test{BASE_PATH}/{job_id}/content?index=0"
        ])
        self.assertEqual(payload["usage"]["gpu_seconds"], 72.5)
        self.assertNotIn("cost", payload["usage"])

        content = await self.client.get(
            f"{BASE_PATH}/{job_id}/content?index=0", headers=self.auth()
        )
        self.assertEqual(content.status_code, 200, content.text)
        self.assertEqual(content.headers.get("content-type"), "video/mp4")
        self.assertEqual(content.headers.get("accept-ranges"), "bytes")
        self.assertEqual(content.content, video)

        partial = await self.client.get(
            f"{BASE_PATH}/{job_id}/content?index=0",
            headers={**self.auth(), "Range": "bytes=2-5"},
        )
        self.assertEqual(partial.status_code, 206, partial.text)
        self.assertEqual(partial.headers.get("content-range"), f"bytes 2-5/{len(video)}")
        self.assertEqual(partial.content, video[2:6])

        unsatisfiable = await self.client.get(
            f"{BASE_PATH}/{job_id}/content?index=0",
            headers={**self.auth(), "Range": f"bytes={len(video)}-"},
        )
        self.assertEqual(unsatisfiable.status_code, 416, unsatisfiable.text)
        self.assertEqual(
            unsatisfiable.headers.get("content-range"), f"bytes */{len(video)}"
        )

    async def test_all_video_routes_require_bearer_authentication(self) -> None:
        job_id = "gen_auth_test"
        await WORKER.write_job_async(
            job_id,
            {"id": job_id, "status": "completed", "model": WORKER.MODEL_ID},
        )
        (self.jobs_dir / f"{job_id}.mp4").write_bytes(b"mp4")

        responses = [
            await self.client.get(f"{BASE_PATH}/models"),
            await self.client.post(BASE_PATH, json=self.request_body()),
            await self.client.get(f"{BASE_PATH}/{job_id}"),
            await self.client.get(f"{BASE_PATH}/{job_id}/content?index=0"),
        ]
        for response in responses:
            with self.subTest(path=str(response.request.url)):
                self.assertEqual(response.status_code, 401, response.text)
                self.assertEqual(response.headers.get("www-authenticate"), "Bearer")
                self.assert_error_envelope(response)

    async def test_invalid_json_types_capabilities_and_oversized_requests_are_rejected(self) -> None:
        malformed = await self.client.post(
            BASE_PATH,
            headers={**self.auth(), "Content-Type": "application/json"},
            content=b"{not-json",
        )
        self.assertEqual(malformed.status_code, 400, malformed.text)
        self.assert_error_envelope(malformed)

        wrong_content_type = await self.client.post(
            BASE_PATH,
            headers={**self.auth(), "Content-Type": "text/plain"},
            content=b"{}",
        )
        self.assertEqual(wrong_content_type.status_code, 415, wrong_content_type.text)
        self.assert_error_envelope(wrong_content_type)

        oversized = await self.client.post(
            BASE_PATH,
            headers={**self.auth(), "Content-Type": "application/json"},
            content=b'{"prompt":"' + b"x" * (WORKER.MAX_JSON_BODY_BYTES + 1) + b'"}',
        )
        self.assertEqual(oversized.status_code, 413, oversized.text)
        self.assert_error_envelope(oversized)

        invalid_bodies = [
            (b"[]", 400),
            (json.dumps(self.request_body(prompt=7)).encode(), 400),
            (json.dumps(self.request_body(prompt="  ")).encode(), 400),
            (json.dumps(self.request_body(prompt="x" * (WORKER.MAX_PROMPT_LENGTH + 1))).encode(), 400),
            (json.dumps(self.request_body(model="modal/other-model")).encode(), 400),
            (json.dumps(self.request_body(duration=1.5)).encode(), 400),
            (json.dumps(self.request_body(duration=True)).encode(), 400),
            (json.dumps(self.request_body(duration=WORKER.MAX_DURATION + 1)).encode(), 400),
            (json.dumps(self.request_body(resolution="1080p")).encode(), 400),
            (json.dumps(self.request_body(aspect_ratio="1:1")).encode(), 400),
            (json.dumps(self.request_body(generate_audio=True)).encode(), 400),
            (json.dumps(self.request_body(generate_audio="false")).encode(), 400),
            (json.dumps(self.request_body(callback_token=None)).encode(), 400),
            (json.dumps(self.request_body(callback_url=None)).encode(), 400),
            (json.dumps(self.request_body(callback_url="http://dashboard.example/callback")).encode(), 400),
            (json.dumps(self.request_body(callback_url="https://user:pass@dashboard.example/callback")).encode(), 400),
            (json.dumps(self.request_body(callback_url="https://dashboard.example/callback?token=x")).encode(), 400),
            (json.dumps(self.request_body(callback_token="short")).encode(), 400),
            (json.dumps(self.request_body(callback_token="!" * 43)).encode(), 400),
            (json.dumps(self.request_body(extra_option=1)).encode(), 400),
        ]
        for body, expected_status in invalid_bodies:
            with self.subTest(body=body[:80]):
                response = await self.client.post(
                    BASE_PATH,
                    headers={**self.auth(), "Content-Type": "application/json"},
                    content=body,
                )
                self.assertEqual(response.status_code, expected_status, response.text)
                self.assert_error_envelope(response)
        self.assertEqual(self.dispatch.calls, [])

        valid_boundary, _ = await self.submit(prompt="v" * WORKER.MAX_PROMPT_LENGTH)
        self.assertTrue((self.jobs_dir / f"{valid_boundary}.json").is_file())

    async def test_invalid_job_ids_unknown_jobs_and_output_indices_are_rejected(self) -> None:
        invalid_id = await self.client.get(f"{BASE_PATH}/has-invalid-id!", headers=self.auth())
        self.assertEqual(invalid_id.status_code, 400, invalid_id.text)
        self.assert_error_envelope(invalid_id)
        self.assertFalse(WORKER.safe_job_id("../outside"))

        unknown = await self.client.get(f"{BASE_PATH}/gen_missing_job", headers=self.auth())
        self.assertEqual(unknown.status_code, 404, unknown.text)
        self.assert_error_envelope(unknown)

        bad_index = await self.client.get(
            f"{BASE_PATH}/gen_missing_job/content?index=1", headers=self.auth()
        )
        self.assertEqual(bad_index.status_code, 404, bad_index.text)
        self.assert_error_envelope(bad_index)

    async def test_dispatch_failure_is_durable_and_never_echoes_exception_secrets(self) -> None:
        sentinel = "do-not-return-this-secret"
        self.dispatch.error = RuntimeError(f"Modal dispatch leaked {sentinel}")
        with patch.object(WORKER, "definitive_modal_rejection", return_value=True):
            response = await self.client.post(
                BASE_PATH,
                headers=self.auth(),
                json=self.request_body(),
            )
        self.assertEqual(response.status_code, 500, response.text)
        self.assertNotIn(sentinel, response.text)
        self.assert_error_envelope(response)

        records = [
            path for path in self.jobs_dir.glob("gen_*.json")
            if not path.name.endswith(".call.json")
        ]
        self.assertEqual(len(records), 1)
        failed = json.loads(records[0].read_text(encoding="utf-8"))
        self.assertEqual(failed["status"], "failed")
        self.assertNotIn(sentinel, failed.get("error", ""))
        self.assertEqual(len(self.callback_deliveries), 1)
        delivery = self.callback_deliveries[0]
        self.assertEqual(delivery["job_id"], failed["id"])
        self.assertEqual(delivery["callback_url"], CALLBACK_URL)
        self.assertEqual(delivery["callback_token"], CALLBACK_TOKEN)
        self.assertEqual(
            delivery["payload"],
            {
                "id": failed["id"],
                "status": "failed",
                "model": WORKER.MODEL_ID,
                "error": "Video generation failed",
            },
        )
        self.assertNotIn(CALLBACK_TOKEN, json.dumps(delivery["payload"]))

        status = await self.client.get(f"{BASE_PATH}/{failed['id']}", headers=self.auth())
        self.assertEqual(status.status_code, 200, status.text)
        self.assertEqual(status.json()["status"], "failed")
        self.assertNotIn(sentinel, status.text)

    async def test_ambiguous_gpu_ack_stays_pending_without_callback_failure(self) -> None:
        self.dispatch.error = RuntimeError("unknown dispatch acknowledgement")
        response = await self.client.post(
            BASE_PATH,
            headers=self.auth(),
            json=self.request_body(),
        )
        self.assertEqual(response.status_code, 202, response.text)
        job_id = response.json()["id"]
        stored = await WORKER.read_job_async(job_id)
        self.assertEqual(stored["status"], "pending")
        self.assertEqual(self.callback_deliveries, [])

    async def test_stable_callback_id_is_reused_and_changed_payload_conflicts(self) -> None:
        job_id = "client-job-123"
        body = self.stable_request_body(job_id)
        first = await self.client.post(BASE_PATH, headers=self.auth(), json=body)
        self.assertEqual(first.status_code, 202, first.text)
        self.assertEqual(first.json()["id"], job_id)
        self.assertEqual(len(self.supervisor_dispatch.calls), 1)
        self.assertEqual(self.dispatch.calls, [])

        private_job = await WORKER.read_job_async(job_id)
        self.assertEqual(private_job["callback_mode"], "callback")
        self.assertEqual(private_job["callback_token"], CALLBACK_TOKEN)
        self.assertNotIn("callback_token", first.text)
        self.assertNotIn(CALLBACK_TOKEN, repr(self.claims.values))
        self.assertNotIn(CALLBACK_URL, repr(self.claims.values))

        duplicate = await self.client.post(BASE_PATH, headers=self.auth(), json=body)
        self.assertEqual(duplicate.status_code, 202, duplicate.text)
        self.assertEqual(duplicate.json()["id"], job_id)
        self.assertEqual(len(self.supervisor_dispatch.calls), 2)
        self.assertEqual(self.dispatch.calls, [])

        conflict = await self.client.post(
            BASE_PATH,
            headers=self.auth(),
            json=self.stable_request_body(job_id, prompt="A different request."),
        )
        self.assertEqual(conflict.status_code, 409, conflict.text)
        self.assertEqual(len(self.supervisor_dispatch.calls), 2)

    async def test_no_callback_submission_remains_supported(self) -> None:
        body = self.request_body()
        body.pop("callback_url")
        body.pop("callback_token")
        response = await self.client.post(BASE_PATH, headers=self.auth(), json=body)
        self.assertEqual(response.status_code, 202, response.text)
        self.assertEqual(len(self.dispatch.calls), 1)
        self.assertNotIn("callback_url", self.dispatch.calls[0])
        self.assertNotIn("callback_token", self.dispatch.calls[0])
        stored = await WORKER.read_job_async(response.json()["id"])
        self.assertNotIn("callback_url", stored)
        self.assertNotIn("callback_token", stored)

    async def test_dispatch_id_write_does_not_overwrite_a_started_job(self) -> None:
        async def mark_started_before_return(kwargs) -> None:
            job = await WORKER.read_job_async(kwargs["job_id"])
            job.update({"status": "in_progress", "stage": "preparing", "progress": 5})
            await WORKER.write_job_async(kwargs["job_id"], job)

        self.dispatch.before_return = mark_started_before_return
        job_id, _ = await self.submit()
        stored = await WORKER.read_job_async(job_id)
        self.assertEqual(stored["status"], "in_progress")
        self.assertEqual(stored["stage"], "preparing")
        self.assertEqual(await WORKER.read_modal_call_id_async(job_id), "fc-offline-test")

    async def test_transient_and_terminal_modal_call_poll_errors_are_distinguished(self) -> None:
        transient_id = "gen_transient_poll"
        await WORKER.write_job_async(
            transient_id,
            {"id": transient_id, "status": "pending", "progress": 0},
        )
        await WORKER.write_modal_call_id_async(transient_id, "fc-transient")

        async def transient_error(*, timeout):
            raise OSError("temporary control-plane network interruption")

        transient_call = SimpleNamespace(get=SimpleNamespace(aio=transient_error))
        with patch.object(WORKER.modal.FunctionCall, "from_id", return_value=transient_call):
            pending = await self.original_reconcile(
                await WORKER.read_job_async(transient_id)
            )
        self.assertEqual(pending["status"], "pending")

        terminal_id = "gen_terminal_poll"
        await WORKER.write_job_async(
            terminal_id,
            {
                "id": terminal_id,
                "status": "pending",
                "progress": 0,
                "model": WORKER.MODEL_ID,
                "callback_url": CALLBACK_URL,
                "callback_token": CALLBACK_TOKEN,
            },
        )
        await WORKER.write_modal_call_id_async(terminal_id, "fc-terminal")
        sentinel = "poll-exception-secret"

        async def terminal_error(*, timeout):
            raise RuntimeError(sentinel)

        terminal_call = SimpleNamespace(get=SimpleNamespace(aio=terminal_error))
        with patch.object(WORKER.modal.FunctionCall, "from_id", return_value=terminal_call):
            failed = await self.original_reconcile(
                await WORKER.read_job_async(terminal_id)
            )
        self.assertEqual(failed["status"], "failed")
        self.assertNotIn(sentinel, failed["error"])
        persisted = await WORKER.read_job_async(terminal_id)
        self.assertEqual(persisted["status"], "failed")
        self.assertNotIn(sentinel, json.dumps(persisted))
        self.assertEqual(len(self.callback_deliveries), 1)
        self.assertEqual(self.callback_deliveries[0]["job_id"], terminal_id)
        self.assertEqual(
            self.callback_deliveries[0]["payload"],
            {
                "id": terminal_id,
                "status": "failed",
                "model": WORKER.MODEL_ID,
                "error": "Video generation failed",
            },
        )

    async def test_successful_spawn_stays_accepted_when_call_reference_save_fails(self) -> None:
        with patch.object(WORKER, "jobs_volume", _FailThirdCommitVolume()):
            response = await self.client.post(
                BASE_PATH,
                headers=self.auth(),
                json=self.request_body(),
            )

        self.assertEqual(response.status_code, 202, response.text)
        self.assertEqual(response.json()["status"], "pending")
        job_id = response.json()["id"]
        stored = json.loads((self.jobs_dir / f"{job_id}.json").read_text(encoding="utf-8"))
        self.assertEqual(stored["status"], "pending")
        self.assertEqual(len(self.dispatch.calls), 1)


if __name__ == "__main__":
    unittest.main()
