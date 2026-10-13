#!/bin/sh
# Administrator-owned image entrypoint; no runtime package installation or RPC
# supplied executable/configuration paths. Fail the container if enrollment fails.
set -eu
umask 077
ssh-keygen -A
/usr/sbin/sshd -t -f /etc/ssh/sshd_config
# This entrypoint owns the container's sshd. A previous container boot may leave
# its PID file, and PID numbers can repeat before the new daemon publishes it.
if [ -e /run/sshd.pid ] || [ -L /run/sshd.pid ]; then
    test ! -L /run/sshd.pid
    test -f /run/sshd.pid
    rm -- /run/sshd.pid
fi
/usr/sbin/sshd -D -e -f /etc/ssh/sshd_config &
sshd_pid=$!
trap 'kill "$sshd_pid" 2>/dev/null || true; wait "$sshd_pid" || true' EXIT
trap 'exit 143' TERM INT
attempt=0
while [ "$attempt" -lt 100 ]; do
    if [ -r /run/sshd.pid ] && [ "$(cat /run/sshd.pid)" = "$sshd_pid" ]; then
        break
    fi
    kill -0 "$sshd_pid"
    attempt=$((attempt + 1))
    sleep 0.1
done
test "$(cat /run/sshd.pid)" = "$sshd_pid"
/usr/local/bin/sysbox-guest-task --ssh-service-install /etc/sysbox/sshd-service.json
set +e
wait "$sshd_pid"
result=$?
# wait reaped the child; never signal its numeric PID after that boundary.
trap - EXIT
exit "$result"
