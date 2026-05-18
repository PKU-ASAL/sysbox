#!/usr/bin/env bash
# lab.sh — sysbox two-networks topology lifecycle
#
# Usage (from sysbox/ project root):
#   sudo -E examples/two-networks/lab.sh up
#          examples/two-networks/lab.sh down
#          examples/two-networks/lab.sh status

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
FIELD_FILE="${SCRIPT_DIR}/field.sysbox.hcl"
STATE_FILE="${REPO_ROOT}/runs/two-networks/state.json"
SYSBOX="${REPO_ROOT}/bin/sysbox"
API_ADDR="${SYSBOX_API_ADDR:-:9876}"
API_PID_FILE="$(dirname "${STATE_FILE}")/api.pid"

GO="${GO:-$(command -v go 2>/dev/null || echo /usr/local/go/bin/go)}"

die() { echo "ERROR: $*" >&2; exit 1; }
require_root() { [ "$(id -u)" = "0" ] || die "Run: sudo -E $0 $*"; }

sysbox() { "${SYSBOX}" --state "${STATE_FILE}" --file "${FIELD_FILE}" "$@"; }

build_sysbox() {
    cd "${REPO_ROOT}"
    CGO_ENABLED=0 "${GO}" build -o bin/sysbox ./cmd/sysbox
}

start_api() {
    stop_api 2>/dev/null || true
    echo "==> Starting API server on ${API_ADDR}..."
    "${SYSBOX}" serve --addr "${API_ADDR}" &
    echo $! > "${API_PID_FILE}"
    sleep 1
    if kill -0 "$(cat "${API_PID_FILE}")" 2>/dev/null; then
        echo "    API PID=$(cat "${API_PID_FILE}")  http://localhost${API_ADDR}/v1/health"
    else
        echo "    WARNING: API server failed to start (port in use?)"
        rm -f "${API_PID_FILE}"
    fi
}

stop_api() {
    if [ -f "${API_PID_FILE}" ]; then
        local pid
        pid="$(cat "${API_PID_FILE}")"
        if kill -0 "${pid}" 2>/dev/null; then
            echo "==> Stopping API server (PID ${pid})..."
            kill "${pid}" 2>/dev/null
            wait "${pid}" 2>/dev/null || true
        fi
        rm -f "${API_PID_FILE}"
    fi
}

cmd_up() {
    require_root "up"
    build_sysbox
    mkdir -p "$(dirname "${STATE_FILE}")"
    if [ -f "${STATE_FILE}" ]; then
        sysbox destroy --auto-approve 2>/dev/null || rm -f "${STATE_FILE}" "${STATE_FILE}.lock"
    fi
    sysbox apply --auto-approve
    echo ""
    echo "  node_a  10.0.1.10"
    echo "  node_b  10.0.2.20"

    start_api
}

cmd_down() {
    require_root "down"
    stop_api
    sysbox destroy --auto-approve
}

cmd_status() {
    docker ps --filter "name=sysbox-" --format "  {{.Names}}\t{{.Status}}" 2>/dev/null || true
    echo ""
    sysbox state list 2>/dev/null || echo "  (no state)"
    echo ""
    if [ -f "${API_PID_FILE}" ] && kill -0 "$(cat "${API_PID_FILE}")" 2>/dev/null; then
        echo "==> API  http://localhost${API_ADDR}/v1/health  (PID $(cat "${API_PID_FILE}"))"
    else
        echo "==> API  (not running)"
    fi
}

CMD="${1:-help}"
shift 2>/dev/null || true
case "${CMD}" in
    up)     cmd_up ;;
    down)   cmd_down ;;
    status) cmd_status ;;
    api)    local action="${1:-start}"; case "${action}" in start) start_api ;; stop) stop_api ;; *) echo "Usage: $0 api {start|stop}" ;; esac ;;
    help|--help|-h) sed -n '2,7p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//' ;;
    *) echo "Usage: $0 {up|down|status|api}"; exit 1 ;;
esac
