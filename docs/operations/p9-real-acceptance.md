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

This matrix proves its listed Run, guest task, file, node power, adoption and
provider lifecycle boundaries. It does not cover the full P0–P9 scope: full sshd
management, automatic GC, snapshot/restore maintenance, remaining distributed
faults and upgrade acceptance still require their own gates. The two-VM evidence
below is separate from these same-host tests.

2026-10-08 real run: signed file installation, Docker task supervisor, and
Docker node power scenarios passed in the available Docker/KVM environment.
At that checkpoint the matrix failed at `TestRemoteDockerCompletionLossRecoveryE2E`: the
legacy in-process Agent fixture reaches Docker image entry without a host-issued
guest task grant. This is a genuine P2/P9 failure, not a skipped test.

2026-10-09: that obsolete fixture was migrated to host-supervisor admission and
signed host completion; the complete `make test-p9-real` target passed. See the
final section for the exact coverage and remaining boundaries.

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

### Host Run child management

Run task launches now enter a trusted `TaskStartHandler` before guest
transmission. The handler obtains canonical Run/task identity, derives the
child grant, and calls the same durable task Dispatcher used by API tasks.
Explicit signed Dispatcher-to-guest calls do not recurse through the handler.
The child belongs to the parent Run context, not a temporary transport request.

`RunChildren` observes each admitted child and renews it using the current
parent envelope. A finite child must have a known successful exit and a reported
confirmed-stop receipt before the parent callback can finish. Nonzero/unknown
exit fails the parent. On cancellation or sibling failure, starts and watchers
are canceled and stop reconciliation gets a bounded independent context;
unresponsive providers remain uncertain and their durable records are retained.
Long-running services are not silently killed to manufacture a successful Run;
they continue to hold the parent open. A separate persistent-service ownership
model is still required where services must outlive Apply.

Creating nodes are not always in final state. The privileged resolver may use
the same Run's persisted create/reset candidate, recheck full ownership and
generation, and verify actual provider observation before making a connection.
Task records persist trusted Run provenance so cold Observe/Cancel/Renew can
resolve the same candidate. Wrong Run, ambiguous identity, stale generation,
unsigned/missing provenance and conflicting current resources fail closed.

Race tests cover duplicate starts, slow admission versus sibling cancellation,
noncooperative observation, parent cancellation, exact current-parent renewal,
failed exits, lost-start replies, and candidate-backed cold reconciliation.
The Docker fake transport test was fixed to consume task stdin before closing
the upgraded connection; previously unread input intermittently reset TCP.
This slice has not yet been validated with a real cross-VM background child.
It does not close unknown Run-intent recovery, persistent service transfer,
Reset child lifecycle, or the remaining P5–P9 capability/fault gates.

### Real cross-VM background child acceptance

Prepare `sysbox-p9-guest:local` on the execution VM using
`scripts/acceptance/guest.Dockerfile`, the current static `cmd/sysbox-init`
binary named `sysbox-guest-task`, and the current issuer **public** key named
`issuer.pub`. Transfer these via pinned SSH, never share the private key. The
probe option `--background --provision-seconds 45` uses this image with a private
cgroup namespace and privileges needed for the disposable guest supervisor.
The VM remains the outer isolation boundary. This is a finite task, not a
service intended to outlive Apply.

Real execution exposed and fixed two integration bugs:

- Create now persists an incomplete action candidate before guest effects,
  including ownership, generation, codec state and dependencies. It does not
  publish the resource or claim composite completion. A failed checkpoint
  prevents routes/provisioners/image entry from running.
- Candidate resolution loads state and action journal in one repository
  transaction. Separate reads could otherwise combine pre-publication state
  with a succeeded action and spuriously cancel a valid child. Reads retain
  the aggregate serial and return detached copies.

The fourth round `p9-5ecab24983f4` passed: parent epoch19, child epoch20;
Apply started09:36:51.323286438Z. Child renewal issued09:37:02.999993858Z with
expiry09:39:31.069891814Z; child stopped09:37:38.704818791Z with known exit0,
confirmed termination and acknowledged host receipt. Parent ended afterwards,
at09:37:38.756211213Z (47.43s). Destroy, independent execution-side owner absence
and non-force metadata DELETE passed. Control evidence:
`/var/tmp/p9-child-45s-04.json`.

The previous successful lifecycle round03 revealed an inaccurate parent end
timestamp taken before waiting for children; `RunChildren.Finish` now timestamps
completion only after verified child termination. Rounds01/02 remain failed
evidence with retained resources/journals; they are not counted as recovered.
This successful gate does not close child cancellation under partition, cold
recovery of unknown intents, persistent services, or the full fault matrix.

### Running-child API outage gate

`scripts/acceptance/child_partition.py --root LAB --topology p9-IDENTITY`
requires exactly one durably observed running child before injecting a fault.
It stops only the control VM's test HAProxy, waits for a confirmed host child
stop, restores HAProxy in a `finally` block, and verifies the identical stop
receipt is acknowledged. It fails if evidence already exists, the child is not
running, the stop is unconfirmed, or receipt identity changes. This isolates
API unavailability; PostgreSQL and SSH stay reachable. It is not a complete
VM network partition or an API-commit response-loss test.

Real round `p9-d271491329c1`: child stopped in1.54 seconds after proxy outage;
the exact persisted receipt was acknowledged after reconnect. Independent
execution-VM inspection found no `sleep 120` process and both task cgroup event
files reported `populated 0`. Exit status stayed unknown with termination
confirmed, rather than inventing exit0. Proxy service was restored and active.
Host evidence is `/tmp/sysbox-p9-two-vms-20261008/p9-d271491329c1-partition.json`.

The lifecycle probe correctly did **not** pass: the parent stayed running at
its bounded observation timeout, with the host Run intent still uncertain.
Its container and canonical records remain retained for evidence-driven
recovery; no successful Destroy/metadata-delete is claimed for this round.
This closes only the running child's API-outage stop/reconnect proof. Parent
unknown-intent recovery and complete fault cleanup remain required.

### Confirmed failure convergence and normal cleanup

The trusted Run callback now has a narrow failure outcome: resource execution
must already have returned success, child admission must be closed with no
in-flight or handleless start, and every admitted child must have a validated,
acknowledged stop receipt bound to its original Run/grant identity. Only then
may failed child execution produce a failed terminal Run. Unknown exit codes
remain failure, never success. Arbitrary provider errors cannot construct this
internal outcome.

After that callback returns, the coordinator stores the failed result against
the exact historical signed intent, even if authority expired. This transaction
authorizes no new effect. It retries transient persistence failure while the
supervisor service remains alive, and logs failed attempts. A crash before the
proof is persisted still leaves an uncertain intent; old callback exit cannot
be invented after restart. Existing signed-result reporting and API parent-stop
preparation enforce authoritative child inventory before releasing exclusion.

