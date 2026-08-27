# Runtime Owner Provider Sessions and Native Steering

## Problem Statement

A **Runtime Owner** can be a Project **Task** or a **Non-Project Session**.
Both owners use one persistent provider session and one shared Runtime Owner
Workspace. Operator follow-up must not create a different owner or make the UI
report `idle` while a provider **Runtime Turn** is still active.

Codex App Server supports `turn/steer` for same-turn input. A blanket
interrupt-then-replace implementation loses that behavior and can start an
expensive replacement Turn. The Conversation can also appear blank if the
Runtime output projection stores only completed assistant messages and tools.

## Solution

Use provider-native same-turn steering when the current active Turn is a
steerable **Work Runtime Turn** and the requested **Runtime Turn Selection** does
not require replacement. For Codex, send App Server `turn/steer` with the
current thread ID, `expectedTurnId`, and operator input. For Pi, use its native
same-turn RPC. If a provider does not implement same-turn steering, use its
native interrupt, wait for acknowledged settlement, and start one replacement
Work Turn on the same native session.

Every request first becomes durable **Accepted Steering**. The Runtime Harness
then dispatches requests first-in, first-out for that Runtime Owner. Runtime
activity follows the provider Turn, and the Conversation shows bounded
reasoning summaries plus started and completed tool work.

## Required Behavior

1. Steering keeps the Task or Session identity and its native provider session.
2. Direct same-turn steering requires one active provider Turn whose Harness
   lineage is `work`. It never targets a Harness Control Turn.
3. Codex `turn/steer` uses the current active Turn as an explicit precondition.
   A mismatched or completed Turn fails closed.
4. A same-turn steer request is a Harness control action but does not reclassify
   the active Work Turn. A new or replacement provider Turn has `work` lineage.
5. A model or Requested Reasoning Effort change that cannot apply in the active
   Turn, or an explicit force-replace request, uses interrupt-then-replace. Pi
   also uses replacement for a Model Provider change when the target belongs to
   its fixed projected Provider set. Other Model Provider changes follow the
   existing Config Projection and fresh Continuation rules: Task keeps its
   queue-then-stop-then-resume safety order, while Session stops and sends the
   operator message on the fresh Continuation. A live Session steer rejects
   that non-Pi cross-provider request so it cannot bypass the restart path.
6. If `turn/steer` returns JSON-RPC method-not-found, the Codex adapter disables
   same-turn steering for that live adapter and safely retries the accepted
   request through interrupt-then-replace. Codex InitializeResponse exposes
   server identity, not a server capability set, so version text is not a
   capability authority.
7. A request ID is idempotent from the public Runtime Owner API through provider
   dispatch. Durable replay is checked before live provider binding, active-Turn
   checks, and server-selected steering mode.
8. Replay identity uses only client-controlled fields: request ID, operator
   message, Model Provider, model, and Requested Reasoning Effort. A changed
   provider Turn ID or fallback mode cannot create a false conflict.
9. A 202 response is returned only after the operator Conversation projection
   and Accepted Steering record commit in one transaction.
10. Session steering attachments are staged before acceptance. Attachment
    Events, the Conversation Event, and Accepted Steering commit in one database
    transaction. Failed acceptance removes staged files.
11. The durable send-start fence remains the recovery authority. A pre-fence
    request may dispatch after restart. A post-fence request without a durable
    result becomes `action_required` and is never sent again automatically.
12. Per-owner serialization covers steering dispatch, stop, close, and recovery.
    Steering requests for different owners do not block each other.
13. Runtime Activity is `busy` while a control RPC is active or while the
    provider session has an active Turn ID. Elapsed time and historical Events
    are not liveness authority.
14. Codex Runtime output retains bounded `item/started` tool records and
    completed reasoning summaries. Deltas remain excluded. Conversation
    projects reasoning as collapsed `thinking`, not as an assistant answer.
15. Started command and MCP tool records project a tool call. Completion adds
    the matching result without duplicating the tool call.
16. Scope, Project Interface authority, Runner choice, credentials, Workdir,
    and Runtime Non-Interactive Defaults do not change during steering.

## Implementation Decisions

- The shared ProviderSession contract exposes `send_turn`, `interrupt_turn`,
  `interrupt_then_replace`, `in_turn_steer`, `permission_response`, persistent
  session identity, and atomic Turn state.
- Codex App Server is a non-PTY JSON-RPC transport. Its same-turn method is
  `turn/steer`; its fallback is `turn/interrupt` followed by `turn/start`.
- The provider manifest advertises the supported wire contract. Live
  method-not-found is the backward-compatible negotiation and downgrade path.
- Accepted Steering is the source of truth for dispatch and settlement.
  Conversation and Timeline entries are owner-local projections.
- Task and Session adapters share the Accepted Steering state machine. Their
  persistence and continuation transitions remain owner-specific.
- Runtime Turn kind comes from Harness request lineage. Provider output cannot
  choose or change it.
- Same-turn steering preserves the source Work Turn lineage. Replacement sends
  always create Work Turn lineage, including method-not-found fallback.
- Provider Turn liveness is read as one atomic snapshot when possible.
- Pi applies model, Model Provider, and Requested Reasoning Effort through
  pre-prompt RPCs. Therefore a selection-changing Pi steer always starts a
  replacement Work Turn; `pi/steer` is used only when the raw selection is
  unchanged.

## Testing Decisions

Use TDD. The main seam is the daemon Task and Session HTTP APIs with real owner
services, SQLite, and deterministic fake ProviderSessions.

Required tests cover:

- Codex `turn/steer` wire parameters and expected active Turn checks.
- Same-turn Work lineage preservation and replacement Work lineage creation.
- Method-not-found downgrade and one safe interrupt-then-replace fallback.
- Request replay before live provider checks, including daemon restart and the
  fallback window.
- Client-controlled conflict identity.
- Atomic Session attachment, Conversation, and Accepted Steering persistence,
  including staged-file rollback.
- Provider Turn busy/idle transitions after launch, direct steer, replacement,
  completion, interruption, and close.
- Bounded reasoning and started-tool Runtime output parsing and Transcript
  projection.
- A web TranscriptRow test that renders `thinking` as collapsed text.
- Full backend tests, web tests, web build, and `git diff --check`.

## Out of Scope

- Making maximum Requested Reasoning Effort faster.
- Rewriting completed reasoning, messages, or tool items.
- Backfilling historical Sessions that did not store reasoning summaries.
- Raw PTY input, shell emulation, terminal bytes, or process-kill steering.
- Cross-provider native session migration.
- Automatic Sandbox-to-Host Runner fallback.
- Changing Scope or Project Interface authority during steering.
