#!/usr/bin/env bash
# lab.sh — sysbox microvm (Firecracker) topology lifecycle
#
# Usage (from sysbox/ project root):
#   sudo -E examples/microvm/lab.sh up      # apply topology
#   sudo -E examples/microvm/lab.sh down    # destroy topology
#          examples/microvm/lab.sh status   # show state
#
# Prerequisites:
#   - firecracker binary in PATH
#   - SYSBOX_ROOTFS set, or default ~/.cache/sysbox/rootfs/ubuntu-24.04.ext4
#   - sudo -E (preserve HOME / SYSBOX_ROOTFS into the sysbox process)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
FIELD_FILE="${SCRIPT_DIR}/field.sysbox.hcl"
STATE_FILE="${REPO_ROOT}/runs/microvm/state.json"
SYSBOX="${REPO_ROOT}/bin/sysbox"

GO="${GO:-$(command -v go 2>/dev/null || echo /usr/local/go/bin/go)}"

die() { echo "ERROR: $*" >&2; exit 1; }
require_root() { [ "$(id -u)" = "0" ] || die "Run: sudo -E $0 $*"; }

sysbox() { "${SYSBOX}" --state "${STATE_FILE}" --file "${FIELD_FILE}" "$@"; }

build_sysbox() {
    cd "${REPO_ROOT}"
    CGO_ENABLED=0 "${GO}" build -o bin/sysbox ./cmd/sysbox
}

cmd_up() {
    require_root "up"
    build_sysbox
    mkdir -p "$(dirname "${STATE_FILE}")"
    if [ -f "${STATE_FILE}" ]; then
        sysbox destroy --auto-approve 2>/dev/null || rm -f "${STATE_FILE}" "${STATE_FILE}.lock"
    fi
    echo "==> Applying microvm topology..."
    sysbox apply --auto-approve
    echo ""
    echo "  node_attack  10.0.11.10 / 172.22.0.10  (firecracker)"
    echo "  node_web     10.0.12.10                 (firecracker)"
    echo "  node_db      10.0.12.20                 (firecracker)"
}

cmd_down() {
    require_root "down"
    sysbox destroy --auto-approve
}

cmd_status() {
    sysbox state list 2>/dev/null || echo "  (no state)"
}

CMD="${1:-help}"
shift 2>/dev/null || true
case "${CMD}" in
    up)     cmd_up ;;
    down)   cmd_down ;;
    status) cmd_status ;;
    help|--help|-h) sed -n '2,12p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//' ;;
    *) echo "Usage: $0 {up|down|status}"; exit 1 ;;
esac
