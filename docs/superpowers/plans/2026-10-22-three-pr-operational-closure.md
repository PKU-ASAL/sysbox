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
| Run disposition | Public reconcile-intents, immutable archive/GC references, file write-ahead and SQL transactions, exact-image retries | Guest-task child stop → parent disposition now verified on three backends; derived SSH child stop and committed/lost-result publication are implemented; genuinely unknown-result residue accounting remains |
| Reset disposition | Exact source retain/absence before replacement; absent old source plus incomplete replacement retention | Unstarted SSH dependencies can be retained with exact old source; recorded and discoverable lost committed SSH result publication implemented; no-receipt SSH, attachment absence, missing creation identity and composite cases remain |
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

## Derived SSH cessation slice (2026-10-23)

- [x] Guest scope/store locked stop, exact committed-history check and durable
  unknown stop with no old request replay.
- [x] Scope format 3 blocks old helpers already queued on the same lock; current
  helper supports stop retry after scope-fence/tombstone crash and next epoch.
- [x] Distinct host signed unknown stop, canonical target revalidation and
  release of exact derived child grant, without declaring SSH configuration success.
- [x] Temporary authority failures remain pending; only HTTP 409 grants the host
  the obsolete-request stop path.
- [x] Independent review, five-package full race regression, four same-source
  machine gates, and real SSH/helper delayed-original/successor acceptance.

Still required: started ResetSSH inventory reconciliation in the parent
transaction; this child-stop proof is necessary but does not settle that metadata.
The other first-stage remaining items above retain their scope.


## Committed ResetSSH publication slice (2026-10-23)

- [x] Read-only observation of old node absence/residue, replacement presence and
  each journal-recorded SSH result against the staged replacement.
- [x] Evidence-bound planner and atomic node/SSH publication on file/SQLite/PG;
  preserve uncertain archive/outcome and exact previous/result GC references.
- [x] Prepared-write recovery, exact retry, changed canonical state rejection;
  independent review and three same-source contract gates with no skips.

The old "started ResetSSH" gap now splits into recorded-result publication
(implemented) and missing-result recovery/unknown residue accounting (open).
This does not close PR 1 or its broad checkboxes. No real full Reset acceptance
or other remaining first-stage functionality is implied by backend contracts.

### Lost committed SSH result implementation boundary

Continue the approved read-only evidence path: discover exact committed guest
receipts, bind them into signed nested observations, construct a detached
publication candidate, and keep the original journal's missing Result unchanged.
Reuse the preceding atomic Reset publication and authority checks. A missing
receipt is never an absence proof. Initially accept only unchanged pinned SSH
configuration, for which old desired metadata is still authoritative; changed
configuration without original metadata remains blocked. Tests cover identity,
no mutation/replay, signed snapshot/crash recovery and retained GC references.


The lost-result boundary above is implemented: exact guest receipt discovery,
signed nested evidence, detached candidate publication and archive-based GC
references. Runtime observation, signed prepared-write crash/retry, and public
file/SQLite/PostgreSQL API contracts pass. Original journal and archive keep nil
Result. Changed configuration without original metadata and genuinely unknown
SSH results are still open, as are the other PR 1 integration requirements.


Next functional gaps, in order: account for stopped SSH children with no committed
receipt and changed-config metadata loss; cover old/new attachment absence and
composite Reset generation inventory; discover unrecorded creation identities;
then persistent-file/vsock cessation and repeated full operator integration.
Each requires its own evidence and implementation; none is declared closed by
the recorded/discoverable-result work.

## Fixed stage 1 closure checklist (2026-10-23)

These are the remaining approved goals, not new delivery stages. Commits are
checkpoints; implementation continues until this checklist passes or an external
blocker is established. Historical evidence must never be fabricated.

1. [x] Unknown ResetSSH: retain the exact replacement under a rebuild/delete
   obligation; archive old SSH records and any results; remove stale live SSH
   metadata atomically. Require old source/residue absence, complete child
   inventory and canonical cessation of every grant. Test postimage, signed
   prepared retry, all backends, refusal of ordinary work, then explicit recovery.
