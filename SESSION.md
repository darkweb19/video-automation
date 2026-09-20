# Session handoff — 2026-09-20

## What was done
- Added `go run . recovery-code` and the Docker equivalent to generate a one-time password recovery code.
- Added hashed recovery-code storage with 15-minute expiry, replacement invalidation, atomic consumption, and session invalidation.
- Added a rate-limited unauthenticated password-recovery endpoint with generic invalid/expired errors.
- Added a minimal Forgot password flow inside the existing white login card, including password confirmation.
- Documented local and Docker recovery commands in README.md.
- Added tests for successful recovery, expiry, one-time use, session invalidation, new-password login, and code formatting.
- Previously added generation-form clearing and the custom provider-aware model picker remain in the uncommitted working tree.
- Passed `node --check static/app.js`, `gofmt`, `go test ./...`, `git diff --check`, and a real CLI smoke test.

## Decisions locked
- Recovery uses terminal-generated codes instead of email/SMTP.
- Codes expire after 15 minutes, work once, and are stored only as SHA-256 hashes.
- Generating a replacement invalidates the prior code; resetting a password invalidates all sessions.
- The recovery UI stays inside the existing login card with no new dependency.

## Open questions
1. Sujan: visually confirm the recovery form and custom model picker; no browser backend was available for visual QA.
2. Sujan: decide later whether official provider SVG logos should replace the current branded initial marks.

## Next steps
1. Rebuild/restart the app so the new command, migration, endpoint, and UI are active.
2. Generate a recovery code and reset the forgotten password from the Forgot password form.
3. Sign in with the new password and confirm the used code cannot be reused.

## Gotchas
- Docker command: `docker compose exec video-automation /app/video-automation recovery-code`.
- Local command: `go run . recovery-code`; it must use the same `DATA_DIR` as the server.
- Use workspace-local `GOCACHE=.gocache` and `GOMODCACHE=.gomodcache` for Go checks on this machine.
- Feature changes are uncommitted; only handoff updates are committed separately.
