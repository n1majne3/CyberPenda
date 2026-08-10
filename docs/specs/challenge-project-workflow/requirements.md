# Challenge Project Workflow Requirements

## Problem

CyberPenda supports `ctf_challenge` in its domain and Blackboard, but the Project creation interface omits the Project kind. The daemon therefore creates a Pentest Project by default. A Runtime can then start challenge work and fail later when it writes a Solution. Challenge execution also depends on several independent Runtime calls for Attempt identity, remote submission, Evidence retention, Blackboard relationships, and Task completion. This creates duplicate Attempts, missing Evidence, and stranded semantic debt.

## Scope

This change connects the existing CTF Challenge Project capability to Project creation, Task Launch, challenge execution, and Task Finish. It also adds a safe conversion path for existing Projects and structured Task Policy enforcement.

## Non-goals

- This change does not remove the semantic distinction between a Pentest Project and a CTF Challenge Project.
- This change does not let a Skill expand Scope.
- This change does not automatically convert a Project Fact into a Solution.
- This change does not automatically perform Task Finish.
- This change does not add an NSSCTF credential or call a live challenge platform in tests.

## User stories and acceptance criteria

### R1. Explicit Project kind

As an operator, I can select the Project kind when I create a Project.

- When an operator creates a Project, the system shall require `pentest` or `ctf_challenge`.
- When the Project list renders, the system shall show the Project kind.
- When a caller omits or supplies an unsupported Project kind, the daemon shall reject the request instead of silently selecting Pentest.

### R2. Safe Project kind conversion

As an operator, I can preview whether an existing Project can change kind.

- When an operator requests a conversion preview, the system shall list every active Task and incompatible current record as a blocker.
- While any Task is non-terminal, the system shall reject Project kind conversion.
- When blockers are absent and the operator confirms, the system shall change the Project kind atomically.
- The system shall not infer Finding-to-Solution or Project-Fact-to-Solution conversions.

### R3. Capability-aware Preflight

As an operator, I receive a launch blocker before incompatible work starts.

- When an enabled Runtime Extension requires a Project kind, Preflight shall compare the requirement with the selected Project.
- When a requirement is not satisfied, Preflight shall fail before Runtime execution and shall return an actionable blocker.
- A Runtime Extension requirement shall not change Project Scope or Project kind.

### R3a. Explicit Task Type

As an operator, I can select the Task Type during Task Launch.

- Task Launch shall expose `pentest` and `ctf_challenge`.
- The daemon shall store the selection as an immutable Task Type Snapshot.
- The selected Task Type shall match the current Project Kind.
- A later Project Kind Conversion shall not rewrite an existing Task Type Snapshot.
- Challenge Workflow operations shall require both a CTF Challenge Project and a CTF Challenge Task.

### R4. Structured Task Policy

As an operator, I can set bounded challenge execution limits in Run Controls.

- When a Task starts, the system shall store an immutable Task Policy Snapshot.
- The Challenge Workflow shall enforce maximum Attempts, wrong submissions, wall time, consecutive failures, Rating drawdown, and no-progress duration when configured.
- When a policy limit is reached, the Runtime Harness shall reject the next governed challenge action and record a Task Event.

### R5. Challenge Workflow

As a Runtime, I can use one small Interface for challenge claim, submit, abandon, and finalization.

- When a challenge is claimed, the Challenge Workflow shall create or reuse exactly one Attempt for `(Project, platform, external_attempt_id)`.
- When a candidate is submitted, the Challenge Workflow shall persist a durable operation before it calls the external platform Adapter.
- When the same operation is replayed, the Challenge Workflow shall return the previous result without creating another Attempt or submission result.
- When a claim, submit, or abandon response is received, the Challenge Workflow shall retain a redacted response Evidence Artifact and link it to the Attempt.
- When an Attempt is finalized, the Challenge Workflow shall close or preserve its related Exploration Objective consistently.

### R6. Evidence correctness

As an operator, I can trust Evidence Artifact metadata.

- When the system retains a response, it shall derive media type, digest, and byte size from the retained payload.
- When supplied metadata conflicts with the retained payload, the system shall reject the retention request.
- Challenge finalization shall report missing required Evidence instead of treating Task Events as durable proof.

### R7. Finish Readiness

As an operator, I can see why a Task cannot finish.

- When Finish Readiness is requested, the system shall inspect all Pending Blackboard Conclusions, open Attempts, unfinalized Challenge Attempts, open Exploration Objectives owned by the Task, valid Finish Intent state, reconciliation state, and required challenge Evidence.
- The projection shall return `ready_to_finish` and stable typed blockers.
- When Task Finish is requested while blockers exist, the system shall reject Task Finish and return the same blockers.
- The Task page shall show all blockers and links to the relevant surfaces.

### R8. Restart recovery

As an operator, I do not lose challenge state after a daemon restart.

- When the daemon restarts after a remote response but before local settlement, the Challenge Workflow shall recover the durable operation without creating a duplicate Attempt.
- When an accepted steering, conclusion dispatch, or challenge operation is pending, recovery shall settle it to `applied`, `failed`, or `action_required`.

## Confirmed test seams

The user confirmed implementation of all proposed mechanisms. Tests use these public seams:

1. Project create and Project kind conversion HTTP interfaces.
2. Task Launch Preflight and Run Controls HTTP interfaces.
3. Challenge Workflow Interface with a production HTTP Adapter and an in-memory test Adapter.
4. Finish Readiness and Task Finish HTTP interfaces.