Real two-VM round `p9-c466f4bc8320` passed this fault/cleanup chain: test API
proxy outage, child stop confirmed in1.73s, identical receipt acknowledged after
reconnect, parent failed with `run child execution failed after confirmed
termination`, ordinary Destroy done, independent Docker owner absence, then
metadata DELETE without force. Owner was
`8bd178f3-1768-49db-9cd0-467344b09a74`. Control evidence:
`/var/tmp/p9-child-partition-recovery-01.json` and
`/var/tmp/p9-failed-cleanup-01.json`. Proxy routing was restored.

`recover_failed_cleanup.py` requests normal Destroy only for this explicitly
confirmed failure, retaining a separate result file. After independently
checking provider absence, `--finalize` performs non-force metadata deletion.
It cannot recover a running/unknown intent. The earlier `p9-d271491329c1`
record and other historical uncertain runs remain retained: their old binaries
did not persist sufficient callback-exit evidence. This is not a claim that all
legacy unknown outcomes or daemon crash windows have been solved.

### Quiescent result persistence retry

`CommitQuiescentRun` now accepts an exact historical replay of an already
completed result. This covers a lost commit acknowledgement, including a
successful file rename followed by a directory-sync error: replay retries the
directory durability barrier without rewriting the journal. It requires the
exact signed envelope and the same result under the journal's JSON encoding;
changed evidence or another renewal envelope remains fenced. A stopped epoch
or a newer admitted epoch does not prevent this read-only confirmation.

Regression tests cover reopening the gate, sealed and acknowledged receipts,
successor admission, unchanged journal bytes, rejected result/envelope changes,
and JSON whitespace/HTML escaping. Effectgate and hostsupervisor race tests
passed, with local socket permission required for the supervisor tests; vet
passed for both packages. These are local protocol regressions, not a new
two-VM fault acceptance result or recovery of intent-only outcomes.

### Resource checkpoint recovery after supervisor SIGKILL

The trusted Run callback now seals child admission and records a resource-done
checkpoint in the existing effect ledger before waiting for finite children.
The checkpoint includes immutable Run request identity and every admitted child
handle/grant, without transient Inputs or a reusable cleanup receipt. An OS
execution lock spans Run admission through callback exit and result persistence.
Recovery must acquire that same lock, validate the historical checkpoint and
each child's current journal identity, and obtain acknowledged signed stop
evidence before committing a failed Run. It never repeats provider effects or
claims success from unknown child exit status. Children have independent recovery
budgets; a slow or unavailable receipt cannot prevent cancellation of siblings.

2026-10-09 two-VM round `p9-d092f4373057` passed: a120-second background task
was observed running after its parent resource checkpoint; the test sent SIGKILL
to the execution VM's dedicated supervisor. The service restarted from PID85364
to PID88601. In4.95seconds the new process reported the original Run as failed
with `run controller stopped after resource execution; child termination confirmed`,
with every checkpoint child stopped and acknowledged. Ordinary Destroy then
completed, an independent Docker query found no container for owner
`3e586051-b489-4ab8-a52e-8955a10fbf0d`, and non-force metadata DELETE succeeded.
Agent and supervisor services remained active. Earlier unknown test resources
were not deleted or reclassified.

Evidence: host
`/tmp/sysbox-p9-two-vms-20261008/p9-d092f4373057-supervisor-crash.json`;
control VM `/var/tmp/p9-checkpoint-crash-01.json` and
`/var/tmp/p9-checkpoint-cleanup-01.json`. Execution binary SHA256:
`a73d3121ac96acc85c41c17d5ef09516accf446b03f7877f9969ecf3e73eb321`
(built from this checkpoint implementation with `-buildvcs=false`).

Reproduce after starting a new120-second background probe with `cross_host_run.py`:

```sh
python3 scripts/acceptance/run_checkpoint_crash.py \
  --root /tmp/sysbox-p9-two-vms-20261008 --topology <new-p9-topology>
```

The injector refuses absent checkpoints, non-running children and overwritten
evidence. Use `recover_failed_cleanup.py` for normal Destroy, independently
verify provider absence, then use its `--finalize` for metadata deletion.
The general lifecycle probe intentionally exits with an error upon the injected
failed Apply; the fault injector and cleanup evidence are the acceptance gates.

Full Go regression, effectgate/hostsupervisor race, local subprocess SIGKILL,
commands regression, vet and independent review passed. This closes the
resource-complete/child-wait crash window. A daemon call interrupted before the
resource checkpoint, legacy intent-only records, persistent-service transfer,
P5–P8 capability gaps and the remaining P9 faults are still open.

### PostgreSQL outage during a running child

`scripts/acceptance/database_outage.py --root LAB --topology p9-IDENTITY`
requires a running child, matching resource checkpoint, provider ownership,
and a populated guest cgroup before stopping only the control VM's
`postgresql@16-main`. It rejects pre-existing cancellation. During the outage
it independently reads the guest journal and the original cgroup inode, pins
task/supervisor PID and start ticks, and requires cancelled status, confirmed
termination and `populated 0`. A natural successful exit cannot pass this gate.
PostgreSQL is restarted and checked with `pg_isready` in `finally`, including
when observation fails. The original child must then report its stop receipt.

Use a600-second background fixture, longer than the protocol's five-minute
maximum signed authorization lifetime. The stop observation budget is315seconds
(five minutes plus15seconds of observation slack). With the database unavailable,
the host cannot necessarily resolve canonical resources to request cancellation;
the guest's independent lease guard remains the stop boundary. This gate proves
cancellation during the outage, not which of the guest/host mechanisms caused it.
It does not promise a seconds-level stop bound during database failure.

2026-10-09 round `p9-886e91a3eee3` passed using the execution binary from the
checkpoint recovery round (`f60f261` implementation). PostgreSQL PID changed
from68995 to73273. With PostgreSQL still stopped, the guest reached cancelled
and its original cgroup showed `populated 0` after138.85seconds. Exit status
remained unknown; no success code was inferred. Child epoch34's stop was then
acknowledged after database restart. The original parent Run converged to failed
with `run controller stopped after resource execution; child termination confirmed`.
Normal Destroy completed, independent Docker inspection found no container for
owner `9f6c7f46-eb95-4d68-b159-1efa37c1d5f4`, and non-force metadata DELETE passed.
Both APIs, PostgreSQL, HAProxy, Agent and supervisor remained active afterward.

Evidence: host
`/tmp/sysbox-p9-two-vms-20261008/p9-886e91a3eee3-database-outage.json`;
control `/var/tmp/p9-database-outage-02.json` and
`/var/tmp/p9-database-cleanup-02.json`. The lifecycle polling probe itself exits
on the injected HTTP503; its original Run is subsequently checked by the cleanup
probe. No replacement Apply is submitted. The fault and cleanup evidence, taken
together, are the acceptance result.

The initial round `p9-44d7974cbe21` did not pass the outage stop gate: its
arbitrary45-second observation deadline was shorter than the valid signed grant.
PostgreSQL was restored; the parent then failed and ordinary Destroy, independent
absence and non-force metadata cleanup succeeded. Its failure evidence remains
at `p9-44d7974cbe21-database-outage.json` and the control round01 files. It is not
counted as a stop-during-outage pass. Harness regression tests now also reject
natural completion, pre-existing cancellation and replacement cgroup identity.

