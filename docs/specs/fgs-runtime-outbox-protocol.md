# FGS Runtime and Outbox protocol

Status: protocol design with implemented FGS integration. Current Projects and
Sessions select FGS; historical records remain unconverted. See section 11 for
implementation scope. Proposed details below are not a promise of CLI support.

Decision: [ADR 0035](../adr/0035-use-fgs-as-the-blackboard-working-model.md).
Domain terms: [CONTEXT.md](../../CONTEXT.md).

Current feature boundary: [supported and retired behavior](../product/current-boundary.md).
Normal Project Task Policy enforcement and legacy Intent settlement are retired;
older references to them below do not apply to current launches.

## 1. Purpose and confirmed scope

FGS replaces Exploration Objective, Attempt, and Project Fact with Goal, Step,
and Fact. The user also confirmed removal of Entity, Finding, Solution, and
Evidence Artifact as target Blackboard node types. The target graph contains
only Goal, Step, and Fact. Historical record and file deletion is not authorized
by this model decision. Runtime reporting uses structured
Outbox updates. Blackboard displays the accepted graph. The user confirmed core
FGS instruction delivery through Harness-projected AGENTS.md / CLAUDE.md. No FGS
Skill invocation is required. Detailed schemas and examples are local references.

The user confirmed these seven principles on September 6, 2026:

1. Blackboard stores accepted durable state; local files store working state.
   Recovery reconciles unconfirmed Outbox updates without overwriting local work.
2. Outbox carries explicit structured graph updates.
3. Ingest during Runtime work and check again at the end. Reported graph state
   does not prove agent liveness.
4. Decide publishes; Execute agents return result files. One Runtime can do both.
5. Fact content is append-only. Corrections are new Facts; changed conditions do
   not make an earlier accurate observation false.
6. Step completion means work ended with a result. Goal completion requires its
   success criteria and implies neither Task completion nor external platform
   verification of a result.
7. Receipts expose failures. New updates repair or withdraw rejected updates;
   dependent updates wait while independent execution can continue.

Other details below remain proposals. This draft does not change product code,
existing records, or a live Session.

## 2. Responsibility and authority

| Surface | Responsibility |
| --- | --- |
| Runtime / Decide | Select work, dispatch agents, interpret results, maintain local working state, publish FGS updates |
| Execute agent | Perform its assigned work and return a bounded result file tied to its Step |
| FGS Runtime Instructions | Teach the reporting loop, file rules, and recovery procedure; link schemas and examples |
| Harness | Bind Owner and Continuation, receive stable files, enforce transport limits, retry delivery |
| Blackboard service | Validate FGS operations and references, commit state and receipt atomically, provide reads |
| Blackboard UI | Show accepted nodes, edges, reported states, update age, and delivery problems |

No semantic update is inferred from chat, native Tasks, or arbitrary workdir
files. A Step state is a Runtime report, not proof of process liveness. Existing
Scope, Task Policy, lifecycle controls, and provider authority still apply.

A Project owns the graph shared by its Tasks. A Session owns an isolated graph.
Trusted Origin records which Runtime Owner and Continuation submitted a change.
Neither identity nor Scope can be selected in an FGS payload.

## 3. Durable state and local files

Blackboard owns accepted FGS state. Outbox owns published but possibly unaccepted
updates. Runtime files may contain newer drafts. These are three distinct states.

```text
state.md                         short Runtime navigation note
graph/goals.yaml                  Runtime working Goals
graph/steps.yaml                  Runtime working Steps
graph/facts/<fact-key>.md         immutable local Fact bodies
graph/data/                      large output, addressed by Fact references
graph/results/<step-key>/        Execute result files awaiting Decide processing
graph/outbox/<continuation>/     immutable published update envelopes
graph/receipts/<continuation>/   Harness projections of durable receipts
```

Only Outbox is ingested. Markdown and YAML are not a second parser input. An
Execute result is not accepted graph state until Decide submits it. Receipts
are not Runtime-editable authority. Filesystem edits cannot change DB receipts.

