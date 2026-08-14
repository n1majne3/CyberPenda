#!/usr/bin/env bash
# Validate the TSecBench local-mode API through the same image used for Hosted Mode.
set -euo pipefail

usage() {
  echo "usage: scripts/validate-tsecbench-local-mode.sh --env-file <path> [--image <tag>] [--container-cli <docker|podman>]" >&2
}

ENV_FILE=""
IMAGE="${CYBERPENDA_TSECBENCH_HOSTED_IMAGE:-cyberpenda-tsecbench-hosted:local}"
CONTAINER_CLI="${PENTEST_CONTAINER_CLI:-docker}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --env-file)
      [[ $# -ge 2 ]] || { usage; exit 64; }
      ENV_FILE="$2"
      shift 2
      ;;
    --image)
      [[ $# -ge 2 ]] || { usage; exit 64; }
      IMAGE="$2"
      shift 2
      ;;
    --container-cli)
      [[ $# -ge 2 ]] || { usage; exit 64; }
      CONTAINER_CLI="$2"
      shift 2
      ;;
    *)
      usage
      exit 64
      ;;
  esac
done

if [[ -z "${ENV_FILE}" || -z "${IMAGE}" || -z "${CONTAINER_CLI}" ]]; then
  usage
  exit 64
fi

# Do not source this file. Validate its shape and permissions, then let the
# container engine inject it without putting Secret values in process arguments.
python3 - "${ENV_FILE}" <<'PY'
import os
import stat
import sys

path = sys.argv[1]
try:
    metadata = os.lstat(path)
except OSError as error:
    raise SystemExit(f"env-file check failed: {error.strerror}")
if not stat.S_ISREG(metadata.st_mode):
    raise SystemExit("env-file check failed: path must be a regular file and not a symbolic link")
if stat.S_IMODE(metadata.st_mode) & 0o077:
    raise SystemExit("env-file check failed: group and other permission bits must be clear (use chmod 600)")

required = {"BENCHMARK_BASE_URL", "BENCHMARK_TOKEN"}
seen = set()
try:
    with open(path, "r", encoding="utf-8") as stream:
        for line_number, raw_line in enumerate(stream, 1):
            line = raw_line.rstrip("\r\n")
            if not line or line.lstrip().startswith("#"):
                continue
            if "=" not in line:
                raise SystemExit(f"env-file check failed: invalid entry on line {line_number}")
            name, value = line.split("=", 1)
            if name not in required:
                raise SystemExit(f"env-file check failed: unsupported variable on line {line_number}")
            if name in seen:
                raise SystemExit(f"env-file check failed: duplicate {name}")
            if not value:
                raise SystemExit(f"env-file check failed: {name} is empty")
            if "\x00" in value:
                raise SystemExit(f"env-file check failed: invalid {name}")
            seen.add(name)
except (OSError, UnicodeError) as error:
    raise SystemExit(f"env-file check failed: cannot read file ({type(error).__name__})")

missing = sorted(required - seen)
if missing:
    raise SystemExit("env-file check failed: missing " + ", ".join(missing))
PY

echo "Validating TSecBench local-mode API. The host VPN must already be connected." >&2
exec "${CONTAINER_CLI}" run --rm \
  --network host \
  --env-file "${ENV_FILE}" \
  --entrypoint /usr/local/bin/tsecbench-local-validate \
  "${IMAGE}"
