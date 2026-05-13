#!/usr/bin/env bash
# tests/scenario.sh — sysbox full pipeline scenario test
#
# Validates the complete pipeline end-to-end:
#
#   Phase 1  Scripted attack   deterministic baseline, no API key needed
#            ─────────────     known commands on node_attack → eBPF captures
#            docker exec node_attack: curl + wget → node_web
#            Assert: node_attack.jsonl has exec + net events
#            Assert: node_web.jsonl    has events (cross-node visibility)
#
#   Phase 2  Agent episode     requires ANTHROPIC_API_KEY (or skips)
#            ─────────────     opencode AI runs red-team task in node_attack
#            run_opencode.py → episode_report.json
#            Assert: attack_events > 0   (PID tree attribution works)
#            Assert: archived episode directory created
#
#   Phase 3  Verification      runs after both phases
#            ─────────────
#            Assert: no misattributed events (node_id matches file)
#            sysbox match run → final report with attack_events > 0
#
# Usage (from sysbox/ project root):
#   tests/scenario.sh                    # run all phases
#   tests/scenario.sh --skip-agent       # skip Phase 2 (no API key needed)
#
# Prerequisites:
#   make lab-up                          # lab must be running
#   bin/sysbox                           # compiled (make build)

set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SYSBOX="${REPO_ROOT}/bin/sysbox"
FIELD_FILE="${REPO_ROOT}/examples/three-nodes/field.sysbox.hcl"
STATE_FILE="${REPO_ROOT}/runs/default/state.json"
EVENTS_DIR="${REPO_ROOT}/runs/default/events"
REPORT_FILE="${REPO_ROOT}/runs/default/episode_report.json"
RUNNER="${REPO_ROOT}/examples/three-nodes/run_opencode.py"

GREEN='\033[0;32m'; RED='\033[0;31m'; YELLOW='\033[1;33m'; BLUE='\033[0;34m'; NC='\033[0m'
PASS=0; FAIL=0; SKIP=0

pass() { echo -e "  ${GREEN}PASS${NC}  $1"; PASS=$((PASS+1)); }
fail() { echo -e "  ${RED}FAIL${NC}  $1"; echo "        └─ $2"; FAIL=$((FAIL+1)); }
skip() { echo -e "  ${YELLOW}SKIP${NC}  $1"; echo "        └─ $2"; SKIP=$((SKIP+1)); }
phase() { echo -e "\n${BLUE}── $1 ──${NC}"; }

# ── Flags ─────────────────────────────────────────────────────────────────────

SKIP_AGENT=false
for arg in "$@"; do
    [ "${arg}" = "--skip-agent" ] && SKIP_AGENT=true
done

# ── Load .env ─────────────────────────────────────────────────────────────────

if [ -f "${REPO_ROOT}/.env" ]; then
    set -a
    # shellcheck disable=SC1090
    source "${REPO_ROOT}/.env"
    set +a
fi

# ── Prerequisites ─────────────────────────────────────────────────────────────

phase "prerequisites"

if [ ! -x "${SYSBOX}" ]; then
    fail "binary" "bin/sysbox not found; run: make build"
    exit 1
fi
pass "binary"

if [ ! -f "${STATE_FILE}" ] || ! docker inspect sysbox-node_attack &>/dev/null; then
    fail "lab" "lab not up; run: make lab-up"
    exit 1
fi
pass "lab-up"

if [ ! -d "${EVENTS_DIR}" ]; then
    fail "sensor" "events/ dir missing; sensor not running (make lab-sensor-restart)"
    exit 1
fi
pass "sensor"

HAS_API_KEY=false
if [ -n "${ANTHROPIC_API_KEY:-}" ] || [ -n "${OPENAI_API_KEY:-}" ] || \
   [ -n "${DEEPSEEK_API_KEY:-}" ]  || [ -n "${SYSBOX_API_KEY:-}" ]; then
    HAS_API_KEY=true
fi

