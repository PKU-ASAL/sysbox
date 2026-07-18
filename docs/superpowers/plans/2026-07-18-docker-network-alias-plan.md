# Docker Managed-Network Alias Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Resolve Docker nodes by logical HCL name and declared extra aliases on Sysbox-managed Docker networks.

**Architecture:** Aliases become typed attachment intent carried from HCL through runtime requests and durable state. Only the Docker NIC driver consumes aliases, and only for Docker-managed NAT attachments. Planning includes effective aliases so old or changed nodes replace explicitly.

**Tech Stack:** Go, HCL v2, Docker Engine API, testify, Bash Docker acceptance

## Global Constraints

- Automatically include the `sysbox_node` resource name as the first alias.
- Support optional `link.aliases` with stable de-duplication.
- Do not add hostname behavior.
- Do not apply aliases to isolated veth, routers, Firecracker, or libvirt.
- Persist aliases through state, checkpoint, refresh, and reset paths.
- Reject unsupported alias/network combinations before partial attachment.
- Do not preserve old-node behavior; desired changes replace nodes explicitly.

---

### Task 1: Typed Alias Intent

**Files:**
- Modify: `pkg/config/schema.go`
- Modify: `pkg/runtime/attachment_intent.go`
- Modify: `pkg/runtime/attachment_intent_test.go`
- Modify: `pkg/runtime/nic_wire.go`
- Modify: `pkg/driver/capability.go`
- Modify: `pkg/state/attachment.go`

**Interfaces:**
- Produces: `LinkConfig.Aliases`, `AttachmentInput.Aliases`,
  `AttachmentIntent.Aliases`, `NICSpec.Aliases`,
  `driver.AttachmentRequest.Aliases`, and `state.Attachment.Aliases`.

- [ ] **Step 1: Write failing normalization tests**

Assert owner `sysbox_node.mongo` produces `aliases=["mongo"]`, declared
`["mongodb", "mongo", "database"]` produces
`["mongo", "mongodb", "database"]`, and empty/whitespace aliases fail.

- [ ] **Step 2: Run tests and verify RED**

Run: `go test ./pkg/runtime -run 'AttachmentIntent.*Alias' -count=1`

Expected: FAIL because aliases are absent.

- [ ] **Step 3: Implement typed propagation**

Add aliases to each typed boundary and durable attachment. Generate the
automatic alias only for `sysbox_node` owners; router requests remain empty.

- [ ] **Step 4: Run tests and verify GREEN**

Run: `go test ./pkg/runtime ./pkg/state ./pkg/driver -count=1`

Expected: PASS.

### Task 2: Desired Diff and Reset Propagation

**Files:**
- Modify: `pkg/runtime/desired.go`
- Modify: `pkg/runtime/resource_node.go`
- Modify: `pkg/runtime/reset.go`
- Modify: `pkg/runtime/refresh.go`
- Modify: `pkg/runtime/checkpoint_hooks.go`
- Modify: relevant runtime tests

**Interfaces:**
- Consumes: effective aliases from normalized link intent.
- Produces: alias-aware desired payload, reset requests, refresh requests, and
  checkpoint cleanup/recovery requests.

- [ ] **Step 1: Write failing lifecycle tests**

Assert automatic aliases appear in desired payload, alias edits plan node
replacement, and state reconstruction retains aliases for reset/refresh.

- [ ] **Step 2: Run tests and verify RED**

Run: `go test ./pkg/runtime -run 'Alias|AttachmentRefresh|Reset' -count=1`

Expected: FAIL at missing propagation points.

- [ ] **Step 3: Implement lifecycle propagation**

Build node desired links from effective alias intent and copy aliases at every
state-to-request boundary. Increment the node resource schema version.

- [ ] **Step 4: Run tests and verify GREEN**

Run: `go test ./pkg/runtime -count=1`

Expected: PASS.

### Task 3: Docker Attach and Observation

**Files:**
- Modify: `pkg/provider/docker/network.go`
- Modify: `pkg/provider/docker/nic.go`
- Modify: Docker provider tests

**Interfaces:**
- Consumes: `driver.AttachmentRequest.Aliases`.
- Produces: Docker `EndpointSettings.Aliases` and alias-aware observation.

- [ ] **Step 1: Write failing Docker tests**

Assert `NetworkConnect` receives exact aliases, isolated attachment with aliases
fails before veth creation, and observation reports missing endpoint aliases.

- [ ] **Step 2: Run tests and verify RED**

Run: `go test ./pkg/provider/docker -run 'Alias' -count=1`

Expected: FAIL because aliases are ignored.

- [ ] **Step 3: Implement attach and observe**

Pass aliases only on Docker-managed network connect. Compare desired and
observed alias sets during observation and return not-found/drift semantics for
missing aliases.

- [ ] **Step 4: Run tests and verify GREEN**

Run: `go test ./pkg/provider/docker -count=1`

Expected: PASS.

### Task 4: Real Docker DNS Acceptance

**Files:**
- Create: `tests/e2e/docker-network-alias/field.sysbox.hcl`
- Create: `tests/e2e/docker_network_alias.sh`
- Modify: `Makefile`

**Interfaces:**
- Produces: `make test-docker-network-alias`.

- [ ] **Step 1: Add the acceptance fixture**

Create `mongo`, `target`, and `attacker` Alpine nodes on one NAT network.
Declare an extra `database` alias for mongo.

- [ ] **Step 2: Run acceptance and verify behavior**

From target/attacker, resolve and ping `mongo`, `target`, and `database`; repeat
after reset, verify no-op plan, destroy, and assert zero residue.

- [ ] **Step 3: Run full verification**

Run `go test ./...`, `go vet ./...`, privileged-test compilation,
`git diff --check`, and the real Docker acceptance.

Expected: all pass.

- [ ] **Step 4: Review**

Review the implementation against the approved spec, fix all Critical and
Important findings, and rerun affected plus full verification.
