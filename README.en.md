# Sysbox

English | [简体中文](README.md)

Sysbox brings Terraform-like topology orchestration to labs running on bare-metal Linux hosts: declare, build, validate, and reset experiments that combine containers, microVMs, virtual machines, and Linux networks.

## Why Sysbox Exists

Real-world cybersecurity experiments rarely involve a single node or a single virtualization technology. An attacker may belong in a container, a target service may need its own kernel, and a database may depend on a full virtual machine. Fixed addresses, routes, NAT, and access policy must connect them into one environment.

Managing containers, virtual machines, and network resources separately can make the environment run. But being able to start it is not the same as being able to manage it. Does reality still match the intended setup? What will a change affect? Which resources exist after an interrupted run? Can an experiment reset without changing its network identity? How can cleanup prove that a resource belongs to this experiment?

Sysbox starts with the experiment itself and treats those resources as one stateful dependency graph. You declare the topology you want; one lifecycle then plans changes, follows dependencies, observes state, and handles interruption recovery.

## How It Works

1. **Declare the topology:** describe nodes, images, networks, routes, policy, and dependencies instead of scripting command order.
2. **Preview changes:** `validate` checks configuration and the dependency graph; `plan` compares declared intent with state and can refresh external state before execution.
3. **Run the lifecycle:** `apply` creates in dependency order, `reset` restores guests from immutable baselines, and `destroy` cleans up in reverse order.
4. **Observe and recover:** on-demand refresh and control-plane observation expose drift, while checkpoints record critical steps for interruption recovery.

The result is more than a collection of scripts that happened to start successfully: the environment becomes an experiment you can explain, repeat, and recover. One topology can combine Docker containers, Firecracker microVMs, libvirt VMs, isolated networks, routing, NAT, and nftables policy.

## Container And Virtual Machine Ecosystem

Sysbox can currently combine Docker containers, lightweight KVM microVMs, and full KVM virtual machines managed through libvirt/QEMU in one experimental topology. KVM is the shared virtualization foundation for those VMs, not an interchangeable provider interface; each VMM still needs its own lifecycle, networking, state, and recovery implementation.

| Isolation model | Sysbox integration target | Foundation | Status | Meaning |
|---|---|---|---|---|
| Application container | Docker Engine | Linux namespaces and cgroups; may use containerd internally | Supported | A provider covers the current complete lifecycle |
| Application container | containerd | OCI runtime, Linux namespaces, and cgroups | Architecturally extensible | A runtime provider can be added; there is no direct integration today |
| Kubernetes workload | CRI runtime + CNI | Container or VM sandbox | Architecturally extensible | It spans pod sandbox, image, and networking contracts, requiring a separate integration design |
| Application container / pod | Podman / libpod | OCI runtime, Linux namespaces, and cgroups | Architecturally extensible | A provider can be added; there is no direct integration today |
| System container | LXC / LXD | Linux namespaces and cgroups | Architecturally extensible | Its different lifecycle and networking model needs an adapter |
| microVM | Firecracker | KVM | Supported | A provider directly manages the VMM process and resources |
| microVM | Cloud Hypervisor | KVM | Architecturally extensible | The foundation fits; a provider has not been implemented |
| Sandboxed container | Kata Containers | KVM + multiple VMM options | Architecturally extensible | It spans a container control plane and VM runtime, requiring a separate integration design |
| Full VM | libvirt → QEMU | KVM | Supported | A provider manages QEMU/KVM through libvirt |
| Full VM | QEMU/KVM direct | KVM | Architecturally extensible | QEMU is currently used through libvirt; there is no direct provider |
| Full VM | Xen | Xen hypervisor | Outside current scope | Supporting it would extend the current Linux/KVM execution assumptions |
| Full VM | VMware vSphere / ESXi | ESXi | Outside current scope | It requires different control-plane, identity, and networking integrations |
| Full VM | Hyper-V | Windows hypervisor | Outside current scope | It is outside the current Linux host provider boundary |

“Architecturally extensible” means that the capability driver boundary permits a new implementation, not that the technology is already compatible. A provider enters the supported set only after it implements node lifecycle, networking, state, observation, recovery, and safe-deletion contracts.

## Quick Start

Start with the smallest setup, which depends only on Docker. You need Linux, Go 1.26, and a Docker Engine accessible to the current user.

