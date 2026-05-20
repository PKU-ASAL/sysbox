#!/usr/bin/env bash
# Microvm topology verification: full clean → apply → sensor → destroy → audit.
# Uses --state runs/microvm/state.json so events are isolated to
# runs/microvm/events/.
#
# Usage:  sudo ./scripts/microvm-verify.sh
#
# The script is designed to run entirely as root (via sudo on the
# script itself). All internal commands run without sudo since we
# are already root.

set -euo pipefail

cd "$(dirname "$0")/.."

HCL=examples/microvm/field.sysbox.hcl
STATE=runs/microvm/state.json

# ── Environment fixup when running under sudo ───────────────────────────────

if [ -n "${SUDO_USER:-}" ]; then
  REAL_HOME=$(getent passwd "$SUDO_USER" 2>/dev/null | cut -d: -f6 || echo "")

  # Preserve caller's PATH so firecracker etc. are found.
  if [ -z "${SYSBOX_PRESERVE_PATH:-}" ]; then
    export SYSBOX_PRESERVE_PATH=1
    USER_PATH=$(sudo -u "$SUDO_USER" env | grep '^PATH=' | head -1 | cut -d= -f2-)
    if [ -n "$USER_PATH" ]; then
      export PATH="$USER_PATH:$PATH"
    fi
  fi

  # Set SYSBOX_FIRECRACKER_PATH explicitly — the firecracker provider checks
  # this env var first, bypassing PATH/HOME lookups entirely.
  if [ -z "${SYSBOX_FIRECRACKER_PATH:-}" ] && [ -n "$REAL_HOME" ]; then
    FC_CANDIDATE="$REAL_HOME/.local/bin/firecracker"
    if [ -x "$FC_CANDIDATE" ]; then
      export SYSBOX_FIRECRACKER_PATH="$FC_CANDIDATE"
    fi
  fi

  # Override rootfs path — HCL local.rootfs_path uses env("HOME").
  if [ -z "${SYSBOX_ROOTFS:-}" ] && [ -n "$REAL_HOME" ]; then
    export SYSBOX_ROOTFS="$REAL_HOME/.cache/sysbox/rootfs/ubuntu-24.04.ext4"
  fi
fi

# ── Cleanup ─────────────────────────────────────────────────────────────────

echo "=== full cleanup (TAPs, veths, netns, fc procs, fc dirs, docker) ==="
pkill -9 firecracker 2>/dev/null || true
rm -rf /tmp/fc-images/sysbox-* "$STATE" 2>/dev/null || true

for tap in $(ip -o link show 2>/dev/null | awk -F': ' '/tap-/{print $2}' | awk '{print $1}'); do
  ip link del "$tap" 2>/dev/null || true
done
for veth in $(ip -o link show 2>/dev/null | awk -F': ' '/v[hg]-/{print $2}' | awk '{print $1}' | cut -d@ -f1); do
  ip link del "$veth" 2>/dev/null || true
done
for ns in $(ip netns list 2>/dev/null | awk '/^sysbox-net-/{print $1}'); do
  ip netns delete "$ns" 2>/dev/null || true
done

docker ps -aq --filter "name=sysbox-" 2>/dev/null | xargs -r docker rm -f >/dev/null 2>&1 || true
docker network ls --format '{{.Name}}' 2>/dev/null | grep '^sysbox-' | xargs -r docker network rm >/dev/null 2>&1 || true

# ── Apply ───────────────────────────────────────────────────────────────────

echo "=== apply (streaming; kernel boot lines filtered) ==="
# stdbuf -oL forces line-buffered output from grep so the user sees progress
# as VMs boot, not just a wall of text at the end.
# --state runs/microvm/state.json isolates events to runs/microvm/events/.
./bin/sysbox apply -f "$HCL" --state "$STATE" --auto-approve 2>&1 | \
  stdbuf -oL grep -vE '^\[\s*[0-9.]+\] |^\[\s*OK\s*\] |systemd\[1\]:|^\s+(Mount|Start|Listen|Reach|Wait|Found|Finish|Crea|Set)'

echo
echo "=== state inventory ==="
jq -r '.resources[] | "\(.type) \(.name)"' "$STATE" 2>/dev/null || echo "(state file missing)"

# ── Sensor ──────────────────────────────────────────────────────────────────

echo
echo "=== sensor start (5s probe) ==="
timeout 5 ./bin/sysbox sensor start -f "$HCL" --state "$STATE" 2>&1 | tail -10 || true

# ── Destroy ─────────────────────────────────────────────────────────────────

echo
echo "=== destroy ==="
./bin/sysbox destroy -f "$HCL" --state "$STATE" --auto-approve 2>&1 | tail -20

# ── Audit ───────────────────────────────────────────────────────────────────

echo
echo "=== post-destroy audit ==="
echo "-- TAP devices --"
ip -o link show 2>/dev/null | awk -F': ' '/tap-/{print "  " $2}' | head -5 || echo "  (none)"
[ -z "$(ip -o link show 2>/dev/null | grep tap-)" ] && echo "  (none)"

echo "-- veths --"
ip -o link show 2>/dev/null | awk -F': ' '/v[hg]-/{print "  " $2}' | head -5
[ -z "$(ip -o link show 2>/dev/null | grep -E 'v[hg]-')" ] && echo "  (none)"

echo "-- netns --"
ip netns list 2>/dev/null | grep -E '^sysbox-net-' || echo "  (none)"

echo "-- firecracker procs --"
pgrep -af firecracker || echo "  (none)"

echo "-- FC images --"
ls -d /tmp/fc-images/sysbox-* 2>/dev/null || echo "  (none)"

echo "-- docker resources --"
docker ps -a --filter "name=sysbox-" --format '  {{.Names}} {{.Status}}' 2>/dev/null || true
docker network ls --format '{{.Name}}' 2>/dev/null | grep '^sysbox-' | sed 's/^/  /' || true

echo "-- state resource count --"
jq '.resources | length' "$STATE" 2>/dev/null || echo "(state file missing)"
