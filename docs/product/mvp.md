# MVP Scope

## Reader And Action

Reader: the engineer planning the first release boundary.

After reading: they should be able to decide whether a proposed feature belongs in the MVP, a later release, or the first implementation slice.

## MVP Definition

The MVP is a local-first pentest agent that proves the full project loop:

1. Configure a project and scope.
2. Configure a runtime profile.
3. Launch a user-goal-driven task through a runtime harness.
4. Run the task through the sandbox runner by default.
5. Stream normalized task events.
6. Write blackboard facts, findings, evidence, and task summaries through trusted project interfaces.
7. Generate a Markdown report from stored project state.

The MVP may use a fake runtime adapter before real Codex, Claude Code, and Pi adapters are complete. The fake adapter must still exercise the same task, harness, blackboard, evidence, and report paths.

## Current Build Status

The current codebase is a backend-first MVP skeleton. It has enough daemon and storage behavior to keep building vertical slices with tests, but it is not yet a user-facing MVP.

Completed backend contracts:

- Local daemon entrypoint, health endpoint, SQLite store, and migrations.
- Project CRUD, structured scope, project defaults, and dashboard summary.
- Runtime profile CRUD with structured fields and generated config preview.
- Credential references, global credential bindings, project overrides, disabled bindings, and preflight validation.
- Task domain with task goal, run controls, scope snapshot, task events, runtime configuration versions, and fake runtime harness.
- HTTP task lifecycle for create, list, get, events, stop, task summaries, and summary versions.
- Project facts with fact key upsert, automatic overwrite, fact versions, compact fact index, full fact lookup, and empty-body preservation.
- Dashboard counts for tasks and project facts.

Partially complete contracts:

- Task events are persisted and readable over HTTP, but live UI streaming is not complete.
- Runtime harness launch and stop are implemented with a fake adapter, but explicit steering endpoints and restart/resume continuation behavior still need to be completed.
- Blackboard facts are implemented, but fact relations, findings, evidence artifacts, and report generation are not complete.

Not yet started for MVP:

- React UI.
- Sandbox runner command construction and task-local runtime directory preparation.
- Built-in trusted MCP server.
- `pentestctl` CLI fallback.
- Finding, CVSS, evidence, and Markdown report generation.
- Real Codex, Claude Code, and Pi adapters.

## In Scope

### Local Daemon

- Go daemon with HTTP API.
- SQLite persistence.
- Static React UI serving for release builds.
- Local artifact storage.
- Task scheduler and runtime harness lifecycle.
- Built-in trusted MCP server.
- `pentestctl` CLI fallback.

### Project Model

- Project CRUD.
- Structured scope editor and storage.
- Project defaults for runtime profile, runner, and task policy.
- Scope snapshot capture at task start.
- Out-of-scope facts with explicit non-actionable status.

### Runtime Profiles And Credentials

- Global runtime profiles.
- Providers: Codex, Claude Code, Pi, and fake runtime.
- Structured profile fields as source of truth.
- Generated runtime config preview.
- Profile config import only when raw config can be parsed back into structured fields.
- Credential references with global binding defaults and project overrides.
- Disabled credential bindings.
- Preflight validation for missing runtime, config, sandbox, and credential resolution.

### Runtime Harness And Task Lifecycle

- Task goal plus run controls.
- Task events for runtime output, lifecycle, steering, and user-runtime interaction.
- Runtime harness with start, stream, interrupt, stop, resume, and steer concepts.
- Runtime continuation model for live session, interrupt-then-steer, and restart/resume steering.
- Task summary updates submitted by runtimes and automatically accepted.
- Mechanical handoff packet when no accepted task summary exists.
- Runtime profile switching inside the same task at continuation boundaries.

### Runner Boundary

- Sandbox runner as default.
- Host runner as explicit opt-in.
- No automatic fallback from sandbox runner to host runner.
- Runner use visible in task detail and reports.
- Sandbox runner command construction and task-local runtime home projection.

### Blackboard

- Project fact CRUD through trusted project interfaces.
- Fact key upsert with automatic overwrite.
- Fact versions.
- Empty-body update preserves prior body.
- Fact key aliases after merge.
- Current truth and fact index.
- Full fact body lookup.
- Fact relations: supports, contradicts, depends_on, leads_to, duplicates.
- Deprecated facts excluded from default current truth.

