# sysbox API

The sysbox API exposes product-level control plane objects over `/v1`.

## Projects

```bash
GET /v1/projects
GET /v1/projects/default
GET /v1/projects/default/workspaces
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
