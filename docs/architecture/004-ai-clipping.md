# ADR-004: Integrated AI clipping with Go and a Modal worker

**Status:** Accepted for the M2 foundation on 2026-10-08. Remaining M1 feasibility explicitly waived; paid providers, measured budget/quality, languages and render-placement decisions remain open.

**Date:** 2026-10-08

**Scope:** AI clipping in the existing video-automation repository.

## Context

FrameVault already owns authenticated UI/API access, SQLite state, local media, asynchronous processing, Modal callbacks, and YouTube publishing. Clipping adds long media inputs, multilingual ASR, temporal visual analysis, editorial selection, audio-preserving renders, editing, and scheduling. The existing generation project is five silent six-second scenes; clipping has a different lifecycle and must not be represented as a fake generation project.

## Proposed decisions

1. Extend the existing Go application in `internal/app` and the embedded plain JavaScript/CSS dashboard. Keep SQLite and source/output media on the single `/data` volume with one owning app instance. No Rust rewrite, new frontend framework, external database, or object-storage migration is included.
2. Add a Python clipping worker under `workers/modal/`, deployed independently from the same repository. Python hosts ASR/alignment/vision dependencies; Go owns authorization, job state, budgeting, scheduling, and publication. The independently deployed worker is a component of the same product.
3. Workers do not mount or directly mutate the Go app's SQLite database or `/data`. Use scoped, expiring authenticated media access and a versioned job/result protocol. Persist dispatch state first, authenticate callbacks, reject stale/duplicate results safely, and avoid frequent Modal polling. Specify artifact transfer, expiry, cancellation, and retention in M1/M2.
4. Use a dedicated bounded FFmpeg clipping renderer that preserves audio, with atomic file outputs. Initially place render orchestration in Go; M1 measures whether execution should stay on the app or move to the worker. Retain existing generation output settings and resource limits.
5. Introduce separate source, analysis, candidate, edit-version, export, and job/budget concepts with migration coverage. Keep exact schemas and endpoint names in implementation contracts rather than guessing them in this ADR. Source timestamps are canonical; edits/render timestamps map back to them.
6. Analyze full-timeline audio and visual signals, build global context, and refine finalists with denser temporal analysis. Preserve evidence and model/prompt versions. Rank editorial potential honestly; defer engagement-trained prediction.
7. Reuse immutable exports in the existing YouTube publishing flow. Extending the source-kind constraint requires a tested migration, source-path validation, deletion/in-flight-upload protection, and existing authorization/Vault behavior. Choose the scheduling mechanism in a follow-up decision.
8. Treat runtime clipping providers as separate adapters/capabilities from developer-agent model assignments and current story generation pins. Benchmark provider support for the target languages and budget before selecting any paid clipping model. Do not alter existing free-only YouTube metadata behavior incidentally.

## M2 implementation bounds

With feasibility waived, M2 uses explicit unmeasured limits: 20 GiB/source, 100 GiB aggregate source reservations, four hours, 100 jobs/batch, and a 4 GiB physical free-space floor checked against pending source reservations. These supersede the provisional M1 16 GiB source-admission proposal for the M2 lifecycle; frozen M1 evaluation schemas remain unchanged. M2 has no derived renders, so the proposed 8 GiB render/scratch allowance is not yet implemented or validated. Actual analysis/render capacity must be decided before later milestones.

Sources default to 30-day retention (configurable 1–365 days). Deletion/retention removes source-derived artifacts and media while preserving job/stage/budget audit. Import URLs and worker capabilities use the existing encrypted security storage; workers receive scoped headers, not application volume mounts. Expired uncertain stages retain their reservations; cancellation revokes media but permits bounded authenticated cost settlement without advancing the job. These are implementation decisions, not a paid-provider quote or cost guarantee.

## Alternatives and tradeoffs

| Option | Assessment |
| --- | --- |
| Extend Go + Python/Modal worker | Reuses existing lifecycle and deployment patterns; adds a versioned remote media/job boundary |
| Rewrite backend or clipping orchestration in Rust | Additional language and integration work without a measured bottleneck; reconsider only with profiling evidence |
| Separate clipping API/product with its own database | Duplicates auth, persistence, media, and publishing; unnecessary for current personal-channel scope |
| Run all AI inside the web container | Simplifies transfer but brings large ML dependencies and resource contention into the dashboard process |

Local persistent storage and a single owner limit horizontal scaling. Source retention and disk headroom become material for four-hour videos. Remote workers incur transfer overhead. These costs must appear in M1 results; changing the storage architecture requires an explicit decision.

## Approval and evolution

On 2026-10-08 the user authorized M2 and explicitly skipped remaining feasibility. Apply Go/SQLite/local-volume and a separate worker protocol to the M2 foundation; this does not establish measured feasibility or select a paid provider. M2 does not implement ASR, candidate selection or rendering. Provider work remains inactive until configured.

This ADR records the accepted M2 design, not deployed behavior. Append or supersede decisions with evidence when subsequent evaluation resolves providers/render placement or later requirements change. Do not silently rewrite accepted architecture during an agent task.

Related: [ADR-001](001-go-service-layout.md), [ADR-003](003-railway-persistence.md), [implementation plan](../clipping/PLAN.md), [tracker](../clipping/TRACKER.md).
