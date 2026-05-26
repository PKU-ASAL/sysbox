# sysbox API

The sysbox API exposes product-level control plane objects over `/v1`.

```bash
GET /v1/schema
```

## Projects

```bash
GET /v1/projects
GET /v1/projects/default
GET /v1/projects/default/workspaces
```

## Agents

Agents are host-local execution nodes. Agent identity, protocol version,
capabilities, disabled/quarantined state, and heartbeat projection are
persisted in the API store. Durable topology state, checkpoints, and artifact
metadata remain on the agent host unless a shared backend is explicitly
configured.

```bash
GET  /v1/agents
POST /v1/agents
GET  /v1/agents/{agent_id}
POST /v1/agents/{agent_id}/heartbeat
GET  /v1/agents/{agent_id}/commands
GET  /v1/agents/{agent_id}/commands/stream
POST /v1/agents/{agent_id}/commands/{command_id}/cancel
GET  /v1/agents/{agent_id}/command-events
GET  /v1/agents/{agent_id}/projections
GET  /v1/agents/{agent_id}/inventory
POST /v1/agents/{agent_id}/inventory
POST /v1/agents/{agent_id}/runs/{run_id}/claim
POST /v1/agents/{agent_id}/runs/{run_id}/renew
POST /v1/agents/{agent_id}/runs/{run_id}/complete
```

Example registration:

```bash
curl -X POST http://127.0.0.1:9876/v1/agents \
  -H 'Content-Type: application/json' \
  -d '{"id":"host-a","capabilities":["docker","network","kvm"],"labels":{"role":"lab"}}'
```

## Topologies And Workspaces

```bash
GET    /v1/topologies
POST   /v1/topologies
GET    /v1/topologies/{name}
DELETE /v1/topologies/{name}

GET /v1/topologies/{name}/hcl
PUT /v1/topologies/{name}/hcl

GET  /v1/topologies/{name}/plan
POST /v1/topologies/{name}/plans
GET  /v1/topologies/{name}/plans
GET  /v1/topologies/{name}/plans/{plan_id}

POST /v1/topologies/{name}/revisions
GET  /v1/topologies/{name}/revisions
GET  /v1/topologies/{name}/revisions/{revision_id}

POST /v1/topologies/{name}/apply
POST /v1/topologies/{name}/destroy
```

When `POST /apply` receives `{"plan_id":"..."}`, the API executes the stored
plan actions and rejects stale plans whose recorded state serial no longer
matches current state.

## State And Observability

```bash
GET  /v1/topologies/{name}/state
GET  /v1/topologies/{name}/state/metadata
GET  /v1/topologies/{name}/state/lock
POST /v1/topologies/{name}/state/force-unlock
GET  /v1/topologies/{name}/state/snapshots
POST /v1/topologies/{name}/state/snapshots/{snapshot}/restore

GET /v1/topologies/{name}/outputs
GET /v1/topologies/{name}/preflight
GET /v1/topologies/{name}/stack-state
GET /v1/topologies/{name}/lease
GET /v1/topologies/{name}/snapshots
GET /v1/topologies/{name}/health
GET /v1/topologies/{name}/resources
GET /v1/topologies/{name}/status/stream
GET /v1/topologies/{name}/resources/{resource}/health
```

## Runs

```bash
GET  /v1/runs
GET  /v1/runs/{run_id}
GET  /v1/runs/{run_id}/logs
GET  /v1/runs/{run_id}/checkpoint
GET  /v1/runs/{run_id}/actions
GET  /v1/runs/{run_id}/events
POST /v1/runs/{run_id}/resume
POST /v1/runs/{run_id}/recover
POST /v1/runs/{run_id}/cleanup
```

Run records include the owning `agent_id`, protocol version, lease owner, lease
expiry, and attempt count. The API creates and assigns command intent; agents
keep an outbound WebSocket command stream open at
`/v1/agents/{agent_id}/commands/stream`. Commands use a durable envelope with
`id`, `type`, `protocol`, and command-specific payload. Agents ACK, start,
complete, and fail commands on the same WebSocket; command events are persisted
and available from `/v1/agents/{agent_id}/command-events`. Commands are
persisted before delivery, leased with an atomic store operation before
WebSocket delivery, replayed to reconnecting agents after lease expiry, and can
be cancelled with `cancel_command`. Postgres stores command/run lease owner,
lease expiry, and attempt count so multiple API replicas do not deliver or
claim the same work concurrently. The legacy SSE command stream has been
removed. When a run is assigned, the API pushes a `run_assigned` command; the
agent then claims the run through the same lease/attempt model and executes it
on the host. While a run is active, the agent renews its run lease through
`/v1/agents/{agent_id}/runs/{run_id}/renew`; expired running leases are marked
failed/recoverable by the supervisor. On completion, the agent posts the final
run status and a state projection to
`/v1/agents/{agent_id}/runs/{run_id}/complete`.

