# sysbox Deployment

sysbox keeps deployment small and explicit:

- `sysbox.yaml` describes API/agent service defaults.
- `.env` describes deploy-time host wiring.
- HCL describes topology intent.
- Docker Compose starts the control plane and, optionally, one local Docker
  agent.

## Targets

```bash
cp .env.example .env
$EDITOR .env        # change SYSBOX_POSTGRES_PASSWORD outside disposable labs

make api build-api    # rebuild API/agent image only
make api deploy       # API + Postgres
make api deploy-full  # API + Postgres + Docker agent
make api seed         # copy example HCL workspaces
make api build-ui     # build and start Web UI for the running API
make api down
make api clean        # stop compose, remove Postgres volume, clear API workspaces
make api logs
```

`make api deploy` starts only the control plane. It does not mount the host Docker
socket into the API container.

`make api deploy-full` first registers an agent identity, then starts a
`sysbox-agent` container. The agent mounts the host Docker socket and executes
Docker-substrate runs assigned by the API.

`make api build-ui` builds and starts the browser console on
`http://${SYSBOX_WEB_HOST_ADDR:-0.0.0.0}:${SYSBOX_WEB_HOST_PORT:-3001}`. The UI
uses the same-origin `/v1` proxy for HTTP and WebSocket console traffic.

API and Web are published on `0.0.0.0` by default so another machine can reach
them through the host IP. Postgres is bound to `127.0.0.1` by default.

If you change `SYSBOX_POSTGRES_PASSWORD` after Postgres has already initialized,
the existing Docker volume keeps the old database password. For local disposable
labs, run `make api clean` and deploy again.

`make api deploy` does not seed example HCL workspaces. Run `make api seed`
explicitly when you want the bundled examples in the API workspace list.

## Service Config

The containers read `/etc/sysbox/sysbox.yaml` by default. Docker Compose mounts
`deploy/docker/sysbox.yaml` there. Use `SYSBOX_CONFIG` only when pointing at a
different mounted config path.

Environment variables are for deployment wiring, not product intent. Keep
project/workspace intent in HCL and control-plane objects; keep provider
defaults, state backend, paths, lease policy, and supervisor defaults in
`sysbox.yaml`.

## Local Runtime Layout

By default, generated local state lives under `.sysbox/`:

- `.sysbox/api`: API workspaces, agent identity, fallback state, run metadata,
  checkpoints.
- `.sysbox/runs`: CLI/example state and e2e state files.

Compose exposes only host-side path variables. `SYSBOX_HOST_HOME_DIR` is mounted
to container `/var/lib/sysbox`, and `SYSBOX_HOST_CACHE_DIR` is mounted to
container `/var/cache/sysbox`.

## State Backend

Leave `SYSBOX_STATE_BACKEND` empty for the normal Compose config. Compose
assembles a Postgres DSN for `sysbox-postgres:5432` and passes it to both API
and agent.

Set `SYSBOX_STATE_BACKEND` only when using an external backend. Keep real
credentials in local `.env` or your deployment secret manager, not in
`deploy/docker/sysbox.yaml`.

`docker compose config` expands environment variables, so its output can include
the effective Postgres DSN. Treat that output as sensitive when it contains real
credentials.

## Smoke Test

```bash
make api deploy-full
make api seed
curl http://127.0.0.1:9876/v1/agents
curl -X POST http://127.0.0.1:9876/v1/topologies/docker-service/apply
curl http://127.0.0.1:9876/v1/runs
make test-e2e
```

`examples/docker-service` is intentionally Docker-only so it can run through the
containerized agent with just the Docker socket mounted.

## Firecracker Artifacts

Sysbox does not embed kernels or rootfs images. Declare them as
`sysbox_kernel` and `sysbox_image` resources, then mount them explicitly or let
the artifact cache fetch the declared source.

Use an uncompressed `vmlinux` with virtio and vsock support. The repository
script prepares an ext4 rootfs from the pinned Ubuntu squashfs input:

```bash
scripts/prepare-fc-rootfs.sh
```

The script is idempotent and stores generated output below the Sysbox cache.
The guest needs a standard init or shell as `chain_init`; `sysbox-init` injects
hostname, SSH keys, environment and the vsock agent at boot through the config
drive.

For libvirt, use an immutable qcow2 baseline. Sysbox creates a generation-owned
overlay and validates its ownership before cleanup. Never point two writable
nodes directly at the same baseline file.

Verify artifacts with the `examples/microvm` and
`examples/heterogeneous-matrix` workflows before production use.
