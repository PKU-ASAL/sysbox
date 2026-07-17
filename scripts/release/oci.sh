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
    *) echo "usage: $0 <preflight|build|verify|reconcile> --tag vMAJOR.MINOR.PATCH --image REGISTRY/OWNER/IMAGE" >&2; exit 2 ;;
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
  local ref="${image}:${tag}" raw labels metadata="${BUILD_METADATA:-}" expected_fingerprint digest
  local child_digests=()
  [[ -f "${metadata}" ]] || { echo "release: BUILD_METADATA is required for OCI identity verification" >&2; return 1; }
  validate_release_fingerprint "${metadata}"
  expected_fingerprint="$(jq -er '.release_fingerprint | select(type == "string" and test("^[0-9a-f]{64}$"))' "${metadata}")"
  raw="$(docker buildx imagetools inspect --raw "${ref}")"
  jq -e '[.manifests[].platform | "\(.os)/\(.architecture)"] | sort == ["linux/amd64","linux/arm64"]' <<<"${raw}" >/dev/null
  mapfile -t child_digests < <(jq -er '.manifests[].digest | select(type == "string" and test("^sha256:[0-9a-f]{64}$"))' <<<"${raw}")
  [[ "${#child_digests[@]}" == 2 ]] || { echo "release: OCI image must contain two valid child manifest digests" >&2; return 1; }
  for digest in "${child_digests[@]}"; do
    labels="$(docker buildx imagetools inspect --format '{{json .Image.Config.Labels}}' "${image}@${digest}")"
    [[ -n "${labels}" ]] || { echo "release: OCI child manifest has no inspectable labels: ${digest}" >&2; return 1; }
    jq -e --arg version "${tag}" --arg revision "$(release_commit)" --arg fingerprint "${expected_fingerprint}" \
      '."org.opencontainers.image.version" == $version and ."org.opencontainers.image.revision" == $revision and ."org.opencontainers.image.licenses" == "MulanPSL-2.0" and ."io.github.pku-asal.sysbox.release-fingerprint" == $fingerprint' \
      <<<"${labels}" >/dev/null
  done
  verified_digest="sha256:$(printf '%s' "${raw}" | sha256sum | awk '{print $1}')"
  echo "release: verified OCI image ${ref}"
}

manifest_sha() {
  local raw
  raw="$(docker buildx imagetools inspect --raw "$1")" || return 1
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

reconcile_promotions() {
  local source_ref="${image}:${tag}" source_sha existing ref index
  source_sha="$(manifest_sha "${source_ref}")"
  mapfile -t refs < <(oci_tags "${tag}" "${image}")
  for index in "${!refs[@]}"; do
    ((index > 0)) || continue
    ref="${refs[${index}]}"
    if ((index == 1)); then
      if existing="$(manifest_sha "${ref}" 2>/dev/null)"; then
        [[ "${existing}" == "${source_sha}" ]] || { echo "release: immutable OCI tag differs: ${ref}" >&2; return 1; }
        continue
      fi
      assert_absent "${ref}"
    fi
    docker buildx imagetools create --tag "${ref}" "${source_ref}" >/dev/null
    [[ "$(manifest_sha "${ref}")" == "${source_sha}" ]] || { echo "release: reconciled OCI tag differs: ${ref}" >&2; return 1; }
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
    validate_release_fingerprint "${BUILD_METADATA:?release: BUILD_METADATA is required}"
    commit="$(release_commit)"
    created="$(release_time)"
    release_fingerprint="$(jq -er '.release_fingerprint' "${BUILD_METADATA:?release: BUILD_METADATA is required}")"
    docker buildx build --platform linux/amd64,linux/arm64 --provenance=false --push \
      --file "${repo_root}/Dockerfile" \
      --build-arg "VERSION=${tag}" --build-arg "REVISION=${commit}" \
      --build-arg "CREATED=${created}" --build-arg "SOURCE_URL=${SOURCE_URL:-https://github.com/PKU-ASAL/sysbox}" \
      --build-arg "RELEASE_FINGERPRINT=${release_fingerprint}" \
      --tag "${image}:${tag}" "${repo_root}"
    verify
    promote "${verified_digest}"
    record_digest "${verified_digest}"
    ;;
  reconcile)
    source_ref="${image}:${tag}"
    if docker buildx imagetools inspect "${source_ref}" >/dev/null 2>&1; then
      verify
      reconcile_promotions
      record_digest "${verified_digest}"
      echo "release: reused verified OCI image ${source_ref}"
    else
      build_output="$(docker buildx imagetools inspect "${source_ref}" 2>&1 || true)"
      case "${build_output,,}" in
        *"manifest unknown"*|*"name unknown"*|*"not found"*)
          preflight
          validate_release_fingerprint "${BUILD_METADATA:?release: BUILD_METADATA is required}"
          commit="$(release_commit)"
          created="$(release_time)"
          release_fingerprint="$(jq -er '.release_fingerprint' "${BUILD_METADATA:?release: BUILD_METADATA is required}")"
          docker buildx build --platform linux/amd64,linux/arm64 --provenance=false --push \
            --file "${repo_root}/Dockerfile" \
            --build-arg "VERSION=${tag}" --build-arg "REVISION=${commit}" \
            --build-arg "CREATED=${created}" --build-arg "SOURCE_URL=${SOURCE_URL:-https://github.com/PKU-ASAL/sysbox}" \
            --build-arg "RELEASE_FINGERPRINT=${release_fingerprint}" \
            --tag "${image}:${tag}" "${repo_root}"
          verify
          reconcile_promotions
          record_digest "${verified_digest}"
          ;;
        *) echo "release: cannot inspect OCI source ${source_ref}: ${build_output}" >&2; exit 1 ;;
      esac
    fi
    ;;
  verify)
    verify
    ;;
  *)
    echo "usage: $0 <preflight|build|verify|reconcile> --tag vMAJOR.MINOR.PATCH --image REGISTRY/OWNER/IMAGE" >&2
    exit 2
    ;;
esac