`state.md` points to active Goals, current Steps, and unconfirmed update IDs. It
does not duplicate every Step status. FGS Runtime Instructions direct the Runtime
to update it when those pointers change. Native Tasks can serve provider UI needs, but must not become
an independently maintained semantic plan.

Restore accepted graph state through a read command; keep local drafts and
published envelopes intact. A refresh writes a separate snapshot file or returns
JSON. It never overwrites Runtime files to make them match the DB.

## 4. Minimal FGS nodes

Keys are stable, readable Blackboard Keys: for example `goal:service-access`,
`step:check-connectivity`, and `fact:connectivity-empty-response`. They are unique
across the owning graph, never reused, and contain no Task or Continuation IDs.
Database versions and Trusted Origin are service-owned. Fact keys are not limited
to the old `fact_0001` pattern.

| Node | Required fields | Optional fields |
| --- | --- | --- |
| Goal | `key`, `title`, `success_criteria` | `parent_goal` |
| Step | `key`, `goal`, `action` | `inputs` (Fact keys), `after` (Step keys), `priority`, `executor` |
| Fact | `key`, `step`, `summary` | `body`, `data_refs`, `corrects` (Fact key) |

Goal creation starts at `open`. Step creation starts at `open`. Priority defaults
to `normal` and accepts `high`, `normal`, or `low`. Executor is a reported label,
not an authority or a verified provider agent identity.

A Fact records an observation or result, including unsuccessful work. Its
content is immutable. To correct a false statement, add a Fact with `corrects`
and an explanation. A later observation of a changed environment is a new Fact,
not automatically a correction of an earlier accurate observation.

External information can be recorded through a Step such as “read operator
input.” This keeps a common output relation without claiming tool execution.

Large data remains outside node text. A data reference includes a relative
workdir path, digest, and size. It is a local reference, not proof of durable file retention.
Missing bytes remain visible as unavailable; they do not remove the Fact.
The target graph has no Evidence Artifact node. Durable file retention and its
Project/Session boundaries need a separate design.

### States

| Node | Allowed transitions | Guard |
| --- | --- | --- |
| Goal | `open -> active`, `open/active -> done`, `open/active -> abandoned` | `done` needs output Fact references and a completion summary; `abandoned` needs a reason |
| Step | `open -> running`, `open/running -> blocked`, `blocked -> open`, `open/running/blocked -> done`, `open/running/blocked -> cancelled` | `blocked` and `cancelled` need a reason; `done` needs at least one output Fact |

Done means the Step ended with a recorded outcome; it does not mean success.
Cancelled means the intended work is not continuing and does not require a
fabricated observation. Goal and Step descriptions can change with Semantic
History retained. A terminal Goal can explicitly reopen to `open` with a reason;
retain prior completion Facts and summaries in history. A terminal Step does not
reopen: a retry creates a new Step that references the earlier Facts. These
behaviors are user-confirmed. Reopening a child Goal under a done parent requires
explicitly reopening the parent in the same batch; do not silently change it.

`done` Goal guards require child Goals to be terminal and attached Steps to be
terminal in the same accepted graph. Independent work can continue under another
Goal. Goal completion never completes a Task or proves external platform acceptance.

Allow `open -> done` for a short Step reported after execution. The UI must not
invent a prior running interval. Transition timestamps are receipt timestamps;
optional future observed times must be separately labelled.

### Relations

| Source | Relation | Target | Source field |
| --- | --- | --- | --- |
| Goal | `part_of` | Goal | `parent_goal` |
| Step | `toward` | Goal | `goal` |
| Step | `uses` | Fact | `inputs` |
| Step | `depends_on` | Step | `after` |
| Step | `produces` | Fact | Fact `step` |
| Fact | `satisfies` | Goal | Goal completion `facts` |
| Fact | `corrects` | Fact | `corrects` |

Create edges from these fields; do not submit a duplicate edge list. Reject
missing endpoints, wrong types, cross-graph references, self-links, and cycles
in Goal hierarchy, Step dependencies, or Fact correction chains. A Fact used
as input cannot be produced by that same Step. Reject causal cycles through
Step input/output links. One Fact has one producing Step. A Fact can feed many
Steps. A completion Fact must be produced under that Goal or its descendants.

