# Sysbox 生命周期重构：实施状态与验证边界

> 日期：2026-09-28
> 更新：2026-10-04
> 工作分支：`refactor/observation-driven-runtime`
> 核心实现提交：`72e227a`；未合入 main，未推送远端。
> Guest metadata 共享仓储提交：`3679f21`。
> Run 写入、租约与轮次隔离提交：`d581b80`；独立复审及全仓 unit/race/vet/docs 检查通过。
> 本文记录实际代码；[目标架构](2026-09-28-observation-driven-runtime-refactor.md)与[接口清单](2026-09-28-interface-change-inventory.md)包含尚未交付的后续设计。

## 1. 当前落地的主线

```text
CLI / Agent / 本机恢复请求
        ↓
固定配置与计划 → 观察并核对身份 → 写入 action intent
        ↓
Provider 执行 → 保存恢复证据 → 再观察验证
        ↓
同一 repository 事务提交 action outcome + resource state
```

`unknown` 不等于不存在；进程退出、连接断开或数据库提交失败不等于外部动作没有发生。未完成动作保留在 journal 中，不能通过新建空 state、盲重放 provisioner 或清理 checkpoint 绕过。

| 模块 | 已实现 | 尚不能据此声称完成 |
|---|---|---|
| 生命周期 repository | 文件、SQLite、Postgres 共用 aggregate 编解码、身份校验、意图/结果转换与 CAS | 全部 API metadata repository、outbox、跨控制器 fencing 已统一 |
| Guest metadata repository | GuestExecution 与 GuestFileOperation 共用版本化 codec、CAS、状态约束、私有绑定和 acceptance 汇总；三种后端仅做物理读写 | Run、Agent、命令队列等其余 metadata 已迁移；Postgres 已通过真实服务测试 |
| Run repository | Save/Restart/Claim/Renew/Expire 共用原子状态规则；完整 SQLite 快照；执行轮次和 Inputs 隔离 | 全部读取与 dispatch 适配、Agent 仓储、完整 outbox 和资源侧 fencing 已完成 |
| Command repository | 三种后端共用载荷绑定、租约、终态与退休规则；发送前原子核对授权；文件派发 ID 防冲突 | 完整 operation+command outbox、资源侧 fencing 或带租约证明的事件协议已完成 |
| Agent repository | 三种后端共用注册、心跳、策略和离线判定；锁内合并；存储错误不回退缓存授权 | inventory、projection、命令事件已统一，或已经有完整身份轮换协议 |

Command event repository 已开始统一 codec：事件 ID、AgentID、Type、Status、时间严格校验；filesystem 使用 JSONL codec，SQLite 保留完整 `record_json`（包括 recovery/fencing 字段），Postgres 使用完整 JSONB。旧 SQLite 索引列仍可读取，但缺失完整快照的旧事件只能按兼容字段读取；真实事件重新写入后即拥有完整 proof。

Projection codec 也已开始统一：filesystem、SQLite、Postgres 的 ResourceProjection 读取都经过共享 topology/时间校验和安全解码，避免后端把畸形 projection 当成空结果。AgentInventory codec 已接入三后端：AgentID 必须存在，ObservedAt 规范化，损坏 JSON/无身份记录统一拒绝；filesystem/Postgres 保存完整 JSON，SQLite 继续维护索引列并在读取时执行共享身份校验。inventory 仍不是完整 CAS repository，策略/观测并发版本控制尚待下一步。

NodeOperation 完成保存已接入三后端 CAS：filesystem 在共享记录锁内比较完整快照，SQLite 使用事务内读取/比较/更新，Postgres 使用行锁事务；共享 transition codec 拒绝跨身份、跨资源 generation、非法状态回退和终态结果覆盖。持久化错误会传播到 Agent/API，而不是返回虚假成功。

Provider generation fencing 第一阶段已接入 substrate observation：NodeObservation 返回 owner UID、resource UID、generation；Docker managed Observe 会核对 operation identity 和容器 labels，generation 不匹配返回 unknown/conflict。Firecracker manifest 已有等价 process/manifest generation 校验。真实 Docker daemon/libvirt/Firecracker 远程宿主机 E2E 仍未完成，当前仅有 provider fixture/合同验证。

