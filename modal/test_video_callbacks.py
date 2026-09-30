"""Offline tests for the Wan callback contract; no Modal or GPU calls are made."""

import asyncio
import copy
import importlib.util
import os
import sys
import types
import unittest
from pathlib import Path
from unittest import mock


def _async_noop(*_args, **_kwargs):
    async def result():
        return None

    return result()


class _FakeVolume:
    def __init__(self):
        self.commit = types.SimpleNamespace(aio=_async_noop)
        self.reload = types.SimpleNamespace(aio=_async_noop)


class _FakeDict:
    def __init__(self):
        self.values = {}
        self._lock = asyncio.Lock()
        self.get = types.SimpleNamespace(aio=self._get)
        self.put = types.SimpleNamespace(aio=self._put)

    async def _get(self, key, default=None):
        return copy.deepcopy(self.values.get(key, default))

    async def _put(self, key, value, skip_if_exists=False):
        async with self._lock:
            if skip_if_exists and key in self.values:
                return False
            self.values[key] = copy.deepcopy(value)
            return True


class _FakeImage:
    def __getattr__(self, _name):
        return lambda *_args, **_kwargs: self


class _FakeApp:
    @staticmethod
    def function(*_args, **_kwargs):
        return lambda value: value

    @staticmethod
    def cls(*_args, **_kwargs):
        return lambda value: value


def _load_worker_module():
    fake_modal = types.ModuleType("modal")
    fake_modal.App = lambda *_args, **_kwargs: _FakeApp()
    fake_modal.Volume = types.SimpleNamespace(from_name=lambda *_args, **_kwargs: _FakeVolume())
    fake_modal.Dict = types.SimpleNamespace(from_name=lambda *_args, **_kwargs: _FakeDict())
    fake_modal.Secret = types.SimpleNamespace(from_name=lambda *_args, **_kwargs: object())
    fake_modal.Image = types.SimpleNamespace(debian_slim=lambda *_args, **_kwargs: _FakeImage())
    fake_modal.Retries = lambda **kwargs: types.SimpleNamespace(**kwargs)
    class _TransientModalError(Exception):
        pass

    class _DefinitiveModalError(Exception):
        pass

    fake_modal.exception = types.SimpleNamespace(
        ClientClosed=_TransientModalError,
        ConnectionError=_TransientModalError,
        InternalError=_TransientModalError,
        InternalFailure=_TransientModalError,
        ResourceExhaustedError=_TransientModalError,
        ServiceError=_TransientModalError,
        TimeoutError=_TransientModalError,
        InvalidError=_DefinitiveModalError,
        AuthError=_DefinitiveModalError,
        PermissionDeniedError=_DefinitiveModalError,
        NotFoundError=_DefinitiveModalError,
        VersionError=_DefinitiveModalError,
        UnimplementedError=_DefinitiveModalError,
        RequestSizeError=_DefinitiveModalError,
    )
    fake_modal.FunctionCall = types.SimpleNamespace(from_id=lambda call_id: types.SimpleNamespace(
        get=types.SimpleNamespace(aio=_async_noop)
    ))
    fake_modal.enter = lambda *_args, **_kwargs: (lambda value: value)
    fake_modal.method = lambda *_args, **_kwargs: (lambda value: value)
    fake_modal.asgi_app = lambda *_args, **_kwargs: (lambda value: value)

    module_name = "wan_video_worker_under_test"
    spec = importlib.util.spec_from_file_location(
        module_name,
        Path(__file__).with_name("video.py"),
    )
    module = importlib.util.module_from_spec(spec)
    previous = sys.modules.get("modal")
    sys.modules["modal"] = fake_modal
    try:
        spec.loader.exec_module(module)
    finally:
        if previous is None:
            sys.modules.pop("modal", None)
        else:
            sys.modules["modal"] = previous
    return module


worker = _load_worker_module()


