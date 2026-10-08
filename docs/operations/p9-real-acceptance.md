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
- [ ] Configure two API services, stable proxy address, issuer key and host
  public key mapping in control; expose PostgreSQL only on the dedicated LAN.
- [ ] Configure root supervisor with its own host key and canonical Postgres
  credentials, and nonroot Agent with only the public issuer key/socket access.
  The supervisor materializes revisions from canonical Postgres. Agent inventory
  and recovery still need auditing before granting any database access.
- [ ] Run real remote lifecycle and capture terminal API/journal/provider proof.
- [ ] Inject API/Agent/supervisor/guest crashes, partition, delayed responses,
  database restart and test-volume exhaustion. Retain uncertain outcomes.
- [ ] Run capability migration, rebuilding and ownership-verified cleanup gates.

2026-10-08 environment evidence: lab `/tmp/sysbox-p9-two-vms-20261008` passed
the isolation check; both guests boot Ubuntu kernel6.8.0-124 with distinct boot
IDs. PostgreSQL16.15 is active in control, Docker/libvirt active in execution,
and nested QEMU started successfully. This closes environment provisioning,
not API/Agent cross-host lifecycle or full P9 acceptance.