This closes one checkpointed Docker Run's database-outage/restart/cleanup gate.
It does not cover database failure during a provider daemon effect, restore/GC
transactions, a full network partition, disk-full or the remaining provider matrix.

### Real provider target after host Run fixture migration

The shared `agentexec.HostRunExecutor` now contains the trusted callback used by
both the production `host-supervisor` command and the Docker completion-loss
fixture. Canonical claim/serial/resource checks, immutable revision materialization,
child grant derivation and renewal, resource checkpoint and final child evidence
remain in the same order. The command only wires dependencies; the fixture does
not implement a second execution policy.

The old test was reproduced failing because its direct Agent execution tried
to start an image entry without a signed guest grant. It now requires the host
supervisor socket, shares canonical SQLite, uses separate API/host revision
directories, and explicitly launches the container's `sleep infinity` entrypoint.
Apply and Destroy completion responses are withheld after the API commits the
host-signed result. Rebuilding the API/Agent/supervisor verifies replay of the
exact original receipt and evidence; repeated admission retains one container
with the same external ID. Stale claims, changed result evidence, resource
UID/generation observation, Destroy and non-force metadata deletion are checked.
The fixture's Agent retains canonical database access for its read-only remote
inventory check; this does not establish least-privilege observation routing.

2026-10-09 `make test-p9-real` passed every named gate without skips:

- API package with race:10MiB file installation, running/unhealthy node power,
  durable guest task stop/successor, and host Run completion-loss recovery.
- Docker adoption of an initially unlabelled container, including cold provider
  reconstruction, pause/resume, stale proof refusal and destruction.
- Firecracker controller SIGKILL recovery7.85s, generation lifecycle8.08s,
  reset/cold adoption25.65s (all old-generation effects rejected), and independent
  process recovery11.27s.
- Libvirt real generation effects4.10s, including stale context/handle rejection.

The final phase-bound fault injector also passed its focused real Docker race
run in14.06s. The in-process components are reconstructed, not separate OS
processes; use the earlier two-VM results for process/network claims. An async
Run dispatch unit test now waits for its callback owner before TempDir removal;
the cleanup race regression passed50 repetitions under race detection.

Artifacts were rebuilt from current source for both helper architectures:
amd64 SHA256 `ced431e5d3432322c3ab14dfa762dd945a719100374d5261bb178a37b90f1da9`;
arm64 SHA256 `f213a0746de7e5140145b17db69198e5546b95ec3504a67309ada55492294b81`.
Firecracker inputs were `/home/jiandong/.cache/sysbox/tools/firecracker`,
`/home/jiandong/.cache/sysbox/artifacts/local/vmlinux-5.10.245` and
`/home/jiandong/.cache/sysbox/rootfs/ubuntu-24.04.ext4`. Test-owned resources were
destroyed by their identity-scoped cleanup. Passing this target does not close
the remaining P5–P8 capabilities, pre-checkpoint daemon ambiguity, persistent
services, full partition/disk-full or upgrade/main integration.

### Remote observation no longer reads worker canonical storage

Run recovery commands now relay only command ID, lease owner and attempt over
the host socket. The host independently fetches canonical authorization and
revision from the API, enforces the returned command lease, executes the existing
Observe/Recover engine, and reauthorizes before returning its report. Missing
supervisor configuration fails without using the Agent executor as a fallback.
The worker's local revision/repository capability no longer gates advertisement
of `run-recovery.v1`; configured host routing does.

The real Docker completion-loss race test passed in13.43seconds with the Agent
using an independent empty SQLite database while API and host share canonical
storage. After reconstruction, its remote observation returned the exact live
container UID/generation/external ID; normal Destroy and non-force metadata
deletion passed. This supersedes the previous fixture's worker database access
limitation for explicit Run observation. It does not prove all periodic inventory
or console/provider routes have moved, nor constitute new two-VM acceptance.

A host-backed recover-mode socket test also creates an incomplete journal by
rejecting the original state patch, then commits the proven outcome using the
API-authorized immutable revision. It verifies advanced canonical serial,
succeeded action, one original Create and no repeated Create/Delete. Additional
tests cover absent supervisor, post-observation authorization rejection, unsafe
command identity, configured leases longer than30seconds, and a valid recovery
authorization response above1MiB. The latter two exposed transport truncation
regressions and now pass with canonical lease limits and a recovery-only16MiB
response budget. Full Go regression and affected-package race passed.

At the routing checkpoint, final reauthorization could reject a report after a
previously authorized journal write. The following change closes that specific
command/Agent/Run authorization race for recovery outcome commits; daemon-unknown
recovery, P5–P8 and the remaining P9 fault matrix remain separate requirements.

### Atomic authorization of recovery metadata outcomes

Host recover mode now requires a command-bound recovery repository. The ordinary
repository remains available for observation; no ordinary write fallback is used
for recover. Each outcome commit validates the current command lease, owner,
attempt, retirement and recover mode; the Agent's enabled state and capability;
the original failed/recoverable Run incarnation; and absence of an active peer
Run in the same topology. Action identity must match that Run. Recovery cannot
create a new action intent.

The file backend holds the API run-record lock through the aggregate commit and
evaluates expiry after acquiring the aggregate lock. SQLite uses one
`BEGIN IMMEDIATE` transaction. PostgreSQL uses the existing Run admission
advisory lock, then locks Run, command, Agent and aggregate rows until commit.
All paths reuse one shared action outcome/CAS transition; SQL adapters do not
reimplement serialization, identity or state-transition policy. Recovery expects
the host to access the same canonical authority and lifecycle storage.

2026-10-09 tests ran against files, SQLite and a dedicated PostgreSQL16 instance
on loopback port32937 with tmpfs data (not the two-VM or user database). Under
race detection, all backends accepted the live command and rejected a retired
command, wrong owner/attempt, blocked Agent, active successor Run, mismatched Run
incarnation and expired lease without changing state. File and PostgreSQL tests
also held the aggregate lock, proved the recovery process had acquired the
authority lock, and verified concurrent command retirement could not pass it.
A lease expiring while waiting for the aggregate lock caused commit rejection.
PostgreSQL coverage executed without skips. Full Go regression with that database
enabled, persistence/agentexec race, vet and independent review passed.

This proves authorization serialization for one existing recovery outcome. It
does not freeze an entire topology across all maintenance operations, transfer
provider execution ownership, prove an interrupted daemon call stopped, or replace
the remaining restore/adoption/GC/SSH and distributed fault acceptance gates.

### Snapshot restoration preconditions

State maintenance now requires all three activity loaders (Run, node operation
and guest activity). Previously force-unlock rejected an incomplete registry,
but restore could silently omit the missing activity class. Both now fail closed.
The shared snapshot restore transition also rejects a changed state lineage,
resource type or resource schema version, in addition to resource ownership,
generation, external identity and attributes. Restoring older metadata must not
reinterpret current provider-private data under a historical schema.

