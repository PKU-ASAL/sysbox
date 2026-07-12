# Resource Handler And Capability Driver Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace ResourceProvider/Substrate overlap with resource-semantic handlers and small capability drivers, leaving runtime free of concrete infrastructure commands.

**Architecture:** `ResourceHandler` owns schema, validation, plan, state normalization, lifecycle, and import. `pkg/driver` owns capability interfaces, registry, stable error categories, and driver selection. Docker/libvirt/Firecracker/Linux-network implementations register only capabilities they implement.

**Tech Stack:** Go 1.26, existing HCL/cty schema, Docker/libvirt/Firecracker/Linux networking implementations.

## Global Constraints

- No Provider/Handler compatibility aliases or permanent dual registry.
- Runtime and application services do not import concrete driver packages or execute infrastructure commands.
- Unsupported capability combinations fail during validation/planning before mutation.
- Import is handler-owned and saves normalized state with CAS.
- Every task follows verified RED/GREEN, full tests, audit, and atomic commit.

### Task 1: ResourceHandler And Driver Registry

**Files:** create `pkg/driver/capability.go`, `registry.go`, `errors.go`; rewrite `pkg/runtime/resource_provider.go`.

- [x] Test handler duplicate registration, driver capability lookup, unsupported capability, and stable error categories.
- [x] Replace `ResourceProvider` with `ResourceHandler` everywhere and delete old symbols.
- [x] Define Node, NIC, Snapshot, Console, GuestExec, Network, Artifact and Import capability interfaces.
- [x] Run full tests and commit `refactor(runtime): establish handler and driver registries`.

### Task 2: Image And Kernel Drivers

- [x] Move image/kernel external operations behind ArtifactDriver and image preparation capability.
- [x] Add planning-time capability validation and stable driver errors.
- [x] Remove image/kernel substrate lookups from handlers and commit.

### Task 3: Network, Node, And Attachment Drivers

- [x] Move managed network, node lifecycle, NIC attach, observation, connection, and state codecs behind capability interfaces.
- [x] Make attachment handles typed state dependencies.
- [x] Remove substrate lookups from node/network/data handlers and commit.

### Task 4: Router, Firewall, Access, And Actor Drivers

- [x] Move router NAT, route execution, firewall, SSH access, and actor guest execution behind NetworkDriver/GuestExecDriver.
- [x] Delete runtime `exec.Command`, nsenter, iptables, ip route, Docker inspect and concrete network imports.
- [x] Commit each resource group with focused/full tests.

### Task 5: Capability Preflight And Handler-Owned Import

- [x] Add handler `RequiredCapabilities` and validate combinations during plan.
- [x] Add handler import parse/read/normalize contract and route CLI/API import through it with lock+CAS.
- [x] Test unsupported combinations and deterministic post-import NoOp plan.

### Task 6: Removal Audit And Documentation

- [x] Delete legacy Substrate interface/registry after the final consumer moves.
- [x] Add dependency tests forbidding concrete driver imports and infrastructure commands in runtime/API.
- [x] Document handler/driver contracts; run full tests, vet, focused race, and removal searches.
