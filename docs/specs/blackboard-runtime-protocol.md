# Blackboard Runtime and Project Interface Protocol Contract

- **Status:** superseded by ADRs 0003–0014; historical reference only; do not implement

> **STOP:** This document specifies Blackboard v1. It is retained only as migration history. Implement [Blackboard v2](./blackboard-v2-spec.md) through the [v2 TDD plan](./blackboard-v2-tdd-plan.md).
- **Map:** [Map: Refactor Blackboard into bounded graph memory](https://github.com/n1majne3/CyberPenda/issues/55)
- **Graph contract:** [Blackboard Typed Property Graph Contract](./blackboard-graph-contract.md)
- **Storage contract:** [Blackboard SQLite Persistence, History, Compaction, and Health Contract](./blackboard-graph-storage.md)
- **Runtime protocol version:** `1`
- **Mutation schema version:** `1`
- **Pinned snapshot:** `CanonicalMainGraphV1`

This document fixes the trusted Runtime and operator interfaces to the graph-first Blackboard. It specifies the semantic operations exposed through MCP, CLI, and HTTP; their common request, result, error, authorization, provenance, and idempotency rules; task-local snapshot and instruction projection; normal completion reconciliation; and interrupted Attempt handling.

The keywords **MUST**, **MUST NOT**, **SHOULD**, and **MAY** are normative.

## 1. Decision and module seams

All graph state changes continue to cross the one deep mutation seam fixed upstream:

~~~go
type BlackboardGraphService interface {
	Apply(ctx context.Context, batch MutationBatch) (MutationResult, error)
}
~~~

MCP, CLI, HTTP, Goal projection, normal completion, interruption recovery, Evidence retention, migration, compaction, and restore are adapters. None may write graph tables directly or duplicate graph schema, lifecycle, endpoint, alias, merge, provenance, idempotency, archive, or Project-isolation rules.

The project-interface module exposes six semantic capabilities:

1. apply one atomic typed graph mutation batch;
2. resolve current graph records after alias and merge resolution;
3. read the current full Runtime graph;
4. retain Evidence payload content and then represent it in the graph;
5. checkpoint one open Attempt into Task Events and graph state;
6. finish one Runtime Continuation's Blackboard protocol.

Only capability 1 mutates graph state directly. Capabilities 4 and 5 orchestrate external durable work and then call `BlackboardGraphService.Apply`. Capability 6 writes Task Summary and Continuation protocol state outside the graph after verifying graph state. Read capabilities never mutate or repin a Continuation.

The deletion test applies: removing this project-interface module would force provenance binding, role authorization, cross-domain idempotency, path confinement, transport error mapping, snapshot verification, and completion reconciliation into every transport. Keeping them here provides leverage and locality.

## 2. Scope and explicit non-responsibilities

This contract owns:

- trusted Runtime mutation and operational-read shapes;
- task-bound MCP, task-bound or operator CLI, and authenticated HTTP transport parity;
- Runtime Project Interface credentials and provenance binding;
- source Task Event validation;
- retained Evidence orchestration;
- Attempt checkpoint events;
- Continuation finish markers and normal reconciliation;
- exact task-local file placement and snapshot integrity;
- the canonical Blackboard Runtime Protocol and its adapter projections.

This contract does not own:

- graph node, edge, lifecycle, merge, alias, or validation semantics fixed by the graph contract;
- SQLite ledger, projection bytes, compaction, Health persistence, or crash recovery fixed by the storage contract;
- operator dashboards, report information architecture, Graph Explorer, or general read projections owned by [Specify Blackboard read projections, reports, and operator UI](https://github.com/n1majne3/CyberPenda/issues/59);
- legacy tool/route translation, deprecation, or cutover owned by [Specify legacy Blackboard migration and compatibility cutover](https://github.com/n1majne3/CyberPenda/issues/60);
- final red-green implementation order owned by [Design TDD acceptance matrix and implementation slices for graph Blackboard](https://github.com/n1majne3/CyberPenda/issues/61);
- command approval, packet filtering, shell policy, or a graph scheduler.

## 3. Versioning and canonical envelopes

`protocol_version` versions the project-interface request and response contract. `batch.schema_version` versions graph mutation semantics. They are separate and both are required.

Version 1 is additive-only:

- unknown request fields fail closed;
- new optional response fields MAY be added;
- existing field meaning, enum meaning, tool name, command name, or route behavior MUST NOT change in place;
- a breaking project-interface change requires protocol version 2 and parallel transport support during migration;
- a breaking graph mutation change requires a new mutation schema version under the graph contract.

Every non-streaming success response uses:

~~~json
{
  "protocol_version": 1,
  "request_kind": "apply",
  "project_id": "project id",
  "observed_graph_revision": 42,
  "result": {}
}
~~~

`project_id` is returned for operator clarity but is always resolved from trusted context or the authenticated route. A task-bound Runtime never supplies it in the JSON body.

Every project-interface failure uses `ProjectInterfaceErrorV1`:

~~~json
{
  "error": {
    "protocol_version": 1,
    "code": "version_conflict",
    "message": "expected node version 2, current version is 3",
    "operation_index": 1,
    "op_id": "close-attempt",
    "path": "batch.operations[1].expected_version",
    "retryable": true,
    "details": {
      "current_version": 3
    },
    "request_id": "opaque request id"
  }
}
~~~

The graph contract's domain error fields and codes remain canonical. This ticket adds only interface/orchestration codes:

| Code | Meaning |
| --- | --- |
| `actor_forbidden` | Bound actor is not permitted to request the operation. |
| `continuation_context_required` | A task-only operation lacks a valid Continuation grant. |
| `continuation_closed` | A new Runtime write was attempted after finish or terminal state. Exact replay remains readable. |
| `continuation_open_attempts` | Finish found one or more open Attempts created by the Continuation. |
| `continuation_finish_conflict` | The same finish key was reused for a different payload. |
| `source_event_not_found` | A referenced Task Event does not exist. |
| `source_event_mismatch` | A referenced Event belongs to another Task or violates Continuation binding. |
| `evidence_source_forbidden` | Evidence source path escapes the permitted task/operator roots. |
| `evidence_source_changed` | Source bytes changed across an idempotent Evidence retry. |
| `snapshot_unavailable` | The pinned full graph cannot be reconstructed, rendered, or hash-verified. |
| `storage_busy` | SQLite writer lock exhaustion; retryable and consumes no idempotency key. |

Error messages may improve; codes and structured details are stable.

## 4. Trusted execution context

### 4.1 Continuation Interface Grant

Every real Runtime Continuation receives one daemon-issued **Continuation Interface Grant**. The server stores:

- grant ID and a hash of a cryptographically random bearer token;
- Project ID;
- Task ID;
- Continuation ID;
- captured Task Runtime Configuration Version ID;
- Runtime Profile ID;
- Runtime Plugin/provider ID;
- Runner;
- derived Runtime actor ID;
- issued, finished, revoked, and terminal timestamps.

The plaintext token is projected only to the task-local Runtime environment and trusted MCP configuration. It MUST NOT be stored in graph records, Task Events, logs, `.pentest/context.json`, `.pentest/blackboard.json`, generated instructions, or HTTP request logs.

Runtime provenance is derived as follows:

| Provenance field | Trusted source |
| --- | --- |
| `actor_type` | Fixed to `runtime`. |
| `actor_id` | Server-derived as `runtime:<runtime_plugin_id>:<continuation_id>`. |
| `project_id` | Grant. |
| `task_id` | Grant. |
| `continuation_id` | Grant. |
| `runtime_profile_id` | Captured Runtime configuration referenced by the Continuation. |
| `runner` | Continuation row. |
| `recorded_at` | Graph service clock. |

The Runtime request MUST NOT contain any of these fields. Supplying a provenance envelope, Project ID, Task ID, Continuation ID, Runtime Profile ID, Runner, actor, or timestamp in a task-bound request fails with `provenance_spoofed` or `invalid_request`.

The grant is created in the same short transaction that pins the Continuation's Runtime Configuration Version and Blackboard snapshot. A Runtime process cannot start until the token, snapshot metadata, and task-local files agree.

### 4.2 Grant lifetime

- New writes are allowed only while the bound Continuation is pending, running, or paused and has not finished the protocol.
- Calling Finish closes the grant to new project-interface mutations.
- Exact idempotent replay and read operations remain allowed after finish so a lost response can be recovered.
- When a Continuation becomes terminal without Finish, new Runtime writes are rejected; the system reconciler owns later graph changes.
- Revocation or Task deletion rejects every use.

The existing daemon-wide bearer token remains an operator/API credential. It MUST NOT be used to fabricate Runtime provenance. Query-string MCP tokens are permitted only as a transport fallback, MUST be Continuation-scoped, and MUST be redacted from logs.

### 4.3 Operator, system, and migration context

- Authenticated HTTP resolves a stable operator principal. In single-user MVP the default principal is `local-operator`.
- CLI outside a Task requires explicit Project and operator identity. It creates `actor_type=operator` provenance and can never claim `actor_type=runtime`.
- Goal projection, normal reconciliation, interruption recovery, compaction, and restore use named `system` actors.
- Migration uses a trusted migration adapter and required `migration_source`; ordinary MCP/HTTP/CLI callers cannot select migration context.

## 5. Actor authorization

The graph service remains the final validator. The project-interface module also rejects actor-ineligible requests before they reach Apply.

| Capability | Runtime | Operator | System | Migration |
| --- | --- | --- | --- | --- |
| Create/patch/transition Entity, ExplorationObjective, Observation, Hypothesis, ProjectFact, Finding, Solution | Yes | Yes | Yes | Yes |
| Create/patch/transition Attempt | Yes, except `interrupted` | Yes, except `interrupted` | Yes | Yes |
| Create ProjectDirective | `proposed` only | Yes | Yes | Yes |
| Activate ProjectDirective | No | Yes | Yes | Yes |
| Create or patch Goal | No | No | Goal projector only | Migration only |
| Create/replace available EvidenceArtifact through generic Apply | No | No | No | Migration only |
| Retain available Evidence through the Evidence operation | Yes | Yes | Yes | Yes |
| Put or retire controlled semantic edges | Yes | Yes | Yes | Yes |
| Archive/restore through `set_disposition` | No | Yes | Trusted maintenance | Migration |
| Merge nodes | No | Yes | Yes | Migration |
| Mark Attempt `interrupted` | No | No | Interruption reconciler only | Migration |
| Compaction/restore maintenance metadata | No | Operator maintenance route | Compactor/restorer | No |
| Finish a Runtime Continuation | Bound Continuation only | No | Recovery only | No |

Runtime-created Observation, Hypothesis, ProjectFact, Finding, Solution, and EvidenceArtifact records still require a matching incoming `produced` edge from an Attempt with the same bound Task and Continuation provenance, as fixed by the graph contract.

## 6. Semantic operation 1: Apply graph mutation

### 6.1 Request

All transports normalize to `ApplyMutationV1`:

~~~json
{
  "protocol_version": 1,
  "batch": {
    "schema_version": 1,
    "idempotency_key": "admin-enum:complete",
    "operations": []
  },
  "source_event_ids_by_op": {
    "record-admin-fact": ["event-id"]
  }
}
~~~

Rules:

- `batch` is exactly the MutationBatch contract from the graph schema.
- A batch contains 1-128 operations.
- `source_event_ids_by_op` is optional, keyed only by an existing `op_id` in the batch.
- Each operation may reference at most 32 ordered, deduplicated Event IDs.
- Event mappings are part of the canonical request hash and therefore part of idempotency conflict detection.
- Every Event MUST belong to the bound Task. Newly recorded Runtime Events MUST also belong to the bound Continuation.
- The adapter places verified Event mappings into the sealed per-operation execution context; it never copies caller provenance JSON.
- Unknown fields are rejected.

Task-bound scopes are derived as `continuation:<continuation_id>`. Operator, system, and migration scopes remain as fixed by the graph contract.

### 6.2 Result

`MutationResultV1` is stored canonically and returned exactly on idempotent replay:

~~~json
{
  "mutation_id": "opaque id",
  "mutation_seq": 18,
  "base_graph_revision": 41,
  "result_graph_revision": 42,
  "recorded_at": "RFC3339Nano",
  "changed": true,
  "operations": [
    {
      "op_id": "record-admin-fact",
      "kind": "create_node",
      "changed": true,
      "node": {},
      "edge": null,
      "resolved_from_alias": null,
      "effects": {}
    }
  ],
  "projection": {
    "status": "measured",
    "renderer_version": "canonical_main_graph_v1",
    "estimator_version": "utf8_bytes_div_4_v1",
    "hash": "sha256",
    "bytes": 2048,
    "estimated_tokens": 512,
    "budget_state": "within_target"
  }
}
~~~

Operation results are discriminated by `kind`:

- node operations return the resulting full node envelope;
- edge operations return the resulting full edge envelope;
- Merge Nodes returns `canonical_node`, `merged_node`, sorted rewired/retired edge IDs, and created/repointed aliases in `effects`;
- no-op operations return the current record with `changed=false`;
- a dirty projection returns null measurement fields and `budget_state=unknown` without losing the accepted graph write.

The response has no mutable “replayed” field because exact replay must return identical bytes. HTTP MAY add `CyberPenda-Idempotent-Replay: true`; MCP and CLI need not expose transport-only replay metadata.

## 7. Semantic operation 2: Resolve current records

`ResolveRecordsV1` is the narrow optimistic-concurrency read:

~~~json
{
  "protocol_version": 1,
  "nodes": [
    {
      "node_type": "attempt",
      "stable_key": "attempt:task-7:admin-enum"
    }
  ],
  "edge_ids": ["edge-id"]
}
~~~

Rules:

- at most 100 node references plus edge IDs per request;
- ordinary reads resolve stable-key aliases and immutable-ID merge redirects;
- every result names `observed_graph_revision`;
- a node result includes its full current envelope and optional `resolved_from_alias` / `resolved_from_merged_id`;
- a missing reference is reported in a `missing` array rather than turning other requested records into a partial transport failure;
- Runtime callers cannot request literal merged identity history or archived history through this operation;
- operator history and traversal projections remain with the read-projection ticket.

This operation exists so a caller can recover from `version_conflict` without loading a UI projection or querying SQLite.

## 8. Semantic operation 3: Read current Runtime graph

The current Runtime graph operation returns the exact current `CanonicalMainGraphV1` bytes and observed projection metadata:

~~~json
{
  "protocol_version": 1,
  "request_kind": "current_graph",
  "project_id": "project id",
  "observed_graph_revision": 42,
  "result": {
    "renderer_version": "canonical_main_graph_v1",
    "projection_hash": "sha256",
    "projection_bytes": 2048,
    "estimated_tokens": 512,
    "graph": {}
  }
}
~~~

The `graph` object is byte-for-byte equivalent to canonical JSON when separately serialized with Canonical JSON v1. The HTTP endpoint also returns an ETag derived from the projection hash and honors `If-None-Match` with 304.

This is an explicit read, not dynamic prompt injection:

- it does not change the Continuation's pinned revision;
- it does not rewrite `.pentest/blackboard.json`;
- it does not alter the initial context already supplied to the Runtime;
- it may expose writes from concurrent Tasks after the pin.

The full graph is intentionally not paginated or relevance-filtered. Its bounded/full semantics are part of the destination. General operator lists, search, Health, history, and traversal remain with the read-projection ticket.

## 9. Semantic operation 4: Retain Evidence

Runtime callers do not create an available EvidenceArtifact by claiming a host path, managed path, digest, size, or availability status through generic Apply. `RetainEvidenceV1` performs the filesystem step under trusted Runner mapping and then calls Apply.

### 9.1 Request

~~~json
{
  "protocol_version": 1,
  "idempotency_key": "admin-login:retain-http-exchange",
  "stable_key": "evidence:admin-login-response",
  "expected_version": null,
  "artifact_type": "http_exchange",
  "media_type": "application/http",
  "source_path": "captures/admin-login.txt",
  "summary": "Authenticated request and response proving the admin endpoint",
  "captured_at": "RFC3339Nano",
  "produced_by_attempt": {
    "node_type": "attempt",
    "stable_key": "attempt:task-7:admin-enum"
  },
  "links": [
    {
      "edge_type": "evidences",
      "to": {
        "node_type": "project_fact",
        "stable_key": "endpoint:admin-panel"
      }
    }
  ]
}
~~~

Rules:

- `expected_version=null` means create a new EvidenceArtifact and conflicts with an existing live key or alias.
- Replacing retained content for the same Evidence identity requires the current `expected_version`. It creates a new node version and preserves prior content under the Artifact Root retention policy.
- A Runtime request MUST provide `produced_by_attempt`, and the Attempt MUST be open or terminal for the same Task and Continuation.
- `links` may contain `evidences` and `about` edges only. Other semantic edges use Apply.
- The Runtime supplies `source_path`, classification, and semantic summary. The adapter supplies `managed_path`, `sha256`, `size_bytes`, and `status=available`.
- The adapter never deletes the source file.

### 9.2 Path and payload binding

For a task-bound Runtime:

- sandbox paths MUST resolve through the known task workdir or task artifact mount;
- host paths MUST remain under the Task Runtime Workdir or Task Artifact Root;
- path cleaning, symlink evaluation, and final opened-file identity MUST all remain inside the permitted roots;
- a path that is swapped after validation is detected by hashing the opened file descriptor and rechecking identity before publication;
- arbitrary host paths, the project database, Runtime credential files, and another Task's roots are forbidden.

An operator may retain from an explicitly selected local source root. That root and operator identity are recorded outside graph payload properties; graph provenance still records the operator.

### 9.3 Durable orchestration

Evidence retention uses a durable project-interface request record keyed by trusted idempotency scope plus key. The canonical request hash includes the resolved source file identity, SHA-256, size, graph references, and semantic metadata.

The exact sequence is:

1. resolve trusted context and permitted source path;
2. open, hash, and size the source;
3. reserve or replay the durable interface request;
4. copy to a temporary file under the correct Task Artifact Root, fsync, and atomically publish a content-addressed managed path;
5. call Apply with a derived graph idempotency key, creating or patching the EvidenceArtifact and atomically putting `produced` / requested `evidences` / `about` edges;
6. store the exact compound result and mark the interface request complete.

Crash and retry behavior:

- failure before file publication leaves no graph state;
- file publication followed by process death is recovered by reusing the verified managed file and replaying Apply;
- graph commit followed by lost response replays both graph and compound result;
- same scope/key with changed source bytes or metadata returns `evidence_source_changed` or `idempotency_conflict`;
- a retained file whose graph mutation is permanently rejected remains outside graph truth and is eligible for explicit cleanup/reconciliation.

The response returns the EvidenceArtifact envelope, created/current edge envelopes, managed relative path, content hash/size, and underlying MutationResult.

## 10. Semantic operation 5: Checkpoint Attempt

An Attempt checkpoint creates a compact, Attempt-bound Task Event and updates the open Attempt's summary through Apply. It is the recovery anchor for normal and interrupted reconciliation; it is not a raw command log.

### 10.1 Request

~~~json
{
  "protocol_version": 1,
  "idempotency_key": "admin-enum:checkpoint-2",
  "attempt": {
    "node_type": "attempt",
    "stable_key": "attempt:task-7:admin-enum"
  },
  "expected_version": 2,
  "summary": "Enumerated the authenticated shell; role-management API remains untested."
}
~~~

The Attempt MUST:

- resolve to a canonical main Attempt;
- have `status=open`;
- have Runtime provenance matching the bound Task and Continuation;
- match `expected_version`.

### 10.2 Effects and idempotency

The adapter:

1. reserves a durable interface request;
2. appends one idempotent Task Event with:
   - kind `blackboard_checkpoint`;
   - bound `continuation_id`;
   - `attempt_node_id`;
   - compact summary;
   - interface idempotency scope/key;
3. calls Apply with a derived key to `patch_node` summary and binds the checkpoint Event ID to that operation's provenance;
4. stores the exact compound result.

The Event commits before Apply because Task Events and graph state are separate aggregates. If Apply then fails, the Event remains a truthful checkpoint and the error details include its ID. Retrying the same key reuses that Event and resumes/replays Apply; it never creates another checkpoint Event.

If the normalized summary already equals the graph summary, Apply records an ordinary no-op while the first-seen checkpoint Event remains available to recovery.

## 11. Semantic operation 6: Finish Continuation protocol

Finish is the last project-interface write a Runtime makes in one Continuation. It does not complete the Task and does not infer or close an ExplorationObjective.

### 11.1 Request

~~~json
{
  "protocol_version": 1,
  "idempotency_key": "continuation-finish",
  "summary": "Confirmed the authenticated admin surface and retained the request/response. Role-management authorization remains untested.",
  "objective_outcome": {
    "objective": {
      "node_type": "exploration_objective",
      "stable_key": "objective:find-admin-surface"
    },
    "status": "supported",
    "supporting_node_refs": [
      {
        "node_type": "project_fact",
        "stable_key": "endpoint:admin-panel"
      }
    ]
  }
}
~~~

`summary` is required. `objective_outcome` is optional and is allowed only when the Task is pursuing the referenced primary ExplorationObjective. Its status is `supported`, `contradicted`, `inconclusive`, or `blocked`. Supporting references MUST resolve in the same Project. The outcome is stored in the Task Summary Version; it does not transition the Objective.

### 11.2 Preconditions and atomic effects

Finish:

1. resolves the bound Continuation grant and checks for an exact stored Finish replay before applying new-write state gates;
2. canonicalizes and checks the finish idempotency key/hash;
3. starts `BEGIN IMMEDIATE` so Finish serializes with Apply and every direct CLI writer;
4. revalidates the grant and verifies that no canonical main Attempt created by this Continuation remains open;
5. validates Objective Outcome references;
6. in that same transaction:
   - creates an idempotent Task Summary Version bound to the Continuation;
   - records the finish request hash/key;
   - records the current graph revision and latest Runtime mutation sequence for the Continuation;
   - records the Summary Version ID and finish timestamp;
   - closes the grant to new writes.

Apply authoritatively revalidates the grant only after it acquires its own `BEGIN IMMEDIATE` writer lock. Therefore an Apply that wins the lock commits before Finish observes the graph, while a Finish that wins the lock closes the grant before a later Apply can validate. No Runtime mutation can commit “behind” a successful Finish.

If open Attempts exist, Finish returns `continuation_open_attempts` with sorted node references and changes nothing. The Runtime must conclude them through Apply and retry Finish.

Same key and same request returns the original finish result. Same key with a different summary or outcome returns `continuation_finish_conflict`. After a successful Finish:

- exact replays and reads remain allowed;
- any new mutation, Evidence retain, or checkpoint fails with `continuation_closed`;
- the Runtime should exit;
- the Harness, not Finish, still owns Task and Continuation terminal status.

## 12. Transport parity

### 12.1 Canonical mapping

| Semantic capability | Trusted MCP tool | `pentestctl` command | HTTP |
| --- | --- | --- | --- |
| Apply mutation | `blackboard_apply` | `blackboard apply --input <file|->` | `POST /api/projects/{project_id}/blackboard/mutations` |
| Resolve records | `blackboard_resolve_records` | `blackboard records resolve --input <file|->` | `POST /api/projects/{project_id}/blackboard/records:resolve` |
| Current Runtime graph | `blackboard_get_current_graph` | `blackboard graph current` | `GET /api/projects/{project_id}/blackboard/runtime-graph` |
| Retain Evidence | `blackboard_retain_evidence` | `blackboard evidence retain --input <file|->` | `POST /api/projects/{project_id}/blackboard/evidence:retain` |
| Checkpoint Attempt | `blackboard_checkpoint_attempt` | `blackboard attempt checkpoint --input <file|->` | `POST /api/projects/{project_id}/blackboard/attempts:checkpoint` |
| Finish Continuation | `blackboard_finish_continuation` | `blackboard continuation finish --input <file|->` | `POST /api/projects/{project_id}/tasks/{task_id}/continuations/{continuation_id}:finish` |

Transport adapters MUST pass a shared conformance suite. Given equivalent trusted context and request JSON, they return the same domain/compound result JSON and stable error code. Transport-only metadata such as HTTP request IDs, headers, and CLI exit status may differ.

### 12.2 MCP

- The trusted MCP endpoint authenticates with a Continuation Interface Grant.
- Task-bound tool schemas omit Project, Task, Continuation, Runtime Profile, Runner, actor, and timestamp fields.
- Tools return the canonical JSON in both text content and structured content where the SDK supports it.
- A domain/interface failure returns `isError=true` with `ProjectInterfaceErrorV1`; it is not converted to an unstructured transport exception.
- Tool descriptions are generated from the same versioned Runtime Protocol source as task instructions.
- External MCP servers do not receive a Continuation Interface Grant and cannot call trusted tools as a Runtime Project Interface.

The six v1 tool names are stable. Compatibility tools remain a migration concern and MUST translate to the same graph service rather than become another canonical write path.

### 12.3 CLI

Task mode:

- defaults to `.pentest/context.json` plus `PENTEST_INTERFACE_TOKEN`;
- Project and Continuation are implicit;
- `--project`, `--task`, `--continuation`, `--runtime-profile`, `--runner`, and Runtime actor overrides are rejected;
- calls the daemon project-interface HTTP routes using `PENTEST_API_URL` and the Continuation grant;
- does not require or receive a mount of the Project SQLite database.

Operator mode:

- requires `--project <id>` and `--actor-id <stable local identity>`;
- produces operator provenance only;
- cannot call checkpoint or Finish without a valid Continuation grant;
- supports daemon mode through `--api <url>` and offline/local mode through `--db <path>`;
- in `--db` mode opens the same SQLite store and calls the same project-interface module, preserving the storage contract's concurrent daemon/CLI writer serialization.

CLI output is one JSON document on stdout for success and one `ProjectInterfaceErrorV1` document on stderr for failure. Stable exit statuses are:

| Exit | Meaning |
| --- | --- |
| 0 | Success, including exact replay or no-op. |
| 2 | Usage, malformed JSON, or unsupported protocol version. |
| 3 | Authentication, grant, or actor authorization failure. |
| 4 | Project/record not found. |
| 5 | Version, idempotency, key, transition, merge, finish, or other conflict. |
| 6 | Semantic validation failure. |
| 7 | Retryable storage failure. |
| 1 | Unexpected internal/integrity failure. |

### 12.4 HTTP

Operator/UI HTTP uses the daemon bearer credential and resolved operator identity. Runtime HTTP uses a Continuation Interface Grant. When a Runtime token is used, every Project/Task/Continuation path value MUST match the grant or the request fails without graph access.

Status mapping:

| HTTP | Cases |
| --- | --- |
| 200 | Successful action, exact replay, or no-op. |
| 304 | Current graph ETag unchanged. |
| 400 | Malformed JSON, invalid request envelope, unsupported protocol version. |
| 401 | Missing/invalid credential. |
| 403 | `actor_forbidden`, revoked grant, or forbidden Evidence path. |
| 404 | Project, current node/edge, or Continuation not found. |
| 409 | Version/idempotency/key/edge/transition/merge conflict, closed Continuation, open Attempts at Finish. |
| 422 | Well-formed request violating graph schema, endpoint matrix, property, cycle, guard, or invariant rules. |
| 429 | Explicit local rate limit, with `Retry-After`. |
| 500 | Unexpected internal or integrity failure. |
| 503 | Retryable SQLite busy/unavailable, with `Retry-After`. |

Mutation/action responses use `Cache-Control: no-store`. Current graph uses `Cache-Control: private, no-cache` and a projection-hash ETag. No endpoint returns HTTP 200 with an error body.

Implementation MUST publish and CI-validate an OpenAPI document for these routes. The Markdown contract remains the decision source; the OpenAPI document is the generated/executable transport contract.

## 13. Continuation pinning and task-local files

### 13.1 Launch order

The existing order—project Runtime config first, Continuation later—must be replaced. Before starting a Runtime:

1. reconcile the Task-owned Goal projection;
2. run required pre-pin compaction/Health work without truncating the graph;
3. resolve the exact Task Runtime Configuration Version;
4. in one short `BEGIN IMMEDIATE` transaction:
   - capture that Runtime Configuration Version;
   - render and pin the current `CanonicalMainGraphV1` revision/hash/size/versions;
   - create the Continuation with reconciliation status pending;
   - create the Continuation Interface Grant hash and bound context;
5. commit;
6. materialize task-local files from the pinned revision;
7. verify the snapshot bytes and hash;
8. project Runtime-specific config, MCP, instructions, credentials, and launch arguments using that exact Continuation;
9. start the Runtime process only after every mandatory file exists.

A crash after the database transaction but before files are complete regenerates files from graph history for the same pin. It does not create another Continuation or select a newer graph revision.

### 13.2 Files

The task workdir contains:

| Path | Contract |
| --- | --- |
| `.pentest/blackboard.json` | Exact canonical bytes of the pinned `CanonicalMainGraphV1`. Read-only input; never a write channel. |
| `.pentest/context.json` | Non-secret identifiers, protocol/snapshot metadata, and paths. |
| `.pentest/scope.json` | Immutable Scope Snapshot already captured for the Task. |
| `AGENTS.md` | Canonical Runtime Protocol block plus task/runtime pointers for runtimes that read AGENTS instructions. |
| `CLAUDE.md` | The same canonical protocol block plus task/runtime pointers for Claude-compatible instruction discovery. |

`.pentest/context.json` has:

~~~json
{
  "protocol_version": 1,
  "project_id": "project id",
  "task_id": "task id",
  "continuation_id": "continuation id",
  "runtime_config_version_id": "runtime config version id",
  "runtime_profile_id": "runtime profile id",
  "runtime_plugin_id": "codex",
  "runner": "sandbox",
  "api_url": "http://host.docker.internal:8787/api",
  "mcp_url": "http://host.docker.internal:8787/mcp",
  "scope_path": ".pentest/scope.json",
  "blackboard_path": ".pentest/blackboard.json",
  "blackboard_graph_revision": 42,
  "blackboard_renderer_version": "canonical_main_graph_v1",
  "blackboard_estimator_version": "utf8_bytes_div_4_v1",
  "blackboard_projection_hash": "sha256",
  "blackboard_projection_bytes": 2048,
  "blackboard_estimated_tokens": 512
}
~~~

No token or credential value appears in this file. The process receives `PENTEST_INTERFACE_TOKEN`, `PENTEST_API_URL`, `PENTEST_MCP_URL`, `PENTEST_PROJECT_ID`, `PENTEST_TASK_ID`, and `PENTEST_CONTINUATION_ID`. IDs are convenience only; the token binding remains authoritative.

Files are written atomically with owner-only permissions. Sandbox mounts make `blackboard.json` and `scope.json` read-only. Host-mode modification has no effect on canonical state and is detected if the file is re-verified; regeneration always uses the pinned database revision.

## 14. Canonical Blackboard Runtime Protocol v1

The daemon owns one versioned source string. Every built-in Runtime receives these normative rules:

1. **Start from the pinned full graph.** Read the initial Blackboard context and `.pentest/blackboard.json`. It is the complete main graph at the stated revision, not a relevance-selected subset.
2. **Treat the snapshot as immutable.** Never edit it as a write mechanism. Explicit current-graph reads may show later concurrent changes but do not replace the pinned Continuation context.
3. **Write semantic milestones, not command noise.** Raw commands, full logs, and payload bytes remain Task Events, logs, or retained Evidence.
4. **Open work explicitly.** Before an exploration episode, create or reuse an Exploration Objective when needed, create one Attempt, and put at least one `tests` edge in the same atomic batch.
5. **Keep provenance honest.** Never send Project, Task, Continuation, Runtime Profile, Runner, actor, or timestamp claims. The trusted interface binds them.
6. **Use stable identities and optimistic versions.** Reuse stable keys for the same durable concept, supply current expected versions, and reread on `version_conflict`.
7. **Make retries replay-safe.** Choose an idempotency key before each semantic action and reuse that exact key and payload after uncertainty. Never reuse a key for a different payload.
8. **Checkpoint meaningful progress.** Use Attempt checkpoint after a material phase so interruption recovery has a compact truthful summary.
9. **Record outcomes with their reasoning chain.** Link Runtime-created outputs with `produced`, retain proof with Evidence, and use `supports`, `contradicts`, `evidences`, `satisfies`, and lineage edges precisely.
10. **Conclude every Attempt.** Transition it once to `succeeded`, `failed`, `blocked`, or `inconclusive` with a distilled summary. Do not mark `interrupted` yourself.
11. **Resolve Objectives only with `satisfies`.** A Task Summary outcome alone does not close an Objective.
12. **Treat scope labels as memory, not authorization.** Follow `.pentest/scope.json` and the Runner/task policy. Blackboard scope status never grants permission.
13. **Finish last.** After all current-Continuation Attempts are terminal and the graph is current, call Finish with a compact handoff summary. Make no later Blackboard write in that Continuation.
14. **Do not hide protocol defects.** If a trusted operation fails, surface the stable error and retry only when its contract says retryable.

The generated protocol block includes the protocol version, current Continuation ID, pinned graph revision/hash, file paths, trusted tool names, and a warning that Runtime/Profile instructions may add guidance but cannot replace these rules.

MCP descriptions, generated instruction files, and launch/system context are rendered from this one source. A test fixture MUST fail when their embedded protocol version or normative rule digest diverges.

## 15. Runtime-adapter projection

The full graph is delivered once per Continuation through the adapter's initial context renderer and once on disk as exact canonical JSON. Generated instruction files reference the snapshot; they do not duplicate the full graph and inflate context.

| Runtime Plugin | Required projection |
| --- | --- |
| Codex | Generated `AGENTS.md`; trusted MCP in Codex config; launch context includes protocol/version and the complete pinned graph rendering. |
| Claude Code | Generated `CLAUDE.md` and `AGENTS.md`; strict trusted MCP config; launch context includes protocol/version and the complete pinned graph rendering. |
| Pi | Generated `AGENTS.md` and `CLAUDE.md` for discoverability; trusted MCP in Pi config; the launch context carries the protocol and complete pinned graph because instruction-file discovery is not assumed. |
| Fake/test | Receives the same logical protocol, context metadata, and snapshot fixture through the fake adapter seam. |
| Future Runtime | Its Runtime Plugin must declare an instruction projection capable of delivering the canonical protocol and full pinned graph before it is considered Blackboard-ready. |

Where a Runtime exposes a true system-instruction channel, the canonical protocol goes there. Otherwise it is an immutable daemon-generated launch prefix before the user-authored Task Goal. Profile instructions are appended after the canonical protocol and MUST NOT suppress it.

Native resume creates a new Continuation and therefore a new pin. The resume message includes a clearly delimited `CURRENT CONTINUATION SNAPSHOT` carrying the new revision/hash and complete graph. It states that older snapshot blocks in the native session are historical and MUST NOT be treated as current.

Prompt presentation may become more readable under the read-projection ticket, but it MUST remain a lossless rendering of all `CanonicalMainGraphV1` content, name its renderer version, and never omit or relevance-filter records. `.pentest/blackboard.json` remains the exact machine-readable fallback.

## 16. Normal completion and interruption reconciliation

### 16.1 Clean completion after Finish

When the Runtime process exits cleanly after a valid Finish marker:

1. the Harness verifies the marker's recorded graph revision/latest Runtime mutation sequence;
2. it verifies there are no open Attempts for the Continuation;
3. it marks `blackboard_reconciliation_status=completed` with the finish/summary references;
4. it records no graph mutation merely to acknowledge completion.

Because Finish closes new writes, no later Runtime mutation can silently invalidate the marker.

### 16.2 Clean completion without a valid Finish

A cleanly completed Continuation still runs normal reconciliation:

1. load all canonical main open Attempts created by that Continuation, ordered by stable key then ID;
2. if none remain open, mark reconciliation completed and record a compact system Task Event that Finish was omitted;
3. if any remain open, leave their semantic state unchanged, mark reconciliation failed with the sorted Attempt references, and let Blackboard Health report `completion_protocol_gap` / `completion_protocol_stuck` under the storage contract.

A clean process exit proves only that the Runtime process ended; it does not prove whether an unfinished exploration episode was failed, blocked, or inconclusive. Normal reconciliation therefore MUST NOT guess an Attempt terminal status.

Normal reconciliation also does not invent Observations, Facts, Findings, Solutions, Evidence edges, Objective resolutions, or a Runtime-authored Task Summary. If no accepted Task Summary exists, later Continuation handoff uses the Mechanical Handoff Packet. An operator or later trusted Runtime may explicitly conclude the stranded Attempt with the evidence available; interruption recovery must not relabel it merely because it is old.

### 16.3 Unexpected terminal Continuation

Failed process execution, forced stop, timeout, daemon restart recovery, or another unexpectedly terminal Continuation follows the storage contract's interruption algorithm:

- open matching Attempts become `interrupted`;
- summaries come from Attempt state, matching checkpoint Events, terminal lifecycle/error Events, or the fixed fallback;
- Event references are ordered, deduplicated, matching, and bounded;
- no Evidence relationship is inferred from shared Continuation provenance;
- retries converge through the reconciliation idempotency key and maintenance subject.

A valid terminal Attempt is never overwritten. Task and Continuation statuses remain owned by the Task domain.

### 16.4 Races and anomalies

- Runtime terminal transition first: reconciliation rereads and leaves it unchanged.
- Reconciliation transition first: stale Runtime Apply receives `version_conflict`.
- Finish and process exit race: the finish Task-domain transaction either commits first and is observed, or normal/unexpected reconciliation proceeds from durable state.
- Graph reconciliation committed but marker update lost: startup finds the reconciliation mutation by maintenance subject or observes no open Attempts and completes the marker without another mutation.
- Reconciliation marked complete while an Attempt remains open: Health reports `completion_protocol_gap` / `completion_protocol_stuck` and startup reruns the normal reconciliation audit; it does not relabel a clean completion as interruption.

## 17. TDD acceptance seams

The pre-agreed behavioral seams are:

1. `BlackboardGraphService.Apply` for graph semantics;
2. the project-interface module for grant binding and compound operations;
3. each MCP/CLI/HTTP adapter through its public interface;
4. Continuation preparation through the Runtime launch interface;
5. normal/unexpected reconciliation through durable Task/Continuation state.

Tests observe results through these interfaces. They do not query SQLite to prove domain behavior. Storage, crash, token-hash, file-publication, and recovery tests may inspect a real temporary file-backed SQLite database and Artifact Root behind the seam. Stable injected internal seams are limited to clock, ID/token source, trusted context resolver, artifact filesystem, projection renderer, and failure points.

Minimum red-first matrix for this ticket:

1. Equivalent Apply through MCP, task CLI, operator CLI, and HTTP produces the same MutationResult and current graph.
2. A task-bound request cannot supply or spoof Project, Task, Continuation, profile, Runner, actor, or timestamp.
3. A path Project/Task/Continuation mismatch fails before graph access.
4. Invalid, revoked, finished, and terminal grants reject new writes; exact replay remains recoverable.
5. Actor authorization covers every operation and restricted node transition.
6. Source Event references accept same-Task records, reject cross-Task records, and bind new Events to the current Continuation.
7. Same idempotency key and payload returns byte-identical result after a lost response; changed payload conflicts.
8. MCP errors are structured `isError` results; HTTP status and CLI exit mappings match the same stable code.
9. Resolve Records returns current versions, alias/merge resolution metadata, and missing references at one observed revision.
10. Current Graph is full, deterministic, ETag-aware, and does not repin or rewrite the Continuation snapshot.
11. Evidence retention rejects path traversal, symlink escape, another Task's root, and source-file replacement races.
12. Evidence retry converges across file-publication, graph-commit, and lost-response crash points.
13. Runtime Evidence receives system-computed path/hash/size and matching `produced` provenance.
14. Checkpoint creates one Event, patches one Attempt, binds the Event to provenance, and reuses the Event after partial failure.
15. Finish rejects open Attempts without writing Summary/marker.
16. Finish stores Summary, optional Objective Outcome, graph position, and write-closed grant atomically.
17. Any new write after Finish fails while Finish replay succeeds.
18. Clean exit with a valid Finish produces no reconciliation mutation.
19. Clean exit without Finish completes reconciliation when no Attempt is open, but preserves open Attempts and emits the protocol-gap Health result when semantic state is incomplete.
20. Unexpected end transitions only matching open Attempts to `interrupted`.
21. Runtime completion versus reconciler races are decided by expected versions and never double-transition.
22. Continuation creation atomically binds Runtime Configuration, graph pin, reconciliation state, and grant.
23. Missing task-local snapshot regenerates byte-identically at the same pin before launch.
24. Snapshot hash mismatch prevents Runtime start with `snapshot_unavailable`.
25. Every built-in adapter receives the same protocol version/rule digest and full graph.
26. Native resume receives a new full snapshot and clearly supersedes historical snapshot context.
27. A graph at or above 20K estimated tokens is still delivered in full; no adapter truncates or relevance-filters it.
28. Compatibility tools, when implemented by the migration ticket, pass the same graph-service conformance suite.

The downstream slicing ticket decides vertical red-green order. It MUST NOT turn this matrix into one horizontal “write every test, then implement everything” phase.

## 18. Existing implementation delta

Current code provides useful adapter patterns but violates this contract in known ways:

- trusted MCP uses a daemon-wide token and accepts caller-supplied Project IDs;
- task context lacks Continuation, Runtime Configuration Version, Runtime Profile, Runner, graph revision/hash, and a bound grant;
- the Continuation is created after Runtime config/file projection rather than atomically with the graph pin;
- only `AGENTS.md` is generated; `CLAUDE.md` and a versioned canonical protocol source are absent;
- current Runtime instructions mention Fact-centric compatibility tools rather than typed graph milestones;
- no `.pentest/blackboard.json` pinned full graph exists;
- MCP errors are unstructured text;
- CLI coverage is narrower than MCP/HTTP and has no task-bound provenance token;
- HTTP handlers use route-specific error shapes and no graph protocol envelope;
- Evidence attachment does not provide the retained-file/idempotent graph saga defined here;
- Task Events lack Continuation/Attempt checkpoint idempotency fields required by recovery;
- Task Summary Versions are not bound to Continuations or Finish requests;
- normal completion does not verify graph Attempts or persist protocol-gap state;
- interruption recovery does not yet operate on graph Attempts.

Implementation replaces these behaviors at the shared seams. It MUST NOT layer a second graph write path beside Apply.

## 19. Downstream boundaries and frontier

This resolution leaves the existing downstream tickets intact:

- the read-projection ticket owns richer Runtime presentation, operator/API projections, Health/report/UI shapes, search, history, provenance traversal, and Graph Explorer;
- the migration ticket owns exact translation and deprecation of current Fact/Finding/Evidence/Task Summary tools, routes, commands, and consumers;
- the TDD slicing ticket owns implementation sequence and commit-sized tracer bullets;
- the final assembly ticket owns the unified refactor specification.

No new Wayfinder ticket is required. The already-listed downstream tickets cover every newly sharpened dependency.
