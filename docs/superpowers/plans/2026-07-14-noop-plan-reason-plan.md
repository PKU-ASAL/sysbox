# Clean No-Op Plan Reason Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ensure semantically unchanged resources produce a clean NoOp plan with no reason even when stored desired audit hashes differ.

**Architecture:** Keep typed schema diff as the only semantic change source. Change `diffDesiredState` to return an empty reason when the typed diff is empty; retain existing diagnostics for malformed payloads and existing reasons for real changes.

**Tech Stack:** Go 1.26, `testify/require`, existing runtime typed schemas and plan model.

## Global Constraints

- Desired hashes remain audit metadata only.
- No state schema, public configuration, driver contract, or provider lifecycle changes.
- No CLI-only suppression or compatibility path.

---

### Task 1: Return A Clean NoOp For Typed Equality

**Files:**
- Modify: `pkg/runtime/runtime_test.go`
- Modify: `pkg/runtime/desired.go`
- Modify: `docs/superpowers/plans/2026-07-13-guest-network-init-plan.md`

**Interfaces:**
- Consumes: `planDiffByDesiredHash(*graph.Node, *state.Resource) (controlplane.PlannedChange, error)` and `diffDesiredState(*graph.Node, *state.Resource) ([]controlplane.FieldChange, string)`.
- Produces: unchanged resources with `PlanActionNoop`, `nil` changes, and an empty reason.

- [x] **Step 1: Strengthen the existing audit-hash regression test**

Add these assertions after the existing action assertion in `TestPlanDiffDoesNotUseDesiredHashAsSemanticInput`:

```go
require.Empty(t, plan.Actions[0].Changes)
require.Empty(t, plan.Actions[0].Reason)
```

- [x] **Step 2: Run the focused test and verify RED**

Run:

```bash
go test ./pkg/runtime -run '^TestPlanDiffDoesNotUseDesiredHashAsSemanticInput$' -count=1
```

Expected: FAIL because `Reason` is `desired configuration hash changed`.

- [x] **Step 3: Implement the minimal semantic fix**

Change the empty typed-diff branch in `diffDesiredState`:

```go
if len(changes) == 0 {
	return nil, ""
}
```

- [x] **Step 4: Verify focused and package tests are GREEN**

Run:

```bash
go test ./pkg/runtime -run '^TestPlanDiffDoesNotUseDesiredHashAsSemanticInput$' -count=1
go test ./pkg/runtime
```

Expected: PASS.

- [x] **Step 5: Update completed Batch 4 plan bookkeeping**

Mark every completed checkbox in
`docs/superpowers/plans/2026-07-13-guest-network-init-plan.md` as `[x]`.
Do not alter requirements or acceptance evidence.

- [x] **Step 6: Run final verification**

Run:

```bash
go test ./...
go vet ./...
CGO_ENABLED=1 go test -race ./pkg/runtime ./pkg/state
make test-heterogeneous-matrix
git diff --check
```

Expected: all commands pass; repeated matrix plan reports exactly
`Plan: 0 to add, 0 to replace, 0 to destroy, 8 unchanged.` and contains no
`desired configuration hash changed` line.

- [x] **Step 7: Commit atomically**

```bash
git add pkg/runtime/desired.go pkg/runtime/runtime_test.go \
  docs/superpowers/plans/2026-07-13-guest-network-init-plan.md
git commit -m "fix(plan): keep semantic no-ops clean"
```
