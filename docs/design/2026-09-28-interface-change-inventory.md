# Sysbox 重构接口变更清单

> 日期：2026-09-28
> 关联设计：[观察驱动的整体重构方案](2026-09-28-observation-driven-runtime-refactor.md)
> 状态：目标接口基线；实施前必须逐项确认。实际代码与迁移缺口见[实施状态](2026-09-28-runtime-refactor-implementation-status.md)。文中的 v2、新枚举、对象型错误和候选 CLI 参数不是已经交付的承诺；本阶段 HTTP `error` 仍为字符串。

本文回答一个问题：重构后，哪些接口必须保持，哪些接口可以扩展，哪些接口明确换代。

### SSH宿主完成证据

`POST /v1/hosts/{agent}/node-operations/{operation}/completed`现在区分SSH与adoption。
SSH apply/delete回执必须签署`evidence_digest`，结果包含稳定`access_id`；apply还需
与请求规范密钥内容一致的`sha256`，delete需`deleted=true`。验证持久grant后，先在
command/operation事务中记录完成，再释放授权。generic stopped接口不接受SSH操作。
过期或退休command不丢弃已验证的历史宿主结果，相同回执可重试。

SSH supervisor冷恢复从原journal读取完整结果并重报同一签名回执。旧无结果摘要的
SSH回执及无access_id证据拒绝，不自动改签；旧操作需明确处置后重建。此完成链路
不证明guest安装授权、sshd生效或密钥轮换已完成真实验收。

普通Agent node-operation completion接口现在拒绝SSH apply/delete的done和failed
报告，即使worker租约仍有效。Agent不再发送这类重复终态，只有签名宿主结果可完成
canonical操作；宿主调用失败只形成command进度/错误，不能把未知资源结果宣称为终态。

例外仅限可证明没有执行授权的失败：command已持久退休且完整grant/stop历史均无该
command时，API可把匹配SSH操作收敛为failed。事件处理及supervisor冷扫描共用此规则。
一旦有过grant（包括过期或已停止的历史grant），仍只接受宿主完成证据，不凭worker
错误猜测资源结果，也不自动重放。

### SSH配置身份与操作身份分离

`SSHAccessRequest`新增必填`access_id`：同一节点上一份受管配置的稳定身份。
原`id`继续表示本次操作，绑定command/grant/回执；轮换或删除使用新的操作ID、相同
access ID。两字段都进入签名摘要，dispatch后均不可变。宿主Apply/Delete文件路径
从access ID派生，runtime HCL的access ID来自资源名称。

缺失access ID的旧请求拒绝，不从操作ID补默认值；旧持久SSH请求需按新合同重新受理。
这修复“新操作无法定位旧配置”的建模问题，不表示共享sshd、安装grant、内容CAS删除
及实际轮换验收已全部完成。

### 已安装文件的实时观察SPI

新增`ConnectionInstalledFileInspector.InspectInstalledFile(handle)`，通过guest
`file-inspect`返回present/drifted/absent或unknown错误；绑定原安装handle，不能传任意
文件路径。SSH/vsock/Docker接入同一协议。旧`ObserveFileInstall`仍表示历史安装结果，
不用于推断目标文件的当前状态。缺少新操作的旧helper返回错误，不回退到shell读取。
SSH access保存安装handle并消费此SPI的接线仍未完成。

host-supervisor的Unix `file-inspect`也已接入：请求携带原FileInstallHandle及canonical
TargetRef，宿主从已有signed安装记录重建provider handle并在读取前后核对资源身份。
响应回显完整安装handle；客户端拒绝错配。只查询当前文件，不上报/改变历史停止回执。

### SSH公钥输入合同收紧

`SSHAccessRequest`校验真实SSH公钥编码及类型，拒绝无效base64/wire数据、多行和
authorized_keys选项（包括`command=`）；单行上限16KiB。此前仅两个词即可通过的
伪公钥不再接受。宿主与runtime使用统一排序后的密钥文件内容，与签名摘要的排序
保持一致；不修改调用方数组。共享sshd配置与完整访问生命周期仍单独实施。

