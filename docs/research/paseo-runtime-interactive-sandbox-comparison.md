# Paseo Runtime Interaction and CyberPenda Sandbox Research

Date: 2026-07-19

## Question

What does `getpaseo/paseo` do differently around runtime connections, and can
CyberPenda turn its currently non-interactive Runtime into a user-interactive
Task Conversation that remains inside the Sandbox Runner?

## Executive conclusion

Paseo's most useful idea is a two-layer interaction model. Its UI feels like a
native terminal-driven agent experience for Claude Code, Codex, Pi, and other
providers, but it achieves that through a long-lived provider session with a
typed protocol connection. A user follow-up is another prompt on that same
session; `interrupt`, permissions, and resume are provider operations. Paseo
also keeps ordinary shell terminals as a separate PTY resource, so the UI can
offer both an agent conversation surface and a real terminal surface without
making either one impersonate the other.

CyberPenda already has the surrounding domain model: one Task owns a Task
Conversation, Runtime Continuations, Harness Steering, a Sandbox Runner, and a
normalized transcript. The current runtime adapter is the missing transport
piece. It launches one foreground process through `docker start -a`, reads
stdout/stderr, and ends the continuation when that process exits. It does not
retain a writable provider connection for a later prompt.

The recommended direction is a **sandbox-local provider protocol bridge**:

1. Create one sandbox-owned runtime session for a Task/Runtime Profile.
2. Keep the provider's protocol stream alive with stdin/stdout (or a fixed
   Unix socket inside the sandbox), without allocating a PTY for the agent
   protocol.
3. Translate typed daemon commands (`send prompt`, `interrupt`, `permission
   response`, `stop`) to provider-native messages and translate provider events
   back into Task Events.
4. Preserve Scope Snapshot, Project Interface authority, Runtime Workdir,
   redaction, Continuation pins, and the rule that mid-turn input only takes
   effect at an explicit provider/runtime boundary.

This is a design direction, not an implementation approval. The semantics of
"interactive" (next provider turn versus input while a tool is running versus
raw terminal access) still require operator decisions.

## Paseo findings

The research used the `getpaseo/paseo` `main` tree at commit
`2185779d6c51137006f6f52256c60dce118efae4`.

### 1. The UI agent surface is interactive without being a PTY

The agent surface lets the operator keep talking to a live Claude Code, Codex,
Pi, or ACP-compatible session from the UI, with provider-native tool calls,
permissions, interruptions, and session persistence. Paseo also has a separate
PTY terminal feature, but CyberPenda explicitly does not need that feature for
this effort. The relevant lesson is therefore the agent session protocol and
its UI lifecycle, not terminal emulation.

Paseo's architecture documents an `AgentSession` abstraction with
`run/startTurn`, `interrupt`, `close`, persistence handles, and provider
adapters. The built-in providers include Codex App Server, Claude Agent SDK,
ACP, OpenCode, and Pi. Providers own authentication and session persistence;
the daemon owns lifecycle and normalized timeline events.

Codex is launched as a JSON-RPC app-server child. Its transport reads JSON
lines from stdout and writes requests/responses to the child's stdin. The
native operations include `thread/start`, `thread/resume`, `turn/start`, and
`turn/interrupt`. This is a persistent protocol channel, not a shell prompt.

ACP follows the same shape: the daemon spawns the provider with ordinary
stdio, wraps the streams in an ACP `ClientSideConnection`, calls
`session/new`/`session/load`, and sends subsequent session prompts over that
connection.

Paseo's terminal subsystem is independent. It uses `node-pty` for shell
sessions, exposes binary WebSocket frames for output/input/resize/snapshot,
and keeps terminal streams low-latency through a worker and output coalescer.
The agent conversation is not implemented by typing into this PTY.

Sources:

- `getpaseo/paseo` `docs/architecture.md` (AgentSession/provider and WebSocket
  boundaries)
- `getpaseo/paseo` `docs/agent-lifecycle.md` (initializing/idle/running/error/
  closed state machine and persistence)
- `getpaseo/paseo` `packages/server/src/server/agent/agent-sdk-types.ts`
  (`AgentSession` and `AgentClient` contracts)
- `getpaseo/paseo`
  `packages/server/src/server/agent/providers/codex/app-server-transport.ts`
  (stdin/stdout JSON-RPC transport)
- `getpaseo/paseo`
  `packages/server/src/server/agent/providers/codex-app-server-agent.ts`
  (`thread/*`, `turn/*`, interrupt and session lifecycle)
- `getpaseo/paseo` `packages/server/src/server/agent/providers/acp-agent.ts`
  (ACP stdio connection and session load/new)
- `getpaseo/paseo` `packages/server/src/terminal/terminal.ts` and
  `terminal-session-controller.ts` (examined to confirm PTY is a separate,
  out-of-scope resource)

### 2. Follow-up prompts can replace or coexist with a running turn

Paseo's normal `send_agent_message` path sends a new prompt through the
provider session. The manager can interrupt/replace an in-flight foreground
turn, while explicit out-of-band handlers can handle a command such as
`/goal pause` without cancelling the active turn. This distinction is exposed
in real-provider tests for “send during tool call”, “send while running”, and
Codex goal updates.

