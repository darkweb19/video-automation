# Session handoff — 2026-10-08

## Current focus — AI clipping M1 feasibility

- The user approved M1 on 2026-10-08 and authorized starting C1.1 and C1.2 with agents. Propose concrete benchmark inputs and a spending cap before any paid runs. The user subsequently authorized committing and pushing the feasibility work to `feat/clipper-ai`, based directly on `430751ee169fbae0d1b7c267c5ff1c077ab13a0a`. Later milestones and deployment are not authorized. Earlier planning documents were committed/pushed to `main` separately.
- Read `docs/clipping/PLAN.md`, `docs/clipping/TRACKER.md`, and `docs/architecture/004-ai-clipping.md` for scope, authoritative task state, proposed architecture, and next action. Keep cross-session status in the tracker.
- Requested workflow: GPT-6.1 SOL High leads; TERRA Max and LUNA Max implement bounded tasks. TERRA is unavailable in this session; acquisition, evaluation, and independent review use actual `gpt-6-luna` at `max`. Lead model/effort are not exposed by this interface. `AGENTS.md` defines ownership and handoff; developer-agent choices do not select runtime video-analysis models.
- M1 branch snapshot: ACQUISITION.md and EVALUATION.md, three contracts, and two synthetic fixture bundles are integrated with offline checks. See docs/clipping/M1-STATUS.md; Git history records the feasibility commit on `feat/clipper-ai`. No paid calls, real-media import, live video publication, or deployment. Agents hit usage limits before final independent review; C1.1/C1.2 remain in review, and real inputs/labels plus C1.3 paid approval remain pending. Proposed cap is US$20 total, with US$0 currently authorized.
- Resume from the next authorized dependency-ready task in TRACKER.md. Routine session continuation does not require renewed approval once implementation scope is explicitly authorized.

## What was done
- The `feat/framevault-studio-ux` branch has 18 commits from `main`, including this handoff, covering the studio redesign and interaction improvements across all six visual areas. README workflows and `docs/frontend-redesign.md` were updated.
- Added intent-aware story/clip entry, real recent project activity, loaded-record History filters, stable media and action focus during live updates, native categories, protected random ideas, in-flight prompt drafts, provider guidance, and accurate live connection and failure states.
- Final diff was accepted by the independent reviewer. Verification passed: `go test ./...` (36.599s), `go vet ./...`, 87/87 Node dashboard tests (0 failed or skipped), JavaScript syntax, Python callback regressions, `git diff --check`, compiled Go binary, and 28 isolated local HTTP checks for embedded assets, CSP, auth/session/password gate, Settings, Vault grant revocation, logout, and the latest embedded `app.js`.
- No provider operations or deployment occurred.

## Decisions locked
- Keep Go + SQLite, embedded plain JavaScript/CSS, same-origin authenticated APIs, EventSource updates, and local state/media on one `/data` volume. No new framework or frontend dependency.
- Preserve the studio's quiet light surfaces, clear creation formats, and closed technical disclosures. Do not invent previews, usage totals, or unavailable provider pricing.
- Activity totals count clips; recent work includes real projects. History filters loaded records, and older-clip pagination remains. Video pricing is runtime data; story planning cost is separate.
- Projects remain five sequential six-second silent 480p scenes at 9:16, joined into a silent 1080×1920 output. Keep provider, Vault, YouTube, and callback authorization boundaries.
- Production and Development have separate state and volumes. Keep encrypted credentials and `secret.key` with backups. Preserve bearer callbacks, redirect protection, bounded retries, and stop-before-inference behavior.
- Callback fix `f34c12a` is on `main`. Sujan deactivated Under Attack Mode on 2026-10-06; no callback relay is selected.

## Open questions
1. Sujan: backup frequency, retention, and recovery objectives remain undecided.

## Next steps
1. Review the redesigned screens in a rendered browser at desktop and mobile widths; visual review remains outstanding.
2. Set production `VIDEO_CALLBACK_BASE_URL` to `https://video-automation-production-27b3.up.railway.app`, redeploy through the approved workflow, and verify authenticated callback delivery before generation. This configuration has not been applied. If retaining the custom callback domain, deploy both workers with the merged header fix first.
3. After routing is verified, use a fresh generation; the absent original job cannot be recovered by changing its persisted callback origin. Complete the prior Railway persistence and real YouTube upload smoke checks.

## Gotchas
- Browser automation could not start in the Windows sandbox. Verification covers source, contracts, simulated interactions, and isolated HTTP checks; rendered layout is unverified.
- Production routing was not rechecked during this redesign. Probes on 2026-10-06 reached Go but returned 404 for absent job `gen_954e9ffbe0f220ddddbda41a2edb361f` (reason unknown); authenticated delivery remains unverified. Default Python requests to the custom domain received Cloudflare 403/1010 that day.
- Development uses a different service and volume; never direct production callbacks there. The local Modal profile previously showed Wan only; the SkyReels deployment target remains unverified.
- No paid generation, provider credential access, deployment, or `.env` read occurred. Existing `workers/modal/__pycache__/` remains untracked and untouched.
