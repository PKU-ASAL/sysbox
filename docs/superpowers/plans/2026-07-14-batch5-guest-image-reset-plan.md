# Batch 5 Guest, Image, And Reset Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver strict guest families, structured execution, digest-bound immutable images and kernels, and recoverable topology or targeted node reset across Docker, libvirt, and Firecracker.

**Architecture:** Public semantics live in `pkg/substrate` and capabilities in `pkg/driver`; runtime validates and orchestrates without provider-name branches. Providers own execution translation and reset mechanics, while state v6 rejects all earlier state and stores only immutable public identities plus opaque private handles.

**Tech Stack:** Go 1.26, HCL v2, existing typed state/value schemas, Docker Engine API, libvirt/virsh, Firecracker, qcow2 overlays, checkpoint recorder, testify.

## Global Constraints

- Increase persistent state directly from v5 to v6 and reject every non-v6 state with destroy/recreate guidance.
- Do not add a state migrator, compatibility decoder, deprecated alias, implicit shell, or permanent dual path.
- IPv4 remains the only network family; retain existing explicit IPv6 rejection.
- Formal stored plans bind image and kernel digests and never re-resolve to newer content.
- Runtime must not branch on provider names or understand SSH, WinRM, overlays, snapshots, Docker create options, or Firecracker processes.
- Resolved secrets never enter plan, state, checkpoint, logs, artifact metadata, or reset observations.

---

### Task 1: Public Contracts And State V6

**Files:**
- Create: `pkg/substrate/guest.go`
- Create: `pkg/substrate/artifact.go`
- Create: `pkg/substrate/reset.go`
- Modify: `pkg/substrate/types.go`
- Modify: `pkg/driver/capability.go`
- Modify: `pkg/driver/registry.go`
- Modify: `pkg/state/state.go`
- Modify: `pkg/state/state_test.go`
- Test: `pkg/substrate/guest_test.go`
- Test: `pkg/substrate/artifact_test.go`
- Test: `pkg/driver/registry_test.go`

**Interfaces:**
- Produces: `GuestFamily`, `ShellKind`, `ExecRequest`, `ArtifactKind`, `ArtifactIdentity`, `ResetRequest`, `ResetHandle`, `ResetObservation`, `driver.Reset`, and `CapabilityReset`.
- State contract: `state.SchemaVersion == 6`; non-v6 JSON fails before resource decoding.

- [x] **Step 1: Write failing public-contract and v5 rejection tests**

Assert exact enum strings, validation of unknown values, copy-safe metadata, reset capability lookup, schema version 6, and a v5 load error containing `destroy/recreate or delete the old state`.

```go
require.Equal(t, substrate.GuestFamily("linux"), substrate.GuestFamilyLinux)
require.NoError(t, substrate.ValidateGuestFamily(substrate.GuestFamilyWindows))
require.Error(t, substrate.ValidateShellKind("bash"))
require.Equal(t, 6, state.SchemaVersion)
require.ErrorContains(t, loadVersion(t, 5), "destroy/recreate or delete the old state")
```

- [x] **Step 2: Verify RED**

Run: `go test ./pkg/substrate ./pkg/driver ./pkg/state -run 'Guest|Artifact|Reset|Incompatible' -count=1`

Expected: compile failures for missing public contracts and schema version mismatch.

- [x] **Step 3: Implement immutable public values and capability**

Define validated string enums and these exact contracts:

```go
type ExecRequest struct { Program string; Args []string; Environment map[string]string; WorkingDir string; Shell ShellKind; Stdin io.Reader }
type ArtifactIdentity struct { Kind ArtifactKind; Source, Digest, Architecture string; GuestFamily GuestFamily; Metadata map[string]string }
type ResetRequest struct { Current NodeHandle; Node NodeSpec; Baseline ArtifactIdentity }
type ResetHandle struct { Provider any }
type ResetObservation struct { Phase string; Converged bool; OldExternalID, NewExternalID, BaselineDigest, Reason string; Residue []string }
```

Add `driver.Reset` with the four methods from the approved design, add `CapabilityReset`, descriptor storage, registry lookup, and typed `RequireReset`.

