#!/usr/bin/env bash
# lab.sh — sysbox microvm (Firecracker) topology lifecycle
#
# Usage (from sysbox/ project root):
#   sudo -E examples/microvm/lab.sh up
#   sudo -E examples/microvm/lab.sh down
#          examples/microvm/lab.sh logs
#          examples/microvm/lab.sh status
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
SENSOR_LOG="/tmp/sysbox-sensor.log"

GO="${GO:-$(command -v go 2>/dev/null || echo /usr/local/go/bin/go)}"

die() { echo "ERROR: $*" >&2; exit 1; }
require_root() { [ "$(id -u)" = "0" ] || die "Run: sudo -E $0 $*"; }

sysbox() { "${SYSBOX}" --state "${STATE_FILE}" --file "${FIELD_FILE}" "$@"; }

build_sysbox() {
    cd "${REPO_ROOT}"
    CGO_ENABLED=0 "${GO}" build -o bin/sysbox ./cmd/sysbox
}

stop_sensor() {
    pkill -f "sysbox.*sensor start" 2>/dev/null || true
    sleep 0.3
}

start_sensor() {
    echo "==> Starting vm-vsock sensor..."
    stop_sensor
    setsid nohup "${SYSBOX}" --state "${STATE_FILE}" --file "${FIELD_FILE}" \
        sensor start > "${SENSOR_LOG}" 2>&1 &
    echo "    sensor PID $!  log: ${SENSOR_LOG}"
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
    start_sensor
    echo ""
    echo "  node_attack  10.0.11.10 / 172.22.0.10  (firecracker)"
    echo "  node_web     10.0.12.10                 (firecracker)"
    echo "  node_db      10.0.12.20                 (firecracker)"
}

cmd_down() {
    require_root "down"
    stop_sensor
    sysbox destroy --auto-approve
}

cmd_logs() {
    tail -f "${SENSOR_LOG}"
}

cmd_status() {
    sysbox state list 2>/dev/null || echo "  (no state)"
    echo ""
    if pgrep -f "sysbox.*sensor start" > /dev/null 2>&1; then
        pgrep -a -f "sysbox.*sensor start" | sed 's/^/  sensor: /'
    else
        echo "  sensor: not running"
    fi
}

CMD="${1:-help}"
shift 2>/dev/null || true
case "${CMD}" in
    up)     cmd_up ;;
    down)   cmd_down ;;
    logs)   cmd_logs ;;
    status) cmd_status ;;
    help|--help|-h) sed -n '2,12p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//' ;;
    *) echo "Usage: $0 {up|down|logs|status}"; exit 1 ;;
esac
