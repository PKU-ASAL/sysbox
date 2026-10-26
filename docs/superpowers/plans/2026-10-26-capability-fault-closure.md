# Stage 2 capability and fault closure

Closed against the approved three-PR design; final exit-window revalidation and independent review passed. See
[stage 2 closure](../../operations/stage2-closure.md) for requirement reconciliation,
actual evidence boundaries and the remaining stage-3 release obligations.

Execute inline under the approved three-PR design. Base: bb20bb2; branch:
feat/capability-fault-closure. Stage 1 remains closed.

**Goal:** Close capability and fault gaps against the original P0–P9 requirements.
**Architecture:** Keep the existing canonical repositories, signed host authority,
provider interfaces and strict source-bound acceptance runner. Exercise public
protocols instead of replacing them with direct repository setup.
**Tech stack:** Go, Python unittest, disposable two-QEMU lab, file/SQLite/Postgres.

## Fixed boundaries

Only enrolled disposable resources may be mutated or fenced. Unknown historical
results remain unknown. Missing evidence and skipped tests never mean verified.
P0 final build parity and P9.5/6 final main integration, synchronized runtime and
upgrade rehearsal belong to closing stage 3; retain them in the original ledger.

## Execution checklist

- [ ] Expand scripts/acceptance/campaign.py and campaign.json from 24 to all 65
  original requirements. Preserve exact original text and all existing gates;
  newly inventoried requirements remain open until explicitly mapped and verified.
  In test_campaign.py assert all ten stages and rejection of missing/changed text.
  Run: python3 -m unittest discover -s scripts/acceptance -p test_campaign.py.
- [ ] Exercise public ownership-transfers prepare → host prepare-transfer →
  prepared → submit → completion/retry. Cover file/SQLite/Postgres, then real
  Docker/libvirt/Firecracker. Change pkg/api/ownership_transfer_test.go and
  ownership_transfer_e2e_test.go to avoid internal transfer preparation shortcuts.
  Add operator CLI only where the public operations have no usable CLI entry.
  Enforce unchanged source on missing proof and no second effect on exact retry.
- [ ] Reconcile P7 against tests/e2e/heterogeneous_matrix*.sh and
  heterogeneous_reset*.sh: managed target replacement, mixed NAT communication,
  reset/recovery, SSH key rotation/deletion, unrelated access preservation and
  exact destruction. Fix defects at the provider/runtime boundary they violate.
- [ ] Record finite fault cells from original section 5 and P9.3: admission,
  effect, journal and report boundaries; API/Agent/helper process death; lost,
  delayed/duplicate/reordered messages; disk full; DB restart; uncancellable
  daemon calls. Map existing tests before adding missing cells. Every cell must
  identify actual injected failure, observable contract and applicable scope.
- [ ] Run current-source contracts and applicable real lab cells without skips,
  independently review, update ledger, commit/push and open stacked PR against
  feat/intent-disposition-closure. Do not claim stage closure before these pass.

## Initial findings

The strict catalog omitted P0–P4/P7 entirely. Existing transfer real tests bypass
public prepare/submit through repository and server-internal methods; their
provider fault evidence remains valid but does not prove operator workflow.

## Execution checkpoint: public transfer

- [x] Full 65-requirement inventory, exact text and missing-entry validation.
- [x] Explicit operator CLI prepare/prepared/submit/abort; no role impersonation,
  redirects, automatic replay or automatic progression after an uncertain reply.
- [x] Public API contracts on file/SQLite/Postgres: missing preparation refusal,
  preparation preserves source, forged signature refusal, accepted dispatch,
  cold API exact retry and changed-plan conflict.
- [x] Public contracts plus existing API process crash matrix pass with race,
  without skips; full CLI package race passes. Independent review found no blocker.
- [ ] Real libvirt/Firecracker public prepare/prepared/submit connected to
  existing host coordinator SIGKILL matrix. First run exposed a test trust-root
  permission mismatch; fix fixture to 0700, preserve production refusal.
- [ ] Docker public transfer, mixed network/SSH and remaining fault cells.

This checkpoint does not close stage 2. Public API contract fixtures and real
provider tests inject an administrator request subject; they do not establish
production proxy authentication. Real host preparation calls the production
function directly; full CLI/supervisor deployment remains separately evidenced.
The 41 newly inventoried original requirements deliberately remain open pending
specific capability/gate reconciliation.

### Verified real-provider checkpoint

