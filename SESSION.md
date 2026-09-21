# Session handoff — 2026-09-21

## What was done
- Delivered the durable project pipeline and generation audit trace in `3158fb0` (`feat: expose permanent generation pipeline trace`).
- Added README documentation for prompts, schema, raw response, model metadata, normalized outputs, events, timestamps, and retries in `c6c4ea2` (`docs: document generation trace`).
- Current verification: `go test ./...`, `node --check static/app.js`, and `git diff --check` pass.
- No paid generation was performed; browser visual QA was unavailable.

## Decisions locked
- Script generation uses `openrouter/free`; video scenes use the selected compatible six-second vertical model.
- Every project has five independent six-second scenes and a combined 30-second vertical MP4.
- The trace is an operational process/audit record, not hidden chain-of-thought or private reasoning.
- Failed scenes can be retried without regenerating successful scenes; final video remains authenticated.

## Open questions
1. Sujan: configure a valid OpenRouter key and review the selected model price/balance.
2. Sujan: perform a paid end-to-end generation after browser visual QA.

## Next steps
1. Rebuild the Docker app from current `main` and open the dashboard.
2. Verify the pipeline timeline and collapsed generation details in a browser.
3. Submit one low-cost project and inspect the resulting vertical MP4.

## Gotchas
- Local Docker and FFmpeg are unavailable; FFmpeg is installed in the Docker image.
- `openrouter/free` availability can vary, so text-generation failures remain retryable.
- Use workspace-local `GOCACHE=.gocache` and `GOMODCACHE=.gomodcache` for Go checks.
- Back up `/data/secret.key` with the database; it is required to decrypt the stored API key.
