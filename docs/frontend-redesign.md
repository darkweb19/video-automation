# FrameVault frontend redesign

## Brief

FrameVault is a self-hosted video workspace. The main job is to start a single clip or a fixed 30-second project, follow its progress, then organize completed videos in History or the protected Vault. The visual direction is professional, monochrome, and inspired by Apple's clear typographic hierarchy and generous spacing.

## Critique of the previous interface

The dashboard used a generic purple SaaS-card style: colored metric icons, repeated rounded panels, soft shadows, and an oversized two-column generator. The dark job console introduced a separate visual language. Four equal-weight metric cards competed with the primary create action, while dense settings and history surfaces had inconsistent treatments.

## Design system

| Token | Value | Use |
| --- | --- | --- |
| Ink | `#000000` | Primary text, actions, selected navigation, and the video stage |
| White | `#FFFFFF` | Main surfaces and text on black |
| Cloud | `#F5F5F7` | Quiet grouped surfaces and selected navigation |
| Rule | `#D2D2D7` | Dividers, control borders, and outlines |
| Muted | `#6E6E73` | Secondary copy and metadata |
| Graphite | `#3A3A3C` | Emphasis on secondary content |

Typography uses the platform system sans stack, with compact sentence-case labels, large page and hero headings, and readable body copy. Controls use an 8px radius, video previews 14px, and panels 18px. Primary actions are black. Focus is visibly outlined; motion follows the reduced-motion preference. Toasts remain white cards.

## Layout

The dashboard keeps a compact left rail and a sticky page header. On narrow screens, the rail becomes a drawer below the header so its toggle stays reachable. Content uses a restrained maximum width and collapses to one column at tablet widths.

Content is left-aligned throughout. Page titles, headings, labels, and form fields share the same reading edge; short empty-state messages and the Vault gate use centered copy.

```text
Overview
+------+  Workspace / Overview                         account
| rail |  + black creation lead ----------------+
|      |  | Turn a topic into a video.          |  project facts
|      |  +-------------------------------------+
|      |  live metrics in a divider strip
|      |  Recent generations as a clean list

Generate
+------+  Generate
| rail |  + format / idea / model / options ----+
|      |  | estimate and Generate action        |  status + preview
|      |  +-------------------------------------+
```

The overview's black feature panel is the single strong visual element. Its project facts describe the real fixed preset. The generator status panel uses a neutral empty state and a black preview stage; it displays real output after a job starts. History, Vault, and Settings reuse the same typography, spacing, borders, and status language.

## Product and interaction decisions

- Keep the 30-second project preset at five six-second scenes, each generated at 480p and 9:16. The existing FFmpeg pipeline joins them into a 30-second 1080×1920 MP4.
- Populate model, duration, resolution, aspect ratio, audio, and pricing details from runtime model capabilities. The interface only states the fixed project requirements and relies on the existing capability validation.
- Label dashboard cost as provider-reported. Do not imply a price where the provider supplies none.
- Keep the existing IDs, forms, data attributes, and same-origin authenticated API flow. Keep model-specific request logic and provider selection behavior outside the visual layer.
- Keep credentials out of browser responses. Preserve the Vault's in-memory grant behavior and lock on refresh or explicit lock.
- Use status text, neutral fills, and borders to distinguish job states. Do not use decorative semantic colors, gradients, fake thumbnails, or sample generation data.
- Preserve history logs and project audit details while presenting them in the same light, readable interface as other screens.

## Verification

- Run `go test ./...`, `go vet ./...`, `node --check static/app.js`, `node --test tests/dashboard-ui.test.cjs`, and `git diff --check`.
- The Node regression suite executes the application state handlers with a small DOM stub. It checks pending submissions, format changes, submission errors, project compatibility, stored result visibility, and mobile navigation accessibility. It does not verify rendered layout.
- The HTTP smoke check starts a compiled server with a fresh temporary SQLite directory and no provider key. It checks the embedded assets, login/password gate, non-secret settings, History endpoints, and Vault setup, unlock, lock, and logout.
- The existing desktop visual direction is retained. Browser access was unavailable during this continuation, so fresh desktop/mobile screenshots and visual approval remain outstanding.
- Test model capabilities and submission messages are synthetic fixtures used only by the regression suite. No sample videos or invented usage/pricing data are added to the dashboard.
- No paid provider generation is part of UI verification.

## Continuation review

Keep the existing monochrome structure rather than introducing a new template. The refinement pass restores sentence-case project labels, resolves keyboard-focus cascade conflicts, and makes narrow-screen project facts, metadata, and History logs wrap without truncation. Submission feedback stays associated with its selected format, and model updates cannot enable a second submission while the first request is in flight.
