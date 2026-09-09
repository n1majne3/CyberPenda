---
name: verify-finding
description: Confirm or reject a Finding through the Blackboard contract. Use when a Pentest Project Finding needs confirmed or false_positive status.
blackboard_modes: [interactive, working_graph]
---

# Verify Finding

Use this Skill only to settle an existing Finding. Do not invent a Finding, a target, or proof. Scope, authorization, and Runner choice stay in the product. This Skill does not grant them.

## Working Graph

If the projected Mode Skill is `cyberpenda-blackboard-working-graph`:

- Do not call `pentestctl blackboard change`, `pentestctl blackboard evidence retain`, or `pentestctl blackboard continuation finish`.
- Publish the check result with `pentestctl working-graph emit --input <file>`.
- Use `fact.append` for what was observed. A Fact is not a confirmed Finding.
- Do not write confirmed Finding status into the working graph.

## Interactive Blackboard

If the projected Mode Skill is `cyberpenda-blackboard-interactive`, write one atomic batch:

`pentestctl blackboard change --input <file>`

The file uses `schema` `semantic-change-batch/v2`, one `idempotency_key`, and ordered `changes`.

Read the Finding first with `pentestctl blackboard read --key <key>`. Use that current version. Do not print, copy, or persist `PENTEST_INTERFACE_TOKEN`.

### Confirm

Confirm only when the same batch has both of these:

1. A transition of the current Finding version to `confirmed`.
2. An active `evidences` edge from Evidence, or a `supports` edge from a ProjectFact whose confidence is `confirmed`.

A tentative Fact is not support. If either requirement is missing, leave the Finding `unconfirmed`.

A confirmed Finding must already have, or the same batch must set:

- `target`
- `proof` (compact text; raw proof stays in Evidence)
- `impact`
- `recommendation`
- `cvss_version` (`4.0` for a new vector)
- `cvss_vector` (a complete vector for that version)

Do not set `severity` or `cvss_pending`. The service derives them.

Do not confirm from a hypothesis, a missing command, or an untested plan.

### Reject

Transition to `false_positive` only when the check disproves the Finding. `false_positive` is terminal. Do not reopen that Finding. A later question is a new Finding.

## After the write

Read the Finding again. Stop if the status is not `confirmed` or `false_positive`. Do not finish the Task from this Skill. Finding confirmation, Goal completion, and Task Finish are separate outcomes.
