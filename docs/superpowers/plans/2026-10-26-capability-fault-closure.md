# Stage 2 capability and fault closure

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
