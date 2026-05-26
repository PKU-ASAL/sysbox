# sysbox

> Terraform-like control plane for Linux lab topologies. sysbox turns HCL into Docker, Firecracker, VM, and network resources through a local CLI or a service-grade HTTP API.

## What It Is

sysbox focuses on three layers:

1. **Declarative topology runtime**: parse HCL, build a dependency graph, plan changes, and converge external resources with apply/destroy.
2. **Provider/substrate execution**: Docker for fast container labs, Firecracker/microVM and VM substrates for stronger isolation, plus Linux network primitives.
3. **Optional control plane + host agent execution**: local CLI owns single-host state by default; registered agents can receive control-plane commands while keeping durable topology state on the host.

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
make lint                          # go vet

make plan TOPO=two-networks        # plan an example
sudo -E make apply TOPO=two-networks
sudo -E make destroy TOPO=two-networks

cp .env.example .env               # one local 12-factor config file
make config                        # inspect resolved compose config
make deploy                        # API + Postgres
make deploy-full                   # API + Postgres + Docker agent
make undeploy
make reset                         # stop compose and remove local Postgres volume
make logs
```

`make up` and `make down` remain local CLI aliases for `apply` and `destroy`.

## CLI

Common commands:

```bash
bin/sysbox -f examples/two-networks/field.sysbox.hcl --state .sysbox/runs/two-networks/state.json validate
bin/sysbox -f examples/two-networks/field.sysbox.hcl --state .sysbox/runs/two-networks/state.json plan
sudo -E bin/sysbox -f examples/two-networks/field.sysbox.hcl --state .sysbox/runs/two-networks/state.json apply --auto-approve
sudo -E bin/sysbox -f examples/two-networks/field.sysbox.hcl --state .sysbox/runs/two-networks/state.json destroy --auto-approve
```

`output` is reserved for HCL topology outputs, matching Terraform-style semantics:

```bash
bin/sysbox -f examples/three-nodes/field.sysbox.hcl output
bin/sysbox -f examples/three-nodes/field.sysbox.hcl output attacker_lab_ip
bin/sysbox -f examples/three-nodes/field.sysbox.hcl output --json
```

State/resource inspection lives under `state`:

```bash
bin/sysbox --state .sysbox/runs/two-networks/state.json state list
bin/sysbox --state .sysbox/runs/two-networks/state.json state show sysbox_node.node_a
bin/sysbox --state .sysbox/runs/two-networks/state.json state get sysbox_node.node_a.primary_ip
```

## API / Docker Compose

The API server is the service-mode control plane. Compose defaults to Postgres for state, run records, checkpoints/action logs, and health snapshots, so API state does not have to live beside local CLI state files. Local runtime data is consolidated under `.sysbox/`: `.sysbox/api` for API-owned data and `.sysbox/runs` for CLI/example state.

For the full deployment model, see [docs/deployment.md](docs/deployment.md).

```bash
cp .env.example .env
make deploy
curl http://127.0.0.1:9876/v1/health
curl http://127.0.0.1:9876/v1/topologies
```

Deployment follows a 12-factor style: keep deploy-time choices in `.env`, keep topology intent in HCL, and keep the command surface small. Start by copying the template:

```bash
cp .env.example .env
```

Use one of two deployment targets:

```bash
make deploy       # control plane only: API + Postgres
make deploy-full  # control plane + Docker agent
make reset        # local reset: removes the Compose Postgres volume
```

`deploy` is the clean control-plane mode. It does not mount the Docker socket
into the API container. `deploy-full` adds `sysbox-agent`, which mounts the
host Docker socket and executes Docker-substrate runs assigned by the API.

For a quick API-driven smoke test:

```bash
make deploy-full
curl -X POST http://127.0.0.1:9876/v1/topologies/docker-service/apply
curl http://127.0.0.1:9876/v1/runs
```

`make seed` copies `examples/*/field.sysbox.hcl` into `.sysbox/api/workspaces`
only when a workspace is missing. After that, API-managed HCL is independent
from `examples/`.

Important API endpoints are documented in [docs/api.md](docs/api.md).

Product-level apply flow:

```bash
curl -X POST http://127.0.0.1:9876/v1/topologies/two-networks/revisions
PLAN_ID=$(curl -s -X POST http://127.0.0.1:9876/v1/topologies/two-networks/plans | jq -r .id)
curl -X POST http://127.0.0.1:9876/v1/topologies/two-networks/apply \
  -H 'Content-Type: application/json' \
  -d "{\"plan_id\":\"${PLAN_ID}\"}"
```

When `plan_id` is supplied, apply executes the stored plan actions instead of recomputing a new diff. The plan records the state serial it was created against; if state changed meanwhile, apply rejects it as stale. Runs keep the linked `revision` and `plan_id`, so `/v1/runs/{run_id}/events` remains explainable after an API restart.

Runs are scheduled onto agents by declared topology capabilities. The API only
creates and assigns command intent; the host agent executes topology changes and
keeps durable state/checkpoints locally unless configured otherwise. Local CLI
`apply`/`destroy` and API-assigned agent runs both execute through the same
executor; CLI uses a local bridge, while registered agents use the control-plane
bridge.

```bash
sysbox agent register --api http://127.0.0.1:9876 --id host-a
sysbox agent start
```

`DELETE /v1/topologies/{name}` removes workspace/state metadata only when the topology is empty. If state still contains resources, it returns `409`; call `POST /destroy` first. `force=true` is intentionally explicit for metadata-only deletion while leaving external resources behind.

## Runtime Layout

Generated local state is intentionally kept out of the project tree surface:

| Path | Purpose |
|---|---|
| `.sysbox/api` | API workspaces, fallback state, run records, checkpoints, health snapshots |
| `.sysbox/runs` | CLI/example/e2e state files and local event logs |
| `~/.cache/sysbox` | Kernels, rootfs images, qcow2 files, downloaded tools |

`.sysbox/`, old `data/`, and old `runs/` are ignored. New commands and docs use `.sysbox/` so runtime files do not spread across the repository root.

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

Postgres is the default backend in Docker Compose. Local CLI still defaults to local state files unless `--backend` or `SYSBOX_STATE_BACKEND` is used. When `SYSBOX_STATE_BACKEND` is a Postgres URL, the API also stores runs/checkpoints/health in Postgres tables. Leave `SYSBOX_STATE_BACKEND` empty in `.env` to let Compose choose the default API/agent Postgres URL.

## Product Objects

The API exposes product-level objects that map sysbox to Terraform Cloud /
CloudFormation-style control plane concepts:

| Object | Current sysbox representation |
|---|---|
| Project | `/v1/projects`, currently a default project namespace |
| Workspace / Topology | HCL workspace under `.sysbox/api/workspaces` plus state backend entry |
| Revision | SHA256-addressed HCL revision |
| Plan | Stored plan record for a workspace revision |
| Run | Async apply/destroy/recover operation with agent ownership |
| Agent | Host-local execution node registered through `/v1/agents`; Compose `deploy-full` starts a Docker-capable agent |
| Stack State | Current state plus backend metadata |
| Event / Action | Checkpoint/action-log steps exposed as run events |
| Artifact | Files in the sysbox artifact cache |
| Lease | State lock/lease metadata |
| Policy | Advisory policy object placeholder for pre-apply gates |
| Snapshot | State backend snapshot/restore point |

## Service Configuration

API deployments load service defaults from `sysbox.yaml` and use environment
variables only as deploy-time overrides. Docker Compose mounts
`deploy/docker/sysbox.yaml` at `/etc/sysbox/sysbox.yaml`; set `SYSBOX_CONFIG`
to point at another file.

```yaml
version: 1
api:
  listen: ":9876"
paths:
  home: /var/lib/sysbox
  cache: /var/cache/sysbox
supervisor:
  policy: observe_only
  interval: 30s
providers:
  default_policy:
    preflight: warn
  docker:
    enabled: true
  network:
    enabled: true
  firecracker:
    enabled: true
    binary: /opt/sysbox/bin/firecracker
    workdir: /var/lib/sysbox/firecracker
  libvirt:
    enabled: true
artifacts:
  policy:
    cache_mode: on_demand
    verify: warn
```

The Postgres DSN is assembled by Compose from `.env` and passed as
`SYSBOX_STATE_BACKEND`, so `sysbox.yaml` does not carry a password.

Recommended environment overrides:

| Variable | Meaning |
|---|---|
| `SYSBOX_CONFIG` | Service config file path, default `/etc/sysbox/sysbox.yaml` |
| `SYSBOX_API_HOST_PORT` | Host port published for the API, default `9876` |
| `SYSBOX_API_TOKEN` | Optional API Bearer token |
| `SYSBOX_HOST_HOME_DIR` | Host directory mounted to container `/var/lib/sysbox`, default `.sysbox/api` |
| `SYSBOX_HOST_CACHE_DIR` | Host directory mounted to container `/var/cache/sysbox`, default `~/.cache/sysbox` |
| `SYSBOX_HOST_DOCKER_SOCKET` | Host Docker socket path for `deploy-full`, default `/var/run/docker.sock` |
| `SYSBOX_POSTGRES_DATABASE` | Compose Postgres database name |
| `SYSBOX_POSTGRES_USERNAME` | Compose Postgres username |
| `SYSBOX_POSTGRES_PASSWORD` | Compose Postgres password; set in local `.env`, do not commit real values |
| `SYSBOX_POSTGRES_HOST_PORT` | Host port published for Postgres, default `55432` |
| `SYSBOX_STATE_BACKEND` | Optional external state/API backend URL; overrides Compose-generated DSN |

The container paths `/var/lib/sysbox` and `/var/cache/sysbox` are fixed by the
sysbox image and service config. `.env` only chooses the host directories that
back those paths.

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
deploy/docker/              Docker Compose base file and capability overlays
docs/                       Current docs
examples/                   Example topologies
pkg/artifact/               Artifact resolver/cache
pkg/api/                    HTTP control plane, scheduling, jobs, recovery/cleanup
pkg/config/                 HCL schema and eval
pkg/controlplane/           Product-level objects such as Project, Plan, Run, Agent
pkg/graph/                  Dependency graph
pkg/provider/               Docker, Firecracker, network, libvirt providers
pkg/runtime/                Plan/apply/destroy/checkpoint runtime and execution journal primitives
pkg/state/                  Local/Postgres/HTTP/S3/SQLite state backends
pkg/substrate/              Provider abstraction
pkg/agent/                  Agent identity and registration
pkg/agentexec/              Agent loop, local bridge, and apply/destroy execution
runner/                     Optional Python episode runner for agent examples
scripts/                    Artifact preparation and verification helpers
tests/e2e/                  Integration tests with build tag e2e
.sysbox/                    Ignored local runtime data
```