### Resource fencing 迁移进度

Agent command/event 模型增加可选的 `action_id`、`resource_uid`、`generation`、`lease_epoch`、`lease_owner`、`attempt` 证明字段。命令已绑定 fencing 身份时，事件必须完整匹配其证明；无 fencing 身份的 legacy 命令仍兼容无证明事件。校验、状态推进与终态退休通过共享 CommandRepository 在同一存储锁/事务内完成；拒绝或提交失败不发布事件缓存或更新 console/Guest 派生状态。Agent ack、执行事件与目标取消事件均回传对应命令的证明，SQLite 保留完整快照中的 fencing 字段。

此处是命令事件层的原子防陈旧写入，不是完整资源侧 fencing：命令派发尚未全面绑定实际资源 generation，Run/NodeOperation/Guest 完成入口与 provider 外部副作用仍需迁移。跨聚合派生状态更新也未成为一个原子事务；真实 Postgres 16 的本仓合同已验收，生产部署未验收。

Run 完成入口进一步迁移到共享 `RunRepository.CompleteRun`：三后端在同一存储锁/事务内检查 owner、attempt、可用执行轮次、绑定、lease/deadline 与 projection 归属，再合并结果。过期 Agent 不能用 failed/cancelled 绕过租约授权；并发续租的截止时间不会被旧完成快照覆盖。首次提交成功后才更新缓存，终态相同重试仅返回已存结果，不再次发布 projection；不同终态或断言结果冲突被拒绝。该步骤使用已有 agent.v1 Run 证明，不引入新的必填 wire 字段；NodeOperation/Guest 完成回报和 provider 资源 generation 的完整 fencing 仍未完成。

Guest 完成回报现在接受同一组可选 fencing 证明；若 operation 绑定的 command 仍存在且已带 fencing，HTTP completion 必须匹配 command 的 action/resource/generation/epoch/owner/attempt。旧 command 或旧 Agent v1 回报缺少证明时保持兼容。Agent executor 会回传执行 command 的证明字段；GuestExecution 和 GuestFileOperation 的完成入口在状态 CAS 前拒绝明显的旧证明。NodeOperation 完成回报和 provider 真实资源 generation 已接入；provider 真实外部资源的 generation 全链路验收仍待完成。

NodeOperation 完成回报已保存 command proof，并在绑定 operation 的情况下核对 action/resource/generation/epoch/owner/attempt；生命周期/import 派发会绑定 operation action ID、resource UID 和 generation 到 command，再把 command identity 回写 operation。旧无 proof operation 继续兼容，带不完整或错误 proof 的回报返回冲突。Supervisor/AgentService 会按 durable AgentID 修复 assigned Run 的缺失 command，running Run 不会因为控制器没有本地进程锚点而被重派；这提供 owning-agent 恢复的第一步。provider 返回的真实资源 generation 与外部资源 fencing 尚未统一接入。
| Runtime | 默认 Apply/Destroy/Reset 走观察、授权、写前意图、验证、原子提交；恢复只提交可证明结果 | 每种资源都已有完整的 discovery/read-back；所有 interrupted reset 都能自动继续 |
| Firecracker | 版本化 manifest、身份/代际/进程锚点、文件锁、冷恢复、pidfd 校验后发信号；reset 保留真实目标 ActionID | 已通过真实 KVM、宿主机重启或内核版本矩阵验证 |
| Docker / libvirt / isolated network | 管理身份、发现、拥有者约束；reset 的旧/新代际校验；隔离网络的观测与 ownership 标记 | 已通过真实 Docker/libvirt/网络设备 E2E |
| 执行 SPI | `Node.Connection` 的前台 Exec 为共同入口，Start/Copy/Observe/Cancel 为独立可选接口；限制输出、区分未知退出码 | 每个 transport 都有持久后台任务、可验证的远端取消或文件摘要校验 |