Regression tests reproduced all of these missing checks before the fix. File
and SQLite tests verify that rejected lineage/type/schema restoration changes
neither resources nor serial; API tests cover each missing activity loader.
These checks are preconditions, not a complete P6 freeze/revoke/restore protocol:
concurrent admission across independent controllers and authorized reconciliation
of a different resource inventory still require implementation and acceptance.

### Maintenance authority barrier foundation (format change)

Execution authority format2 adds durable maintenance request ID, topology UID,
request digest and exclusion epoch. Beginning maintenance atomically revokes all
active parent/child grants and prevents new grants for that topology. Ending it
requires the exact barrier identity and confirmed stopped grants, including grants
whose lease already expired. Released records remain as tombstones; historical
retries cannot unfreeze a successor. Other topologies retain their authorization.

Tests cover cold repository reconstruction, retry/conflict behavior, parent/child
revocation and stop ordering, forged release fields, independent topology, and
concurrent maintenance/grant admission through file and SQLite API storage.
PostgreSQL is represented by the same adapter contract test but was not configured
in this round; no new real PostgreSQL or two-VM acceptance is claimed.

Format1 now fails rather than silently losing barriers on an old writer's next
save. This change has not been deployed into the existing two-VM lab: its retained
format1 authority and uncertain resources were not altered. Rebuild must stop old
writers and preserve/resolve resource evidence; editing the version field is not
a migration. Restore HTTP orchestration, metadata freeze and a full upgrade
exercise remain required before the barrier can close the P6 user workflow.

### Recovery writes respect maintenance exclusion

Explicit recovery outcome commits now check the maintenance ledger under the
same lock/transaction as the journal/state write. File storage holds the shared
API record lock; SQLite reads authority inside its immediate transaction;
PostgreSQL locks the authority row before source records and aggregate. PostgreSQL
initializes a missing ledger before locking it, so a missing-row read cannot race
the first maintenance request. Existing empty/corrupt ledgers are rejected rather
than treated as absence.

The new regression first reproduced a state write through an active maintenance
barrier. File, SQLite and real PostgreSQL16 now reject it without modifying state.
File/PostgreSQL lock-contention tests also prove a new freeze cannot pass an
in-flight recovery commit. The maintenance-versus-grant admission race was rerun
on all three backends, including PostgreSQL without skips, using a dedicated
tmpfs database on loopback32938. Full Go regression with this database enabled,
focused API race, persistence race and vet passed. The isolated database is
removed after verification; the existing two-VM format1 authority is untouched.

This adds explicit recovery metadata exclusion to the barrier foundation; it
does not yet freeze every metadata writer or wire the complete HTTP restore
admission/commit/release workflow.

The first full PostgreSQL-enabled regression in this round exposed a catalog
creation race: two API instances concurrently opening the lifecycle repository
could collide on `pg_type_typname_nsp_index` despite `CREATE TABLE IF NOT EXISTS`.
Lifecycle schema bootstrap now uses a schema-local PostgreSQL advisory lock and
DDL in one transaction. The existing dual-API keyed Apply regression passed ten
consecutive isolated-schema runs after the fix; this is an initialization fix,
not a relaxation of idempotent admission or a retry that hides the SQL error.

### Idempotent snapshot restore commit foundation

The lifecycle aggregate now stores immutable restore receipts (format3).
`PlanSnapshotRestore` binds an operation ID, topology UID, snapshot ID/digest,
expected serial and maintenance epoch. `CommitSnapshotRestore` reuses the normal
restore validation and commits restored metadata plus receipt atomically. A
duplicate returns the original receipt even after later metadata updates or
snapshot retention deletion; conflicting request identity is rejected. Snapshot
payloads omit the receipt catalog to avoid recursive historical capture.

File and SQLite cold-reopen tests verify unchanged successor lineage/serial,
exact receipt replay after source snapshot deletion, and rejection of changed
snapshot, digest, serial, maintenance epoch or owner. Concurrent SQLite identical
requests produce one serial increment and one receipt; a distinct stale request
fails CAS. These are repository contract tests, not new PostgreSQL or VM restore
acceptance. The primitive itself does not authorize maintenance: its caller must
retain the matching barrier through commit. HTTP restore orchestration and its
authority transaction are still required.

Format2 aggregates are explicitly rejected. No old lab aggregate was rewritten
or version-edited; the upgrade/rebuild exercise remains open alongside format2
authority deployment. Full restoration of a different resource inventory and
provider snapshot semantics are not established by this metadata receipt.

Restore planning now executes the same feasibility validation as commit before
maintenance needs to revoke grants: lineage, schema, inventory and unfinished
journal conflicts fail without changing state or serial. Epoch-zero drafts
support this ordering; a domain-separated digest binds immutable request fields
while excluding the subsequently allocated maintenance epoch. Zero-epoch commit
is rejected. Historical draft retries recover the original request/epoch even
after snapshot deletion and successor advancement. Persistence race and dependent
API/Agent tests cover this foundation; public restore orchestration remains open.

The next shared restore transition binds that draft to a persisted maintenance
barrier: ID, topology UID, digest and allocated epoch must match. A new restore
requires a frozen barrier and confirmed stopped grants with nonempty receipts;
revocation or expiry alone cannot pass. Released barriers only permit reading an
exact already-committed restore receipt. Tests cover rejection before host stop,
successful transition after stop, request tampering, missing stop receipt and
historical replay after release. The transition copies historical output bytes
and reuses the same restore state/CAS logic as the repository primitive.

`ApplyAuthorizedSnapshotRestore` is pure logic for a storage transaction that
holds both authority and aggregate locks until commit. It is not an RPC or an
authorization token. Its joint-lock adapters and public restore orchestration
are not wired yet; these tests establish the transition contract, not a complete
user restore workflow or a new distributed acceptance result.

### Restore storage transaction adapters

Restore commit now has file, SQLite and PostgreSQL adapters retaining authority
and aggregate exclusion through commit. File storage takes the API record lock,
the actual authority-file lock (also used by direct repository writers), and the
aggregate lock; it leaves the authority bytes unchanged. SQLite uses one immediate
transaction. PostgreSQL takes the Run admission advisory lock, authority row lock
and aggregate row lock. Each adapter reuses the shared restore transition and
returns no successful receipt on a failed commit. None releases the barrier.

Real PostgreSQL testing exposed an older snapshot encoding defect: JSONB rewrites
embedded JSON, invalidating the raw snapshot byte digest. Snapshot payloads now
use base64-encoded bytes and lifecycle aggregate format4. Old formats are rejected
before payload decoding. The existing two-VM data has not been upgraded or erased.

