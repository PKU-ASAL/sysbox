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

preflight() {
  docker info >/dev/null
  for ref in "${immutable[@]}"; do
    if docker buildx imagetools inspect "${ref}" >/dev/null 2>&1; then
      echo "release: immutable OCI tag already exists: ${ref}" >&2
      return 1
    fi
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
  echo "release: verified OCI image ${ref}"
}

case "${operation}" in
  preflight)
    preflight
    ;;
  build)
    preflight
    commit="$(release_commit)"
    created="$(release_time)"
    mapfile -t refs < <(oci_tags "${tag}" "${image}")
    tag_args=()
    for ref in "${refs[@]}"; do tag_args+=(--tag "${ref}"); done
    docker buildx build --platform linux/amd64,linux/arm64 --push \
      --build-arg "VERSION=${tag}" --build-arg "REVISION=${commit}" \
      --build-arg "CREATED=${created}" --build-arg "SOURCE_URL=${SOURCE_URL:-https://git.pku.edu.cn/oslab/sysbox}" \
      "${tag_args[@]}" "${repo_root}"
    verify
    ;;
  verify)
    verify
    ;;
  *)
    echo "usage: $0 <preflight|build|verify> --tag vMAJOR.MINOR.PATCH --image REGISTRY/OWNER/IMAGE" >&2
    exit 2
    ;;
esac
