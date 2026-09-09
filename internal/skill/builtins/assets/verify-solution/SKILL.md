---
name: verify-solution
description: Verify or reject a CTF Solution through the Blackboard contract. Use when a ctf_challenge Solution needs verified or rejected status.
blackboard_modes: [interactive, working_graph]
---

# Verify Solution

Use this Skill only to settle an existing Solution in a `ctf_challenge` Project. A Solution is invalid in a Pentest Project. Do not invent a flag, an answer, or a verification summary. This Skill does not submit a platform result and does not finish the Task.

## Working Graph

If the projected Mode Skill is `cyberpenda-blackboard-working-graph`:

- Do not call `pentestctl blackboard change`.
- Publish the check result with `pentestctl working-graph emit --input <file>` as a Fact.
- A Fact is not a verified Solution. Derived solved state changes only when a current Solution has `kind` `flag` and `status` `verified`.

## Interactive Blackboard

Write with `pentestctl blackboard change --input <file>`.

Read the Solution first. Use its current version. Do not print, copy, or persist `PENTEST_INTERFACE_TOKEN`.

### Verify

Only a `candidate` Solution can become `verified`.

The transition uses:

- `op`: `transition`
- `status`: `verified`
- `verification_summary`: required, concise, and specific to the check that accepted the value

Do not set `solved`. Solved state is derived.

Do not change `kind` or `value` in the verify transition. After `verified`, `kind`, `value`, and `verification_summary` are immutable. A correction is a new candidate Solution. The live Solution transition statuses are `verified` and `rejected`.

A verified `flag` or `answer` requires a non-empty `value` with no surrounding whitespace.

`verification_summary` names the accepting check. Do not copy the Solution `summary`. Do not put `verification_summary` on an Objective, Attempt, or Finding transition.

Add an outgoing `satisfies` edge from the verified Solution to the Objective it settles, in the same batch when that edge is not already current. Do not create a Goal node. The Blackboard does not copy Task Goals.

### Reject

Transition to `rejected` only when a current Fact already contradicts the Solution and keeps that invalidation meaning. The rejected transition also requires `verification_summary`. A rejected Solution stays rejected. If every verified flag later becomes rejected, derived solved state becomes false. History stays.

## After the write

Read the Solution again. Do not treat a candidate, a Fact, or a missing Receipt as verified. Do not finish the Continuation from this Skill.
