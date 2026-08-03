#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
config="${root}/tests/e2e/windows10/field.sysbox.hcl"
run_dir="$(mktemp -d /tmp/sysbox-windows10-e2e.XXXXXX)"
chmod 0755 "${run_dir}"
state="${run_dir}/state.json"
baseline="${run_dir}/windows10.qcow2"
topology="$(basename "${run_dir}" | tr '[:upper:]' '[:lower:]')"
domain="sysbox-lab-${topology}-node-windows10"
vm_dir=''

die() {
  printf 'windows10 libvirt e2e: %s\n' "$*" >&2
  exit 1
}

cleanup() {
  if [[ -f "${state}" ]]; then
    "${root}/bin/sysbox" --state "${state}" -f "${config}" destroy --auto-approve >/dev/null 2>&1 || true
  fi
  rm -rf "${run_dir}"
}
trap cleanup EXIT INT TERM

[[ -n "${SYSBOX_WINDOWS10_QCOW2:-}" ]] || die 'SYSBOX_WINDOWS10_QCOW2 is required'
[[ -f "${SYSBOX_WINDOWS10_QCOW2}" ]] || die "qcow2 not found: ${SYSBOX_WINDOWS10_QCOW2}"
for command_name in go qemu-img virsh; do
  command -v "${command_name}" >/dev/null 2>&1 || die "required command not found: ${command_name}"
done
qemu-img check "${SYSBOX_WINDOWS10_QCOW2}" >/dev/null
if virsh dominfo "${domain}" >/dev/null 2>&1; then
  die "domain ${domain} already exists; refusing to reuse it"
fi
cp --reflink=auto "${SYSBOX_WINDOWS10_QCOW2}" "${baseline}"
chmod 0644 "${baseline}"
export SYSBOX_WINDOWS10_QCOW2="${baseline}"

mkdir -p "${root}/bin"
(cd "${root}" && GOCACHE="${GOCACHE:-/tmp/sysbox-gocache}" CGO_ENABLED=0 go build -buildvcs=false -o bin/sysbox ./cmd/sysbox)

sysbox=("${root}/bin/sysbox" --state "${state}" -f "${config}")
"${sysbox[@]}" validate
"${sysbox[@]}" plan
"${sysbox[@]}" apply --auto-approve

[[ "$(virsh domstate "${domain}" | tr '[:upper:]' '[:lower:]' | xargs)" == 'running' ]] || die 'Windows domain is not running'
old_uuid="$(virsh domuuid "${domain}")"
disk_path="$(virsh domblklist "${domain}" --details | awk '$2 == "disk" {print $4; exit}')"
[[ -n "${disk_path}" ]] || die 'managed disk path was not found'
vm_dir="$(dirname "${disk_path}")"

plan_output="$("${sysbox[@]}" plan)"
printf '%s\n' "${plan_output}"
grep -Fq 'Plan: 0 to add, 0 to replace, 0 to destroy, 2 unchanged.' <<<"${plan_output}" || die 'second plan was not a no-op'

"${sysbox[@]}" reset --auto-approve
new_uuid="$(virsh domuuid "${domain}")"
[[ "${new_uuid}" != "${old_uuid}" ]] || die 'reset did not replace the domain identity'
[[ "$(virsh domstate "${domain}" | tr '[:upper:]' '[:lower:]' | xargs)" == 'running' ]] || die 'reset Windows domain is not running'

new_disk_path="$(virsh domblklist "${domain}" --details | awk '$2 == "disk" {print $4; exit}')"
new_vm_dir="$(dirname "${new_disk_path}")"
"${sysbox[@]}" destroy --auto-approve

! virsh dominfo "${domain}" >/dev/null 2>&1 || die 'domain remained after destroy'
[[ ! -e "${vm_dir}" ]] || die "old VM directory remained: ${vm_dir}"
[[ ! -e "${new_vm_dir}" ]] || die "reset VM directory remained: ${new_vm_dir}"
cleanup
trap - EXIT INT TERM

printf 'Windows 10 libvirt domain-start lifecycle passed with zero owned residue.\n'
