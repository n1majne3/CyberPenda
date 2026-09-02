---
status: accepted
date: 2026-09-02
---

# Add Global Skill Opt-Out

This decision extends ADR-0001. Skills remain default-on, but Profile Skill Opt-Out alone cannot stop one Skill from entering direct launches or every current and future Runtime Profile.

## Decision

- A **Global Skill Opt-Out** disables one Skill by stable **Skill ID** for direct launches and every current or future **Runtime Profile**.
- A Global Skill Opt-Out overrides effective Profile enablement. It does not delete or rewrite existing **Profile Skill Opt-Outs**.
- Removing a Global Skill Opt-Out restores each Runtime Profile's existing Profile Skill Opt-Out choice.
- Updating or re-importing the same live Skill ID preserves both Global and Profile Skill Opt-Outs.
- **Skill Deletion** removes both kinds of opt-out. Recreating the same Skill ID starts with **Default Skill Enablement**.
- Started Runtime Owners keep their captured **Runtime Configuration Snapshot** and **Task Skills Root**. The change applies only to later launch resolution.
- The **Skills Page** exposes separate Global and Profile controls. A Profile control is inactive while the Global Skill Opt-Out is active because it cannot change effective enablement.
- The **Skills Page** exposes separate Global and Profile bulk controls. Disable all globally atomically creates a Global Skill Opt-Out for every current Skill, while Enable all globally removes every Global Skill Opt-Out. Later imports still follow Default Skill Enablement.

## Considered options

- Require an opt-out on every Runtime Profile. This does not cover direct launches or future Profiles and requires repeated work.
- Convert Skills to default-off. This removes the shared baseline behavior chosen in ADR-0001.
- Delete a Skill to disable it globally. This loses the managed bundle and source provenance and makes later re-enablement an import operation.

## Consequences

- Enabled Skill resolution is the live Skill library minus Global Skill Opt-Outs and then minus the selected Profile Skill Opt-Outs.
- Preflight, direct launch, Task launch, and Session launch use the same effective resolution.
- Skill list responses must distinguish effective enablement, Global Skill Opt-Out state, and Profile Skill Opt-Out state so the UI can show both controls without losing either choice.
