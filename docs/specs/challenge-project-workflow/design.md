# Challenge Project Workflow Design

## Design summary

The change keeps Project kind as a strong semantic invariant. It connects the existing CTF Challenge Project implementation to the creation interface and adds a deep Challenge Workflow module. Callers use four operations. The module owns identity, Task Policy, external Adapter calls, Evidence retention, Blackboard settlement, and recovery.

## Modules and seams

### Project Management module

Interface:

- create a Project with an explicit kind;
- preview a kind conversion;
- confirm a blocker-free conversion.

The existing SQLite implementation remains behind this interface. Conversion is allowed only when the Project has no non-terminal Task and no incompatible current Finding or Solution.

### Preflight module

Preflight reads Runtime Extension requirements and the immutable launch configuration. It returns typed blockers. It does not modify Project kind or Scope.

Runtime Extension metadata can declare:

```yaml
requirements:
  project_kinds:
    - ctf_challenge
  scope_capabilities:
    - challenge_platform
```

Unknown requirement keys fail validation.

### Challenge Workflow module

External Interface:

```go
type Workflow interface {
    Claim(context.Context, ClaimRequest) (AttemptResult, error)
    Submit(context.Context, SubmitRequest) (AttemptResult, error)
    Abandon(context.Context, AbandonRequest) (AttemptResult, error)
    Finalize(context.Context, FinalizeRequest) (Finalization, error)
}
```

The external challenge platform is a true external dependency. A `PlatformAdapter` port sits at the internal seam. Production uses an HTTP Adapter. Tests use an in-memory Adapter.

Production loads strict Platform Adapter JSON through `--challenge-platform-config` or `PENTEST_CHALLENGE_PLATFORM_CONFIG`. The file names a bearer-token environment variable. It does not contain the token value. See `docs/examples/challenge-platforms.example.json`.

### Finish Readiness module

Interface:

```go
type FinishReadiness interface {
    Project(context.Context, taskID string) (Readiness, error)
    Require(context.Context, taskID string) error
}
```

`Project` is read-only. `Require` returns the same typed blockers and is called by Task Finish. The Task lifecycle, Runtime Activity Indicator, reconciliation state, and Finish Readiness remain separate projections.

## Data model

### Project kind

The existing `projects.kind` field remains canonical. Project creation requires an explicit value. A conversion changes only the kind after compatibility checks.

### Task Policy Snapshot

Store the Task Policy Snapshot inside the existing immutable `run_controls_json` Task creation record. It stores nullable limits:

- `max_attempts`;
- `max_wrong_submissions`;
- `max_wall_time_seconds`;
- `max_consecutive_failures`;
- `max_rating_drawdown`;
- `max_no_progress_seconds`.

The Snapshot is immutable after Task Launch.

### Task Type Snapshot

Store `pentest` or `ctf_challenge` on the Task row. Task Launch shows both values and initializes the selection from the current Project Kind. The daemon rejects a mismatch. Existing Tasks receive their Project Kind during migration, and later Project Kind Conversion does not rewrite the stored Task Type.

### Challenge Attempt identity

Add a durable challenge operation store with:

- Project ID;
- Task ID;
- platform;
- external challenge ID;
- external Attempt ID;
- operation ID;
- operation kind;
- request hash;
- state;
- redacted response payload reference;
- timestamps.

The database enforces uniqueness for `(project_id, platform, external_attempt_id)` and `(task_id, operation_id)`.

## Consistency and recovery

An external request cannot participate in the SQLite transaction. The module therefore uses durable at-least-once execution with operation idempotency:

1. Persist the operation as `pending`.
2. Call the platform Adapter with the operation ID.
3. Store the redacted response and move the operation to `recording`.
4. Idempotently retain Evidence and settle Blackboard state.
5. Atomically settle local counters and move the operation to `completed`.
6. On restart, call the Platform Adapter again with the same operation ID and resume the incomplete local phase.

## Scope

CTF Challenge Projects use normal Scope. A challenge platform can be represented as a scoped URL plus a testing-limit entry. A future delegated dynamic Scope rule can authorize platform-issued ephemeral targets, but this change does not let an Adapter or Skill expand Scope.

## UI design

- Purpose: expose Project kind at creation and surface launch or finish blockers before work is performed.
- Direction: existing industrial and utilitarian CyberPenda design.
- Palette: `#FFFFFF`, `#171717`, `#E5E5E5`, `#38828A`, and `#DC9209` through existing design tokens.
- Typography: Geist and Geist Mono.
- Layout: retain the current left-aligned Project form and Runtime Owner Workspace. Add compact badges, blocker lists, and a destructive confirmation area. No visual-system redesign.

## Test strategy

TDD proceeds as vertical slices through the confirmed seams. HTTP tests use the real daemon router and SQLite test store. Challenge Workflow tests use a real SQLite test store, managed temporary Artifact Root, and in-memory platform Adapter. Frontend tests exercise visible form and blocker behavior. Browser verification covers Project creation and Task finish blockers.
