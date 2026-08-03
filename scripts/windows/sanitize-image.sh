#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "${script_dir}/lib.sh"

[[ $# -eq 1 ]] || { windows_die "usage: $0 WINDOWS_QCOW2"; exit 2; }
image="$1"
[[ -f "${image}" ]] || windows_die "Windows qcow2 not found: ${image}"

for command_name in qemu-img virt-customize virt-ls virt-win-reg; do
  windows_require_command "${command_name}"
done

work="$(mktemp -d /tmp/sysbox-windows-sanitize.XXXXXX)"
trap 'rm -rf "${work}"' EXIT
reg_file="${work}/clear-autologon.reg"
cat >"${reg_file}" <<'EOF'
Windows Registry Editor Version 5.00

[HKEY_LOCAL_MACHINE\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Winlogon]
"AutoAdminLogon"=-
"AutoLogonCount"=-
"DefaultUserName"=-
"DefaultPassword"=-
EOF
chmod 0600 "${reg_file}"

virt-win-reg --merge "${image}" "${reg_file}"
virt-customize -a "${image}" \
  --delete '/Windows/Panther'

windows_listing="${work}/windows-files.txt"
if ! virt-ls -a "${image}" /Windows >"${windows_listing}"; then
  windows_die 'Windows image sanitization could not audit the Windows directory'
  exit 1
fi
if grep -Fixq 'Panther' "${windows_listing}"; then
  windows_die 'Windows image sanitization left unattended setup artifacts'
  exit 1
fi

winlogon="${work}/winlogon.reg"
virt-win-reg --unsafe-printable-strings "${image}" \
  'HKLM\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Winlogon' >"${winlogon}"
if grep -Eiq '^"(AutoAdminLogon|AutoLogonCount|DefaultUserName|DefaultPassword)"=' "${winlogon}"; then
  windows_die 'Windows image sanitization left automatic logon values'
  exit 1
fi
qemu-img check "${image}" >/dev/null
