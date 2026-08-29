# Hermes ACP Delegation / Child-Agent Events

Date: 2026-08-29

Issue: n1majne3/CyberPenda#247 (parent: #237, Timeline projection of delegated
/child agents — CONTEXT.md **Subagent Activity**).

Verdict: **no child-agent wire shape exists on the ACP connection Hermes
serves.** Neither the ACP specification (stable v1 schema nor the unstable
extension surface at the examined commit) nor Hermes Agent 0.20.6's ACP
adapter emits any child-session, nested-agent, or delegation notification.
The only delegation signal on the wire is the generic `tool_call` /
`tool_call_update` pair for Hermes' `delegate_task` tool, which CyberPenda
already receives and drops nothing of — it carries no durable per-child
identity. Mark Hermes out of scope for the Subagent Activity wire projection,
or file an upstream feature request against NousResearch/hermes-agent. Details
and citations below.

## Versions examined

| Component | Version / commit | Source |
| --- | --- | --- |
| ACP spec + JSON schema | repo `agentclientprotocol/agent-client-protocol` (mirrored from `zed-industries/agent-client-protocol`), commit `9e6f550706b94705e8080b69f7cf46ca3cdb7614` (2026-08-28), library release line 1.7.0 (CHANGELOG.md heading `## [1.7.0] ... - 2026-08-20`) | https://github.com/agentclientprotocol/agent-client-protocol/tree/9e6f550706b94705e8080b69f7cf46ca3cdb7614 |
| ACP Python SDK used by Hermes | `agent-client-protocol==0.9.0`, pinned in hermes-agent `pyproject.toml:283` (`acp = ["agent-client-protocol==0.9.0"]`) and locked in `uv.lock:36-44` (PyPI sdist `agent_client_protocol-0.9.0.tar.gz`, upload-time 2026-03-26). Verified installed 0.9.0: `PROTOCOL_VERSION = 1` | https://github.com/NousResearch/hermes-agent/blob/586672532347252e3df7d893223ce003070ee434/pyproject.toml#L283 |
| Hermes Agent | `NousResearch/hermes-agent` commit `586672532347252e3df7d893223ce003070ee434` (2026-08-29), version 0.20.6 (`hermes_cli/__init__.py:17` `__version__ = "0.20.6"`; `pyproject.toml` `version = "0.20.6"`) | https://github.com/NousResearch/hermes-agent/tree/586672532347252e3df7d893223ce003070ee434 |
| CyberPenda | commit `6a578a1549086e8b4891a7f8667fa74bb6c9271e` | this repo |

CyberPenda pins no Hermes version: the sandbox and hosted Dockerfiles install
"latest release at image-build time" via
`https://hermes-agent.nousresearch.com/install.sh` and record the resolved
version at build time
(`docker/pentest-sandbox/Dockerfile:49-55`,
`docker/tsecbench-hosted/Dockerfile:147-154`).

## 1. ACP: every `session/update` kind, and whether any child-session concept exists

### Stable schema (`schema/v1/schema.json`, examined commit)

`SessionUpdate` (`schema/v1/schema.json:3656-3831`, `oneOf` with discriminator
`sessionUpdate`) defines exactly **11** variants:

1. `user_message_chunk` — "A chunk of the user's message being streamed." (`schema.json:3661-3678`)
2. `agent_message_chunk` — "A chunk of the agent's response being streamed." (`schema.json:3677-3694`)
3. `agent_thought_chunk` — "A chunk of the agent's internal reasoning being streamed." (`schema.json:3697-3710`)
4. `tool_call` — "Notification that a new tool call has been initiated." (`schema.json:3713-3726`)
5. `tool_call_update` — "Update on the status or results of a tool call." (`schema.json:3729-3742`)
6. `plan` — "The agent's execution plan for complex tasks." (`schema.json:3745-3758`)
7. `available_commands_update` — "Available commands are ready or have changed" (`schema.json:3761-3774`)
8. `current_mode_update` — "The current mode of the session has changed" (`schema.json:3777-3790`)
9. `config_option_update` — "Session configuration options have been updated." (`schema.json:3793-3806`)
10. `session_info_update` — "Session metadata has been updated (title, timestamps, custom metadata)" (`schema.json:3809-3822`)
11. `usage_update` — "Context window and cost update for the session." (`schema.json:3814-3827`)

