# Sysbox 重构接口变更清单

> 日期：2026-09-28
> 关联设计：[观察驱动的整体重构方案](2026-09-28-observation-driven-runtime-refactor.md)
> 状态：目标接口基线；实施前必须逐项确认。实际代码与迁移缺口见[实施状态](2026-09-28-runtime-refactor-implementation-status.md)。文中的 v2、新枚举、对象型错误和候选 CLI 参数不是已经交付的承诺；本阶段 HTTP `error` 仍为字符串。

本文回答一个问题：重构后，哪些接口必须保持，哪些接口可以扩展，哪些接口明确换代。

### 2026-10-04 已实施：owning-agent 远端恢复扩展

新增管理员异步接口 `POST /v1/runs/{id}/remote/observe`、`POST /v1/runs/{id}/remote/recover`，返回 command_id；结果经 `GET /v1/runs/{id}/remote-reports/{command}` 查询。Agent 使用签名保护的 `POST /v1/agents/{agent}/commands/{command}/recovery-authorize` 在执行前取得当前授权。

这是一项 capability 协商扩展：仅 `run-recovery.v1` Agent 接收 `run_recovery`，现有 agent.v1 命令不新增必填字段。新恢复报告必须完整携带 Run 轮次、action/resource UID/generation、command lease proof；旧 journal 没有 run_queued_at/run_revision 时不能远程恢复。running Run 可以 Observe，未过期 lease 不允许 Recover。Recover 只恢复有证据的 journal outcome，不自动重放外部 effect，也不等于 Run 已成功。

### 2026-09-30 已实施：Agent metadata

本检查点保留 HTTP v1 DTO、agent.v1 和字符串 `error`，没有引入下文设计中的 v2。实际行为调整如下：

| 接口 | 当前行为 |
|---|---|
| POST /v1/agents | 重新注册原子保留禁用、隔离、原因和创建时间；受保护身份必须提供当前实际 secret，省略或替换凭据返回 409；尚无凭据轮换接口 |
| POST /v1/agents/{agent}/heartbeat | 心跳不修改策略或凭据；省略名称等字段保留旧值，存活时间由服务端确定 |
| Agent 策略更新 | 只原子修改显式给出的策略字段，不覆盖并发心跳和其他策略字段 |
| Agent Get/List、调度、Guest 路由 | 存储失败不再使用缓存授权，也不伪装成空列表或目标缺失；不存在 404、冲突 409、损坏或不可用 500 |
| 内部 Go 接口 | AgentService.List 增加 error 返回值；新增 Register/Heartbeat/UpdatePolicy/ExpireHeartbeat 操作，后端共享状态规则 |
| 旧 Agent metadata | 缺失 status/protocol 等必需字段的记录明确拒绝，不自动补成在线节点；升级须停止旧 writer，按需显式重建 |

## 1. 变更分类

| 标记 | 含义 | 发布要求 |
|---|---|---|
| 保持 | 现有调用方式和语义继续成立 | 必须有回归测试 |
| 兼容扩展 | 原调用仍可用，但新增字段、状态或可选能力 | 旧客户端不能因未知字段失败；文档说明新状态 |
| 破坏性变更 | 旧调用、数据或二进制不能继续工作 | 提升协议/格式版本，启动时明确报错，提供重建或升级指引 |
| 内部重构 | 不属于用户或 provider 接口 | 不承诺 Go 私有类型兼容，但必须保持行为契约 |

目标不是“所有接口永远不变”。目标是每一项变化都有明确类别、版本和测试，而不是随着重构偶然改变。

## 2. 总览

| 接口面 | 结论 | 计划版本 |
|---|---|---|
| HCL 资源类型和核心属性 | 保持 | HCL v1 |
| HCL 新恢复/执行策略属性 | 兼容扩展 | HCL v1，新增可选字段 |
| CLI 核心命令 | 保持 | CLI v1 |
| CLI 输出文本 | 兼容扩展 | 增加结构化状态和诊断，不保证逐字相同 |
| HTTP 路由和核心请求 | 保持为目标 | API v1 |
| HTTP 响应状态/错误/执行结果 | 兼容扩展 | API v1，客户端必须容忍新枚举和字段 |
| API ↔ Agent 控制协议 | 破坏性变更 | protocol v2 |
| `driver.Descriptor` 与 capability 体系 | 保持模型，扩展契约 | driver SPI v2 |
| `driver.Node` / `NIC` / `NodeState` 方法 | 可能破坏性变更 | driver SPI v2，提供适配层 |
| `substrate.Connection` | 兼容扩展后收敛为主执行 SPI | execution v2 |
| `driver.GuestExec` | 过渡保留，最终收敛 | execution v2 |
| state JSON、API DB、Firecracker manifest | 破坏性变更 | schema generation +1 |
| HTTP/S3 state backend mutation | 不进入新 mutation 引擎 | 明确 unsupported |

