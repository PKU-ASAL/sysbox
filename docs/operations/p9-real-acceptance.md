# P9 real acceptance

## Current SSH operation revalidation during grants (2026-10-11)

Issuance/authorization/renewal now read the current guest SSH NodeOperation in
the authority adapter, rather than relying solely on a queued command snapshot.
Missing operation, terminal status, changed mutation, incarnation, node, command
or resource generation reject the source. A semantic rejection retains the
durable authority clock floor and cannot extend the delivery lease.

The authenticated grant regression first reproduced signing for a missing
operation, then passed after the check was added. All guest SSH HTTP tests ran
with race on filesystem, SQLite and an isolated real PostgreSQL 16 instance.
Direct rule tests cover terminal, missing, replaced and prepopulated-result
operations. This does not close the preexisting PostgreSQL completion/issuance
lock-order inversion; concurrent completion and renewal need correction before
production admission/dispatch is enabled.

## Guest SSH command-backed grant issuance (2026-10-10)

The production command execution-grant endpoint now derives guest SSH grants
from the persisted NodeOperation.SSHMutation. The shared command decoder checks
agent, action, command binding, topology/node, resource/generation, operation
kind and nonterminal state. It does not accept replacement mutation data from
the requesting worker.

Authenticated HTTP tests exercised signature verification, exact CAS payload
digest, guest boot binding, rejection of an extra mutation field, online
rejection of a newly signed envelope for a different CAS revision, and retired
command rejection. Local, SQLite and a dedicated real PostgreSQL 16 instance
all executed with race, alongside the completion and injection regressions.
Shared controlplane, Agent and host race suites and relevant vet passed.

The test seeds a durable accepted command. Public admission, host mutation
dispatch, current guest result reporting and automatic sshd deployment still
need integration; this does not prove an end-user SSH lifecycle.

## Guest SSH canonical completion across all three stores (2026-10-10)

NodeOperation now persists the immutable guest SSH mutation and the committed
SSH result. The three existing host-completion adapters share the same guest
SSH transition: exact command/action/attempt, resource scope, canonical node,
request digest, user and CAS revision are checked. Retired commands recover
from the retained operation request plus immutable command tombstone identity.
Ordinary worker completion cannot finish guest SSH; ordinary operation CAS
cannot inject a result or change its original mutation.

The existing host node-operation completion HTTP route now authenticates the
new receipt, checks the canonical mutation, commits through that shared rule,
then releases authority. `TestGuestSSHStopCannotBypassCanonicalCompletion` and
the preexisting SSH host-completion regression ran with race on local, SQLite
and a dedicated real PostgreSQL 16 instance; every backend subtest executed.
They cover generic-stop rejection, signed wrong-node rejection, command
retirement, result persistence and exact retry. Shared persistence/controlplane
full race suites also passed.

Review identified a creation-time injection path: queued operations could be
seeded with SSHResult before ordinary CAS. The failure reproduced on all three
stores. Shared encoding/decoding now rejects prepopulated queued/running results,
raw Create and atomic dispatch reject injection, and ordinary CAS cannot advance
guest SSH to a terminal state. Those three-store regressions passed with race.
The API (real PostgreSQL enabled), host and Agent full race suites passed as
well before the final codec guard; the guard was then checked with all-store
focused API and shared persistence/controlplane regressions.

This is canonical completion, not a full remote workflow: production admission,
issuer and host mutation dispatch still need wiring. The tests seed their own
command/operation/grant records; they do not prove production issuance.

## Guest SSH completion evidence contract (2026-10-10)

Added a signed SSH result to the common execution-stop receipt. Generic stop
signing rejects guest SSH operations without their concrete result. Dedicated
signing/verification binds request digest, user, AccessID, CAS revision,
guest boot, resource scope/generation, epoch and apply/delete presence.
A test additionally signs an inconsistent revision with a valid host key and
confirms canonical request verification rejects it.

The local and SQLite API regression reproduced the generic stopped endpoint
prematurely releasing a guest SSH grant (HTTP 200). That route now rejects it
with HTTP 409 and retains the authorization scope until dedicated canonical
completion can commit. This is a prerequisite contract/security boundary;
production issuance and dedicated completion are not yet wired. No new real
PostgreSQL or distributed SSH completion pass is claimed by these tests.

## Canonical repository through host RPC to real Docker discovery (2026-10-10)

`TestDockerProductionSSHAccessE2E` now persists the real container's identity
and explicit `ubuntu` connection user in a filesystem lifecycle repository,
closes the writing repository, and uses `CanonicalResolver.Resolve` in the
host dispatcher. Every RPC reopens the repository; no fixture resolver supplies
the user, provider ID or connection.

The real test, using a freshly built helper and race, passed in **0.92 seconds**:
initial absence, signed configuration creation, discovery through the Unix
host RPC with canonical node name/user, stop and recreation of the supervisor
service instance, unchanged discovery, deletion tombstone, normal provider
Destroy and exact container NotFound. A stale worker generation was also
rejected through the real host socket. Production provider identity checks
remain in the path. The two server instances run sequentially in the same test
process: this is not an independent-process crash or two-host acceptance.

Mutations still use fixture-signed guest grants via the provider SPI. The test
does not claim API issuance, host mutation completion, runtime resource migration
or automatic sshd config installation. Those P7 requirements remain open.

## Canonical guest user after cold handle reconstruction (2026-10-10)

The host resolver now recovers its guest user from the node's persisted desired
connection/provider configuration after verifying real node identity. Explicit
first connection.User takes priority, then provider_config.SSHUser; absence
stays unspecified. No root fallback or worker-selected user was introduced.
Malformed configuration and invalid usernames reject guest connection resolution
before connecting. Provider-only NodeTarget resolution remains independent, so
SSH configuration does not block power operations.

The canonical Run-candidate repository test persists the username, reopens the
record through the normal resolver and verifies `Target.User == ubuntu` while
retaining wrong-node rejection. Unit tests cover precedence, absent fields,
malformed values and not selecting a later connection implicitly. Host full
race and focused persistence regressions passed; this is not two-host SSH
grant issuance/completion acceptance.

## Owning-host SSH discovery RPC boundary (2026-10-10)

Added the read-only `ssh-discover` host RPC and client. Only canonical target
identity plus AccessID cross from the worker; the resolver supplies the guest
user, provider ID and connection. Missing user and scope mismatches reject
before guest discovery. The host validates returned configuration and maps the
provider ID back to the canonical node name; the client revalidates the result.
Shared `SSHAccessDiscovery.Validate` is also used by guest transports.

A real Unix socket regression exercised serialization, host routing and client
validation with a trusted fixture resolver. Negative checks cover missing user,
wrong scope, foreign response and unsafe AccessID. This is RPC boundary evidence,
not a real canonical repository/API/Agent or two-host discovery acceptance.
Guest grant issuance, mutations and automatic sshd deployment are still open.

## Production Docker SSH capability (2026-10-10)

The formal Docker provider connection now implements the complete
`ConnectionSSHAccess` SPI by forwarding through its existing guarded
`dockerConn.Exec` helper channel. No separate Docker client or unguarded
command path is introduced. The daemon-based regression proves discovery,
mutation, observation and inspection reject stale resource generations before
any exec creation.

`TestDockerProductionSSHAccessE2E` ran with the current static helper and real
Docker under race, passing in **0.52 seconds**. It used `Substrate.Connection`,
not the earlier test-only Docker shell adapter: signed create, discovery,
observation through a newly obtained connection, stale ownership proof rejection,
signed deletion and revision tombstone all passed. Normal `DestroyNode` succeeded
and exact container inspection returned NotFound. The fixture used a cached
Alpine image and introduced no host SSH configuration.

The test is included in `make test-p9-real`; the full matrix was not rerun for
this change. Provider race and vet are separate checks. This closes Docker's
missing connection capability, not runtime resource handling, host/API grant
issuance/completion, or automatic sshd config publication.

## Managed SSH discovery through a real helper (2026-10-10)

The disposable two-listener sshd/Docker-helper lifecycle now additionally uses
`DiscoverSSHAccess`. The current helper reported initial absence without a
record, returned the original handle/config after creation, rejected a foreign
owner as unknown, and returned the committed deletion tombstone at revision 3.
Rotation, revocation, independent access preservation and endpoint restriction
checks continued to pass; the full test finished in **5.20 seconds**.

Discovery does not write or mint grants. Unit/race coverage checks cold reads,
corrupt records, owner/resource/generation conflicts, response query/digest/
revision tampering, and real Engine round trips without extra mutations.
This is configuration discovery over the Docker helper adapter, not proof of
SSH/vsock transport E2E, live service readiness, or runtime/API integration.

## Staged SSH config syntax and effective-command conflicts (2026-10-10)

`ValidateSSHD` runs `sshd -t -f` followed by `sshd -T -C` for each supplied
connection probe. It requires the generated command and command user to be
effective, and the expected administrator key-file setting to remain unchanged.
Missing/duplicate/oversized output and subprocess failures reject validation.
The real test now invokes the production `NewSSHDRunner`: absolute executable,
validation modes only, a five-second call budget, and a combined 1 MiB output
limit. Subprocess regressions cover stdout/stderr overflow, nonzero exits and
caller cancellation; failed calls return no partial configuration.

`TestRealSSHDEffectiveConfigurationConflictE2E` passed against local OpenSSH
8.9p1 using temporary config and host keys, without starting or changing the
system daemon. Four address/port combinations exercised intersection, address,
port and fallback rules. Earlier Match rules selecting a foreign command,
different command user or `AuthorizedKeysFile none` were rejected. A global
Port inside a conditional Match was rejected by syntax checking. The local
version accepts Port after Match all, so no text-only assumption is used for
that case.

This proves staged-config validation for the supplied probes. Production
enumeration of all relevant Match classes, automatic publication/reload and
post-reload observation remain required; it is not a global config equivalence
proof or a P7 completion claim.

## Generated SSH endpoint rules: real two-listener validation (2026-10-10)

`pkg/sshaccess.RenderSSHD` generates deterministic, bounded
`Match LocalAddress/LocalPort` rules feeding fixed endpoint arguments to the
guest helper. Intersections precede partial matches and the unrestricted
fallback. It does not modify listeners or `AuthorizedKeysFile`.

The existing disposable sshd test now uses these generated rules and two
loopback ports. The production helper is reached through the test-only Docker
command adapter. After the original rotation/deletion checks, a key restricted
to `127.0.0.1` and the first port successfully authenticates there and receives
`Permission denied` on the second port. The independent unrestricted managed
key and administrator key still authenticate on the second port. The real test
passed in **5.49 seconds**, using a helper built from the current source.

This extends the earlier unrestricted-key test below. It verifies the generated
configuration against a real sshd, not automatic config installation or reload,
third-party command coexistence, runtime/API dispatch, or a two-host workflow.

## Real managed SSH access rotation and revocation (2026-10-10)

Built the current static `sysbox-init` helper and ran a disposable OpenSSH
server whose `AuthorizedKeysCommand` invoked that helper inside an isolated
Docker container. The helper reopened its durable guest store for every lookup;
the server's administrator-owned `authorized_keys` remained separate.

The test proved an administrator key could log in before, during and after the
managed lifecycle. A managed key was added, could log in, then was rotated:
the old key was rejected and the replacement key worked. A second independent
managed access continued to work. Deleting the first access with its old
revision was rejected; deleting with the current revision revoked the
replacement key while preserving the second managed key and administrator key.
Historical observation of the original handle remained readable and current
inspection returned `absent` at revision 3. The corrected test passed in
**4.09 seconds**, requiring `Permission denied` for rejected keys rather than
accepting arbitrary connection failures.

