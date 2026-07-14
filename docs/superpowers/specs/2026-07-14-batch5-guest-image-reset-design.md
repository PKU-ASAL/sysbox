# Batch 5 Guest, Image, And Reset Design

Date: 2026-07-14

## 1. Goal And Scope

Batch 5 makes guest semantics, execution, immutable artifacts, and reset
provider-neutral. It delivers four ordered changes:

1. guest-family contracts and persisted state;
2. structured guest execution with explicit shell selection;
3. content-addressed image identities and digest-bound stored plans;
4. topology and targeted node reset with checkpoint recovery.

The change is intentionally breaking. It has no legacy decoder, migration
command, implicit shell path, loose image compatibility type, or reset cleanup
script.

## 2. State V6 Boundary

`state.SchemaVersion` increases from 5 to 6 once, in the first implementation
commit. Loading any state whose version is not 6 fails immediately with an
`IncompatibleVersionError` whose user-facing message requires destroy/recreate
or deletion of the old state. Runtime never mutates, imports, or partially
loads v5 state.

V6 image public state contains its artifact kind, canonical non-secret source,
verified digest, architecture, guest family, provider image ID, and
non-sensitive metadata. V6 node public state contains the resolved guest
family. Provider handles, overlay paths, snapshot IDs, connection coordinates,
and reset observations remain runtime-private state.

No v5-to-v6 migration function or compatibility fixture is retained.

## 3. Public Guest Contracts

The provider-neutral public layer defines:

```go
type GuestFamily string

const (
    GuestFamilyLinux   GuestFamily = "linux"
    GuestFamilyWindows GuestFamily = "windows"
    GuestFamilyUnknown GuestFamily = "unknown"
)

type ShellKind string

const (
    ShellNone       ShellKind = "none"
    ShellLinux      ShellKind = "linux"
    ShellPowerShell ShellKind = "powershell"
    ShellCmd        ShellKind = "cmd"
)

type ExecRequest struct {
    Program     string
    Args        []string
    Environment map[string]string
    WorkingDir  string
    Shell       ShellKind
    Stdin       io.Reader
}
```

`ShellNone` executes `Program` and `Args` directly. Other shell kinds express
intent; the selected connection driver owns quoting, encoding, invocation, and
stdin transport. Runtime never inserts `sh -c`, PowerShell, cmd.exe, systemd,
filesystem roots, or guest networking commands.

`GuestExec`, background execution, and connection execution consume structured
requests. `ExecSpec`, implicit string command lists, and `Connection.ExecInline`
are deleted after all consumers migrate. File copy remains a separate structured
operation.

## 4. Guest Family Resolution

`sysbox_image.guest_family` accepts only `linux`, `windows`, or `unknown`.
Artifact/provider metadata may derive a family for known sources. Existing
rootfs and qcow2 fixtures explicitly declare `linux`; inference is not based on
file names. Docker image platform metadata may provide its family when reliable.

`sysbox_node.guest_family` is an optional override. Resolution follows these
rules:

1. a known node override must equal a known image family;
2. a known override resolves an unknown image family for that node;
3. without an override, the node inherits its image family;
4. an unresolved `unknown` node cannot use execution, guest network
   initialization, provisioning, or reset;
5. unsupported family/provider and family/shell pairs fail during plan.

The resolved family is persisted on image and node resources. Drivers consume
the public enum; runtime does not branch on provider names.

## 5. Immutable Artifact Model

Loose source-specific image fields are replaced by:

```go
type ArtifactKind string

const (
    ArtifactOCI       ArtifactKind = "oci"
    ArtifactRootFS    ArtifactKind = "rootfs"
    ArtifactQCow2     ArtifactKind = "qcow2"
    ArtifactRaw       ArtifactKind = "raw"
    ArtifactKernel    ArtifactKind = "kernel"
    ArtifactISO       ArtifactKind = "iso"
    ArtifactDriverISO ArtifactKind = "driver_iso"
)

type ArtifactIdentity struct {
    Kind         ArtifactKind
    Source       string
    Digest       string
    Architecture string
    GuestFamily  GuestFamily
    Metadata     map[string]string
}
```

The image HCL becomes:

```hcl
resource "sysbox_image" "ubuntu" {
  substrate    = substrate.libvirt.local
  kind         = "qcow2"
  source       = "/images/ubuntu.qcow2"
  sha256       = "<64 lowercase hex characters>"
  architecture = "amd64"
  guest_family = "linux"
}
```

`docker_ref`, `rootfs`, and `qcow2` are removed. Unknown attributes are errors;
there is no compatibility decoder. Kernel resources adopt the same identity
contract in this batch so formal plans pin both images and kernels.

Development CLI planning may accept a mutable OCI tag or a local/remote source
without a supplied SHA256. Before apply, artifact resolution computes the
actual digest and produces an immutable `ArtifactIdentity`. Apply uses only the
resolved identity. Formal stored plans serialize the identity and fingerprint
its digest. Executing a stored plan re-observes the source and rejects any
digest mismatch; it never resolves the plan to a newer object.

Credentials, license material, private URLs, and resolved secret values are not
artifact metadata. Secret references are resolved only at driver call time and
are excluded from durable identities except for a non-reversible reference
fingerprint where required.

## 6. Structured Provisioning HCL

The old `inline` command list and implicit `sh -c` behavior are removed. Exec
provisioners use:

```hcl
provisioner "exec" {
  program     = "/usr/bin/install"
  args        = ["-d", "/opt/lab"]
  environment = { MODE = "test" }
  working_dir = "/"
  shell       = "none"
  background  = false
}
```

