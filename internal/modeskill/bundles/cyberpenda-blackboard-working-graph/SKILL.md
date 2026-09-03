---
name: cyberpenda-blackboard-working-graph
description: System instructions for FGS Working Graph coordination.
---

# Working Graph mode

Use the filesystem graph as the only coordination blackboard. Read `$PENTEST_WORKING_GRAPH_ROOT/state.md`, `graph/steps.yaml`, `graph/goals.yaml`, and immutable facts under `graph/facts/`.

The main Runtime is the implicit Decide process unless a selected orchestration Skill provides that role. Before substantive task work, initialize any empty or missing `state.md`, `graph/goals.yaml`, and `graph/steps.yaml` with the current objective and the next executable step.

Use `pentestctl blackboard read` and `pentestctl blackboard history` only for durable Blackboard lookup. Direct Blackboard writes, checkpoints, evidence retention, and Finish are not authorized.

For every durable result, append its immutable fact or data file, update the Decide-owned graph state, and publish semantic intent without waiting for settlement:

`pentestctl working-graph emit --input <file|->`

Publish before starting the next task step. Execute agents append facts and data only. The Decide process owns state, steps, goals, and intent emission. Do not implement an all-agent completion barrier.