### 当前HTTP snapshot恢复流程

旧的空body直接恢复接口已移除。先调用
`POST /v1/topologies/{topology}/state/snapshots/{snapshot}/restore-plan`，携带非空
`Idempotency-Key`作为请求ID；响应是完整`SnapshotRestoreRequest`。保存此请求，原样
提交至同路径的`/restore`。该接口返回200 `restored`及持久回执，或202等待状态及绑定
epoch的请求。等待期间重提原草案或返回的绑定请求继续处理；没有后台自动轮询器。

恢复流程做只读预检、建立持久屏障、等待活动记录/停止证明、授权事务提交、释放屏障。
已提交请求重试直接确认原回执，不要求后继工作再次静止。提交成功但释放失败返回
202 `committed_waiting_for_release`且保留receipt，重试只需完成释放。

`POST .../{snapshot}/restore-cancel`接收同一请求。只允许放弃未提交恢复，且旧授权
必须都已确认停止；不会凭租约到期解除屏障。若取消与提交竞争，返回实际获胜结果。
已提交恢复不能通过取消回滚；原请求再次提交完成其释放流程。未启动请求的取消不会
隐式创建屏障。取消后同一ID不能重新发起恢复，需新的计划ID。

这接通了现有inventory兼容的metadata restore流程，不表示不同资源集合reconcile、
所有旧writer隔离、自动后台维护调度、完整升级/跨宿主验收或整个P6已经完成。

后续已提交恢复可由API supervisor自动补做屏障释放：只消费已有receipt，重新匹配
持久barrier后解除，不重做restore。缺少receipt的等待请求仍需重试原计划或显式取消；
这不是所有维护操作的后台自动执行。该能力依赖部署启用supervisor扫描。

### 当前存储格式补充：snapshot字节载荷与事务适配

lifecycle aggregate当前为format4：snapshot的`data`保存base64字节载荷，防止Postgres
JSONB重排嵌入JSON后破坏原字节摘要。先前format3及更早格式明确拒绝，仍需停旧writer
并重建，不迁移现有VM数据。后文format3记录是此前恢复回执的实施节点。

恢复提交已接入文件/SQLite/Postgres事务适配：文件依次持有API记录锁、authority文件锁、
aggregate锁；SQLite同一写事务读取授权与aggregate；Postgres按admission、authority、
aggregate顺序取锁。三者均复用共享恢复转换并原子保存state/receipt，不在事务中释放
屏障。公开restore受理、完整活动冻结和提交后的释放编排仍待接入。

### 2026-10-09 snapshot恢复提交回执基础

lifecycle aggregate升为format3，新增不可变恢复回执；format2明确拒绝，必须在停止
旧writer、处置原资源与证据后重建，不支持手改version。现有双VM旧数据未自动迁移。

repository新增 `PlanSnapshotRestore` / `CommitSnapshotRestore`。请求绑定ID、
topology UID、snapshot ID/摘要、预期serial及maintenance epoch。首次提交将恢复后的
state与回执在同一aggregate事务内保存；重复请求读取原回执，不重复推进serial。
snapshot已删除或后继状态已变化时，相同请求仍只返回原提交证据；同ID不同内容拒绝。
snapshot内容不嵌套保存历史恢复回执，回执保留在当前aggregate，直到拓扑元数据重建。

这些是待接入维护编排的repository primitive，不是已经交付的新HTTP契约。
maintenance epoch在此只绑定请求，不替代授权校验；调用方必须在提交期间保持匹配
的维护屏障。公开restore受理、授权事务、崩溃续作及释放流程仍未完成。

恢复预检已与提交共用同一身份/schema/资源集合/未完成journal校验。`PlanSnapshotRestore`
允许epoch0形成只读草案，其`Digest()`绑定全部不可变字段但不含后分配的epoch；
编排可先验证草案，再用摘要申请维护屏障，填入epoch后提交。epoch0提交明确拒绝。
已有回执的草案重试返回原请求，不受snapshot保留删除或后继state变化影响。