`program` and `shell` are mandatory. `args`, `environment`, `working_dir`, and
`background` are optional. Shell execution is explicit: a script fragment is
the `program` for `linux`, `powershell`, or `cmd`; `args` remain distinct
arguments supplied to that shell mode. Empty programs, unknown shell kinds,
family/shell conflicts, and unsupported connection capabilities fail during
plan.

Docker uses daemon exec. Firecracker uses its vsock or SSH connection driver.
Libvirt uses SSH for Linux. A future WinRM or QEMU guest-agent connection is
added by registering a driver capability, without changing runtime, identity,
network attachments, or plan types.

## 7. Reset Model

Reset is a first-class operation with a dedicated plan, run record, checkpoint
steps, observations, and refresh. `controlplane.PlanActionReset` is valid only
in reset plans; normal apply rejects it. The CLI is:

```text
sysbox reset [--target sysbox_node.<name>] [--auto-approve]
```

Without `--target`, the reset plan contains every managed node. A target must
be exactly one declared `sysbox_node` resource address. Data sources, networks,
routers, policies, images, kernels, and undeclared nodes are invalid targets.

The runtime reset sequence is:

1. validate v6 state, immutable baselines, resolved families, and reset driver
   capabilities before provider mutation;
2. record the reset run and immutable plan fingerprint;
3. process selected nodes in reverse dependency order to stop and discard
   mutable guest state;
4. process selected nodes in dependency order to recreate from the pinned
   baseline, restore declared attachments, start, and observe guest readiness;
5. refresh selected nodes and their attachment observations;
6. commit state only after each checkpointed provider transition;
7. finish only when topology health is equivalent to the pre-reset declared
   baseline and no superseded owned artifacts remain.

Reset preserves resource address, declared MAC/IP, logical attachment names,
image digest, and topology lineage. Provider external IDs may change and are
updated atomically in state.

## 8. Provider Ownership

A new `driver.Reset` capability owns provider mechanics:

```go
type ResetRequest struct {
    Current  NodeHandle
    Node     NodeSpec
    Baseline ArtifactIdentity
}

type Reset interface {
    PrepareReset(context.Context, substrate.ResetRequest) (substrate.ResetHandle, error)
    ApplyReset(context.Context, substrate.ResetHandle) (substrate.NodeHandle, error)
    ObserveReset(context.Context, substrate.ResetHandle) (substrate.ResetObservation, error)
    CleanupReset(context.Context, substrate.ResetHandle) error
}
```

The exact reset handle is opaque, versioned provider state. Observation reports
phase, convergence, old and new external IDs, baseline digest, and a bounded
residue inventory without exposing secret values.

`ResetRequest.Node` contains the same resolved, secret-safe declaration used
for normal creation. Runtime resolves secrets immediately before the provider
call and never persists the resolved request. Reset drivers return a stopped
fresh node handle; runtime restores declared NIC attachments through the normal
NIC capability before starting and observing the node.

- Docker recreates the container from the digest-pinned OCI identity and
  declared volumes. It deletes only the superseded topology-owned container.
- Libvirt uses a per-run qcow2 overlay. Reset destroys the domain, discards the
  old overlay, creates a new overlay from the verified immutable base, and
  recreates the domain and seed. The base image is never modified.
- Firecracker uses a per-run rootfs overlay/copy owned by the node. Reset stops
  the VMM, discards the old writable rootfs and VM directory, rebuilds from the
  pinned baseline, and starts a new VMM.

The runtime never understands overlay formats, Docker create options, libvirt
snapshots, Firecracker processes, SSH, WinRM, or guest-agent commands.

## 9. Failure And Recovery

Every reset transition writes a checkpoint before and after external mutation.
Provider reset handles contain sufficient ownership anchors to observe whether
old state was removed and new state exists. A retry reconstructs the handle,
observes the current phase, and continues idempotently. It never reports a
half-reset guest as healthy.

Failure preserves the checkpoint and last valid state patch. Cleanup deletes
only superseded objects carrying exact topology/node ownership. Missing
unmanaged objects, digest mismatch, unknown family, unsupported reset, and
residue are stable categorized errors. A residue error lists exact owned
objects and keeps the run failed until recovery or explicit destroy completes.

## 10. Acceptance

Automated acceptance requires:

- v5 and every non-v6 state are rejected with destroy/recreate guidance;
- no production references to `ExecSpec`, `inline`, `docker_ref`, `rootfs`, or
  `qcow2` remain as public compatibility fields;
- Linux direct and explicit shell execution pass through structured requests;
- a fake Windows driver supports PowerShell/cmd without core changes;
- family conflicts and unresolved unknown families fail during plan;
- development sources resolve to verified identities before apply;
- stored image and kernel plans contain digests and reject source mutation;
- secret canary scans find no resolved secret in plan, state, checkpoint, API,
  logs, artifact metadata, or reset observations;
- Docker, libvirt, and Firecracker each pass targeted reset;
- a complete heterogeneous topology passes three consecutive reset-and-run
  cycles with equivalent health, stable logical network identity, pinned
  digests, and no prior-run container, domain, overlay, VM directory, VMM
  process, namespace, link, route, NAT, or policy residue;
- full tests, vet, focused race tests, CI, privileged network acceptance, and
  heterogeneous reset acceptance pass with a clean worktree.

## 11. Delivery Order

Implementation uses separate reviewed commits that keep the repository green:

1. public guest/artifact/exec/reset contracts and state v6 rejection;
2. strict HCL schemas and guest-family resolution;
3. structured connection and provider execution migration, then deletion of
   old execution types;
4. immutable image/kernel preparation and stored-plan binding, then deletion
   of loose image fields;
5. reset runtime, CLI, checkpoint recovery, and target validation;
6. Docker reset;
7. libvirt overlay reset;
8. Firecracker writable-rootfs reset;
9. three-cycle heterogeneous acceptance, removal audit, and documentation.
