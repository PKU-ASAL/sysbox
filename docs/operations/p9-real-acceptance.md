# P9 real acceptance

`make test-p9-real` is the explicit real environment gate for the current
distributed runtime slice. It runs the API/Agent completion-loss test, guest
task supervisor recovery, Docker pause/resume fencing, signed file installation,
Docker adoption cold lifecycle, Firecracker/KVM lifecycle and libvirt generation
fencing.

The target intentionally fails when Docker, KVM, the Firecracker artifacts or
libvirt are unavailable. A skipped individual test is not a P9 pass. Record the
commit, kernel/rootfs/firecracker paths, Docker image digests, libvirt URI and
cleanup results with the release evidence.

This matrix proves the currently migrated guest task, file, node power and
adoption boundaries. Ordinary multi-resource Run execution, full sshd service
management, automatic GC deletion, and distinct-host API/Agent acceptance
remain separate gates until their provider and cross-host contracts are wired.

2026-10-08 real run: signed file installation, Docker task supervisor, and
Docker node power scenarios passed in the available Docker/KVM environment.
The matrix still fails at `TestRemoteDockerCompletionLossRecoveryE2E`: the
legacy in-process Agent fixture reaches Docker image entry without a host-issued
guest task grant. This is a genuine P2/P9 failure, not a skipped test.

## Two-VM acceptance environment

The approved P9 layout uses two KVM VMs with independent kernels and writable
disks. The control VM hosts PostgreSQL and the API instances; the execution VM
hosts the unprivileged Agent, root host supervisor and providers. The physical
host launches the VMs and drives tests. It supplies no guest filesystem mounts.
This tests cross-kernel/network operation, not independent physical power loss.

`scripts/acceptance/two_vms.py` requires Python 3.11+, QEMU, qemu-img,
genisoimage, OpenSSH and read/write access to `/dev/kvm`. It verifies the pinned
Ubuntu cloud image checksum before creating any VM. Each VM uses a private
qcow2 overlay, 16 GiB virtual capacity, a dedicated SSH key and a pinned host
key. The control VM uses 2 vCPU/3 GiB, execution 4 vCPU/6 GiB. Allow at least
10 GiB of free disk for initial setup; provider images require additional space.

The VMs communicate over a dedicated QEMU socket Ethernet segment:
`10.77.0.10` (control) and `10.77.0.20` (execution). A separate user-mode NAT
interface supplies package downloads. SSH forwards bind only to host loopback;
ports are recorded in the private manifest. No host bridge, firewall or existing
libvirt network is changed. A bind collision fails startup rather than reusing
an existing listener. Stop any partially started lab using its saved manifest.

```sh
python3 scripts/acceptance/two_vms.py up \
  --root /tmp/sysbox-p9-lab \
  --base /home/jiandong/.cache/sysbox/libvirt/ubuntu-24.04-server-cloudimg-amd64-20260615.img
python3 scripts/acceptance/two_vms.py prepare --root /tmp/sysbox-p9-lab
python3 scripts/acceptance/two_vms.py check --root /tmp/sysbox-p9-lab
```

`check` records distinct host/control/execution boot IDs, bidirectional guest
network connectivity, independent writes to the same guest path, and a real
nested KVM QEMU launch inside execution. It requires QEMU installed by `prepare`;
a missing device or failed launch fails the gate. Evidence is `isolation.json`.
`prepare` installs PostgreSQL16 in control and Docker/libvirt/QEMU in execution.
It does not configure Sysbox services or claim lifecycle acceptance.

```sh
python3 scripts/acceptance/two_vms.py upload --root /tmp/sysbox-p9-lab \
  --role control --source ./bin/sysbox --destination /home/p9/sysbox
python3 scripts/acceptance/two_vms.py exec --root /tmp/sysbox-p9-lab \
  --role execution --command 'sudo virsh list --all'
python3 scripts/acceptance/two_vms.py stop --root /tmp/sysbox-p9-lab
python3 -m unittest discover -s scripts/acceptance -p 'test_two_vms.py' -v
```