File, SQLite and a dedicated tmpfs PostgreSQL16 instance (loopback32939) passed
the restore transaction contract under race: wrong epoch refusal, state/receipt
commit, barrier release, successor state update, source snapshot deletion, and
exact historical receipt replay without rewinding serial/lineage. File and real
PostgreSQL tests also blocked aggregate acquisition, proved the restore held
authority exclusion, and confirmed concurrent barrier release had to wait.
These tests cover actual storage transactions; public HTTP orchestration and
complete activity freezing are still open.

Final verification: PostgreSQL-enabled full Go regression passed with package
parallelism limited to2. An earlier higher-load run had a slow-child receipt
fixture exceed its per-child timeout; that failure was retained, and no timeout
was enlarged in production. Focused restore race, persistence race, vet,
documentation checks and independent adapter review also passed.

### Public snapshot restore workflow

The API now exposes `POST .../state/snapshots/{snapshot}/restore-plan` with an
`Idempotency-Key`, returning an immutable request. Persist that JSON and submit
it to `POST .../{snapshot}/restore`; empty-body direct restoration is removed.
The workflow validates the canonical plan, starts/reuses its maintenance barrier,
checks activity/stop evidence, commits using the joint-lock adapter, and releases
the barrier. It returns200 with the receipt when restored, or202 with the bound
request while waiting. Clients retry the original or returned request; there is
no background maintenance scheduler yet.

`restore-cancel` accepts the same request and releases only an uncommitted
operation whose grants are confirmed stopped. It never starts a missing barrier,
and it rereads the receipt after release to report a concurrently committed
restore accurately. An already committed restore cannot be rolled back by this
endpoint. If commit succeeds but release fails, the restore response retains the
receipt with `committed_waiting_for_release`; a retry completes the release.

Tests cover API reconstruction and identical receipt replay, revoked-but-not-
stopped grants retaining exclusion, continuation after host stop, plan-key
requirements, successful HTTP replay, and explicit abandonment. The shared
activity registry continues to fail closed if any activity loader is absent.
The previous WorkspaceService direct restore method is removed. These checks do
not establish automatic reconciliation of changed inventory, a background
maintenance worker, full writer isolation, or a new two-VM restore gate.

### Automatic release recovery for committed restores

The existing API supervisor now scans committed restore receipts before health
and reconciliation for each topology. It inspects the existing maintenance
record, compares the exact request digest/topology/epoch, and retries release
with an independent five-second budget per receipt. It never starts maintenance,
restores state, or infers completion from a barrier without a receipt. Already
released receipts do not affect a successor barrier.

Regression tests reconstruct the API after state/receipt commit but before
release, then use the real supervisor scan to release the original barrier with
no additional serial increment. A later uncommitted barrier remains frozen.
Missing authority yields an error and is not recreated. Such an inconsistency
stops that topology's health/reconciliation pass; the outer scanner logs it and
continues other topologies, avoiding auto-repair under uncertain maintenance.

This automatic step runs only when the configured API supervisor runs. Pending
restores without committed receipts still require the original request to be
retried or explicitly abandoned; automatic pending-operation execution and full
maintenance scheduling remain open. No new cross-host acceptance is claimed.

### Canonical local recovery maintenance preflight

The API's local (no owning Agent) canonical recovery and cleanup entry now checks
the current topology's maintenance ledger before loading the pinned revision or
calling provider observers/deletion. A regression reproduced both paths modifying
the journal or resource under an existing freeze; both now reject with repository
conflict without additional provider reads/deletes or journal changes. Empty
existing authority data is rejected as corrupt rather than treated as absence.

This is an early refusal of an already-frozen topology. It does not atomically
exclude a freeze that begins after preflight; those local paths still require
the same effect/commit fencing as the owning-host path before full writer
isolation can be claimed. The preflight must not be cited as that completion.

The metadata-commit portion of local canonical recovery is now additionally
bound to the same transactional repository used for owning-host recovery. An
explicit internal local mode requires a Run with no Agent owner, exact original
incarnation/attempt/lease owner, failed/recoverable state, matching action
incarnation, and no active peer Run. The remote bridge cannot select this mode.
Authority and source Run are checked under the commit transaction; file storage
now also holds the actual authority-file lock, preventing direct authority
repository users from bypassing the API record lock.

A deterministic regression begins maintenance from inside the provider observer,
after preflight but before commit. The recovery now returns conflict and leaves
state/serial unchanged. Existing canonical recovery/supervisor and remote
recovery transaction regressions pass; shared local validation rejects remote,
changed, active or nonrecoverable Run evidence. This closes that local recovery
metadata-write window. Local cleanup's provider Delete path still needs effect
fencing; it remains protected only against a freeze already present at preflight.

### Managed SSH target identity contract

The host SSH coordinator no longer compares the topology's user-facing name to
its immutable UID. The signed request digest binds the name/node/configuration;
the separately signed scope binds topology UID, resource UID and generation.
Apply/Delete require a resource-scoped grant and matching action ID/host boot.
The Unix boundary verifies request name/node against the outer target and matches
the full signed scope before invoking a trusted callback. Command decoding also
rejects disagreement between the nested SSH request and NodeOperation target.

Regression fixtures now deliberately use a topology UID different from its name;
this reproduced denial of valid Apply and missing recovery evidence before the
fix. Callback admission and original receipt recovery pass afterward. Target
name/node/UID/resource/generation changes are rejected before any effect, and the
real Unix socket contract passes under race detection. These are protocol tests,
not a complete SSH key lifecycle acceptance. Shared sshd configuration, key
rotation/removal semantics, grant-bound installation and real end-to-end managed
SSH access remain required P7 work.

Managed SSH requests now parse actual authorized-key wire data instead of merely
checking for two whitespace-separated fields. Invalid encodings, mismatched key
types, multiline input and authorized-key options are rejected; each key line
is limited to16KiB. Options such as `command=` are outside this public-key-only
contract. Host/runtime key installation uses one canonical content function with
the same key ordering as the signed digest, so reordered requests cannot share
authority yet write different file bytes. Tests use valid encoded public keys
and cover malformed data/options/type mismatch and nonmutating content ordering.
This does not implement sshd reload, key rotation or precise owned deletion.

Runtime SSH deletion now derives its exact target from the canonical node user
and current access resource name, using the same path contract as the host
supervisor. It rejects a different user/access path or topology owner before
opening a connection. Missing ProviderContext now returns an error instead of
panicking. Both Create and Delete normalize `.id`-style node references before
resolving the connection/user. Tests verify rejection without reaching provider
access. This is target-identity validation, not content-CAS unlink: a file changed
concurrently still needs a guest-side owned configuration primitive. Live Read,
shared sshd lifecycle and full key rotation remain open.

### Live inspection of a durable file installation

`ConnectionInstalledFileInspector.InspectInstalledFile` and guest `file-inspect`
observe a committed installation's current target using its exact persisted
handle. SSH, vsock and Docker task transports expose the same SPI. The guest
validates historical grant/handle identity, current boot and parent directory,
opens without following symlinks, and checks bounded content hash, mode, size and
stable inode/timestamps. An identical replacement inode is drift, not ownership
continuity. Missing targets return absent; unsafe or changing observations return
unknown/error. No current file contents are returned. Historical file-observe
continues to report the original installation outcome.