```bash
git clone https://github.com/PKU-ASAL/sysbox.git
cd sysbox
go build -o bin/sysbox ./cmd/sysbox

bin/sysbox -f examples/docker-service/field.sysbox.hcl validate
bin/sysbox -f examples/docker-service/field.sysbox.hcl plan
bin/sysbox -f examples/docker-service/field.sysbox.hcl apply --auto-approve
bin/sysbox -f examples/docker-service/field.sysbox.hcl destroy --auto-approve
```

See the [Quickstart](docs/quickstart.md) for a complete first run.

## Minimal Topology

The commands above run an ordinary HCL file. The minimal model describes only the execution environment, network, image, and how the node connects to them:

```hcl
substrate "docker" { alias = "local" }

resource "sysbox_network" "lab" {
  cidr = "10.44.0.0/24"
  nat  = true
}

resource "sysbox_image" "alpine" {
  substrate    = substrate.docker.local
  kind         = "oci"
  source       = "alpine:3.22"
  architecture = "amd64"
  guest_family = "linux"
}

resource "sysbox_node" "node" {
  substrate = substrate.docker.local
  image     = sysbox_image.alpine.id

  link "lab" {
    network = sysbox_network.lab.id
    ip      = "10.44.0.10/24"
  }
}
```

When an experiment needs stronger isolation or a full virtual machine, the orchestration model does not change. The same resource addresses, dependency graph, planning, and state model apply to Firecracker and libvirt; see [`examples/mixed`](examples/mixed/) for a complete heterogeneous example.

## Architecture And Extension Boundary

That consistency comes from a simple boundary: the Sysbox core defines what a resource means, while a provider defines how it is implemented. The core manages the topology graph, plans, state, observation, recovery, and ownership. Providers plug in through capabilities such as node lifecycle, NIC, artifact, guest execution, reset, and policy. A new execution environment does not require provider-specific branches in the core, but it does need the lifecycle contract required by its use cases.

The same boundary carries into API/Agent mode. Host Agents register their configured capabilities and preflight verifies the actual environment before execution. The scheduler uses those declarations to assign an **entire topology run** to one Agent that satisfies its combined requirements. Sysbox therefore supports heterogeneous nodes within one topology, but it does not currently place individual nodes from that topology on different Agents or provide a general multi-host overlay network.

The local CLI and API/Agent/Web control plane share the same decoder, planner, executor, state manager, and providers. There is no second topology model. See [Architecture](docs/architecture.md) for the detailed contracts.

## Supported Scope

Sysbox deliberately stays within Linux experiment environments whose lifecycle it can verify. The currently supported boundary is:

| Area | Current support |
|---|---|
| Guest providers | Docker, Firecracker, libvirt |
| Networking | Isolated IPv4, static addresses, routes, NAT, Docker aliases |
| Policy | Topology-owned, atomically replaced nftables policy for IPv4 |
| Lifecycle | Validate, plan, apply, targeted/full reset, destroy, recovery |
| State | Local, SQLite, Postgres; HTTP/S3 mutation requires an explicit unsafe override |
| Interfaces | CLI, HTTP API, host Agent, Web console |
| Distribution | Linux amd64/arm64 CLI archives and GHCR API/Agent runtime |

Terraform-like refers to the declarative planning and lifecycle experience; it does not mean Terraform provider compatibility. Sysbox is also not a general cloud orchestrator. IPv6 policy, arbitrary guest operating systems, cross-Agent node placement, and general cloud resources are outside the current guarantees. That controlled boundary is what makes identity validation, observation, recovery, and safe deletion possible.

## Documentation

The README is only the project entrance. Continue with the document that matches the task ahead:

- [Documentation Index](docs/index.md): choose a path by task.
- [Design Principles](docs/design-principles.zh-CN.md): the trade-offs behind Sysbox.
- [Architecture](docs/architecture.md): resource, state, provider, and recovery contracts.
- [Authoring Topologies](docs/guides/authoring-topologies.md): build practical topologies.
- [HCL Reference](docs/reference/hcl.md): configuration fields and constraints.
- [Development](docs/development/contributing.md): contribute to the project.

## Verification And Contributing

The regular unit tests run directly. Heterogeneous environment checks need the corresponding host capabilities:

```bash
go test ./...
make test-privileged-container
make test-heterogeneous-matrix
make test-heterogeneous-reset
```

The final three commands require a controlled Linux host with Docker, KVM, Firecracker, and libvirt. See [Testing](docs/development/testing.md) for the complete gates.

Read [Contributing](docs/development/contributing.md) before contributing. New resource semantics and provider capabilities must include observation, recovery, and safe-deletion contracts.

## License

[MulanPSL-2.0](LICENSE)
