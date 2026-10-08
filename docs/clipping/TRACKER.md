# AI clipping tracker

Last updated: 2026-10-08. This file is the authoritative status and authorization record. Scope/acceptance: [PLAN.md](PLAN.md). Architecture: [ADR-004](../architecture/004-ai-clipping.md). Agent workflow: [AGENTS.md](../../AGENTS.md).

## Current checkpoint

- Phase: M2 complete, independently approved and locally verified; remaining M1 feasibility explicitly waived. Existing M1 offline artifacts are preserved, without claiming completed feasibility.
- Next implementation: obtain M3 scope authorization and record provider/spending decisions before paid dispatch. M2 commit/push to `feat/clipper-ai` is authorized; Git history and remote head record the resulting publication.
- Code deployment: none for clipping. Live verification: none. This checkpoint was authored before the authorized commit/push; publication is verified separately against the remote branch.
- Requested lead: GPT-6.1 SOL / High. Implementers: TERRA / Max and LUNA / Max. Actual execution must be recorded per assignment; this file does not select models.
- M2 availability: TERRA is not exposed; implementation uses requested `gpt-6-luna` / `max`. Lead model/effort are not exposed. Earlier M1 agent quota interruption is historical evidence.
- M2 applies Go/SQLite/local-volume and worker protocol architecture. Product budget interpretation, paid provider choice and measured feasibility remain unvalidated; explicit integer microUSD limits do not authorize automatic spending.

## Authorization log

| Date | User instruction / scope | Effect |
| --- | --- | --- |
| 2026-10-08 | Analyze, ask questions, plan, and wait; do not implement | Research/planning authorized; feature implementation withheld |
| 2026-10-08 | Update AGENTS.md and implementation-plan guidelines for sessions and multiple models | Repository documentation edits and agent-assisted document review authorized |
| 2026-10-08 | Commit and push these planning changes to `main` | Authorized this documentation publication; feature implementation, paid benchmarks, and deployment remain unapproved |
| 2026-10-08 | Approve M1; start C1.1 and C1.2; propose benchmark inputs and spending limit before paid runs; use agents | M1 feasibility work authorized; no paid-run approval, no later milestones, publication, or deployment authorization |
| 2026-10-08 | Push the feasibility work to `feat/clipper-ai`, forked after `430751ee169fbae0d1b7c267c5ff1c077ab13a0a` | Commit and branch push authorized from that exact baseline; no merge, paid runs, later milestones, live video publication, or deployment authorized |
| 2026-10-08 | “Can you implement M2, we are skipping feasibility” | Authorizes M2/C2.1–C2.3 and necessary local verification/review; waives remaining M1 dependency. No paid provider/benchmark or deployment authorization. |
| 2026-10-08 | “commit and push the changes to feat/clipper-ai braanch after completing in github” | Authorizes commit and push of completed M2 to https://github.com/darkweb19/video-automation.git on `feat/clipper-ai`; preserve its existing M1 commit. No merge or deployment authorization. |

Record later approvals here with the milestone/task range, spending limit where applicable, and any external action scope. Do not translate a product per-video budget into permission to spend engineering funds. Do not require new permission at every session if the existing approval already covers the work.

## Milestone board

| Milestone | State | Completion evidence |
| --- | --- | --- |
| M0 Planning and execution workflow | done | C0.1 checkpoint below; documentation checks and LUNA Max review complete |
| M1 Feasibility | skipped | User explicitly waived remaining feasibility; offline artifacts preserved, no measured results |
| M2 Sources, jobs, batches | done | C2.1–C2.3 checkpoint below; final normal/race/regression checks and independent review passed |
| M3 Understanding and selection | planned | — |
| M4 Editor and exports | planned | — |
| M5 YouTube and schedules | planned | — |
| M6 Verification and rollout | planned | Code readiness, deployment, and live checks tracked separately |

States: `planned`, `ready`, `in_progress`, `in_review`, `blocked`, `skipped`, `done`. Dependencies and authorization must be satisfied before `ready`. A milestone is `done` only when its PLAN.md exit criteria have evidence, not merely because its code was written.

## Task board