The `session/update` notification envelope is
`{sessionId, update, _meta?}` (`schema.json:3619-3653`, `x-method:
"session/update"`).

### Unstable schema additions (`schema/v1/schema.unstable.json`)

Four more variants, all marked **UNSTABLE** and none about child agents:

- `plan_update` — "A content update for a plan identified by ID." (`schema.unstable.json:5078-5094`)
- `plan_removed` — "Removal notice for a plan identified by ID." (`schema.unstable.json:5094-5110`)
- `compaction_update` — "A context compaction has been created or updated." (`schema.unstable.json:5201-5218`)
- `compaction_summary_chunk` — "A content block appended to a context compaction's retained summary." (`schema.unstable.json:5218-5234`)

### Session-related methods (unstable schema `x-method` inventory)

`initialize`, `authenticate`, `logout`, `session/new`, `session/load`,
`session/list`, `session/prompt`, `session/cancel`, `session/update`,
`session/request_permission`, `session/set_mode`,
`session/set_config_option`, plus unstable: `session/fork`, `session/resume`,
`session/close`, `session/delete`
(`schema.unstable.json` `x-method` markers, e.g. `session/fork` at
lines 4216 and 7195).

`session/fork` is **client→agent**: "Forks an existing session to create a new
independent session ... The agent should create a new session with the same
conversation context as the original, allowing operations like generating
summaries without affecting the original session's history."
(`schema.unstable.json:6173-6177`). It is a client-initiated copy operation,
**not** an agent-spawned child notification; it carries no parent→child
lifecycle events.

### Does ACP have any subagent/child-session concept?

**No.** Case-insensitive search of the full stable and unstable v1 schemas for
`delegat`, `subagent`, `sub-agent`, `child` finds only `session/fork` (above);
there is no `sessionUpdate` variant, method, or capability for subagents,
child sessions, or delegation. There is also no RFD for subagents under
`docs/rfds/` (directory listing at examined commit; closest topics are
`session-fork.mdx`, `session-compaction.mdx`, `session-notices.mdx`).

The protocol's sanctioned escape hatch is `_meta`: "The _meta property is
reserved by ACP to allow clients and agents to attach additional metadata to
their interactions. Implementations MUST NOT make assumptions about values at
these keys." (`schema.json:3642-3648`, pointing at
https://agentclientprotocol.com/protocol/extensibility).

## 2. What Hermes emits over ACP when `delegation.*` / `agent.*` config is active

Hermes Agent 0.20.6's ACP adapter bridges exactly these update kinds (all
emission goes through `conn.session_update(...)`):