## 2. Repository 的边界

Runtime 定义 `ActionRepository` 端口，`pkg/persistence` 实现它。Provider 不读取数据库；数据库适配器不执行 provider 动作。

- 文件后端：一个 topology 一个聚合文件，稳定 flock 文件、临时文件写入、fsync 和原子 rename。
- SQLite：`BEGIN IMMEDIATE` 下更新聚合。Postgres：事务和行锁下更新同一聚合。
- `StateSerial` 记录最初授权的 state 版本；`ExpectedSerial` 用于当前结果提交；`Revision` 单独保护 action 阶段更新。只有 state CAS 不足以防止旧阶段覆盖新恢复证据。
- terminal outcome 的相同重试可幂等返回；不同结果、旧 revision、阶段回退、丢失 candidate/完成证据都会被拒绝。
- 解码不仅检查 JSON：还检查 owner、resource UID、generation、action key/ID、status、revision 和 candidate 身份。
- SQL 的 BEGIN 或 ROLLBACK 结果不确定时丢弃连接，不把未知事务状态放回连接池。
- CLI、Agent 和 API 共用地址解析。`sqlite://local.db` 是持久文件，不是临时数据库；空路径与 `:memory:` 不接受。
- 固定 `?topology=lab` 不能把另一个 topology 请求重定向到 lab；目录枚举不再伪造 `__list__`。共享库的 `?topology={topology}` 可枚举，存储路径本身含 topology 模板时需要另外的目录索引，当前明确报错。
- 第一次 create 只有 intent、尚无成功资源时，目录列表仍能看到它。
- 删除 metadata 使用 owner + serial CAS，拒绝未完成动作；保留 repository 锁文件和新 owner 身份，避免 unlink 锁导致双写。

GuestExecution 和 GuestFileOperation 已迁移到 `GuestRepository`，Run 写入与租约转移已迁移到 `RunRepository`，命令状态规则已迁移到 `CommandRepository`，Agent 注册与存活策略已迁移到 `AgentRepository`。inventory、projection、命令事件和部分 Run 读取/dispatch 适配仍在既有 store 中。沿用原有表和路径；与生命周期 aggregate 之间仍无跨域原子事务，不能声称三套存储已全部合并。

### Agent 的实际规则

- 心跳仅刷新名称、能力、标签、版本和服务端接收时间；省略的上报字段保留旧值，不能改写禁用、隔离、原因、凭据或创建时间。
- 策略更新在原子事务内只修改显式提供的管理字段；离线扫描在锁内重读当前心跳，不用扫描旧值覆盖新心跳。离线节点的无关策略更新不会直接变为在线。
- 重新注册保留已有策略与创建时间。受保护身份必须提供当前实际 secret；省略 secret 或只提供公开 hash 会返回 409。修改已有凭据也返回 409；凭据轮换流程尚未实现。首次注册与无凭据 legacy 身份仍沿用现有准入方式。
- 文件后端用稳定跨进程锁与原子写入；SQLite 用 BEGIN IMMEDIATE；Postgres 用行锁和受主键约束的创建事务。并发新建冲突返回 Conflict，不以未经重读的快照覆盖。
- 存储错误、损坏和不存在保持区分。Get/List 不再回退 registry；空列表不能复活缓存 Agent。调度与 guest 路由传播存储错误，HTTP 保持字符串 error：缺失 404、冲突 409、损坏/不可用 500。
- SQLite 更新完整元数据，严格解析 JSON、时间和布尔值；Postgres 核对 JSON 与身份/策略/凭据索引列。缺失 status/protocol 的旧或损坏记录不会被自动补成可调度身份。
- 内部 Save 保留显式管理替换能力，不是任意调用者的 CAS 接口；运行中的注册、心跳、策略和离线更新均使用各自原子操作。registry 只作缓存，不能据其内容授权。

