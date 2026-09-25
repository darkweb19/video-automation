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
