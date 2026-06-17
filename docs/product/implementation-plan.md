# Implementation Plan

## Reader And Action

Reader: the engineer implementing the MVP.

After reading: they should be able to start the build in vertical slices, know what each slice proves, and know what tests or checks make a slice complete.

## Build Strategy

Build the daemon, storage model, runtime harness, project interfaces, and React UI around a fake runtime first. Real runtime adapters are added after the task and blackboard contracts are stable.

The key implementation rule is that every slice must move one user-visible workflow forward. Avoid building deep infrastructure without a path to project dashboard, task run, blackboard write, or report output.

## Current Build Position

The backend MVP is complete: every backend slice (0-5, 8, 9) and all
cross-cutting unit, adapter, runner, and integration tests are implemented and
passing. The only remaining work is the React UI (Slice 6, Slice 7, and Next
Slice E).

Backend status by slice:

- Slice 0: daemon backend skeleton, SQLite migrations, and the test harness are present and passing; the React shell and a combined dev command are not present.
- Slice 1: project CRUD, structured scope, project defaults, and the dashboard summary (scope status + task/fact/finding/evidence counts) are present and tested.
- Slice 2: global runtime profile CRUD, structured fields, generated config preview, credential references, global bindings, project overrides/disables, and preflight validation are present and tested.
- Slice 3: task model, runtime harness, fake adapter, task events, runtime config versions, steering endpoint, runtime continuation, profile-switch-as-version, and mechanical handoff are present and tested.
- Slice 4: task-local directory layout, sandbox command construction, config projection, host runner activation, and no sandbox-to-host fallback are present and tested.
- Slice 5: fact upsert/versions/index/lookup/relations, finding upsert/versions/CVSS-pending/confirmed-validation, evidence attach with managed paths, task summary auto-accept/versioning, report trigger, CLI fallback, and a shared service layer are present and tested.
- Slice 8: Markdown report generation separating confirmed/unconfirmed findings, with fact context, evidence references, runner and scope context, and CVSS data — derived from stored state only — is present and tested.
- Slice 9: adapter launch argument construction, secret redaction, binary detection, and restart/resume prompt construction are present and tested. Real binary smoke tasks run out-of-band.

UI status: Slice 6, Slice 7, and Next Slice E (React shell) are not started.

The next implementation work is the React UI.

## Slice 0: Repository Skeleton

Goal: establish a runnable local app skeleton.

Deliverables:

- Go daemon entrypoint.
- SQLite migration runner.
- React app shell.
- Development command for daemon and UI.
- Basic health endpoint.
- Basic test harness.

Acceptance checks:

- Daemon starts locally.
- React shell can load.
- Health endpoint returns daemon version and database status.
- Tests can run without container or real runtime dependencies.

## Slice 1: Project, Scope, And Dashboard Base

Goal: create the first project dashboard loop.

Deliverables:

- Project CRUD.
- Structured scope model.
- Project defaults model.
- Project dashboard route.
- Scope editor.
- Dashboard cards for scope status, task count, fact count, finding count, and evidence count.

Acceptance checks:

- A user can create a project and define scope.
- Scope includes assets, exclusions, testing limits, and notes.
- Dashboard loads project state from the daemon.
- Scope updates persist across daemon restart.

## Slice 2: Runtime Profiles And Credential Bindings

Goal: make runtime configuration editable without storing secrets in profiles.

Deliverables:

- Global runtime profile CRUD.
- Providers: fake, Codex, Claude Code, Pi.
- Structured profile fields.
- Generated runtime config preview.
- MCP configuration structured editor.
- Credential references.
- Global credential bindings.
- Project credential binding overrides and disabled bindings.
- Preflight validation endpoints.

Acceptance checks:

- A user can create and edit a runtime profile.
- Profile does not persist secret values.
- Project can use global credential bindings by default.
- Project can override or disable a credential binding.
- Generated config preview reflects structured fields.
- Invalid profile or credential state is reported before task launch.

## Slice 3: Task Model And Runtime Harness

Goal: launch and control a fake runtime through the same harness real runtimes will use.

Deliverables:

- Task model with task goal and run controls.
- Task runtime configuration capture.
- Task runtime configuration versions.
- Task events.
- Runtime harness interface.
- Fake runtime adapter.
- Event streaming to UI.
- Task detail view.
- Harness steering endpoint and UI controls.
- Runtime continuation model.

Acceptance checks:

