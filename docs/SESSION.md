# Session handoff — 2026-09-25

## What was done
- Redesigned History and Settings; completed History logs now collapse behind per-card toggles.
- Added a password-protected Vault tab for completed generated videos and 30-second project videos.
- Added encrypted Vault code storage, PIN attempt throttling, server-enforced grants, lock/reload revocation, and reversible History/Vault moves.
- Protected Vault membership, listing, media, details, and downloads across API paths; added persistence and concurrency tests.

## Decisions locked
- Vault is a separate tab; moving a video removes it from regular History, and Restore returns it.
- Use a four-digit code; the Vault locks on refresh or explicit Lock. There is no forgotten-code recovery in this version.
- Keep the code safe; it is encrypted at rest and never returned by Settings.
- Do not run paid video generation for verification.

## Open questions
- None for the feature scope.

## Next steps
1. Release agent creates and merges a PR from the pushed feature branch.
2. Verify the Railway deployment and manually check the History, Settings, Vault, move, restore, and lock flows.
3. If rolling back to a pre-Vault image, first account for vaulted items becoming visible in regular History.

## Gotchas
- Old pre-Vault app images ignore the `in_vault` flag and can expose vaulted items in normal History/media routes; keep Vault-aware code deployed or restore items before rollback.
- SQLite migration is additive and preserves existing records; encrypted Vault code depends on persistent `data/secret.key`.
- Keep Vault unlock grants in JS memory only; do not put codes or tokens in URLs, logs, or local storage.
- Never read or print `.env` contents.

## Railway cost review — 2026-09-26

### Cost findings
- Project processing polls the pending queue every five seconds. Its prior project loader hydrated the full text-generation trace (raw response up to 1 MiB) and pipeline history for each active project each pass. Large project snapshots and list responses also repeatedly carried that raw response.
- The existing live SSE path emitted full project data on every publish call, including snapshots where only poll timestamps had changed. The same authenticated SSE transport and callback architecture were already present in the working tree; this review optimizes their resource use without claiming to have introduced them.
- Ordinary local video routes used `no-store`, so repeated playback/download requests could transfer the same large file again. Provider-to-Railway video bytes are inbound, not Railway egress. Browser delivery and a future Railway-to-bucket upload are egress.
- Multiple projects could combine in parallel, and FFmpeg thread/filter behavior could expand memory use during concurrent final assembly.

### Architecture and trade-offs
- Keep clips and final videos on the existing persistent application volume so retries, restart recovery, and same-origin authorization continue to work. Provider-to-Railway downloads are inbound traffic. Railway-to-browser video delivery is egress; uploading files to an object store from Railway would also be egress, so a bucket was not selected. The volume continues to retain scene clips and final videos until project deletion.
- Ordinary authenticated media now uses private revalidation with an ETag derived from the item identity, current file path, size, and nanosecond modification time. Playback and download reuse the same media URL. A local Playwright fixture confirmed preview plus download reused one `206` response of 2,275 fixture bytes and downloaded the exact bytes without a second request. This confirms same-client reuse for that tested path; first delivery, revalidation requests, and another client/device still consume Railway egress. Vault media remains `no-store`; a local browser check confirmed download reused the currently open player's blob with one media GET before/after the click and exact bytes/name.
- FFmpeg keeps the existing 1080x1920, medium-preset, CRF 20 output quality. Project combination uses one independent slot, one filter thread, and a four-thread decoder/encoder cap; failed command output keeps only a 4 KiB diagnostic tail. This can lower concurrent combine memory while increasing wait time when several projects finish together.
- The local macOS synthetic five-input benchmark for the selected thread limits measured peak RSS at about 550,000 KiB versus 745,000 KiB for the baseline (~26% lower), and RSS integral at about 4.17M versus 4.91M KiB·s (~15% lower). Wall time rose from 7.05 to 8.04 seconds. This is a local benchmark proxy, not a Railway bill projection or a hard memory ceiling: codec-dependent frame buffers can still affect RSS. Production memory and egress savings have not been measured.
- `PendingProjects` uses a worker-specific project/scenes loader, omitting raw script response and pipeline audit history from recurring worker reads. Project and Vault media authorization use the same lean loader. `Store.Project` and the authenticated project detail API still return the full persisted trace and pipeline records.
- Project list and live SSE snapshots omit the up-to-1 MiB raw script response while keeping trace metadata and pipeline diagnostics. The Raw model response disclosure fetches the complete authenticated detail only on demand; the browser checks project/trace revision and merges only that raw field into current state. This avoids repeat raw-response transfer and allocation while preserving the detail API.
- Cost improvements to the existing SSE path skip database reads and serialization when no clients are connected. A bounded hash-only LRU suppresses duplicate snapshots while ignoring poll-only generation/project/scene `updated_at` fields. Meaningful changes still send the remaining project representation and audit metadata. On queue overflow the server drains stale events and closes the slow client; EventSource refreshes authoritative APIs on every open. Each SSE frame has a 15-second write/flush deadline where supported, and writer errors unsubscribe.
- One fresh native idle-server sample with an empty temporary `DATA_DIR` and no provider credentials showed about 25.7 MiB RSS after two seconds. This was measured locally before the final cost changes and is only a diagnostic proxy, not Railway idle telemetry.

