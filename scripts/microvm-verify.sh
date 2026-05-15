#!/usr/bin/env bash
# Thorough verification: full clean → apply → sensor probe → destroy → audit.
# Requires sudo; intended for interactive use.

set -euo pipefail

cd "$(dirname "$0")/.."

HCL=examples/microvm/field.sysbox.hcl
STATE=runs/microvm/state.json

# When the script runs under sudo, root's PATH lacks the user's
# personal bin dirs (e.g. ~/.local/bin where firecracker lives).
if [ -n "${SUDO_USER:-}" ] && [ -z "${SYSBOX_PRESERVE_PATH:-}" ]; then
  export SYSBOX_PRESERVE_PATH=1
  USER_PATH=$(sudo -u "$SUDO_USER" env | grep '^PATH=' | head -1 | cut -d= -f2-)
  if [ -n "$USER_PATH" ]; then
    export PATH="$USER_PATH:$PATH"
  fi
fi

# When the script runs under sudo, $HOME=/root and the HCL
# local.rootfs_path resolves incorrectly. Export SYSBOX_ROOTFS.
if [ -z "${SYSBOX_ROOTFS:-}" ]; then
  REAL_HOME="${SUDO_USER_HOME:-}"
  if [ -z "$REAL_HOME" ] && [ -n "${SUDO_USER:-}" ]; then
    REAL_HOME=$(getent passwd "$SUDO_USER" 2>/dev/null | cut -d: -f6)
  fi
  if [ -n "$REAL_HOME" ]; then
    export SYSBOX_ROOTFS="$REAL_HOME/.cache/sysbox/rootfs/ubuntu-24.04.ext4"
  fi
fi

echo "=== full cleanup (TAPs, veths, netns, fc procs, fc dirs, docker) ==="
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

echo "=== apply (streaming; kernel boot lines filtered) ==="
# stdbuf -oL forces line-buffered output from grep so the user sees progress
# as VMs boot, not just a wall of text at the end.
# --state runs/microvm/state.json isolates events to runs/microvm/events/.
sudo -E ./bin/sysbox apply -f "$HCL" --state "$STATE" --auto-approve 2>&1 | \
  stdbuf -oL grep -vE '^\[\s*[0-9.]+\] |^\[\s*OK\s*\] |systemd\[1\]:|^\s+(Mount|Start|Listen|Reach|Wait|Found|Finish|Crea|Set)'

echo
echo "=== state inventory ==="
jq -r '.resources[] | "\(.type) \(.name)"' "$STATE" 2>/dev/null || echo "(state file missing)"

echo
echo "=== sensor start (5s probe) ==="
sudo -E timeout 5 ./bin/sysbox sensor start -f "$HCL" --state "$STATE" 2>&1 | tail -10 || true

echo
echo "=== destroy ==="
sudo -E ./bin/sysbox destroy -f "$HCL" --state "$STATE" --auto-approve 2>&1 | tail -20

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
