#!/usr/bin/env bash
# lab.sh — sysbox mixed (docker + firecracker) lab lifecycle
#
# Usage (from sysbox/ project root):
#   sudo -E examples/mixed/lab.sh up             # build → destroy old → apply → sensor
#   sudo -E examples/mixed/lab.sh down           # destroy + stop sensor
#          examples/mixed/lab.sh logs            # tail sensor log
#          examples/mixed/lab.sh status          # containers, state, sensor
#
# Prerequisites:
#   - Docker running, firecracker in PATH
#   - SYSBOX_ROOTFS set (or default ~/.cache/sysbox/rootfs/ubuntu-24.04.ext4)
#   - DEEPSEEK_API_KEY in env or .env file (for opencode actor)
#   - sudo -E for up/down (firecracker + Docker both need root)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
FIELD_FILE="${SCRIPT_DIR}/field.sysbox.hcl"
STATE_FILE="${REPO_ROOT}/runs/mixed/state.json"
SYSBOX="${REPO_ROOT}/bin/sysbox"
SENSOR_LOG="/tmp/sysbox-sensor.log"

GO="${GO:-$(command -v go 2>/dev/null || echo /usr/local/go/bin/go)}"

load_dotenv() {
    local env_file="${REPO_ROOT}/.env"
    [ -f "${env_file}" ] || return 0
    while IFS= read -r line; do
        [[ "$line" =~ ^[[:space:]]*# ]] && continue
        [[ -z "${line// }" ]] && continue
        key="${line%%=*}"; val="${line#*=}"
        [[ -n "$key" ]] && export "${key}=${val}"
    done < "${env_file}"
}
load_dotenv

die() { echo "ERROR: $*" >&2; exit 1; }
require_root() { [ "$(id -u)" = "0" ] || die "Run: sudo -E $0 $*"; }

sysbox() { "${SYSBOX}" --state "${STATE_FILE}" --file "${FIELD_FILE}" "$@"; }

build_sysbox() {
    echo "==> Building sysbox..."
    cd "${REPO_ROOT}"
    CGO_ENABLED=0 "${GO}" build -o bin/sysbox ./cmd/sysbox
}

build_image() {
    echo "==> Building attacker image..."
    docker build --network=host \
        -t sysbox-attacker:latest \
        -f "${REPO_ROOT}/examples/three-nodes/Dockerfile.attacker-opencode" \
        "${REPO_ROOT}/examples/three-nodes"
    echo "    sysbox-attacker:latest ready"
}

stop_sensor() {
    pkill -f "sysbox.*sensor start" 2>/dev/null || true
    docker exec sysbox-sensor pkill -9 tracee 2>/dev/null || true
    sleep 0.5
}

start_sensor() {
    echo "==> Starting eBPF sensor (Docker nodes)..."
    stop_sensor
    setsid nohup "${SYSBOX}" --state "${STATE_FILE}" --file "${FIELD_FILE}" \
        sensor start > "${SENSOR_LOG}" 2>&1 &
    local spid=$!
    echo "    sensor PID ${spid}  log: ${SENSOR_LOG}"
    local waited=0
    while [ "${waited}" -lt 20 ]; do
        sleep 2; waited=$((waited+2))
        grep -q "started in sysbox-sensor" "${SENSOR_LOG}" 2>/dev/null && break
    done
    grep -E "mntns=|started in|monitor|Error" "${SENSOR_LOG}" 2>/dev/null | sed 's/^/    /' || true
}

cmd_up() {
    require_root "up"
    build_sysbox
    build_image
    mkdir -p "$(dirname "${STATE_FILE}")"
    if [ -f "${STATE_FILE}" ]; then
        echo "==> Destroying previous state..."
        sysbox destroy --auto-approve 2>/dev/null || true
    fi
    echo "==> Applying mixed topology..."
    sysbox apply --auto-approve
    echo ""
    start_sensor
    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo " Lab UP  (docker + firecracker)"
    echo ""
    echo "  node_attack  10.0.1.10 / 172.20.0.10  attacker (docker)"
    echo "  node_web     10.0.2.10                nginx    (docker)"
    echo "  node_db      10.0.2.20                postgres (firecracker)"
    echo ""
    echo "  ACP endpoint:  http://172.20.0.10:4096"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
}

cmd_down() {
    require_root "down"
    echo "==> Stopping sensor..."
    stop_sensor
    echo "==> Destroying mixed topology..."
    sysbox destroy --auto-approve
    echo "Down."
}

cmd_logs() {
    echo "==> Sensor log (${SENSOR_LOG})"
    tail -f "${SENSOR_LOG}"
}

cmd_status() {
    echo "==> Containers"
    docker ps --filter "name=sysbox-" --format "  {{.Names}}\t{{.Status}}" 2>/dev/null || true
    echo ""
    echo "==> State"
    sysbox state list 2>/dev/null || echo "  (no state)"
    echo ""
    echo "==> Sensor"
    if pgrep -f "sysbox.*sensor start" > /dev/null 2>&1; then
        pgrep -a -f "sysbox.*sensor start" | sed 's/^/  /'
    else
        echo "  not running"
    fi
}

CMD="${1:-help}"
shift 2>/dev/null || true
case "${CMD}" in
    up)     cmd_up ;;
    down)   cmd_down ;;
    logs)   cmd_logs ;;
    status) cmd_status ;;
    help|--help|-h)
        sed -n '2,12p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
        ;;
    *)
        echo "Unknown command: ${CMD}"
        echo "Usage: $0 {up|down|logs|status}"
        exit 1
        ;;
esac
