#!/usr/bin/env bash
# Shared by release builds and tests which require freshly embedded helpers.
# Caller owns the temporary output directory and its cleanup.
prepare_helper_overlay() {
  local source_root="$1" output="$2" flags="$3" guest_arch
  mkdir -p "${output}"
  for guest_arch in amd64 arm64; do
    CGO_ENABLED=0 GOOS=linux GOARCH="${guest_arch}" "${GO:-go}" build -trimpath -buildvcs=false \
      -ldflags "${flags}" -o "${output}/init-${guest_arch}" ./cmd/sysbox-init
  done
  jq -n --arg root "${source_root}/pkg/provider/firecracker/initbin" --arg tmp "${output}" \
    '{Replace:{($root+"/sysbox-init.linux-amd64.bin"):($tmp+"/init-amd64"),($root+"/sysbox-init.linux-arm64.bin"):($tmp+"/init-arm64")}}' >"${output}/overlay.json"
}
