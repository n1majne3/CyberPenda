# Blackboard Read Projections, Reports, and Operator UI Contract

- **Status:** implementation contract for [Specify Blackboard read projections, reports, and operator UI](https://github.com/n1majne3/CyberPenda/issues/59)
- **Map:** [Map: Refactor Blackboard into bounded graph memory](https://github.com/n1majne3/CyberPenda/issues/55)
- **Depends on:** [Blackboard Typed Property Graph Contract](./blackboard-graph-contract.md), [Blackboard SQLite Persistence, History, Compaction, and Health Contract](./blackboard-graph-storage.md), and [Blackboard Runtime and Project Interface Protocol Contract](./blackboard-runtime-protocol.md)
- **Read protocol:** BlackboardReadV1
- **Canonical Runtime projection:** CanonicalMainGraphV1

## 1. Decision summary

All graph-backed operator, report, and compatibility reads pass through one revision-aware Blackboard read module. HTTP handlers, the Web UI, operator CLI commands, report renderers, and legacy adapters do not query graph tables or reconstruct relationships independently.

The primary Blackboard interface is a dense work view: summary strip, facet rail, sortable ledger, and record inspector. It is optimized for deciding what matters next and for editing through the existing mutation interface. Entity browsing is a dedicated hierarchy-and-table view. Provenance and history are first-class inspector views. A visual Graph Explorer is secondary and always has an equivalent list/table representation; a force-directed canvas is never the only way to understand or navigate the Blackboard.

The required deterministic projections are:

1. the existing full CanonicalMainGraphV1 Runtime context;
2. Blackboard Work View and project-dashboard summary;
3. Current Truth and Exploration Frontier;
4. typed record collections and point details;
5. Entity hierarchy and Entity-centered records;
6. lineage, neighborhood, version history, and provenance;
7. Blackboard Health summary, runs, and results;
8. Pentest Report semantic model plus Markdown renderer;
9. CTF Solution semantic model plus Markdown renderer;
10. a bounded Graph Explorer projection;
11. legacy Fact, Finding, Evidence, report, MCP, CLI, and UI compatibility projections.

Reports and CTF outputs are derived views, not graph nodes or independent stores. Blackboard Health remains derived operational diagnosis. No read projection grants scope authorization, changes Frontier, repins a Continuation, repairs state, or mutates graph records.

## 2. Scope and boundaries

This contract fixes:

- the shared read-module seam;
- snapshot, revision, alias, merge, pagination, filtering, ordering, and error rules;
- operator HTTP response shapes and corresponding operator CLI reads;
- information architecture for Project Dashboard, Blackboard Work, Entities, record inspection, Health, reports, CTF output, and Graph Explorer;
- legacy read translation semantics;
- deterministic report and CTF rendering;
- TDD acceptance seams for all read behavior.

This contract does not change:

- graph node, edge, lifecycle, provenance, merge, or archive rules;
- CanonicalMainGraphV1 bytes, budget thresholds, compaction policy, or Health detector definitions;
- the six trusted Runtime capabilities or their MCP/CLI/HTTP names;
- Task, Continuation, Scope, Task Summary, or artifact-payload ownership;
- migration transaction and cutover sequencing;
- implementation slice order.

Mutation controls shown by the UI call BlackboardGraphService.Apply or the compound project-interface operations from the Runtime protocol. Read handlers never write graph tables. Starting a Health run writes only the derived Health cache through the Health checker; it does not mutate semantic graph state.

## 3. Deep read module

The external seam is one module:

~~~go
type BlackboardReadService interface {
    Read(ctx context.Context, request ReadRequest) (ReadEnvelope, error)
}
~~~

ReadRequest is a closed, versioned union whose kinds correspond to the projections in section 7. Each kind has a typed request and result. Transport adapters may expose convenient typed functions, but they all call this seam.

The module owns:

- one SQLite snapshot transaction per request;
- current or historical graph reconstruction;
- Project isolation;
- alias and merged-identity resolution;
- literal historical identity access when explicitly requested;
- graph-schema-to-read-model translation;
- deterministic filtering, search, sorting, pagination, facets, and hashes;
- joins to durable Task, Continuation, Scope Snapshot, Runtime configuration, Task Event metadata, and Health cache where a projection requires them;
- legacy shape translation;
- report and CTF semantic-model assembly.

The module does not expose a table-shaped repository interface. Production and behavioral tests use the real SQLite adapter. Tests exercise the read seam and compare independent golden fixtures.

The deletion test applies: removing this module would force snapshot consistency, alias redirects, historical reconstruction, cursor stability, report selection, provenance joins, legacy translation, and deterministic ordering into every handler and page.

## 4. Common read contract

### 4.1 Snapshot and revision

Every read:

1. authenticates and resolves one Project;
2. opens one SQLite read transaction;
3. resolves the requested graph revision, defaulting to current;
4. reads all graph and joined durable state from that transaction;
5. returns the observed graph revision and state hash.

Current reads MAY observe a newer revision than a Continuation's pinned snapshot. They never repin or rewrite that snapshot.

Operator reads accept optional at_revision. Historical reads reconstruct exact full-state versions under the storage contract. at_revision greater than current returns revision_not_found. A revision that cannot be reconstructed returns snapshot_unavailable.

### 4.2 Common envelope

Every new canonical JSON operator projection returns:

~~~json
{
  "protocol_version": 1,
  "projection": "blackboard_work_view_v1",
  "project_id": "project-id",
  "project_kind": "pentest",
  "observed_graph_revision": 42,
  "observed_state_hash": "sha256",
  "source_pins": {},
  "projection_hash": "sha256",
  "result": {}
}
~~~

Rules:

- protocol_version is the transport/read protocol version.
- projection names the exact response renderer.
- project_kind is pentest or ctf_challenge.
- observed_state_hash is the semantic state hash for the observed revision.
- source_pins names non-graph immutable inputs required to reproduce the projection, such as a Health run ID or selected Scope Snapshot ID; it is empty when graph state alone is sufficient.
- projection_hash is H("CyberPenda.Blackboard.ReadProjection.v1", canonical JSON of protocol_version, projection, Project identity/kind, observed revision/state hash, source_pins, and result). The projection_hash field itself, transport request IDs, and HTTP headers are excluded.
- source record timestamps remain present where specified.
- no generated_at value is added merely because a read occurred.
- Health run timestamps are persisted source data and therefore remain visible.
- report bytes do not contain the wall-clock render time; the HTTP Date header may.

HTTP GET responses use projection_hash as a strong ETag and honor If-None-Match with 304. Current-state reads use Cache-Control: private, no-cache. A fixed historical at_revision response may use Cache-Control: private, max-age=31536000, immutable only when every non-graph source pin is itself immutable. Projections joining current Project Scope or other mutable state remain private, no-cache.

Existing Dashboard and legacy compatibility routes preserve their top-level response shapes. They carry the same metadata in an additive _read object plus ETag/headers:

~~~json
{
  "_read": {
    "protocol_version": 1,
    "projection": "project_blackboard_summary_v1",
    "observed_graph_revision": 42,
    "observed_state_hash": "sha256",
    "source_pins": {
      "health_run_id": "run-id"
    },
    "projection_hash": "sha256"
  }
}
~~~

PentestReportV1 and CTFSolutionV1 JSON are the result object inside the common envelope. Their Markdown forms use the exact byte and header contract in their sections. The Health-run POST action uses the action response specified in section 14.

### 4.3 Errors

Canonical reads reuse ProjectInterfaceErrorV1 from the Runtime protocol:

~~~json
{
  "error": {
    "protocol_version": 1,
    "code": "invalid_cursor",
    "message": "The cursor does not match this query.",
    "operation_index": null,
    "op_id": null,
    "path": "cursor",
    "retryable": false,
    "details": {},
    "request_id": "request-id"
  }
}
~~~

Additional stable read codes are:

| Code | HTTP | Meaning |
| --- | ---: | --- |
| invalid_query | 400 | Unknown filter, invalid combination, or malformed search. |
| invalid_cursor | 400 | Cursor is malformed or belongs to another query/projection. |
| revision_not_found | 404 | Requested graph revision does not exist. |
| record_not_found | 404 | No matching identity, stable key, alias, or redirect exists. |
| literal_identity_required | 409 | A history request would redirect unless literal identity was explicitly selected. |
| projection_too_large | 422 | A bounded traversal or Explorer request cannot fit its explicit limits. |
| project_kind_mismatch | 422 | Pentest-only or CTF-only projection requested for the other Project kind. |
| health_run_not_found | 404 | Requested Health run does not exist for the Project. |
| health_run_in_progress | 409 | A request requires a completed Health run. |
| snapshot_unavailable | 503 | Required graph or joined durable state cannot be reconstructed. |

Authentication and authorization use the existing daemon operator credential. Runtime Interface Grants can call only the Runtime reads fixed by the Runtime protocol; they do not gain general operator search, history, Health, report, or Graph Explorer access.

### 4.4 Pagination

Every potentially unbounded collection is cursor-paginated:

~~~json
{
  "items": [],
  "page": {
    "limit": 50,
    "total_items": 137,
    "next_cursor": "opaque-or-null"
  }
}
~~~

Rules:

- default limit is 50; maximum is 200;
- cursors are opaque, authenticated encodings of projection version, Project ID, observed graph revision, non-graph source pins, normalized filters, sort definition, and final sort tuple;
- following a cursor continues at its original graph revision even if the current graph advances;
- historical reconstruction therefore gives stable, duplicate-free pages under concurrent writes;
- changing any filter, sort, limit, or Project while reusing a cursor returns invalid_cursor;
- a cursor is invalidated only when its projection version is no longer supported; if its graph revision or pinned derived source such as a pruned Health run is unavailable, the read returns snapshot_unavailable;
- total_items is exact at the pinned revision;
- null next_cursor means the collection is complete.

### 4.5 Search

Search is deterministic and lexical in version 1. It does not use embeddings, model inference, or semantic-similarity ranking.

Normalization is Unicode NFKC, Unicode lowercase, whitespace collapse, and trim. Search fields are:

- stable key;
- Entity name and locator;
- Objective text;
- Observation summary and detail;
- Hypothesis statement and rationale;
- ProjectFact category, summary, and body;
- Finding title, target, description, proof, impact, and recommendation;
- Solution summary and value;
- EvidenceArtifact summary, managed-path basename, media type, and digest;
- ProjectDirective text and rationale.

Results rank by exact stable-key/locator match, key/locator prefix, exact primary-label match, primary-label prefix, token conjunction, then token disjunction. Ties use node-type ordinal, stable key, and immutable ID. Search never inspects artifact payload bytes, raw logs, Task transcripts, or secret Credential values.

## 5. Shared read shapes

### 5.1 NodeRefV1

~~~json
{
  "id": "node-id",
  "node_type": "finding",
  "stable_key": "finding:sqli-login",
  "label": "SQL injection in login"
}
~~~

label is deterministic by type:

- Goal: text;
- Entity: name, followed by locator when distinct;
- ExplorationObjective: objective;
- Attempt: summary when present, otherwise stable key;
- Observation: summary;
- Hypothesis: statement;
- ProjectFact: summary;
- Finding: title;
- Solution: summary;
- EvidenceArtifact: summary;
- ProjectDirective: directive.

### 5.2 NodeRowV1

~~~json
{
  "ref": {},
  "version": 3,
  "disposition": "main",
  "lifecycle": {
    "field": "status",
    "value": "confirmed"
  },
  "scope_status": "in_scope",
  "severity": "critical",
  "secondary": "https://example.com/login",
  "updated_at": "RFC3339Nano",
  "about_entities": [],
  "relationship_counts": {
    "about_entities": 1,
    "incoming": 2,
    "outgoing": 3,
    "evidence": 1,
    "contradictions": 0
  },
  "updated_provenance": {}
}
~~~

Fields that do not apply are explicit nulls in canonical JSON. lifecycle names the type-specific lifecycle field: task_status, status, confidence, or disposition. secondary is a compact type-specific secondary label and never contains a full body or secret. about_entities contains at most three NodeRefs; relationship_counts carries the exact count.

### 5.3 ProvenanceSummaryV1

~~~json
{
  "actor_type": "runtime",
  "actor_id": "runtime:codex:continuation-id",
  "task_id": "task-id",
  "continuation_id": "continuation-id",
  "runtime_profile_id": "captured-profile-id",
  "runner": "sandbox",
  "source_event_count": 2,
  "migration_source": null,
  "recorded_at": "RFC3339Nano"
}
~~~

The compact shape counts source Events but does not inline them. Full source Event metadata is available only through the provenance projection.

### 5.4 EdgeRowV1

~~~json
{
  "id": "edge-id",
  "edge_type": "evidences",
  "from": {},
  "to": {},
  "version": 1,
  "state": "active",
  "summary": "Captured response proves authentication bypass",
  "updated_at": "RFC3339Nano",
  "updated_provenance": {}
}
~~~

Direction is always preserved. UI labels may explain direction in prose but cannot reverse endpoints.

## 6. Deterministic orderings

The graph contract deliberately leaves Frontier ordering open. This read contract fixes all v1 presentation orderings.

### 6.1 Frontier

Frontier Objectives sort by:

1. parent Goal Task status rank: running, paused, pending, no parent Goal, completed, failed, stopped, interrupted;
2. Objective created_at ascending;
3. stable key ascending;
4. immutable ID ascending.

The response includes rank starting at 1. Rank is derived only for the returned revision and is not persisted or a scheduler claim.

When an Objective has multiple parent Goals, it uses the earliest rank represented by any parent Task status. The complete parent Goal list remains visible in the item.

### 6.2 Current Truth

ProjectFacts sort by:

1. confidence: confirmed before tentative;
2. scope status: in_scope, unknown, out_of_scope;
3. category ascending;
4. stable key ascending;
5. immutable ID ascending.

### 6.3 Findings

Findings sort by:

1. status: confirmed, unconfirmed, false_positive;
2. severity: critical, high, medium, low, none/pending;
3. target ascending;
4. title ascending;
5. stable key ascending;
6. immutable ID ascending.

### 6.4 Evidence and Solutions

Evidence sorts by status available, missing, superseded; then captured_at descending with null last; stable key; ID.

Solutions sort by status verified, candidate, rejected, superseded; kind flag, answer, procedure; stable key; ID.

### 6.5 General ledger

The Work ledger default attention order is:

1. critical Health subjects;
2. warning Health subjects;
3. open Frontier Objectives;
4. open Attempts;
5. confirmed Findings;
6. verified or candidate Solutions;
7. confirmed Current Truth;
8. tentative Current Truth;
9. open/supported/contradicted Hypotheses;
10. recent Observations;
11. Evidence;
12. Entities;
13. active or proposed Directives;
14. terminal or historical records when included.

Within a class: updated_at descending, node-type ordinal, stable key, ID. The UI may offer other documented sorts, but no adapter invents its own default.

## 7. Full Runtime context and projection inventory

### 7.1 Full Runtime context presentation

CanonicalMainGraphV1 remains the only semantic full-graph source for a Continuation. The exact bytes are pinned in .pentest/blackboard.json and returned by the existing current Runtime graph operation.

The adapter launch-context renderer is RuntimeBlackboardContextV1. It is a deterministic, lossless text rendering of every CanonicalMainGraphV1 field:

1. renderer version, Project kind, graph revision, projection hash, bytes, and estimated tokens;
2. Frontier stable keys/IDs in the CanonicalMainGraphV1 stable node order;
3. Current Truth stable keys/IDs in the CanonicalMainGraphV1 stable node order;
4. every node grouped by canonical node-type ordinal and ordered stable key then ID;
5. every node envelope field, every type-specific property, and compact created/updated provenance;
6. every edge grouped by edge-type ordinal and ordered from-node tuple, to-node tuple, then edge ID;
7. explicit end marker containing the projection hash.

The renderer uses LF newlines, deterministic JSON scalar escaping, explicit null, and no wall-clock render timestamp. It MUST NOT summarize, omit bodies, drop provenance, choose relevant records, or replace values with prose. Its rendered hash is stored beside the canonical projection hash in launch diagnostics.

RuntimeBlackboardContextV1 may be larger than the canonical JSON. Its byte/token measurement is diagnostic only; CanonicalMainGraphV1 remains the sole budget and compaction input.

The launch context may wrap the rendering in Runtime-specific delimiters, but the Runtime Plugin adapter must prove by conformance fixture that parsing the RuntimeBlackboardContextV1 block reconstructs the exact CanonicalMainGraphV1 document. Generated AGENTS.md and CLAUDE.md reference the snapshot and protocol; they do not duplicate the complete graph.

On native resume, the new Continuation receives a newly pinned RuntimeBlackboardContextV1 block clearly marked CURRENT CONTINUATION SNAPSHOT. Older blocks remain historical. Explicit current-graph reads continue to return CanonicalMainGraphV1 JSON, not the prompt text renderer.

### 7.2 Operator projection routes

Route references beginning with /blackboard or /reports in later sections are shorthand relative to /api/projects/{project_id}.

The CanonicalMainGraphV1 route is the existing Runtime-protocol operation and retains its request_kind/current-graph envelope. Every other row below is an operator BlackboardReadV1 projection.

| Projection | Canonical HTTP route | Primary consumer |
| --- | --- | --- |
| CanonicalMainGraphV1 | GET /api/projects/{project_id}/blackboard/runtime-graph | Runtime explicit read |
| ProjectBlackboardSummaryV1 | GET /api/projects/{project_id}/dashboard | Project Dashboard |
| BlackboardWorkViewV1 | GET /api/projects/{project_id}/blackboard/work-view | Blackboard initial screen |
| CurrentTruthV1 | GET /api/projects/{project_id}/blackboard/current-truth | Runtime/operator context inspection |
| ExplorationFrontierV1 | GET /api/projects/{project_id}/blackboard/frontier | Next-work view |
| RecordCollectionV1 | GET /api/projects/{project_id}/blackboard/records | Ledger and search |
| RecordDetailV1 | GET /api/projects/{project_id}/blackboard/records/{node_id} | Inspector |
| RecordHistoryV1 | GET /api/projects/{project_id}/blackboard/records/{node_id}/history | Audit/history |
| RecordProvenanceV1 | GET /api/projects/{project_id}/blackboard/records/{node_id}/provenance | Source traversal |
| GraphTraversalV1 | GET /api/projects/{project_id}/blackboard/records/{node_id}/traversal | Neighborhood/lineage |
| EntityCollectionV1 | GET /api/projects/{project_id}/blackboard/entities | Entity browser |
| EntityDetailV1 | GET /api/projects/{project_id}/blackboard/entities/{node_id} | Entity inspector |
| GraphExplorerV1 | GET /api/projects/{project_id}/blackboard/graph-explorer | Secondary visual explorer |
| BlackboardHealthSummaryV1 | GET /api/projects/{project_id}/blackboard/health | Status strip/dashboard |
| BlackboardHealthRunV1 | GET /api/projects/{project_id}/blackboard/health-runs/{run_id} | Health run status |
| BlackboardHealthResultsV1 | GET /api/projects/{project_id}/blackboard/health-runs/{run_id}/results | Health detail |
| PentestReportV1 | GET /api/projects/{project_id}/reports/pentest | Pentest report UI/export |
| CTFSolutionV1 | GET /api/projects/{project_id}/reports/ctf-solution | CTF result UI/export |

An explicit Health scan is started by POST /api/projects/{project_id}/blackboard/health-runs as specified in section 14.

Operator CLI commands mirror these reads:

- pentestctl blackboard work-view --project PROJECT;
- pentestctl blackboard records list|get|history|provenance|traversal;
- pentestctl blackboard frontier;
- pentestctl blackboard entities list|get;
- pentestctl blackboard graph-explorer;
- pentestctl blackboard health show|run|results;
- pentestctl report pentest;
- pentestctl report ctf-solution.

Offline --db and daemon --api modes call the same read module and return the same JSON. There are no new trusted Runtime MCP tools beyond the Runtime protocol.

## 8. Project Dashboard summary

ProjectBlackboardSummaryV1 extends the existing dashboard without turning it into the full work surface:

~~~json
{
  "_read": {
    "protocol_version": 1,
    "projection": "project_blackboard_summary_v1",
    "observed_graph_revision": 42,
    "observed_state_hash": "sha256",
    "source_pins": {
      "health_run_id": "run-id"
    },
    "projection_hash": "sha256"
  },
  "project_id": "project-id",
  "name": "Acme External",
  "project_kind": "pentest",
  "scope": {
    "ready": true,
    "domains": 1,
    "ips": 0,
    "cidrs": 0,
    "urls": 1,
    "ports": 1,
    "excluded": 1,
    "has_testing_limits": true,
    "has_notes": true
  },
  "tasks": {
    "total": 7,
    "running": 1,
    "paused": 0,
    "needs_attention": 1
  },
  "blackboard": {
    "observed_graph_revision": 42,
    "nodes_by_type": {},
    "current_truth": 18,
    "frontier": 3,
    "open_attempts": 1,
    "confirmed_findings": 2,
    "unconfirmed_findings": 1,
    "available_evidence": 6,
    "missing_evidence": 0,
    "budget_state": "within_target",
    "estimated_tokens": 7230
  },
  "health": {
    "status": "degraded",
    "stale": false,
    "critical": 0,
    "warning": 2,
    "info": 1,
    "latest_run_id": "run-id"
  },
  "ctf": null,
  "next_actions": []
}
~~~

next_actions is deterministic, rule-based, and contains at most five links. Its priority is: scope not ready; critical Health; compaction required; reconciliation stuck; missing Evidence supporting a confirmed Finding or verified Solution; Frontier stalled; open Frontier; unconfirmed Findings; no Tasks. It is not model-generated advice and never mutates state.

For a CTF Project, ctf is:

~~~json
{
  "solved": true,
  "verified_flag_count": 1,
  "candidate_solution_count": 0,
  "primary_solution": {}
}
~~~

The Dashboard remains the Project entry point for scope posture, active Tasks, high-signal Blackboard state, and a link into Work. It does not inline arbitrary graph records.

## 9. Blackboard Work View

### 9.1 Response

BlackboardWorkViewV1 provides one bounded initial payload:

~~~json
{
  "summary": {
    "graph_revision": 42,
    "node_counts": {},
    "edge_counts": {},
    "current_truth": 18,
    "frontier": 3,
    "open_attempts": 1,
    "confirmed_findings": 2,
    "unconfirmed_findings": 1,
    "verified_solutions": 0,
    "evidence_missing": 0,
    "budget": {
      "state": "within_target",
      "projection_bytes": 28920,
      "estimated_tokens": 7230,
      "target_tokens": 12000,
      "warning_tokens": 16000,
      "required_tokens": 20000
    },
    "health": {
      "status": "degraded",
      "stale": false,
      "critical": 0,
      "warning": 2,
      "info": 1,
      "latest_run_id": "run-id"
    }
  },
  "attention": {
    "items": [],
    "page": {
      "limit": 20,
      "total_items": 7,
      "next_cursor": null
    }
  },
  "frontier": {
    "items": [],
    "page": {
      "limit": 20,
      "total_items": 3,
      "next_cursor": null
    }
  },
  "active_attempts": {
    "items": [],
    "page": {
      "limit": 20,
      "total_items": 1,
      "next_cursor": null
    }
  },
  "recent_changes": {
    "items": [],
    "page": {
      "limit": 20,
      "total_items": 20,
      "next_cursor": "opaque"
    }
  },
  "facets": {
    "node_type": {},
    "lifecycle": {},
    "scope_status": {},
    "severity": {},
    "entity_kind": {},
    "actor_type": {}
  }
}
~~~

The four embedded collections are previews. Their cursors continue through RecordCollectionV1 with the normalized filters encoded in the cursor. The Work screen never requests the complete graph merely to render summary cards.

attention contains Health results mapped to subject NodeRefs plus stranded/frontier-stalled state that has no single subject. It does not duplicate all Frontier items.

recent_changes is ordered by the latest semantic node/edge version changes, not raw Task Events. Exact replay and graph no-op writes do not appear as semantic changes. Mutation audit remains inspectable through record history.

### 9.2 Information architecture

Desktop Work uses four persistent regions:

1. **Status strip:** revision, Health, budget, Current Truth, Frontier, active Attempts, Findings, and CTF solved state.
2. **Facet rail:** Work, Frontier, Current Truth, Objectives, Attempts, Observations, Hypotheses, Findings, Solutions when valid, Evidence, Directives, and All records.
3. **Dense ledger:** sortable, cursor-paginated rows using NodeRowV1.
4. **Inspector:** selected record detail, relationships, Evidence, provenance, history, and mutation actions.

The ledger is the primary navigation surface. It supports keyboard row navigation, stable deep links by immutable node ID, copyable stable keys, visible scope status, and text labels in addition to color. Rows do not require hover to reveal status or actions.

On narrow screens the facet rail becomes a filter sheet and the inspector becomes a full-height route/drawer. Selection remains URL-addressable.

The Work screen uses a restrained investigation-ledger visual language: compact typography, aligned columns, tabular numbers, clear separators, and severity/status accents. It avoids a dashboard made entirely of isolated cards.

## 10. Current Truth and Frontier

### 10.1 Current Truth

GET /blackboard/current-truth accepts:

- confidence=confirmed|tentative, repeatable;
- scope_status=in_scope|unknown|out_of_scope, repeatable;
- category;
- entity_id;
- query;
- at_revision;
- limit and cursor.

Each item is:

~~~json
{
  "fact": {},
  "category": "service",
  "summary": "Admin service is exposed on 443",
  "body": "semantic detail",
  "confidence": "confirmed",
  "scope_status": "in_scope",
  "support": {
    "evidence": {
      "items": [],
      "total_items": 1,
      "traversal_href": "/api/projects/project-id/blackboard/records/node-id/traversal?edge_type=evidences"
    },
    "supporting_records": {
      "items": [],
      "total_items": 2,
      "traversal_href": "/api/projects/project-id/blackboard/records/node-id/traversal?edge_type=supports"
    },
    "contradicting_records": {
      "items": [],
      "total_items": 0,
      "traversal_href": "/api/projects/project-id/blackboard/records/node-id/traversal?edge_type=contradicts"
    }
  }
}
~~~

body is included because this is an operator projection. The compact Runtime graph remains governed by CanonicalMainGraphV1. Out-of-scope facts always carry non_actionable=true.

Each support list is a preview of at most ten NodeRefs with an exact count and traversal link.

### 10.2 Frontier

GET /blackboard/frontier accepts parent_goal_id, entity_id, query, at_revision, limit, and cursor.

Each item is:

~~~json
{
  "rank": 1,
  "objective": {},
  "objective_text": "Can the admin endpoint be reached without authentication?",
  "parent_goals": [],
  "about_entities": [],
  "resolved_prerequisites": [],
  "open_attempts": [],
  "derived_reasons": [
    "objective_open",
    "all_dependencies_resolved",
    "all_blockers_resolved"
  ]
}
~~~

Frontier contains only Objectives satisfying the graph contract. blocked Objectives are available from RecordCollectionV1 with derived blockers in detail; they are never mixed into Frontier with a flag.

Frontier rank is presentation order, not priority persisted in the graph, a claim, a lease, or automatic Task scheduling.

parent_goals, about_entities, resolved_prerequisites, and open_attempts are bounded previews of at most 25 NodeRefs with exact counts and traversal/collection links in the response schema.

## 11. Record collections and detail

### 11.1 RecordCollectionV1

GET /blackboard/records is the shared dense-ledger and search endpoint. It accepts:

- node_type, repeatable;
- disposition=main by default; archived and merged require explicit inclusion;
- lifecycle value, repeatable;
- scope_status, severity, Entity kind, actor_type, task_id, continuation_id, runtime_profile_id, runner;
- about_entity_id;
- edge_type plus direction=incoming|outgoing|either;
- has_evidence, has_contradiction, frontier, health_severity;
- updated_before and updated_after;
- query;
- sort=attention|updated_desc|created_asc|stable_key|severity;
- at_revision, limit, and cursor.

The response contains NodeRowV1 items, exact facets for the normalized query before pagination, and page metadata. Unknown properties are not returned as ad hoc columns. Callers fetch RecordDetailV1 for complete type-specific properties.

Archived and merged records are excluded by default. include_disposition=merged returns literal merged rows with merge_target; it never redirects them. Ordinary stable-key searches still resolve aliases to the canonical current record and report the original query in resolution metadata.

### 11.2 Point addressing

The canonical point route uses immutable node ID. A convenience resolver accepts node_type plus stable_key:

GET /api/projects/{project_id}/blackboard/records:resolve?node_type=project_fact&stable_key=dns:example.com

It returns:

~~~json
{
  "requested": {
    "node_type": "project_fact",
    "stable_key": "dns:example.com"
  },
  "resolved": {},
  "resolved_from_alias": null,
  "resolved_from_merged_id": null
}
~~~

The resolver is an operator convenience over the same alias rules as ResolveRecordsV1. It is not a new Runtime capability.

### 11.3 RecordDetailV1

GET /blackboard/records/{node_id} returns:

~~~json
{
  "node": {
    "id": "node-id",
    "node_type": "finding",
    "stable_key": "finding:sqli-login",
    "version": 3,
    "disposition": "main",
    "properties": {},
    "created_at": "RFC3339Nano",
    "updated_at": "RFC3339Nano",
    "created_provenance": {},
    "updated_provenance": {},
    "merge_target": null
  },
  "derived": {
    "is_current_truth": false,
    "is_frontier": false,
    "ctf_solved_contributor": false,
    "non_actionable": false,
    "health": {
      "highest_severity": "warning",
      "result_count": 1
    }
  },
  "about_entities": {
    "items": [],
    "total_items": 1,
    "records_href": "/api/projects/project-id/blackboard/records?about_entity_id=entity-id"
  },
  "relationships": {
    "incoming": {
      "items": [],
      "total_items": 2,
      "traversal_href": "/api/projects/project-id/blackboard/records/node-id/traversal?direction=incoming&max_depth=1"
    },
    "outgoing": {
      "items": [],
      "total_items": 3,
      "traversal_href": "/api/projects/project-id/blackboard/records/node-id/traversal?direction=outgoing&max_depth=1"
    }
  },
  "evidence": {
    "items": [],
    "total_items": 1,
    "records_href": "/api/projects/project-id/blackboard/records?node_type=evidence_artifact&edge_type=evidences"
  },
  "support": {
    "supporting": {
      "items": [],
      "total_items": 2,
      "traversal_href": "/api/projects/project-id/blackboard/records/node-id/traversal?edge_type=supports"
    },
    "contradicting": {
      "items": [],
      "total_items": 0,
      "traversal_href": "/api/projects/project-id/blackboard/records/node-id/traversal?edge_type=contradicts"
    },
    "satisfies": {
      "items": [],
      "total_items": 0,
      "traversal_href": "/api/projects/project-id/blackboard/records/node-id/traversal?edge_type=satisfies"
    }
  },
  "history_summary": {
    "versions": 3,
    "first_mutation_seq": 12,
    "latest_mutation_seq": 40
  }
}
~~~

Relationship previews contain at most 25 incoming and 25 outgoing EdgeRowV1 items in edge-type ordinal, opposite-node type ordinal, opposite stable-key, edge-ID order. total_items and traversal_href make omitted relationships explicit.

Every embedded relationship collection contains at most 25 items, exact total_items, and a canonical continuation link. Evidence includes active EvidenceArtifact nodes connected by evidences, plus whether the selected record is itself Evidence. Merely sharing an Attempt does not make an artifact Evidence for the record.

When node_id names a merged identity, ordinary detail responds with the canonical target and sets resolved_from_merged_id. literal=true returns the immutable merged row, merge target, original stable key, and literal history. Archived identities are addressable without restore.

## 12. Entity browsing

EntityCollectionV1 treats Entity part_of as an acyclic directed hierarchy that may have multiple parents. It does not pretend the graph is a strict tree.

GET /blackboard/entities accepts:

- parent_id, where parent_id=root returns Entities with no active incoming Entity part_of edge;
- ancestor_id to return all descendants;
- kind, status, scope_status, query;
- include_counts=true by default;
- at_revision, limit, and cursor.

Each item is:

~~~json
{
  "entity": {},
  "kind": "service",
  "name": "Admin HTTPS",
  "locator": "host:example.com:443/tcp",
  "description": "Administrative web service",
  "scope_status": "in_scope",
  "status": "active",
  "parent_entities": [],
  "child_count": 2,
  "record_counts": {
    "objectives": 1,
    "attempts": 2,
    "facts": 4,
    "findings": 1,
    "solutions": 0,
    "evidence": 3
  },
  "highest_finding_severity": "critical",
  "health_severity": null
}
~~~

Default ordering is Entity kind ordinal, normalized name, locator, stable key, ID. root is a query value, not a synthetic Entity.

EntityDetailV1 adds:

- complete Entity node envelope;
- all direct parent and child Entity links;
- deterministic ancestor paths and descendant counts;
- records with active about edges, grouped by node type;
- inherited display breadcrumbs;
- Findings, Frontier Objectives, active Attempts, and Evidence concerning the Entity;
- provenance and Health summaries.

Direct parent/child links and each grouped record collection are bounded previews of at most 25 items with exact totals and continuation links to EntityCollectionV1, RecordCollectionV1, or GraphTraversalV1.

Breadcrumb rules:

- because multiple parents are allowed, every shortest root-to-Entity path is returned up to 20 paths;
- paths sort lexicographically by Entity kind ordinal, name, locator, stable key, and ID at each level;
- if more than 20 shortest paths exist, paths_truncated=true;
- breadcrumbs are explanatory navigation only and do not imply Scope inheritance or authorization.

The Entity screen presents a collapsible hierarchy beside a sortable Entity ledger. Selecting an Entity opens the standard inspector. Searching shows matching Entities with their shortest deterministic breadcrumbs. Credential Entities show credential_ref only; secret values are never read from the credential store into Blackboard projections.

## 13. History, provenance, and traversal

### 13.1 RecordHistoryV1

GET /blackboard/records/{node_id}/history accepts literal=true, before_version, limit, and cursor.

The default follows a merged identity to its canonical target. literal=true is required to inspect the source identity's own pre-merge history.

~~~json
{
  "identity": {},
  "resolved_canonical": {},
  "versions": {
    "items": [
      {
        "version": 3,
        "disposition": "main",
        "properties": {},
        "created_at": "RFC3339Nano",
        "provenance": {},
        "mutation": {
          "mutation_seq": 40,
          "graph_revision": 42,
          "mutation_kind": "normal",
          "idempotency_scope": "continuation:id",
          "idempotency_key": "checkpoint-3",
          "operation_index": 1
        },
        "semantic_hash": "sha256"
      }
    ],
    "page": {}
  },
  "key_history": [],
  "merge": null
}
~~~

Versions sort version descending. key_history includes stable and alias events relevant to the identity. It does not expose bearer tokens, request bodies, raw logs, or artifact bytes.

An edge history route uses the same shape at /blackboard/edges/{edge_id}/history and returns edge versions in descending order.

### 13.2 RecordProvenanceV1

GET /blackboard/records/{node_id}/provenance accepts version=current|NUMBER, provenance=created|updated|all, and literal.

~~~json
{
  "record": {},
  "version": 3,
  "entries": [
    {
      "provenance": {},
      "task": {
        "id": "task-id",
        "goal": "Validate admin authentication",
        "status": "completed"
      },
      "continuation": {
        "id": "continuation-id",
        "number": 2,
        "status": "completed"
      },
      "runtime_configuration": {
        "version_id": "config-version-id",
        "runtime_plugin_id": "codex",
        "runtime_profile_id": "captured-profile-id",
        "runner": "sandbox",
        "model_provider_id": "provider-id",
        "model": "model-id"
      },
      "scope_snapshot": {
        "task_id": "task-id",
        "summary": {
          "domains": 1,
          "ips": 0,
          "cidrs": 0,
          "urls": 1,
          "ports": 1,
          "excluded": 0,
          "has_testing_limits": true,
          "has_notes": false
        }
      },
      "source_events": [
        {
          "id": "event-id",
          "sequence": 18,
          "kind": "lifecycle",
          "phase": "checkpoint",
          "created_at": "RFC3339Nano"
        }
      ]
    }
  ]
}
~~~

Task Event metadata is intentionally compact. Full transcripts and raw runtime output remain on Task surfaces and are not copied into provenance responses. migration provenance returns migration_source and no fabricated Task or Continuation.

source_events contains at most 200 entries per provenance row, with source_event_count, source_events_truncated, and a Task Events continuation link when more exist.

The UI renders provenance as a readable chain:

Record version → actor → Task → Continuation → captured Runtime configuration and Runner → Scope Snapshot → source Events.

Every segment is independently linkable. Host Runner is visually explicit. Missing durable joined state is not silently omitted: the response includes join_status=missing and Blackboard Health may report missing_provenance.

### 13.3 GraphTraversalV1

GET /blackboard/records/{node_id}/traversal accepts:

- direction=incoming|outgoing|both;
- edge_type, repeatable;
- node_type, repeatable;
- max_depth, default 1 and maximum 5;
- max_nodes, default 100 and maximum 500;
- include_archived=false by default;
- include_retired_edges=false by default;
- at_revision.

Traversal is breadth-first. Nodes sort by depth, node-type ordinal, stable key, and ID. Edges sort by edge-type ordinal, from-node tuple, to-node tuple, and ID. The response contains root, nodes, edges, and explicit truncation:

~~~json
{
  "root": {},
  "nodes": [],
  "edges": [],
  "limits": {
    "max_depth": 3,
    "max_nodes": 100,
    "reached_depth": 2,
    "truncated": false,
    "truncation_reason": null
  }
}
~~~

Traversal never follows Task/Event/provenance joins as if they were graph edges. Those belong to RecordProvenanceV1.

## 14. Blackboard Health projections

### 14.1 Latest summary

GET /blackboard/health returns the latest completed or failed Health run plus current-state staleness:

~~~json
{
  "current_graph": {
    "revision": 42,
    "state_hash": "sha256",
    "main_projection_hash": "sha256"
  },
  "latest_run": {
    "run_id": "run-id",
    "checker_version": "blackboard_health_v1",
    "status": "completed",
    "overall": "degraded",
    "checked_graph_revision": 42,
    "checked_state_hash": "sha256",
    "checked_projection_hash": "sha256",
    "artifact_scan_status": "completed",
    "started_at": "RFC3339Nano",
    "completed_at": "RFC3339Nano",
    "stale": false,
    "metrics": {},
    "counts": {
      "critical": 0,
      "warning": 2,
      "info": 1
    },
    "top_results": []
  }
}
~~~

top_results contains at most ten results sorted critical, warning, info; code; subject kind; subject ID/key; fingerprint.

If no run exists, latest_run is null and overall is unknown. A run is stale under the storage contract when graph hashes/revision or filesystem scan inputs no longer match. Stale is independent from overall severity.

### 14.2 Starting a run

POST /blackboard/health-runs starts an explicit full scan. It accepts an optional Idempotency-Key header and body:

~~~json
{
  "sqlite_integrity": "quick"
}
~~~

An explicit run always performs the full required Health detector set, including artifact verification. sqlite_integrity is quick or full; quick runs the required quick_check/foreign_key_check path, while full additionally runs explicit integrity_check and may be slow.

The endpoint returns 202:

~~~json
{
  "protocol_version": 1,
  "run_id": "run-id",
  "status": "running",
  "status_url": "/api/projects/project-id/blackboard/health-runs/run-id"
}
~~~

The same idempotency key and request returns the same run. Reuse with a different request returns idempotency_conflict. Multiple non-idempotent runs at one graph revision are allowed because artifacts and reconciliation age can change.

The checker records completed, failed, or cancelled. Failure stores/returns overall unknown and never changes graph state.

### 14.3 Run and result detail

GET /blackboard/health-runs/{run_id} returns the persisted run and metrics. While running it returns 200 with status running and null completed_at.

GET /blackboard/health-runs/{run_id}/results accepts severity, code, subject_kind, subject_id, limit, and cursor.

Each result is:

~~~json
{
  "fingerprint": "stable-fingerprint",
  "code": "evidence_missing",
  "severity": "critical",
  "subject": {
    "kind": "node",
    "id": "evidence-node-id",
    "stable_key": "evidence:sqli-response",
    "ref": {}
  },
  "details": {},
  "operator_links": [
    {
      "label": "Open Evidence",
      "href": "/projects/project-id/blackboard?record=evidence-node-id"
    }
  ]
}
~~~

operator_links are deterministic route hints, not automatic repair actions. details is canonical per detector and versioned with blackboard_health_v1.

The Health UI groups results by severity and detector family: projection/budget, reconciliation/completion, graph topology, Evidence, duplicates/contradictions, Objectives/Frontier, Goal projection, materialization/history, alias/provenance/archive, and SQLite integrity. It exposes exact metrics and never reduces Health to one unexplained color.

## 15. Pentest Report projection

PentestReportV1 is valid only for project_kind=pentest. It is a deterministic semantic model rendered from graph conclusions plus durable Project, Scope, Task, Continuation, and Runtime context. It is not a stored conclusion and never reads raw Runtime output.

### 15.1 Request

GET /api/projects/{project_id}/reports/pentest accepts:

- at_revision;
- include_unconfirmed=true by default;
- include_tentative_facts=true by default;
- include_out_of_scope_context=true by default;
- include_unresolved_work=false by default;
- scope_context=current by default, or task:TASK_ID for an explicitly historical engagement context;
- evidence_detail=summary by default, or index;
- format=json by default, or markdown.

Unknown options return invalid_query. at_revision fixes graph state. The read transaction also captures:

- Project name, description, and current Scope when scope_context=current;
- the selected Task Scope Snapshot when scope_context=task:TASK_ID;
- every Task, Continuation, captured Runtime configuration, Runner, and Scope Snapshot referenced by included node/edge provenance;
- Evidence metadata and artifact availability.

The projector computes source_hash over canonical graph revision/state hash, selected options, Project identity/name/description, selected Scope context, and all joined durable rows that affect output. The same source_hash and renderer version produce identical semantic JSON and Markdown bytes.

scope_context=current is deterministic for the Scope content observed by that read, but current Scope and Project display metadata are mutable outside the graph. If either later changes, the same at_revision request produces a different source_hash and report. scope_context=task:TASK_ID pins authorization context to an immutable Scope Snapshot; exact long-term byte reproduction uses an explicitly retained report artifact/source envelope.

### 15.2 Semantic response

~~~json
{
  "report_version": "pentest_report_v1",
  "source": {
    "project_id": "project-id",
    "project_name": "Acme External",
    "graph_revision": 42,
    "state_hash": "sha256",
    "source_hash": "sha256",
    "scope_context": "current",
    "renderer_version": "pentest_markdown_v1"
  },
  "engagement": {
    "description": "External perimeter assessment",
    "scope": {},
    "testing_limits": [],
    "scope_notes": null,
    "contributing_tasks": [],
    "runner_summary": {
      "sandbox": 3,
      "host": 1
    }
  },
  "summary": {
    "confirmed_findings": 2,
    "unconfirmed_findings": 1,
    "severity_counts": {
      "critical": 1,
      "high": 1,
      "medium": 0,
      "low": 0
    },
    "confirmed_facts": 12,
    "tentative_facts": 4,
    "evidence_available": 6,
    "evidence_missing": 0,
    "unresolved_objectives": 3
  },
  "confirmed_findings": [],
  "unconfirmed_findings": [],
  "current_truth": {
    "confirmed": [],
    "tentative": [],
    "out_of_scope": []
  },
  "explicit_paths": [],
  "evidence_index": [],
  "unresolved_work": null,
  "provenance_summary": [],
  "limitations": []
}
~~~

No wall-clock generated timestamp appears in the semantic model. HTTP Date is transport metadata. A retained exported report may acquire EvidenceArtifact captured_at when explicitly retained; that timestamp is not inserted into the report bytes.

### 15.3 Finding projection

Each report Finding contains:

~~~json
{
  "finding": {},
  "title": "SQL injection in login",
  "status": "confirmed",
  "severity": "critical",
  "cvss_version": "4.0",
  "cvss_vector": "CVSS:4.0/...",
  "target": "https://example.com/login",
  "description": "Issue description",
  "proof": "Compact proof",
  "impact": "Authentication bypass",
  "recommendation": "Use parameterized queries",
  "about_entities": [],
  "supporting_facts": [],
  "supporting_observations": [],
  "contradictions": [],
  "evidence": [],
  "provenance": []
}
~~~

Only active supports, contradicts, evidences, about, derived_from, and relevant produced edges are followed. An artifact produced by the same Attempt but lacking evidences is not presented as proof for the Finding.

Confirmed Findings sort by severity, target, title, stable key, ID. Unconfirmed Findings use the same order after all confirmed Findings. False positives are excluded from the main report; when include_unresolved_work=true they appear in a clearly labelled audit appendix, never as vulnerabilities.

### 15.4 Current Truth and explicit paths

Current Truth sections contain ProjectFacts only:

- confirmed in-scope/unknown facts;
- tentative in-scope/unknown facts when requested;
- out-of-scope facts in a separate non-actionable context section when requested.

Deprecated, archived, and merged ProjectFacts are excluded. ProjectDirectives, Objectives, Hypotheses, Observations, Attempts, Goals, Entities, Solutions, and Evidence are not inserted as Current Truth.

explicit_paths are explanatory paths already represented by active graph edges. The projector may assemble a path ending at a confirmed Finding when every hop is an explicit active about, supports, evidences, derived_from, leads_to, produced, or satisfies relationship. It never invents a missing edge, causal claim, or attack narrative.

Path enumeration is deterministic:

1. prefer paths starting at an in-scope Entity or confirmed ProjectFact;
2. shortest hop count first;
3. endpoint and edge ordinal tuples;
4. stable key and ID ties;
5. maximum 20 paths per Finding and 100 paths per report, with truncation stated.

Stable Attack Chain summaries remain ordinary ProjectFacts and may appear in Current Truth. explicit_paths are a read explanation, not a second Attack Chain source of truth.

### 15.5 Engagement context and provenance

The report does not use the latest Task as a proxy for all work.

For scope_context=current:

- current Project Scope is the headline authorization context;
- each included conclusion carries compact provenance to its source Task/Continuation;
- contributing Task Scope Snapshots are summarized when they differ from current Scope;
- differences are reported as historical context, not silently merged into authorization.

For scope_context=task:TASK_ID:

- that immutable Task Scope Snapshot is the headline context;
- project-wide conclusions may still be included, but conclusions produced outside the selected Task are marked cross_task=true;
- this mode preserves the legacy task-anchored report use case.

runner_summary lists every Runner represented in included provenance. Any host Runner contribution is explicit in engagement context and on the affected Finding/fact provenance. Runtime Profile display deletion does not erase captured Runtime configuration identifiers.

Provenance is high-signal:

- actor type and stable actor ID;
- Task goal and ID;
- Continuation number/ID;
- Runtime Plugin, captured Runtime Profile/configuration, model provider/model when present;
- Runner;
- source Event count and links;
- recorded time.

It does not inline every Task Event, transcript line, command, or log.

### 15.6 Evidence index

Evidence entries contain stable key, artifact type, media type, summary, status, SHA-256, size, captured time, managed-path basename, evidenced record refs, and provenance. Absolute host source paths are not printed. Missing Evidence remains listed with an explicit missing status.

evidence_detail=summary includes Evidence directly relied on by included conclusions. evidence_detail=index includes every main EvidenceArtifact at the selected graph revision, grouped into relied-on and unreferenced/superseded categories.

### 15.7 Markdown renderer

Accept: text/markdown or format=markdown returns UTF-8 Markdown with:

1. title and source revision/hash;
2. Engagement Context and Scope;
3. Executive Summary with deterministic counts;
4. Confirmed Findings;
5. Unconfirmed Findings, when included;
6. Confirmed Current Truth;
7. Tentative Context, when included;
8. Out-of-Scope Context, when included;
9. Explicit Evidence Paths;
10. Evidence Index;
11. Unresolved Work, when included;
12. Provenance and Execution Context;
13. Limitations.

Within a Finding, fields appear in this order: status, severity/CVSS, target, description, proof, impact, recommendation, related Entities, supporting facts/observations, contradictions, Evidence, provenance.

Empty sections are emitted with an explicit no-records sentence so report shape is stable. Markdown escaping is deterministic. Newlines are LF. The final file ends with exactly one newline.

The response uses Content-Type: text/markdown; charset=utf-8, ETag from exact bytes, and headers:

- X-CyberPenda-Graph-Revision;
- X-CyberPenda-Source-Hash;
- X-CyberPenda-Renderer-Version.

JSON remains the canonical semantic contract. Future HTML/PDF renderers must consume PentestReportV1 rather than re-query graph tables.

## 16. CTF Solution projection

CTFSolutionV1 is valid only for project_kind=ctf_challenge. It does not render Pentest Finding sections.

### 16.1 Request and response

GET /api/projects/{project_id}/reports/ctf-solution accepts at_revision, include_candidates=true, include_procedure=true, and format=json|markdown.

~~~json
{
  "solution_version": "ctf_solution_v1",
  "source": {
    "project_id": "project-id",
    "project_name": "Challenge name",
    "graph_revision": 17,
    "state_hash": "sha256",
    "source_hash": "sha256",
    "renderer_version": "ctf_solution_markdown_v1"
  },
  "solved": true,
  "primary_verified_flag": {},
  "verified_flags": [],
  "candidate_flags": [],
  "answers": [],
  "procedures": [],
  "supporting_facts": [],
  "evidence": [],
  "goals_satisfied": [],
  "provenance_summary": [],
  "health": {
    "conflicting_verified_flags": false,
    "missing_evidence": false
  }
}
~~~

Unsolved is a successful 200 response with solved=false and primary_verified_flag=null. It is not a missing resource.

verified_flags includes every main Solution(kind=flag,status=verified). primary_verified_flag is the first by stable key then immutable ID. Multiple distinct verified flag values are all displayed and set conflicting_verified_flags=true; the projector does not silently choose truth. Duplicate Health results remain visible.

candidate_flags contains main candidate flag Solutions only when requested. rejected and superseded Solutions are excluded from current output but remain available through history.

Flag and answer values are exact local Project data and are not redacted in this operator-only projection. List summaries and notifications must not expose the value; the dedicated Solution view requires an explicit reveal/copy interaction.

### 16.2 Procedure and support

procedures contains Solution(kind=procedure) records ordered verified, candidate, rejected, superseded; stable key; ID. A procedure is not synthesized from raw logs. supporting_facts contains Current Truth linked to verified/candidate Solutions. Evidence requires active evidences edges.

goals_satisfied lists explicit satisfies edges from verified flags to Goals. A verified flag lacking the required Goal edge cannot exist under the graph contract.

### 16.3 Markdown and UI

The Markdown section order is:

1. Challenge and source revision;
2. Solved status;
3. Verified Flag or Flags;
4. Candidate Flags, when requested;
5. Answers;
6. Procedure;
7. Supporting Facts;
8. Evidence;
9. Provenance.

The CTF Project top-level output route is labelled Solution, not Report. The primary page shows solved state, a deliberate flag reveal/copy control, procedure, supporting Evidence, and provenance. Candidate values are visually distinct from verified values. Changing all verified flags to rejected/superseded immediately makes a current read unsolved while historical revisions remain reproducible.

## 17. Graph Explorer

Graph Explorer is a secondary Blackboard mode reached from Work or Entity/record inspectors. It is for topology questions, not the default work queue or only record browser.

GET /blackboard/graph-explorer accepts:

- seed_node_id, repeatable;
- node_type, edge_type, lifecycle, scope_status, entity_kind;
- direction and max_depth when seeded;
- query;
- include_archived=false;
- include_retired_edges=false;
- max_nodes, default 200 and maximum 500;
- max_edges, default 500 and maximum 1000;
- at_revision.

At least one seed or filter is required when the current main graph exceeds max_nodes or max_edges. An unfiltered graph that fits is allowed. If it does not fit, the endpoint returns projection_too_large with exact current counts and suggested filters; it never silently relevance-filters or samples.

~~~json
{
  "graph": {
    "nodes": [
      {
        "row": {},
        "x_group": "entity:host:example.com",
        "is_seed": true
      }
    ],
    "edges": []
  },
  "legend": {
    "node_types": {},
    "edge_types": {},
    "lifecycle_values": {}
  },
  "limits": {
    "max_nodes": 200,
    "max_edges": 500,
    "node_count": 87,
    "edge_count": 134,
    "truncated": false
  },
  "equivalent_record_query": {}
}
~~~

x_group is a deterministic grouping hint such as primary about Entity or node type. It is not a persisted coordinate. The server never stores force-layout positions as graph semantics.

The UI provides:

- type/status/scope filters and search;
- selectable directed edges with readable verbs;
- zoom, pan, fit, and focus-neighborhood;
- a synchronized accessible node/edge table;
- keyboard selection and inspector opening;
- deterministic hierarchical/radial layout where meaningful, with optional force layout as a user choice;
- an always-visible return to Work with equivalent filters.

Color never carries type, status, scope, or severity alone. Edge arrowheads and text labels preserve direction. Canvas-only content is mirrored in the table and inspector for keyboard and screen-reader use.

Graph Explorer consumes GraphExplorerV1 or the exact CanonicalMainGraphV1 when explicitly viewing the complete current graph. It does not define a second graph schema.

## 18. Legacy compatibility projections

Legacy HTTP routes, trusted MCP tools, CLI commands, and current Web pages remain adapters during migration. They read from the canonical graph through BlackboardReadService. They never become a second canonical store.

The migration contract owns transaction sequencing, dual-version release policy, write translation, rollback, and removal criteria. This section fixes only the read meanings those adapters must preserve.

Every legacy list route/tool gains additive limit and cursor inputs plus page output. Default and maximum limit are 200 for compatibility. A caller omitting pagination receives the first deterministic page; when more rows exist, next_cursor and compatibility_truncated=true make that explicit. No legacy adapter performs an unbounded graph read.

### 18.1 Project Facts

Legacy GET /api/projects/{project_id}/facts/index maps main ProjectFact nodes:

| Legacy field | Graph source |
| --- | --- |
| fact_key | stable_key |
| category | properties.category |
| summary | properties.summary |
| confidence | properties.confidence |
| scope_status | properties.scope_status |

Default output is Current Truth only. include_deprecated=1 additionally returns main ProjectFacts with confidence=deprecated. Archived and merged nodes are never current index rows.

Legacy GET /facts/{fact_key} resolves aliases and merged redirects, returning:

~~~json
{
  "id": "canonical-node-id",
  "project_id": "project-id",
  "fact_key": "canonical-stable-key",
  "version": 3,
  "category": "service",
  "summary": "Admin service exposed",
  "body": "semantic detail",
  "confidence": "confirmed",
  "scope_status": "in_scope",
  "created_at": "RFC3339Nano",
  "updated_at": "RFC3339Nano",
  "resolved_from_alias": "old-key-or-null",
  "resolved_from_merged_id": "old-id-or-null"
}
~~~

The two resolution fields are additive and optional for old clients.

Legacy versions are reconstructed from ProjectFact node versions. version remains the semantic record version, not graph revision or mutation sequence. Versions sort ascending to preserve the existing API.

### 18.2 Fact Relations

Legacy Fact Relations map active edges whose two endpoints are ProjectFact:

- supports;
- contradicts;
- depends_on;
- leads_to.

The legacy shape is:

~~~json
{
  "id": "edge-id",
  "project_id": "project-id",
  "source_fact_key": "source-key",
  "target_fact_key": "target-key",
  "relation": "supports",
  "summary": "relationship summary",
  "created_at": "RFC3339Nano",
  "updated_at": "RFC3339Nano"
}
~~~

Edge direction is preserved. Edges involving another node type cannot be represented and are not returned by this compatibility route.

A migrated legacy duplicates relation becomes an explicit node merge/alias when migration can establish duplicate identity. It is not retained as an active duplicates edge because the graph schema has no such semantic edge. The legacy relation list therefore does not synthesize current duplicates rows from similarity candidates. Literal merge/key history remains available through canonical history projections.

### 18.3 Findings

Legacy GET /findings maps main Finding nodes one-to-one:

| Legacy field | Graph source |
| --- | --- |
| finding_key | stable_key |
| version | node version |
| title through recommendation | corresponding properties |
| status | properties.status |
| cvss_version/vector | corresponding properties |
| cvss_pending and severity | derived graph properties |
| created_at/updated_at | node envelope |

Archived and merged Findings are omitted. false_positive remains returned because existing consumers separate by status. Ordering uses the Finding order in section 6.3.

Finding versions map node versions and remain addressable through canonical aliases. Merge resolution fields are additive on point reads.

Legacy Finding versions sort ascending to preserve the existing API.

### 18.4 Evidence

Legacy GET /evidence returns one row per main EvidenceArtifact, never one duplicate row per evidences edge:

~~~json
{
  "id": "evidence-node-id",
  "project_id": "project-id",
  "evidence_key": "stable-key",
  "attach_to_type": "finding",
  "attach_to_key": "finding:sqli-login",
  "artifact_type": "http_exchange",
  "source_path": "legacy-source-reference",
  "managed_path": "artifacts/evidence/response.txt",
  "sha256": "hex",
  "summary": "Captured authentication bypass",
  "created_at": "RFC3339Nano",
  "updated_at": "RFC3339Nano",
  "attachments": []
}
~~~

attachments is additive and contains every active evidences target as NodeRefV1.

The singular legacy attach target is selected deterministically:

1. the original migrated legacy target when it remains a current canonical ProjectFact or Finding;
2. otherwise the first current ProjectFact or Finding target by node-type ordinal, stable key, and ID;
3. otherwise empty strings.

Evidence concerning Observation, Hypothesis, Solution, or another type therefore remains visible to new consumers through attachments even when the singular legacy fields are empty. Absolute host paths are not introduced by translation; migrated source_path is preserved only to the extent the old endpoint already exposed it.

### 18.5 Dashboard and reports

The existing Dashboard fields remain:

- counts.tasks from durable Tasks;
- counts.facts from main ProjectFact nodes, including deprecated;
- counts.findings from main Finding nodes, including false positives;
- counts.evidence from main EvidenceArtifact nodes.

The additive blackboard, health, and ctf fields from ProjectBlackboardSummaryV1 do not break old clients.

Legacy POST /api/projects/{project_id}/report accepts optional task_id and delegates to PentestReportV1:

- no task_id maps to scope_context=current;
- task_id maps to scope_context=task:TASK_ID;
- include_unconfirmed and include_tentative_facts remain true;
- response remains status, format=markdown, and markdown;
- status is generated;
- the Markdown bytes are pentest_markdown_v1.

For a CTF Project the legacy Pentest report route returns project_kind_mismatch. It does not disguise a CTF Solution as a vulnerability report.

### 18.6 MCP and CLI

The compatibility read tools:

- get_project_fact;
- list_project_facts;
- search_project_facts;
- list_vulnerabilities;
- generate_report;

delegate to the same legacy projections. Runtime provenance and Interface Grant rules remain fixed by the Runtime protocol. Compatibility tools do not expose operator history, Health, general graph traversal, or Graph Explorer.

Legacy pentestctl fact, finding, evidence, and report reads use the same translation in both --api and --db mode.

### 18.7 Existing UI routes

During cutover:

- /projects/{id}/facts redirects or renders the Blackboard Work view with node_type=project_fact;
- /findings remains a focused Finding view backed by RecordCollectionV1;
- /evidence remains a focused Evidence view backed by RecordCollectionV1;
- /report uses PentestReportV1;
- CTF Projects expose /solution using CTFSolutionV1;
- existing bookmarks and browser history keep working.

Focused pages are views over the same records and inspector. They do not reimplement versions, relations, provenance, or merge resolution.

## 19. Operator information architecture

### 19.1 Project navigation

The Project navigation order is:

1. Overview;
2. Tasks;
3. Blackboard;
4. Findings for Pentest Projects, or Solution for CTF Projects;
5. Evidence;
6. Report for Pentest Projects;
7. Scope.

Blackboard has subroutes/tabs:

- Work;
- Entities;
- Explorer.

Health opens as a status route/panel from Overview and Work. Record detail is addressable as /projects/{project_id}/blackboard/records/{node_id} and may render in the inspector without losing the selected collection query.

### 19.2 Responsibility by surface

| Surface | Question it answers |
| --- | --- |
| Overview | Is the Project ready, active, healthy, and moving? |
| Tasks | What Runtime work happened or is happening? |
| Blackboard Work | What does the Project know, what is open, and what needs operator attention? |
| Entities | What assets/objects exist and what knowledge concerns each one? |
| Record Inspector | What exactly is this record, why is it believed, and how did it change? |
| Findings | What reportable Pentest issues need review? |
| Solution | Is the CTF solved and what verified output/procedure supports it? |
| Evidence | What retained proof exists and what does it evidence? |
| Report | What deterministic deliverable follows from current conclusions? |
| Explorer | How are selected records topologically connected? |
| Scope | What testing is authorized now? |

No surface confuses visibility with authorization. Every out-of-scope Entity, Observation, ProjectFact, Finding context, or Evidence relationship has a visible non-actionable marker.

### 19.3 Inspector sections

The standard inspector orders sections:

1. identity, type, stable key, version, disposition, lifecycle;
2. complete type-specific properties;
3. scope and Entity context;
4. derived role: Current Truth, Frontier, solved contributor, Health subject;
5. supporting, contradicting, Evidence, and other relationships;
6. created/updated provenance;
7. version and key/merge history;
8. mutation actions allowed for the current operator.

Actions include edit/transition, relate, merge, archive/restore, Evidence retain/open, and Objective/Attempt workflows as permitted by the graph and Runtime contracts. Controls are omitted or disabled with an exact stable reason; reads do not guess transition validity beyond server-provided capabilities.

### 19.4 Mutation capability hints

RecordDetailV1 MAY include:

~~~json
{
  "capabilities": {
    "patch": {
      "allowed": true,
      "reason_code": null
    },
    "transition": {
      "allowed": true,
      "targets": ["false_positive"]
    },
    "merge": {
      "allowed": true
    },
    "archive": {
      "allowed": false,
      "reason_code": "archive_guard_active_finding"
    }
  }
}
~~~

These are advisory hints computed at the observed revision. Every mutation still supplies expected_version and is authoritatively revalidated by BlackboardGraphService.Apply. A stale hint never authorizes an invalid mutation.

## 20. API evolution, security, and performance

### 20.1 Versioning

Existing routes remain unprefixed. Read protocol and projection names carry v1 versions. Additive optional fields are allowed. Removing/renaming fields, changing default ordering, changing cursor semantics, or changing report bytes requires a new projection/renderer version and a compatibility window.

OpenAPI schemas generated from the same versioned read definitions are part of implementation. Web TypeScript types, Go result types, CLI JSON, and OpenAPI examples use golden conformance fixtures.

### 20.2 Authorization and disclosure

- Operator routes require the existing daemon operator credential.
- Project isolation is enforced inside BlackboardReadService, not only handlers.
- Runtime grants receive only Runtime protocol reads.
- Credential Entity values are never resolved.
- Artifact payload bytes require the existing artifact access path and are not embedded in projections.
- canonical projections exclude absolute host paths, bearer tokens, secret environment values, full Task transcripts, raw commands, and full logs; the legacy Evidence adapter may preserve its already-public source_path field under section 18.4 until cutover;
- report and CTF renderers escape untrusted text and never execute Markdown/HTML.

### 20.3 Consistency and concurrency

All data in one response comes from one SQLite snapshot. Pagination pins the first page revision. Cross-endpoint navigation may observe a newer revision and always displays it. If-Match on mutations uses record versions from detail responses; read ETags are not mutation preconditions.

### 20.4 Query limits

- query length maximum: 500 Unicode scalar values;
- repeated filter values maximum: 50 per field;
- collection limit maximum: 200;
- traversal depth maximum: 5;
- traversal nodes maximum: 500;
- Explorer nodes maximum: 500;
- Explorer edges maximum: 1000;
- embedded Work previews maximum: 20 each;
- report explicit paths maximum: 100.

Exceeding a declared maximum returns invalid_query. A response never silently truncates except where the response has an explicit truncated flag and deterministic bound.

Read implementation uses the storage-contract indexes and may add read-only indexes through numbered migrations. It does not persist arbitrary UI caches as semantic graph state. Materialized read caches, if later needed, are disposable, revision-keyed, and validated against the same golden projection fixtures.

## 21. TDD acceptance matrix

Implementation proceeds red-first through BlackboardReadService and the public HTTP/CLI seams. Behavioral tests do not query SQLite to assert domain output. Storage-adapter tests may inspect/corrupt a real temporary SQLite database.

Required tests:

1. CanonicalMainGraphV1 exact bytes and Runtime route remain unchanged by adding operator projections.
2. Every canonical response names Project, project kind, projection version, observed graph revision, state hash, and projection hash.
3. One read sees a single SQLite snapshot while a concurrent writer commits.
4. at_revision reconstructs an older result exactly after later updates, merges, archives, and restores.
5. If-None-Match returns 304 only for the exact projection/query/revision.
6. Current cursors pin their initial graph revision across concurrent writes.
7. Cursor reuse with another Project, filter, sort, limit, or projection fails invalid_cursor.
8. Pagination has no duplicate or missing row at equal sort timestamps because stable key/ID ties are encoded.
9. Ordinary stable-key reads resolve aliases and merged IDs to one canonical node with resolution metadata.
10. literal history returns the merged source identity without redirect.
11. archived records are omitted by default and readable when explicitly requested.
12. Project isolation rejects cross-Project node IDs, aliases, Health run IDs, and cursors.
13. lexical search uses fixed normalization/ranking and never reads artifact payloads or raw logs.
14. Work summary counts agree with golden graph fixtures without querying legacy tables.
15. Work attention ordering places critical/warning Health before Frontier and active work.
16. Recent changes excludes replay/no-op mutations and orders semantic changes deterministically.
17. Current Truth contains only main tentative/confirmed ProjectFacts and marks out-of-scope rows non-actionable.
18. Frontier includes only open Objectives with every dependency/blocker resolved.
19. Frontier ordering follows parent Task status, creation time, key, and ID.
20. A stranded Objective remains off Frontier and appears through Health/record detail.
21. Record detail preserves edge direction and only treats active evidences edges as Evidence support.
22. Record mutation capability hints are advisory and become stale safely after a concurrent write.
23. Entity roots, children, descendants, and shortest breadcrumbs handle a multi-parent DAG deterministically.
24. Entity Credential detail exposes credential_ref but no secret value.
25. provenance joins the exact Task, Continuation, captured Runtime configuration, Runner, Scope Snapshot, and compact source Events.
26. deleted live Runtime Profile metadata does not erase captured provenance identifiers.
27. provenance never inlines transcript text, command lines, bearer tokens, or raw runtime output.
28. history orders semantic versions separately from graph revision and mutation sequence.
29. traversal is breadth-first, direction-preserving, deterministic, bounded, and explicit when truncated.
30. latest Health reports stale independently from overall severity.
31. Health result sorting and detector-family grouping are deterministic.
32. explicit Health run idempotency returns one run for the same key/request and conflicts on changed input.
33. a failed Health scan returns unknown and does not mutate graph revision.
34. PentestReportV1 same source_hash/renderer produces byte-identical JSON and Markdown.
35. Pentest report uses current Scope by default rather than the latest Task.
36. task-anchored report uses the selected immutable Scope Snapshot and marks cross-Task conclusions.
37. host Runner contributions are explicit in report engagement and affected conclusion provenance.
38. confirmed/unconfirmed/false-positive Findings appear only in their specified sections.
39. tentative and out-of-scope ProjectFacts are separated from confirmed conclusions.
40. report Evidence requires evidences edges; produced-only artifacts are not presented as proof.
41. missing Evidence remains visible and does not cause report generation to invent proof.
42. explicit report paths follow only allowed active edges and obey path limits/order.
43. report output contains no render-time clock value and ends with exactly one LF.
44. CTF unsolved returns 200 with solved=false.
45. one verified flag makes solved true and its satisfies Goal appears.
46. rejecting/superseding every verified flag reverses current solved state while historical output stays reproducible.
47. multiple distinct verified flags are all returned with deterministic primary and conflict indicator.
48. flag values are absent from dashboard/list summaries and present only in the explicit CTF Solution projection.
49. Graph Explorer rejects an oversized unfiltered graph with exact counts rather than sampling.
50. Graph Explorer list/table data is identical to its canvas data and preserves edge direction.
51. legacy Fact index, point, version, and relation golden responses are produced from graph records.
52. legacy Finding and Evidence golden responses preserve old fields and additive resolution/attachments fields.
53. multi-target Evidence returns once with deterministic singular legacy target and complete attachments.
54. legacy report POST delegates to pentest_markdown_v1 and task_id selects task Scope context.
55. CTF legacy Pentest report fails project_kind_mismatch.
56. operator CLI --api and --db return canonical-equivalent JSON for each read kind.
57. HTTP, Go, TypeScript, CLI, and OpenAPI fixtures agree on field names, nullability, enums, and ordering.

Each vertical implementation slice adds one failing behavior test, the minimal read-module behavior, then its HTTP/CLI/UI adapter. Tests never begin as a horizontal batch disconnected from implemented behavior.

## 22. Rejected alternatives

### 22.1 Force-directed graph as primary UI

Rejected because dense record comparison, status scanning, keyboard access, exact text, provenance, and bulk filtering are stronger in a ledger. Graph topology remains valuable as a secondary Explorer.

### 22.2 Separate query logic per page

Rejected because aliases, merges, Project isolation, historical reconstruction, ordering, and report semantics would drift across Dashboard, Facts, Findings, Evidence, and Report.

### 22.3 Returning the full canonical graph to every page

Rejected because summary/list pages need pagination, filters, stable cursors, facets, and operator-specific joins. CanonicalMainGraphV1 remains the bounded complete Runtime context and optional complete Explorer input.

### 22.4 Relevance-selected Runtime context

Rejected by the destination. Runtime context remains the full CanonicalMainGraphV1. Operator search/filtering does not alter pinned or explicit Runtime graph semantics.

### 22.5 Model-generated reports or next actions

Rejected for v1 because report claims and operator recommendations must be deterministic and traceable to canonical records. A future explicitly requested analysis Task may propose narrative, but it cannot replace PentestReportV1 source semantics.

### 22.6 Report as canonical stored state

Rejected because Findings, ProjectFacts, Evidence, relationships, Scope, and provenance remain authoritative. A report may be explicitly retained as an EvidenceArtifact without becoming the source of its own conclusions.

### 22.7 Offset pagination

Rejected because it drifts under writes and cannot continue a stable historical snapshot. Revision-bound cursors are required.

### 22.8 Persisted Frontier ranks or Graph coordinates

Rejected because Frontier and layout are derived views. Persisting them would create stale scheduling or presentation state inside semantic memory.

## 23. Downstream handoff

The legacy migration ticket owns:

- translating old rows and aliases into canonical graph identities/history;
- preserving original Evidence attachment preference;
- compatibility write commands;
- release flags, rollback, deprecation headers, and final legacy-table removal.

The TDD slicing ticket owns the red-green implementation sequence across storage, Runtime protocol, reads, UI, reports, migration, and cutover.

No new domain glossary term is required. Blackboard Work View, Entity Browser, Provenance view, Health view, and Graph Explorer are interface/projection names, not new semantic graph concepts. No separate ADR is required for this ticket: the versioned contract records the reversible read/UI choice, while the canonical graph/storage/runtime architectural decisions remain in their owning contracts.

No additional Wayfinder ticket is required. The existing migration, TDD slicing, and final assembly tickets cover every remaining decision exposed here.
