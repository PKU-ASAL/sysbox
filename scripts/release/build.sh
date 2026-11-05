#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "${repo_root}/scripts/release/lib.sh"
cd "${repo_root}"

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
require_clean_source
[[ "${allow_untagged}" == true ]] || require_release_repo "${tag}"
for command in go git jq tar gzip sha256sum date; do require_command "${command}"; done
tar --version | head -1 | grep -q 'GNU tar' || { echo "release: GNU tar is required" >&2; exit 1; }

commit="$(release_commit)"
epoch="$(release_epoch)"
build_time="$(release_time)"
go_version="$(go env GOVERSION)"
source_url="${SOURCE_URL:-https://github.com/PKU-ASAL/sysbox}"
oci_image="${OCI_IMAGE:-ghcr.io/pku-asal/sysbox}"
release_repository="${RELEASE_REPOSITORY:-PKU-ASAL/sysbox}"
ldflags="$(release_ldflags "${tag}" "${commit}" "${build_time}")"

[[ ! -e "${output}" && ! -L "${output}" ]] || { echo "release: output must be a new directory: ${output}" >&2; exit 1; }
mkdir -p "${output}"
declare -A sysbox_hashes init_hashes netns_hashes
for arch in amd64 arm64; do
  stage="${output}/.stage-${arch}"
  archive="sysbox_${tag}_linux_${arch}.tar.gz"
  mkdir -p "${stage}"
  GOARCH="${arch}" "${repo_root}/scripts/release/build-binaries.sh" "${stage}" "${ldflags}"
  sysbox_hashes["${arch}"]="$(sha256sum "${stage}/sysbox" | awk '{print $1}')"
  init_hashes["${arch}"]="$(sha256sum "${stage}/sysbox-init" | awk '{print $1}')"
  netns_hashes["${arch}"]="$(sha256sum "${stage}/sysbox-netns" | awk '{print $1}')"
  install -m 0644 README.md "${stage}/README.md"
  install -m 0644 LICENSE "${stage}/LICENSE"
  install -m 0755 deploy/install.py "${stage}/install.py"
  install -D -m 0644 deploy/systemd/sysbox-host.service "${stage}/deploy/systemd/sysbox-host.service"
  install -D -m 0644 deploy/systemd/sysbox-agent.service "${stage}/deploy/systemd/sysbox-agent.service"
  install -D -m 0644 deploy/docker/agent.yaml "${stage}/deploy/docker/agent.yaml"
  install -D -m 0644 deploy/systemd/sysbox-host.yaml "${stage}/deploy/systemd/sysbox-host.yaml"
  mkdir -p "${stage}/docs" "${stage}/examples"
  install -m 0644 docs/architecture.md docs/interfaces.md "${stage}/docs/"
  cp -R examples/docker-service "${stage}/examples/docker-service"
  # Deployment assets are from the same commit as the binaries, not a later checkout.
  cp -R deploy/docker "${stage}/deploy/"
  install -m 0755 deploy/prepare-worker.sh "${stage}/deploy/prepare-worker.sh"
  install -m 0644 .env.example "${stage}/.env.example"
  jq -S -n --arg version "${tag}" --arg tag "${tag}" --arg commit "${commit}" \
    --arg build_time "${build_time}" --arg go_version "${go_version}" --arg os linux --arg arch "${arch}" \
    --arg license MulanPSL-2.0 --arg source "${source_url}" --arg oci_image "${oci_image}" \
    '{version:$version,tag:$tag,commit:$commit,build_time:$build_time,go_version:$go_version,os:$os,architecture:$arch,license:$license,source:$source,oci_image:$oci_image}' \
    >"${stage}/build-metadata.json"
  chmod 0644 "${stage}/build-metadata.json"
  (cd "${stage}" && find . -type f ! -name INSTALL-SHA256SUMS -printf '%P\0' | LC_ALL=C sort -z | xargs -0 sha256sum > INSTALL-SHA256SUMS)
  tar --sort=name --format=ustar --owner=0 --group=0 --numeric-owner --mtime="@${epoch}" \
    --mode='u+rwX,go+rX,go-w' -C "${stage}" -cf - .env.example LICENSE README.md INSTALL-SHA256SUMS install.py deploy docs examples build-metadata.json sysbox sysbox-init sysbox-netns \
    | gzip -n -9 >"${output}/${archive}"
  rm -rf "${stage}"
done

