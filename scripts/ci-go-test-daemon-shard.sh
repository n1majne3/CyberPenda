#!/usr/bin/env bash
# Run one shard of ./internal/daemon tests.
# Usage: scripts/ci-go-test-daemon-shard.sh <shard_index> <shard_count>
#
# Tests are packed by measured duration (longest-processing-time first) using
# scripts/ci-daemon-test-times.tsv ("TestName<TAB>seconds"), so shards finish
# together instead of one shard owning every slow test. Tests missing from the
# file get a default weight, so new tests are still always run. Refresh the file
# from CI logs: each shard prints "--- PASS: TestX (Ns)" lines and a slowest-test
# summary.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"

shard_index="${1:?shard index required}"
shard_count="${2:?shard count required}"
times_file="${CI_DAEMON_TEST_TIMES:-scripts/ci-daemon-test-times.tsv}"
default_weight="${CI_DAEMON_TEST_DEFAULT_SECONDS:-0.15}"

if ! [[ "$shard_index" =~ ^[0-9]+$ && "$shard_count" =~ ^[0-9]+$ ]]; then
  echo "shard index/count must be non-negative integers" >&2
  exit 2
fi
if [ "$shard_count" -lt 1 ] || [ "$shard_index" -ge "$shard_count" ]; then
  echo "shard index $shard_index out of range for count $shard_count" >&2
  exit 2
fi

list_tests() {
  if [ -n "${CI_DAEMON_TEST_LIST_FILE:-}" ]; then
    cat "$CI_DAEMON_TEST_LIST_FILE"
  else
    node scripts/web-build-cli.mjs ensure-embed-stub
    go test -list . ./internal/daemon
  fi
}

mapfile -t tests < <(list_tests | grep '^Test' | LC_ALL=C sort)
if [ "${#tests[@]}" -eq 0 ]; then
  echo "no Test* names listed for ./internal/daemon" >&2
  exit 1
fi

# Deterministic LPT packing: every shard computes the same assignment.
mapfile -t selected < <(
  printf '%s\n' "${tests[@]}" | awk -v times="$times_file" -v def="$default_weight" \
    -v n="$shard_count" -v want="$shard_index" '
    BEGIN {
      while ((getline line < times) > 0) {
        if (line ~ /^#/ || line == "") continue
        split(line, f, "\t"); w[f[1]] = f[2] + 0
      }
    }
    { name = $0; weight = (name in w) ? w[name] : def; printf "%012.3f\t%s\n", weight, name }
  ' | LC_ALL=C sort -t$'\t' -k1,1r -k2,2 | awk -F'\t' -v n="$shard_count" -v want="$shard_index" '
    BEGIN { for (i = 0; i < n; i++) load[i] = 0 }
    {
      best = 0
      for (i = 1; i < n; i++) if (load[i] < load[best]) best = i
      load[best] += $1
      if (best == want) print $2
    }
    END { printf "shard %d planned load: %.1fs\n", want, load[want] > "/dev/stderr" }
  '
)

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
if [ -n "${CI_DAEMON_SHARD_DRY_RUN:-}" ]; then
  printf '%s\n' "${selected[@]}"
  exit 0
fi
log="$(mktemp)"
trap 'rm -f "$log"' EXIT
status=0
go test -timeout 20m ./internal/daemon -run "^(${pattern})$" -count=1 -v 2>&1 | tee "$log" || status=$?

echo "--- slowest tests in shard ${shard_index}/${shard_count} ---"
# awk (not head) consumes all input so sort never hits SIGPIPE under pipefail.
sed -nE 's/^--- (PASS|FAIL): (Test[^ ]+) \(([0-9.]+)s\)$/\3\t\2/p' "$log" | LC_ALL=C sort -t$'\t' -k1,1gr | awk 'NR <= 15'
exit "$status"
