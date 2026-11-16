# Gemini clipping operations and release gates

This runbook supports the implemented Gemini dashboard and execution path. G5 adds offline evaluation code and release documentation; it does not deploy Gemini, create Google Cloud resources, verify an ADC identity, run an inference request, or complete a backup/restore test. Code integration and offline verification are complete through G5; live provider, quality, cost, backup and rollout gates remain unverified.

## Supported deployment shape

Keep FrameVault as one owning Go service on one Google Compute Engine VM, in one container, with one persistent disk mounted at `/data`. Keep `/data/app.db`, `/data/secret.key`, generated media, and clipping artifacts on that disk. Only one service instance may own the SQLite database at a time. Do not autoscale the service, run multiple writers against the database, move SQLite to an ephemeral container filesystem, or introduce an agent host or separate queue for Gemini.

Attach a dedicated Google Cloud service account to the VM and let Application Default Credentials use the VM metadata identity. Give it an IAM role containing `aiplatform.endpoints.predict`; Google's predefined `roles/aiplatform.user` is the practical standard starting point. Restrict who can attach or impersonate that identity. The VM's access scope must allow the Vertex AI API, while IAM remains the permission boundary. Do not create, download, mount, or paste service-account JSON keys, and do not put credentials in app settings or logs.

Before any future inference, an operator with Google Cloud authority must enable the Vertex AI API, associate a billing account, select a supported project/location, review applicable quotas, and configure an explicit spending limit outside the application. Cloud Billing budget notifications are alerts, not hard spending caps; bound the approved source/attempt count and stop manually when evidence or cost is uncertain. These requirements are not satisfied by offline tests. Check Google's current primary documentation before setup because IAM permissions, model availability, API behavior, quota, and prices can change:

- [Application Default Credentials](https://cloud.google.com/docs/authentication/application-default-credentials)
- [Attach a service account to a Compute Engine VM](https://cloud.google.com/compute/docs/access/create-enable-service-accounts-for-instances)
- [Vertex AI access control](https://cloud.google.com/vertex-ai/docs/general/access-control)
- [Vertex AI quotas](https://cloud.google.com/vertex-ai/docs/quotas)
- [Cloud Billing budgets and alerts](https://cloud.google.com/billing/docs/how-to/budgets)
- [Workload Identity Federation](https://cloud.google.com/iam/docs/workload-identity-federation)

An external host is acceptable only after its OIDC/workload identity token issuer and claims have been verified to work with Google Workload Identity Federation. Do not assume that a hosting provider supplies compatible workload tokens. Railway support for this exact federation path is unverified; do not substitute a service-account key.

## Persistent state and backup

Keep the database, encryption key, and media in the same `/data` backup set. A database copy without `/data/secret.key` makes encrypted settings unusable; a key-only restore can also break access to existing encrypted data. Restrict disk and backup access to trusted operators.

For a portable file-level backup, stop the service cleanly first, then archive the complete `/data` tree or take a stopped-disk snapshot. Do not copy only `app.db` while the application is writing. Record the app revision, backup time, and disk identity with the backup, but keep those records free of secrets. Resume the single service only after the backup completes.

For restore, stop the service, restore the matching `/data` backup as a unit, verify ownership and restrictive permissions, and start one instance. Confirm health, sign-in, and access to a known existing media artifact before accepting new work. Perform a restore rehearsal on a separate test volume before relying on a backup. The offline evaluator does not inspect backups, keys, disk persistence, or restore results.

## Future canary procedure

Do not start a live canary until G1–G4 code is integrated and reviewed, the Vertex AI identity and endpoint are verified, the exact model/API/processing mode are supported, source rights are approved, and the operator has set a spending limit and attempt cap. Live inference and deployment require their own authorization; this repository task does not grant either.

1. Record the application revision, model and API version, prompt/schema digests, options, location, quota, spending cap, and one explicitly permitted source. Keep its URL and rights evidence in restricted operator records, never in an evaluation report.
2. Start with one source below the initial 40-minute target and one inference attempt. Human-measure source duration. Do not use private/account-owned media or treat source availability as guaranteed.
3. Watch the authenticated job state, provider outcome, reported token usage, and billing/metering separately. An HTTP response or provider-reported token count is not an invoice. Stop if the outcome is ambiguous, the budget could be exceeded, provider behavior differs from the pinned request contract, or the output fails schema validation.
4. Save the exact structured result and immutable request snapshot. Have two blinded reviewers complete source coverage and candidate rubrics using [EVALUATION.md](EVALUATION.md). Report descriptive measurements by language and content type; do not call a small canary a quality pass.
5. Compare the actual invoice or metered charge against the operator's ledger, retain safe evidence references, and reconcile the source, request, report, and actual cost before expanding the canary.

No retry may repeat an ambiguous provider submission unless the durable job and provider contract establish that the earlier attempt cannot still complete. Do not re-run a sample merely to improve a metric or exceed the approved attempt budget.

## Drain and rollback

Before replacing a deployed revision, stop accepting new Gemini submissions and record the owning revision. Let active attempts reach a durable terminal state. Resolve or preserve uncertain attempts and late completions using the job's recorded attempt identity; cancellation from the dashboard does not prove the provider stopped processing. Do not start a second application instance against the same `/data` volume while draining or switching revisions.

Take and verify a recoverable `/data` backup before rollout. Roll out the new revision to the single owner, run health and a permitted low-cost canary, and watch terminal job state and safe error codes. Expand only after the canary is reviewed and cost is reconciled.

If the canary fails, stop new submissions, let issued attempts settle or remain durably marked uncertain, and stop the new process. Restore the last known-good application revision against the same intact `/data` state only after confirming schema compatibility. Do not delete new job/result rows or restore an older database snapshot just to make the prior binary start. If schema compatibility is uncertain, keep submissions disabled and inspect a copy of the data before making another change. Re-enable work only after health and persisted state are confirmed.

The same drain rule applies to a future legacy-path retirement: do not remove a path while active work or provider callbacks can still settle through it. See the phase tracker for the actual retirement gate; an offline report is not evidence that production is drained.

## Video duration and Preview limits

Direct YouTube video input is a Preview capability and must be verified for the selected model, API, region, account, and source before release. The initial application target is at most 40 minutes. This is a planning limit, not verified provider support. The evaluator compares operator-entered and model-reported duration and flags values over the target; agreement between the two values does not prove the source duration or the endpoint accepted the full video.

Four-hour input support is unverified and is not part of the initial target. Do not silently split a long video into chunks or aggregate chunk results: no chunking/aggregation method has been validated for context, timestamps, ranking, repeated moments, latency, or cost. Keep sources above the target blocked until a separately approved end-to-end plan and measurement show the intended behavior.

## Current release gate summary

The G5 command is read-only and offline. It can validate an interchange file and summarize human evidence. It cannot establish the following operational facts:

| Gate | Current evidence from this tool | Required evidence before release |
| --- | --- | --- |
| Provider availability | Unverified; no live inference | Approved, bounded canary against the configured Vertex AI project and selected model |
| ADC authentication | Unverified; no attached identity is inspected | Successful identity and permission check from the actual single-owner runtime |
| Source rights | Operator attestation only; not independently verified | Per-source permission evidence retained by the operator |
| Human quality | Insufficient or descriptive only | Complete adjudicated labels across an approved corpus, with every stratum reported separately |
| Usage and actual cost | Optional provider-reported counts and optional operator records | Metered usage and invoice reconciliation; unknown values remain unknown |
| Long-video support | Preview / 40-minute target unverified | Approved full-source test at target duration; no four-hour claim without a separate test |
| Deployment and backup | Not run by the evaluator | Single-owner persistent-disk check, backup, and successful restore rehearsal |

Readiness labels are `not_run`, `blocked`, `insufficient_evidence`, `unverified`, and `descriptive_only`. None means production ready. The service is not deployed through this runbook, and no live provider or billing check has been made in this session.
