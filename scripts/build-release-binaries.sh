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
if [[ ! "${version}" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]]; then
  echo "invalid version ${version}: use letters, numbers, dot, underscore, or dash" >&2
  exit 2
fi

for target in "${targets[@]}"; do
  if [[ ! "${target}" =~ ^[a-z0-9][a-z0-9._-]*/[a-z0-9][a-z0-9._-]*$ ]]; then
    echo "invalid target ${target}: expected safe GOOS/GOARCH" >&2
    exit 2
  fi
done

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${repo_root}"

dist_arg="${2:-dist/release}"
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

mkdir -p "${dist_dir}"
staging_root="$(mktemp -d "${dist_dir}/.cyberpenda-release.XXXXXX")"
release_output="${staging_root}/output"
mkdir -p "${release_output}"
cleanup() {
  rm -rf -- "${staging_root}"
}
trap cleanup EXIT

for target in "${targets[@]}"; do
  goos="${target%/*}"
  goarch="${target#*/}"
  exe=""
  archive_ext="tar.gz"
  if [ "${goos}" = "windows" ]; then
    exe=".exe"
    archive_ext="zip"
  fi

  package_name="cyberpenda_${version}_${goos}_${goarch}"
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
    (cd "${staging_root}" && zip -qr "${release_output}/${package_name}.zip" "${package_name}")
  else
    tar -C "${staging_root}" -czf "${release_output}/${package_name}.tar.gz" "${package_name}"
  fi

  rm -rf -- "${package_dir}"
done

(
  cd "${release_output}"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum * > SHA256SUMS
  else
    shasum -a 256 * > SHA256SUMS
  fi
)

for artifact in "${release_output}"/*; do
  mv -f -- "${artifact}" "${dist_dir}/"
done
