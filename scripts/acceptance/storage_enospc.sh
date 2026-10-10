#!/usr/bin/env bash
# Run on a disposable execution VM. Does not fill its root filesystem.
set -euo pipefail
target="${1:?usage: storage_enospc.sh /var/tmp/sysbox-enospc-NAME config.hcl}"
config="${2:?configuration required}"
case "$target" in /var/tmp/sysbox-enospc-*) ;; *) echo 'isolated test path required' >&2; exit 1;; esac
name="${target#/var/tmp/}"
[[ "$name" =~ ^sysbox-enospc-[a-zA-Z0-9_-]+$ ]]
test -f "$config"
test ! -e "$target"
test ! -e "$target.log"
mkdir -m 0700 "$target"
mount -t tmpfs -o size=1M,mode=0700 sysbox-enospc "$target"
test "$(findmnt -n -o FSTYPE --target "$target")" = tmpfs
release_filler() {
  # Preserve all journals and the mount even on failure; only release the
  # exact filler this test created so it cannot remain full accidentally.
  if test -f "$target/fill"; then truncate -s 0 "$target/fill"; fi
}
trap release_filler EXIT
if dd if=/dev/zero of="$target/fill" bs=64K status=none; then
  echo 'expected ENOSPC was not reached' >&2
  exit 1
fi
set +e
sysbox --state "$target/state.json" -f "$config" --auto-approve apply > "$target.log" 2>&1
result=$?
set -e
test "$result" -ne 0
grep -q 'no space left on device' "$target.log"
test -z "$(docker ps -aq --filter "label=sysbox.topology=$name")"
release_filler
sync
echo "Initial persistence failed with ENOSPC; no owned container was created."
echo "Space restored. Retained evidence: $target and $target.log"
echo 'Same-state Apply/Destroy and independent absence checks remain required.'
