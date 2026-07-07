#!/usr/bin/env bash
set -euo pipefail

DEFAULT_TARGETS=(
  "linux/amd64"
  "linux/arm64"
  "darwin/amd64"
  "darwin/arm64"
  "windows/amd64"
  "windows/arm64"
)

targets=("${DEFAULT_TARGETS[@]}")
if [ -n "${PENTEST_RELEASE_TARGETS:-}" ]; then
  targets=()
  for target in ${PENTEST_RELEASE_TARGETS}; do
    targets+=("${target}")
  done
fi

usage() {
  printf 'usage: %s <version> [dist-dir]\n' "$0"
  printf '       %s --list-targets\n' "$0"
}

case "${1:-}" in
  --help|-h)
    usage
    exit 0
    ;;
  --list-targets)
    printf '%s\n' "${targets[@]}"
    exit 0
    ;;
esac

version="${1:-}"
if [ -z "${version}" ]; then
  usage >&2
  exit 2
fi

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${repo_root}"

dist_dir="${2:-dist/release}"
case "${dist_dir}" in
  /*) ;;
  *) dist_dir="${repo_root}/${dist_dir}" ;;
esac

rm -rf "${dist_dir}"
mkdir -p "${dist_dir}"

for target in "${targets[@]}"; do
  case "${target}" in
    */*) ;;
    *)
      echo "invalid target ${target}: expected GOOS/GOARCH" >&2
      exit 2
      ;;
  esac

  goos="${target%/*}"
  goarch="${target#*/}"
  exe=""
  archive_ext="tar.gz"
  if [ "${goos}" = "windows" ]; then
    exe=".exe"
    archive_ext="zip"
  fi

  package_name="cyberpenda_${version}_${goos}_${goarch}"
  staging_root="$(mktemp -d)"
  package_dir="${staging_root}/${package_name}"
  mkdir -p "${package_dir}"

  echo "building ${target}"
  GOOS="${goos}" GOARCH="${goarch}" CGO_ENABLED=0 go build \
    -trimpath \
    -ldflags="-s -w -X main.version=${version}" \
    -o "${package_dir}/pentestd${exe}" \
    ./cmd/pentestd
  GOOS="${goos}" GOARCH="${goarch}" CGO_ENABLED=0 go build \
    -trimpath \
    -ldflags="-s -w" \
    -o "${package_dir}/pentestctl${exe}" \
    ./cmd/pentestctl
  cp README.md "${package_dir}/"

  if [ "${archive_ext}" = "zip" ]; then
    (cd "${staging_root}" && zip -qr "${dist_dir}/${package_name}.zip" "${package_name}")
  else
    tar -C "${staging_root}" -czf "${dist_dir}/${package_name}.tar.gz" "${package_name}"
  fi

  rm -rf "${staging_root}"
done

(
  cd "${dist_dir}"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum * > SHA256SUMS
  else
    shasum -a 256 * > SHA256SUMS
  fi
)
