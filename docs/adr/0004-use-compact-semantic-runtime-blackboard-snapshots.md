# Use compact semantic Runtime Blackboard snapshots

A Runtime Continuation receives a pinned, topology-complete view of Current Work, Project Knowledge, and their current semantic relationships instead of a lossless rendering of storage, audit, and task-history fields. This preserves the full current reasoning graph while removing Task Goals already supplied at launch, Trusted Origin data, internal IDs, infrastructure identifiers, lifecycle timestamps, auxiliary bodies, repeated relationship summaries, terminal workflow records, and other audit-only metadata. Full semantic details remain addressable on demand through project-isolated Blackboard Keys.

## Snapshot shape

The top level identifies `schema`, explains the selection `semantics`, names the pinned `revision`, and contains `work`, `knowledge`, and `relations`. Work and knowledge are grouped by record type, and each project-wide Blackboard Key is the map key, so records do not repeat their key or type. Relationships use `[from_key, relation_type, to_key]`; only `supports`, `contradicts`, and `depends_on` may add a concise non-redundant reason. Separate Frontier and Current Truth key lists are not carried.

Serialization is deterministic. Top-level fields are ordered `schema`, `semantics`, `revision`, `work`, `knowledge`, `relations`; work groups are ordered objectives, attempts; knowledge groups are ordered entities, facts, findings or solutions, evidence. Keys sort lexicographically and relationships sort by source, type, then target. Empty type groups and absent optional fields are omitted without `null`, while `work`, `knowledge`, and `relations` always exist. The canonical JSON is compact, and the same semantic state produces identical bytes and internal hash.

Current Work uses this explicit allowlist:

| Group | Fields |
| --- | --- |
| `objectives` | `version`, `status`, `objective` |
| `attempts` | `version`, `status`, `summary` |

The first Project Knowledge groups use this allowlist:

| Group | Fields |
| --- | --- |
| `entities` | `version`, `status`, `kind`, `name`, optional `locator`, optional concise `description`, `scope_status`, optional non-secret `credential_ref` |
| `facts` | `version`, `category`, `summary`, `confidence`, `scope_status` |
| `findings` | `version`, `status`, `title`, optional `target`, optional concise `description`, optional derived `severity`, `cvss_pending` |
| `solutions` | `version`, `status`, `kind`, `summary`, optional `value`, optional concise `verification_summary` |
| `evidence` | `version`, `status`, `artifact_type`, `summary`, optional `media_type`, optional semantically relevant `captured_at` |

Entity descriptions are compact identifying context, Evidence time appears only when it changes the conclusion, and Project Fact bodies are read by Blackboard Key when needed. Finding proof, impact, recommendation, CVSS version/vector, and full description are detail fields read by key; the startup view retains enough information to identify, prioritize, and recognize incomplete validation or scoring. Candidate and verified Solutions remain current; rejected and superseded Solutions leave the snapshot after any reusable rejection reason becomes a Project Fact. Evidence source/managed paths, digest, and size are fetched by key when the Runtime chooses to inspect the artifact.

Before false-positive, deprecated, rejected, superseded, or otherwise invalidated Project Knowledge leaves the Runtime Snapshot, a reusable invalidation reason is retained as a concise Project Fact or current replacement record. A superseded record identifies its replacement. An invalidation with no reusable meaning may leave without creating an empty placeholder.

Field names remain explicit rather than abbreviated. Lifecycle or validation state stays local to each record even when its section implies currentness. New storage or Blackboard fields are excluded until deliberately admitted to the Runtime allowlist.

Blackboard Keys are at most 96 ASCII characters. Primary Runtime semantic text (`objective`, Attempt summary, Fact/Solution/Evidence summary, and Finding description) is at most 1024 UTF-8 bytes. Optional identifying description or relationship reason is at most 512 UTF-8 bytes. Oversized writes fail with a stable error directing supporting detail into on-demand record content or Evidence rather than silently truncating it.

## Continuity

The snapshot is self-describing when read without its original launch prompt. Launch and resume instructions explain that work is active, knowledge is current, and terminal records, history, and details are read by key. The task-local snapshot remains available for rereading after long execution or context compaction; a resumed Continuation receives a newly pinned snapshot.

Each Continuation retains an immutable internal Launch Blackboard Pin and exposes a task-local Working Blackboard Snapshot at `.pentest/blackboard.json`. The Working Snapshot starts at the pin, advances atomically after the Runtime's own successful semantic writes, and is the file reread after long execution or context compaction. When another Task advances the same Project's semantic graph, the service sends active Continuations one coalesced Blackboard Change Notice containing only last-acknowledged and current revisions. It does not asynchronously inject the graph.

The first trusted Blackboard tool or checkpoint response after a pending external change piggybacks the complete current `runtime-blackboard/v2`, updates the Working Snapshot, and acknowledges the notice. A one-line structured explanation says that another Task changed shared project knowledge and that the attached revision is now current; no Task identity is exposed. With no unseen external change, a successful write returns only its semantic delta plus Working Snapshot path/revision. A resumed Continuation receives a new Launch Pin and Working Snapshot.