## 3. HCL 配置接口

### 3.1 保持不变

以下使用方式不应因为 runtime 重构改变：

- `sysbox_node`、`sysbox_network`、`sysbox_image` 等资源类型名称；
- `substrate`、`image`、`links`、`depends_on`、`provisioner` 等核心属性；
- 资源地址、模块文件布局和 revision 内容寻址规则；
- Docker、Firecracker、libvirt 的现有 provider 配置基本结构；
- `plan`、`apply`、`destroy`、`reset` 对用户的核心含义。

用户现有拓扑在新 state generation 中重新 apply 时，应该得到相同的资源目标和依赖关系。

### 3.2 兼容扩展

可以增加显式的可选字段，但不能通过默认值悄悄扩大破坏性行为：

```hcl
resource "sysbox_node" "vm" {
  # 现有字段保持

  lifecycle {
    # 新增：是否允许自动恢复/替换的策略
    recovery = "observe" # observe | restart | replace | manual
  }
}
```

候选扩展包括：

- recovery 策略；
- action timeout、retry budget、maintenance window；
- 是否允许后台执行、远程取消和输出上限；
- provider manifest 的保留或清理策略。

这些字段必须进入 plan fingerprint；修改它们应使旧 plan 失效。

### 3.3 不允许的隐式变化

- 不能因为新引擎支持 reconcile，就默认把 `destroy` 变成自动修复；
- 不能把 `unknown` 当成 `absent` 并自动创建或删除；
- 不能把 provisioner 重新执行当作普通幂等 action；
- 不能改变同一 HCL 在没有诊断的情况下的资源地址或 provider 选择。

## 4. CLI 接口

### 4.1 保持的命令

保留现有核心命令及主要参数：

```text
sysbox validate
sysbox plan
sysbox apply
sysbox destroy
sysbox reset
sysbox show
sysbox state
sysbox gc
```

### 4.2 兼容扩展

新增面向诊断和恢复的命令或选项：

```text
sysbox observe <topology>
sysbox recover <topology> --run <run-id>
sysbox actions <topology>
sysbox reset-state --scope <topology|database|all>
sysbox adopt <topology> --resource <address>
```

建议新增机器可读输出：

```text
--output json
--output jsonl
```

文本输出可以增加字段和诊断，但不把当前日志文本当作稳定协议。脚本必须使用 JSON 输出或退出码。

### 4.3 退出码

保留成功/失败二分，同时增加可区分的失败类别：

| 类别 | 含义 | 是否可直接重试 |
|---|---|---|
| success | 目标已满足 | 不需要 |
| validation | 配置或能力不合法 | 否，先修改配置 |
| conflict | plan、generation、lease 或 CAS 冲突 | 重新 observe/plan |
| unknown | 外部结果无法确认 | 先 recover/observe |
| unavailable | provider、agent、数据库不可用 | 有界重试 |
| failed | 已确认动作失败 | 按 action policy 决定 |

旧版本只识别非零退出码时仍然安全；新脚本可以按结构化 `category` 处理。

## 5. HTTP API 接口

### 5.1 保持为目标

保留现有主要资源路径和请求方式：

- topology、revision、plan、apply、destroy、reset；
- run 查询、日志和状态流；
- agent 注册、heartbeat、command；
- node、guest execution、guest file operation；
- health、readiness、resource observation。

既有字段不应改变含义。需要移除字段时必须走新 API version，而不是在 v1 中静默复用为其他含义。

### 5.2 兼容扩展字段

Run response 增加：

```json
{
  "status": "uncertain",
  "phase": "verifying",
  "attempt": 2,
  "plan_sha256": "sha256:...",
  "state_generation": 4,
  "lease_epoch": 18,
  "action_id": "...",
  "requires_recovery": true,
  "diagnostics": []
}
```

Resource observation 增加：

```json
{
  "existence": "present",
  "identity": "verified",
  "runtime_status": "running",
  "conditions": [],
  "observed_at": "...",
  "observation_stale": false
}
```

