#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "${repo_root}/scripts/release/lib.sh"

fail() {
  echo "release test: $*" >&2
  exit 1
}

validate_version v0.1.0
for invalid in v1 1.2.3 v1.2.3-rc.1 v01.2.3 v1.02.3 v1.2.03; do
  if validate_version "${invalid}" >/dev/null 2>&1; then
    fail "accepted invalid version ${invalid}"
  fi
done

expected_tags=$'registry.example/oslab/sysbox:v0.3.4\nregistry.example/oslab/sysbox:0.3.4\nregistry.example/oslab/sysbox:0.3\nregistry.example/oslab/sysbox:0\nregistry.example/oslab/sysbox:latest'
[[ "$(oci_tags v0.3.4 registry.example/oslab/sysbox)" == "${expected_tags}" ]] || fail "OCI tag set is incorrect"
for arg in VERSION REVISION CREATED SOURCE_URL; do
  grep -Eq "^ARG ${arg}(=|$)" "${repo_root}/Dockerfile" || fail "Dockerfile is missing ARG ${arg}"
done
for label in org.opencontainers.image.version org.opencontainers.image.revision org.opencontainers.image.created org.opencontainers.image.licenses; do
  grep -F "${label}" "${repo_root}/Dockerfile" >/dev/null || fail "Dockerfile is missing ${label}"
done
[[ -f "${repo_root}/Dockerfile.cli" ]] || fail "Dockerfile.cli is missing"
[[ -f "${repo_root}/Dockerfile.metadata" ]] || fail "Dockerfile.metadata is missing"
grep -Eq '^FROM scratch$' "${repo_root}/Dockerfile.cli" || fail "CLI image runtime must be scratch"
grep -F 'COPY dist/oci/linux/${TARGETARCH}/sysbox /usr/local/bin/sysbox' "${repo_root}/Dockerfile.cli" >/dev/null || fail "CLI image must use the verified release sysbox binary"
grep -F 'COPY dist/oci/linux/${TARGETARCH}/sysbox-init /usr/local/bin/sysbox-init' "${repo_root}/Dockerfile.cli" >/dev/null || fail "CLI image must use the verified release sysbox-init binary"
grep -Eq '^FROM scratch$' "${repo_root}/Dockerfile.metadata" || fail "metadata image runtime must be scratch"
grep -F 'COPY dist/build-metadata.json /build-metadata.json' "${repo_root}/Dockerfile.metadata" >/dev/null || fail "metadata image is missing final build metadata"
grep -F -- '--provenance=false' "${repo_root}/scripts/release/oci.sh" >/dev/null || fail "OCI build must disable provenance attestations"
grep -F -- '--dockerfile' "${repo_root}/scripts/release/oci.sh" >/dev/null || fail "OCI publisher must accept an explicit Dockerfile"
grep -F -- '--metadata-field' "${repo_root}/scripts/release/oci.sh" >/dev/null || fail "OCI publisher must record a distinct metadata field"
grep -F '${SYSBOX_IMAGE:-sysbox:latest}' "${repo_root}/deploy/docker/compose.yml" >/dev/null || fail "API Compose image is not pinnable"
grep -F '${SYSBOX_IMAGE:-sysbox:latest}' "${repo_root}/deploy/docker/compose.agent.yml" >/dev/null || fail "Agent Compose image is not pinnable"
grep -F 'SYSBOX_IMAGE=ghcr.io/pku-asal/sysbox:v0.1.0' "${repo_root}/.env.example" >/dev/null || fail ".env.example does not use canonical GHCR image"
grep -F '${SOURCE_URL}/blob/main/docs/README.md' "${repo_root}/Dockerfile" >/dev/null || fail "runtime image documentation label is not a GitHub URL"

tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT

git_repo="${tmp}/git-contract"
mkdir -p "${git_repo}"
(
  cd "${git_repo}"
  git init -q
  git config user.name sysbox-release-test
  git config user.email release-test@sysbox.invalid
  printf 'one\n' >tracked
  git add tracked
  git commit -qm initial
  git tag v0.1.0
  require_release_repo v0.1.0
  printf 'dirty\n' >>tracked
  if require_release_repo v0.1.0 >/dev/null 2>&1; then
    fail "accepted dirty tracked worktree"
  fi
  git restore tracked
  printf 'two\n' >tracked
  git add tracked
  git commit -qm second
  if require_release_repo v0.1.0 >/dev/null 2>&1; then
    fail "accepted tag that does not point at HEAD"
  fi
)

"${repo_root}/scripts/release/build.sh" --tag v0.1.0 --output "${tmp}/first" --allow-untagged
"${repo_root}/scripts/release/verify.sh" "${tmp}/first"
"${repo_root}/scripts/release/build.sh" --tag v0.1.0 --output "${tmp}/second" --allow-untagged
"${repo_root}/scripts/release/verify.sh" "${tmp}/second"

cmp "${tmp}/first/SHA256SUMS" "${tmp}/second/SHA256SUMS"
for arch in amd64 arm64; do
  archive="sysbox_v0.1.0_linux_${arch}.tar.gz"
  cmp "${tmp}/first/${archive}" "${tmp}/second/${archive}"
  members="$(tar -tzf "${tmp}/first/${archive}" | sort)"
  expected=$'LICENSE\nREADME.md\nbuild-metadata.json\nsysbox\nsysbox-init'
  [[ "${members}" == "${expected}" ]] || fail "unexpected ${arch} archive members: ${members}"
  extracted="${tmp}/extracted-${arch}"
  mkdir -p "${extracted}"
  tar -xzf "${tmp}/first/${archive}" -C "${extracted}" sysbox sysbox-init
  cmp "${extracted}/sysbox" "${tmp}/first/oci/linux/${arch}/sysbox"
  cmp "${extracted}/sysbox-init" "${tmp}/first/oci/linux/${arch}/sysbox-init"
  sysbox_sha="$(sha256sum "${extracted}/sysbox" | awk '{print $1}')"
  init_sha="$(sha256sum "${extracted}/sysbox-init" | awk '{print $1}')"
  jq -e --arg arch "${arch}" --arg sysbox_sha "${sysbox_sha}" --arg init_sha "${init_sha}" \
    '.targets[] | select(.architecture == $arch) | .sysbox_sha256 == $sysbox_sha and .sysbox_init_sha256 == $init_sha' \
    "${tmp}/first/build-metadata.json" >/dev/null || fail "binary hashes missing from ${arch} metadata"
done

echo "Release artifact tests passed."
