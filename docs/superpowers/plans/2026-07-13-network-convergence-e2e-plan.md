# Network Convergence E2E Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:test-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make typed attachment recovery and cleanup verifiable against real Linux network objects through an explicit privileged test gate.

**Architecture:** Keep convergence behavior in the existing runtime and API recovery hooks. Extend the build-tagged E2E suite to prove repeated checkpoint recovery adopts existing attachments without duplication and cleanup removes all test-owned namespace, bridge, and tap objects; expose separate compile and privileged Make targets so missing host capabilities are reported as skips rather than passes.

**Tech Stack:** Go 1.26, Linux network namespaces/iproute2, existing `pkg/provider/network`, Go build tags, GNU Make.

## Global Constraints

- Do not add nftables, NAT, firewall, or controlled-egress behavior in this slice.
- Do not infer provider physical interface names in runtime code.
- Privileged tests must be explicitly tagged and must report unavailable capabilities as skipped.
- Cleanup assertions cover only objects created and owned by each test.

---

### Task 1: Compile-Gated Privileged Suite

**Files:**
- Modify: `Makefile`
- Modify: `pkg/api/recovery_e2e_test.go`

**Interfaces:**
- Consumes: existing `e2e` build tag and checkpoint recovery hooks.
- Produces: `make test-privileged-compile` and `make test-privileged` gates.

- [x] **Step 1: Run the tagged API test and record the expected compile failure**

Run: `GOCACHE=/tmp/sysbox-gocache go test -tags e2e ./pkg/api`

Expected: FAIL because the E2E test references `runtime` without importing it.

- [x] **Step 2: Add the missing import and explicit Make targets**

Add `github.com/oslab/sysbox/pkg/runtime` to the E2E test imports. Add a compile-only target using `go test -tags e2e -run '^$' ./pkg/api`, and a privileged target using `go test -tags e2e ./pkg/api`.

- [x] **Step 3: Verify tagged compilation**

Run: `make test-privileged-compile`

Expected: PASS without creating network objects.

### Task 2: Idempotent Recovery And Residue Assertions

**Files:**
- Modify: `pkg/api/recovery_e2e_test.go`

**Interfaces:**
- Consumes: `recoverCheckpoint`, `cleanupCheckpoint`, and provider network observation helpers.
- Produces: real-object evidence that repeated recovery adopts one attachment and cleanup leaves no test-owned bridge, tap, or namespace.

- [x] **Step 1: Extend tests with failing idempotency and residue assertions**

Call recovery twice against the same checkpoint and state manager. Assert one canonical state resource and an unchanged attachment inventory. After cleanup, assert namespace absence, which proves the contained bridge and tap are absent as well.

- [x] **Step 2: Run privileged tests**

Run: `make test-privileged`

Expected: PASS as root with CAP_NET_ADMIN, otherwise explicit SKIP lines.

- [x] **Step 3: Fix only convergence defects exposed by the test**

If the second recovery duplicates state or attachments, make checkpoint replay replace the canonical resource by address and retain one attachment per `(owner, logical name)`.

- [x] **Step 4: Run full verification**

Run `go test ./...`, `go vet ./...`, focused `go test -race`, `make ci`, the tagged compile gate, the privileged gate, the legacy-removal audit, and `git diff --check`.

- [ ] **Step 5: Commit atomically**

Commit the plan, E2E coverage, Make targets, and any directly required convergence fix as `test(network): verify privileged recovery convergence`.
