#!/usr/bin/env bash
set -euo pipefail

usage() {
  printf 'usage: %s <version> <hosted-image> [dist-dir]\n' "$0"
}

version="${1:-}"
image="${2:-}"
dist_arg="${3:-dist/tsecbench-hosted}"
if [ -z "${version}" ] || [ -z "${image}" ]; then
  usage >&2
  exit 2
fi
if [[ ! "${version}" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]]; then
  echo "invalid version ${version}: use letters, numbers, dot, underscore, or dash" >&2
  exit 2
fi

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${repo_root}"
case "${dist_arg}" in
  /*) dist_candidate="${dist_arg}" ;;
  *) dist_candidate="${repo_root}/${dist_arg}" ;;
esac
dist_dir="$(python3 -c 'import os, sys; print(os.path.realpath(sys.argv[1]))' "${dist_candidate}")"
case "${dist_dir}" in
  "${repo_root}"|"${repo_root}/")
    echo "dist-dir must not be the repository root" >&2
    exit 2
    ;;
  "${repo_root}"/*) ;;
  *)
    echo "dist-dir must stay inside the repository" >&2
    exit 2
    ;;
esac

package_name="cyberpenda-tsecbench-hosted_${version}_linux_amd64"
final_dir="${dist_dir}/${package_name}"
if [ -e "${final_dir}" ]; then
  echo "bundle output already exists: ${final_dir}" >&2
  exit 2
fi

local_runner="${TSECBENCH_LOCAL_RUNNER_SOURCE:-${repo_root}/scripts/validate-tsecbench-local-mode.sh}"
local_env_template="${TSECBENCH_LOCAL_ENV_TEMPLATE_SOURCE:-${repo_root}/docs/tsecbench/tsecbench-local.env.example}"
hosted_env_template="${repo_root}/docs/tsecbench/tsecbench.env.example"
integration_guide="${repo_root}/docs/tsecbench/README.md"
troubleshooting_guide="${repo_root}/docs/tsecbench/TROUBLESHOOTING.md"
for required in "${local_runner}" "${local_env_template}" "${hosted_env_template}" "${integration_guide}" "${troubleshooting_guide}"; do
  if [ ! -f "${required}" ]; then
    echo "required delivery input is missing: ${required}" >&2
    exit 2
  fi
done
if [ ! -x "${local_runner}" ]; then
  echo "local-mode runner is not executable: ${local_runner}" >&2
  exit 2
fi

max_archive_bytes="${TSECBENCH_MAX_ARCHIVE_BYTES:-3000000000}"
if [[ ! "${max_archive_bytes}" =~ ^[1-9][0-9]*$ ]]; then
  echo "invalid TSECBENCH_MAX_ARCHIVE_BYTES" >&2
  exit 2
fi

mkdir -p "${dist_dir}"
staging_root="$(mktemp -d "${dist_dir}/.tsecbench-hosted-bundle.XXXXXX")"
staging_bundle="${staging_root}/${package_name}"
mkdir -p "${staging_bundle}"
cleanup() {
  rm -rf -- "${staging_root}"
}
trap cleanup EXIT

echo "running complete acceptance suite"
make test-ci >&2
echo "running Hosted Image smoke"
CYBERPENDA_TSECBENCH_HOSTED_IMAGE="${image}" make smoke-tsecbench-hosted-image >&2

platform="$(docker image inspect --format '{{.Os}}/{{.Architecture}}' "${image}")"
if [ "${platform}" != "linux/amd64" ]; then
  echo "Hosted Image platform is ${platform}; require linux/amd64" >&2
  exit 1
fi
image_id="$(docker image inspect --format '{{.Id}}' "${image}")"

runtime_inventory="${staging_root}/runtime-versions.json"
if ! docker run --rm --network none --entrypoint cat "${image}" /opt/cyberpenda/runtime-versions.json > "${runtime_inventory}"; then
  echo "failed to read Runtime inventory" >&2
  exit 1
fi
python3 - "${runtime_inventory}" <<'PY'
import json, sys
path = sys.argv[1]
with open(path, encoding="utf-8") as source:
    inventory = json.load(source)
if inventory.get("schema") != "cyberpenda-hosted-runtime-versions/v1":
    raise SystemExit("invalid Runtime inventory schema")
for name in ("pi", "codex", "claude_code", "hermes"):
    entry = inventory.get("runtimes", {}).get(name, {})
    for field in ("package", "version", "binary"):
        if not str(entry.get(field, "")).strip():
            raise SystemExit(f"Runtime inventory is missing {name}.{field}")
sdk = inventory.get("components", {}).get("claude_agent_sdk", {})
if sdk.get("package") != "@anthropic-ai/claude-agent-sdk":
    raise SystemExit("Runtime inventory is missing claude_agent_sdk.package")
if not str(sdk.get("version", "")).strip():
    raise SystemExit("Runtime inventory is missing claude_agent_sdk.version")
PY

tool_inventory="${staging_root}/tool-versions.txt"
if ! docker run --rm --network none --entrypoint sh "${image}" -c '
set -eu
first_line() { "$@" 2>&1 | sed -n "1p" | tr "\n" " "; printf "\n"; }
printf "kali="; sed -n "s/^VERSION=//p" /etc/os-release | tr -d "\""
printf "python3="; first_line python3 --version
printf "go="; first_line go version
printf "gcc="; first_line gcc --version
printf "gdb="; first_line gdb --version
printf "binutils="; first_line ld --version
printf "nmap="; first_line nmap --version
printf "openssl="; first_line openssl version
printf "chromium="; first_line chromium --version
printf "agent_browser="; first_line agent-browser --version
printf "pwntools="; python3 -c "import pwnlib; print(getattr(pwnlib, \"__version__\", \"installed\"))"
' > "${tool_inventory}"; then
  echo "failed to read tool inventory" >&2
  exit 1
fi
for component in kali python3 go gcc gdb binutils nmap openssl chromium agent_browser pwntools; do
  if ! grep -Eq "^${component}=.+" "${tool_inventory}"; then
    echo "component inventory is missing ${component}" >&2
    exit 1
  fi
done

archive="${staging_bundle}/${package_name}.tar.gz"
echo "exporting Hosted Image"
if ! docker save "${image}" | gzip -9 -n -c > "${archive}"; then
  echo "failed to export Hosted Image" >&2
  exit 1
fi
archive_bytes="$(wc -c < "${archive}" | tr -d '[:space:]')"
if [ "${archive_bytes}" -ge "${max_archive_bytes}" ]; then
  echo "compressed Hosted Image is ${archive_bytes} bytes; it must be smaller than ${max_archive_bytes} bytes" >&2
  exit 1
fi

{
  printf 'schema=cyberpenda-hosted-delivery-components/v1\n'
  printf 'version=%s\n' "${version}"
  printf 'platform=linux/amd64\n'
  printf 'image=%s\n' "${image}"
  printf 'image_id=%s\n' "${image_id}"
  printf 'archive_bytes=%s\n' "${archive_bytes}"
  printf '\n[runtime_inventory]\n'
  cat "${runtime_inventory}"
  printf '\n[tool_inventory]\n'
  cat "${tool_inventory}"
} > "${staging_bundle}/COMPONENTS.txt"

cp "${hosted_env_template}" "${staging_bundle}/tsecbench.env.example"
cp "${local_env_template}" "${staging_bundle}/tsecbench-local.env.example"
cp "${local_runner}" "${staging_bundle}/run-tsecbench-local-mode.sh"
chmod 0755 "${staging_bundle}/run-tsecbench-local-mode.sh"
cp "${integration_guide}" "${staging_bundle}/README.md"
cp "${troubleshooting_guide}" "${staging_bundle}/TROUBLESHOOTING.md"

(
  cd "${staging_bundle}"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "${package_name}.tar.gz" > SHA256SUMS
    sha256sum -c SHA256SUMS
  else
    shasum -a 256 "${package_name}.tar.gz" > SHA256SUMS
    shasum -a 256 -c SHA256SUMS
  fi
)

mv -- "${staging_bundle}" "${final_dir}"
echo "Hosted Delivery Bundle: ${final_dir}"
