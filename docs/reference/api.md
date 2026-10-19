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

Canonical aggregates support snapshots and the explicit restore-plan/restore workflow.
`GET .../state/lock` reports execution-authority grants and maintenance barriers,
including revoked or expired executions without confirmed host stops. It does not
claim to inspect transient database mutexes. `POST .../state/force-unlock` with an
`Idempotency-Key` persistently revokes authority and returns 202 `waiting_for_stop`
until activity and host-stop evidence allow release; retry the same key after
restart. 200 `unlocked` includes the original durable barrier/audit identity and
does not remove flock files. Calls without a key retain the conservative legacy
behavior; canonical backend raw force-unlock is unsupported.

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
running sessions. The API uses its request subject for identity: the configured
user header (`X-Sysbox-User` by default) or `api`, and the configured roles header
(`X-Sysbox-Roles` by default). Body-supplied `requested_by` and `roles` cannot
override that identity. Console RBAC is configured in
`api.console.allowed_roles` plus `api.rbac.admin_roles`; when no console roles
are configured, local development remains permissive. Session records include
the policy name and audit events for create, allow/deny, attach, cancel,
timeout, and close. Creation, metadata reads, cancellation and browser attachment
all check the current Console role policy; denied existing-session requests
return 403 before data disclosure, state changes or WebSocket upgrade. Registry
failure returns a redacted 503; a missing session returns 404. Cancellation audit
records the acting subject's roles, not the creator's roles. Agent attachment
uses its separate agent identity and session assignment checks.

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
durable `queued` resource. The owning Agent reports `running`; the host's signed
receipt establishes `completed`, `failed`, or `cancelled`. A non-zero guest exit is `completed`
with `result_class: exit`; stdout and stderr are base64 encoded and bounded.
The private request and output payload expire 15 minutes after completion,
while the sanitized lifecycle metadata remains available.

Execution cancellation persists `cancel_requested: true` and keeps the current
queued/running status until an actual outcome is reported. Repeated requests
reuse one cancellation command; restart reconciliation repairs missing delivery
without resetting a live lease. A real completion may win the cancellation race
and retain its result. Cancellation blocks subsequent execution-grant issuance,
authorization and renewal, but does not itself prove remote termination.
Cancellation intent and its outbox commit together. SQLite/Postgres also revoke
issued grants in that transaction. File storage commits the intent and outbox
seed in one record, then publishes revocation; restart replay finishes that
publication even after task completion or delivery retirement. Notification
failure does not turn durable acceptance into an API failure. Host-signed
execution completion now carries the bounded result with the quiescence proof;
the API can finish an execution after worker lease expiry or restart. Missing
exit status is recorded as unknown rather than success.

The worker `/v1/agents/{agent}/guest-executions/{execution}/complete` route has
been removed. Host receipts use `/v1/hosts/{agent}/execution-grants/stopped`
and require `guest_result` with an explicit `exit_code_known` flag. Completion
is committed before releasing authority; exact replay survives output expiry
and successor epochs. Run-derived tasks release their child grant without
creating a separate API execution. Receipt requests are limited to 4 MiB;
stdout/stderr are base64 encoded and bounded to 1 MiB each before signing.

For a cancelled execution that never received execution authority, recovery
retires the original delivery, verifies the original admission and the complete
grant/stop history under the authority lock, then records `cancelled` with
`result_class: not_started`. This also works when an Agent already reported
`running`: that report alone does not prove remote execution. Any historical
grant requires host evidence instead. Interrupted cleanup resumes from the
durable cancellation intent without changing an established terminal result.

Supervisor recovery also handles failed, denied or cancelled deliveries when
the user did not request cancellation. It retires the delivery and verifies
the original admission plus complete grant/stop history in the same authority
transaction. Never-authorized tasks become `failed / not_started` without
setting `cancel_requested`. Previously authorized tasks retain their execution
state; their remaining grants are revoked and host evidence supplies the result.

`POST /v1/agents/{agent}/guest-task-stops` lets the owning host discover revoked
standalone and Run-derived guest tasks, using signed Agent authentication. A disabled Agent may
still drain effects; an identity without stored credentials is rejected. The
JSON cursor contains optional `after_epoch` and `through_epoch`. Start with `{}`;
continue with the returned `next_epoch` as `after_epoch` and keep the returned
`through_epoch` until `next_epoch` is zero. Each page has at most 64 entries
across `stops` and `issues`. Each stop carries topology/node names and a signed,
stop-only cancellation proof. Issues identify unavailable source records; they
do not prevent pagination. This route issues no execution authority.

