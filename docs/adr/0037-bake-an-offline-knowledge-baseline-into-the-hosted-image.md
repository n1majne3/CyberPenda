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

The TSecBench Hosted Image and the Sandbox image now carry the same offline
knowledge baseline under `/opt/knowledge`, built by the shared installer
`docker/knowledge-baseline/install.sh` so both images hold identical
reference data:

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
  `SECLISTS_SHA` defaults in the shared installer, overridable from the build
  environment), fetched with `--depth 1 --filter=blob:none`, checked out
  detached, and stripped of `.git` so the layer stays small and
  reproducible. Bumping a SHA in the installer is the only way upstream
  content changes, and it changes both images together.
- The full seclists package and full-size dictionaries stay excluded from
  the Hosted Image. The image contract test now guards the curated subset
  and keeps `rockyou`-scale content on the exclusion list for both the
  Dockerfile and the installer. The Sandbox image keeps its full seclists
  apt package alongside the curated `/opt/knowledge` subset.
- A new `pentest-knowledge-lookup` command searches the baseline by keyword
  (fixed-string, case-insensitive; path-name matches before content
  matches) and prints up to 20 reference files with a read-in-full hint.
  The command is installed into both images and is part of the Hosted Image
  build-time tool verification and image smoke test.

Usage rules are written into the Runtime-facing instructions: the
`ctf-orchestrator` Skill environment note, the Execute dispatch template, the
projected `execute` agent type, and the `tsecbench-hosted-challenge-loop`
Skill all present the baseline as an on-demand resource. Challenge work
follows its own judgment first and consults the baseline when a pass stalls
(hypotheses exhausted, repeated verification failures), then reads matched
reference files completely. A mandatory lookup-first step is deliberately
avoided so the baseline does not constrain the model's own approach. The
baked wordlists stay available for brute force and discovery.

## Consequences

The Hosted Tool Baseline definition in CONTEXT.md now includes curated
offline reference data, and the Sandbox image carries the same reference
data through the shared installer. The delivery bundle grows by the size of
the baked references; the 3 GB archive limit still applies and is enforced
by the bundle build, so future additions to the baseline must be weighed
against the remaining headroom. ADR 0026 otherwise still stands: no Ghidra,
no Android SDK, no full Kali suite, no runtime package downloads.

Upstream content only changes when a pin SHA is bumped deliberately, so
evaluation runs stay reproducible and the smoke test pins marker files for
each knowledge root. The Runtime-facing instructions spend a few extra lines
per dispatch on the on-demand lookup note.
