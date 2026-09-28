# Observation-Driven Runtime Refactor Implementation Plan

> **For agentic workers:** Implement this plan task-by-task with a fresh test cycle at every task boundary.

**Goal:** Move Sysbox lifecycle execution toward observation-driven convergence while preserving the substrate capability model and making persistence and Firecracker recovery explicit.

**Architecture:** First fix local atomicity and Firecracker synchronization. Then introduce shared domain transitions and repository ports without forcing a universal store interface. Migrate one vertical runtime path through observe/plan/action/verify/commit, followed by Firecracker manifest adoption and execution SPI convergence. Provider specific details stay behind `driver` and `substrate` interfaces.

**Tech Stack:** Go 1.26, `testing`, `testify`, SQLite, PostgreSQL, filesystem persistence, existing Docker/Firecracker/libvirt providers.

## Global Constraints

- Existing substrate registration remains capability based through `driver.Descriptor` and `driver.Registry`.
- Unknown observations never become absent and never authorize destructive mutation.
- Provider side effects are not assumed exactly once; uncertain outcomes require observation before retry.
- Existing state/database/Firecracker formats may be rejected and rebuilt; no implicit destructive migration.
- Each task starts with a failing behavior test, then minimal implementation, then focused and full verification.
- The implementation must not introduce a permanent second execution path or dual writer for one topology.

## Task 1: Establish the P0 test and synchronization baseline

**Files:**
- Modify: `pkg/provider/firecracker/node.go`
- Modify: `pkg/provider/firecracker/pause.go`
- Test: `pkg/provider/firecracker/node_concurrency_test.go`
- Test: `pkg/provider/firecracker/pause_test.go`

**Interfaces:**
- Consumes: existing `vmMu`, `vmProcess.mu`, `StartNode`, `Pause`, `Resume`, `ObserveNode`.
- Produces: lock-safe VM snapshots and lifecycle operations that cannot race on `started`, `cmd`, `pid`, or process metadata.

- [ ] Step 1: Add a test that runs two concurrent start attempts against the same VM and asserts only one reaches child process start.
- [ ] Step 2: Add a test that concurrently observes and reaps a VM and asserts no race-visible inconsistent snapshot.
- [ ] Step 3: Add a test that pause/resume uses a stable process snapshot after the registry lock is released.
- [ ] Step 4: Run `go test -race ./pkg/provider/firecracker`; confirm the new tests fail because fields are read outside `vmProcess.mu` and start is not serialized.
- [ ] Step 5: Add a per-VM lifecycle mutex or equivalent locked transition; use snapshot helpers for reads and writes.
- [ ] Step 6: Re-run focused race tests, then `go test ./pkg/provider/firecracker`.
- [ ] Step 7: Commit `fix(firecracker): synchronize lifecycle state transitions`.

## Task 2: Make workspace publication atomic and reads revision-stable

**Files:**
- Modify: `pkg/api/workspace_service.go`
- Modify: `pkg/api/revision_snapshot.go`
- Test: `pkg/api/workspace_service_test.go`
- Test: `pkg/api/revision_directory_tree_test.go`

**Interfaces:**
- Consumes: `WorkspaceService.UpsertProject`, `MaterializeRevision`, `validateRelPath`.
- Produces: complete-tree publication where readers see one complete revision, and run inputs remain pinned to an immutable digest.

- [ ] Step 1: Add a test that injects a write failure during publication and verifies the previous workspace remains readable.
- [ ] Step 2: Add a test that publishes two revisions concurrently and verifies readers never see a mixed file tree.
- [ ] Step 3: Run `go test ./pkg/api`; confirm the tests fail because `UpsertProject` removes the live directory before writing.
- [ ] Step 4: Stage the complete tree under a unique directory, fsync files and directory where supported, and atomically update a current revision marker.
- [ ] Step 5: Make readers resolve one marker/digest before reading all files.
- [ ] Step 6: Add cleanup protection for active run and revision references.
- [ ] Step 7: Re-run focused tests and `make docs-test`.
- [ ] Step 8: Commit `fix(api): publish workspace revisions atomically`.

## Task 3: Stop silently hiding local persistence corruption

**Files:**
- Modify: `pkg/api/api_store.go`
- Modify: `pkg/api/api_store_runs.go`
- Modify: `pkg/api/guest_execution_store.go`
- Modify: `pkg/api/guest_file_operation_store.go`
- Test: `pkg/api/local_store_corruption_test.go`