- A user can launch a fake-runtime task from the dashboard.
- The task captures runtime profile, runner, scope snapshot, and run controls.
- Fake runtime emits normalized events.
- UI streams task events.
- Steering creates task events and affects the next fake runtime continuation.
- Runtime profile switch inside a task creates a new continuation and configuration version, not a new task.

## Slice 4: Sandbox Runner And Host Runner Boundary

Goal: make execution boundaries explicit and testable.

Deliverables:

- Sandbox runner command construction.
- Task-local runtime workdir, runtime home, artifact root, and logs layout.
- Config projection into task-local runtime homes.
- Host runner activation model.
- No automatic sandbox-to-host fallback.
- Runner markers in task detail.

Acceptance checks:

- Sandbox runner can prepare task directories.
- Sandbox runner can construct container launch commands without real runtime execution.
- Config projection writes generated config into task-local runtime home.
- Host runner requires explicit activation or YOLO-mode declaration.
- Sandbox runner failure does not start host runner.

## Slice 5: Trusted Project Interfaces

Goal: allow runtimes to write project state through stable interfaces.

Deliverables:

- Trusted MCP server.
- CLI fallback.
- Shared service layer for MCP and CLI.
- Fact upsert and lookup.
- Fact index.
- Fact relations.
- Finding record and update.
- Evidence attach.
- Task summary update.
- Report generation trigger.

Acceptance checks:

- MCP and CLI writes produce equivalent stored state.
- Fact upsert by fact key overwrites current fact and preserves fact version.
- Empty fact body update preserves prior body.
- Fact index omits full bodies.
- Finding upsert by finding key overwrites current finding and preserves finding version.
- Task summary update is automatically accepted and versioned.

Current status:

- Project fact upsert, fact versions, empty-body preservation, fact index, full fact lookup, and task summary versioning are implemented.
- Fact relations, findings, evidence attach, MCP server, CLI fallback, and report trigger remain open.

## Slice 6: Blackboard UI

Goal: make project memory inspectable and useful.

Deliverables:

- Fact browser.
- Fact detail with versions.
- Fact body lookup.
- Fact relation browser.
- Current truth view.
- Deprecated fact visibility toggle.
- Out-of-scope fact marker.
- Task summary panel.

Acceptance checks:

- A user can inspect current truth without seeing full raw output.
- A user can open a fact body on demand.
- Fact versions are visible.
- Out-of-scope facts are visibly non-actionable.
- Task summary latest version is visible.

## Slice 7: Findings, Evidence, And CVSS

Goal: make reportable issues and proof manageable.

Deliverables:

- Finding browser.
- Finding detail and versions.
- Finding key update flow.
- Finding merge and alias basics.
- Finding groups for presentation.
- CVSS v4.0 fields.
- CVSS pending state.
- Evidence artifact browser.
- Explicit attach or retain action from runtime workdir.

Acceptance checks:

- A finding can be recorded with CVSS pending.
- A confirmed finding requires CVSS vector, target, proof, impact, and recommendation.
- Finding severity is derived from CVSS data.
- Evidence artifacts reference managed artifact roots.
- Runtime workdir files do not become evidence without explicit attach or retain.

## Slice 8: Markdown Report Generation

Goal: generate a useful deliverable from stored project state.

Deliverables:

- Markdown report generator.
- Confirmed findings section.
- Unconfirmed findings section.
- Fact and attack chain context.
- Evidence references.
- High-signal provenance.
- Runner and YOLO markers.
- CVSS version and severity display.

Acceptance checks:

- Report can be generated from a project with fake runtime output.
- Confirmed and unconfirmed findings are separated.
- Reports do not read raw runtime output directly.
- Reports include runner, scope context, key evidence, and CVSS data.

## Slice 9: Real Runtime Adapters

Goal: add real runtimes after the harness and interfaces are stable.

Implementation order:

1. Codex adapter.
2. Claude Code adapter.
3. Pi adapter.

Adapter responsibilities:

- Detect binary and version.
- Build launch args from task runtime configuration.
- Project runtime home into task-local runtime home.
- Stream normalized runtime events.
- Support the best available steering mode.
- Fall back to restart/resume continuation when live steering is unavailable.
- Avoid leaking secrets into logs or task events.

Acceptance checks:

- Adapter can run a smoke task.
- Adapter writes through trusted project interface.
- Adapter can receive fact index and task summary or mechanical handoff packet.
- Adapter failure preserves error, runtime, runner, task paths, and event history.

## Cross-Cutting Tests

Unit tests:

