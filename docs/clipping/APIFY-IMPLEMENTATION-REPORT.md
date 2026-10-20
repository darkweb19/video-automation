# Apify acquisition implementation report — 2026-10-19

Code is implemented and locally committed on `feat/clipper-ai`. Main orchestrated; actual `gpt-6-luna` / `max` agents authored changes and independently reviewed them. No outstanding concrete review findings remain. The four implementation commits were published to `origin/feat/clipper-ai` on 2026-10-20 after explicit user authorization; remote parity was verified at `cc4c484`. A documentation-only publication record follows. No deployment, main merge, paid actor run or GPU call occurred.

**Live acceptance remains pending. No real video was imported with a configured Apify actor. Mocked success does not establish that the integration works in production.**

## Architecture and preservation

Before: YouTube URL → durable import queue → local yt-dlp/Deno and FFmpeg → persistent storage → probe/hash/atomic Ready → explicit Modal analysis.

After: YouTube URL → same queue/attempt/lease → durable Apify intent/run ID → bounded polling → secure saved-file streaming → same persistent storage/probe/hash/atomic Ready → unchanged Modal analysis.

No yt-dlp fallback remains. FFmpeg/ffprobe remain; exclusive downloader runtime dependencies are removed. Database changes are additive. Existing Ready sources, source/job IDs, Modal worker/algorithms, authorization/callback contracts, go.mod and go.sum are unchanged. The dashboard uses truthful stages and indeterminate progress, with sanitized errors.

A durable start fence prevents automatic duplicate paid starts after ambiguous responses; known runs recover on restart. Attempt/lease guards prevent stale publication. Deletion cancels local work and best-effort aborts known external runs. New v3 imports stream one MP4 with the existing 20 GiB source allowance, aggregate reservations and 4 GiB disk floor; completed legacy v2 recovery remains supported. HTTPS/public DNS checks, pinned dialing, bounded redirects and separate API/media credentials protect retrieval. Media must pass actual MP4/audio/video/dimensions/timing/duration checks, SHA-256 and guarded atomic Ready.

## Configuration and actor contract

Required: `APIFY_API_TOKEN` and `APIFY_YOUTUBE_ACTOR_ID=thenetaji~youtube-video-downloader` (slash spelling accepted). Optional: `APIFY_REQUEST_TIMEOUT=60s` (positive, ≤5m) and `APIFY_MAX_POLL_DURATION=4h` (1m–12h). Keep one app owner and persistent `/data`, including SQLite, media and secret key. Railway setup, token acquisition, pricing and troubleshooting are in [YOUTUBE-IMPORT.md](YOUTUBE-IMPORT.md).

