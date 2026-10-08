"""Dependency-free regression checks for Modal terminal callback delivery.

This file parses the callback helpers rather than importing ``video.py`` so it
does not initialize Modal resources or require a GPU/Modal installation.
"""

import ast
import asyncio
import json
import re
from email.message import Message
from pathlib import Path
from typing import Any
from urllib import error as urllib_error
from urllib import request as urllib_request


SOURCE = Path(__file__).with_name("video.py")
WORKER_SOURCES = (SOURCE, Path(__file__).with_name("skyreels.py"))


def extracted(name, namespace, source=SOURCE):
    tree = ast.parse(source.read_text())
    for node in ast.walk(tree):
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef, ast.ClassDef)) and node.name == name:
            node.decorator_list = []
            module = ast.Module(body=[node], type_ignores=[])
            ast.fix_missing_locations(module)
            exec(compile(module, f"<extracted {name}>", "exec"), namespace)
            return namespace[name]
    raise AssertionError(f"{name} is missing from video.py")


def test_both_workers_identify_callback_requests_and_preserve_redirect_safety():
    token = "c" * 43
    payload = {"id": "user-agent-job", "status": "completed"}
    for source in WORKER_SOURCES:
        requests = []
        namespace, _logs, _fake_time = callback_namespace()
        namespace["log_event"] = lambda *args, **fields: None
        namespace["CALLBACK_DELIVERY_MAX_ATTEMPTS"] = 1
        namespace["CALLBACK_DELIVERY_TIMEOUT_SECONDS"] = 10
        namespace["open_callback_request"] = lambda request, timeout: (
            requests.append((request, timeout)) or FakeResponse(204)
        )

        handler_type = extracted("CallbackNoRedirectHandler", namespace, source)
        request = urllib_request.Request("https://example.test/callback", data=b"{}")
        assert handler_type().redirect_request(
            request, None, 302, "Found", Message(), "https://redirect.test/capture"
        ) is None

        legacy = extracted("deliver_terminal_callback", namespace, source)
        legacy("https://example.test/legacy", token, payload)
        bearer = extracted("post_video_callback_once", namespace, source)
        assert bearer("https://example.test/durable", token, payload) == 204

        assert len(requests) == 2
        legacy_request, bearer_request = (entry[0] for entry in requests)
        assert legacy_request.get_header("User-agent") == "FrameVault-Modal-Callback/1.0"
        assert legacy_request.get_header("X-modal-callback-token") == token
        assert bearer_request.get_header("User-agent") == "FrameVault-Modal-Callback/1.0"
        assert bearer_request.get_header("Authorization") == f"Bearer {token}"
        assert legacy_request.full_url.endswith("/legacy")
        assert bearer_request.full_url.endswith("/durable")


class FakeTime:
    def __init__(self):
        self.sleeps = []

    def sleep(self, seconds):
        self.sleeps.append(seconds)

    def time(self):
        return 1_800_000_000


class FakeResponse:
    def __init__(self, status, headers=None):
        self.status = status
        self.headers = headers or Message()

    def getcode(self):
        return self.status

    def __enter__(self):
        return self

    def __exit__(self, *_):
        return False


def callback_namespace():
    logs = []
    fake_time = FakeTime()
    namespace = {
        "Any": Any,
        "json": json,
        "re": re,
        "asyncio": asyncio,
        "urllib_request": urllib_request,
        "urllib_error": urllib_error,
        "time": fake_time,
        "CALLBACK_RETRY_DELAYS_SECONDS": (0, 1, 2, 4, 8),
        "CALLBACK_DELIVERY_MAX_ATTEMPTS": 5,
        "CALLBACK_DELIVERY_TIMEOUT_SECONDS": 10,
        "CALLBACK_DELIVERY_MAX_RETRY_AFTER_SECONDS": 15,
        "log_event": lambda level, event, **fields: logs.append((level, event, fields)),
    }
    return namespace, logs, fake_time


def terminal_callback_namespace(namespace):
    namespace["CallbackDeliveryError"] = type("CallbackDeliveryError", (RuntimeError,), {})
    namespace["SupervisorStateError"] = type("SupervisorStateError", (RuntimeError,), {})
    namespace["CallbackWaitDeferredError"] = type("CallbackWaitDeferredError", (RuntimeError,), {})
    namespace["_callback_event"] = lambda job_id, status, sequence, progress, stage, **fields: {
        "id": job_id,
        "status": status,
        "sequence": sequence,
        "progress": progress,
        "stage": stage,
        **fields,
    }


