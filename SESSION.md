# Session handoff — 2026-10-06

## What was done
- Diagnosed job `gen_954e9ffbe0f220ddddbda41a2edb361f` callback rejection; the callback transport fix merged and pushed to main as `f34c12a`.
- Added `FrameVault-Modal-Callback/1.0` User-Agent to Wan and SkyReels legacy and bearer callbacks, with regression coverage for all four request paths. Live probes with this identity avoid Cloudflare 1010 on the custom domain, but the header alone does not solve Railway's browser challenge.
- Sujan deactivated Under Attack Mode. Afterward, production Railway `/health` returned 200 with the default Python User-Agent. A callback-shaped POST for job `gen_954e9ffbe0f220ddddbda41a2edb361f` with a dummy bearer token returned Go 404, "callback job not found". The same callback via the custom domain with the FrameVault User-Agent returned Go 404; a custom-domain diagnostic callback with default Python User-Agent still received Cloudflare 403/error 1010.
- These probes establish routing to Go for those requests, not successful authenticated delivery. The original job is currently absent; the reason is unknown. No paid generation or deployment occurred.

## Decisions locked
- Keep Go + SQLite and production state/media on the service's own `/data` volume.
- Production and Development have independent state. Never send production callbacks to Development or another service's volume.
- The prior merge included `62726dc`, which stops Wan before inference when its initial callback is rejected; it did not change the callback origin, routing, bearer headers, or transport.
- Keep bearer authentication, redirect protection, bounded retries, and stop-before-generation behavior intact.
- Under Attack Mode is currently deactivated on the production service by Sujan. Do not add a callback relay; direct production-origin routing is now available and simpler.

## Open questions
1. Sujan: backup frequency, retention, and recovery objectives remain undecided.

## Next steps
1. Set production `VIDEO_CALLBACK_BASE_URL` to `https://video-automation-production-27b3.up.railway.app`, apply/redeploy the production app, and verify callback delivery before generation. This configuration has not been applied.
2. Start a fresh generation attempt after routing is configured; the original job is absent and persisted callback URLs do not change when the setting changes. If retaining the custom callback domain, deploy both workers with the merged header fix first.
3. Complete Railway persistence and YouTube upload smoke checks from the prior handoff.

## Gotchas
- Header fix `f34c12a` is on main. A default Python callback to the custom domain still receives Cloudflare 403/error 1010; explicit FrameVault requests reach Go. Production Railway `/health` now returns 200 and callback-shaped probes return Go 404 for the absent job. No authenticated callback success has been verified.
- Development `https://development-branch-development-60bf.up.railway.app` returns health 200 and callback 404 for both a diagnostic job and the supplied failed job; it is a separate service/volume and must not be used for production callbacks.
- Verified: full Go suite in escalated execution, Python callback regression suite, dashboard JS syntax, and `git diff --check`. Default-sandbox full Go checks fail on cache/temp-path access.
- No paid inference, provider credential access, or deployment was performed. The local Modal profile lists only the deployed Wan `video-generation` app; SkyReels deployment target is unverified.
- Existing `workers/modal/__pycache__/` remains untracked and untouched. Never read `.env` or expose credentials; back up SQLite and `secret.key` together.