The command uses only the OpenSSH-supported `%u` token. OpenSSH supplies neither
local address nor local port through this command's tokens. Unknown endpoints
therefore authorize only configurations without endpoint restrictions. A new
regression reproduced and fixed port-restricted keys incorrectly being emitted
for port zero. The real test now explicitly uses unrestricted configurations;
it does not prove listener-restricted login. Invalid or corrupt records fail
closed and emit no keys.

This proves the guest helper and its Docker-backed transport against an
independent sshd. SSH/vsock adapter unit tests are separate evidence; this test
does not exercise those transports. Host/API dispatch and multi-API completion are
still separate P7 gates; this does not close P7 or the full P0–P9 matrix.

## Real private-network partition with a running guest task (2026-10-10)

On the retained two-VM deployment, `p9-0d4114398801` started a 600-second
guest task. The injector verified its populated cgroup, process incarnation,
resource identity and parent resource checkpoint, then brought down only the
execution VM's `lan0`. Management SSH continued over `nat0`. TCP probes to
control API port 19870 and PostgreSQL port 5432 were reachable before the fault,
unreachable during it, and reachable again after restoration. PostgreSQL
MainPID remained **9474** throughout.

The original guest task was cancelled after **136.92 seconds**, with the same
process/cgroup identity and `populated 0`. After restoration, its original host
stop receipt was reported and the API parent Run became failed with persistent
host completion: `run controller stopped after resource execution; child
termination confirmed`. The acceptance verifier additionally checked unchanged
provider ID, complete handle, Run ID, and receipt grant scope/epoch/session/
command/task digest. Receipt signature acceptance is provided by the API;
the Python verifier independently compares the recorded identities.

- Parent: `request-423e2fc6381c429d9172d306a9ebdf5b5e8004a9683aa9b82b4e6ee9cd71a85f`.
- Child: `run-task-37292acd8a10708be89c8ab94b4b1655d2c0b5dd186be8f1cecdaf808a150b47`.
- Host fault evidence: `/tmp/sysbox-p9-lab-current/p9-0d4114398801-private-network-outage.json`.
- Control original: `/var/tmp/p9-network-partition-retry-20261010.json`.
- Control terminal proof/cleanup: `/var/tmp/p9-network-partition-retry-cleanup-20261010.json`.

Normal Destroy succeeded. Exact container
`6dd3bf2930cf827f1bb523455ebf1b06d9b65b78fcabdbac40faf960e995816c`
and all containers under owner `86f17057-a658-4f15-b14c-16b30e41154e` were
independently absent. Normal metadata DELETE completed without force.

The first experiment, `p9-f9d367d24376`, retained its original incomplete fault
evidence: a single immediate ping after link-up failed and prevented the final
evidence save. Its canonical stopped result was subsequently verified, followed
by normal Destroy, independent absence checks and metadata DELETE. No original
evidence was overwritten. The injector now waits for connectivity, separately
records fault/restoration/final-validation errors, and preserves intermediate
readiness failures. The successful experiment reproduced two initial failed
pings before connectivity returned, confirming the need for readiness polling.

This closes the Docker running-child **private-network partition** scenario
after a successful resource checkpoint. It does not prove unknown provider
intent recovery, other providers/transports, mid-effect ENOSPC, upgrade or the
full P0–P9 matrix. Historical statements below describe earlier checkpoints.

## Real host-supervisor SIGKILL after resource checkpoint (2026-10-10)

On the current two-VM deployment, started a 600-second guest task on
`p9-4ff3d9839d6c`. Before SIGKILL, verified exactly one host Run intent with a
version1 resource checkpoint and its children observed running. Killed only
the execution VM's supervisor MainPID **64200**. systemd started replacement
MainPID **91249**. After **4.65 seconds**, the original Run outcome was complete
and reported as failed: `run controller stopped after resource execution;
child termination confirmed`.

- Parent Run: `request-5f77e96be5d9712fab69aecb43613e6878c7ae1afa54c0ca4764073a491415f6`.
- Child: `run-task-a95c0d3bf904bd94227e526813e6f823c4eface36f2035cdabfc533285360536`.
- Fault evidence: `/tmp/sysbox-p9-lab-current/p9-4ff3d9839d6c-supervisor-crash.json`.
- Control original evidence: `/var/tmp/p9-supervisor-crash-20261010.json`.
- Control cleanup/terminal proof: `/var/tmp/p9-supervisor-crash-cleanup-20261010.json`.

Independent comparison confirmed full child handles, Run IDs and provider IDs
unchanged; child termination was confirmed and its stop receipt reported.
The API returned the same parent Run with persistent host completion evidence.
Normal Destroy succeeded. Exact container
`1d7b23d7eee83727f16d98d79be129d770fa55d79ed5dfe1927650ab2a7bce00`
was independently absent, with no containers under owner
`ac2a56f1-af3a-4bf7-85b2-3b82d72cd255`. Normal metadata DELETE completed.

The fault script now checks the complete child incarnation, not just task ID,
and rejects generation changes. Its four unit tests pass. This proves recovery
**after a successful resource checkpoint**; cold intents interrupted inside a
provider callback (including retained Destroy `p9-5213226d26bb`) remain a
separate unresolved case. It also does not prove a cross-version upgrade.

## Real isolated ENOSPC before resource creation (2026-10-10)

On the execution VM, mounted a new **1 MiB tmpfs** at
`/var/tmp/sysbox-enospc-20261010`; no root filesystem or database disk was filled.
The filler received actual ENOSPC. CLI Apply failed writing the initial
lifecycle aggregate: `.aggregate-459101826: no space left on device`.
Only a zero-length aggregate lock file remained; Docker inventory contained
no container for that topology. Failure preceded intent admission/provider work.

After truncating only the generated filler, Apply on the same state path
succeeded. Container
`1fcd618eff8707976b65de7102c1aa9e5025d5e5566854abde16cd6ee7dc5618`
had owner `df212276-89c1-4864-8ed4-a63a55d65da4`, generation 1.
Normal Destroy succeeded; exact-ID inspection returned no such object and
owner inventory was empty. All tmpfs files and the failure log were archived
before unmounting the exact test mount.

Execution VM archive: `/var/tmp/sysbox-enospc-20261010-evidence.tgz`, SHA256
`865520ee9e95c6d5276eabbf7430227b5859730cf48bdab18ebdf50b652991b8`.
Reusable fixture: `scripts/acceptance/storage_enospc.sh` and
`storage_enospc.hcl`. The fixture establishes the initial persistence boundary;
same-state recovery and independent cleanup checks must follow.
This does not prove ENOSPC during running effects, host receipt publication
or PostgreSQL WAL writes. Those remain separate required gates.

## Destroy evidence preservation correction (2026-10-10)

Inspection of the interrupted Destroy path found two independent correctness
bugs: runtime substituted an ID-only handle after codec failure, and ignored
attachment request/delete failures before removing the node and its state.
Failing regressions reproduced corrupt-private-state, missing-network and NIC
delete-error paths returning success. Runtime now validates the handle and all
attachment requests/capabilities before effects; non-NotFound attachment errors
stop node deletion and preserve state. Cancellation is checked between effects.
The existing best-effort Stop remains separate from authoritative Destroy.

Runtime, Agent, Docker/libvirt/Firecracker full race suites, vet and independent
review passed. A partial-attachment regression verifies that a retry accepts
the first attachment's NotFound, finishes the second and deletes the node once.
This fixes evidence loss, not unknown-effect recovery authorization.

Read-only reinspection of retained `p9-5213226d26bb` confirms its exact container
`7c7bb3e764fd3c36c88adc6554fd07b10bb61e020c8e7da261662acf574c54fc`
still exists, exited. The corresponding host Destroy outcome remains `intent`
with no run checkpoint or result. No forced cleanup or journal rewrite occurred;
explicit safe recovery of this case remains required.

## Real running-child API failover with bounded health detection (2026-10-10)

Added `scripts/acceptance/live_failover.py`: it verifies a unique running child
and actual populated cgroup, immediately stops API1, requires unchanged
task/container/Run identities and successful completion read directly from API2,
then restores API1 in finally. Failure evidence is retained; new checks also
require API1 still stopped at completion and record restoration errors.

An automatic run on `p9-e28b2ffc3e5e` with default HAProxy health settings
failed: the child was cancelled. Primary was restored, its canonical signed
failure was retained, normal Destroy and exact Docker absence verification
completed, then normal metadata DELETE succeeded. Evidence:
`/tmp/sysbox-p9-lab-current/p9-e28b2ffc3e5e-live-failover.json` and control VM
`/var/tmp/p9-live-failover-retry-cleanup-20261010.json`.

Default multi-second health detection exceeded the host's bounded online
authorization window. The acceptance proxy now uses `inter/fastinter/downinter
100ms`, `fall 1`, `rise 1`, and `timeout connect 500ms`. Original configuration
was archived before a validated HAProxy reload. Host authority deadlines and
explicit rejection semantics were not relaxed. This is a deployment constraint,
not a promise of seamless failover with arbitrary proxy settings.

With those settings and bounded transport retry, topology `p9-5098f4f6620b`
passed actual running-child failover:

- API1 stopped at `2026-10-10T05:00:55.721911080Z`, after running-task and
  populated-cgroup checks.
- Run `request-f2b1073eef58bf403b8fd1c25be1b5140af474ad8273cf0ff9a9dc8b65ebbbdc`
  completed on API2 at `05:02:19.630365233Z`, attempt 1, with API-returned
  durable host completion proof.
- Child `run-task-270a845ac3f550f78a39679c46d21af268565e1ed0e5a1048a0d23ce75426c10`
  retained its exact handle and container, was never cancellation-marked,
  exited with known code 0 and reported confirmed termination.
- Independent systemd journal confirms API1 restarted only at `05:02:21Z`.
- Destroy `op-fa5e4b10904a035af1190ef741e7ef25a718d3df49c81a4f0d790df2f38cd994`
  completed. Exact container `20e5fc5f3190cc273f52ff69125278e0bc3fa35175d76426d876b8534df85cbc`
  was independently absent; owner `c16c6556-99dd-4979-b79f-6d1cb44bdca0` had
  no containers. Normal metadata DELETE succeeded.

Evidence: host `/tmp/sysbox-p9-lab-current/p9-5098f4f6620b-live-failover.json`,
control VM `/var/tmp/p9-live-failover-fast-20261010.json`.
The earlier unknown Destroy on `p9-5213226d26bb` remains retained and unresolved.
Full network partition, disk-full, upgrade and interrupted reset continuation
still require their own acceptance.

## Open: primary API switch interrupted Destroy (2026-10-10)

The intended live-Apply failover probe did not meet its timing gate: Apply
`request-b0d6521f0a2c301cd530c3bcfb5784f1f1f23285c24b4f936ba08c8d2cc492f7`
finished at `04:45:55.817Z`, before API1 was stopped at `04:46:02Z`. It must not
be counted as successful running-Apply failover. Destroy was already running
when the primary stopped, exposing a real failure instead.

Topology: `p9-5213226d26bb`; control evidence:
`/var/tmp/p9-live-failover-20261010.json`. Destroy Run:
`op-f03427da1f549134953b257295f39c8af37305770354447e78673f7d72dba0b0`.
At `04:46:04Z` the host callback was canceled and checkpoint publication failed
with `context canceled`. The original container
`7c7bb3e764fd3c36c88adc6554fd07b10bb61e020c8e7da261662acf574c54fc`
was stopped (exit137), but not removed. The canonical Destroy still reported
running when the observing client timed out. API1 was restored; both APIs and
HAProxy are active. No force cleanup, Run rewrite or new Destroy was submitted.
The original state, host intent and stopped container remain available for recovery.

AuthorityClient.Authorize previously canceled on the first transport EOF.
It now retries only transport-level failures up to three attempts, spaced
100 ms, inside the original caller context/watchdog budget. All HTTP rejection
statuses remain final, as do exhausted attempts/deadlines. Issue, renewal and
completion mutations are not retried by this change. The EOF regression failed
before the change; explicit HTTP rejection and deadline tests, full host race
suite and vet passed. Real failover revalidation and retained Destroy recovery
are still required; bounded retry alone does not close this failure.

