# sysbox API

> 重构分支的 recover/cleanup 以固定 revision 和 canonical action 为准，不再用 checkpoint patch 授权恢复。远端 run 必须在 owning agent 上观测；尚未实现派发的入口明确拒绝本机恢复。HTTP v1 的 `error` 仍是字符串。参见[实施状态](../design/2026-09-28-runtime-refactor-implementation-status.md)。

Plan action 使用 canonical `address`。Stored plan 带完整输入指纹；配置、
state、schema、driver、artifact 或变量变化时，会在执行前拒绝。配置验证
错误返回 HTTP 422 和结构化 `diagnostics` 数组。参见
[`architecture.md`](../architecture.md)。

State v4 的 observation status 为 `present/absent/drifted/degraded/unknown`。
API 不返回 driver-private payload 或已解析秘密值。

The sysbox API exposes product-level control plane objects over `/v1`.

Run completion validates the existing lease_owner and attempt fields against the currently claimed execution. Unclaimed or stale-round completion is rejected; a failed persistence write cannot publish a successful run/projection. Renewal never shortens a live lease and cannot revive an expired one. An API restart alone does not mark independently assigned agents failed.

Run claim requests must include `queued_at` and `revision` from the dispatched Run snapshot. Empty claim bodies return 400; a snapshot from a previous execution returns 409. Command IDs distinguish execution incarnations even when a Run ID is reused. Upgrade API and Agent together.

After claim, the Agent posts its claimed Run to `POST /v1/agents/{agent}/runs/{id}/revision`. Agent authentication, ownership, the complete Run incarnation, attempt and live lease must match. The response is the immutable `GlobalRevision` tree; the Agent verifies its digest and materializes it in its own runs directory before execution. `SnapshotPath` remains a canonical API record binding, never an Agent filesystem location. Recovery uses the same materializer. Destroy publishes its captured configuration as a revision too; state-only Destroy uses an explicit empty configuration tree.

Remote binary artifacts must use files relative to that revision tree or HTTP(S) URLs with an explicit SHA-256. Absolute host paths, traversal and symlinks are rejected. URL content is verified in the Agent cache; bundled content is covered by the revision digest. OCI references retain their provider resolution contract. Standalone CLI operations may still select explicit local paths.

Apply accepts `Idempotency-Key` (1–128 ASCII letters, digits, colon, underscore or hyphen) with an immutable global `revision`. The key is scoped to topology and operation. Replaying the same request returns the original Run, including terminal results; changing revision, inputs, explicit agent, unsafe flag or deadline returns 409. A new execution needs a new key. Competing active Runs for one topology are rejected in the store transaction on all three backends.

Keyed Apply pins its immutable snapshot without rewriting the mutable workspace. Preflight and execution use that snapshot. Inputs stay in memory: after restart, `GET /v1/runs/{id}` reports `inputs_required: true` until the original request is resubmitted to the Agent-connected API. Claim returns 503 before taking a Run lease; Agent retries 503 with the original queued_at/revision. Terminal replay does not restore secrets. Durable Run query errors return 503, authoritative absence404, stale claim/completion409. Nonempty revision labels that are not published global revisions now return400.

Authenticated `GET /v1/capabilities` includes protocol `sysbox-runtime-v1`, `durable_apply`, `keyed_destroy`, `cleanup_inspection`, `single_api_execution`, `external_absence_inspection` and `resource_lease_revocation`. The last two are false: no implied live absence proof or resource-side lease revocation.

`GET /v1/topologies/{name}/cleanup-inspection` returns redacted `resources`, `unfinished_actions`, `active_operations`, `state_exists`, `metadata_exists`, source/time and scope. State+journal are one snapshot; operation lists are separate snapshots and cover only the reported `active_operation_kinds` (Run and NodeOperation). Remote ownership reports `owning_agent_not_observed`; `external_presence` is `unknown`. Empty lists never authorize cleanup. Storage errors return503. Use owning-agent Observe/Recover for actual provider evidence; metadata deletion still checks repository serial/owner and unfinished actions.

Workspace responses can include `active_run_id`, `active_revision` and `placement_agent_id`. A remotely placed topology remains listed after its Run ends. If the API has neither local resource evidence nor a mutable HCL pointer, phase is `unknown` with `OwningAgentNotObserved`, not `gone`. Deleting placement prevents retained terminal Run history from recreating the catalog entry.

An owning Agent may include a `cleanup` receipt with a successful whole-topology Destroy completion. It binds version1, topology/UID, AgentID, RunID/queued_at/revision, attempt/lease_owner, nonzero state_serial, observed_at and empty=true. The Agent proves empty managed state and no unfinished action from one repository snapshot; fresh/missing state cannot produce a receipt. Completion validates and persists the receipt atomically with the current lease; terminal retries must carry the identical receipt. Run queries return the stored `cleanup` field.

Firecracker guest transport separates `ping` liveness from `ready.v1` initialization. `ready.v1` is required before exec or file-write effects: systemd guests wait for both `sysinit.target` and successful `systemd-tmpfiles-setup.service`; custom init guests must write `/run/sysbox/boot-ready` containing the current boot ID after their own initialization. Console remains available for diagnostics while readiness is incomplete.

