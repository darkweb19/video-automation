# Session handoff — 2026-10-06

## What was done
- Diagnosed job `gen_954e9ffbe0f220ddddbda41a2edb361f` callback rejection on branch `fix/modal-callback-403`, created from main `0f2d96b`.
- Confirmed `https://video.sujanshrestha.ca` returns Cloudflare HTTP 403/error 1010 for Python urllib's default client identity, before Go handles the request.
- Added `FrameVault-Modal-Callback/1.0` User-Agent to Wan and SkyReels legacy and bearer callbacks, with regression coverage for all four request paths.
- Updated callback troubleshooting guidance. Bearer authentication, redirect protection, bounded retries, and stop-before-generation behavior remain intact.

## Decisions locked
- Keep Go + SQLite and production state/media on the service's own `/data` volume.
- Production and Development have independent state. Never send production callbacks to Development or another service's volume.
- The prior merge included `62726dc`, which stops Wan before inference when its initial callback is rejected; it did not change the callback origin, routing, bearer headers, or transport.
- Keep the dashboard custom domain. If its gateway blocks machine callbacks, use the same production service's Railway-generated HTTPS origin for `VIDEO_CALLBACK_BASE_URL`.

## Open questions
1. Sujan: provide the production service's Railway-generated domain from Settings → Networking. The supplied Development URL does not contain the failed job.
2. Sujan: backup frequency, retention, and recovery objectives remain undecided.

## Next steps
1. Configure the correct production callback origin and redeploy the configured Modal workers with the header fix; verify receiver reachability before paid generation.
2. Retry the failed generation/project scene after configuration is verified. Callback URLs are persisted per job; environment changes only affect new attempts.
3. Complete Railway persistence and YouTube upload smoke checks from the prior handoff.

## Gotchas
- Live probes: the custom domain's default Python identity receives Cloudflare 403/error 1010; the explicit FrameVault identity receives 429. Removing 1010 does not establish callback acceptance.
- Development `https://development-branch-development-60bf.up.railway.app` returns health 200 and callback 404 for both a nonexistent diagnostic job and the supplied failed job.
- Verified: full Go suite in escalated execution, Python callback regression suite, dashboard JS syntax, and `git diff --check`. Default-sandbox full Go checks fail on cache/temp-path access.
- No paid inference, provider credential access, or deployment was performed. The local Modal profile lists only the deployed Wan `video-generation` app; SkyReels deployment target is unverified.
- Existing `workers/modal/__pycache__/` remains untracked and untouched. Never read `.env` or expose credentials; back up SQLite and `secret.key` together.
