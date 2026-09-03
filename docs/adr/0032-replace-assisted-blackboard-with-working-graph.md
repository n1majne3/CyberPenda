# Replace Assisted Blackboard with Working Graph settlement

## Status

Accepted — September 1, 2026

## Context

Assisted Blackboard mode inferred semantic debt from provider Turns and then
started Harness-owned conclusion Turns. This depended on provider-specific
observation contracts, trusted MCP injection, and a second model protocol. It
was difficult to recover and it mixed Runtime work with Harness reasoning.

File Graph State has worked better in hosted CTF runs. Codex, Claude Code, Pi,
and other runtimes can all use files and a CLI without provider-specific MCP
support.

## Decision

Blackboard Mode has exactly three values: `interactive`, `working_graph`, and
`disabled`. A Task defaults to `working_graph`; a Non-Project Session defaults
to `disabled`. Historical `assisted` rows migrate to `working_graph`; the old
public JSON key and value are rejected.

Every launch projects exactly one system Mode Skill, separate from ordinary
Skills. Interactive mode receives a full CLI grant. Working Graph mode receives
a read-only CLI grant plus `PENTEST_WORKING_GRAPH_ROOT`,
`PENTEST_WORKING_GRAPH_OUTBOX`, and `PENTEST_WORKING_GRAPH_RECEIPTS`. Disabled
mode receives no Blackboard authority. Authentication uses process environment
variables and a grant that rotates for each Runtime Continuation.

CyberPenda does not inject a built-in Blackboard MCP server. User-configured
external MCP servers remain Runtime configuration but do not gain Blackboard
authority.

The Working Graph layout is `state.md`, `graph/facts`, `graph/data`,
`graph/steps.yaml`, `graph/goals.yaml`, `graph/outbox/<continuation>`, and
`graph/receipts/<continuation>`. `pentestctl working-graph emit` writes a
bounded, atomic, monotonically numbered Intent to the local Outbox. Runtime
payloads do not contain Blackboard versions or idempotency keys.

At lifecycle and Continuation boundaries, the Harness scans only canonical
regular Intent files for the current Continuation. It claims each Intent in a
durable owner-scoped Store, compiles semantic operations, resolves current
versions, applies through the owner-neutral Blackboard service, and writes a
Receipt. Settlement is ordered. `action_required` blocks later Intents.

Reason Task and provider-driven Assisted Conclusion are removed. Historical
tables remain inert so migration does not destroy user data.

## Consequences

The Blackboard write path is provider-neutral and testable without model
observations. Runtime files can recover after process or response loss because
claims and Blackboard idempotency are durable. The Harness must settle the
current Continuation before Finish, Stop, accepted Steering, or replacement.
The UI and documentation must show Mode Skills and Working Graph status, not
Assisted Conclusion state or Reason Task actions.
