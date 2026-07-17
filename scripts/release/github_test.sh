#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
tmp="$(mktemp -d)"; trap 'rm -rf "${tmp}"' EXIT
tag=v0.1.0
dist="${tmp}/dist"; remote="${tmp}/remote"; mkdir -p "${dist}" "${remote}" "${tmp}/bin"
commit="$(git -C "${repo_root}" rev-parse HEAD)"
RELEASE_REPOSITORY=PKU-ASAL/sysbox "${repo_root}/scripts/release/build.sh" --tag "${tag}" --output "${dist}" --allow-untagged
jq '.oci_digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"' \
  "${dist}/build-metadata.json" >"${dist}/build-metadata.json.tmp"
mv "${dist}/build-metadata.json.tmp" "${dist}/build-metadata.json"

cat >"${tmp}/bin/git" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
case " $* " in
  *" diff "*) exit 0 ;;
  *" rev-parse HEAD "*) printf '%s\n' "${FAKE_COMMIT}" ;;
  *" rev-parse "*"^{commit}"*) printf '%s\n' "${FAKE_COMMIT}" ;;
  *" show -s --format=%ct HEAD "*) command git -C "${FAKE_REPO}" show -s --format=%ct HEAD ;;
  *) command git -C "${FAKE_REPO}" "$@" ;;
esac
EOF
chmod +x "${tmp}/bin/git"

cat >"${tmp}/bin/gh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == api ]]; then
  jq -n --arg commit "${FAKE_COMMIT}" '{object:{type:"commit",sha:$commit}}'
  exit 0
fi
[[ "${1:-}" == release ]]; shift
case "${1:-}" in
  view)
    [[ -e "${FAKE_RELEASE}" ]] || { echo 'release not found' >&2; exit 1; }
    if [[ " $* " == *" --json "* ]]; then
      assets="$(find "${FAKE_REMOTE}" -maxdepth 1 -type f -printf '%f\n' | sort | jq -Rsc 'split("\n")[:-1] | map({name:.})')"
      jq -n --arg tag "${FAKE_TAG}" --argjson assets "${assets}" \
        '{tagName:$tag,isDraft:false,isPrerelease:false,assets:$assets}'
    else
      echo "${FAKE_TAG}"
    fi
    ;;
  download)
    dir=""; pattern="*"; while (($#)); do case "$1" in --dir) dir="$2"; shift 2;; --pattern) pattern="$2"; shift 2;; *) shift;; esac; done
    find "${FAKE_REMOTE}" -maxdepth 1 -type f -name "${pattern}" -exec cp {} "${dir}/" \;
    ;;
  upload)
    shift 2
    cp "$1" "${FAKE_REMOTE}/"
    ;;
  *) echo "unexpected gh command: $*" >&2; exit 2 ;;
esac
EOF
chmod +x "${tmp}/bin/gh"

common=(env GOCACHE="${GOCACHE:-/tmp/sysbox-gocache}" PATH="${tmp}/bin:${PATH}" GH_TOKEN=test FAKE_TAG="${tag}" FAKE_COMMIT="${commit}" FAKE_REPO="${repo_root}" FAKE_REMOTE="${remote}" FAKE_RELEASE="${tmp}/release-exists")
"${common[@]}" "${repo_root}/scripts/release/github.sh" preflight --tag "${tag}" --repository PKU-ASAL/sysbox
touch "${tmp}/release-exists"
cp "${dist}/sysbox_${tag}_linux_amd64.tar.gz" "${remote}/"
"${common[@]}" "${repo_root}/scripts/release/github.sh" reconcile --tag "${tag}" --dist "${dist}" --repository PKU-ASAL/sysbox
"${common[@]}" "${repo_root}/scripts/release/github.sh" audit --tag "${tag}" --dist "${dist}" --repository PKU-ASAL/sysbox
printf 'tampered\n' >"${remote}/SHA256SUMS"
if "${common[@]}" "${repo_root}/scripts/release/github.sh" reconcile --tag "${tag}" --dist "${dist}" --repository PKU-ASAL/sysbox >/dev/null 2>&1; then
  echo "github release test: accepted conflicting existing asset" >&2
  exit 1
fi
echo "GitHub Release tests passed."