A Step dependency is a scheduling hint to Runtime. Accept a reported running
Step even if its dependencies are not done, but expose that inconsistency. The
Harness cannot prove actual execution order from report arrival order.

Entity, Finding, Solution, and Evidence Artifact are not retained as graph node
types. Do not add them back as mandatory Fact subtypes. Facts can describe assets,
findings, candidate results, platform responses, and references to data. The exact
representation and report projections remain design work. Removing the node types
does not delete evidence bytes or imply removal of platform operations. Migration
must cover all legacy node types, relationships, history, and report behavior
before full cutover. The first slice uses a fresh isolated FGS fixture.

## 5. Update operations and Outbox envelope

Initial operations: `goal.create`, `goal.describe`, `goal.transition`, `step.create`,
`step.describe`, `step.transition`, and `fact.append`. No arbitrary property map, SQL, automatic
upsert, or legacy semantic batch is accepted.

Transitions carry `from` and `to`. The service rejects a stale `from`; it never
silently rebases a transition against another Task's change. Completion also
carries `facts` and `summary` for Goals, or `outputs` for Steps. Output keys must
refer to Facts produced by that Step. Reasons are required as specified above.
Description operations update Goal `title` / `success_criteria` or Step `action`.
They carry expected prior values for changed fields to reject stale concurrent
edits without Runtime-supplied DB versions. They cannot change keys, relationships,
states, or Fact content. Changing a done Goal's success criteria requires an
explicit reopen in the same batch. Every accepted change retains Semantic History.

The CLI wraps a submitted operation list in an envelope:

```json
{
  "schema": "fgs-update/v1",
  "id": "intent_00000001",
  "sequence": 1,
  "operations": [
    {"op": "goal.create", "key": "goal:service-access", "title": "Check service access", "success_criteria": "Receive a valid health response"},
    {"op": "step.create", "key": "step:check-connectivity", "goal": "goal:service-access", "action": "Read the health endpoint"}
  ]
}
```

The Runtime supplies only `operations` and an optional `resolves` identity.
The CLI assigns envelope ID and sequence with a process-safe allocator that
releases its lock on process exit. Gaps after a failed publish are valid.
The envelope is written to a temporary file, synced, then atomically published
without replacing an existing file. Initial limits: 1 MiB per envelope, 100
operations, 64 KiB per Fact body. These are proposed limits, to be validated.

Identity is `(owner_kind, owner_id, continuation_id, intent_id)`. Store the
validated payload bytes and hash on claim. An exact identity and payload replay
returns the same Receipt. A changed payload under the same identity is rejected.
Same short IDs in different Continuations are distinct. Node keys stay stable
across Continuations. New delivery IDs do not make duplicate node creation valid.

Apply an envelope in one DB transaction. Resolve operation references against
the staged transaction state, so create Step, append Fact, and finish Step can
share one envelope. Either all operations commit or none do. Update node history,
graph revision, and the applied receipt in that transaction. Receipt file delivery
can retry after commit. Do not recompile against newer node versions on replay.

## 6. Ingestion, ordering, errors, and recovery

Run a bounded owner mailbox scan while the daemon is active, including idle
Runtimes. A proposed normal polling interval is one second, with a bounded work
budget and backoff on repeated I/O failure. File notifications may wake a scan
early, but periodic scans recover missed notifications. Old applied envelopes
use a persisted scan cursor and receipt index rather than repeated full parsing.
Daemon startup discovers pending mailboxes from durable Continuation records.

Order envelopes by sequence within each Continuation. Do not invent a total
execution order between Task owners that share a Project. Blackboard transactions
serialize accepted mutations and state guards expose conflicts.

Transport states remain `pending`, `applying`, `retry_pending`, `applied`,
`action_required`, and `superseded`. A recovered `applying` claim resumes from its
stored payload under exclusive per-owner settlement. Infrastructure failures
remain retryable; validation errors identify the operation and field. Include
`applying` and unclaimed published files in delivery status, or label scan state
unknown. A zero DB pending count alone cannot mean the mailbox is clean.