Selected actor: [thenetaji/youtube-video-downloader](https://apify.com/thenetaji/youtube-video-downloader). Current Store input/output/pricing checked 2026-10-19; publisher integration provenance pinned at `thenetaji/apify-n8n-nodes` commit `760dd49fa8dad652edd091523477fd3dde07f9ea`. This is not an immutable actor build pin. Other actors/clones are rejected rather than assuming compatible schemas.

Exact request:

```json
{"urls":[{"url":"https://www.youtube.com/watch?v=VIDEO_ID"}],"region":"US","saveMedia":true,"quality":"best","format":"mp4","includeTranscript":false}
```

Asynchronous `POST /v2/acts/thenetaji~youtube-video-downloader/runs?waitForFinish=0` includes a run timeout from the remaining persisted deadline. Persist the acknowledged ID, poll `GET /v2/actor-runs/{runId}?waitForFinish=30` with bounded backoff, then read the successful run's dataset. Require exactly one item containing `savedFile.url`; reject metadata-only/direct-format fallback. Returned identity must match when present, and declared bytes must match when provided. Validate downloaded media independently.

Paid-plan `saveMedia` entitlement is required even though pricing lists a Free row. Costs can include starts, video events, downloaded media and platform usage. The checked pricing lists $10/$9/$8/$7 per 1,000 videos and $2/$1.80/$1.60/$1.40 per 1,000 billed MB across Free/Starter/Scale/Business; verify current account pricing before use. These are documentation values, not measured spend.

## Executed verification

Final frozen-source checks used managed Go 1.27.1 and actual SQLite. Supported scoped network permission resolved initial module/proxy/loopback restrictions. No omission overlay was used in final checks.

| Check | Result |
| --- | --- |
| Exact `go test -p=1 ./...` | Passed; app 61.285s |
| Focused real-SQLite `go test -race` | Passed; 13 cases, 9.995s |
| `go vet -p=1 ./...`, `go build -p=1 ./...` | Passed |
| app.js/clipping.js syntax | Passed |
| Node clipping/dashboard harnesses | Passed; 21/87 logical checks |
| Python callback delivery | Passed |
| Modal protocol | Passed; 14 tests |
| Static Modal source contract | Completed; two cases skipped for missing Modal dependencies |
| Broader unchanged Modal worker suite | Uncompleted; interrupted after >2 minutes |
| Compose config | Passed |
| Go formatting, staged whitespace, local Markdown links | Passed |
| Docker image build/rendered-browser/live acquisition | Not verified |

Mocked HTTP and real local MP4 fixtures cover Ready publication, actor/auth/rate-limit failures, timeout, malicious URLs/redirects, missing/corrupt/truncated/empty/oversized media, cleanup, deletion/cancellation races, restart reuse and uncertain-start duplicate prevention. Existing clipping revision tests cover downstream compatibility. Initial suite failures exposed stale fixtures and a fresh-start state shadowing bug; those were fixed before the final passing suite. Independent review reran key Ready, deletion, uncertain-start and empty-body regressions.

## Remaining risks and acceptance

- No configured actor/token or real public-video import was exercised. Verify a real source through Ready, then existing Modal analysis under appropriate live-test authorization.
- Community actor builds can change independently. Upstream duration/size limits and saved-URL lifetime are undocumented; local admission limits do not prove upstream capacity.
- Original-language audio selection is not guaranteed by the actor. The adapter preserves the delivered file unchanged and requests best quality without local quality downgrade.
- Optional output identity cannot independently establish source identity when absent.
- Charges may accrue before local rejection. Run timeouts bound lifetime, not money or upstream media bytes. Ambiguous starts need operator inspection, not automatic resubmission.
- Production Railway/image execution remains unverified; the broader optional Modal worker suite did not finish. Modal files are unchanged and protocol/callback checks passed.

## Commits

- `ae324c4`: durable acquisition, storage, media and regressions; obsolete execution removal.
- `75116dd`: dashboard stages and sanitized errors.
- `2879f24`: runtime configuration, actor/operator documentation and CI.
- The following handoff/report commit records verification; Git history identifies its final hash.

## Files changed

A = added, M = modified, D = deleted. This report itself is added by the handoff commit.

```text
M	.env.example
M	.github/workflows/clipping-dispatch-regression.yml
M	Dockerfile
M	README.md
M	SESSION.md
M	docker-compose.yml
M	docs/architecture/004-ai-clipping.md
M	docs/clipping/PLAN.md
M	docs/clipping/TRACKER.md
M	docs/clipping/YOUTUBE-IMPORT.md
M	internal/app/clipping_acquisition.go
M	internal/app/clipping_acquisition_test.go
A	internal/app/clipping_apify.go
A	internal/app/clipping_apify_attach_race_test.go
A	internal/app/clipping_apify_client_test.go
A	internal/app/clipping_apify_http_test.go
A	internal/app/clipping_apify_recovery_test.go
A	internal/app/clipping_apify_storage.go
A	internal/app/clipping_apify_storage_test.go
M	internal/app/clipping_diagnostics.go
M	internal/app/clipping_diagnostics_test.go
M	internal/app/clipping_pipeline_revision_dispatch_test.go
M	internal/app/clipping_storage.go
M	internal/app/clipping_youtube.go
D	internal/app/clipping_youtube_debug.go
D	internal/app/clipping_youtube_debug_test.go
D	internal/app/clipping_youtube_process_other.go
D	internal/app/clipping_youtube_process_unix.go
D	internal/app/clipping_youtube_process_unix_test.go
D	internal/app/clipping_youtube_process_windows.go
M	internal/app/clipping_youtube_test.go
M	internal/webui/static/clipping.js
M	tests/clipping-ui.test.cjs
D	tools/clipping/check_youtube_runtime_contract.py
D	tools/clipping/requirements-youtube.lock
```

Verification scratch/cache/logs are outside the repository under `/workspace/work/video-automation-apify-verification-20261019`. A local patch is retained there. The requested chat output directory is unavailable on this execution filesystem (read-only `/codex`), so the report is stored with the repository documentation.