Real filesystem tests cover unchanged content, content/mode drift, replacement,
absence and symlink rejection; transport tests reject a foreign epoch in the
response. Unknown handles are rejected before creating journal or lock files.
Full regression, focused race and vet passed; amd64/arm64 helpers are rebuilt
from the new sources. This is not yet wired into SSH access state: retaining its
installation handle, guest owned-delete transactions and shared sshd management
remain required. No new KVM or cross-VM live-file acceptance is claimed.

The owning host now exposes the same `file-inspect` operation through its Unix
socket. It loads an existing signed, committed file record without creating new
journal entries, checks the original reference and immutable provider identity,
and resolves canonical identity again after the guest read. A replaced node or
an inconsistent `present` result is rejected. Inspection does not renew grants,
mint stop evidence or rewrite the historical installation receipt.

2026-10-09 real Docker file E2E passed under race in4.02seconds with a helper
built from current sources. After10MiB installation and API/supervisor object
reconstruction, inspection reported present, then drifted after chmod, then
absent after removing that test file. Historical Observe still reported committed.
The existing completion-loss, successor-epoch and revoked-request cases also
passed, and the isolated test container was cleaned up. Host unit regressions
cover changed references/provider IDs, replacement during observation, and
unchanged journal bytes. This is the file inspection path, not yet the complete
SSH access resource/sshd lifecycle or an independent two-VM acceptance.

### Stable SSH configuration identity

Managed SSH requests now distinguish per-operation `id` from mandatory stable
`access_id`. The operation ID still binds command admission, effect identity and
receipt; the access ID selects the managed key configuration path. Apply/rotate/
delete may therefore use distinct operation IDs without switching to unrelated
files. Both identifiers are signed by the request digest and immutable after
dispatch. Runtime uses the HCL resource name as its access ID; host callbacks use
the supplied validated access ID rather than the operation ID.

Contract tests verify distinct operation digests for the same configuration path,
digest changes when access identity changes, missing-access-ID refusal, and
rejection of dispatched-payload access-ID mutation. This enables lifecycle
targeting but does not itself prove key rotation or shared sshd management.

### SSH host completion and evidence recovery

The previously adoption-only host node-operation completion route now accepts
SSH apply/delete through a dedicated validator. SSH stop signatures bind the
complete result digest; API validation also requires the stable access ID and
the expected canonical key-content hash for Apply or confirmed deletion marker
for Delete. The existing three-backend command/operation transaction records
completion before authority release. The generic stopped route rejects SSH to
prevent bypassing this ordering. Host recovery reloads original result bytes and
retries the original signed receipt rather than reporting a nil result.

File, SQLite and real PostgreSQL16 completion contracts passed with retired
commands and expired worker leases, identical replay, tampered evidence rejection,
correctly signed but inconsistent result rejection, and generic-stop refusal.
The database was an isolated tmpfs instance on loopback32954. PostgreSQL-enabled
full regression, focused SSH race and vet passed. Cold host recovery also verifies
that both original receipt and original evidence are retained. The temporary
database is removed after validation.

Old pending SSH receipts without evidence digest/access identity cannot be
automatically recovered under this contract; explicit rebuild remains required.
This closes protocol completion, not actual grant-bound key installation, shared
sshd configuration, safe deletion or complete SSH access E2E acceptance.

SSH canonical completion is now host-only on both API paths: the shared ordinary
worker completion transition rejects SSH apply/delete even with a valid live
command lease, for both done and failed reports. Agent execution emits delivery
progress but no longer posts a second worker-authored terminal NodeOperation.
A host transport failure therefore remains unconfirmed instead of allowing a
worker to overwrite the independently recoverable host result. Regressions
reproduced all four worker bypass cases and verify zero HTTP completion requests
from the Agent when the host path is unavailable. Other node-operation types
retain their existing completion rules.

Worker failure events still retire the delivery command. If SSH fails before
any host result exists, the canonical operation stays unconfirmed/queued rather
than receiving an invented failed result. Automated re-admission/reconciliation
for that pre-host failure remains a liveness gap; host result recovery already
supports retired command records.

The no-grant subset of that liveness gap now has explicit reconciliation.
After reading a durably retired failed/denied/cancelled command, the API checks
both current grants and retained stop history in a validated authority transaction.
Since issuance validates the same command transaction, no grant can be issued
after its retirement. Only a command with no execution history may mark its
matching SSH operation failed with `SSH access delivery failed before execution
authorization`. An issued, expired, stopped or superseded grant prevents this
inference; those operations still require host evidence.

The command-event path runs this check after retirement, and the API supervisor
repeats it from durable operation/command records after restart. Tests cover
never-issued versus issued commands, idempotent scan, and retained stop history
after a successor replaces the current scope. This does not automatically replay
SSH work or resolve an operation whose host effects remain unknown.

### Firecracker managed inventory and GC preview (2026-10-09)

`ListManagedNodes` enumerates durable manifests under the configured runtime
root and reuses the normal ownership, generation, canonical-path and config
digest verifier. Corrupt records, duplicate identities, symlinks, a non-directory
root and nonregular manifests fail the entire observation; no partial list is
returned. Adoption lookup also reports malformed JSON instead of treating it as
NotFound. Unknown JSON fields are not rejected; this is not strict schema decoding.
The before/after manifest comparison detects ordinary updates, not an atomic
tree snapshot or ABA changes. Returned handles are observations, not proof of
liveness or permission to delete.

CLI GC preview now includes Firecracker and protects IDs referenced by current
canonical state or retained snapshots. Unknown owners are skipped. Missing
runtime roots are reported as unavailable; malformed existing inventories fail
the preview. A batch containing Firecracker is rejected before any delete or
journal intent because the authorized reference-revalidation pipeline is not
yet connected. No new raw Firecracker deletion path was added.

Real KVM `TestFirecrackerRealGenerationLifecycleE2E` passed in 9.89 seconds,
including inventory of the actual VM and the existing lifecycle cleanup. This
is not a real orphan-GC acceptance. Command/provider regressions cover snapshot
retention, owner filtering, corrupt-inventory propagation and whole-batch
execution rejection. Independent review found malformed directory entries could
be accepted as empty inventory; regression tests reproduced and fixed both cases.
After those fixes, the full Go suite and command/Firecracker race suites passed;
vet, documentation checks (53 canonical documents) and independent re-review
also passed. This round did not enable a real Postgres test database.

P8 remains open: full API reference inventories, TAP orphan discovery, authorized
plan dispatch and provider GC acceptance remain unfinished. Inspection also found
the production host GC callback enters the coordinator a second time under the
same gate, while its inventory validator currently returns success without
checking references. Those paths require repair before automatic GC acceptance.

### GC gate and coherent lifecycle references (2026-10-09)