def test_permanent_callback_rejection_is_durable_and_replay_skips_generation():
    namespace, logs, fake_time = callback_namespace()
    terminal_callback_namespace(namespace)
    extracted("terminal_callback_event", namespace)
    extracted("deliver_video_callback", namespace)
    extracted("persist_callback_delivery", namespace)
    extracted("callback_delivery_rejection_result", namespace)
    supervisor = extracted("run_callback_supervisor", namespace)
    job_id = "stable-job"
    stored = {"id": job_id, "status": "completed", "progress": 100}
    callbacks = []
    generation_calls = []

    async def read_job(_job_id):
        return dict(stored)

    async def write_job(_job_id, job):
        stored.clear()
        stored.update(job)

    async def run_generation():
        generation_calls.append(job_id)

    def reject(_url, _token, payload):
        callbacks.append(dict(payload))
        return 403

    for _ in range(2):
        result = asyncio.run(
            supervisor(
                job_id,
                "https://dashboard.example/api/video-callbacks/stable-job",
                "a" * 43,
                run_generation,
                read_job,
                write_job,
                post_once=reject,
                sleep=fake_time.sleep,
            )
        )
        assert result["delivery_rejected"] is True
        assert result["delivery_status"] == 403

    assert generation_calls == []
    assert len(callbacks) == 2
    assert stored["status"] == "completed"
    assert stored["callback_delivery"] == {
        "state": "rejected",
        "code": "http_403",
        "status_code": 403,
        "attempts": 1,
    }
    assert "a" * 43 not in repr(stored)
    assert "a" * 43 not in repr(logs)


def test_transient_callback_exhaustion_remains_retryable():
    namespace, _logs, fake_time = callback_namespace()
    terminal_callback_namespace(namespace)
    extracted("terminal_callback_event", namespace)
    extracted("deliver_video_callback", namespace)
    extracted("persist_callback_delivery", namespace)
    extracted("callback_delivery_rejection_result", namespace)
    supervisor = extracted("run_callback_supervisor", namespace)
    job_id = "stable-transient-job"
    stored = {"id": job_id, "status": "completed", "progress": 100}

    async def read_job(_job_id):
        return dict(stored)

    async def write_job(_job_id, job):
        stored.clear()
        stored.update(job)

    async def run_generation():
        raise AssertionError("a saved terminal result must not run inference")

    namespace["CallbackDeliveryError"] = type("CallbackDeliveryError", (RuntimeError,), {})
    try:
        asyncio.run(
            supervisor(
                job_id,
                "https://dashboard.example/api/video-callbacks/stable-transient-job",
                "b" * 43,
                run_generation,
                read_job,
                write_job,
                post_once=lambda *_args: 503,
                sleep=fake_time.sleep,
            )
        )
    except namespace["CallbackDeliveryError"]:
        pass
    else:
        raise AssertionError("transient delivery exhaustion should keep Modal retryable")

    assert stored["status"] == "completed"
    assert stored["callback_delivery"]["state"] == "retry_exhausted"
    assert fake_time.sleeps == [1, 2, 4, 8]


def test_initial_callback_rejection_stops_before_generation():
    namespace, logs, fake_time = callback_namespace()
    terminal_callback_namespace(namespace)
    namespace["WorkerDispatchInterrupted"] = type("WorkerDispatchInterrupted", (RuntimeError,), {})
    namespace["WorkerDispatchError"] = type("WorkerDispatchError", (RuntimeError,), {})
    extracted("classify_worker_failure", namespace)
    extracted("terminal_callback_event", namespace)
    extracted("deliver_video_callback", namespace)
    extracted("persist_callback_delivery", namespace)
    extracted("callback_delivery_rejection_result", namespace)
    supervisor = extracted("run_callback_supervisor", namespace)
    job_id = "stable-preflight-job"
    stored = {"id": job_id, "status": "pending", "progress": 0}
    generation_calls = []

    async def read_job(_job_id):
        return dict(stored)

    async def write_job(_job_id, job):
        stored.clear()
        stored.update(job)

    async def run_generation():
        generation_calls.append(job_id)

    result = asyncio.run(
        supervisor(
            job_id,
            "https://dashboard.example/api/video-callbacks/stable-preflight-job",
            "c" * 43,
            run_generation,
            read_job,
            write_job,
            post_once=lambda *_args: 403,
            sleep=fake_time.sleep,
        )
    )

    assert generation_calls == []
    assert result["status"] == "failed"
    assert result["error_code"] == "callback_delivery_rejected"
    assert result["delivery_rejected"] is True
    assert stored["status"] == "failed"
    assert stored["error_code"] == "callback_delivery_rejected"
    assert stored["callback_delivery"]["status_code"] == 403
    assert "c" * 43 not in repr(stored)
    assert "c" * 43 not in repr(logs)


