#!/usr/bin/env bash
# Run one shard of ./internal/daemon tests.
# Usage: scripts/ci-go-test-daemon-shard.sh <shard_index> <shard_count>
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"

shard_index="${1:?shard index required}"
shard_count="${2:?shard count required}"

if ! [[ "$shard_index" =~ ^[0-9]+$ && "$shard_count" =~ ^[0-9]+$ ]]; then
  echo "shard index/count must be non-negative integers" >&2
  exit 2
fi
if [ "$shard_count" -lt 1 ] || [ "$shard_index" -ge "$shard_count" ]; then
  echo "shard index $shard_index out of range for count $shard_count" >&2
  exit 2
fi

node scripts/web-build-cli.mjs ensure-embed-stub

mapfile -t tests < <(go test -list . ./internal/daemon | grep '^Test' | LC_ALL=C sort)
if [ "${#tests[@]}" -eq 0 ]; then
  echo "no Test* names listed for ./internal/daemon" >&2
  exit 1
fi

selected=()
for i in "${!tests[@]}"; do
  if (( i % shard_count == shard_index )); then
    selected+=("${tests[$i]}")
  fi
done

if [ "${#selected[@]}" -eq 0 ]; then
  echo "shard ${shard_index}/${shard_count}: no daemon tests assigned"
  exit 0
fi

# Escape regex metacharacters in test names (keep them literal for -run).
escape_regex() {
  printf '%s' "$1" | sed -e 's/[.[\*^$()+?{|]/\\&/g'
}

parts=()
for name in "${selected[@]}"; do
  parts+=("$(escape_regex "$name")")
done
pattern="$(IFS='|'; echo "${parts[*]}")"

echo "shard ${shard_index}/${shard_count}: ${#selected[@]} / ${#tests[@]} daemon tests"
go test -timeout 20m ./internal/daemon -run "^(${pattern})$" -count=1
