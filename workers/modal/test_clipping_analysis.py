"""Offline fixtures for the deterministic M3 analysis core."""

import os
import hashlib
import json
import shutil
import subprocess
import sys
import tempfile
import types
import unittest
from unittest.mock import patch
from pathlib import Path

from clipping_analysis import (
    AnalysisError,
    CANDIDATE_INSPECTION_ALGORITHM_VERSION,
    MAX_TRANSCRIPT_SEGMENTS,
    MAX_AUDIO_EVENTS,
    analysis_runtime_manifest,
    _MODEL_CACHE,
    _model_snapshot_fingerprint,
    _configured_model_revision,
    analyze_source,
    inspect_candidate_window,
    prewarm_faster_whisper_snapshot,
    scan_audio_events,
    scan_visual_events,
    transcribe_faster_whisper,
    WorkCancelled,
)


def injected_runtime_manifest():
    return {
        "model_repository": "fixture-model",
        "model_snapshot": "0123456789abcdef0123456789abcdef01234567",
        "model_weights_sha256": "a" * 64,
        "ctranslate2_version": "not-used-injected-transcriber",
        "language_mode": "auto",
        "ffmpeg_version": "fixture ffmpeg version",
        "ffmpeg_configuration_sha256": "b" * 64,
        "python_version": "3.11.9",
        "modal_version": "1.1.4",
        "runtime_manifest_sha256": "c" * 64,
    }


def no_op_candidate_inspector(_path, _start, _end, *, cancel_check):
    cancel_check()
    return {"sample_rate_hz": 2, "sampled_frames": 0, "visual_events": []}


