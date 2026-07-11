# Sysbox Heterogeneous Topology Runtime Design

## 1. Purpose

Sysbox is a declarative runtime for local security-lab topologies. It connects
Docker containers, virtual machines, microVMs, and Linux networking while
providing planned, recoverable, and disposable lifecycle management.

Sysbox borrows the correctness properties of Terraform-style infrastructure as
code, but it is not a general Terraform replacement. Its differentiating scope
is heterogeneous local runtimes, explicit Linux data-plane construction,
security-oriented isolation, deterministic recovery, and residue-free cleanup.
The architecture must admit a later Windows VM driver without core changes, but
implementing that driver is outside this program.

This design upgrades the existing implementation in five independently
mergeable batches:

1. Core identity and plan semantics.
2. Typed schema and state.
3. Resource-handler and driver boundaries.
4. Network convergence and controlled egress.
5. Guest abstractions, immutable images, and reset.

The experiment layer remains outside Sysbox. Agent prompts, C2 workflows,
telemetry, detection, truth labels, and episode scoring are consumers of
Sysbox, not Sysbox resources.

## 2. Design Principles

1. A stored plan must describe exactly what apply executes.
2. Every managed object has one stable structured address across configuration,
   graph, plan, state, checkpoint, API, and logs.
3. The core understands resource convergence, not Docker, libvirt, Firecracker,
   Linux commands, or guest operating systems.
4. Resource handlers own resource semantics. Drivers own atomic interaction
   with external systems.
5. Unsupported capabilities fail during validation or planning, before external
   resources are mutated.
6. Network intent uses stable logical names. Kernel interface names, PIDs,
   namespaces, and provider IDs are private driver state.
7. Apply, recovery, and destroy converge from observed reality. They do not
   assume that the previous process completed successfully.
8. Sensitive values are references until execution and are not persisted in
   plans, state, checkpoints, API payloads, or logs.
9. This is a deliberate breaking architecture release. Legacy HCL addressing,
   state schemas, internal APIs, and compatibility adapters are not preserved.
   Old state is rejected without mutation and users recreate managed labs.
10. Features not required by heterogeneous security topologies are excluded.

## 3. Target Architecture

```text
CLI / HTTP API / Remote Host Agent
                 |
          Application Services
                 |
    Configuration and Topology Core
      address / graph / plan / state
      execution / recovery / diagnostics
                 |
          Resource Handlers
 node / image / network / attachment / router
 route / nat / firewall / access / actor
                 |
              Drivers
 Docker / libvirt / Firecracker / Linux network
 SSH / WinRM / guest agent / artifact storage
```

CLI, HTTP API, and remote execution use the same application services:
validate, plan, apply stored plan, refresh, destroy, inspect, and reset. No
transport implements a second lifecycle path.

### 3.1 Topology Core

The core owns:

- configuration parsing and strict diagnostics;
- structured resource addresses and reference resolution;
- dependency graph construction;
- state lineage, serials, locks, and backend capability enforcement;
- plan generation and stored-plan validation;
- deterministic action scheduling;
- checkpoints, recovery decisions, and ownership reconciliation.

The core does not execute `docker`, `virsh`, `ip`, `nsenter`, `iptables`,
`nft`, SSH, PowerShell, or guest-specific commands.

### 3.2 Resource Handlers

A resource handler defines what a resource means:

```go
type ResourceHandler interface {
    Type() string
    Schema() ResourceSchema
    ValidateConfig(context.Context, ConfigValue) Diagnostics
    PlanChange(context.Context, PriorState, ConfigValue) PlannedChange
    Read(context.Context, ResourceContext, PriorState) ReadResult
    Create(context.Context, ResourceContext, ConfigValue) ApplyResult
    Update(context.Context, ResourceContext, PriorState, ConfigValue) ApplyResult
    Delete(context.Context, ResourceContext, PriorState) Diagnostics
}
```

Import is an optional capability. Update is advertised only when the handler
performs a real in-place update. Otherwise changes plan as replacement.

