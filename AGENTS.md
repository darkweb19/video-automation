# Project instructions

- This application is a Go HTTP server with an embedded static dashboard. Keep browser code in `static/` and preserve the existing same-origin authenticated API pattern.
- Video generation is selected through persisted application settings. Implement providers behind `VideoService`; keep story/script generation on OpenRouter and provider-specific request handling inside provider clients.
- Treat model capabilities as runtime data. Populate and consume `VideoModel` fields for duration, resolution, aspect ratio, audio, and pricing instead of spreading provider rules through the UI or processor.
- API credentials must be encrypted at rest and never returned to the browser. Settings responses may expose configured booleans and non-secret configuration only. Never log credentials or read/print `.env` contents.
- Keep the 30-second project preset at five six-second 9:16 scenes unless the product requirements change. Reject incompatible models clearly.
- Persistent application data lives in SQLite and the configured data directory/Docker volume. Changes to settings storage must remain restart-safe and preserve existing records.
- Useful local checks: `go test ./...`, `node --check static/app.js`, and `git diff --check`. Do not run paid provider generation as a routine check.
