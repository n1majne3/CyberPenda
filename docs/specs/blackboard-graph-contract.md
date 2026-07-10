# Blackboard Typed Property Graph Contract

- **Status:** implementation contract for [Specify typed Blackboard graph schema and invariants](https://github.com/n1majne3/CyberPenda/issues/56)
- **Map:** [Map: Refactor Blackboard into bounded graph memory](https://github.com/n1majne3/CyberPenda/issues/55)
- **Destination decision:** [Grill: Lock graph-first Blackboard destination and invariants](https://github.com/n1majne3/CyberPenda/issues/54)
- **Schema version:** `1`

This document fixes the domain contract for CyberPenda's project-local Blackboard graph. It deliberately does not choose SQLite tables, HTTP paths, MCP tool names, CLI syntax, UI layout, migration sequencing, or compaction algorithms; those decisions belong to the downstream map tickets.

The keywords **MUST**, **MUST NOT**, **SHOULD**, and **MAY** are normative.

## 1. Boundary and module seam

The Blackboard is the canonical project-local semantic memory. Task lifecycle, Runtime execution, Continuations, Scope Snapshots, raw Task Events, full logs, and artifact payload bytes remain outside the graph. Graph records refer to those systems through provenance.

All graph writes cross one deep module seam:

```go
type BlackboardGraphService interface {
	Apply(ctx context.Context, batch MutationBatch) (MutationResult, error)
}
```

HTTP, MCP, CLI, migration, reconciliation, interruption recovery, and system projections are adapters at this seam. They MUST NOT duplicate schema, lifecycle, endpoint, provenance, idempotency, merge, or project-isolation rules.

The service accepts semantic mutation operations, not arbitrary graph queries, SQL, or unrestricted property maps. Read interfaces may be split later, but all reads MUST resolve aliases and enforce Project isolation in the same domain module.

## 2. Project prerequisites

The graph belongs to exactly one Project. Every node, edge, alias, mutation, and graph revision is scoped to one `project_id`; cross-Project references are invalid.

Project has a kind outside the graph:

| Project kind | Meaning |
| --- | --- |
| `pentest` | A bounded security-testing engagement. Tasks complete against their Task Goals; the Project has no automatic solved state. |
| `ctf_challenge` | One challenge whose Project is solved when the current graph contains a verified flag Solution. |

Project kind is supplied to the graph service by the Project domain and is never caller-controlled graph data.

## 3. Graph record envelopes

### 3.1 Node envelope

Every node has this envelope:

```json
{
  "id": "opaque immutable id",
  "project_id": "project id",
  "node_type": "project_fact",
  "stable_key": "dns:example.com",
  "version": 3,
  "disposition": "main",
  "properties": {},
  "created_at": "RFC3339Nano",
  "updated_at": "RFC3339Nano",
  "created_provenance": {},
  "updated_provenance": {},
  "merge_target_id": null
}
```

Envelope rules:

- `id`, `project_id`, and `node_type` are immutable.
- `stable_key` is immutable and unique within `(project_id, node_type)` across both live keys and aliases.
- `version` starts at `1` and increases only when current semantic state changes. Replay and exact no-op writes do not create versions.
- `disposition` is `main`, `archived`, or `merged`.
- `merge_target_id` is required only when disposition is `merged`.
- Unknown envelope fields or unknown type-specific properties are rejected for schema version `1`.
- A node's semantic type never changes. A Hypothesis becoming established creates or updates a ProjectFact, Finding, or Solution and links the records; it does not mutate the Hypothesis into another type.

Disposition rules:

- `main` nodes appear in the current compact graph.
- `archived` nodes remain addressable and historical but do not appear in the current compact graph or frontier.
- `merged` nodes are immutable redirect records. Reads and ordinary writes through their stable keys resolve to the canonical node.
- `main -> archived` is allowed only when the node satisfies its type's archive guard and no active edge would become dangling.
- `archived -> main` is an explicit restore operation by an operator or system actor.
- `main|archived -> merged` occurs only through Merge Nodes and is terminal.

### 3.2 Edge envelope

Every edge has this envelope:

```json
{
  "id": "opaque immutable id",
  "project_id": "project id",
  "edge_type": "supports",
  "from_node_id": "source id",
  "to_node_id": "target id",
  "version": 1,
  "state": "active",
  "summary": "optional concise explanation",
  "created_at": "RFC3339Nano",
  "updated_at": "RFC3339Nano",
  "created_provenance": {},
  "updated_provenance": {}
}
```

Edge rules:

- Edge direction is semantic and MUST NOT be silently reversed.
- Active edge identity is unique by `(project_id, edge_type, from_node_id, to_node_id)`.
- `state` is `active` or `retired`; edges are retired, never hard-deleted through the domain interface.
- `summary` is the only writable core edge property in schema version `1`.
- Self-edges are forbidden for every core edge type.
- Both endpoints MUST resolve to current canonical nodes in the same Project.
- New active edges cannot target or originate from archived nodes. Alias references are resolved before validation.

### 3.3 Provenance

Every operation, node version, and edge version carries provenance:

```json
{
  "actor_type": "runtime",
  "actor_id": "runtime identity",
  "task_id": "task id",
  "continuation_id": "continuation id",
  "runtime_profile_id": "runtime profile id",
  "runner": "sandbox",
  "source_event_ids": ["event id"],
  "migration_source": null,
  "recorded_at": "RFC3339Nano"
}
```

| Field | Rule |
| --- | --- |
| `actor_type` | Required: `runtime`, `operator`, `system`, or `migration`. |
| `actor_id` | Required stable identity of the runtime, operator, subsystem, or migration batch. |
| `task_id` | Required for Runtime writes; optional for operator/system writes made outside a Task. |
| `continuation_id` | Required for Runtime writes and MUST belong to `task_id`. |
| `runtime_profile_id` | Required for Runtime writes and MUST match the Continuation's captured runtime configuration. |
| `runner` | Required for Runtime writes: `sandbox` or `host`; MUST match the Continuation. |
| `source_event_ids` | Optional references to Task Events that materially support the mutation. All must belong to `task_id`. |
| `migration_source` | Required only for `migration`; contains the legacy store, record key, and optional version. |
| `recorded_at` | Server-generated. Callers cannot set or override it. |

Trusted task context binds all Runtime provenance. A Runtime may not claim another Project, Task, Continuation, Runtime Profile, Runner, actor, or timestamp. CLI used inside a Task receives the same bound context. CLI used outside a Task requires an explicit operator or migration identity and cannot fabricate Runtime provenance.

## 4. Stable keys, references, and aliases

All node types have a stable key because every node in this graph is durable semantic memory, even if it may later be archived.

New stable keys:

- MUST match `[a-z0-9][a-z0-9._:/-]{0,159}`.
- Are case-sensitive, lowercase by contract, and are not silently normalized beyond trimming surrounding ASCII whitespace.
- SHOULD carry a readable type/domain prefix, for example `host:example.com`, `objective:enumerate-admin`, or `attempt:task-id:recon-1`.
- Are unique within node type, not across different node types. A ProjectFact and Finding may therefore retain the same legacy key without identity collision.
- Never change in place. A replacement identity uses a new node plus `supersedes`; a duplicate identity uses Merge Nodes.

Node references accepted by the service are either an immutable `id`, a `(node_type, stable_key)` pair, or a same-batch operation reference. If both `id` and key are supplied, they MUST resolve to the same node.

Alias rules:

- Aliases are scoped by `(project_id, node_type)` and obey the same key grammar for new records. Migration MAY preserve nonconforming legacy aliases.
- An alias points directly to one current canonical node; alias chains are flattened and alias cycles are forbidden.
- An alias cannot shadow a live stable key or another alias.
- Reads and ordinary writes through an alias resolve to the canonical node and report `resolved_from_alias` in the result.
- Create Node never resolves an alias into an update; it returns `node_key_conflict` so accidental duplicate creation is visible.

## 5. Node schemas

Strings marked required MUST be non-empty after trimming. Timestamps are RFC3339Nano. Unknown properties are rejected.

### 5.1 Goal

Goal is a read-only graph projection of a Task Goal. Task remains the owner.

| Property | Requirement | Meaning |
| --- | --- | --- |
| `task_id` | Required, immutable, system-bound | Owning Task. |
| `text` | Required, immutable through graph operations | User-authored Task Goal. |
| `task_status` | Required, system-managed | `pending`, `running`, `paused`, `completed`, `failed`, `stopped`, or `interrupted`. |

Rules:

- Stable key is system-derived as `task:<task_id>:goal`.
- Only the system Task projection may create or patch a Goal.
- Goal status mirrors Task status and is not a graph lifecycle decision.
- Goal is not mergeable or supersedable.

### 5.2 Entity

Entity identifies what Blackboard knowledge or work is about.

| Property | Requirement | Meaning |
| --- | --- | --- |
| `kind` | Required, immutable | Controlled Entity kind from section 6. |
| `name` | Required | Human-readable name. |
| `locator` | Kind-dependent | Non-secret canonical address, path, symbol, or identifier. |
| `description` | Optional | Concise identifying context. |
| `scope_status` | Required | `in_scope`, `out_of_scope`, or `unknown`. Visibility never grants authorization. |
| `credential_ref` | Credential only | Non-secret Credential Reference. Raw credential values are forbidden. |
| `status` | Required, default `active` | `active`, `retired`, or `superseded`. |

Transitions: `active -> retired|superseded`, `retired -> active|superseded`; `superseded` is terminal and requires one incoming active `supersedes` edge.

### 5.3 ExplorationObjective

ExplorationObjective is durable Project-level interrogative planning state. It is not Current Truth and is not a Task Goal.

| Property | Requirement | Meaning |
| --- | --- | --- |
| `objective` | Required | Precise investigation question or desired conclusion. |
| `status` | Required, default `open` | `open`, `resolved`, `abandoned`, or `superseded`. Blocking is derived, not stored. |
| `resolution_summary` | Required for terminal states | Concise reason for resolution, abandonment, or replacement. |
| `resolved_at` | System-managed for terminal states | Transition timestamp. |

Transitions: `open -> resolved|abandoned|superseded`. Terminal states do not reopen; renewed investigation creates a new Objective linked with `derived_from` or `supersedes`.

Additional guards:

- `resolved` requires an incoming active `satisfies` edge from a ProjectFact, Finding, or Solution in the same mutation or current graph.
- `superseded` requires an incoming active `supersedes` edge.
- An Objective SHOULD be linked to its parent Goal with `part_of` when it decomposes a Task Goal.

### 5.4 Attempt

Attempt is one exploration episode, not one command or tool call.

| Property | Requirement | Meaning |
| --- | --- | --- |
| `status` | Required, default `open` | `open`, `succeeded`, `failed`, `blocked`, `inconclusive`, or `interrupted`. |
| `summary` | Optional while open; required when terminal | Distilled account of what the episode established. |
| `ended_at` | System-managed when terminal | Terminal transition timestamp. |

Rules:

- Runtime-created Attempts require Runtime provenance.
- An open Attempt requires at least one active outgoing `tests` edge in the same mutation or current graph.
- `open` may transition once to any terminal state. Terminal states are immutable.
- Only the system interruption reconciler may set `interrupted`.
- `succeeded` requires at least one active outgoing `produced` edge.
- Commands, tool calls, and full output remain Task Events, logs, or EvidenceArtifact payloads.

### 5.5 Observation

Observation is a significant observed result, including a useful negative result. It is not raw output and is not automatically a reusable assertion.

| Property | Requirement | Meaning |
| --- | --- | --- |
| `summary` | Required | Compact observed result. |
| `detail` | Optional | Additional semantic detail, never a full log dump. |
| `observed_at` | Optional | When the underlying event was observed; defaults to recorded time. |
| `scope_status` | Required | `in_scope`, `out_of_scope`, or `unknown`. |
| `status` | Required, default `recorded` | `recorded` or `superseded`. |

Transition: `recorded -> superseded`; `superseded` requires an incoming active `supersedes` edge.

### 5.6 Hypothesis

Hypothesis is a testable proposition, not Current Truth.

| Property | Requirement | Meaning |
| --- | --- | --- |
| `statement` | Required | The proposition being tested. |
| `rationale` | Optional | Why the proposition is plausible. |
| `status` | Required, default `open` | `open`, `supported`, `contradicted`, `inconclusive`, or `superseded`. |

Transitions:

- `open -> supported|contradicted|inconclusive|superseded`
- `inconclusive -> open|supported|contradicted|superseded`
- `supported -> contradicted|superseded`
- `contradicted -> supported|superseded`
- `superseded` is terminal

`supported` requires an incoming active `supports` edge. `contradicted` requires an incoming active `contradicts` edge. `superseded` requires an incoming active `supersedes` edge.

### 5.7 ProjectFact

ProjectFact remains the reusable project assertion and Current Truth primitive.

| Property | Requirement | Meaning |
| --- | --- | --- |
| `category` | Required | Stable classification such as `dns`, `service`, `access`, or `progress`; migration may use `uncategorized`. |
| `summary` | Required | Compact assertion used in index and graph projections. |
| `body` | Optional | Full semantic detail, excluding raw proof payloads. |
| `confidence` | Required, default `tentative` | `tentative`, `confirmed`, or `deprecated`. |
| `scope_status` | Required | `in_scope`, `out_of_scope`, or `unknown`. |

Transitions: `tentative -> confirmed`, `confirmed -> tentative`, and `tentative|confirmed -> deprecated`. Deprecated is terminal for that identity; a renewed assertion uses a new ProjectFact with `derived_from` or `supersedes`.

Rules:

- Confirmed means supported by evidence, reproduction, human confirmation, or independent corroboration.
- Creating or transitioning to `confirmed` requires at least one of: an incoming `evidences` edge; an incoming `supports` edge from an Observation or confirmed ProjectFact; an incoming `produced` edge from an Attempt that is `succeeded` in the final graph and has matching Task/Continuation provenance; or operator/system/migration provenance with a non-empty body recording the confirmation basis.
- Updating with an omitted optional field preserves it. Clearing uses an explicit `clear` field list.
- Current Truth includes `tentative` and `confirmed` ProjectFacts, with uncertainty and scope status explicit; it excludes deprecated, archived, and merged nodes.

### 5.8 Finding

Finding is a reportable security issue and remains separate from ProjectFact.

| Property | Requirement | Meaning |
| --- | --- | --- |
| `title` | Required | Report-facing issue title. |
| `description` | Optional | Issue description. |
| `status` | Required, default `unconfirmed` | `unconfirmed`, `confirmed`, or `false_positive`. |
| `target` | Required when confirmed | Affected target or entry point. |
| `proof` | Required when confirmed | Compact proof description; raw proof is EvidenceArtifact content. |
| `impact` | Required when confirmed | Security impact. |
| `recommendation` | Required when confirmed | Remediation guidance. |
| `cvss_version` | Required when confirmed | `4.0` for new findings; `3.1` is accepted for compatibility imports. |
| `cvss_vector` | Required when confirmed | Complete vector valid for `cvss_version`. |
| `severity` | Derived, read-only | Derived from CVSS; callers cannot set it. |
| `cvss_pending` | Derived, read-only | True when no complete vector exists. |

Transitions: `unconfirmed -> confirmed|false_positive`; `confirmed -> false_positive`; `false_positive` is terminal.

Confirmation requires complete report fields plus at least one active incoming `evidences` edge from EvidenceArtifact or `supports` edge from a confirmed ProjectFact. The graph service validates both the field completeness and supporting edge in the same atomic batch/current graph.

### 5.9 Solution

Solution represents a CTF challenge conclusion. It is invalid in a Pentest Project.

| Property | Requirement | Meaning |
| --- | --- | --- |
| `kind` | Required, immutable | `flag`, `answer`, or `procedure`. |
| `summary` | Required | Concise explanation. |
| `value` | Required for `flag` and `answer` | Exact flag/answer value. |
| `status` | Required, default `candidate` | `candidate`, `verified`, `rejected`, or `superseded`. |
| `verification_summary` | Required when verified | How the value was verified or accepted. |

Transitions: `candidate -> verified|rejected|superseded`; `verified -> rejected|superseded`; `rejected|superseded` are terminal. Superseded requires an incoming `supersedes` edge.

A verified flag Solution requires Runtime/operator/system provenance, a non-empty flag value, and an outgoing active `satisfies` edge to the producing Task's Goal. A CTF Challenge Project is derived as solved when at least one main, non-merged `Solution(kind=flag,status=verified)` exists. If all verified flags become rejected or superseded, the derived solved state becomes false; history remains intact.

### 5.10 EvidenceArtifact

EvidenceArtifact is a durable reference to proof content under an Artifact Root; payload bytes remain outside the graph.

| Property | Requirement | Meaning |
| --- | --- | --- |
| `artifact_type` | Required | `http_exchange`, `screenshot`, `terminal_capture`, `log`, `pcap`, `file`, `binary`, `source_code`, `structured_data`, `report`, or `other`. |
| `media_type` | Optional | MIME type when known. |
| `source_path` | Optional | Original source path or source reference. |
| `managed_path` | Required | Path under the managed Artifact Root. |
| `sha256` | Required when status is `available` | Lowercase hex digest of content. |
| `size_bytes` | Optional | Non-negative payload size. |
| `summary` | Required | What the artifact proves or contains. |
| `status` | Required, default `available` | `available`, `missing`, or `superseded`. |
| `captured_at` | Optional | Source capture time. |

Transitions: `available <-> missing`; `available|missing -> superseded`; superseded is terminal and requires an incoming `supersedes` edge.

EvidenceArtifact SHOULD have at least one outgoing `evidences` edge before it is relied upon to confirm a Finding or support the ProjectFact, Finding, or Solution that satisfies an Objective.

### 5.11 ProjectDirective

ProjectDirective is durable, project-scoped strategy. It is imperative, advisory, and never Current Truth.

| Property | Requirement | Meaning |
| --- | --- | --- |
| `directive` | Required | Strategy steer for future work. |
| `rationale` | Optional | Why the steer is useful. |
| `status` | Required | `proposed`, `active`, `retired`, or `superseded`. Runtime writes default to `proposed`; operator/system writes may start `active`. |

Transitions: `proposed -> active|retired|superseded`; `active -> retired|superseded`; `retired|superseded` are terminal. Superseded requires an incoming `supersedes` edge.

A Runtime may propose a Directive but MUST NOT activate one. Operator or system approval transitions it to active. A Directive is never included in Current Truth or Reports and does not enforce Runtime behavior.

## 6. Controlled Entity kinds

| Kind | Locator rule | Typical use |
| --- | --- | --- |
| `network` | CIDR or stable network identifier required | Network or subnet. |
| `host` | Hostname or stable host identifier recommended | Machine or device. |
| `ip_address` | Valid IPv4 or IPv6 address required | Address identity distinct from a host. |
| `domain` | DNS name required | Domain or subdomain. |
| `service` | Host/service/port/protocol locator required | Listening or remote service. |
| `endpoint` | Absolute URL, route, RPC method, or equivalent locator required | Callable application entry point. |
| `application` | Product/instance locator recommended | Web app, API, daemon, or deployed application. |
| `identity` | Non-secret account/principal identifier required | User, role, service account, or principal. |
| `credential` | `credential_ref` required; secret values forbidden | Pointer to managed authentication material. |
| `data_store` | Non-secret database/storage locator recommended | Database, bucket, queue, or datastore. |
| `file` | Normalized path or file identifier required | File or document. |
| `binary` | Path, hash, or binary identifier required | Executable or binary object. |
| `function` | Symbol/module/address locator required | Function, method, handler, or code symbol. |
| `challenge_component` | Stable challenge-local locator recommended | CTF-specific object not covered by another kind. |

Entity `part_of` child -> parent kind pairs are restricted to:

- `host -> network`
- `network -> network`
- `ip_address -> host|network`
- `domain -> host|application`
- `service -> host|ip_address`
- `endpoint -> service|application`
- `application -> host|service`
- `identity -> application|service`
- `credential -> identity|application|service`
- `data_store -> application|service|host`
- `file -> host|application|binary|challenge_component`
- `binary -> host|application|challenge_component`
- `function -> binary|application|challenge_component`
- `challenge_component -> challenge_component`

The Entity `part_of` graph MUST be acyclic.

## 7. Controlled edges and endpoint matrix

| Edge | Direction and allowed endpoints |
| --- | --- |
| `about` | `Goal|ExplorationObjective|Attempt|Observation|Hypothesis|ProjectFact|Finding|Solution|EvidenceArtifact|ProjectDirective -> Entity` |
| `part_of` | `Entity -> Entity` using section 6 kind pairs; `ExplorationObjective -> Goal` |
| `tests` | `Attempt -> ExplorationObjective|Hypothesis|Entity` |
| `produced` | `Attempt -> Observation|Hypothesis|ProjectFact|Finding|Solution|EvidenceArtifact` |
| `evidences` | `EvidenceArtifact -> Observation|Hypothesis|ProjectFact|Finding|Solution` |
| `supports` | `Observation|Hypothesis|ProjectFact -> Hypothesis|ProjectFact|Finding|Solution` |
| `contradicts` | `Observation|Hypothesis|ProjectFact -> Observation|Hypothesis|ProjectFact|Finding|Solution` |
| `derived_from` | `Entity|ExplorationObjective|Observation|Hypothesis|ProjectFact|Finding|Solution|EvidenceArtifact|ProjectDirective -> Goal|Entity|ExplorationObjective|Attempt|Observation|Hypothesis|ProjectFact|Finding|Solution|EvidenceArtifact|ProjectDirective` |
| `depends_on` | `ExplorationObjective -> ExplorationObjective`; dependent points to prerequisite |
| `blocks` | `ExplorationObjective -> ExplorationObjective`; blocker points to blocked Objective |
| `leads_to` | `ExplorationObjective|Attempt|Observation|Hypothesis|ProjectFact|Finding -> ExplorationObjective|Attempt|Observation|Hypothesis|ProjectFact|Finding|Solution` |
| `satisfies` | `ProjectFact|Finding|Solution -> ExplorationObjective|Goal` |
| `supersedes` | Same-type pairs only for `Entity`, `ExplorationObjective`, `Observation`, `Hypothesis`, `ProjectFact`, `Finding`, `Solution`, `EvidenceArtifact`, or `ProjectDirective`; replacement points to replaced node |

Semantic rules:

- `about` names the Entity a record concerns; it does not imply `part_of` or scope authorization.
- `produced` identifies the Attempt that emitted semantic output. Runtime-created Observation, Hypothesis, ProjectFact, Finding, Solution, or EvidenceArtifact nodes MUST have an incoming `produced` edge from an Attempt with matching Task and Continuation provenance.
- `evidences` means the artifact is proof for the target. It is stronger than merely being produced in the same Attempt.
- `supports` and `contradicts` are directed assertions. The reverse edge is not implied.
- `depends_on` and `blocks` jointly form the Objective prerequisite graph and MUST be acyclic. For cycle detection, `A depends_on B` is treated as prerequisite arc `B -> A`; `A blocks B` is already `A -> B`.
- `satisfies` is the only edge that resolves an Objective or proves a Goal in the core schema.
- `supersedes` is acyclic. A superseded node may have at most one active incoming `supersedes` edge. Duplicate identities use Merge Nodes instead.
- `derived_from` and `leads_to` preserve semantic lineage but do not by themselves change lifecycle state.

## 8. Derived views and invariants

### 8.1 Frontier

Frontier is derived and never persisted as a node, status, queue, claim, or lease.

An ExplorationObjective is on the frontier when all are true:

1. It is `disposition=main` and `status=open`.
2. Every active `depends_on` target has `status=resolved`.
3. Every active Objective with a `blocks` edge into it has `status=resolved`.

An abandoned or superseded prerequisite is not considered resolved; graph health may flag the dependent Objective as stranded. Frontier ordering is not part of this contract.

### 8.2 Current Truth

Current Truth is the projection of main, canonical ProjectFacts with confidence `tentative` or `confirmed`. It excludes deprecated, archived, and merged ProjectFacts. Tentative and out-of-scope entries remain explicitly marked.

Observation, Hypothesis, Finding, Solution, ExplorationObjective, Goal, Entity, EvidenceArtifact, and ProjectDirective are not ProjectFacts and do not enter Current Truth merely by existing.

### 8.3 Task and Attempt reconciliation

- Task/Continuation state remains outside the graph.
- When a Continuation ends unexpectedly, the system transitions each matching open Attempt to `interrupted`, adds the best available summary, and links available Events/Artifacts. It does not change Task status semantics.
- A later Continuation creates a new Attempt; it does not reopen the interrupted Attempt.

### 8.4 CTF solved state

`ctf_solved` is a Project projection computed from current Solution nodes as defined in section 5.9. It is not a stored graph node and is independent from whether an individual Task has completed.

### 8.5 Main-graph integrity

- No active edge may reference an archived or merged endpoint.
- No node may be archived while required by an active Goal, open Objective, open Attempt, active Directive, unresolved contradiction, confirmed Finding, or verified Solution unless the archiving mutation also rewires/retires those dependencies atomically.
- Raw Task Events, commands, tool calls, logs, and payload bytes MUST NOT be copied into node properties to evade bounded-memory rules.

## 9. Mutation batch contract

```json
{
  "schema_version": 1,
  "idempotency_key": "continuation-7:checkpoint-3",
  "operations": []
}
```

The service applies the entire batch atomically after validating its final proposed graph. Operation order may create local references, but no partial state is visible. A failed operation rolls back every operation in the batch.

Each operation has a unique `op_id`. Node/edge references may point to an existing ID, an existing `(node_type, stable_key)`, or a prior same-batch `op_id`.

Allowed operations:

| Operation | Required behavior |
| --- | --- |
| `create_node` | Create a typed node with full required properties. Existing live key or alias is a conflict. Goal creation is system-only. |
| `patch_node` | Explicit `set` map plus `clear` list; preserves omitted properties; requires `expected_version`. Cannot change immutable fields or semantic status. |
| `transition_node` | Change the type-specific lifecycle field and MAY atomically set fields required by that transition, such as `summary` or `resolution_summary`; requires `expected_version` and all transition guards. |
| `put_edge` | Create an edge or no-op an identical edge. Updating an existing summary requires `expected_version`. |
| `retire_edge` | Set edge state to retired; requires `expected_version` and must leave the final graph valid. |
| `set_disposition` | Archive or restore a node; requires `expected_version` and archive guards. Merge is not performed here. |
| `merge_nodes` | Merge one node into another same-type canonical node using section 11; requires expected versions for both. |

There is no hard-delete node operation and no arbitrary property/edge operation.

`expected_version` is mandatory for changes to existing state. A mismatch fails the whole batch with `version_conflict`; the service never silently applies a stale patch.

## 10. Idempotency

Every mutation batch requires an `idempotency_key` matching `[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}`.

The system derives the idempotency scope:

- Runtime/Task CLI: `continuation:<continuation_id>`
- Operator outside a Task: `operator:<actor_id>`
- System: `system:<actor_id>`
- Migration: `migration:<actor_id>`

The service canonicalizes the validated batch, excluding server-generated fields, and records its payload hash with the result.

- Same scope + key + same payload hash returns the original result exactly and creates no versions, timestamps, or duplicate edges.
- Same scope + key + different payload hash returns `idempotency_conflict`.
- A Create Node retried under a new idempotency key still returns `node_key_conflict`; natural identity is not used to hide a replay bug.
- `put_edge` with an already-identical active edge is an ordinary no-op and returns the current edge.
- `patch_node` whose normalized result equals current state is an ordinary no-op and does not increment version.

## 11. Merge semantics

Merge Nodes is explicit identity consolidation, never semantic-role conversion and never similarity-based automation.

Preconditions:

- Source and canonical nodes differ, belong to the same Project, and have the same node type.
- Both are current canonical nodes with disposition `main` or `archived`.
- Goal and Attempt are not mergeable.
- The caller supplies current expected versions for source and canonical nodes.
- The canonical stable key is retained. The source stable key becomes an alias.

Atomic effects:

1. Apply an optional explicit `canonical_patch` to the canonical node. Without this patch, canonical properties win; source properties never silently overwrite them.
2. Preserve both nodes' prior versions and the merge provenance in history.
3. Rewire every active incoming and outgoing source edge to the canonical node.
4. Collapse duplicate rewired edges by edge identity, retaining the canonical edge and the most recent non-empty summary in history.
5. Retire any rewired edge that would become a forbidden self-edge, recording that retirement in mutation history.
6. Repoint all source aliases directly to the canonical node, then add the source stable key as an alias.
7. Set source disposition to `merged`, set `merge_target_id`, and remove it from the main graph.

Semantic similarity may create a Blackboard Health duplicate candidate, but only an explicit Merge Nodes mutation changes identity.

## 12. Validation error contract

All validation failures use a stable machine-readable shape:

```json
{
  "code": "edge_endpoint_type",
  "message": "supports cannot connect evidence_artifact to finding",
  "operation_index": 2,
  "op_id": "link-proof",
  "path": "operations[2].from",
  "retryable": false,
  "details": {}
}
```

Core codes:

| Code | Meaning |
| --- | --- |
| `unsupported_schema_version` | Mutation schema version is not supported. |
| `invalid_request` | Malformed operation or reference. |
| `unknown_node_type` | Node type is not controlled vocabulary. |
| `unknown_edge_type` | Edge type is not controlled vocabulary. |
| `unknown_property` | Property is not defined for the node type/version. |
| `missing_property` | Required property is absent or empty. |
| `invalid_property` | Property value, format, or enum is invalid. |
| `project_not_found` | Project does not exist. |
| `project_mismatch` | Reference/provenance belongs to another Project. |
| `project_kind_violation` | Node or transition is not allowed for the Project kind. |
| `node_not_found` | Node reference cannot be resolved. |
| `node_key_conflict` | Stable key is already live or reserved by an alias. |
| `alias_conflict` | Alias shadows another key/alias or points across type/Project. |
| `alias_cycle` | Alias or merge would create a redirect cycle. |
| `immutable_field` | Mutation attempts to change ID, Project, type, stable key, or another immutable property. |
| `version_conflict` | `expected_version` does not equal current version. Retryable after reread. |
| `invalid_transition` | Lifecycle transition is not allowed. |
| `transition_guard_failed` | Required supporting edge/property/provenance is missing. |
| `edge_endpoint_not_found` | Edge endpoint does not exist. |
| `edge_endpoint_type` | Endpoint types/direction violate section 7. |
| `edge_conflict` | Existing edge differs and no valid expected version was supplied. |
| `self_edge_forbidden` | Edge resolves to the same canonical node. |
| `graph_cycle` | `part_of`, Objective prerequisites, or `supersedes` would cycle. |
| `provenance_required` | Required bound provenance is missing. |
| `provenance_spoofed` | Caller-supplied provenance conflicts with trusted context. |
| `idempotency_conflict` | Same idempotency scope/key was used for a different payload. |
| `merge_self` | Source and canonical node are identical after alias resolution. |
| `merge_type_mismatch` | Source and canonical types differ or type is non-mergeable. |
| `merge_conflict` | Merge cannot preserve graph invariants without explicit resolution. |
| `archive_guard_failed` | Node is still live/required by the current graph. |
| `invariant_violation` | Final proposed graph violates another named contract invariant. |

Validation errors are domain errors. Transport-specific status codes and error envelopes are decided by the interface ticket.

## 13. TDD acceptance seam

Implementation MUST proceed test-first at `BlackboardGraphService.Apply`; tests MUST observe results through this interface rather than querying SQLite internals. Storage-adapter tests may separately prove persistence/recovery behind the seam.

Minimum red-first behavioral matrix:

1. Every node type accepts its minimal valid property set and rejects missing, unknown, and invalid properties.
2. Every controlled Entity kind accepts valid kind-specific locator rules; raw Credential values and unknown kinds are rejected.
3. Every allowed edge pair/direction succeeds; every other pair or reversed direction fails with `edge_endpoint_type`.
4. Cross-Project node, edge, alias, provenance, and Event references fail without partial writes.
5. Goal is created and synchronized only by the Task projection.
6. Objective frontier is derived correctly across `depends_on`, `blocks`, resolved, abandoned, superseded, archived, and merged prerequisites.
7. Attempt terminal transitions enforce summary, `tests`, `produced`, Runtime provenance, and system-only interruption.
8. Hypothesis supported/contradicted transitions require matching incoming edges.
9. Confirmed ProjectFact requires evidence, support, a succeeded producing Attempt, or explicit trusted confirmation provenance.
10. Confirmed Finding requires complete CVSS/report fields and EvidenceArtifact or confirmed ProjectFact support.
11. Verified flag Solution is CTF-only, requires `satisfies` to its Task Goal, and drives reversible derived Project solved state.
12. Identical replay returns the original result; altered replay fails with `idempotency_conflict`.
13. Stale node or edge mutation fails with `version_conflict` and preserves current state.
14. Stable-key aliases resolve reads/writes, block duplicate creation, remain one-hop, and never cross types.
15. Merge preserves canonical properties, versions, aliases, evidence/semantic edges, and prevents self-edge/cycle corruption.
16. Semantic roles cannot mutate into other node types.
17. Archive/restore preserves history and never leaves an active dangling edge.
18. Unknown fields and enums fail closed under schema version `1`.
19. MCP, CLI, HTTP, migration, and system adapters later pass the same conformance suite against the shared service.

## 14. Worked mutation example

This example atomically records a successful exploration episode and resolves its Objective. Transport adapters may render a narrower interface, but the domain result must be equivalent.

```json
{
  "schema_version": 1,
  "idempotency_key": "task-7:attempt-admin-enum:complete",
  "operations": [
    {
      "op_id": "fact",
      "kind": "create_node",
      "node_type": "project_fact",
      "stable_key": "endpoint:admin-panel",
      "properties": {
        "category": "endpoint",
        "summary": "The application exposes /admin behind authentication",
        "body": "GET /admin returns the authenticated administration shell.",
        "confidence": "confirmed",
        "scope_status": "in_scope"
      }
    },
    {
      "op_id": "produced-fact",
      "kind": "put_edge",
      "edge_type": "produced",
      "from": {"node_type": "attempt", "stable_key": "attempt:task-7:admin-enum"},
      "to": {"op_id": "fact"}
    },
    {
      "op_id": "satisfy-objective",
      "kind": "put_edge",
      "edge_type": "satisfies",
      "from": {"op_id": "fact"},
      "to": {"node_type": "exploration_objective", "stable_key": "objective:find-admin-surface"}
    },
    {
      "op_id": "finish-attempt",
      "kind": "transition_node",
      "node": {"node_type": "attempt", "stable_key": "attempt:task-7:admin-enum"},
      "expected_version": 1,
      "to_status": "succeeded",
      "set": {"summary": "Confirmed the authenticated administration endpoint."}
    },
    {
      "op_id": "resolve-objective",
      "kind": "transition_node",
      "node": {"node_type": "exploration_objective", "stable_key": "objective:find-admin-surface"},
      "expected_version": 2,
      "to_status": "resolved",
      "set": {"resolution_summary": "The administration endpoint was identified and confirmed."}
    }
  ]
}
```

## 15. Explicit downstream boundaries

This contract intentionally leaves these questions to existing map tickets:

- SQLite tables, graph revision storage, append-only mutation history, recovery, compaction, budgets, and Blackboard Health persistence.
- MCP tools, CLI commands, HTTP routes, snapshot serialization, Runtime instructions, and adapter-specific request shapes.
- Read projections, dense Blackboard UI, Graph Explorer, reports, and compatibility views.
- Legacy data migration, cutover, rollback, and compatibility command translation.
- Full acceptance matrix sequencing and implementation slices.

No additional wayfinding ticket is required by this schema decision; those decisions are already represented in the map.
