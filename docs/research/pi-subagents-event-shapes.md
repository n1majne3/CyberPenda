# Pi Subagents Event Shapes (issue #246)

Date: 2026-08-29

Wire facts for the Runtime Owner Timeline subagent projection: what the Pi
subagents extensions emit when a Pi Work Runtime Turn delegates to subagents,
in both Pi persistent RPC mode and Pi one-shot session JSONL mode.

## Versions examined

| Component | Version | Source |
| --- | --- | --- |
| `@tintinweb/pi-subagents` | 0.19.0 (npm `latest`, tag `v0.19.0`, commit `4f572eaa04`) | <https://registry.npmjs.org/@tintinweb/pi-subagents/-/pi-subagents-0.19.0.tgz>; repo <https://github.com/tintinweb/pi-subagents> |
| `pi-subagents` (unscoped, nicobailon) | 0.59.0 (npm `latest`) | <https://registry.npmjs.org/pi-subagents/-/pi-subagents-0.59.0.tgz>; repo <https://github.com/nicobailon/pi-subagents> |
| `@earendil-works/pi-coding-agent` (Pi CLI) | 0.84.4 (npm `latest`) | <https://registry.npmjs.org/@earendil-works/pi-coding-agent/-/pi-coding-agent-0.84.4.tgz>; repo <https://github.com/earendil-works/pi> (`packages/coding-agent`) |
| `@earendil-works/pi-agent-core` | 0.84.4 | <https://registry.npmjs.org/@earendil-works/pi-agent-core/-/pi-agent-core-0.84.4.tgz> |

CyberPenda pins nothing: both Dockerfiles install `@earendil-works/pi-coding-agent@latest`
and `@tintinweb/pi-subagents@latest`
(`docker/pentest-sandbox/Dockerfile:43,90`; `docker/tsecbench-hosted/Dockerfile:96-97`),
and enable both packages in Pi settings
(`{"packages":["npm:@tintinweb/pi-subagents","npm:pi-subagents"]}` at
`docker/pentest-sandbox/Dockerfile:92`, `docker/tsecbench-hosted/Dockerfile:145`).
`@tintinweb/pi-subagents@0.19.0` declares peer dependency
`@earendil-works/pi-coding-agent >=0.84.0` (`package.json` of the 0.19.0 tarball).

