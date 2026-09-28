"""Offline ASGI contract tests for the SkyReels Modal worker."""

from __future__ import annotations

import importlib.util
import json
import os
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
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


class SkyReelsAPITests(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self) -> None:
        self.temp_dir = tempfile.TemporaryDirectory()
        self.jobs_dir = Path(self.temp_dir.name)
        self.dispatch = _Dispatch()
        self.original_generator = WORKER.VideoGenerator
        self.original_reconcile = WORKER.reconcile_modal_call

        async def no_remote_reconcile(job):
            return job

        self.patches = [
            patch.object(WORKER, "JOBS_DIR", str(self.jobs_dir)),
            patch.object(WORKER, "jobs_volume", _Volume()),
            patch.object(WORKER, "reconcile_modal_call", no_remote_reconcile),
            patch.dict(os.environ, {"MODAL_VIDEO_API_KEY": API_KEY}),
        ]
        for active_patch in self.patches:
            active_patch.start()
        _VideoGeneratorFactory.dispatch = self.dispatch
        self.generator_patch = patch.object(WORKER, "VideoGenerator", _VideoGeneratorFactory())
        self.generator_patch.start()

        app = WORKER.create_api()
        self.client = httpx.AsyncClient(
            transport=httpx.ASGITransport(app=app),
            base_url="http://worker.test",
        )

    async def asyncTearDown(self) -> None:
        await self.client.aclose()
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
        }
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

        initial = json.loads((self.jobs_dir / f"{job_id}.json").read_text(encoding="utf-8"))
        self.assertEqual(initial["status"], "pending")
        self.assertNotIn("modal_call_id", initial)
        self.assertEqual(await WORKER.read_modal_call_id_async(job_id), "fc-offline-test")

        queued = await self.client.get(f"{BASE_PATH}/{job_id}", headers=self.auth())
        self.assertEqual(queued.status_code, 200, queued.text)
        self.assertEqual(queued.json()["status"], "pending")
        self.assertEqual(queued.headers.get("retry-after"), str(WORKER.STATUS_POLL_RETRY_SECONDS))

        completed_job = {
            **initial,
            "status": "completed",
            "progress": 100,
            "gpu_seconds": 72.5,
            "gpu_name": "L40S",
        }
        await WORKER.write_job_async(job_id, completed_job)
        video = b"0123456789-video-content"
        (self.jobs_dir / f"{job_id}.mp4").write_bytes(video)

        completed = await self.client.get(f"{BASE_PATH}/{job_id}", headers=self.auth())
        self.assertEqual(completed.status_code, 200, completed.text)
        payload = completed.json()
        self.assertEqual(payload["status"], "completed")
        self.assertEqual(payload["progress"], 100)
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
        response = await self.client.post(
            BASE_PATH,
            headers=self.auth(),
            json=self.request_body(),
        )
        self.assertEqual(response.status_code, 500, response.text)
        self.assertNotIn(sentinel, response.text)
        self.assert_error_envelope(response)

        records = list(self.jobs_dir.glob("gen_*.json"))
        self.assertEqual(len(records), 1)
        failed = json.loads(records[0].read_text(encoding="utf-8"))
        self.assertEqual(failed["status"], "failed")
        self.assertNotIn(sentinel, failed.get("error", ""))

        status = await self.client.get(f"{BASE_PATH}/{failed['id']}", headers=self.auth())
        self.assertEqual(status.status_code, 200, status.text)
        self.assertEqual(status.json()["status"], "failed")
        self.assertNotIn(sentinel, status.text)

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
            {"id": terminal_id, "status": "pending", "progress": 0},
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

    async def test_successful_spawn_stays_accepted_when_call_reference_save_fails(self) -> None:
        with patch.object(WORKER, "jobs_volume", _FailSecondCommitVolume()):
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


if __name__ == "__main__":
    unittest.main()
