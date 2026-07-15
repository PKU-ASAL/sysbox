#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "${repo_root}/scripts/release/lib.sh"

operation="${1:-}"
[[ -n "${operation}" ]] && shift || true
tag=""
dist="${repo_root}/dist"
while (($#)); do
  case "$1" in
    --tag) tag="${2:-}"; shift 2 ;;
    --dist) dist="${2:-}"; shift 2 ;;
    *) echo "usage: $0 <preflight|publish|audit> --tag vMAJOR.MINOR.PATCH [--dist DIR]" >&2; exit 2 ;;
  esac
done

validate_version "${tag}" || { echo "release: invalid Forgejo tag ${tag}" >&2; exit 1; }
for command in curl jq sha256sum; do require_command "${command}"; done
: "${RELEASE_TOKEN:?release: RELEASE_TOKEN is required}"
: "${FORGEJO_API_URL:?release: FORGEJO_API_URL is required}"
: "${FORGEJO_REPOSITORY:?release: FORGEJO_REPOSITORY is required}"

api="${FORGEJO_API_URL%/}"
auth="$(mktemp)"
chmod 0600 "${auth}"
printf 'header = "Authorization: token %s"\n' "${RELEASE_TOKEN}" >"${auth}"
trap 'rm -f "${auth}"' EXIT

api_status() {
  local output="$1" url="$2"
  curl -sS --config "${auth}" -o "${output}" -w '%{http_code}' "${url}"
}

preflight() {
  local response status
  response="$(mktemp)"
  status="$(api_status "${response}" "${api}/repos/${FORGEJO_REPOSITORY}/releases/tags/${tag}")"
  rm -f "${response}"
  case "${status}" in
    404) ;;
    200) echo "release: Forgejo release already exists for ${tag}" >&2; return 1 ;;
    *) echo "release: Forgejo release preflight returned HTTP ${status}" >&2; return 1 ;;
  esac
}

release_id() {
  curl -fsS --config "${auth}" "${api}/repos/${FORGEJO_REPOSITORY}/releases/tags/${tag}" | jq -er '.id'
}

assets() {
  local version
  version="$(version_without_v "${tag}")"
  printf '%s\n' \
    "sysbox_${tag}_linux_amd64.tar.gz" \
    "sysbox_${tag}_linux_arm64.tar.gz" \
    SHA256SUMS \
    build-metadata.json
}

publish() {
  preflight
  "${repo_root}/scripts/release/verify.sh" "${dist}"
  local payload response id name encoded
  payload="$(jq -n --arg tag "${tag}" --arg target "$(release_commit)" \
    '{tag_name:$tag,target_commitish:$target,name:("Sysbox " + $tag),body:("Automated Sysbox release " + $tag + ". Verify downloaded archives with SHA256SUMS."),draft:false,prerelease:false}')"
  response="$(mktemp)"
  curl -fsS --config "${auth}" -H 'Content-Type: application/json' --data "${payload}" \
    "${api}/repos/${FORGEJO_REPOSITORY}/releases" >"${response}"
  id="$(jq -er '.id' "${response}")"
  rm -f "${response}"
  while IFS= read -r name; do
    [[ -s "${dist}/${name}" ]] || { echo "release: missing asset ${name}" >&2; return 1; }
    encoded="$(jq -rn --arg value "${name}" '$value|@uri')"
    curl -fsS --config "${auth}" -H 'Content-Type: application/octet-stream' \
      --data-binary "@${dist}/${name}" \
      "${api}/repos/${FORGEJO_REPOSITORY}/releases/${id}/assets?name=${encoded}" >/dev/null
  done < <(assets)
  audit
}

audit() {
  local id response expected actual name url downloaded
  id="$(release_id)"
  response="$(mktemp)"
  curl -fsS --config "${auth}" "${api}/repos/${FORGEJO_REPOSITORY}/releases/${id}/assets" >"${response}"
  expected="$(assets | sort)"
  actual="$(jq -r '.[].name' "${response}" | sort)"
  [[ "${actual}" == "${expected}" ]] || { echo "release: Forgejo asset set mismatch" >&2; rm -f "${response}"; return 1; }
  while IFS= read -r name; do
    url="$(jq -er --arg name "${name}" '.[] | select(.name == $name) | .browser_download_url' "${response}")"
    downloaded="$(mktemp)"
    curl -fsS --config "${auth}" "${url}" -o "${downloaded}"
    cmp "${dist}/${name}" "${downloaded}" || { echo "release: downloaded asset differs: ${name}" >&2; rm -f "${downloaded}" "${response}"; return 1; }
    rm -f "${downloaded}"
  done < <(assets)
  rm -f "${response}"
  echo "release: audited Forgejo release ${tag}"
}

case "${operation}" in
  preflight) preflight ;;
  publish) publish ;;
  audit) audit ;;
  *) echo "usage: $0 <preflight|publish|audit> --tag vMAJOR.MINOR.PATCH [--dist DIR]" >&2; exit 2 ;;
esac
