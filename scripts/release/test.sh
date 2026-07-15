#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "${repo_root}/scripts/release/lib.sh"

fail() {
  echo "release test: $*" >&2
  exit 1
}

validate_version v0.1.0
for invalid in v1 1.2.3 v1.2.3-rc.1 v01.2.3 v1.02.3 v1.2.03; do
  if validate_version "${invalid}" >/dev/null 2>&1; then
    fail "accepted invalid version ${invalid}"
  fi
done

tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT

git_repo="${tmp}/git-contract"
mkdir -p "${git_repo}"
(
  cd "${git_repo}"
  git init -q
  git config user.name sysbox-release-test
  git config user.email release-test@sysbox.invalid
  printf 'one\n' >tracked
  git add tracked
  git commit -qm initial
  git tag v0.1.0
  require_release_repo v0.1.0
  printf 'dirty\n' >>tracked
  if require_release_repo v0.1.0 >/dev/null 2>&1; then
    fail "accepted dirty tracked worktree"
  fi
  git restore tracked
  printf 'two\n' >tracked
  git add tracked
  git commit -qm second
  if require_release_repo v0.1.0 >/dev/null 2>&1; then
    fail "accepted tag that does not point at HEAD"
  fi
)

"${repo_root}/scripts/release/build.sh" --tag v0.1.0 --output "${tmp}/first" --allow-untagged
"${repo_root}/scripts/release/verify.sh" "${tmp}/first"
"${repo_root}/scripts/release/build.sh" --tag v0.1.0 --output "${tmp}/second" --allow-untagged
"${repo_root}/scripts/release/verify.sh" "${tmp}/second"

cmp "${tmp}/first/SHA256SUMS" "${tmp}/second/SHA256SUMS"
for arch in amd64 arm64; do
  archive="sysbox_v0.1.0_linux_${arch}.tar.gz"
  cmp "${tmp}/first/${archive}" "${tmp}/second/${archive}"
  members="$(tar -tzf "${tmp}/first/${archive}" | sort)"
  expected=$'LICENSE\nREADME.md\nbuild-metadata.json\nsysbox\nsysbox-init'
  [[ "${members}" == "${expected}" ]] || fail "unexpected ${arch} archive members: ${members}"
done

echo "Release artifact tests passed."