An `action_required` update blocks ordinary later updates in its Continuation.
A resolution envelope is the only exception: `resolves` names the full identity
of the current rejected update and carries corrected operations, or an explicit
withdrawal reason with no operations. Scan it despite the blocked head. Reject
resolution of applied work, another Owner's work, or a non-current blocker.
Successful resolution and superseding the rejected update commit atomically;
failed resolution leaves the original blocker unchanged and returns its own
validation Receipt. Do not make that failed resolution a second queue blocker.
Then resume queued ordinary envelopes, validating each against current state.
Invalid resolution targets also get durable rejection Receipts. They must not
abort the scan before a later valid repair or withdrawal. A repair of a failed
repair still targets the original rejected update. No operation from a rejected
batch was applied; a replacement must contain the complete corrected batch.
Withdrawal discards the batch, including any Fact and Step operations before the
failed operation. Missing receipts are not proof of rejection.

A new Continuation can resolve an earlier blocker for the same Owner through
this explicit identity. Ordinary newer writes wait until older pending updates
settle or are explicitly resolved. Resume must permit this repair input rather
than require a clean graph before the Runtime can repair it.

Malformed canonical files get a durable delivery diagnostic keyed by Continuation,
filename, and digest. Do not silently skip them or depend on parsing their ID to
report the error. A repair command can explicitly withdraw this exact digest;
it preserves the original bytes. Never edit a claimed or published payload.

Stop must remain possible during failure. Stop the Runtime, attempt bounded
drain, then retain pending updates and diagnostics for daemon retry or operator
repair. Owner stop does not cancel accepted storage work or mark Steps done.
Finish readiness reports outstanding delivery; it never equates Runtime silence
with a complete Goal. Settlement uses the submission's stored authority binding,
not a new credential or a newer Continuation's identity.

Accepted files are read only as canonical regular files under the bound workdir.
Use safe directory-relative opens and size checks on the opened handle. Validate
ancestor paths and reject symlinks; a prior path check is not sufficient. Retain
published payloads until receipt delivery and explicit retention policy permit
cleanup. This draft authorizes no deletion of existing files or history.

## 7. Proposed Runtime interface and FGS Runtime Instructions

Project core rules through the instruction file that the selected Runtime
supports: AGENTS.md or CLAUDE.md. Keep one source for the FGS content so provider
adapters do not maintain different protocols. Verify file discovery on launch
and resume for each supported Runtime. These files are instructions, not
system-role messages, and do not guarantee compliance.

Include the rules in a clearly delimited Harness-owned section and link local
schema and example files. Preserve unrelated user instructions. Existing files
must not be replaced wholesale. Remove the redundant Working Graph Mode Skill
invocation during implementation; do not require both mechanisms. Subagent
dispatch still carries its Step and output contract explicitly, since instruction
inheritance cannot be assumed across providers.