## Real API proxy outage during a live child (2026-10-10)

On the isolated two-VM deployment, started a 600-second background Apply on
`p9-c623347655cc`. After confirming its durable child was running, stopped the
test HAProxy service. Database and SSH connectivity remained available: this
is an API-path outage, not a full network partition.

The host confirmed child termination after **1.85 seconds**. The proxy was
restored in finally; the **identical** signed stop receipt was then acknowledged.
Exit code remained unknown (`ExitCodeKnown=false`), while termination was
confirmed. The same canonical parent Run became failed with durable
`host_completion`, message `run child execution failed after confirmed termination`.
No Apply was resubmitted.

- Parent: `request-ba95686880a149182403f705825d46be46dd63e66e70554852c3a18ee8ef4b61`.
- Child: `run-task-00740891f91c14e0e052d98280f8d39b340431b05131ec098fbcc0691f134870`.
- Fault evidence: `/tmp/sysbox-p9-lab-current/p9-c623347655cc-partition.json`.
- Control VM original evidence: `/var/tmp/p9-api-outage-20261010.json`.
- Control VM cleanup/terminal proof: `/var/tmp/p9-api-outage-cleanup-20261010.json`.

Normal Destroy completed. Independent Docker inspection confirmed exact
container `a1253fbbb0185cbc4767541103d33dbac70b623717988db0133e938ee012fc2c`
absent and no containers under owner `7322fa7a-eafa-4142-9fb3-bdcbfa8e52e1`.
Normal metadata DELETE succeeded without force. This closes this API-path
interruption scenario only; full partition, controller failover, disk-full,
upgrade and interrupted reset-continuation acceptance remain separate gates.

## Real PostgreSQL outage during a live guest task (2026-10-10)

The cleanup observation loop had the same fixed-ticker starvation as normal
observation. A 100 ms slow cleanup probe reproduced a 12 ms reacquisition gap;
it now waits a fresh 50 ms after each incomplete observation. Host full race,
vet and independent review passed, then the fixed supervisor was deployed.

Started a 600-second background Apply on `p9-235c662ef0ca`. Before stopping
PostgreSQL, the fault script verified the exact parent checkpoint, live child,
provider owner/UID/generation, guest process anchors, cgroup inode and
`populated 1`. The guest stopped during the outage after **123.69 seconds**:
same process/cgroup incarnation, `status=cancelled`, termination confirmed,
`populated 0`. PostgreSQL was restored in finally, MainPID changed 2003→9474.
The original child receipt was acknowledged after restart (`reported=true`).

- Run: `request-b6670158d337d347f4c7d3228e65ae884179d00a374a5cd4421bab14c157aa9a`.
- Task: `run-task-2318b3fd25b17aef995c3fcb52d1574efd386722f9bfa6c140ff5a01677ebff7`.
- Fault evidence on host: `/tmp/sysbox-p9-lab-current/p9-235c662ef0ca-database-outage.json`.
- Original client evidence on control VM: `/var/tmp/p9-db-outage-20261010.json`.
- Cleanup and canonical terminal proof: `/var/tmp/p9-db-outage-cleanup-20261010.json`.

The observing client exited on expected HTTP503 while the database was down;
the Apply was not resubmitted. After restoration, the same canonical Run became
failed with `host_completion` and message `run controller stopped after resource
execution; child termination confirmed`. Normal Destroy then succeeded.
Independent Docker inspection confirmed container
`9757346d9cd9c1afe9571ea49d29720b4072620c57759710a8ffef4962df6623` absent,
with no resources under owner `70cff66b-9e41-439c-9198-93a9ac6f3b27`.
Normal metadata DELETE completed without force. This proves this database
outage scenario; it does not close partition, disk-full, controller replacement,
upgrade or interrupted reset-continuation acceptance.

## Resolved: slow child observations starved recovery (2026-10-10)

The fixed 100 ms ticker in `RunChildren.observe` was already readable whenever
Docker observation exceeded that interval. The watcher immediately reacquired
the task lock, starving the independent recovery loop. A regression with a
150 ms observation failed before the fix: only 17 ms elapsed before the next
locked observation. Polling now starts a fresh 100 ms timer after observation
returns and releases its lock; cancellation and renewal remain selectable.
Host supervisor full race tests, vet and independent review passed.

A 15-second diagnostic sample excluded slow Start acknowledgement (the new
slow-Start warning did not trigger) and reproduced task-lock recovery timeouts.
Its evidence `/var/tmp/p9-task-start-timing-20261010.json` was cleaned normally
after exact container absence verification.

Real fix validation used supervisor binary SHA256
`96ff95513d307f98f9585ecc45128ce04aaf8df2feabe91923810c03c1b65986`, deployed
at `2026-10-10T04:25:32Z`. Control evidence:
`/var/tmp/p9-reset-poll-fix-20261010.json`, topology `p9-6bf970dd1cca`.
The complete 45-second background Apply/Reset/Destroy passed:

- Apply `request-41f2bd609ab705dc38132531bdfa9cdacf223cd8319554dfb90aab743ccc5145`.
- Reset `51842154-12fe-4bd2-9775-3f449a0b28d2`.
- Destroy `op-f9f9e4cc186c50684ab1665795ca2815bcc41cbbe749aa0bd3810fdd8d6857bd`.

The entire supervisor journal since deployment contained no recovery timeout.
Exact containers `26daa40f9eb095a9d16e35ffb996c8ae1054510e9cc1ff605901a47015c20633`
and `3b40dad4abd5ed6518ec361d9cfc2d8c4223d269751487ee803a83cc2eec09d3`
were independently absent; owner `a4962fd7-cd60-40ce-b001-17de3ef2d727` had no
containers. Normal metadata DELETE completed. This closes the demonstrated
normal observation starvation, not all cancellation/fault-recovery behavior;
the cleanup polling loop still needs its own contention audit.

## Background recovery timeout localization (2026-10-10)

Added error provenance without changing deadlines/retry behavior: supervisor
recovery errors identify the component; task errors identify task/generation,
and initial task-lock failures retain `acquire task lock` plus the original
wrapped error. Host supervisor full race tests passed.

Deployed that diagnostic build to the execution VM only after the prior test
was cleaned up. A separate 45-second background Apply/Reset/Destroy sample
completed: control evidence `/var/tmp/p9-reset-background-diagnostic-20261010.json`,
topology `p9-821fc6e9095e`. New logs localized repeated timeouts to
`guest-tasks recovery`, specifically acquiring task
`run-task-25d92157839874f2260a4d3617480857f479508f54f1c76b07a31df2d6364862`
at generation 1. This identifies contention but does not yet establish why the
lock holder excludes recovery long enough to exhaust its budget. Docker exec
history contains repeated helper calls; no timeout increase or error suppression
has been applied. Root-cause analysis remains open.

The sample completed Destroy; both exact container IDs
`a312732d35ddb904db2e28cd60a7779ce5aeb8595c90c8bbfddfe3c3fb3c08f2` and
`6055d0a06e9f602eefc9563210d3ef5a1d2c6da7059033e3d0b060082301f4c4`
were independently absent on the execution VM, with no containers under owner
`a4559e85-0a3b-4564-a646-2b364d9c54ac`. Normal metadata DELETE completed.

## Real two-host Reset with background guest tasks (2026-10-10)

On the same isolated deployment/build as the normal reset below, built
`sysbox-p9-guest:local` (`3f781968a376`) on the execution VM using the current
static `cmd/sysbox-init` helper and this deployment's issuer public key.
No private signing key was copied into the guest image.

Ran `cross_host_run.py --reset --background --provision-seconds 45`.
Evidence: control VM `/var/tmp/p9-reset-background-20261010.json`;
topology `p9-980664a6d2e7`, topology UID
`b1ed66fb-f8fe-449b-8f7b-bf763a1f5ad3`.

- Apply `request-6a8a6a76e8a7ef4fdbc0f6dac1124ebe4ea1abe8a76372c1c6c37e3c10c9a51d` completed.
- Reset `5f3d2539-d2d3-4a17-b95d-c9fc7ac13588` completed.
- Destroy `op-5b252cc0494eb25c01cf8885ed0aa0f114fdb813da9977197202fc5a14c6d7bc` completed.

Independent host task-journal inspection confirmed both finite tasks exited
with known exit code 0, `RemoteTermination=confirmed` and `reported=true`.
Apply task: `run-task-a78a3f53701b72fb759b4e056313c5924bb1a6119f96765b5845c71d9a069b33`, generation 1, epoch 5.
Reset task: `run-task-c7c8f1d80a952f1287bd4503ddab6f6a7cafe8a4eb14c9ffd3d2941032db9ccf`, generation 2, epoch 7.
Both retained resource UID
`f4197f2eed268b19e88cdd18f6e3b1ea155d5660ed8fd086f2655a04d97bbec1`.

Execution-VM Docker independently reported `no such object` for both exact
container IDs after Destroy:
`520b646f8feb0121c90aaabed14d91359241b438419c9777bcfc413ad7eab490` and
`6fbc4b3a1931528cae3ee117a3b6e6e95e2721e6ed0e2a3685ecb9e6e60abcf9`.
Owner-UID inventory was empty. Normal metadata DELETE succeeded without force;
evidence phase is `metadata_deleted`.

An unresolved observation remains: supervisor logged repeated
`reconciliation: context deadline exceeded` during this run. The last-minute
supervisor/API logs were empty after cleanup, but that does not identify or fix
the source. Diagnose before claiming healthy fault recovery. This acceptance
proves normal remote generation-2 task routing and parent/child completion;
it does not prove interrupted continuation, partition, failover or all P9 gates.

## Real two-host Apply / Reset / Destroy (2026-10-10)

The retained lab `/tmp/sysbox-p9-lab-current` now runs build `749bc88` with binary
SHA256 `eca6dc774167c1774a8262230301d5cdb47f957036e33163854faa40662f548d`.
Both API services and PostgreSQL run on the control VM; the Agent, host
supervisor and Docker run on the execution VM. Deployment uses separate new
database/role `sysboxp9_reset_20261010` and local deployment evidence directory
`deployment-reset-20261010`. Both API services, Agent and supervisor started.
The deployment helper now binds database name to its deployment directory and
rejects an existing role or database before replacing binaries/configuration;
it never rotates an old deployment's database password. Interrupted deployment
must be inspected rather than blindly rerun. This guard was added after the
successful fresh-database deployment below; no existing database was redeployed.

Before deployment, stopped old service configuration, identities, journals and
binary were moved/copied into `/var/lib/sysbox-p9-archive-reset-20261010` on
their respective VMs. The old `sysboxp9` database remains unchanged. Its dump is
in the control archive with SHA256
`6a0fb6dae35b72d9a3caf012642fe41f7f967d73b24f10ede677d3697941f3a4`.
The old running Run was not declared stopped or removed.

Acceptance evidence: control VM `/var/tmp/p9-reset-20261010.json`.
Topology: `p9-074c49109274`.

- Apply: `request-c9dbde7ae83a515bfc992755740a3de9560af82eb084aed0d0255f0a468f78dd`, done.
- Reset: `3069f2b3-f8f7-4964-905c-1f14bda1b793`, done.
- Destroy: `op-47eb9fb6a2f10e02114c25a3a2970b311a1127884d21563f0a60018e7f7c3f86`, done.
- Stable topology UID: `27fe6313-c983-4553-ade7-33cf16065240`.
- Stable node UID: `7ba469867a19a3699c27baf22ccb0feb7c6bf4b7444b6942cc15641346303b02`.
- Generation advanced from 1 to 2; external container identity changed from
  `750193b5511d6be378efc14c00ad51fef6cf7b348bd2b0551dfd53ca6a0c5b32` to
  `71dcbfc082d88d40cc6bbe8a0a57bff0f881659e138b3cf661bcf513b58a2691`.

