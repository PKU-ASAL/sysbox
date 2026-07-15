#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "${repo_root}/scripts/release/lib.sh"

tag=""
output="${repo_root}/dist"
allow_untagged=false
while (($#)); do
  case "$1" in
    --tag) tag="${2:-}"; shift 2 ;;
    --output) output="${2:-}"; shift 2 ;;
    --allow-untagged) allow_untagged=true; shift ;;
    *) echo "usage: $0 --tag vMAJOR.MINOR.PATCH [--output DIR] [--allow-untagged]" >&2; exit 2 ;;
  esac
done

validate_version "${tag}" || { echo "release: invalid tag ${tag}" >&2; exit 1; }
[[ "${allow_untagged}" == true ]] || require_release_repo "${tag}"
for command in go git jq tar gzip sha256sum date; do require_command "${command}"; done
tar --version | head -1 | grep -q 'GNU tar' || { echo "release: GNU tar is required" >&2; exit 1; }

commit="$(release_commit)"
epoch="$(release_epoch)"
build_time="$(release_time)"
go_version="$(go env GOVERSION)"
source_url="${SOURCE_URL:-https://git.pku.edu.cn/oslab/sysbox}"
oci_image="${OCI_IMAGE:-git.pku.edu.cn/oslab/sysbox}"
ldflags="$(release_ldflags "${tag}" "${commit}" "${build_time}")"

rm -rf "${output}"
mkdir -p "${output}"
for arch in amd64 arm64; do
  stage="${output}/.stage-${arch}"
  archive="sysbox_${tag}_linux_${arch}.tar.gz"
  mkdir -p "${stage}"
  CGO_ENABLED=0 GOOS=linux GOARCH="${arch}" go build -trimpath -buildvcs=false -ldflags "${ldflags}" -o "${stage}/sysbox" ./cmd/sysbox
  CGO_ENABLED=0 GOOS=linux GOARCH="${arch}" go build -trimpath -buildvcs=false -ldflags "${ldflags}" -o "${stage}/sysbox-init" ./cmd/sysbox-init
  install -m 0644 README.md "${stage}/README.md"
  install -m 0644 LICENSE "${stage}/LICENSE"
  jq -S -n --arg version "${tag}" --arg tag "${tag}" --arg commit "${commit}" \
    --arg build_time "${build_time}" --arg go_version "${go_version}" --arg os linux --arg arch "${arch}" \
    --arg license MulanPSL-2.0 --arg source "${source_url}" --arg oci_image "${oci_image}" \
    '{version:$version,tag:$tag,commit:$commit,build_time:$build_time,go_version:$go_version,os:$os,architecture:$arch,license:$license,source:$source,oci_image:$oci_image}' \
    >"${stage}/build-metadata.json"
  chmod 0644 "${stage}/build-metadata.json"
  tar --sort=name --format=ustar --owner=0 --group=0 --numeric-owner --mtime="@${epoch}" \
    --mode='u+rwX,go+rX,go-w' -C "${stage}" -cf - LICENSE README.md build-metadata.json sysbox sysbox-init \
    | gzip -n -9 >"${output}/${archive}"
  rm -rf "${stage}"
done

(cd "${output}" && sha256sum "sysbox_${tag}_linux_amd64.tar.gz" "sysbox_${tag}_linux_arm64.tar.gz" >SHA256SUMS)
targets='[]'
for arch in amd64 arm64; do
  archive="sysbox_${tag}_linux_${arch}.tar.gz"
  checksum="$(awk -v file="${archive}" '$2 == file {print $1}' "${output}/SHA256SUMS")"
  targets="$(jq -c --arg os linux --arg arch "${arch}" --arg archive "${archive}" --arg sha256 "${checksum}" '. + [{os:$os,architecture:$arch,archive:$archive,sha256:$sha256}]' <<<"${targets}")"
done
jq -S -n --arg version "${tag}" --arg tag "${tag}" --arg commit "${commit}" \
  --arg commit_time "${build_time}" --arg build_time "${build_time}" --arg go_version "${go_version}" \
  --arg license MulanPSL-2.0 --arg source "${source_url}" --arg oci_image "${oci_image}" --argjson targets "${targets}" \
  '{version:$version,tag:$tag,commit:$commit,commit_time:$commit_time,build_time:$build_time,go_version:$go_version,license:$license,source:$source,oci_image:$oci_image,targets:$targets}' \
  >"${output}/build-metadata.json"
echo "release: built ${tag} artifacts in ${output}"
