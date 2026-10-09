# Sysbox 完整能力迁移与分布式执行闭环：实施修复计划

日期：2026-10-07。评估基线：`a3ef18e`，worktree `/tmp/sysbox-refactor`。

本文是待实施计划，不是已完成声明。它取代“这些缺口可以全部推迟为独立增强”的范围判断；已有实现与证据仍见[当前实施状态](2026-09-28-runtime-refactor-implementation-status.md)。

## 1. 目标与完成标准

**目标：完整迁移 Sysbox 的受管能力，并让 API、Agent、provider、guest 在独立进程和独立宿主机上形成可恢复、可撤销授权、可核验结果的执行闭环。**

Sysbox 负责基础设施、执行任务和证据，不负责 cyberfield 的场景、业务编排或归档。继续使用现有 repository、action journal、provider capability，禁止另起一套恢复状态机。

完成须同时满足：

- 原有功能逐项完成新模型迁移，包括 import、自动 GC、state 运维、`ssh_access`；临时 Unsupported 只表示阶段未完成，不能计为交付。
- API/Agent 不共享工作目录也能 Apply/Observe/Recover/Destroy，产物和配置通过明确合同传递。
- 旧资源代际、旧执行 epoch、旧 Agent session 均不能授权新副作用；断连不产生双执行者。
- 后台任务在 API/Agent/guest helper 重启后可重新定位，取消有可验证结果；unknown 不伪装成退出或取消成功。
- 三存储后端共享业务规则；真实跨宿主、真实 provider、真实数据库故障验收通过。
- 升级、能力矩阵、错误分类、部署路径和构建产物有一致说明；不保留旧格式/旧执行路径回退。

这里的撤销是停止后续授权并终止可控的在途任务，**不意味着已完成的文件写入、创建或外部请求可以自动回滚**。强制取消不了的 daemon 调用必须记录 uncertain 并通过观察处理；不能承诺任意命令 exactly-once。

## 2. 对照当前代码的缺口

| 编号 | 当前基础与证据 | 必须补齐的交付 |
|---|---|---|
| G1 远端交付 | `Run.SnapshotPath` 是路径；现有 Docker 全链路 E2E 共享宿主文件；恢复有固定 revision 机制 | 普通执行和恢复统一下载/校验 revision，Agent 本地物化；artifact 按 digest 交付，取消 API 绝对路径依赖 |
| G2 授权闭环 | completion 校验 lease/attempt，provider 校验 generation；API 当前限定单实例执行 | topology/resource 执行 grant、单调 epoch、Agent session incarnation；资源宿主强制执行 gate；失联自动到期、撤销确认与接管协议 |
| G3 持久任务 | `pkg/transport/background.go` 以 nohup 启动并返回 PID；Observe/Cancel SPI 尚无 SSH/vsock 实现 | 统一任务 ID、持久 manifest、过程锚点、退出结果、输出读取、进程组/cgroup 终止与重启恢复 |
| G4 管理能力 | import、GC、snapshot/restore、`ssh_access` 被拒绝或只读 | 在新 ownership/journal/grant 模型上实现，而非恢复旧脚本或直接写 state |
| G5 元数据与证据 | inventory/projection 缺 producer epoch/sequence；清理检查不枚举 guest execution/file | 有序观测、明确陈旧/未知、完整活跃操作索引；恢复/删除检查涵盖所有可能产生 effect 的操作 |
| G6 网络能力迁移 | 安全目标证明已建立；Firecracker/libvirt 直接接 Docker NAT 的旧猜名路径已移除 | 正式 managed-network attachment target SPI，提供可验证 bridge 证明，恢复支持组合并实测 |
| G7 交付验收 | 真 Postgres、KVM、Docker、libvirt 分层通过；新增 guest ready gate | 最终二进制与源码一致、全链路进程故障/断网/跨宿主验收、部署与破坏性重建演练 |

已有销毁回执不重做，但需要在 G2/G5 引入 epoch、所有活跃操作检查及新观测序号后升级失效规则，避免一张旧回执跨授权轮次继续生效。

## 3. 目标分层与依赖

