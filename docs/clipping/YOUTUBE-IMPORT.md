# YouTube source import

Clipping accepts one public YouTube video link, downloads the actor's saved media to a validated MP4 source, and then makes the ready source available to the existing Modal analysis worker. The public source API is `POST /api/clipping/sources/import` with `{ "url": "…", "rights_attested": true }`. The server accepts a video ID or supported YouTube watch, shorts, embed, live, `/v/`, or `youtu.be` link, validates the ID, and stores only the canonical `https://www.youtube.com/watch?v=…` URL, encrypted at rest. Playlist parameters and non-YouTube providers are rejected.

The importer needs an Apify token and a paid Apify plan. The actor's `saveMedia` option is **Paid plan only**; the Free pricing row does not grant access to that feature. Without valid operator configuration, the app can start normally, but an import fails with a configuration error. The service does not start an actor during construction or startup. Never put the token in source control, logs, browser settings, or a URL. Docker Compose passes it to the service from the host environment. Copy [`.env.example`](../../.env.example) to a private `.env` file and set `APIFY_API_TOKEN` and `APIFY_YOUTUBE_ACTOR_ID=thenetaji~youtube-video-downloader` to enable imports.

There is no upload endpoint in the current source flow, and Drive/Dropbox imports are no longer accepted. Existing Ready sources remain available to existing analysis jobs. On upgrade, queued legacy upload, Drive, and Dropbox imports are marked `legacy_source_import_unsupported`; their files are retained and are not automatically removed.

## Selected actor and evidence

FrameVault is configured for the Apify Store actor `thenetaji/youtube-video-downloader` (API path form `thenetaji~youtube-video-downloader`). `APIFY_YOUTUBE_ACTOR_ID` must be set to that actor using either spelling; operator clones, unknown actors, and arbitrary schemas are rejected. The ID does not pin an immutable actor build; the Store actor can change independently of a FrameVault release. Review the Store's current input, output, limits, and pricing before configuring or running imports.

The exact contract below was checked against the actor's public Store documentation on 2026-10-19. Pricing and actor documentation can change. The publisher's integration source is pinned to commit `760dd49fa8dad652edd091523477fd3dde07f9ea`; that source is useful provenance for its API paths and field handling, but it is not an immutable Apify actor build or a live run.

