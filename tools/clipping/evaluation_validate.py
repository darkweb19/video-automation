#!/usr/bin/env python3
"""Validate illustrative clipping evaluation contracts and linked fixtures offline."""

from __future__ import annotations

import argparse
import copy
import hashlib
import json
import math
import re
import sys
from pathlib import Path
from typing import Any


ROOT = Path(__file__).resolve().parents[2]
CONTRACTS = ROOT / "docs/clipping/contracts"
FIXTURES = ROOT / "docs/clipping/fixtures"
SCHEMA_CACHE: dict[Path, Any] = {}


class InvalidFixture(ValueError):
    pass


def load_json(path: Path) -> Any:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise InvalidFixture(f"cannot read JSON {path.relative_to(ROOT)}: {exc}") from exc


def type_matches(value: Any, expected: str) -> bool:
    if expected == "object":
        return isinstance(value, dict)
    if expected == "array":
        return isinstance(value, list)
    if expected == "string":
        return isinstance(value, str)
    if expected == "integer":
        return isinstance(value, int) and not isinstance(value, bool)
    if expected == "number":
        return isinstance(value, (int, float)) and not isinstance(value, bool)
    if expected == "boolean":
        return isinstance(value, bool)
    if expected == "null":
        return value is None
    raise InvalidFixture(f"unsupported JSON Schema type {expected!r}")


def pointer_get(document: Any, fragment: str) -> Any:
    if not fragment or fragment == "/":
        return document
    if not fragment.startswith("/"):
        raise InvalidFixture(f"unsupported schema reference fragment #{fragment}")
    value = document
    for raw_part in fragment[1:].split("/"):
        part = raw_part.replace("~1", "/").replace("~0", "~")
        try:
            value = value[part] if isinstance(value, dict) else value[int(part)]
        except (KeyError, IndexError, ValueError, TypeError) as exc:
            raise InvalidFixture(f"unresolved local schema reference #{fragment}") from exc
    return value


def schema_at(path: Path) -> Any:
    resolved = path.resolve()
    if resolved not in SCHEMA_CACHE:
        SCHEMA_CACHE[resolved] = load_json(resolved)
    return SCHEMA_CACHE[resolved]


def validate_schema(value: Any, schema: dict[str, Any], schema_path: Path, instance_path: str = "$", depth: int = 0) -> None:
    if depth > 64:
        raise InvalidFixture("schema reference depth exceeded")
    if "$ref" in schema:
        reference = schema["$ref"]
        file_part, _, fragment = reference.partition("#")
        target_file = (schema_path.parent / file_part).resolve() if file_part else schema_path.resolve()
        target = pointer_get(schema_at(target_file), fragment) if fragment else schema_at(target_file)
        validate_schema(value, target, target_file, instance_path, depth + 1)

    expected = schema.get("type")
    if expected is not None:
        alternatives = expected if isinstance(expected, list) else [expected]
        if not any(type_matches(value, candidate) for candidate in alternatives):
            raise InvalidFixture(f"{instance_path}: expected type {expected}, got {type(value).__name__}")
    if "const" in schema and (value != schema["const"] or
            isinstance(schema["const"], bool) and type(value) is not bool):
        raise InvalidFixture(f"{instance_path}: expected constant {schema['const']!r}")
    if "enum" in schema and value not in schema["enum"]:
        raise InvalidFixture(f"{instance_path}: value {value!r} is outside the allowed enum")

    if isinstance(value, dict):
        required = schema.get("required", [])
        missing = [key for key in required if key not in value]
        if missing:
            raise InvalidFixture(f"{instance_path}: missing required field(s) {', '.join(missing)}")
        properties = schema.get("properties", {})
        if schema.get("additionalProperties") is False:
            extras = sorted(set(value) - set(properties))
            if extras:
                raise InvalidFixture(f"{instance_path}: unexpected field(s) {', '.join(extras)}")
        if len(value) < schema.get("minProperties", 0):
            raise InvalidFixture(f"{instance_path}: fewer than {schema['minProperties']} properties")
        for key, child in properties.items():
            if key in value:
                validate_schema(value[key], child, schema_path, f"{instance_path}.{key}", depth + 1)

    if isinstance(value, list):
        if len(value) < schema.get("minItems", 0):
            raise InvalidFixture(f"{instance_path}: fewer than {schema['minItems']} items")
        if "maxItems" in schema and len(value) > schema["maxItems"]:
            raise InvalidFixture(f"{instance_path}: more than {schema['maxItems']} items")
        if schema.get("uniqueItems"):
            keys = [json.dumps(item, sort_keys=True, ensure_ascii=False) for item in value]
            if len(keys) != len(set(keys)):
                raise InvalidFixture(f"{instance_path}: duplicate array items")
        item_schema = schema.get("items")
        if item_schema:
            for index, item in enumerate(value):
                validate_schema(item, item_schema, schema_path, f"{instance_path}[{index}]", depth + 1)

    if isinstance(value, str):
        if len(value) < schema.get("minLength", 0):
            raise InvalidFixture(f"{instance_path}: string is shorter than {schema['minLength']}")
        if "maxLength" in schema and len(value) > schema["maxLength"]:
            raise InvalidFixture(f"{instance_path}: string is longer than {schema['maxLength']}")
        pattern = schema.get("pattern")
        if pattern and re.search(pattern, value) is None:
            raise InvalidFixture(f"{instance_path}: string does not match required pattern")

    if isinstance(value, (int, float)) and not isinstance(value, bool):
        if not math.isfinite(value):
            raise InvalidFixture(f"{instance_path}: number must be finite")
        if "minimum" in schema and value < schema["minimum"]:
            raise InvalidFixture(f"{instance_path}: number is below {schema['minimum']}")
        if "maximum" in schema and value > schema["maximum"]:
            raise InvalidFixture(f"{instance_path}: number is above {schema['maximum']}")


