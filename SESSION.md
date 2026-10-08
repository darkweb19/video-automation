# Session handoff — 2026-10-08

## What was done
- Reused `assets/logo.png` byte-for-byte at `internal/webui/static/logo.png`, served publicly as `/static/logo.png` by both HTTP handlers, and embedded it in the dashboard.
- Added the logo to the login and sidebar brand surfaces, favicon, and Apple touch icon; added HTTP coverage for both routes and favicon markup.
- Independent review approved. Final checks passed: `go test ./...` (21.542s for `internal/app`), 87/87 dashboard tests, JavaScript syntax, Python callback regression, and `git diff --check`.
- Pushed feature commits `49d1969` and `1558b79` to `main`; verified `origin/main` at `1558b7921298d2f95c34d152a5150e755b4605b4`.

## Decisions locked
- Keep Go + SQLite, embedded plain JavaScript/CSS, same-origin authenticated APIs, EventSource updates, and local state/media on one `/data` volume. No new framework or frontend dependency.
- Keep using the supplied monochrome logo file unchanged; no generated derivatives or additional image dependencies.
- Preserve the studio's quiet light surfaces, clear creation formats, and closed technical disclosures. Do not invent previews, usage totals, or unavailable provider pricing.
- Activity totals count clips; recent work includes real projects. History filters loaded records, older-clip pagination remains, video pricing is runtime data, and story planning cost is separate.
- Projects remain five sequential six-second silent 480p scenes at 9:16, joined into a silent 1080×1920 output. Keep provider, Vault, YouTube, and callback authorization boundaries.
- Production and Development have separate state and volumes. Keep encrypted credentials and `secret.key` with backups. Preserve bearer callbacks, redirect protection, bounded retries, and stop-before-inference behavior.
- Callback fix `f34c12a` is on `main`. Sujan deactivated Under Attack Mode on 2026-10-06; no callback relay is selected.

## Open questions
1. Sujan: backup frequency, retention, and recovery objectives remain undecided.

## Next steps
1. Review the logo and studio screens at desktop and mobile widths in a rendered browser when the Windows browser runtime can launch.
2. Set production `VIDEO_CALLBACK_BASE_URL` to `https://video-automation-production-27b3.up.railway.app`, redeploy through the approved workflow, and verify authenticated callback delivery before generation. This configuration has not been applied. If retaining the custom callback domain, deploy both workers with the merged header fix first.
3. After routing is verified, use a fresh generation and complete the prior Railway persistence and real YouTube upload smoke checks.

## Gotchas
- Browser automation could not start because the Windows browser runtime failed before launch; rendered layout remains unverified.
- Production routing remains unverified. Probes on 2026-10-06 reached Go but returned 404 for absent job `gen_954e9ffbe0f220ddddbda41a2edb361f`; authenticated delivery remains unverified. Default Python requests to the custom domain received Cloudflare 403/1010 that day.
- Development uses a different service and volume; never direct production callbacks there. The local Modal profile previously showed Wan only; the SkyReels deployment target remains unverified.
- No paid generation, provider credential access, deployment, or `.env` read occurred. Existing `workers/modal/__pycache__/` remains untracked and untouched.