| ID | Task | Depends on | State | Owner / evidence |
| --- | --- | --- | --- | --- |
| C0.1 | Scope, ADR, tracker, agent workflow, session handoff | — | done | Lead; C0.1 checkpoint below |
| C1.1 | Acquisition contract and source feasibility | Scope approval | skipped | Offline draft/checks preserved; remaining feasibility waived |
| C1.1a | Source contract, official route review, adversarial offline simulation | Scope approval | skipped | ACQUISITION.md; 20 local tests; final M1 review waived |
| C1.1b | Controlled live acquisition on permitted sample inputs | C1.1a; actual inputs and allowed access | skipped | No user media supplied; YouTube permitted automated route unresolved |
| C1.2 | Evaluation fixtures, metrics, targets, transcript/candidate contracts | Scope approval | skipped | Draft protocol, schemas, synthetic fixtures; actual corpus not established |
| C1.2a | Evaluation protocol/contracts and synthetic fixture checks | Scope approval | skipped | EVALUATION.md; 2 synthetic bundles and 12 rejection checks; final M1 review waived |
| C1.2b | Freeze actual corpus and blinded reference labels | C1.2a; permitted sample inputs and human labelers | skipped | Eight 15-minute slots plus genuine four-hour source proposed; no acquired media or labels |
| C1.3 | Provider and complete-pipeline cost/quality benchmark | C1.1, C1.2; sample inputs and paid-run authorization | skipped | Proposed US$20 ceiling; actual authorized spend US$0; providers/rates/licenses undecided |
| C1.4 | Record provider, budget, language, and architecture decisions | C1.3 | skipped | M2 foundation accepted; paid-provider and feasibility decisions remain open |
| C2.1 | Schema/migrations, source lifecycle, job/budget state machine | M1 waived; M2 approval | done | m2_storage / LUNA Max; checkpoint below |
| C2.2 | Upload and public-link adapters | C2.1 contracts | done | m2_acquisition / LUNA Max; checkpoint below |
| C2.3 | Worker protocol, retries/cancellation, batch queue, progress | C2.1 contracts | done | m2_integration + m2_ui / LUNA Max; checkpoint below |
| C3.1 | ASR, alignment, audio/visual events, global context | M2 | planned | Unassigned |
| C3.2 | Content-specific selection, scores, boundaries, deduplication, hooks | C3.1 | planned | Unassigned |
| C4.1 | Caption/framing/edit contracts and audio-preserving renderer | M3 | planned | Unassigned |
| C4.2 | Preview/editor, overrides, translations, export lifecycle | C4.1 contracts | planned | Unassigned |
| C5.1 | Clip export integration with YouTube storage and uploads | M4 | planned | Unassigned |
| C5.2 | Scheduling decision, durable schedules, UI, recovery | C5.1 | planned | Unassigned |
| C6.1 | End-to-end and regression evidence, independent review | M2–M5 | planned | Unassigned |
| C6.2 | Rollout/rollback documentation and readiness review | C6.1 | planned | Unassigned |
| C6.3 | Worker/app rollout and live smoke verification | C6.2; deployment/live-action authorization | planned | Unassigned |

Dependencies marked “contracts” allow parallel implementation only after the shared interface is agreed and file ownership assigned. All milestone tasks inherit PLAN.md acceptance criteria. Split these tasks into smaller numbered tasks as needed; preserve completed IDs and evidence.

## Active assignments

TERRA is not exposed in this session; M2 implementation and independent review use the requested available LUNA Max. The lead model/effort are not exposed to the agent interface; no lead-model claim is made. M1 assignments below are historical.

| Task ID | Agent / actual model / effort | Exclusive write paths | Contract/dependency | Required checks | State |
| --- | --- | --- | --- | --- | --- |
| C1.1 | acquisition / gpt-6-luna / max | docs/clipping/ACQUISITION.md; tools/clipping/acquisition_contract.py; tools/clipping/test_acquisition_contract.py | Approved M1; source-time milliseconds; no product downloader | 20 offline tests passed; primary sources documented | interrupted by agent quota; lead integrated |
| C1.2 | evaluation / gpt-6-luna / max | docs/clipping/EVALUATION.md; docs/clipping/contracts/; docs/clipping/fixtures/; tools/clipping/evaluation* | Approved M1; source-time milliseconds; corpus/spending proposed only | 2 fixture bundles and 12 rejection checks passed | interrupted by agent quota; lead integrated |
| C1.1/C1.2 review | review_m1 / gpt-6-luna / max | Read-only | Independent reviewer did not author artifacts | Preliminary doc review and 16 acquisition tests; final review interrupted by agent quota | pending final review |

