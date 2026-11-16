# Gemini clipping migration

Authorized 2026-11-10 (America/Toronto). Baseline: `c85d376` on the local `work` branch. This plan supersedes the download-first/Modal clipping decisions for new Gemini work; historical records remain evidence, not current requirements.

## Execution and authorization

The user authorized implementation in phases, at least 15 meaningful non-empty commits per phase, Terra and LUNA agents at maximum reasoning, and the main thread as CTO/task owner. Each phase requires current README.md and AGENTS.md, relevant passing checks, independent review, and a reconciled tracker/handoff. The user subsequently explicitly selected automatic continuation through all phases; do not ask for approval at each phase boundary. Root coordinates architecture, integration, Git and central records; agents author implementation and tests. Actual requested models are `gpt-6.1-terra` / `max` and `gpt-6-luna` / `max`.

Local implementation and commits are authorized. On 2026-11-12 the user additionally authorized pushing each completed phase to a feature branch; the selected target is `origin/feat/gemini-video-analysis`. This does not authorize deployment, infrastructure changes, live paid inference, source downloads, extractor integration, merge, force-push or main-branch changes. Integration stays on the supplied local `work` branch. The CTO may assign isolated local worktrees/branches for dependency-ready drafts and integrate their reviewed commits in phase order. Use the current phase-push authorization; do not infer deployment or main-branch permissions from it. Do not pad commit counts with empty commits or arbitrary formatting changes.

## Architectural decisions

- Keep Go, the cohesive internal/app package, authenticated embedded dashboard, SQLite, and one owning instance with persistent /data. Preserve generation, Vault, YouTube upload, Modal video generation and FFmpeg assembly.
- New analysis sends a canonical public YouTube URL as a video part to managed Gemini using ADC/service accounts. No API keys, local source acquisition, media upload/staging, Modal clipping work, or extractor fallback.
- Gemini owns creative selection, hooks, titles, candidate boundaries, scores and rank. Go validates and preserves these decisions. Invalid output is rejected or sent back to Gemini under a bounded correction policy; never silently rescore, reorder, trim, deduplicate by heuristic or fill candidate quotas.
- Results use schema v3, source-time integer milliseconds, explicit model-reported duration provenance and editorial/timestamp disclaimers. A URL/request digest is never a media SHA-256. Reference sources cannot claim independently verified media properties.
- Initial proposed model is gemini-3.8-flash; verify model availability, SDK version/API names, static processing and schema support against current primary sources. Store model/prompt/schema/options snapshots and actual usage. Do not claim a model endpoint or mode was tested because documentation exists.
- Begin with an isolated provider feasibility implementation; production routing remains unchanged until compatibility, integration and release gates pass. Four-hour input support, private/account-owned videos and uniform language quality are not assumed.
- No new agent hosting, queue service, object storage or database is needed for this migration. Google-hosted deployment, if later authorized, should preserve the single-owner persistent-storage architecture; external hosting requires verified federation rather than service-account keys.

## Phases and acceptance

| Phase | Scope | Exit criteria |
| --- | --- | --- |
| G1 Provider feasibility foundations | Versioned contracts/fixtures, strict validator, ADC SDK client, video request/prompt/schema, response/error/usage handling and reproducible offline evidence | >=15 meaningful commits; no production activation; offline contract/provider tests pass; independent review; README/AGENTS updated. Live ADC/YouTube/quality/cost feasibility is a separately recorded release gate, never claimed passed by mocks. |
| G2 Compatible reference persistence | URL source kind, safe SQLite constraint migration, provider/configuration snapshots, new cache identity and legacy artifact compatibility | >=15 meaningful commits; populated/empty/restart migrations and referential integrity pass; no fake file fields; existing media/jobs/results preserved; README/AGENTS and independent review complete. |
| G3 Durable Gemini orchestration | Persist-before-send job execution, bounded concurrency/deadlines, lifecycle, uncertain outcomes, cancellation/deletion, validated artifact completion, observability | >=15 meaningful commits; restart/late-response/failure/idempotency tests pass; no heuristic selection or acquisition in Gemini path; opt-in only; README/AGENTS and independent review complete. |
| G4 Dashboard/API integration | URL-to-analysis submission, preserved existing source/job APIs where sound, Gemini results/rank/evidence, truthful progress and provider readiness | >=15 meaningful commits; auth, same-origin, API, Node and rendered browser checks pass; legacy results render; no fake local-media links or extraction UI; README/AGENTS and independent review complete. |
| G5 Evaluation and release readiness | Corpus/evaluation tooling, long-video strategy evidence, deployment/auth/backup/runbook, canary/rollback controls | >=15 meaningful commits; code readiness separate from external checks; approved live runs required for provider/quality/cost claims; limits and Preview dependency explicit; README/AGENTS and independent review complete. Actual deployment remains separately authorized. |
| G6 Legacy clipping retirement | Stop new legacy submissions, drain safety, remove clipping downloader/worker-only runtime/dependencies, preserve history and unrelated generation | >=15 meaningful commits; retirement gated on validated replacement and drained attempts; generation/Vault/upload/media regressions pass; README/AGENTS and independent review complete. |

