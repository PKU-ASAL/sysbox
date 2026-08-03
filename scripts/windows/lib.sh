#!/usr/bin/env bash

windows_die() {
  printf 'windows image tooling: %s\n' "$*" >&2
  return 1
}

windows_require_command() {
  command -v "$1" >/dev/null 2>&1 || windows_die "required command not found: $1"
}

windows_validate_password() {
  local password="$1" classes=0
  [[ ${#password} -ge 12 ]] || windows_die 'WINDOWS_ADMIN_PASSWORD must be at least 12 characters'
  [[ "${password}" != *$'\n'* && "${password}" != *$'\r'* ]] || windows_die 'WINDOWS_ADMIN_PASSWORD must not contain newlines'
  [[ "${password}" =~ [[:lower:]] ]] && classes=$((classes + 1))
  [[ "${password}" =~ [[:upper:]] ]] && classes=$((classes + 1))
  [[ "${password}" =~ [[:digit:]] ]] && classes=$((classes + 1))
  [[ "${password}" =~ [^[:alnum:]] ]] && classes=$((classes + 1))
  (( classes >= 3 )) || windows_die 'WINDOWS_ADMIN_PASSWORD must contain characters from at least three of four classes: lowercase, uppercase, digits, symbols'
}

windows_xml_escape() {
  sed -e 's/&/\&amp;/g' \
    -e 's/</\&lt;/g' \
    -e 's/>/\&gt;/g' \
    -e 's/"/\&quot;/g' \
    -e "s/'/\\\&apos;/g"
}

windows_replace_literal() {
  local value="$1" needle="$2" replacement="$3" prefix
  while [[ "${value}" == *"${needle}"* ]]; do
    prefix="${value%%"${needle}"*}"
    printf '%s%s' "${prefix}" "${replacement}"
    value="${value#*"${needle}"}"
  done
  printf '%s' "${value}"
}

windows_absolute_path() {
  local path="$1" directory basename
  directory="$(cd "$(dirname "${path}")" && pwd)" || return 1
  basename="$(basename "${path}")"
  printf '%s/%s\n' "${directory}" "${basename}"
}

windows_validate_sha256() {
  [[ "$1" =~ ^[[:xdigit:]]{64}$ ]] || windows_die "$2 must be a 64-character SHA-256 digest"
}

windows_validate_microsoft_url() {
  local url="$1"
  if [[ "${WINDOWS_ALLOW_FILE_URL:-0}" == 1 && "${url}" == file://* ]]; then
    return 0
  fi
  [[ "${url}" =~ ^https://([[:alnum:]-]+\.)*(microsoft\.com|microsoft\.net)/ ]] || \
    windows_die 'WINDOWS_ISO_URL must use HTTPS and a Microsoft download host'
}

windows_validate_fedora_url() {
  local url="$1"
  if [[ "${WINDOWS_ALLOW_FILE_URL:-0}" == 1 && "${url}" == file://* ]]; then
    return 0
  fi
  [[ "${url}" =~ ^https://([[:alnum:]-]+\.)*(fedorapeople\.org|fedoraproject\.org)/ ]] || \
    windows_die 'VIRTIO_ISO_URL must use HTTPS and a Fedora Project download host'
}

windows_download_verified() {
  local url="$1" sha256="$2" destination="$3" label="$4" directory tmp
  windows_validate_sha256 "${sha256}" "${label} checksum"
  directory="$(dirname "${destination}")"
  mkdir -p "${directory}"
  if [[ -f "${destination}" ]] && printf '%s  %s\n' "${sha256}" "${destination}" | sha256sum --check --status; then
    return 0
  fi
  tmp="$(mktemp "${directory}/.${label}.XXXXXX")"
  if ! curl --fail --location --output "${tmp}" "${url}"; then
    rm -f "${tmp}"
    return 1
  fi
  if ! printf '%s  %s\n' "${sha256}" "${tmp}" | sha256sum --check --status; then
    rm -f "${tmp}"
    windows_die "${label} checksum mismatch"
    return 1
  fi
  chmod 0644 "${tmp}"
  mv "${tmp}" "${destination}"
}
