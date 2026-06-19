# Runtime Transcript Design

## Status

Approved design for implementation planning.

## Goal

Add a readable, complete-as-retained runtime transcript to the task detail page. The transcript must work for existing and new tasks, preserve continuation boundaries, expose tool activity without overwhelming the conversation, and retain unknown provider output instead of silently dropping it.

## Context

Task execution currently emits ordered `task_events`. Runtime stdout and stderr are stored as redacted `runtime_output` events, while lifecycle and steering actions use their own event kinds. The task detail page renders these records as a diagnostic Timeline.

The Timeline is useful for debugging but does not provide a readable conversation. The new transcript is a projection over the same retained events, not a replacement for the Timeline or a second source of truth.

## User Experience

The task detail page adds two tabs:

- **Conversation**: the default view, optimized for reading the runtime transcript.
- **Timeline**: the existing ordered event view, preserved without behavioral changes.

Conversation displays:

- The original task goal as the first user message.
- Assistant messages reconstructed from provider output.
- Steering directives as later user messages.
- A labeled separator for every resumed continuation.
- Tool calls and tool results as collapsed disclosure rows.
- Output that cannot be parsed as collapsed **Runtime output** disclosure rows.

Each disclosure row shows a concise label and status while collapsed. Expanding it shows the complete retained and redacted payload. Stdout and stderr remain distinguishable.

While a task is active, the Conversation view polls on the same cadence as the Timeline. New entries append without resetting manually expanded disclosure rows. Completed and failed tasks stop polling.

The first version does not add transcript search, filtering, editing, or export.

## Architecture Decision

Generate the transcript in the backend through a dedicated read endpoint. Do not parse provider streams in React and do not add canonical transcript persistence in this version.

This approach provides:

- Immediate support for historical tasks whose runtime output is already retained.
- One normalization contract shared by all clients.
- Provider parsing that can be unit tested independently of the UI.
- No database migration or duplicated transcript storage.
- A fallback path that keeps unfamiliar output visible.

Historical fidelity is limited to events already retained by the current runtime capture path. The projection cannot recover output that was never stored.

## HTTP API

Add:

```text
GET /api/projects/{project_id}/tasks/{task_id}/transcript
```

The handler uses the same project/task ownership checks and not-found behavior as the task events endpoint.

The response shape is:

```json
{
  "task_id": "task-id",
  "entries": [
    {
      "id": "stable-derived-id",
      "seq": 12,
      "continuation": 0,
      "kind": "message",
      "role": "assistant",
      "text": "Mapped the login endpoint.",
      "created_at": "2026-06-19T10:00:00Z"
    }
  ]
}
```

Transcript entry fields:

- `id`: stable across repeated projections of unchanged events.
- `seq`: source event sequence used for ordering.
- `continuation`: zero-based execution segment number.
- `kind`: `message`, `tool_call`, `tool_result`, `runtime_output`, or `continuation`.
- `role`: present for messages; `user`, `assistant`, or `system`.
- `text`: readable message or retained output.
- `tool_call_id`: provider call identifier when available.
- `tool_name`: provider tool name when available.
- `details`: structured tool input or result when available.
- `stream`: `stdout` or `stderr` for runtime fallback entries.
- `status`: tool or continuation status when available.
- `created_at`: source event timestamp.

Fields that do not apply to an entry kind are omitted. The API returns entries in ascending event order.

## Transcript Projection

The projector receives the task and its ordered events and performs one deterministic pass.

1. Emit the task goal as a user message before runtime events.
2. Start continuation zero at the first lifecycle start event.
3. Increment the continuation number at each later lifecycle start event and emit a continuation separator.
4. Emit steering directives as user messages at their event positions.
5. Route each `runtime_output` line through the parser selected for its continuation.
6. Emit normalized messages and tool entries returned by the parser.
7. Emit any unconsumed line as a `runtime_output` fallback entry.

