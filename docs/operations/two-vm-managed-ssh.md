# Two-VM managed SSH acceptance

Run from the refactor worktree with the retained `two_vms.py` lab. The control
and execution VMs must be reachable with pinned management host keys, have
independent kernels and private addresses `10.77.0.10` and `10.77.0.20`. The
prepared lab supplies PostgreSQL 16 on control, Docker on execution, the
`sysboxapi` user and the `sysboxagent` user/group with GID 2000. PostgreSQL must
already listen on the private network; this gate does not change its listeners
or restart the shared database service. Building the Alpine OpenSSH fixture
requires access to its package repositories.

Build current Linux binaries, then run:

```sh
SYSBOX_P9_LAB_ROOT=/tmp/sysbox-p9-lab-current \
SYSBOX_CLI_BINARY=/absolute/path/to/current/sysbox \
SYSBOX_GUESTTASK_HELPER=/absolute/path/to/current/sysbox-init \
make test-p9-ssh-two-vm
```

The gate creates a unique database/role, authority keys, service directories and
systemd units. It appends a database-specific HBA include and reloads PostgreSQL
configuration without disconnecting existing clients. Historical service files,
authority journals, databases and resource evidence remain in place. Private
keys and API credentials are confined to private files; login host keys are
pinned from the identified guest through the execution VM's trusted channel.

The fixture supplies an administrator-enrolled image profile and automatically
installs/recovers the managed sshd drop-in at container start. It exercises:

1. API → Agent → host → guest permission creation and actual cross-VM login.
2. Key rotation: old key authentication rejected, new key accepted.
3. API, Agent and host-supervisor process restart with the same identity/state.
4. Revocation: both managed keys rejected; the administrator key still works.
5. Runtime `sysbox_ssh_access` creation and targeted node reset, then actual login
   on the replacement with the same resource UID and next generation.
6. Destroy, independent absence checks for both container IDs and owner-scoped
   containers/networks, then normal metadata deletion and test-service stop.

Before reset, a host-only HTTP proxy withholds the committed runtime Run's
completion acknowledgment. All retries remain withheld until the host stops.
After API/proxy/host restart, the gate requires the exact original completion
body digest to be replayed and the full SSH resource to remain unchanged.
Agent WebSocket and public API requests continue to use the direct API endpoint.

Connection failure and host-key failure do not count as authentication rejection.
An unavailable Docker daemon does not count as resource absence.

Evidence is written incrementally under `<lab>/sshgate*/result.json`, including
binary/helper/script hashes, guest boot IDs, operation identities, completed
results, login checks and cleanup verification. On failure the gate does not
force-delete resources or fabricate completion; inspect the recorded namespace
and original operations before resuming cleanup. Successful namespaces retain
their database, image, private configuration and evidence for inspection, with
their service units stopped. Keep the lab private.

`make test-p9-ssh-reboot` uses the same variables and additionally reboots both
dedicated VMs. It verifies changed kernel boot IDs, restarts only the test
services and exact owned container, then proves the node UID/generation, keys,
administrator configuration and deployment profile survived. Revocation after
reboot must succeed. Every helper RPC is a fresh process; the gate additionally
restarts the whole container service to exercise the preserved guest journal.

VM restart is deliberate and requires a dedicated lab without unrelated work.
The gate explicitly starts the services/container after reboot; it does not
claim general automatic VM workload recovery. Network partition and other
providers remain separate acceptance gates.