A reboot stop receipt additionally contains `observed_guest_boot_id`, signed
alongside the original `guest_boot_id`. The host must first verify canonical
resource identity. A changed boot proves old-process termination only, so its
guest result is `unknown` without output or a known exit code. Terminal standalone
executions still rely on exact host-receipt replay to finish an interrupted
authority release.

For a Run child, the cancellation proof also signs `run_id` and `topology`.
The API verifies the parent grant and original Run command digest before
signing. The response leaves `node` empty: the host derives it from trusted
canonical state or an unfinished candidate belonging to the signed Run. A bad
parent source yields an issue, not cancellation authority. Parent revocation
atomically revokes unfinished children; stopped children remain stopped.
Acknowledging a child stop releases only that child, never the parent Run.

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

### Guest completion evidence format

Lifecycle aggregates now require format 6 for GC reservations and immutable reset continuation
receipts. Format 4 is rejected by normal lifecycle access. The explicit
`sysbox state import-format4` tool previews by default; applying requires stopped
writers, an exact source digest and a durable original-byte archive. Preserve
checkpoints/configuration separately. Import converts metadata only and does
not authorize provider recovery; never edit version fields manually.

The current API schema is 14. Host completion of a standalone guest task stores
its terminal result together with an immutable grant identity and receipt hash;
SQL backends commit result and authority release together. The file backend
replays that durable proof after an interrupted publication. Old execution
format 4 and API schema 13 data require explicit rebuild; there is no compatibility
reader. A replay never contacts the guest and cannot release a successor epoch.

Metadata deletion uses a durable `delete.intent` containing topology UID and
state serial. It is written before state reset and removed after workspace,
placement and health cleanup. A restart or supervisor scan resumes this intent;
new workspace publication is rejected while it exists. The marker does not
prove external resource absence; provider teardown must already have a valid
owning-agent cleanup receipt.

SSH access and adoption host completion also require the original authority grant
at the storage boundary; callers cannot bypass this by invoking a backend adapter
directly. Their operation record and authority release remain a separate
retryable commit boundary until the operation mutation is migrated to the shared
atomic completion adapter.

### GC dispatch and evidence

`GET /v1/topologies/{name}/gc/references` returns the coherent lifecycle reference
digest and retained resource identities. It is not a provider inventory or deletion
authorization. `POST /v1/topologies/{name}/gc` takes one candidate (kind, external_id,
topology_uid, owner_uid, resource_uid, generation, inventory_digest) and requires
`Idempotency-Key`. It binds the existing owning-Agent placement, checks complete
activity and stopped authority under storage exclusion, and reserves the lifecycle
reference set before publishing `gc_delete`. 202 returns the reservation/command
identity. Same-key retries cannot change the candidate or restart a completed delete.

The reservation remains across lease expiry and process restart. Lifecycle,
snapshot and adoption writes cannot introduce references during deletion. The
host must read the identical reservation from its canonical repository and verify
exact provider identity and post-delete absence. Only a candidate-bound enrolled
host receipt can complete the reservation. Completion retires the command before
stopping authority and reopening writes; exact receipt replay repairs interrupted
completion. Worker terminal events and generic quiescence receipts do not prove
deletion. Unknown effects keep the reservation; automatic uncertain-intent
reconciliation and full batch inventory/retention are not yet implemented.

### Owning-agent adoption inspection

`POST /v1/topologies/{name}/adoption-inspections` accepts `type`, `name`, `id` and
`substrate`, requires `Idempotency-Key`, and returns202 with a durable
`adoption_inspect` NodeOperation. The owning host performs bounded read-only
inspection and signs the exact result. Successful operations expose
`inspection_evidence`; failed inspection is a known signed read-only failure.
Worker reports cannot supply or replace the host evidence. Source validation
rejects further grants once the operation is terminal.

`POST /v1/topologies/{name}/adoption-inspections/{inspection}/confirm` requires a
separate `Idempotency-Key` and consumes only the stored successful evidence. It
returns202 with the separate adoption operation. Current topology UID/serial,
placement and provider fingerprint are checked before mutation. Managed evidence
must belong to this topology; its existing resource UID/generation is preserved.
This supplies the public inspection/confirmation route, but does not add unowned
libvirt adoption or close the full P5 cold-fault matrix.