```text
P0 能力清单、协议与验收基线
  ├─ P1 远端配置/产物交付 ─────────────┐
  └─ P2 持久授权与宿主执行 gate ─ P3 持久任务协议
                                      │
                    P4 端到端撤销/接管与有序证据
                                      │
            ┌─────────────────────────┼────────────────────┐
      P5 显式 adoption          P6 snapshot/lease      P7 ssh_access/网络迁移
            └─────────────────────────┼────────────────────┘
                              P8 自动 GC
                                      │
                         P9 全能力跨宿主验收与交付
```

P1、P2 在 P0 契约确定后可并行。P5/P6/P7 在 P4 的共同授权边界稳定后可并行。P8 必须消费已有的 owned Delete、task cancellation、snapshot retention，不自己猜测和删除。

### 核心协议草案

| 对象 | 必需内容与规则 |
|---|---|
| ExecutionGrant | topology UID、resource UID/generation或拓扑范围、action ID、command ID、Agent ID/session、epoch、attempt、有效期、授权操作、期望摘要；签名/认证、不可由 worker 自行续期 |
| AgentSession | 持久 Agent 身份之外的每次重启会话；旧 session 的报告和 lease renewal 不能覆盖新 session |
| TaskRecord | task ID、grant 绑定、不可变请求摘要、guest boot ID、PID/start time/cgroup、状态版本、结果、输出游标；不把 PID 当身份 |
| ObservationEnvelope | owner/resource/generation、producer session、单调 sequence、observed_at、来源、存在性/身份/运行态、完整性；消费端拒绝倒序 |
| AdoptionPlan | 外部不可变 ID、当前 ownership、目标 UID/generation、配置/依赖摘要、预期版本、用户认领授权；冲突即停止 |
| AggregateSnapshot | topology UID、schema、state/journal 一致快照、serial、digest、创建时间、依赖对象引用；不是仅复制 resource JSON |

上述是新增合同草案，进入各阶段时固化字段、错误和测试，不把它们当作当前已有接口。所有 wire/schema 变化集中到接口变更清单，统一版本协商，缺必需能力在派发前拒绝。

## 4. 分阶段实施

### P0：冻结能力矩阵与验收基线

范围：`docs/design/*implementation-status*`、`*interface-change-inventory*`、`pkg/api/capabilities.go`、`pkg/driver/capability.go`、`Makefile`、`scripts/release/workflowcheck`。

- [ ] 列出 CLI/API × Docker/Firecracker/libvirt × 文件/SQLite/Postgres 的能力矩阵；每格必须是通过、有明确故障、尚未实现或不适用。
- [ ] 复核最新 readiness guest 二进制确实由当前源码构建；amd64/arm64 的构建依赖和测试入口不能漏掉新增源文件。补旧 guest 拒绝和自定义 init marker 验收。
- [ ] 把后续总验收拆成可执行目标：本地合同、双进程、双宿主、故障注入、完整业务拓扑生命周期。
- [ ] 记录主线分支差异，但不边开发边覆盖重构合同；最终集中整合。

验收：clean build 可重复，版本/capability 与二进制一致；本计划所有缺口都有具体测试入口/阶段负责人。不能仅凭历史测试宣布当前提交通过。

### P1：跨宿主 revision、artifact 与输入交付

修改入口：`pkg/agentexec/agent.go`、`executor.go`、`run_recovery.go`，`pkg/api/revision_snapshot.go`、`execution_bridge.go`，`pkg/controlplane/model.go`、`pkg/artifact`。

- [ ] command/run 只携不可变 revision/artifact 身份与授权下载信息；SnapshotPath 仅为执行宿主本地细节，不作跨宿主位置合同。
- [ ] Agent 下载并核对整棵 revision tree digest，拒绝路径穿越/不受控 symlink；临时目录完整落盘后原子发布，创建与恢复共用物化函数。
- [ ] artifact 使用 digest/架构/guest family，支持 Agent 缓存与可靠来源；配置中的本地路径明确归属，不能把 API 主机路径当作 Agent 可读文件。
- [ ] 敏感输入不进持久日志/快照；API 重启或转移实例后的补交有明确操作状态，Agent 不靠 session affinity 猜测授权。
- [ ] 在没有共享挂载的独立主机上跑第一条 Apply→Observe→Destroy→receipt→metadata DELETE。

