# Typed Network Attachments Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace untyped NIC maps and physical interface identity with schema-v5 typed attachments addressed by explicit logical names.

**Architecture:** Core owns normalized attachment intent and typed persistent semantics. Capability drivers own concrete device naming and opaque observe/delete/recovery state; state v1-v4 and unlabeled node links are rejected without compatibility paths.

**Tech Stack:** Go 1.26, HCL v2/gohcl, typed Sysbox state/value model, capability driver registry, testify.

## Global Constraints

- State schema v5 is a hard break: no migration, dual read, dual write, or automatic destroy.
- Every node link and router interface has an explicit logical name.
- `(node address, logical name)` is attachment identity; declaration order and `ethN` are never identity.
- Core never parses driver opaque attachment state or imports concrete provider packages.
- Default MACs are deterministic locally administered unicast addresses and may be explicitly overridden.
- Every task follows verified RED/GREEN, full tests, vet, focused race, removal audit, and an atomic commit.

---

### Task 1: Schema v5 Typed Attachment State

**Files:**
- Create: `pkg/state/attachment.go`
- Create: `pkg/state/attachment_test.go`
- Modify: `pkg/state/state.go`
- Modify: `pkg/state/state_v4_test.go`
- Modify: state fixtures that contain `"version": 4`

**Interfaces:**
- Produces: `state.Attachment`, `state.AttachmentObservation`, `Resource.Attachments []Attachment`, and `SchemaVersion == 5`.
- `Attachment.DriverState` is opaque `json.RawMessage`; core may copy but not decode it.

- [x] **Step 1: Write failing v4 rejection and v5 round-trip tests**

```go
func TestDecodeRejectsV4AfterAttachmentSchemaBreak(t *testing.T) {
    _, err := Decode([]byte(`{"version":4,"resources":[]}`))
    require.ErrorContains(t, err, "state schema v4")
    require.ErrorContains(t, err, "expects v5")
    require.ErrorContains(t, err, "remove")
}

func TestAttachmentRoundTripsDeterministically(t *testing.T) {
    in := &State{Version: SchemaVersion, Resources: []Resource{{
        Address: address.Resource("sysbox_node", "web"),
        Attachments: []Attachment{{
            Name: "uplink", Node: address.Resource("sysbox_node", "web"),
            Network: address.Resource("sysbox_network", "public"),
            MAC: "02:00:00:00:00:01", IPPrefixes: []string{"10.0.0.10/24"},
            Gateway: "10.0.0.1", Driver: "docker",
            Observation: AttachmentObservation{GuestDevice: "eth7"},
            DriverState: json.RawMessage(`{"network_id":"abc"}`),
        }},
    }}}
    first, err := Encode(in)
    require.NoError(t, err)
    decoded, err := Decode(first)
    require.NoError(t, err)
    second, err := Encode(decoded)
    require.NoError(t, err)
    require.Equal(t, first, second)
}
```

- [x] **Step 2: Run RED**

Run: `GOCACHE=/tmp/sysbox-gocache go test ./pkg/state -run 'Test(DecodeRejectsV4AfterAttachmentSchemaBreak|AttachmentRoundTripsDeterministically)$'`

Expected: FAIL because schema is v4 and attachment types/field do not exist.

- [x] **Step 3: Add the typed model and bump the schema**

```go
type Attachment struct {
    Name        string                `json:"name"`
    Node        address.Address       `json:"node"`
    Network     address.Address       `json:"network"`
    MAC         string                `json:"mac"`
    IPPrefixes  []string              `json:"ip_prefixes,omitempty"`
    Gateway     string                `json:"gateway,omitempty"`
    Driver      string                `json:"driver"`
    Observation AttachmentObservation `json:"observation,omitempty"`
    DriverState json.RawMessage       `json:"driver_state,omitempty"`
}

type AttachmentObservation struct {
    GuestDevice string `json:"guest_device,omitempty"`
}
```

Set `SchemaVersion = 5`, add `Attachments []Attachment` to `Resource`, and update the incompatible-version error to instruct removal/rebuild without mutating input.

- [x] **Step 4: Run GREEN and repository state tests**

