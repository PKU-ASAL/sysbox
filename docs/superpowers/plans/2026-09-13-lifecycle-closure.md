# Lifecycle Closure Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Bind every run to immutable configuration and placement, make ownership cleanup fail closed, then harden heterogeneous provider contracts.

**Architecture:** Add a durable per-run revision snapshot and expose its path through the execution bridge. Persist topology placement and validate it before dispatch. Centralize state inventory validation so deletion and GC never treat unreadable state as empty.

**Tech Stack:** Go 1.26, existing API store abstractions, local filesystem snapshots, testify.

## Global Constraints

- No sensitive apply inputs enter durable snapshots or run/command records.
- Missing, corrupt, or unreachable state fails closed for delete and GC.
- Existing provider interfaces remain source-compatible until capability migration tasks land.
- Every behavior change gets a failing test before implementation.

---

### Task 1: Immutable run revision snapshots

**Files:** Modify `pkg/api/workspace_service.go`, `pkg/api/run_service.go`, `pkg/api/execution_bridge.go`, `pkg/agentexec/executor.go`; test `pkg/api/upsert_apply_test.go`, `pkg/api/run_service_test.go`, `pkg/agentexec/guest_operation_test.go`.

- [x] Add `WorkspaceService.MaterializeRevision(topology, revision, files) (string, error)` using `runs/<topology>/revisions/<digest>/`, validation, temp directory and rename.
- [x] Add run snapshot path to the bridge and use it in apply/repair/reset execution paths.
- [x] Keep legacy workspace reads only for old runs without a snapshot (compatibility fallback).
- [x] Add two-revision test that asserts each apply run resolves its own HCL snapshot path.
- [x] Run `go test ./pkg/api ./pkg/agentexec` (sandbox network listeners may require escalation).

### Task 2: Durable topology placement

**Files:** Modify `pkg/controlplane/model.go`, `pkg/api/api_store_interfaces.go`, store implementations under `pkg/api/`, `pkg/api/scheduler_service.go`, `pkg/api/run_service.go`; test `pkg/api/scheduler_test.go` and add placement tests.

- [ ] Define placement record with topology, agent ID, protocol, status and timestamps.
- [x] Add store load/save/CAS methods with SQLite/Postgres implementations following existing API store patterns; local backend retains the locked sidecar fallback.
- [x] On first resource creating run, claim placement atomically; on later runs require the stored agent and reject conflicting `AgentID`.
- [x] Add tests for first bind, implicit reuse, explicit conflict and concurrent bind.
- [x] Run `go test ./pkg/api ./pkg/controlplane`.

### Task 3: Fail-closed state inventory and deletion

**Files:** Modify `cmd/sysbox/commands/gc_cmd.go`, `pkg/api/workspace_service.go`; test `cmd/sysbox/commands/gc_cmd_test.go`, `pkg/api/cleanup_test.go`.

- [ ] Make `collectActiveIDs` return `(activeIDs, error)` and fail on directory, state read, or decode errors.
- [ ] Make GC refuse destructive mode when inventory is incomplete; dry-run reports the error without deleting.
- [ ] Make workspace delete return state read errors before removing state/workspace; `force=true` remains explicit.
- [x] Add tests proving corrupt/missing state preserves files and prevents deletion; retain successful orphan cleanup tests.
- [ ] Run `go test ./cmd/sysbox/commands ./pkg/api`.

### Task 4: Provider capability contract for heterogeneous networking

**Files:** Modify `pkg/substrate/capabilities.go`, `pkg/substrate/types.go`, `pkg/runtime/resource_node.go`, `pkg/provider/firecracker/nic.go`, `pkg/provider/firecracker/node.go`; test provider and runtime packages.

- [ ] Add explicit namespace and guest-network-init constraints to capabilities.
- [ ] Reject Firecracker nodes whose attachments require multiple network namespaces unless all TAPs share the VMM namespace.
- [ ] Generate guest IP initialization data for every Firecracker NIC and persist it in provider state.
- [x] Add attach-level tests for two NICs and two namespaces; plan-level capability rejection remains.
- [ ] Run `go test ./pkg/provider/firecracker ./pkg/runtime ./pkg/substrate`.

### Task 5: Unified guest connection context and real observation

**Files:** Modify `pkg/transport/ssh.go`, `pkg/transport/console.go`, `pkg/provider/libvirt/exec.go`, `pkg/provider/libvirt/network.go`; test transport and libvirt packages.

- [ ] Introduce a connection context carrying netns, endpoint, credentials, known_hosts and host-key policy.
- [ ] Route Exec, background, copy and console through the same command builder.
- [ ] Preserve strict SSH verification when configured, including inside a network namespace.
- [ ] Make libvirt attachment observation query domain XML/interface state and return Unknown on unavailable inspection.
- [ ] Add contract tests for namespace propagation, trust settings and external NIC drift.
- [ ] Run `go test ./pkg/transport ./pkg/provider/libvirt ./pkg/runtime`.

### Task 6: Full verification and documentation

**Files:** Modify `scripts/docscheck/main.go`, `docs/development/testing.md`, `docs/design/2026-09-13-project-review.md`, `docs/design/2026-09-13-heterogeneous-review.md`.

- [ ] Add new canonical review/design records to the docs checker or move them under its maintained set.
- [ ] Document placement, snapshots, ownership fail-closed behavior and heterogeneous limitations.
- [ ] Run `go test ./...`, `go vet ./...`, relevant race tests, and `make docs-test`.
- [ ] Run privileged heterogeneous acceptance only when host capabilities are present and record skipped prerequisites otherwise.