验收：API 的绝对路径在 Agent 上不存在也能完成；错误 digest、下载中断、缺失输入不触发 provider effect；Agent 缓存重建不改变 revision。真正双宿主环境由测试设施提供，未配置时报告“未验收”，不能以 skip 算通过。

### P2：持久 grant 与资源宿主执行边界

现有入口：`pkg/persistence/{run_repository,command_repository,run_completion,node_operation_completion,guest_completion}.go`、`pkg/api/run_admission.go`、`pkg/agentexec/agent.go`、`pkg/substrate/operation.go`、provider effect 入口。建议新增 `pkg/controlplane/execution_grant.go`、`pkg/persistence/execution_grant.go`、`pkg/agentexec/effect_gate.go`。

- [ ] 在共享 repository 中原子分配 scope epoch、绑定 Agent session、签发/续租/撤销 grant；epoch 不随数据库记录删除而回退。
- [ ] 同一资源上的 Run、NodeOperation、guest exec/file、adoption、GC 都经过同一冲突规则；不能只在 API 进程内 mutex 下互斥。
- [ ] 资源宿主由常驻 supervisor/gateway 独占受管 provider 凭据和 effect 通道；worker 只提交带 grant 的请求。绕过该边界的运行模式不能宣称同代 fencing。
- [ ] gate 在效果开始前核验 grant，持久记录 action 去重、最小 epoch 和在途效果；lease 无法刷新时先停止新效果，再由 watchdog 终止可控任务。
- [ ] epoch只由持久授权事务推进；本机到期使用单调时钟预算，进程/宿主重启后旧grant不能按旧倒计时复活。协议定义最大有效期、时钟偏差及断网停止预算，并测试时钟回拨。
- [ ] 新 owner 不能仅凭 API lease 超时就与旧 owner 并发执行。必须取得旧 gate 撤销/到期停止证明，或对旧宿主实施可验证 fencing；无法证明时保持 blocked。
- [ ] 对不可取消的 daemon 调用记录 uncertain，在它返回或经观察澄清前不发互斥替代动作；API completion 拒绝只是最后一道校验，不能当作执行中止。

验收：暂停旧 worker、过期 grant、启用新 epoch、再恢复旧 worker，旧者不能发新 effect；gate 重启不忘记 epoch；数据库/网络不可达期间不无限续权。记录并实测停止上界、时钟偏差假设和强制终止后的残留处理。

### P3：SSH/vsock 统一持久任务管理

现有入口：`pkg/substrate/execution.go`、`pkg/transport/background.go`、`ssh.go`、`vsock.go`、`pkg/vsockrpc/protocol.go`、`cmd/sysbox-init/server.go`。建议新增可独立测试的 `pkg/guesttask`，SSH 与 vsock 使用同一任务实现，经不同传输访问。

- [ ] 实现 Start/Observe/Cancel/List/ReadOutput 的版本化协议；request/task ID 在发出前持久化，相同 ID+摘要重复请求返回原任务，摘要不同冲突。
- [ ] guest supervisor 持久任务 manifest、请求摘要、boot/process/cgroup 锚点与退出结果；helper 重启能发现原任务，输出支持配额、截断标志和游标。
- [ ] SSH 通过预装并验证版本的受管 helper 接入，移除 nohup+echo PID 作为正式后台能力的实现；helper 不可用必须在 effect 前说明如何安装。
- [ ] Cancel 先持久取消意图，定位同一 boot/进程/cgroup 后终止整棵任务，核验退出再返回 confirmed；PID 被复用不能误杀。
- [ ] guest helper保存可接受的最小epoch并独立执行到期watchdog；暂停旧Agent或断开vsock/SSH时，guest任务不能因上游不再发消息而无限继续获授权。
- [ ] guest 重启时旧任务标记为 boot-changed/未知退出结果，不伪造 exit code；启动窗口丢失回执通过 task ID 恢复，不重放任意命令。
- [ ] API guest execution/file、provisioner、后台 Start 与 reset/destroy 任务清理统一消费同一模型；移除 PID-only transitional adapter。

