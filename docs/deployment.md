# sysbox Deployment

sysbox uses a 12-factor style deployment model:

- `.env` describes deploy-time configuration for this host.
- HCL describes topology intent.
- Docker Compose files describe capability layers.
- `make api-up` is the stable operational entrypoint.

## Profiles

Set `SYSBOX_DEPLOYMENT` in `.env`:

| Profile | Compose files | Purpose |
|---|---|---|
| `service` | `deploy/docker/compose.yml` | API + Postgres + Docker socket |
| `netns` | base + `deploy/docker/compose.netns.yml` | host netns/veth/tap labs |
| `firecracker` | netns + `deploy/docker/compose.firecracker.yml` | Firecracker microVM labs |
| `libvirt` | netns + `deploy/docker/compose.libvirt.yml` | libvirt/QEMU VM labs |
| `full` | netns + Firecracker + libvirt | mixed virtualization development |

The base profile avoids host networking and host PID namespace. Profiles that
touch Linux netns, tap devices, KVM, or libvirt opt into those host privileges
explicitly.

## Workflow

```bash
cp .env.example .env
$EDITOR .env
make api-config
make api-up
```

`make api-config` prints the resolved Compose config, which is the best quick
check before starting a privileged profile.

## Local Runtime Layout

By default, generated local state lives under `.sysbox/`:

- `.sysbox/api`: API workspaces, fallback state, run metadata, checkpoints.
- `.sysbox/runs`: CLI/example state and e2e state files.

The legacy `data/` and `runs/` directories are ignored, but new docs and
Makefile targets use `.sysbox/` so runtime files do not spread across the
repository root.

## State Backend

Leave `SYSBOX_STATE_BACKEND` empty for the normal Compose defaults:

- `service`: `postgres://...@sysbox-postgres:5432/...`
- host-network profiles: `postgres://...@127.0.0.1:${SYSBOX_POSTGRES_PORT}/...`

Set `SYSBOX_STATE_BACKEND` only when using an external backend.

## Artifacts

Large artifacts should not be baked into the API image. Prefer:

- host-mounted cache via `SYSBOX_CACHE_DIR`
- HCL `sysbox_kernel` / `sysbox_image` artifact declarations
- explicit Firecracker binary mount via `SYSBOX_FIRECRACKER_BIN`

This keeps images small while still allowing reproducible, pinned artifacts.