### GC batch plans and historical intents

`POST /v1/topologies/{name}/gc/batch` accepts 1–128 distinct candidates and an
`Idempotency-Key`. The entire normalized batch is reserved in one authority /
activity / aggregate transaction; a bad later candidate leaves no partial batch.
The key binds topology, owning Agent and all candidate identities. Changing,
truncating or appending candidates on replay conflicts. Containers and VMs precede
networks. Only the first unfinished reservation can receive execution authority;
its verified host completion publishes the next. Unknown outcomes stop progress
and keep all lifecycle references frozen. Batch metadata uses the existing
format6 reservation map; older code rejects multiple pending reservations.

`GET /v1/topologies/{name}/recovery/intent-only` reports unfinished journal actions
without provider effects or journal changes. Each entry includes the Run, exact
resource identity, journal revision/SHA256, quiescence and specific blockers.
Missing old operation anchors, unknown daemon calls, incomplete guest effects and
reset continuation requirements remain distinct. Returned evidence is classified
as requiring authorized observation; it is not automatically declared complete.
Unknown records are not force-cleared. This endpoint is a disposition report,
not physical host fencing or permission to replay an old request.

### Disposable host fencing evidence

This opt-in interface records externally signed process-exclusion evidence. It
never reboots a host or completes/replays an interrupted operation. Enroll only
isolated disposable acceptance Agents in API configuration:

```yaml
api:
  execution_grant:
    disposable_fence_targets:
      disposable-agent:
        lab_id: sysbox-p9-example
        controller_public_key_file: /etc/sysbox/fence-controller.pub
```

The raw Ed25519 public key must differ from the API issuer and every enrolled
host key. Keep its private key exclusively on the external controller.

- `POST /v1/topologies/{name}/host-fences` requires `Idempotency-Key` and
  `{"run_id":"...","grant":{...}}`, containing the exact retained parent Run
  grant. It checks the original durable command, Run incarnation and production
  request digest, requires an anchored nonempty action journal, and returns202
  with a `HostFenceRecord`.
- `GET /v1/topologies/{name}/host-fences/{id}` reads that record after restart.
- `POST /v1/topologies/{name}/host-fences/{id}/receipt` accepts the signed receipt
  and returns200. Conflicting receipt retries or invalid signatures return409.

Admission atomically freezes the topology, revokes its active grants and places
a persistent hold on the Agent across topologies. Another non-stopped topology
grant on that Agent prevents admission. New grants, command delivery, dispatch,
and nonterminal Run claims/writes are blocked; observation, stop and completion
evidence remain available. The journal hash binds an observed snapshot, not an
atomic cross-repository recovery decision. Request replay uses its original
pinned controller key and journal hash, including after configuration rotation.

Receipts bind the request digest, lab, original/new execution kernel boots,
external controller boot continuity, original QEMU process exit, disk identity
and `operation_result=unknown`. Receipt acceptance does not synthesize a stop
receipt or change any Run/action/resource outcome. It leaves the hold intact;
ordinary maintenance release is rejected. The explicit release endpoint below supports
only independently reconciled empty delete topologies; it does not resolve
unknown effects. See [upgrade boundaries](../operations/upgrades-and-recovery.md).

#### Observation after a host fence

`GET /v1/topologies/{name}/host-fences/{id}/recovery-plan` returns a read-only
triage plan. It checks the current original Run/journal against the admission
hash, distinguishes verified host process exclusion from provider operation
completion, and lists action identity/anchor, external identity, guest effect and
reset-continuation blockers. It does not create a missing lifecycle or write
Run/action/state records. `release_allowed` and `replay_allowed` remain false.

After a receipt is accepted, `POST .../host-fences/{id}/observe` with
`Idempotency-Key` dispatches `run_recovery` in **observe** mode to the original
owning Agent. No request body is needed. New requests require the original
journal snapshot to match and the Agent to advertise `run-recovery.v1`. The
request digest and key determine one durable command. Concurrent retries,
retries after delivery, and terminal retries return the same `command_id`
without resetting its lease or rerunning the command. An existing key remains
readable after journal drift; a new key is rejected. Use a new key only for a
new observation of the still-matching snapshot.

