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

# When the script runs under sudo, root's PATH lacks the user's
# personal bin dirs (e.g. ~/.local/bin where firecracker lives).
# Preserve the caller's PATH so that `sudo -E` sub-commands find it.
if [ -n "${SUDO_USER:-}" ] && [ -z "${SYSBOX_PRESERVE_PATH:-}" ]; then
  export SYSBOX_PRESERVE_PATH=1
  USER_PATH=$(sudo -u "$SUDO_USER" env | grep '^PATH=' | head -1 | cut -d= -f2-)
  if [ -n "$USER_PATH" ]; then
    export PATH="$USER_PATH:$PATH"
  fi
  # Also set SYSBOX_FC_BIN explicitly — the firecracker provider checks
  # this env var first and it bypasses PATH/HOME lookups entirely.
  FC_BIN=$(sudo -u "$SUDO_USER" which firecracker 2>/dev/null || true)
  if [ -n "$FC_BIN" ]; then
    export SYSBOX_FC_BIN="$FC_BIN"
  fi
fi

# When the script itself runs under sudo, $HOME is /root and the HCL
# local.rootfs_path (derived from env("HOME")) resolves to the wrong
# directory. Export SYSBOX_ROOTFS to override it with the real user's
# cache path. "sudo -E" on individual commands then propagates this.
if [ -z "${SYSBOX_ROOTFS:-}" ]; then
  REAL_HOME="${SUDO_USER_HOME:-}"
  if [ -z "$REAL_HOME" ] && [ -n "${SUDO_USER:-}" ]; then
    REAL_HOME=$(getent passwd "$SUDO_USER" 2>/dev/null | cut -d: -f6)
  fi
  if [ -n "$REAL_HOME" ]; then
    export SYSBOX_ROOTFS="$REAL_HOME/.cache/sysbox/rootfs/ubuntu-24.04.ext4"
  fi
fi

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
