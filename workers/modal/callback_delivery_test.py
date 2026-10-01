"""Dependency-free regression checks for Modal terminal callback delivery.

This file parses the callback helpers rather than importing ``video.py`` so it
does not initialize Modal resources or require a GPU/Modal installation.
"""

import ast
import json
from email.message import Message
from pathlib import Path
from urllib import error as urllib_error
from urllib import request as urllib_request


SOURCE = Path(__file__).with_name("video.py")


def extracted(name, namespace):
    tree = ast.parse(SOURCE.read_text())
    for node in ast.walk(tree):
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef, ast.ClassDef)) and node.name == name:
            node.decorator_list = []
            module = ast.Module(body=[node], type_ignores=[])
            ast.fix_missing_locations(module)
            exec(compile(module, f"<extracted {name}>", "exec"), namespace)
            return namespace[name]
    raise AssertionError(f"{name} is missing from video.py")


class FakeTime:
    def __init__(self):
        self.sleeps = []

    def sleep(self, seconds):
        self.sleeps.append(seconds)


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
        "json": json,
        "urllib_request": urllib_request,
        "urllib_error": urllib_error,
        "time": fake_time,
        "CALLBACK_DELIVERY_MAX_ATTEMPTS": 5,
        "CALLBACK_DELIVERY_TIMEOUT_SECONDS": 10,
        "CALLBACK_DELIVERY_MAX_RETRY_AFTER_SECONDS": 15,
        "log_event": lambda level, event, **fields: logs.append((level, event, fields)),
    }
    return namespace, logs, fake_time


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
    test_redirects_are_not_followed_and_are_terminal()
    test_success_retry_and_auth_rejection_keep_credentials_out_of_the_payload()
    test_supervisor_uses_bounded_modal_retries_and_propagates_cancellation()
    print("callback delivery regression checks passed")
