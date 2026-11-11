#!/usr/bin/env bash
# Build all runtime executables with freshly compiled embedded guest helpers.
set -euo pipefail
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
output="${1:?output directory required}"
ldflags="${2:--s -w}"
arch="${GOARCH:-$("${GO:-go}" env GOARCH)}"
case "${arch}" in amd64|arm64) ;; *) echo "unsupported release architecture: ${arch}" >&2; exit 1 ;; esac
mkdir -p "${output}"
output="$(cd "${output}" && pwd)"
cd "${repo_root}"
tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT
source "${repo_root}/scripts/release/helper-overlay.sh"
prepare_helper_overlay "${repo_root}" "${tmp}" "${ldflags}"
CGO_ENABLED=0 GOOS=linux GOARCH="${arch}" "${GO:-go}" build -overlay "${tmp}/overlay.json" -trimpath -buildvcs=false \
  -ldflags "${ldflags}" -o "${output}/sysbox" ./cmd/sysbox
install -m 0755 "${tmp}/init-${arch}" "${output}/sysbox-init"
CGO_ENABLED=0 GOOS=linux GOARCH="${arch}" "${GO:-go}" build -trimpath -buildvcs=false \
  -ldflags "${ldflags}" -o "${output}/sysbox-netns" ./cmd/sysbox-netns