After Destroy, API resources were empty. Independent execution-VM `docker
inspect` returned `no such object` for both exact IDs; owner-UID inventory was
also empty. Normal metadata DELETE then succeeded without force. The evidence
phase is `metadata_deleted`. This validates normal remote Docker reset through
the host execution protocol, not fault continuation, background child tasks,
Firecracker/libvirt, or the remaining P9 fault matrix. Both VMs and new services
remain running for continued acceptance.

## Retained two-VM lab restart (2026-10-10)

`scripts/acceptance/two_vms.py start --root /tmp/sysbox-p9-lab-current`
now restarts existing disks without recreating keys, overlays or cloud-init
seeds. It checks both old PIDs/monitors before launch, rejects uncertain/live
processes, serializes starters and retains stale process files and separate
restart serial logs. A partial launch preserves both disks and reports failure;
it must not be retried blindly while the first guest is running.

Real restart succeeded. `check` verified independent guest filesystems,
private-network connectivity and nested KVM. Control boot ID:
`aaff8020-1de1-4af8-8a7c-0941b5dc0d81`; execution boot ID:
`5ca09ca7-b83e-41c1-aedd-c1937d95d1c0`. Both run kernel `6.8.0-124-generic`.
The restarted serial logs use prefix `restart-bbc0c56b2cc6`.

Sysbox API1/API2, Agent and host supervisor remain inactive. PostgreSQL, HAProxy
and Docker are active; Docker listed no containers. Read-only SQL inspection
found API schema 14 and two lifecycle format4 aggregates. The original Run
`request-791bf2ad72aa0f6fbd4c31ab2bfcc5799b4c26d3efea14a7eb22d9bfacc1eab1`
for topology `p9-86774be76235` still reports running. No terminal result was
invented and no record was changed. This is lab availability evidence only,
not lifecycle/upgrade/reset acceptance. Preserve the old database and host
journals before deploying current code into a separate acceptance scope.

## Open reset recovery failure (2026-10-10)

Reset now publishes a separate new-generation `replacement` before guest route
commands, provisioners and image entry. The old candidate remains intact until
setup finishes. Host task resolution can use the exact applying replacement;
it still verifies the real provider identity and does not permit cold replay.
File/SQLite/PostgreSQL identity constraints and publication-failure tests pass,
as do related full race suites. Review found route execution initially preceded
publication; a failing route-call regression demonstrated and fixed that order.
Guest-task resolution now maps a canonical continuation execution to its
original action Run only after matching revision, signed parent stop evidence
and committed continuation receipt. Worker requests cannot supply the mapping.
Lookup, connection and cold dispatcher cancellation share that resolver. File,
SQLite and real PostgreSQL Run-store tests, cold cancellation, related full race
suites and review passed. Multi-level parent traversal now verifies every
predecessor independently before resolving the root action Run; tests reject
cycles, changed revisions, active parents and missing intermediate return/host
proofs. Local persisted return-chain tests passed. Full repeated-failure executor
integration and real two-host guest-task continuation remain unverified.
These are not real VM continuation acceptance.

Returned host reset failures now have a restricted signing path: after the
synchronous executor returns, reacquire execution exclusion, read the original
Run's action incarnation/phases and prove all children stopped. Only known safe
reset phases produce recoverable signed failure evidence. Unknown phases,
missing actions, foreign incarnation and unknown child admission stay uncertain.
The HostRunExecutor-to-RunCoordinator integration test covers a real file
repository and confirms safe failure signing versus preparing-phase refusal;
no VM provider is exercised. Host/Agent/API/runtime full race tests passed.
Cold process-exit proof and the retained heterogeneous incident remain open.

The API now stores `Run.host_completion` (signed receipt plus exact original
result bytes) atomically with the terminal Run. Ordinary Save/Restart cannot
inject or change the proof. The reset continuation verifier rechecks its host
signature, original Run incarnation/attempt/lease/result, current topology UID
and matching historical authority stop tombstone. Active/revoked grants are
rejected; a successor epoch does not erase historical stop evidence. File,
SQLite and real PostgreSQL 16 completion/verifier tests passed, including JSONB
round trips. Related full race suites and independent review passed. This is
not yet a real two-host reset continuation E2E, nor recovery of the old incident;
old Runs without signed proof still cannot pass this verifier.

Host execution now accepts `run_reset` through the same grant, async callback,
effect journal, derived task and signed completion paths as apply/destroy.
Tests verify exact signed receipt recovery without callback replay and refusal
to replay unknown intents. Completion contract tests passed for all three
operations on file, SQLite and real PostgreSQL 16, including active-child
rejection, tampered evidence, expired/retired commands and successor-epoch
protection. These protocol results alone do not authorize continuation of the
retained incident.

Runtime continuation now prepares original-incarnation intents, reuses durable
receipts after restart and admits them before entering reset phase CAS. Real
file/SQLite/PostgreSQL repository integration tests model two destroyed nodes
and one missing intent, then retry from a new executor: each node is destroyed
and recreated only once, original action identity/generation is preserved, and
the original checkpoint bytes remain unchanged. Providers in this test are
simulated. Runtime/repository/Agent/CLI race regressions passed. API reset
resume now requires a validated complete parent checkpoint before creating a
child Run. Execution additionally requires a `ResetContinuationVerifier`; the
API bridge requires signed host-stop verification as described above. CLI
exposes `reset --resume <run-id>` only with a durable synchronous
return receipt matching the original checkpoint hash and action incarnation.
It rejects `--target` overrides and confirms the complete original scope.
Old/crashed executors without that receipt cannot resume through this path.
Agent/API/CLI full race regressions, targeted missing-receipt/changed-checkpoint/
target-override rejection tests, vet and independent review passed. This does
not prove end-to-end CLI continuation of the retained incident.
Real two-host continuation acceptance and retained-VM recovery are still open.

The continuation admission primitive now records the complete original request
and missing intents atomically in aggregate format 5. Normal repository access
rejects format 4. The retained heterogeneous incident has not been imported or
modified. An explicit format4 import preview, run with a read-only container,
read-only source mount and disabled network, validated topology UID
`597da2ee-b1f7-48b9-8d9a-afa9e351aaaf`, serial 8, 10 actions and four snapshots.
The source SHA256 was
`9d9b2552617772abc7d0830efad5ade52d41e3bf465f003debf315a9da6dac55`.
The final preview returned the same digest. The explicit importer passed real
file/SQLite/PostgreSQL 16 tests for atomic source comparison, durable original
archives, archive failure rollback and snapshot conversion, plus CLI preview
and apply tests on disposable fixtures. Full persistence/Agent/API/CLI race
regressions, vet and independent review passed. Neither preview imported the
retained incident. Its two pending reset records retain identical queued-at
`2026-10-09T22:53:55.414519496Z` and no Run revision (local CLI provenance);
do not invent a revision during continuation.
The continuation execution entry point remains unfinished. See
[reset continuation](../design/2026-10-10-reset-continuation.md).

CLI and Agent lifecycle execution now acquires a repository-scoped execution
lock before reading workspace/state, independent of aggregate transaction
locks. File/SQLite use persistent sidecar lock files; PostgreSQL uses a dedicated
connection and an advisory key derived from the resolved aggregate table OID
and topology. Lock waits can be canceled. Tests cover independent processes,
SQLite symlink aliases, PostgreSQL search-path aliases, transaction access while
holding execution ownership, and cancellation before lifecycle workspace access.
Full persistence, agentexec, API and CLI race suites passed with a real disposable
PostgreSQL 16; vet and independent review passed. See
[execution exclusion](../design/2026-10-10-execution-exclusion.md) for path and
connection-loss limits. This is cooperating-executor exclusion, not proof of
provider quiescence; management mutations and old binaries do not all
participate. Explicit continuation and the retained reset matrix remain open.

Runtime reset no longer implicitly takes over another Run's pending actions.
Pending actions must belong to the current Run or an explicitly selected
resume Run; an absent or unrelated resume ID fails before preparation,
destruction or creation. Regression tests demonstrate the previous implicit
takeover and verify that rejection preserves both action records and provider
state. Explicit cold recovery tests still preserve original action identities.
Selecting a resume ID is not authorization: the bridge must validate the
original checkpoint, Run incarnation and exclusive execution ownership before
granting continuation. That complete entry point remains unfinished.

Reset batch retries now validate the complete admitted action ID set inside
the aggregate transaction. Reordering is allowed; omission, replacement and
expansion are rejected. File, SQLite and real PostgreSQL 16 race tests verify
these boundaries. The runtime and agentexec package race suites also pass.
This prevents ordinary retry from repairing a historical partial batch;
explicit continuation admission and execution ownership remain required before
the retained heterogeneous reset can be recovered safely.

The heterogeneous lifecycle matrix passed, but the subsequent reset matrix
failed with a missing Firecracker manifest and a supposedly live PID. Commit
`b9f77bf` fixes the demonstrated zombie false-positive; Firecracker package race
tests pass, including live-process and mismatched-start-time rejection. This is
not yet proof that the heterogeneous reset failure is resolved.

The original repository remains at
`/tmp/sysbox-e2e/heterogeneous-matrix/state.json.lifecycle`. Read-only inspection
found uncertain resets for both Firecracker and libvirt; original Firecracker
PID 2050769 was absent at inspection. A real recovery attempt using the original
privileged Docker mounts, retained SSH key and baseline image passed filesystem
access but failed with `repository conflict: unresolved action
a09090bbaf825174ddfac1e9b462189c`. Its snapshot is
`bec96f3d-45c5-4395-85b6-d97e66311b24`.

The original checkpoint `runs/792e8523-6e61-497e-9cb6-26948ac80822.checkpoint.json`
contains all three node resets, state serial 8 and plan digest
`sha256:f35e80a3d216905593a1e3b587f46fd64a8867e8113d38b75535151db5508c49`.
Only libvirt and Firecracker have durable intents, both in phase `destroyed`;
Docker was not started. This is a partial-intent publication window, not a
completed Docker action being replayed.

The runtime can consume pending reset actions, but a new CLI reset also tries
to create the missing Docker action under a different Run ID. Repository
exclusion correctly rejects this while the original Run remains unfinished.
Recovery must validate the original complete plan and bind any not-yet-started
action to an explicitly authorized continuation. The unused LocalBridge
ResumeResetPlan hook alone does not explain or fix this failure. No journal was edited,
no unknown resource was forcibly deleted, and no successful cleanup or reset
matrix pass is claimed. This remains required work for the P0–P9 goal.

The next runtime change publishes all new reset intents before calling any
provider prepare/destroy. A failed second intent write now leaves every provider
untouched, verified by a regression that failed with the original ordering.
An intent starts in `accepted`; before prepare, a durable CAS advances it to
`preparing`. Cold recovery may execute an `accepted` action using the same ID,
but still refuses `preparing`, whose effect outcome is unknown. Runtime and
persistence package race tests passed; PostgreSQL was not enabled in this run.
This does not yet resolve admission of missing intents under a different Run
in the retained old matrix, nor the overall reset recovery workflow.

Independent review of `f940907` also identifies required work for current-format
records: sequential intent publication can leave a partial accepted batch that
cannot admit missing actions under a new Run; prepared recovery has no competing
writer CAS before destroy; repository phase validation rejects regressions but
does not yet enforce all legal initial/transition combinations. These are open
correctness issues, not merely missing fault-matrix evidence.

Reset resume plan validation now checks both input plans before filtering
completed steps, rejecting duplicate addresses and non-reset actions. All three
regressions failed before the change and runtime package race passed afterwards.
This input validation fix does not close the three recovery issues above.

