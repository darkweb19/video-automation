"""Offline contract tests for the Rust scene analyzer / Python worker boundary."""

import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
from unittest.mock import patch

from clipping_analysis import (
    AnalysisError,
    WorkCancelled,
    _rust_scene_analysis,
    scan_visual_events,
    inspect_candidate_window,
)


class RustSceneBoundaryTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.binary = os.environ.get(
            "FRAMEVAULT_SCENE_BINARY", "/usr/local/bin/framevault-scene-analyzer"
        )
        if not Path(cls.binary).is_file() or not shutil.which("ffmpeg"):
            raise unittest.SkipTest("Rust release binary and ffmpeg required")
        cls.tmp = tempfile.TemporaryDirectory()
        cls.media = Path(cls.tmp.name) / "sample.mp4"
        subprocess.run(
            [
                "ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-y",
                "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=24:duration=4",
                "-an", "-c:v", "mpeg4", "-pix_fmt", "yuv420p", str(cls.media),
            ],
            check=True, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE,
        )

    @classmethod
    def tearDownClass(cls):
        cls.tmp.cleanup()

    def test_full_source_scan_uses_existing_event_contract(self):
        with patch.dict(os.environ, {
            "FRAMEVAULT_SCENE_ENGINE": "rust",
            "FRAMEVAULT_SCENE_BINARY": self.binary,
            "FRAMEVAULT_SCENE_DECODE": "software",
        }):
            scan = scan_visual_events(self.media, 4000)
        self.assertEqual(scan["covered_duration_ms"], 4000)
        self.assertEqual(scan["algorithm_version"], "rust-scene-v1-gray-histogram-motion")
        self.assertEqual(scan["decoder"], "software")
        self.assertLessEqual(len(scan["events"]), 2)
        for event in scan["events"]:
            self.assertIn(event["kind"], {"scene_change", "motion_change"})
            self.assertGreaterEqual(event["start_ms"], 0)
            self.assertGreater(event["end_ms"], event["start_ms"])
            self.assertLessEqual(event["end_ms"], 4000)

    def test_dense_scan_preserves_go_candidate_contract(self):
        with patch.dict(os.environ, {
            "FRAMEVAULT_SCENE_ENGINE": "rust",
            "FRAMEVAULT_SCENE_BINARY": self.binary,
            "FRAMEVAULT_SCENE_DECODE": "software",
        }):
            scan = inspect_candidate_window(self.media, 500, 3500)
        self.assertEqual(scan["sample_rate_hz"], 2)
        self.assertLessEqual(scan["sampled_frames"], 6)
        self.assertLessEqual(len(scan["visual_events"]), 32)
        for event in scan["visual_events"]:
            self.assertEqual(event["kind"], "dense_motion_change")
            self.assertGreaterEqual(event["start_ms"], 500)
            self.assertLessEqual(event["end_ms"], 3500)
            self.assertGreaterEqual(event["score"], 0)
            self.assertLessEqual(event["score"], 1)

    def test_invalid_decoder_fails_before_starting_subprocess(self):
        with patch.dict(os.environ, {
            "FRAMEVAULT_SCENE_BINARY": self.binary,
            "FRAMEVAULT_SCENE_DECODE": "not-a-decoder",
        }):
            with self.assertRaisesRegex(AnalysisError, "configuration"):
                _rust_scene_analysis(
                    self.media, source_duration_ms=4000, start_ms=0,
                    end_ms=4000, mode="full", cancel_check=lambda: None,
                )

    def test_revoked_lease_is_respected_before_process_spawn(self):
        def revoked():
            raise WorkCancelled("lease revoked")
        with patch.dict(os.environ, {"FRAMEVAULT_SCENE_BINARY": self.binary}):
            with self.assertRaises(WorkCancelled):
                _rust_scene_analysis(
                    self.media, source_duration_ms=4000, start_ms=0,
                    end_ms=4000, mode="full", cancel_check=revoked,
                )

    def test_model_free_binary_rejects_invalid_windows(self):
        proc = subprocess.run(
            [self.binary, "--input", str(self.media),
             "--duration-ms", "4000", "--start-ms", "3500",
             "--end-ms", "500", "--mode", "dense", "--decode", "software"],
            capture_output=True, text=True,
        )
        self.assertNotEqual(proc.returncode, 0)
        self.assertEqual(proc.stdout, "")
        self.assertNotIn(str(self.media), proc.stderr)


if __name__ == "__main__":
    unittest.main()
