# Sysbox

English | [简体中文](README.md)

Describe a topology once, then build, validate, and repeatedly reset an experimental environment made of containers, microVMs, virtual machines, and Linux networks.

## Why Sysbox Exists

Real systems and security experiments rarely fit on one machine. An attacker may belong in a container, a target service may need its own kernel, and a database may depend on a full virtual machine. Fixed addresses, routes, NAT, and access policy must connect them into one environment.

Calling Docker, Firecracker, libvirt, and networking scripts can start that environment, but scripts usually cannot answer the questions that matter over its full lifetime. Does reality still match the intended setup? What will a change affect? Which resources exist after an interrupted run? Can an experiment reset without changing its network identity? How can cleanup prove that a resource belongs to this experiment?

Sysbox treats those resources as one stateful dependency graph. You declare the topology you want; Sysbox plans the change, executes it in dependency order, observes external state, and recovers or cleans up safely after interruption.

## How It Works

1. **Declare the topology:** describe nodes, images, networks, routes, policy, and dependencies instead of scripting command order.
2. **Preview changes:** `validate` checks configuration and the dependency graph; `plan` compares declared intent with state and can refresh external state before execution.
3. **Run the lifecycle:** `apply` creates in dependency order, `reset` restores guests from immutable baselines, and `destroy` cleans up in reverse order.
4. **Observe and recover:** on-demand refresh and control-plane observation expose drift, while checkpoints record critical steps for interruption recovery.

One topology can therefore combine Docker containers, Firecracker microVMs, libvirt VMs, isolated networks, routing, NAT, and nftables policy. Sysbox is intended for security research, systems experiments, network validation, and platform engineering that requires environments to be explainable, repeatable, and recoverable.

## Quick Start

You need Linux, Go 1.26, and a Docker Engine accessible to the current user.

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

The same resource addresses, dependency graph, planning, and state model apply to Firecracker and libvirt. See [`examples/mixed`](examples/mixed/) for a complete heterogeneous example.

## Architecture And Extension Boundary

The Sysbox core defines shared resource semantics: the topology graph, plans, state, observation, recovery, and ownership. Providers perform external operations and plug in through capabilities such as node lifecycle, NIC, artifact, guest execution, reset, and policy. Adding an execution environment does not require provider-specific branches in the core, but the provider must implement the complete lifecycle contract required by its use cases.

In API/Agent mode, host Agents register their configured capabilities and preflight verifies the actual environment before execution. The scheduler uses those declarations to assign an **entire topology run** to one Agent that satisfies its combined requirements. Sysbox therefore supports heterogeneous nodes within one topology, but it does not currently place individual nodes from that topology on different Agents or provide a general multi-host overlay network.

The local CLI and API/Agent/Web control plane share the same decoder, planner, executor, state manager, and providers. There is no second topology model. See [Architecture](docs/architecture.md) for the detailed contracts.

## Supported Scope

| Area | Current support |
|---|---|
| Guest providers | Docker, Firecracker, libvirt |
| Networking | Isolated IPv4, static addresses, routes, NAT, Docker aliases |
| Policy | Topology-owned, atomically replaced nftables policy for IPv4 |
| Lifecycle | Validate, plan, apply, targeted/full reset, destroy, recovery |
| State | Local, SQLite, Postgres; HTTP/S3 mutation requires an explicit unsafe override |
| Interfaces | CLI, HTTP API, host Agent, Web console |
| Distribution | Linux amd64/arm64 CLI archives and GHCR API/Agent runtime |

Sysbox is not a general cloud orchestrator and does not support arbitrary Terraform providers. IPv6 policy, arbitrary guest operating systems, cross-Agent node placement, and general cloud resources are outside the current guarantees. A controlled resource and provider scope is what makes identity validation, observation, recovery, and safe deletion possible.

## Documentation

- [Documentation Index](docs/index.md): choose a path by task.
- [Design Principles](docs/design-principles.zh-CN.md): the trade-offs behind Sysbox.
- [Architecture](docs/architecture.md): resource, state, provider, and recovery contracts.
- [Authoring Topologies](docs/guides/authoring-topologies.md): build practical topologies.
- [HCL Reference](docs/reference/hcl.md): configuration fields and constraints.
- [Development](docs/development/contributing.md): contribute to the project.

## Verification And Contributing

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