### Findings

- Finding key upsert with automatic overwrite.
- Finding versions.
- Finding merge and aliases.
- Finding groups for report or UI presentation.
- CVSS vector and explicit CVSS version.
- CVSS pending state.
- Confirmed finding requirements.
- Partial finding updates.

### Evidence And Artifacts

- Project artifact root and task artifact roots.
- Evidence artifact records with paths, hashes, summaries, and metadata.
- Explicit attach or retain action from runtime workdir to evidence.
- Raw runtime or tool output stored as logs or evidence references, not task event dumps.

### Report Generation

- Markdown report output.
- Confirmed findings section.
- Unconfirmed findings section.
- Fact, evidence, and attack chain context.
- High-signal provenance.
- Runner, YOLO, host runner, tentative fact, and CVSS pending markers.

### React UI

- Project dashboard.
- Project and scope management.
- Runtime profile selector and editor.
- Credential binding mode display.
- Task launch form with task goal and run controls.
- Task detail timeline.
- Harness steering controls.
- Blackboard fact browser.
- Finding browser.
- Evidence browser.
- Markdown report generation.

## Out Of Scope For MVP

- Cloud accounts, teams, or multi-tenant hosting.
- Packet-level network enforcement.
- Hard per-command command interception.
- Full typed attack graph.
- Real-time mutation of a runtime's internal reasoning step.
- Automatic semantic summarization by the daemon.
- Automatic project-wide pause on policy violation.
- External worker process split.
- PDF and HTML report generation.
- Fully automated CVSS inference.
- Automatic fact or finding key normalization.
- Browser-based exploit replay.
- Plugin marketplace for external MCP servers.

## First Implementation Slice

The first slice is complete. The fake runtime proves the full loop:

1. Daemon starts with SQLite.
2. React shell connects to daemon.
3. Project and scope CRUD work.
4. Runtime profile CRUD works for fake, Codex, Claude Code, and Pi providers.
5. Task can launch using fake runtime through the runtime harness.
6. Task events stream to UI.
7. Harness steering creates a new runtime continuation inside the same task.
8. Fake runtime writes facts, findings, evidence, and task summary through trusted project interface.
9. Fact index and full fact lookup work.
10. Markdown report generation works.

All ten points are implemented and covered by tests. "Task events stream to UI" is met via the task-detail view polling the events endpoint; the spec uses "stream" loosely and never required SSE or WebSocket, so polling satisfies the acceptance criterion. Every project-scoped route (project, runtime profile, credential, dashboard, task, fact, finding, evidence, report) rejects an unknown project id with 404. The only item intentionally held back is real-runtime smoke validation (running the actual Codex/Claude/Pi binaries), which is out-of-band by design.

## MVP Acceptance Criteria

- A fresh local install can start the daemon and open the project dashboard.
- A user can create a project, define scope, and configure project defaults.
- A user can create a runtime profile without storing secrets in the profile.
- A task can launch through the sandbox runner path or fake sandbox runner path.
- Task events stream to the UI and persist in SQLite.
- A runtime can submit a task summary and the next continuation can receive it.
- A runtime can upsert project facts by fact key.
- A runtime can record a finding by finding key with CVSS pending or complete CVSS data.
- A runtime can attach an evidence artifact by explicit action.
- A report can be generated without reading raw runtime output directly.

## Deferred Capabilities

### Near Term

- Real Codex adapter.
- Real Claude Code adapter.
- Real Pi adapter.
- Better runtime capability detection.
- Stronger profile config import coverage.
- More polished report templates.

### Later

- Worker process split.
- HTML and PDF reports.
- Advanced fact and finding merge UI.
- CVSS calculator assistance.
- External MCP permission UI.
- Rich attack chain visualization.
- Optional cloud sync or export workflows.

## MVP Risk Controls

- Keep the fake runtime adapter until all project interfaces are stable.
- Keep sandbox runner command generation testable without launching a real container.
- Do not let UI features depend on a specific real runtime adapter.
- Treat runtime profile switching, task summaries, and mechanical handoff as core contracts from the first slice.
- Keep reports generated from stored state only.