def require(condition: bool, message: str) -> None:
    if not condition:
        raise InvalidFixture(message)


def valid_interval(start: Any, end: Any, duration: int, label: str) -> None:
    require(isinstance(start, int) and not isinstance(start, bool), f"{label}: start_ms must be an integer")
    require(isinstance(end, int) and not isinstance(end, bool), f"{label}: end_ms must be an integer")
    require(0 <= start < end <= duration, f"{label}: expected 0 <= start_ms < end_ms <= source duration")


def validate_bundle(bundle: dict[str, Any]) -> None:
    source = bundle["source"]
    transcript = bundle["transcript"]
    candidates = bundle["candidates"]
    manifest = bundle["manifest"]
    payload = bundle["payload"]
    fixture_name = bundle["fixture_name"]

    for document, schema_name in (
        (source, "source.v1.schema.json"),
        (transcript, "transcript.v1.schema.json"),
        (candidates, "candidates.v1.schema.json"),
    ):
        validate_schema(document, schema_at(CONTRACTS / schema_name), CONTRACTS / schema_name)

    digest = hashlib.sha256(payload).hexdigest()
    require(source["source_sha256"] == digest, f"{fixture_name}: source_sha256 does not match the synthetic sentinel bytes")
    require(source["source_bytes"] == len(payload), f"{fixture_name}: source_bytes does not match the synthetic sentinel bytes")
    require(source["source_id"].startswith("fixture-"), f"{fixture_name}: source_id must identify a synthetic fixture")
    route_by_kind = {
        "upload": "local_upload",
        "youtube_original_file": "youtube_original_file_upload",
        "google_drive_public": "drive_public_content_url",
        "dropbox_public": "dropbox_public_dl1",
    }
    require(source["acquisition_route"] == route_by_kind[source["source_kind"]], f"{fixture_name}: source kind and route are inconsistent")
    manifest_bytes = bundle["manifest_bytes"]
    manifest_hash = hashlib.sha256(manifest_bytes).hexdigest()
    require(manifest.get("fixture_only") is True, f"{fixture_name}: pipeline manifest must be explicitly synthetic")
    require(transcript["pipeline_manifest_sha256"] == manifest_hash, f"{fixture_name}: transcript run-manifest hash mismatch")
    require(candidates["pipeline_manifest_sha256"] == manifest_hash, f"{fixture_name}: candidate run-manifest hash mismatch")
    require(transcript["source"] == source and candidates["source"] == source, f"{fixture_name}: source ID/hash metadata differs across artifacts")
    require(candidates["transcript_artifact_id"] == transcript["artifact_id"], f"{fixture_name}: candidate transcript reference does not resolve")
    duration = source["duration_ms"]

    event_by_id: dict[str, dict[str, Any]] = {}
    for event in transcript["evidence_events"]:
        valid_interval(event["start_ms"], event["end_ms"], duration, f"{fixture_name} evidence {event['evidence_id']}")
        require(event["evidence_id"] not in event_by_id, f"{fixture_name}: duplicate evidence ID {event['evidence_id']}")
        event_by_id[event["evidence_id"]] = event

    segment_by_id: dict[str, dict[str, Any]] = {}
    for segment in transcript["segments"]:
        valid_interval(segment["start_ms"], segment["end_ms"], duration, f"{fixture_name} segment {segment['segment_id']}")
        require(segment["segment_id"] not in segment_by_id, f"{fixture_name}: duplicate segment ID {segment['segment_id']}")
        segment_by_id[segment["segment_id"]] = segment
        previous_end = 0
        for span in segment["language_spans"]:
            require(span["start_char"] < span["end_char"] <= len(segment["text"]), f"{fixture_name}: language span is outside segment text")
            require(span["start_char"] >= previous_end, f"{fixture_name}: language spans overlap or are unsorted")
            previous_end = span["end_char"]
        for evidence_ref in segment.get("evidence_refs", []):
            require(evidence_ref in event_by_id, f"{fixture_name}: segment has unresolved evidence reference {evidence_ref}")

    words_by_segment: dict[str, list[dict[str, Any]]] = {segment_id: [] for segment_id in segment_by_id}
    word_ids: set[str] = set()
    for word in transcript["words"]:
        require(word["word_id"] not in word_ids, f"{fixture_name}: duplicate word ID {word['word_id']}")
        word_ids.add(word["word_id"])
        require(word["segment_id"] in segment_by_id, f"{fixture_name}: word references missing segment {word['segment_id']}")
        words_by_segment[word["segment_id"]].append(word)
        if word["alignment_state"] == "aligned":
            valid_interval(word["start_ms"], word["end_ms"], duration, f"{fixture_name} word {word['word_id']}")
            segment = segment_by_id[word["segment_id"]]
            require(segment["start_ms"] <= word["start_ms"] < word["end_ms"] <= segment["end_ms"], f"{fixture_name}: word timing is outside its segment")
        else:
            require(word["start_ms"] is None and word["end_ms"] is None, f"{fixture_name}: unaligned word must have null times, never invented timing")
    for segment_id, words in words_by_segment.items():
        segment = segment_by_id[segment_id]
        previous_end = 0
        for word in words:
            start, end = word["start_char"], word["end_char"]
            require(previous_end <= start < end <= len(segment["text"]), f"{fixture_name}: word character spans overlap or are out of bounds")
            require(not segment["text"][previous_end:start].strip(), f"{fixture_name}: word records omit non-whitespace text")
            require(segment["text"][start:end] == word["text"], f"{fixture_name}: word text differs from its original segment slice")
            require(any(span["start_char"] <= start < end <= span["end_char"] and span["language"] == word["language"] for span in segment["language_spans"]), f"{fixture_name}: word language does not match its character span")
            previous_end = end
        require(not segment["text"][previous_end:].strip(), f"{fixture_name}: word records omit segment text")

    candidate_ids: set[str] = set()
    previous_rank = 0
    previous_sort_key: tuple[int, int, str] | None = None
    candidate_intervals: list[tuple[dict[str, Any], int, int]] = []
    for candidate in candidates["candidates"]:
        require(candidate["candidate_id"] not in candidate_ids, f"{fixture_name}: duplicate candidate ID {candidate['candidate_id']}")
        candidate_ids.add(candidate["candidate_id"])
        require(candidate["rank"] == previous_rank + 1, f"{fixture_name}: ranks must be contiguous from 1")
        previous_rank = candidate["rank"]
        valid_interval(candidate["start_ms"], candidate["end_ms"], duration, f"{fixture_name} candidate {candidate['candidate_id']}")
        clip_duration = candidate["end_ms"] - candidate["start_ms"]
        require(15000 <= clip_duration <= 180000, f"{fixture_name}: candidate duration must be 15–180 seconds")
        require(candidate["evidence_refs"], f"{fixture_name}: candidate must cite evidence")
        require(set(candidate["evidence_refs"]) <= set(event_by_id), f"{fixture_name}: candidate contains unresolved evidence references")
        require(any(event_by_id[ref]["start_ms"] < candidate["end_ms"] and candidate["start_ms"] < event_by_id[ref]["end_ms"] for ref in candidate["evidence_refs"]), f"{fixture_name}: candidate has no evidence within its interval")
        score = candidate["editorial_score"]
        components = score["components"]
        total = sum(component["value"] for component in components.values())
        require(total == score["total"], f"{fixture_name}: editorial total does not equal component sum")
        for component in components.values():
            require(set(component["evidence_refs"]) <= set(candidate["evidence_refs"]), f"{fixture_name}: score rationale cites evidence absent from candidate")
        sort_key = (-score["total"], candidate["start_ms"], candidate["candidate_id"])
        require(previous_sort_key is None or previous_sort_key <= sort_key, f"{fixture_name}: rank order must be score descending, then time and ID")
        previous_sort_key = sort_key
        candidate_intervals.append((candidate, candidate["start_ms"], candidate["end_ms"]))

    for index, (left, left_start, left_end) in enumerate(candidate_intervals):
        for right, right_start, right_end in candidate_intervals[index + 1:]:
            overlap = max(0, min(left_end, right_end) - max(left_start, right_start))
            shorter = min(left_end - left_start, right_end - right_start)
            require(overlap / shorter < 0.70, f"{fixture_name}: returned candidates {left['candidate_id']} and {right['candidate_id']} violate deduplication threshold")

    for suppressed in candidates["suppressed_candidates"]:
        require(suppressed["candidate_id"] not in candidate_ids, f"{fixture_name}: suppressed candidate is also returned")
        require(suppressed["kept_candidate_id"] in candidate_ids, f"{fixture_name}: suppression target does not resolve")