The parser for a continuation is selected from the lifecycle event's adapter field. Recorded runtime configuration and the task runtime profile provide fallback identification for historical events that lack that field. This preserves correct parsing when steering changes the runtime profile between continuations.

Lifecycle noise such as process IDs and command arguments remains in Timeline unless it is needed to label a continuation. Lifecycle failures may produce a concise system message so a failed conversation does not appear to end without explanation.

Stable entry IDs are derived from the source event ID plus an index for events that produce multiple transcript entries. The projector does not use timestamps or random values to construct IDs.

## Provider Parsers

Provider parsing lives behind a small internal interface that accepts one ordered runtime event at a time and may keep state for streamed messages and tool calls.

Initial parsers cover:

- Pi JSON/session output.
- Claude Code stream JSON output.
- Codex JSON output.

Parsers normalize only recognized semantic records:

- Assistant text blocks become assistant messages.
- Tool-use records become tool calls.
- Tool-result records become tool results.
- Provider error records become system messages or failed tool results, depending on their relationship to a call.

Stream fragments are buffered until a provider completion boundary when the format supplies one. The parser must avoid displaying both partial and final copies of the same message. If a stream ends before completion, the buffered text is emitted as a partial message rather than discarded.

JSON decoding failure, an unknown record type, or an incomplete provider record is not fatal. The original retained line is returned as runtime output. This fallback is required for forward compatibility and for plain-text provider output.

## Tool Activity

Tool calls and results remain separate transcript entries and are correlated by provider call ID when one exists. A parser must not guess correlations using tool names alone.

The collapsed UI summary includes:

- Tool name.
- Running, completed, or failed status when known.
- A short result indicator without dumping the payload.

Expanded content renders structured details as formatted JSON and plain details as preformatted text. Large content remains inside the disclosure area and must not resize unrelated transcript rows.

## Redaction And Security

The transcript reads the already-redacted event payloads. It must not inspect runtime profile credentials, auth projections, or process environment values to enrich the display.

The endpoint applies normal project/task authorization and returns no filesystem paths beyond content already present in the retained events. Unknown output is displayed verbatim only after existing runtime redaction.

## Error Handling

- A missing project or task returns the existing API not-found response.
- A malformed event affects only that event and becomes collapsed runtime output when text is available.
- A parser failure does not fail the transcript endpoint; processing continues with later events.
- A transcript with no runtime output still shows the task goal and any lifecycle failure.
- The UI shows the existing API error treatment and keeps Timeline independently usable.

## Testing

Backend tests cover:

- Deterministic projection and stable ordering.
- Original goal, steering directives, and multiple continuation boundaries.
- Provider changes between continuations.
- Representative Pi, Claude Code, and Codex message streams.
- Tool calls and results with and without call IDs.
- Partial streamed messages at end of input.
- Malformed JSON and unknown records falling back without data loss.
- Stdout/stderr preservation and existing redaction behavior.
- Project/task ownership and not-found responses.
- Existing historical event fixtures requiring no migration.

Frontend tests cover:

- Conversation is the default tab and Timeline remains available.
- Messages, continuation separators, and disclosure rows render correctly.
- Tool and runtime rows expand and collapse.
- Polling runs only for active tasks.
- Poll updates preserve expansion state by stable entry ID.
- Empty, loading, failed, and completed transcript states.

## Rollout And Compatibility

No schema migration or backfill job is required. Existing tasks are projected when opened. New tasks use the same endpoint and benefit from parser coverage immediately.

The Timeline endpoint and UI remain available as the diagnostic source view. Unknown future provider records continue to appear through runtime-output fallback until a parser is added.

## Non-Goals

- Persisting a second canonical transcript table.
- Replacing task events or the Timeline.
- Recovering runtime output that was not retained historically.
- Rendering provider-specific transcript components in React.
- Transcript search, filters, export, or editing.
- Changing task execution, resume, steering, or credential injection behavior.
