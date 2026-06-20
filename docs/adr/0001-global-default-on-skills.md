# Global Skills are default-on with per-profile opt-out

Skills are global, runtime-agnostic bundles managed from the Skills page and projected into task-local runtime boundaries. We chose default-on Skill enablement for all current and future Runtime Profiles, with per-profile opt-out, because Skills are intended to be shared baseline agent capabilities rather than provider-specific plugins; provider-specific extensions remain explicit and runtime-owned.

## Considered Options

- Explicit opt-in per Runtime Profile: safer by default, but makes shared baseline Skills easy to forget and weakens the purpose of a global Skills library.
- Default-on with per-profile opt-out: chosen because it keeps Skills broadly available while preserving profile-level control.

## Consequences

- Skill import, edit, deletion, enablement changes, and opt-outs must be visible in audit and preflight surfaces.
- Skill publication must be validated and atomic because a bad live Skill can affect future tasks across profiles.
- Started tasks keep their already-projected skills; new tasks use the current live Skills unless their profile opts out.
