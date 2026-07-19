# Interactive Sandbox Provider Sessions and Native Steer

## Problem Statement

CyberPenda executes each Runtime Continuation as a one-shot foreground process.
Task controls can stop, resume, or steer only by ending the current sandbox
container and starting another continuation from a persisted native session
identifier. This is not the interactive Agent UI experience operators expect
from Paseo: an active Claude Code, Codex, or Pi session should remain inside
one sandbox-owned Task session, receive a user follow-up through the provider's
native control protocol, and preserve a truthful Task Conversation and
transcript.

## Solution

Add a Task-owned Sandbox/provider session bridge. The bridge keeps the provider
process and its bidirectional, non-PTY protocol channel alive for the Task.
Runtime Continuations become durable turn/control boundaries inside that
session. A user message is recorded once as a Task Conversation event. Direct
provider in-turn steer is preferred when capability negotiation exposes it.
Otherwise the Runtime Harness performs provider-native interrupt/cancel/abort,
waits for acknowledged settlement, and sends the new prompt on the same
session. The daemon records typed lifecycle and provider acknowledgement events
and projects them once into the existing transcript.

The first provider release is Claude Code, Codex, and Pi. Providers without a
persistent session or interrupt capability fail with a typed unsupported
capability error. PTY, shell emulation, raw terminal input, process-kill steer,
and mechanical handoff are outside this feature.

## User Stories

1. As an operator, I want to send a follow-up message while a Task Runtime is
   active, so that I can redirect work without creating a new Task.
2. As an operator, I want my Task to keep one Sandbox/provider session, so that
   the provider retains its native context and workdir.
3. As an operator, I want provider-native in-turn steer used when available,
   so that the provider can apply its own interruption semantics.
4. As an operator, I want providers without direct steer to interrupt natively
   and then receive my prompt on the same session, so that fallback preserves
   provider context.
5. As an operator, I want failed or unsupported steer to be reported clearly,
   so that the UI never claims a redirect was applied when it was not.
6. As an operator, I want one Task Conversation user message, so that the
   transcript does not duplicate my prompt as both conversation and steering.
7. As an operator, I want the transcript to show steer requested, provider
   acknowledgement, old-turn settlement, and replacement turn boundaries, so
   that I can audit what happened.
8. As an operator, I want duplicate steer requests to be idempotent, so that a
   retry after a lost response cannot create two replacement turns.
9. As an operator, I want concurrent steer, stop, and close operations to be
   serialized, so that one Task cannot own conflicting active controls.
10. As an operator, I want provider permission requests and responses to remain
    on the same typed session channel, so that approval state is not lost.
11. As an operator, I want a daemon restart to fail closed on orphaned live
    bridges, so that a stale process cannot mutate a Task after ownership is
    lost.
12. As an operator, I want restart recovery to use durable provider metadata
    and a fresh Continuation pin, so that recovery is deterministic.
13. As an operator, I want Claude Code, Codex, and Pi supported in the first
    release, so that the common local runtimes share one interaction model.
14. As an operator, I want unsupported providers to expose capability errors,
    so that the UI can disable controls honestly.
15. As an operator, I want Scope Snapshot and Project Interface authority to
    remain unchanged during steer, so that interactivity cannot expand access.
16. As an operator, I want Host Runner activation and Sandbox Runner reporting
    to remain explicit, so that steer cannot cause an invisible runner switch.
17. As an operator, I want provider protocol diagnostics redacted and separated
    from user conversation, so that secrets and raw wire data do not leak.
18. As an operator, I want the existing mechanical handoff path preserved as a
    separately named recovery action, so that native interaction does not blur
    recovery modes.

## Implementation Decisions

- A Task owns one Sandbox/provider session for its selected Runtime Profile and
  provider. Runtime Continuations own provider turn/control boundaries inside
  that session.
- The provider-session contract separates `persistent_session`, `send_turn`,
  `interrupt_turn`, `interrupt_then_replace`, `in_turn_steer`,
  `permission_response`, and `resume_session` capabilities.
- The daemon-to-bridge v1 transport is a non-PTY, line-framed JSON-RPC stream
  over daemon-owned container stdin/stdout. Bridge stdout contains only framed
  protocol; diagnostics use redacted stderr/runtime events.
- A bridge process owns the provider child process. The daemon owns bridge
  lifecycle, Task binding, request ids, provider turn ids, and event persistence.
- Direct `in_turn_steer` is preferred and is used by Pi RPC. The fallback is provider-native
  interrupt/cancel/abort, acknowledged settlement, then same-session
  replacement prompt. Process cancellation, PTY input, and raw stdin injection
  are not steer implementations.
- The first provider adapters are Claude Code, Codex, and Pi. Production
  capability advertisement is gated by a verified transport: Codex App Server
  and Pi RPC are stable non-PTY protocols; Claude native interrupt requires an
  explicit Claude Agent SDK `Query` bridge and remains typed unsupported until
  that bridge is installed. ACP is deferred until provider handshake
  capability negotiation is implemented.
- The canonical user message is a Task Conversation event. Control/provider
  lifecycle events are correlated typed Task Events and are projected once into
  the transcript without duplicating the user message.
- A steer request is idempotent by request id. Provider turn ids and session ids
  bind acknowledgements and replacement Continuations.
- The old Continuation is not marked replaced/applied until provider
  acknowledgement and settlement are durable. A failed acknowledgement cannot
  produce `steering_applied`.
- Per-Task control serialization covers steer, stop, close, and recovery.
- Daemon restart does not blindly reattach an old stdio bridge in v1. Startup
  cleans stale ownership fail-closed and resumes through durable provider
  metadata plus a fresh Continuation pin.
- Scope, Preflight, Project Interface grants, credential projection, Runtime
  Workdir, Host Runner activation, and Runtime Non-Interactive Defaults remain
  unchanged.

## Testing Decisions

- Use TDD at the daemon Task control API as the highest seam, with a fake
  provider session and fake bridge for deterministic red-green cycles.
- Test the public behavior: same Task/session identity, typed event ordering,
  acknowledgement gating, idempotency, unsupported capability errors,
  concurrent control conflicts, and truthful failure states.
- Add provider contract tests for Codex App Server JSON-RPC, Claude Code SDK
  input/interrupt, and Pi RPC prompt/steer/abort. Tests must not require live model
  credentials for the core contract.
- Add sandbox lifecycle tests proving bidirectional non-PTY protocol input,
  `-i` without `-t`, cancellation cleanup, and no duplicate containers during
  native steer.
- Add transcript projection tests for one canonical user message and the
  correlated steer/provider lifecycle entries.
- Add restart/crash-window tests for request-before-send, sent-before-ack, and
  ack-before-commit, with at-most-once replacement creation.
- Port Paseo's tool-call replacement behavior as an integration acceptance
  scenario: run a long tool, send a follow-up, observe no false idle/error gap,
  and verify same session identity.

## Out of Scope

- PTY, shell emulation, terminal resize, raw terminal bytes, or arbitrary Docker
  attach/exec exposed to the web client.
- ACP implementation in the first provider release.
- Cross-provider session migration.
- Automatic fallback from Sandbox Runner to Host Runner.
- Dynamic injection into an already-running model reasoning step without a
  provider-defined boundary.
- Replacing provider-native persistence stores.
- Distributed worker orchestration or remote bridge ownership.

## Further Notes

The existing native resume and Interrupt & Steer paths remain useful as an
explicit recovery path while the Task-owned bridge is introduced. They must not
be labeled as live native steer after the new capability is available.
