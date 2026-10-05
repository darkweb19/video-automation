# Video generation prompt contract v3

**Version:** 3.0.0

**Prompt IDs:** `framevault.story.v2`, `framevault.random.v3`

**Author:** Terra

**Date:** 2026-10-05

**Change note:** Keep story v2; target shorter project ideas and correct unusable random project output once without relaxing validation.

**Production source:** `internal/app/video_prompt_contract.go`, `internal/app/random_project_prompt.go`

The project idea prompt now targets 160–220 Unicode characters, leaving room below the existing 280-character limit. Haiku receives at most two requests inside the existing 110-second operation deadline. The first attempt reserves time for the second. Invalid or incomplete output adds a short application-owned correction requesting a fresh concise idea with the same JSON schema and category constraints. Provider content is never copied into the corrective instructions, and oversized output is never truncated into an accepted idea.

Transient transport and server failures may retry once. Authentication, billing, invalid-request, and explicit refusal failures remain actionable terminal errors. Shared account and paid-model cooldowns still apply; caller cancellation prevents further attempts. The single-clip route and five-scene story contract retain their v2 behavior and model pins.

Mock HTTP tests cover overlong Unicode, empty and malformed JSON, extra fields, Markdown, normalized/native truncation, transient failures, caller deadlines, cancellation, refusal, quota resets, and two failed attempts. Fixtures are synthetic; these tests do not establish live provider output quality or account availability. This file is the immutable v3 record; future prompt changes require a new version.
