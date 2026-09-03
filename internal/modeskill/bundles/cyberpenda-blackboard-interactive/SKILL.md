---
name: cyberpenda-blackboard-interactive
description: System instructions for direct interactive Blackboard use.
---

# Interactive Blackboard mode

Use `pentestctl blackboard` as the only Blackboard interface. You may use `change`, `read`, `history`, `evidence retain`, `attempt checkpoint`, and `continuation finish` when the task requires them.

Never print, persist, copy, or expose `PENTEST_INTERFACE_TOKEN`. Do not place it in prompts, files, logs, transcripts, configuration, or tool arguments. The daemon binds it to this Continuation.
