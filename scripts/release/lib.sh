#!/usr/bin/env bash

validate_version() {
  [[ "${1:-}" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]
}

version_without_v() {
  validate_version "${1:-}" || return 1
  printf '%s\n' "${1#v}"
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "release: required command not found: $1" >&2
    return 1
  }
}

require_release_repo() {
  local tag="$1"
  validate_version "${tag}" || { echo "release: tag must be canonical vMAJOR.MINOR.PATCH: ${tag}" >&2; return 1; }
  git diff --quiet --ignore-submodules -- || { echo "release: tracked worktree is dirty" >&2; return 1; }
  git diff --cached --quiet --ignore-submodules -- || { echo "release: index is dirty" >&2; return 1; }
  local head tagged
  head="$(git rev-parse HEAD)"
  tagged="$(git rev-parse "${tag}^{commit}" 2>/dev/null)" || { echo "release: tag does not exist: ${tag}" >&2; return 1; }
  [[ "${head}" == "${tagged}" ]] || { echo "release: tag ${tag} does not point at HEAD" >&2; return 1; }
}

release_commit() { git rev-parse HEAD; }
release_epoch() { git show -s --format=%ct HEAD; }
release_time() { date -u -d "@$(release_epoch)" +%Y-%m-%dT%H:%M:%SZ; }

release_ldflags() {
  local tag="$1" commit="$2" build_time="$3"
  printf '%s' "-s -w -X github.com/oslab/sysbox/pkg/buildinfo.Version=${tag} -X github.com/oslab/sysbox/pkg/buildinfo.Commit=${commit} -X github.com/oslab/sysbox/pkg/buildinfo.BuildTime=${build_time}"
}

oci_tags() {
  local tag="$1" image="$2" version major minor
  version="$(version_without_v "${tag}")" || return 1
  IFS=. read -r major minor _ <<<"${version}"
  printf '%s\n' "${image}:${tag}" "${image}:${version}" "${image}:${major}.${minor}" "${image}:${major}" "${image}:latest"
}