- [x] **Step 4: Bump state to v6 with strict error guidance**

Set `SchemaVersion = 6`, document v3-v6 history, and make `IncompatibleVersionError.Error()` include the required destructive recreation guidance. Do not decode resources when the top-level version differs.

- [x] **Step 5: Run focused and full tests, then commit**

Run: `go test ./pkg/substrate ./pkg/driver ./pkg/state && go test ./...`

Commit: `feat(batch5): add guest artifact reset contracts and state v6`

---

### Task 2: Strict HCL And Guest Family Resolution

**Files:**
- Modify: `pkg/config/schema.go`
- Modify: `pkg/runtime/schema.go`
- Modify: `pkg/runtime/desired.go`
- Modify: `pkg/runtime/resource_image.go`
- Modify: `pkg/runtime/resource_node.go`
- Modify: `pkg/runtime/workspace.go`
- Test: `pkg/runtime/resource_image_test.go`
- Test: `pkg/runtime/resource_node_test.go`
- Test: `pkg/runtime/workspace_test.go`

**Interfaces:**
- Consumes: Task 1 enums and artifact identity.
- Produces: strict `ImageConfig{Substrate, Kind, Source, SHA256, Architecture, GuestFamily, Size}` and `NodeConfig.GuestFamily`; `resolveGuestFamily(image, override)`.

- [x] **Step 1: Write failing strict-schema tests**

Test valid known/unknown families, inherited family, matching override, unknown-image override, known conflict, unresolved unknown with guest operations, and rejection of `docker_ref`, `rootfs`, and `qcow2`.

- [x] **Step 2: Verify RED**

Run: `go test ./pkg/runtime -run 'Image.*(Schema|Family)|Node.*Family|LegacyImage' -count=1`

Expected: failures because the strict fields and resolver do not exist.

- [x] **Step 3: Replace image and node HCL shapes**

Use exact tags:

```go
type ImageConfig struct { Substrate string `hcl:"substrate"`; Kind string `hcl:"kind"`; Source string `hcl:"source"`; SHA256 string `hcl:"sha256,optional"`; Architecture string `hcl:"architecture"`; GuestFamily string `hcl:"guest_family"`; Size string `hcl:"size,optional"` }
```

Add `GuestFamily string hcl:"guest_family,optional"` to `NodeConfig`. Remove old image fields, desired payload keys, schema declarations, and decoder allowances.

- [x] **Step 4: Resolve and persist family**

Resolve family during planning from the referenced image plus optional node override. Persist `guest_family` on image and node public attributes. Fail known conflicts and unresolved unknown before capability calls.

- [x] **Step 5: Migrate all image fixtures and commit**

Update every HCL example to `kind/source/architecture/guest_family`, run `make ci`, and commit `feat(config): require explicit artifact and guest identity`.

---

### Task 3: Structured Execution Migration

**Files:**
- Modify: `pkg/config/schema.go`
- Modify: `pkg/substrate/guest.go`
- Modify: `pkg/driver/capability.go`
- Modify: `pkg/runtime/resource_node.go`
- Modify: `pkg/provider/docker/exec.go`
- Modify: `pkg/provider/firecracker/exec.go`
- Modify: `pkg/provider/libvirt/exec.go`
- Modify: `pkg/provider/docker/docker.go`
- Modify: `pkg/agentexec/node_operation.go`
- Modify: all provisioner HCL fixtures
- Test: provider exec tests and `pkg/runtime/resource_node_test.go`

**Interfaces:**
- Consumes: `ExecRequest`, `ShellKind`, and resolved node family.
- Produces: structured `GuestExec.ExecInNode/ExecBackground`; strict `ProvisionerConfig` without `Inline`.

- [x] **Step 1: Write failing execution tests**

Prove direct argv and environment preservation, explicit Linux shell translation, fake Windows PowerShell/cmd execution without core changes, family/shell mismatch rejection, stdin forwarding, and old `inline` rejection.

- [x] **Step 2: Verify RED**

Run: `go test ./pkg/runtime ./pkg/provider/docker ./pkg/provider/firecracker ./pkg/provider/libvirt -run 'Structured|Shell|Provisioner' -count=1`

