# Documentation guide

Start with the repository [README](../README.md) for setup and everyday workflows. For current clipping status, authorization, evidence, and next steps, use the [clipping tracker](clipping/TRACKER.md); this guide points to scope and operator references.

## Clipping

- [Gemini migration](clipping/GEMINI-MIGRATION.md) — current phased migration, architectural decisions and acceptance gates; production routing remains legacy during the foundation phase.
- [Gemini execution contract](clipping/GEMINI-EXECUTION.md) — implemented G3 lifecycle, uncertainty, automatic retries and cancellation behavior; public integration follows in G4.
- [Gemini persistence](clipping/GEMINI-PERSISTENCE.md) — reference sources, constraint migration, immutable intent and cache provenance.
- [Gemini provider contract](clipping/GEMINI-PROVIDER.md) — ADC integration, request limits, outcome handling and documentation sources.

- [Implementation plan](clipping/PLAN.md) — product scope and milestone acceptance criteria. The tracker records what is complete.
- [M2 source and job workflow](clipping/M2-IMPLEMENTATION.md) — historical source, upload, batch, budget, and API design; superseded by the current YouTube-only Clipping workflow.
- [Public YouTube import](clipping/YOUTUBE-IMPORT.md) — current URL rules, media handling, transfer limits, diagnostics, and support limits.
- [M3 analysis setup](clipping/M3-IMPLEMENTATION.md) — operator setup for the opt-in Modal analysis worker, result behavior, and quality limits. The worker is not deployed, and real-media quality and cost have not been measured.
- [Gemini architecture decision](architecture/005-gemini-clipping.md) — accepted target architecture and compatibility/release boundaries.
- [Historical clipping architecture](architecture/004-ai-clipping.md) — legacy architecture and dated extensions.

The M1 [acquisition contract](clipping/ACQUISITION.md), [evaluation protocol](clipping/EVALUATION.md), and [status checkpoint](clipping/M1-STATUS.md) are historical planning records. The user waived remaining M1 feasibility before M2. M1 is skipped, not passed; its proposed gates are not results. The provisional 16 GiB M1 source limit is superseded by the current 20 GiB import bound. Use the YouTube and M3 guides for current Clipping behavior.

## Other decisions and records

- [Architecture decisions](architecture/) cover the Go service layout, text routing, persistence, and clipping.
- [Dashboard redesign](frontend-redesign.md) and [YouTube upload UI](youtube-upload-ui.md) record implemented product behavior and design constraints.
- [Prompt records](prompts/) preserve versioned contracts and identify their production sources; later versions explain which parts they supersede.
- [Session archive](SESSION.md) preserves earlier handoffs; the root `SESSION.md` is the current task handoff.
