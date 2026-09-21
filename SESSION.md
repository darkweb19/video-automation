# Session handoff — 2026-09-21

## What was done
- Added a durable 30-second Shorts project workflow: topic to free-model story/script to five independent six-second clips to final MP4.
- Persisted projects, scripts, continuity bible, scene attempts/progress, provider IDs, clip paths, and final-video metadata in SQLite.
- Added authenticated project APIs, a dashboard project view/history, overall and per-scene progress, and failed-scene-only retry.
- Added FFmpeg to the Docker runtime; assembly normalizes five clips to silent 1080x1920 H.264, 30 fps, square pixels, and 30 seconds.
- Kept the existing single-clip flow and protected stored project/video paths.
- Verified gofmt, Go tests/vet, JavaScript syntax, diff whitespace, combiner mocks, and an isolated compiled-server `/health` response.

## Decisions locked
- Script generation uses `openrouter/free`; video scenes default to compatible MiniMax K3 Pro, with a compatible 6-second/9:16 fallback only when unavailable.
- Each project contains exactly five scenes at six seconds; no narration, subtitles, music, YouTube upload, Hermes, or autonomous scheduling.
- Retries of provider creation are explicit per scene; download failures retry durably without resubmitting a paid generation.
- The final output is served from the authenticated dashboard at `/api/projects/{id}/video`.

## Open questions
1. Sujan: configure a valid OpenRouter key and perform a paid end-to-end generation after reviewing the selected model price.
2. Sujan: visually verify the dashboard in Docker after rebuild; no browser UI session was available here.

## Next steps
1. Rebuild the Docker app from current `main`, then open the dashboard and set the OpenRouter API key.
2. Confirm MiniMax K3 Pro appears as the selected compatible default and submit a real topic.
3. Check the finished vertical MP4 in YouTube Shorts before considering an optional future upload integration.

## Gotchas
- Local FFmpeg and Docker are not installed; FFmpeg is installed in the Dockerfile and assembly behavior is covered with command-level mocks.
- The available OpenRouter balance was not inspected and no paid API call was made.
- `openrouter/free` availability can vary; a script-generation failure remains retryable at the project level.
- Use workspace-local `GOCACHE=.gocache` and `GOMODCACHE=.gomodcache` for Go checks on this machine.