Reset admission now requires `ResetIntentCommitter.CommitResetIntents`: all new
actions for one Run share one aggregate transaction. A failed second action
validation publishes no intents (filesystem and SQLite tested); exact batch
replay is idempotent. A missing atomic admission capability rejects reset before
provider work instead of reverting to sequential writes. Read-only recovery
wrappers do not expose the capability. Runtime two-node cold recovery retains
both original action IDs, Run IDs and generations after interruption before
prepare; that orchestration test uses the in-memory repository.

Runtime, persistence and agentexec full package race tests and vet passed, and
independent read-only review found no new blocker. PostgreSQL batch admission
was not exercised in this run. This addresses new-batch partial publication;
the retained old partial batch, competing prepared destroy recovery and strict
repository phase transition constraints remain open.

The shared repository now rejects new reset intents unless they start in
`accepted` without effect-complete evidence. Reset outcomes may remain in the
same phase or advance exactly one phase; `verifying` requires effect-complete
evidence, and earlier phases cannot claim it. Filesystem/SQLite tests cover all
invalid initial phases, skipped phases, inconsistent completion flags, phase
regression and the full legal sequence. Runtime/persistence package race and
vet passed. This does not establish exclusive ownership of an in-flight
destroy callback; that execution-boundary issue remains open, along with the
retained old partial batch and the real reset matrix recovery.

Reset now persists `prepared -> destroying` using the action revision CAS before
attachment deletion or `DestroyReset`. A failed claim performs no destroy;
filesystem/SQLite tests reject a second claimant using the same prepared
revision. `destroying -> destroyed` records completion of the owned callback.
Cold recovery of `destroying` remains uncertain and never repeats destruction:
resource absence alone is not proof that the original callback has returned.
The legal phase order and tests include this new stage. Runtime/persistence
race and vet passed. This closes duplicate destroy dispatch from the same
prepared revision, but durable proof of callback exit and recovery of an
interrupted destroying stage remain required for the full goal.

A subsequent `destroy_returned` checkpoint records successful callback return
before the absence observation is committed. If the `destroyed` write fails,
cold recovery from this checkpoint observes the original generation and does
not repeat destruction. A regression injects that write failure and verifies
exactly one destroy call across reconstruction. If callback-return evidence
itself was not durably committed, `destroying` still remains unknown; this
change does not manufacture exit evidence for a crashed controller. The phase
contract tests include `destroy_returned`; runtime/persistence race passed.

The reset admission/phase contract now also runs against real PostgreSQL 16 in
an isolated schema per test. All filesystem, SQLite and PostgreSQL subtests
passed under race: invalid second-item admission leaves no intents, exact batch
retry preserves the two intents, and two concurrent transactions using the same
prepared revision have exactly one successful destroy claim. Invalid initial
phases, skipped phases, regressions and contradictory completion flags are
rejected. The dedicated disposable database is separate from the VM labs.
These are repository transaction tests, not a successful recovery of the
retained heterogeneous provider matrix.

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
topology, blocks restore, force-unlock and other state maintenance. Follow-up
verification found the first predicate incorrectly allowed terminal statuses
without an end timestamp, and `lost` with a restart timestamp. Both are now
rejected by one shared check used for maintenance and metadata deletion. The
console registry loader is required rather than silently skipped.
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

Correction to the raw Run writer checkpoint: `fa89866` added sequential checks
but did not hold the file authority lock through append, and PostgreSQL acquired
authority after the Run row. Its test run did not enable PostgreSQL. Its `{}`
authority exemption and invented `legacy` UID were unsafe; the earlier claim of
complete three-backend fencing was not supported by that implementation.

The corrected implementation holds file authority through lifecycle observation
and Run append; SQLite retains its write transaction; PostgreSQL materializes
and locks authority before Run/command rows. Active writes validate complete
authority and lifecycle records through one shared persistence function. An
uninitialized lifecycle is allowed only when no maintenance barrier is active;
malformed records never become an empty authority. Existing unrelated topology
UIDs remain independent. Terminal Run updates bypass admission freeze so work
can finish, while restarting to queued remains blocked.

Full API race also exposed per-request writes to activity loader callbacks from
the console/command changes. Those callbacks are now initialized only with the
service. Commands are enumerated without filtering by registered Agents, so a
missing Agent registration cannot hide durable work. None of these changes
establish atomicity for every registry writer or complete P6/P8.

Verification of the correction: API and persistence full race suites passed
with both PostgreSQL test variables enabled (55.54 and 3.95 seconds). The file
lock regression blocks the first actual Run append and observes authority held
until append resumes; 20 repeated runs passed. An earlier lifecycle-lock test
was replaced because it no longer blocked the implementation's read path.
Tests also cover terminal reporting while frozen, restart rejection/release,
corrupt authority, unknown lifecycle during maintenance, and unrelated owners.
No legacy UID or `{}` exemption remains.

Final full Go regression with API and persistence PostgreSQL integration enabled,
vet, docscheck and independent re-review passed. The test-only Postgres container
was stopped and automatically removed. No real provider effect was exercised in
this correction round; the database/race evidence does not replace P9 acceptance.

### Lost console evidence is not completion (2026-10-09)

API restart changes queued/running consoles to `lost` and records an end time
for the broken connection. That timestamp does not prove a remote stop. A real
store/hub regression reproduces the transition and requires restore quiescence
and forced metadata deletion to remain blocked. Known terminal statuses require
creation/end timestamps in order; unknown and lost statuses always remain
unresolved. An unrelated topology in the same workspace does not block this one.

This is an activity-selection correction, not a complete console recovery or
signed-stop protocol. Raw console status writes still need formal authority
binding, and console admission must join the shared maintenance transaction.
P6/P8 and the complete P9 acceptance therefore remain open.

Validation: full API race passed with real PostgreSQL enabled (54.26 seconds),
alongside file/SQLite. Extended status/time tests passed under race, including
queued/running with populated times and missing creation time. Vet, docscheck
and independent review passed. The temporary PostgreSQL container was stopped
and automatically removed. No provider effects were performed in this round.

### Cleanup inspection includes console and command blockers (2026-10-09)

The diagnostic endpoint now consumes the same unresolved-console predicate used
by maintenance, and enumerates all commands rather than only registered Agents.
Lost consoles and pending commands carrying the current lifecycle UID appear in
`active_operations`; foreign topology UIDs are excluded. Agent IDs are included
without exposing command arguments, environment values or console error/audit
content. Failed enumeration returns a redacted 503 rather than an empty list.

Existing cleanup receipts remain visible as historical evidence, but their status
becomes `blocked_by_console_activity` or `blocked_by_agent_command` when these
additional blockers exist. This is diagnostic coverage, not a cross-registry
transaction or deletion authorization. It does not repair missing stop protocols
or enable GC. P0–P9 remain unfinished.

Verification: the full API race suite passed with real PostgreSQL enabled
(57.60 seconds), including file/SQLite coverage. Vet, docscheck and independent
review passed. The temporary test database was stopped and automatically removed;
this round did not execute provider effects.

### Command preflight shared by maintenance and metadata deletion (2026-10-09)

Missing AgentCommand enumeration is now an incomplete registry error, matching
the other required maintenance sources. The same command preflight is used for
restore/force-unlock and before any metadata Delete mutation, including force.
Previously a pending command could block maintenance yet be omitted by Delete.
The regression reproduced deletion succeeding and checks that a rejected delete
preserves the original topology UID. Registry read failures also prevent deletion.

Tests cover unregistered Agents, command retirement, and foreign topology UIDs.
The helper loads lifecycle identity once with the request context and does not
infer quiescence from command lease expiry. This remains a preflight snapshot:
it does not serialize every command writer with lifecycle deletion, and cannot
substitute for host stop evidence or the pending cross-registry admission work.

Validation: full API race passed with real PostgreSQL enabled (101.36 seconds).
The publication/deletion concurrency regression exposed an unnecessary lifecycle
read for empty command lists; the helper now resolves UID only when a pending
command exists. Targeted race, vet, docscheck and independent review passed.
The temporary database was stopped and automatically removed after verification.

### Incomplete command identity remains a blocker (2026-10-09)

Pending commands without topology UID were omitted even when their durable
payload explicitly named the topology. Maintenance/delete preflight and cleanup
inspection now share a predicate: explicit UIDs match only that incarnation;
missing UID plus a matching Run, node operation, guest execution, file or console
payload name adds a blocker. This never fills an identity field, binds an old
command to a successor, or permits execution. Terminal commands remain excluded.

Regression tests reproduced all five missing-UID cases and protect unrelated
topology, explicit old UID and terminal behavior. This is conservative evidence
handling; incomplete records still require explicit resolution/rebuild. It does
not implement atomic command admission or recover unknown remote effects.

Validation: full API race passed (62.65 seconds), including file/SQLite; no
PostgreSQL instance was enabled in this round. HTTP regressions verify activity
visibility and `blocked_by_agent_command` for missing UID records, with the
historical receipt retained. Targeted race, vet, docscheck and independent review
passed. No external resource or database mutation was performed for acceptance.

### Console cancellation records intent rather than stop (2026-10-09)

The API cancellation request previously wrote `cancelled` with an end timestamp
before any remote confirmation. It now persists `cancelling` with no stop time,
then updates in-memory state and signals the relay. Failed persistence returns
an error without changing state or signalling; retries do not duplicate the
cancellation audit. Restart leaves the outcome unresolved (`lost`). Late attach
or started events do not overwrite cancelling with running.

Websocket relay end/attach timeout now record transport loss instead of inventing
closed/failed execution. Already reported terminal results are preserved. Update
and Cancel persistence are serialized within the hub; snapshots are detached
copies read under lock, and Agent attach reads identity from that snapshot.
The delayed-write regression ensures an earlier running update cannot overwrite
a later cancellation. This locking is process-local; multi-API CAS, durable cancel
delivery and host-signed console completion are still outstanding. Existing
worker completion events remain the current terminal-report path, not a claim of
verified remote termination. P0–P9 remain open.

Verification: full Go regression and full API race (63.18 seconds) passed, as
did vet, docscheck and independent re-review. Regressions cover persistence
failure, cancellation retries, restart, late started events, terminal reporting,
transport-loss classification, delayed writes and detached snapshots. PostgreSQL
and real provider acceptance were not run for this in-process state correction.

### Cancellation dispatch failures stay visible (2026-10-09)

ConsoleService no longer discards cancel-command publication failures. It retains
the persisted cancelling session and returns the error; a later call for the same
session retries dispatch without adding a second cancellation audit event. HTTP
returns 404 only for an actually missing session, and a redacted 503 for storage
or publication failures, advising retry of the same session. No internal dispatch
error or connection detail is exposed.

Regression tests reproduce the ignored publication error and incorrect 404
mapping, then verify retained intent, successful retry and unchanged audit count.
This does not provide a durable cancellation outbox, exactly-once delivery or a
remote stop acknowledgment. Those remain part of the full console migration.

Verification: full API race passed (62.68 seconds), along with targeted HTTP
storage-failure and dispatch-failure cases, vet, docscheck and independent review.
PostgreSQL and real provider acceptance were not run in this error-routing slice.

### Console creation requires persistence (2026-10-09)

The console hub previously published its in-memory session and discarded the
storage error. Create now accepts the request context, requires configured
persistence, and saves the session before exposing it in the hub. The service
stops immediately on a storage failure, so it cannot publish `session_open` for
that failed creation. HTTP returns a redacted 503 without a session ID.

Tests verify failed saves create no hub entry or command, cancelled contexts and
missing stores reject, and a successful returned identity can be read from the
store. This does not make session creation and command outbox one transaction;
a crash after session save can still leave queued evidence without dispatch.
Cross-process console CAS and atomic session/outbox admission remain unfinished.

Verification: full API race passed (63.96 seconds), together with targeted
create/cancel tests, vet, docscheck and independent review. File and SQLite were
exercised; the PostgreSQL test branch was not enabled in this round. No real
provider effects were performed.