The model-visible launch header contains only the selected Runner, Scope path, Blackboard path, snapshot schema, and pinned revision. Project, Task, Continuation, Runtime Configuration, Runtime Profile, and Runtime Plugin identifiers; API/MCP URLs; projection hash and size; token estimate; and protocol digest remain in trusted server bindings or diagnostics rather than Runtime attention. Removing those fields does not weaken Project isolation because the Continuation Interface Grant remains authoritative.

The previous long protocol block and repeated trusted-tool catalog are replaced by one adapter-appropriate projection of this compact checklist:

1. Reread Scope and the Blackboard snapshot before planning, after context compaction, and after resume.
2. Write semantic milestones only; commands, logs, and raw output stay outside the Blackboard.
3. Write with Blackboard Keys and current versions, and reuse the same idempotency key for an uncertain retry.
4. Exploration flows through an open Attempt, reusable outcome records, and a terminal Attempt.
5. Blackboard scope labels never grant authorization, and Finish occurs only after every Attempt is terminal.

Tool schemas, server validation, stable errors, and on-demand protocol documentation carry operation detail. A Runtime with a reliable persistent instruction channel receives the checklist there and not again in the launch prompt; other adapters receive it once in their launch prefix.

## On-demand reads

Runtime reads by Blackboard Key use a separate semantic record contract rather than operator or storage DTOs. A current detail response names its schema, observed graph revision, Blackboard Key, record type and version, then returns the type-specific semantic record and relationships by key. It may include startup-omitted content such as a Fact body, Finding proof/impact/recommendation, or Evidence path and integrity details when requested, but never Project/Task/Continuation identifiers, Trusted Origin data, storage hashes, or non-semantic lifecycle timestamps.

There is no separate Fact Index projection or `list_project_facts` Runtime protocol. Current facts already appear once in `knowledge.facts` with their keys, categories, summaries, confidence, and scope status; full bodies remain available through the normal read-by-key contract. Blackboard v2 retires the duplicate legacy protocol without a long-lived compatibility layer.

Current detail and semantic history are separate requests. Terminal workflow records and old semantic versions appear only after an explicit history request, and history does not expand Trusted Origin data. Internal origin inspection remains an advanced diagnostic operation outside the Runtime Blackboard contract.

## Ordinary Blackboard UI

The operator-facing Blackboard uses the same Current Work, Project Knowledge, and relationship semantics as the Runtime rather than a richer audit-oriented read model. Lists show the same allowlisted fields; record selection loads semantic detail by Blackboard Key; Semantic History is an explicit secondary action. Provenance JSON, internal IDs, graph/storage hashes, source events, and Recent Changes audit streams are removed from normal Blackboard pages. Health focuses on Runtime Snapshot attention budget, semantic integrity, and actionable anomalies, while Graph Explorer uses Blackboard Keys and current relationships only. Trusted Origin and database inspection remain advanced diagnostics outside the Blackboard UI.

## Consequences

The Runtime Snapshot is the canonical serialized Continuation input rather than a renderer that must reconstruct every field of a storage-oriented graph document. Semantic and topological completeness replace transport-field losslessness, so the Runtime projection requires a new schema/version and conformance tests. Current semantic state and per-record history follow [ADR 0006](./0006-store-versioned-semantic-state-not-a-graph-event-ledger.md); exact Snapshot bytes are stored at pin time rather than regenerated by replaying an operation ledger.

Runtime token budgeting measures the exact deterministic `runtime-blackboard/v2` bytes that a Continuation receives, including its self-describing header, current records, and relationships. Storage history, Trusted Origin, operator DTOs, and on-demand detail payloads are excluded. Database growth and integrity use separate health metrics and cannot trigger semantic-memory compaction merely because invisible storage metadata grew. Each semantic write remeasures the Runtime projection, and Continuation pinning hashes the same deterministic bytes internally.

The default attention budget is 16K tokens as the healthy target, 32K as warning, and 64K as semantic-consolidation-required. These are attention thresholds for the user's 200K-to-1M-context models, not context-window limits. A snapshot above any threshold remains complete and launchable; it is never truncated or relevance-filtered.

Budget states trigger visibility, not automatic semantic mutation. At 32K the product offers Blackboard consolidation; at 64K it persistently marks consolidation required while continuing to launch and accept the complete graph. An explicitly requested Reason Task may propose duplicate merges, tentative-to-confirmed Fact refinement, supersession, summary tightening, and relation cleanup. The operator approves proposals before they are applied; even exact or likely duplicates are candidates rather than silent merges.

The server keeps only the internal Trusted Origin bindings needed to enforce Project/Continuation ownership, reconcile interrupted Attempts, preserve Evidence integrity, and diagnose corruption or concurrency failures. Runtime snapshots, Runtime detail reads, ordinary Blackboard pages, and reports do not expose those bindings. The expanded Provenance JSON view is retired from normal product surfaces; exceptional inspection belongs to advanced diagnostics rather than Blackboard content.
