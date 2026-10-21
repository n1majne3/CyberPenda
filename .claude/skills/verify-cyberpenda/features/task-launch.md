# Task Launch

Launch a Project Task from its Task Goal and Runtime Launch Controls. The empty-data baseline can verify entry points and blocked controls. It cannot prove Runtime execution without a real authorized launch configuration.

## Sub-features

- `task-launch-entry`: dashboard, Tasks list, empty Tasks state, and direct URL open the launch form.
- `task-type`: Task Type must be explicit and match Project Kind.
- `task-launch-boundary`: Host Browser is off; missing Model Provider or Runner readiness blocks launch.
- `task-launch-real`: successful Preflight creates a Task and a Runtime produces a retained response.

## How to get to it (user POV)

- Choose `Launch task` on the Project Dashboard.
- Choose `Tasks` in `Project sections`, then its header `Launch task` control or its empty-state `Launch task` control.
- Open the Project URL plus `/tasks/new` directly.

## Driving it with agent-browser

Preconditions: Doctor passes. Complete [Projects and Scope](projects-scope.md), and retain `PROJECT_URL` for the `Verification Project` dashboard. No Task exists initially.

- **Dashboard entry.** Run `ab open "$PROJECT_URL"`, then `ab find role link click --name 'Launch task' --exact`. Require `#task-type`, `#goal`, and `#launch-runtime`. Save the launch form snapshot.
- **Tasks entries.** Run `ab open "$PROJECT_URL/tasks"` and `ab wait --text 'No tasks yet'`. Run `ab click '[data-testid="project-page-shell-header"] a[href$="/tasks/new"]'`; require `#task-type`. Return to `"$PROJECT_URL/tasks"`, wait for `No tasks yet`, and run `ab click '#main-content [role="status"] a[href$="/tasks/new"]'` for the empty-state entry. Require the same launch form and save a snapshot for each entry.
- **Direct entry.** Run `ab open "$PROJECT_URL/tasks/new"`, then `ab wait '#task-type'`. Require the same Task Goal and Runtime controls.
- **Task Type validation.** Run `ab select '#task-type' ctf_challenge`. Require the alert `Task Type must match this Project's kind.` and a disabled `Launch` button. Save `ab snapshot > "$EVIDENCE/task-type-mismatch.txt"`. Run `ab select '#task-type' pentest` and `ab fill '#goal' 'Reply with verification-ready. Do not use tools or access the network.'`.
- **Baseline boundary.** Run `ab is checked '#launch-host-browser'`; require false. On empty Model Provider data, require `No compatible providers` in `#launch-model-provider` and a disabled `Launch` button. Save `ab screenshot "$EVIDENCE/task-launch-blocked.png"` and `ab snapshot > "$EVIDENCE/task-launch-blocked.txt"`. Record `task-launch-real` as blocked by missing Model Provider and Runtime prerequisites. Do not click Launch merely to see what happens.
- **Real launch, only with approved prerequisites.** Use a separate run with an operator-approved Model Provider, its credential supplied through the app, an installed Runtime or prepared Sandbox image, and the applicable Project Scope. Select Runtime, Model Provider, and model from the actual `#launch-runtime`, `#launch-model-provider`, and `#launch-model` option values shown by the snapshot. Expand the Runner control and select Docker or Podman for the prepared Sandbox Runner; keep Host Browser off. Record the exact selected values and Task Goal. If any prerequisite is missing, stop this branch and report it.
- **Launch action.** With that approved configuration, save the completed form and run `ab find role button click --name Launch --exact`. Preflight runs automatically. Require a Task detail URL under the same Project, not just an HTTP success or a passed Preflight panel. Wait for the Runtime's visible `verification-ready` response and retain the conversation snapshot and screenshot. A failed Preflight is a failure with its visible checks, not a successful launch.
- **Durable proof.** On the created Task route, run `ab eval 'fetch("/api/projects/" + location.pathname.split("/")[2] + "/tasks/" + location.pathname.split("/")[4]).then(r => { if (!r.ok) throw new Error(String(r.status)); return r.json(); })' > "$EVIDENCE/task-created.json"`. Reload the same Task and require the retained response. Save the Task ID, selected configuration, Runtime activity, and any remaining lifecycle work in the run report. Settle or stop this run's Runtime before instance cleanup.

## Gotchas

- `verify.invalid` in the baseline Scope authorizes no network testing. The baseline is a UI fixture, not a live testing engagement.
- The offline `Verification Provider` from the Model Providers recipe is deliberately unusable for model calls. A configured fake key is not proof of provider readiness.
- Preflight is read-only and does not prove a provider response. A Runtime Activity Indicator also does not prove Task completion.
- Task Finish and Stop are different actions. This recipe does not prove Finish Readiness or Task Finish.
- Host Runner Activation and Host Browser Enablement require separate explicit operator choices. Do not enable either as a fallback for a blocked Sandbox Runner.
- The Tasks page's duplicated links share an accessible name. Its header test ID and empty-state status landmark distinguish them.
- No real Runtime launch is pre-seeded. Missing credentials, image, Runtime binary, or launch-compatible Skills must remain reported blockers rather than being replaced with mocked responses.
