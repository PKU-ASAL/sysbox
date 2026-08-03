#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "${script_dir}/lib.sh"

windows_require_command curl
windows_require_command sha256sum

windows_url="${WINDOWS_ISO_URL:-}"
windows_sha="${WINDOWS_ISO_SHA256:-}"
virtio_url="${VIRTIO_ISO_URL:-https://fedorapeople.org/groups/virt/virtio-win/direct-downloads/archive-virtio/virtio-win-0.1.285-1/virtio-win-0.1.285.iso}"
virtio_sha="${VIRTIO_ISO_SHA256:-e14cf2b94492c3e925f0070ba7fdfedeb2048c91eea9c5a5afb30232a3976331}"

[[ -n "${windows_url}" ]] || windows_die 'WINDOWS_ISO_URL is required; generate a temporary URL at https://www.microsoft.com/en-us/software-download/windows10ISO'
[[ -n "${windows_sha}" ]] || windows_die 'WINDOWS_ISO_SHA256 is required'
windows_validate_microsoft_url "${windows_url}"
windows_validate_fedora_url "${virtio_url}"

cache_root="${SYSBOX_CACHE:-${XDG_CACHE_HOME:-${HOME}/.cache}/sysbox}"
media_dir="${cache_root}/windows"
windows_iso="${media_dir}/windows-10-22h2-english-x64.iso"
virtio_iso="${media_dir}/virtio-win-0.1.285.iso"

windows_download_verified "${windows_url}" "${windows_sha}" "${windows_iso}" windows-iso
windows_download_verified "${virtio_url}" "${virtio_sha}" "${virtio_iso}" virtio-iso

printf 'WINDOWS_ISO=%s\n' "${windows_iso}"
printf 'VIRTIO_ISO=%s\n' "${virtio_iso}"