The nested production GC coordinator call was reproduced as a timeout before
the provider ran. `Server.GCEffect` now accepts only context and candidate; the
RPC server owns the sole coordinator invocation. Provider clients initialize
inside that callback, so Docker setup cannot disable libvirt/Firecracker GC or
historical GC observation and receipt recovery. A Unix-socket regression invokes
the real server/client with a simulated Docker endpoint, completes deletion and
observes its recorded result. This is protocol integration, not real provider GC
acceptance.

The production no-op inventory validator has been removed. Until the complete
reference/plan authority is connected, a new host GC delete returns a fenced
error before authorization, journal intent or provider access. Already recorded
outcomes and stop receipts remain recoverable. Removing the recursive lock must
not inadvertently enable deletes based only on a worker-provided digest.

`Repository.LoadGCReferences` now gathers state, successful action candidates,
retained snapshot state and retained snapshot action candidates in one aggregate
transaction. Each resource identity includes its source. The digest binds the
entire aggregate, including action revisions and snapshot catalog, and is stable
across unchanged reads. Unresolved current/retained journals, corrupt snapshots
or storage failures return a zero snapshot, never partial references. This shared
implementation serves file, SQLite and Postgres and replaces the CLI's three
separate lifecycle reads and duplicated reference loops.

CLI GC no longer reads standalone `state.json` records or keeps the old fallback
collector. Their presence requires explicit rebuild. Existing lifecycle data is
not rewritten, but prior GC inventory digests must be regenerated. The lifecycle
snapshot still excludes API Run/command/guest-task/file/revision/placement and
authority references. It is a planning input, not a complete deletion proof or
a lock held through provider execution. P8 remains open for that integration,
legacy raw CLI deletion removal and real orphan-GC acceptance.

Validation: the full Go suite passed with an isolated Postgres 16 database
enabled for persistence tests via `SYSBOX_TEST_POSTGRES_DSN`. The API integration
tests require the separate `SYSBOX_TEST_DATABASE_URL` variable, which was not
set in that round; its Postgres branches were skipped. Host-supervisor and
persistence race suites passed;
the commands race suite passed before the socket integration addition, and the
final socket/nil-validator cases passed under race after correcting fixture
directory permissions and target fields. Vet, docscheck (53 canonical documents)
and independent re-review passed. No real provider resource was deleted in this
round; the temporary database belongs only to this test run.

### Metadata deletion and outstanding authority (2026-10-09)

A regression reproduced metadata deletion succeeding after a Run was marked
failed while its provider grant still lacked a host stop receipt. Ordinary and
forced API deletion now commit through a shared quiescence/CAS transition. The
file adapter holds API record and authority locks through lifecycle commit;
SQLite uses `BEGIN IMMEDIATE`; PostgreSQL locks admission, materializes/locks
the authority row and then locks the aggregate. The PostgreSQL insert is needed
because `SELECT FOR UPDATE` alone cannot exclude first issuance into an absent
authority row. No provider effect is performed by this metadata transition.

All grants for the exact topology UID must have persisted stop evidence, and no
unreleased maintenance barrier may exist. Revoked or expired grants still block
deletion; `force` does not bypass them. Grants for other topology UIDs do not
block this topology. The serial/UID comparison prevents a stale delete from
resetting a successor. Cleanup inspection includes `execution_grant` entries
and marks an otherwise available receipt `blocked_by_execution_authority` when
there are unresolved effects.

Reads, grant transitions, maintenance writes, restore and metadata deletion use
the same authority decoder. Missing mandatory JSON fields, malformed scopes,
invalid states and stopped records without receipts are rejected. Only an
actually absent ledger initializes an empty authority; an existing `{}` record
is corruption. Independent review caught default initialization masking missing
fields, and explicit regression tests reproduced and fixed that behavior.

File/SQLite/Postgres tests cover failed Run versus outstanding authority,
revocation, exact host-stop acceptance, maintenance and unrelated topology
grants. File and PostgreSQL concurrency tests block lifecycle commit and verify
that first authority creation cannot pass the held authority boundary. SQLite
uses its single write transaction for both records. Both
`SYSBOX_TEST_DATABASE_URL` (API) and `SYSBOX_TEST_POSTGRES_DSN` (persistence) must
be set when running the complete database regression.

This closes deletion over already-outstanding authority, not the entire P6/P8
protocol. All operation registry/admission checks still need a single durable
exclusion model, and issuance based on a stale lifecycle UID after deletion must
be fenced at its source. Workspace filesystem cleanup remains outside the
database transaction. Automatic GC is still disabled pending complete reference
and plan authorization.

Validation completed with both PostgreSQL test variables set: full Go regression
passed, including API and persistence Postgres cases. API race (48.90 seconds)
and persistence race passed; vet, docscheck and independent re-review passed.
Real Docker `TestHostSupervisorRealTaskStopAndSuccessorE2E` passed in 3.39 seconds
and `TestRemoteDockerCompletionLossRecoveryE2E` in 12.64 seconds under race with
a freshly built guest helper. The latter confirms that host-signed Destroy and
cold API metadata deletion still succeed without force after verified stops.
These tests use isolated resources; they do not replace two-host P9 acceptance.

### Command grants bind the current lifecycle UID (2026-10-09)

The late-issuance regression reproduced an old retained command acquiring a new
epoch after its previous grant stopped and API metadata deletion replaced the
topology UID. Command-backed issuance, renewal, host authorization and derived
guest authority now revalidate the durable command's topology against the
canonical lifecycle aggregate within the authority transaction. GC issuance
uses the same command-backed path instead of constructing an independent grant.

File storage checks the aggregate while holding the API record and authority
locks; SQLite reads inside its write transaction; PostgreSQL locks authority,
command and lifecycle rows through commit. The topology name comes only from
the persisted command payload. Missing/replaced lifecycle evidence fails closed.
Session and stop-receipt transitions remain usable without current lifecycle
metadata so historical execution evidence can still be recovered.

Tests cover ordinary late issuance and simultaneous grant/delete attempts on
file, SQLite and real PostgreSQL: either the grant commits and deletion conflicts,
or deletion commits and issuance conflicts. Neither can return a speculative
grant on failure. Independent review found lifecycle rejection bypassed the
existing durable clock-floor update; the added failing regression was fixed by
sharing the source-error path. Rejected proofs commit only their observed clock,
preserve the original source error and never extend command leases.

This binds topology incarnation, not every resource generation or complete GC
reference plan. API operation admission and reference enumeration still need
their full shared protocol. Direct offline repository mutation remains outside
the API authority transaction; automatic provider GC remains fenced.

Validation: full Go regression passed with both PostgreSQL variables enabled;
three-backend concurrent issuance/deletion tests passed under race. The final
clock-floor fix passed the full database regression and independent re-review.
With a freshly built helper, real Docker host task/stop/successor E2E passed in
3.01 seconds and Run completion-loss/recovery/Destroy/cold metadata deletion E2E
passed in 12.86 seconds under race. Vet and documentation checks passed. The
test-only PostgreSQL container was stopped and automatically removed afterward.

