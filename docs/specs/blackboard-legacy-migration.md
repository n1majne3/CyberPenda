# Blackboard Legacy Migration and Compatibility Cutover Contract

- **Status:** implementation contract for [Specify legacy Blackboard migration and compatibility cutover](https://github.com/n1majne3/CyberPenda/issues/60)
- **Map:** [Map: Refactor Blackboard into bounded graph memory](https://github.com/n1majne3/CyberPenda/issues/55)
- **Graph contract:** [Blackboard Typed Property Graph Contract](./blackboard-graph-contract.md)
- **Storage contract:** [Blackboard SQLite Persistence, History, Compaction, and Health Contract](./blackboard-graph-storage.md)
- **Runtime contract:** [Blackboard Runtime and Project Interface Protocol Contract](./blackboard-runtime-protocol.md)
- **Read contract:** [Blackboard Read Projections, Reports, and Operator UI Contract](./blackboard-read-projections.md)
- **Migration contract version:** `legacy_blackboard_to_graph_v1`
- **Canonical store after cutover:** `graph_v1`
- **Compatibility family:** `legacy_blackboard_v1`

This document fixes how CyberPenda replaces the current Fact-centric Blackboard with the graph-first Blackboard without losing existing Project data, introducing a long-lived dual-write design, or leaving different transports with different semantics. It defines inspection, backup, transactional import, normalization, compatibility translation, rollback, release sequencing, deprecation, and final removal.

The keywords **MUST**, **MUST NOT**, **SHOULD**, and **MAY** are normative.

## 1. Decision

Cutover is an offline, backup-first, single-transaction migration of one SQLite database:

1. inspect the legacy database without modifying it;
2. stop Task launch and require that no Runtime Continuation is active;
3. create and verify a SQLite backup;
4. import every Project into the graph ledger inside one `BEGIN IMMEDIATE` transaction;
5. compare graph-backed compatibility projections with the legacy source inside that transaction;
6. flip one store-epoch marker to `graph_v1`, freeze legacy tables, and commit;
7. serve every canonical and compatibility read/write from the graph, project-interface, read, Task Summary, and report modules.

There is no dual-write interval. Before the store-epoch flip, legacy tables are canonical and graph data is not served. After the flip, the graph ledger is canonical and legacy Blackboard tables are never read for current semantic state and never written.

The legacy Blackboard tables MAY remain physically present for a bounded rollback window, but they are frozen source material, not a second store. They are `project_facts`, `project_fact_versions`, `project_fact_relations`, `fact_key_aliases`, `findings`, `finding_versions`, `finding_key_aliases`, and `evidence_artifacts`. Compatibility adapters read the graph and migration manifest, not those frozen tables.

Task lifecycle, Task Events, Runtime Configuration Versions, Continuations, Scope Snapshots, Task Summary Versions, artifact payload bytes, and Reports remain outside the graph as fixed upstream. This migration adds the fields those domains need, preserves their rows, and changes their consumers without pretending they are graph nodes.

## 2. Deep modules and seams

### 2.1 Migration module

Startup and operator CLI call one migration module:

```go
type BlackboardMigrationService interface {
    Execute(ctx context.Context, request MigrationRequest) (MigrationResult, error)
}
```

`MigrationRequest` is a closed union with these kinds:

- `inspect` — read-only plan and diagnostics;
- `cutover` — backup, import, parity verification, and atomic store flip;
- `verify` — post-cutover integrity and compatibility verification;
- `finalize_legacy` — explicitly remove frozen legacy tables after the removal gates pass.

The module owns source discovery, normalization, backup orchestration, source/mapping digests, migration state, transaction lifetime, graph import, parity checks, recovery instructions, and finalization. Startup, HTTP, MCP, CLI, Web UI, report, and graph modules MUST NOT reimplement those rules.

### 2.2 Graph import remains at Apply

The migration module does not write graph ledger or materialized tables directly. It builds a sealed `LegacyImportPlanV1` and invokes `BlackboardGraphService.Apply` with trusted migration context.

The migration coordinator may bind the already-open cutover transaction into the sealed execution context. In that mode, `Apply` uses the supplied transaction-scoped executor and does not begin or commit a nested transaction. This is an internal migration capability; it is not serializable, is not available to HTTP/MCP/ordinary CLI callers, and does not create a second public write seam.

`LegacyImportPlanV1` is not a new public MutationBatch operation. The graph module expands it into the same validated immutable identities, full versions, edges, key events, provenance, operations, heads, hashes, and result bytes used by ordinary Apply. Final current state MUST satisfy the graph schema plus only the explicit migration exceptions in this contract.

### 2.3 Compatibility module

Legacy writes cross one translation module:

```go
type BlackboardCompatibilityService interface {
    Call(ctx context.Context, request LegacyCall) (LegacyResult, error)
}
```

`LegacyCall` is a closed union for the legacy HTTP, MCP, and `pentestctl` operations named in section 12. The module owns field defaults, alias resolution, expected-version lookup, idempotency adaptation, graph operation construction, response translation, deprecation metadata, and stable errors.

Legacy reads are projection kinds inside `BlackboardReadService`. Task Summary reads/writes remain in the Task domain. Report compatibility delegates to `PentestReportV1`. No transport queries SQLite tables or translates data independently.

## 3. Migration control state

The preparation release introduces numbered, checksummed schema migrations before graph cutover. It also introduces these non-semantic control structures.

### 3.1 `blackboard_store_state`

One singleton row records:

- `canonical_store`: `legacy_v1`, `graph_v1`, or `graph_v1_finalized`;
- `cutover_state`: `legacy`, `inspecting`, `migration_required`, `graph`, `recovery_required`, or `finalized`;
- migration contract and graph schema versions;
- cutover ID;
- source digest and mapping digest;
- verified backup path and SHA-256;
- cutover application version and timestamps;
- whether any post-cutover graph write has committed;
- latest successful verification time and result hash.

The store epoch is global to one SQLite database. Mixed legacy-canonical and graph-canonical Projects in the same database are forbidden.

### 3.2 `blackboard_migration_runs`

This operational table records inspect, cutover, verify, and finalize attempts with state, stable diagnostic codes, counts, source/mapping digests, backup metadata, and timestamps. It MUST NOT contain secret values, artifact payload bytes, full Task output, or bearer tokens.

A failed transaction may leave an operational run marked failed, but it MUST leave `canonical_store=legacy_v1` and all legacy semantic rows unchanged.

### 3.3 `blackboard_legacy_mappings`

This migration/audit index maps each legacy source record to imported state. It contains:

- Project ID;
- source table/kind and legacy primary ID;
- original stable key and optional original version;
- a hash of the canonicalized source row;
- target kind, immutable ID, and optional graph record version;
- mapping status, such as `imported`, `normalized`, `legacy_noop_version`, `legacy_transition_exception`, `alias`, `audit_only_relation`, or `unresolvable_legacy_alias`;
- small compatibility metadata required by the read contract, including original Evidence attachment preference and audit-only relation fields;
- migration mutation sequence and cutover ID.

It MUST NOT duplicate bodies, proof text, report content, or artifact bytes that already exist in canonical graph or Task-domain records. Compatibility metadata is bounded and schema-validated.

The sorted mapping rows have a deterministic `mapping_digest` stored in the migration mutation metadata and `blackboard_store_state`. Verification recomputes the digest. The mapping table is not semantic Current Truth and does not participate in the graph state hash.

## 4. Read-only inspection

`inspect` performs no database or filesystem writes. It opens a consistent SQLite read snapshot and produces a deterministic `LegacyMigrationPlanV1` containing source digest, row counts, estimated graph records, mapping decisions, warnings, blockers, and backup requirements.

Inspection covers:

1. SQLite `quick_check`, foreign-key state, schema shape, schema-migration checksums, and unsupported newer versions;
2. Projects, Tasks, Runtime Configuration Versions, Continuations, Task Events, and Task Summary Versions;
3. current Project Facts, Fact Versions, Fact Relations, Fact aliases, Findings, Finding Versions, Finding aliases, Evidence rows, and managed-artifact observations;
4. live-key and alias graphs, chains, cycles, shadows, dangling targets, and source histories;
5. stable-key grammar and immutable-ID collisions across node types and Projects;
6. version continuity, current-row/version-row drift, redundant versions, and legacy-invalid lifecycle transitions;
7. Fact confidence/category/scope values and Finding status/CVSS completeness;
8. relation vocabulary and graph endpoint representability;
9. Evidence managed-path confinement, file existence, digest drift, and target resolution;
10. active Runtime Continuations and any other process owning the database;
11. expected compatibility projection counts and hashes.

### 4.1 Blocking diagnostics

Cutover MUST NOT begin while any of these are present:

- SQLite integrity failure or unknown/checksum-mismatched schema migration;
- another daemon owner or any pending, running, or paused Continuation;
- a current Project Fact missing key or summary, or with an unknown confidence value;
- a current Finding missing key/title, using an unknown status, or marked confirmed without complete report fields and a valid CVSS 4.0/3.1 vector after deterministic version inference;
- a non-final gap or duplicate ordinal in a legacy Fact/Finding version sequence that cannot be reconstructed from the current row;
- a live key shadowed by a legacy alias such that the legacy system exposes two incompatible current identities;
- a cross-Project reference or source row whose owning Project cannot be established;
- a migration plan whose deterministic IDs/keys collide after normalization;
- a source database change between inspection and the cutover transaction.

The diagnostic names the Project, source table, key/ID, stable code, and repair options. It never logs the full sensitive body/proof value.

### 4.2 Non-blocking diagnostics

These conditions are preserved and reported but do not block:

- nonconforming legacy keys that require a generated canonical key plus alias;
- redundant or legacy-invalid historical transitions;
- aliases with no recoverable source history;
- documented legacy relations that are not graph-representable;
- unknown relation strings retained only in the migration audit;
- confirmed legacy records that need the scoped confirmation exception in section 10;
- missing Evidence files, stale recorded digests, unknown artifact types, or dangling attachment targets;
- legacy Continuations without a reconstructable Runtime Configuration Version or Blackboard snapshot;
- Task Summary Versions that cannot be attributed to a specific Continuation.

## 5. Backup and maintenance gate

Cutover runs before the daemon accepts API/MCP connections or launches a Runtime. It takes an exclusive application-level maintenance lease in addition to SQLite's writer lock.

For a file-backed database, cutover MUST:

1. checkpoint WAL through SQLite;
2. create a consistent backup with the SQLite backup API or `VACUUM INTO`, never a raw main-file copy that ignores WAL;
3. fsync the backup and containing directory;
4. open the backup independently and run `quick_check`;
5. calculate and store its SHA-256;
6. use owner-only file permissions.

The default name is `<database>.pre-graph-v1.<UTC timestamp>.bak`, unless the operator supplies another local path. Backup failure aborts before any source mutation. In-memory databases used by tests may explicitly waive the backup requirement.

The migration never edits source repositories, user worktrees, Runtime Workdirs, host runtime configuration, or unrelated files. It reads Evidence payloads only through the managed Artifact Root inspector and never follows a path outside that root.

## 6. Atomic cutover algorithm

After a blocker-free inspection and verified backup, `cutover` follows this exact sequence:

1. start `BEGIN IMMEDIATE`;
2. revalidate maintenance ownership, SQLite settings, migration checksums, and absence of active Continuations;
3. reread and hash every legacy source row plus every managed-artifact observation; require the source digest from inspection;
4. apply the numbered surrounding-schema and graph-ledger DDL using the same transaction-scoped connection;
5. backfill Project kind and the legacy-safe Task/Continuation/Event/Summary columns from section 8;
6. build one deterministic `LegacyImportPlanV1` per Project;
7. call transaction-bound `BlackboardGraphService.Apply` once per Project with migration actor `legacy-blackboard-v1` and idempotency key `legacy-blackboard-v1:<source-digest>:<project-id>`;
8. write the bounded legacy mapping rows and verify their mapping digest;
9. rebuild/verify graph heads and key registry from the imported ledger;
10. render and size `CanonicalMainGraphV1`, calculate state/projection hashes, and run Blackboard Health;
11. execute every compatibility parity assertion in section 11 against the uncommitted graph snapshot;
12. install legacy-table write guards;
13. set `canonical_store=graph_v1`, persist cutover/backup/digest metadata, and record the data-migration checksum;
14. commit;
15. reopen a read transaction and rerun state hash, projection hash, mapping digest, legacy write-guard, and compatibility smoke verification before serving requests.

Any failure before commit rolls back DDL, imported ledger rows, mappings, guards, and store state together. The legacy source remains canonical and byte-for-byte unchanged. A process death before commit has the same result when SQLite recovers.

A process death after commit is detected by `canonical_store=graph_v1` plus the cutover ID. Startup verifies the committed result; it does not rerun import or fall back to legacy tables.

### 6.1 Imported graph revision

Each Project import is one `mutation_kind=migration` mutation. An imported non-empty semantic graph moves from graph revision 0 to 1. All imported historical node/edge/key post-images belong to the migration mutation and revision 1; pre-cutover semantic history is addressed by record version and migration mapping, not by pretending historical graph revisions existed.

An empty Project may remain at graph revision 0 with an initialized graph state. Its first semantic write creates revision 1.

The migration mutation's `recorded_at` and provenance time are the cutover time. Imported record `created_at`, `updated_at`, version timestamps, and edge timestamps preserve validated legacy source times. Those source times are trusted from the source snapshot and named in `migration_source`; they are not caller-controlled provenance timestamps.

## 7. Deterministic identity and key normalization

### 7.1 Immutable IDs

Legacy Fact, Finding, and Evidence IDs are retained as graph node IDs when they are non-empty and globally unique across all imported graph nodes. A collision or invalid empty ID is replaced deterministically with:

```text
mig_<hex SHA-256("node\0" || project_id || "\0" || source_table || "\0" || source_id)>
```

Edge and generated identity IDs use the same construction with a distinct domain string. A source-to-target mapping is always recorded. Hash collision is a blocker; the importer never silently adds a random suffix.

### 7.2 Stable keys

If a legacy Fact/Finding/Evidence key already satisfies the graph key grammar, it remains the stable key. Otherwise the canonical stable key is:

```text
legacy-import:<short-node-type>:<hex SHA-256(project_id || "\0" || original_key)>
```

The original key becomes a direct alias with `legacy_nonconforming=true`. Compatibility reads and writes continue to accept it. New graph-native callers receive the canonical stable key plus alias-resolution metadata.

The importer trims no stored key bytes beyond the behavior already reflected in the stored row. It does not lowercase, transliterate, or guess human-readable replacements.

### 7.3 Historical versions

Legacy Fact and Finding version rows are imported as immutable historical post-images with their original payload, version number, and timestamp. Current rows are compared with the last historical row:

- if no version exists, current state becomes version 1;
- if current state differs, it becomes the next version;
- if it is identical, no synthetic final version is added.

The importer preserves redundant historical versions and legacy transition sequences even when ordinary graph v1 Apply would treat them as no-ops or reject the transition. They are marked in `blackboard_legacy_mappings` as imported history exceptions. This exception applies only to the one-time historical import. The current head MUST be valid under the rules in sections 9 and 10, and every post-cutover mutation follows ordinary graph version and lifecycle rules.

If a version sequence has a gap, a final missing version may be synthesized from the current row only when it is exactly `max(version)+1`. Any other gap blocks cutover; the importer never silently renumbers historical versions.

## 8. Projects, Tasks, Continuations, Events, and Summaries

### 8.1 Projects

Every existing Project receives immutable `kind=pentest`. The migration does not infer CTF kind from names, goals, flags, reports, or data. New CTF Challenge Projects are created explicitly after cutover.

### 8.2 Task Goals

Every Task creates one system-owned Goal node at `task:<task_id>:goal` with immutable goal text and current Task status. Goal provenance is migration provenance linked to the source Task. Later Goal reconciliation is owned by the Task projector.

### 8.3 Existing Continuations

Cutover requires no active Continuation. Existing pending/running/paused rows are first reconciled by the existing Task lifecycle into a terminal interrupted state before inspection can pass.

New surrounding columns use these legacy rules:

- `runtime_config_version_id` is backfilled only when one unique Task Runtime Configuration Version with matching Task/profile and the latest creation time not after Continuation start exists; otherwise it remains null and is reported as legacy-incomplete provenance;
- Blackboard revision/hash/renderer/estimator snapshot fields remain null for pre-graph Continuations; no historical snapshot is fabricated;
- reconciliation status is `legacy_not_applicable`, with no graph mutation ID, because no graph Attempt existed to reconcile;
- new Continuations after cutover require all fields fixed by the storage/runtime contracts.

Database checks or triggers enforce the new requirements for post-cutover rows without rewriting unknown history.

### 8.4 Task Events

Legacy Task Events remain outside the graph. `continuation_id` and `attempt_node_id` remain null unless the source already has an exact durable association. The migration never infers an Attempt from raw output, command text, timing proximity, or Task identity.

### 8.5 Task Summary Versions

Every existing Task Summary Version remains in `task_summary_versions` with the same ID, Task, version, summary, submitter, and creation time. `continuation_id` remains null unless the source has an exact durable association. The migration does not guess it from timestamps.

New Runtime Task Summaries are created by the Finish protocol and are bound to a Continuation. Operator-authored compatibility summaries may remain unbound and carry operator provenance in the Task domain.

### 8.6 Reports

Reports are derived and have no legacy canonical rows to migrate. After cutover, report routes and commands render `PentestReportV1`; a report previously retained as Evidence migrates only as its EvidenceArtifact row and payload reference.

## 9. Project Facts, aliases, merges, and relations

### 9.1 Project Facts

Each current or historically addressable Fact identity becomes a ProjectFact node.

Field mapping is:

| Legacy | Graph |
| --- | --- |
| `fact_key` | stable key or generated key plus alias |
| empty `category` | `uncategorized` |
| `summary` | `summary` |
| `body` | `body` unchanged |
| empty confidence | `tentative` |
| `tentative|confirmed|deprecated` | same confidence |
| empty scope status | `unknown` |
| `in_scope|out_of_scope|unknown` | same scope status |

An unknown non-empty confidence is a blocker. Any other non-empty scope status maps to `unknown` and is preserved in the migration audit.

### 9.2 Legacy confirmation exception for Project Facts

A current legacy ProjectFact marked confirmed may lack Evidence, supporting records, a succeeded Attempt, or a non-empty body because the old service did not require them.

The migration actor MAY preserve that confirmed head when the source confidence is exactly `confirmed`. The created provenance records the exact legacy Fact/version. A derived `legacy_confirmed_fact_without_basis` Health warning remains until normal graph support is added or confidence changes.

The exception is inherited only by that imported identity. Ordinary creation or transition to confirmed still requires the graph contract's confirmation guard. A later patch may preserve the imported confirmed state, but removing and then re-entering confirmed requires normal support.

### 9.3 Fact aliases and merges

The importer builds the complete alias graph and flattens it to current canonical nodes.

- When the source key still has source Fact Versions, the importer creates the source node with that history and imports a merge into the canonical node at the alias timestamp.
- When the old merge copied/rebadged source versions under the canonical key, those rows remain preserved as legacy canonical history and are marked `legacy_rebadged_copy`; the importer does not pretend it can recover information the old merge discarded.
- When no source history exists, the importer creates a direct migration alias without fabricating a source node body.
- Alias chains become one-hop key redirects while immutable merged IDs keep their historical redirect chain.
- A dangling/cyclic alias with no live canonical target is retained as audit metadata and omitted from the current key registry.

No semantic-similarity merge is inferred.

### 9.4 Fact Relations

Legacy relations use this closed normalization table:

| Legacy relation | Imported semantic result |
| --- | --- |
| `supports` | active `supports` ProjectFact-to-ProjectFact edge |
| `contradicts` | active `contradicts` ProjectFact-to-ProjectFact edge |
| `leads_to` or `leads-to` | active `leads_to` ProjectFact-to-ProjectFact edge |
| `depends_on` or `depends-on` | audit-only compatibility relation; not a graph edge |
| `duplicates` | represented by an existing provable alias/merge when present; otherwise audit-only duplicate candidate |
| any other non-empty value | audit-only migration record |

ProjectFact-to-ProjectFact `depends_on` is not imported as a core edge because graph v1 reserves `depends_on` for Exploration Objective prerequisites. It never affects Frontier.

Audit-only relations are preserved in the hashed migration mapping and migration report. During the compatibility window, documented legacy `depends_on` rows may still be returned by the legacy relation projection. `duplicates` and undocumented relation strings are not synthesized as active graph relations.

Post-cutover compatibility writes accept only `supports`, `contradicts`, `leads_to`, and `leads-to`. `depends_on`, `duplicates`, and unknown strings fail with stable code `legacy_relation_not_graph_representable`; callers must use graph-native Objective dependencies or explicit Fact Merge as appropriate.

## 10. Findings and Evidence

### 10.1 Findings

Each current or historically addressable legacy Finding identity becomes a Finding node. Legacy version payloads and timestamps are preserved under section 7.3.

The importer ignores stored `severity` and `cvss_pending` as derived values and recomputes them from the validated CVSS vector. If `cvss_version` is empty but the vector has an exact `CVSS:4.0/` or `CVSS:3.1/` prefix, the version is inferred. No other CVSS version is guessed.

Legacy `unconfirmed`, `confirmed`, and `false_positive` statuses map directly. Current unconfirmed or false-positive Findings may have incomplete report fields. Current confirmed Findings MUST have title, target, proof, impact, recommendation, and a complete valid CVSS 4.0/3.1 vector or inspection blocks.

### 10.2 Legacy confirmation exception for Findings

The old service allowed a complete Finding to become confirmed without an Evidence attachment or supporting ProjectFact. To preserve existing status without inventing synthetic Evidence or changing dashboard counts, migration MAY import such a complete confirmed Finding with trusted migration provenance.

The graph derives `legacy_confirmed_finding_without_support` Health warning until an active `evidences` or qualifying `supports` edge is added or the Finding leaves confirmed state. The exception cannot be used by ordinary Runtime/operator/system writes and cannot create a new unsupported confirmed Finding after cutover.

A later non-status patch may preserve the imported exception. Any transition into confirmed after leaving it requires ordinary graph support.

### 10.3 Finding aliases and merges

Finding aliases follow the Fact rules in section 9.3. When source Finding Versions exist, the source identity/history is imported and merged. Existing Evidence target keys resolve through the flattened canonical alias before `evidences` edges are created. No Finding properties silently overwrite canonical properties during merge.

### 10.4 EvidenceArtifacts

Each legacy Evidence row becomes one EvidenceArtifact node.

| Legacy condition | Import behavior |
| --- | --- |
| graph-known artifact type | preserve type |
| unknown artifact type | use `other`; retain original type in mapping metadata |
| confined existing managed file | compute actual SHA-256 and size; status `available` |
| missing/unreadable managed file | status `missing`; preserve managed reference |
| stored digest differs from actual | actual digest becomes canonical; old digest stays in mapping metadata and Health reports drift |
| unsafe absolute/escaping managed path | do not read it; use a deterministic missing managed reference and preserve the original only in migration audit |

`source_path`, summary, and source timestamps remain unchanged. The migration never copies from arbitrary `source_path`; only already-managed Artifact Root content is inspected.

If the original attachment target resolves to a current ProjectFact or Finding, migration creates one active `evidences` edge. If it does not resolve, the EvidenceArtifact remains present without that edge and Health reports it. The original singular target preference is stored in mapping metadata so the legacy Evidence projection remains deterministic after later graph-native attachments.

Evidence rows have no legacy version table, so migration imports one current version. Later retained-content replacement follows the Runtime protocol.

## 11. In-transaction parity gates

Before the store-epoch flip, cutover compares source-backed legacy results with graph-backed compatibility results for every Project.

Required gates are:

1. Fact index count and field equality after documented normalization, with deprecated filtering both off and on;
2. Fact point reads through live and resolvable alias keys;
3. Fact version payload/order equality, including original legacy version numbers through migration mapping;
4. supported Fact Relation equality, plus preserved legacy `depends_on` compatibility rows;
5. Finding current/version field equality except recomputed derived CVSS fields;
6. Evidence row equality, original singular target preference, and complete attachment list;
7. Dashboard Task/Fact/Finding/Evidence counts, excluding no synthetic semantic records;
8. latest/all Task Summary Version equality;
9. deterministic legacy report fixture equality under the new report contract's already-decided scope semantics;
10. canonical graph, state, mapping, and compatibility projection hashes;
11. Project isolation for every mapped ID/key/alias;
12. no legacy-table write succeeds after guards are installed.

Every intentional difference is named by a stable normalization code in the migration result. An unexplained difference aborts the transaction.

## 12. Compatibility calls after cutover

### 12.1 Common rules

Compatibility adapters:

- authenticate through the operator credential or Continuation Interface Grant fixed upstream;
- resolve Project/Task/Continuation from trusted context and reject mismatches;
- accept optional `expected_version` and idempotency input additively;
- when expected version is absent, resolve once and use that current version; a race returns `version_conflict` and is never silently retried;
- return the legacy success shape plus only the additive metadata fixed by the read contract;
- return `ProjectInterfaceErrorV1` and honest HTTP/CLI status mappings;
- increment local compatibility-use counters without recording request bodies or secrets.

Exact lost-response replay is guaranteed only when the legacy caller supplies the additive idempotency key/header. Without one, PUT-like same-state retries normally converge through graph no-op behavior, but the compatibility surface does not promise exact replay bytes.

### 12.2 HTTP mapping

| Legacy HTTP route | Translation |
| --- | --- |
| `PUT /api/projects/{id}/facts/{fact_key}` | create/patch/transition ProjectFact through Apply |
| `GET /facts/index`, `GET /facts/{key}`, `GET /versions` | legacy read projections |
| `PUT /facts/{key}/relations` | supported edge put; non-representable relation rejected |
| `GET /facts/{key}/relations` | graph edges plus preserved legacy `depends_on` mapping |
| `POST /facts/merge` | `merge_nodes` ProjectFact |
| `PUT /findings/{key}` | create/patch/transition Finding; new confirmation requires normal support |
| `GET /findings`, `GET /findings/{key}/versions` | legacy read projections |
| `POST /findings/merge` | `merge_nodes` Finding |
| `POST /evidence` | `RetainEvidenceV1` plus `evidences` link |
| `GET /evidence` | legacy Evidence projection |
| `PUT/GET /tasks/{task_id}/summary` | Task Summary compatibility service |
| `POST /report` | `PentestReportV1` compatibility renderer |

Legacy Evidence write requests gain additive `idempotency_key`, `expected_version`, and `produced_by_attempt`. A task-bound Runtime MUST provide `produced_by_attempt`; operator calls use an explicitly configured local source root. A Runtime request without an Attempt fails `compatibility_attempt_required` rather than fabricating provenance.

### 12.3 MCP tools

The existing names remain registered during the compatibility window:

- `upsert_project_fact`;
- `get_project_fact`;
- `list_project_facts`;
- `search_project_facts`;
- `deprecate_project_fact`;
- `upsert_fact_relation`;
- `record_vulnerability`;
- `list_vulnerabilities`;
- `attach_evidence`;
- `generate_report`;
- `submit_task_summary`.

Read tools delegate to legacy read projections. Fact/Finding/relation writes delegate to the compatibility service. `attach_evidence` delegates to retained Evidence and requires an Attempt for a Runtime.

`submit_task_summary` under a Continuation Grant delegates to `blackboard_finish_continuation`: it validates matching Project/Task arguments, requires no open Attempts, stores the summary, and closes the grant. Operator-authored Task Summary writes remain a separate deprecated Task-domain operation and do not impersonate Finish.

Compatibility MCP tools are not included in newly generated Runtime instructions after cutover. Their tool descriptions are marked deprecated through transport metadata where supported.

### 12.4 `pentestctl`

The existing commands remain wrappers during the window:

- `fact upsert`;
- `task summary put`;
- `evidence attach`;
- `finding upsert`;
- `report generate`.

Task mode uses the Continuation Grant and new project-interface routes. Operator `--api` and `--db` modes use operator provenance and the same compatibility module. JSON stdout remains machine-readable; one deprecation warning is written to stderr unless suppressed.

### 12.5 Deprecation metadata

Legacy HTTP responses include:

- `Deprecation: true`;
- a `Sunset` header once a concrete removal release/date is published;
- `Link: <migration-documentation>; rel="deprecation"`;
- `CyberPenda-Compatibility: legacy_blackboard_v1`.

CLI warnings and MCP metadata convey the same replacement operation. Payload fields are not renamed in place.

## 13. Consumer cutover

### 13.1 Runtime context and instructions

New Continuations receive the canonical Blackboard Runtime Protocol, Continuation Interface Grant, pinned `CanonicalMainGraphV1`, `.pentest/blackboard.json`, generated `AGENTS.md`, and generated `CLAUDE.md` fixed by the Runtime contract.

The old handoff builder MUST stop separately injecting Fact Index rows, `progress:*` bodies, and Finding lists. Task Summary or Mechanical Handoff remains Task context, while semantic Project memory comes from the full pinned graph. This prevents duplicate/stale memory channels.

No active legacy Continuation crosses cutover. Every post-cutover Continuation is created under the new protocol.

### 13.2 Web UI and dashboard

The bundled Web UI and daemon ship together for cutover:

- `/projects/{id}/facts` redirects to or renders Blackboard Work filtered to ProjectFact;
- Finding, Evidence, and Report bookmarks remain valid focused views;
- Dashboard counts and Blackboard summary come from `BlackboardReadService`;
- versions, relations, provenance, and merge resolution use shared read shapes;
- CTF Solution routes appear only for explicit CTF Projects created after cutover.

The UI does not read migration/frozen legacy tables and does not contain a fallback legacy query path.

### 13.3 Reports

HTTP, MCP, CLI, and Web Report consumers share `PentestReportV1`. Legacy no-Task requests use current Scope, while explicit `task_id` uses that Task's Scope Snapshot, as fixed by the read contract. CTF Projects return `project_kind_mismatch` on the Pentest report compatibility route.

### 13.4 Artifact and worktree safety

Migration may read/hash managed Evidence files but does not rename/delete them. It never imports Runtime Workdir files merely because they exist. No repository, source tree, `node_modules`, git index, branch, or unrelated working-tree change is part of database migration.

## 14. Failure recovery and rollback

### 14.1 Before commit

Inspection or backup failure changes nothing. A cutover validation/storage/process failure before commit rolls back the transaction. The new binary starts in `migration_required` maintenance mode with read-only diagnostics/export and no Task launch or Blackboard writes. The operator may repair the legacy database with a compatible old release, restore the verified backup, or retry after correcting the blocker.

### 14.2 After commit, before serving

If post-commit verification fails before requests are accepted, startup enters `recovery_required`. It MUST NOT consult legacy tables as live state or accept writes. The safe recovery is restoring the verified backup and retrying with a corrected binary.

### 14.3 After graph writes begin

There is no automatic reverse migration. Graph-native Entity/Objective/Attempt/Observation/Hypothesis/Solution/Directive state cannot be losslessly projected into the old schema.

Restoring the pre-cutover backup after post-cutover writes discards those writes. Before such a restore, the operator SHOULD export the graph ledger/migration/Health state for diagnosis. The CLI must state the cutover ID, backup hash, and whether post-cutover writes exist before accepting restore instructions.

An old binary MUST refuse the newer schema/store epoch. It never silently opens graph-canonical data and writes frozen legacy tables.

### 14.4 Lost response and retry

Cutover is idempotent by source digest and cutover ID. Retrying after a committed cutover returns the existing result. A retry with another source digest or migration contract version conflicts and requires explicit recovery; it never imports twice.

## 15. Release sequence

### Release A — prepare

- introduce numbered/checksummed migrations and per-connection SQLite settings;
- add graph, read, Runtime protocol, migration, and compatibility modules behind `canonical_store=legacy_v1`;
- add `inspect`, backup, verify, and recovery CLI;
- ship parity golden fixtures and warn about relation/confirmation records that need normalization;
- keep legacy tables canonical and do not dual-write graph state.

### Release B — cut over

- require blocker-free inspection and verified backup on first open;
- perform the atomic cutover;
- project only the new Runtime protocol to new Continuations;
- switch bundled UI, dashboard, reports, HTTP, MCP, and CLI to graph/read/project-interface modules;
- retain compatibility routes/tools/commands with deprecation metadata;
- freeze legacy Blackboard tables.

### Release C — retire compatibility writes

- after the gates in section 16, remove or return `410 compatibility_removed` for legacy Fact/Finding/relation/Evidence/Runtime-summary writes;
- keep compatibility reads and UI redirects for one additional stable release;
- graph-native and Finish interfaces remain unchanged.

### Release D — retire compatibility reads and optionally finalize tables

- remove legacy MCP tool registration, CLI aliases, and HTTP read routes after their gates pass;
- keep documented browser redirects where cheap and unambiguous;
- drop frozen legacy tables only through explicit `finalize_legacy`, never merely because code stopped calling them.

## 16. Removal and finalization gates

Compatibility writes may be retired only when all are true:

1. at least two stable releases have shipped the graph-native replacement;
2. every bundled Runtime adapter and generated instruction uses only the six v1 capabilities;
3. no active/pre-cutover Continuation exists;
4. local compatibility-write counters are zero for at least 30 days or the operator explicitly waives the observation period;
5. migration verification and Blackboard Health have no migration/integrity critical result;
6. replacement documentation and stable `410` error guidance exist.

Compatibility reads may be retired only when all write gates pass, bundled Web/CLI clients use canonical projections, and local compatibility-read counters meet the same observation/waiver rule.

`finalize_legacy` additionally requires:

- an explicit operator command naming the cutover ID;
- the verified backup path and SHA-256 still available or an explicit acknowledgement that rollback is being surrendered;
- a fresh successful migration verify result;
- no compatibility route/tool/command enabled;
- an export of the migration summary and mapping digest;
- one numbered transactional migration that drops only the frozen legacy Blackboard tables and their guards.

Finalization does not drop Task, Continuation, Event, Summary, Scope, Artifact, graph ledger, migration mapping, or Health data. `blackboard_legacy_mappings` remains as bounded audit/provenance metadata.

## 17. Security and integrity rules

- Backup and migration metadata are local, owner-readable only, and never uploaded automatically.
- Migration diagnostics redact Fact bodies, Finding proof, Summary text, paths outside the managed root, tokens, and credential values.
- Source row and mapping digests use framed domain-separated SHA-256 inputs, not ambiguous string concatenation.
- Migration provenance uses actor type `migration`, actor ID `legacy-blackboard-v1`, and exact source table/key/version. It never fabricates Runtime, Task, Continuation, Runner, or Runtime Profile provenance.
- Legacy absolute `source_path` values are not promoted into canonical operator projections beyond the already-public compatibility field.
- Legacy-table guards abort INSERT/UPDATE/DELETE after cutover, including accidental calls from stale code.
- The migration module never loads artifact payload bytes into graph properties, migration logs, or issue/report output.
- A source or mapping digest mismatch fails closed.

## 18. TDD acceptance seams

Implementation proceeds red-first at these pre-agreed seams:

1. `BlackboardMigrationService.Execute` for inspect/cutover/verify/finalize behavior;
2. `BlackboardGraphService.Apply` with sealed transaction-bound migration context;
3. `BlackboardCompatibilityService.Call` for legacy write translation;
4. `BlackboardReadService` for compatibility parity;
5. public startup/CLI/HTTP/MCP/UI routes for observable cutover behavior.

Behavioral tests observe these interfaces. Storage crash/corruption tests may inspect a real temporary file-backed SQLite database and Artifact Root. Stable injected internal seams are limited to clock, ID source, backup implementation, artifact inspector, failure points, migration transaction binder, and compatibility-use counter.

Minimum red-first matrix:

1. Inspect is deterministic and performs no DB/filesystem writes.
2. Source digest changes for any source row change and is stable across SQL row order.
3. Unknown/checksum-mismatched migrations and SQLite integrity failure block cutover.
4. Any active Continuation blocks cutover; reconciled terminal legacy Continuations pass.
5. Backup uses a WAL-consistent SQLite snapshot, passes independent `quick_check`, and has stable hash/permissions.
6. Backup failure leaves the source and store epoch unchanged.
7. Failure after DDL, Project import, mappings, head build, parity check, guards, or state flip rolls back the entire transaction.
8. Process death before commit reopens legacy-canonical state; death after commit reopens graph-canonical state.
9. Committed cutover retry returns the original result; changed source digest conflicts.
10. Old binaries/newer schema fail closed and cannot mutate frozen tables.
11. Every legacy Project becomes immutable `pentest`; no CTF inference occurs.
12. Every Task gets the exact Goal projection and current Task status.
13. Legacy Event/Summary/Continuation associations remain null rather than inferred when not exact.
14. Exact Runtime Configuration match backfills; ambiguous/missing match remains explicit legacy-incomplete state.
15. Conforming keys and globally unique IDs are retained.
16. Nonconforming keys receive deterministic canonical keys and resolving legacy aliases.
17. ID/key hash collisions block rather than randomize.
18. Current Fact without versions gains version 1; drift from the last version gains one final version.
19. A non-final or otherwise unreconstructable version gap blocks cutover.
20. Redundant and legacy-invalid Fact history remains addressable and is marked as imported exception.
21. Empty Fact category/scope normalize to `uncategorized`/`unknown`; unknown confidence blocks.
22. Unsupported confirmed Fact imports with migration provenance and emits the Health warning; a new unsupported confirmation still fails.
23. Fact alias chains flatten; source histories import as merged identities when available.
24. Dangling/cyclic aliases remain audit-only and never corrupt the key registry.
25. Legacy rebadged merge versions remain preserved and marked.
26. `supports`, `contradicts`, and both `leads_to` spellings produce correct directed edges.
27. Fact `depends_on` remains compatibility/audit-only and never affects Frontier.
28. `duplicates` never creates a merge without provable alias identity.
29. Unknown relations remain hashed audit records and cannot be newly written after cutover.
30. Current Finding without versions and current/version drift follow the Fact rules.
31. `false_positive` Finding status imports directly and remains terminal for later graph mutations.
32. CVSS version is inferred only from exact supported prefixes; stored severity/pending are recomputed.
33. Incomplete/invalid confirmed Finding blocks cutover without source mutation.
34. Complete unsupported confirmed Finding imports with warning; ordinary unsupported confirmation fails.
35. Finding aliases/merges preserve source history and retarget Evidence through the canonical key.
36. Known Evidence types remain; unknown types become `other` with audited original value.
37. Existing confined Evidence is rehashed/sized; missing Evidence remains visible as missing.
38. Digest drift preserves the actual digest and reports the legacy mismatch.
39. Escaping Evidence paths are never opened and become deterministic missing references.
40. Resolved attachment creates one evidences edge; dangling target retains the Evidence node and original preference metadata.
41. No synthetic Fact/Finding/Evidence record changes Dashboard counts.
42. Legacy Fact/Finding/Evidence/alias/version mapping digest is deterministic and verifiable.
43. Fact index/point/version/relation compatibility golden fixtures match documented normalization.
44. Finding/Evidence compatibility fixtures preserve fields, alias resolution, and singular attachment preference.
45. Dashboard and Task Summary compatibility parity pass before commit.
46. Legacy report delegates to deterministic `PentestReportV1` and obeys current/task Scope selection.
47. Legacy Fact upsert maps create/patch/transition and preserves old omitted/empty-field semantics where documented.
48. Legacy merge routes call graph Merge Nodes and preserve response shapes.
49. Legacy relation writes reject non-representable semantics with stable 422/code.
50. Legacy Finding confirmation requires graph support after cutover.
51. Operator Evidence compatibility uses retained Evidence; Runtime Evidence requires a matching Attempt.
52. Runtime `submit_task_summary` maps to Finish, rejects open Attempts, and closes the grant.
53. Operator Task Summary compatibility preserves Task-domain versioning without fabricating Finish.
54. Equivalent legacy HTTP/MCP/CLI calls produce the same translated semantic result and error code.
55. Missing expected version observes once; a concurrent write returns version_conflict without hidden retry.
56. Supplied compatibility idempotency key replays exactly; absent key is documented best-effort only.
57. Deprecation headers, MCP metadata, and CLI stderr warnings do not corrupt legacy JSON payloads.
58. New Continuations receive only graph-native instructions/full snapshot; no duplicate Fact/`progress:*`/Finding handoff is injected.
59. Bundled UI routes use shared read projections and never query frozen legacy tables.
60. Post-commit verify detects state, projection, mapping, guard, and parity corruption and enters recovery-required mode.
61. Backup restore warning names lost post-cutover writes and never performs an implicit reverse migration.
62. Finalize refuses without gates/confirmation, then drops only frozen legacy Blackboard tables transactionally.

The downstream slicing ticket chooses vertical tracer-bullet order. It MUST NOT implement this as one horizontal migration-test phase followed by all production code.

## 19. Existing implementation delta

Current code differs from this contract in the following concrete ways:

- `internal/store/store.go` uses unnumbered idempotent DDL and one-time PRAGMA calls;
- current Fact/Finding current-row and version-row writes are separate autocommits;
- Fact/Finding merges copy/rebadge history, rewrite references, delete source rows, and replace aliases destructively;
- Fact Relations accept arbitrary text and have no versions;
- confirmed Findings do not require Evidence/support;
- Evidence attachment records a path/reference but does not perform retained-file orchestration;
- Projects have no kind;
- Continuations/Events/Summaries lack graph/provenance/Finish fields;
- trusted MCP uses a daemon-wide token and caller-supplied Project IDs;
- HTTP, MCP, and CLI call the legacy service directly and have divergent errors/idempotency;
- generated Runtime instructions advertise legacy tools;
- handoff resume separately injects Fact Index, `progress:*` bodies, and Findings;
- Dashboard, reports, and Web pages query legacy-shaped services;
- no inspection, backup, source digest, store epoch, mapping manifest, frozen-table guard, compatibility-use counter, or finalization workflow exists.

Implementation replaces those seams. It does not layer graph writes beside the current service and does not include unrelated worktree/UI prototype changes in the migration work.

## 20. Downstream handoff

The next ticket, [Design TDD acceptance matrix and implementation slices for graph Blackboard](https://github.com/n1majne3/CyberPenda/issues/61), owns the vertical red-green implementation order across numbered migrations, graph storage, migration import, Runtime protocol, compatibility adapters, read projections, reports, and UI.

The final assembly ticket owns the unified implementation-ready refactor document and decides whether execution should use one implementation map or several maps split by storage, Runtime protocol, and UI/migration blast radius.

No new domain glossary term is required. `LegacyImportPlanV1`, store epoch, compatibility family, and cutover state are implementation/protocol names, not semantic Blackboard concepts. No separate ADR is required: this versioned contract records the backup/no-dual-write/rollback trade-off and may be superseded explicitly if implementation evidence forces a different cutover.