本检查点未改变 HTTP v1 DTO 或 agent.v1 协议，也没有删除现有元数据或真实资源。旧 API writer 必须停用后升级；完整 outbox、资源侧 fencing、inventory/projection 迁移以及真实 Postgres 验收仍未完成。

### Durable outbox 迁移进度

恢复路径补强：filesystem command Get/Claim 现在可直接读取 Guest seed，不依赖此前 PublishCommand；已更新命令与退休记录优先于初始 seed；Guest CAS 保留 seed；Guest enqueue 与其他命令写入共用全局锁并检查命令 ID 冲突。文件上传提交后的通知只唤醒 registry，不再二次保存命令或因通知失败删除载荷。新增无通知领取/退休与 CAS 后恢复回归；Run 原子派发尚未完成。

已先落地 Guest operation 的共享 dispatch seam：命令 ID 在 durable enqueue 前生成，service 支持由后端一次提交 operation 与 command，提交成功后通知 Agent 失败不会回滚 queued operation。Guest dispatch 编码会核对 operation、agent、command type、请求载荷和初始状态，并提供文件 seed 的编码、校验和恢复接口。

filesystem、SQLite 与 Postgres 已接通 GuestExecution/GuestFileOperation 的 operation+command 同事务入队；SQLite 使用 `BEGIN IMMEDIATE`，Postgres 使用同一事务，文件后端使用稳定记录锁。命令列表重启后可恢复，通知失败不回滚 durable queued 状态。Supervisor 会周期性从 durable command store 重建连接 Agent 的唤醒提示，控制进程重启不会丢失 queued intent；通知失败使用指数退避（1–64 秒），失败次数、下次重试时间和最后错误已持久到 filesystem/SQLite/Postgres retry metadata，跨控制器共享这些后端时退避窗口也能延续。普通 Scheduler Run 派发也已改为 `CreateRunDispatch`，assignment 与 `run_assigned` command 先 durable 提交，再发送内存通知；已有 assigned Run 的 command 缺失时，local/SQLite/Postgres 会在事务内补挂 command；legacy idempotent destroy 继续使用 RequestOnly mapping。真实 Postgres 16 合同已验收，完整跨控制器退避持久化仍未完成。

Agent 检查点通过独立复审及全仓无缓存 unit/race、vet、docs、diff 检查，覆盖跨进程锁、并发策略/心跳、重启、损坏记录、提交失败及注册身份旁路。全仓 race 另暴露 Firecracker 进程回收窗口：两次读取 stat 之间进程消失会被误判为 PID 身份冲突。已改用同一 stat 快照核对 starttime，保留后续完整身份验证；相关 race 回归重复 10 次通过。

### Command 的实际规则

- 身份、agent、类型与载荷绑定不可变。普通 Save 不能替换 lease owner、attempt 或截止时间；控制面取消保留当前租约，不能借取消清空原载荷。
- leased 仍是待交付状态，租约过期后可重新领取；TTL 必须为正，授权时间在存储锁内采样，提交失败不返回成功授权。
- 发送前用 `PrepareAgentCommandDelivery` 原子检查当前 leased 状态、未过期 owner/attempt 与截止时间，再持久化 delivered。不能把 stale Save 的无操作成功当成发送许可。无持久存储不允许从内存授予租约。
- Delete 保留退休记录，列表不再展示、也不能重新创建同 ID。文件后端的退休记录覆盖初始 dispatch seed，防止命令删除后重新出现；这不是对全部历史载荷的物理擦除。
- FS 命令写入与 Run dispatch 共用跨进程锁。新的命令 ID 冲突拒绝整个 bundle；接管已有 Run 只写 request mapping，不生成第二条命令。文件发布共用带文件与目录 fsync 的原子写入。
- SQLite 保存完整快照并核对索引字段、载荷与时间；Postgres 核对身份、状态与租约列，回调决策在行锁事务中提交。列表排序一致，取消 PostgreSQL 独有的 512 条截断。
- WebSocket 事件中的非空 AgentID 必须等于认证连接身份。现有 v1 事件没有 owner/attempt 证明，因此本阶段不宣称阻止所有迟到事件或资源侧副作用；后续需要显式协议设计和 fencing。

