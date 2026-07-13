# Atomic Nftables Policy Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:test-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace Sysbox append-style iptables and fixed firewall tables with IPv4-only, topology-owned, atomically replaced nftables policy.

**Architecture:** Core normalizes typed policy and persists semantic identity plus digest. The owning provider resolves logical attachments, atomically applies one owned nftables table, reads it back, and owns deletion/residue reporting; runtime never handles physical interfaces or nftables handles.

**Tech Stack:** Go 1.26.1, `github.com/google/nftables`, HCL v2, Linux network namespaces, Docker Engine API.

## Global Constraints

- IPv4 is the only accepted family; IPv6 CIDRs fail validation explicitly.
- Runtime must not import provider packages or execute `nft`, `iptables`, or `nsenter`.
- Policy mutation replaces one fully owned table and verifies a canonical SHA-256 digest.
- Table deletion requires full ownership identity and reports remaining owned objects.
- Docker daemon-owned bridge NAT remains outside Sysbox ownership.
- Remove `ConfigureNAT`, `ApplyFirewall`, `DeleteFirewall`, fixed `sysbox_fw`, and append-style Sysbox iptables with their final consumers.

---

### Task 1: Typed Policy Contract And Canonical Digest

**Files:**
- Create: `pkg/driver/policy.go`
- Create: `pkg/driver/policy_test.go`
- Modify: `pkg/driver/capability.go`

**Interfaces:**
- Produces: `AddressFamily`, `Verdict`, `Direction`, `ConnectionState`, `PortRange`, `PolicyRule`, `NATPolicy`, `RulesetSpec`, `PolicyTarget`, `RulesetObservation`, `OwnedObject`, and `Policy`.
- Produces: `NormalizeRuleset(RulesetSpec) (RulesetSpec, error)`, `RulesetDigest(RulesetSpec, map[string]string) (string, error)`, and `RulesetTableName(string) string`.

- [x] Write tests proving IPv6 rejection, CIDR canonicalization, sorted/deduplicated states, invalid protocol/port combinations, deterministic table names, and stable digest excluding counters.
- [x] Run `go test ./pkg/driver -run 'Test(NormalizeRuleset|RulesetDigest|RulesetTableName)'` and confirm RED on missing symbols.
- [x] Implement the typed contract and versioned canonical JSON digest with IPv4 validation.
- [x] Run `go test ./pkg/driver` and confirm GREEN.
- [x] Commit as `feat(driver): add atomic policy contract`.

### Task 2: Owned Nftables Compiler And Lifecycle

**Files:**
- Replace: `pkg/provider/network/firewall.go`
- Create: `pkg/provider/network/policy.go`
- Create: `pkg/provider/network/policy_test.go`
- Modify: `pkg/provider/network/driver.go`

**Interfaces:**
- Consumes: Task 1 policy types and digest functions.
- Produces: pure `compileRuleset(spec, bindings)` expression plan tests plus `Driver.ApplyRuleset`, `Driver.ObserveRuleset`, and `Driver.DeleteRuleset` for isolated namespaces.

- [ ] Write tests for base-chain policies, TCP/UDP ports, IPv4 source/destination matches, conntrack states, logical iif/oif bindings, accept/drop/reject, counter/log expressions, masquerade, ownership comments, and inventory digest.
- [ ] Run `go test ./pkg/provider/network -run 'Test(CompileRuleset|OwnedInventory)'` and confirm RED.
- [ ] Implement preflight compilation and a single `google/nftables` batch that replaces the deterministic table.
- [ ] Implement readback ownership verification, canonical observation, delete-then-observe residue checks, and stable driver errors.
- [ ] Run `go test ./pkg/provider/network ./pkg/driver` and confirm GREEN.
- [ ] Commit as `feat(network): add owned nftables rulesets`.

### Task 3: Docker Policy Execution And Router NAT Migration

