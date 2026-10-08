# M1 feasibility checkpoint — 2026-10-08

M1 is authorized. This checkpoint covers C1.1 and C1.2 preparation; it is not an M1 pass or a provider-selection decision. Current paid benchmark authorization is **US$0**. The user authorized publishing this snapshot to `feat/clipper-ai`, based directly on `430751ee169fbae0d1b7c267c5ff1c077ab13a0a`; Git history records the resulting commit.

## Work and evidence

- [Acquisition contract and feasibility](ACQUISITION.md): input routes, observable failures, local contract checks, and outstanding live evidence.
- [Evaluation protocol and benchmark proposal](EVALUATION.md): sample slots, source-time formats, numeric gates, synthetic fixtures, and proposed spending controls.
- [Tracker](TRACKER.md): authoritative task states, agent assignments, checks, and next action.

Local verification: 20 acquisition tests pass; 2 simulated evaluation bundles pass and 12 invalid mutations are rejected. Three LUNA Max agents authored and preliminarily reviewed artifacts, then hit their usage limit before final review. C1.1/C1.2 remain in review rather than done. No real media inputs, provider inference, deployment, or live publication have been performed. Offline simulation demonstrates contract rules only. Synthetic media does not establish language, selection, crop, storage, latency, or production cost feasibility.

## Integration findings

The inspected application is Go with SQLite and embedded dashboard assets. Existing generation assembly in `internal/app/combiner.go` explicitly removes audio with `-an`; clipping needs the planned dedicated audio-preserving renderer. Existing download paths stream to temporary files and rename on completion. These are relevant foundations, not implemented clipping features.

The proposed ASR comparison is faster-whisper with an optional WhisperX alignment pass on Modal versus a managed transcription adapter. No runtime provider is adopted in this checkpoint. Package, weight, alignment, VAD, and diarization licenses must be reviewed at pinned revisions before adoption.

Primary-source review on 2026-10-08:

- [faster-whisper](https://github.com/SYSTRAN/faster-whisper) exposes word timestamps and an MIT code license; model weights and dependency terms remain separate.
- [WhisperX](https://github.com/m-bain/whisperX) has a BSD-2-Clause code license and requires language-specific alignment. Its [default alignment map](https://raw.githubusercontent.com/m-bain/whisperX/main/whisperx/alignment.py) includes English and Hindi but no Nepali entry at inspection. Nepali requires a tested alternative or an explicitly lower-precision timing path; it cannot inherit English/Hindi timing claims.
- A potential managed comparator, [OpenAI file transcription](https://developers.openai.com/api/docs/guides/speech-to-text), documents word/segment timestamps for `whisper-1` and a 25 MB upload limit. The adapter must use bounded chunks and restore source-time offsets. This is a proposed comparison option, not a model pin or purchase approval. Model availability, language behavior, and applicable price must be reverified before runs; no guessed price is used here.

## Benchmark approval proposal

Propose **eight complete 15-minute sources** (two each of podcasts, comedy, gaming, and movies), covering English, Hindi/Hinglish, Nepali, and mixed speech, plus **one genuine four-hour gaming source**. The eight typical sources run through both candidate pipelines; only the selected eligible pipeline runs the long case. Transcript-only selection reuses the same transcript. Detailed source IDs and feature requirements are in EVALUATION.md. The user must identify actual permitted media and acquisition permission; sample slots are not yet an acquired or approved corpus.

Propose a **US$20 aggregate engineering benchmark ceiling**, including provider calls, GPU/CPU compute, cold starts, transfer, storage attributable to the run, initial renders, and retries. It is a hard authorization ceiling, not a measured estimate or a guarantee every comparison fits. No stage starts without a bounded reservation and a verified rate; unresolved billing or insufficient headroom blocks dispatch. Reconcile charges and keep settlement/retry contingency. The product's proposed per-video target is separate: `max(US$1, source_hours × US$1)` for analysis plus initial 7–10 exports, with fewer clips if warranted.

## Remaining gates

1. YouTube-first automated acquisition remains unresolved: public availability and reuse permission do not establish download authorization. Actual source links/files, permission scope, and a permitted acquisition route are required; current worker egress does not include YouTube/Drive/Dropbox hosts and no provider credentials are configured.
2. Explicit approval of benchmark inputs and the proposed engineering cap, followed by provider/model/rate/license and bounded-dispatch checks.
3. Real multilingual quality, full-timeline selection, captions, framing, render, resource, and end-to-end cost measurements in C1.3.
4. C1.4 provider/budget/language/render-placement decisions and ADR acceptance based on evidence.

Do not start M2 until M1 exit criteria are met or a scoped decision changes the plan.
