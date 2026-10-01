# CyberPenda — Current-State System Model Summary

Produced by the `system-modeler` skill with the `c4model` format foundation. 2026-09-30.

## What this is

CyberPenda is a **local-first pentest agent** for authorized security testing. One Go module
(`pentest`, Go 1.25) provides the whole control plane; a React dashboard provides the operator UI;
container images provide the execution boundaries. Data stays on the operator's machine: a SQLite
database (`pentest.db`), task run directories, and managed artifact roots.

## The model in one paragraph

An **Operator** drives the **React Dashboard** against the local **pentestd** daemon
(HTTP :8787, bearer token or loopback operator session). The daemon orchestrates everything:
its **HTTP API** (internal/daemon) fronts domain services (Project, Scope, Task, Session, Skill,
Model Provider, Credential, Report, Steering, Finish Readiness), the **Blackboard v2** semantic
memory, and the **FGS store** (accepted Goal/Step/Fact updates with Receipts). All persistence
goes through the shared **SQLite store** with migrations, kept inline in the domain services
(ADR 0017). For each Task or Session, the daemon's **Runtime Harness** (internal/runtime) prepares
launch through **Runner + Config Projection** (internal/runner, internal/preflight) and then
launches, resumes, steers, and stops one **Agent Runtime** — a Codex, Claude Code, or Pi CLI —
inside the **Sandbox Runner** (default Kali container, via the container engine) or the explicit
opt-in **Host Runner**. **Provider Bridges** frame provider protocols between daemon and Runtime.
Inside the Runtime environment, **pentestctl** is the Project Interface: it publishes FGS updates
and reads Blackboard state over an authenticated Continuation Interface. The Runtime calls the
**Model Provider** directly (never proxied by the daemon) and tests **Authorized Target Systems**
within Project Scope.

The **TSecBench Hosted Image** is a separate, self-starting linux/amd64 distribution (ADR 0026):
its **Hosted Controller** boots an in-process pentestd, seeds one evaluation Project and Task,
runs bounded platform operations through the process-isolated **Hosted Challenge Client**, and
emits a JSONL transcript. The image bundles the Runtimes, a bounded Kali toolset, and a read-only
offline knowledge baseline under `/opt/knowledge` (ADR 0037).

## Views and artifacts

| File | View | Question it answers |
| --- | --- | --- |
| `system-context.dsl` | C4 System Context (L1) | What is the system boundary, who uses it, what external systems does it touch? |
| `system-model.dsl` → `CyberPendaContainers` | C4 Container (L2) | What are the runnable/deployable parts of the control plane? |
| `system-model.dsl` → `PentestdComponents` | C4 Component (L3) | Which responsibilities live inside the daemon? |
| `system-model.dsl` → `HostedImageContainers` | C4 Container (L2) | What is inside the TSecBench Hosted Image? |
| `system-model.evidence.md` | Evidence index | Which code/config/docs prove each node and edge? |

Open a `.dsl` file with Qoder's Structurizr DSL viewer for visual inspection. The DSL files are
the source of truth; rendered previews are derived artifacts.

## Config Projection ownership

The Config Projection module captures launch dependencies and uses that prepared state
for native files and the process environment. Grant-dependent rendering receives the
Continuation grant, Continuation ID, and prepared Working Graph paths without new
database reads. Native launch-argument construction stays outside this module.

Task and Session keep their own lifecycle ordering, grant issuance, Working Graph
preparation, and failure settlement. Task Precommit runs before the Continuation
transaction; BindGrant runs inside it. Session prepares its Continuation and grant
before projection. Preflight and Runtime Profile configuration previews remain
read-only; previews do not materialize launch credentials.

Database rollback does not provide whole-projection filesystem rollback. Task retains
its existing Working Blackboard Snapshot restoration; other projected files can remain
changed after a failure. Session retains its failed-Continuation settlement without
restoring projected files. Resume reuses owner-local paths, so these directories must
not be treated as disposable output from the latest projection attempt.

Tests cross the Config Projection interface and retain the Task and Session launch
tests. They check matching files and environment, fixed captured inputs, returned
errors, and existing failure behavior. Inline SQLite persistence remains unchanged
under ADR 0017.

## Evidence strength

Almost all nodes and edges are **high confidence**: they trace to package doc comments
(e.g. internal/runtime/runtime.go:1-5, internal/fgs/fgs.go:1-2), route registrations
(internal/daemon/server.go:939+), Dockerfiles, ADRs, and README sections. Two items are weaker:

- **GHCR image pull (medium)** — image coordinates are documented; pull mechanics are engine behavior.
- **Sandbox container as a boundary** — drawn as node descriptions, not a C4 container, because it
  is a deployment environment. Use `deployment-topology-analyzer` if a runtime topology view is needed.

## Deliberate exclusions

- Retired or read-only legacy surface (Challenge Workflow history, Working Graph Intents, Blackboard
  v1 migration, the daemon `/mcp` endpoint which now returns 404, Task Policy metadata) is recorded
  in the evidence file but kept out of the diagrams.
- Runtime behavior below container level (provider session wire protocols, subagent paging) is not
  modeled; internal/runtime and cmd/pentest-provider-bridge are the entry points.

## Validation gaps / next steps

1. Confirm the GHCR pull edge against a live engine if distribution accuracy matters.
2. For per-Runtime protocol detail (Codex app-server, Pi RPC, Claude Agent SDK), route to
   `flow-visualizer` with internal/runtime as the scope.
3. For Sandbox/Host/Compose/Hosted runtime placement, route to `deployment-topology-analyzer`.
4. Keep this model current: when internal/ package responsibilities change, update
   `system-model.dsl` and re-check the evidence index (candidate for `architecture-health`).

## Maintenance note

Regenerate or edit the `.dsl` files in place; do not hand-edit rendered exports. The evidence
index lists every claim's source; drop or relabel a node when its source is removed.
