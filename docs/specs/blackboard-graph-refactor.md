# Graph-first Blackboard Refactor Specification

- **Status:** implementation-ready assembly for [Assemble the implementation-ready graph-first Blackboard refactor specification](https://github.com/n1majne3/CyberPenda/issues/62)
- **Planning map:** [Map: Refactor Blackboard into bounded graph memory](https://github.com/n1majne3/CyberPenda/issues/55)
- **Destination decision:** [Grill: Lock graph-first Blackboard destination and invariants](https://github.com/n1majne3/CyberPenda/issues/54)
- **Specification version:** `graph_blackboard_refactor_v1`
- **Mutation schema version:** `1`
- **Runtime/read protocol version:** `1`
- **Implementation plan version:** `graph_blackboard_tdd_v1`
- **Normative acceptance cases:** `212`
- **Vertical implementation slices:** `29`
- **Execution maps:** `4`
- **Canonical store after cutover:** `graph_v1`

This document is the canonical implementation entry point for replacing CyberPenda's Fact-centric Blackboard with a bounded, project-local typed property graph. It assembles the resolved architecture, points to the five detailed implementation contracts, fixes the red-green delivery order, and defines the handoff into four execution maps.

It deliberately does not duplicate every node field, SQLite column, transport payload, read shape, migration rule, or acceptance case. The linked contracts remain normative for those details. This document is normative for how those contracts compose, their dependency order, release gates, and the implementation handoff.

**Reader:** an engineer or agent arriving without the Wayfinder session history. **Post-read action:** choose the first unblocked execution-map ticket, write its named failing test at the specified public seam, and implement that slice without inventing architecture or cutover behavior.

The keywords **MUST**, **MUST NOT**, **SHOULD**, and **MAY** are normative.

## 1. Destination and readiness decision

The refactor is ready for implementation planning when all of the following are true:

1. the typed graph domain, storage ledger, Runtime protocol, operator reads/UI, and legacy migration contracts are versioned and mutually consistent;
2. every one of their `212` acceptance cases maps to at least one vertical red-green slice;
3. all `29` slices have explicit dependency edges and belong to one of four execution maps;
4. Release B cutover cannot start before graph/storage, Runtime, read/UI, and compatibility prerequisites are green;
5. compatibility retirement remains separate from cutover; and
6. no open design choice is left for an implementation ticket to invent.

Those conditions are satisfied by this specification and its normative contract set. **No unresolved architectural or sequencing decision remains before implementation begins.** Implementation begins only after the Wayfinder planning map is closed and work is claimed from the execution-map frontier.

### 1.1 In scope

- replace the Fact-centric Blackboard with a Project-local typed property graph as canonical semantic memory;
- support both Pentest Projects and single-challenge CTF Projects;
- preserve Task, Continuation, Runtime, Runner, Scope Snapshot, Task Event, Task Summary, and artifact-payload ownership outside the graph;
- retain SQLite as the physical store;
- expose one shared domain implementation through MCP, CLI, HTTP, Runtime projection, reports, and the bundled Web UI;
- migrate existing data without a dual-write interval;
- preserve legacy interface behavior through bounded compatibility adapters;
- deliver the change through test-first vertical slices and explicit release gates.

### 1.2 Out of scope

- per-command approval, packet-level scope enforcement, or a Runtime shell Policy Engine;
- replacing SQLite with an external graph database;
- copying, vendoring, or linking Cairn AGPL implementation code;
- turning raw commands, full logs, transcripts, artifact payload bytes, or all Task Events into graph nodes;
- relevance-filtering or dynamically injecting context into an already-running Continuation;
- a distributed graph scheduler, claim/heartbeat dispatcher, or automatic multi-worker lease system;
- implementation choices that weaken the versioned contracts without first revising the owning contract.

## 2. Normative contract set

The documents below form one specification bundle. Detailed rules live in exactly one owning contract and are referenced, not restated, elsewhere.

| Contract | Version / cases | Normative ownership |
| --- | --- | --- |
| [Blackboard Typed Property Graph Contract](./blackboard-graph-contract.md) | schema `1`; G1-G19 | Node and edge schemas, lifecycle, identity, aliases, merge, provenance, mutation semantics, validation, Current Truth, Frontier, and CTF solved state. |
| [Blackboard SQLite Persistence, History, Compaction, and Health Contract](./blackboard-graph-storage.md) | storage `1`; S1-S46 | Append-only full-state ledger, current indexes, transactions, reconstruction, hashes, budgets, compaction/restore, Continuation pins, recovery, and Blackboard Health persistence. |
| [Blackboard Runtime and Project Interface Protocol Contract](./blackboard-runtime-protocol.md) | protocol `1`; P1-P28 | Six semantic capabilities, trusted Continuation Interface Grants, MCP/CLI/HTTP parity, retained Evidence, Attempt checkpoints, Finish, Runtime files, and reconciliation. |
| [Blackboard Read Projections, Reports, and Operator UI Contract](./blackboard-read-projections.md) | `BlackboardReadV1`; R1-R57 | Revision-aware reads, deterministic ordering/search/pagination, Work/Entity/Health/Explorer views, Pentest and CTF outputs, legacy read projections, and operator information architecture. |
| [Blackboard Legacy Migration and Compatibility Cutover Contract](./blackboard-legacy-migration.md) | `legacy_blackboard_to_graph_v1`; M1-M62 | Inspection, backup, deterministic import, parity gates, one-transaction cutover, compatibility translation, rollback/recovery, deprecation, and finalization. |
| [Graph Blackboard TDD Acceptance Matrix and Implementation Slices](./blackboard-tdd-acceptance-and-slices.md) | `graph_blackboard_tdd_v1`; 212 cases | Pre-agreed test seams, fixtures, capability matrix, 29 vertical slices, dependency graph, release strategy, and completion gates. |

### 2.1 Precedence and change control

1. The graph contract owns semantic validity.
2. The storage contract owns durability and reconstruction without changing graph meaning.
3. The Runtime protocol owns trusted operational access without changing graph or storage rules.
4. The read contract owns presentation and compatibility reads without becoming a second source of truth.
5. The migration contract owns cutover exceptions and compatibility translation without creating a second canonical store.
6. The TDD plan owns implementation order without weakening upstream acceptance behavior.
7. This assembly owns cross-contract composition, release readiness, and execution-map structure.

If implementation evidence exposes a contradiction, work MUST stop at the affected slice. The owning contract must be revised explicitly before implementation continues; adapters MUST NOT invent local exceptions.

## 3. Architecture at a glance

The Blackboard is canonical Project-local semantic memory. Task lifecycle and Runtime execution remain separate domains and join graph records through provenance.

```text
Runtime / operator / system / migration callers
                    |
                    v
        Project-interface and compatibility modules
                    |
                    v
        BlackboardGraphService.Apply  <---- one semantic write seam
                    |
                    v
       Append-only SQLite full-state ledger
                    |
          +---------+---------+
          |                   |
          v                   v
BlackboardReadService   CanonicalMainGraphV1
          |                   |
          v                   v
HTTP / CLI / Web /      pinned Runtime context
reports / legacy reads  for one Continuation
```

### 3.1 Deep module seams

| Module | Interface seam | Responsibility |
| --- | --- | --- |
| Canonical graph module | `BlackboardGraphService.Apply` | All semantic validation and atomic graph mutation behavior. |
| Read projection module | `BlackboardReadService.Read` | Current/historical projections, deterministic reports, compatibility reads, and operator views. |
| Runtime project-interface module | Six closed semantic capabilities | Grant binding, provenance, retained Evidence, Attempt checkpoints, Finish, transport-neutral orchestration. |
| Migration module | `BlackboardMigrationService.Execute` | Inspect, backup, import, cutover, verify, recovery, and finalization. |
| Compatibility module | `BlackboardCompatibilityService.Call` | Legacy write translation, honest concurrency/idempotency behavior, deprecation metadata, and response parity. |
| Existing adapters | HTTP, MCP, CLI, Runtime projection, Web UI | Thin adapters that call the owning module and contain no graph, migration, or compatibility semantics. |

The deletion test justifies each seam: removing any one of these modules would spread its rules across multiple transports and pages. Behavioral tests and callers cross the same public interfaces.

### 3.2 Dependency direction

- Graph semantics do not import transport, UI, or migration behavior.
- Storage implements graph durability; it does not define domain validity.
- Project-interface and compatibility modules call graph/read/Task interfaces; transports call those modules.
- The read module reconstructs canonical state and joins durable external provenance; pages and renderers do not query graph tables.
- Migration enters graph state through trusted transaction-bound `Apply`; it never writes graph tables directly.
- Production wiring selects exactly one canonical store epoch and never calls legacy and graph writes together.

## 4. Cross-contract invariants

These invariants apply to every slice and release:

1. **Project isolation:** every node, edge, alias, mutation, revision, provenance reference, snapshot, compaction, and Health result belongs to exactly one Project.
2. **One semantic write seam:** graph tables are private implementation details; all semantic graph state changes cross `BlackboardGraphService.Apply`.
3. **One canonical store:** `legacy_v1` is canonical before cutover and `graph_v1` after cutover. There is no production dual-write or hidden read fallback.
4. **Append-only truth:** immutable full-state ledger records are canonical; mutable heads and caches are rebuildable projections.
5. **Server-bound provenance:** a Runtime cannot supply or spoof its Project, Task, Continuation, captured Runtime configuration, Runtime Profile, Runner, actor, or timestamp.
6. **Full Runtime context:** a Continuation receives the complete deterministic `CanonicalMainGraphV1` at its immutable pin. Budget pressure triggers Health and compaction behavior, never truncation or relevance filtering.
7. **Derived views stay derived:** Current Truth, Frontier, solved state, reports, CTF output, Graph Explorer layout, and Blackboard Health are not independent semantic stores.
8. **Task ownership stays outside the graph:** Goals project Task state, while Continuations, Task Events, Task Summaries, Scope Snapshots, raw logs, and artifact payload bytes remain in their owning domains.
9. **Explicit negative outcomes survive:** failed, blocked, inconclusive, and interrupted Attempts remain durable anchors and are protected from unsafe compaction.
10. **Compatibility is an adapter:** legacy reads and writes translate through canonical modules after cutover and never touch frozen legacy tables for current state.
11. **History is reproducible:** current and historical graphs, reports, and pinned snapshots are deterministic under random SQL row order and process reopen.
12. **Secrets remain out of semantic memory:** bearer tokens, raw Credential values, transcript text, command lines, and artifact payload bytes are not stored in graph properties or projection metadata.

## 5. Integrated behavior

### 5.1 Graph model

The graph contains the controlled semantic roles `Goal`, `Entity`, `ExplorationObjective`, `Attempt`, `Observation`, `Hypothesis`, `ProjectFact`, `Finding`, `Solution`, `EvidenceArtifact`, and `ProjectDirective`. Their property schemas and lifecycle transitions are closed under mutation schema version `1`; unknown fields and unknown enum values fail closed.

Stable keys are Project-local and type-scoped. Aliases resolve one hop to canonical identities. Merge is explicit, same-type, non-destructive, version-checked, and preserves literal source history. Edges are controlled, directional, and validated against the final proposed graph.

Pentest Projects have no automatic solved state. A CTF Challenge Project is solved only while its current graph contains a verified flag `Solution` that satisfies its Task `Goal`; rejecting or superseding every verified flag reverses current solved state without deleting history.

### 5.2 Persistence, snapshots, compaction, and Health

Accepted mutation batches, exact results, provenance, node/edge versions, key events, and compaction manifests form an append-only full-state ledger. Current heads, key registries, projection metrics, and Health caches are materialized and rebuildable.

Every graph mutation uses an immediate SQLite writer transaction. Mutation sequence, graph revision, record version, snapshot revision, and Health revision are distinct counters. Exact replay returns the original result bytes and consumes no counter; a first-seen all-no-op batch records one mutation sequence but no graph revision.

`CanonicalMainGraphV1` has exact field, ordering, byte, hash, and token-estimate rules. Continuation creation atomically binds its Runtime Configuration Version, graph pin, reconciliation state, and Interface Grant. Later mutation, merge, archive, restore, or compaction cannot alter an older pin.

Budget states are measured at exact 12K, 16K, and 20K thresholds. A valid write that reaches or crosses 20K commits first, then attempts deterministic safe compaction. If protected meaning prevents enough reduction, the full graph remains available and Health reports `compaction_blocked`.

### 5.3 Runtime and operator interfaces

The project-interface module exposes exactly six semantic capabilities:

1. apply an atomic typed graph mutation batch;
2. resolve current records after alias and merge resolution;
3. read the current full Runtime graph;
4. retain Evidence and represent it in the graph;
5. checkpoint an open Attempt into Task Events and graph state;
6. finish one Runtime Continuation's Blackboard protocol.

MCP, task CLI, operator CLI, and HTTP share canonical request/result/error behavior and a conformance corpus. Task-bound requests derive identity from a Continuation Interface Grant. Operator/system/migration callers use explicit trusted identities and cannot fabricate Runtime provenance.

Finish requires the Continuation's current Attempts to be terminal, persists its Task Summary and optional Objective Outcome, records the graph position, and closes the grant to new writes. Clean completion without a valid Finish is audited without guessing an open Attempt outcome. Unexpected termination marks only matching open Attempts as interrupted through system reconciliation.

### 5.4 Reads, reports, and operator UI

Every graph-backed operator read passes through `BlackboardReadService`. Reads name their observed graph revision and state hash, use deterministic ordering, and bind cursors to the original revision and normalized query. Potentially unbounded collections use cursor pagination with a default limit of 50 and maximum of 200.

The primary Blackboard interface is a dense ledger-first Work view with summary/Health strip, facets, sortable records, and one inspector. Entity browsing, provenance, history, and lineage are first-class. Graph Explorer is secondary and always has an equivalent accessible table/list representation.

`PentestReportV1` and `CTFSolutionV1` are deterministic derived semantic models with deterministic Markdown renderers. They do not become graph nodes or independent stores. Reports show only traceable graph and Scope conclusions; they do not synthesize proof or next actions with a model.

### 5.5 Migration and compatibility

Cutover is offline, backup-first, and atomic:

1. inspect the legacy database without modifying it;
2. stop Task launch and require no active Continuation;
3. create and independently verify a WAL-consistent SQLite backup;
4. import each Project through transaction-bound trusted `Apply`;
5. compare graph-backed compatibility projections with the source;
6. install legacy write guards, flip the global store epoch to `graph_v1`, and commit;
7. verify hashes, mappings, guards, and compatibility again before serving.

Failure before commit leaves the source canonical and unchanged. Failure after commit enters explicit recovery/verification; startup never silently reruns import or falls back to legacy tables. Frozen legacy tables remain only for the bounded rollback window and are removed solely through the Release D finalization gate.

## 6. TDD strategy and pre-agreed seams

Implementation follows red → green one tracer bullet at a time. A slice starts with its named first failing behavioral test, adds only enough behavior to make it pass, and repeats until the slice exit gate is satisfied. It MUST NOT write the entire 212-case suite before implementation or build tables/repositories/services horizontally before an executable capability exists.

| Test seam | Behavioral responsibility |
| --- | --- |
| `BlackboardGraphService.Apply` | Graph schema, lifecycle, atomic mutation, provenance, idempotency, versions, aliases, merge, archive, and Project isolation. |
| `BlackboardReadService.Read` | Current/historical projections, alias resolution, pagination, search, reports, Health presentation, and compatibility reads. |
| Project-interface module | Grant binding and the six semantic Runtime capabilities. |
| `BlackboardMigrationService.Execute` | Inspect, backup, cutover, verify, recovery, and finalization. |
| `BlackboardCompatibilityService.Call` | Legacy mutation translation, concurrency, idempotency adaptation, deprecation, and parity. |
| Task and Continuation public interfaces | Goal projection, atomic Continuation pinning, Finish, and reconciliation. |
| HTTP, MCP, and CLI public interfaces | Canonical-equivalent success, errors, authorization, and deprecation behavior. |
| Bundled Web UI through HTTP fixtures | Operator-visible information architecture and absence of legacy fallback requests. |

Behavioral tests observe results through these interfaces rather than querying SQLite. Real temporary file-backed SQLite and an Artifact Root MAY be inspected or deliberately damaged only for persistence, corruption, WAL, backup, migration-source, frozen-table, filesystem-confinement, crash, and recovery tests.

Expected values come from fixed literals and independently reviewed golden files. Tests MUST NOT call the production canonicalizer to manufacture their own expected bytes.

## 7. Master acceptance matrix

All 212 upstream cases remain normative. This matrix groups them by user-observable capability and assigns their first owning slices.

| ID | Acceptance capability | Normative cases | Primary slices |
| --- | --- | --- | --- |
| A01 | Closed node envelopes, type properties, Entity kinds, immutable roles, unknown-field rejection | G1-2, G16, G18 | C02-C03 |
| A02 | Controlled edge directions, final-graph validation, complete Project isolation | G3-4, S34 | C03 |
| A03 | System-owned Task Goals, Project kind, Objective prerequisites, deterministic Frontier | G5-6, S26-27, S37-38, R17-20 | C04, U02 |
| A04 | Attempt lifecycle for succeeded, failed, blocked, inconclusive, and interrupted outcomes | G7, S22-24, S41-42, P15, P19-21 | C05, I04-I05 |
| A05 | Runtime provenance, Events, support/contradiction, confirmation guards, provenance-safe reads | G8-10, S25, P2, P5-6, R25-27 | C05, I02, U03 |
| A06 | CTF-only verified flags, Goal satisfaction, reversible solved state, secret-safe summaries | G11, R44-48 | C06, U04 |
| A07 | Replay, payload conflicts, optimistic versions, concurrent keys, lost responses | G12-13, S3-6, S33, P4, P7, M9, M55-56 | C07, I02, M04-M05 |
| A08 | Atomic ledger commits, reconstruction, hash chains, SQLite integrity/connection policy | S1-2, S7-10, S31-32, S35, S43 | C01, C07 |
| A09 | Stable keys, aliases, non-destructive merge, edge collapse, duplicate candidates | G14-16, S11-13, R9-10, M23-29 | C08, M02-M03 |
| A10 | Archive guards, restore, historical reproducibility, one-snapshot restore hold | G17, S14-17, S39-40, R4, R11 | C08-C10, U01 |
| A11 | Exact canonical bytes, history, pins, remeasurement, resume, full delivery | S21, S36, S44-45, P10, P22-27, R1, M58 | C09, I06, M05 |
| A12 | Exact budget boundaries, safe compaction, 20K preservation, compaction-blocked behavior | S18-20, S39-40, P27 | C10, I06 |
| A13 | Complete Health detectors, staleness, failed scans, migration exceptions | S28-32, S46, R15, R20, R30-33, M22, M34, M60 | C10, U03, M03, M05 |
| A14 | MCP, task CLI, operator CLI, HTTP semantic/error parity | G19, P1, P3, P8-10, P28, M54 | I01-I02, M04 |
| A15 | Retained Evidence confinement/hash/retry/provenance and Attempt checkpointing | P11-14, M37-40, M51 | I03-I04, M03-M04 |
| A16 | Finish, closed grants, reconciliation, interruption, races, concurrent Tasks | P15-21, S22-24, S41-42, M52-53 | I04-I05, M04 |
| A17 | Revision-aware reads, ETags, alias history, stable cursors, pagination, search | R2-13, R28-29 | U01 |
| A18 | Dashboard, Work, Current Truth, Frontier, records, Entities, attention ordering | R14-24, S26-27 | U02 |
| A19 | Provenance joins, bounded traversal, Health projections, Graph Explorer | R25-33, R49-50 | U03 |
| A20 | Deterministic Pentest report, Scope selection, Evidence, stable Markdown | R34-43, M46 | U04-U05 |
| A21 | Legacy read projections and canonical-equivalent operator CLI modes | R51-57, M41-46 | U05, M03 |
| A22 | Bundled UI, bookmarks, accessible graph/table parity, no frozen-table fallback | M59, R49-50 | U03, U06 |
| A23 | Deterministic inspection, blockers, verified backup, unchanged source on failure | M1-6 | M01 |
| A24 | Deterministic Project, Task, Goal, Continuation, Fact, alias, merge, relation import | M11-29 | M02 |
| A25 | Deterministic Finding, Evidence, attachment, digest, missing path, mapping, parity import | M30-46 | M03, U05 |
| A26 | Legacy writes, honest conflicts, optional replay, Finish translation, deprecation | M47-57 | M04 |
| A27 | Atomic cutover, crash/reopen, store flip, guards, activation, verify, recovery | M7-10, M58-61 | M05 |
| A28 | Compatibility retirement and guarded finalization | M62 and migration section 16 | M06-M07 |

## 8. Four execution maps and 29 vertical slices

The implementation MUST use four execution maps. This split contains failure blast radius while preserving the shared seams and native dependency graph.

| Execution map | Slices | Destination |
| --- | --- | --- |
| Graph core and SQLite | C01-C10 | Canonical graph semantics, ledger, deterministic projection, compaction, and Health are complete behind `legacy_v1`. |
| Runtime project interfaces | I01-I06 | All six Runtime capabilities, grant/provenance binding, Evidence, Finish, reconciliation, snapshots, and adapter parity are complete. |
| Reads, reports, and operator UI | U01-U06 | One read module serves canonical projections, reports, legacy reads, and the bundled UI. |
| Migration, cutover, and retirement | M01-M07 | Legacy data is safely cut over, direct legacy writes freeze, compatibility retires by gates, and tables may be finalized. |

The canonical dependency graph is:

```text
C01 -> C02 -> C03 -> C04 -> C05 -> C06
                  \                 \
                   -> C07 -> C08 -> C09 -> C10
                                    |       |
                                    |       +-------> U03
                                    +--------------> U01 -> U02
                                    +--------------> I01 -> I02 -> I03 -> I04 -> I05 -> I06
C06 ------------------------------------------------> U04
U01 -> U02 -> U03 -> U04 -> U05 -> U06
C01 ----------------------> M01
C07 + C08 + M01 ----------> M02
M02 + U05 ----------------> M03
I04 + M03 ----------------> M04
C10 + I06 + U06 + M04 ---> M05 -> M06 -> M07
```

### 8.1 Graph core and SQLite

| Slice | Title | Blocked by |
| --- | --- | --- |
| [C01](./blackboard-tdd-acceptance-and-slices.md#c01--introduce-numbered-migrations-and-the-store-epoch) | Introduce numbered migrations and the store epoch | — |
| [C02](./blackboard-tdd-acceptance-and-slices.md#c02--deliver-the-first-canonical-graph-round-trip) | Deliver the first canonical graph round-trip | C01 |
| [C03](./blackboard-tdd-acceptance-and-slices.md#c03--complete-schema-entity-edge-and-validation-conformance) | Complete schema, Entity, edge, and validation conformance | C02 |
| [C04](./blackboard-tdd-acceptance-and-slices.md#c04--project-task-goals-and-objective-frontier) | Project Task Goals and Objective Frontier | C03 |
| [C05](./blackboard-tdd-acceptance-and-slices.md#c05--attempt-outcomes-semantic-support-and-confirmation-guards) | Attempt outcomes, semantic support, and confirmation guards | C04 |
| [C06](./blackboard-tdd-acceptance-and-slices.md#c06--ctf-flag-completion-and-reversible-solved-state) | CTF flag completion and reversible solved state | C05 |
| [C07](./blackboard-tdd-acceptance-and-slices.md#c07--idempotent-ledger-optimistic-concurrency-reconstruction-and-integrity) | Idempotent ledger, optimistic concurrency, reconstruction, and integrity | C03 |
| [C08](./blackboard-tdd-acceptance-and-slices.md#c08--alias-merge-archive-restore-and-duplicate-behavior) | Alias, merge, archive, restore, and duplicate behavior | C07 |
| [C09](./blackboard-tdd-acceptance-and-slices.md#c09--canonicalmaingraphv1-historical-snapshots-and-immutable-pins) | CanonicalMainGraphV1, historical snapshots, and immutable pins | C08 |
| [C10](./blackboard-tdd-acceptance-and-slices.md#c10--budget-policy-semantic-compaction-restore-hold-and-blackboard-health) | Budget policy, semantic compaction, restore hold, and Blackboard Health | C09 |

### 8.2 Runtime project interfaces

| Slice | Title | Blocked by |
| --- | --- | --- |
| [I01](./blackboard-tdd-acceptance-and-slices.md#i01--continuation-interface-grant-and-first-transport-tracer) | Continuation Interface Grant and first transport tracer | C09 |
| [I02](./blackboard-tdd-acceptance-and-slices.md#i02--full-mcp-cli-http-authorization-error-and-replay-parity) | Full MCP, CLI, HTTP, authorization, error, and replay parity | I01 |
| [I03](./blackboard-tdd-acceptance-and-slices.md#i03--retained-evidence-saga) | Retained Evidence saga | I02 |
| [I04](./blackboard-tdd-acceptance-and-slices.md#i04--attempt-checkpoint-and-continuation-finish) | Attempt checkpoint and Continuation Finish | I03 |
| [I05](./blackboard-tdd-acceptance-and-slices.md#i05--normal-and-unexpected-reconciliation-across-concurrent-tasks) | Normal and unexpected reconciliation across concurrent Tasks | I04 |
| [I06](./blackboard-tdd-acceptance-and-slices.md#i06--atomic-continuation-pin-files-runtime-protocol-and-resume) | Atomic Continuation pin, files, Runtime protocol, and resume | I05 |

### 8.3 Reads, reports, and operator UI

| Slice | Title | Blocked by |
| --- | --- | --- |
| [U01](./blackboard-tdd-acceptance-and-slices.md#u01--common-read-envelope-history-search-and-cursors) | Common read envelope, history, search, and cursors | C09 |
| [U02](./blackboard-tdd-acceptance-and-slices.md#u02--dashboard-blackboard-work-current-truth-frontier-and-entities) | Dashboard, Blackboard Work, Current Truth, Frontier, and Entities | U01 |
| [U03](./blackboard-tdd-acceptance-and-slices.md#u03--provenance-traversal-health-views-and-graph-explorer) | Provenance, traversal, Health views, and Graph Explorer | U02, C10 |
| [U04](./blackboard-tdd-acceptance-and-slices.md#u04--deterministic-pentest-and-ctf-deliverables) | Deterministic Pentest and CTF deliverables | U03, C06 |
| [U05](./blackboard-tdd-acceptance-and-slices.md#u05--legacy-read-projections-and-operator-cli-parity) | Legacy read projections and operator CLI parity | U04 |
| [U06](./blackboard-tdd-acceptance-and-slices.md#u06--bundled-ui-and-bookmark-compatible-cutover-behavior) | Bundled UI and bookmark-compatible cutover behavior | U05 |

### 8.4 Migration, cutover, and retirement

| Slice | Title | Blocked by |
| --- | --- | --- |
| [M01](./blackboard-tdd-acceptance-and-slices.md#m01--deterministic-inspection-and-verified-backup) | Deterministic inspection and verified backup | C01 |
| [M02](./blackboard-tdd-acceptance-and-slices.md#m02--import-projects-tasks-facts-aliases-merges-and-relations) | Import Projects, Tasks, Facts, aliases, merges, and relations | C07, C08, M01 |
| [M03](./blackboard-tdd-acceptance-and-slices.md#m03--import-findings-evidence-attachments-and-parity-gates) | Import Findings, Evidence, attachments, and parity gates | M02, U05 |
| [M04](./blackboard-tdd-acceptance-and-slices.md#m04--compatibility-writes-finish-translation-and-deprecation-parity) | Compatibility writes, Finish translation, and deprecation parity | I04, M03 |
| [M05](./blackboard-tdd-acceptance-and-slices.md#m05--atomic-cutover-activation-verification-and-recovery) | Atomic cutover, activation, verification, and recovery | C10, I06, U06, M04 |
| [M06](./blackboard-tdd-acceptance-and-slices.md#m06--release-c-compatibility-write-retirement) | Release C compatibility-write retirement | M05 |
| [M07](./blackboard-tdd-acceptance-and-slices.md#m07--release-d-compatibility-read-retirement-and-explicit-finalization) | Release D compatibility-read retirement and explicit finalization | M06 |

### 8.5 Allowed parallelism and critical path

- C10, I01, and U01 may begin independently after C09.
- M01 may begin after C01 because read-only inspection and backup do not require graph behavior.
- U04 may begin once both C06 and U03 are complete.
- Migration import tests before M05 operate only on disposable legacy fixtures and rolled-back transactions.
- The critical cutover path is C01-C10, I01-I06, U01-U06, and M01-M05.
- M06 and M07 are deliberately outside the initial cutover release.

## 9. Release sequence

| Release | Canonical store | Required slices | Production behavior |
| --- | --- | --- | --- |
| Release A — prepare | `legacy_v1` | C01-C10, I01-I06, U01-U06, M01-M04 | Graph modules, conformance, reads, UI, and migration behavior are complete behind legacy canonical wiring. Production routes do not write graph state. |
| Release B — cut over | transition to `graph_v1` | M05 after C10, I06, U06, M04 | Offline verified backup and one atomic store flip; graph-native Runtime/UI activate; legacy direct writes freeze; compatibility adapters serve from graph. |
| Release C — retire writes | `graph_v1` | M06 after stable Release B evidence | Compatibility writes are removed only after replacement adoption and local-use gates pass. |
| Release D — retire reads/finalize | `graph_v1` or `graph_v1_finalized` | M07 after stable Release C evidence | Compatibility reads may retire and frozen legacy tables may be explicitly finalized after all removal gates pass. |

Release B MUST NOT delete legacy tables or collapse M06/M07 into cutover. A green suite does not waive live cutover gates such as no active Continuations, verified backup, matching source digest, or blocker-free inspection.

## 10. Completion gates

The refactor is ready for Release B only when:

1. all G1-G19, S1-S46, P1-P28, R1-R57, and M1-M62 cases trace to passing tests;
2. exact `CanonicalMainGraphV1` golden bytes, hashes, and token estimates survive reopen;
3. HTTP, MCP, task CLI, and operator CLI pass the same transport conformance corpus;
4. legacy HTTP, MCP, CLI, Dashboard, report, and UI pass the compatibility corpus;
5. file-backed concurrency, crash, WAL, backup, restore, and migration failure-point suites pass;
6. every Blackboard Health detector has positive and negative fixtures;
7. the budget corpus proves full delivery at and above 20K;
8. every Attempt terminal outcome is proven;
9. concurrent Tasks prove Task/Continuation provenance isolation;
10. the CTF fixture proves solved-state reversal and flag-disclosure rules;
11. Web tests prove no frozen-table fallback request exists;
12. a static or integration guard proves graph-v1 public writes cannot mutate frozen legacy Blackboard tables;
13. `go test ./...` passes;
14. Web tests, lint, and production build pass; and
15. migration verification and Blackboard Health contain no critical integrity result.

## 11. ADR and domain-language decision

No new domain glossary term is introduced by this assembly. `CanonicalMainGraphV1`, store epoch, contract version names, conformance corpus, execution-map names, and slice identifiers are implementation or protocol names, not domain concepts. The existing `CONTEXT.md` vocabulary remains authoritative.

No new ADR is required. The hard-to-reverse choices and their rejected alternatives are already recorded in the owning versioned contracts:

- bounded typed graph rather than an unrestricted attack graph or Fact-only model;
- append-only full-state ledger rather than pure reducer replay or competing current/audit stores;
- one deep graph write seam rather than transport-specific rules;
- full pinned Runtime graph rather than relevance-selected context;
- one atomic no-dual-write cutover rather than a prolonged dual-canonical interval;
- four vertical execution maps rather than one mega issue or horizontal layer phases.

Creating another ADR would duplicate those normative records without adding decision context. A future change that reverses one of these choices should create an ADR at that time and explicitly supersede the owning contract version.

## 12. Implementation handoff

The tracker handoff consists of four execution-map issues and their 29 unassigned child implementation tickets. Each child ticket links to its complete **First red test**, **Minimal green path**, **Exit gate**, and **Coverage** section in the TDD plan. Native GitHub dependency edges are canonical; an issue is claimable only when all blockers are closed and it has no assignee.

Execution is explicitly in scope for those four maps. Their agents MUST:

- claim a frontier ticket before work;
- preserve unrelated worktree changes;
- work one red-green tracer bullet at a time through the named public seam;
- keep production on one canonical store epoch;
- close a slice only after its exit gate and relevant repository suites pass;
- update its execution map with a concise outcome pointer;
- stop and revise the owning contract if implementation evidence reveals a real specification contradiction.

This Wayfinder planning map produces decisions and the implementation handoff only. It does not implement any slice.
