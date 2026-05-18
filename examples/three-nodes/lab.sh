#!/usr/bin/env bash
# lab.sh — sysbox three-node attack lab lifecycle manager
#
# Usage (from sysbox/ project root):
#   sudo -E examples/three-nodes/lab.sh up             # build → destroy old → apply → sensor
#   sudo -E examples/three-nodes/lab.sh down            # destroy lab + stop sensor
#          examples/three-nodes/lab.sh status           # containers, state, sensor
#          examples/three-nodes/lab.sh exec <node> [cmd...]  # shell into a node
#   sudo -E examples/three-nodes/lab.sh sensor          # (re)start sensor only
#   sudo -E examples/three-nodes/lab.sh sensor-restart  # restart sensor after node reprovision
#          examples/three-nodes/lab.sh logs             # tail sensor log
#          examples/three-nodes/lab.sh clean            # remove per-episode artefacts (keep state/keys)
#
# Prerequisites:
#   - Docker running
#   - ANTHROPIC_API_KEY + ANTHROPIC_BASE_URL + SYSBOX_MODEL in .env or environment
#   - sudo -E (preserve environment) for up/down

set -euo pipefail

# ── Paths (absolute, script-location-independent) ─────────────────────────────
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
FIELD_FILE="${SCRIPT_DIR}/field.sysbox.hcl"
STATE_FILE="${REPO_ROOT}/runs/default/state.json"
SYSBOX="${REPO_ROOT}/bin/sysbox"
SENSOR_LOG="/tmp/sysbox-sensor.log"
EVENTS_DIR="/tmp/sysbox-events"

GO="${GO:-$(command -v go 2>/dev/null || echo /usr/local/go/bin/go)}"

