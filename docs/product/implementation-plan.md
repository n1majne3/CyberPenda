# Implementation Plan

## Reader And Action

Reader: the engineer implementing the MVP.

After reading: they should be able to start the build in vertical slices, know what each slice proves, and know what tests or checks make a slice complete.

## Build Strategy

Build the daemon, storage model, runtime harness, project interfaces, and React UI around a fake runtime first. Real runtime adapters are added after the task and blackboard contracts are stable.

The key implementation rule is that every slice must move one user-visible workflow forward. Avoid building deep infrastructure without a path to project dashboard, task run, blackboard write, or report output.

## Current Build Position

The full MVP — backend and React UI — is implemented and passing. Every slice
(0-9) including the cross-cutting unit, adapter, runner, and integration tests
and the React views is complete.

Backend status by slice:

- Slice 0: daemon backend skeleton, SQLite migrations, React shell (Vite +
  embedded SPA), combined dev command (`make dev`), and the test harness are
  present and passing.
- Slice 1: project CRUD, structured scope, project defaults, dashboard summary,
  and the React dashboard + scope editor are present and tested.
- Slice 2: global runtime profile CRUD, structured fields, generated config
  preview, credential references, global bindings, project overrides/disables,
  preflight validation, and the React profile/credential editors are present
  and tested.
- Slice 3: task model, runtime harness, fake adapter, task events, runtime
  config versions, steering endpoint, runtime continuation, profile-switch-as-
  version, mechanical handoff, and the React task launch + timeline + steering
  views are present and tested.
- Slice 4: task-local directory layout, sandbox command construction, config
  projection, host runner activation, and no sandbox-to-host fallback are
  present and tested.
- Slice 5: fact upsert/versions/index/lookup/relations, finding upsert/versions/
  CVSS-pending/confirmed-validation, evidence attach with managed paths, task
  summary auto-accept/versioning, report trigger, CLI fallback, shared service
  layer, and the React fact/finding/evidence browsers are present and tested.
  Every project-scoped route rejects an unknown project id with 404.
- Slice 6: the React blackboard browser — fact index, expandable fact body
  lookup, fact versions, confidence/scope badges, and the task summary panel —
  is present.
- Slice 7: the React findings and evidence browsers — confirmed/unconfirmed
  separation, CVSS v4.0 vector and severity display, evidence grouped by attach
  target — are present.
- Slice 8: Markdown report generation separating confirmed/unconfirmed findings,
  with fact context, evidence references, runner and scope context, and CVSS
  data — derived from stored state only — plus the React report generator view
  are present and tested.
- Slice 9: adapter launch argument construction, secret redaction, binary
  detection, and restart/resume prompt construction are present and tested.
  Real binary smoke tasks run out-of-band.

The remaining work is real-runtime smoke validation (Slice 9 acceptance that
requires the actual Codex/Claude/Pi binaries present), which is out-of-band by
design.

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

- Project fact upsert, fact versions, empty-body preservation, fact index, full fact lookup, task summary versioning, fact relations, findings (with CVSS pending and confirmed validation), evidence attach with managed paths, CLI fallback, and report trigger are all implemented and tested. The built-in trusted MCP server remains the one Slice 5 deliverable that is not yet wired (it is not required to exercise the loop through the HTTP/CLI surfaces).

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

The earlier next-up slices A through E (close task continuation and steering,
runner boundary, complete trusted project interfaces, attach MCP and CLI
transports, minimal React shell) are all implemented. Each of their acceptance
checks is covered by an existing test or React view:

- Slice A (continuation and steering): `task_test.go` and the integration flow
  cover steering events, profile-switch-as-version, and the
  summary-or-mechanical-handoff continuation response.
- Slice B (runner boundary): `runner_test.go` covers task-local layout, sandbox
  command construction, host activation, and the no-sandbox-to-host-fallback
  rule.
- Slice C (trusted project interfaces): `blackboard_test.go` and the service
  tests cover fact relations, finding CVSS pending → confirmed, and evidence
  attach with managed paths; the report trigger is exercised end-to-end.
- Slice D (MCP and CLI transports): the `pentestctl` CLI fallback is implemented
  and tested; the built-in trusted MCP *server* transport is the one deferred
  item (the shared service layer it would call is already complete and used by
  HTTP and CLI).
- Slice E (React shell): the daemon embeds and serves the SPA, and all listed
  views exist.

The only remaining out-of-band work is real-runtime smoke validation (running
the actual Codex/Claude/Pi binaries against the adapter logic).

Slice 6/7 UI wiring note: the blackboard and findings React views originally
exposed the fact index and finding cards but left several declared features
stubbed while the backing HTTP endpoints already existed. Those are now wired:

- Fact detail now expands to show the full body, fact versions
  (`GET .../facts/{key}/versions`), and fact relations
  (`GET .../facts/{key}/relations`).
- The "show deprecated" toggle now works end to end: `FactIndex` takes an
  `IncludeDeprecated` option and `GET .../facts/index?include_deprecated=1`
  surfaces deprecated facts (badged, struck through) alongside Current Truth.
  The default still excludes them so dashboards, reports, and runtime context
  are unaffected.
- The blackboard view surfaces a task summary panel
  (`GET .../tasks/{id}/summary`), since task summaries are project memory.
- The findings view shows per-finding version history
  (`GET .../findings/{key}/versions`).

Each behavior is covered by `blackboard_test.go` (deprecated default vs
include) and the React build. Fact/finding merge and alias UI remain out of
scope here (the underlying merge logic is not yet implemented).

## Implementation Notes

- Keep business behavior in daemon services shared by HTTP, MCP, and CLI.
- Keep runtime adapters thin and provider-specific.
- Keep runner boundary separate from adapter boundary.
- Keep task events structured and small.
- Keep raw output in logs or evidence artifacts.
- Keep reports derived from stored state, not live runtime output.
- Keep fake runtime maintained until at least two real adapters pass the same task loop.