Current Runtime commands are `emit --input`, `read --limit --cursor`, `status`,
and `history --key --cursor`. The additional command shapes below remain
proposals; use the [README](../../README.md#runtime-cli-pentestctl) for supported
examples:

```text
pentestctl working-graph validate --input update.json
pentestctl working-graph emit --input update.json
pentestctl working-graph status
pentestctl working-graph receipt --id intent_00000001
pentestctl working-graph read --active
pentestctl working-graph read --key step:check-connectivity
```

`emit` waits up to 5 seconds for the published update's Receipt and returns it.
Acceptance returns `applied`; rejection returns `action_required` and a nonzero
exit status. `--wait 0` returns `published` without waiting. The wait is bounded
at 30 seconds. Timeout returns `published` and a nonzero exit status with guidance
to inspect status, not to republish or withdraw the pending update. Receipt reads
are scoped to the current Continuation and update ID. This bounded feedback does
not schedule Runtime work or change the atomic Outbox acceptance contract.
`validate` runs schema and available reference checks without
mutation; acceptance is still decided at commit. `read --active` is a bounded
projection of active Goals, open/running/blocked Steps, related Fact summaries,
and pending delivery identities, with cursors for more data. Facts are read in
full only when needed. Repair uses a new `emit` input with `resolves`.

FGS Runtime Instructions teach this loop:

1. On startup or resume, read accepted active FGS and delivery status. Compare
   local drafts and old pending Outbox updates before selecting work. Do not
   reset working files or assume a missing Receipt means failure.
2. Create the Goal and next Step. Publish before proceeding to substantive work.
3. When dispatch succeeds, publish `running` with the executor label. Do not
   report a planned dispatch as an actual dispatch. A failed dispatch produces
   a bounded failure result and a blocked or cancelled Step report.
4. Give each Execute agent its Step key, relevant Fact bodies, and an exclusive
   result path. It writes a temporary result then publishes atomically. It does
   not edit shared goals, steps, state, or Outbox. Decide assigns final Fact keys.
5. Decide reads each result, appends Facts, reports the Step outcome, and creates
   useful next Steps. Publish these together when they depend on each other.
6. Check Receipts at the next decision boundary and before ending a Work Runtime
   Turn. Repair rejected updates. Continue independent work without a global
   wait-for-all-agents barrier. Do not wait indefinitely for persistence.
7. Keep local state pointers current. When reporting to the operator, distinguish
   published work, accepted graph state, and delivery failures.

### Reporting at decision boundaries

The projected instructions require an observed result before the Runtime selects
the next experiment. One Step can contain several tool calls, but a long sequence
of different experiments must not remain in one unchanged Step until success.
A finished experiment can have a negative result: publish its Fact and mark the
Step done. Cancel an approach that is no longer pursued, with a reason. Create a
new Step for a different approach or a retry of completed work. Use `inputs` for
the Facts that justify the choice and `after` when Step order matters. Use
`step.describe` with expected values when the same work needs a corrected
description. Publish dependent results and plan changes together.

Compare draft reports with the latest observations before publication. Keep
untested plans in Step actions. Fact summaries state observations; an optional
interpretation in the body must be labelled as a hypothesis, with its evidence,
limits, and remaining checks. Failure to obtain an expected result does not prove
the cause. A result file reference supports inspection but does not retain the
file by itself.

Correct each accepted false Fact with a new Fact whose `corrects` field names it.
Explain the false claim, new evidence, and any claims that remain valid. A later
summary alone is not an explicit correction. Update affected Step plans in the
same batch. Later Steps and Goal completion must use current supporting Facts,
not disproved claims.

Before ending a Work Runtime Turn, read accepted state and compare it with the
work done. Report missing results and correct inaccurate descriptions or Facts.
Goal completion must cite the Facts that support its success criteria, not all
historical Facts. Confirm that the final update ID is `applied`; an empty
`action_required` count alone does not prove acceptance.

These are Runtime reporting instructions. The Harness does not infer false Facts
from the Transcript or enforce the choice and timing of experiments. Deterministic
protocol tests do not prove that a model follows these semantic rules.

The linked protocol reference includes a schema and copyable JSON examples. Help must make
these discoverable without source-code access. Generic FGS has no CTF deadline,
platform API, target selection, score policy, or requirement to spawn agents.
Those remain in `ctf-orchestrator`. A single Runtime can own Decide and execution.

### Complete result example

After the first envelope in section 5 is accepted, or queued ahead in the same
Continuation, submit this input with `emit`:

```json
{
  "operations": [
    {"op": "fact.append", "key": "fact:connectivity-ok", "step": "step:check-connectivity", "summary": "The health endpoint returned status ok"},
    {"op": "step.transition", "key": "step:check-connectivity", "from": "open", "to": "done", "outputs": ["fact:connectivity-ok"]},
    {"op": "goal.transition", "key": "goal:service-access", "from": "open", "to": "done", "facts": ["fact:connectivity-ok"], "summary": "The health response meets the access check"}
  ]
}
```

The missing running report is allowed and remains visible as missing. This batch
does not change Session lifecycle or prove that every service is reachable.

## 8. Blackboard visual graph

Show Goal, Step, and Fact as distinct node types. Use the field-derived edges in
section 4. Default to active work and its input/output Facts. Offer filters by
Goal, Step state, and submitting Runtime Owner, plus explicit history expansion.

Selecting a Step shows its action, reported executor, dependencies, input Facts,
output Facts, reported state, and accepted update time. Selecting a Fact shows
its producing Step, content, data availability, and correction links.

Keep pending and rejected updates in a delivery panel, separate from accepted
nodes. Show last accepted update time and Runtime liveness separately. Do not
label the graph “live” or “complete” solely because no DB intents are pending.
An API or ingestion outage must show unknown delivery freshness. Corrected Facts
remain inspectable, with the replacement prominently linked.

## 9. Real-run acceptance and TDD slices

Reference: Session `session-01b4c0ea37bad88c3c4a4265925c4081`, September 3, 2026.
Use sanitized fixtures; no live platform requests, credentials, or attack commands
are needed. This Session used Claude Code / MiniMax-M3 and native agents. A
projected Skill file does not prove that the Runtime invoked or read that Skill.

| Scenario | Required observable result |
| --- | --- |
| Initial network failure | One access Goal, a Step, a failure Fact, and a blocked state are accepted |
| Network recovery | A later Fact records recovery and the Step is explicitly reopened or followed by a new Step; UI does not infer recovery from text |
| Challenge list obtained | Its Step is done with output; the graph no longer reports it pending |
| Three agents dispatched | Three distinct running Steps receive executor labels and isolated result destinations |
| One agent returns | Its Fact and Step outcome appear without waiting for the other two |
| Runtime omits an update | UI keeps the last reported state and exposes its age; it never fabricates completion |
| Native Tasks change alone | Accepted graph stays unchanged, proving the reporting gap rather than hiding it |
| Daemon restarts after graph commit | Same Receipt is recovered; no duplicate node or second transition |
| New Continuation starts at sequence 1 | Identity does not collide with the previous Continuation |
| Invalid update then correction | Resolution bypasses only the blocker; later updates resume without file edits |
| Multiple Tasks update the same Step | One guarded transition wins; the stale report gets a useful conflict |

Implementation slices, each with a failing behavior test before code:

1. Fresh FGS domain fixture: node types, transitions, edges, isolation, atomic
   same-batch references, immutable Facts, and correction history.
2. Durable ingestion: full identity, stored payload, atomic receipt/state commit,
   replay after each crash boundary, exclusive settlement, and resolution.
3. Runtime interface: emit, validation, read, receipts, safe file publication,
   incremental scanning, startup recovery, and lifecycle failure behavior.
4. FGS Runtime Instructions: examples pass schema tests. A fake Execute result flows through
   Decide submission into the graph. A small real Runtime run then tests whether
   the instructions are followed without an FGS Skill invocation or
   `ctf-orchestrator`; unit tests cannot prove compliance. Verify AGENTS.md /
   CLAUDE.md discovery, resume loading, and preservation of user instructions.
5. Blackboard UI: render the same accepted graph, delivery failures, correction
   links, bounded reads, and age independently from Runtime liveness.
6. Migration and cutover: explicit preview, backup, active-owner handling, legacy
   relationship and history mapping, reporting guards, verification, and rollback.

## 10. Decisions required before production cutover

Confirmed rollout: Store migration 77 upgrades existing Projects and Sessions to
FGS. Tasks inherit the upgraded Project protocol. New and resumed Runtime
Continuations receive FGS instructions; already running processes need a restart.
Disabled Blackboard Mode stays disabled. Historical records and evidence files
remain intact and are not converted into FGS nodes by this protocol upgrade.

The seven principles in section 1 are confirmed. Specific limits, commands,
schemas, transition guards, and migration details remain proposals. Instruction
delivery through AGENTS.md / CLAUDE.md is confirmed.

Still requiring a separate design: existing Fact confidence and deprecation
mapping; migration of all removed node types and their relationships; file
retention and FGS-based reports; historical Attempt terminal states;
interactive-mode FGS writes; explicit legacy migration tooling; public API
versioning; mailbox retention limits. Description editing, Goal reopening, and
new-Step retries are confirmed; their wire details above are implementation proposals.

Keep legacy data readable until a verified migration exists. Do not translate
new FGS writes back into legacy records as a shortcut. A feature-gated fresh
fixture is acceptable for the first slice; it is not a completed product migration.

## 11. Implementation status

The FGS service supports atomic updates, durable receipts, replay, description
history, Goal reopening, terminal Step retries, Fact corrections, priority, and
data references. Malformed JSON produces a durable rejected receipt that can be
repaired or withdrawn. Data references identify data; they do not retain files.
Step priority accepts `low`, `normal`, or `high`.

Migration 74 adds the FGS tables. Migration 75 persists the protocol choice on
Projects and Sessions. New owners select FGS. Migration 77 upgrades existing rows to FGS without deleting historical records.
Tasks inherit the Project protocol, including after a database restart.

Migration 76 adds the durable Outbox inventory. The daemon polls server-bound
Continuations during work in pages of 64. Each mailbox scan discovers at most 64
entries per call, then settles sorted pages of at most 64 entries. Up to 64 scans
can be active. Restart repeats discovery without losing accepted receipts. It uses
FGS settlement at lifecycle boundaries for FGS owners. Rejected FGS updates block
Task Finish until repaired or withdrawn. Recovery can resolve an earlier
Continuation even when ordinary work is already queued in a new Continuation.

Runtime launch projects managed FGS sections into AGENTS.md and CLAUDE.md while
preserving user instructions, plus `.pentest/fgs-input.schema.json`. FGS launch
omits the legacy Mode Skill. `pentestctl working-graph emit --input FILE`
publishes updates; `read`, `history --key KEY`, and `status` use scoped HTTP reads.
`read` accepts `--cursor` and `--limit` (1 to 200). The graph root environment
variable names the Runtime workdir; Outbox and Receipts are under `graph/`.

Read endpoints are `/api/v2/projects/{id}/fgs` and
`/api/v2/sessions/{id}/fgs`, with `/status` and `/nodes/{key}/history` below each. History returns newest
versions first, up to 100 per page, with a 2 MiB payload budget. Pass the last
version as `cursor` to read older versions.
Graph pages have at most 200 nodes and a 2 MiB node payload budget. A single node
is limited by the update byte limit. Edges can refer to another page. Status
includes up to 100 recent receipts, the last acceptance time, and unresolved
rejection count. These fields do not imply Runtime liveness.

Project Blackboard and the Session Blackboard route render Goal, Step, and Fact,
relationships, a page filter, selected-node details, history, and delivery state.
Legacy Project Blackboard keeps its existing view. The shared Runtime workspace
links FGS owners to their Blackboard.

Verification covers the FGS service, concurrent publication, crash-safe replay,
malformed updates, repair across Continuations, owner protocol persistence,
continuous daemon ingestion, scoped HTTP reads, CLI publication, instruction
preservation, Finish Readiness, frontend interactions, and a browser check with
local example data. An opt-in real Codex acceptance test also passed: the Runtime computed 2 + 2
in an isolated workdir, followed projected AGENTS.md instructions, and published
a completed Goal, completed Step, and result Fact without a Skill invocation.
This proves one basic workflow; it does not guarantee compliance on every model
or complex task. Run it with `CYBERPENDA_FGS_REAL_RUNTIME=1 go test ./internal/fgs
-run TestRealRuntimeFollowsProjectedFGSInstructions -count=1`.

Canonical unsafe and oversized update entries produce durable rejected receipts
without reading symlink targets or oversized bodies. Their identity can be
withdrawn through the normal protocol. Noncanonical filenames are transport
errors and must be corrected locally.

Legacy semantic change, Attempt checkpoint, and Evidence-retain HTTP endpoints
reject writes to FGS owners. Upgraded owners use FGS; historical records remain stored.
FGS Project navigation, dashboard counts, and reports use the FGS model. The
report endpoint `/fgs/report` renders one accepted graph revision as Markdown,
including success criteria, reported state, Step results, and correction links.
It does not infer vulnerability severity or Challenge Platform success.

Existing legacy migration, retention, and platform-specific reporting policy
remain separate design work. Explicit full-drain lifecycle and report generation
still process a complete mailbox or graph; only background receipt polling and
interactive read/history pages have bounded work. Longer multi-agent workloads
and each additional Runtime provider need their own acceptance coverage.
