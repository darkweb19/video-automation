"""Adversarial, network-free fixtures for the C1.1 acquisition contract."""

from __future__ import annotations

import hashlib
import unittest

from acquisition_contract import (
    MAX_DURATION_MS,
    MAX_SOURCE_BYTES,
    MAX_REDIRECTS,
    SOURCE_VERSION,
    classify_source,
    simulate_probe,
)

PUBLIC_IP = "8.8.8.8"  # A fixture value only; no DNS lookup is performed.
VIDEO_BYTES = b"\x00\x00\x00\x18ftypisom\x00\x00\x02\x00isommp42" + b"fixture-media"
GOOGLE_VIDEO_URL = "https://drive.google.com/file/d/abc123/view?usp=sharing"
DROPBOX_INPUT_URL = "https://www.dropbox.com/s/example/video.mp4?dl=0"
DROPBOX_DOWNLOAD_URL = "https://www.dropbox.com/s/example/video.mp4?dl=1"
DROPBOX_CDN_URL = "https://dl.dropboxusercontent.com/s/example/video.mp4?dl=1"


def video_probe(duration_ms: int = 60_000) -> dict:
    return {
        "duration_ms": duration_ms,
        "video_streams": [
            {
                "codec": "h264",
                "width": 1920,
                "height": 1080,
                "frame_rate_num": 30,
                "frame_rate_den": 1,
            }
        ],
        "audio_streams": [{"codec": "aac", "sample_rate_hz": 48000, "channels": 2}],
    }


def final_hop(
    *,
    url: str | None = DROPBOX_DOWNLOAD_URL,
    body: bytes = VIDEO_BYTES,
    content_type: str = "video/mp4",
    content_length: int | None = None,
    downloaded_bytes: int | None = None,
    duration_ms: int = 60_000,
    status: int = 200,
    ip: str = PUBLIC_IP,
) -> dict:
    headers = {"content-type": content_type}
    if content_length is not None:
        headers["content-length"] = str(content_length)
    elif downloaded_bytes is None or downloaded_bytes == len(body):
        headers["content-length"] = str(len(body))
    return {
        "url": url,
        "resolved_ips": [ip],
        "status": status,
        "headers": headers,
        "body": body,
        "downloaded_bytes": len(body) if downloaded_bytes is None else downloaded_bytes,
        "probe": video_probe(duration_ms),
    }


def dropbox_hops(final: dict | None = None, *, dns: list[str] | None = None) -> list[dict]:
    return [
        {
            "url": DROPBOX_DOWNLOAD_URL,
            "resolved_ips": [PUBLIC_IP] if dns is None else dns,
            "status": 302,
            "location": DROPBOX_CDN_URL,
        },
        final if final is not None else final_hop(url=DROPBOX_CDN_URL),
    ]


def simulate_dropbox(hops: list[dict], *, free_bytes: int = 32 * 1024**3, rights_attested: bool = True) -> dict:
    return simulate_probe(
        source_id="eval-podcast-en-01",
        source_kind="dropbox_public",
        route="dropbox_public_dl1",
        start_url=DROPBOX_DOWNLOAD_URL,
        rights_attested=rights_attested,
        hops=hops,
        disk_free_bytes=free_bytes,
    )


