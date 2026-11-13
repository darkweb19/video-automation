# FrameVault Dashboard UX Contract

This contract records observable behavior for the Clipping workflow. Visual roles and runtime ownership live in [DESIGN.md](DESIGN.md); broader dashboard design evidence is in [docs/frontend-redesign.md](docs/frontend-redesign.md).

## Business and API sources

| Decision area | Authority |
|---|---|
| Gemini workflow, source reference, limits, result provenance, and no-acquisition rule | [Gemini migration plan](docs/clipping/GEMINI-MIGRATION.md), [ADR-005](docs/architecture/005-gemini-clipping.md), and the G4 API contract |
| Legacy records and compatibility | [ADR-004](docs/architecture/004-ai-clipping.md) and the authenticated source/job APIs |
| Dashboard shell, same-origin authentication, existing controls, and accessibility conventions | [AGENTS.md](AGENTS.md) and [docs/frontend-redesign.md](docs/frontend-redesign.md) |

Business and API documents define data policy. This file describes what the browser shows and how it behaves; it does not redefine API validation or provider lifecycle rules.

## Clipping workflow

| Operation | Trigger | Pending state | Success state | Failure and recovery | Focus |
|---|---|---|---|---|---|
| Request Gemini analysis from a link | **Analyze video** with a valid YouTube URL, rights attestation, and options | Disable duplicate submission and fields; say the request is being saved. Do not show media import progress or a percentage. | On the server's persisted job response, show the job and refresh saved references/jobs. Clear the link and attestation after acceptance. | Keep the entered URL and options. Show the safe inline error. Do not auto-submit, poll, or retry. An explicit repeat carries the same idempotency key and identical body until the request is accepted or the form changes. | After acceptance, focus the Analysis heading. On validation failure, focus the first invalid field. |
| Analyze a saved YouTube source | **Analyze with Gemini** on a ready YouTube URL reference or supported legacy YouTube source | Disable that row's action and announce request saving; do not imply a media download. | Display the persisted Gemini job in Analysis. | Keep the saved row and show safe inline action feedback. Never replay an unknown provider attempt; follow the job's `retry_safety` value. | Keep focus in the source row on failure; focus Analysis after acceptance. |
| Read analysis | **View saved analysis** or the current job result state | Show a stable loading message in the result area. | Render the existing results envelope; display legacy artifacts and schema-v3 Gemini analysis by their exact schema. Preserve candidate order, rank, proposed millisecond boundaries, title variant order, evidence, estimates, shortfall, and limitations. | Keep the job visible and offer a manual refresh. Do not replace an error with a guessed result or rescore candidates. | Keep focus on the result disclosure through live updates. |
| Delete a saved source or job | **Delete** action in a source/job row | Open an app-owned modal; name the affected source/results and consequence. Initially focus **Cancel**. Keep the dialog open while deletion is pending. | Remove the deleted row after server acknowledgement and announce the server result. | Keep the dialog or row context and show a safe error. No optimistic deletion or automatic retry. | Restore focus to the trigger if canceled; after deletion, focus the next action or owning list heading. |

## Provider state and recovery

- Configuration is read only from the authenticated same-origin `/api/clipping/config` response. `configured` and `enabled` gate the new form; `verification: unverified` is shown as unverified. The browser does not probe ADC or claim live provider access.
- The URL request atomically saves a rights-attested reference and job through `POST /api/clipping/gemini/jobs`. The request body carries the URL or source ID and the exact options; `Idempotency-Key` protects an explicit same-body resubmission.
- New URL references show a generic YouTube label only. They do not expose source URLs, filenames, file sizes, local paths, or presumed media duration. No import, download, extraction, or fabricated progress state appears in the flow.
- Job status is authoritative. Gemini jobs do not display a percentage unless a future API supplies meaningful determinate progress. No client timer or automatic retry is used.
- Interpret `retry_safety` as returned: `not_submitted_retryable`, `rate_limited_wait` (show `retry_at` without a timer), `response_received_terminal`, `outcome_unknown_no_replay`, `terminal`, or `none`. An unknown provider outcome is never replayed by the browser.
- Legacy saved source/job endpoints and artifact envelopes remain readable. Legacy artifacts keep their established renderer; only the exact `framevault.clipping.analysis.v3` artifact uses the typed Gemini renderer.

## Shared interaction rules

- API requests remain authenticated and same-origin. Existing logout lifecycle invalidation, stale-list response protection, SSE updates, and deleted-record tombstones remain active.
- Clipping content type and suggestion count use native `<select>` controls; the browser or operating system owns their popup geometry and keyboard behavior. Their values remain part of the labeled analysis form.
- While a submission's HTTP outcome is unresolved, keep its body and idempotency key only in memory. An explicit resend uses those exact values even if the visible options have changed; no automatic submission is made. Block other new submissions until that response is recovered because an unacknowledged link cannot yet be associated with a saved source. Known video-to-source associations stay in memory for the current session so the link form cannot bypass a visible active or unknown provider outcome.
- Canceling an analysis is an app-owned confirmation and changes the saved local job. It does not assert that Google has stopped work already submitted. Deletion has a separate confirmation.
- Forms use `novalidate`, associated labels, inline errors, preserved non-sensitive values, and duplicate-submit protection. Disabled states do not trigger actions.
- Empty, loading, stale, and error states stay within their owning panel. Status text is visible and announced without moving the primary controls.
- The page follows the existing responsive dashboard shell, visible focus style, keyboard-operable controls, and reduced-motion behavior. Browser-native `alert()`, `confirm()`, and `prompt()` are not product UI.
