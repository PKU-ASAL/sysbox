# Provider In-Flight Quiescence Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Ensure an interrupted provider request is never treated as complete unless the provider returns durable evidence or exposes a verified quiescence proof.

**Architecture:** The runtime persists the provider operation identity with each action. Substrates optionally implement a quiescence SPI that proves the exact operation is no longer in flight and binds the proof to the action identity. Recovery requires that proof before committing an outcome for an action whose provider call did not return.

**Tech Stack:** Go, substrate interfaces, runtime action journal, table-driven unit tests.

## Global Constraints

- Lease expiry, controller restart, resource existence, and resource absence are not quiescence proofs.
- Historical intent-only records remain unknown and are never upgraded automatically.
- Provider deletion and creation are never replayed by recovery.

### Task 1: Persist operation identity and proof state

**Files:**
- Modify: `pkg/runtime/action.go`
- Modify: `pkg/runtime/convergence.go`
- Test: `pkg/runtime/quiescence_recovery_test.go`

- [ ] Add `ProviderOperationID`, `ProviderOperationAnchor`, and `Quiescence` fields to `ActionRecord`.
- [ ] Set the operation ID from the action ID before provider effects and persist it with the intent.
- [ ] Mark returned provider calls as `returned`; leave interrupted calls as `unknown`.
- [ ] Add a failing recovery test proving an absent resource does not close an interrupted delete without proof.
- [ ] Implement the minimal guard and run the focused test.

### Task 2: Add the substrate quiescence SPI

**Files:**
- Create: `pkg/substrate/quiescence.go`
- Modify: `pkg/runtime/action_recovery.go`
- Test: `pkg/substrate/quiescence_test.go`

- [ ] Define `QuiescenceProof`, `OperationQuiescer`, validation, and a conservative unsupported error.
- [ ] Require identity, operation ID, and provider evidence digest in validation.
- [ ] Have recovery call the SPI only for records with `Quiescence == "unknown"`.
- [ ] Reject unsupported or mismatched proofs with `ErrActionUncertain`.

### Task 3: Wire provider adapters conservatively

**Files:**
- Modify: `pkg/provider/docker/docker.go`
- Modify: `pkg/provider/firecracker/firecracker.go`
- Modify: `pkg/provider/libvirt/libvirt.go`
- Test: provider-specific quiescence tests

- [ ] Return `ErrQuiescenceUnsupported` unless a provider-specific durable daemon anchor exists.
- [ ] Keep existing successful-return and independent-observation recovery paths unchanged.
- [ ] Add tests that unsupported providers fail closed without making mutations.

### Task 4: Full verification

- [ ] Run focused runtime/substrate/provider tests.
- [ ] Run `go test ./pkg/runtime ./pkg/substrate ./pkg/provider/...`.
- [ ] Commit the bounded change with a clear message.
