# Railway persistence

**Status:** Accepted; local implementation complete
**Scope:** Preserve FrameVault accounts, settings, job history, and generated media across Railway redeploys.

## Problem and evidence

Railway redeploys currently lose the app's password, settings, history, and media. The user reports that the Railway Development service has no persistent volume and confirms there is no Development data to preserve; live deployment state was not independently inspected. The repository uses SQLite and local files beneath `DATA_DIR`; its documented layout is a single `/data` volume. `Run` defaults to relative `data`, and `OpenStore` creates directories and opens `app.db` before serving. The first-run user insert is `ON CONFLICT DO NOTHING`, so a redeploy against the same database should not overwrite an existing password. Missing persistent storage explains the reported reset behavior.

## Decision

Keep the existing Go service, SQLite database, and local media architecture. Attach one persistent volume to the Railway Development service at `/data`; set `DATA_DIR=/data`; keep `app.db`, `secret.key`, `videos/`, and `projects/` together. Keep one app replica per volume. Do not add an external database, object storage, or another persistence service. Keep Development and Production on separate volumes.

Before any storage initialization or bootstrap writes, detect Railway runtime metadata and validate the configured data path against Railway's supplied volume mount path. Resolve paths and reject a `DATA_DIR` outside the actual mounted volume. Fail startup clearly on absent, mismatched, or unsafe mount metadata. Do not infer a mount merely because `/data` exists or hardcode a Railway service name.

The container entrypoint validates the resolved storage path with the read-only `storage-path` command before creating or changing anything. As root, it initializes only the two required subdirectories and narrowly adjusts ownership of the validated root and known database/key files; it does not recursively traverse media on each restart. It then drops privileges with [`su-exec`](https://github.com/ncopa/su-exec), a small purpose-built privilege-drop utility. The Go process runs as the unprivileged `app` user.

Use Railway's existing dashboard service settings for the root Dockerfile build, port 8080, health check, environment, and volume attachment. Railway documents service volumes in its [volume guide](https://docs.railway.com/volumes). Do not add deprecated `railway.toml`/`railway.json` files or migrate infrastructure-as-code for this fix. Railway supplies mount metadata; do not set an invented mount-path variable.

## Assumptions and operational targets

- One service instance owns the SQLite database and media. Credentials and callback tokens are encrypted in the database using `secret.key`.
- No measured workload forecast, latency SLO, or production availability target was supplied. Development availability is best effort; owner: Sujan.
- With the same intact volume, normal redeploys should retain committed persisted state. This does not cover volume loss or corruption; backup cadence and disaster recovery RPO/RTO remain unset.

## Risks

Mounting an empty or different volume appears as a fresh install. Losing `secret.key` while retaining `app.db` makes encrypted settings and tokens unreadable; back up both together. Incorrect ownership can prevent startup or writes. Verify the live volume configuration during rollout. A single SQLite-owning replica limits horizontal scaling and matches the current architecture.

## Rollout

1. Push the approved changes on `feat/youtube-upload-and-generation-reliability`.
2. In Railway Development, attach a new persistent volume at `/data` and set `DATA_DIR=/data`; keep Production on its separate existing volume. Leave `RAILWAY_RUN_UID` unset so the entrypoint can initialize ownership before dropping privileges.
3. Deploy and verify login, password change, Settings, history, media, and health. Verify these persist across a subsequent redeploy. No Development data backup/restore is needed per the user's confirmation; Production data is not copied or shared.

## Acceptance checks

- Startup performs no storage/bootstrap writes until service and mount checks pass; invalid or absent metadata fails closed with a useful non-secret error.
- Restart/redeploy with the same volume retains password, encrypted settings, history, and media. A new volume is explicitly treated as empty state.
- The app process runs as `app`; ownership initialization avoids recursive media traversal and secret/database permissions remain restrictive.
- Backup and restore keep `app.db` and `secret.key` together and include scene clips and completed videos; restore checks cover decryption and media availability.
- Local Go tests, dashboard UI tests, JavaScript syntax, callback delivery, entrypoint syntax, Linux CGO-disabled build, and diff checks passed; independent review found no blocking issues. Two symlink tests were skipped on Windows due to platform privileges. Docker image/runtime checks and live Railway checks remain unavailable and must be completed after deployment. No paid generations were run.

## Open operations decision

Set backup frequency, retention, and disaster recovery objectives after reviewing storage size and acceptable data loss.
