---
version: alpha
name: FrameVault
description: A quiet video production workspace that keeps creation, analysis evidence, and saved work clear.
omitted:
  - section: colors
    reason: "Runtime colors are owned by internal/webui/static/styles.css; this document records their roles without creating a second token source."
  - section: typography
    reason: "The system font stack is owned by internal/webui/static/styles.css and is referenced below."
  - section: rounded
    reason: "Control, preview, and panel radii are owned by the existing runtime CSS."
  - section: spacing
    reason: "Spacing remains component-specific in the established CSS; no global spacing scale is defined."
  - section: components
    reason: "Plain HTML and the existing stylesheet are canonical; no separate component-token layer exists."
---

# FrameVault Design Context

## Overview

### Creative North Star

FrameVault should feel like a quiet production workspace: the material is the user's video and the evidence attached to it, while the interface stays out of the way. The existing monochrome palette, compact controls, white work surfaces, and modest borders provide familiarity across the dashboard. Clipping keeps model-ranked proposals readable as an editorial record with source-time evidence rather than suggesting that remote media is already available locally.

### Product context and register

- **Audience and primary job:** Creators and channel operators use the authenticated dashboard to create videos, review saved work, and analyze a public YouTube reference.
- **Target market(s) and evidence:** The product is an English-language web dashboard. No market-specific user research is recorded; do not infer regional requirements from the deployment host.
- **Locale(s) and language policy:** Dashboard copy is English. Use the platform system font stack and preserve Unicode in user and model text; do not add a remote font dependency.
- **Usage scene:** Browser-based work on desktop and mobile, including narrow screens where actions and long evidence text must remain reachable.
- **Register:** Product UI throughout. Task clarity, familiar controls, and truthful status take precedence over promotional expression.
- **Memorable signature:** Creation formats use small frame sequences tied to real output shapes; Clipping uses ranked, source-time proposals with their evidence and limitations.
- **Restraint:** Keep workspace pages left-aligned, media and analysis states factual, and technical detail behind deliberate disclosure. Avoid invented thumbnails, metrics, or local-media affordances for remote references.
- **Anti-references:** Marketing hero layouts, decorative gradients, ornamental metric cards, remote font loading, and technical traces as the primary reading surface conflict with the established production-workspace direction.
- **Token ownership/runtime mapping:** `internal/webui/static/styles.css` is the canonical runtime source for colors, font stack, and shared radii. Feature styles in `internal/webui/static/*.css` consume its custom properties. `DESIGN.md` records intent and role names; it does not generate or duplicate runtime values. Review drift against the stylesheet and `docs/frontend-redesign.md`.

## Colors

The runtime uses a monochrome hierarchy. `--ink` carries primary text and the main action; `--white` is the work surface; `--cloud` separates the page canvas and quiet notices; `--rule` marks controls and dividers; `--muted` supports secondary copy; and `--graphite` carries supporting emphasis. These semantic roles are defined in `styles.css`, which owns their exact values. Keep focus visible and keep status understandable through words and shape as well as color. The current dashboard is a light theme; do not introduce a feature-only palette.

## Typography

Use the system sans stack defined by `styles.css`. Page titles are broad but restrained, section headings are compact, and captions or timestamps use smaller text with tabular numerals where available. Preserve user and model text without truncating its meaning; allow long evidence and identifiers to wrap. Do not add a font package or remote font request for a feature.

## Layout

Keep content left-aligned in a natural document flow. Use the existing dashboard shell and panel rhythm, with responsive grids that collapse into one column on narrow screens. Preserve readable control spacing and stable geometry while data loads. Clipping's options and primary action belong together; saved references and analysis jobs remain separate readable lists. Never impose a viewport-height or hidden-overflow rule on the shared page to constrain a feature panel.

## Elevation & Depth

Hierarchy comes from the canvas, white panels, and thin borders rather than shadows, blur, or gradients. Dialog backdrops are reserved for app-owned modal actions. Static content should not gain extra elevation to make a feature feel more prominent.

## Shapes

Use the established control, preview, and panel radii from `styles.css`. Borders are quiet and consistent. A result proposal may use a simple divider to separate ranked entries; avoid decorative pills and containers that make estimates look like measured guarantees.

## Components

### Foundational visual states

Use the existing button, field, panel, badge, focus, and reduced-motion rules in the shared stylesheet. Async work keeps the control dimensions stable, announces a truthful status, and never uses a guessed percentage. Disabled actions retain their reason in nearby copy where it is not obvious.

### Buttons and actions

Use one primary action for the current form decision and secondary buttons for refresh or navigation. Use explicit verbs. Destructive actions are separated from routine actions and confirmed in an accessible app-owned dialog.

### Navigation and data display

Keep existing dashboard navigation and list semantics. Show saved records as readable stacked rows at narrow widths. A Gemini result preserves the model's rank and order; its times, score estimates, evidence, and limitations are labeled as proposals or model-reported values.

### Forms and overlays

Use native labeled controls where their platform behavior is accepted, app-owned inline validation, and modal `<dialog>` behavior for destructive confirmation. Keep descriptions, correction guidance, and the least destructive initial focus available to keyboard users.

### Iconography

Use the existing inline SVG icon style only where a symbol improves recognition. Important actions retain text labels; do not add an icon library.

### Motion

Motion is limited to existing short state transitions. Respect `prefers-reduced-motion`; no polling animation or repaint loop is part of a job state.

### Content and data visualization

Use plain English, sentence case, and direct action labels. Do not present editorial scores as probabilities, model-reported duration as independently measured media metadata, or a remote reference as a playable local file.

## Do's and Don'ts

- **Do:** Reuse the established shell, CSS variables, system font stack, focus treatment, and same-origin API path.
- **Do:** Keep legacy saved results readable while adding the typed Gemini result.
- **Don't:** Add a new framework, font dependency, or screen-local token palette.
- **Don't:** Imply that an analysis request downloaded, inspected, or verified the source beyond the saved provider status.
