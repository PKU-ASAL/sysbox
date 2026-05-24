#!/usr/bin/env bash
# lab.sh — sysbox three-node attack lab lifecycle
#
# Usage (from sysbox/ project root):
#   sudo -E examples/three-nodes/lab.sh up      # build image + apply topology
#   sudo -E examples/three-nodes/lab.sh down     # destroy topology
#          examples/three-nodes/lab.sh status    # show containers and state
#          examples/three-nodes/lab.sh exec <node> [cmd...]
#
# Prerequisites:
#   - Docker running
#   - DEEPSEEK_API_KEY (or other LLM key) in .env or environment
#   - sudo -E for up/down

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
FIELD_FILE="${SCRIPT_DIR}/field.sysbox.hcl"
STATE_FILE="${REPO_ROOT}/.sysbox/runs/three-nodes/state.json"
SYSBOX="${REPO_ROOT}/bin/sysbox"
API_ADDR="${SYSBOX_API_ADDR:-:9876}"
API_PID_FILE="$(dirname "${STATE_FILE}")/api.pid"

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

start_api() {
    # Stop any previous API server on this port.
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
    # Kill via pid file if available.
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
    # Fallback: kill any process listening on API_ADDR.
    local port
    port="${API_ADDR##*:}"
    if [ -n "${port}" ] && command -v fuser >/dev/null 2>&1; then
        fuser -k "${port}/tcp" 2>/dev/null || true
    fi
}

build_sysbox() {
    echo "==> Building sysbox..."
    cd "${REPO_ROOT}"
    CGO_ENABLED=0 "${GO}" build -buildvcs=false -o bin/sysbox ./cmd/sysbox
}

build_image() {
    echo "==> Building attacker image..."
    docker build --network=host \
        -t sysbox-attacker:latest \
        -f "${SCRIPT_DIR}/Dockerfile.attacker-opencode" \
        "${SCRIPT_DIR}"
    echo "    sysbox-attacker:latest ready"
}

clean_netns() {
    local count=0
    for ns in $(ip netns list 2>/dev/null | awk '{print $1}' | grep "^sysbox-"); do
        ip netns del "$ns" 2>/dev/null && count=$((count+1)) || true
    done
    [ $count -gt 0 ] && echo "    cleaned $count stale netns" || true
}

cmd_up() {
    require_root "up"
    local real_user="${SUDO_USER:-${USER}}"

    build_sysbox
    build_image

    mkdir -p "$(dirname "${STATE_FILE}")"

    # Lab-specific SSH keypair; public key injected via HCL provisioner "file".
    LAB_KEY="$(dirname "${STATE_FILE}")/lab_key"
    export LAB_SSH_PUBKEY="${LAB_KEY}.pub"
    if [ ! -f "${LAB_KEY}" ]; then
        echo "==> Generating lab SSH keypair..."
        ssh-keygen -t ed25519 -f "${LAB_KEY}" -N "" -C "sysbox-lab" -q
        chmod 600 "${LAB_KEY}"
        chmod 644 "${LAB_SSH_PUBKEY}"
        chown "${real_user}:${real_user}" "${LAB_KEY}" "${LAB_SSH_PUBKEY}"
    else
        echo "==> Using existing lab key: ${LAB_SSH_PUBKEY}"
    fi

    if [ -f "${STATE_FILE}" ]; then
        echo "==> Destroying previous state..."
        sysbox destroy --auto-approve 2>/dev/null || rm -f "${STATE_FILE}" "${STATE_FILE}.lock"
    fi

    echo "==> Cleaning stale netns..."
    clean_netns

    echo "==> Applying topology..."
    sysbox apply --auto-approve

    chown -R "${real_user}:${real_user}" "$(dirname "${STATE_FILE}")"

    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo " Lab UP"
    echo ""
    echo "  node_attack   10.0.1.10 / 172.20.0.10   attacker + opencode"
    echo "  node_web      10.0.2.10                  nginx"
    echo "  node_db       10.0.2.20                  postgres:16-alpine :5432"
    echo ""
    echo "  ACP endpoint:   http://172.20.0.10:4096"
    echo "  API endpoint:   http://localhost${API_ADDR}/v1/health"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

    start_api
}

cmd_down() {
    require_root "down"
    stop_api
    sysbox destroy --auto-approve
    clean_netns
    echo "Down."
}

cmd_status() {
    echo "==> Containers"
    docker ps --filter "name=sysbox-" --format "  {{.Names}}\t{{.Status}}" 2>/dev/null || true
    echo ""
    echo "==> State"
    sysbox state list 2>/dev/null || echo "  (no state)"
    echo ""
    echo "==> Actor 'red'"
    sysbox state show sysbox_actor.red 2>/dev/null \
        | python3 -c "
import sys, json
d = json.load(sys.stdin)
i = d.get('instance', {})
print(f'  pid={i.get(\"pid\",\"?\")}  port={i.get(\"port\",\"?\")}  acp={i.get(\"acp_url\",\"?\")}')
" 2>/dev/null || echo "  not in state"
    echo ""
    if [ -f "${API_PID_FILE}" ] && kill -0 "$(cat "${API_PID_FILE}")" 2>/dev/null; then
        echo "==> API  http://localhost${API_ADDR}/v1/health  (PID $(cat "${API_PID_FILE}"))"
    else
        echo "==> API  (not running)"
    fi
}

cmd_exec() {
    local node="${1:-node_attack}"
    shift 2>/dev/null || true
    local cmd=("${@}")
    [ ${#cmd[@]} -eq 0 ] && cmd=("/bin/bash")
    docker exec -it "sysbox-${node}" "${cmd[@]}"
}

cmd_api() {
    local action="${1:-start}"
    case "${action}" in
        start) start_api ;;
        stop)  stop_api ;;
        *)
            echo "Usage: $0 api {start|stop}"
            echo "  API_ADDR=${API_ADDR}"
            ;;
    esac
}

CMD="${1:-help}"
shift 2>/dev/null || true
case "${CMD}" in
    up)     cmd_up ;;
    down)   cmd_down ;;
    status) cmd_status ;;
    exec)   cmd_exec "$@" ;;
    api)    cmd_api "$@" ;;
    help|--help|-h) sed -n '2,13p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//' ;;
    *) echo "Usage: $0 {up|down|status|exec|api}"; exit 1 ;;
esac
