---
status: accepted
date: 2026-09-09
---

# Add verification Skills to the curated Built-in set

ADR 0033 froze the daemon-seeded Built-in Skill set at ten bundles. This
decision adds two product-protocol Skills. It does not restore pruned
methodology Skills.

## Decision

- Add `verify-finding` and `verify-solution` to the curated Built-in Skill
  set. `TestBuiltinBundlesIncludeRequestedProjects` remains the equality
  gate, now twelve bundles.
- These Skills state only Blackboard settlement rules the Runtime cannot
  infer from model pretraining: required fields, derived severity, support
  edges, immutable verified Solution fields, and the Working Graph write
  ban.
- They do not grant Scope, authorization, Host Runner activation, or a
  platform submit. Working Graph publication of a Fact is not confirmation
  or verification.

## Why these two

- Finding confirmation and Solution verification are separate product
  outcomes. One Skill would hide the Pentest and `ctf_challenge` split.
- Interactive Blackboard writes use `pentestctl blackboard change`. Working
  Graph Mode does not authorize that write. The Skills have to say both, or
  a Runtime will call the wrong interface.
- Methodology for how to test a target stays out. ADR 0033 already rejected
  that class of Built-in Skill.

## Consequences

- Daemon startup seeds the two new Built-in Skills. A user-imported Skill
  with the same ID is not purged.
- Historical Runtime Configuration Snapshots do not gain these IDs. New
  launches can enable them through the existing Skill enablement path.
- A later removal must add the IDs to `retiredPrunedBuiltinIDs` and update
  the equality test in the same change.
