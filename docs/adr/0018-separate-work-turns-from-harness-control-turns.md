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
- In `assisted` mode, a completed `work` Turn persists ordered semantic-debt
  watermarks. Every terminal non-Blackboard Tool Result advances work; a later
  successful `blackboard_change`, `blackboard_checkpoint_attempt`, or
  `blackboard_finish` covers work observed before it. Reads, history, Evidence
  retention, and failed mutations do not cover work. The checkpoint is pending
  only when work remains beyond semantic persistence. Duplicate Tool Results
  and completion delivery are idempotent.
- A pending receipt is projected as Harness state in the Task API and Timeline.
  It is not a Task Conversation message and does not itself write semantic
  Blackboard records.
- A pending receipt does not stop, complete, or finish a Task. Task Finish
  remains an explicit operator action.
- A durable pending receipt dispatches one initial Conclude `control` Turn and
  may dispatch at most one automatic follow-up: either a schema repair or a
  version-aware regeneration after synchronizing Current Truth. Both stay on
  the same receipt, Runtime Continuation, source provider/model/reasoning
  selection, and apply-idempotency lineage. An explicit operator retry does not
  reopen this automatic budget.
- The Harness validates each closed result and supplies trusted identity,
  idempotency, and revision preconditions before applying it through Blackboard
  v2. Control Turns remain non-recursive and never create assisted conclusion
  debt of their own.
- The durable receipt is also the daemon-restart recovery record. It retains
  source correlation, semantic-debt watermarks, deterministic request and apply
  lineage, canonical validated bytes and hash, revisions, bounded counters,
  stable errors, and timestamps, but never raw tool output or model reasoning.
- Immediately before every provider `SendTurn`, the Harness durably records a
  one-way send-start fence. A pre-fence request may be replayed only after a
  non-spawning recovery capability proves ownership of the exact existing
  Runtime and session. A post-fence request with no persisted result is
  acceptance-ambiguous and becomes operator-actionable instead of being sent
  again. Providers whose bridge transport cannot be reattached after daemon
  restart fail closed through the same path; native session metadata alone is
  not ownership proof and cannot authorize a replacement Runtime.
- Persisted canonical results resume Blackboard application without another
  model Turn. Replaying an already committed apply uses the same Service
  idempotency key and recovers the original revision before completing the
  receipt. Restart recovery preserves automatic-turn limits and an existing
  retry cooldown.
- The control directive and structured result remain outside Task Conversation
  messages and cannot invoke Task Finish or infer Facts/Evidence from raw output.

## Consequences

The daemon can enforce assisted workflow state without parsing unbounded
provider output or trusting model-authored metadata. Providers must implement
the bounded observation capability before assisted mode is offered. Automatic
dispatch is bounded and non-recursive; exhausting recovery requires operator
action while the Task remains live and operator control of Task completion
remains unchanged. Daemon restart cannot turn native session metadata into
Runtime ownership or silently launch a second Runtime; deployments that need
automatic pre-send replay must provide a reconnectable, non-spawning ownership
recovery capability.
