# Local-First Pentest Agent PRD

## Reader And Action

Reader: an engineer or product owner preparing to implement the first local-first pentest agent.

After reading: they should be able to explain the product boundary, core workflows, user-visible behavior, and non-goals without needing the original brainstorming thread.

## Product Summary

The product is a local-first pentest agent that coordinates authorized security testing work inside a project. It combines managed local agent runtimes with a project-local blackboard so multiple runtimes can share facts, findings, evidence, and task context without relying on a cloud control plane.

The first product experience is a local daemon with a React UI. The daemon manages project state, runtime profiles, task lifecycle, runtime harness control, blackboard memory, findings, evidence, reports, and task-level policy records. Agent runtimes run through a runtime harness and, by default, execute inside a sandbox runner backed by a Kali-style security testing environment.

The daemon is not a pentest tool proxy. Pentest tools run inside the runtime environment. The daemon is the control plane, memory plane, task lifecycle plane, and reporting plane.

## Users

### Primary User

An operator running authorized security testing against a scoped project. They need to configure local runtimes, launch task runs, steer active runs, inspect accumulated findings, preserve evidence, and generate reports.

### Secondary User

A developer extending runtime adapters, project interfaces, blackboard behavior, or UI workflows. They need stable domain language and a replaceable runner boundary.

## Goals

- Run pentest agent tasks locally without a cloud multi-tenant platform.
- Support managed local runtimes such as Codex, Claude Code, and Pi.
- Run runtimes through a sandbox runner by default.
- Preserve project memory in a CyberStrike-style blackboard.
- Keep task runs goal-driven rather than step-driven.
- Allow runtime harness steering inside the same task.
- Support compact fact injection through a fact index, with full fact bodies fetched on demand.
- Record findings with CVSS-derived severity and evidence links.
- Generate Markdown reports from findings, facts, evidence, and high-signal provenance.
- Provide a React project dashboard for scope, task runs, blackboard growth, findings, and evidence.

## Non-Goals

- Do not build a cloud SaaS or multi-tenant platform.
- Do not proxy every pentest tool through the daemon.
- Do not implement packet-level or command-level network enforcement in the daemon.
- Do not build a full typed attack graph in the first release.
- Do not treat the daemon as an LLM summarizer.
- Do not make chat the primary product surface.
- Do not require every runtime adapter to support identical live steering capabilities.

## Product Principles

### Local First

Project data, task artifacts, runtime configuration projections, blackboard memory, and reports live on the local machine unless the user explicitly exports them.

### Project-Scoped Memory

The blackboard belongs to one project. Runtime profiles are global, but project facts, findings, evidence, scope, and task state are project-local.

### Runtime Harness Control

The runtime harness owns process lifecycle and continuation control for one task. Harness steering changes the next runtime continuation at input, checkpoint, interrupt, or resume boundaries. It does not edit a runtime's already-running internal reasoning step.

### Sandbox By Default

The sandbox runner is the default runner. It separates runtime filesystem state, dependencies, runtime homes, and process environment from the host. It is not a full network or command enforcement boundary.

### Facts Before Reports

Durable project understanding should be written to the blackboard during task execution, not only summarized after completion.

### Compact Context First

Runtimes receive a fact index by default. Full fact bodies, evidence, and logs are fetched only when needed.

## Core Workflows

### Create A Project

The user creates a project, defines structured scope, and selects project defaults for runtime profile, runner, and task policy. Meaningful testing requires scope to exist before launch.

### Configure Runtime Profiles

The user defines global runtime profiles for Codex, Claude Code, Pi, or later providers. A profile uses structured fields as the source of truth for generated runtime config. Raw config preview and import are advanced compatibility features, not the primary editing model.

Profiles contain credential references, not secret values. Credential references resolve through project credential bindings first, then global credential bindings. Projects default to using global credential bindings unless the user explicitly overrides or disables them.

### Launch A Task

The user starts a task from a natural-language task goal plus structured run controls. The task captures its runtime configuration, runner, scope snapshot, credential binding mode, and artifact behavior.

Preflight checks run before runtime launch. If required runtime, sandbox, configuration, or credential resolution is missing, the task fails before the runtime starts.

### Steer An Active Task

The user or runtime can steer the runtime harness without creating a new task. Steering may revise the goal, request a pause, resume, interrupt, stop after current step, switch runtime profile, or change run controls.

Changes that affect runner, runtime profile, or other run controls apply only at runtime continuation boundaries and are recorded as task events.

Runtime continuations receive a runtime-maintained task summary by default. If no accepted summary exists, the daemon provides a mechanical handoff packet assembled from structured task state.

### Write To The Blackboard

