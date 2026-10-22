#!/usr/bin/env bash
set -uo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
# pwd -W gives a Windows-style path that native git can store in core.hooksPath.
hooks_dir=$(cd "$repo_root/.githooks" && { pwd -W 2>/dev/null || pwd; })
work_root=$(mktemp -d) || { echo "FAIL - setup: mktemp failed" >&2; exit 1; }
trap 'rm -rf "$work_root"' EXIT

failures=0

case_failed() {
  echo "FAIL - $1"
  failures=$((failures + 1))
}

# Every git call goes through a non-empty directory so a broken setup can
# never fall back to this repository.
git_in() {
  [ -n "$1" ] || { echo "FAIL - setup: empty repository directory; aborting" >&2; exit 1; }
  require_scratch "$1" || exit 1
  git -C "$@"
}

# Every write stays inside the throwaway scratch root.
require_scratch() {
  case "$1" in
    "$work_root"/*) return 0 ;;
  esac
  case_failed "setup: refusing to touch a path outside the scratch root: $1"
  return 1
}

# Byte counts are exact, so core.autocrlf stays off for deterministic blobs.
repo_dir=
new_repo() { # $1 name, $2 optional "nohook"; aborts the suite on failure
  repo_dir=$work_root/$1
  require_scratch "$repo_dir" || exit 1
  git init -q "$repo_dir" &&
    git_in "$repo_dir" config user.name size-check &&
    git_in "$repo_dir" config user.email size-check@example.com &&
    git_in "$repo_dir" config core.autocrlf false &&
    { [ "${2:-}" = nohook ] || git_in "$repo_dir" config core.hooksPath "$hooks_dir"; } ||
    { case_failed "setup: cannot prepare repo $1"; exit 1; }
}

make_text() { # $1 file, $2 bytes of 'a'
  require_scratch "$1" || return 1
  head -c "$2" /dev/zero | tr '\0' 'a' > "$1"
  [ "$(wc -c < "$1")" -eq "$2" ] || { case_failed "setup: wrote $(wc -c < "$1") of $2 bytes to $1"; return 1; }
}

make_binary() { # $1 file, $2 bytes of NUL
  require_scratch "$1" || return 1
  head -c "$2" /dev/zero > "$1"
  [ "$(wc -c < "$1")" -eq "$2" ] || { case_failed "setup: wrote $(wc -c < "$1") of $2 bytes to $1"; return 1; }
}

stage_worktree() { # $1 dir, $2 path
  git_in "$1" add -- "$2" || { case_failed "setup: git add failed: $2"; return 1; }
}

stage_blob() { # $1 dir, $2 mode, $3 path; content on stdin, no worktree file
  local sha
  sha=$(git_in "$1" hash-object -w --stdin) ||
    { case_failed "setup: hash-object failed: $3"; return 1; }
  git_in "$1" update-index --add --cacheinfo "$2" "$sha" "$3" ||
    { case_failed "setup: update-index failed: $3"; return 1; }
}

stage_blob_or_skip() { # like stage_blob, but a platform path refusal is a skip
  local sha out
  sha=$(git_in "$1" hash-object -w --stdin) ||
    { case_failed "setup: hash-object failed: $3"; return 1; }
  out=$(git_in "$1" update-index --add --cacheinfo "$2" "$sha" "$3" 2>&1) || {
    if grep -q 'Invalid path' <<<"$out"; then
      echo "skip - git refuses the path '$3' on this platform"
      return 0
    fi
    case_failed "setup: update-index failed: $3"
    printf '%s\n' "$out"
    return 1
  }
}

run_commit_rejected() { # $1 dir (any subdir works), $2 label, then required needles
  local dir=$1 label=$2 out status missing=0 needle
  shift 2
  out=$(git_in "$dir" commit -m test 2>&1); status=$?
  if [ "$status" -eq 0 ]; then
    case_failed "$label: commit succeeded but must be rejected"
    return 0
  fi
  if grep -q '^pre-commit:' <<<"$out"; then
    case_failed "$label: hook aborted with an internal error instead of checking"
    printf '%s\n' "$out"
    return 0
  fi
  for needle in "$@"; do
    if ! grep -qF -- "$needle" <<<"$out"; then
      echo "FAIL - $label: output is missing \"$needle\""
      missing=1
    fi
  done
  if [ "$missing" -eq 0 ]; then
    echo "ok - $label"
  else
    printf '%s\n' "$out"
    failures=$((failures + 1))
  fi
}

run_commit_passes() { # $1 dir, $2 label
  local out status
  out=$(git_in "$1" commit -m test 2>&1); status=$?
  if [ "$status" -eq 0 ]; then
    echo "ok - $2"
  else
    case_failed "$2: commit exited $status"
    printf '%s\n' "$out"
  fi
}

commit_passes() { # $1 dir, $2 file, $3 label
  stage_worktree "$1" "$2" || return 0
  run_commit_passes "$1" "$3"
}

commit_rejected() { # $1 dir, $2 file, $3 label, then required needles
  local dir=$1 file=$2
  shift 2
  stage_worktree "$dir" "$file" || return 0
  run_commit_rejected "$dir" "$@"
}

t01() {
  new_repo t01
  make_text "$repo_dir/small.txt" 1000 || return
  commit_passes "$repo_dir" small.txt "1,000-byte text file commits"
}

t02() {
  new_repo t02
  make_text "$repo_dir/big.txt" 25000 || return
  commit_rejected "$repo_dir" big.txt "25,000-byte text file is rejected with split instructions" \
    "5,000-token limit" \
    "Group by responsibility" \
    "name files after the concept, never part1/part2" \
    "Keep the public API" \
    "separate _test.go or .test.tsx files" \
    "stay under 5,000 tokens" \
    "Run gofmt" \
    "Generated files are skipped"
}

t03() {
  new_repo t03
  make_text "$repo_dir/exact.txt" 20000 || return
  commit_passes "$repo_dir" exact.txt "exactly 20,000 bytes commits"
}

t04() {
  new_repo t04
  make_text "$repo_dir/over.txt" 20001 || return
  commit_rejected "$repo_dir" over.txt "20,001 bytes is rejected" "5,000-token limit"
}

t05() {
  new_repo t05
  make_text "$repo_dir/package-lock.json" 25000 || return
  commit_passes "$repo_dir" package-lock.json "25,000-byte package-lock.json commits"
}

t06() {
  new_repo t06
  require_scratch "$repo_dir/gen.txt" || return
  { printf '// Code generated by test. DO NOT EDIT.\n'; head -c 25000 /dev/zero | tr '\0' 'a'; } > "$repo_dir/gen.txt"
  commit_passes "$repo_dir" gen.txt "file with a generated header commits"
}

t07() {
  new_repo t07
  make_binary "$repo_dir/asset.png" 25000 || return
  commit_passes "$repo_dir" asset.png "binary PNG with NUL bytes commits"
}

t08() {
  new_repo t08
  require_scratch "$repo_dir/vendor" || return
  mkdir -p "$repo_dir/vendor" || { case_failed "setup: mkdir vendor"; return; }
  make_text "$repo_dir/vendor/big.txt" 25000 || return
  commit_passes "$repo_dir" vendor/big.txt "file under vendor/ commits"
}

t09() {
  new_repo t09
  make_text "$repo_dir/big.txt" 25000 || return
  stage_worktree "$repo_dir" big.txt || return
  make_text "$repo_dir/big.txt" 1000 || return
  run_commit_rejected "$repo_dir" "staged large file with a small worktree copy is rejected" "5,000-token limit"
}

t10() {
  new_repo t10
  make_text "$repo_dir/now-big.txt" 1000 || return
  stage_worktree "$repo_dir" now-big.txt || return
  make_text "$repo_dir/now-big.txt" 25000 || return
  run_commit_passes "$repo_dir" "staged small file commits although the worktree copy is large"
}

t11() {
  new_repo t11
  make_text "$repo_dir/gone.txt" 25000 || return
  stage_worktree "$repo_dir" gone.txt || return
  require_scratch "$repo_dir/gone.txt" || return
  rm -f "$repo_dir/gone.txt" || { case_failed "setup: rm failed: gone.txt"; return; }
  run_commit_rejected "$repo_dir" "staged large file with the worktree copy deleted is rejected" "5,000-token limit"
}

t12() {
  new_repo t12
  make_text "$work_root/link-target.blob" 30000 || return
  stage_blob "$repo_dir" 120000 big-link <"$work_root/link-target.blob" || return
  run_commit_passes "$repo_dir" "staged symbolic link is skipped"
}

t13() {
  new_repo t13 nohook
  make_text "$repo_dir/big.txt" 25000 || return
  git_in "$repo_dir" add -- big.txt || { case_failed "setup: git add failed: big.txt"; return; }
  git_in "$repo_dir" commit -q -m seed || { case_failed "setup: seed commit failed"; return; }
  git_in "$repo_dir" config core.hooksPath "$hooks_dir" ||
    { case_failed "setup: core.hooksPath t13"; return; }
  git_in "$repo_dir" mv big.txt moved.txt || { case_failed "setup: git mv failed"; return; }
  run_commit_rejected "$repo_dir" "renamed large file is rejected" "5,000-token limit"
}

t14() {
  new_repo t14
  make_text "$repo_dir/keep.txt" 1000 || return
  commit_passes "$repo_dir" keep.txt "t14 seed commit of a small file"
  make_text "$repo_dir/keep.txt" 25000 || return
  make_text "$repo_dir/other.txt" 500 || return
  commit_passes "$repo_dir" other.txt "unstaged worktree growth does not block another staged file"
}

t15() {
  new_repo t15
  require_scratch "$repo_dir/gen.txt" || return
  { printf '/*\n * fixture header\n * auto-generated by the test suite\n */\n'; head -c 25000 /dev/zero | tr '\0' 'a'; } > "$repo_dir/gen.txt"
  [ "$(wc -c < "$repo_dir/gen.txt")" -gt 20000 ] || { case_failed "setup: gen.txt is too small"; return; }
  commit_passes "$repo_dir" gen.txt "multi-line generated header commits"
}

