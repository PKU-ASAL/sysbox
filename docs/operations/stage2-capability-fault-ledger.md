# Stage 2 capability and fault ledger

Stage 2 closure decision: see [stage2-closure.md](stage2-closure.md).
Entries below retain implementation history; the closure document takes precedence.

This is closing stage 2, stacked PR #54, not the earlier GC stage2 selector.
Original wording remains authoritative in campaign.json (65 P0–P9 entries).
No omitted requirement, skipped test or previously successful source closes a cell.

## Delivered slices

| Capability | Implemented and exercised | Remaining boundary |
| --- | --- | --- |
| Public ownership transfer | Explicit CLI prepare/prepared/submit/abort; file/SQLite/Postgres public route contracts and cold retries; Docker/libvirt/Firecracker real host completion and crash chains | Deployed CLI/proxy/supervisor integration; fixtures supply admin subject and invoke host preparation directly |
| Transfer fault positions | Before effect, after ownership, after observation, before report, after state commit, after report, for all three providers | These are host process SIGKILL positions; do not imply every provider-internal write or API/helper process position |
| Libvirt attachment retry | Same logical attachment is idempotent; changed MAC, address, gateway, target or generation is rejected; persisted cold retry and corrupt duplicate record refusal | Existing corrupt duplicate records remain rejected; no silent rewrite |
| Docker NAT lifecycle | Actual CLI Apply and whole-topology Reset with all three running guests; six directional pings before/after; NAT to a separate VM with observed masqueraded source; Destroy and exact absence | Register repeatable enclosing campaign and complete same-name replacement/fault cells |
| SSH access | Real sshd key rotation/delete, endpoint restrictions, administrator and independent managed access preservation; staged config validation, drop-in/reload/cold service recovery | Combined provider guest service and enclosing topology reset remain separate |

## Fixed next completion items

1. P7 normal mixed NAT lifecycle now passes end to end. Remaining: turn the
   scoped recorded run into the repeatable campaign and attach the existing
   same-name target replacement/fault gates to that enclosing lifecycle.
2. Complete the 41 newly inventoried P0–P4/P7 requirement mappings. Attach precise
   implementation and test entries; retain pending current evidence. Do not
   report the current 65-entry inventory as 65 completed requirements.
3. Reconcile original section-5 failure contracts and P9.3 positions against
   existing API/Agent/helper tests. Every missing cell must identify operation,
   process, fault boundary and expected durable outcome before implementation.
   Current transfer SIGKILL evidence cannot substitute for guest transport loss,
   disk-full, database outage or provider-internal partial-write evidence.
4. Current-source strict campaign and final independent review for stage 2.
   Final latest-main integration, synchronized runtime images and actual upgrade
   rehearsal remain closing stage 3 (including P9.5/P9.6).

## Current evidence and limitations

Final real slice report: /tmp/sysbox-stage2-real-final-20261026/run.json.
The runner records exact test events, two repetitions, local/remote binary hashes,
helper hash and source hash; each group must pass without skips. This is a scoped
slice report, not the full P0–P9 campaign. Contract report:
/tmp/sysbox-stage2-p7-contract-final-20261026/run.json.
Full libvirt race: /tmp/sysbox-stage2-libvirt-race-20261026.log.

Earlier failed attempts remain preserved. SSH fixture execution required its
normal primary group plus a temporary docker supplementary group; the test does
not alter persistent user groups or the host's production sshd. Firecracker NAT
fixture initially used an invalid manifest directory; corrected to the existing
owner/node/generation layout, retaining production identity validation.

## Real CLI integration defects fixed

The actual deployed Docker registration omitted OwnershipAdoption, although
provider implementations and test registries exposed it. Register the capability
at the real entry point and cover it with a regression.

Runtime NAT Create and Discover passed a ManagedBridgeTarget pointer through
MustAttributes and panicked after the network effect. Write the proof directly
to private state; preserve its raw JSON through later private-state writes so
uint64 namespace inode and int64 generation above 2^53 remain exact.

Libvirt guest network convergence assumed an isolated namespace. Managed Docker
NAT bridges use the root namespace: require and lock the exact managed target
proof, then probe from the corresponding namespace. No proof still means refusal.

Evidence: /tmp/sysbox-stage2-mixed-reset-states.json (all three generations and
external IDs changed, logical NIC coordinates/network identity preserved),
/tmp/sysbox-stage2-mixed-reset-verified.json (old markers absent and six-way
traffic), /tmp/sysbox-stage2-nat-egress-20261026.json and
/tmp/sysbox-stage2-nat-egress-after-reset-20261026.json (all three guests reached
the independent control VM, echo source 10.77.0.20 instead of guest addresses),
/tmp/sysbox-stage2-mixed-cleanup-verified.json (successful mixed-nat-v6 only).
Current guest helper was embedded via a build overlay, not the tracked old bin.

Earlier failed test incarnations are NOT declared clean. Their state/journals and
small provider evidence were archived at /tmp/sysbox-stage2-mixed-failures.tar.gz.
Exact provider teardown removed the verified same-boot instances. A previous-boot
FC directory and stale bridge anchors correctly refused ordinary deletion and
remain retained with their original unknown journals. This is test-fixture
cleanup, not operational disposition of historical Runs.

## Recovery inputs now implemented

For keyed failed/recoverable Apply, resubmit the original Apply request and key to
the API serving recovery. Request fingerprint validation rejects changed values.
GET run reports inputs_required after restart; successful hydration stays only
in memory. Recovery authorization returns transient inputs under the exact
canonical incarnation and active command lease. Agent/host reconstruct the graph
with those values; ordinary terminal Run execution remains prohibited.

Both run-recovery-inputs.v1 Agent capability and host inputs_protocol=1 are
required, so an old host cannot silently recover with default values. Empty keyed
input maps also require explicit resubmission/new protocol. API failover to an
instance without hydration requires resubmission, not session-affinity guessing.
Unverifiable legacy inputs remain blocked. No new secret persistence is added.

Three-backend public authorization, malformed/changed inputs, old Agent/host,
new API, changed incarnation and no persisted canary checks pass. Real local
host-supervisor socket recovery uses a required non-default variable and proves
the already-returned provider result is committed without replay.
Full race: /tmp/sysbox-stage2-recovery-inputs-full-race-20261026.log
(API 186.073s, agentexec 5.493s, hostsupervisor 31.805s, controlplane 1.154s).
Independent review found no blocker.

## Host persistence ENOSPC and capability scope

Actual kernel ENOSPC was injected on a dedicated 1MiB tmpfs in the isolated VM's
private mount namespace, twice per cell. After effect success but outcome write
failure, cold recovery keeps unknown and refuses replay or a stop receipt even
after space is released. Unpersisted stop receipts cannot report/release authority.
Failure to persist report acknowledgement retries the identical durable receipt
without repeating the effect.

Reproducible entry: scripts/acceptance/enospc.py --root LAB --output NEW_DIRECTORY.
Evidence: /tmp/sysbox-stage2-enospc-final-20261026/run.json. These cells cover the
host effect ledger, not every guest-task or SQL transaction failure point.

Capabilities explicitly identify shared-postgres versus host-local store scope
and PostgreSQL multi-API support. Existing unconditional absence/revocation
booleans stay false. Recovery input protocol is explicitly advertised; actual
recovery also checks Agent and host capabilities. Three-backend capability tests
pass. Reading a capability endpoint does not authorize effects or prove absence.
