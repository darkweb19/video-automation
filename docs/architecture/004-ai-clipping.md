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

### C2.4 public YouTube import extension — 2026-10-08 (client local; 2026-10-09 UTC)

The user authorized one public single-video import route by YouTube link. Use a version-pinned maintained extractor with its pinned JavaScript challenge package/runtime. Every extractor metadata, player, and API request must pass through a controlled HTTPS CONNECT proxy that accepts only the documented YouTube/API/CDN host families, validates all DNS answers as global unicast, and pins the connection address. The Go importer separately validates and streams only a selected muxed audio/video media URL through the same public-DNS and redirect rules, then applies the existing actual-media probe, source size/duration limits, hash, lease, cancellation, and cleanup lifecycle. Keep signed media URLs ephemeral and out of records, errors, logs, dashboard views, and events.

Accept only canonical standard watch, shorts, embed, and youtu.be links for one 11-character video ID. Strip query noise; reject playlist selection, live/upcoming media, credentials, non-HTTPS/non-443 endpoints, and unsupported formats. Do not use browser cookies, login/private/age-gate/DRM bypass, or caller-controlled extractor configuration. Provide an upload fallback when a public video cannot be fetched without restricted access or lacks a safe muxed format. This does not establish broad or live-provider availability. Full acceptance and deterministic verification are in [YOUTUBE-IMPORT.md](../clipping/YOUTUBE-IMPORT.md); M1 feasibility artifacts remain historical and unchanged.

## Alternatives and tradeoffs

| Option | Assessment |
| --- | --- |
| Extend Go + Python/Modal worker | Reuses existing lifecycle and deployment patterns; adds a versioned remote media/job boundary |
| Rewrite backend or clipping orchestration in Rust | Additional language and integration work without a measured bottleneck; reconsider only with profiling evidence |
| Separate clipping API/product with its own database | Duplicates auth, persistence, media, and publishing; unnecessary for current personal-channel scope |
| Run all AI inside the web container | Simplifies transfer but brings large ML dependencies and resource contention into the dashboard process |

Local persistent storage and a single owner limit horizontal scaling. Source retention and disk headroom become material for four-hour videos. Remote workers incur transfer overhead. Remaining M1 feasibility was waived, so these costs are unmeasured; revisit them in any future authorized evaluation. Changing the storage architecture still requires an explicit decision.

## Approval and evolution

On 2026-10-08 the user authorized M2 and explicitly skipped remaining feasibility. Apply Go/SQLite/local-volume and a separate worker protocol to the M2 foundation; this does not establish measured feasibility or select a paid provider. M2 does not implement ASR, candidate selection or rendering. Provider work remains inactive until configured.

### M3 implementation extension — 2026-10-09

The user authorized C3.1/C3.2 implementation, local review/checks, ten meaningful commits, and publication to the existing `feat/clipper-ai` branch. M3 adds an opt-in separate Modal faster-whisper/FFmpeg worker, authenticated source-scoped media/callback transfer, a versioned source-analysis artifact, heuristic candidate selection and editing over a persisted core. The worker is not deployed or live-tested. Before any deployment, an operator must use an immutable 40-character Hugging Face model commit, set a shared `FRAMEVAULT_PIPELINE_REVISION` in both Modal and FrameVault Settings, configure the callback origin and worker bearer, and review the exact model snapshot license. A new pipeline revision invalidates old source-analysis reuse. The model and runtime hashes in an artifact describe the run; unmeasured M1 quality, language and cost targets remain open.

Reservations use a bounded operator-declared per-second estimate, not a Modal quote. Only an explicit invoice amount and reference (including zero) settles the hold. Unknown completion is not automatically retried. Worker capabilities are attempt-scoped, HTTPS-only and retention-bound; source SHA-256 is checked before analysis. The complete operational contract and limitations are in [M3-IMPLEMENTATION.md](../clipping/M3-IMPLEMENTATION.md).

### C2.6 import retry repair and C3.UI2 intake refinement — 2026-10-13

Request separate direct YouTube tracks concurrently. An eligible per-track transient failure gets one bounded retry after 250 ms; remove and sync only that failed track file and restore its committed bytes to scratch-space accounting while retaining a successful peer. A selected-track HTTP 401, 403, or 410 schedules the existing outer retry with fresh metadata, within the five-attempt limit. See [YOUTUBE-IMPORT.md](../clipping/YOUTUBE-IMPORT.md) for the exact retry classes and safe diagnostic codes.

Keep the initial Clipping view centered on link import and file upload. Place public-link constraints in a closed disclosure and source metadata, batch selection/profile, and removal in a per-source **Details** disclosure; keep the source name, concise status, progress/error, and primary analysis action visible. Preserve an open source disclosure and the focused source control when the source list refreshes. This is a presentation refinement to the existing plain JavaScript dashboard and same-origin APIs, not a change to the source or analysis lifecycle.

The `/api/events` response does not set a hop-by-hop `Connection` header. Local streaming tests cover HTTP/1.1 and HTTP/2 with race checks; Go's HTTP/2 server also strips that header. Those tests do not verify a particular live proxy path. Live YouTube import is unverified because the current outbound proxy returned a policy 403. No deployment or universal source-availability claim follows from this repair.

This ADR records the accepted M2 design, not deployed behavior. Append or supersede decisions with evidence when subsequent evaluation resolves providers/render placement or later requirements change. Do not silently rewrite accepted architecture during an agent task.

### C2.8 / C3.SIMPLE scope replacement — 2026-10-14

The user explicitly removed the application YoutubeProxy and narrowed Clipping to YouTube import plus Modal analysis. This supersedes the earlier acquisition proxy/Go track-selector decisions, operator-invoice/budget workflow, and instructions to retain other workflows in disclosures. Use yt-dlp's maintained native downloader with FFmpeg MP4 merge/finalization, canonical single-video inputs, bounded temporary files and the existing source probe/hash/atomic-ready lifecycle. The execution environment's outbound network proxy is infrastructure and is separate from the removed application component.

Remove active clipping upload, public Drive/Dropbox, batch, retention-setting, selection-edit and reconciliation API/UI flows. Keep legacy SQLite schema/data for compatible upgrades; source capacity and dispatch lifecycle safeguards remain technical necessities. Modal analysis algorithms, runtime model pins, worker protocol, authenticated transfer, replay guards and read-only artifacts remain. Analysis dispatch no longer depends on operator billing-rate settings or manual invoice settlement. Persist genuine outcomes and runtime estimates without claiming invoices were measured. Never automatically repeat an uncertain accepted inference.

The dashboard shows a YouTube-link form, imported sources and explicit analysis actions/results, with compact connection settings. Editor, exports and publishing are outside the current scope. Local code validation and independent review are distinct from live YouTube availability, paid inference or deployment.

Related: [ADR-001](001-go-service-layout.md), [ADR-003](003-railway-persistence.md), [implementation plan](../clipping/PLAN.md), [tracker](../clipping/TRACKER.md).
