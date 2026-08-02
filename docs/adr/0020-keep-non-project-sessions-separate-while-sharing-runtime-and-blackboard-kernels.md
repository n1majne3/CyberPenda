# Keep Non-Project Sessions separate while sharing Runtime and Blackboard kernels

CyberPenda models a **Non-Project Session** as a durable aggregate separate from **Task**, never as a Task with an absent Project reference. A Session owns its Conversation, Events, Blackboard, Workdir, attachments, lifecycle, and Runtime state, while the Runtime Harness and Blackboard v2 semantic protocol are reached through explicit owner capabilities shared by Task and Session. Runtime lifecycle/control is one owner-neutral harness; Blackboard persistence uses isolated owner-specific tables so a Session can never read or write Project records while retaining the same v2 change, snapshot, history, and relationship contract.

## Considered options

- Making Project optional on Task was rejected because Project Scope, shared Blackboard, Findings, Evidence, reporting, and Trusted Origin are defining Task relationships; optional ownership would spread conditional Project semantics across every Task consumer.
- Building a separate Session Runtime control stack was rejected because provider controls, persistent Runtime recovery, Runtime Turn Selection, permissions, and assisted conclusions must evolve consistently for both owners. Isolated Session Blackboard persistence is required, but it is exposed through the same owner-capability adapter and semantic protocol.
- Treating Non-Project Mode as a hidden or synthetic Project was rejected because a Session has no Project Scope, Project knowledge sharing, or Project artifacts and must not imply those semantics.

## Consequences

- Session persistence and public interfaces use Session identity and cannot alias Project or Task identity.
- Shared Runtime control and the Blackboard owner adapter accept an explicit owner contract with bounded capabilities instead of an optional Project followed by scattered conditionals.
- A Session Blackboard admits only Entities, Exploration Objectives, Attempts, Session Facts, and their supported relationships; Project-only records and operations are rejected by the shared semantic boundary.
- Session state never synchronizes with a Project or another Session. Any future transfer into a Project must be an explicit, Scope-aware copy rather than conversion or shared ownership.
- Non-Project Mode may use the same Runtime Profiles, providers, Runners, multi-turn controls, and assisted-conclusion behavior as Tasks without changing Project authorization semantics.
