# Three-PR execution ledger

Approved design: ../specs/2026-10-22-operational-recovery-closure-design.md.
Base: 7b23a06, existing PR #52. Execute inline, using test-first changes and
independent review at each delivery boundary.

## PR 1: operational recovery

- [ ] Add deterministic Run/reset disposition planning beside
  pkg/runtime/action.go. Tests must distinguish exact owned presence, proven
  absence, candidate/replacement ambiguity, missing identity, child work and
  composite guest effects. No planner result grants mutation permission.
- [ ] Integrate inventory into pkg/api/host_fence_recovery.go and expose
  actionable blockers; retain immutable observation/run/serial binding.
- [ ] Implement canonical disposition archives in pkg/persistence, validation,
  references and admission exclusion; preserve old result and prohibit replay.
- [ ] Add transactional/write-ahead Run and reset disposition commits, then
  authority release after exact dispatch/child retirement.
- [ ] Add guest operation disposition with task/scope stop proof and explicit
  external-effect boundaries; integrate active-operation exclusion.
- [ ] Wire public API and CLI, test all supported backends and crash points;
  real isolated runs prove a new request proceeds without replay or resource loss.
- [ ] Independent review, scoped full regression, commit/push and open PR.

## PR 2: capability and fault coverage

- [ ] Extend scripts/acceptance/campaign.json and parser to cover all original
  P0–P9 requirements, preserving source text and evidence identity.
- [ ] Reconcile actual CLI/API/provider/backend support; identify every uncovered
  original requirement before choosing tests.
- [ ] Close public transfer, mixed network/NAT/SSH and lifecycle gaps.
- [ ] Close required provider/transport/API/Agent/helper fault cells, fixing bugs.
- [ ] Review, same-source campaign, commit/push and open stacked PR.

## PR 3: final integration

- [ ] Inspect current main delta and integrate without regressing contracts.
- [ ] Synchronize capabilities, schemas, helper/guest/API/Agent and runtime build.
- [ ] Rehearse old-writer drain and evidence-preserving upgrade/rebuild in lab.
- [ ] Run full dual-host same-revision acceptance; record images, versions,
  environments and precise cleanup. Independently review release changes.
- [ ] Commit/push and open stacked PR; report all original requirements without
  implying authorization to merge or deploy production.

## Delivery ledger (2026-10-22, PR #53)

This replaces the earlier foundation-only status. PR #53 remains WIP; the broad
PR 1 checkboxes above stay open where only bounded slices have shipped.

| Deliverable | Implemented | Still required |
| --- | --- | --- |
| Evidence-bound plan | Candidate/source/replacement inventory, signed observation digest, Run incarnation and serial bindings, blocker diagnostics | Broader composite-action coverage |
| Run disposition | Public reconcile-intents, immutable archive/GC references, file write-ahead and SQL transactions, exact-image retries | Guest-task child stop → parent disposition now verified on three backends; derived SSH unknown-result linkage remains |
| Reset disposition | Exact source retain/absence before replacement; absent old source plus incomplete replacement retention | Unstarted SSH dependencies can be retained with exact old source; started SSH, source absence with SSH, missing creation identity and composite cases remain |
| Incomplete-resource obligations | Durable archive-backed rebuild/delete requirements, guarded admission/state edits, verified obligation transfer and resolution | Attachment inventory now preserved under rebuild/delete obligation; attachment absence and full real repeated recovery integration remain |
| Guest termination | Existing signed task reboot completion; scoped guest-tmpfs install reboot stops with unknown result | Real in-flight tmpfs install → VM power-off/reboot → delayed SSH callback verified; vsock and persistent storage stop proofs remain |
| Operator CLI | recovery-plan, observe, reconcile-intents; exact scope/key validation and existing proxy authentication | End-to-end operator flow in isolated lab |
| Validation | Focused backend/crash tests; real Docker/libvirt whole-VM rounds; actual tmpfs install | Current-source complete PR 1 campaign and final review |

Committed slices: edf582b (plan), 4fb0975 (Run commit), c59ec2b (Reset source),
e553d78 (incomplete resource obligations), 8af890a (scoped tmpfs guest reboot).
See docs/operations/p9-real-acceptance.md for the exact real acceptance boundaries.
The libvirt incomplete fixture is a stopped diskless domain; the tmpfs installation
and simulated reboot regression are separate evidence, not a real in-flight reboot.

Historical incidents lacking authoritative identity/stop evidence remain read-only
and unresolved. This is not permission to infer absence, replay or discard journals.
PR 2 and PR 3 have not started. Existing PRs #50–52 and this PR are not merged.