t16() {
  new_repo t16
  make_binary "$repo_dir/data.dat" 25000 || return
  commit_passes "$repo_dir" data.dat "binary NUL content with a .dat extension commits"
}

t17() {
  new_repo t17
  # Staging reads from a file redirect because a helper in a pipeline segment
  # runs in a subshell and could not record a setup failure.
  make_text "$work_root/content.blob" 25000 || return
  stage_blob "$repo_dir" 100644 'has space.txt' <"$work_root/content.blob" || return
  stage_blob "$repo_dir" 100644 'semi;colon.txt' <"$work_root/content.blob" || return
  stage_blob "$repo_dir" 100644 'amp&ersand.txt' <"$work_root/content.blob" || return
  stage_blob "$repo_dir" 100644 'paren(thesis).txt' <"$work_root/content.blob" || return
  stage_blob "$repo_dir" 100644 'brace{}.txt' <"$work_root/content.blob" || return
  stage_blob "$repo_dir" 100644 'dollar$sign.txt' <"$work_root/content.blob" || return
  stage_blob "$repo_dir" 100644 'hash#tag.txt' <"$work_root/content.blob" || return
  stage_blob_or_skip "$repo_dir" 100644 'colon:name.txt' <"$work_root/content.blob" || return
  stage_blob_or_skip "$repo_dir" 100644 'star*name?.txt' <"$work_root/content.blob" || return
  stage_blob_or_skip "$repo_dir" 100644 'brack[et].txt' <"$work_root/content.blob" || return
  stage_blob_or_skip "$repo_dir" 100644 'quote"name.txt' <"$work_root/content.blob" || return
  stage_blob_or_skip "$repo_dir" 100644 $'tab\tname.txt' <"$work_root/content.blob" || return
  stage_blob_or_skip "$repo_dir" 100644 $'new\nline.txt' <"$work_root/content.blob" || return
  run_commit_rejected "$repo_dir" "large files with unusual names are rejected" \
    "5,000-token limit" "has space.txt" "semi;colon.txt"
}

