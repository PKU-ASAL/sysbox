#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "${script_dir}/lib.sh"

for command_name in genisoimage qemu-img setfacl virsh virt-ls; do
  windows_require_command "${command_name}"
done

running_kernel="$(uname -r)"
if [[ -z "${SUPERMIN_KERNEL:-}" && -r "/boot/vmlinuz-${running_kernel}" && -d "/lib/modules/${running_kernel}" ]]; then
  export SUPERMIN_KERNEL="/boot/vmlinuz-${running_kernel}"
  export SUPERMIN_MODULES="/lib/modules/${running_kernel}"
fi

password="${WINDOWS_ADMIN_PASSWORD:-}"
windows_validate_password "${password}"

verify_windows_disk() {
  local candidate="$1"
  qemu-img check "${candidate}" >/dev/null && \
    virt-ls -a "${candidate}" /Windows/System32 >/dev/null && \
    virt-ls -a "${candidate}" / | grep -Fxq 'sysbox-image-build.complete'
}

cache_root="${SYSBOX_CACHE:-${XDG_CACHE_HOME:-${HOME}/.cache}/sysbox}"
media_dir="${cache_root}/windows"
windows_iso="${WINDOWS_ISO:-${media_dir}/windows-10-22h2-english-x64.iso}"
virtio_iso="${VIRTIO_ISO:-${media_dir}/virtio-win-0.1.285.iso}"
output="${WINDOWS_QCOW2:-${media_dir}/windows-10-pro-22h2-amd64.qcow2}"
output_dir="$(dirname "${output}")"
failed_output="${output%.qcow2}.failed.qcow2"
domain_name="${WINDOWS_BUILD_DOMAIN:-sysbox-build-windows10}"
timeout="${WINDOWS_BUILD_TIMEOUT:-7200}"
poll_interval="${WINDOWS_BUILD_POLL_INTERVAL:-10}"

[[ -f "${windows_iso}" ]] || windows_die "Windows ISO not found: ${windows_iso}"
[[ -f "${virtio_iso}" ]] || windows_die "VirtIO ISO not found: ${virtio_iso}"
[[ "${domain_name}" =~ ^sysbox-build-[a-z0-9-]+$ ]] || windows_die 'WINDOWS_BUILD_DOMAIN must start with sysbox-build- and contain only lowercase letters, digits, and hyphens'
mkdir -p "${output_dir}"
if [[ "${WINDOWS_KEEP_FAILED_IMAGE:-0}" == 1 && -e "${failed_output}" ]]; then
  windows_die "refusing to overwrite existing failed image: ${failed_output}"
  exit 1
fi

if virsh dominfo "${domain_name}" >/dev/null 2>&1; then
  windows_die "domain ${domain_name} already exists; refusing to reuse or delete it"
  exit 1
fi

if [[ -f "${output}" ]]; then
  if verify_windows_disk "${output}"; then
    "${script_dir}/sanitize-image.sh" "${output}"
    printf '%s\n' "${output}"
    exit 0
  fi
  windows_die "existing Windows baseline failed verification: ${output}; remove or replace it explicitly"
  exit 1
fi

build_dir="$(mktemp -d /tmp/sysbox-windows10-build.XXXXXX)"
chmod 0711 "${build_dir}"
answer_xml="${build_dir}/Autounattend.xml"
answer_iso="${build_dir}/answer.iso"
disk="${build_dir}/windows10.qcow2"
domain_xml="${build_dir}/domain.xml"
installer_iso="${build_dir}/windows.iso"
driver_iso="${build_dir}/virtio-win.iso"
domain_defined=false
tmp_output=''

cleanup() {
  local existing_xml
  if [[ "${domain_defined}" == true ]]; then
    existing_xml="$(virsh dumpxml "${domain_name}" 2>/dev/null || true)"
    if [[ "${existing_xml}" == *'<sysbox:kind>windows-image-build</sysbox:kind>'* ]]; then
      virsh destroy "${domain_name}" >/dev/null 2>&1 || true
      virsh undefine "${domain_name}" >/dev/null 2>&1 || true
    fi
  fi
  [[ -z "${tmp_output}" ]] || rm -f "${tmp_output}"
  rm -rf "${build_dir}"
}
trap cleanup EXIT

WINDOWS_ADMIN_PASSWORD="${password}" "${script_dir}/render-unattend.sh" "${answer_xml}"
cp "${windows_iso}" "${installer_iso}"
cp "${virtio_iso}" "${driver_iso}"
chmod 0644 "${installer_iso}" "${driver_iso}"
(
  cd "${build_dir}"
  genisoimage -quiet -output "${answer_iso}" -volid OEMDRV -joliet -rock Autounattend.xml
)
chmod 0600 "${answer_iso}"
setfacl -m u:libvirt-qemu:r "${answer_iso}"
rm -f "${answer_xml}"

qemu-img create -f qcow2 "${disk}" 64G >/dev/null
chmod 0600 "${disk}"
setfacl -m u:libvirt-qemu:rw "${disk}"
"${script_dir}/render-domain.sh" "${domain_name}" "${installer_iso}" "${driver_iso}" "${answer_iso}" "${disk}" >"${domain_xml}"
chmod 0600 "${domain_xml}"

domain_defined=true
virsh define "${domain_xml}" >/dev/null
defined_xml="$(virsh dumpxml "${domain_name}")"
[[ "${defined_xml}" == *'<sysbox:kind>windows-image-build</sysbox:kind>'* ]] || windows_die 'defined installer domain is missing the Sysbox ownership marker'
virsh start "${domain_name}" >/dev/null

deadline=$(( $(date +%s) + timeout ))
while :; do
  state="$(virsh domstate "${domain_name}" | tr '[:upper:]' '[:lower:]' | xargs)"
  if [[ "${state}" == 'shut off' ]]; then
    break
  fi
  if (( $(date +%s) >= deadline )); then
    windows_die "Windows installation did not shut down within ${timeout} seconds; inspect with: virsh vncdisplay ${domain_name}"
    exit 1
  fi
  sleep "${poll_interval}"
done

virsh undefine "${domain_name}" >/dev/null
domain_defined=false
if ! verify_windows_disk "${disk}"; then
  if [[ "${WINDOWS_KEEP_FAILED_IMAGE:-0}" == 1 ]]; then
    chmod 0600 "${disk}"
    "${script_dir}/sanitize-image.sh" "${disk}"
    mv "${disk}" "${failed_output}"
    chmod 0600 "${failed_output}"
  fi
  windows_die 'Windows installation verification failed: the offline guest is missing Windows/System32 or the build completion marker'
  exit 1
fi
"${script_dir}/sanitize-image.sh" "${disk}"
tmp_output="$(mktemp "${output_dir}/.$(basename "${output}").XXXXXX")"
mv "${disk}" "${tmp_output}"
chmod 0644 "${tmp_output}"
[[ ! -e "${output}" ]] || windows_die "refusing to overwrite existing Windows baseline: ${output}"
mv "${tmp_output}" "${output}"
tmp_output=''
printf '%s\n' "${output}"
