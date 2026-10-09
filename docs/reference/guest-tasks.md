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

Remote API guest executions require a separate `sysbox host-supervisor`
process. The Agent submits signed requests over a Unix socket; it cannot
select a provider endpoint through that protocol. The supervisor resolves the
resource from its configured canonical repository and checks the resource UID,
generation and actual provider identity before connecting. It checks current
API authority before starting or renewing a task. Missing configuration fails
closed; there is no direct-provider fallback for this command type.

The supervisor records the task identity before starting it. Recovery observes
that identity instead of replaying the command. Loss of API authority triggers
cancellation; only confirmed guest termination permits a host-signed stop
receipt. The receipt is saved before transmission and retried after restart.
It includes the bounded output, exit-status knowledge and result classification;
neither an exit code alone nor worker cancellation proves remote quiescence.
Output read failure leaves the task pending for retry. Provider truncation flags
are preserved even when the retained output file is below the host limit.
API receipt tombstones make an identical late retry safe after a successor
epoch is issued. Unknown termination continues to block a successor.

Run the supervisor as root with a separate, non-root worker group. Its config,
identity, issuer public key, host signing key and canonical repository must
reside under trusted absolute paths without symlinks or writable ancestors.
Private files and directories must deny access to other users. The socket
directory is root-owned, group-readable/searchable, and not group-writable;
the socket itself allows the configured worker group to connect. Example
supervisor configuration (replace group ID and paths for the deployment):

```yaml
paths:
  runs_dir: /var/lib/sysbox/canonical
agent:
  execution_grant_public_key_file: /etc/sysbox/issuer.pub
host_supervisor:
  socket: /run/sysbox-supervisor/host.sock
  root: /var/lib/sysbox/host-tasks
  signing_key_file: /etc/sysbox/host.key
  worker_group_id: 2000
```

Start with `sysbox host-supervisor --config /etc/sysbox/supervisor.yaml
--identity /etc/sysbox/supervisor-identity.json`. Configure the Agent with
`agent.host_supervisor_socket` pointing at the same socket and its own pinned
issuer public key. Configure the API's
`api.execution_grant.host_public_key_files` map from Agent ID to the matching
host public key file. Keys use raw Ed25519 bytes: 64-byte private keys and
32-byte public keys. The host signing key is separate from the API issuer key
and must never be available to the worker.

File and SQLite repositories are checked for private supervisor ownership,
including SQLite sidecars. PostgreSQL deployments must enforce equivalent
isolation with separate database roles; the supervisor cannot prove role
isolation from a DSN. This boundary currently covers API guest tasks, API file
installation and remote node pause/resume. Ordinary Run lifecycle, provisioner
file copying and foreground provider calls have not all
moved behind it. A privileged ordinary Agent still has those direct provider
paths, so this is not yet whole-Agent credential isolation.

Remote pause/resume uses a fixed `node_power` request. Its digest binds topology,
logical node name and operation; its grant binds the resource identity and host
boot. The supervisor records an intent before the provider call. Completion
requires both an acknowledged synchronous call and an independently observed
matching identity and power state. Health remains a separate observation.
A lost daemon response leaves an unresolved intent even if the node currently
looks paused: that observation alone cannot prove no request remains in flight.
Such intents are neither replayed nor certified as stopped. Completed results
and stop receipts survive supervisor restart; historical reads never reapply
the old operation. The effect ledger is format 2 and rejects older formats.

`make test-host-power-e2e` tests actual Docker pause/resume for both ordinary
and unhealthy containers, lost stop acknowledgments, service reconstruction,
successor epochs and old-proof rejection. Standalone CLI pause/resume still
uses its explicitly local provider path; the remote Agent has no such fallback.

API file installation uses a fixed signed operation, not a root command. The
signature covers installation ID, canonical guest path, mode, size and SHA256.
The host downloads the payload from its configured API and verifies it; worker
URLs and host source paths never become installation inputs. Payloads up to
16 MiB use the same helper over Docker, SSH or vsock.

Administrators must create a dedicated guest data directory and configure a
root-owned `/etc/sysbox/install-roots.json`, for example
`["/var/tmp/sysbox-install"]`. Create that directory with mode 0700; the helper
does not create destination parents or silently expand the policy. Broad roots,
symlinks, writable ancestors and supervisor control paths are rejected. File
installation does not require cgroup delegation; arbitrary tasks still do.

The guest writes into private staging on the destination filesystem, retains an
inode anchor, records commit intent, then renames and fsyncs before recording
completion. After a crash it proves committed or cancelled from those records;
conflicting evidence remains unknown and blocks successor authorization. It
never retries a rename merely because a previous response was lost. Cancellation
and rename share a short commit lock, so cancellation before commit prevents
installation; cancellation after commit reports committed without rollback.

API cancellation returns `cancel_requested: true`. A running operation remains
nonterminal until a signed host result confirms committed or cancelled; a
queued operation without issued authority can be cancelled immediately. Workers
cannot report file completion. Pending payloads survive expiry until terminal
state and grant retirement permit cleanup. API schema 13, file metadata format 2
and guest scope format 2 require explicit rebuilding of old data and updated
helpers; aggregate state and Firecracker manifest formats are unchanged.

Guest execution metadata uses `record_format: 4` for cancellation intent and
its immutable outbox seed. Cancellation keeps queued/running status, blocks new
grants and renewals, and revokes issued grants. SQL commits all three together;
file storage commits intent/outbox together and recovers interrupted authority
publication from that record. This does not establish host stop evidence.
Guest file format 2, lifecycle aggregate 4 and authority format 2 are unchanged;
earlier execution formats and API schemas require explicit rebuilding, with no
compatibility reader. Host task records now use version 2 and reject version 1;
rebuild old task data explicitly. Worker execution completion is removed; only
authenticated host evidence can establish an API execution's terminal result.

Run `make test-host-file-e2e` for the actual API/host/Docker installation test,
including lost stop acknowledgment, cold recovery, checksum/mode and revoked
request rejection. Staging anchors are retained as recovery evidence; automatic
GC remains a separate unfinished stage.

Managed `ssh_access` derives the target home from the canonical node's explicit
connection user. It refuses nodes without an explicit user and writes only
`<home>/.ssh/authorized_keys.d/sysbox-<access-id>`; it never assumes root or
changes a shared `authorized_keys` file. The host supervisor performs the same
operation through a signed request and canonical provider connection.

Verification commands:

```sh
make test-host-supervisor-e2e
CGO_ENABLED=0 go build -buildvcs=false -o /tmp/sysbox-task-helper ./cmd/sysbox-init
SYSBOX_GUESTTASK_HELPER=/tmp/sysbox-task-helper \
  go test -tags e2e ./pkg/transport -run '^TestSSHDurableGuestTaskE2E$' -count=1 -v
```

The SSH test uses a temporary pinned SSH host key and client key, a separate
loopback sshd, and a disposable container with its own cgroup namespace. It
does not modify the host SSH service or user authorized keys. The Firecracker
test is `TestFirecrackerDurableGuestTaskE2E`; it requires actual KVM and current
guest binaries, kernel and rootfs artifacts.

The host supervisor test uses actual Docker tasks and Unix socket requests.
It checks lost stop acknowledgments, API and supervisor object restart with the
same storage, cancellation after Agent session replacement, and successor epochs.
It does not constitute distinct-host acceptance or a complete Agent service
restart test.