# Load .env from repo root so env() calls in HCL can see SYSBOX_MODEL etc.
# We do this early (before sudo context changes anything) and export everything.
load_dotenv() {
    local env_file="${REPO_ROOT}/.env"
    [ -f "${env_file}" ] || return 0
    while IFS= read -r line; do
        [[ "$line" =~ ^[[:space:]]*# ]] && continue
        [[ -z "${line// }" ]] && continue
        key="${line%%=*}"
        val="${line#*=}"
        [[ -n "$key" ]] && export "${key}=${val}"
    done < "${env_file}"
}
load_dotenv

# ── Helpers ───────────────────────────────────────────────────────────────────

die() { echo "ERROR: $*" >&2; exit 1; }

require_root() {
    [ "$(id -u)" = "0" ] || die "This command requires root. Run: sudo -E $0 $*"
}

sysbox() { "${SYSBOX}" --state "${STATE_FILE}" --file "${FIELD_FILE}" "$@"; }

build_sysbox() {
    echo "==> Building sysbox..."
    cd "${REPO_ROOT}"
    CGO_ENABLED=0 "${GO}" build -o bin/sysbox ./cmd/sysbox
    echo "    bin/sysbox ready ($(bin/sysbox version 2>/dev/null || echo ok))"
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

stop_sensor() {
    pkill -f "sysbox.*sensor start" 2>/dev/null || true
    # Also kill tracee running inside the sensor container (docker exec -d
    # detaches the client; the tracee process itself must be killed separately).
    docker exec sysbox-sensor pkill -9 tracee 2>/dev/null || true
    sleep 0.5
}

start_sensor() {
    echo "==> Starting eBPF sensor..."
    stop_sensor
    mkdir -p "${EVENTS_DIR}"

    # sysbox sensor start reads sysbox_monitor from state, resolves mntns IDs,
    # starts tracee inside the sensor container, and tails the event stream —
    # all driven by the TraceeBackend in pkg/monitor/tracee.go.
    setsid nohup "${SYSBOX}" --state "${STATE_FILE}" --file "${FIELD_FILE}" \
        sensor start \
        > "${SENSOR_LOG}" 2>&1 &
    local spid=$!
    echo "    sensor PID ${spid}  log: ${SENSOR_LOG}"
    # Poll until tracee reports "started" (eBPF init takes ~8-10s).
    local waited=0
    while [ "${waited}" -lt 20 ]; do
        sleep 2; waited=$((waited+2))
        if grep -q "started in sysbox-sensor" "${SENSOR_LOG}" 2>/dev/null; then
            break
        fi
    done
    grep -E "mntns=|started in|monitor lab|Error" "${SENSOR_LOG}" 2>/dev/null | sed 's/^/    /' || true
}

# ── Commands ──────────────────────────────────────────────────────────────────

cmd_up() {
    require_root "up"
    local real_user="${SUDO_USER:-${USER}}"

    build_sysbox
    build_image

    mkdir -p "$(dirname "${STATE_FILE}")"

    # Generate a lab-specific SSH keypair (never use personal ~/.ssh keys).
    # The public key is injected into lab nodes via the HCL file provisioner.
    LAB_KEY="$(dirname "${STATE_FILE}")/lab_key"
    export LAB_SSH_PUBKEY="${LAB_KEY}.pub"
    if [ ! -f "${LAB_KEY}" ]; then
        echo "==> Generating lab SSH keypair..."
        ssh-keygen -t ed25519 -f "${LAB_KEY}" -N "" -C "sysbox-lab" -q
        chmod 600 "${LAB_KEY}"
        chmod 644 "${LAB_SSH_PUBKEY}"
        chown "${real_user}:${real_user}" "${LAB_KEY}" "${LAB_SSH_PUBKEY}"
        echo "    ${LAB_KEY} (private, never shared)"
    else
        echo "==> Using existing lab key: ${LAB_SSH_PUBKEY}"
    fi

    # Tear down previous deployment cleanly before rebuilding.
    if [ -f "${STATE_FILE}" ]; then
        echo ""
        echo "==> Destroying previous state..."
        sysbox destroy --auto-approve 2>/dev/null || rm -f "${STATE_FILE}" "${STATE_FILE}.lock"
    fi

    echo ""
    echo "==> Cleaning stale netns..."
    clean_netns

    echo ""
    echo "==> Applying field..."
    sysbox apply --auto-approve

    # Return runs/ ownership to the calling user.
    chown -R "${real_user}:${real_user}" "$(dirname "${STATE_FILE}")"

    echo ""
    start_sensor

    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo " Lab UP"
    echo ""
    echo "  node_attack   10.0.1.10 / 172.20.0.10   attacker + opencode"
    echo "  node_web      10.0.2.10                  nginx"
    echo "  node_db       10.0.2.20                  postgres:16-alpine :5432"
    echo "  sensor        (tracee sidecar)            events → ${EVENTS_DIR}/events.jsonl"
    echo ""
    echo "  ACP endpoint:   http://172.20.0.10:4096"
    echo "  Run episode:    uv run python3 examples/three-nodes/run_opencode.py"
    echo "  Inspect events: ls runs/default/events/"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
}

cmd_down() {
    require_root "down"
    echo "==> Stopping sensor..."
    stop_sensor

    echo "==> Destroying lab..."
    sysbox destroy --auto-approve

    echo "==> Cleaning stale netns..."
    clean_netns

    echo "Down."
}

cmd_status() {
    echo "==> Containers"
    docker ps --filter "name=sysbox-" \
        --format "  {{.Names}}\t{{.Status}}" 2>/dev/null || true

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
print(f'  pid={i.get(\"pid\",\"?\")}  port={i.get(\"port\",\"?\")}  acp={i.get(\"acp_url\",\"?\")}  node={i.get(\"node\",\"?\")}')
" 2>/dev/null || echo "  sysbox_actor.red not in state (try sysbox_agent.red for legacy state)"

    echo ""
    echo "==> Sensor"
    if pgrep -f "sysbox.*sensor start" > /dev/null 2>&1; then
        pgrep -a -f "sysbox.*sensor start" | sed 's/^/  /'
        echo "  log: ${SENSOR_LOG}"
        local events_dir="${REPO_ROOT}/runs/default/events"
        if [ -d "${events_dir}" ]; then
            for f in "${events_dir}/"*.jsonl; do
                [ -f "${f}" ] && printf "  %-30s %d lines\n" \
                    "events/$(basename "${f}")" "$(wc -l < "${f}")"
            done
        else
            echo "  events: (not yet)"
        fi
    else
        echo "  not running"
    fi
}

cmd_exec() {
    local node="${1:-node_attack}"
    shift 2>/dev/null || true
    local cmd=("${@}")
    [ ${#cmd[@]} -eq 0 ] && cmd=("/bin/bash")
    docker exec -it "sysbox-${node}" "${cmd[@]}"
}

cmd_sensor() {
    require_root "sensor"
    mkdir -p "${EVENTS_DIR}"
    start_sensor
}

cmd_sensor_restart() {
    require_root "sensor-restart"
    echo "==> Restarting sensor (re-resolves node handles)..."
    setsid nohup "${SYSBOX}" --state "${STATE_FILE}" --file "${FIELD_FILE}" \
        sensor restart \
        > "${SENSOR_LOG}" 2>&1 &
    local spid=$!
    echo "    sensor PID ${spid}  log: ${SENSOR_LOG}"
    # Wait for tracee eBPF init (takes ~8-10s); poll for the "started" line.
    local waited=0
    while [ "${waited}" -lt 20 ]; do
        sleep 2; waited=$((waited+2))
        if grep -q "started in sysbox-sensor" "${SENSOR_LOG}" 2>/dev/null; then
            break
        fi
    done
    grep -E "mntns=|started in|monitor lab|Error" "${SENSOR_LOG}" 2>/dev/null | sed 's/^/    /' || true
}

cmd_logs() {
    echo "==> Sensor log (${SENSOR_LOG})"
    tail -f "${SENSOR_LOG}"
}

cmd_clean() {
    local runs_dir="${REPO_ROOT}/runs/default"
    echo "==> Cleaning episode outputs in ${runs_dir}/episodes/"
    local count=0
    if [ -d "${runs_dir}/episodes" ]; then
        count=$(find "${runs_dir}/episodes" -mindepth 1 -maxdepth 1 -type d | wc -l)
        rm -rf "${runs_dir}/episodes"
        echo "    removed ${count} episode(s)"
    else
        echo "    nothing to remove"
    fi
    # Also remove stale root-level step_log (legacy location).
    rm -f "${runs_dir}/step_log.jsonl" 2>/dev/null
    echo ""
    echo "    Preserved:"
    echo "      state.json         — topology state (sysbox apply)"
    echo "      events/*.jsonl     — sensor syscall stream (append-only)"
    echo "      lab_key / lab_key.pub"
}

# ── Dispatch ──────────────────────────────────────────────────────────────────

CMD="${1:-help}"
shift 2>/dev/null || true

case "${CMD}" in
    up)              cmd_up ;;
    down)            cmd_down ;;
    status)          cmd_status ;;
    exec)            cmd_exec "$@" ;;
    sensor)          cmd_sensor ;;
    sensor-restart)  cmd_sensor_restart ;;
    logs)            cmd_logs ;;
    clean)           cmd_clean ;;
    help|--help|-h)
        sed -n '2,15p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
        ;;
    *)
        echo "Unknown command: ${CMD}"
        echo "Usage: $0 {up|down|status|exec|sensor|sensor-restart|logs|clean}"
        exit 1
        ;;
esac