Stop verifies the QMP VM name against the lab identity, then quits only those
VMs. It retains disks, keys and logs. Failed setup also retains its evidence;
`up` never overwrites an existing lab. The lab directory contains private keys
and cloud-init seed data and must remain mode0700. Do not publish that directory.

### Deployment and acceptance sequence

- [x] Create two independent VMs and validate nested KVM.
- [x] Install PostgreSQL16 and execution providers; transfer current binary via
  pinned SSH, with no shared guest directories.
- [x] Configure two API services, stable proxy address, issuer key and host
  public key mapping in control; expose PostgreSQL only on the dedicated LAN.
- [x] Configure root supervisor with its own host key and canonical Postgres
  credentials, and nonroot Agent with only the public issuer key/socket access.
  The supervisor materializes revisions from canonical Postgres. Agent inventory
  and recovery still need auditing before granting any database access.
- [x] Run real remote Docker Apply/Destroy and capture terminal API/journal/provider proof.
- [ ] Inject API/Agent/supervisor/guest crashes, partition, delayed responses,
  database restart and test-volume exhaustion. Retain uncertain outcomes.
- [ ] Run capability migration, rebuilding and ownership-verified cleanup gates.

2026-10-08 environment evidence: lab `/tmp/sysbox-p9-two-vms-20261008` passed
the isolation check; both guests boot Ubuntu kernel6.8.0-124 with distinct boot
IDs. PostgreSQL16.15 is active in control, Docker/libvirt active in execution,
and nested QEMU started successfully. This closes environment provisioning,
not API/Agent cross-host lifecycle or full P9 acceptance.

### Deploy and exercise the production service path

`deploy_two_vms.py` requires the host Python `cryptography` package for raw
Ed25519 keys. Run it only against the disposable lab created above. It installs
two systemd API services behind HAProxy, configures the dedicated PostgreSQL
database, registers `execution-p9`, and starts the root supervisor and nonroot
Agent. The worker has neither Docker group membership nor the supervisor's
database credentials or host private key. Registration and deployment reuse
persisted keys/identity on reruns. Configs and secrets remain under the private
lab directory; do not publish them with evidence.

```sh
CGO_ENABLED=0 GOCACHE=/tmp/sysbox-gocache go build -buildvcs=false -o /tmp/sysbox-p9-lab/sysbox ./cmd/sysbox
python3 scripts/acceptance/deploy_two_vms.py --root /tmp/sysbox-p9-lab --binary /tmp/sysbox-p9-lab/sysbox
python3 scripts/acceptance/two_vms.py upload --root /tmp/sysbox-p9-lab --role control \
  --source scripts/acceptance/cross_host_run.py --destination /home/p9/cross_host_run.py
python3 scripts/acceptance/two_vms.py exec --root /tmp/sysbox-p9-lab --role control \
  --command 'sudo python3 /home/p9/cross_host_run.py --evidence /var/tmp/p9-lifecycle.json'
```

The lifecycle probe publishes a revision, uses an idempotency key to request
Apply, records actual canonical resource identities, requests Destroy, and waits
for terminal results. Image `alpine:3.22` must be available to execution Docker.
If public registry access is unavailable, export it with `docker save`, transfer
the archive with pinned SSH, compare archive SHA256 and RootFS layer digests,
then import it in execution. This is explicit artifact provisioning, not a
shared cache or proof that registry downloads survive partitions.

Before finalizing, independently query execution Docker for the exact owner UID
recorded in `state_after_apply`; successful Docker queries must show no remaining
containers. Then run the probe with the same `--evidence` and `--finalize` to
perform metadata DELETE without force. The helper does not claim to perform
that independent provider check itself. Use `--stop-primary` with a fresh evidence
path to stop the test primary API before the lifecycle round; GET and explicitly
keyed mutations retry transient connection failures with the original body/key.
Unkeyed mutations are never automatically replayed. Restore `sysbox-api1` after
the failover round.

