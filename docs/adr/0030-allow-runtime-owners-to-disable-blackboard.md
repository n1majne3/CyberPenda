# Allow Runtime Owners to disable Blackboard

CyberPenda supports an immutable **Blackboard Mode** of `interactive`, `assisted`, or `disabled` for Project Tasks and Non-Project Sessions, while keeping `interactive` as the default. A **Reason Task** cannot use `disabled` because its fixed planning goal requires the complete **Runtime Blackboard Snapshot**. Disabled mode exists for operators who prefer the Runtime to trace its own work in ordinary workdir files instead of paying the context and workflow cost of Blackboard integration.

## Considered options

- Reusing `interactive` was rejected because it still supplies Blackboard context, tools, grants, continuity, and Finish obligations.
- A read-only Blackboard mode was rejected because it still injects the Blackboard context that Disabled is intended to remove.
- A fixed or parsed state-file protocol was rejected because it would create a second semantic memory system. CyberPenda only gives a short startup reminder to use a state file; the Runtime chooses and owns any files.

## Consequences

A disabled Runtime receives no Blackboard Snapshot, Project Interface, grant, or conclusion and reconciliation workflow. Scope, Task Policy, Runtime lifecycle, Transcript, and ordinary attachments remain active. Blackboard-specific Finish blockers do not apply. Reports do not infer conclusions from the Runtime's Transcript or workdir, but an operator may later retain or Reconcile selected output. The server adds no mode-specific Blackboard API error: a disabled Runtime has no Blackboard credential and fails ordinary authorization, while operator-authorized actions remain valid.
