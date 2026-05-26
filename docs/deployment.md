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

make deploy       # API + Postgres
make deploy-full  # API + Postgres + Docker agent
make deploy-ui    # Web UI for the running API
make undeploy
make reset        # stop compose and remove local Postgres volume
make logs
```

`make deploy` starts only the control plane. It does not mount the host Docker
socket into the API container.

`make deploy-full` first registers an agent identity, then starts a
`sysbox-agent` container. The agent mounts the host Docker socket and executes
Docker-substrate runs assigned by the API.

`make deploy-ui` starts the browser console on
`http://${SYSBOX_WEB_HOST_ADDR:-0.0.0.0}:${SYSBOX_WEB_HOST_PORT:-3000}`. The UI
uses the same-origin `/v1` proxy for HTTP and WebSocket console traffic.

API and Web are published on `0.0.0.0` by default so another machine can reach
them through the host IP. Postgres is bound to `127.0.0.1` by default.

If you change `SYSBOX_POSTGRES_PASSWORD` after Postgres has already initialized,
the existing Docker volume keeps the old database password. For local disposable
labs, run `make reset` and deploy again.

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
make deploy-full
curl http://127.0.0.1:9876/v1/agents
curl -X POST http://127.0.0.1:9876/v1/topologies/docker-service/apply
curl http://127.0.0.1:9876/v1/runs
make test-e2e
```

`examples/docker-service` is intentionally Docker-only so it can run through the
containerized agent with just the Docker socket mounted.