**Files:**
- Replace: `pkg/provider/docker/router.go`
- Remove: `pkg/provider/docker/egress.go` append-style helpers after consumer migration.
- Create: `pkg/provider/docker/policy.go`
- Create: `pkg/provider/docker/policy_test.go`
- Modify: `pkg/provider/docker/docker.go`

**Interfaces:**
- Consumes: Task 1 `Policy` contract and Task 2 nftables compiler/lifecycle primitives through an explicit namespace executor.
- Produces: Docker `Policy` capability resolving logical attachment observations inside its owned container namespace.

- [ ] Write tests proving logical attachment binding, deterministic target identity, NAT compilation, fatal inspect/resolution failures, and no `iptables` command construction.
- [ ] Run focused Docker tests and confirm RED.
- [ ] Implement Docker namespace execution using the container PID and structured nftables connection; keep PID and interface names provider-local.
- [ ] Migrate router NAT to one `RulesetSpec`; return any apply/readback error instead of warning continuation.
- [ ] Remove append-style egress helpers after `sysbox_network` stops calling them.
- [ ] Run `go test ./pkg/provider/docker ./pkg/runtime` and confirm GREEN.
- [ ] Commit as `refactor(docker): replace iptables router policy`.

### Task 4: Firewall Schema, Runtime State, And Refresh

**Files:**
- Modify: `pkg/config/schema.go`
- Modify: `pkg/config/validate.go`
- Modify: `pkg/runtime/schema.go`
- Replace: `pkg/runtime/firewall.go`
- Modify: `pkg/runtime/router.go`
- Modify: `pkg/runtime/resource_network.go`
- Modify: `pkg/runtime/refresh.go`
- Modify: `pkg/runtime/desired.go`
- Modify: runtime and config tests.

**Interfaces:**
- Consumes: Task 1 `RulesetSpec` and provider `Policy` capability.
- Produces: HCL fields for target, family, default policies, typed rules, logging/counters, and normalized policy state with table/desired/observed digests.

- [ ] Write config tests for all typed fields, logical interface validation, IPv6 rejection, default drop, and invalid rule combinations.
- [ ] Write runtime tests for apply/readback persistence, fatal NAT failure, matching/missing/mismatched/unavailable refresh, replacement, and delete residue propagation.
- [ ] Run focused config/runtime tests and confirm RED.
- [ ] Implement schema normalization and policy target resolution against typed attachments.
- [ ] Persist semantic policy, table identity, and digests only after verified apply.
- [ ] Implement observe-first refresh and delete through `Policy`; remove legacy runtime egress and NAT calls.
- [ ] Run `go test ./pkg/config ./pkg/runtime ./pkg/provider/docker ./pkg/provider/network` and confirm GREEN.
- [ ] Commit as `feat(runtime): converge atomic firewall policy`.

### Task 5: Recovery, Privileged E2E, Documentation, And Removal

**Files:**
- Modify: `pkg/runtime/checkpoint_hooks.go`
- Modify: `pkg/api/recovery_e2e_test.go`
- Create: `pkg/provider/network/policy_e2e_test.go`
- Modify: `Makefile`
- Modify: `docs/architecture/handler-driver-contracts.md`
- Modify: affected examples and README files.

**Interfaces:**
- Consumes: complete owned policy lifecycle.
- Produces: observe-first checkpoint adoption/replacement and privileged default-deny/NAT/idempotency/residue evidence.

- [ ] Write recovery tests for matching adoption, mismatched atomic replacement, and repeated recovery without duplicate owned objects.
- [ ] Write build-tagged privileged tests for default deny, explicit allow, established return traffic, masquerade, stable repeated digest, and zero-residue delete.
- [ ] Run compile-only privileged gate, then the capability test; unavailable CAP_NET_ADMIN must be SKIP.
- [ ] Update contracts/examples and remove all production append-style iptables, fixed `sysbox_fw`, and legacy capability symbols.
- [ ] Run `go test ./...`, `go vet ./...`, focused `go test -race`, `make ci`, privileged compile/run gates, removal audit, and `git diff --check`.
- [ ] Commit as `test(network): verify atomic policy convergence`.