新增共享转换`ApplyAuthorizedSnapshotRestore`供后端事务使用：同时校验authority
与aggregate快照，要求维护屏障ID/topology UID/请求摘要/epoch完全匹配；首次写入还
要求屏障未释放、所有旧grant已停止且具备回执。已释放屏障只允许确认原先同一恢复
回执，不能授权新写入。调用适配器必须把两个存储锁保持到提交，独立读取后调用此
纯函数不构成原子授权。公开restore事务适配与HTTP编排仍待完成。

### 2026-10-09 维护授权屏障基础

执行授权 ledger 升为format2，format1拒绝读写并要求明确重建；没有自动迁移或忽略
新字段的兼容模式。原因是旧writer若不认识维护屏障，可能在写回时丢弃冻结状态。
现有部署必须先停止旧writer、核实并保留停止证据，再执行独立的重建流程；禁止仅
手改version。当前双VM实验室仍保留原format1数据，未用本次变更自动覆盖或删除。

repository新增 `BeginMaintenance(id, topologyUID, requestDigest)` 与
`EndMaintenance(barrier)`。Begin在同一授权事务内分配epoch、保存不可变请求身份、
冻结新grant并撤销该拓扑所有父/子grant。重复ID只读回原记录，摘要冲突拒绝。
End必须匹配原屏障，且该拓扑所有grant都有已确认停止状态；过期不等于停止。
已释放记录保留为幂等墓碑，旧End不能解开后继屏障；屏障不靠超时自动解除。

这不是新增公开HTTP restore流程：维护请求受理、snapshot提交、重启续作及屏障
释放仍待编排接入。屏障只限制执行授权，不声称已经冻结全部metadata写入入口。

### 2026-10-09 已实施：远端 Run Observe/Recover 宿主路由

远端 `run_recovery` command 不再由 Agent 打开 repository 或直接调用 provider。
Agent 必须配置 `host_supervisor_socket`，仅经 Unix socket 的 `run-recovery` 操作
传递 `command_id`、`lease_owner`、`attempt`；不接受 worker 指定的 Target、Run、
revision 或 provider 坐标。缺宿主入口直接失败，没有旧本地路径回退。

宿主使用自身 Agent 身份向现有 `recovery-authorize` HTTP 接口重新授权，取得
canonical Run、mode 和不可变 revision。响应新增必需的 `lease_until`；缺失、过期
或 owner 不符时宿主拒绝。API 与 supervisor 应协调升级，不为旧响应补默认期限。
宿主复用观察驱动引擎，结束时再次确认 command/Run 授权，再经 worker 回传原
`RunRecoveryReport`；报告仍不表示 Run 完成，也不转移执行 ownership。

输入解码保持30秒预算；具体恢复按 API 返回的 command 租约限时，响应写入另限
30秒。recovery 授权响应最多16MiB，覆盖8MiB revision 上传在 base64 编码后的体积；
普通宿主授权响应仍限1MiB。自动声明 `run-recovery.v1` 能力以配置宿主 socket 为前提。

Recover 仅提交既有 journal 的可证明结果，不重放 Create/Delete/guest command。
后续已补单次 journal outcome 的原子授权边界：host bridge 必须提供绑定 command
租约与Run身份的 RecoveryActionRepository。每次提交在文件共同锁或数据库同一事务
中检查 command 未退休、owner/attempt/期限、Agent 未禁用且保有能力、原Run仍可恢复、
同拓扑无活动Run，然后复用共享CAS状态转换。缺少该能力的bridge拒绝recover写入；
observe不受影响。该边界不等于全局maintenance freeze或daemon quiescence。

### 2026-10-07 已实施：guest task 宿主监督接口

