# Blackboard v2 Specification

- **Status:** accepted and implementation-ready after shared-understanding confirmation
- **Tracker spec:** [#97 — Replace Blackboard v1 with compact semantic Blackboard v2](https://github.com/n1majne3/CyberPenda/issues/97)
- **Runtime snapshot schema:** `runtime-blackboard/v2`
- **Semantic change schema:** `semantic-change-batch/v2`
- **Supersedes:** the seven Blackboard v1 specifications marked historical
- **Decisions:** ADRs 0003–0014

This is the single normative contract for Blackboard v2. `CONTEXT.md` owns domain vocabulary, the ADRs explain why the design was chosen, and `blackboard-v2-tdd-plan.md` owns implementation order. Transport schemas, storage code, UI code, and tests must conform to this document rather than restating a different Blackboard model.

## 1. Purpose and boundary

The Blackboard is one Project's durable semantic memory. It gives every Runtime Continuation the complete current reasoning graph in a compact form while keeping raw proof, history, control bindings, and audit-oriented storage details out of model attention.

Blackboard v2 contains:

- Current Work: open Exploration Objectives and open Attempts;
- Project Knowledge: current Entities, Project Facts, Findings, CTF Solutions, and Evidence Artifact references;
- current typed semantic relationships among those records.

It does not contain Task Goals, Tasks, Task status, Scope authorization, Observations, Hypotheses, Project Directives, Task Summaries, Objective Outcome copies, Mechanical Handoffs, Task Events, commands, logs, raw artifact bytes, or Trusted Origin data.

Task Goal belongs to Task. Authorization and testing limits belong to Scope. Task-local adjustment belongs to Harness Steering. Raw execution belongs to Task Events, logs, the Runtime Workdir, or retained Evidence.

Every Blackboard belongs to exactly one Project. Keys, reads, writes, relationships, revisions, redirects, pins, and synchronization are Project-isolated. Caller-supplied Project or execution identity is never authoritative.

## 2. Semantic records

Each record has one project-wide Blackboard Key, a semantic type, a current semantic version, type-specific fields, and optional prior versions in Semantic History. Storage may use private identifiers, but no Runtime or ordinary UI contract exposes them.

| Type | Current Runtime states | Leaves Runtime when | Canonical semantic fields |
| --- | --- | --- | --- |
| Entity | `active` | retired or superseded | `kind`, `name`, optional `locator`, optional `description`, `scope_status`, optional non-secret `credential_ref`, `status` |
| Exploration Objective | `open` | resolved, abandoned, or superseded | `objective`, `status`, optional terminal `resolution_summary` |
| Attempt | `open` | succeeded, failed, blocked, inconclusive, or interrupted | `status`, `summary` |
| Project Fact | `tentative`, `confirmed` | deprecated or superseded | `category`, `summary`, optional `body`, `confidence`, `scope_status` |
| Finding | `unconfirmed`, `confirmed` | marked false positive or superseded | `status`, `title`, optional `target`, optional `description`, optional `proof`, optional `impact`, optional `recommendation`, optional `cvss_version`, optional `cvss_vector`; `severity` and `cvss_pending` are derived |
| Solution | `candidate`, `verified` | rejected or superseded | `status`, `kind`, `summary`, optional `value`, optional `verification_summary`; valid only for a CTF Challenge Project |
| Evidence Artifact | `available`, `missing` | superseded | `status`, `artifact_type`, `summary`, optional `media_type`, optional `source_path`, managed `managed_path`, managed digest and size, optional semantically relevant `captured_at` |

Unknown record types and unknown type-specific fields are rejected. Partial updates preserve omitted fields; clearing an optional field must be explicit. Derived fields are never caller-written.

### 2.1 Lifecycle guards

- Objective resolves only with an incoming current `satisfies` relationship from a current Project Fact, Finding, or Solution. Supersession requires a replacement.
- Attempt terminal states require a distilled summary and at least one `tests` relationship. Runtime interruption is reconciled by the server; a Runtime cannot mark its own Attempt interrupted.
- Confirmed Fact requires semantic support: Evidence, a confirmed supporting Fact, a succeeded producing Attempt bound to the Runtime, or an explicit trusted operator/system confirmation basis.
- Confirmed Finding requires complete report fields, a complete valid CVSS vector, and Evidence or a confirmed supporting Fact.
- Verified Solution requires its type-specific value when applicable and a non-empty verification summary. A current verified flag Solution determines CTF solved state without a copied Goal node or Task-status coupling.
- Superseded Entity, Objective, Fact, Finding, Solution, or Evidence requires one current replacement through `supersedes`.
- Before a terminal Current Work record or invalidated Project Knowledge record leaves Runtime context, reusable outcome or invalidation meaning must exist as current semantic knowledge or a current replacement. Meaningless invalidations need not manufacture an empty Fact.

Lifecycle timestamps are not semantic fields. A time is retained only when it changes security meaning, such as Evidence capture, observation, expiry, or authorization validity.

## 3. Blackboard Keys, versions, and consolidation

A Blackboard Key is non-empty, human-readable ASCII, at most 96 characters, and unique across all record types in one Project. It must not embed an internal Project, Task, Continuation, Runtime, database, generated-ID, or hash value. An external identifier may appear when it is part of the domain meaning.

The same durable concept keeps the same key across updates. Every semantic change increments that record's or relationship's version and the Project Blackboard revision. Replay and exact no-op writes do not create versions.

Record Merge consolidates duplicate same-type Project Knowledge. It atomically:

1. applies any approved canonical update;
2. rewrites current relationships to the canonical key;
3. moves the source record to Semantic History;
4. creates a project-local Blackboard Key Redirect from source to canonical.

Reads and writes through a redirect resolve to and report the canonical key. Redirects are absent from Runtime snapshots. Current Work is concluded or superseded, never merged. Similarity may propose consolidation but never applies it automatically.

## 4. Relationships

A current relationship is identified in public contracts by `(from_key, type, to_key)` and has a semantic version. It has no public edge ID. Both endpoints must be current canonical records in the same Project. `supersede` is the special atomic case: it validates a current replacement and current replaced record, creates their `supersedes` relationship, then moves the replaced record and that relationship into Semantic History so only the replacement remains current.

| Relationship | Allowed direction |
| --- | --- |
| `about` | Exploration Objective, Attempt, Project Fact, Finding, Solution, or Evidence Artifact → Entity |
| `part_of` | Entity → Entity, or Exploration Objective → Exploration Objective |
| `tests` | Attempt → Exploration Objective, Entity, Project Fact, Finding, or Solution |
| `produced` | Attempt → Entity, Exploration Objective, Project Fact, Finding, Solution, or Evidence Artifact |
| `evidences` | Evidence Artifact → Project Fact, Finding, or Solution |
| `supports` | Project Fact → Project Fact, Finding, or Solution |
| `contradicts` | Project Fact → Project Fact, Finding, or Solution |
| `derived_from` | Exploration Objective → Project Fact, Finding, or Solution; Project Fact → Project Fact or Evidence Artifact; Evidence Artifact → Evidence Artifact |
| `depends_on` | dependent Exploration Objective → prerequisite Exploration Objective |
| `satisfies` | Project Fact, Finding, or Solution → Exploration Objective |
| `supersedes` | replacement → replaced record of the same type for Entity, Exploration Objective, Project Fact, Finding, Solution, or Evidence Artifact |

Every self-link is invalid. `part_of`, `derived_from`, `depends_on`, `supersedes`, and the Project-Fact-to-Project-Fact subset of `supports` are independently acyclic. A replaced record has at most one current replacement. Reciprocal `contradicts` relationships are valid. No global DAG rule spans different relationship types.

Only `supports`, `contradicts`, and `depends_on` may carry a concise, non-redundant reason. `contradicts` never changes lifecycle state by itself. `part_of` never propagates lifecycle state. `blocks` is represented by reversing `depends_on`; `leads_to` is represented with precise current relationships plus an Attack Chain Fact and report narrative.

## 5. Runtime Blackboard Snapshot

Every launch and resume receives the topology-complete current Blackboard, never a relevance-selected subset. Completeness means every current reusable record and relationship is represented, not that auxiliary bodies or proof payloads are inlined.

The canonical top-level shape is:

```json
{
  "schema": "runtime-blackboard/v2",
  "semantics": "work is active; knowledge is current; history and details are available by key",
  "revision": 42,
  "work": {},
  "knowledge": {},
  "relations": []
}
```

`work`, `knowledge`, and `relations` always exist. Empty type groups and absent optional fields are omitted; `null` is not serialized. Work and knowledge groups are maps keyed by Blackboard Key, so a record does not repeat its key or type.

### 5.1 Snapshot field allowlist

| Group | Fields |
| --- | --- |
| `work.objectives` | `version`, `status`, `objective` |
| `work.attempts` | `version`, `status`, `summary` |
| `knowledge.entities` | `version`, `status`, `kind`, `name`, optional `locator`, optional concise `description`, `scope_status`, optional non-secret `credential_ref` |
| `knowledge.facts` | `version`, `category`, `summary`, `confidence`, `scope_status` |
| `knowledge.findings` | `version`, `status`, `title`, optional `target`, optional concise `description`, optional derived `severity`, `cvss_pending` |
| `knowledge.solutions` | `version`, `status`, `kind`, `summary`, optional `value`, optional concise `verification_summary` |
| `knowledge.evidence` | `version`, `status`, `artifact_type`, `summary`, optional `media_type`, optional semantically relevant `captured_at` |

A Pentest Project never contains `solutions`. Every empty group is omitted. There is no Fact Index, Frontier list, Current Truth key list, Task summary, provenance block, storage metadata block, or token-diagnostic block.

Relationships serialize as `[from_key, type, to_key]` or, only for the three reason-capable types, `[from_key, type, to_key, reason]`.

### 5.2 Text limits

- Blackboard Key: 96 ASCII characters.
- Primary Runtime semantic text (`objective`, Attempt summary, Fact/Solution/Evidence summary, Finding description): 1024 UTF-8 bytes.
- Optional identifying description, rationale, explanation, or relationship reason: 512 UTF-8 bytes.

Oversized writes fail with a stable error directing supporting content to on-demand detail or Evidence. Runtime content is never silently truncated.

### 5.3 Canonical serialization

Top-level order is `schema`, `semantics`, `revision`, `work`, `knowledge`, `relations`. Work groups order as `objectives`, `attempts`. Knowledge groups order as `entities`, `facts`, then the project-valid `findings` or `solutions`, then `evidence`. Keys sort lexicographically. Relationships sort by source, type, target, then reason. The same semantic state produces identical compact JSON bytes.

The exact bytes are the Continuation input and may be internally hashed for pin integrity. That hash is never part of Runtime content.

## 6. Launch, long execution, and parallel Tasks

Each Runtime Continuation has:

- an immutable internal Launch Blackboard Pin containing its exact starting snapshot bytes;
- a Runtime-readable Working Blackboard Snapshot at `.pentest/blackboard.json`;
- a server-owned last-acknowledged Blackboard revision.

The Working Snapshot begins at the Launch Pin. It advances atomically after the Runtime's successful semantic writes. After context compaction or a long execution, the Runtime rereads this file instead of relying on remembered prompt content. Resume creates a new Continuation, Launch Pin, and Working Snapshot from current Project state.

When another Task advances the same Project:

1. active Continuations receive one coalesced Blackboard Change Notice containing only last-acknowledged and current revisions;
2. changes are not asynchronously injected into model context;
3. the next trusted Blackboard tool or checkpoint response piggybacks the complete current `runtime-blackboard/v2` plus a concise explanation that another Task changed shared Project knowledge;
4. the service atomically replaces the Working Snapshot and acknowledges that revision.

With no unseen external change, a successful write returns only its semantic delta and the Working Snapshot path/revision.

The model-visible launch header contains only Runner, Scope path, Blackboard path, snapshot schema, and pinned revision. Internal Project, Task, Continuation, Runtime Configuration/Profile/Plugin IDs, service URLs, hashes, sizes, token estimates, and protocol digests remain outside model attention.

The Runtime receives this checklist once through the adapter's reliable persistent instruction channel, or once in the launch prefix when no such channel exists:

1. Reread Scope and Blackboard before planning, after context compaction, and after resume.
2. Write semantic milestones only; commands, logs, and raw output stay outside Blackboard.
3. Use Blackboard Keys and current versions; reuse the same idempotency key after an uncertain retry.
4. Exploration flows through an open Attempt, reusable outcome records, and a terminal Attempt.
5. Blackboard scope labels never authorize testing; Finish only after every Attempt is terminal.

## 7. Semantic reads

Startup context comes only from `runtime-blackboard/v2`. There is no separate `list_project_facts` or Fact Index protocol.

A current read by key returns a pure semantic DTO containing:

- schema, observed Blackboard revision, canonical key, record type, and record version;
- the complete type-specific semantic record;
- current relationships by Blackboard Key.

It may return Fact body, Finding proof/impact/recommendation/CVSS fields, or Evidence path/integrity details. It never returns internal IDs, Project/Task/Continuation identifiers, Trusted Origin, storage hashes unrelated to Evidence integrity, or non-semantic lifecycle timestamps.

Current detail and Semantic History are separate operations. History returns prior semantic versions and terminal workflow records only after explicit request. Trusted Origin inspection is an advanced diagnostic outside the Runtime and ordinary Blackboard contracts.

## 8. Semantic writes

All Runtime and operator writes use one atomic `semantic-change-batch/v2` envelope with one idempotency key and an ordered flat list of changes. The permitted verbs are:

| Verb | Meaning |
| --- | --- |
| `create` | Create a typed record at a caller-chosen Blackboard Key |
| `update` | Partially update one current record using its current version |
| `transition` | Apply one legal semantic lifecycle transition using its current version |
| `relate` | Create or version a relationship using Blackboard Keys |
| `unrelate` | Retire a current relationship using its current version |
| `merge` | Consolidate duplicate same-type Project Knowledge with both current versions |
| `supersede` | Atomically relate a replacement and transition the replaced record |

### 8.1 Closed change shapes

The canonical service/MCP/CLI request is:

```json
{
  "schema": "semantic-change-batch/v2",
  "idempotency_key": "caller-chosen-retry-key",
  "changes": []
}
```

Each item in `changes` is a closed object selected by `op`:

| `op` | Required fields | Optional fields |
| --- | --- | --- |
| `create` | `key`, `type`, complete typed `record` | none |
| `update` | `key`, current `version`, `type`, closed typed partial `record` | `clear` list of optional field names |
| `transition` | `key`, current `version`, `status` | exactly the type-valid concise terminal/verification summary field |
| `relate` | `from`, `relation`, `to` | current relationship `version` when changing an existing reason; `reason` only for the three allowed types |
| `unrelate` | `from`, `relation`, `to`, current relationship `version` | none |
| `merge` | `source`, `source_version`, `canonical`, `canonical_version` | closed typed `canonical_record` partial update and `clear` |
| `supersede` | `replacement`, `replacement_version`, `replaced`, `replaced_version` | none |

`replacement_version` is omitted only when the replacement was created earlier in the same batch; it is then version 1. `relate` creates when absent, is an exact no-op when the same current relationship already exists, and requires the current version to change an existing reason. `supersedes` relationships are created only through `supersede`, not ordinary `relate`.

`type` is one of `entity`, `objective`, `attempt`, `fact`, `finding`, `solution`, or `evidence`. A `record` is a discriminated closed semantic object, never an arbitrary property map. Unknown top-level or change fields are rejected.

There are no operation IDs, node/edge IDs, nested references, arbitrary property maps, caller-supplied Project/context/provenance, or storage mutation kinds. A create's chosen key is immediately usable by later changes in the same batch. Existing semantic state changes carry current versions.

The server validates the whole final batch state, including type schemas, lifecycle guards, relationship endpoints, cycles, Project isolation, optimistic versions, and trusted Continuation authority. Failure is atomic.

A successful response has schema `semantic-change-result/v2`, the resulting `revision`, `records` as sorted `[key, version]` pairs, `relations` as sorted `[from, relation, to, version]` tuples, and `working_snapshot` containing only `.pentest/blackboard.json` plus its revision. It omits mutation sequence, timestamps, hashes, internal IDs, and actor/context data. A version conflict identifies the canonical key or relationship, expected/current versions, and the next semantic action. An idempotent replay returns the original semantic result; reuse of the key with different semantics fails.

If unseen external changes are pending, the successful trusted response additionally carries the complete current Runtime Blackboard Snapshot and synchronization explanation described in section 6.

### 8.2 Shared synchronization attachment

Every authenticated trusted response, including a semantic validation error, may add:

```json
{
  "sync": {
    "reason": "another_task_changed_shared_project_knowledge",
    "from_revision": 40,
    "revision": 42,
    "snapshot": {}
  }
}
```

The Snapshot is the exact canonical `runtime-blackboard/v2` object. The attachment is added only after Project/Continuation authentication succeeds. Delivering it atomically replaces the Working Snapshot and acknowledges `revision`; an unauthenticated response never exposes it.

### 8.3 Trusted tool and CLI catalog

Runtime adapters expose exactly these trusted MCP tools:

| MCP tool | Semantic input/result |
| --- | --- |
| `blackboard_change` | the batch and result in sections 8.1–8.2 |
| `blackboard_read` | one `key`; returns `blackboard-record/v2` current detail |
| `blackboard_history` | one `key`, optional opaque `cursor`, optional `limit`; returns `semantic-history/v2` |
| `blackboard_retain_evidence` | confined Evidence request below; returns a semantic change result |
| `blackboard_checkpoint_attempt` | Attempt checkpoint request below; returns a semantic change result |
| `blackboard_finish` | finish request below; returns `continuation-finish/v2` |

There is no trusted `get_current_graph` or Fact-list tool. The complete Snapshot is the Working Snapshot and is piggybacked on synchronization.

CLI Fallback mirrors the catalog as `blackboard change`, `blackboard read`, `blackboard history`, `blackboard evidence retain`, `blackboard attempt checkpoint`, and `blackboard continuation finish`. Operator CLI calls add Project selection outside the semantic payload; Runtime MCP calls never accept Project, Task, or Continuation identity.

The Evidence retain request contains `idempotency_key`, Evidence `key`, optional current Evidence `version`, producing `attempt` key for Runtime calls, confined `source_path`, `artifact_type`, `summary`, optional `media_type`, optional semantic `captured_at`, and optional `links` as `[relation, target_key]` pairs. Links are limited to `evidences` and `about`; the service derives `produced` from the Attempt. Runtime creation requires an open Attempt. An exact replay remains valid after the Attempt becomes terminal. Managed path, digest, size, Project, and origin are server-derived.

The checkpoint request contains only `idempotency_key`, Attempt `key`, current Attempt `version`, and the new compact `summary`. It versions the Attempt summary, may add a task-local checkpoint marker outside Blackboard, and participates in synchronization. A Runtime cannot checkpoint an Attempt it does not own or one already terminal, except by exact replay.

The finish request contains only `idempotency_key`. It accepts no summary or outcome copy. It succeeds only when every Attempt owned by the current Continuation is terminal, applies any pending synchronization attachment, closes later writes, and returns `schema`, `status: "finished"`, current Blackboard `revision`, and `working_snapshot`.

### 8.4 HTTP contract

Same-repository operator/UI and grant-authenticated Runtime adapters use path-versioned HTTP v2:

| Method and path | Behavior |
| --- | --- |
| `POST /api/v2/projects/{project_id}/blackboard/changes` | atomic Semantic Change Batch |
| `GET /api/v2/projects/{project_id}/blackboard/snapshot` | complete current Runtime Snapshot |
| `GET /api/v2/projects/{project_id}/blackboard/records/{key}` | current semantic detail |
| `GET /api/v2/projects/{project_id}/blackboard/records/{key}/history` | paginated Semantic History |
| `POST /api/v2/projects/{project_id}/blackboard/evidence:retain` | confined Evidence retention |
| `POST /api/v2/projects/{project_id}/blackboard/attempts/{key}:checkpoint` | Attempt checkpoint |
| `POST /api/v2/projects/{project_id}/continuation:finish` | finish bound Continuation |

Runtime HTTP uses a Continuation Interface Grant bearer token; operator/UI uses daemon authentication and an operator identity. The server verifies that path Project and grant Project match. V2 does not accept bearer credentials in query strings. MCP transport authentication remains adapter-owned and never becomes caller-supplied semantic identity.

HTTP requires `Idempotency-Key` on every POST and maps it into the canonical request; MCP/CLI carry `idempotency_key` directly. GET Snapshot and detail responses use a revision-based ETag and honor `If-None-Match`. Snapshot is deliberately unpaginated because completeness is its contract. History uses an opaque cursor, default limit 20, maximum 100, and returns `next_cursor` only when more items exist.

All HTTP errors use one envelope:

```json
{
  "error": {
    "code": "version_conflict",
    "message": "human-readable explanation",
    "path": "changes[0].version",
    "retryable": false,
    "details": {}
  }
}
```

Malformed JSON/schema uses 400; missing/invalid authentication 401; authenticated authority failure 403; absent Project/key 404; closed Continuation 410; version, key, relationship, or idempotency conflicts 409; semantic validation, lifecycle guards, endpoint types, cycles, and size limits 422; storage contention 503 with `Retry-After`; unexpected faults 500. There is no 200 response containing an error. Authenticated semantic errors may carry the shared `sync` sibling from section 8.2.

Breaking changes require a new URL/schema version. Additive storage fields never enter Runtime Snapshot or tool allowlists until deliberately admitted to this contract and its conformance fixtures.

## 9. Persistence and internal control state

The canonical store is versioned current semantic state, not an event ledger. It retains:

- current typed records and relationships;
- prior per-record and per-relationship semantic versions;
- one monotonic Blackboard revision per Project;
- Blackboard Key Redirects;
- exact Launch Pin bytes and minimal pin integrity data;
- idempotency receipts;
- the minimum Trusted Origin and Continuation binding needed for Project isolation, interrupted Attempt reconciliation, Evidence integrity, and corruption/concurrency diagnosis.

It does not retain an append-only operation ledger, source-event joins, operation hash chain, full historical graph revisions, caller-visible immutable graph IDs, or audit-first current projections.

Semantic History is retained by default and pruned only through an explicit safe operation. A prune cannot remove state referenced by an active Launch Pin or required by a Blackboard Key Redirect. Evidence payload retention is governed separately.

## 10. Attention budget and consolidation

Budgeting measures the exact deterministic Runtime Snapshot bytes only. It excludes history, Trusted Origin, operator DTOs, detail responses, and storage metadata.

- 16K tokens: healthy target.
- 32K tokens: warning and offer consolidation.
- 64K tokens: consolidation required indicator.

Every threshold remains launchable and complete. The system never truncates, relevance-filters, or blocks startup. An explicitly requested Reason Task may propose merges, Fact refinement, supersession, summary tightening, and relationship cleanup. The operator approves every semantic mutation.

## 11. Ordinary UI and reports

The ordinary Blackboard UI uses the same Current Work, Project Knowledge, and relationship model as the Runtime. Lists show the snapshot allowlist; selection loads semantic detail by key; Semantic History is an explicit secondary action. Graph Explorer renders current records and the closed relationship vocabulary using Blackboard Keys.

Normal pages do not show Provenance JSON, internal IDs, graph/storage hashes, source events, or Recent Changes audit streams. Health surfaces attention budget, semantic integrity, stranded work, invalid relationships, missing Evidence, and actionable anomalies. Advanced Trusted Origin or database inspection is outside the Blackboard UI.

Reports use current Findings, Facts, relationships, and Evidence. They present conclusions and key evidence, not execution-origin metadata. Tentative Facts and unconfirmed Findings remain visibly distinct from confirmed conclusions.

## 12. Atomic v1-to-v2 cutover

Migration uses three offline CLI operations:

- `pentestctl --db DB blackboard v2 inspect --artifact-root ROOT --output PLAN.json`
- `pentestctl --db DB blackboard v2 migrate --plan PLAN.json --backup BACKUP.db --artifact-root ROOT`
- `pentestctl --db DB blackboard v2 verify --artifact-root ROOT`

`inspect` is read-only. It emits `blackboard-v2-migration-plan/v1` with a source digest, deterministic per-Project proposed mappings, validation blockers, and `required_decisions`. Each decision identifies its source by Project plus v1 semantic type/key, lists closed allowed actions, and has operator-editable `decision` and optional target key. It never requires an internal node ID. Hypothesis actions are `objective`, `tentative_fact`, or `discard`; active Directive actions are `scope_limit` or `objective`; ambiguous Observation confidence requires `tentative_fact` or `confirmed_fact`. Discard is available only when the source has no reusable meaning under the accepted migration rules.

`migrate` rejects a changed source digest, missing decision, unknown action, active Continuation, invalid backup, or non-conforming generated Snapshot before epoch switch. Its result schema is `blackboard-v2-migration-result/v1` and reports status, verified backup path, Project counts/revisions, validation outcome, and resulting `blackboard_v2` epoch without exposing migrated storage IDs. `verify` reopens the cut-over database and revalidates store epoch, Project isolation, redirects, semantic invariants, Evidence paths/integrity, and exact Snapshot bytes.

Migration is offline, backup-first, and atomic per database:

1. stop Task launch and require no active Runtime Continuation;
2. create and verify a full SQLite backup;
3. rebuild each Project from current v1 heads into fresh v2 semantic-state tables;
4. remove copied Goal records and Goal-only relationships;
5. map Observations to Facts; map active Hypotheses conservatively to open Objectives or tentative Facts;
6. require operator classification for active Project Directives into Scope/testing limits or Objectives;
7. map reusable terminal summaries lacking outcomes to conservative tentative Facts and place terminal workflow records in Semantic History;
8. rewrite all identities to project-wide Blackboard Keys, resolving cross-type collisions and opaque keys without long-lived migration aliases;
9. reverse `blocks` into `depends_on`, remove `leads_to`, and validate the v2 endpoint matrix;
10. generate and validate every Project's exact `runtime-blackboard/v2` bytes;
11. atomically switch the store epoch to v2.

There is no long-lived v1 compatibility API, dual read, or dual write. Failure before epoch switch leaves v1 authoritative and the verified backup available. Post-cutover consolidation is explicit and approval-required.

## 13. Clean replacement boundary

The implementation replaces Blackboard v1 rather than adapting it. V1-specific ledger, hash/integrity replay, provenance views, Goal/frontier projection, removed record types, compatibility service, v1 protocol, and old UI/tests are deleted through the TDD slices.

Shared Project, Task, Scope, Artifact/Evidence file management, Report delivery, Runner, Runtime Profile, Continuation grant, and trusted-MCP configuration capabilities remain and are adapted to the v2 semantic contracts. The Claude trusted-MCP allowlist mechanism in the current worktree is preserved while its v1 tool list is replaced.
