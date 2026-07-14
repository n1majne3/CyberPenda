# Research: CyberPenda Blackboard and Task surfaces today

**Wayfinder ticket:** [#49 Research: CyberPenda Blackboard and Task surfaces today](https://github.com/n1majne3/CyberPenda/issues/49)

**Map:** [#46 Map: Leverage Cairn graph base into CyberPenda](https://github.com/n1majne3/CyberPenda/issues/46)
**Date:** 2026-07-09

This ticket records what CyberPenda already owns in the Blackboard and Task orchestration areas that Cairn-like leverage would touch. It intentionally does **not** adopt, adapt, reject, or defer anything; that belongs to the gap-analysis and decision tickets.

## Sources (primary)

| Source | Role |
|--------|------|
| `CONTEXT.md` | Domain language and relationships for Project, Scope, Task, Blackboard, Project Fact, Fact Relation, Finding, Evidence Artifact, Harness Steering, Attack Chain |
| `docs/product/prd.md` | Product boundary, goals, non-goals, functional requirements |
| `docs/product/mvp.md` | MVP scope, current build status, deferred capabilities |
| `docs/product/implementation-plan.md` | Implemented slices, transport notes, remaining work |
| `internal/store/store.go` | SQLite schema for projects, tasks, task events, task continuations, fact/finding/evidence tables |
| `internal/project/project.go` | Project, Scope, Project Defaults service |
| `internal/blackboard/facts.go` | Project Fact, Fact Index, Fact Relation, Fact Merge service behavior |
| `internal/blackboard/findings.go` | Finding, Finding Version, Finding Merge service behavior |
| `internal/blackboard/evidence.go` | Evidence Artifact service behavior |
| `internal/task/task.go` | Task, Run Controls, Scope Snapshot, events, continuations, summaries |
| `internal/runtime/runtime.go` | Runtime Harness and adapter lifecycle |
| `internal/daemon/blackboard_handlers.go` | HTTP blackboard, finding, evidence, report routes |
| `internal/daemon/task_handlers.go` | HTTP task launch, resume, steering, summary, handoff routes |
| `internal/mcpserver/server.go` | Built-in trusted MCP project-interface tools |
| `internal/pentestctl/pentestctl.go` | CLI fallback commands |
| `internal/runner/mcp.go` | Trusted MCP projection and task-context files |
| `internal/runner/projection.go` | Task-local runtime configuration projection |
| `internal/report/report.go` | Markdown report assembly from stored state |

---

## One-line essence

CyberPenda already has a local-first **Project** with project-local **Blackboard** memory and a goal-driven **Task** lifecycle. The current model has durable facts, fact relations, findings, evidence, task events, continuations, summaries, steering, trusted MCP, and CLI fallback; it does **not** currently have a first-class Intent, Hint, graph frontier, reason lease, or multi-worker claim/heartbeat dispatcher.

Sources: `docs/product/prd.md:11-16`, `docs/product/prd.md:40-48`, `docs/product/mvp.md:23-45`, `internal/store/store.go:110-274`.

---

## Product and MVP bounds

CyberPenda is a **local-first Pentest Agent**. The daemon owns control-plane, memory-plane, task lifecycle, and reporting-plane responsibilities; pentest tools run inside runtime environments rather than being proxied by the daemon. Project data, task artifacts, runtime config projections, blackboard memory, and reports stay local unless exported. Sources: `docs/product/prd.md:11-16`, `docs/product/prd.md:52-58`.

The first-release boundary explicitly excludes a cloud platform, daemon-level pentest tool proxying, packet/command enforcement, a full typed attack graph, daemon-as-LLM-summarizer behavior, chat as the primary surface, and identical live steering across runtime adapters. Sources: `docs/product/prd.md:40-48`, `docs/product/mvp.md:153-168`.

The current MVP is described as implemented and release-ready for local use: backend, React UI, MCP server, CLI fallback, blackboard, findings, evidence, report generation, sandbox runner command construction, real adapter launch logic, task lifecycle, steering, continuation, and tests are complete. Real binary smoke validation is out-of-band. Sources: `docs/product/mvp.md:23-45`, `docs/product/mvp.md:47-50`, `docs/product/implementation-plan.md:15-60`.

The relevant MVP non-goals for this map are **full typed attack graph**, **external worker process split**, **automatic semantic summarization**, and **automatic fact/finding key normalization**. Sources: `docs/product/mvp.md:153-168`.

---

## Domain anchors

CyberPenda's **Blackboard** is project-local memory for durable facts, relationships, findings, and evidence. A **Project Fact** is a stable, project-scoped assertion reusable by later tasks without carrying raw proof content. A **Fact Index** is a compact current-truth view exposing key, category, summary, confidence, and scope status without full bodies. Sources: `CONTEXT.md:447-480`.

A **Fact Relation** connects two Project Facts and does not directly connect Findings. The current glossary deliberately avoids calling it an attack graph edge. An **Attack Chain** is a narrative path that connects Project Facts and Findings; it is not a separate graph source of truth, and stable Attack Chain summaries are stored as Project Facts. Sources: `CONTEXT.md:491-497`, `CONTEXT.md:831-836`, `CONTEXT.md:923-924`.

A **Task** starts from one **Task Goal** plus **Run Controls**, resolves to one runtime profile, chooses one runner, has one runtime harness, and runs under exactly one immutable **Scope Snapshot**. A Task may contain internal steps, but those steps are not separate Tasks. Harness Steering is represented as Task Events and affects Runtime Continuations, not Task identity and not an already-running internal reasoning step. Sources: `CONTEXT.md:752-797`.

Runtimes use **Project Interfaces** to read/write Project state. The CLI fallback has the same project semantics as other Project Interfaces, and direct storage changes outside Project Interfaces require Reconciliation before they affect Current Truth or Reports. Sources: `CONTEXT.md:798-801`.

---

## Storage shape

The current SQLite schema has project, task, blackboard, finding, and evidence tables, but no table named or shaped as Intent, Hint, Reason lease, exploration frontier, or worker claim. Sources: `internal/store/store.go:59-274`.

Task storage:

| Table | Current role |
|-------|--------------|
| `tasks` | One user-goal-driven project run with status, runner, runtime profile, run controls JSON, immutable scope snapshot JSON |
| `task_runtime_config_versions` | Historical task-specific runtime configuration versions |
| `task_continuations` | Runtime Continuation records with number, runtime provider, runner, status, container id, native session id/path |
| `task_events` | Structured task timeline entries |
| `task_summary_versions` | Versioned compact task summaries |

Source: `internal/store/store.go:110-164`.

Blackboard and reporting storage:

| Table | Current role |
|-------|--------------|
| `project_facts` | Current Project Facts keyed by `(project_id, fact_key)` |
| `project_fact_versions` | Historical fact versions keyed by `(project_id, fact_key, version)` |
| `project_fact_relations` | Directed fact-to-fact relation rows keyed by source, target, relation |
| `fact_key_aliases` | Historical Fact Key redirects after merge |
| `findings` | Current reportable issues keyed by `(project_id, finding_key)` |
| `finding_versions` | Historical Finding versions |
| `finding_key_aliases` | Historical Finding Key redirects after merge |
| `evidence_artifacts` | Evidence records attached to a fact or finding key |

Source: `internal/store/store.go:165-274`.

---

## Project and Scope surface

`project.Scope` is structured as domains, IPs, CIDRs, URLs, ports, exclusions, testing limits, and notes. A Project stores Scope and Project Defaults. Project Defaults choose a default runtime profile, runner, and task policy, but do not copy global runtime profiles. Sources: `internal/project/project.go:20-62`.

Task creation captures the current Project Scope into the Task's immutable Scope Snapshot. The snapshot is historical authorization and does not change if the current Project Scope changes later. Sources: `internal/task/task.go:51-54`, `internal/task/task.go:207-260`, `CONTEXT.md:792-793`.

Scope expansion exists as domain language and runtime instruction text, but the current source does not expose a built-in MCP tool named `request_scope_expansion`. `internal/runner/mcp.go` instructs runtimes to use trusted MCP `request_scope_expansion`, and `docs/product/implementation-plan.md` says MVP tools include it, while `internal/mcpserver/server.go` currently registers only fact/search/deprecate/relation/finding/evidence/report/task-summary tools. Sources: `CONTEXT.md:862`, `internal/runner/mcp.go:174-177`, `docs/product/implementation-plan.md:375-379`, `internal/mcpserver/server.go:34-247`.

---

## Blackboard facts today

`blackboard.Fact` contains id, project id, stable fact key, category, summary, body, confidence, scope status, created timestamp, and updated timestamp. `FactIndexEntry` intentionally excludes body and exposes key, category, summary, confidence, and scope status. Sources: `internal/blackboard/facts.go:34-53`.

Fact confidence values are `tentative`, `confirmed`, and `deprecated`. Scope status values are `in_scope` and `out_of_scope`. If a fact write omits confidence, the service defaults it to tentative. Sources: `internal/blackboard/facts.go:19-32`, `internal/blackboard/facts.go:138-149`.

Fact upsert semantics are identity-by-Fact-Key. A missing key or summary is rejected. A new key creates a row and appends version 1. An existing key updates current category, summary, confidence, scope status, and body; if the incoming body is empty, the existing body is preserved. Every update appends a version row. Sources: `internal/blackboard/facts.go:123-210`, `internal/blackboard/facts.go:651-681`.

Fact Index returns compact current-truth rows ordered by updated time and excludes deprecated facts by default. Search can match fact key, summary, or body, but still returns compact Fact Index entries; deprecated facts are excluded unless requested. Sources: `internal/blackboard/facts.go:89-94`, `internal/blackboard/facts.go:212-245`, `internal/blackboard/facts.go:259-300`.

Fact deprecation is implemented as an update to confidence `deprecated`, preserving body and history. Sources: `internal/blackboard/facts.go:302-316`.

Fact Merge consolidates a source Fact Key into a canonical Fact Key, copies source version history under the canonical key, redirects source/target fact relations, deletes the source current fact row, and records an alias so future reads/writes through the old key resolve to the canonical key. Sources: `internal/blackboard/facts.go:439-517`, `internal/blackboard/facts.go:547-606`.

---

## Fact Relations today

`blackboard.FactRelation` stores id, project id, source Fact Key, target Fact Key, relation string, summary, and timestamps. The service requires source key, target key, and relation to be non-empty, and requires both endpoint facts to exist. Sources: `internal/blackboard/facts.go:68-77`, `internal/blackboard/facts.go:96-102`, `internal/blackboard/facts.go:345-364`.

Fact Relation upsert is keyed by `(project_id, source_fact_key, target_fact_key, relation)`. A missing relation creates a row; an existing relation updates only the relation summary and timestamp. There is no relation version table. Sources: `internal/blackboard/facts.go:365-405`, `internal/store/store.go:191-201`.

The product docs and MCP schema name these relation strings: `supports`, `contradicts`, `depends_on`, `leads_to`, and `duplicates`. The service itself currently validates only non-empty relation text rather than enumerating the allowed set. Sources: `docs/product/mvp.md:100-110`, `internal/mcpserver/server.go:277-283`, `internal/blackboard/facts.go:345-357`.

Fact Relations are retrieved by source Fact Key only. Sources: `internal/blackboard/facts.go:408-437`, `internal/daemon/blackboard_handlers.go:244-268`.

---

## Findings and Evidence today

Findings have separate stable identity from facts: `finding_key`, current `version`, title, description, status, target, proof, impact, recommendation, CVSS version/vector, CVSS pending flag, severity, and timestamps. Sources: `internal/blackboard/findings.go:11-69`.

Finding upsert preserves unspecified fields when updating an existing finding. New findings default to unconfirmed when status is omitted. A confirmed finding is rejected unless it has CVSS vector, target, proof, impact, and recommendation. Severity is derived from the CVSS vector, and an empty vector leaves `cvss_pending` true. Sources: `internal/blackboard/findings.go:78-162`.

Finding Merge preserves source version history under the canonical key, redirects evidence attachments to the canonical finding key, deletes the source current finding, and records a finding-key alias. Sources: `internal/blackboard/findings.go:264-320`.

Evidence Artifacts attach to either a fact or finding key, not to arbitrary task output. Attachment requires evidence key, target key, artifact type, and an existing target fact/finding. Evidence records source path, managed path, sha256, summary, and timestamps. Sources: `internal/blackboard/evidence.go:12-43`, `internal/blackboard/evidence.go:50-113`, `internal/blackboard/evidence.go:173-184`.

Managed evidence paths are derived under `artifacts/<evidence_key>/<source basename>`. Source: `internal/blackboard/evidence.go:216-222`.

---

## Task model today

`task.Task` is a user-goal-driven run with project id, goal, status, runner, runtime profile id, run controls, immutable scope snapshot, runtime controls, active/latest continuation pointers, and timestamps. Sources: `internal/task/task.go:139-154`.

`task.RunControls` currently carries host activation, sandbox network mode, notes, and an extras map. The runner is stored separately because it gates execution boundary visibility. Sources: `internal/task/task.go:20-49`.

Task Events are structured, compact timeline entries with per-task sequence numbers. Event kinds are `runtime_output`, `status`, `steering`, `conversation`, and `lifecycle`. Raw output is supposed to stay in logs or evidence artifacts rather than being stored as complete event dumps. Sources: `internal/task/task.go:56-79`, `internal/task/task.go:339-421`, `CONTEXT.md:779`, `CONTEXT.md:861`.

Runtime configuration versions are task-specific and versioned. A runtime-profile switch inside a task creates a new task runtime configuration version, not a new task. Sources: `internal/task/task.go:81-91`, `internal/task/task.go:423-468`, `CONTEXT.md:768-769`.

Runtime Continuations are first-class task records. Each continuation has a per-task number, runtime profile id, runtime provider, runner, lifecycle status, optional container id, optional native session id/path, and timestamps. Sources: `internal/task/task.go:93-109`, `internal/task/task.go:470-557`, `internal/task/task.go:559-629`.

Task Summary updates are automatically accepted and versioned. The task continuation endpoint returns the latest Task Summary when one exists; otherwise it returns a mechanical handoff packet with task id, project id, goal, runtime profile id, runner, scope domains/notes, run controls, event count, and config-version count. Sources: `internal/task/task.go:130-137`, `internal/task/task.go:665-737`, `internal/daemon/task_handlers.go:1386-1467`.

---

## Runtime Harness and launch surface

The Runtime Harness launches, resumes, steers, and stops a runtime for one task, but does not execute pentest tools itself. Adapters are provider-specific and emit normalized events. Sources: `internal/runtime/runtime.go:1-30`, `CONTEXT.md:794-797`.

A task launch applies project defaults, runs preflight, validates host/sandbox activation, creates the task, builds a launch plan, records the initial runtime config, and launches the runtime in the background. Sources: `internal/daemon/task_handlers.go:47-139`.

For real runtime profiles, launch projection prepares a task-local layout, rewrites loopback targets for sandbox tasks, computes the trusted MCP endpoint, projects runtime config into the provider home, resolves model provider snapshot data when needed, constructs launch args, injects process environment, and records a redacted launch command in the task runtime config. Sources: `internal/daemon/task_handlers.go:239-395`, `internal/runner/projection.go:23-116`.

The harness lifecycle is: register active run, emit lifecycle `started`, mark task and continuation running, run adapter, emit lifecycle final phase, collect native/container metadata when available, update task and continuation terminal status, and unregister active run. Sources: `internal/runtime/runtime.go:46-150`.

The fake adapter emits runtime output events and exercises the same harness/event contract without a real runtime. Sources: `internal/runtime/runtime.go:211-230`.

---

## Resume and steering today

Task detail exposes runtime controls computed from the current runtime plugin and latest continuation. Controls include handoff resume, queued steering, native resume availability, interrupt steering availability, whether native session was captured, and same-runtime-provider-only. Sources: `internal/daemon/task_handlers.go:546-626`, `internal/task/task.go:111-121`.

Handoff resume builds a prompt from stored state rather than replaying the whole timeline. `buildResumeGoal` includes:

- the compact Fact Index as `fact_key: summary` lines;
- full bodies for `progress:*` facts;
- the latest findings by key/title/status;
- the latest Task Summary, if any;
- the latest steering directive from Task Events.

Source: `internal/daemon/task_handlers.go:897-979`.

Queued steering records a Task Event with phase `steering_requested`, mode `queue`, directive, optional submitter, optional runtime profile id, optional model provider id, and optional model override. If a runtime/model selection is included, it records a task runtime configuration version for a future continuation. Sources: `internal/daemon/task_handlers.go:994-1059`, `internal/daemon/task_handlers.go:1219-1340`.

Active steering records a steering request, optionally records selected runtime configuration, prepares a native resume request, emits lifecycle `interrupting`, stops the current harness run, builds a native-resume launch plan, records another runtime config, emits lifecycle `resuming_native`, launches the resumed continuation, and records a `steering_applied` event. Sources: `internal/daemon/task_handlers.go:1061-1206`.

Runtime profile steering must keep the same runtime provider. Model-provider steering may create a launch-resolved runtime profile for the same runtime provider with the selected model provider/model override. Sources: `internal/daemon/task_handlers.go:1261-1325`.

---

## Trusted project interfaces today

HTTP exposes project-scoped routes for fact upsert, fact index, full fact lookup, fact merge, fact versions, fact relations, finding upsert/list/versions/merge, evidence list/attach, report trigger, task CRUD/events/timeline/transcript/stop/resume/steering/summary, and dashboard. The blackboard HTTP handlers call the shared blackboard service rather than duplicating business logic. Sources: `internal/daemon/blackboard_handlers.go:13-503`, `internal/daemon/task_handlers.go:47-1560`.

The built-in trusted MCP server is a thin transport over the same domain services. Current registered tools are:

- `upsert_project_fact`
- `get_project_fact`
- `list_project_facts`
- `search_project_facts`
- `deprecate_project_fact`
- `upsert_fact_relation`
- `record_vulnerability`
- `list_vulnerabilities`
- `attach_evidence`
- `generate_report`
- `submit_task_summary`

Sources: `internal/mcpserver/server.go:1-4`, `internal/mcpserver/server.go:34-247`, `internal/mcpserver/server.go:252-320`.

The `pentestctl` CLI fallback currently supports `fact upsert`, `task summary put`, `evidence attach`, `finding upsert`, and `report generate`. It opens the same store and calls the same project, blackboard, task, and report services used by the daemon. Sources: `internal/pentestctl/pentestctl.go:1-45`, `internal/pentestctl/pentestctl.go:47-278`.

Trusted MCP is automatically added to runtime MCP configuration unless disabled by `PENTEST_DISABLE_TRUSTED_MCP`. The trusted server URL is adjusted for sandbox reachability and may carry the daemon auth token as a query parameter because some runtime MCP transports cannot attach per-request headers. Sources: `internal/runner/mcp.go:67-93`.

Task context files are written under `.pentest/` in the task workdir with project id, task id, MCP URL, and scope snapshot. The generated task `AGENTS.md` tells runtimes to use trusted MCP for blackboard writes, record facts after recon phases, record findings, attach evidence, submit task summaries, and stay within the Scope Snapshot. Sources: `internal/runner/mcp.go:105-187`.

---

## Report surface today

Reports are generated deliverables, not source of truth. The report generator reads findings, compact Fact Index entries, evidence artifacts, and task scope/runner context from stored state; it does not read raw runtime output. Sources: `internal/report/report.go:1-16`, `internal/report/report.go:55-122`.

The Markdown report separates confirmed and unconfirmed findings, renders fact summaries from the Fact Index, and renders evidence references. It does not currently render Fact Relations as a graph or Attack Chain structure beyond whatever stable Attack Chain summary facts exist in the Fact Index. Sources: `internal/report/report.go:184-333`, `CONTEXT.md:835-836`, `CONTEXT.md:866-869`.

---

## Current closest equivalents to Cairn-shaped concepts

This section names surfaces only; it does not recommend reuse.

| Cairn-shaped concern | Current CyberPenda surface |
|----------------------|----------------------------|
| Durable knowledge node | Project Fact, with stable Fact Key, confidence, scope status, body, and versions |
| Compact graph/context projection | Fact Index, plus full fact lookup on demand |
| Causal/semantic link | Fact Relation between two Project Facts |
| Reportable issue | Finding, separate from Project Fact |
| Raw/derived proof | Evidence Artifact attached to a fact or finding |
| Exploration run | Task / Runtime Continuation |
| Runtime work history | Task Events, Task Summary Versions, Runtime Config Versions, Continuations |
| Steering instruction | Harness Steering Task Event |
| Resume context | Latest Task Summary or Mechanical Handoff Packet; handoff prompt includes Fact Index, progress facts, findings, latest summary, latest steering directive |
| Agent write channel | Trusted MCP server or CLI fallback |
| Agent-safe task context | Projected `.pentest/context.json`, `.pentest/scope.json`, and generated task `AGENTS.md` |

No current first-class equivalent was found for Cairn's Intent hyperedge, open frontier, Hint, project-level Reason lease, or claim/heartbeat worker coordination.

---

## Facts likely to matter for the next gap-analysis ticket

- CyberPenda's Fact Relation is fact-to-fact only; it does not directly connect Findings and is explicitly not an attack graph source of truth. Sources: `CONTEXT.md:831-836`, `CONTEXT.md:906`.
- Stable Attack Chain material is stored as Project Facts and presented narratively, not as a typed graph. Sources: `CONTEXT.md:923-924`, `docs/product/prd.md:160`.
- Task orchestration already owns continuation, steering, resume, runtime config versioning, and task summary mechanics. Sources: `internal/task/task.go:81-154`, `internal/daemon/task_handlers.go:755-1206`, `internal/daemon/task_handlers.go:1386-1544`.
- Handoff/resume already treats `progress:*` facts specially by injecting their full bodies. Source: `internal/daemon/task_handlers.go:915-932`.
- The built-in trusted MCP tool list is blackboard/report/task-summary centric; it has no current Intent/frontier/claim/heartbeat/scope-expansion tool in source. Source: `internal/mcpserver/server.go:34-247`.
- The CLI fallback is narrower than HTTP/MCP for read/search/relation operations; it mainly writes facts, task summaries, findings, evidence, and reports. Source: `internal/pentestctl/pentestctl.go:31-45`.
- The storage schema currently has no generic graph-edge lifecycle beyond Fact Relations, no relation versions, and no task-to-fact or finding-to-fact join table. Source: `internal/store/store.go:110-274`.
