# Verified baseline

The generated skill completed a clean Windows 11 run on 2026-10-21. It used the actual daemon and Vite source UI, with agent-browser 0.27.0 and empty application data. No mock API or Runtime was used.

Proved [Projects and Scope](features/projects-scope.md):

- All five creation entry points and cancellation.
- Explicit Project Kind validation and creation.
- Project card and sidebar dashboard navigation.
- Add asset, Scope navigation, and direct Scope access.
- Scope save, reload, visible values, and a read-only API persistence assertion.

The final run completed 74 browser commands with exit code 0 and no browser errors. The browser and app were stopped, both ports closed, and scratch data removed. Evidence survived cleanup.

Local evidence, intentionally outside the skill bundle:

`runs/verify-cyberpenda-20261021-06/evidence/`

Start with `report.md`, `project-persisted.json`, `scope-persisted.png`, `scope-notes-persisted.png`, and `post-cleanup.json`. The report distinguishes the unique daemon build identity from Git HEAD observed at completion; this run did not pin a full source snapshot.

The other four feature recipes are source-grounded, not runtime-proven. Real Task and Session launches need separately approved Model Provider credentials and Runner prerequisites. Embedded release UI, other operating systems, and Hosted evaluation remain unverified. Use a new run directory to verify current code; this historical proof is not a substitute for a new run.

## Real-model Steer run

A second Windows run on 2026-10-21 launched real Sandbox Runtimes against an approved local model gateway. Evidence: `runs/verify-steer-20261021-02/evidence/`; start with `report.md`.

- Pi and Claude Code both reached real model execution. The earlier Windows Skill symlink launch blocker no longer reproduces.
- Claude Code `interrupt_then_replace` settled and replied in three iterations, with a working probe and surviving Runtime identity.
- Pi `in_turn_steer` settled `applied` once and `action_required` (`target_turn_changed`) three times, although the model answered each Steer. The recorded cause is a turn-identity defect in the Sandbox provider bridge, not a model or timing failure.
- One model service, one Sandbox image revision, Disabled Blackboard Mode, and globally disabled Skills. FGS, enabled Skills, and other Runtimes are unverified.
