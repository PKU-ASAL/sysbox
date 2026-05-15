#!/usr/bin/env bash
# Mixed topology verification: apply → sensor → destroy → audit.
# Uses --state runs/mixed/state.json so events are isolated to
# runs/mixed/events/ (not mixed with other topologies).
#
# Requires: sudo, docker, firecracker, tracee image.
set -euo pipefail

cd "$(dirname "$0")/.."

HCL=examples/mixed/field.sysbox.hcl
STATE=runs/mixed/state.json
EVENTS=runs/mixed/events

# ── Cleanup ─────────────────────────────────────────────────────────────────

echo "=== full cleanup ==="
sudo pkill -9 firecracker 2>/dev/null || true
sudo rm -rf /tmp/fc-images/sysbox-* "$STATE" 2>/dev/null || true

for tap in $(ip -o link show 2>/dev/null | awk -F': ' '/tap-/{print $2}' | awk '{print $1}'); do
  sudo ip link del "$tap" 2>/dev/null || true
done
for veth in $(ip -o link show 2>/dev/null | awk -F': ' '/v[hg]-/{print $2}' | awk '{print $1}' | cut -d@ -f1); do
  sudo ip link del "$veth" 2>/dev/null || true
done
for ns in $(ip netns list 2>/dev/null | awk '/^sysbox-net-/{print $1}'); do
  sudo ip netns delete "$ns" 2>/dev/null || true
done

docker ps -aq --filter "name=sysbox-" 2>/dev/null | xargs -r docker rm -f >/dev/null 2>&1 || true
docker network ls --format '{{.Name}}' 2>/dev/null | grep '^sysbox-' | xargs -r docker network rm >/dev/null 2>&1 || true

# ── Apply ───────────────────────────────────────────────────────────────────

echo "=== apply ==="
sudo -E ./bin/sysbox apply -f "$HCL" --state "$STATE" --auto-approve 2>&1 | \
  stdbuf -oL grep -vE '^\[\s*[0-9.]+\] |^\[\s*OK\s*\] |systemd\[1\]:|^\s+(Mount|Start|Listen|Reach|Wait|Found|Finish|Crea|Set)'

echo
echo "=== state inventory ==="
jq -r '.resources[] | "\(.type) \(.name)"' "$STATE" 2>/dev/null || echo "(state file missing)"

# ── Sensor (10s — tracee needs ~5s to initialize) ──────────────────────────

echo
echo "=== sensor start (10s probe) ==="
sudo -E timeout 10 ./bin/sysbox sensor start -f "$HCL" --state "$STATE" 2>&1 || true

echo
echo "=== events per node ==="
if [ -d "$EVENTS" ]; then
  for f in "$EVENTS"/node_*.jsonl; do
    [ -f "$f" ] || continue
    name=$(basename "$f" .jsonl)
    count=$(python3 -c "
import json,sys
total=sum(1 for l in sys.stdin if json.loads(l).get('category')!='meta')
print(total)
" < "$f" 2>/dev/null || echo "?")
    echo "  $name: $count events"
  done
else
  echo "  (no events directory)"
fi

# ── Destroy ─────────────────────────────────────────────────────────────────

echo
echo "=== destroy ==="
sudo -E ./bin/sysbox destroy -f "$HCL" --state "$STATE" --auto-approve 2>&1 | tail -20

# ── Audit ───────────────────────────────────────────────────────────────────

echo
echo "=== post-destroy audit ==="

echo "-- TAP devices --"
out=$(ip -o link show 2>/dev/null | awk -F': ' '/tap-/{print "  " $2}')
[ -z "$out" ] && echo "  (none)" || echo "$out"

echo "-- veths --"
out=$(ip -o link show 2>/dev/null | awk -F': ' '/v[hg]-/{print "  " $2}')
[ -z "$out" ] && echo "  (none)" || echo "$out"

echo "-- netns --"
out=$(ip netns list 2>/dev/null | grep -E '^sysbox-net-')
[ -z "$out" ] && echo "  (none)" || echo "$out"

echo "-- firecracker procs --"
out=$(pgrep -af firecracker 2>/dev/null | grep -v defunct || true)
[ -z "$out" ] && echo "  (none)" || echo "$out"

echo "-- FC images --"
out=$(ls -d /tmp/fc-images/sysbox-* 2>/dev/null || true)
[ -z "$out" ] && echo "  (none)" || echo "$out"

echo "-- docker resources --"
docker ps -a --filter "name=sysbox-" --format '  {{.Names}} {{.Status}}' 2>/dev/null || true
docker network ls --format '{{.Name}}' 2>/dev/null | grep '^sysbox-' | sed 's/^/  /' || true

echo "-- state resource count --"
jq '.resources | length' "$STATE" 2>/dev/null || echo "(state file missing)"
