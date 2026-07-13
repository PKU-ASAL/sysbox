#!/usr/bin/env bash
set -euo pipefail

url="https://cloud-images.ubuntu.com/releases/noble/release-20260615/ubuntu-24.04-server-cloudimg-amd64.img"
sha256="5fa5b05e5ec239858c4531485d6023b0896448c2df7c63b34f8dae6ea6051a44"
cache_root="${SYSBOX_CACHE:-${XDG_CACHE_HOME:-${HOME}/.cache}/sysbox}"
dir="${cache_root}/libvirt"
image="${dir}/ubuntu-24.04-server-cloudimg-amd64-20260615.img"

mkdir -p "${dir}"
if [[ -f "${image}" ]] && printf '%s  %s\n' "${sha256}" "${image}" | sha256sum --check --status; then
  printf '%s\n' "${image}"
  exit 0
fi

tmp="$(mktemp "${dir}/.ubuntu-cloud.XXXXXX")"
cleanup() { rm -f "${tmp}"; }
trap cleanup EXIT
curl --fail --location --output "${tmp}" "${url}"
printf '%s  %s\n' "${sha256}" "${tmp}" | sha256sum --check --status
chmod 0644 "${tmp}"
mv "${tmp}" "${image}"
trap - EXIT
printf '%s\n' "${image}"