The useful lesson is to make turn policy explicit in the session contract:
queue for the next turn, replace after an acknowledged interrupt, or handle as
an out-of-band control. Do not infer this policy from whether a process happens
to have a writable stdin.

### 3. The daemon stream is typed and authoritative

Paseo sends typed WebSocket messages, streams agent events live, and treats
authoritative timeline fetch/catch-up as the correctness path. Terminal output
uses a binary stream because it is high-volume; agent messages remain typed
timeline items. This separation avoids making a conversation UI parse raw
terminal escape sequences or provider logs.

### 4. Sandbox and remote-access lessons

Paseo is local-first but supports direct, relay, and self-hosted WebSocket
connections. Its security docs treat the relay as untrusted and require
end-to-end encryption before accepting commands; direct network bindings use
password auth and host allowlists. Docker guidance stresses non-root daemon
execution, tightly scoped mounts, and treating the provider home as sensitive.

CyberPenda does not need Paseo's relay for this feature, but the boundary is
relevant: a new interactive command channel must be authenticated and
Task/Project scoped before it can write to a live runtime.

## CyberPenda findings

### Existing capabilities

- `internal/runtime/runtime.go:19-29` defines a provider adapter with one
  `Run(ctx, goal, emit)` call per continuation.
- `internal/runtime/runtime.go:46-155` owns lifecycle, status, event recording,
  cancellation, and continuation metadata, but has no input/send operation.
- `internal/runtime/docker_sandbox.go:107-225` creates a container, starts it
  with `docker start -a`, scans stdout/stderr, then removes it. The adapter
  never owns a writable stdin or a persistent container session.
- `internal/daemon/server.go:489-495` already exposes task events, stop, resume,
  queued steering, and active steering routes.
- `internal/daemon/task_handlers.go:1125-1510` implements resume/steering as
  continuation-boundary operations. Active steering interrupts the current
  run, then launches a new continuation.
- `internal/transcript/transcript.go:49-180` already projects goals,
  continuation separators, steering directives, conversation messages, and
  normalized runtime output into one transcript.
- `CONTEXT.md:817-842` explicitly defines Task Conversation, Harness Steering,
  Runtime Continuation, and the invariant that steering affects a continuation
  boundary rather than an already-running internal reasoning step.

### Current gap

The current design is “interactive” at the Task/Continuation control-plane
level, but non-interactive at the provider transport level. A browser can
submit steering, but the daemon must stop the foreground adapter and create a
new continuation. The native provider session is not held open across those
turns, and a live provider cannot receive a typed follow-up while its tool turn
is still running.

The existing PTY-like concept is not a shortcut: the Docker adapter uses
separate stdout/stderr pipes and the current security tests intentionally
inspect `docker create` args. Adding `-t` or exposing a raw `docker attach`
would introduce terminal semantics, escape sequences, resize behavior, and a
larger command-injection surface without solving provider session persistence.

## Recommended target architecture

### Transport boundary

Add a provider-session adapter contract alongside the existing continuation
adapter. The contract should have explicit operations similar to:

- `StartSession` / `ResumeSession`
- `SendTurn(prompt)`
- `QueueTurn(prompt)`
- `InterruptTurn`
- `RespondToPermission`
- `StopSession`
- provider event subscription

For sandbox execution, the provider process and bridge stay inside the
container. The daemon talks to the bridge through a fixed, authenticated,
project-scoped channel. For the first tracer bullet, a line-framed JSON-RPC
stream carried over container stdin/stdout is simpler than a network listener;
the daemon must keep the container and its pipes alive until Task termination.

### Lifecycle boundary

Split “container/session lifetime” from “provider turn/Runtime Continuation”:

- The container/session owns the Sandbox Runner, Runtime Workdir, provider
  home, and native session identity.
- A Runtime Continuation records one launch/resume/turn boundary and its pinned
  Blackboard snapshot/configuration.
- A follow-up prompt creates a new continuation event boundary while reusing
  the same authenticated sandbox session when the provider supports it.
- An interrupt/replace operation must be acknowledged by the provider before
  marking the old turn stopped and starting the replacement.

This preserves the current immutable task/runtime configuration rules while
making “same Task, same sandbox, next provider turn” a first-class path.

### Event and UI boundary

Keep `Task Events` and the existing transcript as the source of truth. Add
typed user-message and provider-turn events at the exact positions they occur.
Do not store raw JSON-RPC or terminal byte dumps as conversation content.
Expose live stream updates for responsiveness, with task-event fetch/catch-up
for reconnect correctness, following the existing transcript contract. No raw
PTY or terminal byte stream is required for this target.

### Security boundary

- Never expose a raw Docker socket, arbitrary `docker exec`, or unrestricted
  container attach operation to the web client.
- All runtime input is validated by the daemon, bound to one Task and active
  continuation/session, and redacted before event persistence.
- Keep the agent protocol non-PTY (`-i`/pipes, not `-t`) unless a provider
  explicitly requires a terminal; reserve PTY for the existing Terminal
  resource.
