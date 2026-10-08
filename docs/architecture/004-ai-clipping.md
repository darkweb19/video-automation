# ADR-004: Integrated AI clipping with Go and a Modal worker

**Status:** Proposed; awaiting implementation-plan approval and M1 evidence.

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

## Alternatives and tradeoffs

| Option | Assessment |
| --- | --- |
| Extend Go + Python/Modal worker | Reuses existing lifecycle and deployment patterns; adds a versioned remote media/job boundary |
| Rewrite backend or clipping orchestration in Rust | Additional language and integration work without a measured bottleneck; reconsider only with profiling evidence |
| Separate clipping API/product with its own database | Duplicates auth, persistence, media, and publishing; unnecessary for current personal-channel scope |
| Run all AI inside the web container | Simplifies transfer but brings large ML dependencies and resource contention into the dashboard process |

Local persistent storage and a single owner limit horizontal scaling. Source retention and disk headroom become material for four-hour videos. Remote workers incur transfer overhead. These costs must appear in M1 results; changing the storage architecture requires an explicit decision.

## Approval and evolution

This ADR records the recommended design, not deployed behavior. After plan approval, record its acceptance date and scope here and in TRACKER.md. Append or supersede decisions with evidence when M1 resolves providers/render placement or later requirements change. Do not silently rewrite accepted architecture during an agent task.

Related: [ADR-001](001-go-service-layout.md), [ADR-003](003-railway-persistence.md), [implementation plan](../clipping/PLAN.md), [tracker](../clipping/TRACKER.md).
