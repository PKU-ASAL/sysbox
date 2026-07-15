#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "${repo_root}/scripts/release/lib.sh"

operation="${1:-}"
[[ -n "${operation}" ]] && shift || true
tag=""
image="${OCI_IMAGE:-}"
while (($#)); do
  case "$1" in
    --tag) tag="${2:-}"; shift 2 ;;
    --image) image="${2:-}"; shift 2 ;;
    *) echo "usage: $0 <preflight|build|verify> --tag vMAJOR.MINOR.PATCH --image REGISTRY/OWNER/IMAGE" >&2; exit 2 ;;
  esac
done

validate_version "${tag}" || { echo "release: invalid OCI tag ${tag}" >&2; exit 1; }
[[ -n "${image}" && "${image}" == */* ]] || { echo "release: OCI image must include registry and repository" >&2; exit 1; }
require_command docker
docker buildx version >/dev/null

version="$(version_without_v "${tag}")"
immutable=("${image}:${tag}" "${image}:${version}")

assert_absent() {
  local ref="$1" output
  if output="$(docker buildx imagetools inspect "${ref}" 2>&1)"; then
    echo "release: OCI tag already exists: ${ref}" >&2
    return 1
  fi
  case "${output,,}" in
    *"manifest unknown"*|*"name unknown"*|*"not found"*) ;;
    *) echo "release: cannot prove OCI tag is absent: ${ref}: ${output}" >&2; return 1 ;;
  esac
}

preflight() {
  local output
  docker info >/dev/null
  for ref in "${immutable[@]}"; do
    assert_absent "${ref}"
  done
}

verify() {
  local ref="${image}:${tag}" raw labels
  raw="$(docker buildx imagetools inspect --raw "${ref}")"
  jq -e '[.manifests[].platform | "\(.os)/\(.architecture)"] | sort == ["linux/amd64","linux/arm64"]' <<<"${raw}" >/dev/null
  labels="$(docker buildx imagetools inspect "${ref}" --format '{{json .Image.Config.Labels}}')"
  jq -e --arg version "${tag}" --arg revision "$(release_commit)" \
    '."org.opencontainers.image.version" == $version and ."org.opencontainers.image.revision" == $revision and ."org.opencontainers.image.licenses" == "MulanPSL-2.0"' \
    <<<"${labels}" >/dev/null
  verified_digest="sha256:$(printf '%s' "${raw}" | sha256sum | awk '{print $1}')"
  echo "release: verified OCI image ${ref}"
}

manifest_sha() {
  local raw
  raw="$(docker buildx imagetools inspect --raw "$1")"
  printf '%s' "${raw}" | sha256sum | awk '{print $1}'
}

promote() {
  local expected_digest="$1" source_ref="${image}:${tag}" source_sha promoted_sha ref index
  source_sha="$(manifest_sha "${source_ref}")"
  [[ "sha256:${source_sha}" == "${expected_digest}" ]] || { echo "release: immutable OCI source changed before promotion" >&2; return 1; }
  mapfile -t refs < <(oci_tags "${tag}" "${image}")
  for index in "${!refs[@]}"; do
    ((index > 0)) || continue
    ref="${refs[${index}]}"
    if ((index == 1)); then
      assert_absent "${ref}"
    fi
    docker buildx imagetools create --tag "${ref}" "${source_ref}" >/dev/null
    promoted_sha="$(manifest_sha "${ref}")"
    [[ "${promoted_sha}" == "${source_sha}" ]] || { echo "release: promoted OCI tag differs from immutable manifest: ${ref}" >&2; return 1; }
    [[ "$(manifest_sha "${source_ref}")" == "${source_sha}" ]] || { echo "release: OCI source tag changed during promotion" >&2; return 1; }
  done
}

record_digest() {
  local digest="$1" metadata="${BUILD_METADATA:-}"
  [[ -n "${metadata}" ]] || return 0
  [[ -f "${metadata}" ]] || { echo "release: BUILD_METADATA does not exist: ${metadata}" >&2; return 1; }
  local tmp
  tmp="$(mktemp)"
  jq --arg digest "${digest}" '.oci_digest = $digest' "${metadata}" >"${tmp}"
  mv "${tmp}" "${metadata}"
}

case "${operation}" in
  preflight)
    preflight
    ;;
  build)
    preflight
    commit="$(release_commit)"
    created="$(release_time)"
    docker buildx build --platform linux/amd64,linux/arm64 --provenance=false --push \
      --build-arg "VERSION=${tag}" --build-arg "REVISION=${commit}" \
      --build-arg "CREATED=${created}" --build-arg "SOURCE_URL=${SOURCE_URL:-https://git.pku.edu.cn/oslab/sysbox}" \
      --tag "${image}:${tag}" "${repo_root}"
    verify
    promote "${verified_digest}"
    record_digest "${verified_digest}"
    ;;
  verify)
    verify
    ;;
  *)
    echo "usage: $0 <preflight|build|verify> --tag vMAJOR.MINOR.PATCH --image REGISTRY/OWNER/IMAGE" >&2
    exit 2
    ;;
esac