Read `GET .../recovery-plan?observation={command_id}` for the associated result.
The command must belong to this fence. The plan checks Run incarnation/attempt,
Agent, topology, state serial, observation time after fencing, and exact action
identity/coverage before displaying the reported presence. Status is `pending`,
`failed`, `matched`, `mismatched`, or `stale`; changed current metadata produces
`stale` with blockers, while a foreign command is rejected. The new endpoints
require an administrative role, like the existing remote recovery endpoints.

These reports pass through the owning host's read-only observation path and
normal command lease validation. For a fence-bound command, recovery authorization
includes `fence_observation` with the admitted request digest and the receipt's
new host boot ID. The host supervisor checks its own kernel boot, observes, then
revalidates the authorization before signing the complete report with its host
Ed25519 key. The optional `host_observation` proof also binds the command delivery
owner/attempt; the worker forwards it unchanged.

The plan verifies that proof with `api.execution_grant.host_public_key_files`
for the owning Agent, the exact fence request/new boot, and the retained command
lease. `observation_attestation=verified` removes only
`observation_not_host_boot_attested`. Unsigned reports remain `unverified` and
informational. A supplied but invalid signature produces `invalid`, retains a
`host_observation_signature_invalid` blocker, and exposes presence as unknown.
Missing/rotated host keys cannot establish verification; retain the enrolled key
when auditing existing evidence. A matching signed `absent` report cannot complete
a delete, replay a create, manufacture a stop receipt or release the host hold.
The signature proves who observed which data on which boot, not original operation
success or provider request quiescence.


#### Explicit release after completed deletion

`POST /v1/topologies/{name}/host-fences/{id}/release` requires an administrative
role, `Idempotency-Key`, and `{"observation_command_id":"..."}`. It returns200
with an immutable release receipt only after **all** checks pass in the storage
transaction:

- The original destroy Run is terminal and its incarnation/attempt still matches
  the retired observation command.
- The current topology state is empty, every journal action is succeeded, no GC
  batch is pending, and no reset continuation remains. Every action belonging to
  the original Run is a completed delete with a retained external identity.
- The host-signed report matches the current topology UID/serial, full original
  delete-action coverage, exact external identities and new fence boot, and says
  absent for every selected action.
- The original parent grant has an independently authenticated stop receipt.
  All retained grants for this host or topology are stopped; expired or revoked
  grants are insufficient.
- No unresolved Run, guest operation, node operation, console session or command
  remains for the topology.

The endpoint releases the barrier and host hold in one authority write, retaining
all fence/observation/stop evidence. It does not modify state, action outcomes,
Run status or grant stops. Failed checks return409 without releasing anything.
File storage holds lifecycle exclusion through authority publication; SQLite and
PostgreSQL retain transaction locks through commit.

Same-key/same-observation retries return the original receipt, even after API
restart, key configuration changes or subsequent work. A different key or
observation conflicts. Old retries cannot release a later barrier. New observation
requests and reauthorization against the released fence are rejected. Plans expose
`release_status=held|released`; `release_allowed=false` on the read-only plan is
not a substitute for the atomic endpoint checks.

Release and reconciliation records require execution authority **format5**. There is no automatic
conversion from format3/4; see [upgrade boundaries](../operations/upgrades-and-recovery.md).
The release endpoint itself does not reconcile an uncertain delete; the separate
transition below handles only the explicitly supported case. It cannot adopt an
uncertain create, resume an incomplete reset, or manufacture missing history.

#### Reconcile a fenced unknown deletion

`POST /v1/topologies/{name}/host-fences/{id}/reconcile-delete` requires an
administrative role, `Idempotency-Key` and `{"observation_command_id":"..."}`.
This narrow transition applies only to explicitly enrolled disposable hosts and
anchored, topology-wide destroy Runs. It invokes no provider effects.

The original fence journal hash, Run incarnation/attempt, original command and
revoked parent grant must still match. Every selected action must be an unresolved
`sysbox_node` delete with its original operation ID, `action:{id}` anchor, exact
candidate and state identity, Docker/libvirt driver, and no attachments/reset
work. The host-signed report must cover all selected actions with `absent`,
`provider_scope=local-only` and `delete_residue_absent=true`. The actual Docker
client must use the default local Unix socket; libvirt must report `qemu:///system`.
Libvirt also checks that a managed VM directory is absent; externally adopted
storage remains intentionally preserved. Other outstanding grants, child effects,
remaining resources or unresolved topology activity block the transaction.

