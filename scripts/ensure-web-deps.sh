#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root/web"

repair_reason=""
if [[ ! -x node_modules/.bin/vite ]]; then
  repair_reason="Vite is not installed"
elif [[ ! -f node_modules/.package-lock.json ]]; then
  repair_reason="the installed dependency lock is missing"
elif [[ package.json -nt node_modules/.package-lock.json || package-lock.json -nt node_modules/.package-lock.json ]]; then
  repair_reason="the installed dependencies are stale"
elif ! node -e "import('rolldown')" >/dev/null 2>&1; then
  repair_reason="the platform-specific Rolldown binding is missing"
fi

if [[ -z "$repair_reason" ]]; then
  exit 0
fi

echo "web dependencies: $repair_reason; running npm ci"
npm ci

# Fail here with a focused error instead of starting the backend and then
# discovering that Vite still cannot load its native dependency.
node -e "import('rolldown')"
