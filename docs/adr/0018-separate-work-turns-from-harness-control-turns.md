# ADR 0018: Separate Work Runtime Turns from Harness Control Turns

## Status

Accepted

## Context

An assisted Blackboard workflow needs to notice when a Runtime finishes a work
Turn after using non-Blackboard tools. Provider transcripts are not a safe
orchestration contract: their raw payloads vary by provider, may contain
sensitive tool input and output, and cannot prove whether a Turn was initiated
by the operator or by the Harness.

The first assisted workflow only surfaced a pending conclusion. The next slice
dispatches a separate Harness-owned Turn to reconcile a closed semantic result
into Blackboard v2. That control Turn must not recursively create another
pending conclusion or be mistaken for operator work.

## Decision

- The Runtime Harness owns a closed `work | control` Runtime Turn kind and
  assigns it from request lineage. Provider payloads cannot choose or override
  the kind.
- Provider session adapters may expose a typed, bounded observation stream for
  Tool Use, terminal Tool Result, and Turn completion. The observation contract
  has correlation and status metadata only; it has no tool arguments, raw
  output, message text, or reasoning fields.
- In `assisted` mode, a completed `work` Turn with at least one terminal
  non-Blackboard Tool Result creates one durable pending Blackboard conclusion
  receipt. Duplicate completion delivery is idempotent.
- A pending receipt is projected as Harness state in the Task API and Timeline.
  It is not a Task Conversation message and does not itself write semantic
  Blackboard records.
- A pending receipt does not stop, complete, or finish a Task. Task Finish
  remains an explicit operator action.
- A durable pending receipt may dispatch one deterministic `control` Turn in
  the same persistent Runtime and Runtime Continuation. The Harness validates
  the closed result and supplies trusted identity, idempotency, and revision
  preconditions before applying it through Blackboard v2.
- The control directive and structured result remain outside Task Conversation
  messages and cannot invoke Task Finish or infer Facts/Evidence from raw output.

## Consequences

The daemon can enforce assisted workflow state without parsing unbounded
provider output or trusting model-authored metadata. Providers must implement
the bounded observation capability before assisted mode is offered. Future
automatic dispatch has a non-recursive control lineage, while operator control
of Task completion remains unchanged.