API guest execution 必须配置 `agent.host_supervisor_socket`；缺失时拒绝执行，
不再回退到 Agent 直接连接 provider。新增 `sysbox host-supervisor --config
PATH --identity PATH`，独立的 `host_supervisor` 配置包含 socket、私有日志目录、
host 签名私钥和非 root worker group ID。部署约束见[持久任务](../reference/guest-tasks.md)。

| 接口或证明 | 当前行为 |
|---|---|
| grant 的 `target_boot_id` | guest task 必填、不可变；同一 grant 不能改签给另一个 guest boot |
| `POST /v1/agents/{agent}/execution-grants/inspect` | Agent HMAC 认证；按 scope 读取旧 session/command 的现存 grant，不产生新权限 |
| `POST /v1/agents/{agent}/execution-grants/authorize` | Agent HMAC 认证；校验签名 grant、当前 session、command attempt 和租约，不续租 |
| `POST /v1/hosts/{agent}/execution-grants/stopped` | 验证独立 host key 签名；guest task 回执绑定 guest boot、task ID/digest 和停止确认 |
| 旧 epoch 停止回执重报 | 相同回执幂等成功；不撤销已经发出的后继 epoch |
| Unix socket v1 | 仅传资源身份与签名任务；不接受 worker 提供 provider endpoint、私有状态或主机路径 |

这是 guest task 链路迁移，不代表普通 Run、文件安装和全部生命周期 effect 已迁入
宿主监督进程。旧 grant 缺失 boot 绑定不再兼容；停止旧 writer 后按当前格式重建。

后续节点电源迁移：远端 `node_operation` 的 pause/resume 同样强制使用 supervisor。
派发 command 必须包含 TopologyUID、ResourceUID 和 generation；缺 proof 不补默认值。
新增 `node_power` grant 绑定宿主 boot 和不可变 Topology/Node/Operation 摘要，Unix
协议新增 `host-info`、`power-apply`、`power-observe`。历史查询只读 effect ledger，
不重放 provider 调用。effect ledger 升为 2，旧格式明确重建。未实现的 import
直接返回 Unsupported，不再保留无法成功的伪派发路径。Docker 的生命周期 status
不再被健康检查覆盖，不健康通过独立 `healthy=false` 表达。

文件安装迁移：API `guest_file_put` 签发固定 `guest_file_install` grant，摘要不再
包含 FetchRef。远端 Agent 删除直接 CopyToNode 和终态完成回报；由宿主签名结果
驱动三后端共同终态规则。取消响应公开 `cancel_requested`，存在 grant 时不直接
标 cancelled；commit 抢先完成则保留 committed。新增 guest file install/observe/
cancel SPI 与 host file RPC，guest 须配置可信安装根；缺能力不回退旧复制路径。
API schema 10、文件 metadata format 2、guest scope format 2 需明确重建。

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

实施补充（2026-10-07 destroy receipt）：`RunCompletion.cleanup`携带owning Agent的原子空状态/journal证明；有效完成事务写入`Run.cleanup`，全量记录后端原样保存。证明绑定topology UID、正state serial、Agent、Run轮次、attempt/lease owner。普通metadata删除接受当前placement对应的最新Destroy回执；新操作/重绑失效，旧无回执终态不升级为证明。`runtime.CleanupProver`是可选原子仓储证明端口；leased Agent Destroy缺该能力时拒绝出具成功清理证明。

Agent pause/resume要求完整ActionID、目标UID/generation，构造handle前拒绝旧代或缺失证明；旧import命令在读取state前Unsupported。远端Run缺SnapshotPath不再读取当前workspace作为替代，须以固定revision重新受理。

实施补充（2026-10-07 通用控制面收口）：Apply 新增持久 `Idempotency-Key` 受理，保持执行轮次 command ID；GET Run 增加 `inputs_required`，claim503可重试但queued_at/revision必填，旧轮次409。keyed Apply 的 immutable snapshot 是执行/预检依据，不改 mutable workspace；任意 revision 标签拒绝。capabilities 使用 `sysbox-runtime-v1`，不绑定下游业务。cleanup-inspection 是 state+journal/操作证据查询，external_presence=unknown，不签发“空 state 即已清理”的回执。