2026-10-08 real two-VM results:

- Main API round: `p9-6b16f8158fe0`, Apply/Destroy done, cleanup serial4 and
  `empty=true`; execution independently confirmed no owner-matching containers;
  metadata DELETE succeeded without force. Evidence on control:
  `/var/tmp/p9-lifecycle-04.json`.
- Backup API round: primary deliberately stopped, HAProxy routed to API2;
  `p9-9090be4dc645` Apply/Destroy done, matching empty receipt and independent
  provider absence verified, metadata DELETE succeeded. Evidence:
  `/var/tmp/p9-lifecycle-api2-retry.json`. Primary shutdown took about16ms.
- Earlier failed rounds remain recorded (`p9-lifecycle-01/02/03.json`): missing
  dispatch identity, registry timeout, and host Run completion conflict. The
  third round's resource/journal is retained for uncertain-outcome recovery;
  these rounds are not counted as clean successful acceptance.

The failures led to shared canonical dispatch binding, serial0/topology-scope
Run support, a dedicated host boot query, preservation of the real claimed Run
incarnation, local-only host execution outcomes until signed completion,
cleanup receipt propagation, and context-aware API shutdown. Host bridge
initialization errors now fail instead of silently selecting local storage.

Remaining gates include active-Run API failover, retired-command receipt recovery,
long-running Run renewal beyond the current request budget, definite pre-effect
failure versus uncertain provider results, privileged observations through the
supervisor, guest/File/Reset/Adoption/SSH/GC cross-VM matrix, database/disk faults
and final rebuilding/upgrade exercises. Two successful Docker rounds do not
close these gates or the complete P0–P9 objective.

### Completed-Run receipt recovery after worker retirement

Run completion now signs the exact compacted terminal Run evidence, as well as
the original grant. Generic stop receipts cannot complete or release a Run.
The API verifies the issued grant and reads the original command tombstone and
canonical Run together. A newer command-delivery attempt does not invalidate
the old host evidence, but a different actual Run attempt/owner/incarnation
does. Completion is persisted before authority is released; identical receipt
replay after a successor cannot revoke that successor. Old Run receipts without
an evidence digest are rejected, not silently re-signed.

Real two-VM fault round `p9-272571cff315`: HAProxy returned503 only for host
completion callbacks. Execution completed with a durable, unreported receipt
and evidence digest; the API Run stayed running. Both Agent and supervisor were
stopped, the proxy restriction removed, and both services restarted. Recovery
reported the original evidence for container
`52b0de81b30025004e11028b6ee34ca6f857a1d17ac4f665398bafa6bb539958`;
the external ID remained unchanged. The round then completed Destroy, independent
owner absence verification, and non-force metadata DELETE. The control VM
retains `/var/tmp/p9-receipt-loss-01.json`; normal proxy routing is restored.
This injects callback rejection and process restart, not loss after API commit
or arbitrary in-flight provider cancellation.

File/SQLite/real PostgreSQL regressions additionally cover expired worker lease,
command re-delivery followed by retirement, session replacement, evidence
tampering, different Run incarnation, generic-stop bypass, exact replay after
successor, and rejection after actual Run execution attempt changes.

The remaining early failure records with only `intent` still cannot be converted
to completed receipts from resource existence alone. The renewable Run gate now
has separate durable reservation, exact signed-envelope renewal CAS, and final
commit operations; production Run remains synchronous until the supervisor
active registry, monotonic watchdog, Agent renewal and child-task renewal are
integrated. The thirty-second long-operation failure is not closed by these
foundation APIs.

### Asynchronous parent Run execution

The wire protocol returns `run_observation` with explicit `running`,
`completed`, or `uncertain` status. Short Unix socket request deadlines do not
own provider execution lifetime. The supervisor first reserves the signed Run
in the durable gate, then launches one callback under its service context.
Observe is historical, never a replay instruction. An intent without a live
controller after restart remains uncertain.