验收：启动结果丢失、helper/Agent/API 各自重启、guest reboot、进程树派生、输出超限、PID 复用、取消与完成并发均有确定测试；SSH/vsock 返回一致语义。

### P4：端到端撤销、接管与完整证据

范围：P2 gate、P3 guest supervisor、`pkg/api/run_recovery.go`、`guest_execution_service.go`、`guest_file_operation_service.go`、`cleanup_inspection.go`、`cleanup_receipt_evidence.go`、`pkg/persistence/agent_metadata_codec.go`。

- [ ] grant 到期/显式 cancel 经 owning Agent gate 传播到 guest task；API 分别显示 requested、stopping、confirmed、unknown，断开 SSH/vsock 不等于已终止。
- [ ] 续租与 cancellation 全部受当前 epoch/session 约束；旧任务允许只读观察，但不能获新授权继续执行。
- [ ] inventory/projection 携 producer epoch+sequence，以共享规则原子拒绝倒序；只读快照不授予 ownership。
- [ ] 活跃操作索引包含 Run、NodeOperation、guest exec/file、adoption/GC/恢复任务；Destroy 与 metadata 删除检查该完整索引。
- [ ] receipt 加入执行/placement epoch 与观测序号，验证没有随后被准入的任务；API 切换后仍可判定有效性，不能只靠 wall-clock 排序。
- [ ] 两个 API 共用 Postgres 的准入、outbox、取消、完成、接管实测；配置/秘密交付不能依赖某一个 API 内存才能保持正确性（等待补交可以，但状态必须明确）。

验收：断网、lease 到期、API failover、Agent crash 后任务按合同停止/隔离；重复完成及取消回报幂等；没有新旧执行者同时获得同一互斥 effect 权限。

#### P4 恢复实施补充：资源已完成、子任务未结束

采用现有 effect ledger 的可选 Run checkpoint，不创建第二套恢复日志。
可信 callback 在同步资源执行成功后封闭子任务准入，把 RunRequest、去除
Inputs/Cleanup 的资源结果和完整子任务身份清单持久化；随后仅等待或取消子任务。
checkpoint 不表示整个 Run 成功，也不赋予执行权限。

每个 Run 身份使用独立的宿主 flock，从准入之前持有至 callback 和结果提交结束。
冷恢复必须取得同一锁，重新读取当前 journal，确认 checkpoint 身份及历史签名，
逐个停止/观察清单中的任务并核验同一 Run、parent epoch、签名 grant 和停止回执。
全部停止后只能提交 failed，清理回执重新生成；API 仍检查权威子任务库存后释放父授权。
存活但不合作的 callback 持锁时不能接管。没有 checkpoint 的 intent 保持 unknown；
资源 daemon 请求中断的恢复仍须提供独立 quiescence 证据，不能用资源存在性替代。

实施顺序与文件边界：

1. `pkg/effectgate/run_checkpoint.go`：在 intent 上单次保存不可变 checkpoint，
   绑定当前 signed envelope；续租保留 checkpoint。测试冷读、内容冲突、旧授权拒绝。
2. `pkg/hostsupervisor/run_checkpoint.go`：执行锁、context checkpoint hook、
   已封闭子任务清单以及冷恢复；`run_async.go` 在准入前取锁并将锁所有权转移给 callback。
   测试存活 callback 不被接管、资源已返回但结果丢失可恢复、未记录 checkpoint 不恢复。
3. `cmd/sysbox/commands/supervisor_cmd.go`：资源成功后、等待子任务之前记录 checkpoint，
   recovery 使用已有 Dispatcher/ResolveRun。不能由 worker 提交任意 checkpoint。
4. 回归验证 `go test -race ./pkg/effectgate ./pkg/hostsupervisor`、commands 测试、
   独立审查，然后双 VM 运行中杀 supervisor 验证失败收敛及正常 Destroy。

此切片不关闭 daemon 未知结果、长期服务 ownership 转移或完整 P9 故障矩阵。

### P5：显式 ownership adoption / import

