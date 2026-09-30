# Observation-Driven Runtime Refactor Implementation Plan

> **For agentic workers:** Implement this plan task-by-task with a fresh test cycle at every task boundary. Tasks 4–8 are the migration roadmap and require a focused executable plan at their respective boundaries.

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

## Progress — 2026-09-28

- P0 / Tasks 1–3 implemented: Firecracker entrypoint serialization and reaper ownership; immutable workspace publication and retained pinned revisions; strict local object/JSONL reads, atomic object replacement, locked append/read, and dispatch failure propagation.
- Actual commits: `dbb0ff3`, `be0b965`, `761322b`. The earlier `004a4a7` helper-only change was insufficient; full behavioral coverage arrived in `dbb0ff3`.
- Verified P0: `go test ./...`, `go test -race ./...`, `go vet ./...`, `make docs-test`. No real hypervisor/network/Postgres integration run is claimed.
- The Firecracker fix is in-process synchronization. Cross-process VMM ownership, durable manifest and schema replacement remain later work; workspace publication and JSONL synchronization already use process-shared locks.
- Transitional journal guard implemented and independently reviewed: enforce persistence before existing runtime mutations, suppress destructive cleanup/rollback after journal failure, prevent Agent whole-state saves bypassing the failed patch, and propagate final commit errors. This strengthens the old executor as a bridge toward Task 5; it does not claim a completed observation-driven engine or unified storage transaction.
- Tasks 4–8 are in progress, not wholly complete. The lifecycle aggregate repository, default convergence engine, Firecracker manifest and common execution primitives are implemented in the refactor worktree; API metadata migration, remaining provider capabilities and production validation are still open. See the [implementation status](../../design/2026-09-28-runtime-refactor-implementation-status.md) for the actual boundary. New API enums/protocol versions from the interface inventory remain design candidates.

### Integration hardening in the current implementation

Implementation commit: `72e227a`. Final verification passed with `go test ./... -count=1`, `go test -race ./... -count=1`, `go vet ./...`, `make docs-test`, and `git diff --check` using `GOCACHE=/tmp/sysbox-gocache`. Socket-enabled tests use temporary local fixtures; no physical substrate or live Postgres deployment was exercised.

- [x] Share lifecycle state/action transactions across filesystem, SQLite and Postgres adapters.
- [x] Add action revision CAS independently of resource serial; reject regressive recovery evidence.
- [x] Reject malformed inner aggregate identities and discard unknown SQL transaction sessions.
- [x] Unify CLI/Agent/API backend selection; persist relative SQLite paths; separate catalog enumeration from topology-bound access.
- [x] Protect metadata deletion with serial/owner CAS, unfinished-action checks and stable lock inode retention.
- [x] Persist Firecracker ownership/process manifests; validate reset old/target contexts across cold recovery.
- [x] Scope GC previews to verifiable owners and disable unfenced automatic deletion.
- [x] Route canonical API recovery/cleanup and supervisor through the pinned observation runner; reject wrong-host recovery and checkpoint-only authority.
- [x] Run repository-wide unit tests, race tests, vet, documentation checks and diff checks successfully.
- [x] Preserve transport truncation flags in checks; classify route probe errors as unknown rather than drift.
- [x] Share route convergence between create and reset; require composite guest/image-entry completion evidence and propagate image-entry failures.
- [x] Reject unknown exit codes in generic SSH setup and console fallback; drain console output before reporting completion.
- [ ] Migrate remaining API metadata codecs/transitions, remote recovery dispatch and unsupported resource observations.
- [ ] Verify real Postgres and substrate end-to-end deployments before release.

## Completed vertical slice: Guest operation metadata repositories

Implementation commit: `3679f21`. Independent review issues were reproduced and fixed. Full uncached unit/race suites, vet, docs and diff checks pass. Postgres contract tests are opt-in via `SYSBOX_TEST_POSTGRES_DSN`; no server was configured in this run.

