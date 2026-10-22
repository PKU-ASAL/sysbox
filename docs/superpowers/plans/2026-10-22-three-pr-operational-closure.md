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

## Foundation progress (2026-10-22)

Implemented the diagnostic per-action plan, including candidate/replacement
identity inventory, exact observation binding, residual/attachment/guest blockers,
and authenticated observation digest in the existing recovery-plan API.
Added the explicit operator-choice contract and canonical fence verifier. Neither
validator mutates state or releases authority; no disposition commit endpoint is
claimed yet. A signed mixed report must bind settled as well as unsettled actions.

Guest task reboot termination already has a production path and API regression:
`TestHostGuestRebootReceiptClosesOldEpochWithUnknownResult`. Reuse it. Missing
original evidence, guest file outcomes and parent/reset linkage still need audit.

Actual create-absence observation currently does not emit residual coverage;
synthetic validator acceptance is not real provider acceptance. Extend the
provider observation path before exposing create-absence mutation.
