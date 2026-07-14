#!/usr/bin/env bash
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"

make build-ui

if ! git diff --exit-code HEAD -- internal/daemon/webfs/dist; then
  echo >&2
  echo "Embedded UI is stale." >&2
  echo "The fresh build is now in internal/daemon/webfs/dist." >&2
  echo "Review and commit those generated files before pushing." >&2
  exit 1
fi

echo "Embedded UI matches HEAD."
