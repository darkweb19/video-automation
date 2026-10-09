# AI clipping implementation plan

Planning baseline: 2026-10-08. Repository: https://github.com/darkweb19/video-automation.

On 2026-10-09 (America/Toronto), the user authorized M3/C3.1+C3.2 implementation, local verification and independent review, ten meaningful commits, and publication only to `feat/clipper-ai`. This does not authorize a live paid inference run or deployment. The implemented Modal worker is opt-in after operator configuration. Because M1's corpus and benchmark work was waived, implementation evidence must not be described as meeting measured quality, language, or cost targets; M3's evaluation-dependent exit criteria remain unmeasured.

M2 implementation was approved on 2026-10-08 with remaining M1 feasibility explicitly skipped. Commit/push of completed M2 to `feat/clipper-ai` was subsequently authorized; preserve that branch's prior M1 artifacts. This does not authorize paid provider calls, benchmarks, later milestones or deployment. See [TRACKER.md](TRACKER.md) for the current authorization and task states, [ADR-004](../architecture/004-ai-clipping.md) for the proposed architecture, and [AGENTS.md](../../AGENTS.md) for the execution workflow.

## Product requirements

- Integrate into the existing FrameVault application and repository.
- Personal channels first; public customer accounts are future scope.
- Inputs: YouTube URLs first, uploaded files, and public Google Drive/Dropbox links. Private connected accounts are excluded. The user confirms permission to reuse the YouTube footage; a technically accessible and permitted acquisition route still needs validation.
- Content: podcasts, comedy, gaming, and movies. Typical sources are 10–40 minutes; maximum duration is four hours.
- Target 7–10 distinct meaningful clips per source. Return fewer if the source does not contain enough strong candidates. Initial duration controls should support 15–180 seconds, including clips over 100 seconds; validate each destination's limits separately.
- Analyze the full audio and visual timeline. Use global context plus content-specific selection, not random or fixed-interval splitting. Visual/audio events can nominate candidates independently of speech.
- Explain each candidate's hook, emotional impact, retention potential, shareability, and standalone context with source-time evidence. The initial score is an editorial ranking, not a probability or guarantee of virality.
- Language evaluation priorities: Hindi/Hinglish, English, and Nepali. Other Indian languages remain to be specified and benchmarked. Do not advertise uniform support for every language.
- Captions use the original script by default; translation is opt-in. Preserve editable original transcripts and support mixed-language speech.
- Output: 9:16 video with original audio, intelligent framing, preview, trimming, caption correction, crop adjustment, MP4 and SRT/VTT export.
- Include alternative hooks/titles, batch processing, and YouTube publishing/scheduling. Hook suggestions must remain faithful to the source; generated speech and reordered dialogue are excluded initially.
- Later scope: direct TikTok/Instagram publishing, customer accounts/billing, dubbing, advanced timeline editing, and learning from permitted engagement analytics.

## Budget baseline and assumptions

The user selected US$1 for a one-hour source and said the budget can scale with duration. The proposed interpretation awaiting plan approval is `max(1 USD, source_hours × 1 USD)` for analysis plus the initial 7–10 exports. Examples: 10–60 minutes $1, two hours $2, four hours $4. Existing hosting and retained storage are separate; additional translations/renders require explicit cost disclosure.

This is a spending target, not measured performance or a provider quote. Reserve expected stage and retry costs, reconcile actual usage, and pause before starting work that is expected to exceed the remaining allowance. Provider settlement and metering precision must be reflected honestly. Product budget preferences do not authorize paid engineering benchmark calls.

Benchmark CPU/GPU time, provider tokens, media transfers, retries, cold starts, and all initial renders. Record source duration, resolution, language, content type, and clip count with each result. Cache analysis; a title edit must not rerun ASR. If the budget cannot deliver the agreed quality, report the evidence and propose a tradeoff instead of silently reducing analysis coverage or output quality.

For the initial opt-in M3 worker, the operator-declared rate and bounded compute ceiling produce a reservation estimate, not a Modal quote or settled charge. Keep the reservation outstanding until an operator records an invoice amount and reconciliation reference (including an explicit zero). Unknown/late outcomes block retry. The initial transcript/context/event extraction and candidate scores are deterministic engineering heuristics; score components and signal evidence explain ranking inputs but are not calibrated probabilities, semantic model judgments, or guarantees of engagement.

## Milestones and acceptance criteria

### M0 — Planning and execution workflow

Capture the user requirements, proposed architecture, milestone/task dependencies, authorization boundaries, model roles, file ownership rules, review requirements, and session resumption instructions. Preserve existing application invariants and historical operational follow-ups.

**Exit:** all planning documents and root instruction/handoff references are present; local links and whitespace checks pass; independent document review has no unresolved actionable findings; the tracker records exact changed paths and verification evidence. Completing M0 does not authorize M1 implementation or accept the proposed ADR.

### M1 — Acquisition, quality, and budget feasibility

1. Specify and validate YouTube and public Drive/Dropbox acquisition, failure modes, redirects, streaming, duration/size limits, and upload fallback. Do not promise universal YouTube availability.
2. Define versioned source-time transcript and candidate contracts and an evaluation set spanning all four content types, priority languages, mixed speech, noise, overlapping speakers, silence, and visual-only events. Include typical and four-hour duration cases.
3. Compare faster-whisper/WhisperX on Modal with a managed option using approved inputs and spending. Language-specific alignment and model/dependency licenses must be checked before adoption.
4. Measure complete source-to-export quality, latency, memory/storage demand, and cost. Evaluate usable candidates among the top suggestions, manual edit time, caption timing/accuracy, crop failures, and coverage against a transcript-only baseline.