### GC grants validate lifecycle references (2026-10-09)

Previously GC issuance accepted any nonempty inventory digest. The regression
reproduced issuance with an invented digest. The shared command/lifecycle
validator now derives the reference snapshot under the existing authorization
transaction, compares its exact digest, and rejects a candidate whose resource
UID or provider-scoped external ID is retained by state, journal or snapshots.
Different generations of the same resource UID remain protected. The same
validator runs for issuance, host authorization and renewal; a reference change
after issuance rejects further authorization/renewal without extending the old
grant. Stop receipts remain acceptable after references change.

This reuses the existing reference algorithm, not a second API-specific scan.
File/SQLite/real PostgreSQL race tests cover invented/stale digests, current and
snapshot-only references, and new references after issuance. It does not yet
validate the separate API activity registries, immutable GC plan ownership or
provider inventory. It also does not hold exclusion through a provider call.
The production host GC reference validator remains unwired, so this improvement
does not enable automatic deletion. The legacy local CLI deletion path still
needs removal as part of P8 completion.

Validation: full Go regression passed with both PostgreSQL test variables set;
targeted GC/authority/metadata race suites passed on all three backends. Separate
identity tests cover UID protection across generations, external-ID protection
under a different UID, and equal external IDs in different providers. Vet,
docscheck and independent review passed. No real provider GC effect was run.

### Retire the raw CLI GC deletion path (2026-10-09)

The CLI no longer exposes `gc --execute` or carries `executeOrphans`, its private
deletion journal/replay implementation, direct Docker remove calls, or direct
libvirt destroy/undefine calls. Those implementations could not share exclusion
with lifecycle writers and bypassed the API/host authority contract. Their tests
were removed with the obsolete implementation; the canonical provider and host
gate tests remain. `gc --dry-run` still inventories scoped candidates, and bare
`gc` explicitly refuses execution.

A regression verifies `--execute` is no longer a recognized flag. A preview
integration test uses read-only Docker/virsh fixtures and a historical uncertain
GC journal, verifies its bytes remain unchanged and confirms no old lock or
replacement journal is created. No on-disk historical journal or provider
resource was deleted during this cleanup. Removed source remains recoverable
from Git history. The old GC design document now describes the canonical model
and marks the complete authorized deletion workflow as unfinished. P8 remains
open; removing the bypass does not deliver automatic garbage collection.

Validation: full Go regression, commands race, vet, documentation checks and
independent review passed. This source-removal round did not start PostgreSQL
or run real provider GC effects. No user data was removed.

### Maintenance activity scope (2026-10-09)

The shared restore/force-unlock quiescence check incorrectly passed a topology
name to the NodeOperation list's workspace filter. A running operation with
`Topology=lab` and `Workspace=project-workspace` was therefore omitted, allowing
the preflight to report quiescence. It now enumerates node operations and filters
their actual topology, as metadata deletion and cleanup inspection already do.

The regression reproduced the omission on file and SQLite stores. A three-store
workflow test verifies running operations block force-unlock and retain restore
in `waiting_for_stop`, with no receipt or lifecycle serial change. After the
relevant operation becomes terminal, restore completes even when an unrelated
topology in the same workspace remains queued. This fixes activity selection;
the reads remain separate snapshots, not the completed cross-registry admission
transaction required by P6/P8.

Validation: the full API package passed under race (51.28 seconds) with the
isolated PostgreSQL database enabled alongside file and SQLite. Vet, docscheck
and independent review passed. The temporary PostgreSQL container was stopped
and automatically removed; this round performed no provider effects.

### Guest/file/node admission during maintenance (2026-10-09)

Restore maintenance previously froze grant issuance but did not freeze new
guest execution, guest file, or node-operation dispatch. Those writes could pass
a quiescence read and publish after a barrier began. All three dispatch paths now
validate the durable authority barrier while retaining the same lock/transaction
through operation and command publication. File storage holds the record and
authority locks; SQLite uses one immediate write transaction; PostgreSQL
materializes and locks the authority row before both inserts. Missing topology
UIDs fail closed.

Three-backend tests verify frozen rejection leaves neither operation nor command
for all three dispatch kinds, and release permits execution admission. Repeated
concurrent execution admission/freeze tests include initial authority absence:
successful dispatch is visible before maintenance returns, or maintenance wins
and dispatch conflicts. File/node concurrency uses the same transaction helpers
but was not separately stress-tested. Full API race passed (51.83 seconds) with
real PostgreSQL. Real Docker task-stop/successor (3.12 seconds) and 10 MiB file
install/recovery/presence (2.24 seconds) E2E passed. Vet and independent review
passed. Run dispatch and other raw metadata writers remain outside this slice,
so P6 is not complete.

### Console sessions in quiescence (2026-10-09)

The activity registry now includes durable ConsoleSession records. A queued or
running console session, including one whose workspace name differs from the
topology, blocks restore, force-unlock and other state maintenance. Missing end
timestamps also fail closed unless the session is explicitly terminal. The new
regression uses the real API store and verifies the maintenance conflict; the
existing incomplete-registry tests continue to require every activity source.
This closes a missing activity category, while cross-registry atomic admission
and raw metadata writer fencing remain P6 work.

AgentCommand activity is now included as well. A nonterminal command carrying
the topology UID blocks state maintenance even when no guest activity record
has been created yet; terminal/retired commands do not. This closes the gap
between durable dispatch and guest operation publication. The targeted race
regression passed; raw writer and cross-registry transaction work remains.

The final full Go regression also passed with both PostgreSQL test variables
enabled. The isolated test database was stopped and automatically removed after
verification; real Docker tests cleaned their own containers and helper files.

### Run dispatch during maintenance (2026-10-09)

Run admission and queued-to-assigned outbox publication now validate the same
durable maintenance barrier as guest/file/node dispatch. New requests fail before
writing the run, idempotency mapping or command; a queued run remains queued.
SQL transactions retain the authority lock through run and command inserts; the
file adapter retains its records/authority lock through the atomic bundle. Exact
committed request retries remain historical reads during a freeze and do not
publish a successor command.

Three-backend race tests cover first authority row creation, different run keys,
queued and assigned paths, idempotency mapping rollback and exact retry. Real
PostgreSQL and full repository regression passed. This closes Run dispatch for
the maintenance barrier; raw run Save/Restart and other metadata writers still
need the same admission port, so P6 remains open.

Raw Run metadata updates are now covered too. `SaveRun`/`RestartRun` use the
same authority-before-lifecycle ordering as dispatch: local file writes hold the
authority lock through the append, SQLite checks inside its write transaction,
and PostgreSQL holds the advisory/authority/lifecycle locks before updating the
Run row. Missing legacy lifecycle records remain rebuild/legacy mode; a real
versioned maintenance barrier rejects the update. A three-backend regression
verifies SaveRun rejection during a barrier and success after release, while
existing dispatch/idempotency suites remain green. Global projection/policy/
placement writers still need the same port.