def load_bundle(directory: Path) -> dict[str, Any]:
    manifest_path = directory / "pipeline_manifest.synthetic.json"
    manifest_bytes = manifest_path.read_bytes()
    source_fixture = load_json(directory / "source-fixture.json")
    if source_fixture.get("simulated") is not True:
        raise InvalidFixture(f"{directory.name}: source fixture must be marked simulated outside metadata")
    return {
        "fixture_name": directory.name,
        "source": source_fixture.get("source"),
        "transcript": load_json(directory / "transcript.v1.json"),
        "candidates": load_json(directory / "candidates.v1.json"),
        "manifest": json.loads(manifest_bytes.decode("utf-8")),
        "manifest_bytes": manifest_bytes,
        "payload": (directory / "source_payload.synthetic.txt").read_bytes(),
    }


def self_test(bundle: dict[str, Any]) -> int:
    cases = [
        ("source hash link", lambda item: item["candidates"]["source"].__setitem__("source_sha256", "0" * 64)),
        ("source interval bound", lambda item: item["transcript"]["evidence_events"][0].__setitem__("end_ms", item["source"]["duration_ms"] + 1)),
        ("unaligned word timing", lambda item: item["transcript"]["words"][0].__setitem__("end_ms", None)),
        ("candidate minimum duration", lambda item: item["candidates"]["candidates"][0].__setitem__("end_ms", item["candidates"]["candidates"][0]["start_ms"] + 14999)),
        ("candidate evidence reference", lambda item: item["candidates"]["candidates"][0]["evidence_refs"].append("missing-event")),
        ("candidate editorial total", lambda item: item["candidates"]["candidates"][0]["editorial_score"].__setitem__("total", 5)),
        ("candidate rank ordering", lambda item: item["candidates"]["candidates"][0].__setitem__("rank", 2)),
        ("boolean constant type", lambda item: item["transcript"].__setitem__("generated_speech", 0)),
        ("word timing outside segment", lambda item: item["transcript"]["words"][0].__setitem__("start_ms", 0)),
        ("word original character span", lambda item: item["transcript"]["words"][0].__setitem__("start_char", 1)),
        ("word language location", lambda item: item["transcript"]["words"][0].__setitem__("language", "ne")),
        ("nonfinite numeric value", lambda item: item["candidates"]["suppressed_candidates"].append({"candidate_id": "suppressed-invalid", "kept_candidate_id": item["candidates"]["candidates"][0]["candidate_id"], "overlap_fraction_of_shorter": float("nan")})),
    ]
    for label, mutate in cases:
        mutated = copy.deepcopy(bundle)
        mutate(mutated)
        try:
            validate_bundle(mutated)
        except InvalidFixture:
            continue
        raise InvalidFixture(f"self-test failed to reject invalid case: {label}")
    return len(cases)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--offline", action="store_true", help="validate local schemas, fixtures, hashes, and rejection cases without network access")
    args = parser.parse_args()
    if not args.offline:
        parser.error("run with --offline; this validator never contacts external services")
    fixture_dirs = sorted(path for path in FIXTURES.iterdir() if path.is_dir())
    if not fixture_dirs:
        raise InvalidFixture("no fixture directories found")
    count = 0
    for directory in fixture_dirs:
        bundle = load_bundle(directory)
        validate_bundle(bundle)
        print(f"PASS {directory.name}: source, transcript, candidates, sentinel hash, and run-manifest hash")
        count += 1
    rejected = self_test(load_bundle(fixture_dirs[0]))
    print(f"PASS {count} synthetic fixture set(s); PASS {rejected} invalid mutations rejected; offline only")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except InvalidFixture as exc:
        print(f"FAIL {exc}", file=sys.stderr)
        raise SystemExit(1)