The Agent renews the existing API command/grant transaction and forwards the
new signed envelope through `run-renew`. The supervisor persists exact-envelope
CAS before extending its local monotonic deadline. Duplicate/stale renewal and
expired/canceled execution are rejected. An independent online check detects
revocation; losing authority cancels the execution context without claiming
daemon/guest quiescence. `run-cancel` acknowledges cancellation intent only.

New child requests obtain current parent authority from a dynamic accessor;
already-running child tasks still require their own renewal and termination
evidence. Parent renewal alone cannot be counted as child renewal. The old
synchronous Run RPC path has been removed.

The probe's `--provision-seconds 45` option runs a foreground `sleep` provisioner
inside the real Docker node, making Apply exceed the prior30-second RPC limit.
This exercises parent Run renewal; it does not prove renewal or cancellation
of independently supervised guest tasks. All these distinctions must appear
in the final evidence, including any intent left behind by a fault.

2026-10-08 real two-VM long-Run evidence: `/var/tmp/p9-async-45s-03.json`
on control records topology `p9-59a7850534bf`, with a45-second foreground
provisioner. Apply ran from06:13:59.636111585Z to06:14:46.91627025Z (47.28s).
Its original30-second command budget was renewed in the same epoch11; the
durable signed envelope was issued at06:14:39.649680174Z and expired
at06:17:09.643696197Z. Destroy finished, execution independently verified
owner `e7a12f17-ecf7-476f-8925-4afde0a2df43` had no remaining containers, and
metadata DELETE succeeded without force.

The first two long-Run probe rounds omitted the required provisioner `shell`
field and failed validation before resource execution. Their evidence and
uncertain intents remain retained; the test now sets `shell = "none"`.
Host callback failures are logged with Run ID to distinguish validation failure
from watchdog revocation. No failed round is counted as a successful gate.

Race regressions cover duplicate starts, request cancellation independence,
deadline expiry, renewal across the initial budget, duplicate/old renewal,
unresponsive online authority, noncooperative callback cancellation, and cold
intent rejection. Per-identity admission avoids holding a global registry lock
during network/disk I/O. Deadline cancellation uses an independent monotonic
deadline and cannot wait for a renewal's journal mutex. Completed recovery
requires the exact historical signed envelope; unsigned old outcomes are
rejected rather than adopted.

### Parent-bound guest authority

Derived guest grants now carry immutable `parent_epoch`. Exact active derivation
retries return the existing child without extending its deadline. Replaying a
stopped task cannot allocate a new epoch; a distinct task may reuse the resource
only after its predecessor's verified stop. Resource-scoped parent derivation
currently fails closed instead of overwriting the parent's authority slot.

`POST /v1/agents/{agent}/execution-grant/renew-guest` requires signed parent and
child envelopes. It validates the original command under the authority lock and
bounds the child by both the stored parent expiry and the expiry of the supplied
parent envelope. Generic renewal cannot renew a derived child. Parent revocation
or session replacement prevents subsequent child authorization/renewal, and the
guest refuses a renewal that changes its parent epoch.

Before accepting a parent Run completion, the API atomically verifies that all
linked children have stopped and freezes the parent as revoked. It retains the
execution exclusion while committing the canonical result; new derivations
cannot enter that gap. Final stop acceptance then releases the parent. Failed
canonical completion can retry; historical exact receipt replay never modifies
a successor. Rejected derivation/signature checks preserve the observed clock
floor to prevent wall-clock rollback reviving authority.

Repository and API regressions cover retries, parent bounds, scope collisions,
revocation/session replacement, child-before-parent stop ordering, concurrent
derivation versus completion preparation, and clock rollback. Guest renewal
regression covers parent changes without touching the running task record.
These changes are not yet evidence of periodic child renewal through a live
supervisor or cross-VM child lifecycle acceptance. The guest helper must be
rebuilt for the new field; old overlapping parent/child authority records without
parent binding are rejected rather than inferred or silently migrated.
