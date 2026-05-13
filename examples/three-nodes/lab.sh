#!/usr/bin/env bash
# lab.sh — 3-node attack lab lifecycle manager
#
# Usage:
#   ./lab.sh up       # build, apply field, prepare nodes, start sensor + hook
#   ./lab.sh down     # destroy field
#   ./lab.sh status   # show running containers + hook server status
#   ./lab.sh exec <node> <cmd...>   # run a command on a node
#
# Before running: sudo -E ./lab.sh up

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
FIELD_FILE="$(dirname "$0")/field.sysbox.hcl"
STATE_FILE="${REPO_ROOT}/runs/default/state.json"
RULES_DIR="${REPO_ROOT}/rules"
HOOK_PORT=8081

SYSBOX="${REPO_ROOT}/bin/sysbox"

build_sysbox() {
    echo "==> Building sysbox..."
    cd "${REPO_ROOT}"
    go build -o bin/sysbox ./cmd/sysbox
    echo "    bin/sysbox ready"
}

cmd_up() {
    if [ "$(id -u)" != "0" ]; then
        echo "ERROR: lab up requires root (netns/veth creation)."
        echo "  Run: sudo -E $0 up"
        exit 1
    fi

    build_sysbox

    echo ""
    echo "==> Building attacker image (tools pre-installed for --network none)..."
    docker build -q -t sysbox-attacker:latest \
        -f "$(dirname "$0")/Dockerfile.attacker" \
        "$(dirname "$0")"
    echo "    sysbox-attacker:latest ready"

    echo ""
    echo "==> Applying field (${FIELD_FILE})..."
    "${SYSBOX}" --state "${STATE_FILE}" apply --file "${FIELD_FILE}" --auto-approve

    echo ""
    echo "==> Configuring SSH on node_attack (tools already in image)..."
    # Write the host's public key so node_attack can pivot to other nodes.
    PUB_KEY="$(cat ~/.ssh/id_rsa.pub 2>/dev/null || cat ~/.ssh/id_ed25519.pub 2>/dev/null || echo '')"
    if [ -n "${PUB_KEY}" ]; then
        docker exec sysbox-node_attack sh -c \
            "mkdir -p /root/.ssh && echo '${PUB_KEY}' >> /root/.ssh/authorized_keys && chmod 600 /root/.ssh/authorized_keys"
        echo "    SSH key installed"
    else
        echo "    warn: no ~/.ssh/id_rsa.pub found; skipping key install"
    fi
    # node_web runs nginx (started automatically by the nginx:alpine CMD).

    echo ""
    echo "==> Starting mock DB listener on node_db (port 5432)..."
    # nc -lk: listen on 5432, respond with fake postgres banner
    docker exec -d sysbox-node_db sh -c \
        'while true; do echo "mock-postgres-5.14.3" | nc -l -p 5432; done' || true

    echo ""
    echo "==> Fixing runs/ ownership (apply runs as root, episode runs as user)..."
    chown -R "${SUDO_USER:-$USER}:${SUDO_USER:-$USER}" "$(dirname "${STATE_FILE}")" 2>/dev/null || true

    echo ""
    echo "==> Starting sysbox sensor (tracee, global scope)..."
    # sensor start auto-writes to runs/default/events.jsonl
    # requires --file so loadWorkspace() can read the HCL
    "${SYSBOX}" --state "${STATE_FILE}" --file "${FIELD_FILE}" sensor start &
    SENSOR_PID=$!
    echo "    sensor PID: ${SENSOR_PID}"
    sleep 3  # give tracee time to initialize

    echo ""
    echo "==> Starting hook server (port ${HOOK_PORT})..."
    "${SYSBOX}" --state "${STATE_FILE}" \
        hook serve --port ${HOOK_PORT} --rules "${RULES_DIR}" &
    HOOK_PID=$!
    echo "    hook PID: ${HOOK_PID}"
    sleep 1

    echo ""
    echo "==> Installing Claude Code hook config..."
    cd "${REPO_ROOT}"
    "${SYSBOX}" --state "${STATE_FILE}" hook install --port ${HOOK_PORT}

    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo " Lab is UP. Attack topology:"
    echo ""
    echo "  node_attack  10.0.1.10   (attacker jump box)"
    echo "  node_web     10.0.2.10   (nginx web server)"
    echo "  node_db      10.0.2.20   (mock DB, port 5432)"
    echo ""
    echo " Connect to attacker:"
    echo "  docker exec -it sysbox-node_attack sh"
    echo ""
    echo " Run a simulated attack step:"
    echo "  docker exec sysbox-node_attack nmap -sS 10.0.2.0/24"
    echo "  docker exec sysbox-node_attack ssh root@10.0.2.10 'id'"
    echo "  docker exec sysbox-node_attack curl http://10.0.2.10/"
    echo ""
    echo " After the attack episode:"
    echo "  sudo ${SYSBOX} --state ${STATE_FILE} match run"
    echo "  ${SYSBOX} --state ${STATE_FILE} match report"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
}

cmd_down() {
    if [ "$(id -u)" != "0" ]; then
        echo "ERROR: lab down requires root."
        exit 1
    fi
    build_sysbox
    echo "==> Destroying field..."
    "${SYSBOX}" --state "${STATE_FILE}" destroy --file "${FIELD_FILE}" --auto-approve
    echo "==> Stopping sensor and hook..."
    pkill -f "sysbox.*sensor" 2>/dev/null || true
    pkill -f "sysbox.*hook" 2>/dev/null || true
    echo "Done."
}

cmd_status() {
    echo "==> Running sysbox containers:"
    docker ps --filter "name=sysbox-" --format "  {{.Names}}\t{{.Status}}\t{{.Image}}" 2>/dev/null || echo "  (none)"

    echo ""
    echo "==> Hook server:"
    curl -s "http://127.0.0.1:${HOOK_PORT}/hooks/status" 2>/dev/null | python3 -m json.tool || echo "  not running"

    echo ""
    echo "==> Events:"
    EVENTS="${REPO_ROOT}/runs/default/events.jsonl"
    if [ -f "${EVENTS}" ]; then
        echo "  $(wc -l < "${EVENTS}") events in ${EVENTS}"
    else
        echo "  no events yet"
    fi

    echo ""
    echo "==> Predictions:"
    PREDS="${REPO_ROOT}/runs/default/predictions.jsonl"
    if [ -f "${PREDS}" ]; then
        echo "  $(wc -l < "${PREDS}") predictions in ${PREDS}"
    else
        echo "  no predictions yet"
    fi
}

cmd_exec() {
    NODE="${1:-node_attack}"
    shift
    docker exec "sysbox-${NODE}" "$@"
}

case "${1:-help}" in
    up)     cmd_up ;;
    down)   cmd_down ;;
    status) cmd_status ;;
    exec)   shift; cmd_exec "$@" ;;
    *)
        echo "Usage: $0 {up|down|status|exec <node> <cmd...>}"
        exit 1
        ;;
esac