```text
queued -> assigned -> running -> done|failed|cancelled
```

Agents also enforce a local host policy before executing commands. Configure
`agent.policy.allowed_workspaces`, `allowed_substrates`, `allowed_commands`,
`allow_console`, and `allow_import` in `sysbox.yaml` to keep host-local
capabilities scoped even if the control plane is misconfigured.

Registered agents are persisted in the API store. They have a per-agent secret,
`secret_hash`, protocol version, capabilities, heartbeat, and operational
status. API responses expose only `secret_hash`; agent-originated heartbeat,
inventory, projection, run completion, console attach, and command stream
requests are signed with `X-Sysbox-Agent-*` headers.
`PATCH /v1/agents/{agent_id}` persists disabled/quarantined state; disabled or
quarantined agents are skipped by scheduling and cannot open the command stream.
Agents whose heartbeat becomes stale are marked `offline` by the supervisor and
will not be selected until a fresh heartbeat restores them to `online`.

Agents periodically sync inventory to `/v1/agents/{agent_id}/inventory`,
including local topologies, serials, resource counts, health, labels, and
capabilities. The API treats this as a projection of agent-local truth.

## Console Sessions

Node exec is agent-backed. The API creates the session intent and relays
WebSocket frames; the owning agent opens the substrate console locally.

```bash
POST /v1/topologies/{name}/nodes/{node}/sessions
GET  /v1/sessions/{session_id}
GET  /v1/sessions/{session_id}/attach
POST /v1/sessions/{session_id}/cancel
GET  /v1/agents/{agent_id}/sessions/{session_id}/attach
POST /v1/agents/{agent_id}/projections/resources
POST /v1/agents/{agent_id}/node-operations/{operation_id}/complete
```

Browsers attach to `/v1/sessions/{session_id}/attach`. Agents attach to the
agent-side URL after receiving a `session_open` command on their command
stream.
Session metadata is persisted by the API store; interrupted sessions are marked
`lost` on API restart. `timeout_seconds` on session creation auto-cancels long
running sessions. Console requests accept `requested_by`; if absent, the API
uses the configured user header (`X-Sysbox-User` by default) or `api`. Roles
come from `X-Sysbox-Roles` by default. Console RBAC is configured in
`api.console.allowed_roles` plus `api.rbac.admin_roles`; when no console roles
are configured, local development remains permissive. Session records include
the policy name and audit events for create, allow/deny, attach, cancel,
timeout, and close.

Agents also post resource-level observation projections to
`/v1/agents/{agent_id}/projections/resources`. UI clients can subscribe to
`/v1/topologies/{name}/status/stream` for live topology health updates.

WebSocket text frames use a small JSON envelope. Binary payloads are base64
encoded so the same envelope works across browser terminals and simple tools.

```json
{"type":"stdin","data":"bHMK"}
{"type":"resize","cols":120,"rows":40}
{"type":"stdout","data":"..."}
{"type":"stderr","data":"..."}
{"type":"exit","code":0}
```

## Nodes

```bash
GET  /v1/topologies/{name}/nodes
GET  /v1/topologies/{name}/nodes/{node}
POST /v1/topologies/{name}/nodes/{node}/pause
POST /v1/topologies/{name}/nodes/{node}/resume
POST /v1/topologies/{name}/import
GET  /v1/node-operations/{operation_id}
```

Pause, resume, and import are agent-backed node operations. The API creates an
operation intent, assigns it to a capable agent, and the agent completes the
local substrate call before posting the final operation status back to the API.
Operation records persist requester, roles, status, and audit events so API/UI
users can explain who requested a host-local action and how it completed.

## Artifacts And Policies

```bash
GET  /v1/artifacts
GET  /v1/policies
POST /v1/policies
```