内部 `agentexec.Bridge.Preflight` 接受 `*Run`，只能使用固定 SnapshotPath。SQLite schema 不自动补列，Run/command/event 缺完整记录要求重建。State snapshot/lease 操作不支持时HTTP501；SSHAccess Create/Delete明确拒绝，旧安装脚本与仅删state的假清理移除。

vsock `exec.v2` 新增 stdin_size + body（最多8MiB），`write_file.v2` 要求 sha256，terminal frame 返回匹配摘要；旧操作名不回退。SSH/vsock文件安装校验内容、临时文件同步、精确目标原子替换。后台 Start 仍不代表 durable task，Observe/Cancel可选SPI未实现，不能宣称跨重启恢复或远端终止。

`ready.v1` 新增 guest 初始化屏障；ping 不再作为副作用授权。systemd readiness 绑定 sysinit 与 tmpfiles 成功，自定义 init 绑定 boot ID marker。旧 guest-init 在升级时要求重建 VM，不提供 readiness 兼容回退。

实施补充（2026-10-07 attachment target）：`driver.AttachmentRequest` 新增必需的 `NetworkIdentity`，表示目标网络的 owner/topology/resource/generation，与 context 中节点操作身份分离。runtime 在新建与恢复时均从网络 resource 填入。缺失身份不兼容。网络 bridge 消费端只接受 v2 ownership anchor；libvirt `BridgeAttach.Target` 保存目标引用，旧裸 bridge 记录启动时要求重建。

Firecracker manifest/HandleState 升为 v2，新增每条 attachment 的逻辑名、guest device、网络 target、TAP proof；v1 显式拒绝，必须重建，不能手改 version。Firecracker/libvirt 不再根据 Docker network ID 或普通 bridge 名称猜测可接入的 root bridge；Docker NAT 跨 provider 接入返回 Unsupported，provider Validate 提前拒绝。Docker 原生 NAT 继续支持，Attach/Observe/Delete 均核对网络 ID 和独立网络身份标签。HCL 字段和 HTTP 路由未改，但上述 provider/network 组合的能力范围已收紧。

部署需同时更新 CLI/Agent/provider 二进制，并保留共享 `/var/lib/sysbox/networks` ownership/host lock 目录及一致的 host namespace 视图。先用旧版按精确身份清理旧 VM/网络，再重建 state 和 provider 工作目录；新版本不自动清空旧记录或删除外部资源。

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

实施补充（2026-10-09 GC）：内部 `hostsupervisor.Server.GCDelete(ctx,candidate,grant)`
改为 `GCEffect(ctx,candidate)`，由 RPC 层统一进入 coordinator，provider 回调不得
再次获取 gate。未配置完整引用校验的宿主新删除明确 fenced；历史查询/停止回执
恢复保留。repository 新增 `LoadGCReferences(ctx,topology)`，返回单事务 lifecycle
引用清单与摘要，不能作为独立删除权限。CLI GC 移除 standalone state.json 兼容
读取，旧记录必须明确重建，旧 GC inventory digest 需要重新生成。

实施补充（2026-10-09 metadata Delete）：HTTP 路由不变，`force=true` 不再绕过
未确认停止的执行授权或未释放的维护 barrier。失败/取消的操作状态、租约过期均
不能替代 host stop receipt。清理诊断 `active_operations`/`active_operation_kinds`
新增 `execution_grant`，含 action ID、Agent、scope、epoch 和授权状态；可出现
`cleanup_receipt_status=blocked_by_execution_authority`。文件/SQLite/Postgres
删除统一核验授权与 lifecycle UID/serial；缺字段授权记录按损坏数据拒绝，不做兼容补齐。

