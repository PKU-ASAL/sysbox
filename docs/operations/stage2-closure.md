# Closing stage 2: capability and fault coverage

Decision: closed against the approved three-PR operational closure design.
Stage 1 remains closed. Stage 3 owns latest-main integration, synchronized release
artifacts, full same-version dual-host release validation and upgrade/rebuild.
This decision does not mean every original P9 release obligation is complete.

## Delivered behavior

- Actual Docker executable advertises its implemented adoption capability.
  Public ownership transfer has explicit prepare/proof/submit/abort CLI operations,
  canonical source retirement, exact retries and real three-provider crash coverage.
- Mixed Docker NAT works through the actual CLI with Docker, Firecracker and
  libvirt guests. Runtime bridge proofs are persisted without pointer panics or
  large-integer identity loss; libvirt verifies root-namespace managed targets.
  Repeated libvirt attachment requests preserve one logical NIC and reject conflicts.
- Failed recoverable keyed Apply supports fingerprint-validated input resubmission
  and transient exact-incarnation recovery handoff. Old Agent/host protocols and
  unhydrated API instances refuse recovery. Inputs never enter durable dispatch.
- Firecracker exit observation uses a single final stat snapshot to distinguish
  an exiting owned process from PID reuse; real reset/cold adoption passes.
- Capability API distinguishes shared PostgreSQL from host-local file/SQLite.
  Existing conservative absence/cessation claims remain evidence-dependent.

## Finite fault closure

| Boundary | Evidence and guaranteed result |
| --- | --- |
| Admission/outbox and API process death | Three-backend atomic dispatch, transfer process crash and snapshot/restore process matrix; same request cannot duplicate admission or revive retired dispatch |
| Effect and host process death | Docker/libvirt/Firecracker transfer before effect, ownership, observation, report, state and acknowledgement; non-Docker GC kill/missing/corrupt journal; unknown never replays |
| Completion lost / reset continuation | Real public Docker API completion-loss, returned-destroy and cold/repeated reset continuation; canonical prior result/identity preserved |
| Helper/guest task death | Real SSH tasks, delegated cgroup supervisor SIGKILL, file commit-intent/rename crashes and vsock guest kernel reboot; no invented exit code or PID-only termination |
| API failover | Primary stopped with live child; original child and parent finish on backup, signed completion, Destroy and metadata DELETE |
| Database outage / network partition | Real live guest, bounded watchdog termination, unchanged task identity, stop receipt after restoration, Destroy and metadata DELETE |
| Delayed/repeated/reordered requests | Epoch/session, command/task digest, observation sequence and all-backend CAS contracts; real revoked file and task requests refused |
| ENOSPC after effect / stop receipt / report acknowledgement | Actual kernel ENOSPC on private 1MiB tmpfs; unknown remains unknown after space release, unpersisted receipt cannot release authority, durable receipt retries unchanged |
| Mixed target replacement | Actual owned bridge/TAP, stale generation and same-name replacement refusal; replacement survives rejected deletion |
| Mixed lifecycle | Real three-guest Apply, six traffic directions, individually tagged cross-host SNAT, whole reset with generation changes and old marker absence, repeated traffic/NAT and exact before/after generation absence after Destroy |
| SSH access/service | Public API/Agent login, rotation/revoke, independent administrator access, helper/service restart, lost receipt, runtime Apply/reset/Destroy and metadata DELETE |

These are risk-distinct boundaries, not an unbounded Cartesian product. Shared
storage/authority rules are tested across backends; provider/transport-specific
effects are tested on their real execution environment. The ENOSPC result applies
to the host effect ledger, not every guest/SQL filesystem. Network loss is never
interpreted as a stop proof.

## Evidence

Final contract report: /tmp/sysbox-stage2-contract-delivery-20261026/run.json,
99 exact gates passed with zero skips/failures; source
baf6c64bb4695fbabf115fb1ff883444abcee63da0280a7154953fa1605111ae.

- Repeatable mixed runner: /tmp/sysbox-stage2-mixed-final-20261026/run.json.
- Live faults: /tmp/sysbox-gc-fencing-20261021/fault-failover-b8b45e0e0c42,
  fault-database-39922b3102b4, fault-private-network-b25a587e5183.
- Public SSH: /tmp/sysbox-gc-fencing-20261021/sshgate149e36c182e3/result.json.
- Provider reports: /tmp/sysbox-stage2-provider-matrix-20261026/run.json,
  /tmp/sysbox-stage2-provider-matrix-retry-20261026/run.json,
  /tmp/sysbox-stage2-provider-matrix-final-20261026/run.json. Only individual
  passed gates count in the earlier failed/incomplete reports.
