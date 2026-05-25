# sysbox Deployment

sysbox uses a layered deployment model:

- `sysbox.yaml` describes API service defaults.
- `.env` describes deploy-time overrides for this host.
- HCL describes topology intent.
- Docker Compose files describe capability layers.
- `make api-up` is the stable operational entrypoint.

## Profiles

Set `SYSBOX_DEPLOYMENT` in `.env`:

| Profile | Compose files | Purpose |
|---|---|---|
| `docker` | `deploy/docker/compose.yml` | Docker-only topologies and API/control-plane work |
| `vm` | base + `deploy/docker/compose.vm.yml` | network + Firecracker/VM labs |
| `full` | vm + `deploy/docker/compose.full.yml` | all local substrates including libvirt |

The `docker` profile avoids host networking and host PID namespace. `vm` opts
into local lab capabilities such as host netns, tap devices, and KVM. `full`
adds libvirt socket access.

## Workflow

```bash
cp .env.example .env
$EDITOR .env
make api-config
make api-up
```

Change `SYSBOX_POSTGRES_PASSWORD` before starting the stack.

`make api-config` prints the resolved Compose config, which is the best quick
check before starting a privileged profile.

## Service Config

The API reads `/etc/sysbox/sysbox.yaml` by default. Docker Compose mounts
`deploy/docker/sysbox.yaml` there. Use `SYSBOX_CONFIG` or `sysbox serve --config`
to point at another file.

Environment variables remain useful for 12-factor deployment wiring, but should
not become the product model. Keep project/workspace intent in HCL and control
plane objects; keep provider defaults, state backend, paths, and supervisor
defaults in `sysbox.yaml`.

## Local Runtime Layout

By default, generated local state lives under `.sysbox/`:

- `.sysbox/api`: API workspaces, fallback state, run metadata, checkpoints.
- `.sysbox/runs`: CLI/example state and e2e state files.

Compose exposes only host-side path variables. `SYSBOX_HOST_HOME_DIR` is mounted
to container `/var/lib/sysbox`, and `SYSBOX_HOST_CACHE_DIR` is mounted to
container `/var/cache/sysbox`.

The legacy `data/` and `runs/` directories are ignored, but new docs and
Makefile targets use `.sysbox/` so runtime files do not spread across the
repository root.

## State Backend

Leave `SYSBOX_STATE_BACKEND` empty for the normal Compose config:

- `docker`: `compose.yml` assembles a DSN for `sysbox-postgres:5432`
- `vm` / `full`: `compose.vm.yml` overrides it to `postgres://...@127.0.0.1:${SYSBOX_POSTGRES_HOST_PORT}/...`

Set `SYSBOX_STATE_BACKEND` only when using an external backend. Keep real
credentials in local `.env` or your deployment secret manager, not in
`deploy/docker/sysbox.yaml`.

`docker compose config` expands environment variables, so its output can include
the effective Postgres DSN. Treat that output as sensitive when it contains real
credentials.

## Artifacts

Large artifacts should not be baked into the API image. Prefer:

- host-mounted cache via `SYSBOX_HOST_CACHE_DIR`
- HCL `sysbox_kernel` / `sysbox_image` artifact declarations
- explicit tools directory mount via `SYSBOX_PROVIDER_FIRECRACKER_TOOLS_DIR`

This keeps images small while still allowing reproducible, pinned artifacts.