**Goal:** Move GuestExecution and GuestFileOperation persistence out of the three API adapters while preserving public HTTP shapes, statuses and private dispatch fields.

**Architecture:** A typed `persistence.GuestRepository` owns record codecs, immutable bindings, version increments, terminal-state rules and acceptance aggregation. Private byte-record adapters perform only filesystem/SQL read, list and conditional replacement. Existing API adapters delegate through small typed methods, reusing their existing database pools; no second storage location or dual writer is introduced.

**Files:** Create `pkg/persistence/guest_repository.go`, `guest_codec.go`, `guest_records_file.go`, `guest_records_sql.go`, `guest_repository_test.go`; replace the duplicated implementations in `pkg/api/guest_execution_store.go` and `guest_file_operation_store.go`; add API contract and cross-process tests.

**Contract:** `SaveGuestExecution` / `SaveGuestFileOperation` create or accept an identical replay, never overwrite a changed record. CAS uses the caller's expected version, increments once, returns `(false,nil)` on a stale version and a typed not-found on a missing ID. A terminal record cannot restart or change its result; expiry redaction may only remove request/stdout/stderr. ID, topology, node and agent binding are immutable. CommandID may bind once. File cancel requests are monotonic. Corrupt/unsupported records and I/O errors propagate without exposing payloads.

- [x] Add failing API contract tests for terminal regression, immutable binding, create overwrite, invalid revision and missing-record classification against local and SQLite stores.
- [x] Implement typed codecs/transition validation and physical adapters; local CAS uses stable process-shared locks and SQL CAS locks/reads/validates/writes in one transaction.
- [x] Route both API aggregates and LatestGuestAcceptance through the shared repository, preserving DTO redaction and service behavior.
- [x] Add cross-process local CAS, reopen, corruption, cancellation, list-error and atomic rejected-write coverage; add the opt-in isolated Postgres contract (not run here).
- [x] Obtain an independent review, run full unit/race/vet/docs verification, update implementation status and commit without merging or pushing.

## Completed vertical slice: Run writes, leases and execution rounds

Implementation commit: `d581b80`. Independent review and full uncached unit/race/vet/docs checks passed. PostgreSQL runtime contracts remain skipped without an isolated test DSN.

**Goal:** Share ordinary writes, explicit restart, claim, renewal and expiry across all run stores while retaining their run/dispatch transaction boundary. Agent/command metadata and a complete dispatch outbox remain open.

**Architecture:** `persistence.RunRepository` owns typed transitions over the `RunStorage.UpdateRun` atomic port. Adapters lock, read the current full run, validate and write one result. Ordinary Save cannot change lease authority or overwrite terminal outcomes. Restart is explicit. SQLite uses its latest row and validates full JSON against every index column. Supervisor expiry never performs a second Save.

**Files:** Added `pkg/persistence/run_repository.go`, `pkg/api/run_record_adapters.go`, domain/contract/process tests; modified API adapters, Jobs, scheduler, completion handling and dispatch serialization. HTTP DTO shapes and lease fields remain unchanged.

**Contract:** Empty identities, nonpositive TTL, expired deadline and exhausted attempt counters cannot authorize a claim. A lease must still be running, unexpired, and owned by the same agent/owner to renew. Renewal never shortens an existing lease. Expiry tests the current durable lease while locked and never overwrites a concurrent renewal. Transient inputs never enter a durable run through these paths.

- [x] Reproduce invalid TTL, expired renewal, stale SQLite row and dispatch snapshot/deadline defects.
- [x] Route all Run writes through shared guards; explicit restart isolates reused IDs by QueuedAt.
- [x] Publish cache/projection only after persistence; require current claim proof in completion reports.
- [x] Bind sorting, coalescing, Inputs and atomic claim preconditions to the current incarnation.
- [x] Verify independent-process claims have one winner, stale expiry cannot overwrite renewal, and commit failure grants no lease.
- [x] Complete independent review and uncached full unit/race/vet/docs checks. PostgreSQL remains an opt-in skipped runtime contract.