- Keep Scope Snapshot, Preflight, Project Interface grants, credential
  projection, and Host Runner activation unchanged.
- On daemon restart, recover only from durable native session metadata and
  Continuation launch pins; do not assume an orphaned container is safe to
  reattach without identity and ownership checks.
- A Sandbox failure must not silently fall back to Host Runner.

## Confirmed operator decisions

- The Sandbox/provider session is Task-lived. Runtime Continuations are
  durable provider-turn/control boundaries inside that session.
- The first release supports Claude Code, Codex, and Pi.
- Direct provider `in_turn_steer` is preferred where available. When it is not
  available, use provider-native interrupt, wait for acknowledged settlement,
  then send the new prompt on the same Task session.
- The Task Conversation user message is canonical. Native steer/interrupt
  lifecycle is represented by correlated typed Task Events and projected once
  into the transcript; it does not create a duplicate user message.
- PTY and raw terminal interaction are out of scope.

Remaining work is technical specification rather than an unanswered product
choice: bridge transport, crash-window idempotency, permission/audit events,
restart recovery, and TDD implementation slices.

## Explicit non-goals for this research

- No provider CLI flags are changed.
- No PTY or terminal emulation is added to the current Docker adapter.
- No new permission bypass is introduced.
- No runtime input is injected into an already-running model reasoning step
  without an explicit provider boundary.
- No Paseo code is copied or vendored; this document records interface ideas and
  source references only.

## Sandbox bridge lifecycle design

The first implementation should introduce a Task-lived `SandboxSessionBridge`
beside the existing one-shot `Adapter`, rather than making `Adapter.Run` own a
writable stdin. The bridge process is the container's PID 1 and owns the
provider-native child process and its protocol stream. The daemon owns one
non-PTY, line-framed JSON-RPC connection to the bridge (`docker create -i` and
`docker start -a -i`, or an equivalent attach implementation). Bridge stdout
must contain only framed protocol envelopes; diagnostics belong on stderr and
must pass the same redaction boundary as runtime output.

The bridge/session lifetime and Runtime Continuation lifetime are deliberately
different:

| Resource | Lifetime | Durable identity |
| --- | --- | --- |
| Sandbox session bridge | One Task/runtime-provider session, until close or failure | Task-scoped session id, container id, provider session id |
| Runtime Continuation | One provider turn, including an acknowledged native replacement | Continuation id, runtime config version, Blackboard pin |
| Provider turn | One start-to-complete or interrupt-to-ack interval | Provider turn/request id |

`steer` is a two-phase operation. Persist the request with a client/request id,
send it to the bridge, and wait for provider-native interrupt/replacement
acknowledgement. Only after the acknowledgement should the old Continuation be
terminal and the replacement Continuation be created and bound to the same
session. A crash between these phases must be replay-safe: request ids and
provider turn ids are idempotency keys, and an already-acknowledged request
must not create a second replacement.

For v1, daemon restart is intentionally not a live stdio reconnect contract.
Startup keeps the existing fail-closed path: mark active Task/Continuations
interrupted, stop and remove stale sandbox containers, and resume only through
a new bridge using durable provider-session metadata/provider home and a fresh
Blackboard Continuation pin. A Unix socket is a later option only if hot
reconnect is required; it then needs a Task/Continuation ownership handshake,
nonce/peer authentication, replay/catch-up, and symlink-safe socket path
creation. Never expose Docker attach, `docker exec`, or a bridge socket to the
browser.

This design follows the current ownership boundaries: `Adapter` is one-shot
(`internal/runtime/runtime.go:19-29`), `Harness` binds lifecycle and cancellation
to a Continuation (`internal/runtime/runtime.go:46-155`), Docker currently
starts a foreground process and removes the container on return
(`internal/runtime/docker_sandbox.go:107-225`), and Continuation launch pins
runtime configuration/steering atomically (`internal/task/task.go:624-714`).
Startup cleanup and interruption already exist at
`internal/task/task.go:1186-1244,1333-1377` and
`internal/daemon/server.go:232-288`; adding a live bridge must preserve their
fail-closed behavior or explicitly revise the restart ownership contract.

### TDD seams

1. Fake bridge state machine: `idle -> running -> interrupting -> idle/closed`;
   assert no replacement Continuation before provider acknowledgement.
2. Harness session/turn registry: race-test concurrent steer, stop, close, and
   duplicate request ids.
3. Fake Docker CLI: assert `-i` and `start -a -i`, no `-t`, writable protocol
   input, and stop/remove on cancellation (extend the existing runtime tests).
4. Crash-window table tests: request persisted before send, sent before ack,
   and ack before commit; each request id applies at most once.
5. Restart test: stale container cleanup, interrupted old Continuation, and
   fresh-pin resume (extend `internal/daemon/server_test.go:496`).
6. Security tests: confined bridge paths and mounts, no arbitrary attach/exec,
   and typed event redaction/binding (extend runner confinement tests and
   `internal/runtime/runtime_test.go:77`).
