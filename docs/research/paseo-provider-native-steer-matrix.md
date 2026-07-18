# Provider-Native Steer Capability Matrix

Date: 2026-07-19

This matrix records the source-level evidence used by Wayfinder ticket 132.
The reference Paseo tree is `getpaseo/paseo` at commit
`2185779d6c51137006f6f52256c60dce118efae4`.

## Shared Paseo orchestration

Paseo's `AgentSession` contract requires `startTurn`, permission response,
persistence description, `interrupt`, and `close`, while `AgentClient` owns
create/resume session operations. The normal send path uses
`replaceRunning: true`: an active run is interrupted through the session API,
the manager waits for settlement, then starts the next turn on the same loaded
session. This is the precise meaning of `interrupt_then_replace` for this
project; it is distinct from a provider's direct `in_turn_steer(message)`
primitive.

Sources:

- `packages/server/src/server/agent/agent-sdk-types.ts:613-684`
- `packages/server/src/server/agent/agent-prompt.ts:177-207`
- `packages/server/src/server/agent/agent-manager.ts:2006-2029,2179-2229`

## Matrix

| Provider | Next turn | Native steer | Persistence/resume | First-release assessment |
| --- | --- | --- | --- | --- |
| Codex App Server | JSON-RPC `turn/start` on the current thread | JSON-RPC `turn/interrupt` with `threadId` and active `turnId`; requires `turn/started` | `thread/start` and `thread/resume`; durable thread id; JSONL stdin/stdout app-server transport | Strongest first tracer bullet |
| Claude Code | SDK async input (`push(SDKUserMessage)`) on the long-lived Query | SDK `query.interrupt()`; permission denial can also request interrupt | SDK session id/native handle; provider session persistence and construction-time resume | Supported in first release, adapter-specific transport |
| Pi | RPC `prompt` on the long-lived RPC child | RPC `abort` command | Session id plus durable session file; native resume requires the handle/session file | Supported in first release |
| ACP-compatible | ACP `connection.prompt({sessionId,...})` | ACP `connection.cancel({sessionId})` | `session/new`; `session/load` or unstable resume only when advertised by provider capabilities | Supported only after capability negotiation; no unconditional resume |

## Source details

### Codex App Server

- `packages/server/src/server/agent/providers/codex-app-server-agent.ts:3795-3841`
  sends `turn/start` on the current thread.
- `:4214-4228` sends `turn/interrupt` and rejects interruption before the
  provider identifies the active turn.
- `:3539-3565` uses `thread/resume`; `:4458-4504` starts a new thread.
- `packages/server/src/server/agent/providers/codex/app-server-transport.ts:157-233`
  owns the JSONL child stdin/stdout transport; this is not PTY.

### Claude Code

- `packages/server/src/server/agent/providers/claude/agent.ts:2022-2099`
  drives a long-lived SDK Query and async user input.
- `:2109-2121,3564-3587` calls the SDK query interrupt path.
- `:2243-2317` handles typed `canUseTool` permission requests and resolutions.
- `:1938-1950,2320-2333` carries session id/native handle persistence.

### Pi

- `packages/server/src/server/agent/providers/pi/agent.ts:1243-1302`
  sends an RPC prompt.
- `:1391-1405` aborts an active turn; the wire command is defined in
  `packages/server/src/server/agent/providers/pi/rpc-types.ts:129-140`.
- `packages/server/src/server/agent/providers/pi/runtime.ts:43-74,114-145`
  keeps a long-lived RPC child.
- `packages/server/src/server/agent/providers/pi/agent.ts:1377-1388,2334-2365`
  persists and resumes through a native session file.

### ACP

- `packages/server/src/server/agent/providers/acp-agent.ts:1462-1507`
  sends `connection.prompt`.
- `:2005-2018` sends `connection.cancel`.
- `:1954-1987` maps permission decisions and deny-with-interrupt to typed ACP
  operations.
- `:1378-1443` chooses `session/load` or an unstable resume extension only when
  the provider advertises it.

## Capability contract recommendation

Do not derive live steer from the existing `NativeResume.Supported` flag. Add
independent, runtime-negotiated capabilities:

- `persistent_session`
- `send_turn`
- `interrupt_turn`
- `interrupt_then_replace`
- `in_turn_steer`
- `permission_response`
- `resume_session`

For CyberPenda's first release, the selected provider set is Claude Code,
Codex, and Pi. Current Paseo source shows these three (and generic ACP) expose
`interrupt/cancel/abort` plus a next-turn operation, not a direct
`in_turn_steer(message)` call. They therefore use the selected
`interrupt_then_replace` fallback: provider-native interrupt, acknowledged
settlement, then new prompt on the same Task-owned session. A future provider
with `in_turn_steer` gets the preferred direct path. If a provider cannot even
interrupt or cannot keep a session, the operation must fail explicitly rather
than silently becoming a process kill or mechanical handoff.

## CyberPenda gap confirmed

CyberPenda currently has only `Adapter.Run(ctx, goal, emit)` and one-shot
stdout/stderr adapters. `NativeResume` manifests describe only restart argv and
session discovery. The current Interrupt & Steer path stops the container and
starts a new native-resumed continuation. It has no bidirectional provider
control channel or provider acknowledgement state.

Sources:

- `internal/runtime/runtime.go:19-30,46-155,289-342`
- `internal/runtime/docker_sandbox.go:107-225`
- `internal/runtimeplugin/plugin.go:19-34,76-80,129-176`
- `internal/runtimeplugin/builtin.go:63-67,115-130,167-178`
- `internal/daemon/task_handlers.go:1504-1545`
- `internal/task/task.go:98-118,624-714,1094-1130`

## TDD seams

1. A fake provider session receives `interrupt_then_replace` without process
   cancellation and emits an interrupt acknowledgement before the replacement
   turn; a separate fake covers direct `in_turn_steer`.
2. Daemon integration proves the Task and Sandbox/provider session identity do
   not change during steer; only the Runtime Continuation/turn boundary does.
3. Codex/Claude/Pi adapter contracts map provider acknowledgement, reject,
   timeout, and permission outcomes into typed Task Events.
4. Unsupported providers return a typed capability error; they do not claim
   `steering_applied`.
5. Concurrent steer/stop/close operations are serialized and idempotent.
