# Historical YouTube source import

The download-first YouTube importer is retired in this staged branch. The
former `POST /api/clipping/sources/import` route, native download and probing
code, worker dispatch loop, and bundled extractor runtime are no longer
available. No deployment or production retirement is implied by these local
changes. Follow the gates in [legacy retirement](LEGACY-RETIREMENT.md) before
any separately authorized rollout.

New clipping submissions use a canonical public YouTube video reference for
Gemini analysis. The reference is encrypted at rest; it is not downloaded by
the service, and it does not have a local media file or a verified media hash.
See the [Gemini migration plan](GEMINI-MIGRATION.md) for the replacement's
remaining feasibility and release gates.

Existing legacy sources, media paths, diagnostics, and v2 results remain
readable. Pending imports are marked unavailable without deleting their files
or clearing active import leases. Already-issued worker callbacks and media
capabilities remain available for their bounded drain; source retention and
explicit deletion continue to govern stored files.

The previous importer selected and validated a bounded MP4 using pinned
`yt-dlp`, Deno, and FFmpeg/ffprobe; its code, process tests, runtime lock, and
contract checker remain available in repository history. Its historical local
fixtures did not establish that live YouTube access would work. Those removed
checks and old deployment instructions must not be run as current setup.
FFmpeg and ffprobe remain in the container for unrelated video generation.