**Exit:** a reproducible benchmark report, provider recommendation, supported-input/language matrix, explicit pass/fail targets, and evidence of budget feasibility or a documented decision needed. Define numerical quality targets before comparing results; do not invent measurements. Unavailable live access or paid-run authorization leaves the relevant task blocked, not passed. Update ADR-004 with the resulting decisions.

### M2 — Integrated sources, jobs, and batches

Add source assets, import/upload adapters, durable stage records, canonical analysis artifacts, batch membership, budget reservations, progress events, cancellation, and cleanup/retention controls. Keep media streamed to the existing persistent volume. Validate actual media, block unsafe fetch destinations including redirects, and treat media/transcripts as untrusted input.

The user later authorized C2.4 as an acquisition extension: import one publicly accessible YouTube video by canonical link using a pinned, maintained extractor, a DNS-pinned allowlisted HTTPS proxy for all extractor traffic, and the existing bounded Go stream/probe lifecycle. Require the existing rights attestation; preserve source audio with a single muxed audio/video format. Do not use cookies, credentials, login/private/age-gate/DRM bypass, playlists, live/upcoming streams, caller extractor options, or arbitrary hosts. See [YOUTUBE-IMPORT.md](YOUTUBE-IMPORT.md). This extension does not claim universal YouTube availability or complete the waived M1 feasibility work.

**Exit:** interrupted upload/import and stage execution recover predictably; duplicate callbacks/submissions cannot repeat completed paid work; batches enforce per-job and aggregate limits; canceled jobs cannot advance; authorization and storage bounds are tested. Four-hour inputs do not require whole-file buffering.

### M3 — Full-video understanding and candidate selection

Transcribe once with timestamps and optional speaker labels. Combine overlapping local analysis with a global topic/story map. Analyze scene changes, motion, and audio events throughout the source; inspect sequences around finalists. Select and deduplicate contiguous excerpts, refine boundaries deterministically, and persist score components, evidence, confidence, model/prompt versions, and title/hook variants.

Content rules: preserve comedy setup/punchline/reaction and pauses; podcast context and payoff; gameplay action with commentary/facecam; movie scene/dialogue continuity. An expensive finalist pass must not substitute for timeline-wide coverage.

**Exit:** the evaluation set meets M1 targets; late-video and non-speech moments receive consideration; timestamps stay within the source; boundaries preserve meaning; weak sources can return fewer than seven candidates; saved analysis is reused across edits. When M1 remains waived and its real-media corpus is unavailable, report implementation and deterministic regression evidence separately; do not mark this exit complete or claim target quality from synthetic fixtures alone.

### M4 — Captions, framing, preview, and export

Add speaker-aware smooth cropping, split-screen/padded fallbacks, gameplay/facecam layouts, and manual overrides. Add Indic/Latin font coverage, script shaping, reading-speed/line-break checks, platform safe areas, original-script captions, and opt-in translation. Store versioned edit instructions independently from analysis. Use a dedicated audio-preserving FFmpeg path with bounded concurrency and atomic output writes.

**Exit:** representative rendered clips pass human visual/audio review; captions and audio remain synchronized after trims; relevant subjects/action/text remain visible; manual crop and caption changes survive reloads; only affected renders are invalidated. Existing silent-generation output remains correct.

### M5 — YouTube publishing and scheduling

Extend the existing source abstraction and SQLite source-kind constraint for immutable clip exports. Preserve source authorization, Vault rules where applicable, resumable uploads, and uncertain-outcome handling. Persist selected clip/version/channel and schedules with UTC instants and the user's timezone; define daylight-saving, missed-run, cancellation, and retry behavior. Evaluate native YouTube scheduling versus durable local dispatch and record the decision before implementation.

**Exit:** jobs survive restarts, cannot publish a changed/unapproved export, and avoid duplicate uploads. Source deletion and in-flight publication interact safely. Simulated checks pass; live upload evidence is separate and requires applicable authorization. Scheduling in the product is distinct from Codex chat automations.

### M6 — Verification and rollout

Run cross-workflow regression checks, render/editor browser checks, restart/cancellation/duplicate-event tests, migration tests, and cost reconciliation. Prepare compatible worker-first deployment where protocol changes require it, a feature flag, operator documentation, backup/restore and rollback procedures, and a scoped live smoke plan.

**Exit:** acceptance evidence and independent review are complete; known limitations are documented. Track code readiness, deployment, and live verification separately. Do not downgrade or discard persistent user data during rollback. Actual rollout depends on the user's authorization, not the presence of this milestone.

## Session-sized delivery

Use the task IDs and dependencies in TRACKER.md. A session can complete part of a milestone; never stretch scope merely to finish an entire phase. The lead assigns bounded tasks, agents implement and review, and the lead integrates and records evidence. Continue to the next ready task only within the existing authorization scope.

At each checkpoint record: task state, actual model/effort if known, changed files, exact verification commands/results, reviewer findings, commit/PR if any, unresolved limitations, and the next concrete action. Repository files are the cross-session memory; chat summaries and agent memory are supplementary.

## Future learning

Record accepted/rejected candidates and user edits when practical in the core data model. Engagement ingestion and learned predictions are deferred. Before that phase, establish platform permissions, data-retention rules, publication-to-clip attribution, comparable channel/language/duration cohorts, and held-out evaluation. Keep editorial scores distinct from actual platform metrics.