Runtimes write project facts, fact relations, findings, evidence links, and task summaries through trusted project interfaces. CLI fallback has the same semantics as MCP and is not a bypass.

External MCP servers may run in the runtime environment, but they are not trusted project interfaces by default. Their output enters the blackboard only when the runtime interprets it and writes through a trusted project interface.

### Manage Findings

Findings are reportable issues with stable finding keys. Severity is derived from a CVSS vector. CVSS v4.0 is canonical for new findings, with v3.1 compatibility for import and export.

Findings can exist before scoring is complete, but confirmed findings require a complete CVSS vector, target, proof, impact, recommendation, and support from confirmed facts or evidence artifacts.

### Generate A Report

Reports are generated deliverables, not the source of truth. They present confirmed findings, unconfirmed findings, high-signal provenance, evidence references, tentative facts, attack chain narratives, and task mode context.

## Functional Requirements

### Project And Scope

- The product must support project CRUD.
- The product must support structured scope with assets, exclusions, testing limits, and notes.
- Each task must store an immutable scope snapshot.
- Scope expansion must be explicit.
- Out-of-scope facts may be visible as current context only when clearly marked non-actionable.

### Runtime Profiles

- Runtime profiles must be global and reusable across projects.
- Runtime profile fields must include provider, binary path, model or endpoint, custom args, credential references, MCP configuration, default runner, and generated config preview.
- Runtime profiles must not store secret values.
- Editing a runtime profile must not mutate existing task runtime configurations.

### Runtime Harness

- Each task must have one runtime harness.
- The harness must launch, stream, interrupt, resume, stop, and steer runtimes.
- The harness must support restart/resume continuation for runtimes that cannot be steered live.
- Runtime-profile switches inside a task must create new task runtime configuration versions, not new tasks.
- Runtime switches must not inherit the previous runtime workdir by default.
- Runtime-submitted task summaries must be automatically accepted and versioned.
- The daemon must provide a mechanical handoff packet when no accepted task summary exists.

### Runner Behavior

- Sandbox runner must be the default.
- Host runner must be explicit opt-in through host runner activation.
- Sandbox runner failure must not silently fall back to host runner.
- Host runner use must be visible in the UI and report.

### Blackboard

- Project facts must use stable fact keys.
- Writes to the same fact key automatically update the current project fact.
- Fact updates must preserve fact versions.
- Empty body updates must preserve the existing body unless clearing is explicit.
- Fact index must expose compact current context without full bodies.
- Fact relations must connect project facts, not findings.
- Stable attack chain summaries must be stored as project facts.

### Findings And Evidence

- Findings must use stable finding keys.
- Writes to the same finding key automatically update the current finding.
- Finding updates must preserve finding versions.
- Finding merge must preserve aliases and references.
- Findings on different assets or entry points must not merge; they may be grouped for presentation.
- Runtime workdir files must become evidence artifacts only through explicit attach or retain actions.

### Project Interfaces

- The product must expose a trusted MCP server for project operations.
- The product must expose a CLI fallback with equivalent semantics.
- Writes through MCP and CLI must share validation, provenance, and blackboard behavior.
- Runtime task summary updates must use trusted project interfaces.

### UI

- The primary UI entry point must be the project dashboard.
- The dashboard must show scope status, task runs, blackboard growth, findings, evidence health, and report generation.
- The runtime profile editor must provide fast profile selection plus structured detail editing.
- Host runner use, scope status, and unconfirmed findings must be visually hard to miss.

### Reports

- Markdown report generation is required for MVP.
- Reports must separate confirmed findings from unconfirmed findings.
- Reports must show CVSS-derived severity.
- Reports must show CVSS version where a CVSS vector is present.
- Reports must include high-signal provenance without expanding every task event.

## Success Metrics

- A user can create a project, define scope, configure a runtime profile, launch a sandboxed task, and see task events stream in the UI.
- A runtime can write facts, findings, evidence, and task summaries through a trusted project interface.
- A second runtime continuation can receive a task summary or mechanical handoff packet and continue the same task.
- The blackboard can provide a compact fact index and full fact body lookup.
- A Markdown report can be generated from project state without manually copying runtime output.

## Risks

- Runtime CLIs may have inconsistent session and streaming behavior.
- Sandbox environment setup may vary by host OS and container runtime.
- Raw runtime output can be noisy, sensitive, or too large for task timelines.
- Fact and finding key generation can create duplicates until merge workflows mature.
- External MCP servers can be useful but must not become implicit trusted project interfaces.

## Open Product Questions

- Which runtime adapter should be fully implemented first after the fake adapter proves the loop?
- Which CVSS v4.0 fields are mandatory in the first UI form versus deferred to advanced editing?
- Which artifact retention controls should be visible at task launch in the first UI?
