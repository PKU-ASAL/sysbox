#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "${repo_root}/scripts/release/lib.sh"

operation="${1:-}"
[[ -n "${operation}" ]] && shift || true
tag=""
dist="${repo_root}/dist"
repository="${GITHUB_REPOSITORY:-PKU-ASAL/sysbox}"
while (($#)); do
  case "$1" in
    --tag) tag="${2:-}"; shift 2 ;;
    --dist) dist="${2:-}"; shift 2 ;;
    --repository) repository="${2:-}"; shift 2 ;;
    *) echo "usage: $0 <preflight|publish|audit|reconcile> --tag vMAJOR.MINOR.PATCH [--dist DIR] [--repository OWNER/REPO]" >&2; exit 2 ;;
  esac
done

validate_version "${tag}" || { echo "release: invalid GitHub tag ${tag}" >&2; exit 1; }
[[ "${repository}" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || { echo "release: invalid GitHub repository ${repository}" >&2; exit 1; }
: "${GH_TOKEN:?release: GH_TOKEN is required}"
for command in gh jq cmp mktemp; do require_command "${command}"; done

assets() {
  printf '%s\n' \
    "sysbox_${tag}_linux_amd64.tar.gz" \
    "sysbox_${tag}_linux_arm64.tar.gz" \
    SHA256SUMS \
    build-metadata.json
}

validate_metadata() {
  local file="$1"
  validate_release_fingerprint "${file}" || return 1
  jq -e --arg tag "${tag}" --arg commit "$(release_commit)" --arg repository "${repository}" '
    .version == $tag and .commit == $commit and .release_repository == $repository and
    (.oci_digest | type == "string" and test("^sha256:[0-9a-f]{64}$")) and
    ([.targets[] | select(.os == "linux") | .architecture] | sort == ["amd64","arm64"])
  ' "${file}" >/dev/null || { echo "release: invalid final build metadata" >&2; return 1; }
}

remote_tag_commit() {
  local encoded="${tag//\//%2F}" response type sha depth=0
  response="$(gh api "repos/${repository}/git/ref/tags/${encoded}")"
  type="$(jq -er '.object.type' <<<"${response}")"
  sha="$(jq -er '.object.sha' <<<"${response}")"
  while [[ "${type}" == tag ]]; do
    ((depth++ < 4)) || { echo "release: annotated tag indirection is too deep" >&2; return 1; }
    response="$(gh api "repos/${repository}/git/tags/${sha}")"
    type="$(jq -er '.object.type' <<<"${response}")"
    sha="$(jq -er '.object.sha' <<<"${response}")"
  done
  [[ "${type}" == commit ]] || { echo "release: remote tag does not resolve to a commit" >&2; return 1; }
  printf '%s\n' "${sha}"
}

verify_remote_tag() {
  local actual
  actual="$(remote_tag_commit)" || return 1
  [[ "${actual}" == "$(release_commit)" ]] || { echo "release: remote tag commit mismatch: ${actual}" >&2; return 1; }
}

preflight() {
  local output
  if output="$(gh release view "${tag}" --repo "${repository}" 2>&1)"; then
    echo "release: GitHub Release already exists: ${repository}@${tag}" >&2
    return 1
  fi
  case "${output,,}" in
    *"release not found"*|*"not found"*|*"http 404"*) ;;
    *) echo "release: cannot prove GitHub Release is absent: ${output}" >&2; return 1 ;;
  esac
}

audit() {
  local tmp response expected actual name
  tmp="$(mktemp -d)"
  response="${tmp}/release.json"
  verify_remote_tag
  gh release view "${tag}" --repo "${repository}" \
    --json tagName,isDraft,isPrerelease,assets >"${response}"
  jq -e --arg tag "${tag}" \
    '.tagName == $tag and .isDraft == false and .isPrerelease == false' \
    "${response}" >/dev/null
  expected="$(assets | sort)"
  actual="$(jq -r '.assets[].name' "${response}" | sort)"
  [[ "${actual}" == "${expected}" ]] || { echo "release: GitHub asset set mismatch" >&2; rm -rf "${tmp}"; return 1; }
  mkdir "${tmp}/download"
  gh release download "${tag}" --repo "${repository}" --dir "${tmp}/download"
  while IFS= read -r name; do
    cmp "${dist}/${name}" "${tmp}/download/${name}" || { echo "release: downloaded asset differs: ${name}" >&2; rm -rf "${tmp}"; return 1; }
  done < <(assets)
  validate_metadata "${tmp}/download/build-metadata.json" || { rm -rf "${tmp}"; return 1; }
  rm -rf "${tmp}"
  echo "release: audited GitHub Release ${repository}@${tag}"
}

publish() {
  local files=() name
  require_release_repo "${tag}"
  verify_remote_tag
  "${repo_root}/scripts/release/verify.sh" "${dist}"
  validate_metadata "${dist}/build-metadata.json"
  preflight
  while IFS= read -r name; do
    [[ -s "${dist}/${name}" ]] || { echo "release: missing asset ${name}" >&2; return 1; }
    files+=("${dist}/${name}")
  done < <(assets)
  gh release create "${tag}" --repo "${repository}" --target "$(release_commit)" \
    --title "Sysbox ${tag}" --notes "Verified Sysbox ${tag} binaries, checksums, build metadata, and GHCR runtime identity." \
    "${files[@]}"
  audit
}

reconcile() {
  local output tmp actual expected name
  if output="$(gh release view "${tag}" --repo "${repository}" 2>&1)"; then
    require_release_repo "${tag}"
    verify_remote_tag
    "${repo_root}/scripts/release/verify.sh" "${dist}"
    validate_metadata "${dist}/build-metadata.json"
    tmp="$(mktemp -d)"
    gh release view "${tag}" --repo "${repository}" --json assets >"${tmp}/assets.json"
    actual="$(jq -r '.assets[].name' "${tmp}/assets.json")"
    while IFS= read -r name; do
      [[ -n "${name}" ]] || continue
      if ! assets | grep -Fx "${name}" >/dev/null; then
        echo "release: unexpected existing GitHub asset: ${name}" >&2; rm -rf "${tmp}"; return 1
      fi
      mkdir "${tmp}/existing-${name}"
      gh release download "${tag}" --repo "${repository}" --pattern "${name}" --dir "${tmp}/existing-${name}"
      cmp "${dist}/${name}" "${tmp}/existing-${name}/${name}" || { echo "release: existing GitHub asset differs: ${name}" >&2; rm -rf "${tmp}"; return 1; }
    done <<<"${actual}"
    while IFS= read -r expected; do
      if ! grep -Fx "${expected}" <<<"${actual}" >/dev/null; then
        gh release upload "${tag}" "${dist}/${expected}" --repo "${repository}"
      fi
    done < <(assets)
    rm -rf "${tmp}"
    audit
    return
  fi
  case "${output,,}" in
    *"release not found"*|*"not found"*|*"http 404"*) publish ;;
    *) echo "release: cannot inspect GitHub Release: ${output}" >&2; return 1 ;;
  esac
}

case "${operation}" in
  preflight) preflight ;;
  publish) publish ;;
  audit) audit ;;
  reconcile) reconcile ;;
  *) echo "usage: $0 <preflight|publish|audit|reconcile> --tag vMAJOR.MINOR.PATCH [--dist DIR] [--repository OWNER/REPO]" >&2; exit 2 ;;
esac