Run: `GOCACHE=/tmp/sysbox-gocache go test ./pkg/state`

Expected: PASS.

- [x] **Step 5: Commit**

```bash
git add pkg/state
git commit -m "feat(state): add typed network attachments"
```

### Task 2: Explicit Logical Names In HCL

**Files:**
- Modify: `pkg/config/schema.go`
- Modify: `pkg/config/*_test.go`
- Modify: `tests/testdata/valid_field.hcl`
- Modify: every `examples/**/*.hcl` and affected example documentation

**Interfaces:**
- Produces: `LinkConfig.Name string` decoded from the block label and validated as unique per node.
- Router `RouterInterface.Name` remains the same public label and follows the same validation rules.

- [x] **Step 1: Write failing decode tests**

```go
func TestNodeLinksRequireUniqueLogicalNames(t *testing.T) {
    _, diags := Parse([]byte(`resource "sysbox_node" "web" {
      image = "x"
      substrate = "docker"
      link { network = sysbox_network.public ip = "10.0.0.2/24" }
    }`), "test.hcl")
    require.True(t, diags.HasErrors())
    require.Contains(t, diags.Error(), "link")
}
```

Add a second test with two `link "uplink"` blocks and require a duplicate-name error.

- [x] **Step 2: Run RED**

Run: `GOCACHE=/tmp/sysbox-gocache go test ./pkg/config -run TestNodeLinksRequireUniqueLogicalNames`

Expected: FAIL because unlabeled links currently decode successfully.

- [x] **Step 3: Require the label and validate uniqueness**

```go
type LinkConfig struct {
    Name    string `hcl:"name,label"`
    Network string `hcl:"network"`
    IP      string `hcl:"ip"`
    Gateway string `hcl:"gw,optional"`
    MAC     string `hcl:"mac,optional"`
}
```

Use the existing resource validation path to reject empty/duplicate node link and router interface names with the owning resource address in the diagnostic.

- [x] **Step 4: Convert all repository HCL**

Choose semantic labels such as `uplink`, `internal`, and `dmz`; do not generate labels from declaration order. Update references and docs in the same change.

- [x] **Step 5: Run GREEN and parse all examples**

Run: `GOCACHE=/tmp/sysbox-gocache go test ./pkg/config ./pkg/runtime ./cmd/sysbox/commands`

Expected: PASS.

- [x] **Step 6: Commit**

```bash
git add pkg/config tests/testdata examples docs
git commit -m "feat(config): require logical attachment names"
```

### Task 3: Normalize Attachment Intent And Stable MACs

**Files:**
- Create: `pkg/runtime/attachment_intent.go`
- Create: `pkg/runtime/attachment_intent_test.go`
- Modify: `pkg/runtime/nic_wire.go`
- Modify: `pkg/runtime/resource_node.go`
- Modify: `pkg/runtime/router.go`

**Interfaces:**
- Produces: `AttachmentIntent` and `NormalizeAttachmentIntents(topology string, owner address.Address, links []LinkInput) ([]AttachmentIntent, error)`.
- Produces: `DeterministicMAC(topology string, owner address.Address, logicalName string) net.HardwareAddr`.

- [x] **Step 1: Write failing normalization tests**

```go
func TestDeterministicMACIsStableLocalUnicast(t *testing.T) {
    first := DeterministicMAC("lab", address.Resource("sysbox_node", "web"), "uplink")
    second := DeterministicMAC("lab", address.Resource("sysbox_node", "web"), "uplink")
    require.Equal(t, first, second)
    require.Zero(t, first[0]&1)
    require.NotZero(t, first[0]&2)
}
```

Also test explicit override, duplicate names/prefixes, invalid multicast MAC, invalid prefix, and gateway outside all prefixes.

- [x] **Step 2: Run RED**

Run: `GOCACHE=/tmp/sysbox-gocache go test ./pkg/runtime -run 'Test(DeterministicMAC|NormalizeAttachment)'`

Expected: FAIL because normalization functions do not exist.

- [x] **Step 3: Implement normalized intent**

```go
type AttachmentIntent struct {
    Name       string
    Network    address.Address
    MAC        string
    IPPrefixes []string
    Gateway    string
}
```

