# Bake an offline knowledge baseline into the Hosted Image

## Status

Accepted.

## Context

ADR 0026 bounded the Hosted Tool Baseline to keep the delivery archive below
3 GB and excluded Ghidra, Android SDK, large dictionaries, and the full Kali
tool set. In practice this excluded every form of offline reference data: the
image ships ffuf, gobuster, hydra, and john but no wordlists, and ships no
methodology or payload references at all.

Challenge work on the Hosted Evaluation Run has no public Internet access
(`PI_OFFLINE=1`, isolated network). Without baked reference data, a Runtime
that meets an unfamiliar vulnerability class can only rely on model memory,
and brute-force or directory-discovery steps cannot run at all without
dictionaries.

## Decision

The TSecBench Hosted Image now carries an offline knowledge baseline under
`/opt/knowledge`, as part of the Hosted Tool Baseline:

- `/opt/knowledge/hacktricks` — the HackTricks methodology markdown sources
  (`src/`, without image, banner, and binary attachment directories).
- `/opt/knowledge/payloads-all-the-things` — the PayloadsAllTheThings
  technique and payload references.
- `/opt/knowledge/wordlists/{web,passwords,usernames}` — a curated sparse
  subset of the SecLists sources: common and raft web discovery lists, the
  10k-most-common and 100k-most-used-NCSC password lists, and the
  top-usernames-shortlist plus cirt-default-usernames lists.

Build rules:

- Each upstream is pinned by a full commit SHA (`HACKTRICKS_SHA`, `PATT_SHA`,
  `SECLISTS_SHA` build arguments), fetched with `--depth 1
  --filter=blob:none`, checked out detached, and stripped of `.git` so the
  layer stays small and reproducible. Bumping a SHA is the only way upstream
  content changes.
- The full seclists package and full-size dictionaries stay excluded. The
  image contract test now guards the curated subset and keeps
  `rockyou`-scale content on the exclusion list.
- A new `pentest-knowledge-lookup` command searches the baseline by keyword
  (fixed-string, case-insensitive; content matches plus path-name matches)
  and prints up to 20 reference files with a read-in-full hint. The command
  is part of the build-time tool verification and the image smoke test.

Usage rules are written into the Runtime-facing instructions: the
`ctf-orchestrator` Skill environment note, the Execute dispatch template, the
projected `execute` agent type, and the `tsecbench-hosted-challenge-loop`
Skill all direct challenge work to look up techniques first, read matched
reference files completely, and use the baked wordlists instead of generating
dictionaries during a pass.

## Consequences

The Hosted Tool Baseline definition in CONTEXT.md now includes curated
offline reference data. The delivery bundle grows by the size of the baked
references; the 3 GB archive limit still applies and is enforced by the
bundle build, so future additions to the baseline must be weighed against
the remaining headroom. ADR 0026 otherwise still stands: no Ghidra, no
Android SDK, no full Kali suite, no runtime package downloads.

Upstream content only changes when a pin SHA is bumped deliberately, so
evaluation runs stay reproducible and the smoke test pins marker files for
each knowledge root. The Runtime-facing instructions spend a few extra lines
per dispatch on the lookup discipline.