响应新增字段必须使用 `omitempty` 或明确默认值；客户端必须忽略未知字段。

### 5.3 错误响应

统一错误 envelope：

```json
{
  "error": {
    "code": "state_conflict",
    "message": "state changed after plan",
    "retryable": false,
    "run_id": "...",
    "action_id": "...",
    "diagnostics": []
  }
}
```

HTTP 状态码继续表达粗粒度结果，`error.code` 表达稳定机器语义。建议分类：

```text
invalid_request
unauthorized
forbidden
not_found
conflict
state_conflict
lease_lost
unknown_result
provider_unavailable
unsupported_capability
corrupt_state
```

不能把 provider 原始字符串作为 API 客户端的稳定判断条件。

### 5.4 需要单独确认的 HTTP 变化

- 查询参数传 token 属于遗留兼容方式，建议保留一个过渡周期并标记弃用，默认只接受 Authorization header；
- HTTP/S3 state backend 不提供新 mutation engine 所需的 locking/CAS/事务能力，相关 mutation 返回 `unsupported_capability`，不能由 `allow_unsafe_state` 静默绕过；
- SSE/状态流保留，但事件增加 `action_intent`、`action_outcome`、`observation`、`uncertain` 类型。

## 6. API ↔ Agent 协议

这是本轮明确的破坏性接口，因为 Agent 必须理解 action identity、generation、epoch、执行结果不明和 adopt。

### 6.1 protocol v2 必须携带

```json
{
  "protocol": 2,
  "command_id": "...",
  "action_id": "...",
  "topology_uid": "...",
  "resource_uid": "...",
  "generation": 3,
  "lease_epoch": 18,
  "deadline_at": "...",
  "attempt": 1,
  "operation": "observe|create|start|stop|destroy|exec|copy"
}
```

Agent 必须在开始动作前校验 command、lease epoch 和 resource generation；失去执行权后不开始新动作。

### 6.2 结果协议

Agent 回传必须区分：

- `succeeded`：后置条件已确认；
- `failed`：明确没有达到后置条件；
- `uncertain`：动作可能已生效，但无法确认；
- `cancelled`：本地取消等待，不代表远端动作已终止；
- `expired`：未开始或超过 deadline。

Agent 不得把所有连接断开都映射成 failed，也不得用 PID 作为跨 provider 的通用执行 ID。

### 6.3 升级方式

- Agent 启动时报告协议版本和能力；
- API 与 Agent 建立连接时协商版本；
- v1 Agent 只能接收兼容的只读/legacy run；
- v2 mutation 不能下发给 v1 Agent；
- 旧 Agent 淘汰后才删除 v1 编解码和路由。

## 7. Driver / Substrate SPI

### 7.1 保持的接入模型

以下模型必须保持：

```text
实现能力接口
    ↓
创建 driver.Descriptor
    ↓
注册到 Registry
    ↓
runtime 通过 capability 查询
```

新 substrate 仍然可以通过实现 `Node`、`NIC`、`NodeState` 和需要的 optional capability 接入，不需要修改 API store 或通用恢复算法。

### 7.2 Driver SPI v2 的候选变化

以下变化属于 provider 需要适配的内部扩展：

```go
type OperationContext struct {
    ActionID       string
    ResourceUID    string
    Generation     int64
    LeaseEpoch     int64
    Deadline       time.Time
}
```

生命周期动作和 attach/delete 应接收 operation context，或通过新的 v2 接口承载。旧方法不能隐式忽略身份，否则无法阻止 stale worker 操作新 generation。

provider state 建议从 `any` 收敛为带 codec/version 的 opaque envelope；runtime 仍不解析 provider 私有字段。

### 7.3 生命周期观察语义

`ObserveNode` 不再只返回一个 `Running bool` 作为决策依据。保留旧字段以兼容代码，同时增加：

- existence：present / absent / unknown；
- identity：verified / conflict / unknown；
- runtime status：prepared / running / paused / exited / unknown；
- health conditions；
- observedAt、provider reason、adopted。

`AdoptNode` 表示“接管 Sysbox 已有且身份可验证的资源”，不等同于 Import 外部任意资源。

### 7.4 Node 内执行 SPI

`substrate.Connection` 是主执行入口；`driver.GuestExec` 过渡保留，最终委托给同一实现。

