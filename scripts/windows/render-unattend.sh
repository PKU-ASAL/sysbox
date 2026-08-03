#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "${script_dir}/lib.sh"

if [[ $# -ne 1 ]]; then
  windows_die "usage: WINDOWS_ADMIN_PASSWORD=... $0 OUTPUT"
  exit 2
fi

password="${WINDOWS_ADMIN_PASSWORD:-}"
windows_validate_password "${password}"
output="$1"
template="${script_dir}/Autounattend.xml.tmpl"
mkdir -p "$(dirname "${output}")"
umask 077
tmp="$(mktemp "$(dirname "${output}")/.Autounattend.XXXXXX")"
trap 'rm -f "${tmp}"' EXIT

escaped_password="$(printf '%s' "${password}" | windows_xml_escape)"
while IFS= read -r line || [[ -n "${line}" ]]; do
  windows_replace_literal "${line}" '__WINDOWS_ADMIN_PASSWORD__' "${escaped_password}"
  printf '\n'
done <"${template}" >"${tmp}"
chmod 0600 "${tmp}"
mv "${tmp}" "${output}"
trap - EXIT
