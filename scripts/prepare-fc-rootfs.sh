#!/usr/bin/env bash
# Build a firecracker-ready ext4 rootfs from the firecracker-team-maintained
# ubuntu-24.04 squashfs image. See docs/operations/deployment.md.
#
# Idempotent: cached output is reused; pass --force to rebuild.
#
# Output: $OUT (default ~/.cache/sysbox/rootfs/ubuntu-24.04.ext4)
#
# Requires: wget, sudo, mount, mkfs.ext4, unsquashfs (apt-get install
# squashfs-tools), cp -a.

set -euo pipefail

FC_CI_BASE="${FC_CI_BASE:-https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/v1.14/x86_64}"
SQUASHFS_NAME="${SQUASHFS_NAME:-ubuntu-24.04.squashfs}"
ROOTFS_SIZE_MB="${ROOTFS_SIZE_MB:-1024}"
CACHE_DIR="${SYSBOX_ROOTFS_CACHE:-${XDG_CACHE_HOME:-$HOME/.cache}/sysbox/rootfs}"
OUT="${OUT:-$CACHE_DIR/ubuntu-24.04.ext4}"
SQUASHFS_PATH="$CACHE_DIR/$SQUASHFS_NAME"
TMP_DIR="$(mktemp -d -t fc-rootfs-XXXXXX)"
FORCE=0

cleanup() {
  sudo umount "$TMP_DIR/mnt" 2>/dev/null || true
  # The squashfs extraction is owned by root, so sudo is needed.
  sudo rm -rf "$TMP_DIR" 2>/dev/null || rm -rf "$TMP_DIR" 2>/dev/null || true
}
trap cleanup EXIT

usage() {
  local rc="${1:-0}"
  cat <<EOF
Usage: $0 [--force] [--out PATH]

Environment:
  FC_CI_BASE         firecracker-ci mirror base URL
                     (default: $FC_CI_BASE)
  SQUASHFS_NAME      squashfs file name on the mirror
                     (default: $SQUASHFS_NAME)
  ROOTFS_SIZE_MB     output ext4 size in MiB (default: $ROOTFS_SIZE_MB)
  OUT                output ext4 path
                     (default: $OUT)
  SYSBOX_ROOTFS_CACHE  override cache dir (default \$XDG_CACHE_HOME/sysbox/rootfs)

Reference: docs/operations/deployment.md
EOF
  exit "$rc"
}

while [ $# -gt 0 ]; do
  case "$1" in
    --force) FORCE=1; shift ;;
    --out)   OUT="$2"; shift 2 ;;
    -h|--help) usage 0 ;;
    *) echo "unknown arg: $1" >&2; usage 1 ;;
  esac
done

need() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "missing dependency: $1" >&2
    echo "hint: sudo apt-get install -y $2" >&2
    exit 1
  }
}
need wget    wget
need mkfs.ext4 e2fsprogs
need unsquashfs squashfs-tools
need sudo    sudo

if [ "$FORCE" -eq 0 ] && [ -s "$OUT" ]; then
  echo "[prepare-fc-rootfs] reusing existing $OUT ($(du -h "$OUT" | cut -f1))"
  echo "[prepare-fc-rootfs] pass --force to rebuild"
  exit 0
fi

mkdir -p "$CACHE_DIR" "$TMP_DIR/mnt"
# Note: do NOT pre-create $TMP_DIR/extracted — unsquashfs insists on
# creating its own destination directory.

if [ ! -s "$SQUASHFS_PATH" ]; then
  echo "[prepare-fc-rootfs] downloading $SQUASHFS_NAME ..."
  wget -q --show-progress -O "$SQUASHFS_PATH" "$FC_CI_BASE/$SQUASHFS_NAME"
else
  echo "[prepare-fc-rootfs] using cached squashfs $SQUASHFS_PATH"
fi

echo "[prepare-fc-rootfs] extracting squashfs..."
sudo unsquashfs -q -d "$TMP_DIR/extracted" "$SQUASHFS_PATH"

echo "[prepare-fc-rootfs] creating $ROOTFS_SIZE_MB MiB ext4..."
truncate -s "${ROOTFS_SIZE_MB}M" "$OUT.tmp"
mkfs.ext4 -F -q "$OUT.tmp"

echo "[prepare-fc-rootfs] populating ext4..."
sudo mount "$OUT.tmp" "$TMP_DIR/mnt"
sudo cp -a "$TMP_DIR/extracted/." "$TMP_DIR/mnt/"
sudo umount "$TMP_DIR/mnt"

mv "$OUT.tmp" "$OUT"
sudo chown "$(id -u):$(id -g)" "$OUT" 2>/dev/null || true

echo "[prepare-fc-rootfs] done: $OUT ($(du -h "$OUT" | cut -f1))"
echo
echo "Reference it from HCL:"
echo "  resource \"sysbox_image\" \"alpine_vm\" {"
echo "    substrate = substrate.firecracker.fc"
echo "    rootfs    = \"$OUT\""
echo "  }"
