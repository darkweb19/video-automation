# Session handoff — 2026-09-20

## What was done
- Added `go run . recovery-code` and the Docker equivalent to generate a one-time password recovery code.
- Added hashed recovery-code storage with 15-minute expiry, replacement invalidation, atomic consumption, and session invalidation.
- Added a rate-limited unauthenticated password-recovery endpoint with generic invalid/expired errors.
- Added a minimal Forgot password flow inside the existing white login card, including password confirmation.
- Documented local and Docker recovery commands in README.md.
- Added tests for successful recovery, expiry, one-time use, session invalidation, new-password login, and code formatting.
- Included the generation-form clearing and custom provider-aware model picker in the shipped feature commit.
- Passed `node --check static/app.js`, `gofmt`, `go test ./...`, `git diff --check`, and a real CLI smoke test.
- Committed as `5f748f4 feat: add secure password recovery` and pushed through `origin/main`.
- Fixed the unresponsive Forgot password button caused by potentially mixed cached frontend assets: static files now use a version query and `Cache-Control: no-store`.
- Added coverage confirming versioned static URLs resolve and return the no-store policy; pushed as `c3f232c fix: prevent stale frontend assets`.

## Decisions locked
- Recovery uses terminal-generated codes instead of email/SMTP.
- Codes expire after 15 minutes, work once, and are stored only as SHA-256 hashes.
- Generating a replacement invalidates the prior code; resetting a password invalidates all sessions.
- The recovery UI stays inside the existing login card with no new dependency.

## Open questions
1. Sujan: visually confirm the recovery form and custom model picker; no browser backend was available for visual QA.
2. Sujan: decide later whether official provider SVG logos should replace the current branded initial marks.

## Next steps
1. Rebuild/recreate the Docker app from current `main`, then hard-refresh the login page once.
2. Confirm Forgot password opens the recovery form.
3. Generate a recovery code, reset the password, and confirm the used code cannot be reused.

## Gotchas
- Docker command: `docker compose exec video-automation /app/video-automation recovery-code`.
- Local command: `go run . recovery-code`; it must use the same `DATA_DIR` as the server.
- Use workspace-local `GOCACHE=.gocache` and `GOMODCACHE=.gomodcache` for Go checks on this machine.
- The working tree was clean after the feature push; browser-based visual QA remains unavailable in this environment.
- The hotfix only takes effect after the deployed container is rebuilt from commit `c3f232c` or newer.
