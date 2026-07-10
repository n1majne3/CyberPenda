# Graph Blackboard TDD Acceptance Matrix and Implementation Slices

- **Status:** implementation sequencing contract for [Design TDD acceptance matrix and implementation slices for graph Blackboard](https://github.com/n1majne3/CyberPenda/issues/61)
- **Map:** [Map: Refactor Blackboard into bounded graph memory](https://github.com/n1majne3/CyberPenda/issues/55)
- **Graph contract:** [Blackboard Typed Property Graph Contract](./blackboard-graph-contract.md)
- **Storage contract:** [Blackboard SQLite Persistence, History, Compaction, and Health Contract](./blackboard-graph-storage.md)
- **Runtime contract:** [Blackboard Runtime and Project Interface Protocol Contract](./blackboard-runtime-protocol.md)
- **Read contract:** [Blackboard Read Projections, Reports, and Operator UI Contract](./blackboard-read-projections.md)
- **Migration contract:** [Blackboard Legacy Migration and Compatibility Cutover Contract](./blackboard-legacy-migration.md)
- **Plan version:** graph_blackboard_tdd_v1
- **Upstream acceptance cases:** 212

This document fixes the red-green implementation order for replacing the Fact-centric Blackboard with the graph-first Blackboard. It does not weaken or restate the five upstream contracts. Their acceptance matrices remain normative; this document maps every upstream case to a vertical slice, fixes the test seams, and prevents the work from turning into a horizontal or dual-write rewrite.

The keywords **MUST**, **MUST NOT**, **SHOULD**, and **MAY** are normative.

## 1. Decision

Implementation uses twenty-nine independently reviewable vertical slices grouped into four execution maps:

1. graph core and SQLite;
2. Runtime project interfaces;
3. reads, reports, and operator UI;
4. migration, cutover, and retirement.

Every slice starts with one failing behavioral test at a pre-agreed public seam, adds only enough implementation to pass it, then repeats for the remaining cases owned by that slice. There is no phase that writes all tests before production behavior, and no phase that builds every storage layer before any end-to-end capability exists.

The production store has one canonical epoch at a time:

- Before cutover, legacy_v1 remains canonical. Graph modules may be compiled, tested, inspected, and exercised inside a migration transaction, but production Project Interfaces do not write graph state.
- At cutover, one offline transaction imports, verifies, flips the epoch to graph_v1, and freezes direct legacy Blackboard writes.
- After cutover, graph_v1 is canonical. Legacy routes, tools, and commands are adapters over graph, read, project-interface, and Task modules; they never dual-write or read frozen tables as current semantic state.
- Compatibility writes, compatibility reads, and frozen tables retire only through the separate Release C and Release D gates.

The implementation plan recommends four execution maps rather than one. The graph/storage, Runtime protocol, read/UI, and migration/cutover areas have different test fixtures and failure blast radii, while still sharing the fixed deep seams. The final assembly ticket should create those maps after it publishes the unified specification.

No new domain glossary term is introduced. CanonicalMainGraphV1, store epoch, conformance suite, and slice identifiers are implementation or protocol names rather than Blackboard domain concepts. No new ADR is required: the hard architectural trade-offs are already recorded in the five versioned contracts.

## 2. Pre-agreed seams

The following interfaces are the only behavioral test surfaces for the refactor.

| Seam | What behavioral tests prove | What they do not inspect |
| --- | --- | --- |
| BlackboardGraphService.Apply | Graph schema, lifecycle, atomic mutation, provenance, idempotency, versioning, alias, merge, archive, and Project isolation | SQLite rows, helper calls, reducer internals |
| BlackboardReadService.Read | Current and historical projections, alias resolution, pagination, search, reports, Health presentation, and compatibility reads | Table layout or page-specific query helpers |
| Project-interface module | Continuation grant binding and the six semantic Runtime capabilities | Transport-specific handler internals |
| BlackboardMigrationService.Execute | Inspect, backup, cutover, verify, recovery, and finalization behavior | Unexported importer phases except through injected failure points |
| BlackboardCompatibilityService.Call | Legacy write translation, optimistic concurrency, idempotency adaptation, deprecation metadata, and response parity | Legacy table writes or transport-specific translation code |
| Task and Continuation public interfaces | Goal projection, atomic Continuation pinning, Finish, and reconciliation | Direct Task or graph table inspection for behavioral assertions |
| HTTP, MCP, and CLI public interfaces | Canonical-equivalent success, error, authorization, and deprecation behavior | Internal service call counts |
| Bundled Web UI through HTTP fixtures | Operator-visible information architecture, focused compatibility views, and no legacy fallback query path | Component implementation details |

Storage, crash, corruption, WAL, backup, filesystem confinement, and recovery tests MAY inspect or deliberately damage a real temporary SQLite database or Artifact Root. Those tests prove the storage adapter behind the seam; they do not replace behavioral tests.

### 2.1 Stable injected dependencies

Only these internal dependencies are stable injection points:

- clock;
- immutable ID source;
- grant/token source;
- trusted execution-context resolver;
- Artifact Root filesystem;
- projection renderer and budget estimator;
- backup implementation;
- migration transaction binder;
- compatibility-use counter;
- named persistence and crash failure points.

Production and behavioral tests use real SQLite. The implementation MUST NOT introduce a table-shaped repository interface merely to mock storage, and MUST NOT mock CyberPenda's own graph, read, project-interface, migration, or compatibility modules.

### 2.2 Observable verification rule

Behavioral assertions read results back through the owning public interface:

- a graph write is observed through MutationResult, Resolve Records, or BlackboardReadService;
- a Finish is observed through Task Summary and Continuation state;
- a report is observed through PentestReportV1 or CTFSolutionV1;
- transport parity is observed through canonical JSON/error fixtures;
- Web behavior is observed through rendered pages backed by HTTP fixture responses.

Direct SQL is allowed only for storage integrity, crash, migration-source construction, frozen-table guard, and finalization tests.

## 3. Recommended module landing zones

Interface ownership is normative. The package names below are the recommended low-risk landing zones and may change only if the same dependency direction and seams are preserved.

| Module | Recommended location | Responsibility |
| --- | --- | --- |
| Numbered SQLite migrations and connection policy | internal/store | Checksummed migrations, per-connection pragmas, transaction helpers, store epoch control tables |
| Canonical graph module | internal/blackboard | GraphService, semantic validation, SQLite ledger adapter, alias-resolving current/historical graph access |
| Runtime project-interface module | internal/projectinterface | Continuation Interface Grants, six semantic capabilities, structured errors, transport-neutral orchestration |
| Read projection module | internal/blackboardread | BlackboardReadService, deterministic projections, reports, compatibility reads |
| Migration module | internal/blackboardmigration | Inspect, backup, import plan, cutover, verify, recovery, finalization |
| Compatibility module | internal/blackboardcompat | Legacy write translation and deprecation behavior |
| Existing adapters | internal/daemon, internal/mcpserver, internal/pentestctl, internal/runner, web/src | Thin HTTP, MCP, CLI, Runtime projection, and UI adapters |

The existing internal/blackboard Service remains the legacy implementation while canonical_store is legacy_v1. New production wiring MUST select either the legacy implementation or the graph modules from the store epoch; it MUST NOT call both. After Release D, obsolete Fact/Finding/Evidence write implementation and tests are removed once replacement seam coverage exists.

## 4. Test harness and fixtures

Test infrastructure grows inside the first slice that needs it. There is no standalone horizontal test-harness phase.

### 4.1 Shared deterministic sources

Tests use:

- a sequence clock with explicit RFC3339Nano values;
- a deterministic ID source with readable fixture IDs;
- a deterministic grant/token source whose plaintext never appears in persisted output;
- canonical JSON helpers that compare exact bytes;
- an Artifact Root rooted in a temporary directory;
- file-backed SQLite for reopen, WAL, concurrent-writer, crash, backup, and migration cases;
- in-memory SQLite only for fast non-reopen graph validation cases.

Expected values come from fixed literals and independently reviewed golden files, never by calling the implementation's canonicalizer to manufacture the expected result.

### 4.2 Fixture families

The shared fixture corpus SHOULD live under testdata/blackboard with reusable Go builders under internal/testsupport/blackboardfixture.

| Fixture | Required content |
| --- | --- |
| minimal_pentest | Pentest Project, one Task Goal, one Entity, one Objective, one open Attempt |
| attempt_outcomes | Separate succeeded, failed, blocked, inconclusive, and interrupted Attempts with valid summaries and provenance |
| concurrent_tasks | Two Tasks and Continuations writing the same Project without cross-bound provenance |
| ctf_flag | CTF Goal, candidate and verified flag Solutions, Evidence, satisfies edge, rejection/supersession history |
| duplicate_merge | Alias chains, same-type duplicates, edge collapse, later merge, archive, and restore |
| budget_boundaries | Canonical graphs measuring exactly 12,000, 12,001, 15,999, 16,000, 19,999, and 20,000 estimated tokens |
| health_catalog | Positive and negative fixture for every Blackboard Health detector |
| transport_conformance | One canonical request/result/error corpus executable through project-interface, HTTP, MCP, task CLI, and operator CLI |
| legacy_corpus | Legacy Projects, Tasks, Facts, relations, aliases, merges, Findings, Evidence, reports, incomplete provenance, invalid blockers, and golden compatibility outputs |
| cutover_crash | Named failure point at every migration transaction stage plus pre-commit, post-commit, and lost-response reopen states |

### 4.3 Conformance runners

One transport-neutral conformance runner accepts an adapter satisfying the six project-interface capabilities. HTTP, MCP, task CLI, and operator CLI each run the same requests and compare:

- semantic result;
- observed graph revision;
- structured error code and details;
- idempotent replay bytes;
- current graph after the operation.

Compatibility transports run a second shared corpus against BlackboardCompatibilityService and legacy read projections. Adapters may add headers, MCP metadata, or CLI stderr warnings, but canonical JSON payloads remain comparable.

### 4.4 Existing tests

Current tests remain until replacement behavior is green:

- existing Fact/Finding/Evidence service tests become legacy characterization fixtures;
- daemon, MCP, pentestctl, dashboard, report, Task, and Web tests continue to guard public behavior;
- a legacy test is deleted only in the same slice that adds equal or stronger coverage at the new seam;
- internal-call assertions are not carried forward.

## 5. Master acceptance matrix

Source identifiers use:

- G for graph-contract section 13;
- S for storage-contract section 18;
- P for Runtime-protocol section 17;
- R for read-contract section 21;
- M for migration-contract section 18.

All 212 upstream cases remain required. The matrix below groups them by user-observable capability and assigns their first owning slices.

| ID | Acceptance capability | Normative source cases | Primary slices |
| --- | --- | --- | --- |
| A01 | Closed node envelopes, per-type properties, Entity kinds, immutable semantic roles, and unknown-field rejection | G1-2, G16, G18 | C02-C03 |
| A02 | Controlled edge directions, final-graph validation, and complete Project isolation | G3-4, S34 | C03 |
| A03 | System-owned Task Goals, Project kind, Objective prerequisites, and deterministic Frontier | G5-6, S26-27, S37-38, R17-20 | C04, U02 |
| A04 | Attempt open and terminal lifecycle for succeeded, failed, blocked, inconclusive, and interrupted outcomes | G7, S22-24, S41-42, P15, P19-21 | C05, I04-I05 |
| A05 | Runtime provenance, source Events, Hypothesis support, confirmed Fact/Finding guards, and provenance-safe reads | G8-10, S25, P2, P5-6, R25-27 | C05, I02, U03 |
| A06 | CTF-only verified flags, Goal satisfaction, reversible solved state, and secret-safe summaries | G11, R44-48 | C06, U04 |
| A07 | Exact replay, changed-payload conflicts, optimistic versions, concurrent same-key writes, and lost responses | G12-13, S3-6, S33, P4, P7, M9, M55-56 | C07, I02, M04-M05 |
| A08 | Atomic ledger commits, deterministic reconstruction, hash chains, SQLite integrity, and connection policy | S1-2, S7-10, S31-32, S35, S43 | C01, C07 |
| A09 | Stable keys, one-hop aliases, non-destructive same-type merge, edge collapse, and advisory duplicate candidates | G14-16, S11-13, R9-10, M23-29 | C08, M02-M03 |
| A10 | Archive guards, explicit restore, historical reproducibility, and one-snapshot restore hold | G17, S14-17, S39-40, R4, R11 | C08-C10, U01 |
| A11 | Exact CanonicalMainGraphV1 bytes, historical snapshots, atomic Continuation pins, projection-remeasurement recovery, resume regeneration, and full delivery | S21, S36, S44-45, P10, P22-27, R1, M58 | C09, I06, M05 |
| A12 | Exact budget boundaries, safe deterministic compaction, 20K full-graph preservation, and compaction-blocked behavior | S18-20, S39-40, P27 | C10, I06 |
| A13 | Complete Blackboard Health detectors, staleness, failed scans, and migration exceptions | S28-32, S46, R15, R20, R30-33, M22, M34, M60 | C10, U03, M03, M05 |
| A14 | MCP, task CLI, operator CLI, and HTTP semantic/error parity over one project-interface module | G19, P1, P3, P8-10, P28, M54 | I01-I02, M04 |
| A15 | Retained Evidence confinement, hashing, crash-safe retry, matching produced provenance, and Attempt checkpointing | P11-14, M37-40, M51 | I03-I04, M03-M04 |
| A16 | Finish, write-closed grants, normal reconciliation, unexpected interruption, races, and concurrent Tasks | P15-21, S22-24, S41-42, M52-53 | I04-I05, M04 |
| A17 | Revision-aware reads, ETags, alias/literal history, stable cursors, pagination, and deterministic lexical search | R2-13, R28-29 | U01 |
| A18 | Dashboard, Blackboard Work, Current Truth, Frontier, record detail, Entities, and attention ordering | R14-24, S26-27 | U02 |
| A19 | Exact provenance joins, bounded traversal, Health projections, and secondary Graph Explorer behavior | R25-33, R49-50 | U03 |
| A20 | Deterministic PentestReportV1, Task/current Scope selection, Evidence rules, and stable Markdown | R34-43, M46 | U04-U05 |
| A21 | Legacy Fact/Finding/Evidence/Dashboard/report reads and canonical-equivalent operator CLI modes | R51-57, M41-46 | U05, M03 |
| A22 | Bundled UI focused views, valid bookmarks, accessible graph/table parity, and no frozen-table fallback | M59, R49-50 | U03, U06 |
| A23 | Read-only deterministic inspection, blockers, verified WAL-consistent backup, and unchanged source on failure | M1-6 | M01 |
| A24 | Deterministic Project, Task Goal, Continuation, Fact, alias, merge, relation, and history import | M11-29 | M02 |
| A25 | Deterministic Finding, Evidence, attachment, digest, missing-path, mapping, and parity import | M30-46 | M03, U05 |
| A26 | Legacy write translation, honest conflicts, optional exact replay, Finish translation, and deprecation metadata | M47-57 | M04 |
| A27 | One-transaction cutover, crash/reopen behavior, store flip, frozen-table guards, graph-native Runtime/UI activation, verify, and rollback warning | M7-10, M58-61 | M05 |
| A28 | Compatibility-write retirement, compatibility-read retirement, and explicit guarded finalization | M62 and migration section 16 gates | M06-M07 |

Coverage ledger:

- Graph cases G1-G19 are owned by C03-C08 and I02.
- Storage cases S1-S46 are owned by C01, C04-C05, C07-C10, I02, I05-I06, and U02-U03.
- Runtime cases P1-P28 are owned by I01-I06 and M04.
- Read cases R1-R57 are owned by C06, C09, I06, and U01-U05.
- Migration cases M1-M62 are owned by M01-M07, with compatibility projection cases also exercised in U05-U06.

## 6. Dependency map and execution maps

The four recommended execution maps and their child slices are:

| Execution map | Slices | Destination |
| --- | --- | --- |
| Graph core and SQLite | C01-C10 | Canonical graph semantics, ledger, deterministic projection, compaction, and Health are implementation-ready behind legacy_v1 |
| Runtime project interfaces | I01-I06 | All six Runtime capabilities, grant/provenance binding, Evidence, Finish, reconciliation, snapshots, and adapter parity are complete |
| Reads, reports, and operator UI | U01-U06 | One read module serves canonical projections, reports, legacy reads, and the bundled UI |
| Migration, cutover, and retirement | M01-M07 | Legacy data is safely cut over, direct legacy writes are frozen, compatibility is retired by gates, and tables may be finalized |

The dependency graph is:

    C01 -> C02 -> C03 -> C04 -> C05 -> C06
                         \                 \
                          -> C07 -> C08 -> C09 -> C10
                                           |       |
                                           |       +-------> U03
                                           +--------------> U01 -> U02
                                           +--------------> I01 -> I02 -> I03 -> I04 -> I05 -> I06
    C06 ---------------------------------------------------> U04
    U01 -> U02 -> U03 -> U04 -> U05 -> U06
    C01 -----------------------> M01
    C07 + C08 + M01 -----------> M02
    M02 + U05 -----------------> M03
    I04 + M03 -----------------> M04
    C10 + I06 + U06 + M04 ----> M05 -> M06 -> M07

Allowed parallelism:

- C10, I01, and U01 may begin independently after C09.
- M01 may begin after C01 because inspection and backup do not require graph behavior.
- U04 may begin after C06 and U01; it need not wait for every operator view.
- M02 and M03 remain off the production path until M05; their import tests operate on disposable legacy fixtures and rollback transactions.

The critical cutover path is C01-C10, I01-I06, U01-U06, M01-M05. M06 and M07 deliberately occur in later stable releases.

## 7. Graph core and SQLite slices

### C01 — Introduce numbered migrations and the store epoch

**First red test:** TestOpenRejectsMigrationChecksumDriftWithoutChangingLegacyBlackboard.

**Minimal green path:**

- replace the unversioned DDL loop with numbered, checksummed, one-transaction migrations;
- apply required SQLite pragmas on every connection and use immediate writer transactions;
- add Project kind, Task/Continuation/Event/Summary graph-support fields, schema migration history, store epoch, migration-run, and mapping control tables;
- default an existing database to canonical_store=legacy_v1 without changing public Fact/Finding/Evidence behavior.

**Exit gate:**

- old current-service tests pass unchanged;
- opening an older database preserves every row;
- a newer or checksum-mismatched schema fails closed;
- no production graph write route exists;
- every transaction connection proves the required pragmas and lock mode.

**Coverage:** A03, A08, A23.

### C02 — Deliver the first canonical graph round-trip

**First red test:** TestApplyCreatesTentativeProjectFactAndReadReturnsItAfterReopen.

**Minimal green path:**

- add GraphService with BlackboardGraphService.Apply;
- persist one accepted mutation, trusted provenance, one ProjectFact identity/version, a current head, and a stable-key registry entry atomically;
- add the smallest alias-resolving read needed to observe the record at the committed graph revision;
- keep graph data dark while the store epoch is legacy_v1.

**Exit gate:**

- a valid ProjectFact create returns version 1 and graph revision 1;
- reopen returns the same semantic record and hashes;
- a failed batch leaves no mutation, version, key, or head;
- cross-Project references fail before any state change.

**Coverage:** A01-A02, A08.

### C03 — Complete schema, Entity, edge, and validation conformance

**First red test:** TestApplyRejectsReversedControlledEdgeWithoutPartialWrite.

**Minimal green path:**

- implement the closed node-property schemas and controlled Entity locators;
- implement the complete edge endpoint/direction matrix and cycle checks;
- validate the final proposed graph rather than operation-local intermediate state;
- emit the stable graph validation codes and paths;
- reject unknown fields, enums, semantic-role changes, spoofed provenance, and cross-Project aliases or Events.

**Exit gate:**

- every node type passes its minimal valid fixture;
- every missing, invalid, and unknown property fixture fails independently;
- every allowed edge direction succeeds and every reversed/invalid pair fails;
- adapters can reuse one graph conformance corpus without adding graph rules.

**Coverage:** A01-A02, A05.

### C04 — Project Task Goals and Objective Frontier

**First red test:** TestTaskCreationProjectsExactlyOneGoalAndFrontierWaitsForPrerequisites.

**Minimal green path:**

- make Project kind immutable and part of state/projection hashes;
- add the system Goal projector driven by durable Task state;
- repair missing or status-stale Goals at startup and before Continuation pinning;
- treat immutable Goal text, Task ID, or stable-key drift as critical rather than rewriting it;
- implement ExplorationObjective lifecycle, depends_on, blocks, satisfies, supersedes, and derived Frontier.

**Exit gate:**

- operator and Runtime Apply cannot create or edit Goals;
- concurrent Task status changes converge to the exact Goal status;
- Frontier handles resolved, abandoned, superseded, archived, merged, missing, and corrupt prerequisites deterministically;
- Pentest Projects do not acquire an automatic Project solved state.

**Coverage:** A03, A13, A18.

### C05 — Attempt outcomes, semantic support, and confirmation guards

**First red test:** TestAttemptTerminalOutcomesRequireTestsSummaryAndOutcomeSpecificGuards.

**Minimal green path:**

- implement Attempt open and terminal transitions;
- require tests edges while open, a summary when terminal, produced output for succeeded, and system provenance for interrupted;
- preserve failed, blocked, and inconclusive outcomes as explicit negative-result anchors;
- enforce Runtime-produced edges with matching Task and Continuation provenance;
- implement Hypothesis support/contradiction and confirmed ProjectFact/Finding guards.

**Exit gate:**

- succeeded, failed, blocked, inconclusive, and interrupted fixtures each pass through Apply;
- terminal Attempts cannot reopen or change terminal class;
- unsupported confirmation fails without partial writes;
- a succeeded producing Attempt may support a confirmed conclusion only when provenance matches.

**Coverage:** A04-A05.

### C06 — CTF flag completion and reversible solved state

**First red test:** TestVerifiedFlagSolvesOnlyCTFProjectAndRejectionReversesSolvedState.

**Minimal green path:**

- implement Solution kinds and lifecycle;
- require a verified flag to satisfy its Task Goal;
- reject verified flags in Pentest Projects;
- derive CTF solved state from current verified flag Solutions rather than storing a solved node or boolean;
- preserve historical verified/rejected/superseded output.

**Exit gate:**

- zero verified flags returns unsolved;
- one verified flag returns solved with the satisfying Goal;
- multiple distinct verified flags return deterministic primary/conflict metadata;
- rejecting or superseding all verified flags reverses current solved state.

**Coverage:** A06.

### C07 — Idempotent ledger, optimistic concurrency, reconstruction, and integrity

**First red test:** TestLostResponseReplayReturnsByteIdenticalMutationResult.

**Minimal green path:**

- complete mutation sequence, graph revision, operation results, full node/edge versions, and append-only provenance Event storage;
- canonicalize request hashes and persist exact result bytes;
- distinguish exact replay, first-seen all-no-op, changed-payload conflict, stale expected version, and retryable SQLite busy;
- serialize writers with BEGIN IMMEDIATE;
- rebuild heads and key registry from the ledger;
- add historical reconstruction and mutation/state hash-chain verification.

**Exit gate:**

- exact replay returns identical IDs, timestamps, versions, bytes, sequence, and revision;
- first-seen no-op consumes one mutation sequence and no graph revision;
- concurrent identical same-key writes converge while conflicting payloads conflict;
- deleting heads and keys then rebuilding produces identical current and historical output;
- provenance Event deletion or reorder breaks the integrity chain.

**Coverage:** A07-A08, A14.

### C08 — Alias, merge, archive, restore, and duplicate behavior

**First red test:** TestMergePreservesSourceHistoryAliasesEdgesAndCanonicalIdentity.

**Minimal green path:**

- implement append-only key/alias events and one-hop alias resolution;
- implement same-type non-destructive Merge Nodes with expected versions;
- retarget or collapse active semantic edges without self-edges or cycles;
- add archive guards and atomic retirement of touching active edges;
- add explicit restore from a manifest under current aliases and merge redirects;
- add deterministic advisory duplicate fingerprints with no automatic merge.

**Exit gate:**

- source identities and history remain literal-addressable after merge;
- ordinary reads/writes resolve aliases and report the redirect;
- create never silently updates through an alias;
- archive cannot remove protected live meaning;
- restore recreates only topology valid in the current graph.

**Coverage:** A09-A10.

### C09 — CanonicalMainGraphV1, historical snapshots, and immutable pins

**First red test:** TestCanonicalMainGraphGoldenBytesSurviveRandomSQLOrderAndReopen.

**Minimal green path:**

- implement exact CanonicalMainGraphV1 fields, explicit nulls, type/edge ordinals, stable ordering, and domain-separated hashes;
- implement the utf8_bytes_div_4_v1 estimator;
- reconstruct the same bytes at a historical graph revision;
- add immutable Continuation pin fields and snapshot regeneration primitives;
- treat ordinary projection-sizing failure as a committed graph with dirty/unknown metrics rather than a rejected semantic write.

**Exit gate:**

- the golden fixture asserts exact bytes, hashes, bytes count, and token estimate;
- later mutations, merge, archive, restore, and compaction never alter an older pin;
- a missing snapshot file can be regenerated byte-identically from its pin;
- a hash mismatch fails snapshot verification.

**Coverage:** A10-A12.

### C10 — Budget policy, semantic compaction, restore hold, and Blackboard Health

**First red test:** TestTwentyThousandTokenWriteCommitsFullGraphThenAttemptsSafeCompaction.

**Minimal green path:**

- implement exact budget bands and deterministic protected/eligible component planning;
- commit valid graph writes before required compaction;
- persist append-only compaction/restore manifests and validate stale plans;
- preserve failed, blocked, inconclusive, and interrupted Attempt anchors;
- implement the one-snapshot restore hold;
- persist derived Health runs/results, metrics, detector fingerprints, staleness, and unknown scans.

**Exit gate:**

- exact 12,000, 12,001, 15,999, 16,000, 19,999, and 20,000 fixtures produce the specified states;
- safe candidate exhaustion at or above 20K reports compaction_blocked while still returning the full graph;
- compaction and restore roll back when mandatory before/after rendering fails;
- every Health detector has positive and negative fixtures;
- Health never mutates semantic graph state or becomes a launch policy gate.

**Coverage:** A10-A13.

## 8. Runtime project-interface slices

### I01 — Continuation Interface Grant and first transport tracer

**First red test:** TestTaskBoundApplyCannotSpoofProjectAndIsReadableThroughCurrentGraph.

**Minimal green path:**

- add the Continuation Interface Grant store and trusted context resolver;
- server-bind Project, Task, Continuation, Runtime configuration, Runtime Profile, Runtime plugin, Runner, actor, and timestamp;
- implement Apply Mutation, Resolve Records, and Current Runtime Graph in the project-interface module;
- expose one end-to-end canonical path through trusted MCP and HTTP;
- reject request-body provenance and path/grant mismatches before graph access.

**Exit gate:**

- a Runtime records a graph mutation without supplying Project or provenance fields;
- the same record is visible through Resolve Records and Current Graph;
- plaintext grants do not appear in graph, Events, logs, snapshots, context JSON, or generated instructions;
- finished, revoked, and terminal grants reject new writes while reads and exact replay remain possible.

**Coverage:** A05, A11, A14.

### I02 — Full MCP, CLI, HTTP, authorization, error, and replay parity

**First red test:** TestProjectInterfaceConformanceProducesSameResultAndErrorAcrossAllTransports.

**Minimal green path:**

- add graph-native MCP tools, task and operator pentestctl commands, and HTTP routes for Apply, Resolve Records, and Current Graph;
- run the shared transport conformance corpus through every adapter;
- implement ProjectInterfaceErrorV1 mappings and stable CLI exits;
- enforce actor eligibility and source Event Task/Continuation binding;
- preserve canonical request hashing and exact replay after transport-level lost responses.

**Exit gate:**

- equivalent requests produce identical semantic results and graph state;
- MCP domain failures are structured isError results;
- HTTP status, CLI exit, error code, path, retryability, and details agree;
- no adapter duplicates graph validation or provenance rules.

**Coverage:** A05, A07, A14.

### I03 — Retained Evidence saga

**First red test:** TestRetainEvidenceConvergesAcrossFilePublishGraphCommitAndLostResponseFailures.

**Minimal green path:**

- implement the Retain Evidence capability;
- confine Runtime sources to the Task Artifact Root and operator sources to configured roots;
- reject traversal, symlink escape, another Task root, and source replacement races;
- copy/hash/size payloads before representing them through Apply;
- persist cross-domain idempotency state that converges after each named failure point.

**Exit gate:**

- Runtime Evidence has system-computed managed path, digest, size, and matching Attempt-produced provenance;
- repeated calls never duplicate retained files or graph nodes;
- missing or changed source bytes return stable errors without false support;
- artifact payload bytes never enter graph properties or logs.

**Coverage:** A05, A15.

### I04 — Attempt checkpoint and Continuation Finish

**First red test:** TestFinishRejectsOpenAttemptsThenStoresSummaryAndClosesGrantAfterTerminalCheckpoint.

**Minimal green path:**

- implement Checkpoint Attempt as one durable Event plus one idempotent graph patch with Event provenance;
- implement Finish as an idempotent Task-domain transaction after reading current graph Attempts;
- require every current-Continuation Attempt to be terminal;
- store the Continuation-bound Task Summary, optional Objective Outcome, graph position, finish marker, and write-closed grant atomically;
- allow exact Finish replay and reject changed summaries/outcomes with continuation_finish_conflict.

**Exit gate:**

- checkpoint retry reuses the same Event after partial failure;
- Finish writes nothing when an open Attempt remains;
- succeeded, failed, blocked, and inconclusive Runtime outcomes all Finish without being reclassified;
- any new Runtime write after Finish fails while exact replay succeeds.

**Coverage:** A04, A15-A16.

### I05 — Normal and unexpected reconciliation across concurrent Tasks

**First red test:** TestUnexpectedContinuationInterruptsOnlyItsMatchingOpenAttempts.

**Minimal green path:**

- implement clean-completion auditing that never guesses failed, blocked, or inconclusive;
- implement unexpected-end recovery for matching open Attempts only;
- select Attempt-specific summaries and bounded ordered Event references;
- persist reconciliation markers and discover a committed mutation after a lost marker;
- decide Runtime/reconciler races through expected versions.

**Exit gate:**

- two Tasks and two Continuations may write the same Project without provenance bleed;
- two simultaneous open Attempts receive distinct summaries and Event references;
- already terminal Attempts, other Continuations, other Tasks, and other Projects remain unchanged;
- repeated recovery is a no-op;
- clean completion with an open Attempt becomes a protocol-gap Health result, not an invented terminal status.

**Coverage:** A04, A13, A16.

### I06 — Atomic Continuation pin, files, Runtime protocol, and resume

**First red test:** TestContinuationLaunchAtomicallyPinsConfigGraphGrantAndVerifiesSnapshotBeforeRuntimeStart.

**Minimal green path:**

- create Runtime configuration version, Continuation, graph pin, reconciliation state, and grant in one short transaction;
- materialize and verify .pentest/blackboard.json plus non-secret context metadata;
- generate AGENTS.md and CLAUDE.md from one canonical Blackboard Runtime Protocol;
- prove every built-in Runtime adapter reconstructs exact CanonicalMainGraphV1 from RuntimeBlackboardContextV1;
- give native resume a newly pinned full snapshot that supersedes historical context;
- deliver full graphs at or above 20K without truncation or relevance filtering.

**Exit gate:**

- Runtime never starts before snapshot hash verification;
- a crash after database commit regenerates the same file without a new pin;
- every adapter carries the same protocol version and rule digest;
- resume receives current full context and leaves prior pins historical;
- snapshot_unavailable is a concrete readiness failure, not a Health severity rewrite.

**Coverage:** A11-A12, A16.

## 9. Read, report, and operator UI slices

### U01 — Common read envelope, history, search, and cursors

**First red test:** TestReadCursorPinsRevisionWhileConcurrentWriterCommits.

**Minimal green path:**

- add BlackboardReadService.Read as a closed versioned union;
- open one SQLite snapshot transaction per request;
- return Project kind, observed graph revision, state hash, source pins, projection hash, ETag, and stable read errors;
- implement current and at_revision reconstruction;
- implement alias-resolving ordinary reads and literal merged-source history;
- implement deterministic lexical search and revision-bound cursor pagination;
- expose the first canonical HTTP and operator CLI reads through the same module.

**Exit gate:**

- concurrent writes do not change a paginated query's pinned result;
- cursor reuse with a different Project, filter, sort, limit, or projection fails;
- equal-sort rows have no duplicate or gap;
- archived rows remain hidden by default but readable explicitly;
- offline --db and daemon --api modes return canonical-equivalent JSON.

**Coverage:** A10, A17.

### U02 — Dashboard, Blackboard Work, Current Truth, Frontier, and Entities

**First red test:** TestBlackboardWorkAttentionOrdersCriticalHealthBeforeFrontierAndActiveWork.

**Minimal green path:**

- implement ProjectBlackboardSummaryV1 and BlackboardWorkViewV1;
- implement CurrentTruthV1 and ExplorationFrontierV1 from graph semantics;
- implement record collection/detail and mutation capability hints;
- implement Entity roots, descendants, multi-parent breadcrumbs, and secret-safe Credential detail;
- replace page-specific dashboard/fact/finding counting queries with the shared read module.

**Exit gate:**

- summary counts match the golden graph without reading legacy tables;
- Current Truth includes tentative and out-of-scope ProjectFacts with explicit actionability;
- Frontier ordering and stranded Objective behavior match the contracts;
- Entity DAG traversal is deterministic and Credential values never appear;
- advisory mutation hints tolerate a concurrent write becoming stale.

**Coverage:** A03, A13, A18.

### U03 — Provenance, traversal, Health views, and Graph Explorer

**First red test:** TestRecordProvenanceJoinsCapturedRuntimeStateWithoutRawOutputOrSecrets.

**Minimal green path:**

- implement RecordProvenanceV1 and bounded GraphTraversalV1 over the history foundation from U01;
- join exact Task, Continuation, captured Runtime configuration, Runner, Scope Snapshot, and compact source Events;
- implement Health summary/run/result projections and explicit Health-run action;
- implement GraphExplorerV1 as a secondary bounded projection with exact table/canvas parity;
- return projection_too_large with exact counts instead of sampling.

**Exit gate:**

- provenance survives deletion of a live Runtime Profile;
- transcript text, command lines, tokens, and raw output never appear;
- traversal is breadth-first, direction-preserving, bounded, and explicit when truncated;
- latest Health staleness is independent of overall severity;
- Graph Explorer table and canvas data are identical and accessible without making the graph canvas the only interface.

**Coverage:** A05, A13, A19, A22.

### U04 — Deterministic Pentest and CTF deliverables

**First red test:** TestPentestReportSameSourceHashProducesByteIdenticalJSONAndMarkdown.

**Minimal green path:**

- implement PentestReportV1 semantic assembly and deterministic Markdown;
- default to current Scope, with optional Task Scope Snapshot selection;
- include explicit contributing Task, Continuation, Runner, conclusion, and Evidence provenance;
- separate confirmed, unconfirmed, false-positive, tentative, and out-of-scope content;
- implement CTFSolutionV1 with candidate/procedure/Evidence/provenance output and flag-value disclosure only on the explicit CTF route.

**Exit gate:**

- report output has no render-time clock and ends with exactly one LF;
- produced-only artifacts are not presented as proof;
- missing Evidence remains visible without invented proof;
- host Runner contributions are explicit;
- Pentest report rejects CTF Projects and CTF output rejects Pentest Projects.

**Coverage:** A06, A20.

### U05 — Legacy read projections and operator CLI parity

**First red test:** TestLegacyFactFindingEvidenceGoldenResponsesComeOnlyFromGraphReadService.

**Minimal green path:**

- implement legacy Fact index, point, version, relation, Finding, Evidence, Dashboard, and report projection kinds;
- preserve documented normalization, alias resolution, version history, singular attachment preference, and additive read metadata;
- make old HTTP/MCP read tools and pentestctl compatibility reads call BlackboardReadService;
- run golden fixtures through HTTP, MCP, --api, and --db modes.

**Exit gate:**

- no compatibility read queries frozen legacy tables;
- multi-target Evidence appears once with deterministic legacy target plus complete attachments;
- report compatibility delegates to PentestReportV1 and honors current or Task Scope;
- Go, TypeScript, HTTP, CLI, and OpenAPI fixtures agree on names, nullability, enums, and ordering.

**Coverage:** A20-A21, A25.

### U06 — Bundled UI and bookmark-compatible cutover behavior

**First red test:** TestBundledUIRendersGraphBackedFocusedViewsWithoutLegacyFallbackRequests.

**Minimal green path:**

- make the Project Dashboard and Blackboard pages consume shared canonical projections;
- make the legacy Facts route redirect to or render Blackboard Work filtered to ProjectFact;
- retain Finding, Evidence, Report, version, relation, and merge bookmarks as focused graph-backed views;
- add Entity, provenance, Health, Frontier, and Graph Explorer surfaces;
- show CTF Solution routes only for explicit CTF Projects;
- keep activation behind the store epoch until M05.

**Exit gate:**

- Web tests fail if a page calls a frozen-table or page-specific legacy fallback route;
- dense ledger/table views remain keyboard- and screen-reader-usable;
- Graph Explorer is secondary and its data matches the ledger;
- bundled UI and daemon can activate graph mode together in one release.

**Coverage:** A18-A22, A27.

## 10. Migration, cutover, and retirement slices

### M01 — Deterministic inspection and verified backup

**First red test:** TestInspectIsDeterministicAndWritesNeitherDatabaseNorFilesystem.

**Minimal green path:**

- add BlackboardMigrationService.Execute with inspect and backup orchestration;
- compute source counts, blockers, warnings, deterministic source digest, and estimated mappings;
- detect unknown/checksum-mismatched schema, SQLite integrity failures, active Continuations, impossible histories, invalid aliases, escaping Evidence paths, and unsupported confirmation blockers;
- create a WAL-consistent SQLite backup with independent quick_check, owner-only permissions, and SHA-256 verification.

**Exit gate:**

- SQL row order does not change the plan or digest;
- any source-row change changes the source digest;
- backup failure leaves source rows and store epoch unchanged;
- diagnostics redact bodies, proof, secrets, tokens, and unmanaged paths.

**Coverage:** A23.

### M02 — Import Projects, Tasks, Facts, aliases, merges, and relations

**First red test:** TestLegacyFactCorpusImportsDeterministicallyWithExactGoalAndCompatibilityParity.

**Minimal green path:**

- build a sealed LegacyImportPlanV1 and call Apply through a transaction-bound migration context;
- backfill every legacy Project to immutable pentest without CTF inference;
- project exact Task Goals and preserve Task/Continuation/Event/Summary incompleteness rather than guessing;
- retain conforming keys/IDs, deterministically normalize nonconforming keys, and block collisions;
- import Fact heads/history, import-only confirmation exceptions, aliases, merges, supported relations, and bounded audit-only relation mappings.

**Exit gate:**

- current rows without history gain version 1 and current/history drift gains one final version;
- unreconstructable non-final gaps block cutover;
- alias chains flatten while dangling/cyclic aliases stay audit-only;
- legacy depends_on never affects Objective Frontier;
- duplicates never auto-merge without provable identity;
- mapping digest is deterministic.

**Coverage:** A03, A09, A13, A24.

### M03 — Import Findings, Evidence, attachments, and parity gates

**First red test:** TestLegacyFindingEvidenceCorpusPreservesHistoryAttachmentsAndMissingArtifactsWithoutSyntheticCounts.

**Minimal green path:**

- import Finding heads/history, false-positive terminal state, CVSS derivation, aliases, and merges;
- enforce blocker versus import-only-warning rules for confirmed Findings;
- normalize Evidence types, confined paths, actual digests, missing files, path escapes, and attachment preferences;
- create evidences edges only for resolvable targets and keep dangling preference metadata bounded;
- compute mapping/source digests and run graph/read/Dashboard/Task Summary/report parity before cutover.

**Exit gate:**

- no synthetic Fact, Finding, or Evidence changes legacy counts;
- digest drift records actual digest and reports mismatch;
- escaping paths are never opened;
- every positive legacy read golden matches U05;
- imported unsupported confirmations remain visible through Health warnings while new unsupported confirmations still fail.

**Coverage:** A09, A13, A15, A21, A25.

### M04 — Compatibility writes, Finish translation, and deprecation parity

**First red test:** TestEquivalentLegacyHTTPMCPAndCLIWritesTranslateToOneGraphMutation.

**Minimal green path:**

- implement BlackboardCompatibilityService.Call;
- translate legacy Fact, Finding, relation, merge, Evidence, report, and Task Summary operations to GraphService, project-interface, read, and Task modules;
- observe an omitted expected version once and never silently retry conflicts;
- support additive exact idempotency while documenting best-effort behavior without a key;
- translate Runtime submit_task_summary to Finish and keep operator Task Summary compatibility separate;
- add HTTP headers, MCP metadata, CLI stderr warnings, and local use counters.

**Exit gate:**

- compatibility behavior is tested under a disposable graph_v1 epoch only; legacy_v1 production still uses the old canonical service;
- non-representable relations return stable honest errors;
- Runtime Evidence requires a matching Attempt;
- warnings never corrupt JSON payloads;
- graph-native and compatibility operations pass the same graph-service conformance tests.

**Coverage:** A07, A14-A16, A26.

### M05 — Atomic cutover, activation, verification, and recovery

**First red test:** TestCutoverCommitsImportParityEpochFlipAndLegacyWriteGuardsAtomically.

**Minimal green path:**

- require blocker-free fresh inspection, no active Continuation, and a verified backup;
- run DDL, all Project imports, mappings, head builds, parity gates, guards, and the graph_v1 epoch flip in one BEGIN IMMEDIATE transaction;
- activate graph-native Runtime protocol, shared reads, compatibility adapters, reports, dashboard, and bundled UI only after commit;
- freeze legacy Blackboard tables with INSERT/UPDATE/DELETE guards;
- add post-commit verify, recovery_required state, idempotent cutover retry, and explicit backup-restore guidance.

**Exit gate:**

- failure at every named phase rolls back to unchanged legacy_v1;
- process death before commit reopens legacy-v1 and after commit reopens graph-v1;
- old binaries and direct stale legacy writes fail closed;
- no active legacy Continuation crosses cutover;
- new Continuations receive only full graph-native context, with no Fact Index, progress-body, or Finding reinjection;
- post-cutover verification detects state, projection, mapping, guard, and parity corruption;
- rollback guidance names any post-cutover writes that a backup restore would lose and never performs an implicit reverse migration.

**Coverage:** A07, A11, A13, A22, A27.

### M06 — Release C compatibility-write retirement

**First red test:** TestCompatibilityWritesReturnStable410OnlyAfterEveryRetirementGatePasses.

**Minimal green path:**

- evaluate stable-release age, bundled Runtime adoption, active/pre-cutover Continuations, local compatibility-write counters, Health/migration verification, and documentation gates;
- remove or return 410 compatibility_removed for legacy Fact, Finding, relation, Evidence, and Runtime-summary writes;
- leave canonical graph-native writes and Finish unchanged;
- retain compatibility reads and browser redirects for the additional stable-release window.

**Exit gate:**

- any unmet gate keeps compatibility writes available and deprecated;
- explicit operator waiver is recorded when the observation period is bypassed;
- no direct legacy table write has been possible since M05;
- removal errors point to stable replacement operations.

**Coverage:** A28.

### M07 — Release D compatibility-read retirement and explicit finalization

**First red test:** TestFinalizeLegacyDropsOnlyFrozenBlackboardTablesAfterFreshVerifyAndExplicitCutoverID.

**Minimal green path:**

- gate removal of legacy MCP tools, CLI aliases, and HTTP read routes on bundled-client adoption and read-use counters;
- retain cheap unambiguous browser redirects;
- require explicit finalize_legacy with cutover ID, fresh verify, disabled compatibility, migration-summary export, mapping digest, and backup acknowledgement;
- run one numbered transaction that drops only frozen legacy Blackboard tables and guards;
- retain Task, Continuation, Event, Summary, Scope, Artifact, graph ledger, Health, and migration mapping data.

**Exit gate:**

- finalization never runs automatically at startup or because callers stopped using old routes;
- a missing backup acknowledgement or stale verify blocks finalization;
- graph and compatibility-history audit output remains reproducible;
- the old canonical write implementation and now-redundant implementation-coupled tests are removed.

**Coverage:** A28.

## 11. Release and merge strategy

### 11.1 Slice discipline

Each slice is one reviewable change set and follows:

1. add one failing behavioral test at the owning seam;
2. add the smallest production behavior that passes it;
3. repeat one scenario at a time until the slice's assigned matrix cases pass;
4. run the affected package and adapter suites;
5. review for interface depth and remove only superseded shallow tests;
6. merge without speculative behavior assigned to a later slice.

Refactoring is a review activity after the red-green behavior is complete, not an extra step inside each loop.

### 11.2 Release A — prepare behind legacy_v1

Release A contains:

- C01-C10;
- I01-I06;
- U01-U06 code and tests, with graph activation controlled by the store epoch;
- M01-M04.

Legacy tables remain canonical. Production routes do not call graph writes, and no dual-write comparison period exists. Migration fixtures and disposable graph_v1 test databases are the only places where compatibility adapters write graph before cutover.

### 11.3 Release B — cut over

M05 is the Release B activation:

- offline blocker-free migration;
- verified backup;
- one atomic store flip;
- graph-native Runtime context for every new Continuation;
- bundled UI/daemon switch;
- compatibility adapters over graph;
- frozen legacy writes.

Release B does not delete compatibility or legacy tables.

### 11.4 Releases C and D — retire deliberately

M06 and M07 are separate stable-release changes. They cannot be collapsed into M05:

- Release C retires compatibility writes after evidence of replacement adoption.
- Release D retires compatibility reads and may explicitly finalize frozen tables.

## 12. Verification gates

An implementation map is not complete until its own slice tests pass. The full refactor is not ready for cutover until all of these are green:

1. every upstream case G1-G19, S1-S46, P1-P28, R1-R57, and M1-M62 is traceable to a passing test;
2. the exact CanonicalMainGraphV1 golden bytes, hashes, and token estimates pass on reopen;
3. the transport conformance corpus passes through HTTP, MCP, task CLI, and operator CLI;
4. the compatibility corpus passes through legacy HTTP, MCP, CLI, Dashboard, report, and UI views;
5. file-backed concurrent-writer, crash, WAL, backup, restore, and migration failure-point suites pass;
6. every Health detector has positive and negative fixtures;
7. the budget-boundary corpus proves full delivery at and above 20K;
8. attempt_outcomes proves succeeded, failed, blocked, inconclusive, and interrupted semantics;
9. concurrent_tasks proves Task and Continuation provenance isolation;
10. the CTF fixture proves solved-state reversal and flag-value disclosure rules;
11. Web tests prove no frozen-table fallback request exists;
12. a static or integration guard proves graph_v1 public writes cannot execute INSERT, UPDATE, or DELETE against frozen legacy Blackboard tables;
13. go test ./... passes;
14. the Web test, lint, and production build suites pass;
15. migration verification and Blackboard Health contain no critical integrity result.

Cutover additionally requires the live operator gates from the migration contract; a green test suite does not waive active-Continuation, backup, source-digest, or compatibility-retirement requirements.

## 13. Rejected sequencing alternatives

### 13.1 One mega implementation issue or pull request

Rejected because it would mix graph semantics, crash-safe storage, Runtime credentials, read models, UI, and irreversible migration review in one failure domain. The four-map split preserves shared seams while making progress claimable and reviewable.

### 13.2 Write the complete 212-case suite before implementation

Rejected as horizontal slicing. It would encode imagined implementation structure and delay the first executable capability. Each slice grows the normative matrix one behavior at a time.

### 13.3 Build tables, then repositories, then services, then adapters

Rejected because callers would see no behavior until the end and tests would target internals. C02 establishes an end-to-end semantic round-trip; every later storage change remains observable through the deep seam.

### 13.4 Dual-write legacy and graph for confidence

Rejected because two canonical stores can disagree and rollback becomes ambiguous. Confidence comes from deterministic import, parity fixtures, one transactional flip, frozen-table guards, and post-cutover verification.

### 13.5 Cut over before read/UI and compatibility parity

Rejected because it turns Release B into an emergency adapter rewrite. U05-U06 and M04 must be green before M05 can activate graph_v1.

### 13.6 Keep a hidden UI fallback to legacy tables

Rejected because it perpetuates a second semantic read path and hides parity defects. The UI uses BlackboardReadService or fails visibly.

### 13.7 Finalize legacy tables during cutover

Rejected because it destroys the bounded rollback source before graph behavior has operated in a stable release. Finalization belongs only to M07.

## 14. Downstream handoff

The final assembly ticket should:

- include this matrix and slice DAG as the canonical implementation order;
- preserve all five upstream contracts as normative references rather than duplicating their detailed schemas;
- confirm the recommendation to create four execution maps with native dependency edges;
- create implementation tickets from C01-C10, I01-I06, U01-U06, and M01-M07;
- keep M05 blocked by C10, I06, U06, and M04;
- keep M06 and M07 outside the initial cutover release;
- state explicitly that implementation begins after the Wayfinder planning map closes.

The previously foggy execution-map question is now sharp: use four maps split by graph/storage, Runtime protocol, read/UI, and migration/cutover blast radius. No additional planning ticket is required before final assembly.
