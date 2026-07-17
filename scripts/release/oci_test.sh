#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "${repo_root}/scripts/release/lib.sh"
tmp="$(mktemp -d)"; trap 'rm -rf "${tmp}"' EXIT
mkdir -p "${tmp}/bin" "${tmp}/registry"
tag=v0.1.0
image=registry.example/pku-asal/sysbox
commit="$(git -C "${repo_root}" rev-parse HEAD)"
jq -n --arg tag "${tag}" --arg commit "${commit}" \
  '{tag:$tag,commit:$commit,targets:[{os:"linux",architecture:"amd64",sha256:"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},{os:"linux",architecture:"arm64",sha256:"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"}],release_fingerprint:""}' \
  >"${tmp}/metadata.json"
fingerprint="$(release_fingerprint_for_metadata "${tmp}/metadata.json")"
jq --arg fingerprint "${fingerprint}" '.release_fingerprint = $fingerprint' "${tmp}/metadata.json" >"${tmp}/metadata.json.tmp"
mv "${tmp}/metadata.json.tmp" "${tmp}/metadata.json"

cat >"${tmp}/bin/git" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
case " $* " in
  *" rev-parse HEAD "*) printf '%s\n' "${FAKE_COMMIT}" ;;
  *) /usr/bin/git -C "${FAKE_REPO}" "$@" ;;
esac
EOF

cat >"${tmp}/bin/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
key() { printf '%s' "$1" | tr '/:' '__'; }
[[ "${1:-}" == buildx ]] && shift
case "${1:-}" in
  version) exit 0 ;;
  imagetools)
    shift
    case "${1:-}" in
      inspect)
        shift
        mode=exists
        case "${1:-}" in --raw) mode=raw; shift;; --format) mode=labels; shift 2;; esac
        ref="$1"
        if [[ "${ref}" == *@* ]]; then dir="${FAKE_REGISTRY}/$(key "${FAKE_SOURCE_REF}")"; else dir="${FAKE_REGISTRY}/$(key "${ref}")"; fi
        [[ -d "${dir}" ]] || { echo 'manifest unknown' >&2; exit 1; }
        case "${mode}" in
          raw) cat "${dir}/raw" ;;
          labels)
            if [[ "${ref}" == *@* ]]; then cat "${dir}/labels-${ref#*@}"; else cat "${dir}/labels"; fi
            ;;
        esac
        ;;
      create)
        shift
        [[ "${1:-}" == --tag ]]; dest="$2"; source="$3"
        cp -R "${FAKE_REGISTRY}/$(key "${source}")" "${FAKE_REGISTRY}/$(key "${dest}")"
        ;;
    esac
    ;;
  info) exit 0 ;;
  *) echo "unexpected docker command: $*" >&2; exit 2 ;;
esac
EOF
chmod +x "${tmp}/bin/git" "${tmp}/bin/docker"

write_manifest() {
  local ref="$1" marker="$2" dir
  dir="${tmp}/registry/$(printf '%s' "${ref}" | tr '/:' '__')"
  mkdir -p "${dir}"
  jq -n --arg marker "${marker}" '{schemaVersion:2,marker:$marker,manifests:[
    {digest:"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",platform:{os:"linux",architecture:"amd64"}},
    {digest:"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",platform:{os:"linux",architecture:"arm64"}}
  ]}' >"${dir}/raw"
  jq -n --arg tag "${tag}" --arg commit "${commit}" --arg fingerprint "${fingerprint}" \
    '{"org.opencontainers.image.version":$tag,"org.opencontainers.image.revision":$commit,"org.opencontainers.image.licenses":"MulanPSL-2.0","io.github.pku-asal.sysbox.release-fingerprint":$fingerprint}' >"${dir}/labels"
  cp "${dir}/labels" "${dir}/labels-sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
  cp "${dir}/labels" "${dir}/labels-sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
}

common=(env PATH="${tmp}/bin:${PATH}" FAKE_COMMIT="${commit}" FAKE_REPO="${repo_root}" FAKE_REGISTRY="${tmp}/registry" FAKE_SOURCE_REF="${image}:${tag}" BUILD_METADATA="${tmp}/metadata.json")
write_manifest "${image}:${tag}" source
"${common[@]}" "${repo_root}/scripts/release/oci.sh" reconcile --tag "${tag}" --image "${image}"
for promoted in 0.1.0 0.1 0 latest; do
  [[ -d "${tmp}/registry/$(printf '%s' "${image}:${promoted}" | tr '/:' '__')" ]] || { echo "OCI test: missing reconciled tag ${promoted}" >&2; exit 1; }
done

source_dir="${tmp}/registry/$(printf '%s' "${image}:${tag}" | tr '/:' '__')"
jq '."io.github.pku-asal.sysbox.release-fingerprint" = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"' \
  "${source_dir}/labels" >"${source_dir}/labels-sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
if "${common[@]}" "${repo_root}/scripts/release/oci.sh" reconcile --tag "${tag}" --image "${image}" >/dev/null 2>&1; then
  echo "OCI test: accepted mismatched arm64 child labels" >&2
  exit 1
fi
cp "${source_dir}/labels" "${source_dir}/labels-sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"

rm -rf "${tmp}/registry/$(printf '%s' "${image}:0.1.0" | tr '/:' '__')"
write_manifest "${image}:0.1.0" conflict
if "${common[@]}" "${repo_root}/scripts/release/oci.sh" reconcile --tag "${tag}" --image "${image}" >/dev/null 2>&1; then
  echo "OCI test: accepted conflicting immutable numeric tag" >&2
  exit 1
fi
jq '.release_fingerprint = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"' \
  "${tmp}/metadata.json" >"${tmp}/metadata.json.tmp"
mv "${tmp}/metadata.json.tmp" "${tmp}/metadata.json"
if "${common[@]}" "${repo_root}/scripts/release/oci.sh" reconcile --tag "${tag}" --image "${image}" >/dev/null 2>&1; then
  echo "OCI test: accepted mismatched release fingerprint" >&2
  exit 1
fi
echo "OCI reconcile tests passed."