class CallbackValidationTests(unittest.TestCase):
    def test_accepts_go_callback_submission_and_rejects_untrusted_shapes(self):
        job_id = "gen_" + "a" * 32
        token = "b" * 64
        callback_url = f"https://dashboard.example.test/api/video-callbacks/{job_id}"
        worker.validate_callback_submission(job_id, callback_url, token)

        for invalid in (
            ("../job", callback_url, token),
            (job_id, f"http://dashboard.example.test/api/video-callbacks/{job_id}", token),
            (job_id, f"https://user:pass@dashboard.example.test/api/video-callbacks/{job_id}", token),
            (job_id, callback_url + "?next=https://evil.test", token),
            (job_id, f"https://localhost/api/video-callbacks/{job_id}", token),
            (job_id, f"https://127.0.0.1/api/video-callbacks/{job_id}", token),
            (job_id, f"https://dashboard.example.test/api/video-callbacks/{'gen_' + 'c' * 32}", token),
            (job_id, callback_url, "too-short"),
        ):
            with self.subTest(invalid=invalid):
                with self.assertRaises(ValueError):
                    worker.validate_callback_submission(*invalid)

    def test_callback_fields_are_all_or_none(self):
        self.assertIsNone(worker.callback_submission_from_body({"prompt": "hello"}))
        with self.assertRaisesRegex(ValueError, "provided together"):
            worker.callback_submission_from_body({"job_id": "gen_" + "a" * 32})


class CallbackDeliveryTests(unittest.IsolatedAsyncioTestCase):
    async def test_retries_only_transient_failures_with_a_bounded_attempt_count(self):
        statuses = iter((503, 503, 204))
        calls = []
        delays = []

        async def post_once(url, token, payload):
            calls.append((url, token, payload))
            return next(statuses)

        async def fake_sleep(delay):
            delays.append(delay)

        event = {"id": "gen_" + "a" * 32, "status": "processing", "sequence": 1, "progress": 5}
        result = await worker.deliver_video_callback(
            "https://dashboard.example.test/api/video-callbacks/" + event["id"],
            "b" * 64,
            event,
            post_once=post_once,
            sleep=fake_sleep,
        )
        self.assertTrue(result)
        self.assertEqual(len(calls), 3)
        self.assertEqual(len(delays), 2)

    async def test_permanent_callback_rejection_stops_without_following_redirect(self):
        delays = []

        async def post_once(_url, _token, _payload):
            return 302

        async def fake_sleep(delay):
            delays.append(delay)

        result = await worker.deliver_video_callback(
            "https://dashboard.example.test/api/video-callbacks/gen_" + "a" * 32,
            "b" * 64,
            {"status": "processing"},
            post_once=post_once,
            sleep=fake_sleep,
        )
        self.assertFalse(result)
        self.assertEqual(delays, [])

    async def test_http_sender_disables_redirects_and_uses_bearer_header(self):
        import httpx

        seen = {}

        class FakeResponse:
            status_code = 204

        class FakeClient:
            def __init__(self, **kwargs):
                seen["client_options"] = kwargs

            async def __aenter__(self):
                return self

            async def __aexit__(self, *_args):
                return None

            async def post(self, url, **kwargs):
                seen["url"] = url
                seen["request"] = kwargs
                return FakeResponse()

        with mock.patch.object(httpx, "AsyncClient", FakeClient):
            status = await worker.post_video_callback_once(
                "https://dashboard.example.test/api/video-callbacks/gen_" + "a" * 32,
                "b" * 64,
                {"status": "processing"},
            )

        self.assertEqual(status, 204)
        self.assertFalse(seen["client_options"]["follow_redirects"])
        self.assertEqual(seen["request"]["headers"]["Authorization"], "Bearer " + "b" * 64)


