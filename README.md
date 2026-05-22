# sysbox

> Terraform-like control plane for Linux lab topologies. sysbox turns HCL into Docker, Firecracker, VM, and network resources through a local CLI or a service-grade HTTP API.

## What It Is

sysbox focuses on three layers:

1. **Declarative topology runtime**: parse HCL, build a dependency graph, plan changes, and converge external resources with apply/destroy.
2. **Provider/substrate execution**: Docker for fast container labs, Firecracker/microVM and VM substrates for stronger isolation, plus Linux network primitives.
3. **Service control plane**: an API server with managed workspaces, state backends, leases, run records, checkpoints, recovery, and cleanup.

The core runtime intentionally does not own research-story concepts such as sensors, labelers, reward, attribution, or IOC scoring. Those belong above sysbox as optional lab/application layers. sysbox’s job is narrower: make topology lifecycle explainable, repeatable, and recoverable.

```
HCL topology
  -> sysbox plan/apply/destroy
  -> runtime graph + provider CRUD
  -> local/Postgres state + checkpointed runs
  -> optional API server for multi-process/service use
```

## Requirements

- Linux with network namespace support.
- Docker daemon for Docker substrate examples.
- Go 1.22+.
- Root or equivalent capabilities for real apply/destroy paths that touch netns, veth, tap, KVM, or Docker socket.
- Firecracker examples additionally need `firecracker`, `/dev/kvm`, `mkfs.ext4`, and `losetup`.
- libvirt examples additionally need libvirt/qemu tooling and a qcow2 image.

Large artifacts are not baked into the sysbox image. Kernels, rootfs images, and qcow2 images should be declared in HCL as `sysbox_kernel` / `sysbox_image` inputs and either mounted explicitly or fetched through the artifact cache. For Firecracker rootfs preparation, see `scripts/prepare-fc-rootfs.sh` and [docs/firecracker-vmbox.md](docs/firecracker-vmbox.md).

## Quick Start

```bash
make build
make plan TOPO=two-networks
sudo -E make apply TOPO=two-networks
sudo -E make destroy TOPO=two-networks
```

Useful example topologies:

| TOPO | Purpose |
|---|---|
| `two-networks` | Docker nodes across two isolated networks with a router |
| `three-nodes` | Docker attacker/web/db lab with optional actor resource |
| `microvm` | Firecracker-focused topology |
| `mixed` | Docker + Firecracker topology |
| `libvirt-vm` | Docker + libvirt VM topology |

## Make Targets

The Makefile is intentionally small. Main targets:

```bash
make build                         # build bin/sysbox
make test                          # unit tests
sudo -E make test-e2e              # integration tests using Docker/netns paths
make lint                          # gofmt + go vet

make plan TOPO=two-networks        # plan an example
sudo -E make apply TOPO=two-networks
sudo -E make destroy TOPO=two-networks

make api-up                        # build image, seed workspaces, start API + Postgres
make api-up-fc                     # same, with Firecracker compose override
make api-down
make api-logs
```

Compatibility aliases are kept for muscle memory: `make up`, `make down`, `make docker-up`, `make docker-down`, and `make docker-logs`.

## CLI

Common commands:

```bash
bin/sysbox -f examples/two-networks/field.sysbox.hcl --state runs/two-networks/state.json validate
bin/sysbox -f examples/two-networks/field.sysbox.hcl --state runs/two-networks/state.json plan
sudo -E bin/sysbox -f examples/two-networks/field.sysbox.hcl --state runs/two-networks/state.json apply --auto-approve
sudo -E bin/sysbox -f examples/two-networks/field.sysbox.hcl --state runs/two-networks/state.json destroy --auto-approve
```

`output` is reserved for HCL topology outputs, matching Terraform-style semantics:

```bash
bin/sysbox -f examples/three-nodes/field.sysbox.hcl output
bin/sysbox -f examples/three-nodes/field.sysbox.hcl output attacker_lab_ip
bin/sysbox -f examples/three-nodes/field.sysbox.hcl output --json
```

State/resource inspection lives under `state`:

```bash
bin/sysbox --state runs/two-networks/state.json state list
bin/sysbox --state runs/two-networks/state.json state show sysbox_node.node_a
bin/sysbox --state runs/two-networks/state.json state get sysbox_node.node_a.primary_ip
```

## API / Docker Compose

The API server is the service-mode control plane. It uses API-owned workspaces under `data/workspaces` and run/checkpoint metadata under `data/runs`. Compose defaults to a Postgres state backend, so state does not have to live beside local CLI state files.