def test_initial_transient_exhaustion_is_retryable_before_generation():
    namespace, _logs, fake_time = callback_namespace()
    terminal_callback_namespace(namespace)
    namespace["WorkerDispatchInterrupted"] = type("WorkerDispatchInterrupted", (RuntimeError,), {})
    namespace["WorkerDispatchError"] = type("WorkerDispatchError", (RuntimeError,), {})
    extracted("classify_worker_failure", namespace)
    extracted("terminal_callback_event", namespace)
    extracted("deliver_video_callback", namespace)
    extracted("persist_callback_delivery", namespace)
    extracted("callback_delivery_rejection_result", namespace)
    supervisor = extracted("run_callback_supervisor", namespace)
    job_id = "stable-preflight-transient-job"
    stored = {"id": job_id, "status": "pending", "progress": 0}
    generation_calls = []

    async def read_job(_job_id):
        return dict(stored)

    async def write_job(_job_id, job):
        stored.clear()
        stored.update(job)

    async def run_generation():
        generation_calls.append(job_id)

    try:
        asyncio.run(
            supervisor(
                job_id,
                "https://dashboard.example/api/video-callbacks/stable-preflight-transient-job",
                "d" * 43,
                run_generation,
                read_job,
                write_job,
                post_once=lambda *_args: 503,
                sleep=fake_time.sleep,
            )
        )
    except namespace["CallbackDeliveryError"]:
        pass
    else:
        raise AssertionError("transient initial callback exhaustion should remain retryable")

    assert generation_calls == []
    assert stored["status"] == "pending"
    assert stored["callback_delivery"]["state"] == "retry_exhausted"
    assert stored["callback_delivery"]["status_code"] == 503
    assert fake_time.sleeps == [1, 2, 4, 8]


def test_redirects_are_not_followed_and_are_terminal():
    namespace, logs, fake_time = callback_namespace()
    handler_type = extracted("CallbackNoRedirectHandler", namespace)
    original = urllib_request.Request(
        "https://dashboard.example/api/provider-callbacks/modal",
        data=b"{}",
        method="POST",
        headers={"X-Modal-Callback-Token": "a" * 43},
    )
    assert handler_type().redirect_request(
        original,
        None,
        302,
        "Found",
        Message(),
        "https://redirect-target.example/capture",
    ) is None

    extracted("open_callback_request", namespace)
    deliver = extracted("deliver_terminal_callback", namespace)
    calls = []

    def redirected(_request, timeout):
        calls.append(timeout)
        raise urllib_error.HTTPError(
            "https://dashboard.example/api/provider-callbacks/modal",
            302,
            "Found",
            Message(),
            None,
        )

    namespace["open_callback_request"] = redirected
    deliver("https://dashboard.example/api/provider-callbacks/modal", "a" * 43, {"id": "job", "status": "completed"})
    assert calls == [10]
    assert fake_time.sleeps == []
    assert logs == [("WARNING", "callback_rejected", {"job": "job", "status": 302})]


def test_success_retry_and_auth_rejection_keep_credentials_out_of_the_payload():
    namespace, logs, fake_time = callback_namespace()
    extracted("CallbackNoRedirectHandler", namespace)
    extracted("open_callback_request", namespace)
    deliver = extracted("deliver_terminal_callback", namespace)
    token = "b" * 43
    payload = {"id": "job", "status": "completed", "model": "modal/test", "cost_usd": "0.250000"}
    sent = []
    responses = [
        urllib_error.HTTPError("https://dashboard.example/callback", 503, "Unavailable", Message(), None),
        FakeResponse(204),
    ]

    def transient_then_success(request, timeout):
        sent.append((request, timeout))
        response = responses.pop(0)
        if isinstance(response, Exception):
            raise response
        return response

    namespace["open_callback_request"] = transient_then_success
    deliver("https://dashboard.example/callback", token, payload)
    assert len(sent) == 2
    assert fake_time.sleeps == [1]
    assert json.loads(sent[0][0].data) == payload
    assert token not in sent[0][0].data.decode()
    assert sent[0][0].get_header("X-modal-callback-token") == token
    assert logs[-1] == ("INFO", "callback_delivered", {"job": "job", "attempt": 2})

    logs.clear()
    fake_time.sleeps.clear()
    sent.clear()

    def rejected(request, timeout):
        sent.append((request, timeout))
        raise urllib_error.HTTPError("https://dashboard.example/callback", 401, "Unauthorized", Message(), None)

    namespace["open_callback_request"] = rejected
    deliver("https://dashboard.example/callback", token, payload)
    assert len(sent) == 1
    assert fake_time.sleeps == []
    assert logs == [("WARNING", "callback_rejected", {"job": "job", "status": 401})]


def test_supervisor_uses_bounded_modal_retries_and_propagates_cancellation():
    tree = ast.parse(SOURCE.read_text())
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
    assert retry_options["max_retries"] == 10
    assert retry_options["max_delay"] <= 60
    assert any(
        isinstance(handler.type, ast.Attribute) and handler.type.attr == "CancelledError"
        for node in ast.walk(supervisor)
        if isinstance(node, ast.Try)
        for handler in node.handlers
    )
    assert any(
        isinstance(node, ast.Attribute) and node.attr == "cancel"
        for node in ast.walk(supervisor)
    )


if __name__ == "__main__":
    test_both_workers_identify_callback_requests_and_preserve_redirect_safety()
    test_redirects_are_not_followed_and_are_terminal()
    test_success_retry_and_auth_rejection_keep_credentials_out_of_the_payload()
    test_permanent_callback_rejection_is_durable_and_replay_skips_generation()
    test_transient_callback_exhaustion_remains_retryable()
    test_initial_callback_rejection_stops_before_generation()
    test_initial_transient_exhaustion_is_retryable_before_generation()
    test_supervisor_uses_bounded_modal_retries_and_propagates_cancellation()
    print("callback delivery regression checks passed")