- `tool_call` / `tool_call_update` — from AIAgent `tool_progress_callback`
  (`tool.started`) and `step_callback` (tool completion):
  `acp_adapter/events.py:113-172` and `:186-250`, built by
  `acp_adapter/tools.py` (`build_tool_start`, `build_tool_complete`,
  `build_tool_complete` docstring "Create a ToolCallUpdate (progress) event
  for a completed tool call." at `tools.py:1305-1327`).
- `agent_thought_chunk` — reasoning callback: `acp_adapter/events.py:180-200`
  (`acp.update_agent_thought_text`), wired as `agent.reasoning_callback` in
  `acp_adapter/server.py:1984-1986`.
- `agent_message_chunk` — message/stream-delta callback:
  `acp_adapter/events.py:258-279` (`acp.update_agent_message_text`).
- `user_message_chunk` — queued-prompt replay: `acp_adapter/server.py:2188-2197`.
- `plan` — translated from the `todo` tool result:
  `acp_adapter/events.py:39-84` (`AgentPlanUpdate(session_update="plan", ...)`).
- `available_commands_update`, `usage_update`, `session_info_update` —
  `acp_adapter/server.py:2224-2240`, `:999-1049`, `:1071-1117`.

**Delegation produces no dedicated ACP traffic.** `grep -n "delegation"
acp_adapter/*.py` (examined commit) returns zero hits — the adapter never
mentions delegation. When `delegate_task` runs, it is projected as an ordinary
polished tool call:

- Title: `delegate batch (N tasks)` or `delegate: <goal>`
  (`acp_adapter/tools.py:128-135`).
- Kind: `execute` (`acp_adapter/tools.py:53`).
- Start content: "Delegating task:\n<goal>" or a numbered task list
  (`acp_adapter/tools.py:1237-1254`).
- Completion content: a formatted summary "Delegation results: N tasks in Ts"
  with per-task `✅/✗/⏱/⚠ Task i: status (model, role=..., Ts)` lines
  (`acp_adapter/tools.py:590-620`, `:911`).
- Because `delegate_task` is in `_POLISHED_TOOLS` (`tools.py:62-65`), both
  `raw_input` and `raw_output` are **nulled** (`tools.py:1298`, `:1326`), so
  the wire payload contains no machine-readable child identity at all.

The `delegation.*` config keys CyberPenda's manifest manages
(`internal/runtimeplugin/builtin.go:276`) are real Hermes config — the
example config documents `delegation.max_iterations`,
`max_concurrent_children`, `max_spawn_depth`, `orchestrator_enabled`,
`subagent_auto_approve`, `inherit_mcp_toolsets`, `model`, `provider`
(`cli-config.yaml.example:1530-1554`) and `agent/iteration_budget.py:6,23-25`
confirms `delegation.max_iterations` — but they only govern child execution;
they cause no extra ACP notifications.

Where delegation *is* observable inside Hermes (none of it on the ACP wire):

- In-memory subagent registry for the TUI: records `{subagent_id,
  delegation_id, parent_id, depth, goal, model, started_at, status:
  "running", ...}` (`tools/delegate_tool.py:2622-2648`); subagent ids look
  like `sa-<task_index>-<8 hex>` (`tools/delegate_tool.py:1658`
  `subagent_id = f"sa-{task_index}-{_uuid.uuid4().hex[:8]}"`).
- Append-only live transcripts at
  `<hermes_home>/cache/delegation/live/<delegation_id>/task-<n>.log`
  (`tools/delegation_live_log.py:1-16`) — filesystem side channel,
  deliberately: "Side-channel only." (`delegation_live_log.py:24`).
- TUI/gateway RPC `delegation.status` (`tui_gateway/methods_session.py:3351`)
  and the TUI's `delegationStore` (`ui-tui/src/app/delegationStore.ts`) —
  a separate gateway protocol, not ACP.
- Completion re-entry: async delegation completions are forged into a **new
  user/internal turn** on the parent's conversation via
  `process_registry.completion_queue` (`tools/async_delegation.py:7-28`).

Hermes also has one genuine ACP extension, but it models **context-compression
session rotation, not child agents**: `_meta.hermes.sessionProvenance` on
`session_info_update` (`acp_adapter/provenance.py:1-12`, `:79-97`;
emitted from `acp_adapter/server.py:2133-2158`). Payload keys:
`acpSessionId`, `currentHermesSessionId`, `rootHermesSessionId`,
`parentHermesSessionId`, `sessionKind` (`"continuation"` or `"root"`),
`compressionDepth`, optional `previousHermesSessionId`, `reason` /
`creatorKind` = `"compression"` (`provenance.py:79-97`). The code explicitly
notes "delegate/branch children share the parent_session_id column but are
not compaction boundaries" (`provenance.py:55-57`) — i.e. child sessions exist
in the Hermes DB but are deliberately excluded from this metadata.

## 3. Does a shape with durable child identity + started/settled state exist?

**No.** The only delegation-adjacent wire shapes:

| Shape | Durable child identity? | Started/settled state? |
| --- | --- | --- |
| `tool_call`/`tool_call_update` for `delegate_task` | No. One ACP `toolCallId` per delegation *call* (synthesized `tc-<12 hex>`, `acp_adapter/tools.py:87-89`); `subagent_id` / `delegation_id` never appear (raw input/output nulled, `tools.py:1298`, `:1326`; the formatted summary text includes only status/model/role/duration, `tools.py:590-620`). | Coarse only: tool-call `status` `pending`→`completed`/`failed` for the whole batch, not per child. |
| `_meta.hermes.sessionProvenance` on `session_info_update` | Session ids, yes — but for compression rotation of the *same* ACP session, not children. | No activity state for children; `sessionKind` is `root`/`continuation`. |
| `plan` update | Plan entries have no per-agent identity (ACP `PlanEntry(content, priority, status)`, used at `acp_adapter/events.py:60-84`). | n/a |

By contrast, the already-supported Codex path shows what #237 needs on the
wire: `subAgentActivity` items carry `agentThreadId`, `agentPath`, `kind:
"started"`, and `collabAgentToolCall` carries `receiverThreadIds` plus
per-thread `agentsStates` (`internal/runtime/codex_assisted.go:170-172`;
fixtures in `internal/runtime/codex_subagent_activity_test.go:24,55`). No
Hermes equivalent exists.

## 4. Not applicable vs upstream feature request

Both elements are true and split this way:

- **Not applicable today — record the finding and mark Hermes out of scope for
  #237's wire-driven Subagent Activity.** ACP (the protocol Hermes speaks)
  defines no child-agent notification, and Hermes' adapter emits none.
- **If Hermes coverage is wanted later, it needs an upstream feature request
  to NousResearch/hermes-agent**, because all primitives already exist
  agent-side and only the ACP bridge is missing: the registry records
  `subagent_id`, `delegation_id`, `parent_id`, `status: running`→terminal
  (`tools/delegate_tool.py:2622-2648`), and completion/failure is already
  detected in `_run_single_child` (`tools/delegate_tool.py:2755`,
  `:4303-4325`). A minimal upstream change could surface these as
  `_meta.hermes.delegation` payloads on `session/update` (ACP-sanctioned
  extensibility, `schema/v1/schema.json:3642-3648`) or as a
  `session_info_update` extension, mirroring the existing
  `sessionProvenance` precedent (`acp_adapter/provenance.py`).

## 5. Recommendation (where a parser would plug in, if a shape ever exists)

CyberPenda's per-provider live-event seam for Hermes is
`(*HermesProviderSession).HandleEvent`
(`internal/runtime/hermes_assisted.go:69-125`). Its `switch kind` handles
`agent_message_chunk`, `agent_thought_chunk`, `tool_call`, `tool_call_update`,
`turn_ended` and drops everything else into `providerSessionAdapter.HandleEvent`
(`hermes_assisted.go:121-124`). If Hermes ever emits a delegation update, a
parser plugs in as a new `case` in that switch (kind extraction lives in
`hermesACPUpdate` / `hermesACPUpdateKind`, `hermes_assisted.go:378-402`),
with the replay-side counterpart in `parseHermesACPRecord`
(`internal/runtimeoutput/parse.go:500-583`, whose `default:` at line 580-581
currently drops unknown kinds; the record recognizer `isHermesACPRecord`,
`parse.go:489-498`, already admits any `session/update`).

Do **not** scrape the human-readable `delegate: <goal>` title or "Delegation
results" summary text for identity — there is no identity there to scrape
(`acp_adapter/tools.py:128-135`, `:590-620`). The `delegate_task` tool-call
payload is sufficient only for a *generic tool-call* Timeline entry, which
CyberPenda already projects today via the `tool_call` case.

## Unknowns / not verified

- **Runtime behavior not executed.** Findings are from source at the pinned
  commits; no live `hermes acp` session was run to capture actual frames.
  Plugin hooks or post-0.20.6 commits could add emissions not visible here.
- **Exact ACP schema snapshot behind PyPI `agent-client-protocol==0.9.0`.**
  The 0.9.0 wheel (uploaded 2026-03-26, per `uv.lock:42-44`) exposes the 11
  stable update kinds plus `session/fork`, `session/resume`, `session/list`,
  `session/close`, `session/delete` helpers (verified by installing the wheel;
  its `PROTOCOL_VERSION = 1`), but the exact upstream git commit for the 0.9.0
  release was not located (the python-sdk repo clone, main@`ce23c4a6`, is at
  0.12.1 and its tags were not enumerated). The 0.9.0 wheel source was read
  directly and matches the stable v1 schema set.
- **Hermes gateway/TUI protocol** (`delegation.status` RPC, subagent list/
  steer/stop) was only skimmed to confirm it is a separate, non-ACP surface;
  its full method inventory was not catalogued.
- **ACP v2 draft** (`schema/v2/`, `docs/protocol/v2/`) was not enumerated
  variant-by-variant; it is explicitly a draft/migration surface and Hermes
  pins a v1 (`PROTOCOL_VERSION = 1`) SDK. A v2-only subagent proposal would
  not help #237 until Hermes adopts it; a quick keyword check of v1 unstable
  already found nothing, but the v2 tree was not exhaustively grepped.
- **`session/fork` semantics in Hermes** (`acp_adapter/session.py:253`
  `fork_session`) were confirmed to exist but not exercised; it remains a
  client-initiated copy, not a child-agent event, in both spec text and the
  Hermes implementation.
