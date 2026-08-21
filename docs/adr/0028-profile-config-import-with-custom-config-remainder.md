# Edit runtime config through a structured-fields-first import with a custom config remainder

cc-switch-style direct config editing conflicts with CyberPenda's invariant that
structured Runtime Profile fields are the single source of truth and generated
config is derived. We keep the invariant: the profile editor exposes the
projected config for editing, **Profile Config Import** parses the edited text
back — mappable keys sync into structured fields, the remainder is stored as the
per-profile **Custom Config File**, and **Config Projection** deep-merges that
remainder over the generated config at launch. Structured fields win conflicts
(detected at import time by rejection, and at projection time by override);
**Managed Config Key** lists are declared per Runtime Plugin Manifest, not
hardcoded; secrets never enter the edited text or the remainder. Object keys
merge recursively, scalars and arrays replace whole.

## Considered Options

- Strict structured-only import (reject every unmappable key): rejected — it
  permanently blocks provider-native settings like Codex `[features]` or
  non-official-registry Claude plugins (`warp@claude-code-warp`).
- Making the raw config the source of truth (true cc-switch model, editing the
  host's real `~/.claude/settings.json`): rejected — it breaks the
  structured-fields invariant and the never-write-host-config boundary.

## Consequences

- Overrides the old convention in `internal/runtimeprofile` that generated
  config is "derived, never edited directly": editing is now an explicit,
  validated import path, not an opaque override.
- Import requires TOML and YAML parsing (pure-Go, no cgo: BurntSushi/toml,
  yaml.v3); projection gains a merge step for all four providers.
- Provider switches clear the Custom Config File only after operator
  confirmation.
