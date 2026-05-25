# sysbox API

The sysbox API exposes product-level control plane objects over `/v1`.

## Projects

```bash
GET /v1/projects
GET /v1/projects/default
GET /v1/projects/default/workspaces
```

## Agents

Agents are host-local execution nodes. The API keeps only an online projection
from registration/heartbeat data; durable topology state, checkpoints, and
artifact metadata remain on the agent host unless a shared backend is
explicitly configured.

```bash
GET  /v1/agents
POST /v1/agents
GET  /v1/agents/{agent_id}
POST /v1/agents/{agent_id}/heartbeat
GET  /v1/agents/{agent_id}/stream
GET  /v1/agents/{agent_id}/projections
GET  /v1/agents/{agent_id}/runs
POST /v1/agents/{agent_id}/runs/{run_id}/claim
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

Run records include the owning `agent_id`. The API creates and assigns command
intent; agents keep an outbound SSE command stream open at
`/v1/agents/{agent_id}/stream`. When a run is assigned, the API pushes a
`run_assigned` command; the agent then claims the run and executes it on the
host. `/runs` remains as reconnect/backfill support. On completion, the agent
posts the final run status and a state projection to
`/v1/agents/{agent_id}/runs/{run_id}/complete`.

```text
queued -> assigned -> running -> done|failed|cancelled
```

## Console Sessions

Node exec is agent-backed. The API creates the session intent and relays
WebSocket frames; the owning agent opens the substrate console locally.

```bash
POST /v1/topologies/{name}/nodes/{node}/sessions
POST /v1/topologies/{name}/nodes/{node}/exec
GET  /v1/sessions/{session_id}
GET  /v1/sessions/{session_id}/attach
GET  /v1/agents/{agent_id}/sessions/{session_id}/attach
```

`/exec` is a compatibility entry point that now returns a session object.
Browsers attach to `/v1/sessions/{session_id}/attach`. Agents attach to the
agent-side URL after receiving a `session_open` command on their SSE stream.

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
POST /v1/topologies/{name}/nodes/{node}/exec
POST /v1/topologies/{name}/nodes/{node}/pause
POST /v1/topologies/{name}/nodes/{node}/resume
```

## Artifacts And Policies

```bash
GET  /v1/artifacts
GET  /v1/policies
POST /v1/policies
```
