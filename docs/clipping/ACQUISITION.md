# C1.1 source acquisition contract

Status: bounded offline contract and simulated checks complete; live provider acquisition remains unverified. M1 authorization covers this feasibility work only. No user media links were supplied, and this task made no media requests, inspected no credentials, ran no provider job, and incurred no spend. This document does not claim that YouTube ingestion works.

## Route decision

YouTube remains the product's first-choice input. A YouTube URL is accepted as a source reference, but it is not treated as source media. The current route returns `original_file_required` after the user attests reuse rights. The YouTube Terms of Service restrict downloading or otherwise using content except where expressly authorized by the Service or with prior written permission from YouTube and, where applicable, the respective rights holders. YouTube Help documents downloading a video uploaded by the signed-in user through Studio or Takeout and says other users' videos cannot be downloaded that way; the Studio download may be 720p or 360p. The documented YouTube Data API supports video-resource operations such as metadata and uploads; the API reference reviewed for C1.1 documents no source-video download method. Accordingly, do not use watch-page scraping, player-stream extraction, third-party downloaders, or the metadata API as a media route. A user who has an authorized source file can upload it through the separate file route. YouTube-first is preserved as an unresolved product priority, not marked feasible or solved. ([YouTube Terms](https://www.youtube.com/t/terms), [YouTube Help: download videos you've uploaded](https://support.google.com/youtube/answer/56100?hl=en), [YouTube Data API video resource](https://developers.google.com/youtube/v3/docs/videos), [YouTube Data API reference](https://developers.google.com/youtube/v3/docs))

| Input | C1.1 route decision | Outcome when no permitted media bytes are returned |
| --- | --- | --- |
| YouTube watch, short, or embed URL | Parse as a reference only. Do not fetch the playable stream. The uploader may download their own video using official YouTube controls and upload a local copy; prefer the original source file because Studio's download can be lower resolution. | `original_file_required` (`youtube_has_no_approved_media_fetch_route`) |
| Uploaded local video | Supported candidate. Require an affirmative reuse/processing attestation, bounded upload streaming, the same byte/container/media checks below, and a successful probe before accepting it. | `authorization_needed` until attested; `unsupported` if validation or storage bounds fail |
| Public Google Drive viewer/share URL | Candidate only if an HTTPS GET directly returns a public video file. Do not parse or scrape a viewer page, synthesize undocumented `/uc` URLs, sign in, or follow a page's download controls. The documented browser download route is the file resource's `webContentLink`; the Drive API examples retrieve that link with an authorized API request. A current link-only path from a viewer URL to that link has not been verified. Public content may be downloadable without credentials, but the actual browser content link and download permission still have to work. | `original_file_required` for a viewer/confirmation HTML page; `authorization_needed` for 401/403 or disabled access; `unsupported` for a non-media response or unsafe route |
| Public Dropbox shared file URL | Candidate route: for recognized `/s/` or `/scl/fi/` shared links, set `dl=1` as Dropbox documents and inspect each redirect before following it. Accept only a final media response that passes all checks. `raw=1` is documented to cause a redirect but is not the selected route. | `authorization_needed` for 401/403; `unsupported` for HTML, unsafe redirects, or invalid media; ask for the original file if the public share route does not return bytes |
| Private Drive/Dropbox pages, arbitrary URLs, other hosts, generic URL shorteners | Out of scope for this source contract. No cookies, OAuth, browser automation, cross-account connection, unbounded URL fetch, or arbitrary-host fallback. | `authorization_needed` for missing access; otherwise `unsupported` or `original_file_required` |

The Drive API documents binary file downloads with `files.get?alt=media`, browser downloads via `webContentLink`, and a `capabilities.canDownload` check. Its API download example uses an access token. C1.1 does not assume that a pasted viewer URL is the `webContentLink` or that an unauthenticated API call can discover it. A public browser content link can be tested only as an exact user-supplied URL, with no cookies or token. Google Workspace Docs, Sheets, and Slides are not video sources. ([Drive download and export guide](https://developers.google.com/workspace/drive/api/guides/manage-downloads), [Drive `files` resource](https://developers.google.com/workspace/drive/api/reference/rest/v3/files))

Dropbox documents `dl=1` as a way to force a shared-link download and notes that `raw=1` redirects. This supports a provider-specific route proposal, not a universal guarantee that a public link is downloadable or that every redirect host is known. ([Dropbox Help: force a shared link to download](https://help.dropbox.com/share/force-download))

## Import outcomes

Every attempted source returns one of these stable statuses and a machine-readable reason. Status describes the import attempt; a route classified as `probe_required` by the offline tool is not an import and cannot be recorded as successful.

| Status | Meaning | Example reasons |
| --- | --- | --- |
| `imported` | Complete media bytes were streamed, bounded, probed, hashed, and atomically accepted. The success metadata below is present. The rights field records the user's attestation, not independent proof of ownership or a provider's authorization decision. | `simulated_probe_validated` in offline fixtures; production success requires actual byte/probe evidence |
| `authorization_needed` | The user has not attested reuse/processing rights, or the provider reports restricted/private access. Do not retry with credentials or cookies. | `reuse_rights_not_attested`, `provider_denied_download` |
| `original_file_required` | A reference or viewer page has no approved direct media route in the current scope. Ask the user to upload an original file or provide a permitted public direct-content link. | `youtube_has_no_approved_media_fetch_route`, Drive viewer/interstitial requires a file |
| `unsupported` | The URL, route, response, media, or available storage violates the bounded contract. Do not silently switch to another host or fetch mechanism. | `unsupported_source_host`, `unsafe_dns_answer`, `redirect_provider_mismatch`, `non_media_response`, `source_size_limit_exceeded`, `duration_limit_exceeded`, `storage_reservation_unavailable` |

Reason codes are listed in [`acquisition_contract.py`](../../tools/clipping/acquisition_contract.py) and exercised by offline tests. Product responses should not echo signed URLs, query tokens, response bodies, credentials, or untrusted filenames. Logs may include the status/reason, provider family, byte count, and a redacted source reference only.

## Network and file handling contract

These are required for a future downloader; the Python tool in this task only evaluates fixture data and does not implement them. The SSRF rules follow OWASP guidance to disable automatic redirects, allowlist destinations, validate every resolved address, and bind the connection to a checked address so a second DNS lookup cannot rebind the request. ([OWASP SSRF Prevention Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Server_Side_Request_Forgery_Prevention_Cheat_Sheet.html))

- Parse URLs with a standard URL parser. Accept HTTPS only on port 443, reject userinfo, fragments, control characters, IP-literal hosts, malformed/IDN ambiguity, and unknown provider hosts. Do not accept caller-supplied headers, cookies, proxy settings, or authorization tokens.
- Resolve A and AAAA once per hop. Reject if there are no answers or if **any** answer is not a globally routable unicast IP, including private, loopback, link-local, multicast, reserved, unspecified, IPv4-mapped unsafe IPv6, or mixed public/private answers. Pin the actual socket connection to one validated address while retaining the host for TLS SNI, certificate verification, and the HTTP Host value. Apply the same checks to retries and redirects.
- Disable client-managed redirect following. For Dropbox and Drive only, allow at most five 301/302/303/307/308 hops. Require HTTPS and the same provider's explicit host family at each hop; reject downgrade, host escape, missing `Location`, a redirect loop, or a second unvalidated lookup. No YouTube media redirect route is enabled.
- Send an unauthenticated GET with no cookies or referrer. Accept only a final HTTP 200 response for this no-Range GET; reject 206 Partial Content, other status codes, and any Content-Range header. Map 401/403 to `authorization_needed`; a public link that requires an interstitial/login does not become public because the user supplied its URL.
- Stream in bounded chunks to an application-generated exclusive temporary file under the configured source volume. Enforce bytes counted while streaming even when `Content-Length` is absent or false. Use an independent deadline/cancellation context; do not buffer a video in memory or trust the filename, extension, MIME header, or declared size. Sync and atomically rename after validation; remove partial files on failure/cancellation. Never follow a server-provided local path.
- Require a successful media probe of the completed file. The initial accepted container proposal is MP4, MOV, Matroska, or WebM, with at least one video stream; source audio may be empty. Treat MIME and magic-byte sniffing only as early rejection signals, then validate actual streams, duration, dimensions, codecs, and decoder support with a bounded `ffprobe` process. Probe failures, malformed media, archive/container tricks, and unsupported codecs map to `unsupported`. Do not feed imported media to the existing generation combiner, which strips audio; clipping needs its separately planned audio-preserving render path.
- Calculate SHA-256 over the exact stored bytes. Do not use ETag or provider metadata as an integrity hash. Source/candidate/transcript timestamps use integer milliseconds; intervals are half-open `[start_ms, end_ms)`. Convert nonnegative media probe seconds to milliseconds with decimal arithmetic, rounding half up to the nearest integer; reject missing or invalid duration before persisting it.

## Provisional byte and storage bounds

These are planning values for an initial single-instance deployment, not measured capacity or a verified product limit. Four hours remains the duration ceiling, but not every four-hour file fits the provisional byte cap: 16 GiB covers an average encoded rate of about 9.5 Mbit/s for four hours. Higher-bitrate sources need an explicit cap decision or a user-provided smaller permitted source; do not silently downscale it.

| Bound | Proposed value | Admission rule |
| --- | ---: | --- |
| Maximum source duration | 14,400,000 ms (4 h) | Determine from a successful media probe; reject longer sources |
| Maximum source bytes | 17,179,869,184 bytes (16 GiB) | Enforce against both declared size and actual streamed bytes |
| Per-source derived/scratch reservation | 8 GiB | Reserve space for analysis/render intermediates and initial exports; stop before exceeding the reservation |
| Global free-space safety floor | 4 GiB | Do not start an import unless free space covers the source reservation + 8 GiB + 4 GiB; recheck while streaming and before later stages |

Thus the maximum initial per-job retained allocation is 24 GiB, and an import at the 16 GiB source ceiling requires at least 28 GiB free at admission. Reserve from the declared source size only after validating it; with no trustworthy length, reserve the 16 GiB maximum. Treat `/data` capacity, concurrency, export sizing, cleanup retention, transfer timeouts, and whether the 8 GiB derived reservation is enough as open measurements for M1/M2. Until the actual volume headroom is known, fail closed when a reservation cannot be made. An import should be serialized initially so simultaneous partial downloads cannot consume the same reservation.

## `ai-clip.source.v1` success metadata

Only create this record after actual media bytes have passed the checks above. Fields are intentionally normalized and contain no raw URL or credentials. The offline simulator emits a `simulated: true` marker outside the metadata; that result is fixture evidence and must never be persisted as a production import.

| Field | Type and rule |
| --- | --- |
| `version` | String literal `ai-clip.source.v1` |
| `source_id` | Stable caller-assigned ID, 1–128 characters matching `[A-Za-z0-9][A-Za-z0-9._-]*`; evaluation IDs can be used directly |
| `source_kind` | `upload`, `youtube_original_file`, `google_drive_public`, or `dropbox_public` |
| `source_sha256` | Lowercase 64-character SHA-256 hex of exact imported bytes |
| `source_bytes` | Positive integer; actual stored byte count |
| `duration_ms` | Positive integer, no greater than 14,400,000 |
| `container` | `mp4`, `mov`, `mkv`, or `webm` in this provisional accepted set |
| `video_streams` | Nonempty array. Each normalized object has `codec` string and positive integer `width`, `height`, `frame_rate_num`, `frame_rate_den` |
| `audio_streams` | Array, possibly empty. Each normalized object has `codec` string and positive integer `sample_rate_hz`, `channels` |
| `acquisition_route` | `local_upload`, `youtube_original_file_upload`, `drive_public_content_url`, or `dropbox_public_dl1` |
| `authorization_state` | Literal `attested`; this records the explicit user rights/processing confirmation and is not a provider permission token |
| `source_time_unit` | Literal `ms`; all downstream source timeline values are integer milliseconds |

The fixture validator verifies the exact-byte hash, ID syntax, URL/route family, stream summary shape, and source bounds. Evaluation fixtures should use stable synthetic slot IDs such as `eval-podcast-en-01`; these IDs do not imply that any real file, rights grant, or live import exists.

## Evidence and remaining work

| Evidence class | Result |
| --- | --- |
| Documented in primary sources | YouTube restriction and official uploader download path; documented Drive download/content-link operations; Dropbox `dl=1` behavior; OWASP redirect/DNS pinning guidance |
| Offline simulated | URL classification; missing rights; YouTube fallback; Drive/Dropbox route candidates; provider-redirect checks; private/mixed DNS; redirect cap; HTML/non-media; HTTP authorization failure; source duration/size/storage limits; source hashing; metadata normalization |
| Live provider behavior | Unverified. No YouTube, Drive, or Dropbox destinations were present for a controlled permitted sample; no requests to those providers were made. |

Before a live import probe, the lead must present proposed sample inputs and a spending limit. There are no provider charges in this contract task. YouTube-first acquisition remains blocked. C1.3 may compare quality on separately permitted original files only if the user explicitly accepts that scoped benchmark route; those results cannot be presented as YouTube acquisition success. The offline check from the repository root is:

```sh
python3 tools/clipping/test_acquisition_contract.py
```

It uses Python's standard library only and performs no networking. A reported offline `imported` fixture is a simulated contract outcome, not evidence of successful provider acquisition.
