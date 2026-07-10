# Blackboard SQLite Persistence, History, Compaction, and Health Contract

- **Status:** implementation contract for [Specify SQLite graph persistence, history, compaction, and health](https://github.com/n1majne3/CyberPenda/issues/57)
- **Map:** [Map: Refactor Blackboard into bounded graph memory](https://github.com/n1majne3/CyberPenda/issues/55)
- **Domain contract:** [Blackboard Typed Property Graph Contract](./blackboard-graph-contract.md)
- **Storage schema version:** 1
- **Budget estimator:** utf8_bytes_div_4_v1
- **Compaction policy:** blackboard_compaction_v1
- **Health checker:** blackboard_health_v1

This document fixes the SQLite representation and consistency contract for the typed Blackboard graph. It defines the canonical ledger, rebuildable current indexes, transaction and recovery behavior, deterministic budget policy, semantic compaction, archived-subgraph restoration, and persisted Blackboard Health cache.

The keywords **MUST**, **MUST NOT**, **SHOULD**, and **MAY** are normative.

## 1. Decision

The canonical Blackboard store is an append-only full-state ledger:

- accepted mutation batches and their exact results;
- normalized trusted provenance and source Event references;
- immutable node and edge identities;
- complete node and edge versions;
- append-only stable-key and alias namespace events;
- append-only compaction manifests.

Mutable current-head tables contain only latest-version pointers and indexed derived fields. They are materialized indexes, not an independent source of truth. Deleting and rebuilding every head table from the ledger MUST produce the same current graph, key namespace, state hash, main-graph projection hash, and graph revision.

This rejects two alternatives:

- Pure event replay is rejected because reconstruction would depend on old reducer code and old schema semantics.
- Mutable current JSON plus copied audit rows is rejected because both copies could claim to be authoritative.

Full post-image versions let the current graph and any historical graph revision be reconstructed without replaying domain behavior. The legacy Fact/Finding/Evidence tables cease to be canonical after the migration ticket cuts over; compatibility paths translate into this one graph service rather than dual-writing both stores.

## 2. Module seam and ownership

All semantic writes continue to cross the deep module seam fixed by the graph contract:

~~~go
type BlackboardGraphService interface {
	Apply(ctx context.Context, batch MutationBatch) (MutationResult, error)
}
~~~

SQLite is a local-substitutable dependency. Production and tests use the real SQLite adapter; the module MUST NOT expose a table-shaped repository interface merely to mock its implementation.

HTTP, MCP, CLI, migration, Goal projection, reconciliation, interruption recovery, and compaction are adapters at the Apply seam. None may write graph tables directly or duplicate lifecycle, endpoint, merge, alias, provenance, idempotency, archive, or Project-isolation rules.

The implementation resolves a sealed server-side MutationExecutionContext from context.Context. It carries trusted provenance plus mutation_kind and optional maintenance metadata such as compaction policy/plan or restore manifest reference. Callers cannot serialize or spoof it. The graph module derives ordinary and merge kinds from operations, permits compaction/restore/reconciliation/migration metadata only to their trusted adapters, validates that metadata against the final operations, and includes it in the ledger/hash chain.

Read modules may expose snapshots, Current Truth, Frontier, history, and Health, but each read MUST:

1. name the graph revision it observed;
2. resolve aliases and merge redirects for ordinary semantic reads;
3. enforce Project isolation;
4. read from one SQLite snapshot transaction.

Explicit history/audit reads MAY address a literal merged identity without redirecting it, but MUST label its disposition and resolved canonical target.

## 3. Required surrounding schema

The graph remains Project-local while Task and Runtime lifecycle stay outside it. The surrounding store therefore needs these durable fields before graph cutover:

- **projects.kind**: pentest or ctf_challenge, defaulting legacy Projects to pentest during migration;
- **task_events.continuation_id**: nullable for legacy rows, required for new Runtime lifecycle/output Events;
- **task_events.attempt_node_id**: nullable generally, required for Attempt checkpoint/summary Events;
- **task_summary_versions.continuation_id**: nullable for legacy rows, populated for new Runtime submissions;
- **task_continuations.runtime_config_version_id**: durable reference to the captured Task Runtime Configuration Version;
- immutable **task_continuations.blackboard_graph_revision**, renderer/estimator versions, projection hash, bytes, and estimated tokens as defined in section 12.3.
- **task_continuations.blackboard_reconciliation_status**, reconciliation mutation ID, and reconciled_at for durable normal/crash-recovery completion tracking.

The migration contract defines the explicit nullable/legacy-not-applicable values for pre-graph Continuations when no exact Runtime Configuration Version or historical graph pin can be reconstructed. Every post-cutover Continuation requires the complete fields above.

Project kind is immutable after Project creation or legacy backfill. A future conversion between Pentest Project and CTF Challenge Project requires an explicit new migration/design decision because kind changes graph validity and solved-state semantics. Project kind participates in graph state/projection hashes.

Runtime Profile provenance MUST be checked against the Continuation's captured runtime configuration. It MUST NOT use a cascading foreign key to a live Runtime Profile row that may later be removed.

Schema changes use numbered transactional migrations with:

- migration version;
- migration checksum;
- applied timestamp;
- one BEGIN IMMEDIATE transaction per migration.

Startup MUST refuse to run against a newer unknown schema version or an applied migration whose checksum no longer matches. The existing unversioned list of CREATE TABLE IF NOT EXISTS statements is not sufficient for the graph ledger.

## 4. Version domains

These counters are distinct and MUST NOT be reused:

| Domain | Meaning |
| --- | --- |
| SQLite migration version | Physical database schema revision. |
| Mutation schema_version | Semantic request contract version from MutationBatch. |
| mutation_seq | Per-Project sequence for every first-seen accepted batch, including all-no-op batches. |
| graph_revision | Per-Project semantic state revision; increments once only when a batch changes current graph state. |
| Node/edge/key version | Per-record semantic version; increments only when that record's current state changes. |
| Snapshot revision | Immutable graph_revision captured for one Continuation. |
| Health checked revision | Graph revision evaluated by one Health run. |

Exact idempotent replay consumes none of these counters and returns the originally stored result bytes.

## 5. SQLite connection and transaction contract

Persistent databases MUST use:

- WAL journal mode;
- foreign_keys=ON on every connection;
- busy_timeout=5000 or greater;
- synchronous=FULL for canonical graph commits;
- UTC RFC3339Nano timestamps;
- one serialized writer per process.

With modernc.org/sqlite, store opening MUST apply these settings to every possible connection by using repeated DSN _pragma parameters or a registered connection hook, and MUST select immediate transaction locking with _txlock=immediate (or an equivalent dedicated connection implementation). One-time PRAGMA Exec calls on sql.DB are insufficient if a replacement/pool connection can be opened. Tests MUST read the effective PRAGMAs from the transaction connection.

An empty-path in-memory database remains valid for fast unit tests, but recovery, WAL, reopen, and crash-boundary tests MUST use a temporary file-backed database.

Every graph mutation uses BEGIN IMMEDIATE. This reserves the SQLite writer before reading the current Project revision, so concurrent daemon and CLI writers cannot both allocate the same sequence or validate against a stale head. Optimistic expected_version checks remain the semantic concurrency mechanism.

All persistence helpers used during Apply MUST receive the transaction-scoped executor/connection. They MUST NOT call the shared database pool from inside the writer transaction; with the current one-connection store that can deadlock and it would also escape the atomic consistency boundary.

One daemon owns normal orchestration for a database. Concurrent CLI writers are allowed and serialize through SQLite. Two independent daemon owners of the same database are unsupported.

SQLite busy/locked exhaustion is a retryable storage error. It is not a validation error and MUST NOT consume the idempotency key.

## 6. Canonical ledger tables

All tables in this section are append-only. BEFORE UPDATE and BEFORE DELETE triggers MUST abort writes. Repair rebuilds materialized heads; it never rewrites the ledger.

### 6.1 blackboard_graph_mutations

One row per first-seen accepted MutationBatch:

| Column | Contract |
| --- | --- |
| project_id, mutation_seq | Composite primary key. mutation_seq is contiguous per Project. |
| mutation_id | Globally unique opaque ID. |
| base_graph_revision | Revision read and validated. |
| result_graph_revision | Same as base for all-no-op; otherwise base + 1. |
| schema_version | Mutation schema version. |
| mutation_kind | normal, merge, compaction, restore, reconciliation, projection, or migration. |
| maintenance_metadata_json | Canonical trusted compaction/restore/reconciliation/migration metadata; empty object for ordinary mutations. |
| maintenance_subject_id | Nullable stable subject for indexed recovery, such as Continuation ID or compaction/restore ID. |
| idempotency_scope, idempotency_key | Unique per Project and trusted actor scope. |
| request_hash | SHA-256 of the normalized batch plus trusted maintenance metadata, excluding server-generated IDs/timestamps. |
| request_json | Canonical normalized request. |
| result_json | Exact canonical result returned on replay. |
| result_hash | SHA-256 of result_json. |
| recorded_at | One server-generated timestamp shared by all effects in the batch. |
| previous_mutation_hash, mutation_hash | Per-Project integrity chain. |
| resulting_state_hash | Hash of the full semantic state after the mutation. |
| projection_status | measured or dirty. |
| resulting_main_projection_hash | CanonicalMainGraphV1 hash when measured; null when dirty. |
| projection_renderer_version, projection_estimator_version | Algorithms used when measured. |
| projection_bytes, projection_estimated_tokens | Measurements when measured; null when dirty. |

A partial unique index on (project_id, result_graph_revision) where result_graph_revision is greater than base_graph_revision enforces one state-changing mutation per revision. No-op mutations may share the same graph revision.

Rejected batches are not graph mutations and create no ledger row. Transport/security audit may record them outside the graph.

### 6.2 blackboard_graph_provenance

One immutable provenance row per operation:

- id;
- project_id;
- actor_type and actor_id;
- task_id;
- continuation_id;
- runtime_profile_id;
- runner;
- migration_source_json;
- recorded_at.

The primary key is (project_id, id); id is also globally unique.

Runtime fields are conditionally required exactly as specified by the graph contract. The trusted context resolver, not caller JSON, supplies them.

### 6.3 blackboard_graph_provenance_events

Ordered source Event references:

- project_id;
- provenance_id;
- ordinal;
- event_id.

The composite primary key is (project_id, provenance_id, ordinal), with a uniqueness constraint on (project_id, provenance_id, event_id). The service verifies that every Event belongs to the bound Task and, for new Events, the bound Continuation.

### 6.4 blackboard_graph_operations

One row per requested operation, including no-ops:

- project_id and mutation_seq;
- operation_index;
- op_id, unique inside the mutation;
- operation_kind;
- operation_json;
- result_json;
- changed boolean;
- provenance_id.

The primary key is (project_id, mutation_seq, operation_index), with a unique constraint on (project_id, mutation_seq, op_id). Node, edge, and key versions reference the operation that produced them.

### 6.5 blackboard_nodes

Immutable node identity:

- id;
- project_id;
- node_type;
- original_stable_key;
- created mutation and operation;
- created_at.

Keys and unique constraints:

- primary key (project_id, id);
- globally unique id;
- (project_id, node_type, original_stable_key).

Merged source identities remain forever. Their original stable key later behaves as an alias through key events; the identity row is never deleted or rebadged.

### 6.6 blackboard_node_versions

Complete node post-images:

- project_id and node_id;
- version;
- result_graph_revision;
- mutation_seq and operation_index;
- schema_version;
- disposition;
- merge_target_id;
- canonical properties_json;
- semantic_hash;
- updated_at.

The primary key is (project_id, node_id, version). Versions MUST be contiguous from 1. properties_json MUST pass json_valid and the domain schema before insertion.

The one-time legacy import MAY preserve redundant or legacy-invalid historical post-images only under the migration contract. Current heads and all later Apply-created versions obey the ordinary semantic-version and lifecycle rules.

The semantic hash covers disposition, merge target, and normalized type-specific properties. Exact no-op updates do not insert a version.

### 6.7 blackboard_edges

Immutable edge identity:

- id;
- project_id;
- edge_type;
- created mutation and operation;
- created_at.

The primary key is (project_id, id); id is also globally unique.

### 6.8 blackboard_edge_versions

Complete edge post-images:

- project_id and edge_id;
- version;
- result_graph_revision;
- mutation_seq and operation_index;
- from_node_id and to_node_id;
- state;
- summary;
- semantic_hash;
- updated_at.

The primary key is (project_id, edge_id, version). Versions MUST be contiguous from 1.

Ordinary operations cannot change endpoints. Merge Nodes may append endpoint-changing versions as an internal effect. Retired edges remain in history. A later put_edge for the same tuple creates a new edge identity unless an identical active edge already exists.

### 6.9 blackboard_key_events

The single append-only namespace history for stable keys and aliases:

- project_id;
- node_type;
- key;
- key_version;
- role: stable or alias;
- source_node_id;
- canonical_node_id;
- legacy_nonconforming boolean;
- result_graph_revision;
- mutation_seq and operation_index;
- semantic_hash.

The primary key is (project_id, node_type, key, key_version). New keys conform to the graph contract; migration may mark preserved nonconforming aliases.

Every current key resolves directly to one canonical node. A merge appends key events rather than updating or replacing alias rows.

### 6.10 blackboard_compactions

One immutable manifest header per applied compaction or restore:

- compaction_id;
- project_id;
- trigger: proactive, warning, required, operator, or restore;
- policy_version;
- base and result graph revisions;
- mutation_seq;
- before and after state/projection hashes;
- before and after bytes/estimated tokens;
- target estimated tokens;
- root component count;
- rationale;
- recorded_at.

The primary key is (project_id, compaction_id), with a unique constraint on (project_id, mutation_seq).

### 6.11 blackboard_compaction_members

Manifest membership:

- project_id;
- compaction_id;
- member_kind: node or edge;
- member_id;
- action: archive, restore, retire, create, or preserve;
- before_version;
- after_version;
- component_ordinal.

The primary key is (project_id, compaction_id, member_kind, member_id, action).

Membership explains and assists restoration; it never controls ordinary graph reads.

## 7. Rebuildable materialized tables

These tables are mutable caches/indexes and MAY be deleted and rebuilt. They MUST NOT contain independent semantic properties.

### 7.1 blackboard_graph_state

One row per Project:

- latest mutation_seq;
- current graph_revision;
- materialized mutation_seq;
- history head hash;
- current semantic state hash;
- current main projection hash;
- projection renderer and estimator versions;
- projection bytes and estimated tokens;
- budget state;
- projection dirty revision;
- updated_at.

The primary key is project_id.

### 7.2 blackboard_node_heads

Latest node-version pointer plus query indexes:

- project_id and node_id;
- node_type;
- current version and graph revision;
- disposition;
- merge_target_id;
- lifecycle_state;
- entity_kind;
- scope_status;
- semantic_hash.

The primary key is (project_id, node_id).

properties_json is read by joining to blackboard_node_versions. lifecycle_state is derived from the controlled property for the node type: task_status, status, or confidence.

### 7.3 blackboard_edge_heads

Latest edge-version pointer plus:

- project_id and edge_id;
- edge_type;
- from_node_id and to_node_id;
- state;
- semantic_hash;
- current version and graph revision.

The primary key is (project_id, edge_id).

### 7.4 blackboard_key_registry

The current shared stable-key/alias namespace:

- project_id, node_type, key as primary key;
- latest key_version;
- role;
- source_node_id;
- canonical_node_id;
- semantic_hash.

The primary key is (project_id, node_type, key).

Create Node checks this registry and never interprets an alias collision as an update.

### 7.5 blackboard_projection_metrics

Derived measurements keyed by:

- project_id;
- graph_revision;
- renderer_version;
- estimator_version;
- projection_kind.

The primary key is (project_id, graph_revision, renderer_version, estimator_version, projection_kind).

Metrics include node/edge counts, exact UTF-8 bytes, estimated tokens, projection hash, budget state, and measured_at. projection_kind distinguishes the storage-level CanonicalMainGraphV1 document from downstream Runtime-prompt or snapshot renderings. Only the storage-level document drives the Project budget state. The cache may be pruned and recomputed.

## 8. Required indexes and SQLite backstops

### 8.1 Key and foreign-key matrix

| Table | Primary/unique key | Required references |
| --- | --- | --- |
| blackboard_graph_mutations | PK (project_id, mutation_seq); unique mutation_id; unique idempotency tuple | project_id -> projects |
| blackboard_graph_provenance | PK (project_id, id); unique id | project_id -> projects |
| blackboard_graph_provenance_events | PK (project_id, provenance_id, ordinal); unique provenance/Event | provenance -> blackboard_graph_provenance; event_id -> task_events |
| blackboard_graph_operations | PK (project_id, mutation_seq, operation_index); unique (project_id, mutation_seq, op_id) | mutation -> blackboard_graph_mutations; provenance -> blackboard_graph_provenance |
| blackboard_nodes | PK (project_id, id); unique id; unique Project/type/original key | creation operation -> blackboard_graph_operations |
| blackboard_node_versions | PK (project_id, node_id, version) | node -> blackboard_nodes; producing operation -> blackboard_graph_operations; merge target -> blackboard_nodes, deferred |
| blackboard_edges | PK (project_id, id); unique id | creation operation -> blackboard_graph_operations |
| blackboard_edge_versions | PK (project_id, edge_id, version) | edge -> blackboard_edges; producing operation -> blackboard_graph_operations; both endpoints -> blackboard_nodes, deferred |
| blackboard_key_events | PK (project_id, node_type, key, key_version) | producing operation -> blackboard_graph_operations; source/canonical nodes -> blackboard_nodes, deferred |
| blackboard_compactions | PK (project_id, compaction_id); unique (project_id, mutation_seq) | mutation -> blackboard_graph_mutations |
| blackboard_compaction_members | PK (project_id, compaction_id, member_kind, member_id, action) | compaction -> blackboard_compactions; polymorphic member existence is verified by the graph module |
| blackboard_graph_state | PK project_id | project_id -> projects |
| blackboard_node_heads | PK (project_id, node_id) | exact (project_id, node_id, version) -> blackboard_node_versions |
| blackboard_edge_heads | PK (project_id, edge_id) | exact (project_id, edge_id, version) -> blackboard_edge_versions |
| blackboard_key_registry | PK (project_id, node_type, key) | exact latest key event -> blackboard_key_events |
| blackboard_projection_metrics | PK (project_id, graph_revision, renderer_version, estimator_version, projection_kind) | project_id -> projects |
| blackboard_health_runs | PK (project_id, run_id); unique run_id | project_id -> projects |
| blackboard_health_results | PK (project_id, run_id, fingerprint) | run -> blackboard_health_runs |

Every graph foreign key uses ON DELETE RESTRICT. No canonical ledger record cascades away. Runtime Profile, Task, and Continuation provenance strings are additionally validated against durable captured Task configuration; foreign keys alone are not the trust check.

### 8.2 Indexes

At minimum:

- mutation idempotency: unique (project_id, idempotency_scope, idempotency_key);
- mutation order: (project_id, mutation_seq) and (project_id, result_graph_revision);
- maintenance recovery: (project_id, mutation_kind, maintenance_subject_id, mutation_seq descending);
- provenance: (project_id, task_id), (project_id, continuation_id), and actor/time;
- node versions: (project_id, node_id, version descending) and (project_id, result_graph_revision);
- node heads: (project_id, node_type, disposition, lifecycle_state, node_id);
- Entity heads: (project_id, entity_kind, disposition, node_id);
- key registry primary key and (project_id, canonical_node_id);
- edge versions: (project_id, edge_id, version descending) and (project_id, result_graph_revision);
- edge-head active identity: unique on blackboard_edge_heads (project_id, edge_type, from_node_id, to_node_id) where state is active;
- edge-head outgoing adjacency: blackboard_edge_heads (project_id, state, edge_type, from_node_id, to_node_id);
- edge-head incoming adjacency: blackboard_edge_heads (project_id, state, edge_type, to_node_id, from_node_id);
- projection metrics: unique Project/revision/renderer/estimator/kind;
- Health runs: (project_id, checked_graph_revision, completed_at descending);
- Health results: (run_id, severity, code, fingerprint);
- compactions: (project_id, result_graph_revision).

SQLite CHECK constraints enforce:

- controlled envelope enums;
- non-negative versions and revisions;
- valid JSON;
- active/retired edge states;
- current edge heads satisfy state <> active OR from_node_id <> to_node_id;
- merged disposition requiring merge_target_id;
- non-merged disposition forbidding merge_target_id.

The full endpoint matrix, lifecycle transitions, cycles, archive guards, merge semantics, provenance trust, and final-graph invariants remain in the graph module. They MUST NOT be duplicated as divergent SQL trigger logic.

During Merge Nodes, touched materialized head rows are removed before final head rows are inserted. Because this occurs inside one transaction, partial unique indexes validate the final graph without exposing transient edge collisions.

## 9. Apply transaction

Apply follows this exact consistency boundary:

1. Perform transport-independent syntactic decoding only; do not trust or finalize Project/Task/Continuation context yet.
2. Start BEGIN IMMEDIATE.
3. Resolve and authoritatively revalidate MutationExecutionContext, Project kind, actor, Task, Continuation, captured Runtime configuration, and Runner inside the transaction.
4. Normalize the batch plus trusted maintenance metadata, calculate request_hash/idempotency scope, and read or initialize blackboard_graph_state for the Project.
5. Check idempotency:
   - same scope/key/hash returns the exact stored result and rolls back the empty transaction;
   - same scope/key with a different hash returns idempotency_conflict;
   - no match continues.
6. Load the bounded current main graph plus referenced archived/merged records and key redirects.
7. Resolve references, verify expected versions, construct the final proposed graph, and validate the final graph rather than transient operation order.
8. Allocate all IDs, one recorded_at timestamp, the next mutation_seq, and graph_revision + 1 only if semantic state changes.
9. Calculate every changed-record semantic/integrity hash and the full proposed-state hash.
10. Attempt main-projection rendering and sizing. For ordinary semantic mutations, a sizing failure marks projection_dirty_revision and budget state unknown but does not discard the write. Compaction and restore require verified before/after projection hashes and sizes, so their sizing failure aborts before any ledger insertion.
11. Finalize the exact result bytes and mutation hash after projection_status, renderer/estimator, hash, bytes, and estimated tokens are known.
12. Insert mutation, provenance, operation, immutable identity, full version, key-event, and optional compaction rows with their final hashes.
13. Rebuild the touched node heads, edge heads, and key registry entries from the inserted ledger rows.
14. Update blackboard_graph_state and commit.

All effects, including the idempotency record, are in the same transaction.

Failure or process death before commit leaves the old revision. A commit that succeeded but whose response was lost is recovered by retrying the same idempotency key, which returns identical IDs, timestamps, versions, revision, and result bytes.

### 9.1 No-op behavior

- Exact replay: no mutation row, counter, version, or timestamp.
- First-seen all-no-op batch: mutation_seq increments, graph_revision does not, operations are recorded, and exact result bytes are stored.
- No-op node/edge operation: no record version.
- Validation/storage failure: no mutation row and no consumed key.

### 9.2 Cross-domain consistency

The Apply transaction is atomic for graph ledger and materialization only. Task lifecycle rows, Runtime process state, and artifact payload files remain outside the graph aggregate.

Cross-domain adapters use durable-state-first, idempotent reconciliation:

- Task creation/status commits in the Task domain, then the Goal projector applies the corresponding graph mutation using an idempotency key derived from task ID, status, and durable Task updated_at.
- Continuation terminal state commits before unexpected-end graph recovery; recovery is repeatable from the durable terminal Continuation.
- Evidence payload content is moved into the managed Artifact Root and hashed before an EvidenceArtifact becomes available in the graph. Later file loss or hash drift is a Health result, not an attempt at filesystem/database two-phase commit.

The Goal projector runs immediately after each Task commit, at daemon startup, and before every Continuation snapshot. It compares each Task's immutable goal text and current status with stable key task:<task_id>:goal. A missing Goal is created from durable Task state; a status mismatch patches task_status only. A task_id, stable-key, or goal-text mismatch is integrity corruption because Goal text is immutable, so the projector does not rewrite it. A crash between Task commit and Apply therefore causes temporary missing/status drift, never permanent loss. Blackboard Health reports goal_projection_drift until reconciliation succeeds or corruption is repaired.

Adapters MUST NOT hold a Task-domain transaction open while calling Apply through a second database connection.

## 10. Alias and merge persistence

Key lookup reads blackboard_key_registry and returns its direct canonical_node_id. Immutable-ID lookup follows merge_target_id through node versions with cycle detection. Key aliases remain one-hop even if immutable-ID redirects form a short historical chain.

Merge Nodes MUST:

1. retain source and canonical node identities and their histories;
2. append any explicit canonical patch as a new canonical node version;
3. append a source node version with disposition merged and merge_target_id;
4. append a key event converting the source stable key to an alias;
5. append key events repointing every source alias directly to the final canonical node;
6. append edge versions for rewires, duplicate collapse, summary choice, and forbidden self-edge retirement;
7. update only materialized heads/registry after ledger insertion.

History is never copied or rebadged under the canonical key.

When rewiring collides with an existing active canonical edge:

- an edge already incident on the canonical node wins;
- otherwise the oldest created edge wins, then lowest ID;
- the most recently recorded non-empty summary becomes the surviving current summary;
- losing duplicate edges and forbidden self-edges receive retired versions.

Semantic similarity may create a Health duplicate candidate only. It never writes merge state.

## 11. Reconstruction and integrity

### 11.1 Current reconstruction

To rebuild current materialization:

1. verify the mutation hash chain and contiguous per-record versions;
2. build the expected graph_state, node_heads, edge_heads, and key_registry rows in memory or temporary validation tables;
3. select the latest node, edge, and key version for every identity/key;
4. populate and validate the expected head rows;
5. calculate semantic state and main projection hashes;
6. compare the semantic state hash and, when the ledger projection_status is measured for the same renderer, the main projection hash;
7. in one BEGIN IMMEDIATE transaction, delete and repopulate the fixed materialized rows for that Project, then update graph_state.

WAL readers retain their old read snapshot until the repair commits. Fixed tables are not renamed, so foreign-key definitions, indexes, and triggers remain intact. No historical reducer or mutation validation code is replayed.

### 11.2 Historical reconstruction

Graph revision R is reconstructed by choosing the latest node, edge, and key version whose result_graph_revision is less than or equal to R. Because there are no hard deletes, this reproduces pre-merge and pre-archive snapshots.

### 11.3 Hashes

Canonical JSON v1 uses:

- UTF-8;
- no insignificant whitespace;
- explicitly fixed envelope field order;
- lexicographically ordered property keys by raw UTF-8 bytes;
- deterministic array ordering;
- UTC RFC3339Nano timestamps;
- HTML escaping disabled;
- explicit nulls where the contract requires them.

Hash framing is:

~~~text
frame(x) = uint64_big_endian(len(x)) || x
H(domain, parts...) = SHA256(frame(domain) || frame(part_1) || ... || frame(part_n))
~~~

The first mutation's previous hash is H("CyberPenda.Blackboard.Genesis.v1", project_id). No concatenation is unframed.

Canonical changed-record kind ordinals are:

1. provenance;
2. operation;
3. node identity;
4. node version;
5. edge identity;
6. edge version;
7. key event;
8. compaction header;
9. compaction member.

Project-scoped identity/order bytes are:

- provenance: provenance ID;
- operation: mutation_seq then operation_index;
- node identity: node ID;
- node version: node ID then version;
- edge identity: edge ID;
- edge version: edge ID then version;
- key event: node-type ordinal, key UTF-8 bytes, then key_version;
- compaction header: compaction ID;
- compaction member: compaction ID, member-kind ordinal, member ID, then action ordinal.

Integer fields use unsigned 64-bit big-endian bytes; enum/action fields use their fixed ordinal; hashes use raw 32-byte values; nullable fields are preceded by one presence byte. Strings and JSON use UTF-8 bytes inside frame.

Changed records are ordered by kind ordinal, Project-scoped identity bytes, then record version/operation index. Each provenance integrity hash covers the provenance row plus every ordered provenance-Event child row, including ordinal and Event ID. Each other integrity record hash covers its complete canonical row, including immutable identity fields, producing operation reference, and resolved provenance hash. This is distinct from the stored semantic_hash used for no-op detection, which covers only the record's current domain state.

The mutation hash is H("CyberPenda.Blackboard.Mutation.v1", project_id, mutation_id, previous_mutation_hash, mutation_seq, base_graph_revision, result_graph_revision, schema_version, mutation_kind, maintenance_subject_id, maintenance_metadata_json, idempotency_scope, idempotency_key, request_hash, result_hash, recorded_at, resulting_state_hash, projection_status, projection_renderer_version, projection_estimator_version, optional resulting_main_projection_hash, optional projection_bytes, optional projection_estimated_tokens, ordered changed-record hashes). Every append-only mutation-header column is therefore covered.

The semantic state hash is H("CyberPenda.Blackboard.State.v1", project_id, project_kind, graph schema version, ordered immutable identities plus current semantic_hash values for nodes, edges, and keys). It includes archived, merged, and retired current states. Nodes use type ordinal/stable key/ID ordering; edges use edge type/ID ordering; keys use node type/key ordering. It excludes provenance/timestamps, mutation audit rows, compaction manifests, projection metrics, snapshot files, and Health cache. Provenance/timestamp integrity is covered by the mutation chain, while Runtime-visible provenance is covered by the main projection hash.

The main projection hash is H("CyberPenda.Blackboard.MainProjection.v1", the exact CanonicalMainGraphV1 bytes). Snapshot hashes additionally include their renderer version as a framed part.

Hash chaining detects accidental alteration and partial corruption. It is not a tamper-proof signature against an attacker able to rewrite the entire local database.

Materialized-head drift may be repaired automatically after the ledger verifies. Broken ledger history fails graph writes closed and requires backup or explicit manual repair. That is a storage-integrity failure, not a Blackboard Health policy gate.

## 12. Deterministic main projection and budgets

### 12.1 CanonicalMainGraphV1

The storage-level measured document contains:

- graph schema version;
- Project ID, immutable Project kind, and graph revision;
- every disposition=main canonical node;
- every active edge whose two endpoints are main canonical nodes;
- compact created/updated provenance references;
- derived Frontier node IDs;
- derived Current Truth node IDs.

It excludes:

- aliases and merged redirects;
- archived nodes;
- retired edges;
- node/edge versions and mutation history;
- Health;
- raw Task Events, commands, logs, and artifact payload bytes.

CanonicalMainGraphV1 is canonical JSON v1 with this exact top-level field order:

1. schema_version;
2. project_id;
3. project_kind;
4. graph_revision;
5. nodes;
6. edges;
7. frontier_node_ids;
8. current_truth_node_ids.

Each node object has this exact field order:

1. id;
2. node_type;
3. stable_key;
4. version;
5. disposition;
6. properties;
7. created_at;
8. updated_at;
9. created_provenance;
10. updated_provenance.

Each edge object has this exact field order:

1. id;
2. edge_type;
3. from_node_id;
4. to_node_id;
5. version;
6. state;
7. summary;
8. created_at;
9. updated_at;
10. created_provenance;
11. updated_provenance.

Every compact provenance object has this exact field order, with explicit nulls and an ordinal-preserving source_event_ids array:

1. actor_type;
2. actor_id;
3. task_id;
4. continuation_id;
5. runtime_profile_id;
6. runner;
7. source_event_ids;
8. migration_source;
9. recorded_at.

Type-specific properties use lexicographically ordered keys. No generated_at or scan timestamp appears in the document, so the same Project/revision/renderer always produces the same bytes.

Node type ordinals are:

| Ordinal | Node type |
| --- | --- |
| 0 | goal |
| 1 | entity |
| 2 | exploration_objective |
| 3 | attempt |
| 4 | observation |
| 5 | hypothesis |
| 6 | project_fact |
| 7 | finding |
| 8 | solution |
| 9 | evidence_artifact |
| 10 | project_directive |

Edge type ordinals are:

| Ordinal | Edge type |
| --- | --- |
| 0 | about |
| 1 | part_of |
| 2 | tests |
| 3 | produced |
| 4 | evidences |
| 5 | supports |
| 6 | contradicts |
| 7 | derived_from |
| 8 | depends_on |
| 9 | blocks |
| 10 | leads_to |
| 11 | satisfies |
| 12 | supersedes |

Node ordering is:

1. fixed node-type ordinal from the graph contract;
2. stable_key with SQLite BINARY collation;
3. immutable ID.

Edge ordering is:

1. fixed edge-type ordinal from the graph contract;
2. from_node_id;
3. to_node_id;
4. immutable ID.

Frontier and Current Truth IDs follow their stable node ordering. Timestamps never determine projection order.

The Runtime context and exact read-only task snapshot defined by downstream interface/projection tickets MUST be lossless renderings of this document and MUST record their renderer version. This ticket fixes the storage-level content and deterministic ordering; downstream tickets fix public envelopes, prompt presentation, and UI/API shapes.

### 12.2 Budget estimator

The Project-wide policy uses one provider-neutral estimate:

~~~text
estimated_tokens = ceil(canonical_main_graph_v1_utf8_bytes / 4)
~~~

The estimator ID is utf8_bytes_div_4_v1. Exact bytes and estimated tokens are both exposed. Transport framing, prompt prose, and Runtime-specific tokenizer counts may be diagnostic but MUST NOT change Project Health or compaction decisions.

| Estimated tokens | Budget state | Required behavior |
| --- | --- | --- |
| 0-12,000 | within_target | No budget Health result. |
| 12,001-15,999 | above_target | Informational result; compute reclaimable components. |
| 16,000-19,999 | warning | Warning result; generate a deterministic compaction plan. |
| 20,000 or more | required | Critical result; invoke safe compaction. |
| unavailable | unknown | Preserve the write, mark metrics dirty, and retry sizing. |

Crossing 20K MUST NOT:

- reject or roll back a valid graph write;
- omit nodes or edges;
- substitute a relevance-selected subgraph;
- truncate the snapshot;
- block Runtime launch solely because of Health.

At required, the system attempts compaction after the triggering commit and again before pinning the next Continuation snapshot. If safe candidate exhaustion still leaves 20K or more, the full oversized graph is still delivered and Health reports compaction_blocked.

Compaction targets 12K, not merely 19,999, to provide hysteresis.

### 12.3 Snapshot consistency

A Continuation pins one graph revision and projection hash. Later writes or compaction do not alter that Continuation's context. Regeneration from history MUST be byte-identical for the same renderer and estimator versions.

Continuation creation uses one short Task-launch BEGIN IMMEDIATE transaction:

1. resolve and persist the exact task_runtime_config_versions row;
2. read the current graph revision and render CanonicalMainGraphV1 at that revision;
3. insert the task_continuations row with immutable runtime_config_version_id and Blackboard revision/renderer/estimator/hash/size fields;
4. commit both references together.

The Runtime process MUST NOT start until the pinned snapshot file has been materialized and its hash verified. A crash after the database commit but before file creation is recovered by regenerating the bytes from graph history; it does not create a new pin or revision. Pin fields never change during the Continuation.

## 13. Frontier and Current Truth

Both views are derived inside one read transaction and return the observed graph revision.

### 13.1 Current Truth

Select canonical main ProjectFact nodes whose confidence is tentative or confirmed. Tentative and out-of-scope facts remain explicit. Deprecated, archived, merged, and alias identities are excluded.

Order by stable_key BINARY, then ID.

### 13.2 Frontier

Start with canonical main ExplorationObjective nodes whose status is open. A candidate qualifies only when:

- every active outgoing depends_on target is a main Objective with status resolved;
- every active incoming blocks source is a main Objective with status resolved.

A missing, archived, merged, abandoned, or superseded prerequisite fails closed and keeps the Objective off Frontier. Corruption also produces a critical Health result.

Order by stable_key BINARY, then ID.

Frontier is never stored as a table, queue, claim, lease, or status. A stale-frontier Health result means stranded/corrupt Objective topology or a stale materialized revision, not a cache that became authoritative.

## 14. Interruption recovery

Graph recovery runs after an unexpectedly terminal Continuation and again after Task/Continuation startup reconciliation.

blackboard_reconciliation_status is pending, completed, or failed. Every new Continuation starts pending. Normal completion or unexpected-end recovery is the only path to completed.

For each affected Continuation:

1. read all canonical main Attempts created by that Continuation that remain open;
2. sort them by stable key and ID;
3. build one system-provenance MutationBatch using current expected versions;
4. transition each still-open Attempt to interrupted;
5. preserve an existing non-empty Attempt summary, otherwise choose:
   - latest checkpoint/summary Task Event whose attempt_node_id matches this Attempt;
   - terminal lifecycle/error message;
   - fixed fallback text: Continuation <id> ended before this Attempt was concluded (<reason>).
6. attach an ordered, deduplicated maximum of eight source Event IDs: shared Continuation start/terminal lifecycle Events plus only error/status/checkpoint Events whose attempt_node_id matches this Attempt;
7. preserve already-associated EvidenceArtifact relationships; do not infer or create an Attempt-to-artifact edge from same-Continuation provenance alone. Retained files without an existing explicit graph association remain outside graph truth for later reconciliation;
8. apply with mutation_kind reconciliation, maintenance_subject_id equal to the Continuation ID, and idempotency key reconcile:<continuation_id>:<sorted_attempt_set_hash>;
9. after Apply succeeds, persist blackboard_reconciliation_status=completed, the mutation ID, and reconciled_at on the Continuation.

Expected-version races decide cleanly:

- if Runtime completion commits first, recovery rereads and does not overwrite the terminal Attempt;
- if recovery commits first, the stale Runtime transition receives version_conflict.

Crash windows converge:

- Continuation end committed, graph recovery absent: startup retries;
- graph recovery committed, marker absent: startup queries the latest reconciliation mutation by maintenance_subject_id or observes no open Attempts, then completes the marker without creating another mutation;
- process death inside Apply: SQLite commits all effects or none.

Task and Continuation statuses remain owned by the Task domain. Graph recovery does not rewrite them.

A cleanly completed Continuation sets blackboard_reconciliation_status=completed only after the normal Runtime protocol finishes. If that marker is complete while an Attempt remains open, the state is a Runtime protocol/Health defect. It is not automatically mislabeled interrupted by crash recovery; the normal reconciliation protocol is specified by the Runtime interface ticket.

## 15. Semantic graph compaction

Graph compaction means reducing the current main semantic graph. It is not SQLite VACUUM, WAL checkpointing, event pruning, history squashing, or deletion.

### 15.1 Protected set

The deterministic planner protects these roots:

- nonterminal Goals;
- open ExplorationObjectives and Attempts;
- active ProjectDirectives;
- Current Truth ProjectFacts;
- confirmed Findings;
- verified Solutions;
- both endpoints of an unresolved active contradiction;
- failed, blocked, inconclusive, and interrupted Attempts retained as consolidated negative-result anchors.

Protection closes over required active relationships:

- Objective prerequisites and blockers;
- open Attempt tests targets;
- supports/evidences witnesses required by confirmed state;
- verified Solution satisfies Goal;
- about Entities and required part_of ancestors.

The protected closure is never auto-archived to meet a size number.

### 15.2 Eligible components

Only unprotected, quiescent connected components are automatically eligible. Per-type eligibility is:

| Node type | Auto-eligible state |
| --- | --- |
| Goal | Task status completed, failed, stopped, or interrupted, with no protected dependent path. |
| Entity | status retired/superseded, or active but unreferenced outside the candidate component and not a required about/part_of ancestor. |
| ExplorationObjective | resolved, abandoned, or superseded. |
| Attempt | succeeded with retained outcome nodes; failed/blocked/inconclusive/interrupted remain protected negative-result anchors. |
| Observation | superseded, or recorded only when its closed successful branch has a retained outcome/Attempt summary that preserves its useful conclusion. |
| Hypothesis | superseded, or supported/contradicted/inconclusive only when the branch is closed and a retained outcome/Attempt summary preserves the conclusion. Open is never eligible. |
| ProjectFact | confidence deprecated. Tentative/confirmed Current Truth is protected. |
| Finding | false_positive. Unconfirmed and confirmed are not automatically eligible. |
| Solution | rejected or superseded. Candidate and verified are not automatically eligible. |
| EvidenceArtifact | superseded, or available/missing only when no protected conclusion requires it and it belongs solely to an eligible closed branch. |
| ProjectDirective | retired or superseded. |

A candidate Attempt MUST already carry its required useful summary. Recorded Observations, non-open Hypotheses, active Entities, and available Evidence are eligible only through the explicit branch rules above; their ordinary nonterminal labels alone are never enough. Compaction never invents semantic text.

Failed/inconclusive/interrupted branches retain a consolidated Attempt so later Tasks do not repeat them. Successful branches retain resulting ProjectFacts, Findings, Solutions, and required Evidence.

### 15.3 Deterministic plan

Connected candidate components are ordered by:

1. policy class: superseded/deprecated/rejected/false-positive material; successful-branch intermediates; resolved/abandoned Objective branches; unreferenced Entity/Evidence material; other quiescent material;
2. estimated reclaimable tokens, descending;
3. last semantic graph revision, ascending;
4. minimum node-type ordinal;
5. minimum stable key BINARY;
6. minimum immutable ID.

The planner simulates CanonicalMainGraphV1 after each component and selects until estimated size is at or below 12K or no eligible component remains.

The plan records:

- base graph revision;
- every expected node and edge version;
- nodes to archive;
- edges to retire;
- outcomes and negative-result anchors to preserve;
- before/after simulated hashes and token counts;
- rationale per component.

Apply reacquires the writer lock and requires current graph_revision to equal the plan's base graph revision before recomputing the protected set and deterministic component selection. Any intervening state-changing graph write invalidates the complete plan, even when unrelated; the planner recomputes rather than trying to preserve stale before/after hashes, sizes, or ordering.

### 15.4 Apply and manifest

One compaction mutation:

- retires every active internal or boundary edge touching an archived node;
- sets selected nodes to archived;
- validates the complete final graph;
- records ledger versions and a compaction manifest;
- verifies that the projection shrank;
- records before/after revisions, hashes, sizes, members, and retired edges.

Before/after projection rendering, hashes, and sizes are mandatory for both compaction and restore. If they cannot be calculated, the mutation rolls back, the prior graph remains intact, and Health reports budget_unknown. Shrink verification is mandatory only for compaction; a restore is expected to grow the main projection and records that growth.

In above_target or warning, an empty/insufficient plan remains informational or warning; it does not create compaction_blocked. In required, the system applies every selected safe component toward the 12K target without ever archiving protected content. If the resulting graph remains at or above 20K after all eligible components are exhausted, Health reports compaction_blocked. If it falls below 20K but remains above 12K, the normal above_target/warning status applies instead.

### 15.5 Restore

Restore is a new explicit mutation assembled from a manifest:

- selected archived nodes transition to main;
- desired edges are explicitly recreated through put_edge semantics with new edge identities where no identical active edge exists;
- current aliases and merge redirects are resolved first;
- duplicate active edges collapse to the existing canonical edge;
- self-edges made invalid by later merges stay retired;
- the final graph passes ordinary validation.

Historical edges are not blindly resurrected. The manifest proposes topology; the restore mutation explicitly selects the topology that remains valid now.

Automatically restored nodes receive a deterministic restore hold derived from the restore manifest revision. The automatic compactor excludes that restored component until either:

- at least one Continuation snapshot has pinned a graph revision at or after the restore revision; or
- an operator explicitly requests compaction with override_restore_hold in trusted maintenance metadata.

The hold requires no mutable graph flag: the planner compares the latest restore manifest with durable Continuation pin fields. A restore that crosses 20K may therefore remain critical/oversized for its first snapshot, but it is not immediately re-archived before the restored context can be used.

## 16. Blackboard Health persistence

Blackboard Health is derived operational diagnosis. It is never a graph node, Current Truth, a Runtime policy gate, or an automatic repair channel.

### 16.1 blackboard_health_runs

Persist:

- project_id;
- run_id;
- checked graph revision and state/projection hashes;
- checker version;
- status;
- artifact scan status;
- started_at and completed_at;
- metrics_json.

The primary key is (project_id, run_id); run_id is also globally unique.

### 16.2 blackboard_health_results

Persist:

- project_id;
- run_id;
- stable fingerprint;
- code;
- severity: info, warning, or critical;
- subject kind and ID/key;
- canonical details_json.

The primary key is (project_id, run_id, fingerprint).

Health tables are derived cache and may be pruned/recomputed. Multiple runs at one graph revision are valid because artifact files and reconciliation age can change without a graph mutation.

Overall status:

- healthy: no results;
- attention: info only;
- degraded: at least one warning and no critical;
- critical: at least one critical;
- unknown: scan did not complete.

A Health view is stale when its checked graph revision/hash differs from current state or its filesystem scan inputs are no longer current.

### 16.3 Required metrics

- node counts by type and disposition;
- active and retired edge counts;
- Current Truth and Frontier counts;
- projection bytes, estimated tokens, and budget state;
- protected and reclaimable token estimates;
- eligible component count;
- open Attempts on ended Continuations and maximum age;
- orphan count;
- missing/unavailable Evidence count;
- duplicate candidate count;
- unresolved contradiction count;
- stranded Objective count;
- history, state, and projection hashes;
- last compaction revision.

### 16.4 Required detectors

| Code | Condition | Severity |
| --- | --- | --- |
| projection_above_target | 12,001-15,999 estimated tokens | info |
| projection_warning | 16,000-19,999 estimated tokens | warning |
| compaction_required | 20,000 or more estimated tokens | critical |
| compaction_blocked | Budget is required and safe candidate exhaustion still leaves 20,000 or more estimated tokens | critical |
| budget_unknown | Current projection could not be measured | warning |
| projection_stale | Stored projection metrics/hash do not match current graph revision/renderer | warning |
| reconciliation_pending | Unexpectedly ended Continuation has open Attempt for less than 30 seconds | info |
| reconciliation_lag | Same anomaly for 30-299 seconds | warning |
| reconciliation_stuck | Same anomaly for 300 seconds or more | critical |
| completion_protocol_gap | Cleanly completed Continuation still has an open Attempt for less than 300 seconds after normal reconciliation | warning |
| completion_protocol_stuck | Same clean-completion anomaly for 300 seconds or more | critical |
| orphan_node | Main node is not weakly connected (edge direction ignored for reachability) to a protected root in section 15.1 through active edges | info; warning when Runtime-created |
| evidence_missing | Required Evidence is missing, payload absent, or hash mismatched | warning; critical for confirmed Finding/verified Solution |
| legacy_confirmed_fact_without_basis | Imported confirmed ProjectFact still relies on the migration-only confirmation exception | warning |
| legacy_confirmed_finding_without_support | Imported confirmed Finding still lacks an active evidences/supports edge | warning |
| duplicate_candidate | Deterministic duplicate fingerprint collision | info |
| unresolved_contradiction | Active contradiction between semantically live conclusions | warning; critical when both are confirmed/verified |
| objective_stranded | Open Objective depends on abandoned, superseded, archived, merged, or missing prerequisite | warning |
| objective_satisfied_but_open | Active satisfies edge targets an open Objective | warning |
| frontier_stalled | Open Objectives exist, Frontier is empty, and no open Attempt exists | warning |
| goal_projection_drift | Goal missing/status-stale, or immutable task ID/key/text differs from durable Task state | warning for missing/status drift; critical for immutable mismatch |
| materialization_mismatch | Rebuilt heads differ from materialized heads | critical |
| history_chain_broken | Mutation/version chain verification fails | critical |
| active_dangling_edge | Active edge lacks two main canonical endpoints | critical |
| alias_redirect_invalid | Key registry is cyclic, chained, cross-Project/type, or targets noncanonical state | critical |
| missing_provenance | Current/version record cannot resolve required provenance | critical |
| archive_manifest_mismatch | Manifest members/hashes disagree with ledger | critical |
| sqlite_integrity_failure | quick_check, foreign_key_check, or explicit integrity_check fails | critical |

Continuation preflight is separate from Health severity. Before pinning, it runs Goal reconciliation and fresh projection rendering; failure to produce the mandatory full snapshot prevents Runtime start as a concrete readiness failure, without reclassifying a warning Health code based on invocation context.

### 16.5 Duplicate fingerprints

Duplicate detection is advisory and deterministic:

- EvidenceArtifact: identical non-empty SHA-256;
- Entity: same kind plus normalized locator;
- Solution: same kind plus exact value;
- ProjectFact: same category plus normalized summary;
- Finding: normalized target plus normalized title.

Generic text normalization is Unicode NFKC, Unicode lowercase, replacement of each run of Unicode punctuation/separator characters with one ASCII space, whitespace collapse, and trim. Entity locators instead use their kind-specific canonical locator normalization so meaningful punctuation is preserved. Duplicate candidates never auto-merge and use no semantic similarity model.

### 16.6 Scan cadence

Cheap revision/hash/budget checks run after state-changing mutations. Full Health runs occur:

- at daemon startup;
- after interruption reconciliation;
- before a new Continuation snapshot is pinned;
- after compaction or restore;
- on explicit operator request.

Ordinary repeated writes may debounce filesystem-heavy checks. A failed Health scan stores or returns unknown and never mutates graph state.

## 17. Physical database maintenance

Semantic compaction never removes ledger rows. Physical maintenance is separate:

- WAL checkpoints MAY run automatically or after large graph mutations;
- PRAGMA quick_check and foreign_key_check run during startup/Health verification;
- full integrity_check is explicit maintenance;
- VACUUM MAY reclaim SQLite pages but MUST NOT change logical hashes or revisions;
- backups copy the database and artifact roots consistently.

Append-only graph history has no automatic retention/pruning policy in schema version 1.

## 18. TDD acceptance seams

Implementation proceeds red-first through BlackboardGraphService.Apply and alias-resolving read/snapshot/Health interfaces. Behavioral tests MUST NOT query SQLite to prove domain behavior.

Storage-adapter tests MAY inspect or damage SQLite deliberately to prove persistence and recovery. They use a real temporary file database, not a mocked repository.

Stable injected seams are limited to:

- clock;
- ID source;
- trusted context resolver;
- projection renderer/sizer;
- artifact inspector;
- persistence failure points.

Minimum storage acceptance matrix:

1. Failure after mutation insert, version insert, key event, or head materialization rolls back everything.
2. Reopen after pre-commit death exposes the old revision; reopen after commit exposes the full new revision.
3. Lost-response replay returns identical IDs, timestamps, versions, result bytes, mutation sequence, and graph revision.
4. First-seen all-no-op records one mutation sequence but no graph or record version.
5. Concurrent identical same-key writes converge; conflicting same-key writes return idempotency_conflict.
6. Stale expected versions fail without partial writes.
7. Current heads and key registry rebuild exactly after deletion.
8. Historical revision reconstruction is byte-identical before and after reopen.
9. Current-head tampering is detected and repairable; ledger tampering breaks the hash chain.
10. Random SQL row order produces identical canonical bytes, hashes, Frontier, Current Truth, and budget.
11. Active edge identity and both adjacency directions remain correct after merge.
12. Merge preserves source identity/history, flattens aliases, collapses duplicates, and retires self-edges atomically.
13. Key creation cannot shadow any stable key or alias.
14. Archive retires every internal/boundary active edge atomically.
15. Archive guards preserve truth, open work, active Directives, contradictions, required Evidence, and negative-result anchors.
16. Stale compaction plan fails without partial archiving.
17. Restore recreates valid topology and handles later merges/duplicates/self-edges.
18. Exact budget boundaries cover 12,000, 12,001, 15,999, 16,000, 19,999, and 20,000.
19. A write crossing 20K commits and the full graph remains present and untruncated.
20. At required budget, safe candidate exhaustion that still leaves 20K or more returns compaction_blocked; the same condition below 20K remains warning/above_target.
21. A Continuation snapshot remains unchanged after later mutations and compaction.
22. Unexpected-end recovery interrupts exactly the matching open Attempts.
23. Recovery does not change already terminal Attempts, clean-completion Attempts, other Continuations, or other Projects.
24. Repeated recovery is a no-op and a concurrent Runtime conclusion wins by expected version.
25. Event provenance is matching, ordered, deduplicated, and bounded.
26. Current Truth includes tentative/out-of-scope facts and excludes deprecated/archived/merged/alias records.
27. Frontier handles depends_on and blocks in both directions and fails closed on corrupt prerequisites.
28. Every Health detector has positive and negative fixtures; overall status is maximum severity.
29. Health staleness reacts to graph revisions and filesystem-only Evidence changes.
30. Failed Health scan never changes graph state.
31. foreign_keys, quick_check, and integrity failures are surfaced.
32. WAL checkpoint and VACUUM preserve logical hashes, revisions, and snapshot bytes.
33. Concurrent daemon/CLI writers allocate distinct mutation sequences under BEGIN IMMEDIATE.
34. Project isolation holds for ledger, heads, keys, provenance, snapshots, compactions, and Health.
35. Every transaction connection reports the required modernc PRAGMAs and immediate lock mode.
36. Continuation creation atomically captures Runtime configuration version and Blackboard pin; a missing file is regenerated before Runtime start.
37. Missing/status Task-Goal projection drift is repaired at startup and before Continuation pinning; immutable Goal text/key/task mismatch remains unchanged and critical.
38. Project kind cannot change after creation/backfill and participates in state/projection hashes.
39. Compaction/restore rolls back when before/after projection rendering fails; only compaction requires shrink verification.
40. A restore crossing 20K survives through at least one pinned Continuation snapshot before automatic compaction may re-archive it.
41. Two simultaneous open Attempts recover distinct attempt-scoped summaries/Event references and never infer artifact edges from Continuation identity.
42. Reconciliation committed with a lost marker is discovered through maintenance_subject_id and completes without another mutation_seq.
43. Deleting or reordering provenance-Event rows breaks the mutation integrity chain.
44. A golden CanonicalMainGraphV1 fixture asserts exact bytes, explicit nulls, field/type/edge order, exclusions, hash, and token estimate.
45. Ordinary mutation projection-sizing failure still commits the graph revision, marks projection dirty/unknown, and later remeasurement repairs the cache.
46. Health orphan traversal uses weak active-edge connectivity and context-free severities; Continuation readiness failures remain separate.

## 19. Existing implementation delta

The current code provides useful starting patterns but not this contract:

- internal/store/store.go already uses SQLite, WAL, one connection, and a busy timeout;
- current Fact and Finding writes update current rows and append versions in separate autocommits;
- current merges copy/rebadge history, rewrite references, delete source rows, and use destructive alias replacement;
- current Evidence and relation state lack complete version/provenance history;
- current migrations are unnumbered and not atomic as a set;
- current Fact Index orders by mutable updated_at;
- current restart reconciliation handles Task/Continuation state but not graph Attempts;
- Task Events and Continuations do not yet pin all graph provenance/snapshot fields;
- no graph revision, mutation ledger, key registry, projection metric, compaction manifest, or Blackboard Health persistence exists.

Implementation MUST replace these behaviors at the graph module rather than layering a second canonical store beside them.

## 20. Downstream boundaries

This storage contract intentionally leaves:

- trusted MCP/CLI/HTTP operation shapes, normal Runtime reconciliation, generated instructions, and task-local file placement to [Specify MCP, CLI, and Runtime Blackboard protocol](https://github.com/n1majne3/CyberPenda/issues/58);
- public read response envelopes, prompt/UI presentation renderers, report information architecture, and Health presentation to [Specify Blackboard read projections, reports, and operator UI](https://github.com/n1majne3/CyberPenda/issues/59);
- legacy row normalization, transactional backfill, compatibility translation, cutover, and rollback sequencing to [Specify legacy Blackboard migration and compatibility cutover](https://github.com/n1majne3/CyberPenda/issues/60);
- final red-green slice ordering to [Design TDD acceptance matrix and implementation slices for graph Blackboard](https://github.com/n1majne3/CyberPenda/issues/61).

No new Wayfinder ticket is required by this storage decision. Existing downstream tickets already own every newly sharpened interface and migration question.