实施补充（2026-10-09 grant lifecycle 绑定）：授权 HTTP 路由和载荷不变，签发、
续租、host authorize、派生 guest grant 和 GC 签发要求持久化 command 中的 topology
UID 与当前 lifecycle 一致；metadata 删除后旧 delivery lease 不再获得新 epoch。
校验与 authority 写入使用同一锁边界。停止回执和 session 操作不依赖存活 lifecycle，
以保留历史证据恢复能力。

实施补充（2026-10-09 GC grant）：`inventory_digest` 必须匹配当前 lifecycle
引用快照，任意非空字符串不再有效。当前/保留 state、journal candidate 或 snapshot
引用的 resource UID 或同 provider external ID 会导致签发/续租/authorize 冲突。
载荷格式不变；旧摘要需重新计算。该字段仍不是完整 GC plan 或删除许可。

实施补充（2026-10-09 CLI GC）：删除 `gc --execute`，传入时按未知 flag 拒绝；
没有旧执行模式兼容层。当前仅支持 `gc --dry-run` 的只读候选预览，省略该选项
仍明确拒绝。旧 `_gc/deletion-journal.json` 保留为诊断证据，不读取或重放其中的
删除意图。完整自动 GC 的 API/host 授权派发尚未完成。

实施补充（2026-10-09 maintenance dispatch）：guest execution/file/node-operation
dispatch 的 HTTP 接口未变，但提交要求 command 携带 topology UID，并受未释放
maintenance barrier 拒绝。文件/SQLite/Postgres 保持 operation 与 command 原子
发布；冻结拒绝不会留下半条记录。Run dispatch 和其他 writer 尚未纳入同一 barrier。

实施补充（2026-10-09 Run maintenance dispatch）：Run 受理 HTTP 接口不变，新的
受理和 assignment outbox 需要当前 topology UID，未释放 maintenance barrier 时
返回冲突并不产生 run/request/command。精确已提交请求仍可幂等读取；Run Save/Restart
等其他内部 writer 尚未统一到该 barrier。

实施补充（2026-10-09 cleanup 活动诊断）：`cleanup-inspection` 的
`active_operation_kinds` 新增 `console_session` 和 `agent_command`；未解决的
console 和当前 topology UID 的非终态 command 出现在 `active_operations`。
既有 receipt 可标记 `blocked_by_console_activity` 或 `blocked_by_agent_command`。
不返回 command 参数/环境变量、console 内部错误或审计信息；来源读取失败返回
503。该响应仍是分开读取的证据快照，不构成删除授权。

实施补充（2026-10-09 console cancellation）：取消请求返回 `cancelling`，不再
提前写 `cancelled`/EndedAt；客户端须等待执行结果。websocket 断开和 attach 超时
产生 `lost`，不代表远端已停止。存储失败不确认取消。恢复、维护和清理会把这两种
状态作为未解决证据；远端 signed-stop 协议尚未完成。

实施补充（2026-10-09 cancel 派发错误）：`POST /v1/sessions/{id}/cancel` 在存储
或命令发布失败时返回脱敏503，提示重试同一 session；仅真实不存在返回404。
已提交的 cancelling 意图不会因派发失败被回滚为已完成或已停止。

实施补充（2026-10-09 console create）：内部 hub Create 改为带 context 和显式
error 返回；HTTP 创建路由不变，持久化失败返回脱敏503，不返回会话 ID，也不
派发 session_open。会话保存与 outbox 的原子受理尚未完成。

实施补充（2026-10-09 console completion replay）：已退休 session_open 仅接受
与持久终态及原派发证明一致的重复回报；不恢复命令投递。Supervisor 可用该结果
修复未完成的 session 记录，直接回报也经过同一身份检查。恢复时间/错误来自原
command，不采信重试的新时间或文本；这不替代 host 签名停止证明。

实施补充（2026-10-09 console cancel command）：取消命令 ID 改为 session 不可变
身份摘要，重复请求读取同一 outbox 记录并保留现有投递租约。已退休记录不会被
重新排队；取消 command 的 Session 载荷包含 topology/node/Agent/创建时间。
这不改变取消 HTTP 路由，也不代表 session 意图与 command 已原子提交。

