#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cache_root="${SYSBOX_CACHE:-${XDG_CACHE_HOME:-${HOME}/.cache}/sysbox}"
image_host="$(${root}/scripts/prepare-libvirt-cloud-image.sh)"
kernel_host="${SYSBOX_KERNEL:-$(find "${cache_root}/artifacts" -type f -name 'vmlinux-*' | sort | head -1)}"
rootfs_host="${SYSBOX_ROOTFS:-${cache_root}/rootfs/ubuntu-24.04.ext4}"
modcache="$(go env GOPATH)/pkg/mod"
firecracker_bin="${SYSBOX_FIRECRACKER_BIN:-$(command -v firecracker)}"
key_dir="$(mktemp -d /tmp/sysbox-matrix-key.XXXXXX)"
image_runtime="$(mktemp /tmp/sysbox-ubuntu-cloud.XXXXXX.img)"

cleanup() {
  rm -rf "${key_dir}"
  docker run --rm -v /tmp:/cleanup alpine:latest rm -f "/cleanup/$(basename "${image_runtime}")" >/dev/null 2>&1 || true
  docker run --rm -v /tmp/sysbox-e2e:/cleanup alpine:latest rm -rf /cleanup/heterogeneous-matrix >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker run --rm -v /tmp/sysbox-e2e:/cleanup alpine:latest rm -rf /cleanup/heterogeneous-matrix

test -f "${kernel_host}"
test -f "${rootfs_host}"
test -f "${image_host}"
test -x "${firecracker_bin}"
cp --reflink=auto "${image_host}" "${image_runtime}"
chmod 0644 "${image_runtime}"
ssh-keygen -q -t ed25519 -N '' -C sysbox-matrix -f "${key_dir}/id_ed25519"
public_key="$(cat "${key_dir}/id_ed25519.pub")"
inner_script="${SYSBOX_MATRIX_INNER:-tests/e2e/heterogeneous_matrix_inner.sh}"

case "${kernel_host}" in "${cache_root}"/*) ;; *) echo "SYSBOX_KERNEL must be under ${cache_root}" >&2; exit 1 ;; esac
case "${rootfs_host}" in "${cache_root}"/*) ;; *) echo "SYSBOX_ROOTFS must be under ${cache_root}" >&2; exit 1 ;; esac

docker run --rm --privileged --pid=host --network=host \
  -v /run/netns:/run/netns:rshared \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v /var/run/libvirt:/var/run/libvirt \
	-v /var/lib/sysbox:/var/lib/sysbox:rshared \
  -v /var/lib/libvirt/images:/var/lib/libvirt/images:ro \
  -v /tmp:/tmp:rshared \
  -v "${cache_root}:${cache_root}:ro" \
  -v "${key_dir}/id_ed25519:/keys/matrix:ro" \
  -v "${root}:/src" \
  -v "${modcache}:/go/pkg/mod:ro" \
  -v "${firecracker_bin}:/usr/local/bin/firecracker:ro" \
  -w /src \
  -e GOPROXY=off \
  -e GOCACHE=/tmp/go-build \
  -e HOME=/tmp/heterogeneous-matrix-home \
  -e PATH=/usr/sbin:/usr/local/bin:/usr/local/go/bin:/usr/local/sbin:/usr/bin:/sbin:/bin \
  -e SYSBOX_KERNEL="${kernel_host}" \
  -e SYSBOX_ROOTFS="${rootfs_host}" \
  -e SYSBOX_QCOW2="${image_runtime}" \
  -e SYSBOX_MATRIX_SSH_PRIVATE_KEY=/keys/matrix \
  -e SYSBOX_MATRIX_SSH_PUBLIC_KEY="${public_key}" \
  golang:1.26 sh -c 'apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq docker.io e2fsprogs genisoimage iproute2 iputils-ping libvirt-clients mount openssh-client qemu-utils >/dev/null && sh "$1"' -- "${inner_script}"