- [x] **Step 3: Replace provisioner schema**

Use `Program`, `Args`, `Environment`, `WorkingDir`, `Shell`, and `Background`; require program and shell for exec blocks. Keep file provisioners separate.

- [x] **Step 4: Migrate runtime and connection drivers**

Pass structured requests end-to-end. Translate shell intent only inside the connection/provider implementation. Preserve argument boundaries and environment without runtime string joining.

- [x] **Step 5: Delete legacy execution APIs**

Remove `ExecSpec`, `Connection.ExecInline`, inline-resolution helpers, and production implicit `sh -c` paths. Migrate route/readiness code to structured requests owned by the relevant guest-network driver.

- [x] **Step 6: Run full/race/removal tests and commit**

Run `go test ./...`, `go vet ./...`, focused race on runtime/providers, and require no production `ExecSpec` or `inline` matches. Commit `refactor(exec): require structured guest requests`.

---

### Task 4: Immutable Image And Kernel Plans

**Files:**
- Modify: `pkg/artifact/resolver.go`
- Modify: `pkg/runtime/resource_image.go`
- Modify: `pkg/runtime/resource_kernel.go`
- Modify: `pkg/runtime/plan_fingerprint.go`
- Modify: `pkg/api/plan_service.go`
- Modify: provider image preparation files
- Test: artifact, runtime image/kernel, and stored-plan tests

**Interfaces:**
- Consumes: `ArtifactIdentity` and strict image HCL.
- Produces: `artifact.ResolveIdentity(ctx, spec)`, immutable provider preparation, stored-plan digest verification.

- [x] **Step 1: Write failing identity and stored-plan tests**

Cover local/HTTP digest computation, OCI tag-to-image-ID binding, metadata copy safety, image and kernel fingerprint inclusion, source mutation rejection, and secret canary exclusion.

- [x] **Step 2: Verify RED**

Run: `go test ./pkg/artifact ./pkg/runtime ./pkg/api -run 'Identity|Immutable|StoredPlan|Digest' -count=1`

- [x] **Step 3: Resolve immutable identities before apply**

Canonicalize kind/source/architecture/family, calculate `sha256:<hex>`, and make provider `PrepareImage` consume only `ArtifactIdentity`. OCI resolution stores immutable image ID/digest, never only the mutable tag.

- [x] **Step 4: Bind and verify stored plans**

Serialize resolved image and kernel identities into plan inputs and fingerprint them. Stored-plan execution re-observes each source and rejects digest changes before provider mutation.

- [x] **Step 5: Delete loose artifact types and commit**

Remove `ImageSpec`, old `ImageRef` fields that duplicate identity, and URL-hash-as-identity behavior. Run full/vet/race and commit `feat(artifact): bind plans to immutable identities`.

---

### Task 5: Reset Runtime, CLI, And Recovery

**Files:**
- Create: `pkg/runtime/reset.go`
- Create: `pkg/runtime/reset_test.go`
- Create: `cmd/sysbox/commands/reset.go`
- Modify: `pkg/controlplane/plan.go`
- Modify: `pkg/runtime/plan.go`
- Modify: `pkg/runtime/transaction.go`
- Modify: command registration and API run models

**Interfaces:**
- Consumes: `driver.Reset`, immutable baselines, state v6, graph ordering, recorder.
- Produces: `BuildResetPlan(graph, state, target)`, `Executor.Reset(ctx, plan)`, CLI `reset`.

- [x] **Step 1: Write failing plan and recovery tests**

Test whole-topology selection, exact node target, invalid target kinds, reverse prepare/order and forward apply/order, checkpoint before/after mutation, retry observation, attachment restoration, refresh, residue failure, and `prevent_destroy` independence.

- [x] **Step 2: Verify RED**

Run: `go test ./pkg/runtime ./cmd/sysbox/commands -run 'Reset' -count=1`

- [x] **Step 3: Add reset-only action and planning**

Add `PlanActionReset`; reject it from normal apply. Build a dedicated reset plan containing only nodes, pinned baselines, and stable target validation.

- [x] **Step 4: Implement checkpointed reset orchestration**