| 当前接口 | 目标变化 |
|---|---|
| `Exec` | 保留请求模型，增加结果状态、取消和截断语义 |
| `ExecBackground() (int, error)` | 改为不透明 `ExecutionHandle`；PID 只作为可选诊断 |
| `CopyFile` | 增加摘要、大小、权限和原子安装选项；能力不足时返回 unsupported |
| `WaitReady` 可选接口 | 保留，但纳入连接能力/ready conditions |
| Console | 保持独立，不与普通 Exec 合并 |

Docker、Firecracker、libvirt 必须通过同一组 execution contract tests。

## 8. 状态、数据库和 Firecracker 工作目录

这些接口明确破坏兼容，原因是旧格式无法表达新的 identity、generation、journal、observation 和 manifest 语义。

### 8.1 State JSON

- 提升 schema generation；旧版本返回 `unsupported_state_version`；
- 不把旧 state 当成空 state；
- 通过显式 `reset-state`、导出/重建流程创建新 state；
- state 中保存 ResourceIdentity、generation、provider opaque state 和最近 observation，不保存运行时指针。

### 8.2 SQLite/Postgres

- 新数据库 schema generation 与旧版本不兼容；
- 启动检查 schema，不自动 drop/迁移共享数据库；
- Postgres 数据库必须区分 sysbox deployment/schema，清理命令需要显式 scope；
- 旧数据库在备份、停写和资源清点后重建；
- filesystem backend 新布局使用聚合 snapshot、CAS 和原子替换。

### 8.3 Firecracker manifest

- manifest format 与 state schema 独立版本化；
- 旧 `sysbox.json`、PID 文件或目录不能单独证明资源属于当前 generation；
- 新 manifest 缺失、摘要不匹配或身份不明时返回 orphan/unknown；
- `destroy` 必须先校验 owner、resource UID、generation、PID start time 和 socket 归属；
- 不自动递归删除无法证明归属的旧目录。

## 9. 兼容策略和迁移流程

### 9.1 不做什么

- 不做后台隐式数据库迁移；
- 不允许新旧 runtime 同时写一个 topology；
- 不提供“遇到错误就当不存在”的兼容降级；
- 不把旧 Agent 假装成支持 v2 action 语义；
- 不保证 Go 私有实现类型、日志逐字内容或 provider 目录细节兼容。

### 9.2 推荐流程

1. 停止旧版本 mutation、scheduler 和 Agent dispatch。
2. 使用旧版本执行安全 destroy，或建立包含 owner/generation/external ID 的资源清单。
3. 备份并明确 scope；共享 Postgres 不得按项目目录猜测删除范围。
4. 清理旧 state/database/manifest，初始化新的 schema generation。
5. 部署新 API、Agent 和 CLI，协商 protocol v2。
6. 重新 publish revision 并执行 `validate → plan → apply`。
7. 运行 topology、网络、guest exec、reset、destroy 和恢复验收。
8. 确认没有旧 runtime 写入后，再删除 legacy adapter、v1 协议和旧目录清理代码。

“可以破坏兼容”只意味着允许重建格式，不意味着启动时自动删除远程数据库、运行中的 VM、容器或网络。

## 10. 回归测试要求

### HCL/CLI

- 现有 examples 在新 schema generation 下 validate/plan 结果一致；
- `--output json` 字段稳定，未知状态不会导致客户端 panic；
- unknown、conflict、uncertain 的退出码和诊断可区分。

### HTTP

- 旧请求仍能创建基本 run；
- 旧客户端忽略新增字段仍能读取完成 run；
- v2 action/generation/epoch 的冲突、过期和结果不明有固定响应；
- SSE 新事件不会破坏旧事件解析器的忽略未知类型行为。

### Agent

- v1/v2 协商和拒绝矩阵；
- lease 丢失、deadline、重复 command、out-of-order completion；
- action outcome 的 succeeded/failed/uncertain/cancelled 区分。

### Driver

- Docker、Firecracker、libvirt 通过同一 Node/NIC/Observe/Adopt/Connection contract suite；
- provider state 编解码 version mismatch；
- PID 复用、socket 伪匹配、manifest drift 不会 signal 或删除其他资源。

### Storage

- filesystem、SQLite、Postgres 的 CAS、lease、损坏记录、列表顺序一致；
- 旧 schema 明确拒绝；
- action intent 已提交但 provider 结果丢失时可通过 observe 恢复，不重复不可重放操作。

## 2026-10-06 实施补充：policy 目标身份

