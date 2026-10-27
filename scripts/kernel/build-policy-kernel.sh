#!/usr/bin/env bash
# Build the pinned Firecracker kernel with guest nftables support built in.
# Run in a build VM/container; does not install or boot a host kernel.
set -euo pipefail
: "${KERNEL_SOURCE:?path to linux-5.10.245.tar.xz}"
: "${KERNEL_OUTPUT:?new output directory}"
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
base_config=${KERNEL_BASE_CONFIG:-$script_dir/firecracker-5.10-policy.config}
source_sha=131e75eb14da6fd683b3882bcd856085cfca47cc1aa89cd2deb1b48d22d29d9f
KERNEL_OUTPUT=$(realpath -- "$KERNEL_OUTPUT")
printf '%s  %s\n' "$source_sha" "$KERNEL_SOURCE" | sha256sum -c -
mkdir -m 700 "$KERNEL_OUTPUT"
build_dir=$(mktemp -d "${TMPDIR:-/tmp}/sysbox-policy-kernel.XXXXXX")
trap 'rm -rf -- "$build_dir"' EXIT
tar -xJf "$KERNEL_SOURCE" -C "$build_dir" --strip-components=1
cp "$base_config" "$build_dir/.config"
cd "$build_dir"
for option in NETFILTER NETFILTER_ADVANCED NETFILTER_NETLINK NF_CONNTRACK NF_NAT NF_TABLES NF_TABLES_INET NF_TABLES_IPV4 NFNETLINK NFT_CT NFT_COUNTER NFT_LOG NFT_MASQ NFT_NAT NFT_REJECT NFT_REJECT_INET NFT_REJECT_IPV4 NFT_FIB NFT_FIB_IPV4 NFT_FIB_INET NF_LOG_IPV4; do
 scripts/config --enable "$option"
done
make olddefconfig > "$KERNEL_OUTPUT/config.log" 2>&1
for option in NF_TABLES NETFILTER_NETLINK NFT_CT NFT_COUNTER NFT_LOG NFT_MASQ NFT_NAT NFT_REJECT; do
 if ! grep -qx "CONFIG_$option=y" .config; then echo "required kernel capability missing: $option" >&2; exit 1; fi
done
make -j"${KERNEL_JOBS:-4}" vmlinux > "$KERNEL_OUTPUT/build.log" 2>&1
cp vmlinux "$KERNEL_OUTPUT/vmlinux"
cp .config "$KERNEL_OUTPUT/kernel.config"
sha256sum "$KERNEL_OUTPUT/vmlinux" "$KERNEL_OUTPUT/kernel.config" > "$KERNEL_OUTPUT/SHA256SUMS"
printf 'linux=5.10.245\nsource_sha256=%s\n' "$source_sha" > "$KERNEL_OUTPUT/provenance.txt"