2. [x] Changed-config lost SSH Result: recover original desired metadata from
   immutable admitted intent/revision rather than infer it from an older resource.
3. [ ] Attachment/composite Reset inventory: account independently for both
   generations and every attachment; verify actual provider absence/residue SPI.
4. [ ] Missing creation identity: owning-provider discovery with exact ownership;
   ambiguous/legacy scenes remain unresolved, with actionable diagnostics.
5. [ ] Persistent-file and vsock cessation: enrolled isolated execution-VM fence,
   exact storage/process boundary and delayed-call rejection; no remote-storage
   claims from mere guest reboot.
6. [ ] Full repeated operator flow: public CLI/API observation/disposition,
   rebuild/delete and fresh work; retain unknown history across repeated crashes.
7. [ ] Current-source stage 1 campaign, independent review and accurate PR scope.

Unknown SSH uses the existing `retain-incomplete` decision and node recovery
requirement. It does not fabricate SSH handles. Old SSH metadata is removed only
with an exact absent old node; archived dependencies keep their GC references.
The replacement, including all potentially unknown local SSH configuration, is
retained and cannot serve ordinary work until verified reset/destroy. Original
Result/status remain unchanged. This follows the approved incomplete-resource
contract; no new workflow engine or historical destructive action is introduced.


Items 1–2 passed independent review, same-source contracts and complete affected
package race regression. Item 1 includes actual backend obligation resolution by
successful delete, fresh admission and unchanged old-decision retry. Real enclosing
Reset/operator acceptance remains item 6; it is not implied by these contracts.


Items 3–4 implementation now includes attachment-count/residue coverage, old
source proof versus retained replacement attachments, and exact missing-create
identity discovery from pinned default immutable inputs. Independent review found
and verified fixes for omitted nested count and detached link residue. Contract
and isolated real Docker/network evidence passed. Remaining boundaries are
composite missing-create inventory, provider cells without exact residue proof,
and full repeated operator integration; keep the broad items open until those
cells have explicit evidence. Missing IDs without authoritative evidence remain
unknown by contract, never automatically absent or replayable.


Persistent file cessation implementation path: same-boot, exact signed request,
durable cancellation of commit, scope-format-3 fence and exclusive writer-lock
handoff; preserve original uncertain install record and a separate stop tombstone.
Only guest-kernel-local tmpfs/ext4 syscall scope may use it. No NFS/FUSE/overlay
or changed-boot stop inference. Host/API receipts must distinguish stopped-unknown
from committed/cancelled. Verify delayed originals, crash-between-writes, active
writer timeout, exact cold retry, successor and SSH/vsock transport equivalence.
This is an additional stop route within item 5, not its reboot/remote-storage
completion. Historical scenes remain untouched.

Item 5 now has an implemented same-boot local persistent-file cessation route,
with exact original grant, local target/control FS validation, separate unknown
stop history, and delayed-original/successor proof on real isolated ext4/SSH.
Independent review corrections and three-backend API acceptance passed. Full
API race regression passed (235.154s), along with six affected core packages.
True vsock/reboot and repeated enclosing operator acceptance remain open; the
isolated execution VM has KVM/vhost-vsock available for the next acceptance step.

Next provider coverage gap: Firecracker has exact durable local manifests and
attachment identities, but no host-fence locality/residue SPI, so intent
observation cannot release even a fully absent generation. Reuse its existing
canonical path/process checks; require local tmpfs/ext4 runtime storage, exact
persisted generation identity, and absence of both API/vsock sockets. TAP proof
must match the persisted owned link and generation alias, never infer link
absence solely from VMM exit. Do not add a destructive recovery operation.

Item 5 now additionally has repeated real KVM/vsock same-boot persistent-file
unknown cessation, with current-helper provenance and distinct successor bytes.
Item 3 adds Firecracker exact generation/socket/TAP residue SPI and runtime
identity propagation. Real TAP hold/deletion and file-stop each passed twice on
the enrolled execution VM. Independent review fixed the missing-directory identity
substitution bug. Enclosing full operator/reboot and composite discovery remain.
