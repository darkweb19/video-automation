# Session handoff — 2026-10-06

## What was done
- Implemented Railway runtime volume/path validation before storage initialization, including read-only `storage-path` for the root container entrypoint.
- Added a root entrypoint that validates first, initializes only known `/data` paths, sets restrictive ownership/modes without recursive media traversal, then drops to `app` with `su-exec`.
- Updated Dockerfile, LF `.gitattributes`, Railway README setup/recovery guidance, and [Railway persistence decision](docs/architecture/003-railway-persistence.md).
- Implemented on `feat/youtube-upload-and-generation-reliability`; user authorized push and will attach the Development volume.

## Decisions locked
- Keep Go + SQLite and all state/media under one service-scoped `/data` volume; no external database/object storage or Railway config-as-code.
- Development gets its own new volume; do not share Production's existing volume. User confirms no Development state needs preserving.
- Startup fails closed without valid Railway volume metadata. Set `DATA_DIR=/data`; leave `RAILWAY_RUN_UID` unset so root initialization can run before the app drops privileges.
- A newly attached empty Development volume needs first-run password and provider setup. Same-volume redeploys retain password, Settings, history, and media.
- Preserve existing generation and idempotent project behavior. No paid generations; Railway deployment was not inspected.

## Open questions
1. Sujan: choose backup frequency, retention, and disaster recovery objectives.

## Next steps
1. Attach a new persistent volume to Railway Development at `/data` and set `DATA_DIR=/data`; keep Production separate.
2. Deploy and verify login, Settings, history, media, `/health`, and persistence across a later redeploy.

## Gotchas
- Verification passed: `go test ./...`, 43/43 dashboard UI tests, JavaScript syntax, Python callback delivery, Git Bash `-n`, Linux CGO-disabled deployment build, and `git diff --check`; independent review found no blockers.
- Windows skipped two symlink tests due to platform privileges. Docker build/runtime and live Railway checks remain unavailable; complete those after deployment.
- Railway volume handling is documented in [the architecture decision](docs/architecture/003-railway-persistence.md). Back up SQLite and `secret.key` together; include WAL sidecars and media in a stopped-app copy.
- `workers/modal/__pycache__/` is pre-existing untracked data; leave it untouched. Never read `.env` or expose credentials.