- [Apify Store actor page](https://apify.com/thenetaji/youtube-video-downloader)
- [Apify input schema](https://apify.com/thenetaji/youtube-video-downloader/input-schema)
- [Apify output schema](https://apify.com/thenetaji/youtube-video-downloader/output-schema)
- [Apify pricing](https://apify.com/thenetaji/youtube-video-downloader/pricing)
- [Publisher integration README at pinned commit](https://github.com/thenetaji/apify-n8n-nodes/blob/760dd49fa8dad652edd091523477fd3dde07f9ea/packages/youtube-downloader/README.md)
- [Publisher API integration source at pinned commit](https://github.com/thenetaji/apify-n8n-nodes/blob/760dd49fa8dad652edd091523477fd3dde07f9ea/packages/youtube-downloader/nodes/YouTubeDownloader/YouTubeDownloader.node.ts)
- [Publisher input and result helpers at pinned commit](https://github.com/thenetaji/apify-n8n-nodes/blob/760dd49fa8dad652edd091523477fd3dde07f9ea/packages/youtube-downloader/nodes/YouTubeDownloader/helpers.ts)

The actor page identifies the actor as community-maintained and advertises downloadable public videos and Shorts. The Store documentation does not publish an immutable build/version pin, a maximum source duration or size, a saved-URL lifetime, or a checksum contract. The app's four-hour and 20 GiB admission caps are FrameVault limits; they do not establish that the actor can process every source up to those limits. No live actor run or real-video import has verified availability or those boundaries.

## Actor request and result contract

For each source the app submits exactly one normalized YouTube URL. The Store schema documents `urls` as an array of URL entries or video IDs, `region` as a two-letter region (default `US`), `saveMedia` as disabled by default, `quality` values `144`, `240`, `360`, `480`, `720`, `1080`, `1440`, `2160`, `4320`, or `best`, and `format` values `default`, `mp4`, `webm`, or `mkv`. FrameVault requests the actor's highest available documented quality (`best`) and MP4 output, with transcripts disabled:

```json
{
  "urls": [{ "url": "https://www.youtube.com/watch?v=VIDEO_ID" }],
  "region": "US",
  "saveMedia": true,
  "quality": "best",
  "format": "mp4",
  "includeTranscript": false
}
```

The API base is `https://api.apify.com`; the service sends the token in an `Authorization: Bearer` header. The publisher integration uses the same API paths with `waitForFinish=30` on both start and poll requests. FrameVault starts with `waitForFinish=0` so it can persist the run ID as soon as Apify acknowledges it, then waits using the run endpoint:

1. `POST /v2/acts/thenetaji~youtube-video-downloader/runs?waitForFinish=0` with the input above.
2. Poll `GET /v2/actor-runs/{runId}?waitForFinish=30` until the run is terminal. The publisher integration treats `SUCCEEDED`, `FAILED`, `ABORTED`, and `TIMED-OUT` as terminal run statuses; FrameVault accepts `SUCCEEDED` for dataset retrieval and fails closed on `FAILED`, `ABORTED`, or `TIMED-OUT`.
3. Read `GET /v2/datasets/{datasetId}/items?format=json&clean=true` for the completed run's items.

The actor run's active statuses include `READY` and `RUNNING`; `SUCCEEDED`, `FAILED`, `ABORTED`, and `TIMED-OUT` are terminal. FrameVault retrieves the dataset only for `SUCCEEDED`. It fails closed for actor failures or unsupported statuses. If polling reaches its persisted deadline, it issues a best-effort `POST /v2/actor-runs/{runId}/abort`. A definitive rejected actor start can be retried through the source import lifecycle; if start may have been accepted but its run ID was not received, that state is persisted and not automatically submitted again.

An output item may include the video `id`, canonical `url`, `durationSeconds`, `isLive`, and `savedFile`. The documented `savedFile` object contains `key`, `url`, `contentType`, `container`, `qualityLabel`, `width`, `height`, `bytes`, `sizeMb`, and `billedMb`. The Store schema marks output fields as optional; the app requires a stored media URL before admitting the source. If `id` or `url` is present, it must identify the requested video. If the actor omits `savedFile` or its URL, or returns an identifiable item for another video, the import fails closed. When `savedFile.bytes` is present, the app checks it against the downloaded byte count; the actor does not document a checksum. The publisher integration mentions `formats[].directUrl` as a fallback, but those links are temporary; FrameVault does not use them.

The actor documents combined playable video/audio output, but does not promise selection or preservation of a particular original-language audio track. FrameVault downloads the stored file unchanged, does not strip or transcode its audio, and probes that the media has supported video and audio streams. No real fixture has established that the actor preserves the source's original language track.

## Pricing and limits

The actor's Store pricing page, checked 2026-10-19, lists these variable prices. Apify may change rates, and the plan shown by the operator may differ. **The paid-plan `saveMedia` entitlement is required even though the Store page also lists a Free price.**

| Actor event | Free | Starter | Scale | Business | Billing unit |
| --- | ---: | ---: | ---: | ---: | --- |
| Video primary | $10 | $9 | $8 | $7 | Per 1,000 videos |
| Media downloaded | $2 | $1.80 | $1.60 | $1.40 | Per 1,000 billed MB |
| Transcript | $2 | $1.80 | $1.60 | $1.40 | Per 1,000 transcripts |
| Infrequent actor start | $0.00005 | $0.00005 | $0.00005 | $0.00005 | Per actor start |

Media billing rounds the billed megabyte amount up and is charged after the stored file is available. FrameVault disables transcripts, but actor execution, downloaded media size, and starts can still incur charges. These prices are not an estimate for a particular source or a record of actual spend. Check the live [actor pricing page](https://apify.com/thenetaji/youtube-video-downloader/pricing) and the account's paid-plan entitlement before enabling imports.

FrameVault retains its existing local admission limits: one source, four hours, and 20 GiB. It reserves the 20 GiB source allowance, preserves the aggregate reservation and 4 GiB free-space floor, and accounts for the growing file during transfer. New v3 imports stream one MP4 directly to the private `source.media.part` path; they do not create a second merge/remux workspace. The older bounded scratch reservation remains only for reconciling interrupted v2 imports. These app limits do not imply actor capability: the actor's public docs do not specify a maximum duration or source size, so it may fail below FrameVault's cap.

`APIFY_REQUEST_TIMEOUT` is the per-request timeout for Apify control-plane calls. It accepts a Go duration, defaults to `60s`, and must be positive and no longer than `5m`. `APIFY_MAX_POLL_DURATION` is the persisted deadline for a started actor run; it defaults to `4h` and must be between `1m` and `12h`. FrameVault sends that same remaining duration as Apify's Run Actor API `timeout` parameter and polls with `waitForFinish=30`. This bounds remote execution after a start response is lost; it does not guarantee the actor can handle a source of that length or size and is not a monetary cost cap. The media body transfer does not use the API-request timeout: it is bounded by the import's cancellation and existing maximum 12-hour import context, and by the 20 GiB byte cap. There is no separate media-transfer timeout setting.

## Security and media lifecycle

Only the fixed Apify API and actor-provided saved-file URL are used. The token is sent as an authorization secret and is not returned to the browser or written to logs. Actor IDs are operator configuration, not caller input. The saved-file URL and redirects must use HTTPS on port 443 and resolve to public unicast addresses; the importer rejects IP-literal hosts and limits redirects. It streams media directly to the private temporary source file instead of buffering video in memory, enforces its byte and cancellation limits, and removes partial output after failure, cancellation, or retry. It does not follow arbitrary caller-provided destinations.

The downloaded media must pass ffprobe checks for a supported MP4 container, video and audio streams, supported dimensions, finite duration within four hours, and compatible timing. Both stream durations must agree with each other and with the container duration within the configured tolerance. The importer syncs and probes the staged file, verifies the active import attempt and lease, hashes the media, records its final expectation, and atomically renames it to the Ready path. The Modal analysis worker continues to receive only a Ready source and its existing sanitized media contract.

Cancellation and ambiguous actor-start outcomes are persisted with the source import attempt. An uncertain start is not automatically repeated, because Apify may have accepted the paid run before the response was lost. Actor polling and dataset lookup use bounded API requests; the media body is governed by the enclosing import context and source-size cap, not the API-request timeout. Sanitized source traces use `actor_start`, `actor_poll`, `media_download`, `media_probe`, `download_output_validation`, `media_hash`, and `source_ready` stages.

### Manually checking an uncertain actor start

An import can be marked `apify_start_uncertain` when the start request may have reached Apify but FrameVault did not receive a usable run ID. There is no operator UI for Apify run IDs. The app keeps the correlation in its private `/data/app.db` table `clipping_apify_runs`; it is not part of the source API and contains no Apify token or temporary media URL. Use a read-only SQLite connection to inspect the row for the source:

```sql
SELECT source_id, actor_id, import_attempt,
       datetime(start_intent_at, 'unixepoch') AS start_time_utc,
       datetime(deadline_at, 'unixepoch') AS poll_deadline_utc,
       run_id, start_state
FROM clipping_apify_runs
WHERE source_id = :source_id;
```

For `start_state = 'start_unknown'`, `run_id` is blank by design. Use the actor ID and start time to inspect nearby runs in the Apify Console, and compare the run input and outcome with the submitted source. Check whether a run is still active, completed, failed, or aborted, and review its usage/cost before deciding what to do. FrameVault cannot attach a run found manually in the console. Do not edit the database row or resend the uncertain start. If the operator explicitly submits a new import after reviewing the old run, it may create a second paid actor run; check prior run status and costs first.

## Diagnostics and verification

Import errors expose safe stage and reason codes only. They must not include the source URL, Apify token, actor response bodies, signed media URLs, or raw HTTP errors. A missing token or incompatible actor configuration should identify the required operator setting. A successful actor run without a usable `savedFile.url`, a mismatched returned video identity, failed media transfer, timeout, oversized body, or failed media probe leaves the source out of Ready.

Run the hermetic importer contract and backend tests with:

```sh
go test ./internal/app
```

These checks use local/fake actor responses and local media; they do not start a paid Apify actor or make a live YouTube request. Live verification requires an operator-configured paid Apify plan and a real public video. This branch has not completed that verification, so successful live import, actor capacity, current billing, and original-language audio preservation remain unverified.