M2 active assignments:

| Task ID | Agent / actual model / effort | Exclusive write paths | Contract/dependency | Required checks | State |
| --- | --- | --- | --- | --- | --- |
| C2.1 | m2_storage / gpt-6-luna / max | clipping_models.go, clipping_storage.go/tests, storage.go migration/mutex | source/job/budget contracts | Migration, concurrency, idempotency, cancellation, source-time tests | done |
| C2.2 | m2_acquisition / gpt-6-luna / max | clipping_acquisition.go/tests, clipping_files_unix.go, clipping_files_windows.go | C2.1 source APIs | SSRF, bounded streaming, interruption, media validation, cross-builds | done |
| C2.3 | m2_integration / gpt-6-luna / max | clipping_jobs.go/tests, dashboard_clipping.go/tests, dashboard.go, processor.go, Python clipping protocol/tests, callback regression harness; regression guard in processor_downloads.go/tests | C2.1/C2.2 contracts | Auth, protocol, expiry, duplicates, restart, progress | done |
| C2.3 UI | m2_ui / gpt-6-luna / max | clipping.js/css, index.html/app.js hooks, clipping-ui.test.cjs | HTTP contract | Node UI regression/syntax | done |
| C2.1–C2.3 review | m2_review / gpt-6-luna / max | Read-only | Separate from implementation authors | Independent review of integrated changes and fixes | done |

Only the lead edits this tracker. Agent conclusions do not automatically constitute acceptance. Reconcile shared-worktree changes and verify integrated behavior.

## Evidence and decisions

### 2026-10-08 — C2.1–C2.3 M2 implementation checkpoint

- User authorized M2 with remaining M1 feasibility skipped, then authorized committing/pushing to `feat/clipper-ai`. Preserved remote M1 commit `84b0844`, based on planning commit `430751e`. No paid provider calls, live imports, deployment, merge or M3–M6 work occurred.
- Actual implementers: `m2_storage`, `m2_acquisition`, `m2_integration`, `m2_ui`, all `gpt-6-luna` / `max`. Independent read-only reviewer: `m2_review`, same actual model/effort, separate from the authors. TERRA was unavailable; lead model/effort are not exposed. The linked CTO browser skill was unavailable; repository instructions governed execution.
- Changed paths: root AGENTS/CLAUDE/README/SESSION, PLAN/TRACKER/ADR-004 and new [M2-IMPLEMENTATION.md](M2-IMPLEMENTATION.md); `internal/app/clipping_{models,storage,acquisition,jobs}.go` and tests, OS-specific clipping file helpers, `dashboard_clipping.go` and tests, additive `storage.go` migration, dashboard/processor wiring; embedded clipping JS/CSS, dashboard HTML/app hooks and embed map; `tests/clipping-ui.test.cjs`; provider-neutral Python clipping protocol/tests. Existing callback regression harness received only its missing `typing.Any` import/extraction binding. M1 artifacts and existing model/provider pins are preserved.
- Acceptance evidence covers durable upload offsets/crash-tail truncation, import lease heartbeat/restart/final-attempt cleanup, actual local media probing, bounded streaming and disk reservations, SSRF/DNS/provider-family redirect rejection, encrypted import/callback capabilities, auth/CSRF, batch idempotency, concurrent integer budget reservations, actual-spend overrun, uncertain lease recovery, cancellation and late one-time settlement, source retention/tombstone recovery, strict artifact bounds/source timestamps, and a Go→Python→Go production-preparation protocol fixture. Worker-ledger restarts never re-execute uncertain paid work. No runtime provider is selected; queued jobs await configuration.
- Review found and authors fixed unsafe destination/redirect cases, capability/error disclosure, cancellation races, uncertain budget release, late callback accounting, envelope size parity, and import crash/lease cleanup. Final callback regression verifies worker-supplied signed URLs/tokens cannot reach Store records, API detail or job/batch SSE. The independent reviewer approved the integrated M2 code and the final safe-error fix, with no unresolved actionable finding.
- Required cross-workflow checks exposed a pre-existing stale scene snapshot race: a completed scene could be downloaded again after its task key was released (six requests for five scenes). Integration added a durable status/generation/attempt check before the fetch in `processor_downloads.go`, plus `processor_downloads_test.go` reproducing a stale completed snapshot without timing dependence. Independent review approved the bounded guard; retry scheduling, provider pins, output quality and authentication remain unchanged. Initial full normal/race runs failed this regression; those failures were not counted as passes.
- Final verification: `go test ./... -count=1` passed (app 32.857s); `go test -race ./... -count=1` passed (157.742s); the earlier focused M2 race run passed (24.330s); `go vet ./...` passed. Tests used Go 1.22.12 with isolated caches and permitted loopback listeners. Node dashboard tests passed 87/87 and clipping tests 11/11; both JS syntax checks passed. `PYTHONDONTWRITEBYTECODE=1 python3 workers/modal/test_clipping_protocol.py` passed 8 tests; `workers/modal/callback_delivery_test.py` passed. Preserved M1 offline acquisition checks passed 20 tests; evaluation accepted 2 synthetic bundles and rejected 12 invalid mutations. Final Windows amd64 and Darwin arm64 cross-builds passed; neither platform was executed. Local Markdown link/whitespace/newline checks and `git diff --check` passed.
- Limits are explicit and unmeasured: 20 GiB/source, 100 GiB logical reservations, four hours, 100 jobs/batch, 4 GiB physical headroom, 16 MiB upload chunks and 4 MiB artifact envelopes. Upload resume matches name/size, not original file content; the UI states that limitation. Native Windows clipping source operations fail closed and direct users to Docker/Linux. YouTube has an original-file fallback; live Drive/Dropbox behavior, rendered-browser layout, true four-hour resource use, paid cost/quality and deployment remain unverified. Transcription, candidate selection and exports are later milestones.
- At this evidence-writing checkpoint, verified changes are local/uncommitted on `feat/clipper-ai`; authorized publication follows. Git history records the resulting commit, and the remote head is checked separately after push. Next implementation requires M3 authorization and a recorded provider/spending decision before any paid dispatch.