实施补充（2026-10-09 cancel 恢复）：API 重启保留 cancelling；supervisor 自动
补齐缺失的取消 outbox command。lost 仍是结果未知，不被自动解释为取消意图。
退休取消命令不重建，cancelling 仍需会话执行终态/停止证据才能结束。

实施补充（2026-10-09 console 完整记录）：API SQL schema 10 把 SQLite console
表替换为 `id/data` 完整 JSON 记录，保留 ProjectID/Workspace/StartedAt/Audit 的更新。
SQLite 列表按 Workspace 过滤（空 Workspace 时才回退 Topology）。SQLite/Postgres
共享 API schema 版本门槛，schema 9 必须显式重建；没有就地补列或兼容读取。
本次不修改文件存储记录，不代表 console CAS 已完成。

实施补充（2026-10-09 console CAS）：内部 `consoleStore` 增加
`CompareAndSwapConsoleSession(ctx, old, next)`；`SaveConsoleSession` 变为首次创建/
完整相同重试，拒绝不同内容覆盖。三个后端统一用 `ErrConflict` 表示 CAS 旧记录
不匹配或不存在。身份字段（含创建时间的时区表示）不可变。Hub 冲突后读取并核对
当前身份、刷新缓存，但不自动重放更新函数；调用者重试必须重新判断当前状态。
HTTP 路由、记录格式和 API schema 10 不变；没有保留旧的无条件覆盖写入入口。

实施补充（2026-10-09 console maintenance）：内部创建与 CAS 统一经
`persistence.ConsoleRepository` 和持锁 `ConsoleSessionStorage` 执行，冻结 topology
后的新会话/重新激活写入返回 `ErrConflict`；精确重试和已有任务取消/完成可继续。
共享编码器校验 ID、Topology、Workspace 的路径安全，三后端统一拒绝非法记录。
存储 schema 和 HTTP 路由不变，完成证据判断统一供 maintenance/cleanup/recovery 使用。

实施补充（2026-10-09 console 原子初次受理）：内部 consoleStore 增加
`CreateConsoleDispatch(ctx, session, command)`，共享 `ConsoleRepository.Admit`
负责新会话/初始命令的绑定检查；已有 ID 冲突不会覆盖或重新排队。
HTTP 正常创建的202以完整会话、审计和 outbox 提交为准，唤醒通知失败不再返回
“创建失败”。拒绝/节点不可用只保存终态会话审计。AgentService.PublishCommand
禁止单独发布 session_open；取消接口仍使用已有可恢复意图协议。
文件 session 可携内部 dispatch_command 初始记录，CAS 必须保留；命令状态记录/
退休 tombstone 覆盖该 seed。SQL 表结构与 API schema10 不变，无旧发布路径回退。
请求体的 requested_by/roles 不再用于授权或审计身份；它们由服务端请求主体覆盖。
HTTP idempotency key 受理仍未实现，本变更不承诺提交回包丢失后的 HTTP 重试去重。

实施补充（2026-10-09 console 原子取消）：内部 consoleStore 新增
`CancelConsoleSession(ctx, old, next)`，同时提交 cancelling/audit 与稳定取消 outbox。
hub Cancel 增加 context，HTTP/timeout 统一调用。ConsoleService 只接收 WakeCommand
通知依赖，不再独立发布命令；AgentService 拒绝 Session-bound cancel_command。
HTTP 取消事务失败仍脱敏503，不存在仍404；提交成功后即使通知失败也返回200和
cancelling，依靠持久 outbox 投递。退休/冲突取消命令不重新入队。
文件内部新增 cancel_dispatch_command seed，与 opening seed 一起保留；SQL仍用
同一 session/command 表事务，schema10 不变。200 不代表远端已确认停止。

