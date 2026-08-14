# Use ACP for the Hermes persistent Runtime

Hermes chat has no Codex/Claude-style JSONL stream. Multi-turn without a live process is `hermes --resume` against `state.db`. The Harness therefore drives a Task-scoped persistent `hermes acp` process on Sandbox and Host, not one-shot `hermes chat -q` and not `hermes serve`. ACP supplies send, stream, and cancel. Hermes Launch Selection stays closed until Conclude Runtime Turn also works on that same ACP process.

## Consequences

- A JSON plugin manifest is not enough; Hermes needs an ACP session factory and an assisted adapter.
- Isolated `HERMES_HOME` still holds ACP conversation state; host `~/.hermes` is not used.
- If ACP cannot apply a Runtime Turn Selection live, the Harness restarts the ACP process.
- `--yolo` remains the non-interactive approval path; ACP permission UI is out of the first plugin.
