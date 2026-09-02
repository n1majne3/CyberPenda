---
status: accepted
---

# Resolve launches with owner-local runtime configuration

Runtime Plugin, Model Provider, and model selection will resolve directly into an immutable Runtime Owner-local **Runtime Configuration Snapshot**. This decision supersedes only the Launch UX and Project default Profile parts of ADR-0002; ADR-0002 still governs the separation of Model Providers from Runtime Profiles. Automatic Launch Profile Resolution created global state for an ordinary launch choice, produced duplicate and failed-launch Profiles, and made advanced Profile configuration depend on implicit matching.

## Decision

- **Preflight** resolves direct **Launch Selection** without persistent writes. After it succeeds, the Runtime Owner or Continuation and its Snapshot are stored atomically.
- Task, Reason Task, Session, Resume, Steering, and Runtime Turn model-selection paths never find, create, or reuse a Runtime Profile automatically.
- A **Runtime Profile** is only a user-created reusable advanced configuration. It has no `manual` or `launch_resolve` Kind.
- A Runtime Profile is optional and must be selected explicitly when a Task or Session is created. Project Defaults cannot select one, and an existing Runtime Owner cannot switch one.
- A direct launch uses Runtime Plugin standard configuration, global default Skills after Global Skill Opt-Outs, and explicit Run Controls. Profile-specific MCP, Custom Config, Extensions, and Profile Skill Opt-Out apply only when the launch explicitly selects that Profile.
- Runtime Turn Selection may still change Model Provider, model, and Reasoning Effort. These changes create only Runtime Owner-local state when new Config Projection is required.
- Resume uses the latest immutable Snapshot. Later global-default or Runtime Profile edits do not change it.
- Direct Runtime Owners display Runtime Plugin, Model Provider, and model instead of a synthetic Profile name.
- **Save as Runtime Profile** is an explicit, named, operator-confirmed action. CyberPenda never suggests or performs it automatically.

## Migration

The observed environment had 90 automatic Profiles and four user-created Profiles. Seventy-six automatic Profiles had historical Task or Session references, 59 were still the latest configuration for an Owner, and 22 carried non-minimal fields. Four contained Custom Arguments and Sandbox image settings that were not reliably captured elsewhere.

The migration must first copy every complete non-secret legacy Profile field into each referencing self-contained Snapshot. It must then verify historical reads, Resume, and replacement Continuation behavior. Only after those checks pass may it remove all automatic Profiles. It will not retain hidden Profile tombstones. User-created Runtime Profiles remain.

## Considered options

- Keep automatic Profiles but hide them. This preserves the unwanted global identity and implicit matching behavior.
- Require a Runtime Profile for every launch. This makes a reusable advanced configuration mandatory for an ordinary model choice.
- Apply a Project default or other implicit Profile. This hides the source of advanced configuration and conflicts with explicit Profile selection.

## Consequences

Preflight and Config Projection must accept direct Launch Selection as well as an explicitly selected Runtime Profile. Runtime Profile identity becomes optional provenance on Runtime Owner configuration. Project default Profile fields and automatic Profile kinds disappear. Existing data cannot be deleted before the complete Snapshot migration and recovery checks succeed.