范围：`cmd/sysbox/commands/import_cmd.go`、`pkg/api/node_operation_service.go`、`pkg/runtime` resource handlers、`pkg/driver/capability.go`、Docker/libvirt/Firecracker 的 managed/discovery 代码。

- [ ] 新增只读 InspectAdoption、生成 AdoptPlan、显式 ApplyAdoption；检查当前 ownership、外部不可变身份、配置、依赖、冲突及能力。
- [ ] 认领经过 grant+journal，先保存意图，再写 provider ownership，再验证并提交 state；任意阶段崩溃后能发现本次认领结果。
- [ ] 无 owner 的资源可按计划认领；已有不同 owner 不允许隐式抢占，转移须独立明确授权和旧 owner 退休证明。
- [ ] Docker labels 不可原地更新等 provider 限制必须形成可验证的持久绑定模型或显式替换计划，不能伪称已经写入标签。
- [ ] 首先恢复 Docker/libvirt 原有可导入能力；Firecracker 不具备可核验启动/进程材料的任意进程不算可认领，给出具体缺失证据，已具备完整 manifest 的资源支持正式接管流程。

验收：认领后冷恢复、更新、Destroy 全部可用；同名替代对象不能被采用；一半成功时恢复不重复认领、不删除外部原资源。

### P6：aggregate snapshot、restore 与 lease 运维

范围：`pkg/persistence/repository.go`、`filesystem.go`、`sql.go`，`pkg/state/manager.go`，API state endpoints 与 CLI 运维命令。

- [ ] 三后端实现一致快照、列表、校验、保留策略、删除；snapshot 覆盖 state+journal 的同一事务视图，不持久化明文 inputs 或凭据。
- [ ] 区分“元数据恢复”和“VM/磁盘快照”。恢复 state 不宣称真实 VM 或文件回到了过去；provider snapshot 若属于原功能需单列矩阵验收。
- [ ] restore 在冻结新受理、撤销当前授权后执行；保留原拓扑身份、提升 metadata revision/epoch，禁止 serial/generation 回退授权，随后强制真实观察与重规划。
- [ ] snapshot 中带未完成 action 时必须保留为 unresolved，不绕过 journal；不同 lineage 的恢复作为单独导入/灾备流程，不能默认覆盖。
- [ ] lock/lease inspection 返回实际 owner/epoch/到期/在途情况；force-unlock 变成撤销并等待 gate 停止的审计操作，不 unlink flock 或直接清空 lease。

验收：并发快照、restore 崩溃、旧 worker 恢复、错误 lineage、旧 snapshot 与较新资源冲突全部拒绝或进入需观察状态；三后端恢复语义一致。

### P7：受管 SSH access 与网络能力等价迁移

范围：`pkg/runtime/resource_ssh_access.go`、`pkg/driver/capability.go`、`pkg/substrate`、P3 helper，`pkg/provider/{network,docker,firecracker,libvirt}` NIC 与网络接口。

- [ ] `ssh_access` 通过版本化 guest 配置 SPI：Discover/Observe/Apply/Delete；持久独立配置文件及 ownership/digest，读取实际 keys/config/service/监听状态。
- [ ] 只修改自己管理的 drop-in/授权 key 文件；删除只撤销自己的访问配置，不删除其他 SSH 用户或停掉共享 sshd。
- [ ] 软件包/发行版能力明确声明；运行时不可恢复 apt/apk 猜测链。凭据/密钥引用保留安全属性，输出和 journal 不记录秘密。
- [ ] 用 grant+journal 和任务回执处理配置、reload、观察的崩溃窗口；相同请求可收敛，外部修改报告 drift/conflict。
- [ ] 网络 provider 正式输出可消费的 managed attachment target，包含网络 UID/generation、真实 bridge/namespace 证明与刷新方式；NIC provider 不推导 Docker `br-<id>` 名称。
- [ ] 恢复 Firecracker/libvirt 对受支持 Docker NAT 网络的接入；在创建、启动、Observe、Delete 全链路验证 target。混合 topology 的 reset/恢复重新取得并核对该证明。

