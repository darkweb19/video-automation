# YouTube source import

Clipping accepts one public YouTube video link, downloads it to a validated MP4, and then sends the ready source through the existing Modal analysis worker. The public source API is `POST /api/clipping/sources/import` with `{ "url": "…", "rights_attested": true }`. Inputs include a video ID, a YouTube `watch`, `shorts`, `embed`, `live`, or `/v/` link, or a `youtu.be` link. The server validates the ID and stores only the canonical `https://www.youtube.com/watch?v=…` URL, encrypted at rest. Playlist parameters and non-YouTube providers are rejected.

There is no upload endpoint in the current source flow, and Drive/Dropbox imports are no longer accepted. Existing Ready sources remain available to existing analysis jobs. On upgrade, queued legacy upload, Drive, and Dropbox imports are marked `legacy_source_import_unsupported`; their files are retained and are not automatically removed.

## Downloader and media contract

The runtime uses the SHA-256 pinned `yt-dlp` and `yt-dlp-ejs` wheels in [requirements-youtube.lock](../../tools/clipping/requirements-youtube.lock), a pinned Alpine Deno package for yt-dlp's YouTube JavaScript challenge support, and the installed FFmpeg/ffprobe binaries. `yt-dlp` selects its native best-video/original-audio formats. The application does not parse extractor format metadata or choose track IDs. It asks yt-dlp to merge to MP4 and remux the fixed output path `source.mp4`; the application then probes the actual container and streams before admitting the source.

The downloader runs with a fixed argument list, no user configuration, plugins, cookies, browser profiles, login, or shared cache, and a private temporary Deno cache. It accepts only public, non-live videos with a known duration of at most four hours. The downloaded source is capped at 20 GiB. yt-dlp and FFmpeg intermediates share a bounded 40 GiB workspace, with a 4 GiB physical free-space floor and the existing 100 GiB source-reservation ceiling. The workspace is limited to 4,096 entries. Per-file size limits and a workspace monitor stop oversized work; the importer removes its generated partial files on failure, cancellation, and retry.

The process receives the configured HTTP(S) proxy and certificate environment when present, because the service runtime may require them to reach YouTube. The application has no YouTube proxy server or custom proxy selector and does not bypass the environment's network policy. The downloader's stderr is discarded, stdout is bounded to fixed status markers, and source URLs, signed media URLs, extractor output, and titles are not written to diagnostics.

After download, ffprobe must identify an MP4 container with video and audio streams, supported dimensions, a finite duration within the same four-hour limit, and compatible timing. The pinned runtime contract separately checks yt-dlp's native preference for the original audio format. The importer syncs the staged file and directory, completes workspace cleanup, verifies the import lease and source reservation, hashes the media, and atomically moves it to Ready. The Modal analysis worker continues to receive only a Ready source and its existing sanitized media contract.

Cancellation stops the downloader and its process group before the source becomes Ready. Imports have a bounded process lifetime and use the durable lease/retry path; an expired attempt is cleaned and restarted instead of resuming with stale temporary state. Failures tell the user to check that the link is public, single-video, and within the importer limits. Runtime setup failures point to the service operator.

## Diagnostics and verification

The safe source trace uses `source_url_validation`, `source_reservation`, `import_dispatch`, `media_download`, `media_probe`, `download_output_validation`, `media_hash`, and `source_ready`. The authenticated source API retains bounded stage/status/reason codes; the UI displays sanitized errors. Operational logs should not include input URLs or downloader output.

Run the hermetic contract with:

```sh
python3 tools/clipping/check_youtube_runtime_contract.py
```

It checks the pinned package versions and hashes, the Docker runtime contract, and yt-dlp's native selection against a synthetic local InfoJSON fixture. It also serves generated video and audio tracks over a local HTTP server and verifies yt-dlp's actual download, merge/remux, fixed output path, and MP4/audio result with ffprobe. Go's `internal/app` tests use a fake yt-dlp executable with real local FFmpeg/ffprobe media to validate arguments, actual MP4 output, audio checks, size limits, cancellation, cleanup, and atomic Ready publication. These checks require no live YouTube request or paid inference.

Live YouTube verification remains subject to the deployment network policy. In the development environment used for this rebuild, the permitted outbound proxy returned HTTP CONNECT 403 for YouTube, so a live import could not confirm the upstream failure mode. The exact cause of earlier link failures therefore remains unproven; the hermetic checks validate the new importer contract without claiming otherwise.