Ordinary metadata DELETE accepts only the latest successful Destroy receipt matching current placement and no subsequent lifecycle operation. New Runs, later/uncertain NodeOperations or placement rebinding invalidate it; missing proof returns409. Cleanup inspection reports `cleanup_receipt`, `cleanup_receipt_status` and its source as recorded evidence, not a live global absence query. Explicit `force=true` removes metadata only, never remote resources; active Run/NodeOperation or unfinished local journal blocks deletion in both modes.

Node-operation completion requires `action_id`, `resource_uid`, `generation`, `lease_epoch`, `lease_owner`, and `attempt` from the delivered command. The store checks the current command lease and commits the operation result in one lock/transaction. Expired or replaced leases return 409; identical committed terminal reports remain idempotent.

Guest execution/file metadata now share one persistence contract. Guest endpoints distinguish missing records (404), conflicts (409), and corrupt/unavailable storage (500), with the existing string `error` envelope. Request, agent and command bindings remain private. The new on-disk `record_format` is not an HTTP field; old metadata requires an explicit rebuild as described in the [implementation status](../design/2026-09-28-runtime-refactor-implementation-status.md).

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
  -d '{"id":"host-a","capabilities":["docker","veth","network"],"labels":{"role":"lab"}}'
```

## Topologies And Workspaces

Topologies are created implicitly by applying a published revision: the API
materializes the revision's HCL into the workspace (upsert) and dispatches a
run.

```bash
POST /v1/revisions

GET    /v1/topologies
GET    /v1/topologies/{name}
GET    /v1/topologies/{name}/hcl
DELETE /v1/topologies/{name}

GET  /v1/topologies/{name}/plan

POST /v1/topologies/{name}/apply
POST /v1/topologies/{name}/reset
POST /v1/topologies/{name}/destroy
```

`POST /v1/revisions` accepts raw HCL as the request body and returns a
content-addressed digest: `{"revision":"sha256:<hex>"}`. Publishing the same
HCL returns the same digest.

`POST /v1/topologies/{name}/apply` upserts the workspace with the revision's
HCL and dispatches a run. The request body is:

```json
{"revision":"sha256:<hex>","inputs":{},"deadline_at":"2026-09-06T00:00:00Z"}
```

`inputs` are materialized per-run and never returned by the API. `deadline_at`
bounds convergence. `GET /v1/topologies/{name}` returns workspace metadata and
`status` with `phase`, `conditions` (`Applied`, `Provisioned`, `Asserted`,
`Ready`), `nodes`, `outputs`, and `deadline_at`.

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

The lifecycle aggregate currently does not implement snapshots/restore or lease inspection/force-unlock. These management endpoints return501 rather than an empty successful result.

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

## Guest Operations

Guest operations address only a topology and logical node. Provider handles,
container IDs, VM UUIDs, host paths, credentials and Agent identity are never
part of the public response.

```text
POST /v1/topologies/{topology}/nodes/{node}/executions
GET  /v1/executions/{execution_id}
POST /v1/executions/{execution_id}/cancel
PUT  /v1/topologies/{topology}/nodes/{node}/files?path=/absolute/path&mode=0400
GET  /v1/file-operations/{operation_id}
POST /v1/file-operations/{operation_id}/cancel
```

Execution creation accepts `argv`, optional `environment`,
`working_directory`, and `timeout_seconds`, then returns `202 Accepted` with a
durable `queued` resource. The owning Agent reports `running`, followed by
`completed`, `failed`, or `cancelled`. A non-zero guest exit is `completed`
with `result_class: exit`; stdout and stderr are base64 encoded and bounded.
The private request and output payload expire 15 minutes after completion,
while the sanitized lifecycle metadata remains available.

Cancellation is idempotent at the execution lifecycle boundary. A queued
command is made non-leasable before delivery; a running operation receives a
real Agent cancellation. A late completion cannot replace the cancelled state.

File upload uses the request body as raw binary bytes, `X-Content-SHA256` for
integrity, and query parameters for the absolute guest `path` and octal `mode`.
The limit is 16 MiB. Paths containing NUL, traversal, or a non-absolute path
are rejected. The API response contains a durable operation with `queued`,
`running`, `completed`, `failed`, or `cancelled` status. The operation can be
read and cancelled through `/v1/file-operations/{operation_id}`. Agent and
provider identifiers, host paths, credentials, and private payload references
are never returned. Payloads are staged with restricted permissions, bound to
the owning Agent, and retained for retry until the operation becomes terminal
or the 15-minute staging lifetime expires. Providers install content and mode
atomically where supported.

These endpoints require the dedicated guest-operation authorization check;
the owning Agent uses signed internal start, completion, and payload-fetch
endpoints. Local Agent policy must explicitly allow `guest_execution` and/or
`guest_file_put`. Docker and Firecracker expose execution and file capability;
libvirt remains capability-gated.

## Artifacts And Policies

```bash
GET  /v1/artifacts
GET  /v1/policies
POST /v1/policies
```
