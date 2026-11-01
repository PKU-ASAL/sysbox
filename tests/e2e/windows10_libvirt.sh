#!/usr/bin/env bash
# Run on the isolated owning host against an already published Windows revision.
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "${root}/tests/e2e/service_host_check.sh"
: "${SYSBOX_WINDOWS_REVISION:?publish a digest-pinned Windows image revision first}"
[[ "$SYSBOX_WINDOWS_REVISION" =~ ^sha256:[a-f0-9]{64}$ ]] || { echo 'revision must be a SHA256 digest' >&2; exit 1; }
command -v virsh >/dev/null
topology="sysbox-windows-${RANDOM}-${RANDOM}"
domain="sysbox-lab-${topology}-node-windows10"
client=("$SYSBOX_E2E_BINARY" --topology "$topology")
trap 'echo "Windows acceptance failed; preserve topology ${topology} and its Runs for recovery" >&2' ERR
if virsh dominfo "$domain" >/dev/null 2>&1; then
  echo 'refusing existing test domain' >&2; exit 1
fi
"${client[@]}" apply --revision "$SYSBOX_WINDOWS_REVISION" --auto-approve
[[ "$(virsh domstate "$domain" | xargs)" == running ]]
old_uuid="$(virsh domuuid "$domain")"
old_disk="$(virsh domblklist "$domain" --details | awk '$2 == "disk" {print $4; exit}')"
test -n "$old_disk"
"${client[@]}" plan --revision "$SYSBOX_WINDOWS_REVISION" | jq -e 'all(.actions[]; .action == "no-op")'
"${client[@]}" reset --revision "$SYSBOX_WINDOWS_REVISION" --auto-approve
new_uuid="$(virsh domuuid "$domain")"
[[ "$new_uuid" != "$old_uuid" ]]
[[ "$(virsh domstate "$domain" | xargs)" == running ]]
new_disk="$(virsh domblklist "$domain" --details | awk '$2 == "disk" {print $4; exit}')"
test -n "$new_disk"
"${client[@]}" destroy --auto-approve
! virsh dominfo "$old_uuid" >/dev/null 2>&1
! virsh dominfo "$new_uuid" >/dev/null 2>&1
[[ ! -e "$old_disk" && ! -e "$new_disk" ]]
trap - ERR
printf 'Windows domain-start API lifecycle passed; guest task/QGA matrix is a separate gate.\n'
