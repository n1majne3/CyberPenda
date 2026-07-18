# Blackboard v2 TDD Replacement Plan

- **Status:** accepted; ticket publication and implementation may proceed
- **Parent spec:** [#97 — Replace Blackboard v1 with compact semantic Blackboard v2](https://github.com/n1majne3/CyberPenda/issues/97)
- **Normative behavior:** [Blackboard v2 Specification](./blackboard-v2-spec.md)
- **Delivery style:** vertical Red-Green-Refactor tickets
- **Replacement decision:** ADR 0013

This is the single implementation-order document for replacing Blackboard v1. It does not redefine behavior from the v2 specification. Each ticket begins with a failing behavioral test at a public or durable seam, implements only enough v2 behavior to pass, then removes the v1 code and tests displaced by that ticket.

## 1. Delivery rules

1. Preserve unrelated user work. In particular, do not overwrite the current uncommitted Runner trusted-MCP permission projection; adapt it when the v2 tool catalog exists.
2. Write a failing v2 test before deleting the behavior it replaces.
3. Keep at most one production read/write path active at a time. Temporary code coexistence inside a development branch is allowed; dual Runtime protocols, dual database writes, and compatibility fallbacks are not a released state.
4. Keep the repository buildable after every ticket.
5. Test through public seams first: semantic service, Project Interface, launch projection, HTTP/MCP/CLI, migration command, and UI API boundary.
6. Use exact deterministic fixtures for snapshots and semantic responses. Do not assert private SQL columns or internal IDs unless the test is specifically a storage recovery test.
7. Remove v1 tests when their behavior is deliberately retired; do not rewrite them to bless v2 accidentally.
8. No ticket is complete while it leaves a caller on a retired v1 DTO, tool, route, store epoch, or UI projection.

## 2. Starting point

The implemented graph-v1 change added roughly 56.7K lines across 151 files. The inspected Blackboard-related Go packages currently report 613 passing and 3 failing tests; all three failures are in the v1 compatibility-retirement suite that this replacement removes. Before implementation begins, record a complete baseline and distinguish pre-existing failures from each ticket's red test.

Five files already contain user changes and must be preserved or deliberately ported:

- `internal/blackboard/graph_types.go`: v1 MCP schema descriptions; behavior becomes obsolete, intent moves into v2 schemas.
- `internal/mcpserver/server_test.go`: v1 tool-schema regression; replace with a v2 tool-schema regression.
- `internal/runner/mcp.go`: reusable Claude trusted-MCP allowlist mechanism; preserve.
- `internal/runner/projection.go`: reusable Claude settings projection; preserve.
- `internal/runner/projection_mcp_test.go`: preserve intent and update expected v2 tool names.

## 3. Stable seams to retain

The replacement keeps these product boundaries while changing their Blackboard contracts:

- Project ownership and Project kind;
- Task, Task Goal, Task Conversation, and Runtime Continuation lifecycle;
- Scope and Scope Snapshot projection;
- Continuation Interface Grant and server-owned authority;
- Runtime layout and `.pentest` projection;
- Artifact Root, Evidence file confinement, hashing, and retention;
- report delivery and CTF solved-state consumers;
- HTTP daemon, trusted MCP server, CLI, and Web application shells;
- Runner-specific trusted-MCP configuration, including Claude allowed tools.

## 4. Acceptance groups

| ID | Required behavior |
| --- | --- |
| V2-01 | Project-wide Blackboard Keys, Project isolation, optimistic semantic versions, monotonic revision, idempotent no-op/replay |
| V2-02 | Seven closed record schemas, lifecycle guards, Current Work/Project Knowledge membership, no removed record types |
| V2-03 | Eleven relationship endpoint rules, direction, versions, self-link rejection, cycle rules, merge, redirect, supersession |
| V2-04 | Exact deterministic `runtime-blackboard/v2`, field allowlists, ordering, omission rules, byte limits, attention thresholds |
| V2-05 | Current detail and Semantic History separation; no Fact Index, provenance, IDs, or storage DTO leakage |
| V2-06 | Atomic `semantic-change-batch/v2`, same-batch key references, stable errors, conflicts, replay, semantic delta response |
| V2-07 | Launch Pin, Working Snapshot, compact startup header/checklist, resume, long-context reread behavior |
| V2-08 | Coalesced parallel-Task notice and next trusted-response full-snapshot piggyback |
| V2-09 | Attempt/Evidence/checkpoint/finish reconciliation without Task Summary or copied Goal records |
| V2-10 | Ordinary UI, graph, health, report, and CTF consumers use the same semantic model without audit surfaces |
| V2-11 | Backup-first atomic v1-to-v2 rebuild, mapping, validation, rollback-before-cutover, and no long-lived compatibility path |
| V2-12 | V1 code, tools, routes, tests, UI, schemas, and store epoch are fully retired while retained product seams stay green |

## 5. Published ticket DAG

The published GitHub Issues below are the normative implementation units and ordering. The former coarse Slice 0–11 plan is retired and must not be used as an implementation queue. A ticket may start only after every listed blocker is closed. Each ticket must add its red test first, leave the relevant acceptance behavior green, and remove the v1 behavior it displaces before closing.

| Ticket | Blocked by | Required deliverable |
| --- | --- | --- |
| [T00 #98 — Freeze v2 wire contract and conformance harness](https://github.com/n1majne3/CyberPenda/issues/98) | — | Machine-readable closed schemas and golden fixtures for Snapshot, records, relationships, Semantic Change Batch, and stable errors. |
| [T01 #99 — Make Store bootstrap and epochs v2-safe](https://github.com/n1majne3/CyberPenda/issues/99) | T00 | Fresh databases bootstrap directly into v2; a v1 database can be opened only by the offline migrator. |
| [T02 #100 — Create and update a Project Fact](https://github.com/n1majne3/CyberPenda/issues/100) | T00, T01 | First complete store → service → detail/history → Snapshot path, including key isolation, versions, revisions, no-op, replay, and conflict behavior. |
| [T03 #102 — Model Entity topology](https://github.com/n1majne3/CyberPenda/issues/102) | T02 | Entity lifecycle plus `about` and acyclic `part_of` topology through the same semantic path. |
| [T04 #103 — Run Objectives and Attempts](https://github.com/n1majne3/CyberPenda/issues/103) | T02, T03 | Current Work membership, Objective dependencies, Attempt lifecycle, `tests`, and `produced`. |
| [T05 #104 — Retain Evidence and confirm a Fact](https://github.com/n1majne3/CyberPenda/issues/104) | T04 | Confined Evidence retention, semantic Evidence records, proof relationships, and guarded Fact confirmation. |
| [T06 #105 — Confirm a Finding and render Report](https://github.com/n1majne3/CyberPenda/issues/105) | T05 | Finding confirmation, severity/CVSS semantics, evidence chain, and Pentest report output. |
| [T07 #106 — Verify CTF Solution and solved state](https://github.com/n1majne3/CyberPenda/issues/106) | T05 | Solution verification and CTF solved state without Blackboard Goal records. |
| [T08 #107 — Merge Project Knowledge](https://github.com/n1majne3/CyberPenda/issues/107) | T03, T05–T07 | Atomic Record Merge, relationship rewiring, and Blackboard Key Redirect across all Project Knowledge types. |
| [T09 #108 — Complete relation grammar and supersession](https://github.com/n1majne3/CyberPenda/issues/108) | T04–T08 | All eleven relationship endpoint rules, versions, reasons, self/cycle guards, history, and atomic same-type supersession. |
| [T10 #109 — Deliver complete deterministic Snapshot](https://github.com/n1majne3/CyberPenda/issues/109) | T09 | Exact canonical `runtime-blackboard/v2` bytes, semantic detail/history separation, and 16K/32K/64K attention behavior without truncation. |
| [T11 #110 — Pin and project v2 for Codex](https://github.com/n1majne3/CyberPenda/issues/110) | T10 | Immutable Launch Pin, Agent-visible Working Snapshot, compact startup header/checklist, and reread/resume behavior. |
| [T12 #111 — Checkpoint and reconcile interruption](https://github.com/n1majne3/CyberPenda/issues/111) | T05, T11 | Attempt checkpoints and server-owned interruption reconciliation against the Working Snapshot. |
| [T13 #112 — Finish and resume without handoff copies](https://github.com/n1majne3/CyberPenda/issues/112) | T09, T11, T12 | Finish and resume from Task Goal, Scope, current semantic state, checkpoints, and Harness Steering without duplicate handoff state. |
| [T14 #115 — Expose v2 through HTTP](https://github.com/n1majne3/CyberPenda/issues/115) | T10, T13 | Authenticated `/api/v2` semantic reads/writes with stable status mapping, ETags, and pagination where applicable. |
| [T15 #114 — Expose v2 through trusted MCP](https://github.com/n1majne3/CyberPenda/issues/114) | T10, T13 | Six closed trusted tools backed by the semantic service, with compact v2 schemas and no caller-supplied authority. |
| [T16 #113 — Expose v2 through CLI Fallback](https://github.com/n1majne3/CyberPenda/issues/113) | T10, T13 | CLI fallback with semantic-service parity and no transport-specific semantic behavior. |
| [T17 #116 — Project v2 for Claude and Pi](https://github.com/n1majne3/CyberPenda/issues/116) | T11, T15 | Claude and Pi projection parity while preserving the existing Claude trusted-tool auto-authorization mechanism. |
| [T18 #117 — Synchronize parallel Tasks](https://github.com/n1majne3/CyberPenda/issues/117) | T11–T16 | Project-isolated coalesced revision notice, next-trusted-response full-Snapshot piggyback with reason, atomic replacement, and acknowledgement. |
| [T19 #118 — Rebuild ordinary Blackboard UI](https://github.com/n1majne3/CyberPenda/issues/118) | T10, T14 | One Work/Knowledge semantic UI, key-based Graph Explorer, current detail, and explicit Semantic History. |
| [T20 #119 — Surface semantic health](https://github.com/n1majne3/CyberPenda/issues/119) | T19 | Semantic health for stranded work, missing Evidence, and attention thresholds without audit-derived diagnostics. |
| [T21 #120 — Migrate Finding/Report/Solution consumers](https://github.com/n1majne3/CyberPenda/issues/120) | T06, T07, T14, T19 | Remaining user pages, reports, and CTF consumers read the same v2 DTOs and lifecycle semantics. |
| [T22 #101 — Inspect and back up v1 database](https://github.com/n1majne3/CyberPenda/issues/101) | T00, T01 | Read-only inspection, deterministic classification plan, verified offline backup, and refusal to mutate the source database. |
| [T23 #121 — Rebuild unambiguous v1 heads](https://github.com/n1majne3/CyberPenda/issues/121) | T09, T10, T22 | Unambiguous current v1 heads rebuilt into fresh v2 semantic state and validated by deterministic Snapshot. |
| [T24 #122 — Map removed types and relationships](https://github.com/n1majne3/CyberPenda/issues/122) | T04, T23 | Explicit mapping/classification for Goal, Observation, Hypothesis, Directive, `blocks`, `leads_to`, and ambiguous records. |
| [T25 #123 — Cut over atomically](https://github.com/n1majne3/CyberPenda/issues/123) | T24 | Backup-first validation, rollback before the switch, atomic epoch cutover, and successful `blackboard_v2` reopen. |
| [T26 #125 — Remove public v1 interfaces](https://github.com/n1majne3/CyberPenda/issues/125) | T14–T19, T21, T25 | Old HTTP/MCP/CLI routes, compatibility translation, schemas, tool catalog, and public v1 DTOs removed. |
| [T27 #124 — Remove duplicate workflow state](https://github.com/n1majne3/CyberPenda/issues/124) | T13, T24, T25 | Goal, Task Summary, Objective Outcome, Mechanical Handoff, and their retained consumers removed. |
| [T28 #126 — Remove v1 read/audit projections](https://github.com/n1majne3/CyberPenda/issues/126) | T20, T21, T25, T26 | Provenance, Recent Changes, graph hash/integrity, audit health, and compatibility read projections removed. |
| [T29 #127 — Remove graph ledger and v1 epoch](https://github.com/n1majne3/CyberPenda/issues/127) | T18, T25–T28 | Replay/hash/compaction machinery, runtime v1 tables, old epoch, and obsolete dependencies removed, subject only to the isolated migration-decoder exception below. |
| [T30 #128 — Prove replacement complete](https://github.com/n1majne3/CyberPenda/issues/128) | T29 | Full Go/Web/build/migration/search/review gates prove one Blackboard model, one Runtime Snapshot, one write protocol, and no released compatibility path. |

### 5.1 Safe parallel lanes

Blocking edges above remain authoritative. The following frontiers are intentionally safe to run concurrently when agents have disjoint file ownership:

- After T01: T02 can build the v2 semantic path while T22 performs read-only v1 inspection and backup work.
- After T05: T06 can deliver the Pentest Finding/report path while T07 delivers the CTF Solution path.
- After T10 and T13: T14, T15, and T16 can build the HTTP, MCP, and CLI adapters independently against the frozen semantic service.
- After their blockers close: T17, T19, and T23 can progress as separate Runtime-adapter, Web, and offline-migration lanes.

Do not infer additional concurrency merely because two tickets are open. The issue DAG decides semantic ordering; explicit file ownership decides whether implementation can actually overlap.

### 5.2 Single-owner chokepoints

At any moment, assign one owner to each shared integration chokepoint:

- the Blackboard semantic kernel, canonical DTO/schema definitions, store epoch, and migration state;
- Task/Continuation lifecycle plus Project Interface checkpoint/finish integration;
- daemon registration, generated API contracts, and the shared trusted-tool catalog;
- Web DTO generation and embedded-asset synchronization.

Parallel ticket owners may build isolated adapters and fixtures, but changes to a chokepoint are integrated serially by its owner. No two agents edit the same file concurrently.

### 5.3 Isolated v1-source retirement exception

Production v2 code must not import or call v1 runtime packages. The sole retirement exception is a read-only, package-isolated v1 decoder used by the offline migration command to decode the verified source backup into migration input. It must not be reachable from normal Store bootstrap, daemon, Runtime, HTTP, MCP, CLI, or Web paths; it must not provide dual-read, dual-write, or compatibility fallback behavior.

T29 may retain that decoder only as isolated migration-source support after deleting the v1 epoch and graph ledger. T30 must use a narrow repository-search allowlist for its exact package/path and fail on every other production v1 reference.

## 6. Final verification gates

The replacement is complete only when all are true:

- all v2 acceptance groups pass at semantic-service and relevant public seams;
- full Go test suite passes with no accepted v1 compatibility failures;
- Web unit tests, production build, and embedded-asset synchronization pass;
- deterministic Snapshot golden tests pass across repeated runs;
- cross-Project and concurrent-Task tests pass under the race-capable test configuration used by the repository;
- migration backup, failure rollback, cutover, and post-cutover reopen tests pass on representative v1 fixtures;
- repository search finds no production v1 protocol/store/type references;
- the current five user-authored changes are either preserved unchanged or explicitly ported with equivalent v2 tests;
- the seven v1 specifications remain historical and no implementation entry point links to them as normative.

### 6.1 T30 executable proof matrix

T30 (#128) closes only after these commands pass together from one worktree state:

| Gate | Command |
| --- | --- |
| Full Go suites | `go test ./...` |
| Race-capable synchronization and lifecycle suites | `go test -race ./internal/blackboardv2 ./internal/daemon ./internal/store ./internal/task -run 'Test(Concurrent|.*Race|.*Parallel|.*Synchronization|.*Atomic)' -count=1` |
| Migration inspect/backup/failure/cutover/reopen fixtures | `go test ./internal/blackboardmigration ./internal/pentestctl ./internal/store -run 'Test.*(Migration|Migrate|Backup|Cutover|Reopen|Rollback|Source)' -count=1` |
| Deterministic Snapshot, service, transport, launch/resume, synchronization, UI/health/report, and CTF groups | `go test ./internal/blackboardv2contract ./internal/blackboardv2 ./internal/daemon ./internal/mcpserver ./internal/pentestctl ./internal/runner ./internal/report` |
| Web unit suite | `cd web && npm test` |
| Production Web build | `cd web && npm run build` |
| Embedded asset parity | `make check-ui-sync` |

The repository retirement test owns the narrow source allowlist: the offline migration package and exact historical migration SQL in `internal/store/store.go`. Normal daemon, Runtime, HTTP, MCP, CLI semantic commands, and Web production paths may not import or advertise a v1 schema, tool, route, epoch, type, relationship, Task Summary, or audit projection.

The five pre-existing user changes map to v2 proof as follows:

| Original file | Preserved v2 intent and regression |
| --- | --- |
| `internal/blackboard/graph_types.go` | Closed objective/Attempt create guidance is ported to `TestBlackboardChangeMCPSchemaAdvertisesObjectiveAndAttemptCreateEnvelope`; the obsolete v1 source is deleted. |
| `internal/mcpserver/server_test.go` | The v1 schema test is replaced in place by `TestBlackboardChangeMCPSchemaAdvertisesObjectiveAndAttemptCreateEnvelope`. |
| `internal/runner/mcp.go` | The reusable trusted-MCP allowlist mechanism remains, with v2 names covered by `TestClaudeV2RuntimeConfigPreservesTrustedMCPAllowlistWithoutIdentityContext`. |
| `internal/runner/projection.go` | Claude settings projection remains the shared production path exercised by the same allowlist regression. |
| `internal/runner/projection_mcp_test.go` | Its behavioral intent moved to `internal/runner/blackboard_v2_projection_test.go`, including exact six-tool names and reserved-server isolation. |