# Windows durable-task helper is part of the same release identity. Build it
# from this checkout with the same source commit and deterministic timestamp;
# acceptance must consume this archive instead of compiling a helper on demand.
windows_stage="${output}/.stage-windows-amd64"
windows_archive="sysbox_${tag}_windows_amd64.tar.gz"
mkdir -p "${windows_stage}"
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -buildvcs=false \
  -ldflags "${ldflags}" -o "${windows_stage}/sysbox-windows-task.exe" ./cmd/sysbox-windows-task
install -m 0644 scripts/windows/task/install.ps1 "${windows_stage}/install.ps1"
jq -S -n --arg version "${tag}" --arg commit "${commit}" --arg build_time "${build_time}" \
  '{version:$version,commit:$commit,build_time:$build_time,os:"windows",architecture:"amd64",license:"MulanPSL-2.0"}' \
  >"${windows_stage}/build-metadata.json"
chmod 0644 "${windows_stage}/build-metadata.json"
tar --sort=name --format=ustar --owner=0 --group=0 --numeric-owner --mtime="@${epoch}" \
  --mode='u+rwX,go+rX,go-w' -C "${windows_stage}" -cf - build-metadata.json install.ps1 sysbox-windows-task.exe \
  | gzip -n -9 >"${output}/${windows_archive}"
windows_helper_hash="$(sha256sum "${windows_stage}/sysbox-windows-task.exe" | awk '{print $1}')"
windows_installer_hash="$(sha256sum "${windows_stage}/install.ps1" | awk '{print $1}')"
rm -rf "${windows_stage}"

(cd "${output}" && sha256sum "sysbox_${tag}_linux_amd64.tar.gz" "sysbox_${tag}_linux_arm64.tar.gz" >SHA256SUMS)
echo "$(sha256sum "${output}/${windows_archive}" | awk '{print $1}')  ${windows_archive}" >>"${output}/SHA256SUMS"
targets='[]'
for arch in amd64 arm64; do
  archive="sysbox_${tag}_linux_${arch}.tar.gz"
  checksum="$(awk -v file="${archive}" '$2 == file {print $1}' "${output}/SHA256SUMS")"
  targets="$(jq -c --arg os linux --arg arch "${arch}" --arg archive "${archive}" --arg sha256 "${checksum}" \
    --arg sysbox_sha256 "${sysbox_hashes[${arch}]}" --arg sysbox_init_sha256 "${init_hashes[${arch}]}" --arg sysbox_netns_sha256 "${netns_hashes[${arch}]}" \
    '. + [{os:$os,architecture:$arch,archive:$archive,sha256:$sha256,sysbox_sha256:$sysbox_sha256,sysbox_init_sha256:$sysbox_init_sha256,sysbox_netns_sha256:$sysbox_netns_sha256}]' <<<"${targets}")"
done
windows_checksum="$(awk -v file="${windows_archive}" '$2 == file {print $1}' "${output}/SHA256SUMS")"
targets="$(jq -c --arg os windows --arg arch amd64 --arg archive "${windows_archive}" --arg sha256 "${windows_checksum}" --arg helper_sha256 "${windows_helper_hash}" --arg installer_sha256 "${windows_installer_hash}" \
  '. + [{os:$os,architecture:$arch,archive:$archive,sha256:$sha256,helper_sha256:$helper_sha256,installer_sha256:$installer_sha256}]' <<<"${targets}")"
fingerprint_payload="$(jq -Scn --arg tag "${tag}" --arg commit "${commit}" --argjson targets "${targets}" '{tag:$tag,commit:$commit,targets:$targets}')"
release_fingerprint="$(printf '%s' "${fingerprint_payload}" | sha256sum | awk '{print $1}')"
jq -S -n --arg version "${tag}" --arg tag "${tag}" --arg commit "${commit}" \
  --arg commit_time "${build_time}" --arg build_time "${build_time}" --arg go_version "${go_version}" \
  --arg license MulanPSL-2.0 --arg source "${source_url}" --arg oci_image "${oci_image}" --arg release_repository "${release_repository}" --arg release_fingerprint "${release_fingerprint}" --argjson targets "${targets}" \
  '{version:$version,tag:$tag,commit:$commit,commit_time:$commit_time,build_time:$build_time,go_version:$go_version,license:$license,source:$source,oci_image:$oci_image,release_repository:$release_repository,release_fingerprint:$release_fingerprint,targets:$targets}' \
  >"${output}/build-metadata.json"
validate_release_fingerprint "${output}/build-metadata.json"
echo "release: built ${tag} artifacts in ${output}"