Derive the six MAC bytes from SHA-256 over length-delimited topology, canonical owner address, and logical name; set the local bit and clear the multicast bit. Normalize IP prefixes with `netip.ParsePrefix(...).Masked()` and validate the gateway with `netip.ParseAddr`.

- [x] **Step 4: Replace `NICSpec.Label` and pass logical identity and MAC through wiring**

Make logical name mandatory in the shared wire input and pass normalized MACs
to the existing driver request. Remove `natIdx`, `vethIdx`, `TargetName`, and
`IfaceByName` in Task 4 when the new attachment lifecycle contract lets drivers
return and resolve observed device names without breaking router NAT.

- [x] **Step 5: Run GREEN**

Run: `GOCACHE=/tmp/sysbox-gocache go test ./pkg/runtime`

Expected: PASS; logical names and normalized MACs reach driver requests.

- [x] **Step 6: Commit**

```bash
git add pkg/runtime
git commit -m "refactor(network): normalize logical attachment intent"
```

### Task 4: Attachment Capability Contract

**Files:**
- Modify: `pkg/driver/capability.go`
- Modify: `pkg/driver/registry.go`
- Modify: `pkg/driver/*_test.go`
- Modify: `pkg/provider/docker/nic.go`
- Modify: `pkg/provider/firecracker/nic.go`
- Modify: `pkg/provider/libvirt/network.go`
- Modify: provider NIC tests

**Interfaces:**
- Replaces the one-shot NIC contract with logical attach/observe/delete operations.

```go
type AttachmentRequest struct {
    Name, MAC, Gateway string
    Network address.Address
    IPPrefixes []string
    NetworkState json.RawMessage
}

type AttachmentResult struct {
    Driver string
    GuestDevice string
    State json.RawMessage
}

type NIC interface {
    Attach(context.Context, substrate.NodeHandle, AttachmentRequest) (AttachmentResult, error)
    Observe(context.Context, substrate.NodeHandle, AttachmentRequest, json.RawMessage) (AttachmentResult, error)
    Delete(context.Context, substrate.NodeHandle, AttachmentRequest, json.RawMessage) error
}
```

- [ ] **Step 1: Write failing registry and provider contract tests**

Require all three built-in node drivers to preserve request logical name/MAC/prefix semantics and return opaque JSON sufficient for observe/delete. Require `Observe` not-found to use `driver.CategoryNotFound`.

- [ ] **Step 2: Run RED**

Run: `GOCACHE=/tmp/sysbox-gocache go test ./pkg/driver ./pkg/provider/docker ./pkg/provider/firecracker ./pkg/provider/libvirt`

Expected: compile/test failure because the old `AttachNIC` contract lacks observe/delete and logical identity.

- [ ] **Step 3: Implement the capability and providers**

Move concrete host end, tap, namespace, Docker network ID, and provider device data into provider-owned JSON structs. Drivers choose concrete guest names; runtime supplies no target name. Wrap errors with existing stable driver categories.

- [ ] **Step 4: Run GREEN**

Run: `GOCACHE=/tmp/sysbox-gocache go test ./pkg/driver ./pkg/provider/...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/driver pkg/provider
git commit -m "feat(driver): add attachment lifecycle capability"
```

### Task 5: Persist And Consume Typed Attachments

**Files:**
- Modify: `pkg/runtime/nic_wire.go`
- Modify: `pkg/runtime/resource_node.go`
- Modify: `pkg/runtime/router.go`
- Modify: `pkg/runtime/resource_node_test.go`
- Modify: `pkg/runtime/resource_router_test.go`
- Modify: `pkg/runtime/nic_wire_test.go`
- Modify: API/e2e fixtures constructing `nics`

**Interfaces:**
- Consumes: Task 1 `state.Attachment`, Task 3 normalized intent, Task 4 attachment lifecycle capability.
- Produces: node/router resources whose `Attachments` are the only attachment semantic state.

- [ ] **Step 1: Write failing persistence and logical NAT tests**

