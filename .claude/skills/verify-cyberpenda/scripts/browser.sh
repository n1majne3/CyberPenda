#!/usr/bin/env bash
set -euo pipefail

if (( $# < 2 )); then
  printf 'Usage: bash browser.sh RUN_DIRECTORY COMMAND [ARGUMENTS...]\n' >&2
  exit 2
fi
run=$(cygpath -m "$1")
shift
if [[ ! -f "$run/browser.json" || ! -f "$run/run.json" || ! -d "$run/evidence" ]]; then
  printf 'Launch the isolated instance before using the browser.\n' >&2
  exit 2
fi
session=$(node -e 'process.stdout.write(JSON.parse(require("fs").readFileSync(process.argv[1], "utf8")).session)' "$run/run.json")

# Use a separate browser, not a saved host profile or a remote browser service.
for key in ${!AGENT_BROWSER_@}; do unset "$key"; done
unset HTTP_PROXY HTTPS_PROXY ALL_PROXY http_proxy https_proxy all_proxy
export AGENT_BROWSER_ALLOWED_DOMAINS=127.0.0.1
export MSYS2_ARG_CONV_EXCL='*'

printf '\n> ' >> "$run/evidence/actions.log"
printf '%q ' "$@" >> "$run/evidence/actions.log"
printf '\n' >> "$run/evidence/actions.log"
output=$(mktemp "$run/evidence/browser-command-XXXXXX.txt")
if agent-browser --config "$run/browser.json" --session "$session" "$@" > "$output" 2>&1; then
  status=0
else
  status=$?
fi
tee -a "$run/evidence/actions.log" < "$output"
printf 'exit=%s\n' "$status" >> "$run/evidence/actions.log"
exit "$status"
