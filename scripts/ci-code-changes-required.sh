#!/usr/bin/env bash
# Decide whether a push/PR changes code that must run CI (not docs-only).
# Usage:
#   scripts/ci-code-changes-required.sh <base_sha> <head_sha>
#   scripts/ci-code-changes-required.sh --stdin   # newline-separated paths on stdin
# Prints: code=true|false
set -euo pipefail

changed_files() {
  if [ "${1:-}" = "--stdin" ]; then
    cat
    return
  fi

  if [ "$#" -ne 2 ]; then
    echo "usage: $0 <base_sha> <head_sha> | $0 --stdin" >&2
    exit 2
  fi

  local base="$1"
  local head="$2"
  if [ -z "$base" ] || [[ "$base" =~ ^0+$ ]]; then
    # New branch / unknown base: always run.
    echo "FORCE_CODE"
    return
  fi

  # Fail safe: if the diff cannot be computed (e.g. force-pushed base missing),
  # run everything instead of silently treating the change as docs-only.
  git diff --name-only "$base...$head" 2>/dev/null \
    || git diff --name-only "$base" "$head" 2>/dev/null \
    || echo "FORCE_CODE"
}

# Allowlist of docs-only paths. Markdown is NOT docs by default: embedded skills
# (internal/**), docs read by name in tests, and docs copied into build outputs
# are code. Every Markdown file is still scanned by repo-wide contract tests, and
# README.md carries two content assertions; the docs-contracts CI job runs those
# tests (./scripts and ./internal/blackboardv2) for every docs-only change.
is_docs_only() {
  case "$1" in
    # Read by name in internal/blackboardv2/replacement_proof_test.go.
    docs/specs/blackboard-*.md|docs/adr/0014-*.md) return 1 ;;
    # Copied into the hosted bundle by scripts/build-tsecbench-hosted-bundle.sh.
    docs/tsecbench/*) return 1 ;;
    README.md|README.*.md|GLOSSARY.md|AGENTS.md|LICENSE|LICENSE.*|docs/*) return 0 ;;
  esac
  return 1
}

code=false
while IFS= read -r file; do
  [ -z "$file" ] && continue
  if [ "$file" = "FORCE_CODE" ]; then
    code=true
    break
  fi
  if is_docs_only "$file"; then
    continue
  fi
  code=true
  break
done < <(changed_files "$@")

printf 'code=%s\n' "$code"