Preflight every selected node before mutation. Prepare in reverse dependency order, apply fresh stopped handles in dependency order, restore NICs, start, observe, refresh, and patch external IDs atomically.

- [x] **Step 5: Add CLI and recovery entry points**

Implement `sysbox reset [--target sysbox_node.name] [--auto-approve]`; print exact plan, require approval, record run/checkpoint, and resume incomplete reset using provider observation.

- [x] **Step 6: Run focused/full/race and commit**

Commit `feat(runtime): add checkpointed topology reset`.

---

### Task 6: Docker Reset

**Files:**
- Create: `pkg/provider/docker/reset.go`
- Test: `pkg/provider/docker/reset_test.go`
- Test: privileged Docker reset E2E

- [x] **Step 1: Write failing reset lifecycle tests**

Assert digest-pinned recreate, changed external ID, stable declared identity, exact owned old-container deletion, retry idempotency, and residue observation.

- [x] **Step 2: Implement provider-owned Docker reset**

Persist old/new container ownership anchors in the opaque reset handle. Recreate from immutable OCI ID and resolved NodeSpec; return a stopped handle for runtime NIC restoration.

- [x] **Step 3: Run unit/privileged tests and commit**

Commit `feat(docker): reset nodes from immutable images`.

---

### Task 7: Libvirt Overlay Reset

**Files:**
- Create: `pkg/provider/libvirt/reset.go`
- Modify: libvirt node disk creation to require per-run overlay
- Test: libvirt reset unit and real acceptance tests

- [ ] **Step 1: Write failing overlay ownership tests**

Assert immutable base never changes, overlay path is topology-owned, old domain/overlay/seed removal, fresh overlay/domain identity, retry recovery, and exact residue reporting.

- [ ] **Step 2: Implement stopped fresh-handle reset**

Destroy the old owned domain, discard only its overlay and seed, create a new overlay from the verified base, rebuild domain state, and return before start so runtime restores NICs.

- [ ] **Step 3: Run real libvirt reset twice and commit**

Commit `feat(libvirt): reset guests with qcow2 overlays`.

---

### Task 8: Firecracker Writable Rootfs Reset

**Files:**
- Create: `pkg/provider/firecracker/reset.go`
- Modify: Firecracker node creation to always use an owned writable rootfs
- Test: Firecracker reset unit and privileged recovery tests

- [ ] **Step 1: Write failing rootfs ownership tests**

Assert baseline digest stability, old VMM/socket/rootfs/VM-dir deletion, fresh owned rootfs, stable logical identity, recovery after each checkpoint, and residue reporting.

- [ ] **Step 2: Implement reset mechanics**

Stop the old VMM, discard only owned mutable artifacts, recreate the writable rootfs and VM directory from immutable baseline, and return a stopped handle.

- [ ] **Step 3: Run privileged tests and commit**

Commit `feat(firecracker): reset guests from immutable rootfs`.

---

### Task 9: Three-Cycle Heterogeneous Acceptance

**Files:**
- Create: `tests/e2e/heterogeneous_reset.sh`
- Create: `tests/e2e/heterogeneous_reset_inner.sh`
- Modify: heterogeneous fixture, Makefile, verification docs, examples

- [ ] **Step 1: Extend the real matrix with state mutation markers**

Write a unique marker into each guest, capture external IDs and logical MAC/IP, reset the topology, and prove markers and prior external artifacts are absent while logical network identity and digest remain stable.

- [ ] **Step 2: Execute three reset-and-run cycles**

For each cycle prove six directed communication edges, structured exec, equivalent topology health, pure repeated plan, and zero superseded container/domain/overlay/rootfs/process residue.

- [ ] **Step 3: Test targeted reset**

Reset each provider node individually and prove non-target external IDs remain unchanged while target mutable state is replaced.

- [ ] **Step 4: Run final gates and removal audit**

Run full, vet, focused race, CI, privileged network tests, heterogeneous matrix, heterogeneous reset, secret canary, `git diff --check`, and searches forbidding legacy schema/execution/image paths.

- [ ] **Step 5: Document evidence and commit**

Record commands, digest identities, three cycle results, target results, recovery evidence, and zero residue. Commit `test(batch5): verify guest image and reset abstractions`.
