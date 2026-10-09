# M2: integrated sources, jobs and batches

M2 was authorized on 2026-10-08 with remaining M1 feasibility explicitly waived. Existing M1 documents, schemas, synthetic fixtures and tools remain historical offline evidence; the waiver does not establish live acquisition, quality or cost feasibility. M2 extends the existing authenticated Go application and embedded dashboard. Deployment is separate.

## User workflow and boundaries

Open **Clipping** after signing in. Confirm permission to process the footage, then upload a video or submit a public Drive/Dropbox link. Uploads use bounded chunks and persisted offsets; after interruption, select the exact original file and resume its existing source. Resume checks filename and byte length, not content equivalence; a different file with the same name/size can create mixed bytes. Final hashing records the stored bytes and media probing validates the result, but neither proves it equals the original selection. A source becomes ready only after its stored media passes validation. Source preparation does not start paid analysis.

YouTube links are references only: the application returns an explicit original-file upload fallback. Drive viewer/login/confirmation pages also require an original file or a public content URL that returns the video itself. No watch-page scraping, player extraction, private-account connector or undocumented Drive download URL synthesis is implemented. Dropbox recognized shared-file links use `dl=1`; redirects remain within the provider family. Live provider behavior remains unverified.

Select ready sources and submit a job or batch with explicit per-job and aggregate US-dollar limits. The backend stores integer microUSD (`1 USD = 1,000,000 microUSD`). A batch admission key makes duplicate submissions return the existing batch; reusing a key for different input is rejected. Expected stage costs must be reserved against both limits before any future dispatch. Actual reported spend is settled once; if actual charges exceed a reservation, retain the reported amount and block further work rather than disguise it.

M2 has no selected analysis provider or deployed worker. Its configuration API reports `worker_available: false`; jobs remain queued. The worker protocol and injected local test client exercise dispatch/result handling without paid calls. Transcription, timeline understanding, candidate selection, captions, framing, editing and exports belong to M3/M4 and are not implemented here.

## Persistence, resources and recovery

Clipping uses additive tables separate from generations/projects: sources, batches, jobs, stages, attempts and versioned artifacts. Existing generation, Vault and YouTube schemas retain their workflows. Media stays under an application-generated directory beneath the same persistent data volume; no object storage or remote database is introduced.

M2 admission limits are 20 GiB per source, 100 GiB total source reservations, four hours of actual duration, and 100 jobs per batch. These are bounds, not measured capacity promises; many four-hour files may exceed the byte limit. Unknown import lengths reserve the full source cap. Physical disk headroom is checked independently of the logical reservations. Uploads count real bytes and use a maximum 16 MiB chunk; whole-file buffering is excluded. Stored files are synchronized and atomically published, and exact-byte SHA-256 is recorded. `ffprobe` reads local stored files only, with bounded execution/output, to validate actual video rather than trusting extensions or Content-Type.

Source retention defaults to 30 days and is configurable from 1–365 days. Active jobs and media leases protect their sources. Explicit cancellation prevents later upload/import completion from resurrecting a removed source; tombstones allow interrupted cleanup to resume. Audit/budget records remain durable. Retention governs source media and associated analysis artifacts; it is not a backup schedule.

The processor runs at most one import at a time, recovers interrupted import leases with bounded retries, and schedules cleanup periodically. A paid stage whose lease expires has an uncertain outcome: preserve its reservation and pause the job. Do not start the same work again until the previous attempt is reconciled. A canceled job never advances, but authenticated late results can settle actual spend once. Unresolved attempts remain paused; M2 does not guess their cost or silently release their funds.

## API and worker contract

All browser routes below require the existing authenticated session and completed password change. Mutations require same-origin requests. The authenticated EventSource emits `clipping_source`, `clipping_job` and `clipping_batch`; clients refresh authoritative lists on reconnect.

| Resource | Routes |
| --- | --- |
| Configuration | `GET /api/clipping/config`; `PUT /api/clipping/config/retention` |
| Sources | `GET /api/clipping/sources`; `GET/DELETE /api/clipping/sources/{id}` |
| Upload | `POST /api/clipping/sources/upload`; `PUT /api/clipping/sources/{id}/upload` with `Upload-Offset`; `POST /api/clipping/sources/{id}/finalize` |
| Import | `POST /api/clipping/sources/import` |
| Browser playback | `GET /api/clipping/sources/{id}/media` |
| Jobs | `GET/POST /api/clipping/jobs`; `GET /api/clipping/jobs/{id}`; `POST .../{id}/cancel`; `POST .../{id}/retry` |
| Batches | `GET/POST /api/clipping/batches`; `GET /api/clipping/batches/{id}`; `POST .../{id}/cancel` |
| Scoped worker media | `GET /api/clipping/worker-media/{jobID}/{attemptID}` |
| Scoped worker result | `POST /api/clipping/worker-callbacks/{jobID}/{attemptID}` |

Worker transport version is `framevault.clipping.v1`, initially with the `analysis` stage. A stable dispatch ID identifies the job/stage; each attempt has a distinct ID and bounded lease. Capability tokens are encrypted using the application's existing security key and passed only in bearer headers, never in URLs or browser responses. Media access stops on cancellation/lease expiry; a bounded accounting grace permits late settlement without artifact publication. Workers use their own durable idempotency ledger and never mount the application's SQLite database or media volume. A restart with a running entry is uncertain, not permission to repeat inference. There is no five-second remote polling or automatic dispatch to an unconfigured provider.

Artifacts use a versioned envelope with integer source-time milliseconds, source-duration validation, bounded ranges and a 4 MiB payload bound. They are untrusted data, not instructions. The M2 source lifecycle API is distinct from the frozen M1 `ai-clip.source.v1` success/evaluation schema, whose provisional 16 GiB bound and normalized stream details remain unchanged. Future M3 adapters must validate their transcript/candidate payloads against the relevant schemas; passing the M2 envelope does not itself validate a transcription model's claims.

## Operational notes

Source acquisition targets the Unix/Linux runtime used by the existing Docker deployment. Native Windows builds remain supported for existing workflows, but clipping upload/import/finalization fails closed with a Docker/Linux message. The Windows build is cross-compiled in verification, not executed.

Keep SQLite, media and `secret.key` together when backing up the persistent volume. Source URL capabilities and worker tokens depend on that key. Do not run older code that downgrades or discards the new tables. Removing the application feature does not migrate user data backward. Review disk sizing and permitted real inputs before live imports; configuration of a paid worker/provider and deployment need their own authorized scope.

Verification and independent-review results are recorded in [TRACKER.md](TRACKER.md), not inferred from this description. No benchmark or deployment result is claimed here.

## C2.4 authorized YouTube acquisition extension — 2026-10-08 (client local; 2026-10-09 UTC)

After this M2 snapshot was written, the user authorized public single-video YouTube link import as a bounded extension. The upload-fallback paragraph above records the prior M2 behavior; C2.4 updates that behavior only for canonical single-video YouTube references. It uses a pinned extractor and JavaScript challenge runtime behind an allowlisted DNS-pinned HTTPS proxy, then streams one muxed audio/video format through the existing Go importer. Rights attestation remains mandatory; cookies, credentials, account/login or age-gate/DRM bypass, playlist/live media, caller extractor options, and arbitrary destinations remain disallowed. See [YOUTUBE-IMPORT.md](YOUTUBE-IMPORT.md) for URL, network, size, duration, error, and verification requirements. This is best-effort support with upload fallback, not a universal availability claim; historical M1 artifacts remain unchanged.