Handlers compose driver capabilities. They do not inspect containers, domains,
network namespaces, or host processes directly.

### 3.3 Drivers

Drivers expose small capability interfaces instead of one expanding substrate
interface:

```text
NodeDriver       create, observe, start, stop, delete
NICDriver        attach, detach, observe
SnapshotDriver   create, restore, delete
ConsoleDriver    open console
GuestExecDriver  exec, copy to, copy from
NetworkDriver    bridge, namespace, route, NAT, firewall ruleset
ArtifactDriver   resolve, verify, cache
```

Docker, libvirt, Firecracker, and Linux networking implement only the
capabilities they support. Capability selection and preflight happen during
planning.

## 4. Batch 1: Core Identity And Plan Semantics

### 4.1 Structured Resource Addresses

String concatenation is replaced by a canonical address model:

```go
type ResourceAddress struct {
    ModulePath []ModuleInstance
    Type       string
    Name       string
    Key        InstanceKey
}
```

Canonical forms include:

```text
sysbox_node.web
sysbox_node.web[0]
sysbox_node.web["frontend"]
module.network.sysbox_network.dmz
```

The structured form is the identity in graph nodes, state, plan actions,
checkpoints, API DTOs, import, dependency edges, and logs. Parsing and rendering
are centralized. Keys are never encoded by underscore concatenation.

### 4.2 Strict Configuration Diagnostics

Configuration processing becomes an explicit pipeline:

```text
parse
  -> static schema validation
  -> variable binding
  -> expression evaluation
  -> reference and address resolution
  -> graph validation
  -> handler and driver validation
```

Every error includes source range, resource address where applicable, summary,
and detail. Local, module-output, and expression failures are never ignored.
Missing environment values are errors when required, not empty strings.

### 4.3 Honest Plan Actions

Initial actions are:

```text
Create / Read / NoOp / Replace / Delete / Unknown
```

`Update` is introduced per resource only after in-place behavior exists. Apply
does not reinterpret Update as replacement.

A planned change records before and after values, changed attribute paths,
replacement reason, dependency reason, and diagnostics. A stored plan binds:

- topology revision and configuration digest;
- state lineage and serial;
- resource schema versions;
- selected driver identities and versions;
- resolved artifact digests;
- non-secret variable digest.

Apply rejects stale or incompatible plans before mutation.

### 4.4 Lifecycle Semantics

`prevent_destroy` makes planning fail if a plan would delete or replace the
resource. It does not produce a partial topology by silently skipping deletion.
`ignore_changes` uses typed attribute paths. `replace_triggered_by` may be added
after structured addresses are stable. General `create_before_destroy` is not
part of this design because fixed addresses, ports, and local network names
frequently prevent parallel existence.

### 4.5 Batch 1 Acceptance

- Count, for-each, and module instances have canonical collision-free addresses.
- All existing graph, plan, state, checkpoint, CLI, and API tests use the same
  address parser and renderer.
- Invalid locals, module outputs, variables, and references fail validation with
  source diagnostics.
- Every displayed plan action matches the external operation executed by apply.
- Stored plans reject changed configuration, state serial, driver selection, or
  artifacts before any resource change.

## 5. Batch 2: Typed Schema And State

### 5.1 Resource Schema

Each attribute declares:

```text
type
required / optional / computed
sensitive
immutable or update behavior
default
validation
nested shape
state persistence policy
```

Diffing operates on typed attribute paths rather than hashes of ad hoc maps.
Desired hashes remain as audit data only and are not the semantic source of
truth.

### 5.2 State Model

State separates public attributes from opaque driver data:

```go
type ResourceState struct {
    Address       ResourceAddress
    ResourceType  string
    Driver        string
    SchemaVersion int
    ExternalID    string
    Attributes    DynamicValue
    Private       json.RawMessage
    Dependencies  []ResourceAddress
    Status        ResourceStatus
    CreatedAt     time.Time
    UpdatedAt     time.Time
}
```

