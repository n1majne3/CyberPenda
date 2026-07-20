# Use task-scoped persistent runtimes across runners

Built-in Codex, Claude Code, and Pi runtimes use a Task-scoped persistent process or native session on both Sandbox and Host runners whenever their native session bridge is available. This keeps model and reasoning-effort changes at the native turn boundary and gives both runners the same conversation behavior; a selection or configuration change that requires new projection still closes and restarts the runtime. Runtime lifetime remains operator-controlled: the runtime cannot complete its own Task through a Project Interface, and a live Runtime Activity Indicator stays visible until the operator invokes Task Finish or Stop. Task Finish marks the Task completed after Runtime shutdown and required Continuation reconciliation; Stop remains an interruptible stopped state. Existing Blackboard Finish remains a Continuation-level semantic close, not a Task completion signal.

## Consequences

- Host persistence requires explicit process-tree cleanup and daemon-restart recovery in addition to the existing Sandbox cleanup path.
- A persistent runtime may remain active across many turns, so the UI and runtime control plane must expose Task lifecycle separately from Runtime liveness (`live`, `offline`, `orphaned`, or `unknown`) and live turn activity (`busy` or `idle`), alongside Task Finish and Stop.
- Task Finish is enabled only for a live idle Runtime; Stop is the interruption path for a busy Runtime.
- Runtime liveness must come from current daemon-owned process or session health rather than durable Task status, stored native session identity, event history, or elapsed time.
- An active Task with a confirmed offline Runtime becomes failed, an orphaned Runtime makes it interrupted, and unknown liveness warns without changing Task lifecycle.
- A new user message resumes a completed, failed, interrupted, or stopped Task by preferring provider-native session recovery and otherwise creating a fresh Runtime Continuation from Task-owned state; Task Finish therefore releases Runtime resources without sealing the Task Conversation, while an orphaned Runtime must be cleaned up or proven absent first.
- Plugins without a usable native session bridge may continue to use one-shot execution as a fallback.