命令合同覆盖并发与独立进程抢租约、过期重新交付、提交失败、取消后发送、身份冒用、终态保护、退休防复活、派发冲突、时区 round-trip 和 SQLite 快照损坏。Postgres 16 实库已执行共享合同，使用临时实例与隔离 schema。完整 Run/Guest outbox 尚未完成。

该命令检查点通过独立复审，以及全仓无缓存 unit/race、vet、docs 和 diff 检查。升级时必须停止旧 API writer：旧 writer 不理解退休记录和完整快照约束，不能与本实现混跑。

### Run 的实际规则

- 普通 Save 不能修改 owner、attempt、已绑定执行目标或延长 lease；终态只接受相同结果。显式 Restart 才能重新执行同一确定性 run ID。
- Claim 在获取存储锁后检查时间，拒绝非法 TTL、过期 deadline、错误身份与 attempt 溢出；lease 不超过 deadline。Renew 不能复活过期租约或缩短当前期限。
- Expire 在锁内重读当前记录，不能用扫描旧值覆盖并发续租。完成回报必须证明当前 owner/attempt，projection 也必须属于对应 agent/topology/workspace；持久化失败不发布成功缓存。
- QueuedAt 区分同一 run ID 的执行轮次。排序、缓存、coalescing、完成回报和 Inputs 都核对当前轮次，旧 Done 或超前的 EndedAt 不能遮住新执行。
- SQLite 共用完整插入函数，dispatch 不再丢失 deadline/pinned snapshot；record_json 保留 Project/Workspace/Assertion，并与全部索引列核对。旧行仍按原列读取，非法时间不当作零值。
- Inputs 不进入持久存储，只在同轮次恢复。claim 返回预检验证过的 Inputs 副本，并在原子领取内再核对身份。已丢失非默认 Inputs 的 hashed apply 请求必须重提交原 revision/inputs。
- API 重启本身不证明远端 agent 失败：带 AgentID 的 assigned/running 保留 durable 状态，由租约和观测决定后续处置。

Run 迁移通过全仓无缓存 unit/race/vet/docs 检查和独立复审。FS/SQLite 合同包含并发、独立进程、提交失败、轮次错配、缓存刷新、输入丢失与索引损坏；Postgres 合同可通过 SYSBOX_TEST_DATABASE_URL 启用隔离 schema，已在临时 Postgres 16 上实跑通过。

### Guest metadata 的实际规则

- `Save` 只创建新记录，或接受内容完全相同的重试；不能绕过 CAS 覆盖已有版本。
- CAS 的缺失记录返回 typed NotFound，过期版本返回 `(false, nil)`；成功时只递增一次版本。失败的校验不改写原记录。
- ID、topology、node、agent 绑定不可变；CommandID 最多绑定一次；终态不能回退或改写执行结果。过期清理只允许移除请求和 stdout/stderr，不能重新填回。
- Request 比较采用与 JSON codec 相同的规范化规则，避免空 environment 在 round-trip 变成 nil 后产生假冲突。
- 文件操作的取消请求不能撤销；不能同时记录 CancelRequested 和 Completed。
- 本地更新采用稳定的跨进程 flock；SQLite 在 `BEGIN IMMEDIATE` 内读、验证、写；Postgres 通过事务内占位插入与行锁保护缺失 key 和已有记录。占位记录不能独立提交。
- acceptance 汇总走同一套解码和过滤规则。列表、格式和读取错误不被当成空结果；定点 Get 不扫描其他无关记录。
- 解码错误的公开字符串不包含 payload；内部仍保留 `json.SyntaxError`、`time.ParseError` 等诊断原因。

Guest 记录新增磁盘字段 `record_format: 1`，HTTP DTO 不新增这个字段，private request/agent/command 绑定仍不对外暴露。旧记录没有该格式标记时明确返回 UnsupportedVersion，既不自动补标记，也不覆盖或删除。升级时必须停止旧写入端，审计未完成操作，再按明确计划重建这两类 metadata；不能混跑旧 API writer。

