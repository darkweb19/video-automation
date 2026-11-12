# Gemini persistence and compatibility

G2 adds the storage foundations for direct URL analysis. It does not expose a new submission route or activate inference. G3 adds execution and G4 adds the dashboard/API flow. The [migration tracker](TRACKER.md) records actual acceptance and publication state.

## Sources and immutable intent

`youtube_url` is a reference source, separate from a downloaded `youtube_original_file`. It stores a canonical URL encrypted with the existing application key and records rights attestation. Ready means the reference was accepted. It does not prove access, duration or media availability.

Reference rows have SQL NULL for path, declared size, actual size, duration and media SHA-256. Reservations, offsets and import/media leases are zero. Public DTOs omit file-only fields and use a generic display name; they do not expose a URL or reconstructable video ID. Reference cleanup never calls a media-path operation.

The Gemini creator atomically writes the reference when needed, compatibility job records, stage and immutable request snapshot. It also accepts an eligible existing Ready YouTube source with rights and an encrypted URL; subsequent analysis must use that URL without reading or changing local media. Upload, Drive and Dropbox sources are not eligible.

The snapshot contains nonsecret routing, model/API/mode identifiers, prompt/schema versions and digests, exact millisecond options and the complete request fingerprint. It does not duplicate the URL. Reconstructing the request checks these values and fails before inference on drift. Idempotent replays return the original job/source/deadline; all supplied retention deadlines are excluded from the intent hash, so retries retain the first deadline.

Canonical URLs are encrypted. Fingerprints and idempotency digests are deterministic identity checks, not encryption: someone who already knows possible inputs may compare guesses. They must not be described as confidential media identities or independently verified media hashes.

## Database migration and recovery

Existing source/job constraints require a table rebuild. Startup takes a private WAL-consistent SQLite snapshot using `VACUUM INTO`, syncs the snapshot and directory entries, and journals its ownership before allocation. Managed incomplete snapshots are reclaimed on retry; the previous completed snapshot remains until a new durable one replaces it. Unknown files and symlinks are not deleted as managed backups.

Snapshots live in `clipping-migration-backups/` under the application data directory. They contain database state, not the encryption key or media. Preserve `secret.key` and the rest of the application's persistent volume with normal backups. A database snapshot alone cannot restore encrypted credentials or legacy media.

A dedicated connection changes its foreign-key setting outside the transaction, rebuilds both parent tables, restores dependent views/triggers/indexes, checks foreign keys and commits. Failure rolls back the schema/data changes and restores the connection's foreign-key enforcement. Unknown columns fail closed rather than being silently dropped. A successful migration marker prevents repeated rebuilds/backups on restart.

On existing Windows databases, inability to establish a durable backup defers this migration while preserving ordinary app startup; reference/snapshot creation then fails explicitly. Other migration failures remain errors. Windows runtime behavior has not been validated by this Linux test environment.

## Artifacts and cache

Gemini results use `framevault.clipping.analysis.v3` in the existing artifact envelope, with a direct typed JSON payload. Validation preserves candidate order, titles, hooks, scores and exact proposed ranges. Artifact duration is model-reported and is never written back as source metadata. Both artifact ingestion and decoding enforce the initial 40-minute request ceiling. Legacy artifacts retain their original contract.

The cache is keyed by source ID and full request fingerprint. It references an original completed, nondeleted job and validated artifact, with original creation and expiry provenance. Freshness is anchored to that original result, no longer than 24 hours or current source retention. Reindexing/reuse cannot extend the lifetime or make a reused job a fresh origin.

Deletion and retention shortening invalidate affected cached intent. Extending retention or reindexing an older origin cannot revive an invalidated result; a genuinely fresh completion may become a new origin. Cache reuse preserves origin provenance and does not imply a new inference charge. Actual provider usage belongs to the lifecycle records added in G3.

The old queue, claim, worker dispatch, callback, cache, retry and recovery entry points reject Gemini jobs, including Gemini jobs referring to an existing local YouTube file. Provider identity selects the protocol. Reference kind alone is insufficient for isolation.

## Evidence boundary

Acceptance covers empty/populated/restart/rollback migrations, dependent schema objects, foreign keys, private consistent backups, bounded interrupted recovery, reference/file separation, concurrent idempotency, immutable snapshots, exact options, artifact preservation, cache expiry/deletion and legacy provider isolation. These offline checks do not prove Google access or semantic quality. See the [provider contract](GEMINI-PROVIDER.md) and [ADR-005](../architecture/005-gemini-clipping.md).