### Console updates publish only committed state (2026-10-09)

Hub Update now applies callbacks to a detached session copy and saves it before
replacing the in-memory record. It returns missing-session/storage errors instead
of discarding them. Audit, roles and exit-code pointer copies prevent a failed
callback update from leaking through shared memory. The creation workflow stops
before session-open dispatch if its policy/audit or node-rejection update fails.
Agent command-event handling returns console update errors to its caller's
logging path (not a durable Agent retry acknowledgment), and websocket
attach closes on persistence failure; relay cleanup logs failed loss records.

Regressions verify failed saves preserve prior in-memory/audit state, prevent
dispatch, and do not expose uncommitted completion. Command event and console
record writes are still separate transactions: reporting an error does not roll
back an already persisted command event. Restart reconciliation, cross-process
CAS and full signed console outcomes remain part of the unfinished migration.

In particular, terminal command events retire the command before session update;
current replay rejects retired commands. A failed session completion save can
therefore leave unresolved console evidence until an atomic completion/recovery
protocol is implemented. Browser audit now records an attachment request before
channel admission, avoiding a false success event for rejected duplicate peers.

Verification: full API race passed (63.18 seconds), plus targeted failed-save,
no-dispatch and completion-error tests, vet and docscheck. Independent review
identified the premature browser success audit and it was corrected to request
evidence. PostgreSQL and real provider acceptance were not run in this slice.

### Recover console completion after command retirement (2026-10-09)

An accepted terminal `session_open` command is now retained as replayable
completion evidence. Exact retries validate Agent, command type, status/error,
lease owner, attempt and resource proof against the stored outcome, without
unretiring or writing the command. Other command protocols retain their previous
retirement rules. Retry timestamps/message text cannot replace the accepted
outcome; the session uses the original command end time and error.

Supervisor scanning and direct terminal-event replay share the same recovery
path. It reads durable command records including retired outcomes, rejects
ambiguous multiple commands for one session, and verifies Agent, topology, node
and creation time against the session. It repairs unresolved session metadata
without publishing or executing another command. Retirement alone without a
terminal result remains unresolved. Successful recovery clears the prior lost
transport diagnostic.

Three-store regressions inject a session-write failure after command retirement,
rebuild the hub, run reconciliation and verify repaired completion plus stable
historical timestamps. Altered replay proofs are rejected; duplicate mappings
and changed session identity are rejected through both scan and direct replay.
This repairs the retired-command/session-write window noted above; it is not an
atomic two-record commit or a signed host-stop protocol. Console CAS across API
processes, reliable cancellation outbox and unknown-result recovery remain open.

Verification: API/persistence full race passed with real PostgreSQL enabled
(55.93/3.62 seconds); after review fixes, the shared-path recovery regressions
passed under race on all three stores and full Go regression passed with both
database test variables enabled. Vet, docscheck and independent re-review passed.
The temporary Postgres container was stopped and automatically removed. No real
provider or cross-host console execution was run in this recovery slice.

### Console startup requires complete durable evidence (2026-10-09)

Console registry initialization no longer treats a failed session listing as
an empty registry or exposes an uncommitted lost-state update. It builds its
in-memory map only after loading and saving startup observations successfully.
Initialization errors are retained; hub operations reject them and API startup
returns the failure before listening. Existing evidence is not deleted or
automatically rebuilt. Earlier successfully written lost observations remain
durable if a later record fails; a restart can retry initialization.

Tests cover unavailable enumeration, failed startup saves and a corrupt local
console file preventing API startup without changing its bytes. This does not
introduce cross-process console CAS: concurrent API startup/liveness ownership
and automatically retrying a failed registry initialization remain unresolved.

Verification: full API race passed (36.89 seconds), along with startup-specific
tests, vet, docscheck and independent review. PostgreSQL and real provider E2E
were not enabled in this startup error-handling round.

### Console cancellation uses one immutable outbox identity (2026-10-09)

Console cancellation retries previously created random command IDs and duplicate
deliveries. The command ID now hashes the session's immutable ID, Agent, topology,
node and creation time; mutable status/audit fields are excluded. Its durable
payload carries that same target identity. A shared repository Enqueue operation
inserts a queued command or returns the exact matching live record without
changing delivery state, lease owner, deadline or attempt. Changed payloads and
retired records conflict rather than restarting delivery.

The production publisher uses Enqueue for console cancellation and retains the
existing behavior for other commands. Three-backend tests cover retries after
lease acquisition, cold hub reconstruction and retirement. A storage commit
failure cannot return a speculative command. Once committed, the existing outbox
can enumerate the cancellation; this change does not atomically publish the
session cancellation intent and command together. A crash before first enqueue,
automatic repair of that gap, and verified remote stopping remain unfinished.

Verification: API/persistence full race passed with both PostgreSQL test variables
enabled (57.11/3.73 seconds); file/SQLite/Postgres hot/cold retries all retained
one command and its lease. Vet and docscheck passed. The temporary PostgreSQL
container was stopped and automatically removed; no real provider effect ran.

Independent review found PostgreSQL's absent-row first-insert race could return
a spurious conflict. Enqueue now re-enters a new transaction once on conflict and
compares the same immutable payload; changed/retired records still reject. After
that fix, eight-way first enqueue, hot/cold retries and repository failure cases
passed five repeated race runs across file/SQLite/real Postgres. Re-review found
no remaining issue in this slice. The second isolated verification database was
also stopped and automatically removed.

### Repair cancellation intent before first enqueue (2026-10-09)

Restart now preserves durable `cancelling` status rather than replacing it with
transport-loss status. Supervisor scans cancellation intents before ordinary
outbox delivery and inserts their missing stable cancel command. Existing records
are checked for matching Agent/type/session identity and left unchanged, including
leased commands. Explicitly retired tombstones may have scrubbed Session payload;
their stable ID remains reserved and is never recreated or interpreted as stop
evidence. Per-record failures are reported while other intents continue.

Cold-restart regressions exercise intent saved without command publication, then
repeated scans producing one outbox record. They also preserve retirement,
reject conflicting bindings without overwriting them, and confirm plain `lost`
transport does not invent a cancellation request. This closes the missing-first-
enqueue recovery window for persisted cancelling records. It is a recoverable
two-step workflow, not a transaction spanning session and command; multi-API
session CAS and verified remote stopping remain unfinished.

Verification: full API race passed with real PostgreSQL enabled (57.49 seconds),
and targeted three-store recovery tests additionally verify an existing delivery
lease remains unchanged. Vet, docscheck and independent review passed. The
temporary PostgreSQL container was stopped and automatically removed. Tests
reconstruct API hubs; no cross-host console process execution was performed.

### Complete SQLite console records, API schema 10 (2026-10-09)

The SQLite console adapter dropped ProjectID/Workspace and did not update
StartedAt/Audit on conflict, unlike file and PostgreSQL. A full-record regression
reproduced the difference. SQLite now stores `id/data` JSON snapshots, decodes
all fields together, checks the payload ID against the row key and fails on
malformed/null records instead of ignoring roles/audit/time decoding errors.
Listing honors Workspace, falling back to Topology only when Workspace is empty.

The shared API SQL schema version is now 10. Existing SQLite/Postgres schema 9
databases require explicit rebuild, even though PostgreSQL DDL is unchanged;
no live business database was migrated. File console serialization is unchanged.
Tests exercise complete updates and workspace filtering across stores,
corrupted SQLite payloads and schema-9 rejection. This establishes consistent
record content, not cross-process CAS or atomic console outbox admission.

Verification: full API race with real PostgreSQL passed (57.43 seconds); the
final full Go regression passed with both database test variables enabled.
Vet, docscheck and independent review passed. Additional startup validation
rejects a current-version SQLite schema lacking the full-record column. The
temporary Postgres database was stopped and automatically removed; no existing
deployment was rebuilt or upgraded as part of this test.

### Console full-record CAS (2026-10-09)

File, SQLite and PostgreSQL now reject blind session overwrites. Creation is
exact-idempotent; updates compare the complete expected record and preserve
session identity, including the serialized creation-time offset used by the
cancellation-command digest. Missing CAS targets consistently return conflict.
Independent hubs refresh identity-checked snapshots after conflicts without
replaying callbacks. Startup recovery rechecks active observations with bounded
CAS retries and adopts already-progressed lost/cancelling/terminal observations.

Regressions cover one concurrent winner, immutable identity, create retries,
stale Update and Cancel, retry without duplicate audit, and competing startup
observations. Initial full API race with real PostgreSQL passed (58.373 seconds);
after review fixes, Console race passed with real PostgreSQL (4.432 seconds).
These are persistence/API tests, not cross-host console execution acceptance.
Atomic admission and verified remote stopping remain open.

The final full Go suite passed with both database test variables enabled against
the isolated PostgreSQL instance. API/persistence vet, docscheck (53 canonical
documents), diff checks and independent follow-up review passed. No production
or retained two-VM data was migrated. Sustained startup contention beyond three
CAS attempts still fails initialization explicitly and requires a retry.

### Console admission under maintenance (2026-10-09)

Raw session create/CAS now serialize with maintenance authority in all three
stores. A shared ConsoleRepository owns create/idempotency/CAS and admission
classification; adapters hold the existing file locks or SQL transaction until
publication. New records and transitions that introduce unresolved activity
cannot enter a frozen topology. Existing lost/cancelling observations, valid
completion and exact retries can drain. Lifecycle identity comes from the full
aggregate; missing lifecycle plus any live barrier conservatively rejects new
activity. Cleanup and recovery use the same completion-evidence predicate.

Console race passed with real PostgreSQL (5.740 seconds), and the full Go suite
passed with both database test variables enabled. A subsequent deterministic
three-store test held the first authority transaction before commit: admission
timed out without publishing, then rejected against the committed barrier
(race test 6.634 seconds). SQLite returned after its approximately five-second
busy wait despite a 150ms context deadline; this test proves exclusion and no
partial publication, not a strict SQLite response-time bound. Session/outbox
atomic publication and cross-host console stop evidence remain open.

Final full API race passed with real PostgreSQL (65.558 seconds), including
shared ID/topology/workspace validation and the deterministic contention test.
API/persistence vet, docscheck and independent follow-up review passed. The
isolated database was used only for this slice; existing project and retained
two-VM data were not rebuilt.

### Atomic initial console admission (2026-10-09)

Normal HTTP creation prepares policy/health checks and audit before publishing
a session and its initial session_open command together. File storage commits
the immutable dispatch seed within the session record; SQL inserts session and
outbox rows in one transaction. CAS preserves the file seed, and mutable
command records/tombstones take precedence. Notification is a postcommit wake
hint: failure still returns 202, with durable outbox delivery available.
Standalone AgentService publication of session_open is rejected.

Tests cover complete request round-trip, command collision rollback, frozen
admission, concurrent admissions sharing one command ID, seed preservation on
hub reconstruction/CAS, and retirement without resurrection. The HTTP wake
test inspects both durable records before injecting notification failure.
A pre-existing identity spoof was reproduced (viewer plus console role in the
body returned 202), then fixed: authorization and persisted audit/request now
use the server request subject; the same request returns 403 with no outbox.

Initial full API race with real PostgreSQL passed (67.464 seconds); after the
identity fix and expanded coverage, Console race passed (12.668 seconds).
These tests exercise stores and API reconstruction, not cross-host console
execution or a killed API process. HTTP idempotency-key admission, atomic
cancellation, host ownership and signed remote stop remain open.

The final full Go suite passed with both PostgreSQL test variables enabled.
API/persistence vet, docscheck (53 canonical documents), diff checks and
independent follow-up review passed. No retained deployment was rebuilt or
upgraded; the dedicated PostgreSQL container was used only for these tests.

### Atomic console cancellation (2026-10-09)