实施补充（2026-10-09 console 访问授权）：会话 GET、取消和浏览器 attach 现在
与创建一样检查当前 `api.console.allowed_roles`/管理员角色；仅知道 ID 不再足以
绕过配置的 Console 权限。拒绝返回403，未修改会话/审计/outbox；registry 故障
统一返回脱敏503，不再伪装404。取消审计保存当前操作者及其角色。Agent attach
沿用单独的 Agent 身份和会话归属检查。现有身份 header 的信任边界没有改变。

实施补充（2026-10-09 metadata 删除）：内部提交方法在持锁事务内重新检查 Run/
Console，不能用过期的预检结果替代。未解决活动返回 ErrConflict，损坏 SQLite
Run索引/JSON返回 ErrCorruptRecord，lifecycle UID/serial保持原值。强制删除也不
绕过这项检查。Run未知状态、缺失时间或倒序时间视为未解决，不能作为安全终态。
路由/schema不变；command/guest/node/adoption/GC 的完整提交边界仍未完成。

实施补充（2026-10-09 command 删除门槛）：metadata 删除提交内检查所有完整命令
记录，未解决投递也阻挡 force 删除，即使对应 effect 已有停止证明。缺 UID 的
payload topology 仅作保守阻挡；明确其他 UID、退休和终态沿用现有语义。
Postgres 原始命令写入锁序统一为 authority→command。路由和数据格式未变；
“发布与删除串行化”不代表 raw command 已完成所有维护准入与执行授权检查。

实施补充（2026-10-09 command 维护准入）：原始 SaveAgentCommand、领取租约和
PrepareAgentCommandDelivery 现在在同一存储事务内核对维护屏障，受阻返回
ErrConflict 且不发布命令/租约/派发状态。完成和未扩展租约的执行回报仍可提交；
取消、退休、inventory、run_recovery observe 保留，recover 不豁免。名称只用于
定位冻结范围，不补 UID。Wire/schema 不变；签发执行 grant 仍有独立验证。

实施补充（2026-10-09 node registry）：原始 NodeOperation Create/CAS 在维护
屏障内提交，冻结或未知归属冲突返回 ErrConflict，已有终态回报可收敛。
metadata 删除在提交事务内重查 node 记录，有效结束时间才作为终态证据。
内部文件方法改名为 CreateFilesystemDispatchUnderAuthority，要求调用方持
records/authority，旧名称不保留。SQL repository 继续借用完整 API metadata
schema/pool。公开路由和持久格式不变，此项不替代宿主执行/停止证明。

实施补充（2026-10-09 guest registry）：原始 guest execution/file Save/CAS 接入
共享维护事务；新增记录和活跃状态更新可能返回 ErrConflict，取消意图及终态可
按规则收敛。内部文件方法改为 CreateFilesystemGuestDispatchUnderAuthority，
旧名称移除。GuestActivity.Unresolved 统一检查状态及完整、顺序正确的时间，
DecodeGuestActivity 验证完整记录但不暴露 request/output。SQL借用的 schema 需
包含 execution authority 表。API路由与持久格式未变，不表示取消已获得停止证明。

实施补充（2026-10-09 Agent cancel 事件）：cancel_command 的 completed 仅表示
已向目标执行 context 传达取消请求，message 为 cancellation requested；不再
额外伪造目标命令 cancelled 事件。目标执行路径独立报告结果。Guest 完成回报
使用 RequestTimeout（默认30秒）的独立预算，不随执行 context 一同取消。
Wire/schema 不变；该 completed 不能作为远端停止证明，也不保证回报持久重试。

实施补充（2026-10-10 execution cancellation）：GET/cancel 返回新增
cancel_requested；请求取消后不再立即返回 cancelled，而保留 queued/running。
实际完成可保留 completed 及结果。取消阻止新的 grant issue/authorize/renew，
拒绝不会延长命令租约；stop 接口仍可收敛。稳定取消 outbox 可重启修复，但取消
意图/outbox 尚未原子化，不能据此判断远端已停。API SQL schema 10→11、execution
record_format 1→2，明确重建，无旧格式兼容；file/aggregate/authority 格式不变。