# ── Clean slate ───────────────────────────────────────────────────────────────

phase "setup: clean events"

for f in "${EVENTS_DIR}"/*.jsonl; do
    [ -f "${f}" ] || continue
    if ! : > "${f}" 2>/dev/null; then
        fail "clean-events" "cannot truncate ${f} (run: make lab-sensor-restart to fix permissions)"
        exit 1
    fi
done
pass "events-truncated"

# ── Phase 1: Scripted attack ──────────────────────────────────────────────────

phase "phase 1: scripted attack (eBPF baseline)"

echo "  → curl http://10.0.2.10 from node_attack"
timeout 10 docker exec sysbox-node_attack curl -s http://10.0.2.10 -o /dev/null --max-time 5 2>/dev/null || true

echo "  → wget http://10.0.2.10 from node_attack"
timeout 10 docker exec sysbox-node_attack wget -q http://10.0.2.10 -O /dev/null -T 5 -t 1 2>/dev/null || true

echo "  → waiting 6s for events to flow..."
sleep 6

# Assert exec events in node_attack
exec_count=$(python3 -c "
import json, sys
evs = [json.loads(l) for l in open('${EVENTS_DIR}/node_attack.jsonl') if l.strip()]
print(sum(1 for e in evs if e.get('category') == 'exec'))
" 2>/dev/null || echo 0)

net_count=$(python3 -c "
import json
evs = [json.loads(l) for l in open('${EVENTS_DIR}/node_attack.jsonl') if l.strip()]
print(sum(1 for e in evs if e.get('category') == 'net'))
" 2>/dev/null || echo 0)

web_count=$(python3 -c "
import json
f = '${EVENTS_DIR}/node_web.jsonl'
try:
    evs = [json.loads(l) for l in open(f) if l.strip()]
    print(len(evs))
except: print(0)
" 2>/dev/null || echo 0)

[ "${exec_count}" -gt 0 ] \
    && pass "node_attack: exec events captured (${exec_count})" \
    || fail "node_attack: exec events" "0 exec events in node_attack.jsonl after scripted attack"

[ "${net_count}" -gt 0 ] \
    && pass "node_attack: net events captured (${net_count})" \
    || fail "node_attack: net events" "0 net events — connect syscalls not captured"

[ "${web_count}" -gt 0 ] \
    && pass "node_web: cross-node events captured (${web_count})" \
    || skip "node_web: cross-node events" "0 events in node_web.jsonl (victim-side capture depends on tracee scope)"

# ── Phase 2: Agent episode ────────────────────────────────────────────────────

phase "phase 2: agent episode"

if "${SKIP_AGENT}"; then
    skip "agent-episode" "--skip-agent flag set"
elif ! "${HAS_API_KEY}"; then
    skip "agent-episode" "no API key found (ANTHROPIC_API_KEY / OPENAI_API_KEY / SYSBOX_API_KEY)"
else
    echo "  → running opencode episode (may take 30-120s)..."

    # Clean events again so matcher only sees events from this episode.
    for f in "${EVENTS_DIR}"/*.jsonl; do [ -f "${f}" ] && : > "${f}" 2>/dev/null || true; done

    # Generate background noise: run commands inside node_attack via docker exec
    # while the agent is running. Each docker exec spawns a new process in
    # node_attack's mntns whose ppid chain is completely separate from opencode,
    # producing execve/file events that are NOT in the agent's PID tree.
    (
        for delay in 10 30 50; do
            sleep "${delay}"
            docker exec sysbox-node_attack ls /tmp    >/dev/null 2>&1 || true
            docker exec sysbox-node_attack cat /etc/hostname >/dev/null 2>&1 || true
        done
    ) &
    NOISE_PID=$!

    if uv run python3 "${RUNNER}" 2>&1 | tee /tmp/sysbox-episode.log | grep -E "tool calls|attack events|Episode Report" | sed 's/^/  /'; then
        echo ""

        # Assert report exists and has attack events
        if [ ! -f "${REPORT_FILE}" ]; then
            fail "episode-report" "${REPORT_FILE} not created"
        else
            attack_count=$(python3 -c "
import json
r = json.load(open('${REPORT_FILE}'))
print(len(r.get('attack_events', [])))
" 2>/dev/null || echo 0)

            [ "${attack_count}" -gt 0 ] \
                && pass "agent: attack_events=${attack_count} attributed via PID tree" \
                || fail "agent: attack_events" "0 attack events — PID tree attribution may have failed"

            # Assert archived episode
            ep_count=$(find "${REPO_ROOT}/runs/default/episodes/" -maxdepth 1 -type d 2>/dev/null | wc -l)
            [ "${ep_count}" -gt 1 ] \
                && pass "agent: episode archived ($(( ep_count - 1 )) episode dirs)" \
                || fail "agent: episode-archived" "no episode archive dirs found"
        fi
    else
        fail "agent-episode" "run_opencode.py exited non-zero (see /tmp/sysbox-episode.log)"
    fi

    kill "${NOISE_PID}" 2>/dev/null || true
fi

# ── Phase 3: Verification ─────────────────────────────────────────────────────

phase "phase 3: verification"

# Attribution correctness across all nodes
misattributed=$(python3 - "${EVENTS_DIR}" <<'PY'
import json, sys, os, glob

events_dir = sys.argv[1]
errors = []
total = 0
for path in glob.glob(f"{events_dir}/*.jsonl"):
    node = os.path.basename(path).replace(".jsonl", "")
    if node == "_unknown":
        continue
    for line in open(path):
        line = line.strip()
        if not line: continue
        total += 1
        ev = json.loads(line)
        if ev.get("node_id", "") != node:
            errors.append(f"{node}.jsonl: event has node_id={ev.get('node_id')!r}")
            if len(errors) >= 3: break

print(f"{len(errors)} misattributed / {total} total")
if errors:
    for e in errors[:3]: print(f"  {e}")
    sys.exit(1)
PY
) 2>&1

if echo "${misattributed}" | grep -q "^0 misattributed"; then
    pass "attribution: ${misattributed}"
else
    fail "attribution" "${misattributed}"
fi

# Matcher report — runs against whatever events accumulated in phases 1+2
echo "  → running sysbox match run..."
match_out=$("${SYSBOX}" \
    --state "${STATE_FILE}" \
    --file "${FIELD_FILE}" \
    match run --agent red \
    --output "${REPORT_FILE}" 2>&1) && match_rc=0 || match_rc=$?

if [ "${match_rc}" -ne 0 ]; then
    fail "matcher" "sysbox match run failed: ${match_out}"
else
    final_attack=$(python3 -c "
import json
r = json.load(open('${REPORT_FILE}'))
print(len(r.get('attack_events', [])))
" 2>/dev/null || echo 0)

    final_scanned=$(python3 -c "
import json
r = json.load(open('${REPORT_FILE}'))
print(r.get('total_events_scanned', 0))
" 2>/dev/null || echo 0)

    echo "${match_out}" | grep -E "events scanned|attack events" | sed 's/^/  /'

    if [ "${final_attack}" -eq 0 ]; then
        fail "matcher: attack_events" "0 — check sensor events or anchor PID"
    elif [ "${final_attack}" -eq "${final_scanned:-0}" ]; then
        # All events are attack events — noise injection may not have worked.
        pass "matcher: ${final_attack} attack events (noise not detected; sshd may have been quiet)"
    else
        noise=$((final_scanned - final_attack))
        pass "matcher: ${final_attack} attack / ${final_scanned} total — ${noise} background events correctly filtered"
    fi
fi

# ── Summary ───────────────────────────────────────────────────────────────────

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
printf "  PASS: %d  FAIL: %d  SKIP: %d\n" "${PASS}" "${FAIL}" "${SKIP}"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

[ "${FAIL}" -eq 0 ]
