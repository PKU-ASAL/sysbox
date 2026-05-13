#!/usr/bin/env bash
# run_cli.sh — Mode A: interactive Claude Code CLI with HTTP hook integration.
#
# Prerequisites (run first):
#   sudo -E ./lab.sh up
#
# What this does:
#   1. Verifies the sensor and hook server are running
#   2. Prints lab topology as context
#   3. Launches `claude` with the attack prompt pre-loaded
#
# After the session ends:
#   sudo sysbox match run
#   sysbox match report

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
STATE_FILE="${REPO_ROOT}/runs/default/state.json"
HOOK_PORT=8081
PROMPT_FILE="$(dirname "$0")/prompts/attack.txt"

# ── Preflight checks ──────────────────────────────────────────────────────────

check_sensor() {
    local events="${REPO_ROOT}/runs/default/events.jsonl"
    if [ ! -f "${events}" ]; then
        echo "ERROR: Sensor not running. Start with: sudo -E ./lab.sh up"
        exit 1
    fi
    echo "  sensor: OK ($(wc -l < "${events}") events so far)"
}

check_hook_server() {
    local status
    status=$(curl -s --max-time 2 "http://127.0.0.1:${HOOK_PORT}/hooks/status" 2>/dev/null || echo "")
    if [ -z "${status}" ]; then
        echo "ERROR: Hook server not running at :${HOOK_PORT}."
        echo "  Start with: sudo -E ./lab.sh up"
        exit 1
    fi
    local preds
    preds=$(echo "${status}" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('predictions_written',0))" 2>/dev/null || echo "?")
    echo "  hook:   OK (${preds} predictions so far)"
}

check_claude() {
    if ! command -v claude &>/dev/null; then
        echo "ERROR: claude not found in PATH."
        echo "  Install: npm install -g @anthropic-ai/claude-code"
        exit 1
    fi
    echo "  claude: $(claude --version 2>/dev/null | head -1)"
}

# ── Main ──────────────────────────────────────────────────────────────────────

echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo " sysbox Mode A — Interactive Claude Code Session"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

echo ""
echo "Checking prerequisites..."
check_sensor
check_hook_server
check_claude

echo ""
echo "Lab topology:"
echo "  node_attack  10.0.1.10   (exec: docker exec sysbox-node_attack)"
echo "  node_web     10.0.2.10   (nginx)"
echo "  node_db      10.0.2.20   (mock DB, port 5432)"
echo ""
echo "Hook server:  http://127.0.0.1:${HOOK_PORT}"
echo "Events:       ${REPO_ROOT}/runs/default/events.jsonl"
echo "Predictions:  ${REPO_ROOT}/runs/default/predictions.jsonl"
echo ""
echo "Every Bash command you give Claude will be intercepted by the hook"
echo "server and recorded as an IoC prediction automatically."
echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo " Starting claude... (type /exit to end the session)"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

# Launch Claude Code in the repo root so it picks up .claude/settings.json
# (which contains the HTTP hook configuration installed by `sysbox hook install`).
# Pass the attack prompt as the initial user message.
cd "${REPO_ROOT}"
exec claude --print "$(cat "${PROMPT_FILE}")" --continue