`Attributes` contains typed configured and computed values allowed by the
schema. `Private` belongs exclusively to the selected driver. Driver-private
state has its own versioned codec.

State loading accepts only the current schema version. Loading an unsupported
version fails without modifying the state and identifies the binary version
required to destroy the old lab before upgrading. Golden fixtures cover the
current schema and incompatible-version rejection.

### 5.3 Read And Refresh

Read returns refreshed typed state and one explicit status:

```text
present / absent / drifted / degraded / unknown
```

Provider or host errors produce unknown, not healthy. Unknown state blocks
destructive automatic repair unless an explicit recovery policy allows it.
Computed attributes are refreshed into state using CAS.

Dependency changes do not automatically replace all dependents. Each dependent
handler plans against the changed upstream value and decides whether the change
is NoOp, Update, or Replace.

### 5.4 State Backend Capabilities

Backends advertise locking, CAS, snapshot, delete, lease, and force-unlock
support. Apply, destroy, import, and recovery require lock and CAS by default.
Unsafe backends require an explicit CLI/API override and mark the run unsafe.

Local, SQLite, and Postgres remain supported mutation backends. HTTP and S3
remain unsafe read/write compatibility backends and cannot claim concurrent
mutation safety.

### 5.5 Sensitive Values

Configuration carries secret references, not resolved plaintext. Resolution
occurs immediately before the driver operation. The resolved value is excluded
from plans, state, checkpoints, API responses, and logs.

Existing fields are audited first: node and actor environment, connection
password/private key, provisioner content, authorized keys, and provider config.

### 5.6 Batch 2 Acceptance

- Resource diffing is typed and reports nested attribute paths.
- Current state fixtures round-trip deterministically and incompatible state is
  rejected without mutation.
- Refresh writes computed observations and distinguishes absence from read error.
- Unsafe backend mutation is rejected by default.
- Secret canary values do not occur in plan, state, checkpoint, API, or log
  artifacts.

## 6. Batch 3: Resource And Driver Boundaries

### 6.1 Replacement Strategy

The current `ResourceProvider` is replaced by `ResourceHandler`. Existing
substrates are replaced by capability drivers. Resources move one at a time:

1. image and kernel;
2. network and attachment;
3. node;
4. router, route, and NAT;
5. firewall and access;
6. actor.

No compatibility adapter or permanent dual path is introduced. A branch must
compile and pass tests after each resource moves, and the legacy interface is
deleted as soon as its last consumer moves.

### 6.2 Runtime Purity

Runtime and application-service packages must not import concrete driver
packages or invoke external infrastructure commands. A package-level dependency
test enforces this boundary.

Router orchestration no longer calls Docker inspect, nsenter, or iptables.
Instead it composes node and Linux-network capabilities using attachment handles
obtained from typed state.

### 6.3 Capability Preflight

Handlers declare required capabilities from the desired configuration. For
example, a VM with reset enabled requires Node, NIC, Snapshot, and GuestExec.
Planning reports unsupported combinations before apply.

Driver observations and errors use stable categories such as not-found,
unavailable, permission-denied, invalid-state, and unsupported. Core recovery
does not parse command output strings.

### 6.4 Import

Import is handler-owned and follows parse ID, read remote object, normalize typed
state, and save using CAS. Unsupported resources fail before state mutation.
Imported resources produce a meaningful next plan rather than unconditional
replacement caused by a missing desired hash.

### 6.5 Batch 3 Acceptance

- Runtime contains no direct Docker, libvirt, Firecracker, iproute, namespace,
  iptables, nftables, SSH, or WinRM commands.
- Each built-in resource is implemented through a handler and capability driver.
- Unsupported driver/resource combinations fail during plan.
- Import produces valid typed state and a deterministic subsequent plan.

## 7. Batch 4: Network Convergence And Controlled Egress

### 7.1 Network Attachments

Attachments become stable state objects with:

