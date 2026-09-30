# Session handoff — 2026-09-30

## What was done
- Added random project/single prompt generation using five shuffled OpenRouter `:free` text models, bounded to 12 seconds per attempt and 70 seconds overall, with mocked fallback/error tests.
- Added authenticated Modal completion callbacks for new jobs, durable same-ID claims/recovery, callback retries, and legacy polling compatibility across Go, Wan, and SkyReels workers.
- Capped local single/project-scene downloads at five attempts with persisted actionable failure state and scene retry tests.
- Updated root and worker documentation for user setup, callback rollout, runtime capabilities, and developer checks.
- Verified Go tests/vet/build, gofmt, both Python offline suites (Wan 19; SkyReels 23), JavaScript syntax/UI (11), and `git diff --check`.
- Independent review cleared both Modal callback workers with no remaining findings.
- Created local implementation commit `1fb9ad1` and documentation commit `78cd0ff`; nothing was pushed.

## Decisions locked
- The 30-second project remains five six-second 480p 9:16 scenes; OpenRouter remains the story/script provider.
- Random prompt fallback is limited to the five documented free OpenRouter text models; there is no paid fallback.
- New Modal jobs use callbacks; OpenRouter and legacy Modal jobs retain polling. Unknown/ambiguous Modal dispatch acknowledgments remain pending and are never resubmitted under the same job ID.
- Callback delivery requires a public HTTPS dashboard origin in `VIDEO_CALLBACK_BASE_URL`; deploy updated callback-capable workers before Go submits callback fields.
- No live provider calls, paid generation, deployment, or push occurred.

## Open questions
- None.

## Next steps
1. For an authorized rollout, run preflight, deploy both Modal workers, configure the public HTTPS callback origin on the Go host, then update Go.
2. Do not push or deploy without explicit authorization.

## Gotchas
- Set `$env:GOCACHE = Join-Path (Get-Location) '.gocache'` before Go checks in this PowerShell workspace.
- `VIDEO_CALLBACK_BASE_URL` is optional at startup and for OpenRouter, but required for Modal submissions; it must be a public HTTPS origin without path/query/fragment/userinfo.
- Preserve the pre-existing untracked `modal/__pycache__/`; generated caches and ignored `.gocache` are not part of the planned commits.
- Never read or print `.env` contents or credentials. No live generation/deployment has been tested.