**The two enabled packages are different products.** `@tintinweb/pi-subagents`
("Claude Code-like sub-agents… parallel execution, live widget, fleet view" —
tarball `package.json` description) and `pi-subagents` by nicobailon ("single-agent
delegation and scripted multi-agent workflows" — its tarball `package.json`) each
register their own delegation tools and emit different event vocabularies.
Both are covered below; tintinweb is primary because the issue names it.

## Question 1 — emitted events per mode

### 1a. Tintinweb extension: the event bus is in-process and never crosses the RPC boundary

`@tintinweb/pi-subagents` emits eleven lifecycle events on the Pi extension
event bus via `pi.events.emit()` (README "Events" table, tarball `README.md:722-741`;
event table rows at `README.md:728-738`):

| Event | Payload | Emit site in `dist/index.js` (0.19.0) |
| --- | --- | --- |
| `subagents:created` | `{id, type, description, isBackground}` (always `true`) | `:1247` (detached resume), `:1932` (Agent-tool background spawn) |
| `subagents:started` | `{id, type, description}` | `:563` |
| `subagents:completed` | `{id, type, description, status, durationMs, tokens{input,output,total}, usage, toolUses, result}` | `:522` |
| `subagents:failed` | identical to `completed` (same formatter; `error`, `status` filled) | `:519` |
| `subagents:steered` | `{id, message}` | `:851`, `:2606`, `:2611` |
| `subagents:compacted` | `{id, type, description, reason, tokensBefore, compactionCount}` | `:572` |
| `subagents:scheduled` / `subagents:scheduler_ready` / `subagents:ready` / `subagents:settings_loaded` / `subagents:settings_changed` | see README table | `:714`, `:765`, settings emitters |

Scoping rule: "The four agent-lifecycle events — `subagents:started`,
`:completed`, `:failed`, `:compacted` — are emitted for **top-level agents only**.
Nested subagents and a workflow's children emit nothing at all"
(`README.md:740`; enforced by the `isTopLevelAgent(record)` early returns at
`dist/index.js:513, 552, 569`; predicate = `parentAgentId === undefined &&
workflowId === undefined`, documented in `docs/rpc.md:101`).

**Critical transport fact:** the bus does not cross the process boundary.
`docs/rpc.md:5`: "the bus is in-process. Every 'RPC' call here is a synchronous
`pi.events.emit` into the same event loop… none of this survives a real process
boundary." Pi's RPC mode only forwards `AgentSessionEvent` objects from
`session.subscribe` to stdout:

- `dist/modes/rpc/rpc-mode.js:265-266`: `unsubscribe = session.subscribe((event) => { output(toJsonEvent(event)); })`
- `dist/modes/rpc/rpc-mode.js:10` header comment: "Events: AgentSessionEvent objects streamed as they occur"

The bus itself is a plain Node `EventEmitter` (`dist/core/event-bus.js` of
`@earendil-works/pi-coding-agent` 0.84.4), exposed to extensions as
`pi.events` (`dist/core/extensions/types.d.ts:1077`, "Shared event bus for
extension communication"). Nothing in rpc-mode subscribes to it.

The complete `AgentSessionEvent` union (what RPC mode *can* emit) is defined in
`dist/core/agent-session.d.ts:40-105` and `dist/types.d.ts:375-415` of
`pi-agent-core` 0.84.4: `agent_start`, `agent_end {messages, willRetry}`,
`agent_settled`, `turn_start`, `turn_end {message, toolResults}`,
`message_start {message}`, `message_update`, `message_end {message}`,
`tool_execution_start {toolCallId, toolName, args}`,
`tool_execution_update {toolCallId, toolName, args, partialResult}`,
`tool_execution_end {toolCallId, toolName, result, isError}`, plus
session-level `queue_update`, `compaction_start/end`, `entry_appended {entry}`,
`session_info_changed`, `thinking_level_changed`, `auto_retry_*`,
`summarization_retry_*`, `bash_execution_update`. Extensions can additionally
trigger `extension_ui_request` frames (`dist/modes/rpc/rpc-types.d.ts`,
`RpcExtensionUIRequest`: `select`, `confirm`, `input`, `editor`, `notify`,
`setStatus`, `setWidget`, `setTitle`, `set_editor_text`).

### 1b. RPC mode: what CyberPenda actually receives for a subagent

CyberPenda's bridge forwards every non-response Pi stdout frame verbatim as a
JSON-RPC notification whose method is `pi/` + the frame's `type`
(`cmd/pentest-provider-bridge/main.go:293-311`, `w.event()`):

```go
_ = w.writeFrame(map[string]any{"jsonrpc": "2.0", "method": "pi/" + typeName, "params": params})
```

with `params` = the raw frame plus bridge-injected `session_id` and `turn_id`
(`main.go:298-309`). So the observable subagent footprint in persistent RPC mode
is exactly the parent session's `AgentSessionEvent` stream:

1. **Spawn** — `pi/tool_execution_start` with `toolName: "Agent"` and `args`
   carrying `subagent_type`, `description`, `prompt`, optional `name`,
   `run_in_background`, `resume`, `max_turns`… (tool schema at
   `dist/index.js:1467-1505` of the extension; frame shape
   `{type:"tool_execution_start", toolCallId, toolName, args}` from
   `pi-agent-core` `dist/types.d.ts:399-403`). There is **no** subagent id in
   this frame — only the parent-side `toolCallId`.
2. **Progress** — nothing subagent-specific. The extension updates its TUI
   widget through `ctx.ui`, which in RPC/print mode (`ctx.hasUI === false`,
   `dist/main.js:601` of Pi) degrades to fire-and-forget
   `extension_ui_request` frames (`rpc-mode.js:77-191`). Widget and fleet
   refresh calls are gated on `currentCtx?.hasUI` (`dist/index.js:556`), but
   `ctx.ui.notify(...)` completion/steering notices are **not** hasUI-gated and
   will arrive as `pi/extension_ui_request` with `method: "notify"`,
   `message: "…"`, `notifyType: "info"|"warning"|"error"`
   (e.g. `dist/index.js:852, 865, 887, 901, 923, 958, 977, 999`). These are
   human-worded strings, not structured identity — usable as a heuristic signal
   only.
3. **Settle (foreground)** — `pi/tool_execution_end` for the same
   `toolCallId`, with `result.content[0].text` = the agent's final report and
   `result.details` carrying `{status: "completed"|"steered"|"stopped"|"error"
   |"aborted", agentId, durationMs, toolUses, tokens, modelName, turnCount,
   maxTurns}` — the details keys are enumerated by the extension's own
   `renderResult` (`dist/index.js:1528-1611`). **`details.agentId` is the
   durable subagent id** (background tool result sets it explicitly:
   `dist/index.js:1855` `status: "background", agentId: id`).
4. **Settle (background)** — the parent session receives
   `pi/message_start` / `pi/message_end` for a custom message with
   `customType: "subagent-notification"` ("Background agent group completed:
   …", `dist/index.js:423-430, 461-467`; workflow variant
   `customType: "workflow-result"` at `:2194-2195, 2491-2492`). The notification
   `details` include the first finished agent's id via
   `buildNotificationDetails(...)`, and `details.others[]` for the rest
   (`dist/index.js:455-460`).
5. **Settle (any top-level agent)** — `pi/entry_appended` with
   `entry.type === "custom"`, `entry.customType === "subagents:record"` and
   `entry.data` = `{id, type, description, status, result, error, startedAt,
   completedAt}` (extension side: `pi.appendEntry("subagents:record", …)` at
   `dist/index.js:525-532`; Pi side: `appendEntry` emits
   `{type:"entry_appended", entry}` at `dist/core/agent-session.js:2022-2028`;
   entry shape `CustomEntry` at `dist/core/session-manager.d.ts:68-72`). This is
   the **only structured, id-carrying settled signal on the RPC wire**. It
   fires for foreground and background settles alike (CHANGELOG entry for #105:
   "`onComplete` now fires for foreground agents, emitting
   `subagents:completed` / `subagents:failed` lifecycle events and writing a
   `subagents:record` entry to the parent JSONL").

The in-process bus events (`subagents:created/started/completed/failed/…`)
never appear as `pi/*` frames.

### 1c. One-shot session JSONL mode: record shapes

Pi session files live under `<agentDir>/sessions/--<cwd-with-dashes>--/` and are
named `<ISO timestamp with : and . replaced by ->_<sessionId>.jsonl`
(`dist/core/session-manager.js:669`, dir scheme at `:242-246`). Record types
(`FileEntry = SessionHeader | SessionEntry`, `dist/core/session-manager.d.ts:107-109`):

- Header: `{type:"session", version:3, id, timestamp, cwd, parentSession?}`
  (`session-manager.d.ts:5-12`).
- Entries (`SessionEntry` union, `session-manager.d.ts:104`): `message`
  (`{type:"message", id, parentId, timestamp, message: AgentMessage}`),
  `thinking_level_change`, `model_change`, `compaction`, `branch_summary`,
  `custom` (`{type:"custom", customType, data?}`),
  `custom_message` (`{type:"custom_message", customType, content, details?,
  display}`), `label`, `session_info`. Every entry carries the base fields
  `{type, id, parentId, timestamp}` (`session-manager.d.ts:15-20`).

For subagent activity the **parent** session file contains:

- A `message` entry for the assistant `toolCall` block invoking `Agent`
  (the `AgentMessage` content block carries the tool call id, name `"Agent"`,
  and the `subagent_type`/`description` args).
- A `message` entry with the tool result whose `details` include
  `status`/`agentId` (foreground) or `status:"background", agentId`
  (background kickoff).
- On settle: a `custom` entry `customType: "subagents:record"` with
  `data: {id, type, description, status, result, error, startedAt, completedAt}`
  (`dist/index.js:525-532`).
- On background completion: a `custom_message` entry
  `customType: "subagent-notification"`, `display: true`, with the human summary
  in `content` and per-agent ids in `details` (`dist/index.js:423-430, 461-467`).
- Note: Pi only starts persisting a session file once the first assistant
  message exists (`session-manager.js:_persist`, "hasAssistant" gate at
  `:728-745`), so a fresh one-shot file appears after the first assistant reply.

## Question 2 — durable identity and started/settled sufficiency

Yes, with caveats:

- **Durable identity**: `AgentRecord.id` is `randomUUID().slice(0, 17)`
  (`dist/agent-manager.js:275`). The same id appears in the background tool
  result (`details.agentId`, `dist/index.js:1855`), in every bus lifecycle event
  payload (`id` field), and in the `subagents:record` entry data
  (`dist/index.js:526`). Human-facing type/description/handle ride along:
  `type` (subagent_type), `description` (3-5 word task label), plus optional
  `handle`/`alias` on the record (`dist/types.d.ts:146-163`).
- **Started**: no structured started frame in RPC mode. Best available started
  signal is the parent-side `pi/tool_execution_start` for `Agent`, keyed by
  `toolCallId`; the subagent id arrives only at settle (`subagents:record`) or,
  for background spawns, in the immediate `pi/tool_execution_end`
  (`details.agentId`, `status:"background"`). In one-shot mode the same pairing
  applies to the toolCall/toolResult `message` entries. (In-process,
  `subagents:created`/`subagents:started` would give id-keyed starts, but they
  do not cross the boundary — see 1a.)
- **Settled**: yes. `subagents:record` carries `status` with vocabulary
  `"queued" | "running" | "completed" | "steered" | "aborted" | "stopped" |
  "error"` (`AgentRecord.status`, `dist/types.d.ts:163`), written exactly once
  per top-level settle, plus `startedAt`/`completedAt` epoch-ms.

So a started/settled projection is feasible in both modes: correlate
`toolCallId`(start) → `agentId`(settle) by order of appearance per `Agent`
call, or take background agents' `agentId` straight from the tool result.

## Question 3 — one-shot mode: parent file or own file?

Both, depending on the signal:

- **Bus-visible lifecycle and the `subagents:record` entry land in the PARENT
  session file.** `pi.appendEntry` runs on the root session's manager
  (`dist/index.js:525` is executed by the extension instance bound to the root
  session; `agent-session.js:2022-2028` appends to that session's own manager).
  The toolCall/toolResult `message` entries are also in the parent file.
- **The subagent's own transcript goes to its OWN newer session file by
  default.** `persistSession` defaults to `true` for top-level agents
  (`rememberAgents` default `true`, README:315 and README:632; resolution at
  `dist/agent-runner.js:742`: `agentConfig?.persistSession ?? (options.nested ?
  false : rememberAgents)`), creating
  `SessionManager.create(effectiveCwd, configuredSessionDir ?? defaultSessionDir,
  { parentSession: ctx.sessionManager?.getSessionFile?.() })`
  (`dist/agent-runner.js:749-755`). The child file's header therefore carries
  `parentSession` = the parent's session file path (`SessionHeader.parentSession`,
  `session-manager.d.ts:11`). Nested subagents (`options.nested`) default to
  in-memory (`SessionManager.inMemory`, `:757`) and write no file.

Consequence for the current tailer: `tailPiSession` follows only the newest
`*.jsonl` under the session dir and re-resolves whenever a newer file appears
(`internal/runtime/pi_session_tail.go:103-128`,
`newestSessionFile` at `:180-199`). A spawned subagent creates a newer file,
so **the tailer switches away from the parent file to the subagent file for
the subagent's lifetime**; parent-side entries written meanwhile (including a
`subagents:record` for a *different* agent that settles) are read late or —
if the inner runtime exits while the tail points at the child — only via the
final drain of the *current* file. Subagent activity is thus partially
observable in one-shot mode today (the child's own messages stream by, but
unattributed; the parent's `subagents:record` entries can be stranded).

## Question 4 — versioning and stability

- Tintinweb bus protocol: `PROTOCOL_VERSION = 2`
  (`dist/cross-extension-rpc.d.ts:33`; docs/rpc.md:149: "currently `2`…
  introduced already equal to `2` in 0.5.0"). The version pins only the
  `subagents:rpc:*` reply envelopes; the **lifecycle event payloads are not
  versioned**. docs/rpc.md:175: "Not pinned anywhere, so treat them as
  descriptions rather than contracts". The CHANGELOG shows additive evolution
  (`usage` added to completed/failed in #137/#138; `subagents:compacted` added
  0.x; foreground completion events added in #105).
- Pi RPC protocol: no protocol version field anywhere in
  `dist/modes/rpc/rpc-types.d.ts`; frames are structurally typed. The session
  **file** format is versioned: `CURRENT_SESSION_VERSION = 3`
  (`dist/core/session-manager.d.ts:4`), carried in the header's `version`
  field. Pi `@earendil-works/pi-coding-agent` is pre-1.0 (0.84.4) and the
  subagents extension pins only `>=0.84.0`; the Dockerfile tracks `@latest`, so
  shapes must be treated as moving — pin and re-verify at image build time.
- The nicobailon package versions its async lifecycle artifacts
  (`SUBAGENT_LIFECYCLE_ARTIFACT_VERSION` referenced at
  `src/runs/background/async-execution.ts:1420` of `pi-subagents@0.59.0`) and
  emits `subagents:rpc:v1:ready` (`src/extension/rpc.ts:31`) plus
  `subagent:async-started` / `subagent:async-complete` /
  `subagent:child-status` bus events (`src/shared/types.ts:2188-2197`). Same
  in-process-only transport caveat applies; its async mode spawns detached Pi
  child processes whose results are watched via files, not parent-session
  records.

## Recommendation — where the parser plugs in

The normalized target already exists: `internal/runtimeoutput/turn.go:14-27`
defines `KindSubagentActivity` with `ProviderItemID` (durable child identity),
`LifecyclePhase` (`SubagentActivityStarted/Interrupted/Completed/Failed`),
`Text`, `Tool`; per-provider normalizers live in
`internal/runtimeoutput/subagent_activity.go` (`parseCodexSubAgentActivity`,
`parseCodexCollabAgentToolCall`, `parseClaudeSubagentActivity`), dispatched from
`ParseRecordWithMeta` at `internal/runtimeoutput/parse.go:64-76`.

Plug a Pi parser in at the same seam, in two places:

1. **One-shot / JSONL path** — add `parsePiSubagentActivity(record, createdAt)`
   in `internal/runtimeoutput/subagent_activity.go`, dispatched from
   `ParseRecordWithMeta` for (a) `type:"custom"` with
   `customType:"subagents:record"` → settled turn keyed by `data.id`, label from
   `data.type`/`data.description`; (b) `type:"message"` toolResult entries whose
   tool is `Agent` → started turn keyed by `details.agentId` (background) and
   settled for foreground; (c) `type:"custom_message"` with
   `customType:"subagent-notification"` → settled fallback. The tailed
   `pi_session` lines already flow into `ParseRecordWithMeta` via
   `internal/transcript/transcript.go:316-325` and `:387-389`. Independently,
   the tailer should follow **all** session files (or at least stay pinned to
   the parent's `parentSession` graph) instead of only the newest
   (`pi_session_tail.go:newestSessionFile`, `:180-199`), otherwise parent-side
   `subagents:record` lines are missed while a child file is newest.
2. **Persistent RPC path** — intercept in `(*PiProviderSession).HandleEvent`,
   `internal/runtime/pi_assisted.go:64-93`, ahead of the existing boundary
   switch: recognize `pi/entry_appended` with
   `params.entry.customType == "subagents:record"` (settled; id/type/
   description/status under `params.entry.data`) and
   `pi/tool_execution_start`/`pi/tool_execution_end` with
   `toolName == "Agent"` (started / inline-settled, id from
   `result.details.agentId` where present). This is the same method-prefix
   dispatch pattern already used by `piToolUseBoundary`/`piTerminalBoundary`
   (`pi_assisted.go:279-302`). Events arrive there through
   `(*ProviderSessionRunAdapter).HandleBridgeEvent`
   (`internal/runtime/provider_bridge_adapter.go:121-132`) ←
   `internal/daemon/provider_session_assembler.go:136,210` ← piWire
   `w.event()` (`cmd/pentest-provider-bridge/main.go:293-311`). Project to the
   same `KindSubagentActivity` vocabulary so the Timeline sees one shape
   regardless of mode.

## Unknowns / not verified

- **start↔settle correlation for foreground spawns**: the
  `pi/tool_execution_start` frame has no subagent id; `subagents:record` has no
  `toolCallId`. The record's `AgentRecord.toolCallId` field exists in-process
  (`dist/index.js:1906`) but is not serialized into any wire shape I could find
  in 0.19.0. Whether `details.agentId` is present on *foreground* tool results
  was inferred from the shared `detailBaseFor(record)` helper and
  `renderResult`'s use of `details.agentId` (`dist/index.js:1561`), not from a
  live run.
- **Exact JSON of `subagents:record` as persisted** (key casing inside `data`)
  was read from the emit call, not from a captured file.
- **Extension UI `notify` frame volume in RPC mode**: `ctx.ui.notify` calls are
  fire-and-forget (`rpc-mode.js:83-90`), but I did not verify at runtime whether
  `ctx.hasUI === false` suppresses any of the extension's notification sites
  beyond the widget/fleet ones (`dist/index.js:556` gates only widget/fleet
  timers).
- **The nicobailon package's async child sessions**: whether its detached child
  Pi processes write session files under the same directory (and would thus
  hijack the newest-file tailer) was not traced; its `asyncDir`-based result
  watching suggests a separate tree.
- **Interaction when both extensions are enabled**: both register an `Agent`
  tool; Pi's conflict behavior (which registration wins) was not verified.
- No live Pi run was executed; all claims are from the published 0.19.0 /
  0.59.0 / 0.84.4 artifacts cited above.