```text
node address
network address
logical interface name
MAC
IP prefixes
gateway
driver kind
opaque driver state
```

Users and resource handlers reference logical names such as `uplink`; only the
driver maps these to `eth2`, tap names, veth names, namespaces, or domain device
IDs.

### 7.2 Network Resource Responsibilities

Semantic responsibilities are separated:

```text
network     subnet and L2 boundary
attachment node-to-network connection
router      forwarding node
route       routing intent
nat         address translation
firewall    stateful L3/L4 policy
```

The public HCL changes with the new internal boundaries. Examples, tests, and
documentation move in the same commit as each breaking syntax change; no
compatibility decoder is retained.

### 7.3 Atomic Firewall Rulesets

Firewall attaches to a router or node, not an implementation namespace. It
supports:

- input, output, and forward directions;
- explicit default policy;
- source and destination CIDRs;
- source and destination ports;
- TCP, UDP, ICMP, and all protocols;
- logical input and output interfaces;
- new, established, related, and invalid connection states;
- accept, drop, and reject verdicts;
- counters and optional rate-limited logging.

Linux implementation uses topology-owned nftables tables and atomic ruleset
replacement. Rules carry ownership comments. Apply reads back and verifies the
ruleset digest. Destroy deletes only topology-owned tables.

NAT uses the same convergence model and does not append unmanaged iptables
commands.

### 7.4 Controlled Egress

Sysbox provides complete L3/L4 egress enforcement:

```text
isolated workload network
  -> topology router
  -> default-deny firewall
  -> explicitly allowed proxy or CIDRs
  -> NAT uplink
```

Domain filtering remains an L7 proxy responsibility. A proxy runs as a normal
managed node. Sysbox ensures that direct egress is impossible and only
the proxy endpoint is reachable. Proxy-specific experiment policy is not a
firewall feature.

### 7.5 Network Recovery And Cleanup

All bridges, namespaces, veths, taps, routes, rulesets, and NAT objects carry
topology ownership metadata where the external system supports it. Refresh
observes real attachments and rules. Destroy finishes with an ownership scan
and fails with a residue inventory if managed objects remain.

### 7.6 Batch 4 Acceptance

- Docker, libvirt, and Firecracker nodes communicate through one declared
  topology without provider-specific HCL interface names.
- Router firewall enforces a tested default-deny egress plan.
- Repeated apply does not duplicate route, NAT, or firewall rules.
- Interrupted network apply is recoverable from checkpoint and observation.
- Destroy reports no topology-owned namespace, link, tap, bridge, route, NAT,
  firewall, container, or domain residue.

## 8. Batch 5: Guest, Image, And Reset Abstractions

### 8.1 Guest Families And Execution

Node state records a guest family derived from image metadata with an optional
validated override:

```text
linux / windows / unknown
```

Execution is structured:

```go
type ExecRequest struct {
    Program     string
    Args        []string
    Environment map[string]string
    WorkingDir  string
    Shell       ShellKind
    Stdin       io.Reader
}
```

Linux shell, PowerShell, and cmd are explicit modes. Route, readiness, shutdown,
and provisioning logic do not assume Bash, systemd, `/` paths, or the `ip`
command.

Guest management is independent from virtualization. SSH, WinRM, QEMU guest
agent, vsock, serial, and a future Sysbox guest agent are connection drivers.

### 8.2 Immutable Images

Images are content-addressed artifacts with kind, source, digest, architecture,
guest family, and metadata. Mutable tags are accepted only in development runs;
formal stored plans bind the resolved digest.

The artifact model supports Docker/OCI images, rootfs, qcow2, raw disk, kernel,
ISO, and driver ISO as drivers adopt them. Credentials and licenses are secret
references and never artifact metadata.

### 8.3 Baseline And Reset

Reset is a first-class topology operation, not a guest cleanup script. Drivers
implement it through their safest mechanism:

- Docker: recreate from immutable image and declared volumes;
- libvirt: discard per-run qcow2 overlay or restore a verified snapshot;
- Firecracker: discard per-run rootfs overlay or use supported snapshots;
- future Windows VM: discard overlay or restore generalized baseline.

