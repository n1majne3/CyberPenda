---
status: accepted
date: 2026-09-03
---

# Prune Built-in Skills to a Curated Set

This decision reduces the daemon-seeded Built-in Skill set from 121 bundles
to 10. It extends the pruning pattern of ADR-0001-era retirements and the
earlier `6c0a186` prune, and it records why each removed group went.

## Decision

- The Built-in Skill set is exactly: `tooling-ffuf`, `tooling-httpx`,
  `tooling-katana`, `tooling-naabu`, `tooling-nmap`, `tooling-nuclei`,
  `tooling-sqlmap`, `tooling-subfinder`, `scoreboard-driven-web-challenge`,
  and `ctf-orchestrator`. `TestBuiltinBundlesIncludeRequestedProjects`
  enforces set equality, so any future addition or removal must update the
  test first.
- All removed IDs are added to `retiredPrunedBuiltinIDs`. Daemon startup
  purges their `skills` rows, both opt-out tables, and on-disk bundles for
  every database where they were seeded as `Source.Kind == builtin`. A
  user-imported skill with the same ID is never purged.
- The successor-map machinery (`prunedBuiltinSuccessors`) is removed: no
  removed skill has a successor, and the eight successor-less legacy IDs
  from the earlier prune are retired directly.
- Historical **Runtime Configuration Snapshots** keep the skill IDs they
  captured. Resume paths now skip snapshot skill IDs that no longer resolve
  (`skill.ErrNotFound`) instead of failing, both in snapshot resolution and
  in Preflight, which reports "N captured skill(s) unavailable and skipped"
  in the skills check detail.
- Kept bundles no longer reference tools the Sandbox image does not install
  (`gospider`, `JS-Snooper`, `jsniper`, `wafw00f`); katana and httpx
  guidance now stays inside the preinstalled toolchain.

## Why the removed groups went

- **Vendored zhaoxuya520/reverse-skill cluster (81 bundles, 6.1 MB)**: the
  router mandated startup steps referencing files dropped during vendoring,
  `field-journal/precedent-auth.md` declared all targets pre-authorized
  against the product's Scope discipline, 41 bundles forced Simplified
  Chinese replies, `ctf-sandbox-orchestrator` claimed to be the implicit
  default entrypoint for nearly every security task, and bundled scripts
  wrote `~/.claude/mcp.json` and pulled unvetted `npx` packages.
- **Knowledge-based first-party skills (25 bundles)**: vulnerability-class
  and framework methodology the model already covers after large-scale RL
  on security tasks; the durable value of a Built-in Skill is knowledge the
  model cannot have — sandbox quirks, product integration, operator
  doctrine.
- **SAST skills (3 bundles)**: their toolchain (semgrep, ast-grep,
  tree-sitter, trufflehog, trivy) is not installed in the Sandbox image, so
  the skills advertised unexecutable pipelines.

## Considered options

- Keep all 121 and rely on bulk Global Skill Opt-Out. Rejected: opt-outs do
  not cover future imports, leave the hijack content in the binary, and
  keep the per-task projection cost (~6.4 MB, 480 files).
- Prune only the vendored cluster, keep the 40 first-party bundles.
  Rejected after per-bundle audit: most first-party knowledge skills
  duplicate RL-covered model capability.
- Keep a small long-tail knowledge set (for example
  `vulnerabilities-nosql-injection`). Rejected: model-dependent value and
  routing noise; anything needed later can be re-imported from the Skills
  Page as a normal Skill.

## Consequences

- Per-task skill projection drops from ~6.4 MB / 121 bundles to a few
  hundred KB / 10 bundles; always-visible skill metadata in the runtime
  system prompt drops from ~43.5 KB to ~1.6 KB.
- Users who edited a removed Built-in Skill lose that edit at the next
  daemon start (purge only spares `Source.Kind != builtin` rows).
- A historical Task or Session resume projects only the surviving skills;
  its immutable snapshot is unchanged, and Preflight surfaces the skip.
- Removed content remains recoverable from git history and can be
  re-imported through **Runtime Extension Import** as a user Skill.
- The Sandbox image toolchain (ghidra, jadx, frida, and the rest) is
  unaffected: tools are kept independently of skill bundles.
