# Operational recovery closure

The user approved three ordered large PRs: operational recovery, full capability
and fault coverage, then final release integration. Existing PRs #50–52 are the
base, not substitutes for these deliverables. The first branch is
`feat/intent-disposition-closure`, based on `7b23a06`.

## Contract

An unknown result stays unknown. A disposition ends obsolete dispatch and permits
new work only after stopping every relevant effect and accounting for resources.
Neither acknowledging risk nor recording a reason is a stop proof. Old requests
cannot replay; a new epoch does not authorize an unresolved old effect.

Reuse the existing lifecycle journal, authority barriers, signed host observation
and write-ahead reconciliation. Do not add an independent workflow engine. Prefer
explicit disposition over rerunning arbitrary old commands or clearing records.

A read-only plan inventories original Run/action incarnation, provider identity,
reset candidate/replacement, guest children, stop evidence and external-effect
boundaries. It distinguishes preservation, absence, unresolved identity, conflict,
and missing coverage. Plans are evidence-bound and revalidated at commit; reading
a plan creates no state, authorizes no effect, and never clears a hold.

Run disposition archives original actions and records the operator decision.
Resource-present cases must retain exact identity in state or an explicit
quarantine that blocks conflicting creation and GC. Absence requires fresh exact
identity observation and residual coverage. Missing create receipts require
provider discovery; a missing ID cannot become an absence claim.

Reset disposition accounts for both generations and attachments independently.
It must never copy an old candidate over a new surviving generation. Retained
incomplete guest configuration is not treated as completed initialization.

Guest disposition uses exact boot/process/task identity and durable stop or scope
fencing proof. Local task termination does not prove an external service request
finished. External effects remain explicitly unresolved until separately accounted
for; unsafe automatic replay remains prohibited.

Commit must serialize admission, authority, lifecycle, dispatch and child checks.
File storage uses recoverable prepared decisions and exact pre/postimage hashes;
SQL uses existing transactional boundaries. Release is last. Crashes/retries do
not modify successors or lose archived identity/reference evidence.

## Three delivery boundaries

1. Operational closure: public read-only plan and explicit commit paths for
   Run/reset/guest, original-result preservation, exact scope release, CLI,
   storage contracts, crash recovery and real isolated acceptance.
2. Capability/fault closure: complete P0–P9 requirement ledger; public transfer,
   mixed networking/SSH and provider/transport matrix. Fix discovered defects;
   no unsupported original feature silently becomes out of scope.
3. Integration: incorporate current main, synchronize protocol/schema and all
   binaries, build runtime, run same-revision dual-host lifecycle and upgrade
   rehearsal. Deliver a reviewable PR; no implicit main merge or production deploy.

Only enrolled disposable hosts may be destructively fenced. Historical scenes
remain read-only. Document all cells as verified, missing implementation,
missing evidence, or genuinely inapplicable; skips are never successes.