t18() {
  new_repo t18
  require_scratch "$repo_dir/internal" || return
  mkdir -p "$repo_dir/internal/blackboardv2contract/contractdata" ||
    { case_failed "setup: mkdir contractdata"; return; }
  make_text "$repo_dir/internal/blackboardv2contract/contractdata/manifest.json" 25000 || return
  commit_rejected "$repo_dir" internal/blackboardv2contract/contractdata/manifest.json \
    "handwritten contract data under contractdata is checked" "5,000-token limit"
}

t19() {
  new_repo t19
  require_scratch "$repo_dir/sub" || return
  mkdir -p "$repo_dir/sub" || { case_failed "setup: mkdir sub"; return; }
  make_text "$repo_dir/big.txt" 25000 || return
  stage_worktree "$repo_dir" big.txt || return
  run_commit_rejected "$repo_dir/sub" "commit run from a subdirectory still checks the staged file" "5,000-token limit"
}

t20() {
  new_repo t20 nohook
  make_text "$work_root/type-target.blob" 10 || return
  stage_blob "$repo_dir" 120000 changed.txt <"$work_root/type-target.blob" || return
  git_in "$repo_dir" commit -q -m seed || { case_failed "setup: seed type-change commit failed"; return; }
  git_in "$repo_dir" config core.hooksPath "$hooks_dir" ||
    { case_failed "setup: core.hooksPath t20"; return; }
  make_text "$work_root/type-content.blob" 25000 || return
  stage_blob "$repo_dir" 100644 changed.txt <"$work_root/type-content.blob" || return
  run_commit_rejected "$repo_dir" "symlink changed to an oversized regular file is rejected" "5,000-token limit"
}

t21() {
  new_repo t21
  make_text "$repo_dir/glossary.md" 25000 || return
  commit_passes "$repo_dir" glossary.md "25,000-byte Markdown document commits"
}

t01
t02
t03
t04
t05
t06
t07
t08
t09
t10
t11
t12
t13
t14
t15
t16
t17
t18
t19
t20
t21

if [ "$failures" -gt 0 ]; then
  echo "$failures case(s) failed"
  exit 1
fi
echo "all cases passed"