class SupervisorTests(unittest.IsolatedAsyncioTestCase):
    async def test_success_sends_processing_then_terminal_after_persisted_result(self):
        job_id = "gen_" + "a" * 32
        jobs = {job_id: {"id": job_id, "status": "pending", "progress": 0}}
        sent = []
        write_seen = []

        async def run_generation():
            jobs[job_id] = {"id": job_id, "status": "completed", "progress": 100, "usage_cost_usd": 0.12}

        async def read_job(_job_id):
            return copy.deepcopy(jobs.get(_job_id))

        async def write_job(_job_id, value):
            jobs[_job_id] = copy.deepcopy(value)
            write_seen.append(copy.deepcopy(value))

        async def post_once(_url, _token, payload):
            sent.append(copy.deepcopy(payload))
            return 204

        result = await worker.run_callback_supervisor(
            job_id,
            "https://dashboard.example.test/api/video-callbacks/" + job_id,
            "b" * 64,
            run_generation,
            read_job,
            write_job,
            post_once=post_once,
            sleep=_async_noop,
        )
        self.assertEqual([item["sequence"] for item in sent], [1, 2])
        self.assertEqual([item["status"] for item in sent], ["processing", "completed"])
        self.assertEqual(result["cost_usd"], "0.12")
        self.assertEqual(write_seen, [])

    async def test_gpu_startup_failure_is_persisted_before_terminal_callback(self):
        job_id = "gen_" + "a" * 32
        jobs = {job_id: {"id": job_id, "status": "pending", "stage": "queued", "progress": 0}}
        sent = []

        async def run_generation():
            raise RuntimeError("cold-start details stay out of the callback")

        async def read_job(_job_id):
            return copy.deepcopy(jobs.get(_job_id))

        async def write_job(_job_id, value):
            jobs[_job_id] = copy.deepcopy(value)

        async def post_once(_url, _token, payload):
            sent.append((copy.deepcopy(payload), copy.deepcopy(jobs.get(job_id))))
            return 204

        result = await worker.run_callback_supervisor(
            job_id,
            "https://dashboard.example.test/api/video-callbacks/" + job_id,
            "b" * 64,
            run_generation,
            read_job,
            write_job,
            post_once=post_once,
            sleep=_async_noop,
        )
        self.assertEqual(result["status"], "failed")
        self.assertEqual(result["error_code"], "worker_startup_failed")
        self.assertEqual(sent[-1][0]["sequence"], 2)
        self.assertEqual(sent[-1][1]["status"], "failed")
        self.assertNotIn("cold-start details", str(sent[-1][0]))

    async def test_exhausted_terminal_delivery_retries_persisted_result_without_gpu(self):
        job_id = "gen_" + "a" * 32
        jobs = {job_id: {"id": job_id, "status": "pending", "progress": 0}}
        generation_calls = []
        sent = []

        async def run_generation():
            generation_calls.append(job_id)
            jobs[job_id] = {"id": job_id, "status": "completed", "progress": 100}

        async def read_job(_job_id):
            return copy.deepcopy(jobs.get(_job_id))

        async def write_job(_job_id, value):
            jobs[_job_id] = copy.deepcopy(value)

        async def transient_terminal_failure(_url, _token, payload):
            sent.append(copy.deepcopy(payload))
            return 204 if payload["sequence"] == 1 else 503

        async def success_on_reentry(_url, _token, payload):
            sent.append(copy.deepcopy(payload))
            return 204

        args = (
            job_id,
            f"https://dashboard.example.test/api/video-callbacks/{job_id}",
            "b" * 64,
        )
        with self.assertRaises(worker.CallbackDeliveryError):
            await worker.run_callback_supervisor(
                *args,
                run_generation,
                read_job,
                write_job,
                post_once=transient_terminal_failure,
                sleep=_async_noop,
            )

        self.assertEqual(jobs[job_id]["status"], "completed")
        self.assertEqual(generation_calls, [job_id])
        self.assertEqual(sum(event["sequence"] == 2 for event in sent), 4)

        async def must_not_regenerate():
            self.fail("a Modal supervisor retry must not dispatch GPU work after a persisted result")

        event = await worker.run_callback_supervisor(
            *args,
            must_not_regenerate,
            read_job,
            write_job,
            post_once=success_on_reentry,
            sleep=_async_noop,
        )
        self.assertEqual(event["status"], "completed")
        self.assertEqual(generation_calls, [job_id])
        self.assertEqual(sent[-1]["sequence"], 2)

    async def test_read_error_after_completed_gpu_preserves_terminal_state(self):
        job_id = "gen_" + "a" * 32
        jobs = {job_id: {"id": job_id, "status": "pending", "progress": 0}}
        generation_calls = []
        writes = []
        reads = 0

        async def run_generation():
            generation_calls.append(job_id)
            jobs[job_id] = {"id": job_id, "status": "completed", "progress": 100}

        async def read_job(_job_id):
            nonlocal reads
            reads += 1
            if reads == 2:
                raise OSError("temporary Volume reload failure")
            return copy.deepcopy(jobs.get(_job_id))

        async def write_job(_job_id, value):
            writes.append(copy.deepcopy(value))

        async def post_once(_url, _token, _payload):
            return 204

        with self.assertRaises(worker.SupervisorStateError):
            await worker.run_callback_supervisor(
                job_id,
                f"https://dashboard.example.test/api/video-callbacks/{job_id}",
                "b" * 64,
                run_generation,
                read_job,
                write_job,
                post_once=post_once,
                sleep=_async_noop,
            )

        self.assertEqual(jobs[job_id]["status"], "completed")
        self.assertEqual(generation_calls, [job_id])
        self.assertEqual(writes, [])

    async def test_supervisor_cancellation_does_not_terminalize_running_gpu_child(self):
        job_id = "gen_" + "a" * 32
        jobs = {job_id: {"id": job_id, "status": "pending", "progress": 0}}
        writes = []
        sent = []

        async def interrupted_generation():
            raise asyncio.CancelledError()

        async def read_job(_job_id):
            return copy.deepcopy(jobs.get(_job_id))

        async def write_job(_job_id, value):
            writes.append(copy.deepcopy(value))
            jobs[_job_id] = copy.deepcopy(value)

        async def post_once(_url, _token, payload):
            sent.append(copy.deepcopy(payload))
            return 204

        args = (
            job_id,
            f"https://dashboard.example.test/api/video-callbacks/{job_id}",
            "b" * 64,
        )
        with self.assertRaises(asyncio.CancelledError):
            await worker.run_callback_supervisor(
                *args,
                interrupted_generation,
                read_job,
                write_job,
                post_once=post_once,
                sleep=_async_noop,
            )

        self.assertEqual(jobs[job_id]["status"], "pending")
        self.assertEqual(writes, [])
        self.assertEqual([event["sequence"] for event in sent], [1])

    async def test_transient_modal_wait_failure_reattaches_same_gpu_call_without_terminalizing(self):
        job_id = "gen_" + "a" * 32
        callback_url = f"https://dashboard.example.test/api/video-callbacks/{job_id}"
        callback_token = "b" * 64
        request_fingerprint = worker.callback_request_fingerprint(
            "A quiet forest at dawn",
            worker.MODEL_ID,
            6,
            "480p",
            "9:16",
            callback_url,
            callback_token,
        )
        jobs = {job_id: {"id": job_id, "status": "pending", "progress": 0}}
        claims = _FakeDict()
        await claims.put.aio(
            f"gpu:{job_id}",
            {
                "fingerprint": request_fingerprint,
                "state": "dispatched",
                "claimed_at": 1,
                "modal_call_id": "fc-shared-gpu-call",
            },
        )
        call_ids = []
        writes = []
        events = []
        waits = 0

        async def read_job(_job_id):
            return copy.deepcopy(jobs.get(_job_id))

        async def write_job(_job_id, value):
            writes.append(copy.deepcopy(value))
            jobs[_job_id] = copy.deepcopy(value)

        async def deliver(_url, _token, payload, **_kwargs):
            events.append(copy.deepcopy(payload))
            return True

        async def get_call_result():
            nonlocal waits
            waits += 1
            if waits == 1:
                raise worker.modal.exception.ServiceError("temporary control-plane failure")
            jobs[job_id] = {"id": job_id, "status": "completed", "progress": 100}

        def from_id(call_id):
            call_ids.append(call_id)
            return types.SimpleNamespace(get=types.SimpleNamespace(aio=get_call_result))

        with (
            mock.patch.object(worker, "job_claims", claims),
            mock.patch.object(worker, "read_job_async", read_job),
            mock.patch.object(worker, "write_job_async", write_job),
            mock.patch.object(worker, "deliver_video_callback", deliver),
            mock.patch.object(worker.modal.FunctionCall, "from_id", side_effect=from_id),
            mock.patch.object(worker, "VideoGenerator", side_effect=AssertionError("GPU must not be spawned again")),
        ):
            kwargs = {
                "job_id": job_id,
                "prompt": "A quiet forest at dawn",
                "model": worker.MODEL_ID,
                "duration": 6,
                "resolution": "480p",
                "aspect_ratio": "9:16",
                "seed": 7,
                "callback_url": callback_url,
                "callback_token": callback_token,
                "request_fingerprint": request_fingerprint,
            }
            with self.assertRaises(worker.CallbackWaitDeferredError):
                await worker.callback_video_supervisor(**kwargs)

            self.assertEqual(jobs[job_id]["status"], "pending")
            self.assertEqual(writes, [])
            self.assertFalse(any(event.get("sequence") == 2 for event in events))

            result = await worker.callback_video_supervisor(**kwargs)

        self.assertEqual(result["status"], "completed")
        self.assertEqual(call_ids, ["fc-shared-gpu-call", "fc-shared-gpu-call"])
        self.assertEqual(jobs[job_id]["status"], "completed")
        self.assertEqual(events[-1]["sequence"], 2)

    async def test_gpu_call_reference_dict_failure_keeps_awaiting_accepted_call(self):
        job_id = "gen_" + "a" * 32
        callback_url = f"https://dashboard.example.test/api/video-callbacks/{job_id}"
        callback_token = "b" * 64
        request_fingerprint = worker.callback_request_fingerprint(
            "A quiet forest at dawn",
            worker.MODEL_ID,
            6,
            "480p",
            "9:16",
            callback_url,
            callback_token,
        )
        jobs = {job_id: {"id": job_id, "status": "pending", "progress": 0}}
        claims = _FakeDict()
        sidecar_writes = []
        events = []
        spawn_calls = []
        persisted_job_writes = []

        async def fail_dispatched_claim(key, value, skip_if_exists=False):
            if key == f"gpu:{job_id}" and value.get("state") == "dispatched":
                raise OSError("temporary Dict metadata write failure")
            return await claims.put.aio(key, value, skip_if_exists=skip_if_exists)

        async def read_job(_job_id):
            return copy.deepcopy(jobs.get(_job_id))

        async def write_job(_job_id, value):
            persisted_job_writes.append(copy.deepcopy(value))
            jobs[_job_id] = copy.deepcopy(value)

        async def write_dispatch(_job_id, state, call_id=""):
            sidecar_writes.append((state, call_id))

        async def call_result():
            jobs[job_id] = {"id": job_id, "status": "completed", "progress": 100}

        async def spawn(**_kwargs):
            spawn_calls.append(job_id)
            return types.SimpleNamespace(
                object_id="fc-accepted-gpu-call",
                get=types.SimpleNamespace(aio=call_result),
            )

        async def deliver(_url, _token, payload, **_kwargs):
            events.append(copy.deepcopy(payload))
            return True

        with (
            mock.patch.object(worker, "job_claims", claims),
            mock.patch.object(worker, "put_claim", new=fail_dispatched_claim),
            mock.patch.object(worker, "read_job_async", read_job),
            mock.patch.object(worker, "write_job_async", write_job),
            mock.patch.object(worker, "write_modal_dispatch_state_async", write_dispatch),
            mock.patch.object(worker, "deliver_video_callback", deliver),
            mock.patch.object(
                worker,
                "VideoGenerator",
                return_value=types.SimpleNamespace(
                    generate=types.SimpleNamespace(spawn=types.SimpleNamespace(aio=spawn))
                ),
            ),
        ):
            result = await worker.callback_video_supervisor(
                job_id=job_id,
                prompt="A quiet forest at dawn",
                model=worker.MODEL_ID,
                duration=6,
                resolution="480p",
                aspect_ratio="9:16",
                seed=7,
                callback_url=callback_url,
                callback_token=callback_token,
                request_fingerprint=request_fingerprint,
            )

        self.assertEqual(result["status"], "completed")
        self.assertEqual(spawn_calls, [job_id])
        self.assertEqual(sidecar_writes[-1], ("dispatched", "fc-accepted-gpu-call"))
        self.assertEqual(jobs[job_id]["status"], "completed")
        self.assertFalse(any(value.get("status") == "failed" for value in persisted_job_writes))
        self.assertEqual(events[-1]["status"], "completed")

    async def test_lost_gpu_enqueue_ack_keeps_claim_pending_and_later_child_result_wins(self):
        job_id = "gen_" + "a" * 32
        callback_url = f"https://dashboard.example.test/api/video-callbacks/{job_id}"
        callback_token = "b" * 64
        request_fingerprint = worker.callback_request_fingerprint(
            "A quiet forest at dawn",
            worker.MODEL_ID,
            6,
            "480p",
            "9:16",
            callback_url,
            callback_token,
        )
        jobs = {job_id: {"id": job_id, "status": "pending", "progress": 0}}
        claims = _FakeDict()
        spawn_calls = []
        writes = []
        events = []

        async def read_job(_job_id):
            return copy.deepcopy(jobs.get(_job_id))

        async def write_job(_job_id, value):
            writes.append(copy.deepcopy(value))
            jobs[_job_id] = copy.deepcopy(value)

        async def write_dispatch(_job_id, _state, _call_id=""):
            return None

        async def spawn(**_kwargs):
            spawn_calls.append(job_id)
            # Model Modal accepting the call, then losing its enqueue ACK.
            raise worker.modal.exception.ServiceError("lost FunctionPutInputs acknowledgement")

        async def deliver(_url, _token, payload, **_kwargs):
            events.append(copy.deepcopy(payload))
            return True

        with (
            mock.patch.object(worker, "job_claims", claims),
            mock.patch.object(worker, "read_job_async", read_job),
            mock.patch.object(worker, "write_job_async", write_job),
            mock.patch.object(worker, "write_modal_dispatch_state_async", write_dispatch),
            mock.patch.object(worker, "deliver_video_callback", deliver),
            mock.patch.object(
                worker,
                "VideoGenerator",
                return_value=types.SimpleNamespace(
                    generate=types.SimpleNamespace(spawn=types.SimpleNamespace(aio=spawn))
                ),
            ),
        ):
            kwargs = {
                "job_id": job_id,
                "prompt": "A quiet forest at dawn",
                "model": worker.MODEL_ID,
                "duration": 6,
                "resolution": "480p",
                "aspect_ratio": "9:16",
                "seed": 7,
                "callback_url": callback_url,
                "callback_token": callback_token,
                "request_fingerprint": request_fingerprint,
            }
            with self.assertRaises(worker.CallbackWaitDeferredError):
                await worker.callback_video_supervisor(**kwargs)

            claim = await claims.get.aio(f"gpu:{job_id}")
            self.assertEqual(claim["state"], "dispatching")
            self.assertEqual(jobs[job_id]["status"], "pending")
            self.assertEqual(writes, [])
            self.assertFalse(any(event.get("sequence") == 2 for event in events))

            # The accepted GPU child later commits its real terminal result.
            jobs[job_id].update({"status": "completed", "progress": 100})
            result = await worker.callback_video_supervisor(**kwargs)

        self.assertEqual(result["status"], "completed")
        self.assertEqual(spawn_calls, [job_id])
        self.assertEqual(jobs[job_id]["status"], "completed")
        self.assertFalse(any(value.get("status") == "failed" for value in writes))
        self.assertEqual(events[-1]["sequence"], 2)