验收：SSH 访问实际成功、轮换后旧 key 失效、Delete 后自己的 key 失效且其他访问不受损；三 provider 混合网络通信、NAT、同名替换、reset 和无残留销毁实测。

### P8：自动 GC

范围：`cmd/sysbox/commands/gc_cmd.go`、API operation registry、provider inventory/discovery、P5 ownership、P6 snapshot retention。

- [ ] GC 分 Inventory→Candidate→Plan→AuthorizedDelete→Verify；每个候选有 owner/resource/generation、外部 ID、来源、保留期、依赖与引用证据。
- [ ] 聚合所有 state/journal/task/snapshot/revision/placement 引用，未知、离线、未完整枚举、正在认领/恢复的对象不进入删除计划。
- [ ] 真正删除前重验 epoch、身份、引用和计划版本，走既有 provider Delete+journal，不新增裸路径/名字删除链。
- [ ] 删除中断可恢复，按依赖逆序，小批量、限制预算；provider inventory 和本地 artifact/cache GC 分清权限范围。

验收：dry-run 与执行目标一致；计划后新引用、同名替换、旧 epoch、部分查询失败均不误删；自动清理真实测试孤儿后无受管对象受损。

### P9：完整验收、整合与发布

- [ ] 至少两个独立内核/宿主环境，API 与 Agent 不共享工作目录；KVM/libvirt 场景运行在实际支持这些 provider 的 Agent 上。
- [ ] Docker/Firecracker/libvirt 分别完成 Apply→Observe→Reset→Exec/File→Cancel→Recover→Destroy→receipt→metadata DELETE，另验收 adoption、snapshot、GC、SSH access。
- [ ] 在准入提交、effect 前后、journal提交前后、回报前后逐点 kill API/Agent/helper；增加断网、延迟、重复/乱序消息、磁盘满、DB重启、不可取消调用。
- [ ] Postgres 双 API 切换和服务故障矩阵；文件/SQLite 验证各自支持的独立进程范围，不把共享文件锁描述成跨宿主仲裁。
- [ ] 真实更新/重建演练：停止旧 writer、保留证据、精确处置资源、重建不兼容数据、同步 guest/helper/Agent/API；禁止手改version和共享库整体清空。
- [ ] 整合最新 main、消除能力表与文档矛盾；独立架构/代码审查，测试产物记录 commit、镜像digest、协议/schema、环境与清理结果。

总验收的缺环境不能以 skip 算通过。发布、推送、合并仍按明确授权进行；实现计划本身不授权删除现有项目或重建用户共享数据库。

## 5. 关键故障验收表

| 故障注入 | 必须观察到的结果 |
|---|---|
| API在受理提交后崩溃 | 原key返回原操作，outbox恢复，不重复effect |
| Agent执行前断网 | 无可用grant不开始；未确认请求保持pending/unknown |
| 旧worker暂停至lease过期再恢复 | gate拒绝旧epoch；新owner在旧效果未隔离前不能执行 |
| guest/helper在Start响应前崩溃 | task ID查询到同一任务或明确未知，不盲重放 |
| Cancel响应丢失 | 相同task ID取消幂等；只有退出证明才confirmed |
| PID或资源名字复用 | 不观察成旧任务、不误杀、不误删 |
| restore旧snapshot后旧worker回归 | 旧epoch/serial无法提交或执行；真实资源差异重新规划 |
| GC计划后有新引用 | CAS/重验拒绝删除，候选失效 |
| receipt后出现guest任务或新placement | 普通metadata删除拒绝，不凭旧空状态回执放行 |
| 自定义init或旧guest协议 | 必需能力缺失在effect前发现；正常配置的目标guest完整走通 |

## 6. 组织方式与停止条件

每阶段按“失败回归→最小实现→三后端/对应provider合同→故障验收→独立审查→文档更新→提交”执行。上述是分阶段实施计划；每个阶段开工前固化该阶段的 wire/SPI/schema 与文件级任务清单，不一次性编写整套未来实现。

建议三条工作线：A负责repository/grant/API，B负责Agent/gate/guest task，C负责provider迁移/故障设施；接口文件单一负责人，避免并行引入不同身份模型。P5/P6/P7可分开实施，P8由共同证据合同汇合。

