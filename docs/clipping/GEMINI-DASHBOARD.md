# Direct YouTube analysis

The Clipping dashboard submits a public YouTube URL for Gemini analysis. It creates a reference and analysis job together; the application does not import or download the video. Gemini chooses the clips, hooks, titles, explanations, scores and order. Media extraction and `youtube-extractor-rs` integration are outside this workflow.

## Use the dashboard

1. Open **Clipping**, enter one public YouTube video URL, and confirm permission to process the footage.
2. Choose the analysis profile, clip-duration range and candidate limit, then select **Analyze video**.
3. View the saved analysis when the job completes. Review the suggested hooks, context, evidence and proposed boundaries before using a candidate.

Clip durations can range from 15 to 180 seconds, with 1–10 candidates requested. Gemini may return fewer candidates, including zero, with an explanation. The initial source limit is 40 minutes and is checked against model-reported duration; it is not a download/probe or proof that Google inspected every moment. Google YouTube input is Preview, and availability is best-effort. Analysis may incur Google Cloud charges, including when a result cannot be used.

Results preserve Gemini's rank and exact millisecond boundaries. Scores are editorial estimates, not promises of virality; timestamps are proposed clip boundaries. A Ready reference means the URL was accepted, not that media was downloaded or Google access was verified. Reference rows have no local playback link, byte size, media hash or measured source duration. Eligible old YouTube sources can be analyzed remotely from their saved URL; their existing media and old results remain available.

**Configured** describes local settings only. Authentication, the selected model and a particular public video remain unverified until actual provider work succeeds. Operators use ADC/service accounts with `CLIPPING_GEMINI_ENABLED=true`, `CLIPPING_GEMINI_PROJECT_ID` and `CLIPPING_GEMINI_LOCATION` (`global`, `us` or `eu`). The browser never receives Google credentials or routing configuration. See the [provider guide](GEMINI-PROVIDER.md) and [execution contract](GEMINI-EXECUTION.md).

## Failed or uncertain work

The backend automatically retries only bounded known non-submissions and explicit rate-limit rejections. There is no manual Gemini stage-retry action. An explicit new analysis after a known terminal outcome creates a separate job; it is useful after an operator fixes configuration or credentials. Active or unknown work remains protected against duplicate inference.

If the HTTP submission response is lost, **Resend same request** uses the exact original body and idempotency key. It does not silently create a new key when options or URL spelling change. Other submission paths stay blocked until that request is resolved. This is different from replaying an inference with an unknown provider outcome.

Canceling stops local result publication and requests context cancellation. It cannot confirm that Google stopped or did not charge. Deleting an analysis removes its saved results and invalidates eligible cached intent; it does not make an unknown request safe to repeat. Deleting a source follows the existing source lifecycle and deletion rules.

## HTTP contract

Use the authenticated, same-origin API with `Content-Type: application/json` and an `Idempotency-Key` of at most 128 bytes.

`POST /api/clipping/gemini/jobs` accepts exactly one source form. An illustrative new-reference request is:

```json
{
  "youtube_url": "https://www.youtube.com/watch?v=abcdefghijk",
  "rights_attested": true,
  "options": {
    "content_type": "general",
    "min_clip_ms": 30000,
    "max_clip_ms": 90000,
    "candidate_limit": 10
  }
}
```

Replace the illustrative video ID with the intended public video. Submit the original URL for server validation; do not repair unsupported HTTP, playlist or fragment URLs into accepted inputs. Existing sources use `{ "source_id": "<saved-source-id>", "options": { ... } }` instead. Caller-selected projects, models, prompts, endpoints and credentials are unsupported. Duplicate identical submissions return the original job; a key reused for different intent returns conflict.

The response contains `job` and `duplicate`; creation returns 201 and a replay returns 200. Jobs expose `provider`, exact immutable `options`, safe lifecycle/error codes, available usage and cache-origin provenance. `retry_safety` describes automatic policy, not a manual retry permission. The legacy retry endpoint rejects Gemini jobs.

`GET /api/clipping/config` includes a safe `gemini` descriptor with `configured`, `enabled`, `verification: "unverified"`, and limits. It performs no ADC discovery or provider probe.

`GET /api/clipping/jobs/{id}/results` retains the existing artifact envelope. Gemini artifacts use `schema_version: "framevault.clipping.analysis.v3"`; `payload_json` contains the direct [v3 analysis contract](contracts/gemini-analysis.v3.schema.json). Corrupt payloads, mismatched ranges or invalid envelopes fail closed. Legacy artifacts retain their original representation and renderer.

SSE is an invalidation hint: the dashboard reloads authoritative jobs and sources, protects against stale requests, and retains deletion tombstones and keyboard focus. It does not derive permission to retry from an old event payload.
