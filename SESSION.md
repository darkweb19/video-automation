# Session handoff - 2026-09-25

## What was done
- Redesigned the History and Settings views while preserving the existing API behavior.
- Added a per-card process log disclosure for individual generations and 30-second projects.
- Completed logs start collapsed; active and failed logs start open. Manual choices persist while History refreshes.
- Committed the disclosure as eea6d81 (feat: collapse history process logs).
- Verified go test ./..., go vet ./..., node --check static/app.js, and git diff --check.

## Decisions locked
- History shows persisted job events only; event text remains safely rendered as text.
- Keep the light dashboard and dark process console, with three cards per row on desktop.
- Keep the 30-second project preset at five six-second 9:16 scenes.
- API credentials stay encrypted and are never returned to the browser.
- OpenRouter remains the story and script provider.

## Open questions
1. Sujan: after deployment, does Random Prompt work on the first click in the 30-second tab?
2. Sujan: do generated Mature Content project scenes preserve the intended wardrobe and sensual action?
3. Sujan: can the previously identified OpenRouter key be rotated?

## Next steps
1. Review History disclosure behavior at desktop and mobile widths in a browser.
2. Deploy through the normal release path and verify Random Prompt on the 30-second tab.
3. Check Mature Content in project and single modes, then rotate the OpenRouter key.

## Gotchas
- Embedded static assets are served through explicit Go routes; new files need an embed entry and route.
- Use absolute GOCACHE and GOMODCACHE paths in this environment; Go rejects a relative GOMODCACHE.
- Do not run paid provider generation as a routine check or read/print .env contents.