不以“代码存在”“单测通过”“已返回Unsupported”“没有环境所以skip”标记功能完成。只有能力矩阵指定的真实环境、故障门槛通过才关闭对应缺口。若有不可实现的保证，必须说明底层限制和可实现的替代合同，不能悄悄移出总目标。

**执行起点：P0与P1，随后P2/P3形成授权和任务基础，再完成P4闭环。** 这样最早暴露真实远端问题，也为后续功能迁移提供同一套可靠机制。

### 2026-10-09：P4/P6 会话持久化并发边界

Console 的文件、SQLite、Postgres 存储已统一为创建或完整记录 CAS，移除覆盖更新。
身份字段由共享规则约束；独立 API hub 写入冲突后核验并读取当前状态，保留冲突
返回以免盲目重放过期操作。启动 lost 观察使用同一 CAS，竞争时重新读取并有限
重试，持久取消/终态不会被启动恢复覆盖。该项只关闭会话记录的并发写入缺口；
原子受理/删除边界、会话执行所有权及远端停止证明仍是 P4/P6 的待交付内容，
不能用该项合同测试替代 P9 双宿主故障验收。

### P6 下一步：会话写入与维护屏障串行化

复用 authority 和生命周期记录，不创建新的锁域。文件后端沿用 records → authority
顺序，并持锁至原子写入结束；SQLite 使用 `BEGIN IMMEDIATE`；Postgres 先创建/
锁定 authority 行，再处理 session。状态规则留在 persistence 共享层，后端只提供
锁内读写。Topology 名称只用于定位完整 lifecycle，再从中验证真实 UID。

- [x] 在 `pkg/api/console_maintenance_test.go` 复现冻结后 Save/CAS 仍可新增活跃
  会话的问题；覆盖三后端、冻结与首次写入竞争、取消/终态收敛、终态重新激活拒绝。
- [x] 在 `pkg/persistence/console_repository.go` 固化创建、CAS 和需要新准入的写入规则：新记录
  必须检查；精确重试不产生新活动；已有未解决会话可写取消、lost 和有效完成；
  其他引入未解决活动的写入都检查屏障。缺生命周期且有活动屏障时保守拒绝。
- [x] 将 `pkg/api/run_maintenance.go` 的锁内 topology 检查抽为通用函数，保留
  Run 的终态放行规则；`console_record_adapters.go` 在 Console Save/CAS 的
  提交边界调用同一检查，三个后端共用 repository，未保留覆盖写入分支。
- [x] 验证失败不写入、冻结成功时此前成功受理已可见；运行三后端合同、race、
  全仓回归，独立审查并记录证据。这不代替 session/outbox 原子受理和远端停止证明。

### 2026-10-09：Console 初次受理与 outbox 同提交

正常创建先准备完整会话、policy 审计和节点检查，再调用共享 ConsoleRepository.Admit。
文件适配器将不可变初始 command 随 session 一次原子发布；SQL 适配器将 session 与
command 放在同一事务。沿用现有 authority 锁和维护检查，不增加独立恢复日志。
已有 session 或 command 冲突时整次受理失败；后续 CAS 保留文件 dispatch seed，
命令变更记录及退休 tombstone 优先，seed 不复活已退休命令。

- [x] 三后端初次受理、命令冲突回滚、冻结拒绝、共享 command ID 并发唯一胜者。
- [x] HTTP 不再先落 queued 再单独发布命令；授权审计一起提交，失败不暴露内存会话。
- [x] 提交后仅 WakeCommand 通知，通知失败仍返回202，由持久 outbox 补投递。
- [x] 禁止 AgentService.PublishCommand 单独发布 session_open，移除旧创建生产路径。
- [x] 请求体身份/角色不能替代服务端请求主体；拒绝请求只保存终态审计，无执行命令。
- [x] 最终全仓回归和验收证据归档。

本项关闭正常初次创建的 session/command 分步写窗口。HTTP idempotency key 受理、
取消意图与取消 command 的同事务提交、完整删除事务、宿主会话所有权与签名停止
证明仍需继续实施；不以本项测试关闭整个 P4/P6/P9。
