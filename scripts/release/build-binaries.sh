#!/usr/bin/env bash
# Build all runtime executables with freshly compiled embedded guest helpers.
set -euo pipefail
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
output="${1:?output directory required}"
ldflags="${2:--s -w}"
arch="${GOARCH:-$(go env GOARCH)}"
case "${arch}" in amd64|arm64) ;; *) echo "unsupported release architecture: ${arch}" >&2; exit 1 ;; esac
mkdir -p "${output}"
output="$(cd "${output}" && pwd)"
cd "${repo_root}"
tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT
for guest_arch in amd64 arm64; do
  CGO_ENABLED=0 GOOS=linux GOARCH="${guest_arch}" go build -trimpath -buildvcs=false \
    -ldflags "${ldflags}" -o "${tmp}/init-${guest_arch}" ./cmd/sysbox-init
done
# An overlay leaves the checkout untouched and excludes stale local helpers.
jq -n --arg root "${repo_root}/pkg/provider/firecracker/initbin" --arg tmp "${tmp}" \
  '{Replace:{($root+"/sysbox-init.linux-amd64.bin"):($tmp+"/init-amd64"),($root+"/sysbox-init.linux-arm64.bin"):($tmp+"/init-arm64")}}' >"${tmp}/overlay.json"
CGO_ENABLED=0 GOOS=linux GOARCH="${arch}" go build -overlay "${tmp}/overlay.json" -trimpath -buildvcs=false \
  -ldflags "${ldflags}" -o "${output}/sysbox" ./cmd/sysbox
install -m 0755 "${tmp}/init-${arch}" "${output}/sysbox-init"
CGO_ENABLED=0 GOOS=linux GOARCH="${arch}" go build -trimpath -buildvcs=false \
  -ldflags "${ldflags}" -o "${output}/sysbox-netns" ./cmd/sysbox-netns
