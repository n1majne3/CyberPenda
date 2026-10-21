# Projects and Scope

Create a bounded Project with an explicit Project Kind, open its Project Dashboard, and save its Scope. This journey changes only the isolated verification data. It performs no security testing.

## Sub-features

- `project-entry`: all creation entry points open the same form.
- `project-cancel`: Cancel creates no Project.
- `project-create`: a name and explicit Project Kind are required and persisted.
- `project-open`: the Project card and sidebar reach the same Project Dashboard.
- `scope-save`: edited Scope survives navigation and reload.

## How to get to it (user POV)

- Open Projects at `/`. Choose the page-header `New project` button or the empty-state `New project` button.
- Choose the sidebar's `New project` icon link or its empty-state `New project` link. Both lead to `/?new=1`.
- Open `/?new=1` directly.
- Open the Project card or the sidebar's `Open Verification Project project dashboard` link.
- On that Project, choose `Scope` in `Project sections`, or `Add asset` in the dashboard's Scope readiness section. The direct route is the Project URL plus `/scope`.

## Driving it with agent-browser

Preconditions: Doctor passes; no Project named `Verification Project` exists. The empty-state entry checks require an empty Project list. Use the skill's `ab`, `$UI`, and `$EVIDENCE` variables.

- **Header entry.** Run `ab open "$UI/"`, `ab wait '[role="status"][aria-label="No projects"]'`, and `ab click '#main-content button:not([aria-label="No projects"] button)'`. This excludes the empty-state button. Require `#proj-name` and `#proj-kind` to appear; save the snapshot. Run `ab find role button click --name Cancel --exact`.
- **Empty-state entry.** Run `ab click '[role="status"][aria-label="No projects"] button'`. Require `#proj-name`, then Cancel. Capture the empty Project list again.
- **Sidebar entries.** Run `ab click '#workspace-sidebar a[aria-label="New project"]'`, require the form, then Cancel. Run `ab click '#workspace-sidebar a[href="/?new=1"]:not([aria-label])'`, require the form, then Cancel. These two selectors distinguish the icon and empty-state links.
- **Direct entry and required fields.** Run `ab open "$UI/?new=1"` and `ab wait '#proj-name'`. Run `ab fill '#proj-name' 'Verification Project'`. The `Create` button must remain disabled until Project Kind is selected. Run `ab snapshot > "$EVIDENCE/project-kind-required.txt"`.
- **Create.** Run `ab select '#proj-kind' pentest`, `ab screenshot "$EVIDENCE/project-form.png"`, and `ab find role button click --name Create --exact`. Wait for `ab wait '#main-content a[aria-label="Open Verification Project project dashboard"]'`. The card must show `Pentest` and `Needs scope`. Run `ab snapshot > "$EVIDENCE/project-created.txt"`.
- **Open and retain its URL.** Run `ab click '#main-content a[aria-label="Open Verification Project project dashboard"]'`, then `PROJECT_URL=$(ab get url)`. Require the Project name on its dashboard. Return to Projects and use `ab click '#workspace-sidebar a[aria-label="Open Verification Project project dashboard"]'`; require the same URL.
- **Scope entries.** From the empty-Scope dashboard, run `ab wait '[aria-labelledby="scope-readiness-title"] a[href$="/scope"]'`, then `ab focus '[aria-labelledby="scope-readiness-title"] a[href$="/scope"]'` and `ab press Enter`; require `#scope-domains`. Return to `"$PROJECT_URL"` and run `ab click '[aria-label="Project sections"] a[href$="/scope"]'`; require the same form. Also open `"$PROJECT_URL/scope"` directly when direct navigation is under test.
- **Save Scope.** Run `ab fill '#scope-domains' 'verify.invalid'` and `ab fill '#scope-notes' 'UI-only verification. No network testing is authorized.'`. Keep Default runner as `sandbox`. Save the filled form with `ab screenshot "$EVIDENCE/scope-form.png"`, then run `ab focus '[data-testid="project-page-shell-header"] button'` and `ab press Enter`. Run `ab wait --url "$PROJECT_URL"` to confirm a return to the Project Dashboard.
- **Persistence.** Reopen `"$PROJECT_URL/scope"`, run `ab reload`, wait for `#scope-domains`, and run `ab get value '#scope-domains'` plus `ab get value '#scope-notes'`. Require both exact values. Run `ab eval 'fetch("/api/projects/" + location.pathname.split("/")[2]).then(r => { if (!r.ok) throw new Error(String(r.status)); return r.json(); })' > "$EVIDENCE/project-persisted.json"`. Require `kind: "pentest"`, the domain, the notes, and `defaults.runner: "sandbox"` in the response. Save `ab snapshot > "$EVIDENCE/scope-persisted.txt"`, then `ab focus '#scope-domains'` and `ab screenshot "$EVIDENCE/scope-persisted.png"`. Also focus `#scope-notes` and save `scope-notes-persisted.png` so both stored fields have visible proof.

## Gotchas

- `New project` is not unique on an empty page. The header selector excludes the empty-state landmark; the other selectors distinguish the two sidebar links. If the structure changes, take a fresh snapshot and record the new locator rather than clicking an arbitrary first match.
- `Create` returns to the Project list; it does not open the new Project automatically.
- The Project header can sit behind the sticky navigation after a field scrolls into view. Use the documented focus-and-Enter actions for Save and Add asset, and wait for the destination URL. A successful click command alone does not prove the action.
- Scope values are newline-separated; blank lines and outer spaces are removed on save.
- A Scope-ready badge is not authorization to test `verify.invalid`. This fixture is for UI verification only.
- Check Project cancellation before creation if you need both empty-state entry points. Reuse a new isolated instance to repeat the empty-state journey.
- Scope Expansion proposals and Project Kind Conversion are not covered by `scope-save` or `project-create`. Add dedicated journeys before claiming those behaviors.
