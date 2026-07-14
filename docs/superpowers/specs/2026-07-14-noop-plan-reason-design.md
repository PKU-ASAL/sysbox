# No-Op Plan Reason Design

Date: 2026-07-14

## Goal

Remove the misleading `desired configuration hash changed` reason from
resources whose typed desired-state diff is empty. A clean repeated plan must
represent each unchanged resource with `NoOp`, no field changes, and no reason.

## Contract

Desired hashes remain audit metadata only. They do not decide plan actions or
produce user-facing semantic explanations. The typed resource schema diff is
the sole source of desired-configuration change semantics.

When prior and current desired payloads have no typed differences,
`diffDesiredState` returns:

- no field changes;
- an empty reason.

Malformed or missing prior desired payloads retain their existing diagnostic
reasons. Real typed changes retain their field-level details and replacement or
in-place reason.

## Implementation Boundary

Fix the behavior at `diffDesiredState`, where typed semantic equality is known.
Do not hide reasons in CLI rendering and do not add a plan-layer compatibility
case. This keeps API, stored plan, and CLI representations consistent.

No state schema, public configuration, driver contract, provider lifecycle, or
resource action behavior changes.

## Verification

Add a regression test with a deliberately wrong stored desired hash and an
identical typed desired payload. Assert that planning returns `NoOp`, no changes,
and an empty reason. Retain existing tests that prove genuine desired changes
produce replacement fields and reasons.

Run focused runtime tests, the full Go suite, vet, focused race tests, and the
heterogeneous matrix repeated-plan acceptance. Update completed Batch 4 plan
checkboxes as documentation bookkeeping, then commit the implementation and
verification atomically.