Shared ConsoleRepository.Cancel now commits session CAS, cancellation audit
and the stable cancel_command in one transaction. File records preserve both
opening and cancellation seeds; mutable command records and retirement
tombstones retain precedence. Shared enqueue validation keeps delivery leases
unchanged and rejects conflicting/retired records. HTTP and timeout use the
same method; the old standalone cancellation enqueue adapter was removed.
Repair of incomplete registry evidence also uses the atomic method.

Regressions reproduced cancellation intent surviving an outbox conflict, and
timeout/hub cancellation lacking an outbox. These now preserve neither change
on failure and publish both on success. Further coverage verifies cold retry,
lease retention, both seeds surviving CAS, retirement, canceled request context,
and notification failure after commit. Review found a failed repair could still
signal the local relay during cache refresh; the failing regression was added
and refresh now has no cancellation side effect.

Real PostgreSQL API/persistence race passed (67.397/3.676 seconds). Final Console
race after the review fix passed (13.798 seconds). HTTP 200 confirms durable
cancelling only: remote stopping remains unverified without completion/stop
evidence. HTTP creation idempotency, complete deletion transactions and the
cross-host console execution/stop protocol remain open.

The final full Go regression passed with both database variables enabled.
API/persistence vet, docscheck (53 canonical documents), diff checks and
independent follow-up review passed. Tests used an isolated PostgreSQL database;
no existing project, historical resource or retained VM was rebuilt.

### Console access-policy enforcement (2026-10-09)

Existing-session GET, cancellation and browser attachment now evaluate the
configured Console role policy before disclosure, mutation or upgrade. Tests
reproduced viewer access returning 200 and cancellation audit omitting the
acting subject's role. Denied requests now return 403 without changing session,
audit or outbox; console/admin roles pass policy. Missing authorization fails
closed, and registry failures return redacted 503 rather than 404/internal text.

Full API race passed (44.737 seconds); API vet, diff checks and independent
review passed. This API-only slice did not rerun real PostgreSQL or cross-host
acceptance and changes no persistence format. Trusted-header authentication
and continuous authorization of already-open connections remain separate
boundaries; this test does not establish them.

### Run/Console checks inside metadata deletion (2026-10-09)

Deletion re-reads latest Run and complete Console records while holding the
existing authority exclusion through lifecycle commit. Regressions first passed
preflight, then admitted a queued Run or lost Console in a different workspace:
the old commit path incorrectly deleted metadata. The new path returns conflict
and retains UID/serial. Known terminal latest records and unrelated topology
activity do not block deletion. Run completion requires a known terminal status,
nonzero queue/end times and non-reversed timestamps, shared with preflight.

Review identified that the first SQLite implementation bypassed indexed-column
validation. A corruption-after-preflight regression reproduced deletion despite
LoadRuns rejecting the record; commit now reuses scanSQLiteRun and returns
ErrCorruptRecord without changing lifecycle identity. Initial API race also
exposed three fixtures containing incomplete terminal times; those fixtures now
provide valid completion evidence without weakening the production checks.

Targeted deletion race passed with real PostgreSQL (2.779 seconds). This covers
Run/Console writers already serialized with authority; raw command, guest,
node/adoption/GC writers and their full deletion checks remain open. It is not
cross-host cleanup acceptance or a claim that the entire delete protocol closed.

The final full Go suite passed with both PostgreSQL test variables enabled.
API vet, docscheck (53 canonical documents), diff checks and independent
follow-up review passed. The isolated PostgreSQL instance was used only for
this test slice; no retained project resources or VM lab were deleted.

### Commands inside metadata deletion (2026-10-09)

Deletion now scans complete command records inside the transaction, preserving
existing column/payload validation and retirement/UID matching. Late commands
block deletion even without Agent registration; a missing UID with a matching
payload name adds a blocker, while a different explicit UID does not rebind.
Postgres raw command updates lock/materialize authority before command rows,
matching grant and deletion lock order. File record locks and SQLite write
transactions already provide the corresponding exclusion.

Targeted real-PostgreSQL race passed (8.593 seconds), including an uncommitted
first-authority transaction blocking command publication with no partial row.
Initial full API race exposed old fixtures allowing deletion with unresolved
leased commands. Tests now require API rejection, then explicitly simulate
external rebuilding to retain stale-UID grant and clock-fencing coverage.
Concurrent issuance/deletion requires both stopped effects and retired command
delivery before successful API deletion; production checks were not weakened.

Raw-command maintenance admission, guest/node/adoption/GC writers, and exclusion
through postcommit workspace cleanup remain open. This is not final cross-host
deletion or P9 acceptance.

The final full Go suite passed with both database test variables enabled.
API vet, docscheck (53 canonical documents), diff checks and independent
follow-up review passed. Only the dedicated test PostgreSQL instance was used;
no retained resources or deployment data were rebuilt.

### Raw command admission under maintenance (2026-10-09)

Shared maintenance validation now runs inside raw command publication on all
three backends. New records, claims/lease extensions and first delivery cannot
enter a frozen scope. Existing-lease acknowledgements/running reports, terminal
evidence, retirement, cancellation, inventory and read-only recovery observation
can drain; mutating recovery is not exempt. Missing UID resolves every named
payload against a full lifecycle only for exclusion. Unknown ownership with any
live barrier is rejected rather than assigned a synthetic identity.

Regressions reproduced frozen admission and claim/delivery bypass. Added cases
verify no speculative records/leases escape rejection, ack/started/complete can
converge, and a benign first payload cannot conceal a second frozen scope.
Targeted real-PostgreSQL race passed (1.706 seconds); combined command/recovery
race passed (5.112 seconds). Initial API race exposed a recovery fixture that
admitted an uninitialized scope after another scenario retained a barrier; it
now establishes its real lifecycle first without relaxing production policy.
Persistence race passed (3.649 seconds).

This is not a substitute for execution-grant validation or cross-host stop
evidence. Raw guest/node/adoption/GC records and exclusion across postcommit
workspace cleanup remain open.

Final full Go regression passed with both database variables enabled.
API/persistence vet, docscheck (53 canonical documents), diff checks and
independent follow-up review passed. This slice used only a dedicated test
PostgreSQL instance; retained deployments and resources were not rebuilt.

### NodeOperation maintenance and deletion boundary (2026-10-09)

Raw node Create/CAS hold authority through publication. Active admission into a
frozen topology is rejected while existing terminal reports may drain. File
and Postgres lock authority before the operation; SQLite uses its write
transaction. Compound file dispatch has an explicit under-authority entry point
to avoid recursive locking. Deletion scans complete node records across
workspaces and shares its completion-evidence predicate with preflight.

Regressions reproduced raw admission and late-node deletion bypass. Targeted
race with real PostgreSQL passed (7.314 seconds), including file publication
blocked by authority alone and no partial record after timeout. Combined
node/restore race passed (7.612 seconds). Initial API race exposed an unrelated
topology fixture without lifecycle identity; it now creates a real distinct
lifecycle before freezing the target, retaining all original restore assertions.
Persistence race passed (3.652 seconds).

The final full Go suite passed with both database variables enabled. Vet,
docscheck and independent follow-up review passed. This validates NodeOperation
aggregate storage, not full adoption/SSH migration. Raw guest execution/file
records and postcommit cleanup exclusion/recovery remain open. No retained
deployment or project resources were rebuilt.

### Guest execution/file maintenance and deletion boundary (2026-10-09)

Guest raw records now share the node authority→record guard. Frozen scopes
reject creation and active-state advancement; terminal drain remains possible.
File CancelRequested is exempt only for an existing record with unchanged
status, not queued→running. Compound file dispatch uses an explicit
under-authority method. Deletion, maintenance and inspection share complete
guest-record decoding and completion evidence including nonzero, ordered times.

Regressions reproduced frozen admission and late-record deletion bypass.
Real-PostgreSQL targeted race passed (13.139 seconds), covering both guest kinds
waiting on authority alone, no partial record after timeout and cancellation
flag misuse. Guest persistence race passed (1.679 seconds); malformed-terminal
deletion and late-completion regression race passed (1.301 seconds).
Fixtures now include required authority tables and topology names. Corruption
is injected below production validation, preserving codec/redaction assertions.

The final full Go suite passed with both database variables enabled. Vet,
docscheck and independent follow-up review passed. No existing deployment or
resource was rebuilt. This does not close the existing guest execution Cancel
handler's terminal-before-publication window, remote stop proof, or persistent
exclusion and recovery through postcommit workspace cleanup.

### Agent cancellation does not invent target completion (2026-10-09)

The Agent now acknowledges only the cancel command after signalling a target
context. It no longer emits a synthetic cancelled event for the target, which
could retire the target delivery before its real outcome arrived. Guest
completion reporting uses an independent RequestTimeout budget (default 30s)
while preserving context values and command proof. Execution-owned tracking
tokens prevent old cleanup from removing replacement command/operation aliases.

Regressions reproduced premature target completion, stale tracking cleanup,
and completion reporting with an already-cancelled context. Agentexec race
passed (4.681 seconds), including reporting timeout. Full Go regression, vet,
docscheck and independent review passed. This worker-only change did not rerun
real PostgreSQL or cross-host cancellation. Tests do not establish signed stop
proof, durable report retry, or atomic API guest cancellation; those remain open.

### 2026-10-10 — recoverable execution cancellation and grant source fencing

Execution cancellation persists a monotonic intent without inventing a terminal
result. Restart reconciliation recreates a missing stable cancellation outbox;
retries preserve delivery leases and retired reservations. Real completion may
retain its outcome. API schema 11 and execution record format 2 explicitly reject
the old formats; file format 2, aggregate 4 and authority 2 are unchanged.

Production grant issue/authorize/renew reads the current execution under the
authority boundary. Cancelled, terminal, missing or rebound sources are denied;
denial observes time without extending command leases. Raw host-stop acceptance
remains available. Real PostgreSQL API race (99.026s) and persistence race
(3.786s) passed, along with local-file/SQLite cases. Vet and documentation checks
passed. Full Go regression also passed; additional three-backend cancellation,
clock, identity and commit-serialization race checks passed (9.946s). SQLite's
busy handler can outlast the statement context; the serialization test keeps
the lock owner alive past that timeout and verifies no partial cancellation.
This is repository/API evidence, not a new two-host or VM acceptance run.

Intent/outbox still use recoverable separate writes. Atomic cancellation,
revocation of already-issued grants, host-signed execution completion, durable
result reporting and the full P9 fault matrix remain open.

### 2026-10-10 — host-owned guest results and durable replay

Guest task receipts now require a typed, signed result with explicit exit-code
knowledge. The host reads bounded durable output before sealing the receipt and
preserves provider truncation flags. Only confirmed remote termination permits
signing. Worker completion routes, DTOs and report calls were removed. Standalone
execution results commit before authority release; Run child tasks only retire
their child grant. Exact replay survives retirement, redaction and successor
epochs. API schema 13, execution format 4 and host task record 2 reject old data.

Full Go regression passed. Real PostgreSQL race passed for API (213.480s),
persistence (18.753s), controlplane (1.090s) and Agent (7.760s). The concurrently
run supervisor suite hit one deadline in its slow child-report scenario; an
isolated full supervisor race rerun passed (21.245s), without changing the
production timeout. Additional race tests cover both completion/release write
failures, Run child routing and output truncation. Vet, docscheck and e2e-tagged
compilation passed; independent review found no remaining blocker in this slice.

The real Docker `TestHostSupervisorRealTaskStopAndSuccessorE2E` passed with a
fresh helper (5.53s test / 6.554s package). It verifies API exit code 7 and output,
lost stop acknowledgment, API/supervisor restart and replay, running task
cancellation after Agent session replacement, and successor fencing. The test
removed its disposable container. This is one-host execution evidence, not the
two-VM P9 fault matrix. Never-started task resolution, deletion cleanup recovery
and the broader P0–P9 capability/acceptance backlog remain open.