On success the response contains the durable reconciliation decision. The
transaction commits the delete outcomes and removes exactly the matching state
resources, retires the original command, and closes the interrupted Run as
**failed, nonrecoverable** with an explicit reconciliation reason. It does not
claim that the interrupted execution returned success. The parent grant stop
references the authenticated fence/reconciliation evidence (`fence-delete:`),
not a fabricated host stop signature. The host hold remains until an explicit
`release` request passes its activity checks.

Execution authority format5 records preimage/postimage hashes, pinned verifier
key, signed observation and decision time. SQL commits lifecycle, Run, command
and authority atomically. Filesystem storage persists a prepared decision before
postimages; retries accept only the original or planned snapshots, finish the
remaining writes, then publish completion/stop evidence. Run history uses atomic
replacement, avoiding a torn append that would prevent recovery. A pending decision
blocks new observations and never releases the hold. Same-key completed retries
return the original decision; another key/report conflicts.

Release after reconciliation consumes the retained signed **pre-reconciliation**
report plus the exact committed postimages. The signature is never rewritten to
pretend it observed the new journal status/serial. Plans expose
`reconciliation_status=none|prepared|completed`. A stale original-journal label
after reconciliation does not negate the separately retained decision.

This does not cover create/reset, absent original anchors, remote daemons, unknown
external storage effects, composite guest work or arbitrary historical incidents.

### Snapshot preservation and bounded retention

Metadata restore may retain unresolved actions only when every unresolved entry
is identical in the snapshot and current journal in both directions. It keeps
the current journal unchanged and advances metadata serial; it cannot introduce,
erase, rewind or complete an intent. Changed/missing unresolved evidence remains a
conflict. Existing activity, maintenance and grant-stop requirements still apply.

`POST /v1/topologies/{name}/state/snapshots/retention-plan` accepts an admin request
such as `{"keep_last":3,"older_than":"2026-10-01T00:00:00Z","limit":32}`.
Keep-last must be at least one; batch limit is 1–128. The read-only plan binds the
full aggregate/catalog SHA256, lists oldest eligible snapshot IDs, and reports
protected entries (a compact summary for large catalogs). It preserves snapshots
with unresolved journal or unfinished workflows, committed restore references,
newest retained snapshots and snapshots newer than the cutoff.

Submit the unchanged plan to `POST .../snapshots/retention-apply`. The same
aggregate transaction recomputes eligibility and rejects changed catalogs or
edited IDs with409. A successful response lists removed snapshot metadata and
`resources_deleted=false`; no provider objects are deleted. Replan after a
successful batch or a lost-response conflict. Pending GC reservations block
retention, and state/action serials and contents remain unchanged.

### Provider snapshot capability boundary

Aggregate snapshots cover metadata (state and action journal), not VM memory,
container filesystems or provider disks. At the migration baseline `main=b1cc43d`
and current refactor, Docker, Firecracker and libvirt do **not** implement the
`driver.Snapshot` SPI; the interface alone does not advertise a usable capability.
There is therefore no existing provider snapshot operation to claim migrated.

| Provider | Metadata aggregate snapshots | VM/memory/disk snapshot via Sysbox |
|---|---|---|
| Docker | Supported by the chosen state backend | Not implemented |
| Firecracker | Supported by the chosen state backend | Not implemented |
| libvirt | Supported by the chosen state backend | Not implemented |

`TestBuiltinProvidersDoNotAdvertiseVMSnapshotFromMetadataSupport` pins this
boundary. Any future provider snapshot implementation needs separate grant,
identity, storage and restore acceptance; metadata restore never substitutes for
that acceptance or rewinds external resource generations.

### GC reference domains

A GC reference snapshot now carries explicit `domains` for `revision`, `artifact`,
`cache` and `placement`. Each domain is `complete` with a digest/count when
references are present, or `not_applicable` when the canonical lifecycle has no
reference in that domain. Malformed domain attributes are corrupt evidence.
Catalog collection rejects any other status before producing candidates.

This prevents an incomplete domain from being treated as an empty domain. The
cross-host catalog still needs an external producer for revision/artifact/cache
inventory; until that is supplied, that requirement remains open and GC stays
fail-closed. Snapshot/journal/state references continue to be collected in the
same aggregate transaction.

### Durable external GC reference sources

