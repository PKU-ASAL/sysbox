# Guest Network Initialization Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an explicit provider-owned guest network initialization contract and prove it with a pinned Ubuntu cloud image across the complete Docker/Firecracker/libvirt IPv4 matrix.

**Architecture:** Provider-neutral modes, capabilities, and observations live in `pkg/substrate`; runtime invokes an optional driver capability around node start without understanding NoCloud. Libvirt implements exactly `cloud_init` and `preconfigured`, persists the selected mode in private provider state, and verifies declared IPv4 addresses from their isolated namespaces.

**Tech Stack:** Go 1.26, HCL, libvirt/virsh, NoCloud v2 YAML, Linux network namespaces, Bash acceptance runner.

## Global Constraints

- IPv4 only; reject IPv6 explicitly while retaining prefix-slice interfaces.
- Libvirt `network_init` is mandatory and accepts only `cloud_init` or `preconfigured`.
- No implicit default, auto-detection, fallback, SSH bootstrap, or legacy compatibility path.
- Ubuntu image URL: `https://cloud-images.ubuntu.com/releases/noble/release-20260615/ubuntu-24.04-server-cloudimg-amd64.img`.
- Ubuntu image SHA256: `5fa5b05e5ec239858c4531485d6023b0896448c2df7c63b34f8dae6ea6051a44`.
- Test SSH keys are generated per run and never committed or persisted in state.
- Existing `mixed`, `recon`, `docker-service`, and `env_*` resources must not be modified.

---

### Task 1: Public Capability Contract

**Files:**
- Modify: `pkg/substrate/types.go`
- Modify: `pkg/driver/capability.go`
- Modify: `pkg/driver/capability_test.go`
- Modify: `pkg/driver/registry.go`
- Test: `pkg/driver/capability_test.go`

**Interfaces:**
- Produces: `substrate.GuestNetworkInitMode`, `substrate.GuestNetworkInitObservation`, `driver.GuestNetworkInit`, `driver.CapabilityGuestNetworkInit`, and registry lookup.

- [ ] **Step 1: Write failing contract tests**

Add assertions that a descriptor exposing `GuestNetworkInit` can be required by capability name and that the public mode constants equal `cloud_init` and `preconfigured`.

- [ ] **Step 2: Run the focused tests and verify RED**

Run: `go test ./pkg/driver -run 'Test.*GuestNetworkInit' -count=1`

Expected: compile failure because the capability and public types do not exist.

- [ ] **Step 3: Implement the minimal public contract**

```go
type GuestNetworkInit interface {
    PrepareGuestNetwork(context.Context, substrate.NodeHandle) error
    ObserveGuestNetwork(context.Context, substrate.NodeHandle) (substrate.GuestNetworkInitObservation, error)
}
```

Add the two modes, interface observations, capability constant, descriptor field, switch lookup, and typed registry requirement following existing capability patterns.

- [ ] **Step 4: Run driver and substrate tests**

