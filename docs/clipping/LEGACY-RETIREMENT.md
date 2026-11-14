# Legacy clipping retirement

This is the staged retirement procedure for the download-first Modal clipping
pipeline. The code in a feature branch and offline tests do not establish that
the replacement is usable in production or that existing work has drained. No
production deployment or retirement has been performed by this work.

## Release gates

- Finish and review the Gemini implementation, API, dashboard, evaluation and
  release preparation in the [migration plan](GEMINI-MIGRATION.md). A live
  release also needs separately authorized deployment, working identity and
  provider access, measured source availability, quality, language coverage,
  latency and cost. Mocks cannot satisfy these gates.
- Stop accepting new legacy imports, analysis submissions, retries and worker
  dispatch before observing the drain. Until those entry points are fenced, an
  inventory is only a snapshot and can become stale immediately.
- Keep the existing Modal clipping worker, authenticated v2 callback and media
  endpoints, capability-expiry index, claim and recovery records, and legacy
  artifact settlement available during the drain. Do not replay an ambiguous
  worker submission or force an unknown outcome to failure to accelerate it.
  A terminal or canceled job can still have a valid callback capability; a
  late callback must settle without restoring deleted or superseded results.
- Confirm that imports and callback windows have ended, and resolve any
  remaining claims or inconsistent records through the existing recovery and
  an explicit operator investigation. A successful drain and the decision to
  retire production components are separate live gates.
- Use the normal persistent-volume backup and recovery procedure before any
  later authorized deployment. Preserve existing source media, playback paths,
  historical candidate pools and results under their existing retention and
  user deletion rules. Do not introduce an indefinite retention policy.

## Persisted drain inventory

`Store.LegacyClippingDrainInventory` reads a consistent SQLite snapshot and
returns aggregate counts, without reading capability values or modifying
state. The authenticated `GET /api/clipping/legacy-drain` operator endpoint
returns this aggregate snapshot without changing state. The inventory has these
fields:

| Field | Meaning |
| --- | --- |
| `observed_at` | Server time, in Unix seconds, used for this snapshot. |
| `pending_imports` | Legacy file sources still marked importing, including queued work. |
| `active_import_leases` | Legacy file sources with an unexpired import lease, including deleted or failed sources. |
| `unexpired_callback_windows` | Legacy attempts whose issued callback expiry is in the future, or whose conservative pre-index bound has not elapsed. Terminal, canceled, settled and already-callbacked attempts are included. |
| `unknown_callback_expiry` | Attempts with an invalid indexed expiry, or an absent index and an invalid or overflowing lease. |
| `unresolved_claims` | All remaining legacy source-analysis claim rows, regardless of job status. |
| `inconsistent_capability_rows` | Orphaned attempts, expiry-index rows, or callback setting keys that cannot be matched to a legacy attempt. |

The issued expiry index is authoritative when present. For attempts predating
that index, the inventory uses the attempt lease plus the full 24-hour callback
grace; a subsequently shortened source-retention deadline cannot shorten an
already-issued callback window. The fallback can intentionally wait longer
than the original capability. Unknown or inconsistent records block the drain
check rather than being treated as expired. A database error also prevents a
usable inventory.

Expired bearer settings may be removed once the issued deadline has passed.
The nonsecret expiry index remains while recovery needs it or while the
pre-index lease-plus-grace fallback is unknown or still open. This preserves
proof of an issued deadline for terminal as well as running jobs. Cleanup pages
past malformed records while leaving them unresolved in the inventory; one
unknown source does not block unrelated eligible retention. Database and
filesystem failures still surface. Explicit source deletion cancels its jobs.

All counts must be zero for this persisted-state check to pass after legacy
submission has stopped. Zero does not prove that an in-memory import, remote
worker, recent deployment or other external activity has stopped; confirm those
separately. The inventory is not a cleanup command and never cancels work,
deletes data, releases claims or changes retention.

## Compatibility that remains

This staged branch has removed the native clipping downloader and its probing
processes. The service continues to read legacy sources, results and v2
candidate pools.
The Go legacy artifact builder and authenticated v2 callback settlement remain
only to finish already-issued Modal work; they are never used to choose or
rerank Gemini candidates. The Wan and SkyReels generation workers, generation
callbacks, FFmpeg assembly, Vault and YouTube upload are unrelated to clipping
worker retirement and remain supported. See [ADR-005](../architecture/005-gemini-clipping.md).