- Firecracker fix: /tmp/sysbox-stage2-fc-reset-repeat.log, three complete rounds.
- Guest/SSH/helper: /tmp/sysbox-stage2-guest-matrix-20261026/run.json.
- Vsock: /tmp/sysbox-stage2-vsock-task-current.log.
- File: /tmp/sysbox-stage2-host-file-current.log.
- Network replacement: /tmp/sysbox-stage2-network-replacement.log, two rounds.
- ENOSPC: /tmp/sysbox-stage2-enospc-delivery-20261026/run.json.
- Full Firecracker race passed; independent final hostsupervisor race passed
  (39.191s). System Python acceptance suite passed 72 tests without skips.

This is a layered evidence set, not one all-real same-source campaign. Mixed
evidence used 877a95a..., guest/part of provider evidence used 5c56afd..., final
contracts and repaired gates used baf6c64.... Final unified release validation is
stage 3. Source-invalidated campaign outputs and genuine failed rounds remain
preserved and are not promoted to passing reports.

Independent review found no blocker in this fixed stage scope. No main merge
or production deployment is authorized by this closure.

## Original requirement reconciliation

| Original requirements | Stage-2 disposition |
| --- | --- |
| P0.1/P0.3 | Capabilities, exact requirement catalog, runnable contract/process/real-host/mixed/fault entry points delivered |
| P0.2/P0.4/P0 acceptance release portions | Stage 3: final multi-architecture artifact parity and latest-main integration |
| P1.1–P1.4 | Immutable local materialization, digest/path/source rules, transient input resubmission and denial on missing inputs implemented; exact contracts verified |
| P1.5/P1 acceptance | No shared mounts, independent kernels, real Apply→Destroy→receipt→metadata DELETE and fault recovery verified |
| P2.1–P2.7/P2 acceptance | Durable authority/conflicts, privileged host boundary, monotonic expiry, stop/fence before takeover, unknown refusal; storage and real-host faults verified |
| P3.1–P3.7/P3 acceptance | Shared durable task/file model, SSH/vsock, dedup/output/cgroup cancellation/watchdog/reboot; real guest and protocol contracts verified |
| P4.1–P4.6/P4 acceptance | Cancellation/renewal authority, atomic sequence, complete activity blockers, receipt binding and dual-API outage recovery verified |
| P5.1–P5.5/P5 acceptance | Adoption/transfer provider and canonical API contracts, cold recovery and crash windows verified; arbitrary unprovable external processes remain refused |
| P6.1–P6.5/P6 acceptance | Aggregate metadata snapshots/restore/lease maintenance three-backend/process contracts verified; VM disk snapshot capability is not advertised |
| P7.1–P7.6/P7 acceptance | SSH/service and actual mixed NAT lifecycle/target identity verified, including reset and exact destruction |
| P8.1–P8.4/P8 acceptance | Reference/retirement and batch GC with owning-host deletion, real provider interruption recovery and conservative refusal verified |
| P9.1–P9.4 | Required integration/fault slices completed for this capability stage; final all-version release report stays stage 3 |
| P9.5/P9.6/P9 final acceptance | Stage 3: actual upgrade/rebuild, latest main, synchronized images/binaries and final release review |

The original catalog retains the full P0–P9 text and evidence-level remaining
notes. Its global complete flag is not a closing-stage-2 switch; stage-3 obligations
and same-source release reconciliation must not be erased to turn it green.


## Final exit-window validation and retained scene

Firecracker deletion retries only the specific empty-cmdline transition inside
its bounded post-signal wait. Final stat validates start identity and exit status
from one snapshot; PID reuse, malformed stat and permission failures still refuse
deletion. Real Firecracker batch GC passed three rounds in
/tmp/sysbox-stage2-gc-exitfix-repeat.log; full Firecracker race passed in
/tmp/sysbox-stage2-fc-final-race.log. Independent review found no blocker.

One extra diagnostic attempt, p9-4221803d237b, timed out during guest Start before
failover injection and before a parent checkpoint. It is excluded from successful
failover evidence. Its command is retired; formal force-unlock generated signed
child cancellation, missing-task sealing succeeded, and host reported confirmed
child termination with unknown result. The parent remains unresolved and
maintenance remains waiting_for_stop. No parent success or provider absence is
invented. The isolated VM and original journals remain retained offline.

The 99-gate report precedes this narrow exit-transition refinement; that
refinement has focused identity regressions, full provider race and repeated real
GC evidence. Layered-source qualification applies to this delta. Stage 3 performs
final unified-version release validation.