### Implementation and verification status — updated 2026-09-27
- This cost review changed the worker/media loaders, project list/live snapshot projection and lazy trace disclosure, SSE deduplication/writer path, FFmpeg combiner limits, and ordinary-media caching/Vault download reuse. The focused tests cover no-subscriber reads/cache allocation, snapshot fingerprint behavior, Vault filtering, overflow/reconnect, compact live/list data versus the complete detail API, conditional media authorization/ETags, and FFmpeg command/workflow behavior.
- `GOCACHE=/private/tmp/video-automation-gocache go test ./... -count=1` passed in 10.793s after temporary browser fixture removal. `go test -race ./... -count=1` passed on the same unchanged Go source in Terra's earlier full gate. `node --check static/app.js` and `git diff --check` passed after the final browser JavaScript fix and documentation update. Independent code review found no blocking issue in the lean trace projection or per-frame SSE writer changes.
- Local Playwright checks confirmed Vault playback was ready (`readyState=4`) with no media error under the `blob:` CSP; Download reused the one media GET and returned the exact fixture bytes/name. Close cleared the player source and disabled Download; Lock hid Vault content and showed the locked gate. Ordinary preview+download reused one `206` GET for the exact 2,275-byte fixture. No post-login JavaScript or console errors were observed. The raw-trace/status/race browser harness result was not available to this documentation pass and is not claimed here. No commit or deployment was made. After deployment, measure Railway VM memory and egress over comparable active-job windows before claiming realized savings.
- The original Vault and Modal callback architecture remains part of the existing project context; it is not claimed as a new cost change. The note that Obsidian notes and the personal maintenance marker remained untouched applies only to the 2026-09-26 cost-review session.

### Continuation verification — 2026-09-27
- The user explicitly waived further browser/computer-use verification for this continuation. Earlier local Playwright and trace-harness reports are historical evidence and were not rerun here.
- Fresh automated verification on the current working tree passed: `go test ./... -count=1` in 10.185s, `go test -race ./... -count=1` in 61.692s, `node --check static/app.js`, and `git diff --check`. An independent review completed with no blocking issue across the requested cost paths.
- The existing local 8080 fixture was left untouched and no new fixture server was started. No paid provider generation, commit, push, or deployment was performed.

### Commit preparation — 2026-09-27
- The current branch is `feat/modal-callbacks-live-updates-cost-controls`. The user requested the existing changes be recorded in at least 12 meaningful commits and asked that the engineering code-review workflow apply. Local commits are authorized; pushing and deployment were not requested.
- Fresh verification passed: `GOCACHE=/private/tmp/video-automation-gocache go test ./... -count=1` (10.828s), `GOCACHE=/private/tmp/video-automation-gocache go test -race ./... -count=1` (71.837s), the isolated core-foundation snapshot's full Go suite (3.624s), `node --check static/app.js`, `python3 modal/callback_delivery_test.py`, `PYTHONPYCACHEPREFIX=/private/tmp/video-automation-pycache python3 -m py_compile modal/video.py modal/callback_delivery_test.py`, and `git diff --check`. The first normal Go test attempt was blocked by sandbox denial of the test server's ephemeral loopback bind; rerunning with approved local test networking passed. `git check-ignore -v .DS_Store` confirmed the corrected ignore pattern.
- Engineering code review finished with APPROVE and no blockers. The review verified a callback redirect fix: default urllib behavior could replay the callback token after a `302`; the no-redirect opener now stops at the source response, and the local regression confirmed zero requests to the redirect target. Browser checks verified ordinary playback (`readyState=4`) and an exact 2,275-byte download reused one private `206` media request; Vault playback reached `readyState=4` and its exact-byte download reused the protected request. Close removed the source and disabled Download, Lock hid Vault content, raw trace disclosure made one detail request, and delayed detail plus SSE retained the newer processing state. Forced reconnect reopened EventSource and refreshed authoritative APIs. There were no page exceptions. The fixture logged a synthetic completed-project media `404` because that fixture had no final file, and an expected pre-login `/api/session` `401`; neither represents a production-path failure, and the console was not clean.
- All 14 planned meaningful local commits are complete on `feat/modal-callbacks-live-updates-cost-controls`. The user later requested a push, but automatic approval review rejected `git push --set-upstream origin feat/modal-callbacks-live-updates-cost-controls`, requiring explicit authorization for `https://github.com/darkweb19/video-automation.git` and that branch. The push was not executed; no deployment was performed. No paid provider generation or live port 8080 fixture was used. After an authorized deployment, measure Railway VM memory and egress over comparable active-job windows before claiming realized savings.