### 2026-10-08 — C1.1/C1.2 local feasibility checkpoint

- Synced the initially clean `work` checkout from `02b94b9` to published planning baseline `430751e` by fast-forward only. No new commit, push, PR, deployment, provider inference, or real-media import occurred.
- User approved M1 and C1.1/C1.2 with agents. Acquisition and evaluation were assigned disjoint paths to `gpt-6-luna` / `max`; `review_m1` independently reviewed their initial drafts. TERRA unavailable; no substitute used. All three agents later hit a usage limit before final handoffs.
- Drafts: ACQUISITION.md source routes/failures/storage bounds; EVALUATION.md preregistered corpus/metrics/cost proposal; three source/transcript/candidate JSON schemas; two clearly simulated original-script/word-timing/evidence bundles; stdlib offline acquisition and evaluation checkers. M1-STATUS.md summarizes evidence and limitations. Root PLAN/ADR/SESSION authorization references updated. Generated Python caches ignored only beneath tools/clipping.
- Preliminary reviewer found missing full-source gold transcripts/timings, normalization and gold-window definitions, and partial HTTP response acceptance. Those findings were fixed; lead also fixed malformed fixture helper arguments, tightened word timing/character/language linkage, finite numbers, strict boolean constants, evidence overlap, and source-route mapping. The reviewer has not reviewed the final integrated version; C1.1/C1.2 are not marked done.
- Verification: `PYTHONDONTWRITEBYTECODE=1 python3 tools/clipping/test_acquisition_contract.py` passed 20 tests; `PYTHONDONTWRITEBYTECODE=1 python3 tools/clipping/evaluation_validate.py --offline` passed 2 synthetic bundles and rejected 12 invalid mutations. A Python pathlib/regex check verified all local Markdown links and whitespace in 7 changed/new Markdown files; all clipping JSON and Python parsed. `git diff --check` passed. No Go/dashboard/worker files changed, so application runtime tests were not rerun.
- Benchmark proposal: eight complete 15-minute sources (2 each podcast/comedy/gaming/movie) plus one genuine four-hour gaming source; English, Hindi/Hinglish, Nepali/mixed speech. US$20 aggregate ceiling: US$0 preflight, US$4 canary, US$10 typical remainder, US$5 long winner, US$1 settlement/retry reserve. Product per-source cost target remains separate and tentative. Actual inputs, labelers, permitted acquisition route, rates/licenses/credentials, and paid approval are pending; no quality or cost result exists.
- Source conclusions: YouTube-first automation remains unresolved; original-file quality benchmarks do not count as YouTube acquisition success. Drive public content links and Dropbox `dl=1` are documented candidates; live behavior and network access are unverified. Nepali word alignment needs specific testing. Source/storage values remain provisional.
- Changes were local/uncommitted at this checkpoint; the subsequent branch-publication instruction is recorded below. Resume with final independent review, actual sample selection, and explicit paid-run decision; do not start M2.

