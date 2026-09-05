# Use FGS as the Blackboard working model

## Status

Proposed — September 6, 2026.

The user has confirmed the model replacement, product purpose, and seven FGS
Protocol Principles and AGENTS.md / CLAUDE.md instruction delivery in `CONTEXT.md`.
Detailed protocol choices
below remain proposals, not implemented behavior or an approved data migration.

## Context

Working Graph was intended to bring the Fact-Goal-Step protocol from
`ctf-orchestrator` into CyberPenda and show that graph in Blackboard. The current
implementation provides files and an Intent mailbox, but compiles updates into
Exploration Objectives, Attempts, and Project Facts. It also depends on Runtime
discipline to keep working files current.

A real Session initialized the graph, then maintained progress in native Tasks
and a separate queue. Its state file still reported a network failure after a
Fact recorded recovery and agents resumed work. This shows a missing reporting
loop. It does not show that the Harness must become an agent scheduler.

## Proposed decision

Use Goal, Step, and Fact directly in Blackboard. Replace Exploration Objective,
Attempt, and Project Fact as domain records. Do not add an FGS-to-legacy compiler.
The user also confirmed removal of Entity, Finding, Solution, and Evidence
Artifact as target graph node types. Goal, Step, and Fact are the only target
node types. Legacy data and evidence-file retention are separate migration work;
this decision does not authorize their deletion.

The Runtime follows Harness-projected FGS Runtime Instructions in AGENTS.md or
CLAUDE.md and decides what work to do. No FGS Skill invocation is required. It publishes
structured FGS updates through its continuation-scoped Outbox. The Harness
validates transport and authority. The Blackboard service validates and stores
FGS updates. The UI displays that accepted state and its receipt time.

Accepted Blackboard state is the durable shared graph. Runtime files are local
working state. An unsubmitted local edit is not a Blackboard update. The Runtime
can continue independent work while receipt delivery is pending. The Harness
does not infer semantic results from Transcript or force every tool call through
a Step scheduler.

Use current semantic state, compact Semantic History, and durable delivery
receipts. Outbox delivery is not the canonical graph storage format.

See [the protocol draft](../specs/fgs-runtime-outbox-protocol.md) for the proposed
schemas, authority, recovery, Runtime instructions, UI, and acceptance slices.

## Relationship to existing decisions

- ADR 0032: replace compilation into legacy semantic operations and boundary-only
  ingestion. Keep the three Blackboard Modes, owner authority,
  continuation-scoped mailboxes, and durable Receipts. Replace mandatory Working
  Graph Mode Skill invocation with FGS Runtime Instructions; this decision does
  not change instruction delivery for the other Modes.
- ADR 0005: reopen the prohibition on Goal nodes. An FGS Goal is an explicit
  intended outcome, not an automatic Task Goal or Task lifecycle projection.
- ADR 0006: keep current versioned semantic state and control receipts. Do not
  introduce a permanent replay ledger or historical whole-graph snapshots.
- ADR 0009: retain recovery from accepted graph state, but use FGS and pending
  delivery state instead of legacy Attempt checkpoints.
- ADR 0020: retain Project and Session isolation and shared owner-neutral
  behavior; replace its legacy working record types with FGS.
- Blackboard v2 specifications: the record schemas, relationship endpoints,
  snapshots, reporting, and migration contracts need a coordinated revision.
  The old specifications are not silently superseded by this proposed ADR.

## Consequences

The user confirmed description edits with history, Goal reopening with a reason,
and a new Step for retrying completed work. The rollout starts with newly created
Projects and Sessions. Existing data remains readable and existing runs retain
their legacy protocol. Legacy migration needs its own preview, backup, and
verification plan; no active owner is silently converted.

The Runtime and operator see the same accepted FGS model. A Runtime needs no
CTF-specific Skill to report normal FGS work. CTF selection, budgets, platform
calls, hints, and resource limits remain in the specialized Skill and services.

The graph can be stale when the Runtime does not report. The UI must expose this
limit. A receipt proves persistence, not correctness of an observation, an agent's
liveness, external platform acceptance of a result, or Task completion.

Existing data must remain intact until an explicit migration plan covers keys,
relationships, history, active owners, evidence, and rollback. Shipping a narrow
FGS prototype does not authorize removing the old data or public interfaces.