Docker policy 的 `PolicyTarget.State` JSON 新增必填 `identity`（owner UID、resource UID、topology UID、generation、action ID），与 `container_id` 一起持久化在 firewall/router 的 `policy_target_state`。这是目标节点身份，不是 firewall 自身的操作代际。

Apply/Observe/Delete 在 namespace 访问前核对真实容器 labels 与精确 ID。旧缺失身份的记录返回 InvalidState，不按名字采用新容器；需要在目标节点已有完整持久身份的前提下重建 policy 记录，不能手填当前代际跳过恢复验证。HTTP 路由和 HCL 字段未变。

### Namespace 文件契约

`network.ApplyRulesetInNetNS`、`ObserveRulesetInNetNS`、`DeleteRulesetInNetNS` 的 namespace 参数从路径字符串改为 `*os.File`。调用方负责打开并核验目标，调用期间保持 FD 有效，结束后关闭；底层只借用 FD。旧字符串签名已移除。`sysbox-netns` 命令仍接受路径，并支持继承的 `/proc/self/fd/3`，向 nsenter 转发打开的 FD。

Router checkpoint policy 恢复不再执行 ApplyRuleset；缺少已记录 table/digest 或观察结果不匹配时拒绝恢复，需要后续显式收敛动作处理。

部署时须同步升级 `sysbox-netns` helper；旧 helper 不转发 namespace FD，不满足新调用契约。

## 11. 最终承诺

实施补充（2026-10-07 网络证明 v2）：隔离网络磁盘 ownership 记录必须含 bridge/proxy link 的 namespace 与 ifindex 证明。旧 v1 alias-only 记录明确拒绝，需由运维确认归属后重建；不自动删除旧资源。root bridge alias 从名称标记改为完整资源身份摘要。删除不再使用裸名称的 root bridge/proxy 创建辅助路径。网络操作必须在记录的同一默认/root network namespace 中运行，换 namespace 后即使查询 absent 也拒绝退休证据。

实施补充（2026-10-07 Guest completion）：Guest execution/file completion HTTP 必须提交 command proof（action_id、resource_uid、generation、lease_epoch、lease_owner、attempt）；旧无 proof 回报不兼容，返回 409。内部存储新增原子完成接口，服务层不再允许 Get→CAS 之间被替换。取消未开始的操作退休原 command。重复相同 terminal receipt 仍幂等，其他结果冲突。Agent/API 必须同步升级。

实施补充（2026-10-07 轮次协议）：Run command ID 变为 Run ID/queued_at/revision 的稳定摘要，不再是 `run-<ID>`。`POST /v1/agents/{agent}/runs/{id}/claim` 必须提交 `{queued_at, revision}`；旧空 body 返回 400，旧轮次返回 409，API/Agent 必须同步升级。Postgres schema 6 允许同 Run ID 多轮派发；旧 SQLite run_id 唯一约束要求重建。NodeOperation 完成入口强制完整 action/resource/generation/lease_epoch/lease_owner/attempt，缺失或失效证明返回冲突；新增原子存储接口 `CompleteNodeOperation` 取代 service Get+CAS 完成流程。

实施补充（2026-10-07 原子 outbox）：API 存储新增 `CreateNodeOperationDispatch(operation,command)`，生命周期派发必须使用此原子接口；operation 从提交起就绑定 command ID。`AgentService.WakeCommand` 仅唤醒已持久化命令，不写存储。共享 NodeOperation 记录携初始 dispatch seed；独立命令状态/退休记录覆盖 seed，禁止清除退休记录后重新投递。Run 已存在 queued 记录的派发须保持相同执行快照，并在同事务中改为 assigned 与创建命令。公共 HTTP 路由不变。

实施补充（2026-10-07 attachment）：`LinuxNetwork.DeleteAttachment(kind,name,namespace)` 和 raw TAP/Veth name-only 删除函数已移除。Docker isolated attachment 与 Firecracker TAP 的 driver state 必须含 `AttachmentLink` v1 证明（namespace dev/inode、ifindex、name/type/alias）；旧记录不兼容，Observe/Delete 明确拒绝，不自动认领同名接口。libvirt Create、生命周期及 NIC 修改拒绝缺持久身份；Firecracker Create/reset 必须显式提供 action 身份。旧 NodeResourceHandler.Import 实现已删除，未提供 ownership adoption 的类型在 API 派发前返回 Unsupported；只读 data-source inspection 不因此获得资源管理权。

