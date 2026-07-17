#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "${repo_root}/scripts/release/lib.sh"
dist="${1:-${repo_root}/dist}"
for command in go jq tar sha256sum strings readelf; do require_command "${command}"; done
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
  listing="$(TZ=UTC tar --numeric-owner --full-time -tvzf "${dist}/${archive}")"
  expected_time="${build_time/T/ }"
  expected_time="${expected_time%Z}"
  expected_listing="$(printf '%s\n' "${listing}" | awk '{print $1, $2, $4, $5, $6}')"
  expected_headers="$(printf '%s\n' \
    "-rw-r--r-- 0/0 ${expected_time} LICENSE" \
    "-rw-r--r-- 0/0 ${expected_time} README.md" \
    "-rw-r--r-- 0/0 ${expected_time} build-metadata.json" \
    "-rwxr-xr-x 0/0 ${expected_time} sysbox" \
    "-rwxr-xr-x 0/0 ${expected_time} sysbox-init")"
  [[ "${expected_listing}" == "${expected_headers}" ]] || { echo "release: archive header contract mismatch for ${arch}" >&2; exit 1; }
  tar -xzf "${dist}/${archive}" -C "${tmp}"
  members="$(find "${tmp}" -maxdepth 1 -type f -printf '%f\n' | sort)"
  required=$'LICENSE\nREADME.md\nbuild-metadata.json\nsysbox\nsysbox-init'
  [[ "${members}" == "${required}" ]] || { echo "release: unexpected archive members for ${arch}" >&2; rm -rf "${tmp}"; exit 1; }
  expected_sysbox_sha="$(jq -er --arg arch "${arch}" '.targets[] | select(.architecture == $arch) | .sysbox_sha256' "${dist}/build-metadata.json")"
  expected_init_sha="$(jq -er --arg arch "${arch}" '.targets[] | select(.architecture == $arch) | .sysbox_init_sha256' "${dist}/build-metadata.json")"
  [[ "$(sha256sum "${tmp}/sysbox" | awk '{print $1}')" == "${expected_sysbox_sha}" ]] || { echo "release: sysbox binary checksum mismatch for ${arch}" >&2; rm -rf "${tmp}"; exit 1; }
  [[ "$(sha256sum "${tmp}/sysbox-init" | awk '{print $1}')" == "${expected_init_sha}" ]] || { echo "release: sysbox-init binary checksum mismatch for ${arch}" >&2; rm -rf "${tmp}"; exit 1; }
  jq -e --arg tag "${tag}" --arg commit "${commit}" --arg build_time "${build_time}" --arg arch "${arch}" \
    '.version == $tag and .commit == $commit and .build_time == $build_time and .architecture == $arch and .license == "MulanPSL-2.0"' \
    "${tmp}/build-metadata.json" >/dev/null
  cmp LICENSE "${tmp}/LICENSE"
  cmp README.md "${tmp}/README.md"
  machine="$(readelf -h "${tmp}/sysbox" | awk -F: '/Machine:/ {sub(/^[[:space:]]+/, "", $2); print $2}')"
  init_machine="$(readelf -h "${tmp}/sysbox-init" | awk -F: '/Machine:/ {sub(/^[[:space:]]+/, "", $2); print $2}')"
  case "${arch}" in
    amd64) expected_machine='Advanced Micro Devices X86-64' ;;
    arm64) expected_machine='AArch64' ;;
  esac
  [[ "${machine}" == "${expected_machine}" && "${init_machine}" == "${expected_machine}" ]] || { echo "release: ELF architecture mismatch for ${arch}" >&2; rm -rf "${tmp}"; exit 1; }
  strings "${tmp}/sysbox" | grep -F "${commit}" >/dev/null
  strings "${tmp}/sysbox" | grep -F "${build_time}" >/dev/null
  if [[ "$(go env GOOS)/$(go env GOARCH)" == "linux/${arch}" ]]; then
    "${tmp}/sysbox" version --json | jq -e --arg tag "${tag}" --arg commit "${commit}" --arg build_time "${build_time}" \
      '.version == $tag and .commit == $commit and .build_time == $build_time' >/dev/null
  fi
  rm -rf "${tmp}"
done
echo "release: verified ${tag} artifacts"