## Next milestone: Agent, command and durable dispatch

**Goal:** Unify Agent/command persistence semantics, then atomically commit Run/Guest intents with their queued commands across local, SQLite and Postgres stores.

**Architecture:** Typed AgentRepository and CommandRepository own identity, normalization, state transitions and lease rules. Backend adapters only lock/read/write. Command IDs and payload bindings remain immutable, expired delivery leases can be retried, and retirement leaves evidence that prevents a bundled command from reappearing. Existing run dispatch bundles provide the filesystem atomic boundary; Guest dispatch records gain the same aggregate boundary. SQL commits both records in one transaction. The existing command poller is the durable outbox dispatcher; in-memory notification is only a wake-up hint.

**Files:** Add shared repositories and API physical adapters; modify AgentService, command stream delivery, RunService/SchedulerService, Guest services and their dispatch stores; extend backend contracts and restart tests. HTTP v1 error shape and existing capability registration remain stable.

- [x] Reproduce command payload replacement, invalid lease TTL, stranded leased records, concurrent claims and retired-bundle resurrection.
- [x] Centralize command state rules and strict adapters; persist delivery authorization and reject authenticated stream identity mismatches.
- [x] Independently review the command slice and pass full unit/race/vet/docs/diff checks; keep Agent and full outbox outside this checkpoint.
- [x] Centralize Agent state rules; prevent stale heartbeat/offline updates from overwriting policy.
- [ ] Commit operation plus command atomically for Run and Guest paths; use durable dispatch on retries/restarts and propagate storage failures before delivery.
- [ ] Cover payload privacy, cancellation, command ownership, cross-process races and outbox crash points; run independent review and required checks.
- [ ] Update actual completion boundaries before moving to resource fencing and owning-agent recovery.

### Active Agent repository slice — 2026-09-30

Shared `persistence.AgentRepository` owns validated codecs and explicit Register, Save, Heartbeat, UpdatePolicy and ExpireHeartbeat operations. Register preserves existing policy and requires the current credential for protected identities; Save is internal administrative replacement. Atomic callbacks re-read current policy/liveness before mutation; FS uses its stable record lock, SQLite BEGIN IMMEDIATE, Postgres row locks. Existing agent paths/tables stay authoritative.

- [x] Add and run failing backend/service tests for incomplete SQLite updates, cached authorization on storage errors, stale offline scans and heartbeat policy/credential replacement.
- [x] Implement `pkg/persistence/agent_repository.go`, `pkg/api/agent_records_{file,sqlite,postgres}.go` and thin delegates; validate identifiers, status and persisted records.
- [x] Route heartbeat, registration and policy handlers through typed atomic operations; return list/read errors to scheduling and HTTP callers instead of registry fallback.
- [x] Add concurrent policy/heartbeat, corruption, restart, cross-process and failed-commit regressions; review independently and pass full unit/race/vet/docs checks.
- [x] Document actual guarantees and checkpoint this slice before implementing operation/command outbox.

## Task 1: Establish the P0 test and synchronization baseline

**Files:**
- Modify: `pkg/provider/firecracker/node.go`
- Modify: `pkg/provider/firecracker/pause.go`
- Test: `pkg/provider/firecracker/node_concurrency_test.go`
- Test: `pkg/provider/firecracker/destroy_test.go` (pause/resume concurrency is covered in `node_concurrency_test.go`)

**Interfaces:**
- Consumes: existing `vmMu`, `vmProcess.mu`, `StartNode`, `Pause`, `Resume`, `ObserveNode`.
- Produces: lock-safe VM snapshots and lifecycle operations that cannot race on `started`, `cmd`, `pid`, or process metadata.

