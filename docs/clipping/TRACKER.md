# AI clipping tracker

Last updated: 2026-10-08. This file is the authoritative status and authorization record. Scope/acceptance: [PLAN.md](PLAN.md). Architecture: [ADR-004](../architecture/004-ai-clipping.md). Agent workflow: [AGENTS.md](../../AGENTS.md).

## Current checkpoint

- Phase: planning documents prepared; feature implementation awaiting approval.
- Next action: obtain the user's scope approval, then start C1.1 and the independent portions of C1.2. Paid C1.3 runs need an explicit benchmark spending limit and permitted sample inputs.
- Code deployment: none for clipping. Live verification: none. No clipping code exists from this planning session.
- Requested lead: GPT-6.1 SOL / High. Implementers: TERRA / Max and LUNA / Max. Actual execution must be recorded per assignment; this file does not select models.
- Availability observed in this planning session: LUNA (`gpt-6-luna`) supports Max; TERRA is not exposed. Recheck each session. Do not treat another model name as TERRA without a verified mapping.
- Proposed budget interpretation and ADR-004 are pending implementation-plan approval.

## Authorization log

| Date | User instruction / scope | Effect |
| --- | --- | --- |
| 2026-10-08 | Analyze, ask questions, plan, and wait; do not implement | Research/planning authorized; feature implementation withheld |
| 2026-10-08 | Update AGENTS.md and implementation-plan guidelines for sessions and multiple models | Repository documentation edits and agent-assisted document review authorized |
| 2026-10-08 | Commit and push these planning changes to `main` | Authorized this documentation publication; feature implementation, paid benchmarks, and deployment remain unapproved |

Record later approvals here with the milestone/task range, spending limit where applicable, and any external action scope. Do not translate a product per-video budget into permission to spend engineering funds. Do not require new permission at every session if the existing approval already covers the work.

## Milestone board

| Milestone | State | Completion evidence |
| --- | --- | --- |
| M0 Planning and execution workflow | done | C0.1 checkpoint below; documentation checks and LUNA Max review complete |
| M1 Feasibility | planned | — |
| M2 Sources, jobs, batches | planned | — |
| M3 Understanding and selection | planned | — |
| M4 Editor and exports | planned | — |
| M5 YouTube and schedules | planned | — |
| M6 Verification and rollout | planned | Code readiness, deployment, and live checks tracked separately |

States: `planned`, `ready`, `in_progress`, `in_review`, `blocked`, `done`. Dependencies and authorization must be satisfied before `ready`. A milestone is `done` only when its PLAN.md exit criteria have evidence, not merely because its code was written.

## Task board

| ID | Task | Depends on | State | Owner / evidence |
| --- | --- | --- | --- | --- |
| C0.1 | Scope, ADR, tracker, agent workflow, session handoff | — | done | Lead; C0.1 checkpoint below |
| C1.1 | Acquisition contract and source feasibility | Scope approval | planned | Unassigned |
| C1.2 | Evaluation fixtures, metrics, targets, transcript/candidate contracts | Scope approval | planned | Unassigned |
| C1.3 | Provider and complete-pipeline cost/quality benchmark | C1.1, C1.2; sample inputs and paid-run authorization | planned | Unassigned |
| C1.4 | Record provider, budget, language, and architecture decisions | C1.3 | planned | Lead |
| C2.1 | Schema/migrations, source lifecycle, job/budget state machine | M1 | planned | Unassigned |
| C2.2 | Upload and public-link adapters | C2.1 contracts | planned | Unassigned |
| C2.3 | Worker protocol, retries/cancellation, batch queue, progress | C2.1 contracts | planned | Unassigned |
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

No implementation agents assigned. For each future assignment record:

| Task ID | Agent / actual model / effort | Exclusive write paths | Contract/dependency | Required checks | State |
| --- | --- | --- | --- | --- | --- |

Only the lead edits this tracker. Agent conclusions do not automatically constitute acceptance. Reconcile shared-worktree changes and verify integrated behavior.

## Evidence and decisions

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

- User approval of implementation scope and the proposed minimum-$1 budget interpretation.
- Representative permitted video inputs and an engineering benchmark spending limit.
- YouTube acquisition feasibility; public Drive/Dropbox download behavior.
- Exact additional Indian languages, measurable quality targets, and provider choices from M1.
- Source retention, volume capacity, backup/restore objectives, and scheduling mechanism.
- Existing production callback routing and real YouTube smoke checks remain unverified per root SESSION.md; revalidate before relevant live work.
- Future customer isolation and engagement learning are deferred, not required for this release.

## Resume prompt

“Continue AI clipping in video-automation. Read AGENTS.md, SESSION.md, docs/clipping/PLAN.md, docs/clipping/TRACKER.md, and ADR-004. Inspect the checkout and existing authorization, select the next dependency-ready authorized task, and use the requested lead/implementer workflow. Update the tracker and handoff with evidence before finishing. Do not treat this resume message as authorization for currently unapproved implementation, spending, or deployment.”

To start implementation, the user can instead explicitly approve a scope, for example: “Approve M1; begin C1.1 and C1.2. Prepare the benchmark proposal before paid runs.” A later approval can cover multiple milestones; preserve its scope across sessions.
