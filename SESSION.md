# Session handoff — 2026-09-25

## What was done
- Redesigned History with responsive three-column generation and project cards, accessible process logs, and saved-event counts.
- Reorganized Settings while preserving API behavior and write-only credential fields.
- Added and embedded `static/history-settings.css`; both Go handlers serve it from the same origin.
- Added static-asset coverage for the new stylesheet in both handler paths.
- Verified `go test ./...`, `go vet ./...`, `node --check static/app.js`, formatting, and diff checks. No paid generation was run.

## Decisions locked
- History logs display persisted job events and status colors; they do not use sample or invented events.
- Keep the existing light workspace and use the dark event console as the History accent.
- Keep the 30-second project preset at five six-second 9:16 scenes.
- API credentials stay encrypted and are never returned to the browser.
- OpenRouter remains the story and script provider.

## Open questions
1. Sujan: after deployment, does Random Prompt succeed on the first click in the 30-second tab?
2. Sujan: do generated Mature Content project scenes preserve the intended wardrobe and sensual action?

## Next steps
1. Review History and Settings in a browser at desktop and mobile widths; no browser was available during this implementation.
2. Deploy through the normal release path and verify Random Prompt on the 30-second tab.
3. Check Mature Content in project and single modes, then rotate the OpenRouter key previously identified for rotation.

## Gotchas
- The app serves embedded static files through explicit routes; new static assets need an embed entry and route in both `handlers.go` and `dashboard.go`.
- Local Go builds need `GOCACHE=.gocache` and `GOMODCACHE=.gomodcache` in this environment.
- Do not run paid provider generation as a routine check or read/print `.env` contents.