class AcquisitionContractTests(unittest.TestCase):
    def test_route_classification_requires_reuse_attestation(self) -> None:
        result = classify_source(
            source_id="eval-podcast-en-01",
            url=DROPBOX_INPUT_URL,
            rights_attested=False,
        )
        self.assertEqual((result["status"], result["reason"]), (
            "authorization_needed", "reuse_rights_not_attested"
        ))
        string_false = classify_source(
            source_id="eval-podcast-en-01",
            url=DROPBOX_INPUT_URL,
            rights_attested="false",
        )
        self.assertEqual(string_false["status"], "authorization_needed")

    def test_youtube_url_requires_original_file_even_when_attested(self) -> None:
        result = classify_source(
            source_id="eval-podcast-en-01",
            url="https://www.youtube.com/watch?v=abcdefghijk",
            rights_attested=True,
        )
        self.assertEqual(result["status"], "original_file_required")
        self.assertEqual(result["reason"], "youtube_has_no_approved_media_fetch_route")

    def test_unknown_host_and_unsafe_url_are_rejected(self) -> None:
        for url in (
            "http://www.dropbox.com/s/example/video.mp4?dl=1",
            "https://www.dropbox.com.attacker.invalid/s/example/video.mp4?dl=1",
            "https://user:pass@www.dropbox.com/s/example/video.mp4?dl=1",
            "https://127.0.0.1/video.mp4",
            "https://www.dropbox.com:8443/s/example/video.mp4?dl=1",
        ):
            with self.subTest(url=url):
                result = classify_source(
                    source_id="eval-podcast-en-01", url=url, rights_attested=True
                )
                self.assertEqual(result["status"], "unsupported")

    def test_drive_view_link_is_only_a_probe_candidate_not_a_fetch_success(self) -> None:
        result = classify_source(
            source_id="eval-podcast-en-01",
            url=GOOGLE_VIDEO_URL,
            rights_attested=True,
        )
        self.assertEqual(result["status"], "probe_required")
        self.assertEqual(result["source_kind"], "google_drive_public")
        self.assertEqual(result["route"], "drive_public_content_url")

    def test_dropbox_shared_link_uses_documented_dl1_route(self) -> None:
        result = classify_source(
            source_id="eval-podcast-en-01",
            url=DROPBOX_INPUT_URL,
            rights_attested=True,
        )
        self.assertEqual(result["status"], "probe_required")
        self.assertEqual(result["route"], "dropbox_public_dl1")
        self.assertEqual(result["query_normalization"], "dl=1")

    def test_simulated_public_dropbox_media_response_imports_and_hashes_bytes(self) -> None:
        result = simulate_dropbox(dropbox_hops())
        self.assertEqual(result["status"], "imported")
        self.assertTrue(result["simulated"])
        metadata = result["metadata"]
        self.assertEqual(metadata["version"], SOURCE_VERSION)
        self.assertEqual(metadata["source_sha256"], hashlib.sha256(VIDEO_BYTES).hexdigest())
        self.assertEqual(metadata["source_bytes"], len(VIDEO_BYTES))
        self.assertEqual(metadata["duration_ms"], 60_000)
        self.assertEqual(metadata["source_time_unit"], "ms")
        self.assertEqual(metadata["authorization_state"], "attested")

    def test_private_and_mixed_dns_answers_fail_closed(self) -> None:
        for addresses in (["127.0.0.1"], ["169.254.169.254"], [PUBLIC_IP, "10.0.0.1"], ["::1"]):
            with self.subTest(addresses=addresses):
                result = simulate_dropbox(dropbox_hops(dns=addresses))
                self.assertEqual((result["status"], result["reason"]), (
                    "unsupported", "unsafe_dns_answer"
                ))

    def test_redirected_host_with_private_dns_fails_closed(self) -> None:
        final = final_hop(url=DROPBOX_CDN_URL, ip="10.0.0.8")
        result = simulate_dropbox(dropbox_hops(final))
        self.assertEqual(result["reason"], "unsafe_dns_answer")

    def test_redirect_to_unapproved_provider_is_rejected(self) -> None:
        hops = [
            {
                "url": DROPBOX_DOWNLOAD_URL,
                "resolved_ips": [PUBLIC_IP],
                "status": 302,
                "location": "https://example.invalid/video.mp4",
            },
            final_hop(url="https://example.invalid/video.mp4"),
        ]
        result = simulate_dropbox(hops)
        self.assertEqual(result["reason"], "redirect_provider_mismatch")

    def test_too_many_redirects_are_rejected(self) -> None:
        hops = []
        current = DROPBOX_DOWNLOAD_URL
        for index in range(MAX_REDIRECTS + 1):
            next_url = f"https://www.dropbox.com/s/example/r{index}.mp4?dl=1"
            hops.append({
                "url": current,
                "resolved_ips": [PUBLIC_IP],
                "status": 302,
                "location": next_url,
            })
            current = next_url
        hops.append(final_hop(url=current))
        self.assertEqual(simulate_dropbox(hops)["reason"], "redirect_limit_exceeded")

    def test_incomplete_redirect_and_crossed_route_are_rejected(self) -> None:
        redirect_without_final = [{
            "url": DROPBOX_DOWNLOAD_URL,
            "resolved_ips": [PUBLIC_IP],
            "status": 302,
            "location": DROPBOX_CDN_URL,
        }]
        self.assertEqual(simulate_dropbox(redirect_without_final)["reason"], "redirect_chain_incomplete")
        crossed_route = simulate_probe(
            source_id="eval-podcast-en-01",
            source_kind="dropbox_public",
            route="drive_public_content_url",
            start_url=DROPBOX_DOWNLOAD_URL,
            rights_attested=True,
            hops=dropbox_hops(),
            disk_free_bytes=32 * 1024**3,
        )
        self.assertEqual(crossed_route["reason"], "route_source_kind_mismatch")

    def test_html_200_is_not_accepted_as_video(self) -> None:
        body = b"<!doctype html><html><body>Sign in</body></html>"
        hop = final_hop(body=body, content_type="text/html")
        result = simulate_dropbox([hop])
        self.assertEqual((result["status"], result["reason"]), (
            "unsupported", "non_media_response"
        ))

    def test_drive_viewer_html_requires_the_media_file(self) -> None:
        html = b"<!doctype html><html><body>Drive preview</body></html>"
        result = simulate_probe(
            source_id="eval-podcast-en-01",
            source_kind="google_drive_public",
            route="drive_public_content_url",
            start_url=GOOGLE_VIDEO_URL,
            rights_attested=True,
            hops=[final_hop(url=GOOGLE_VIDEO_URL, body=html, content_type="text/html")],
            disk_free_bytes=32 * 1024**3,
        )
        self.assertEqual((result["status"], result["reason"]), (
            "original_file_required", "drive_viewer_page_not_media"
        ))

    def test_size_duration_storage_and_truncation_limits(self) -> None:
        over_size = final_hop(content_length=MAX_SOURCE_BYTES + 1)
        self.assertEqual(simulate_dropbox([over_size])["reason"], "source_size_limit_exceeded")

        streamed_over_size = final_hop(downloaded_bytes=MAX_SOURCE_BYTES + 1)
        self.assertEqual(simulate_dropbox([streamed_over_size])["reason"], "source_size_limit_exceeded")

        too_long = final_hop(duration_ms=MAX_DURATION_MS + 1)
        self.assertEqual(simulate_dropbox([too_long])["reason"], "duration_limit_exceeded")

        small_disk = simulate_dropbox([final_hop()], free_bytes=1)
        self.assertEqual(small_disk["reason"], "storage_reservation_unavailable")

        truncated = final_hop(content_length=len(VIDEO_BYTES) + 1)
        self.assertEqual(simulate_dropbox([truncated])["reason"], "content_length_mismatch")

    def test_provider_auth_failure_maps_to_authorization_needed(self) -> None:
        denied = final_hop(status=403)
        result = simulate_dropbox([denied])
        self.assertEqual((result["status"], result["reason"]), (
            "authorization_needed", "provider_denied_download"
        ))

    def test_partial_http_response_is_rejected(self) -> None:
        partial = final_hop(status=206)
        partial["headers"]["content-range"] = "bytes 0-100/101"
        result = simulate_dropbox([partial])
        self.assertEqual((result["status"], result["reason"]), (
            "unsupported", "partial_response_not_accepted"
        ))
        partial["status"] = 200
        self.assertEqual(simulate_dropbox([partial])["reason"], "partial_response_not_accepted")

    def test_four_hour_duration_boundary(self) -> None:
        self.assertEqual(simulate_dropbox([final_hop(duration_ms=MAX_DURATION_MS)])["status"], "imported")
        self.assertEqual(simulate_dropbox([final_hop(duration_ms=MAX_DURATION_MS + 1)])["reason"], "duration_limit_exceeded")

    def test_malformed_probe_types_return_failure_not_exceptions(self) -> None:
        malformed = simulate_probe(
            source_id="eval-podcast-en-01",
            source_kind=[],
            route=[],
            start_url=None,
            rights_attested=True,
            hops=[final_hop(url=None)],
            disk_free_bytes=32 * 1024**3,
        )
        self.assertEqual(malformed["reason"], "source_kind_has_no_media_probe_route")
        malformed_rights = simulate_dropbox(dropbox_hops(), rights_attested="false")
        self.assertEqual(malformed_rights["status"], "authorization_needed")

    def test_successful_uploaded_youtube_original_is_distinct_from_youtube_fetch(self) -> None:
        final = final_hop(url=None)
        result = simulate_probe(
            source_id="eval-podcast-en-01",
            source_kind="youtube_original_file",
            route="youtube_original_file_upload",
            start_url=None,
            rights_attested=True,
            hops=[final],
            disk_free_bytes=32 * 1024**3,
        )
        self.assertEqual(result["status"], "imported")
        self.assertEqual(result["metadata"]["source_kind"], "youtube_original_file")
        self.assertEqual(result["metadata"]["acquisition_route"], "youtube_original_file_upload")

    def test_malformed_upload_probe_is_rejected_without_crashing(self) -> None:
        result = simulate_probe(
            source_id="eval-podcast-en-01",
            source_kind="upload",
            route="local_upload",
            start_url=None,
            rights_attested=True,
            hops=[None],
            disk_free_bytes=32 * 1024**3,
        )
        self.assertEqual(result["reason"], "invalid_upload_probe_transcript")


if __name__ == "__main__":
    unittest.main()
