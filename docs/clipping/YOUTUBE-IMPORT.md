# C2.4: public YouTube video-link import

**Status:** authorized for implementation on 2026-10-08 (client local date; 2026-10-09 UTC). No live provider behavior or universal availability is claimed.

This contract extends the M2 public-link importer after the user explicitly authorized YouTube links. The historical M1 acquisition artifacts remain unchanged. M3 transcription/selection, paid analysis, deployment, and publishing are outside this task.

## Accepted input and user flow

- Require the existing explicit `rights_attested` confirmation before creating an import. This records the user's attestation; it is not proof of ownership or a decision by YouTube.
- Accept one public, watchable video from `youtube.com` / `www.youtube.com` / `m.youtube.com`, `youtube-nocookie.com` / `www.youtube-nocookie.com`, or `youtu.be`, over HTTPS on port 443. Supported paths are a standard `watch?v=...`, `/shorts/{id}`, `/embed/{id}`, and the matching one-segment `youtu.be/{id}` form. The video ID is exactly 11 ASCII letters, digits, `_`, or `-`.
- Strip nonessential query fields such as tracking/share parameters from the stored canonical reference. Reject any playlist/multi-video selector, including a `list` value, and reject live/upcoming paths and media. Do not accept channel, search, playlist, URL shortener, arbitrary host, userinfo, IP literal, non-443 port, or fragment forms.
- Do not use browser cookies, account credentials, netrc, local yt-dlp configuration, extractor plugins/options supplied by the caller, browser automation, private/unlisted access assistance, age-gate bypass, DRM bypass, or login flow. A public watch page that requires restricted access is unavailable to this importer.
- Return an actionable original-file upload fallback for missing tools, blocked/unavailable videos, restricted formats, active/upcoming streams, and videos without a safe muxed audio/video format. Never promise that every public YouTube video can be imported.

## Acquisition boundary

Use a maintained, version-pinned yt-dlp extractor together with its required pinned JavaScript challenge component and a supported pinned JS runtime in the Docker image. Verify component compatibility and package provenance from upstream release/package metadata; record the exact versions and hashes with the implementation evidence. Do not fetch helper code dynamically during an import.

Run the extractor as a bounded subprocess with an argument vector (never a shell), an explicit canonical YouTube URL, ignored user configuration, no cookies or netrc, no playlist, no external download, a fixed muxed `video+audio` format selector, a strict wall/output limit, and process-group cancellation. A missing executable/runtime is an actionable unavailable result. Do not return subprocess output or its URLs in application logs or API errors.

All extractor metadata, player, API, and challenge requests must pass through an ephemeral loopback HTTPS CONNECT proxy. The proxy authenticates the child with an ephemeral local secret, accepts CONNECT to port 443 only, rejects ordinary HTTP and any unapproved host, resolves A and AAAA once per hop, rejects the hop if any answer is not global unicast, and dials only one of those validated addresses while preserving the requested TLS hostname. Permit only the verified YouTube page/API and YouTube media-CDN hostname families needed by the pinned extractor; document the exact allowlist in code. The proxy has bounded connection count, deadlines, output-independent errors, and closes all tunnels when the import is canceled or times out. The extractor receives no ambient proxy/config credentials and cannot choose the proxy destination.

The extractor must resolve exactly the input video ID to one non-live item. Reject playlist/result objects, mismatched IDs, `is_live`, upcoming or live status, missing/invalid/non-finite duration, duration beyond four hours, missing format selection, format metadata indicating no audio or no video, and a declared size over 20 GiB. Select one muxed format containing both audio and video; do not silently import a video-only format, merge separate streams, or strip source audio. A muxed format can be lower resolution than separate adaptive formats; this is an explicit support tradeoff.

Treat the selected direct media URL as short-lived secret data. Keep it in memory only. Fetch it with the existing Go streaming importer and pinned-DNS HTTP transport, accepting only the exact YouTube CDN host family approved for media. Validate every redirect independently with HTTPS/443, provider/CDN allowlist, all-public DNS answers and a pinned socket; enforce a small redirect cap. Send no cookies, authorization, or referrer. Reject a non-200/partial response, unsupported content type, declared size over 20 GiB, or actual streamed bytes above 20 GiB. Check duration again against the actual local `ffprobe` result. The persisted source URL remains encrypted and contains only the canonical watch reference; signed CDN URLs never reach SQLite, logs, errors, dashboard data, or SSE.

Reuse M2's single import queue, lease heartbeat/retry, cancellation, disk reservation, streaming-to-generated-file, sync/atomic publication, exact-byte SHA-256, actual local media probe, and failed-file cleanup. Do not write the extracted URL, title, query parameters, cookies, headers, raw stderr, or raw JSON to the source record. Use a stable generated filename based on the validated video ID rather than extractor-controlled filenames.

## Failure behavior

Map safe and expected failures to concise actionable text: confirm rights; use an ordinary single-video link; the video is unavailable without sign-in; no supported audio/video format is available; the video exceeds the four-hour or 20 GiB limit; the importer is unavailable in this runtime; or upload an original file you are permitted to reuse. Do not reveal the signed CDN URL, extractor stderr, raw provider response, internal proxy address/secret, DNS answer, or other untrusted values. Preserve existing M2 retry behavior for temporary network/lease failures and terminal cleanup for permanent restrictions.

## Deterministic verification

Tests must use fake extractors and fake HTTP transports/resolvers or local synthetic media. Cover URL normalization/rejection, rights attestation, video-ID matching, finite duration, live/playlist/unsupported-format rejection, command argument isolation, bounded JSON/output, no-shell invocation, child cancellation, proxy authentication and host/port allowlist, DNS rebinding/private/mixed answers, pinned dialing, redirect validation, signed-URL redaction, content-length and actual-stream size caps, audio preservation selection, ffprobe duration enforcement, retries and partial-file cleanup. Do not download arbitrary real-user videos in automated tests.

Local checks should include the focused Go acquisition/API tests, complete `go test ./... -count=1`, `go test -race ./... -count=1`, `go vet ./...`, dashboard Node tests and syntax checks, the preserved offline M1 contract checks, Dockerfile/dependency pin validation, cross-platform builds if OS-specific helpers change, and `git diff --check`. A live provider smoke, deployment, and media from a real user are not acceptance requirements and remain out of scope.

## Evidence and support limits

This implementation is a best-effort public-link adapter. YouTube may change page, API, player, challenge, CDN, or format behavior; access may also depend on region, availability, restrictions, or the extractor's current support. Record exact dependency versions and test results. Report operational gaps instead of widening the host allowlist, enabling credentials, or claiming universal success.
