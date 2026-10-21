# Non-Project Sessions

Start a durable conversation without a Project, then rename, archive, and restore that Session. The empty-data baseline proves navigation and blocked launch controls only. A real Session needs an approved Runtime launch configuration.

## Sub-features

- `session-entry`: sidebar, empty-state, and direct routes open the new Session form.
- `session-archive-view`: open and archived Session lists have separate routes.
- `session-launch-real`: Preflight creates a Session whose Runtime response survives reload.
- `session-lifecycle`: rename, archive, and restore preserve the same Session identity.

## How to get to it (user POV)

- Choose `Non-project` in the sidebar, or open `/sessions`.
- Choose the sidebar `New session` icon or its empty-state `New session` link; both reach `/sessions#new-session`.
- Choose `Archived Sessions` on the Session home page, or open `/sessions/archived` directly.
- Choose a Session row to open its conversation. Its row actions provide Rename and Archive; an archived row provides Restore.

## Driving it with agent-browser

Preconditions: Doctor passes. Begin with empty Session data and no Model Provider. The real-launch branch requires a separately approved Model Provider credential and prepared Runtime/Runner; do not use the fake offline provider fixture.

- **Main entry.** Run `ab click '#workspace-sidebar a[href="/sessions"]'` on the initial empty sidebar, then `ab wait '#session-initial-input'`. Require `New session` and `Open sessions`. Repeat with `ab open "$UI/sessions"` for direct navigation.
- **New-session entries.** Run `ab click '#workspace-sidebar a[aria-label="New session"]'` and require the new Session form at `/sessions#new-session`. Return to `/sessions`, then run `ab click '#workspace-sidebar a[href="/sessions#new-session"]:not([aria-label])'` for the empty-state link. Require the same form. Also test `ab open "$UI/sessions#new-session"` when direct hash navigation is relevant.
- **Archived view.** Run `ab find role link click --name 'Archived Sessions' --exact`. Require the `Archived Sessions` heading and no launch form. Save `ab snapshot > "$EVIDENCE/sessions-archived-empty.txt"`. Open `"$UI/sessions/archived"` directly and require the same result. Use `ab find role link click --name 'Back to open Sessions' --exact` to return.
- **Baseline launch boundary.** Run `ab fill '#session-initial-input' 'Reply with verification-ready. Do not use tools or access the network.'`. Require `No compatible providers` in `#launch-model-provider`, a disabled `Launch session` button, and `ab is checked '#launch-host-browser'` to report false. Save `ab screenshot "$EVIDENCE/session-launch-blocked.png"`. Record `session-launch-real` and `session-lifecycle` as blocked by missing real launch prerequisites, not passed by this empty-state check.
- **Real launch, only with approved prerequisites.** In a separately approved run, choose actual Runtime, Model Provider, model, and prepared Sandbox Runner values in `#launch-runtime`, `#launch-model-provider`, `#launch-model`, and the Runner controls. Record the option values from the snapshot. Keep Host Browser off. Save the completed form, then run `ab find role button click --name 'Launch session' --exact`. Require a `/sessions/` detail route and the visible `verification-ready` response. A failed Preflight remains a failed journey with its visible reason.
- **Retain identity and response.** On that detail route, run `SESSION_URL=$(ab get url)` and `ab eval 'fetch("/api/sessions/" + location.pathname.split("/")[2]).then(r => { if (!r.ok) throw new Error(String(r.status)); return r.json(); })' > "$EVIDENCE/session-created.json"`. Save the conversation snapshot and screenshot. Reload, and require the same Session and retained response.
- **Rename.** Return to `"$UI/sessions"`. From the Session row snapshot, use the `Rename` button for the Session just created. Fill its visible rename textbox with `Verification Session`, then run `ab find role button click --name 'Save name' --exact`. Require `Open Verification Session session` and capture the saved row. The row's accessible name for its rename action now becomes `Rename Verification Session`.
- **Archive and restore.** Wait until the Runtime is no longer busy. Run `ab find role button click --name 'Archive Verification Session' --exact`. Require the row to leave Open sessions. Open `"$UI/sessions/archived"` and require that row with the same Session URL. Save a screenshot, then run `ab find role button click --name 'Restore Verification Session' --exact`. Return to `"$UI/sessions"`; require the same row and URL. Reopen it, reload, and require the retained response. Save a second GET response using the same read-only command into `"$EVIDENCE/session-restored.json"` and require the same ID, title `Verification Session`, and open lifecycle.

## Gotchas

- Non-Project Mode has no Project Scope. This does not authorize security testing. Keep the verification goal limited to the fixed response and no tools.
- A new Session defaults to Disabled Blackboard Mode; Task Launch defaults differ. Do not change the mode merely to make a shared recipe pass.
- The sidebar and home-page Session links have different accessible names. Use the listed landmark selectors or fresh row refs.
- The sidebar's `/sessions` link is unique only for the empty baseline. Later navigation may include additional Session-related links; take a fresh snapshot if a selector is ambiguous.
- A busy Runtime can block lifecycle actions. Observe its activity and settle or stop this run's Runtime before cleanup; do not force the durable lifecycle through the API.
- This recipe deliberately excludes Session Deletion, which removes the Session Workdir and Events. Archive and restore are not deletion proof.
- Missing Model Provider credentials, Sandbox image, or Runtime binary blocks the real-launch and lifecycle branches. Do not seed Session rows with test-only endpoints or report a mocked transcript as a Runtime response.