**Interfaces:**
- Consumes: local object readers and local CAS implementations.
- Produces: typed `CorruptRecord` and read errors that callers can distinguish from `NotFound`.

- [ ] Step 1: Add tests for malformed JSON, unreadable record, invalid glob, and missing record.
- [ ] Step 2: Run `go test ./pkg/api`; confirm malformed records currently disappear from list results.
- [ ] Step 3: Introduce shared local record reading that returns path-aware errors and preserves `errors.Is` for not-found.
- [ ] Step 4: Update local list/Get/CAS callers to propagate corruption instead of skipping it.
- [ ] Step 5: Add diagnostics that identify the affected record without exposing sensitive payloads.
- [ ] Step 6: Run focused tests and full `go test ./...`.
- [ ] Step 7: Commit `fix(api): surface local persistence corruption`.

## Task 4: Introduce shared domain storage semantics

**Files:**
- Create: `pkg/persistence/errors.go`
- Create: `pkg/persistence/codec.go`
- Create: `pkg/persistence/cas.go`
- Create: `pkg/persistence/repository.go`
- Create: `pkg/persistence/contract_test.go`
- Modify: `pkg/api/api_store_interfaces.go`
- Modify: `pkg/api/api_store.go`
- Test: `pkg/persistence/*_test.go`

**Interfaces:**
- Consumes: current API store interfaces and local/SQLite/Postgres record models.
- Produces: shared error categories, version transitions, action identity, and backend contract tests. Backend adapters remain responsible for transactions and queries.

- [ ] Step 1: Define public errors `NotFound`, `Conflict`, `CorruptRecord`, `UnsupportedVersion`, `Unavailable` and helpers for `errors.Is`/`errors.As`.
- [ ] Step 2: Add tests proving error classification and CAS transition rules before any adapter migration.
- [ ] Step 3: Define aggregate-specific ports (`RunRepository`, `ExecutionRepository`, `ActionRepository`) instead of a universal generic update callback.
- [ ] Step 4: Add versioned codec helpers that validate schema and redact sensitive public values.
- [ ] Step 5: Write a backend contract suite with an in-memory test adapter only for semantic tests; do not use it as production storage.
- [ ] Step 6: Run the persistence package tests and verify current packages still compile.
- [ ] Step 7: Commit `feat(persistence): define shared storage semantics`.

## Task 5: Migrate one end-to-end run path to observe/plan/action/verify/commit

**Files:**
- Modify: `pkg/runtime/plan.go`
- Modify: `pkg/runtime/refresh.go`
- Modify: `pkg/runtime/executor.go`
- Modify: `pkg/runtime/transaction.go`
- Modify: `pkg/runtime/execution_journal.go`
- Create: `pkg/runtime/action_runner.go`
- Create: `pkg/runtime/observation.go`
- Test: `pkg/runtime/action_runner_test.go`
- Test: `pkg/runtime/recovery_convergence_test.go`

**Interfaces:**
- Consumes: `ResourceHandler.Read/PlanDiff/Create/Delete`, `OperationRecorder`, `StatePatchSink`, `RecoveryPolicy`.
- Produces: one action runner used by normal apply and journal recovery. It persists intent, invokes one provider action, observes postconditions, and commits state only after verification.

- [ ] Step 1: Add tests for no-op after healthy observation, missing node create, uncertain create recovery, unknown observe blocking destroy, and stale plan conflict.
- [ ] Step 2: Run focused runtime tests; confirm current executor/recovery paths do not satisfy all cases.
- [ ] Step 3: Define immutable `ActionIntent`, `ActionOutcome`, `ObservationSnapshot` and action pre/postcondition types.
- [ ] Step 4: Implement `ActionRunner.Run` with intent persistence, provider call, postcondition observe, CAS commit, and uncertain outcome handling.
- [ ] Step 5: Route one node resource handler through the runner while keeping existing handlers as adapters.
- [ ] Step 6: Route checkpoint recovery through the same runner decision path.
- [ ] Step 7: Run focused and full runtime tests; add failure injection around provider completion and commit.
- [ ] Step 8: Commit `feat(runtime): converge lifecycle actions through observation`.

## Task 6: Add Firecracker durable manifest and cross-process adoption

**Files:**
- Create: `pkg/provider/firecracker/manifest.go`
- Create: `pkg/provider/firecracker/process_probe.go`
- Modify: `pkg/provider/firecracker/node.go`
- Modify: `pkg/provider/firecracker/pause.go`
- Modify: `pkg/provider/firecracker/reset.go`
- Test: `pkg/provider/firecracker/manifest_test.go`
- Test: `pkg/provider/firecracker/adoption_test.go`

