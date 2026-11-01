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

code=false
while IFS= read -r file; do
  [ -z "$file" ] && continue
  if [ "$file" = "FORCE_CODE" ]; then
    code=true
    break
  fi
  case "$file" in
    *.md|docs|docs/*|LICENSE|LICENSE.*|CHANGELOG|CHANGELOG.*|AUTHORS|AUTHORS.*)
      ;;
    *)
      code=true
      break
      ;;
  esac
done < <(changed_files "$@")

printf 'code=%s\n' "$code"
