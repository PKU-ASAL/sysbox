# CLI Reference

本页对应当前 `sysbox --help`。具体版本以正在运行的 binary help 为准。

> 重构分支的生命周期写入要求原子 repository；`--allow-unsafe-state` 不能绕过这个要求。旧 state 格式需显式重建，`gc --dry-run` 仅预览本地候选；`gc submit` 将显式候选交给 owning Agent 删除。完整能力缺口见[实施状态](../design/2026-09-28-runtime-refactor-implementation-status.md)。

## Host Supervisor

```text
sysbox host-supervisor --config /etc/sysbox/supervisor.yaml --identity /etc/sysbox/supervisor-identity.json
```

Serve signed API guest tasks and remote node pause/resume over a local Unix socket. Requires root, an explicit
non-root worker group, private signing key and trusted canonical repository.
The Agent must set `agent.host_supervisor_socket`; these operations have no
direct-provider fallback. See [guest tasks](guest-tasks.md) for configuration,
recovery semantics and the remaining ordinary lifecycle migration boundary.

## Global Options

| Flag | Default | Meaning |
|---|---|---|
| `-f, --file PATH` | `field.sysbox.hcl` | HCL topology file |
| `--state PATH_OR_URL` | `.sysbox/runs/default/state.json` | State file or remote URL |
| `--backend URL` | empty | Backend URL; overrides `--state` |
| `--auto-approve` | false | Skip mutation confirmation |
| `--allow-unsafe-state` | false | Allow mutation without backend locking or CAS |

## Workspace And Validation

### `init`

Initialize a new Sysbox workspace.

### `validate`

```text
sysbox validate
```

Parse and validate HCL without contacting a provider.

### `plan`

```text
sysbox plan [--refresh]
```

Show ordered changes. `--refresh` probes existing resources for drift.

## Mutation

### `apply`

```text
sysbox apply [--refresh] [--target ADDRESS]
```

Provision planned resources. `--target` limits apply to one canonical resource address and its required dependencies; it is not a substitute for a converged full plan.

### `reset`

```text
sysbox reset [--target sysbox_node.name]
```

Recreate all managed guests or exactly one node from immutable baselines.

### `destroy`

```text
sysbox destroy
```

Tear down resources represented in state in reverse dependency order.

### `import`

```text
sysbox import ADDRESS EXTERNAL_ID --substrate NAME
```

The legacy local state mutation path has been removed. Managed adoption must use
the API adoption plan/confirm workflow so an owning agent proves provider
ownership before the state repository is changed.

## Runtime Control

### `pause` / `resume`

Pause or resume a node using its provider capability. These commands do not change desired topology.

### `show`

Print one resource's state details as JSON.

### `output`

Print evaluated topology output values.

## State

```text
sysbox state list
sysbox state show ADDRESS
sysbox state get ADDRESS[.ATTRIBUTE]
sysbox state mv SOURCE DESTINATION
sysbox state rm ADDRESS
```

- `list` lists canonical addresses.
- `show` prints full public instance attributes.
- `get` prints a resource or one attribute.
- `mv` renames logical state identity without touching the external object.
- `rm` forgets state and does **not** destroy the real object.

Quote addresses containing string keys:

```bash
sysbox state show 'module.lab.sysbox_node.web["blue"]'
```

## Service Commands

### `serve`

Start the HTTP API server using the selected Sysbox config.

### `agent`

Manage or run the host Agent. Use `sysbox agent --help` for the version-specific registration and execution subcommands.

## Other Commands

- `version [--json]`: build version, commit, time and Go version.
- `completion`: shell completion generation.
- `help`: command help.

## Safety Notes

- Mutation rejects backends without locking and CAS unless `--allow-unsafe-state` is explicit.
- `state rm` is not cleanup; it removes the ownership record needed for safe destroy.
- A stale stored plan or changed state serial fails before provider mutation.
- Configuration diagnostics, state conflicts and provider failures all return non-zero status; automation should use structured API diagnostics where available rather than matching prose.

## GC 候选提交

`sysbox gc submit plan.json --api http://127.0.0.1:9876 --key cleanup-1`
提交一个明确候选，不直接访问 provider。API token 可通过 `SYSBOX_API_TOKEN`
提供。`plan.json` 为 `{"topology":"lab","candidate":{...}}`；candidate 包含
kind（container/network/domain/firecracker）、external_id、topology_uid、owner_uid、
resource_uid、generation、inventory_digest。摘要来自 canonical lifecycle references；
实际 provider 身份仍由宿主删除前核验。只使用完整盘点得到的候选，不把显示名当外部 ID。

输出为持久受理回执，不表示删除完成。同 key/同候选重试返回原操作；改参冲突。
API 与执行宿主必须消费同一 canonical reservation。宿主失联或未知删除结果保持冻结，
不能用新 key 绕过。完整 owning-agent inventory、保留期和批量调度尚未验收关闭。

## 显式导入 aggregate format 5

`sysbox state import-format5` 默认只预览。确认旧 writer 全部停止后，使用
`--apply --offline --source-sha256 <预览摘要> --archive <原文归档路径>` 提交。
转换保留资源、未知 action 和 reset continuation，不证明旧 provider 请求已经停止。
format 6 添加持久 GC reservation；旧 writer 必须保持停止，不能手改 version。

## Owning-agent adoption inspection

`sysbox import inspect sysbox_node.web <external-id> --api <url> --topology lab --substrate docker --key inspect-1`
持久派发只读检查，返回 NodeOperation。等待该操作完成并核对证据后，再执行
`sysbox import confirm <inspection-id> --api <url> --topology lab --key adopt-1`。
确认只消费保存的宿主签名证据，不能在命令行替换 provider 证据或外部 ID。
普通 `sysbox import` 的本地直接 state 写入仍已移除。