- SQLite migrations.
- Project and scope repositories.
- Runtime profile validation.
- Credential binding resolution.
- Config projection.
- Fact key upsert and versioning.
- Empty fact body preservation.
- Finding key upsert and versioning.
- CVSS validation.
- Report assembly.

Adapter tests:

- Event parsing.
- Launch argument construction.
- Secret redaction.
- Steering mode behavior.
- Restart/resume prompt construction.

Runner tests:

- Task directory creation.
- Sandbox command construction.
- Host runner activation rules.
- No automatic sandbox-to-host fallback.

Integration tests:

- Start daemon with temporary database.
- Create project and scope.
- Create runtime profile and credential binding.
- Launch fake runtime task.
- Stream task events.
- Write facts through MCP.
- Write findings and evidence through CLI.
- Generate Markdown report.

## Milestone Order

Milestone 1: runnable project dashboard with project, scope, and runtime profile CRUD.

Milestone 2: fake-runtime task loop with runtime harness, task events, steering, and continuation.

Milestone 3: trusted project interfaces with blackboard facts, findings, evidence, and task summaries.

Milestone 4: report generation from stored project state.

Milestone 5: sandbox runner preparation and first real runtime adapter.

## Next Execution Plan

### Next Slice A: Close Task Continuation And Steering

Goal: complete the task lifecycle contract before adding real runtimes.

Deliverables:

- Harness steering endpoint.
- Steering task event.
- Runtime continuation record or response shape.
- Runtime profile switch inside the same task.
- Runtime configuration version capture for the switched continuation.
- Mechanical handoff packet response when no task summary exists.

Acceptance checks:

- A test can launch a fake-runtime task, send a steering directive, and observe a steering event.
- A test can switch runtime profile inside the same task and observe a new runtime configuration version without creating a new task.
- A test can request continuation context and receive either the latest task summary or a mechanical handoff packet.

### Next Slice B: Runner Boundary

Goal: make sandbox and host execution boundaries concrete before real adapters run commands.

Deliverables:

- Task-local runtime workdir, runtime home, artifact root, and log root preparation.
- Sandbox runner command construction.
- Config projection into task-local runtime homes.
- Host runner activation model.
- No automatic sandbox-to-host fallback.

Acceptance checks:

- A test can prepare a task run directory layout.
- A test can construct a sandbox command without launching a real container.
- A test verifies host runner cannot be reached through sandbox fallback.
- A test verifies config projection writes generated runtime config without mutating host runtime config.

### Next Slice C: Complete Trusted Project Interfaces

Goal: finish the backend contracts that runtimes need before MCP and CLI transports are attached.

Deliverables:

- Fact relation upsert and list.
- Finding key upsert, finding versions, CVSS pending, and confirmed finding validation.
- Evidence artifact attach with managed artifact paths.
- Report trigger stub that can later generate Markdown.

Acceptance checks:

- A fact relation can connect two project facts and cannot directly connect findings.
- A finding can be recorded with CVSS pending and then updated with a complete CVSS vector.
- Evidence can be explicitly attached to a fact or finding.
- Report trigger returns a stable response from stored project state, even before full templating exists.

### Next Slice D: Attach MCP And CLI Transports

Goal: expose the same service layer through runtime-facing project interfaces.

Deliverables:

- Built-in trusted MCP server for facts, findings, evidence, task summary, and report trigger.
- `pentestctl` CLI fallback for the same operations.
- Shared validation and response shapes across HTTP, MCP, and CLI.

Acceptance checks:

- MCP and CLI writes produce the same stored state as HTTP service calls.
- CLI fact upsert preserves empty-body semantics and fact versions.
- MCP task summary update is automatically accepted and versioned.

### Next Slice E: Minimal React Shell

Goal: make the backend MVP operable through a local browser.

Deliverables:

- React shell served by the daemon for release builds.
- Project dashboard.
- Project and scope CRUD.
- Runtime profile selector and editor.
- Task launch and task detail timeline.
- Fact index and fact detail views.

Acceptance checks:

- A user can create a project and launch a fake-runtime task from the UI.
- The task timeline shows persisted task events.
- The fact index and full fact lookup are visible from the dashboard.

## Implementation Notes

- Keep business behavior in daemon services shared by HTTP, MCP, and CLI.
- Keep runtime adapters thin and provider-specific.
- Keep runner boundary separate from adapter boundary.
- Keep task events structured and small.
- Keep raw output in logs or evidence artifacts.
- Keep reports derived from stored state, not live runtime output.
- Keep fake runtime maintained until at least two real adapters pass the same task loop.