### 2026-10-10 — cancelled executions that never received authority

Cancellation recovery now retires the source delivery before checking the
original admission and both current grants and stop history under authority.
Only an intact admission with no authorization history becomes
`cancelled / not_started`; an earlier Agent `running` report is not treated as
remote execution evidence. Cleanup then retires the cancellation delivery.

File, SQLite and real PostgreSQL tests cover source-retirement failure,
retirement-to-terminal failure, terminal-to-cancel-cleanup failure, cold replay,
unchanged terminal versions, successor-replaced grant history, and missing or
mismatched admission evidence. Targeted race passed (API 9.283s, persistence
1.247s); the frozen final snapshot passed full Go regression (API 156.790s,
persistence 7.844s). Vet, docscheck and independent review passed. No new
two-host or provider E2E run is claimed for this metadata-only change.

This closes cancelled, never-authorized execution cleanup. Uncancelled delivery
failures still need their own automatic outcome path. Persistent deletion and
the complete P9 matrix remain open. The proof assumes an intact authority
ledger; external deletion of the whole ledger is not covered by these tests.

### 2026-10-10 — automatic cleanup of unissued delivery failures

Supervisor now retires failed/denied/cancelled guest deliveries and applies a
shared execution mutation. Original admission and complete grant/stop history
are checked inside the authority transaction. No history permits
`failed / not_started` without fabricating a cancellation request. Existing
authority is revoked, while the operation waits for host evidence; successor
grants and existing terminal outcomes remain unchanged. The cancellation and
failure paths share the same three storage adapters and admission validation.

Real file permission faults and SQLite/PostgreSQL write faults verify recovery
after source retirement, including failed authority revocation. Tests also
cover Supervisor.Scan wiring, cancellation precedence, old stop history and
API clock skew. Full Go race passed with real PostgreSQL, run sequentially by
package: API 188.146s, persistence 10.302s, supervisor 20.626s. Targeted fault
race passed (2.514s); vet, docscheck, e2e-tag compilation and independent review passed. Persistence
versions remain API13/execution4/host task2. No provider or two-VM E2E pass is
claimed for this metadata-only step.

Issued authority with no host task record still requires a host-side absence
proof. Persistent deletion and the complete P0–P9 acceptance backlog remain
open; delivery failure alone is never remote-stop evidence.

### 2026-10-10 — guest-side cancellation of missing task records

A new stop-only signature binds an issued, revoked guest-task epoch. It cannot
authorize Start/Renew. With that proof, the guest checks boot identity and
serializes scope/task lookup, refuses any orphan cgroup, and writes a durable
cancellation tombstone before confirming termination. A target directory left
without its record can recover; dangling references and missing/corrupt records
for other tasks are not ignored. Scope publication followed by a failed record
write is recoverable. Late Start returns the terminal tombstone; old retries do
not regress successor scope state.

Guest task record version 2 rejects version 1; RPC remains 1 and scope remains
2. API13/execution4/host task2 are unchanged. Both embedded amd64/arm64 helpers
were rebuilt. Full Go race passed sequentially (guesttask 4.302s, transport
1.780s, API 78.480s), along with vet, docscheck and independent review. No new
PostgreSQL gate was run for this guest/transport-only change.

`make test-guesttask-e2e` passed in a disposable private cgroup: orphan cgroup
rejection, late worker launch prevention, signed cancellation of a real running
task, crash recovery, expiry and epoch fencing. The real host/API task E2E also
passed with the fresh helper (3.52s test / 4.542s package), including API output,
lost acknowledgment and restart replay. Both tests cleaned their containers.

This is guest-side infrastructure, not a completed missing-host-record workflow.
API discovery/signing of pending stop proofs and host-side automatic recovery
still need integration. A mismatched current guest boot is rejected for missing
records; separate reboot evidence remains necessary. Full two-VM P9 acceptance
and the wider P0–P9 backlog remain open.

### 2026-10-10 — API discovery and missing-host-task cancellation

The owning host now discovers revoked standalone tasks from the API, including
retired source commands. Discovery uses authenticated owner scope, immutable
request digests, a fixed epoch ceiling and 64-entry pages. Bad source records
do not starve later entries. A disabled Agent can drain its old effects.

The host writes a stop-only task record before provider resolution, then checks
canonical UID/generation and pins provider identity before cancelling. Lost
reports replay the original signed receipt. Existing completed guest results
are preserved. A verified guest reboot signs old/new boot IDs and an unknown
result; replacement providers cannot supply that evidence for the old task.

Full `go test -race ./... -count=1 -timeout=8m` passed with a fresh isolated
PostgreSQL 16 instance enabled for API and persistence tests (API 106.499s,
hostsupervisor 17.879s). Focused tests cover paging past a bad source, fixed
ceilings, owner authentication, provider replacement refusal, old-format and
tampered-proof rejection, receipt replay and HTTP reboot evidence. Vet,
docscheck and all-package e2e-tag compilation also passed.

`make test-host-supervisor-e2e` passed with a freshly built helper (5.28s test /
6.300s package). The added real Docker round issues authority without sending
Start, cancels through the API, recovers missing host/guest records, and rejects
a late Start carrying the still-valid original credential. It also checks API
terminal cancellation without fabricated output. Test containers were cleaned.
Guest reboot evidence was validated in host and HTTP integration tests, not a
new physical reboot E2E in this run.

Host task format is now 3 and rejects older records; both embedded helpers were
rebuilt. Guest task2/RPC1/scope2/API13/execution4 remain unchanged. Run-derived
task discovery is still open. A terminal API result with complete loss of its
original host receipt still cannot automatically finish authority release.
Persistent deletion, remaining P5–P8 capabilities and full two-VM P9 fault
acceptance remain required; this entry does not close the overall plan.

### 2026-10-10 — Run-derived missing-task discovery

Discovery now includes revoked Run children. The API verifies the original
parent epoch, owner/session/command/attempt and immutable Run request digest,
including a retired command whose delivery lease has advanced. Its stop-only
signature binds the Run ID and topology. The host derives the node from
canonical state or that Run's unfinished candidate, and persists/verifies Run
provenance with its task record. Wrong-Run candidates do not reach a provider
connection. Parent revocation now atomically marks unfinished children revoked;
online rejection alone had left them invisible to the discovery scan.

Parent checkpoint recovery and `ProveStopped` accept the revoked reconstruction
of an originally active child, while still comparing every immutable identity
field and requiring an acknowledged, valid host receipt. RED/GREEN regressions
cover both comparisons; explicit epoch/parent-epoch changes remain rejected.
Child stop still cannot create a parent Run terminal result.

Real Docker host E2E passed (6.42s test / 7.445s package), including no child
Start, API discovery from the retired parent command, signed guest tombstone,
stopped child grant, still-revoked parent and late original Start without output
or cgroup. An initial test fixture omitted the non-root task UID, causing a
digest mismatch on replay; the fixture was corrected to match production UID
65534 and the successful retry verified the intended tombstone behavior.

Full `go test -race ./... -count=1 -timeout=8m` passed with a fresh isolated
PostgreSQL 16 instance (API 105.152s, hostsupervisor 17.944s). Both embedded
helpers were rebuilt. Vet, all-package e2e-tag compilation, docscheck and
independent read-only review passed. Tests covered actual canonical candidate
selection, original source after re-lease, wrong parent digest, cold recovery
and altered provenance. No new two-VM fault-matrix pass is claimed here.

Host task3/guest task2/RPC1/scope2/API13/execution4 remain current. Updated guest
helpers are mandatory for the Run-bound cancellation fields. Persistent
deletion, unknown parent Run intent, terminal-result/host-receipt total-loss
recovery and the remaining P0–P9 capability/fault acceptance work remain open.

### 2026-10-10 — durable guest completion evidence and release recovery

Guest execution records now use format 5 and the API schema is 14. A host
completion stores an immutable `{grant, receipt_hash}` proof with the terminal
result. SQL commits that record and the stopped authority in one transaction;
file recovery republishes the stopped authority from the proof after a partial
publication or API restart. The transition never reconnects to the guest and
never regenerates expired output. Exact replay is idempotent and cannot affect a
successor epoch. The host route validates the original authority before accepting
the receipt.

Persistence RED/GREEN tests cover missing or altered proof, epoch and command
identity changes, result redaction, release interruption, and successor fencing.
API and persistence race tests passed with an isolated PostgreSQL 16 instance;
real Docker host E2E passed after the change. The final full repository race
will be rerun before this checkpoint is committed. This closes the guest-result
release window, but persistent deletion, unknown parent Run intent, adoption/GC,
full SSH lifecycle and the remaining two-VM P9 fault matrix remain open.

### 2026-10-10 — durable metadata deletion intent

Metadata deletion now writes a durable `delete.intent` containing topology UID and
state serial before the authority-aware state reset. The intent blocks workspace
publication. If the controller exits after the state commit or during workspace,
placement, or health cleanup, the next Delete or supervisor scan resumes the
cleanup and removes the intent only after all local metadata cleanup succeeds.
The intent is created only after the existing destroy receipt/activity checks, so
recovery never invents provider quiescence or contacts a provider.

### 2026-10-10 — full real-provider gate rerun

With the configured Firecracker binary, kernel
`/home/jiandong/.cache/sysbox/artifacts/local/vmlinux-5.10.245`, and rootfs
`/home/jiandong/.cache/sysbox/rootfs/ubuntu-24.04.ext4`,
`SYSBOX_FIRECRACKER_BIN=... make test-p9-real` passed.

The run covered real Docker file install (3.28s), node power pause/resume
(2.58s), guest task and Run completion-loss recovery (including cleanup receipt
and metadata DELETE), Docker adoption (1.74s), Firecracker controller crash
recovery (9.70s), generation lifecycle (10.80s), reset with old-generation
Start/Stop/Pause/Resume/Destroy fencing and cold adoption (23.70s), independent
cross-process Firecracker recovery (10.40s), and libvirt generation effects
(2.72s). Firecracker and libvirt resources were cleaned by their tests.

This is a real-provider component gate, not a complete P9 claim: the two-host
API/Agent failover matrix, partition, disk-full, upgrade/rebuild rehearsal and
main integration are still outstanding.

### 2026-10-10 — fresh current-commit two-VM lifecycle round

A new lab `/tmp/sysbox-p9-lab-current` was created from the current commit with
independent KVM kernels, private qcow2 disks, no shared guest mounts, distinct
boot IDs, bidirectional private networking and nested KVM. `check` passed and
recorded control boot `12fc6676-3c2c-46f1-98e3-b3480533407a`, execution boot
`f23adb2f-0c0d-4b16-b9a5-8c4e18b29854`, and `nested_kvm_qemu_started=true`.

The current binary was deployed to both VMs. Because execution had no registry
access, the host `alpine:3.22` image was exported, SHA256 verified as
`4cb95149f3e3848b4fca6333430b739695326c82d00c015770d5b4723cfc4225`, transferred
through pinned SSH and imported explicitly. The first round correctly retained
uncertain evidence when the image was unavailable. The second round then passed
Apply and Destroy through the remote Docker provider, produced cleanup receipt
serial 4 with `empty=true`, and independently queried execution Docker for zero
containers matching the recorded topology UID. Non-force metadata DELETE then
succeeded.

This proves a fresh current-format cross-kernel lifecycle round and explicit
artifact provisioning. It does not close API failover during an active Run,
partition, database restart, disk-full, upgrade/rebuild, or the full
provider/SSH/GC matrix.

The GC plan fence is also covered by current contracts: the inventory digest is
bound into the leased `gc_delete` command/grant, checked against the canonical
lifecycle aggregate, and revalidated by the owning host before the provider
effect. A new state or snapshot reference, UID/generation replacement, or
provider external-ID mismatch rejects the old plan without extending authority.