检查成功不等于认领成功。Provider 在认领时再次比较指纹；拓扑 serial 或归属变化
会拒绝。已受管 Firecracker/libvirt 的恢复使用证据中的原 UID/generation，其他
topology 的归属不能转移。无主 libvirt 域的普通导入仍未实现，不能视为完成支持。

## Complete local inventory and bounded GC batches

`sysbox gc plan --runs <dir> --output gc-plan.json` inventories Docker containers /
networks, libvirt domains and Firecracker manifests against every canonical
filesystem aggregate under the selected directory, including retained journals
and snapshots. It runs on the resource host. Use `--catalog <sqlite-or-postgres-url>` to enumerate every topology in a shared
canonical database instead. Credentials are not written to the plan; a catalog
fingerprint prevents mixing earlier observations from another backend. A fixed
topology selector or templated storage path is rejected as an incomplete catalog.
Other runs directories, databases and execution hosts remain outside the selected
scope and cannot be implicitly covered.
A failed provider query or corrupt canonical inventory rejects the plan, rather
than producing deletion candidates from a partial result. Unknown owners are
reported as excluded and never adopted or deleted.

Plans retain raw inventory plus selected candidates. Default selection requires
an earlier complete `--previous-plan <file>` at least `--min-age 24h` old containing
the same owner/resource/generation/external identity. A first plan is an inventory
baseline with no age-qualified candidates. `--min-age 0` explicitly selects newly
observed candidates. `--limit` defaults to32 and is capped at128. Nodes/VMs precede
networks. Any retained resource UID or matching provider external ID protects the
object. This is an observation interval policy, not a claim about creation time.

`sysbox gc batch-submit gc-plan.json --api <url> --topology lab --key batch-1`
submits one topology's selected batch. The latest plan must be complete and no
more than15 minutes old. API admission revalidates references and actual host
ownership, so possession of a plan is not authorization by itself. A retry keeps
the same plan/key. Batches are durably reserved atomically, execute one item at a
time, and retain exclusion after unknown effects. No local raw deletion is used.

The API `GET /v1/topologies/{name}/recovery/intent-only` exposes a read-only
historical disposition report with journal hashes and evidence blockers. It does
not authorize forced success or arbitrary old-request replay.

`sysbox state audit-intents <aggregate.json>` reads a legacy format4/5/6 aggregate or host-effect-v2 ledger
without opening its backend, migrating it or changing version fields. It emits
source and per-record SHA256 plus minimal action identifiers; provider-private
state and credentials are not returned. Keep the original source alongside the
audit. This provides reproducible incident triage, not a new execution receipt.


## Isolated host fencing acceptance

`python3 scripts/acceptance/host_fencing.py create --root <new-lab> --base <pinned-cloud-image>`
creates a fresh private two-VM test lab. Run `prepare`, then `fence`. The script
only accepts a lab with an explicit disposable scope marker. It fingerprints the
exact execution QEMU and disk, uses pidfd/QMP fencing, verifies a new execution
boot ID and unchanged control boot ID, and checks local Docker/libvirt canaries.
Its proof distinguishes `host process exclusion` from `operation_result=unknown`.
It never targets an existing lab or historical resource. Remote daemon jobs,
external storage, migrated domains and business-result success require separate
proofs and remain outside this acceptance command.

### Signed disposable fencing evidence

`sysbox host-fence request-digest <admission.json>` validates the API admission
record and prints its request digest. On the trusted external controller,
`sysbox host-fence attest <admission.json> <evidence.json> --key <controller.key>`
validates the exact request binding and local-provider acceptance evidence, then
prints an Ed25519-signed receipt. The key must be a private regular file containing
64 raw bytes and match the admission's pinned public key. Protect the controller
and its input files: the signer validates evidence structure and identity; it
cannot independently establish the physical truth of arbitrary JSON assertions.

For an already scoped disposable acceptance lab, pass `--admission`, `--attestor`
and `--controller-key` together to `host_fencing.py fence`. Before canary creation
or power-off the script checks the lab, original execution boot and controller
key. It snapshots the admission and produces `.receipt.json` only after pidfd
exit, new kernel boot, unchanged external controller boot and provider readback.
Submit that receipt to the API endpoint documented in [API](api.md). Existing
unsigned reports lack this pre-actuation binding and cannot be attested by this
flow. Receipt acceptance leaves the persistent host hold in place. The API explicit
release endpoint is available only after independently completed deletion,
verified absent observation and all original grant stops; see [API](api.md).

### Scoped cache GC

`sysbox gc cache --catalog sqlite:///absolute/catalog.db --cache-root /absolute/private/cache --path <64-hex-key>/<file>`
outputs a JSON plan without deleting. Save that plan and use
`sysbox gc cache --catalog sqlite:///absolute/catalog.db --apply-plan plan.json`
to remove the exact file after revalidation. This requires a private, current-user
owned cache exclusively used by writers enrolled in this catalog. Stop and enroll
old writers before use. The whole catalog is frozen through deletion, including
new topology insertions. Filesystem catalogs, topology-selected catalogs,
unregistered/incomplete/expired sources, symlinks, hardlinks, writable targets,
retained references and stale file/catalog identities are rejected.

PostgreSQL supports planning and rejects apply: losing a remote database lock
cannot authorize a local unlink. This is not cluster-wide shared-cache deletion.
The cache root retains `.sysbox-gc/<id>.intent` before unlink and `.done` afterward.
An interrupted intent blocks further deletion and is never automatically replayed;
manual investigation is required. A receipt does not authorize deleting a recreated
file. Each invocation handles one file and never recursively removes directories.