Reset preserves declared topology identity while replacing mutable guest state.
It produces a plan, run, checkpoint, and refreshed state.

### 8.4 Batch 5 Acceptance

- Core and node handler contain no Linux-only command assumptions.
- Linux VM execution continues through structured requests.
- A Windows driver can be added without changing core, state identity, network
  attachment, or plan models.
- Formal plans pin image and kernel digests.
- Three consecutive reset-and-run cycles return equivalent topology health and
  leave no prior-run disk or process state.

## 9. Recovery Model

On startup or explicit recover, Sysbox combines desired configuration, prior
state, checkpoint steps, and external observation. Each resource is classified:

```text
managed and present
managed but absent
present but state patch missing
partially created
orphaned and owned by topology
unknown due to observation failure
```

The handler returns one decision:

```text
noop / adopt / repair / replace / cleanup / manual
```

Automatic action requires verified ownership. Unknown or unowned resources are
never deleted automatically. State patches use CAS. Recovery itself is a run
with an auditable plan and checkpoint.

## 10. Control Plane And Security

Topology, plan, and run remain distinct:

```text
Topology  desired configuration, revision, state lineage
Plan      immutable change set for revision and state serial
Run       one apply, destroy, refresh, recover, or reset execution
```

Experiment episodes are outside this model.

Privileged host operations execute in a host agent with declared capabilities.
The control plane stores intent and schedules plans but does not mount Docker
sockets, manipulate host networking, or execute arbitrary shell commands.
Remote actions are structured and constrained by workspace, driver capability,
artifact policy, and ownership.

## 11. Breaking Delivery

Each batch must be independently testable and mergeable. Compatibility with the
pre-redesign internal architecture, HCL instance addressing, and state format is
not a requirement.

Delivery rules:

1. Add tests that capture current supported behavior before changing a boundary.
2. Introduce the new model at one narrow ownership boundary.
3. Replace one resource or state consumer at a time.
4. Run unit, integration, recovery, and privileged topology tests.
5. Remove the legacy path with its final consumer.
6. Publish breaking HCL and state reset notes.

The redesign introduces a new major state schema. Existing state is rejected
without mutation with a precise incompatibility error. Because Sysbox manages
disposable local labs, users destroy them with the old binary before upgrading,
then apply the new configuration with the new binary. No state migration command
is implemented.

## 12. Test Strategy

Testing scales by layer:

- Address and schema: table-driven unit tests and round-trip properties.
- Configuration: diagnostic golden tests with exact source locations.
- Plan: golden action sets and apply/plan conformance tests.
- State: current-version fixtures, incompatible-version rejection, CAS, lock,
  and secret-canary tests.
- Handlers: contract tests with fake capability drivers.
- Drivers: integration tests against Docker, libvirt, Firecracker, and Linux
  namespaces where available.
- Network: connectivity matrix, deny matrix, repeated apply, interrupted apply,
  refresh, and residue scan.
- Recovery: fault injection after every external mutation/state-patch boundary.
- Guest/reset: repeated baseline cycles and Linux/Windows execution contract
  tests when a Windows driver is introduced.

Privileged tests are explicitly tagged and are not replaced by mocked unit
tests. CI reports skipped capability tests as skipped, not passed.

## 13. Non-Goals

This program does not implement:

- Terraform or OpenTofu compatibility;
- Terraform Plugin Protocol;
- cloud-provider ecosystems;
- remote module registries;
- the complete Terraform function or lifecycle surface;
- arbitrary application orchestration;
- Agent prompts, C2 workflows, sensors, detection, truth, or episode scoring;
- domain allowlisting inside an L3/L4 firewall;
- a Windows driver in the initial five batches.

The design prepares a stable Windows driver boundary but adds Windows only as a
separate, evidence-driven feature after Linux heterogeneous lifecycle and reset
are reliable.