## 3. 恢复和执行的安全边界

恢复使用持久化 action 和原始 revision，不信任孤立的 checkpoint patch。正常执行和恢复共享 runtime 观测规则。对可能执行过任意 guest 命令、但没有完成证据的动作，返回不确定结果，不自动重放。

Firecracker reset 的外部上下文表示**目标代际**。Provider 内部访问旧 manifest 时切回旧身份；替换 VM 使用原样持久化的目标身份，不能随机生成另一个 action ID。

API 主机不执行远端资源探测。显式 remote observe/recover 请求通过 durable command 发送到 Run.AgentID，只有宣告 run-recovery.v1 的 owning Agent 可以执行；本机没有 manifest 不构成远端资源不存在的证据。

执行结果中 `ExitCodeKnown=false` 不能被当成 exit 0。请求超时只证明本地等待结束；未验证远端终止时 `RemoteTermination` 保持 unknown。

Create 与 Reset 共用声明路由逻辑：先观察，持久化子步骤后配置，再验证，最后才运行依赖路由的 provisioner。路由查询错误保留为 unknown。内置 node/router 的创建与 reset 要求完整 effect 证据；仅仅观察到 VM 正在运行，不能证明镜像入口或 guest 初始化已执行完成。镜像入口启动失败会向上传播。

## 4. 用户可见变化与剩余工作

| 接口/行为 | 当前边界 |
|---|---|
| HCL、资源地址、HTTP v1 | 不引入设计稿中的新必填字段、协议版本或错误对象；HTTP `error` 保持字符串 |
| Guest HTTP 错误分类 | 不存在为 404，冲突为 409，损坏/不可用为 500；执行 Get 先授权再读存储，不再用 404 隐藏损坏 |
| Provider 接入 | `driver.Descriptor` + capability 模型保留；缺少管理发现或可靠观测的能力明确拒绝，不能默认为成功 |
| Go 执行接口 | `Connection` 只要求前台 Exec；调用 Start/Copy 等能力前需断言对应可选接口；旧 GuestExec 包装器委托同一实现 |
| 旧 state / 数据库行 / Firecracker 磁盘格式 | 不隐式迁移，不把旧状态当空状态，不自动删除真实基础设施；需要显式重建与处置旧资源 |
| `sysbox_ssh_access`、新 firewall create | discovery/read-back 未完整实现，默认引擎明确拒绝；这是当前迁移缺口，不能宣称所有旧场景等价可用 |
| `gc` | 仅可信 OwnerUID 范围内的 `--dry-run` 候选；自动删除暂禁，直到 inventory→delete 有完整 fencing。SQL/remote/其他 runs 目录不在当前 GC 库存范围 |
| State snapshot / lease 运维 | 新 aggregate 尚未实现旧 snapshot/force-unlock 等完整能力；不能把返回空列表视为能力已经迁移 |
| 外部资源 import | 未迁移显式 ownership adoption；旧 import 返回的无 owner/generation 记录会被新 repository 拒绝，不能据此声称 import 已恢复功能等价 |
| SSH/vsock 后台执行 | 句柄不等于跨重启持久任务；观察与可验证取消、vsock stdin、文件 checksum 合同尚有缺口 |
| 执行 generation | managed 主路径已经传递 generation 标签，但尚不等于连接层 fencing；不宣称已阻止所有 stale-generation 执行 |


## 2026-10-04：远端 Observe/Recover 与真实 Postgres 验收

远端入口保留 agent.v1，新增独立 capability `run-recovery.v1` 和 `run_recovery` command；旧 Agent 不会收到不认识的恢复命令。