### 2026-10-08 — Feasibility branch publication

- User authorized committing and pushing the integrated feasibility snapshot to `feat/clipper-ai`, based directly on `430751ee169fbae0d1b7c267c5ff1c077ab13a0a`. The checkout was at that exact baseline, and no existing remote branch with the requested name was found before branch creation. Git history records the resulting commit; remote publication is verified separately after push.
- Included paths: root SESSION.md, ADR-004, clipping plan/tracker/status, acquisition/evaluation documents, contracts, synthetic fixtures, and tools/clipping. No production application or worker files are included.
- C1.1/C1.2 remain in review and M1 remains in progress. Branch publication does not establish live acquisition, provider quality/cost, final independent review, or architecture acceptance. Paid authorization remains US$0.

### 2026-10-08 — C0.1 planning checkpoint

- Baseline inspected: current branch `work`; working tree initially clean. Earlier SESSION.md branch names describe historical work, not this checkout.
- Created the scope/acceptance plan, proposed ADR, and this tracker; extended root agent instructions and handoff.
- Runtime model/provider choices are not pinned by the developer-agent preferences.
- No feature implementation, provider calls, or deployment performed. Documentation commit/push to `main` was authorized in a later instruction; repository history records the resulting publication.
- Changed paths: `AGENTS.md`, `CLAUDE.md`, `SESSION.md`, `docs/clipping/PLAN.md`, `docs/clipping/TRACKER.md`, `docs/architecture/004-ai-clipping.md`.
- Verification: `git diff --check` passed. A Python `pathlib`/regular-expression check resolved every relative Markdown link in all six files. A Python line check found no trailing whitespace or missing final newline in all six files, including the new untracked documents. No runtime tests were required for this documentation-only change.
- Independent reviewer: `review_clipping_docs`, actual model `gpt-6-luna`, effort `max`, read-only. The review found missing M0 acceptance criteria and a pending evidence entry. Added M0 exit criteria to PLAN.md and completed this evidence record; no other actionable issues were reported.
- C0.1 and M0 are complete; this means the planning infrastructure is ready, not that feature implementation or ADR acceptance is approved.

Use a new dated checkpoint for future sessions with task IDs, actual author/reviewer models if known, changed paths, commands/results, artifact references, and next action. Reference actual commits/PRs when they exist; never invent them. Record remaining local changes explicitly.

## Known dependencies and open decisions

- M2 is approved; remaining M1 feasibility is waived. The automatic minimum-US$1 product-budget interpretation remains unvalidated. M3–M6 are not authorized by M2 approval.
- Representative permitted video inputs and an engineering benchmark spending limit.
- YouTube acquisition feasibility; public Drive/Dropbox download behavior.
- Exact additional Indian languages, measurable quality targets, and provider choices from M1.
- Source retention, volume capacity, backup/restore objectives, and scheduling mechanism.
- Existing production callback routing and real YouTube smoke checks remain unverified per root SESSION.md; revalidate before relevant live work.
- Future customer isolation and engagement learning are deferred, not required for this release.

## Resume prompt

“Continue AI clipping in video-automation. Read AGENTS.md, SESSION.md, docs/clipping/PLAN.md, docs/clipping/TRACKER.md, and ADR-004. Inspect the checkout and existing authorization, select the next dependency-ready authorized task, and use the requested lead/implementer workflow. Update the tracker and handoff with evidence before finishing. Do not treat this resume message as authorization for currently unapproved implementation, spending, or deployment.”

To start implementation, the user can instead explicitly approve a scope, for example: “Approve M1; begin C1.1 and C1.2. Prepare the benchmark proposal before paid runs.” A later approval can cover multiple milestones; preserve its scope across sessions.
