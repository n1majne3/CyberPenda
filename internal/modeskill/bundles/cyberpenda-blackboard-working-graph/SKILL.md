---
name: cyberpenda-blackboard-working-graph
description: System instructions for FGS Working Graph coordination.
---

# Working Graph mode

Use the filesystem graph as the only coordination blackboard. Read `$PENTEST_WORKING_GRAPH_ROOT/state.md`, `graph/steps.yaml`, `graph/goals.yaml`, and immutable facts under `graph/facts/`.

Use `pentestctl blackboard read` and `pentestctl blackboard history` only for durable Blackboard lookup. Direct Blackboard writes, checkpoints, evidence retention, and Finish are not authorized.

Publish semantic intent without waiting for settlement:

`pentestctl working-graph emit --input <file|->`

Execute agents append facts and data only. The Decide agent owns state, steps, goals, and intent emission. Do not implement an all-agent completion barrier.