**Interfaces:**
- Consumes: `HandleState`, process anchors, existing cold `ObserveNode`, `AdoptNode`, `killOwnedStaleFirecracker`.
- Produces: versioned manifest store, verified process/socket ownership, provider-local runtime registry, and cross-process lifecycle support.

- [ ] Step 1: Add tests for manifest atomic write/read, schema rejection, path ownership, PID start-time mismatch, socket mismatch, and orphan classification.
- [ ] Step 2: Run focused tests; confirm no manifest contract currently exists.
- [ ] Step 3: Implement manifest format with resource UID, generation, action ID, config digest, network anchors, process anchor, and observed status.
- [ ] Step 4: Implement process probe using exact executable/argv and PID start-time checks; avoid substring-only `pgrep` ownership.
- [ ] Step 5: Implement `RuntimeRegistry` with map lock, per-VM lifecycle lock, and immutable snapshots.
- [ ] Step 6: Route Start/Observe/Adopt/Pause/Resume/Stop/Destroy through the registry and manifest.
- [ ] Step 7: Add a subprocess-style test that writes a manifest, constructs a fresh substrate instance, and adopts the running test process fixture.
- [ ] Step 8: Run `go test -race ./pkg/provider/firecracker ./pkg/runtime` and commit `feat(firecracker): recover VMs from durable manifests`.

## Task 7: Unify node execution semantics and protocol versioning

**Files:**
- Modify: `pkg/substrate/types.go`
- Modify: `pkg/substrate/guest.go`
- Modify: `pkg/driver/capability.go`
- Modify: `pkg/provider/docker/exec.go`
- Modify: `pkg/provider/firecracker/exec.go`
- Modify: `pkg/provider/libvirt/exec.go`
- Modify: `pkg/agentexec/guest_operation.go`
- Modify: `pkg/controlplane/model.go`
- Modify: `pkg/api/handler_agent.go`
- Test: `pkg/substrate/execution_contract_test.go`
- Test: provider execution tests for Docker, Firecracker, and libvirt

**Interfaces:**
- Consumes: `substrate.Connection`, `driver.GuestExec`, `GuestExecution`, agent protocol `agent.v1`.
- Produces: `ExecutionHandle`, explicit result classes, protocol v2 negotiation, and one shared execution behavior.

- [ ] Step 1: Add contract tests for exit code, timeout, cancellation, output cap, background handle, and unsupported cancellation.
- [ ] Step 2: Run focused tests and record current semantic differences.
- [ ] Step 3: Add `ExecutionHandle`, result class, and execution capability declarations without changing HCL.
- [ ] Step 4: Implement adapters so GuestExec delegates to Connection where semantics match.
- [ ] Step 5: Add protocol v2 fields for action ID, resource UID, generation, lease epoch, deadline, and result class.
- [ ] Step 6: Implement v1/v2 negotiation and reject v2 mutation on v1 agents.
- [ ] Step 7: Run full unit, race, vet, docs, and provider integration checks.
- [ ] Step 8: Commit `feat(execution): unify guest execution contract`.

## Task 8: Rebuild format boundaries and finish migration

**Files:**
- Modify: `pkg/state/state.go`
- Modify: `pkg/api/api_store.go`
- Modify: SQLite/Postgres schema initialization files
- Modify: `pkg/provider/firecracker/manifest.go`
- Modify: `docs/operations/upgrades-and-recovery.md`
- Create: `cmd/sysbox/commands/reset_state_cmd.go`
- Test: schema/version rejection and scoped reset tests

**Interfaces:**
- Consumes: shared persistence semantics, protocol v2, Firecracker manifest generation.
- Produces: explicit format generations, scoped reset command, startup refusal for old formats, and no implicit destructive migration.

- [ ] Step 1: Add failing tests for old state/API schema/manifest rejection and scoped reset safety.
- [ ] Step 2: Implement generation checks and typed `UnsupportedVersion` errors.
- [ ] Step 3: Implement scoped reset with dry-run inventory and ownership checks.
- [ ] Step 4: Remove old dual writer and legacy recovery paths only after all resource types use the new runner.
- [ ] Step 5: Update operational docs and examples.
- [ ] Step 6: Run `go test ./...`, `go test -race ./...`, `go vet ./...`, `make docs-test`, and configured integration suites.
- [ ] Step 7: Commit `feat: switch to observation-driven runtime generation`.
