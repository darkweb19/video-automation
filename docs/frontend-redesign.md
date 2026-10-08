# FrameVault studio redesign

## Product intent and critique

FrameVault turns an idea into a single clip or a fixed five-scene story, then preserves the result and its production history. The existing monochrome interface is visually consistent, but its oversized promotional banner, four equally weighted metrics, ambiguous format switch, category slider, and exposed technical traces distract from that job. History has no discovery controls, and provider setup competes with account security in a single settings grid.

The redesign makes FrameVault feel like a quiet production workspace: choose what to make, see the cost and output before starting, follow the story as it develops, and find finished work quickly. The primary product decision is to retain both existing formats and make their differences explicit. Pipeline and retry evidence remain available through disclosures rather than occupying the main result surface.

## Design direction

| Token | Value | Role |
| --- | --- | --- |
| Ink | `#000000` | Typography and primary action |
| Paper | `#FFFFFF` | Work surfaces |
| Canvas | `#F5F6F7` | Studio background |
| Line | `#D7DBDE` | Controls and dividers |
| Slate | `#59636C` | Secondary copy |
| Graphite | `#28313A` | Supporting emphasis |

Use the installed system sans family with a broad page title, restrained headings, tabular data, and compact supporting text. No remote fonts, image placeholders, gradients, or ornamental metric cards. Radius follows purpose: compact controls, larger preview surfaces. White cards and modest borders keep notifications quiet.

```text
Studio         Workspace title                 account
navigation     What would you like to make?
               [Five-scene story] [Single clip]
               Actual activity strip
               Recent work, media first

Create         Format choices
               Idea + category        Production / result
               Model + output         Story / five scenes
               Estimate + action      Details disclosure

Library        Search + status + format
               Results summary
               Responsive media grid
```

All content is left aligned, except bounded authentication and Vault gate states. The creation format choices are the memorable element: their small frame sequences describe the real output rather than suggesting generated assets.

## Execution and acceptance

1. Establish tokens, shell, navigation, login, skip link, connection state, focus and motion rules.
2. Replace the overview banner with descriptive creation choices and actual workspace activity.
3. Add History discovery controls and refine media browsing, empty states and the Vault gate.
4. Rebuild creation hierarchy around format, idea, category, model, output, estimate and action.
5. Bring preview and scene progress forward; retain the story summary area and move technical logs into native disclosures.
6. Group Settings by generation, publishing and security; refine YouTube and narrow layouts.

Implement and review these as six coherent visual commits. Interaction changes and regression tests are separate commits. Preserve DOM IDs and API hooks; coordinate additional hooks before changing them. No new frontend framework or dependency is justified for this embedded Go dashboard.

The fixed project remains five sequential six-second 480p scenes at 9:16 without generated audio, joined into silent 1080×1920 output. Model capabilities and prices come from runtime data. Retain same-origin authenticated requests, EventSource updates, encrypted credentials, Vault grants and local media authorization. Never imply unavailable pricing or invent usage metrics, thumbnails or results.

Verify JavaScript syntax and dashboard regression tests, Go tests, Python callback tests and whitespace checks. Review widths down to 320px, keyboard focus, labels and reduced motion in source. Browser access is unavailable in this environment; do not claim rendered screenshots or visual approval. No paid generation or deployment is part of verification.


## Implementation outcome

Six reviewed visual phases replace the dashboard shell, overview, library and Vault, creator, production result, and Settings/publishing surfaces. The existing embedded asset paths and Go architecture are retained without new dependencies.

- Overview leads with distinct story and clip choices. Activity totals are explicitly scoped to clips; recent work combines real project and clip records.
- Creation separates the idea, production setup and final action. A native category select preserves the existing category contract. Output and cost descriptions distinguish the fixed story format, runtime video pricing and separate story-planning charges.
- The result places preview and scene sequence before closed technical disclosures, with a dedicated story summary area. Audit IDs, retry controls and trace loading remain available.
- History searches and filters loaded work, keeps older-clip pagination available, and retains media and focused actions through unrelated live updates. Vault retains its additional authorization boundary and deliberate media loading.
- Settings groups Generation, Publishing and Security with native section links. The actual required-password notice suppresses unrelated setup groups so the password action appears first. YouTube separates video details from audience and visibility review.
- CSS is consolidated around common tokens and purposeful surfaces. Focus, reduced motion, wrapped metadata, radio and checkbox sizing, portrait video containment and narrow-screen grids are covered. The five-frame format illustration uses smaller frames on narrow screens.

During implementation, JavaScript syntax and 55+ dashboard regressions passed. Source checks verified unique DOM IDs, matching JavaScript hooks, balanced HTML structure, no nested forms, balanced CSS braces and clean whitespace. Regression fixtures are test data only; no sample videos, usage metrics or pricing were introduced into the product.

Browser access was unavailable. The responsive and accessibility review is based on source and layout constraints down to 320px; rendered desktop/mobile screenshots and visual approval remain outstanding. No paid generation or deployment was run for this work. Final repository-wide verification is recorded in the session handoff.