Run: `go test ./pkg/driver ./pkg/substrate`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/substrate/types.go pkg/driver/capability.go pkg/driver/capability_test.go pkg/driver/registry.go
git commit -m "feat(driver): add guest network initialization capability"
```

### Task 2: Typed Configuration And Runtime Lifecycle

**Files:**
- Modify: `pkg/secret/reference.go`
- Modify: `pkg/secret/reference_test.go`
- Modify: `pkg/provider/libvirt/config.go`
- Modify: `pkg/provider/libvirt/config_test.go`
- Modify: `pkg/provider/libvirt/libvirt.go`
- Modify: `pkg/runtime/resource_node.go`
- Modify: `pkg/runtime/resource_node_test.go`
- Modify: `pkg/runtime/resource_handler.go`

**Interfaces:**
- Consumes: Task 1 capability types.
- Produces: typed secret-preserving provider config, mandatory libvirt mode validation, and runtime prepare/observe lifecycle calls.

- [ ] **Step 1: Write failing typed-secret and mode tests**

Test that `secret.ResolveAny` preserves `*libvirt.Config` while resolving string fields; decoding a libvirt block without `network_init` fails; unknown modes fail; both supported modes pass; libvirt advertises both modes.

- [ ] **Step 2: Verify RED**

Run: `go test ./pkg/secret ./pkg/provider/libvirt -run 'Test.*(ResolveAny|NetworkInit)' -count=1`

Expected: failures for untraversed typed structs and missing mode validation.

- [ ] **Step 3: Implement typed resolution and explicit config**

Use reflection in `secret.ResolveAny` to copy pointers, structs, slices, and maps while preserving concrete types and resolving exported string values. Reject unsupported secret references through the existing resolver.

Extend libvirt config with:

```go
NetworkInit      substrate.GuestNetworkInitMode `hcl:"network_init"`
SSHAuthorizedKey string                          `hcl:"ssh_authorized_key,optional"`
```

Validate the mode against `Capabilities().GuestNetworkInitModes`.

- [ ] **Step 4: Write and run failing runtime lifecycle tests**

Use a recording provider to assert runtime calls `PrepareGuestNetwork` after cold attachments and before `StartNode`, then calls `ObserveGuestNetwork` after start and persists the converged observation. Assert non-convergence fails creation.

Run: `go test ./pkg/runtime -run 'Test.*GuestNetworkInit' -count=1`

Expected: RED before lifecycle wiring, then PASS after implementation.

- [ ] **Step 5: Run affected packages and commit**

Run: `go test ./pkg/secret ./pkg/provider/libvirt ./pkg/runtime`

```bash
git add pkg/secret pkg/provider/libvirt/config.go pkg/provider/libvirt/config_test.go pkg/provider/libvirt/libvirt.go pkg/runtime
git commit -m "feat(runtime): enforce explicit guest network initialization"
```

### Task 3: Libvirt Prepare And Observe

**Files:**
- Modify: `pkg/provider/libvirt/cloudinit.go`
- Modify: `pkg/provider/libvirt/attachment_test.go`
- Modify: `pkg/provider/libvirt/node.go`
- Modify: `pkg/provider/libvirt/network.go`
- Create: `pkg/provider/libvirt/network_init.go`
- Create: `pkg/provider/libvirt/network_init_test.go`
- Modify: `pkg/provider/network/bridge.go`

**Interfaces:**
- Consumes: Task 1 driver capability and Task 2 typed config.
- Produces: pre-start seed preparation, preconfigured no-op, bounded IPv4 convergence observations, and explicit IPv6 rejection.

- [ ] **Step 1: Write failing cloud-init tests**

Assert `cloud_init` creates metadata, v2 MAC-matched IPv4 config, and optional cloud-config SSH user/key; assert `preconfigured` creates no seed; assert handle state persists mode, namespace, bridge, MAC, and prefixes.

- [ ] **Step 2: Verify RED**

Run: `go test ./pkg/provider/libvirt -run 'Test.*(CloudInit|Preconfigured|GuestNetwork)' -count=1`

- [ ] **Step 3: Implement provider preparation**

Move seed creation out of `StartNode` into `PrepareGuestNetwork`. Store `SeedISO` in `HandleState`; `StartNode` only consumes prepared state. Build user data with YAML structured APIs and mode `0600` temporary inputs, producing a `0644` read-only ISO for qemu.

- [ ] **Step 4: Write failing observation tests**

Inject a probe function and assert all IPv4 prefixes converge, a failed prefix produces a detailed observation, empty attachments converge, cancellation stops polling, and IPv6 returns an unsupported-family error.

- [ ] **Step 5: Implement bounded observation**

Persist the isolated namespace on `BridgeAttach`. Probe each stripped IPv4 address from that namespace with a cancellation-aware command/function. Return one observation per logical attachment and never silently omit a prefix.

- [ ] **Step 6: Run provider tests and commit**

Run: `go test ./pkg/provider/libvirt ./pkg/provider/network`

```bash
git add pkg/provider/libvirt pkg/provider/network/bridge.go
git commit -m "feat(libvirt): initialize and observe guest networking"
```

### Task 4: Controlled Image And Acceptance Runner

**Files:**
- Create: `scripts/prepare-libvirt-cloud-image.sh`
- Create: `tests/e2e/heterogeneous_matrix.sh`
- Modify: `examples/heterogeneous-matrix/field.sysbox.hcl`
- Modify: `examples/heterogeneous-matrix/README.md`
- Modify: `Makefile`
- Modify: `docs/verification/2026-07-13-batch4-network-acceptance.md`

**Interfaces:**
- Consumes: Task 3 converged libvirt provider.
- Produces: verified cache artifact and reproducible six-edge communication acceptance.

- [ ] **Step 1: Implement and test the image preparation script**

The script uses `curl --fail --location`, writes to a temporary cache file,
verifies the fixed SHA256 with `sha256sum --check`, atomically renames it to
`$SYSBOX_CACHE/libvirt/ubuntu-24.04-server-cloudimg-amd64-20260615.img`, and
prints the absolute path. A matching cached file skips download; a mismatch is
deleted and rebuilt.

Run twice and verify the second run is a cache hit and both digests equal
`5fa5b05e5ec239858c4531485d6023b0896448c2df7c63b34f8dae6ea6051a44`.

- [ ] **Step 2: Update the fixture**

Set `network_init = "cloud_init"`, `ssh_user = "sysbox"`, and
`ssh_authorized_key = env("SYSBOX_MATRIX_SSH_PUBLIC_KEY")`. Use
`SYSBOX_QCOW2` for the verified image path.

- [ ] **Step 3: Implement the acceptance runner**

Generate an Ed25519 keypair in a private temporary directory; install a destroy
trap; run apply; use Docker exec, Firecracker's connection, and libvirt SSH to
prove all six directed ping edges; assert `Plan: 0 to add, 0 to replace, 0 to
destroy, 8 unchanged`; destroy; audit exact topology labels/names plus domain,
netns, bridge, veth, tap, process, VM directory, seed, and state residue.

- [ ] **Step 4: Add durable Make targets and documentation**

Add `prepare-libvirt-cloud-image` and `test-heterogeneous-matrix` targets. Record
the exact image URL, digest, commands, six edge results, idempotent plan, destroy,
and zero-residue inventory in the verification document.

- [ ] **Step 5: Commit**

```bash
git add scripts/prepare-libvirt-cloud-image.sh tests/e2e/heterogeneous_matrix.sh examples/heterogeneous-matrix Makefile docs/verification
git commit -m "test(network): verify complete heterogeneous matrix"
```

### Task 5: Final Gates And Review

**Files:**
- Review all files changed since `a51ab3e`.

**Interfaces:**
- Consumes: Tasks 1-4.
- Produces: merge-ready commits with complete acceptance evidence.

- [ ] **Step 1: Run standard gates**

```bash
go test ./...
go vet ./...
CGO_ENABLED=1 go test -race ./pkg/state ./pkg/runtime ./pkg/provider/network ./pkg/provider/docker ./pkg/provider/libvirt
make ci
make test-privileged-container
make test-heterogeneous-matrix
git diff --check
```

- [ ] **Step 2: Run removal and residue audits**

Search for implicit libvirt network-init defaults, bootstrap fallback code, and
legacy guest-network paths. Audit exact heterogeneous topology Docker labels,
libvirt domain name, netns, root bridge, veths, taps, Firecracker processes,
temporary directories, seed ISOs, and state files.

- [ ] **Step 3: Perform code review**

Review capability boundaries, secret persistence, cleanup ordering, retry
bounds, state recovery, unmanaged-resource protection, and test assertions.
Fix all critical and important findings and rerun affected gates.

- [ ] **Step 4: Commit final corrections**

```bash
git add -A
git commit -m "test(network): harden guest initialization acceptance"
```
