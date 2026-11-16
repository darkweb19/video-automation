# Durable Gemini execution contract

G3 is implemented and verified offline. G4 exposes the authenticated submission route, lifecycle projection and dashboard. This document records the lifecycle behavior; the [tracker](TRACKER.md) records implementation, tests and publication. It is not evidence of live Google access.

## Submission and recovery

Gemini execution is explicitly opt-in using `CLIPPING_GEMINI_ENABLED=true`, `CLIPPING_GEMINI_PROJECT_ID`, and `CLIPPING_GEMINI_LOCATION`. Configuration and health reads never resolve ADC or contact Google. Configured means local routing is valid, not that credentials or a particular YouTube source have been verified.

Two workers own Gemini jobs. A worker restores the source-owned encrypted URL and immutable request snapshot, checks source eligibility/retention, and re-derives the complete fingerprint before submission. It accepts a URL reference or an eligible existing YouTube source; it never reads local media, paths or hashes. Configuration or code drift fails before inference.

The database records the attempt and submission intent before external work. A global guard on the complete request fingerprint prevents concurrent or unknown work from being replayed through a new source ID or idempotency key. A completed fingerprint is not a permanent lock: normal source-scoped cache eligibility or a genuinely new explicit analysis determines subsequent work.

Requests use the G1 single-attempt client and its fixed deadline. A lease fences completion. Restart recovery quarantines unfinished submitted intent as unknown; it must not infer that silence means a free retry. Deleting or canceling an unknown job does not make its fingerprint safe to replay.

## Outcomes and retry policy

| Outcome | Required behavior |
| --- | --- |
| Known not submitted | Bounded automatic retry where appropriate; retain a safe actionable reason. |
| Explicit rejection | Only an explicit 429 receives capped durable backoff; other rejections terminate. |
| Response received | Persist available usage even when validation fails; no automatic correction inference. |
| Unknown after submission | Persist uncertainty and prevent automatic replay, including after restart or deletion. |

There is no manual Gemini stage-retry endpoint in this phase. The existing legacy retry helper remains provider-fenced. G4 exposes `retry_safety` states describing automatic lifecycle behavior: `not_submitted_retryable`, `rate_limited_wait`, `response_received_terminal`, `outcome_unknown_no_replay`, `terminal`, or `none`. `retry_at`, when present, is a persisted deadline, not a browser countdown. The dashboard must not offer a manual retry button merely because safe automatic retry remains possible.

## Cancellation, completion and usage

The cancellation registry belongs to the shared Store, since the dashboard and processor have separate wrappers. Cancellation/deletion commits durable state before canceling an active context. Worker registration checks durable state again so a cancel that races registration cannot launch paid work afterward.

Finalization uses one fenced transaction to check provider, current attempt, lease, fingerprint, cancellation, deletion and source retention. Only a valid current completion writes the v3 artifact and job/stage completion plus eligible cache origin. Late responses cannot restore deleted results or overwrite newer attempts. Returned usage should still be retained for an already-issued attempt when safe; canceling locally cannot guarantee that Google stopped or did not charge.

Cache hits retain original job, creation and expiry provenance. Usage from an origin remains origin usage; reuse does not create a new charge. Missing usage is unknown. Token counts are not an invoice and no currency estimate is invented.

Observability records application-owned job/attempt identities, outcome codes, timing and available counts. Do not persist or log raw SDK errors, provider bodies, source URLs or credentials. Gemini retains all semantic selection and ranking authority. Media extraction remains outside this executor.