Libvirt normal transfer and six host SIGKILL boundaries pass twice:
/tmp/sysbox-stage2-libvirt-transfer-repeat-20261026.log.
Firecracker six host SIGKILL boundaries pass:
/tmp/sysbox-stage2-transfer-real-v2-20261026.log.
The latter also retains a failed libvirt before-report cell: the recovery
scanner's per-outcome 2-second budget expired during completion reporting.
Separate libvirt repetition passed twice; the mixed run is not marked passed.
Earlier permission failure: /tmp/sysbox-stage2-transfer-real-20261026.log.
Coordinator fixture changed to 0700; production trust checks remain unchanged.

Strict public gates: /tmp/sysbox-stage2-public-campaign-20261026/run.json,
source SHA256 34f1513daae6f3a6a78ae1acdb11b20c0daaba1e8a9cc84c2874be025dec70b5.
API three-backend public/process-crash race:
/tmp/sysbox-stage2-public-transfer-20261026.jsonl; no skips/failures.
Full commands race: /tmp/sysbox-stage2-cli-full-20261026.log.
Independent review of CLI, catalog, public contracts and real-provider rewiring
found no blocker. Docker public-route execution and the rest of stage 2 remain
open; this is a delivery checkpoint, not stage closure.

## Second delivery: Docker public transfer and P7 defect

- [x] Docker now follows public prepare/prepared/submit into the real provider;
  normal lifecycle and all six host SIGKILL positions pass twice, preserving
  unknown outcomes and exact command identity on submission retry.
- [x] Fixed libvirt Attach retry duplicating a logical NIC. Exact retries and
  persisted cold retries preserve one interface; changed identity/configuration
  and pre-existing duplicate logical records are refused without rewriting state.
- [x] Real Docker NAT/libvirt domain restart and Firecracker TAP/cold start
  preflight pass twice. No guest traffic or Firecracker guest boot is implied.
- [x] Current helper with real sshd rotation/delete/endpoint restrictions and
  independent administrator access passes twice. Real drop-in/reload/cold service
  recovery and configuration conflict scenarios pass twice.
- [x] Full libvirt race, strict selected contracts, independent review. Final
  real report: /tmp/sysbox-stage2-real-final-20261026/run.json, source SHA256
  ad6f9ad8eb738935eb95a3401af08c2c4b5c3cc3b6bd6148a1e44d2d4b3b56b3.

The precise next scope is docs/operations/stage2-capability-fault-ledger.md.
Stage 2 remains open: full mixed guest NAT/reset, complete original requirement
mapping and remaining fault cells. Test-only temporary user groups and private
sshd instances did not modify the host SSH service or persistent user membership.

## Recovery input handoff implementation boundary

Reuse the existing fingerprint-validated keyed Apply resubmission and incarnation
bound in-memory inputs. Permit hydration for failed recoverable Runs, but never
for completed/cancelled or nonrecoverable terminal Runs. GET keeps an explicit
inputs_required state after API restart/failover. Recheck the canonical Run and
command lease before returning transient inputs in recovery authorization;
command/outbox/journal persistence still excludes input values. Agent workspace
reconstruction must consume those authorized values instead of defaults.
An API without the rehydrated inputs returns conflict and requires resubmission;
no session affinity or durable secret storage is inferred. Legacy requests without
a verifiable request fingerprint keep the existing conservative refusal.

## Third delivery: actual mixed lifecycle and remaining functional gaps

- [x] Fixed deployed Docker adoption registration, runtime NAT proof panic and
  large-integer proof corruption, plus libvirt default-namespace convergence.
- [x] Actual CLI three-provider NAT Apply, all six guest directions, cross-host
  NAT verified at the receiving control VM, whole-topology Reset, marker absence,
  all six directions/NAT again, formal Destroy and exact successful-owner absence.
- [x] Keyed failed/recoverable input resubmission with transient exact-incarnation
  handoff, legacy host/Agent protocol refusal and no provider replay; all three
  backends plus host socket and full API/Agent/host/protocol race verified.
- [x] Real kernel ENOSPC after effect, stop receipt and report ack, two rounds;
  repeatable private-tmpfs runner. Unknown outcomes never become successful merely
  because space is available; a committed receipt retries unchanged.
- [x] Explicit capability store scopes and recovery input protocol.
- [x] P0–P4 all 34 original requirements mapped to specific existing gates/tests
  and finite remaining evidence; P7 mappings updated. Entries remain open until
  their exact current-source acceptance is reconciled.

Normal mixed lifecycle is verified, not the entire closing stage. Remaining:
repeatable enclosing mixed deployment/fault campaign, combined current-source
P0–P9 evidence (including relevant dual-API/guest/transport cells), and final review.
First-stage historic recovery guarantees and stage-3 release/main integration are
not reopened or silently claimed complete. Earlier failed isolated incarnations
remain archived with unknown journals; successful mixed-nat-v6 has no residue.
