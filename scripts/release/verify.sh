#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "${repo_root}/scripts/release/lib.sh"
dist="${1:-${repo_root}/dist}"
for command in go jq tar sha256sum strings; do require_command "${command}"; done
[[ -s "${dist}/SHA256SUMS" && -s "${dist}/build-metadata.json" ]] || { echo "release: missing checksums or metadata in ${dist}" >&2; exit 1; }
(cd "${dist}" && sha256sum -c SHA256SUMS)

tag="$(jq -er '.tag' "${dist}/build-metadata.json")"
commit="$(jq -er '.commit' "${dist}/build-metadata.json")"
build_time="$(jq -er '.build_time' "${dist}/build-metadata.json")"
[[ "$(jq -r '.license' "${dist}/build-metadata.json")" == MulanPSL-2.0 ]]
for arch in amd64 arm64; do
  archive="sysbox_${tag}_linux_${arch}.tar.gz"
  expected="$(jq -er --arg arch "${arch}" '.targets[] | select(.architecture == $arch) | .sha256' "${dist}/build-metadata.json")"
  actual="$(sha256sum "${dist}/${archive}" | awk '{print $1}')"
  [[ "${actual}" == "${expected}" ]] || { echo "release: metadata checksum mismatch for ${archive}" >&2; exit 1; }
  tmp="$(mktemp -d)"
  tar -xzf "${dist}/${archive}" -C "${tmp}"
  members="$(find "${tmp}" -maxdepth 1 -type f -printf '%f\n' | sort)"
  required=$'LICENSE\nREADME.md\nbuild-metadata.json\nsysbox\nsysbox-init'
  [[ "${members}" == "${required}" ]] || { echo "release: unexpected archive members for ${arch}" >&2; rm -rf "${tmp}"; exit 1; }
  jq -e --arg tag "${tag}" --arg commit "${commit}" --arg build_time "${build_time}" --arg arch "${arch}" \
    '.version == $tag and .commit == $commit and .build_time == $build_time and .architecture == $arch and .license == "MulanPSL-2.0"' \
    "${tmp}/build-metadata.json" >/dev/null
  strings "${tmp}/sysbox" | grep -F "${commit}" >/dev/null
  strings "${tmp}/sysbox" | grep -F "${build_time}" >/dev/null
  if [[ "$(go env GOOS)/$(go env GOARCH)" == "linux/${arch}" ]]; then
    "${tmp}/sysbox" version --json | jq -e --arg tag "${tag}" --arg commit "${commit}" --arg build_time "${build_time}" \
      '.version == $tag and .commit == $commit and .build_time == $build_time' >/dev/null
  fi
  rm -rf "${tmp}"
done
echo "release: verified ${tag} artifacts"
