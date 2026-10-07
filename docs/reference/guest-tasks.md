# Durable guest tasks

SSH and vsock use the same versioned guest task helper. A task is identified by
its caller-assigned ID, request digest, guest boot ID, resource identity,
generation and grant epoch. Retrying the same request returns the existing
record; a conflicting digest or epoch is rejected. PID is diagnostic data.

The control plane signs grants with Ed25519. The guest verifies the pinned raw
32-byte public key at `/etc/sysbox/grant-public-key`. SSH invokes the installed
`/usr/local/bin/sysbox-guest-task --guest-task`; Firecracker uses `task.v1` on
vsock. Both binaries must be built from the current source. There is no `nohup`
or unsigned fallback.

Arbitrary persistent commands run as reserved UID/GID 65534 by default, with
supplementary groups cleared, effective capabilities dropped and Linux
`no_new_privs` set. UID 0 requests are rejected. The UID is part of the signed
request digest. This differs from foreground administrative commands; privileged
resource configuration requires a separately authorized fixed operation.
Tasks sharing UID 65534 are not isolated from each other for availability.

The helper requires a writable, administrator-delegated cgroup v2 root at
`/sys/fs/cgroup/sysbox-tasks`. Do not expose the host cgroup root read-write to
an untrusted container. Private task state lives in
`/var/lib/sysbox/guest-tasks`, inaccessible to the task UID. Request argv,
environment and stdin are passed to the supervisor in a sealed memory file,
not stored in task records. Task output is intentionally persisted and may
contain data emitted by the program; callers must treat it as sensitive.

Each task has a separate supervisor and an independent pidfd-based guard.
The guard stops the owned cgroup if its supervisor dies or the grant expires.
On kernels without `cgroup.kill`, the helper freezes the cgroup subtree and
uses pidfds after rechecking frozen membership. It never kills by an unchecked
numeric PID. Grant deadlines are captured in `CLOCK_BOOTTIME`, bound to the
guest boot ID; wall-clock rollback cannot extend them. Renewal requires a new
valid signature for the same task, scope, session and epoch.

Cancel records intent and initiates termination. An accepted cancel may return
`unknown`; callers must observe until termination is `confirmed`. A lost
supervisor result or guest reboot never invents an exit code. Output reads use
bounded byte cursors, and truncation is reported explicitly.

Verification commands:

```sh
CGO_ENABLED=0 go build -buildvcs=false -o /tmp/sysbox-task-helper ./cmd/sysbox-init
SYSBOX_GUESTTASK_HELPER=/tmp/sysbox-task-helper \
  go test -tags e2e ./pkg/transport -run '^TestSSHDurableGuestTaskE2E$' -count=1 -v
```

The SSH test uses a temporary pinned SSH host key and client key, a separate
loopback sshd, and a disposable container with its own cgroup namespace. It
does not modify the host SSH service or user authorized keys. The Firecracker
test is `TestFirecrackerDurableGuestTaskE2E`; it requires actual KVM and current
guest binaries, kernel and rootfs artifacts.
