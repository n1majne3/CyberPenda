# Implementation Plan

- [x] 1. Make Project kind explicit
  - Add failing daemon and React tests.
  - Require Project kind during creation.
  - Display kind in Project list and Dashboard.
  - _Requirements: R1_

- [x] 2. Add safe Project kind conversion
  - Add preview and confirm HTTP interfaces.
  - Block conversion for non-terminal Tasks and incompatible records.
  - Add the operator confirmation UI.
  - _Requirements: R2_

- [x] 3. Add capability-aware Preflight
  - Add Runtime Extension project-kind requirements.
  - Validate requirement metadata.
  - Return typed Preflight blockers before Runtime execution.
  - _Requirements: R3_

- [x] 4. Add Task Policy Snapshot and enforcement
  - Add storage migration and domain validation.
  - Add Run Controls fields.
  - Enforce configured limits in Challenge Workflow.
  - _Requirements: R4_

- [x] 5. Add Challenge Workflow
  - Add the four-operation Interface.
  - Add in-memory and HTTP platform Adapters.
  - Add durable operations and unique Attempt identity.
  - Add restart recovery behavior.
  - _Requirements: R5, R8_

- [x] 6. Add automatic challenge Evidence
  - Retain redacted submit and abandon responses.
  - Derive and validate media type, digest, and byte size.
  - Link Evidence and final Attempt outcome.
  - _Requirements: R5, R6_

- [x] 7. Add Finish Readiness
  - Add the read projection and typed blockers.
  - Enforce the projection in Task Finish.
  - Add the Task page blocker view.
  - _Requirements: R7_

- [x] 8. Update domain documentation and verify
  - Update `CONTEXT.md` with the resolved Project-kind and Challenge Workflow language.
  - Run focused and complete Go and Web tests.
  - Run lint, type check, production build, embedded UI regeneration, and focused interface tests.
  - _Requirements: R1-R8_