```go
func TestWireAttachmentsPersistsTypedState(t *testing.T) {
    got, err := wireAttachments(ctx, fakeNIC{}, st, handle, intents, "web")
    require.NoError(t, err)
    require.Equal(t, "uplink", got[0].Name)
    require.Equal(t, "02:00:00:00:00:01", got[0].MAC)
    require.JSONEq(t, `{"device_id":"opaque"}`, string(got[0].DriverState))
}
```

Add a router test proving `nat_from = "internal"` and `nat_to = "uplink"` reach the driver as logical names without runtime `ethN` lookup.

- [ ] **Step 2: Run RED**

Run: `GOCACHE=/tmp/sysbox-gocache go test ./pkg/runtime -run 'Test(WireAttachmentsPersistsTypedState|RouterNATUsesLogicalAttachments)$'`

Expected: FAIL because runtime still writes `Attributes["nics"]` and resolves NAT to `ethN`.

- [ ] **Step 3: Wire typed state through node and router creation/deletion**

Persist `Resource.Attachments`, pass opaque state back only to the owning capability, and make NAT/route requests carry logical attachment names. Remove all `nics` map construction and parsing from production runtime.

- [ ] **Step 4: Run GREEN and removal audit**

Run: `GOCACHE=/tmp/sysbox-gocache go test ./pkg/runtime ./pkg/api ./pkg/agentexec`

Run: `rg -n 'Attribute(Map)?\(\)\["nics"\]|"nics"\s*:' pkg/runtime pkg/api pkg/agentexec --glob '*.go'`

Expected: tests PASS; search has no production matches.

- [ ] **Step 5: Commit**

```bash
git add pkg/runtime pkg/api pkg/agentexec
git commit -m "refactor(runtime): persist typed attachments"
```

### Task 6: Refresh, Recovery, Documentation, And Final Audit

**Files:**
- Modify: `pkg/runtime/refresh.go`
- Modify: `pkg/runtime/health.go`
- Modify: `pkg/runtime/checkpoint_hooks.go`
- Modify: corresponding runtime tests
- Modify: `docs/architecture/handler-driver-contracts.md`
- Modify: `docs/superpowers/specs/2026-07-12-heterogeneous-topology-runtime-design.md` if acceptance wording needs the new concrete contract
- Create: `docs/superpowers/plans/2026-07-12-network-convergence-plan.md` only after this subproject is complete

**Interfaces:**
- Consumes: typed attachments and driver observe/delete lifecycle.
- Produces: attachment status classification and idempotent checkpoint recovery keyed by owner/logical name.

- [ ] **Step 1: Write failing refresh and recovery tests**

Cover: concrete device rename updates observation without replacement; `not-found` yields drifted; unavailable yields unknown without durable rewrite; completed attach checkpoint plus external attachment is adopted rather than attached twice.

- [ ] **Step 2: Run RED**

Run: `GOCACHE=/tmp/sysbox-gocache go test ./pkg/runtime -run 'Test(RefreshAttachment|RecoverAttachment)'`

Expected: FAIL because health/recovery still parse legacy NIC maps.

- [ ] **Step 3: Implement observe-first refresh and recovery**

Enumerate `Resource.Attachments`, call the owning NIC capability, compare semantic fields independently of `GuestDevice`, update observation only after successful reads, and key checkpoint details by canonical owner address plus logical name.

- [ ] **Step 4: Update contracts documentation and run final removal audit**

Document schema v5 hard break, typed/opaque ownership, logical identity, and observe-first recovery.

Run: `rg -n 'Attributes\["nics"\]|AttributeMap\(\)\["nics"\]|TargetName|IfaceByName|fmt.Sprintf\("eth%d"' pkg --glob '*.go'`

Expected: no production matches; any remaining provider-local physical-device fields are documented opaque implementation details.

- [ ] **Step 5: Run full verification**

```bash
GOCACHE=/tmp/sysbox-gocache go test ./...
GOCACHE=/tmp/sysbox-gocache go vet ./...
GOCACHE=/tmp/sysbox-gocache go test -race ./pkg/state ./pkg/config ./pkg/driver ./pkg/runtime ./pkg/api ./pkg/agentexec
git diff --check
```

Expected: all commands PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg docs
git commit -m "refactor(network): complete typed attachment convergence"
```