| 接口 | 行为 |
|---|---|
| POST /v1/runs/{id}/remote/observe | 返回 202 + command_id，派发到原 Agent；running Run 可观察，不修改其运行状态 |
| POST /v1/runs/{id}/remote/recover | live lease 返回 409；过期 running 经原子 ExpireRunLease 后，才允许 failed/recoverable 恢复；必须有固定 revision、完整输入证据且无其他 active Run |
| POST /v1/agents/{agent}/commands/{command}/recovery-authorize | Agent 执行前验证连接身份、当前 command owner/attempt/期限、Run 轮次及 capability，返回经过摘要验证的固定 revision |
| GET /v1/runs/{id}/remote-reports/{command} | 返回持久恢复报告；尚无报告 404，失败终态或当前 Run 轮次已变化返回 409 |

发起和查询要求管理员权限。报告包含 action ID、owner UID、resource UID、generation、external ID、观测状态、journal 状态、state serial、Run 轮次和执行租约证明，不包含 provider private state。command/report 同事务退休，API 重启后仍可查询；started/failed 事件不能夹带完成报告。

Observe 只调用 provider Read；候选身份缺失、查询失败或观察期间状态改变保留 unknown/冲突，不把它当成 absent。Recover 复用 runtime RecoverActions，只提交 journal 已授权且可证明的结果，不 Create/Delete、不重放 provisioner/guest 指令，不将整个 Run 自动宣布成功。未知复合动作和缺少完成证据继续拒绝。执行期间依赖 action revision/state serial CAS 和未完成 action 冲突检查，不宣称已有跨控制器全局 Run 排他锁。

action intent 新增 run_queued_at/run_revision，并作为不可变身份核对。旧未完成 journal 缺少轮次证据时拒绝远程恢复；历史成功轮次不会被当成当前轮次报告。没有固定 revision 或已丢失非默认 Inputs 的恢复请求也会拒绝，不能隐式选择当前 workspace/default inputs。

实际验证使用缓存镜像 postgres:16-alpine、回环端口和临时数据目录。开启两个测试 DSN 后，全仓 unit/race 通过；新增覆盖 GuestFile command 冲突事务回滚、retry metadata 重开、恢复报告三后端持久化、HTTP/WebSocket 控制器重启、过期/错轮次授权、Agent runner 证明回传，以及 runtime lost-commit 恢复不重放 effect。实库暴露的 PostgreSQL GetRun NotFound 类型不一致已修复。

边界：尚未进行真实 Firecracker/Docker/libvirt 的远程宿主机 E2E，也未验收 Postgres 主备切换或断电故障；上述结论不是生产 HA 验收。

## 5. 验证记录

已运行的针对性测试覆盖：FS/SQLite 共用 repository 合同、并发 intent、丢失提交响应、旧阶段覆盖、畸形 aggregate、SQL 事务清理失败、SQLite 重开、CLI/Agent 地址一致性、SQL 列表、metadata 删除、Firecracker 冷恢复/reset，以及 transport 输出上限和未知退出码。

后续复审补充覆盖：Reset 路由重建与验证、guest/image-entry 完成证据、check 截断标志、console 输出 EOF 与退出结果的竞态，以及 pending topology 在列表和 state API 中的一致可见性。

Guest metadata 迁移通过独立复审和全仓无缓存单测/race/vet/docs 检查。合同测试覆盖 FS/SQLite 的重开、并发 CAS、跨进程锁、create 重试、终态和绑定保护、取消、格式损坏、私有字段及 HTTP 错误分类。本轮设置 `SYSBOX_TEST_POSTGRES_DSN` 与 `SYSBOX_TEST_DATABASE_URL`，连接隔离 PostgreSQL 16 实例执行，测试只操作并清理各自 schema。

本阶段已通过 `go test ./...`、`go test -race ./...`、`go vet ./...`、`make docs-test` 和 `git diff --check`。已运行临时 Postgres 容器上的数据库集成合同；未运行真实 VM/业务容器或网络拓扑。带 `tests/e2e` 的普通 `go test ./...` 输出不代表真实部署测试已运行。

合入/发布前仍需：完整 metadata repository 迁移；剩余资源观测能力与 owning-agent 恢复链路；完整 fencing/outbox；真实 substrate E2E 与生产数据库故障矩阵；逐条核对接口清单中的行为变化。