class ClippingAnalysisTests(unittest.TestCase):
    def test_full_timeline_keeps_late_non_speech_and_visual_events(self):
        duration_ms = 37 * 60 * 1000
        late_start = duration_ms - 9_000

        def transcriber(_path, *, model_name, cancel_check):
            self.assertEqual(model_name, "large-v3")
            cancel_check()
            return {
                "language": "hi",
                "language_probability": 0.73,
                "segments": [
                    {"start_ms": 1_000, "end_ms": 2_500, "text": "नमस्ते, आज का विषय कहानी है।"},
                    {"start_ms": late_start - 60_000, "end_ms": late_start - 58_000, "text": "फिर अंत में ऐसा हुआ।"},
                ],
                "speaker_labels_available": False,
            }

        def audio_scanner(_path, _segments, duration, *, cancel_check):
            cancel_check()
            return {
                "covered_duration_ms": duration,
                "events": [
                    {
                        "kind": "repeated_non_speech_pulses",
                        "start_ms": late_start,
                        "end_ms": late_start + 2_000,
                        "detector": "repeated_energy_transients_without_transcript_overlap",
                        "pulse_count": 4,
                        "acoustic_pattern": "laughter_like_pulses",
                        "semantic_label": None,
                        "confidence_kind": "uncalibrated_signal_heuristic",
                    }
                ],
            }

        def visual_scanner(_path, duration, *, cancel_check):
            cancel_check()
            return {
                "covered_duration_ms": duration,
                "events": [{
                    "kind": "motion_change",
                    "start_ms": late_start + 1_000,
                    "end_ms": late_start + 2_000,
                    "detector": "mean_absolute_gray_frame_difference",
                    "mean_absolute_difference": 46.5,
                    "visual_evidence_strength": "weak",
                }],
            }

        result = analyze_source(
            "fixture-does-not-exist.media",
            source_duration_ms=duration_ms,
            source_sha256="a" * 64,
            requested_content_type="comedy",
            pipeline_revision="framevault_m3_worker_v1",
            transcriber=transcriber,
            audio_scanner=audio_scanner,
            visual_scanner=visual_scanner,
            candidate_inspector=no_op_candidate_inspector,
            runtime_manifest=injected_runtime_manifest(),
        )

        self.assertEqual(result["audio_scan"]["covered_duration_ms"], duration_ms)
        self.assertEqual(result["visual_scan"]["covered_duration_ms"], duration_ms)
        self.assertEqual(result["audio_events"][-1]["start_ms"], late_start)
        self.assertEqual(result["visual_events"][-1]["start_ms"], late_start + 1_000)
        self.assertEqual(result["audio_events"][-1]["semantic_label"], None)
        self.assertEqual(result["context_timeline"][-1]["end_ms"], duration_ms)
        self.assertEqual(result["requested_content_type"], "comedy")
        self.assertEqual(result["source_sha256"], "a" * 64)

    def test_original_script_and_unknown_speaker_metadata_are_preserved(self):
        result = analyze_source(
            "fixture-does-not-exist.media",
            source_duration_ms=30_000,
            source_sha256="b" * 64,
            pipeline_revision="framevault_m3_worker_v1",
            requested_content_type="general",
            transcriber=lambda *_args, **_kwargs: {
                "language": "hi",
                "language_probability": 0.8,
                "segments": [{"start_ms": 100, "end_ms": 900, "text": "कहानी शुरू होती है।"}],
                "speaker_labels_available": False,
            },
            audio_scanner=lambda *_args, **_kwargs: {"events": [], "covered_duration_ms": 30_000},
            visual_scanner=lambda *_args, **_kwargs: {"events": [], "covered_duration_ms": 30_000},
            candidate_inspector=no_op_candidate_inspector,
            runtime_manifest=injected_runtime_manifest(),
        )
        transcript = result["transcript"]
        self.assertEqual(transcript["text"], "कहानी शुरू होती है।")
        self.assertEqual(transcript["original_script"]["name"], "devanagari")
        self.assertFalse(transcript["speaker_labels_available"])
        self.assertIsNone(transcript["segments"][0]["speaker_id"])

    def test_20001st_normalized_transcript_segment_fails_before_scanning_or_callback(self):
        self.assertEqual(MAX_TRANSCRIPT_SEGMENTS, 20_000)
        segments = [
            {"start_ms": index, "end_ms": index + 1, "text": f"segment {index}"}
            for index in range(MAX_TRANSCRIPT_SEGMENTS + 1)
        ]
        with self.assertRaisesRegex(AnalysisError, "transcript has too many segments"):
            analyze_source(
                "fixture.media",
                source_duration_ms=30_000,
                source_sha256="8" * 64,
                pipeline_revision="framevault_m3_worker_v1",
                transcriber=lambda *_args, **_kwargs: {"segments": segments},
                audio_scanner=lambda *_args, **_kwargs: self.fail("audio scan must not run after transcript overflow"),
                visual_scanner=lambda *_args, **_kwargs: self.fail("visual scan must not run after transcript overflow"),
                candidate_inspector=lambda *_args, **_kwargs: self.fail("dense scan must not run after transcript overflow"),
                runtime_manifest=injected_runtime_manifest(),
            )

    def test_every_supported_content_profile_can_be_processed_without_claims(self):
        profiles = ("general", "podcast", "comedy", "gaming", "movie")
        for profile in profiles:
            with self.subTest(profile=profile):
                result = analyze_source(
                    "fixture-does-not-exist.media",
                    source_duration_ms=15_000,
                    source_sha256="c" * 64,
                    requested_content_type=profile,
                    pipeline_revision="framevault_m3_worker_v1",
                    transcriber=lambda *_args, **_kwargs: {"segments": [], "language": None},
                    audio_scanner=lambda *_args, **_kwargs: {"events": [], "covered_duration_ms": 15_000},
                    visual_scanner=lambda *_args, **_kwargs: {"events": [], "covered_duration_ms": 15_000},
                    candidate_inspector=no_op_candidate_inspector,
                    runtime_manifest=injected_runtime_manifest(),
                )
                self.assertEqual(result["requested_content_type"], profile)

    def test_cancel_check_runs_between_injected_pipeline_stages(self):
        calls = []

        def check():
            calls.append("check")

        analyze_source(
            "fixture-does-not-exist.media",
            source_duration_ms=20_000,
            source_sha256="d" * 64,
            pipeline_revision="framevault_m3_worker_v1",
            transcriber=lambda *_args, **_kwargs: {"segments": []},
            audio_scanner=lambda *_args, **_kwargs: {"events": [], "covered_duration_ms": 20_000},
            visual_scanner=lambda *_args, **_kwargs: {"events": [], "covered_duration_ms": 20_000},
            candidate_inspector=no_op_candidate_inspector,
            runtime_manifest=injected_runtime_manifest(),
            cancel_check=check,
        )
        self.assertGreaterEqual(len(calls), 3)

    def test_four_hour_core_keeps_final_timeline_events_and_candidate_anchor(self):
        duration_ms = 4 * 60 * 60 * 1000
        last_start = duration_ms - 12_000
        seen_windows = []

        result = analyze_source(
            "fixture-does-not-exist.media",
            source_duration_ms=duration_ms,
            source_sha256="e" * 64,
            requested_content_type="general",
            pipeline_revision="framevault_m3_worker_v1",
            transcriber=lambda *_args, **_kwargs: {
                "language": "en",
                "segments": [{"start_ms": last_start, "end_ms": last_start + 5_000, "text": "Why did this surprising ending happen?"}],
            },
            audio_scanner=lambda *_args, **_kwargs: {
                "covered_duration_ms": duration_ms,
                "events": [{"kind": "repeated_non_speech_pulses", "start_ms": last_start + 5_000, "end_ms": last_start + 8_000, "pulse_count": 4}],
            },
            visual_scanner=lambda *_args, **_kwargs: {
                "covered_duration_ms": duration_ms,
                "events": [{"kind": "scene_change", "start_ms": duration_ms - 10_000, "end_ms": duration_ms - 9_999}],
            },
            candidate_inspector=lambda _path, start, end, *, cancel_check: (
                cancel_check(),
                seen_windows.append((start, end)),
                {"sample_rate_hz": 2, "sampled_frames": (end - start) // 500, "visual_events": []},
            )[2],
            runtime_manifest=injected_runtime_manifest(),
        )

        self.assertEqual(len(result["context_timeline"]), 240)
        self.assertEqual(result["context_timeline"][-1]["end_ms"], duration_ms)
        self.assertEqual(result["transcript"]["segments"][-1]["end_ms"], last_start + 5_000)
        self.assertEqual(result["audio_events"][-1]["start_ms"], last_start + 5_000)
        self.assertEqual(result["visual_events"][-1]["start_ms"], duration_ms - 10_000)
        self.assertTrue(result["candidate_inspections"])
        self.assertGreaterEqual(max(item["anchor_ms"] for item in result["candidate_inspections"]), last_start)
        self.assertTrue(seen_windows)
        self.assertEqual(max(item["sampled_frames"] for item in result["candidate_inspections"]), 120)

    def test_cached_inspection_pool_covers_profiles_with_measured_continuity_cues(self):
        duration_ms = 10 * 60 * 1000
        transcript_calls = []
        segments = [
            {"start_ms": 10_000, "end_ms": 18_000, "text": "Why did the comedian cross the road?"},
            {"start_ms": 20_000, "end_ms": 25_000, "text": "Turns out that was the joke, which was pretty funny."},
            {"start_ms": 130_000, "end_ms": 140_000, "text": "Today we're talking about the history of these public events."},
            {"start_ms": 150_000, "end_ms": 160_000, "text": "The takeaway is that the answer changed over time."},
            {"start_ms": 260_000, "end_ms": 268_000, "text": "We beat the final boss and move to the next level."},
            {"start_ms": 390_000, "end_ms": 395_000, "text": "The captain says we must leave before the storm arrives."},
            {"start_ms": 397_000, "end_ms": 402_000, "text": "The crew replies that the harbor is already closed."},
            {"start_ms": 510_000, "end_ms": 518_000, "text": "What is the answer to this story and why does it matter?"},
        ]

        def transcriber(_path, *, model_name, cancel_check):
            cancel_check()
            transcript_calls.append(model_name)
            return {"segments": segments, "language": "en"}

        def dense_inspector(_path, start, end, *, cancel_check):
            cancel_check()
            return {
                "sample_rate_hz": 2,
                "sampled_frames": (end - start) // 500,
                "visual_events": [{
                    "start_ms": min(end - 1, start + 1_000),
                    "end_ms": min(end, start + 1_500),
                    "kind": "dense_motion_change",
                    "score": 0.51,
                    "evidence": {"detector": "fixture_gray_frame_difference"},
                }],
            }

        core = analyze_source(
            "fixture.media",
            source_duration_ms=duration_ms,
            source_sha256="9" * 64,
            requested_content_type="comedy",
            pipeline_revision="framevault_m3_worker_v1",
            candidate_limit=1,
            transcriber=transcriber,
            audio_scanner=lambda *_args, **_kwargs: {
                "covered_duration_ms": duration_ms,
                "events": [{
                    "kind": "repeated_non_speech_pulses",
                    "start_ms": 28_000,
                    "end_ms": 30_000,
                    "pulse_count": 3,
                }],
            },
            visual_scanner=lambda *_args, **_kwargs: {
                "covered_duration_ms": duration_ms,
                "events": [
                    {"kind": "motion_change", "start_ms": 270_000, "end_ms": 271_000, "detector": "frame_difference", "mean_absolute_difference": 44.0},
                    {"kind": "scene_change", "start_ms": 403_000, "end_ms": 403_001, "detector": "scene_score", "mean_absolute_difference": 64.0},
                ],
            },
            candidate_inspector=dense_inspector,
            runtime_manifest=injected_runtime_manifest(),
        )

        inspections = core["candidate_inspections"]
        self.assertEqual(core["analysis_versions"]["candidate_inspection"], CANDIDATE_INSPECTION_ALGORITHM_VERSION)
        self.assertEqual(CANDIDATE_INSPECTION_ALGORITHM_VERSION, "ffmpeg-dense-candidate-window-v2-profile-pool")
        kinds = {item["anchor_kind"] for item in inspections}
        self.assertIn("comedy_setup_punchline_reaction", kinds)
        self.assertIn("podcast_topic_payoff", kinds)
        self.assertIn("gaming_motion_with_nearby_commentary", kinds)
        self.assertIn("movie_dialogue_scene_continuity", kinds)
        self.assertTrue(any(item["anchor_kind"].startswith("transcript_") for item in inspections))
        self.assertLessEqual(len(inspections), 5)
        for item in inspections:
            self.assertGreaterEqual(item["start_ms"], 0)
            self.assertLessEqual(item["end_ms"], duration_ms)
            self.assertTrue(any(event["kind"] == "dense_motion_change" for event in item["visual_events"]))

        # The first profile's core contains pool entries for later profile
        # selection, so changing the selector profile never calls ASR again.
        comedy_selection = [item for item in inspections if item["anchor_kind"].startswith("comedy_")]
        podcast_selection = [item for item in inspections if item["anchor_kind"].startswith("podcast_")]
        self.assertTrue(comedy_selection)
        self.assertTrue(podcast_selection)
        self.assertEqual(transcript_calls, ["large-v3"])

    def test_candidate_inspection_output_and_event_limits_are_strict(self):
        with self.assertRaises(AnalysisError):
            analyze_source(
                "fixture-does-not-exist.media",
                source_duration_ms=30_000,
                source_sha256="f" * 64,
                pipeline_revision="framevault_m3_worker_v1",
                transcriber=lambda *_args, **_kwargs: {
                    "segments": [{"start_ms": 1_000, "end_ms": 5_000, "text": "Why is this surprising?"}],
                },
                audio_scanner=lambda *_args, **_kwargs: {
                    "events": [{"kind": "audio_energy_transient", "start_ms": 10_000, "end_ms": 11_000}] * (MAX_AUDIO_EVENTS + 1),
                    "covered_duration_ms": 30_000,
                },
                visual_scanner=lambda *_args, **_kwargs: {"events": [], "covered_duration_ms": 30_000},
                candidate_inspector=no_op_candidate_inspector,
                runtime_manifest=injected_runtime_manifest(),
            )

        with self.assertRaises(AnalysisError):
            analyze_source(
                "fixture-does-not-exist.media",
                source_duration_ms=30_000,
                source_sha256="f" * 64,
                pipeline_revision="framevault_m3_worker_v1",
                transcriber=lambda *_args, **_kwargs: {
                    "segments": [{"start_ms": 1_000, "end_ms": 5_000, "text": "Why is this surprising?"}],
                },
                audio_scanner=lambda *_args, **_kwargs: {"events": [], "covered_duration_ms": 30_000},
                visual_scanner=lambda *_args, **_kwargs: {"events": [], "covered_duration_ms": 30_000},
                candidate_inspector=lambda *_args, **_kwargs: {
                    "sample_rate_hz": 2,
                    "sampled_frames": 361,
                    "visual_events": [],
                },
                runtime_manifest=injected_runtime_manifest(),
            )

    def test_model_snapshot_fingerprint_is_cached_and_content_bound(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            repo = root / "models--Systran--faster-whisper-large-v3"
            revision = "a" * 40
            snapshot = repo / "snapshots" / revision
            snapshot.mkdir(parents=True)
            (repo / "refs").mkdir()
            (repo / "refs" / "main").write_text("b" * 40, encoding="utf-8")
            (snapshot / "model.bin").write_bytes(b"fixture model weights")
            (snapshot / "config.json").write_text('{"fixture":true}', encoding="utf-8")
            previous = os.environ.get("FRAMEVAULT_WHISPER_CACHE")
            os.environ["FRAMEVAULT_WHISPER_CACHE"] = directory
            try:
                checks = []
                digest, actual_revision = _model_snapshot_fingerprint(revision=revision, cancel_check=lambda: checks.append(1))
                self.assertEqual(actual_revision, revision)
                self.assertEqual(len(digest), 64)
                calls_after_hash = len(checks)
                repeated = _model_snapshot_fingerprint(revision=revision, cancel_check=lambda: self.fail("cached manifest should not rehash"))
                self.assertEqual(repeated, (digest, revision))
                self.assertGreater(calls_after_hash, 0)
            finally:
                if previous is None:
                    os.environ.pop("FRAMEVAULT_WHISPER_CACHE", None)
                else:
                    os.environ["FRAMEVAULT_WHISPER_CACHE"] = previous

    def test_transcriber_loads_exact_configured_huggingface_commit(self):
        revision = "c" * 40
        with tempfile.TemporaryDirectory() as directory:
            snapshot = Path(directory) / "models--Systran--faster-whisper-large-v3" / "snapshots" / revision
            snapshot.mkdir(parents=True)
            (snapshot / "model.bin").write_bytes(b"fixture")
            previous_cache = os.environ.get("FRAMEVAULT_WHISPER_CACHE")
            previous_revision = os.environ.get("FRAMEVAULT_WHISPER_MODEL_REVISION")
            os.environ["FRAMEVAULT_WHISPER_CACHE"] = directory
            os.environ["FRAMEVAULT_WHISPER_MODEL_REVISION"] = revision
            calls = []
            pyav_options = []

            class FakeWhisperModel:
                def __init__(self, model_path, **kwargs):
                    calls.append(("model", model_path, kwargs))

                def transcribe(self, audio_path, **_kwargs):
                    sys.modules["av"].open(audio_path, options={"fixture": "kept"})
                    info = types.SimpleNamespace(language="en", language_probability=0.99)
                    return iter(()), info

            def snapshot_download(**kwargs):
                calls.append(("download", kwargs))
                return str(snapshot)

            model_module = types.ModuleType("faster_whisper")
            model_module.WhisperModel = FakeWhisperModel
            hub_module = types.ModuleType("huggingface_hub")
            hub_module.snapshot_download = snapshot_download
            av_module = types.ModuleType("av")

            def fake_av_open(*_args, **kwargs):
                pyav_options.append(kwargs["options"])
                return object()

            av_module.open = fake_av_open
            _MODEL_CACHE.clear()
            try:
                prewarm_calls = []
                prewarm_faster_whisper_snapshot(
                    revision=revision,
                    cache_dir=directory,
                    snapshot_downloader=lambda **kwargs: (prewarm_calls.append(kwargs), str(snapshot))[1],
                )
                with patch.dict(sys.modules, {"faster_whisper": model_module, "huggingface_hub": hub_module, "av": av_module}):
                    result = transcribe_faster_whisper("fixture.media")
                self.assertEqual(result["language"], "en")
                self.assertEqual(prewarm_calls[0]["local_files_only"], False)
                self.assertEqual(calls[0][0], "download")
                self.assertEqual(calls[0][1]["repo_id"], "Systran/faster-whisper-large-v3")
                self.assertEqual(calls[0][1]["revision"], revision)
                self.assertIs(calls[0][1]["local_files_only"], True)
                self.assertEqual(calls[1][0], "model")
                self.assertEqual(calls[1][1], str(snapshot))
                self.assertEqual(calls[1][2]["compute_type"], "float16")
                self.assertEqual(pyav_options, [{
                    "fixture": "kept",
                    "protocol_whitelist": "file,crypto,data",
                    "format_whitelist": "mov,matroska,webm",
                }])
            finally:
                _MODEL_CACHE.clear()
                if previous_cache is None:
                    os.environ.pop("FRAMEVAULT_WHISPER_CACHE", None)
                else:
                    os.environ["FRAMEVAULT_WHISPER_CACHE"] = previous_cache
                if previous_revision is None:
                    os.environ.pop("FRAMEVAULT_WHISPER_MODEL_REVISION", None)
                else:
                    os.environ["FRAMEVAULT_WHISPER_MODEL_REVISION"] = previous_revision

    def test_transcriber_fails_closed_when_cpu_prewarm_snapshot_is_missing(self):
        revision = "d" * 40
        with tempfile.TemporaryDirectory() as directory:
            previous_cache = os.environ.get("FRAMEVAULT_WHISPER_CACHE")
            previous_revision = os.environ.get("FRAMEVAULT_WHISPER_MODEL_REVISION")
            os.environ["FRAMEVAULT_WHISPER_CACHE"] = directory
            os.environ["FRAMEVAULT_WHISPER_MODEL_REVISION"] = revision
            model_calls = []
            hub_calls = []
            model_module = types.ModuleType("faster_whisper")
            model_module.WhisperModel = lambda *args, **kwargs: model_calls.append((args, kwargs))
            hub_module = types.ModuleType("huggingface_hub")
            hub_module.snapshot_download = lambda **kwargs: hub_calls.append(kwargs)
            _MODEL_CACHE.clear()
            try:
                with patch.dict(sys.modules, {"faster_whisper": model_module, "huggingface_hub": hub_module}):
                    with self.assertRaisesRegex(AnalysisError, "not been prewarmed"):
                        transcribe_faster_whisper("fixture.media")
                self.assertEqual(hub_calls, [])
                self.assertEqual(model_calls, [])
            finally:
                _MODEL_CACHE.clear()
                if previous_cache is None:
                    os.environ.pop("FRAMEVAULT_WHISPER_CACHE", None)
                else:
                    os.environ["FRAMEVAULT_WHISPER_CACHE"] = previous_cache
                if previous_revision is None:
                    os.environ.pop("FRAMEVAULT_WHISPER_MODEL_REVISION", None)
                else:
                    os.environ["FRAMEVAULT_WHISPER_MODEL_REVISION"] = previous_revision

    def test_transcriber_observes_cancellation_after_model_load_before_inference(self):
        revision = "e" * 40
        with tempfile.TemporaryDirectory() as directory:
            snapshot = Path(directory) / "models--Systran--faster-whisper-large-v3" / "snapshots" / revision
            snapshot.mkdir(parents=True)
            (snapshot / "model.bin").write_bytes(b"fixture")
            previous_cache = os.environ.get("FRAMEVAULT_WHISPER_CACHE")
            previous_revision = os.environ.get("FRAMEVAULT_WHISPER_MODEL_REVISION")
            os.environ["FRAMEVAULT_WHISPER_CACHE"] = directory
            os.environ["FRAMEVAULT_WHISPER_MODEL_REVISION"] = revision
            prewarm_faster_whisper_snapshot(
                revision=revision,
                cache_dir=directory,
                snapshot_downloader=lambda **_kwargs: str(snapshot),
            )
            model_loaded = []
            transcribe_calls = []

            class FakeWhisperModel:
                def __init__(self, *_args, **_kwargs):
                    model_loaded.append(True)

                def transcribe(self, *_args, **_kwargs):
                    transcribe_calls.append(True)
                    return iter(()), types.SimpleNamespace(language="en", language_probability=1.0)

            model_module = types.ModuleType("faster_whisper")
            model_module.WhisperModel = FakeWhisperModel
            hub_module = types.ModuleType("huggingface_hub")
            hub_module.snapshot_download = lambda **_kwargs: str(snapshot)

            def cancel_after_load():
                if model_loaded:
                    raise WorkCancelled("fixture capability revoked")

            _MODEL_CACHE.clear()
            try:
                with patch.dict(sys.modules, {"faster_whisper": model_module, "huggingface_hub": hub_module}):
                    with self.assertRaises(WorkCancelled):
                        transcribe_faster_whisper("fixture.media", cancel_check=cancel_after_load)
                self.assertEqual(model_loaded, [True])
                self.assertEqual(transcribe_calls, [])
            finally:
                _MODEL_CACHE.clear()
                if previous_cache is None:
                    os.environ.pop("FRAMEVAULT_WHISPER_CACHE", None)
                else:
                    os.environ["FRAMEVAULT_WHISPER_CACHE"] = previous_cache
                if previous_revision is None:
                    os.environ.pop("FRAMEVAULT_WHISPER_MODEL_REVISION", None)
                else:
                    os.environ["FRAMEVAULT_WHISPER_MODEL_REVISION"] = previous_revision

    def test_model_revision_requires_immutable_huggingface_commit(self):
        previous = os.environ.get("FRAMEVAULT_WHISPER_MODEL_REVISION")
        try:
            os.environ["FRAMEVAULT_WHISPER_MODEL_REVISION"] = "main"
            with self.assertRaises(AnalysisError):
                _configured_model_revision()
        finally:
            if previous is None:
                os.environ.pop("FRAMEVAULT_WHISPER_MODEL_REVISION", None)
            else:
                os.environ["FRAMEVAULT_WHISPER_MODEL_REVISION"] = previous

    @unittest.skipUnless(shutil.which("ffmpeg"), "FFmpeg is not installed")
    def test_runtime_manifest_records_actual_ffmpeg_and_python_build_without_model_download(self):
        manifest = analysis_runtime_manifest(include_model=False)
        self.assertTrue(manifest["ffmpeg_version"].startswith("ffmpeg version "))
        self.assertEqual(len(manifest["ffmpeg_configuration_sha256"]), 64)
        self.assertTrue(manifest["python_version"])
        self.assertIn("modal_version", manifest)
        self.assertEqual(manifest["language_mode"], "auto")
        self.assertEqual(len(manifest["runtime_manifest_sha256"]), 64)
        self.assertEqual(manifest["model_weights_sha256"], "not-used-model-weights")
        self.assertEqual(len(manifest["ffmpeg_executable_sha256"]), 64)
        self.assertEqual(len(manifest["python_distributions_sha256"]), 64)
        self.assertGreater(int(manifest["python_distributions_count"]), 0)
        self.assertEqual(len(manifest["os_release_sha256"]), 64)

    def test_runtime_manifest_hash_covers_sorted_packages_os_release_and_ffmpeg_bytes(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            ffmpeg = root / "ffmpeg-fixture"
            ffmpeg_bytes = b"#!/bin/sh\nprintf 'ffmpeg version fixture\\nconfiguration: --fixture\\n'\n"
            ffmpeg.write_bytes(ffmpeg_bytes)
            ffmpeg.chmod(0o700)
            os_release = root / "os-release"
            os_release_bytes = b'ID=fixture\nVERSION_ID="1"\n'
            os_release.write_bytes(os_release_bytes)
            packages = [
                types.SimpleNamespace(metadata={"Name": "z-last"}, version="2.0"),
                types.SimpleNamespace(metadata={"Name": "A_Package"}, version="1.0"),
            ]
            manifest = analysis_runtime_manifest(
                include_model=False,
                ffmpeg=str(ffmpeg),
                os_release_path=os_release,
                distribution_provider=lambda: packages,
            )
            inventory = [("a-package", "1.0"), ("z-last", "2.0")]
            expected_inventory_hash = hashlib.sha256(
                json.dumps(inventory, ensure_ascii=True, separators=(",", ":")).encode("ascii")
            ).hexdigest()
            self.assertEqual(manifest["python_distributions_sha256"], expected_inventory_hash)
            self.assertEqual(manifest["python_distributions_count"], "2")
            self.assertEqual(manifest["os_release_sha256"], hashlib.sha256(os_release_bytes).hexdigest())
            self.assertEqual(manifest["ffmpeg_executable_sha256"], hashlib.sha256(ffmpeg_bytes).hexdigest())
            self.assertEqual(manifest["ffmpeg_version"], "ffmpeg version fixture")

    @unittest.skipUnless(shutil.which("ffmpeg"), "FFmpeg is not installed")
    def test_real_ffmpeg_fixture_scans_silence_energy_motion_and_scene_changes(self):
        with tempfile.TemporaryDirectory() as directory:
            media = Path(directory) / "synthetic.mkv"
            # 250 ms tones align robustly with the 200 ms analysis windows and
            # leave measured pulse gaps below the grouping threshold across FFmpeg builds.
            pulse_audio = r"aevalsrc=if(between(t\,2\,2.25)+between(t\,2.65\,2.9)+between(t\,3.3\,3.55)\,0.95*sin(2*PI*500*t)\,0):s=8000:d=6"
            command = [
                shutil.which("ffmpeg"), "-y", "-hide_banner", "-loglevel", "error",
                "-f", "lavfi", "-i", "color=c=black:s=64x64:r=2:d=2",
                "-f", "lavfi", "-i", "color=c=white:s=64x64:r=2:d=2",
                "-f", "lavfi", "-i", "color=c=black:s=64x64:r=2:d=2",
                "-f", "lavfi", "-i", pulse_audio,
                "-filter_complex", "[0:v][1:v][2:v]concat=n=3:v=1:a=0[v]",
                "-map", "[v]", "-map", "3:a", "-t", "6", "-c:v", "ffv1", "-c:a", "pcm_s16le", str(media),
            ]
            subprocess.run(command, check=True, capture_output=True, timeout=30)
            audio = scan_audio_events(media, [], 6_000)
            visual = scan_visual_events(media, 6_000)
            dense = inspect_candidate_window(media, 1_000, 5_000)

        audio_kinds = {event["kind"] for event in audio["events"]}
        visual_kinds = {event["kind"] for event in visual["events"]}
        self.assertEqual(audio["covered_duration_ms"], 6_000)
        self.assertIn("silence", audio_kinds)
        self.assertIn("non_speech_audio_transient", audio_kinds)
        self.assertIn("repeated_non_speech_pulses", audio_kinds)
        self.assertTrue(any(event["start_ms"] >= 2_000 for event in audio["events"]))
        self.assertIn("scene_change", visual_kinds)
        self.assertIn("motion_change", visual_kinds)
        self.assertEqual(visual["covered_duration_ms"], 6_000)
        self.assertEqual(dense["sample_rate_hz"], 2)
        self.assertLessEqual(dense["sampled_frames"], 360)
        self.assertTrue(any(event["kind"] == "dense_motion_change" for event in dense["visual_events"]))

    @unittest.skipUnless(shutil.which("ffmpeg"), "FFmpeg is not installed")
    def test_ffmpeg_rejects_external_playlist_reference_before_http_request(self):
        with tempfile.TemporaryDirectory() as directory:
            playlist = Path(directory) / "external.ffconcat"
            playlist.write_text(
                "ffconcat version 1.0\nfile 'http://127.0.0.1:9/private-reference'\n",
                encoding="utf-8",
            )
            with patch("clipping_analysis.subprocess.Popen", wraps=subprocess.Popen) as popen_spy:
                with self.assertRaises(AnalysisError):
                    scan_visual_events(playlist, 2_000)
            command = popen_spy.call_args.args[0]
            self.assertIn(("-protocol_whitelist", "file,crypto,data"), list(zip(command, command[1:])))
            self.assertIn(("-format_whitelist", "mov,matroska,webm"), list(zip(command, command[1:])))

            # Re-run the captured production FFmpeg command and verify the
            # demuxer whitelist rejects the playlist before the referenced
            # HTTP URL is opened. No network listener or external request is used.
            rejected = subprocess.run(command, capture_output=True, text=True, timeout=10)
        self.assertNotEqual(rejected.returncode, 0)
        self.assertIn("Format not on whitelist", rejected.stderr)
        self.assertNotIn("private-reference", rejected.stderr)


if __name__ == "__main__":
    unittest.main()