实施补充（2026-10-07 双轨清理）：删除 runtime `CheckpointRecoverer`/`CheckpointCleaner`、`RecoverCheckpointResource`/`CleanupCheckpointResource` 等旧执行接口和 `RecoveryNotRequired` 豁免；正式恢复/清理只走 action journal。checkpoint 仍可作为诊断记录读取，不能授权资源变更。Docker Create 缺 operation identity 返回 InvalidState；generation=0 且无 context identity 的生命周期/执行调用同样拒绝。旧无身份 import 句柄不再可用于副作用；ownership adoption 仍未提供。旧 checkpoint E2E 入口替换为 `test-firewall-recovery-e2e`，不再保留旧接口兼容测试。

实施补充（2026-10-07 firewall discovery）：Policy provider 可实现 `PolicyDiscovery.DiscoverRuleset(context.Context, PolicyTarget, RulesetSpec)`；必须只读核验目标身份、策略代际及解析设备绑定后的期望摘要，不得调用 Apply。默认引擎的 firewall 新建/恢复要求该能力，未实现的 provider 明确返回 unsupported。Firewall 损坏目标记录从 drifted 改为 unknown+error。Docker 普通 firewall 不要求 ip_forward；NAT 在写规则前核验转发，不再先写后报错。

实施补充（整体收口第一步）：policy 共享锁路径由 `/run/sysbox/policy-locks` 改为 `/var/lib/sysbox/policy-locks`，与持久证据同卷；升级必须停止旧 writer 后统一切换。NodeOperation 内部存储接口 `SaveNodeOperation` 删除，使用 create-only 的 `CreateNodeOperation` 或显式 `CompareAndSwapNodeOperation`。三后端保存版本化完整记录；旧文件目录/SQLite列式表/Postgres扁平记录不兼容，需保留备份并重建，不会自动删除或猜测迁移。NodeOperation 公共 CommandID 仍不输出；Agent 不得修改请求人、角色和既有审计前缀。PostgreSQL API schema 提升为 5。

实施补充（policy write-ahead evidence）：执行账户除共享 `/run/sysbox/policy-locks` 外，还需持久访问 `/var/lib/sysbox/policy-evidence`。Delete 成功后保留 deleted 记录；同代 Apply 不再允许重新创建，必须使用更高 policy generation。deleting 阶段仅可重试删除，不能直接 Apply。该目录是资源侧执行证据，不能当缓存删除；容器部署时，各执行进程需要共享同一 evidence/lock 存储。本轮未提供跨宿主迁移或 namespace 重建后的证据迁移协议。

实施补充（policy 资源侧代际）：`PolicyTarget` 新增必填 `policy` 身份，与目标 `identity` 同 owner/topology，但独立 resource UID/generation；旧缺失 policy 字段的记录拒绝。Apply 的同 generation 内容不可变，相同 digest 重试不重建规则；更改内容需要新的 policy generation。Docker/namespace Policy SPI 已接入托管入口，旧无 generation 表注释的表拒绝操作。宿主执行账户需要访问共享 `/run/sysbox/policy-locks`；该本机锁不是 LeaseEpoch 授权，也不保留删除 tombstone。

实施补充（2026-10-06）：`PolicyTarget.Identity` 提升为必填类型字段，与 `Resource` 和 provider 私有 `State` 分开。`policy_target_state` 保存完整 envelope `{resource, identity, state}`，由共享 Encode/Decode 校验；旧仅保存 container_id/bindings/identity 的扁平结构拒绝读取。Docker 的私有 State 不再承担 identity 的协议来源；runtime 的 firewall/router Read/Delete/Recover/Cleanup 均通过新契约。namespace provider 目前只校验身份完整性，真实目标 ownership 核验仍未完成。

Policy Apply 改为一次 nft batch 完成建表/删表/重建。失败 batch 不提交，旧规则保持；成功重建仍会重置计数器，不提供同代并发请求的 CAS 或执行租约。

本次重构对使用者的承诺是：

1. 核心 HCL、CLI 命令和 HTTP 路由尽量保持；
2. 新增状态和诊断采用可扩展字段，不隐藏结果不明；
3. Agent 协议、state/database/manifest 明确换代并有重建流程；
4. substrate 仍按 capability SPI 接入；
5. 所有破坏性变化都有版本、启动检查、迁移步骤和回归测试；
6. 运行时行为从“猜上次做到哪一步”升级为“观察真实状态后继续收敛”。
