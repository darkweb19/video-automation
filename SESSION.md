# Session handoff - 2026-09-30

## What was done
- Published the ordered feature merge as `4ae89dcfd6b0441bc283991d4323c56069ebcba6` (parents `af62ad5` and `44e0835`) to `origin/feat/modal-callbacks-live-updates-cost-controls`.
- Published the main integration as `1e064ac699598ac99057729ceab5bde495529f8f` (parents `a25d621` and `4ae89dc`) to `origin/main`. Parent verified remote ancestry; local `main` matches the published merge.
- Preserved SkyReels callback compatibility and durability/retry protections. The media-cache fixture uses a representable +1 ms timestamp delta within the same second for Windows.
- Updated dashboard UI test asset paths and the frontend redesign guide. Restored `docs/SESSION.md` from `origin/main` to retain its historical handoff.
- All checks passed on the exact published trees: `go test ./...` (11.888s), `go vet ./...`, `go build`, JS syntax, all 11 dashboard UI tests (187 ms), legacy callback plus 15 offline SkyReels tests (2.688s), and `git diff --check`.
- Isolated HTTP smoke passed for embedded assets, auth/password handling, safe settings, History, SSE, and Vault unlock/lock/logout. Temporary server and data were removed. Commit metadata was corrected for GitHub privacy; tested trees and parent order are unchanged.

## Decisions locked
- Use authenticated same-origin SSE for live dashboard updates and authenticated callbacks with sparse recovery for Modal completions.
- Keep callback capabilities out of browser responses and logs; the worker retains callback material in its private durable job record for recovery.
- Keep model capabilities/pricing runtime-driven and preserve the five six-second 480p 9:16 project preset.
- Retain the monochrome visual system, keyboard navigation, focus indicators, stale-snapshot protection, and light notifications.

## Open questions
1. Fresh desktop/mobile browser visual review remains outstanding. No deployment or paid generation was run.

## Next steps
1. Do a desktop/mobile browser visual review when browser access is available.
2. Run preflight before any future deployment or paid generation.

## Gotchas
- Dashboard assets and the JS syntax check live under `internal/webui/static/`; UI tests resolve assets from there.
- Modal callbacks need `PUBLIC_BASE_URL` set to the externally reachable HTTPS dashboard origin. Callback tokens are bearer material stored privately for recovery.
- Preserve restart-safe SQLite data and `/data/secret.key`; never read or print `.env` contents or credentials.
- Leave the user's untracked `modal/__pycache__`, the pre-existing conflicted `.worktrees/apple-main-merge`, backup refs, and other worktrees untouched.