```bash
make api-up
curl http://127.0.0.1:9876/v1/health
curl http://127.0.0.1:9876/v1/topologies
curl http://127.0.0.1:9876/v1/topologies/two-networks/preflight
curl -X POST http://127.0.0.1:9876/v1/topologies/two-networks/apply
```

For Firecracker-capable API containers:

```bash
export SYSBOX_FIRECRACKER_BIN=/home/jiandong/.local/bin/firecracker
make api-up-fc
curl http://127.0.0.1:9876/v1/capabilities
curl http://127.0.0.1:9876/v1/topologies/mixed/preflight
```

`make api-seed` copies `examples/*/field.sysbox.hcl` into `data/workspaces` only when a workspace is missing. After that, API-managed HCL is independent from `examples/`.

Important API endpoints:

```bash
GET  /v1/topologies
GET  /v1/topologies/{name}/plan
GET  /v1/topologies/{name}/outputs
GET  /v1/topologies/{name}/preflight
POST /v1/topologies/{name}/apply
POST /v1/topologies/{name}/destroy
GET  /v1/runs/{run_id}
GET  /v1/runs/{run_id}/checkpoint
POST /v1/runs/{run_id}/recover
POST /v1/runs/{run_id}/cleanup
```

`DELETE /v1/topologies/{name}` removes workspace/state metadata only when the topology is empty. If state still contains resources, it returns `409`; call `POST /destroy` first. `force=true` is intentionally explicit for metadata-only deletion while leaving external resources behind.

## State And Recovery

sysbox supports local state and service backends. The service path now includes:

- topology metadata/listing from the backend
- serial/CAS writes to avoid last-writer-wins
- backend lease/lock metadata
- run persistence
- action checkpoints
- checkpoint-driven recover/cleanup for Docker, local networks, and microVM leftovers
- snapshots where the backend supports them

Postgres is the default backend in Docker Compose. Local CLI still defaults to local state files unless `--backend` or `SYSBOX_STATE_BACKEND` is used.

## Artifacts And Environment

Recommended configuration:

| Variable | Meaning |
|---|---|
| `SYSBOX_HOME` | Service data root, default `/var/lib/sysbox` |
| `SYSBOX_CACHE` | Artifact/cache root, default `/var/cache/sysbox` |
| `SYSBOX_API_LISTEN` | API listen address |
| `SYSBOX_API_TOKEN` | Optional API Bearer token |
| `SYSBOX_WORKSPACES_DIR` | Override API workspace directory |
| `SYSBOX_RUNS_DIR` | Override run/checkpoint directory |
| `SYSBOX_STATE_BACKEND` | State backend URL for service mode |
| `SYSBOX_FIRECRACKER_BIN` | Exact Firecracker binary path |
| `SYSBOX_FIRECRACKER_WORKDIR` | Per-VM Firecracker work directory |

Kernel/rootfs/qcow2 are topology artifacts, not service configuration. Prefer HCL `sysbox_kernel` and `sysbox_image` with `source`, `rootfs`, `qcow2`, and `sha256`. `SYSBOX_ROOTFS` remains a local example convenience variable, not an API deployment contract.

## HCL Resources

| Resource | Description |
|---|---|
| `sysbox_image` | Docker image, Firecracker rootfs, or libvirt qcow2 image declaration |
| `sysbox_kernel` | Firecracker kernel artifact declaration |
| `sysbox_network` | Linux bridge/netns network; `nat=true` uses Docker bridge |
| `sysbox_node` | Docker container, Firecracker microVM, or VM node |
| `sysbox_router` | Multi-interface router node |
| `sysbox_firewall` | nftables rules attached to a network |
| `sysbox_ssh_access` | SSH ingress and authorized key injection |
| `sysbox_actor` | Optional ACP-compatible agent container resource |

## Repository Layout

```
cmd/sysbox/                 CLI and API server entrypoint
cmd/sysbox-init/            Firecracker guest init/RPC helper
examples/                   Example topologies
pkg/artifact/               Artifact resolver/cache
pkg/api/                    HTTP API, jobs, recovery/cleanup
pkg/config/                 HCL schema and eval
pkg/graph/                  Dependency graph
pkg/provider/               Docker, Firecracker, network, libvirt providers
pkg/runtime/                Plan/apply/destroy/checkpoint runtime
pkg/state/                  Local/Postgres/HTTP/S3/SQLite state backends
pkg/substrate/              Provider abstraction
tests/e2e/                  Integration tests with build tag e2e
```
