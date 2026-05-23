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

make api-up                        # API + Postgres, default service mode
make api-up-netns                  # add host netns privileges for veth/tap labs
make api-up-fc                     # add host netns + Firecracker mounts
make api-up-libvirt                # add host netns + libvirt socket
make api-up-full                   # host netns + Firecracker + libvirt
make api-down
make api-logs
```

Compatibility aliases are kept for muscle memory: `make up`, `make down`, `make docker-up`, `make docker-up-fc`, `make docker-down`, and `make docker-logs`.

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

The API server is the service-mode control plane. It uses API-owned workspaces under `data/workspaces`. Compose defaults to Postgres for state, run records, checkpoints/action logs, and health snapshots, so API state does not have to live beside local CLI state files. The local `data/` mount still holds workspaces and acts as the fallback store when no Postgres backend is configured.

```bash
make api-up
curl http://127.0.0.1:9876/v1/health
curl http://127.0.0.1:9876/v1/topologies
curl http://127.0.0.1:9876/v1/topologies/two-networks/preflight
curl -X POST http://127.0.0.1:9876/v1/topologies/two-networks/apply
```

Compose deployment modes are explicit:

| Target | Use when | Extra host access |
|---|---|---|
| `make api-up` | API management, Postgres state, Docker socket access | Docker socket only |
| `make api-up-netns` | Real Linux bridge/netns/veth/tap topologies | `privileged`, host network, host pid |
| `make api-up-fc` | Firecracker/microVM topologies | netns mode, `/dev/kvm`, Firecracker binary |
| `make api-up-libvirt` | libvirt/QEMU VM topologies | netns mode, `/var/run/libvirt` |
| `make api-up-full` | mixed virtualization development | netns mode, Firecracker, libvirt |

Default `make api-up` runs the API as a normal Compose service and connects to Postgres through Compose DNS (`sysbox-postgres:5432`). Netns/Firecracker/libvirt modes intentionally opt into host-level privileges; in host networking mode the API reaches Postgres through `127.0.0.1:${SYSBOX_POSTGRES_PORT:-55432}`.

Firecracker is never auto-mounted. Set the binary path and choose the explicit Firecracker mode:

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
GET  /v1/runs/{run_id}/actions
POST /v1/runs/{run_id}/resume
POST /v1/runs/{run_id}/recover
POST /v1/runs/{run_id}/cleanup
```

`DELETE /v1/topologies/{name}` removes workspace/state metadata only when the topology is empty. If state still contains resources, it returns `409`; call `POST /destroy` first. `force=true` is intentionally explicit for metadata-only deletion while leaving external resources behind.

## State And Recovery

sysbox supports local state and service backends. The service path now includes:

- topology metadata/listing from the backend
- serial/CAS writes to avoid last-writer-wins
- backend lease/lock metadata
- run persistence in the API store
- checkpoint/action log persistence in the API store
- health snapshot persistence in the API store
- checkpoint-driven recover/cleanup for Docker, local networks, and microVM leftovers
- snapshots where the backend supports them

Postgres is the default backend in Docker Compose. Local CLI still defaults to local state files unless `--backend` or `SYSBOX_STATE_BACKEND` is used. When `SYSBOX_STATE_BACKEND` is a Postgres URL, the API also stores runs/checkpoints/health in Postgres tables. The default Compose URL uses the service name; the netns override switches to the host-published Postgres port because host networking cannot use Compose service DNS.

## Artifacts And Environment

Recommended configuration:

| Variable | Meaning |
|---|---|
| `SYSBOX_HOME` | Service data root, default `/var/lib/sysbox` |
| `SYSBOX_CACHE` | Artifact/cache root, default `/var/cache/sysbox` |
| `SYSBOX_API_LISTEN` | API listen address |
| `SYSBOX_API_TOKEN` | Optional API Bearer token |
| `SYSBOX_WORKSPACES_DIR` | Override API workspace directory |
| `SYSBOX_RUNS_DIR` | Override local run/checkpoint directory when no API database is used |
| `SYSBOX_STATE_BACKEND` | State/API backend URL for service mode; compose uses Postgres |
| `SYSBOX_SUPERVISOR_POLICY` | `observe_only` or `restart_on_crash`, default `observe_only` |
| `SYSBOX_SUPERVISOR_INTERVAL` | Supervisor scan interval, default `30s`; set `0`/`off` to disable |
| `SYSBOX_FIRECRACKER_BIN` | Exact Firecracker binary path |
| `SYSBOX_FIRECRACKER_KERNEL` | Default Firecracker kernel path; HCL `sysbox_kernel` is preferred |
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