class APIIdempotencyTests(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        self.jobs = {}
        self.submissions = []
        self.callback_events = []
        self.fail_supervisor_dispatch = False
        self.ambiguous_supervisor_dispatch = False
        self.fail_callback_delivery = False
        self.fake_claims = _FakeDict()
        self.original_read = worker.read_job_async
        self.original_write = worker.write_job_async
        self.original_deliver = worker.deliver_video_callback
        self.original_supervisor = worker.callback_video_supervisor
        self.original_claims = worker.job_claims

        async def read_job(job_id):
            return copy.deepcopy(self.jobs.get(job_id))

        async def write_job(job_id, value):
            self.jobs[job_id] = copy.deepcopy(value)

        async def deliver(url, token, payload, **_kwargs):
            self.callback_events.append(copy.deepcopy(payload))
            return not self.fail_callback_delivery

        async def spawn(**kwargs):
            self.submissions.append(kwargs)
            if self.fail_supervisor_dispatch:
                raise worker.modal.exception.InvalidError("synthetic definitive rejection")
            if self.ambiguous_supervisor_dispatch:
                raise worker.modal.exception.ServiceError("synthetic lost enqueue acknowledgement")
            return types.SimpleNamespace(object_id=f"fc-supervisor-{len(self.submissions)}")

        worker.read_job_async = read_job
        worker.write_job_async = write_job
        worker.deliver_video_callback = deliver
        worker.job_claims = self.fake_claims
        worker.callback_video_supervisor = types.SimpleNamespace(
            spawn=types.SimpleNamespace(aio=spawn)
        )
        self.api = worker.api()
        self.client_context = mock.patch.dict(os.environ, {"MODAL_VIDEO_API_KEY": "offline-test-key"})
        self.client_context.start()
        import httpx

        self.client = httpx.AsyncClient(
            transport=httpx.ASGITransport(app=self.api),
            base_url="http://testserver",
            headers={"Authorization": "Bearer offline-test-key"},
        )

    async def asyncTearDown(self):
        await self.client.aclose()
        self.client_context.stop()
        worker.read_job_async = self.original_read
        worker.write_job_async = self.original_write
        worker.deliver_video_callback = self.original_deliver
        worker.callback_video_supervisor = self.original_supervisor
        worker.job_claims = self.original_claims

    def body(self):
        job_id = "gen_" + "a" * 32
        return {
            "job_id": job_id,
            "callback_url": f"https://dashboard.example.test/api/video-callbacks/{job_id}",
            "callback_token": "b" * 64,
            "model": worker.MODEL_ID,
            "prompt": "A quiet forest at dawn",
            "duration": 6,
            "resolution": "480p",
            "aspect_ratio": "9:16",
        }

    async def test_callback_submit_persists_without_secrets_and_duplicate_does_not_resubmit(self):
        body = self.body()
        first = await self.client.post("/api/v1/videos", json=body)
        self.assertEqual(first.status_code, 202)
        self.assertEqual(first.json()["id"], body["job_id"])
        self.assertEqual(len(self.submissions), 1)
        self.assertEqual(self.submissions[0]["callback_token"], body["callback_token"])
        self.assertNotIn("callback_url", self.jobs[body["job_id"]])
        self.assertNotIn("callback_token", self.jobs[body["job_id"]])

        duplicate = await self.client.post("/api/v1/videos", json=body)
        self.assertEqual(duplicate.status_code, 202)
        self.assertEqual(duplicate.json()["id"], body["job_id"])
        self.assertEqual(len(self.submissions), 1)
        self.assertEqual(len(self.callback_events), 1)

    async def test_concurrent_same_id_submit_has_one_atomic_claim_and_supervisor(self):
        body = self.body()
        first, second = await asyncio.gather(
            self.client.post("/api/v1/videos", json=body),
            self.client.post("/api/v1/videos", json=body),
        )
        self.assertEqual(len(self.submissions), 1)
        self.assertIn(first.status_code, {202, 503})
        self.assertIn(second.status_code, {202, 503})
        claim = await self.fake_claims.get.aio("request:" + body["job_id"])
        self.assertEqual(claim["state"], "supervisor_queued")
        self.assertEqual(claim["supervisor_call_id"], "fc-supervisor-1")
        self.assertNotIn("callback_url", str(claim))
        self.assertNotIn("callback_token", str(claim))

    async def test_same_id_with_different_request_conflicts(self):
        body = self.body()
        self.assertEqual((await self.client.post("/api/v1/videos", json=body)).status_code, 202)
        body["prompt"] = "A different video"
        response = await self.client.post("/api/v1/videos", json=body)
        self.assertEqual(response.status_code, 409)
        self.assertEqual(len(self.submissions), 1)

    async def test_supervisor_reference_save_failure_does_not_fail_accepted_job(self):
        body = self.body()
        original_put_claim = worker.put_claim

        async def fail_only_supervisor_reference(key, value, skip_if_exists=False):
            if key.startswith("request:") and value.get("state") == "supervisor_queued":
                raise OSError("temporary Dict write failure")
            return await original_put_claim(key, value, skip_if_exists=skip_if_exists)

        with mock.patch.object(worker, "put_claim", new=fail_only_supervisor_reference):
            response = await self.client.post("/api/v1/videos", json=body)

        self.assertEqual(response.status_code, 202)
        self.assertEqual(self.jobs[body["job_id"]]["status"], "pending")
        self.assertEqual(len(self.submissions), 1)
        claim = await self.fake_claims.get.aio("request:" + body["job_id"])
        self.assertEqual(claim["state"], "recorded")
        self.assertEqual(claim["supervisor_call_id"], "")

        duplicate = await self.client.post("/api/v1/videos", json=body)
        self.assertEqual(duplicate.status_code, 503)
        self.assertEqual(len(self.submissions), 1)
        self.assertEqual(self.jobs[body["job_id"]]["status"], "pending")

    async def test_lost_supervisor_enqueue_ack_preserves_job_and_same_id_does_not_resubmit(self):
        body = self.body()
        self.ambiguous_supervisor_dispatch = True

        first = await self.client.post("/api/v1/videos", json=body)
        self.assertEqual(first.status_code, 503)
        self.assertEqual(self.jobs[body["job_id"]]["status"], "pending")
        self.assertNotIn("error_code", self.jobs[body["job_id"]])
        self.assertEqual(len(self.submissions), 1)
        self.assertEqual(self.callback_events, [])

        # Simulate an accepted child eventually publishing its real result.
        self.jobs[body["job_id"]].update({"status": "completed", "progress": 100})
        self.ambiguous_supervisor_dispatch = False
        retry = await self.client.post("/api/v1/videos", json=body)
        self.assertEqual(retry.status_code, 202)
        self.assertEqual(retry.json()["status"], "completed")
        self.assertEqual(self.jobs[body["job_id"]]["status"], "completed")
        self.assertEqual(len(self.submissions), 1)
        self.assertEqual(self.callback_events[-1]["status"], "completed")

    async def test_dispatch_failure_callback_can_be_retried_without_resubmitting_gpu(self):
        body = self.body()
        self.fail_supervisor_dispatch = True
        self.fail_callback_delivery = True
        first = await self.client.post("/api/v1/videos", json=body)
        self.assertEqual(first.status_code, 503)
        self.assertEqual(self.jobs[body["job_id"]]["error_code"], "dispatch_failed")

        self.fail_callback_delivery = False
        retry = await self.client.post("/api/v1/videos", json=body)
        self.assertEqual(retry.status_code, 202)
        self.assertEqual(retry.json()["id"], body["job_id"])
        self.assertEqual(len(self.submissions), 1)
        self.assertEqual(self.callback_events[-1]["sequence"], 2)
        self.assertEqual(self.callback_events[-1]["error_code"], "dispatch_failed")


if __name__ == "__main__":
    unittest.main()
