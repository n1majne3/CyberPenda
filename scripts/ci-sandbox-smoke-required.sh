#!/usr/bin/env bash
set -euo pipefail

changed_files() {
  if [ "${1:-}" = "--stdin" ]; then
    cat
    return
  fi

  local base="${1:-}"
  local head="${2:-}"
  if [ -z "$base" ] || [ -z "$head" ] || [[ "$base" =~ ^0+$ ]]; then
    echo ".github/workflows/ci.yml"
    return
  fi

  git diff --name-only "$base...$head" 2>/dev/null || git diff --name-only "$base" "$head"
}

required=false
while IFS= read -r path; do
  case "$path" in
    .github/workflows/ci.yml|\
    Makefile|\
    cmd/pentest-provider-bridge/*|\
    cmd/pentest-claude-sdk-bridge/*|\
    docker/pentest-sandbox/*|\
    scripts/ci-sandbox-smoke-required.sh|\
    scripts/smoke-sandbox-mcp-live.sh|\
    scripts/with-pentestd-live.sh|\
    internal/daemon/task_handlers.go|\
    internal/runner/runner.go|\
    internal/runtime/container.go|\
    internal/runtime/docker_sandbox.go)
      required=true
      break
      ;;
  esac
done < <(changed_files "$@")

printf 'required=%s\n' "$required"