Dependency-ready offline tasks may proceed while external gates are pending. Do not activate or retire a production path while its provider/release gates remain unverified. Runtime needs no engineering paid calls to execute its mock tests.

## G1 delivery checkpoint

Completed offline on 2026-11-12 with 22 meaningful local commits including the closure record. Final code checkpoint `fc821f4` passed the full Go suite, focused race checks, vet, static build, 106 browser-unit checks, Python callback regression and independent Terra Max review. README and AGENTS are current. No production activation or live feasibility is claimed. G2 persistence is also complete offline (2026-11-12): 27 meaningful commits including closure, final code `15cfea1`, full Go suite /broad race/vet/build and independent Terra Max review passed; README/AGENTS and persistence guide are current. G3 execution is complete offline on 2026-11-13 with 25 meaningful commits including closure: final code `34135af`, full Go suite (76.139s), broad clipping/Gemini race (125.573s), vet and static build passed; independent LUNA Max review approved source `c0f0667`. README/AGENTS and execution/provider guides are current. G4 dashboard/API is complete offline (2026-11-16): 30 commits including closure, final app checkpoint `f27dbd5` plus Compose `4e25e78`, independent API/UI/Compose approvals, full Go 49.167s, broad race 154.910s, vet/build, Node 107 and synthetic desktop/mobile Chromium checks passed. README/AGENTS are current; strict premium static audit remains non-clean with inherited findings. G5 evaluation is complete offline (2026-11-16) with 18 commits including closure: final code `4192e60`, full integrated Go suite (app49.121s/CLI0.208s), vet, CLI build and exact synthetic workflow assertions passed; independent Terra Max review approved source `9aa447b`, including empty panels and language-set semantics. README/AGENTS and operations/evaluation guides are current. G6 staged retirement awaits integration.

## G1 reviewable slices

1. Record migration decisions, phase gates and authorization.
2. Add typed schema-v3 request/result contract.
3. Validate bounded strict JSON and response identity.
4. Validate timestamps, duration and candidate ranks without mutation.
5. Validate hooks, evidence and editorial limits.
6. Add representative source/weak-source fixtures and adversarial coverage.
7. Publish the machine-readable analysis contract.
8. Pin the official SDK and compatible toolchain requirement.
9. Validate ADC-only cloud configuration.
10. Add the versioned editorial prompt.
11. Add the Gemini-supported response schema.
12. Build canonical direct-YouTube video requests.
13. Decode provider completion and usage safely.
14. Integrate a single-attempt bounded ADC client.
15. Exercise failure/cancellation/uncertainty through offline transport tests.
16. Record verified evidence and update README, AGENTS and phase handoff.

Slice boundaries may change to keep intermediate commits coherent and reviewable. Count actual commits and record their subjects after delivery, not before.

## Evidence boundaries

The attached environment currently reports no Google runtime identity/credentials and does not allow Google inference endpoints. Do not request secret values in chat or execute paid inference. A future live gate needs a configured Google project/ADC identity, permitted endpoints, approved sample sources and an explicit spending limit. Existing evaluation files contain proposed/unmeasured historical targets; distinguish them from results.

Scores are editorial estimates, not guaranteed virality predictions. Timestamps are proposed clip boundaries. Future youtube-extractor-rs integration must verify actual source/media identity and boundaries independently and remains out of scope.