- [x] Step 1: Add a test that runs two concurrent start attempts against the same VM and asserts only one reaches child process start.
- [x] Step 2: Add a test that concurrently observes and reaps a VM and asserts no race-visible inconsistent snapshot.
- [x] Step 3: Add a test that pause/resume uses a stable process snapshot after the registry lock is released.
- [x] Step 4: Run `go test -race ./pkg/provider/firecracker`; confirm the new tests fail because fields are read outside `vmProcess.mu` and start is not serialized.
- [x] Step 5: Add a per-VM lifecycle mutex or equivalent locked transition; use snapshot helpers for reads and writes.
- [x] Step 6: Re-run focused race tests, then `go test ./pkg/provider/firecracker`.
- [x] Step 7: Commit `fix(firecracker): synchronize lifecycle state transitions`.

## Task 2: Make workspace publication atomic and reads revision-stable

**Files:**
- Modify: `pkg/api/workspace_service.go`
- Modify: `pkg/api/revision_snapshot.go`
- Create: `pkg/api/workspace_publication.go`
- Test: `pkg/api/revision_directory_tree_test.go`

**Interfaces:**
- Consumes: `WorkspaceService.UpsertProject`, `MaterializeRevision`, `validateRelPath`.
- Produces: complete-tree publication where readers see one complete revision, and run inputs remain pinned to an immutable digest.

- [x] Step 1: Add a test that injects a write failure during publication and verifies the previous workspace remains readable.
- [x] Step 2: Add a test that publishes two revisions concurrently and verifies readers never see a mixed file tree.
- [x] Step 3: Run `go test ./pkg/api`; confirm the tests fail because `UpsertProject` removes the live directory before writing.
- [x] Step 4: Stage the complete tree under a unique directory, fsync files and directory where supported, and atomically update a current revision marker.
- [x] Step 5: Make readers resolve one marker/digest before reading all files.
- [x] Step 6: Add cleanup protection for active run and revision references.
- [x] Step 7: Re-run focused tests and `make docs-test`.
- [x] Step 8: Commit `fix(api): publish workspace revisions atomically`.

## Task 3: Stop silently hiding local persistence corruption

**Files:**
- Modify: `pkg/api/api_store.go`
- Modify: `pkg/api/api_store_runs.go`
- Create: `pkg/api/local_records.go`
- Modify: `pkg/api/api_store_agents.go`
- Modify: `pkg/api/run_dispatch_store.go`
- Test: `pkg/api/local_store_corruption_test.go`
- Existing guest execution/file CAS paths consume the strict common reader without duplicated edits.

**Interfaces:**
- Consumes: local object readers and local CAS implementations.
- Produces: typed `CorruptRecord` and read errors that callers can distinguish from `NotFound`.

- [x] Step 1: Add tests for malformed JSON, unreadable record, invalid glob, and missing record.
- [x] Step 2: Run `go test ./pkg/api`; confirm malformed records currently disappear from list results.
- [x] Step 3: Introduce shared local record reading that returns path-aware errors and preserves `errors.Is` for not-found.
- [x] Step 4: Update local list/Get/CAS callers to propagate corruption instead of skipping it.
- [x] Step 5: Add diagnostics that identify the affected record without exposing sensitive payloads.
- [x] Step 6: Run focused tests and full `go test ./...`.
- [x] Step 7: Commit `fix(api): surface local persistence corruption`.

## Transitional Runtime Barrier (Before Task 4/5)

- [x] Reproduce full Apply/Destroy calls executing provider effects after intent persistence fails.
- [x] Retain FileRecorder write errors and expose them through StoreRecorder, including store-less CLI mode.
- [x] Check journal health before mutations and state patch commits; halt subsequent work and unjournaled rollback after journal failure.
- [x] Propagate deletion state-patch failures and the final Finish write failure to the Executor caller.
- [x] Verify runtime/agentexec tests and full repository race suite.
- [x] Commit after independent review.

This slice retains the current checkpoint schema and does not implement the new action identity/state machine, provider postcondition reconciliation, unified state/journal transaction, or cross-process VMM fencing.

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