Topology-scoped publication uses the existing API administrator authentication:

- `GET /v1/topologies/{topology}/gc/sources` reads source versions and manifests.
- `POST /v1/topologies/{topology}/gc/sources/{source}/begin` accepts
  `{"topology_uid":"...","expected_version":0}` for first enrollment.
  The response contains the new `version` and `updating:true`.
- `POST /v1/topologies/{topology}/gc/sources/{source}/complete` accepts the same
  topology identity, the version returned by begin, and a `manifest`.

A manifest contains `domains` with all four keys (`revision`, `artifact`,
`cache`, `placement`), each a string array; `references` with exact
`source/resource_type/driver/external_id/owner_uid/resource_uid/generation`;
and an RFC3339 `valid_until`. An empty array declares an observed empty domain.
Opaque domain values are retained evidence; resource deletion protection needs
an explicit resource reference. Unknown domains and malformed references fail.

A participating publisher must successfully begin **before** changing or using
external references, and complete only after the entire inventory is durable.
No new publication may reuse an earlier completed version. Complete supports
exact replay after a lost response. A lost begin response can be inspected with
GET; it does not permit another writer to assume ownership of that publication.
An incomplete source blocks new GC. Offline/expired inventories are retained
and block new reservations. A reservation pins its immutable directory through
signed deletion completion, so expiry during deletion does not strand recovery.
All publication writes share the reservation transaction lock.

First enrollment explicitly upgrades that aggregate to format 7; other format 6
aggregates remain unchanged. Older writers reject the enrolled aggregate.
Current source registration is not rewound by snapshot restore; retained
snapshots also protect their source references. Metadata DELETE refuses enrolled
sources. Source deregistration and nonparticipating external writers are not
supported by this protocol. Operators must enroll every required producer;
this API does not discover arbitrary external stores or make them transactional.

Production publishers opt in with the service-level `gc_publication_source`
setting (1–64 letters, digits, `_` or `-`). Give each independently writing
API/host a stable unique ID. The service registers separate `-workspace`,
`-placement`, and `-artifacts` sources. Workspace upserts, immutable revision
materialization and legacy workspace snapshots publish before changing files;
placement writes publish before changing affinity. Host execution installs the
publisher context in artifact resolution, including local files, HTTP cache
hits/downloads, mutable-URL invalidation, and Docker OCI inspect/pull.

Contributions retain earlier revision digests, artifact digests and hashed cache
locations. An inventory is fresh for 24 hours after a successful write; an idle
source that expires blocks new GC until its publisher performs a successful
publication. This is deliberately conservative; there is no automatic stale
source removal or timer-based unknown-operation recovery.

Enable producers while old writers are stopped, then start all writers with
consistent configuration. Already enrolled topologies reject a current service
with missing configuration. Local CLI execution against an enrolled topology is
rejected; use the configured host executor. Initial enrollment is not a substitute
for stopping unconfigured/old writer processes. A source left `updating` after a
crash requires explicit investigation and completion using the existing protocol;
cold startup neither resumes its mutation nor clears its record.

The global revision registry (`POST /v1/revisions`) has no topology identity and
is retained outside this per-topology publisher protocol. There is no global
revision, shared artifact/cache deletion, or source retirement authorization in
this change. Topology metadata DELETE remains blocked while sources are enrolled.

A source manifest is bounded to 4,096 entries per domain. Publishers retain
prior entries rather than evicting evidence automatically; exceeding the limit
leaves publication incomplete and requires operator investigation. Concurrent
operations on one source may receive a version conflict and must retry after the
winning operation completes. They must not clear an updating record themselves.

A source with a complete manifest can be permanently retired with
`POST /v1/topologies/{topology}/gc/sources/{source}/retire`, sending
`topology_uid` and its `expected_version` (no manifest). The transaction refuses
in-progress publication and active GC reservations. Retirement increments the
source version, keeps the complete manifest, and rejects subsequent begin or
complete calls, including old completion replay. Repeating the same retirement
request returns the existing result.

Retired sources do not need freshness renewal: their references are immutable
and continue to protect resources and caches indefinitely. Retirement upgrades
only the affected aggregate to format 8, which older writers reject. It is not
proof that a host process stopped, does not discard references, and does not
permit metadata deletion or shared cache deletion. A failed/incomplete publisher
cannot be retired to bypass its unresolved operation.
