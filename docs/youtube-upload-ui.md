# YouTube manual upload UI

The dashboard provides one manual upload composer for a connected YouTube channel. It does not upload automatically, create scheduled posts, or add a separate YouTube navigation page.

## Entry points

The Upload to YouTube action appears only when a finished video is available. It is available from the Generate result for a single clip or project, completed rows in Overview and History, and an unlocked Vault card or Vault player. Each action opens the same dialog with that video preview and source details.

Vault previews are fetched with the current Vault grant and held in a temporary object URL. The grant is added to protected API requests by the existing request helper; it is never copied into page text or upload metadata. Locking the Vault, signing out, restoring a Vault item, or moving a matching History item into the Vault closes the composer and clears its preview. A Vault move is rejected by the service while an active or unresolved upload still depends on local media.

## Channel settings

Settings accepts a Google OAuth web application client ID, a write-only client secret, and the public FrameVault origin. It displays the exact callback URI returned by `/api/youtube/status` for the operator to register with Google. Saved secrets are never read back; leave the secret field blank to retain an existing value.

Connection state and the channel name are loaded from `/api/youtube/status`. The composer refreshes this status before checking the upload history for its source, filters previous uploads to the reviewed channel ID, and remains disabled if either request fails. A channel change while the dialog is open closes it so its metadata and upload history cannot silently transfer to a different channel. The create request includes the reviewed `channel_id`; the service checks that it still matches the connected channel.

## Composer and validation

The composer shows the selected video, channel, editable title and description, an optional Generate title and description action, visibility, made-for-kids selection, and the synthetic-content disclosure. Private is selected by default. The user must explicitly choose whether a video is made for kids. The AI/synthetic disclosure starts checked and remains editable.

Before sending a create request, the browser checks that the trimmed title is nonempty and no more than 100 Unicode code points, the description is no more than 5,000 UTF-8 bytes, neither field contains `<` or `>`, visibility is one of `private`, `unlisted`, or `public`, and made-for-kids is explicitly selected. The service validates the same contract. A failed metadata suggestion leaves the user’s edited values intact.

The browser does not let a pending or completed history lookup be bypassed through a direct form submit. A per-dialog guard prevents duplicate create requests while a request is pending. Only a source without a current-channel upload, or a safely canceled upload with no known video ID or uncertainty warning, can start a normal upload. Project videos honor the selected visibility; private is a default, not a project-only restriction.

## Upload states and actions

| Service state | Dashboard behavior |
| --- | --- |
| `queued`, `initiating`, `uploading` | Show progress and allow cancellation. Poll status only while the composer is open. |
| `processing` | Lock metadata and visibility while YouTube finishes processing. Do not offer another upload. |
| `completed` | Show “Ready on YouTube” and an Open on YouTube link. This state means YouTube processing is finished. |
| `failed` | Offer Retry only when the service has no known YouTube video ID. |
| `needs_reconnect` | Direct the user to Settings. “Abandon upload” stops FrameVault’s tracking; it does not delete a YouTube video. |
| `attention_required` | Link to YouTube Studio and do not offer normal Retry. If no video ID is known, show a separate duplicate-risk recovery panel. |
| `canceled` with a safe outcome | Allow a new manual POST for the same source and channel. |
| `canceled` with `outcome_uncertain` | Keep normal upload disabled. Require the user to open YouTube Studio, check that the video was not uploaded, select the confirmation, then choose Start upload again. |

The rare restart action posts `{ "confirm_not_uploaded": true }` to `/api/youtube/uploads/{id}/restart`. It is available only for `attention_required` or an uncertain canceled record with no known video ID. The service retains resumable upload state and probes it before initiating another upload. The dashboard displays the service’s restart note when present. This explicit action reduces duplicate risk; it cannot guarantee Google has no copy outside the state the service can inspect.

## Accessibility and layout

The composer uses a native dialog with a visible close control, labeled form fields, a required radio choice, live status text, visible keyboard focus, and responsive preview/form layout. It fits narrow screens by stacking the preview above the form and respects reduced-motion preferences for progress animation.
