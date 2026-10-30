# Sysbox 架构设计与重构安排

Sysbox 管理实验环境里的节点、镜像、网络、挂载和策略。核心工作是：**观察现状，计算与配置的差距，执行计划，验证并记录结果。**

[接口文档](interfaces.md)记录当前可调用契约，[快速开始](../README.md)提供运行入口。
源码基线为 `dev@291890a`。本文中的重构目标尚未实现；文档整理不代表代码已完成切换。

## 这轮已经确定的边界

- 使用一次停机重建升级：停止旧服务 → 确认并清理旧资源 → 归档旧数据 → 新版本重新部署。
- 新版本不承担旧 state、旧 journal 的在线迁移和接管，不保留旧执行模式开关。
- 清理旧资源仍使用旧版本及其原始记录；停止 Sysbox 服务不等于资源已经销毁。
- 新版本自己的崩溃恢复、重复请求处理和未知结果保护仍然必需，不能因为允许重建而删除。
- 一个 topology 由一台宿主负责，同时最多执行一个变更；不同 topology 可以并行。
- 保留 Docker、Firecracker、libvirt，以及现有 Linux/Windows 适用能力。不同平台不强求功能完全相同。
- 保留 CyberField 使用的配置发布、幂等 Apply/Destroy、状态查询、清理确认、初始化和健康检查。
- 本轮不扩展跨宿主自动接管、通用 workflow、跨主机 overlay 等新能力。已有高级能力先登记调用方，再决定退出或独立保留。

允许停机重建不等于现在授权清理任意宿主。本轮先重构和验收；实际切换前单独确认目标部署与资源范围。

## 重构后代码负责什么

```text
CLI ──────────┐
              ├─ 宿主服务 → 资源引擎 → Provider SPI → Docker / Firecracker / libvirt / 网络
集中 API ─────┘                │
（可选）                       └─ 资源状态 + 操作记录
```

本地和远程使用同一个宿主操作服务。Unix socket 与远程投递可以不同，资源执行规则只有一份。
集中 API 管用户请求、宿主选择和结果展示，不再参与资源执行和资源恢复决策。

宿主服务内部按权限分进程：普通账号处理请求、计划和操作记录；受控 executor 持有必要的宿主访问权限并执行 provider。
executor 校验调用身份、操作范围和资源归属，不接受任意宿主路径/命令，也不再实现另一套业务编排。
两者之间的在途记录用于说明实际效果，不能被当成第二份资源 state。

| 模块 | 职责 | 不应承担 |
|---|---|---|
| config / graph | 解析配置、变量、依赖 | 调度和执行 |
| model | 资源身份、观察、计划、结果 | HTTP、Agent lease、数据库连接 |
| runtime | 观察、计划、按依赖执行、核对未完成动作 | API Server、远程投递、具体 provider 分支 |
| spi / provider | 明确的能力契约 / 各平台实现 | 比赛业务、调度队列、重复资源状态机 |
| host | 持久受理、去重、执行归属、查询与取消 | 第二套规划算法 |
| executor | 校验执行请求、宿主效果和隔离 | HCL 求值、宿主选择、比赛流程 |
| store | 实际事务、锁、CAS | 不同后端各自决定恢复行为 |
| guest | 任务、文件、console 的执行与结果 | 用户自定义 workflow 引擎 |
| CLI / control | 本地交互 / 远程管理入口 | 绕过宿主服务直接修改资源 |

这些是职责划分，不要求先建一批空包。优先在现有代码上提取和合并，目录迁移与对应行为变更一起完成。

## 资源引擎的固定流程

1. 读取目标配置、操作意图和受管资源记录。Apply、Destroy、Reset 进入同一个计划模型。
2. 通过 SPI 观察。区分存在、不存在、漂移、未知；state 用来确定归属，不能用搜索结果替代。
3. 生成计划，绑定配置摘要、资源身份和 state 版本。Reset 是显式重建意图，即使配置没变也有效。
4. 执行前重新核对版本和目标。漂移时停止或要求新计划，不能扩大已经批准的删除/替换范围。
5. 每个动作先保存意图，再调用 provider，验证后原子提交动作结果与资源状态。
6. 中断后先核对原动作，再观察和规划；不能把结果未知的创建直接重新执行。

更新是可选 SPI 能力；不支持原地更新就规划替换。Replace 的先建后删/先删后建由明确资源约束决定，不能把全部资源强行套进同一个顺序。
Reset 需保留 IP/MAC 等声明身份，同时验证新旧实例和依赖；这些约束不能为合并函数而丢失。

Guest 初始化、文件安装和检查继续支持，调用独立的 guest 能力并记录结果。它们完成前不能把环境报为 Ready。
任意脚本不可自动重放；guest reboot、断连、取消请求都不等于已知退出结果。

## 当前执行路径与信任边界清单

以下是当前源码事实，供逐项切换和删除时核对。

| 操作 | 当前路径 | 权威证据 / 当前缺口 |
|---|---|---|
| validate / plan | validate 本地解析；plan → HTTP → revision/state 预览 | plan 不执行 provider refresh，不改变当前 workspace |
| CLI Apply / Reset / Destroy | CLI → HTTP 准入 → Agent → supervisor → runtime/provider | 与远程 Run 共用 revision、request key、签名授权和完成回执；无本机执行 fallback |
| 远程 Run | API → Agent → supervisor → HostRunExecutor → runtime/provider | revision、claim、grant、host/action 记录；生产 Agent 已要求 supervisor |
| Run 派生后台任务 | runtime → transport handler → host Dispatcher → helper | parent/child 身份、task digest、boot、结果；不是全部 foreground provisioner 都已统一 |
| API guest exec/file | API → Agent → supervisor → helper | 持久任务/文件记录和 host 回执；普通 raw copy/exec 是另一条实现 |
| Console | API → Agent 转发 → supervisor Dispatcher → guest task | UID65534、固定 session/node generation、签名授权、PTY/输入、cgroup 停止证明；worker 不访问 provider |
| check exec/reach | evaluateChecks → Node.Connection.Exec | 外层 Run、目标 state；检查不是无副作用的读取 |
| pause/resume | CLI/API → Agent → supervisor PowerDispatcher | request key、grant、精确 node UID/generation、同步效果及观察；旧请求只返回旧结果 |
| 受管 SSH | Run → supervisor SSH dispatcher → guest | guest CAS、签名结果；已删除 CLI 内嵌 LocalSSHController |
| adoption/transfer | plan → owning host/provider → state commit | 精确身份、reservation、proof；不能按名称接管 |
| Provider GC | preview → submit → owning host | 引用、reservation、删除证据；与缓存清理区分 |
| snapshot/metadata delete | API/CLI → repository | serial、journal、activity barrier；元数据快照不是 VM 磁盘快照 |
| 未知 intent / host fence | 专用 plan/observe/处置接口 | 原始 journal、签名观察、停止证明；旧现场需旧工具保留证据 |
| Windows QGA | libvirt → 固定 helper → Job Object | 非管理员任务、boot/epoch、ACL；raw provisioning 不享有同等保证 |
| Firecracker | provider → jailer → 降权 VMM | manifest v3、jail/进程/cgroup 身份、UID 占用与完整回收；网络沿用拓扑的 namespace |

对应入口：[CLI Apply](../cmd/sysbox/commands/apply_cmd.go)、[执行桥](../pkg/hostexec/executor.go)、
[宿主组装](../cmd/sysbox/commands/supervisor_cmd.go)、[console](../pkg/agentexec/granted_console.go)、
[检查执行](../pkg/hostexec/guest_operation.go)、[宿主 SSH](../pkg/hostexec/host_executor.go)。

当前 [Compose Agent](../deploy/docker/compose.agent.yml)以 UID 32000 运行，不挂 Docker socket、不共享 host PID，所有 capability 清空。
API 签发密钥、host 签名密钥和 guest 验证公钥各有用途；进程分开但共享所有密钥/数据库写权限不构成安全边界。

## 实施顺序

每一批都要能单独验收。开发过程中可以存在尚未接入的替换代码，但不发布新旧两种执行模式。
不先整仓改包名，不先删大量测试，也不新增另一份计划文档。

### 第一批：提取真正独立的资源引擎

**目标：测试可以直接运行资源生命周期，不需要创建 API Server、Agent identity 或 HTTP 服务。**

代码范围：

- 从 原 controlplane/plan.go（现 [model/plan.go](../pkg/model/plan.go)）提取规划所用的资源动作类型到 `pkg/model`，更新直接消费者；API wire 类型在边界转换。
- 整理 [runtime/executor.go](../pkg/runtime/executor.go)、[plan.go](../pkg/runtime/plan.go)、[convergence.go](../pkg/runtime/convergence.go)、[action.go](../pkg/runtime/action.go)：显式注入 provider registry、repository、日志与 secret resolver。
- 把 [agentexec/executor.go](../pkg/hostexec/executor.go)里加载配置、规划、执行所需逻辑归回引擎入口；claim、dispatch、API completion 留在外层。
- 引擎使用 operation ID、revision、资源范围和期限；不接收整个 controlplane.Run，不通过构造 API Server 获得存储。
- 更新 [architecture_test.go](../pkg/runtime/architecture_test.go)，逐步禁止 runtime/model 导入 API、agentexec、具体 provider、数据库驱动和调度对象。

验收先使用测试 provider：依赖顺序、no-op、漂移、陈旧计划拒绝、未知观察不删除、intent 写失败不执行、effect 后写失败不重放、同 topology 互斥。
真实 Docker 验收包括网络/节点 Apply → 第二次 no-op → Reset → Destroy，并确认旧实例和网络残留已清理。
使用隔离测试入口验证新核心；最终 CLI 不能以测试入口为理由保留直接 provider 备用模式。

### 第一批实施进展（2026-10-28）

已落地：

- 规划、健康状态、计划指纹和资源恢复观察类型已移到 `pkg/model`，所有调用方更新，不保留 controlplane 类型别名。
- [runtime.Operation](../pkg/runtime/operation.go)统一加载、规划、Apply/Reset/Destroy、结果保存及 reset continuation；不接收 controlplane.Run。
- runtime 的生产代码不再导入 controlplane，也不读取全局 provider registry。解析、规划、观察、执行、恢复都显式接收同一 registry。
- agentexec 删除重复的三套生命周期编排，保留外层锁、授权准备、检查和远程完成证明；LocalBridge 不再生成/过滤计划。
- 新增 SQLite 核心测试和真实 Docker gate。覆盖重复 Apply、陈旧计划、目标范围、intent/结果写失败、未知不重放、不同 provider 实例并行、取消与 prevent_destroy。
- Docker 隔离 daemon 中已完成带 NAT attachment 的 Apply → no-op → Reset → Destroy，确认旧/新 generation 和受管网络均不存在。

第一批完成时，资源 handler 注册目录仍在进程内共享，CLI 与部署权限尚未切换。后续 CLI 和宿主桥进展见第二批。
公开 Go 内部接口签名已改变；HTTP 字段与 HCL 保留，本轮未改 CyberField 部署或运行环境。
默认 Go 全仓测试、runtime/agentexec/model race 和 vet 已通过；真实 Docker 不代表 Firecracker/Windows 或整个新部署的验收。

### 第二批：本地与远程使用同一个宿主服务

**目标：只有一份操作受理、一份资源执行和一个最终结果来源。**

提取 `internal/host` 的提交、查询、取消、持久去重。重整
[LocalBridge](../pkg/hostexec/local_bridge_fixture_test.go)、[HostRunExecutor](../pkg/hostexec/host_executor.go)、
[API ExecutionBridge](../pkg/hostexec/environment_execution_bridge.go)及宿主启动组装。
宿主运行引擎不再通过 NewExecutionBridge 构造 API Server；HTTP adapter 和 CLI 都调用宿主操作接口。

保留受限 executor 的权限校验和实际效果记录；从宿主初始化中移除与资源执行无关的 API 服务依赖。
切换后删除本地 direct mutation、重复 Bridge 编排和生产 fallback；重复请求必须查询原结果，不创建第二轮执行。
验收覆盖 CLI/API 同语义、丢响应、断开客户端、服务重启、取消与完成并发。

### 第二批实施进展（2026-10-28）

已完成宿主执行对 API Server 的反向依赖拆除：

- Agent 和 supervisor 共用轻量 ExecutionBridge；删除单独的 HostExecutionBridge。
- 执行桥直接打开执行存储、不可变 revision 和生命周期 repository，不创建 API Server、Jobs、调度器、console registry 或 HTTP 路由，不打开 API issuer 私钥。
- 本地执行结束只更新交给宿主回执的结果；正式 Run 完成仍由 API 的 ownership CAS / 签名回执决定。
- 资源 repository pool、revision registry 准入和 SQL consumer 登记与 API 共享实现；恢复事务直接依赖 store，不依赖 Server。
- 删除无人调用的 ReconcileParentJournal 桥方法及旧入口。
- Agent 退出前等待观测、命令和续租结束；supervisor 退出前等待 Run、授权检查、结果回执和本地 child 观察器收尾，随后关闭执行存储。
- 完成上报重试由原执行调用负责，不再启动脱离服务生命周期的后台重试；观测读取状态保留调用者的取消信号。

已验证：宿主启动不读取 API 私钥、不加载损坏的 console registry；初始化失败不回退本地存储；
签名完成、Reset continuation、GC publication 和 SQL/远程 registry 合同测试。
全仓 Go 测试、相关 API race 与 vet 通过；独立 PostgreSQL 实例验证了完成回执、恢复写入时的租约/维护屏障，以及 federation 丢回执恢复。
隔离 Docker 真实链路通过完成回复丢失后的 API/Agent/supervisor 重建、冷 Reset continuation、签名 Destroy 与无 force metadata 删除。
这是同进程测试重建配合真实 Docker 的证据，不等于真实宿主断电或新部署权限边界的验收。

后续收尾已把执行存储与操作准入移到 controlstore，工作区移到 workspace，宿主执行移到 hostexec。实际部署权限验收见第四批。

退出测试覆盖取消时仍在写入的回调、等待中的准入、授权检查、监听失败、已完成但尚未上报的结果，以及断开客户端后继续执行。
等待本地处理结束不代表远端资源已经停止；没有停止证据的操作仍保持未知状态。provider 若不响应取消，正常退出会继续等待其本地调用返回，不能提前关闭存储。

本轮 CLI 切换：

- Plan/Apply/Reset/Destroy 使用服务入口；main 不为这些客户端命令初始化 provider 或读取宿主服务配置。
- CLI 上传主 HCL 和显式 `--include` 文件；`--inputs` 及其硬链接不能混入 revision。符号链接、隐藏文件和超限内容拒绝上传。
- Apply target 绑定请求指纹；Reset、Reset 续接按 key 原子保存 Run 与投递命令，重试查询原结果。
- 输入值仅经请求、claim、宿主 RPC 的内存字段传递；宿主按正式请求指纹验证后交给引擎，准入记录和 checkpoint 不含明文输入。
- 删除 CLI PrepareLocalSSH 和内嵌 LocalSSHController。LocalBridge 仍被内部测试夹具使用，生产生命周期命令不再调用它。
- `sysbox run show/wait/cancel` 查询、等待或请求取消；Ctrl-C 只退出等待。取消请求不代表停止成功。
- target 暂限未索引的根资源地址；模块和 count/for_each 实例明确拒绝，避免把错误范围当成成功。
- Plan 是所选 revision 与正式 state 的预览，不修改 workspace，不代表实时 provider 观察，也不是可保存的审批凭证。提交确认授权的是显示的 revision/操作/target；实际执行仍在宿主锁内重新核对和规划。

本轮验证：全仓 Go 测试、相关 race、vet、三文档检查通过。独立 CLI binary 在隔离 Docker 中通过 Apply/Reset 续接/Destroy、
同 key 重试与 API/Agent/supervisor 重建、非默认/敏感输入传递，以及 SSH 定向轮换和 Reset 后权限重建。
输入测试确认敏感值到达容器且未进入正式 Run/state；这不表示 provider 自身不会保留执行所需的环境变量。
这些测试重建服务对象并运行独立 CLI 进程，未模拟真实宿主断电，未验证新的部署权限或 Firecracker jailer。

### 第三批：接回完整资源与 guest 能力

pause/resume 已改为服务客户端，删除了本机 provider 路径。按 topology + key 保存唯一操作；重复请求只读原结果，
即使节点已被替换也不把旧请求重新作用到新 generation。相同 key 改变节点、操作或请求人会冲突。
创建操作与投递命令仍使用同一个事务；不增加另一份执行状态机。

本轮验证了文件、SQLite、独立 PostgreSQL 下的双 API 并发与重启去重；独立 CLI 在隔离 Docker 中完成 pause → resume → 重试旧 pause key，
确认最后一次查询没有再次暂停容器。已删掉原本仅供 LocalSSHController 使用的候选资源/指定 owner 绕路。

Console 已切换到普通用户 UID65534，复用已有 guest task 的宿主调度、guest 进程管理和停止回执。
删除独立 Console SPI、原生 Docker exec attach、SSH PTY、vsock raw console 和 Agent/provider 直连实现。
TTY/非 TTY、实时输入与 resize 使用同一任务身份；输入只经过内存和私有控制 socket，终端参数参与签名摘要。
输出按游标分块读取；超限会停止任务，不无限堆积，也不在截断后假装成功。

浏览器断连、取消、API 重启和授权到期都不能直接生成成功结果。宿主确认 cgroup 中全部子进程已停止，
API 保存签名结果后释放资源执行范围，并退役原始/取消投递。没有授权历史的会话可明确标记 `not_started`；
文件存储在会话、撤权、投递退役之间中断后，Supervisor 按持久记录继续收尾。
缺少可信 helper、cgroup-v2 domain 或 PTY 条件就拒绝，不回退普通 exec、root 终端或杀 SSH 客户端。
初始化需要的特权操作继续独立授权。Windows 交互终端明确不支持。

真实 guest 测试覆盖普通用户、TTY/管道、resize、正常退出后后台进程清理、取消、到期、输出上限和重试不重放。
隔离 Docker 的 API/Agent/supervisor 链路覆盖 70 KB 终端输出、浏览器断连、主动取消和超时，并检查每个任务 cgroup 为空。
全仓 Go 测试、相关 race、vet、独立 PostgreSQL 和三文档检查通过；验收目录保持 65 项要求、129 个 gate。
独立复核发现的续期越界、离线取消残留投递、文件存储中断后漏撤权、PTY 描述符继承均已修复并验证。
这不等于 Firecracker/libvirt 实机终端验收，也不证明部署权限或 Firecracker jailer 已完成。

**目标：新主干可实际运行现有实验环境，不只会创建空容器。**

接入 Firecracker/libvirt、网络挂载与策略、文件安装、provisioner、受管 SSH、健康检查和 console。
复用现有 provider/guest 实现；不同入口统一调用，删除迁完能力的旧 transport 和 raw fallback。
权限设计必须覆盖赛题初始化：不能简单把原 root 初始化脚本改成 nobody 并宣称兼容；需固定受权初始化操作或准备好的镜像。
保留 sensitive input 不落普通 state/log、Ready 条件、资源 inventory 和观测 label。
逐 provider 测试启动、重置、销毁、丢回复、重启和同名替代对象保护。

### 第四批：完成实际权限隔离

**目标：从操作系统层面禁止未授权的宿主访问。**

目标是非 root 控制进程与受限 executor 分工。目前 host-supervisor 仍以 root 运行；本批逐项缩小它与 Agent 的权限。
CLI/API/worker 不挂 provider socket、不持 host 私钥、不具有任意宿主执行权限。
executor 验证资源归属和参数，不能因调用者有 socket 连接权就允许任意 mount/path/root command。
Firecracker 必须由 jailer 启动并验证 UID、目录、namespace、cgroup、设备与 capability；libvirt 核对 QEMU/管理 socket 权限。

Firecracker jailer 实施范围：宿主配置固定 jailer/Firecracker 可执行文件、私有 jail 根目录和 cgroup-v2 父目录。
节点 HCL 不提供 jailer 参数或宿主 UID。启动沿用现有 manifest 和生命周期锁；先保存 jail 准备意图，
再准备本代磁盘、内核、配置及资源限额，最后运行 jailer 并核对真实 VMM 身份。
manifest 留在 root 管理目录，不能置于 VMM 可写的 jail 中。正式入口删除 allow_direct 与直接启动分支。
启动取消保留未知效果供观察；停止确认后才回收 jail/cgroup，Reset 和 GC 共用该收尾。
已接通 jailer 唯一启动入口；停止使用 cgroup.kill 并等待清空，每次启动创建新的 cgroup 内核对象。
UID 先占用再授予 TAP 访问，释放前撤销残留 TAP 权限。启动准备中断可直接清理，未知启动可恢复原进程。
宿主支持范围固定为配套的 Firecracker/jailer 1.15.1 和具备 cgroup.kill 的 cgroup-v2 环境；不自动降级。
VMM 的 PID/mount/IPC/UTS/network namespace 均与宿主隔离；连接网络必须使用 named netns，宿主 Docker bridge 组合明确拒绝。
因此 jailer 已补上 VMM 降权与文件/进程隔离，但尚不等于全部网络或宿主准备操作的隔离完成。
旧 manifest v2 拒绝读取；不在线接管旧直启进程。
真实 KVM 验证已通过启动、隔离凭据检查、暂停恢复、停止重启、冷销毁、TAP 通信、丢回执恢复、Reset 与旧 generation 拒绝。
准备阶段在 cgroup/root 创建后中断可直接销毁；UID 冲突保留原占用；销毁时残留 TAP 的 owner 恢复为 root。
全仓测试、相关 race、vet、三文档与验收目录检查通过；guest helper 的 amd64/arm64 内嵌制品已重新构建。
宿主验收使用 x86_64 KVM；不把它当成 arm64 宿主或实际部署权限收缩的验收。

Agent 权限收缩：生产 Agent 删除 ExecutionBridge 和本机 Run fallback，不打开 state/数据库、不初始化 provider。
宿主负责能力探测、资源健康和容量清单，通过已有 Unix socket 返回签名观察；API 校验宿主签名及当前 Agent session 后保存。
Agent 只持自己的通信身份和验证公钥，不保留注册时使用的管理员 API token。启用 API token 时，明确列出的 Agent 接口允许本身份签名认证，不能据此访问管理接口。
部署中 Agent 使用非 root 用户、无额外 capability、独立可写数据目录，只挂宿主通信 socket 和公开配置/公钥。
注册由管理员进行，Agent 的调度能力和标签在注册后固定；worker 无权改写。
Run、节点操作和文件操作的最终结果只接受宿主证据，暂停/恢复也已接入。worker 没有直接发起 adoption 的接口。
宿主决定观察顺序，API 验证签名和当前会话；1 分钟后健康快照过期，心跳不能续期。
部署模板包含普通用户 systemd/Compose worker、root 宿主单元，以及分开安装身份与公钥的脚本。
验收覆盖伪造/过期会话观察拒绝、宿主不可用时不回退、受限身份访问宿主私钥/provider socket 失败，以及正常签名执行链。
本批用独立 CLI 进程以 UID/GID 32000、无 capability、NoNewPrivileges 启动 Agent，API 启用管理员 token。
在隔离 Docker daemon 中验证 Apply/Destroy、pause/resume、API/宿主重建和 Agent 进程重启；原 Run 回执丢失可恢复，同 key 不重复操作。
同一身份读取私钥、canonical 数据库、连接 Docker socket 均被拒绝。观察、权限和 power 完成合同通过文件/SQLite/PostgreSQL 检查。
这不等于生产 systemd 部署已上线，也不等于缩小了 root 宿主服务内部的所有权限。
Docker 共享内核，不承诺 VM 级隔离；同 Windows 账号任务也不构成独立安全沙箱。
修复并原生验证已发现的 Windows 续期误杀和文件替换共享冲突。

验收不仅检查配置：以 worker 身份访问私钥、provider socket 和禁止路径必须失败；恶意参数、旧 boot/epoch 不能产生新效果。
任何强隔离能力缺少宿主条件时在启动前拒绝，不悄悄降级。

### 第五批：删除旧版本负担，确定新存储格式

**目标：新部署只跑一个版本、一条路径。**

把 `pkg/persistence`、`pkg/state` 中重复业务转换收敛到共享实现；存储 adapter 只处理真正不同的事务/锁/CAS。
第一批用 SQLite 验证核心，不同时重写所有后端；既有后端在新发布中是否保留，以调用方清单和合同测试决定，不能未经核对直接砍掉 PostgreSQL。

确定新的 schema 与协议版本，拒绝旧运行数据；旧格式 importer、在线兼容 reader、旧 bridge、过渡开关不进入新正常执行路径。
旧 binary/数据归档供独立清理和追溯，不成为新程序自动 fallback。
保留新版本自身的 journal、结果去重、宿主/guest 必要记录和 crash recovery。

已删除下线的 worker Run/节点/文件完成方法、请求类型和存储分支；完成结果只接受宿主签名。
删除由 Run 完成回调写入健康 projection 的旧机制，资源健康只来自签名观察。
已把 cleanup receipt、提交失败、旧执行身份拒绝、命令重派发和回执重放测试迁到宿主路径；
有效历史宿主回执不要求 worker lease 仍有效，但不能修改已封存结果或影响新一代资源。
文件、SQLite、PostgreSQL 共用同一结果规则，后端只保留各自事务与锁实现。
执行桥不再包含可选的 worker 结果上报器和重试器；宿主 RunCoordinator 负责签名回执、重试与恢复。
文件命令退役会清掉可投递内容，迟到回执使用操作记录内的原始 dispatch 校验请求，不依赖已清空的命令。
清理凭据额外绑定宿主授权的拓扑 UID；不匹配时不发布结果、不释放占用。
全仓测试和相关竞态测试通过，PostgreSQL 与文件/SQLite 使用同一套签名回调验证。
隔离 Docker 验证了文件命令实际退役、宿主/API 重建后回执补交，以及普通用户 Agent 的部署、暂停、恢复和回收。


已确定退出的跨宿主迁移/处置接口显式移除或拒绝，不能留下只返回成功的空实现。

### 第六批：CyberField 联调与停机重建

**目标：真实消费者的比赛环境能正常部署、初始化、重置和清理。**

以相邻 CyberField 的 `api/sysbox.v1.yaml` 与 `internal/adapter/sysbox` 为消费清单：
revision、keyed Apply/Destroy、Run 查询、三值 conditions、cleanup-inspection、metadata DELETE、IP/inventory、Docker labels。
比赛业务留在 CyberField；Sysbox 不吸收审批、计分和比赛轮次 workflow。

当前两边 protocol 名称不一致：CyberField 要求 `cyberfield-runtime-v1`，Sysbox dev 返回 `sysbox-runtime-v1`。
联合更新契约与启动检查，不通过关掉能力检查“解决”。赛题用到的初始化命令/文件/flag 注入必须真实验证。

同一提交构建 CLI、API、宿主与 guest 制品；隔离环境先完成 Docker、Firecracker、libvirt 与 Windows 适用测试，
再跑 CyberField 发布 → 激活 → 初始化/flag → Ready → Reset → 清理，并测试请求丢失和组件重启。
最后按确认的部署范围停旧服务、清旧资源、归档旧数据、部署新版本。不做旧运行环境在线接管。

## 联调前收尾计划（2026-10-29）

按已确认的顺序完成结构、兼容清理、宿主边界、发布验收，再进入 CyberField 联调。

- [x] `pkg/controlstore` 承接文件/SQLite/PostgreSQL 的完整存储、准入和恢复事务；API 只调用共同操作。
- [x] `pkg/workspace` 统一 revision 物化、目录清单、placement 与 GC 发布保护；宿主与 API 共用。
- [x] `pkg/hostexec` 承接宿主执行、Bridge、观察和恢复；`pkg/agentexec` 只保留通信与授权转发。生产依赖不允许宿主模块反向导入 API。
- [x] 移除旧格式导入和测试专用本地执行的生产入口，统一当前格式的写入与校验，保留旧格式拒绝及证据保护。
- [x] 宿主在初始化 provider 前验证配置与身份，固定辅助程序/环境；Firecracker 拒绝共享宿主 netns，systemd 声明必要权限上限。完整 systemd 部署仍需单独验收。
- [ ] 验证事务、失败恢复、普通用户边界和真实 Docker/Firecracker/libvirt/Windows 适用路径；缺环境的项目明确记为未验收。
- [ ] 整理本地提交，复核 origin/main，统一生成 Linux amd64/arm64 与 Windows 制品及来源摘要，执行重复构建与制品验收。

每一项都保留原子写入、未知效果不重放和原始资源身份校验。任何环境切换在 CyberField 联调之后进行。

### 当前验收记录（2026-10-29）

代码结构与格式清理已经提交到本地 dev。API 不再实现 SQL 和宿主执行环境；
生命周期数据只接受 format 16，API schema 为 15，不提供旧格式在线兼容。
Agent 已去掉对宿主执行包的直接依赖，但仍通过共享包间接依赖部分执行代码，不能宣称二进制已完全拆开。

| 范围 | 已验证内容 | 尚未覆盖 |
| --- | --- | --- |
| 存储与恢复 | 全仓 Go 测试、vet、相关 race；文件、SQLite、独立 PostgreSQL 的事务与恢复 | 不能代替全部真实 provider 故障矩阵 |
| 普通用户与 Docker | 隔离 Docker 中 UID 32000 的部署、暂停、恢复、回收、重启和回执丢失恢复；实际文件安装 | 最终发布候选仍需核对制品来源 |
| Firecracker | 真实 KVM 下的 jailer 隔离、named netns/TAP、重启恢复和冷清理；相同 systemd capability 上限及 NoNewPrivileges 下复测通过 | 完整 systemd 单元、arm64 宿主及最终发布 helper 的实机验收 |
| Libvirt | 隔离 daemon 下的资源归属变更、丢失返回后的恢复和冷读取；真实无盘 VM 的网络身份检查、旧 generation 拒绝、暂停、恢复和回收 | 完整 guest 初始化、重置及部署隔离验收；测试容器关闭了 libvirt 私有 mount namespace，不证明该隔离配置可用 |
| Windows | Server 2025 基础镜像安装及 QGA 验证通过；使用提交 `63db456` 的发布 helper，通过 5 项原生测试及真实 libvirt/QGA 任务矩阵，覆盖回执丢失、取消、guest 整机重启、旧 boot 拒绝和宿主签名的未知结果停止回执 | 本轮未重启整个 Linux 执行宿主；未覆盖 Windows 10、域环境或完整 systemd/libvirt 部署隔离 |
| 发布 | Linux amd64/arm64 与 Windows amd64 归档校验；重复构建一致；GitHub Release 模拟审计通过 | 未创建正式 tag，未推送或发布；源码变化后需重新构建 |

发布测试的 Git 替身已修正：转发使用事先保存的真实 Git 绝对路径，避免清洁工作区检查递归调用自身。
本轮只操作独立验收容器，没有更新 CyberField 服务或接管其现有资源。

2026-10-30 在独立 Linux 虚拟机中补测正式 systemd 单元：libvirt 的 Apply、Reset、
Destroy 闭环通过；首次启动和 Reset 后均确认 cloud-init 完成、普通用户 SSH 可登录，
Reset 保持资源 UID 并递增 generation，回收后新拓扑的 domain 和磁盘目录均不存在。
测试发现并修复了 `UMask=0077` 下 Reset 目录缺少 QEMU 遍历权限的问题；
provider 磁盘使用单独的 `/var/lib/sysbox-libvirt`（root 所有、0755），不放宽宿主私有目录。
0077 环境下的 libvirt 包回归测试和 vet 通过。该实机复测使用工作区修复版本，尚非重新发布的制品。

netns 入口改由现有 `sysbox-netns` 的 root 专用辅助入口管理：它仅在 PID 1 的
mount/net namespace 中创建、删除 `/run/netns` 下的命名入口，不执行资源规划。
辅助程序没有 setuid，不接受任意宿主路径；删除要求 namespace 类型及 device/inode 匹配。
Provider 的资源身份、链路归属校验和宿主锁仍是操作前提；创建后服务不可见时直接报错，
没有进程内创建的备用路径。正式 systemd 防护设置保持启用。
创建和删除在宿主固定入口锁下串行化；实际执行 `ip` 的子进程继承锁 FD。
执行器、辅助程序和子进程之间设置父进程死亡终止信号，避免服务退出后旧操作继续执行。

服务重启保留入口并重新观察原对象；宿主内核重启不保留 netns，必须按真实资源状态处理，
不能用同名新 namespace 冒充恢复。PID 1 必须属于管理这些资源的执行宿主，
辅助程序须在该宿主同一路径安装；容器内的 PID 1 不能自动代表外部宿主。
Plan 仍是 revision 与 canonical state 的预览，不声称执行 live provider refresh；
重启验收另外核对内核 namespace 身份和 guest 连通性。

独立虚拟机中的正式 systemd 验收已通过：网络资源 Apply → Plan 无差异 →
重启服务 → 原 namespace device/inode 不变 → Plan 无差异 → Destroy 无入口残留；
带 libvirt guest 的同一路径也通过，并验证重启服务后仍能访问原 guest、Reset 后旧文件标记消失、
Destroy 后 domain 和磁盘目录不存在。错误 namespace 身份删除被拒绝；辅助程序退出时
子进程停止、锁保持至子进程退出的中断测试通过。该结果属于工作区版本，尚未生成新发布制品。

Firecracker 的正式服务测试因缺少配套 jailer、下载连接失败尚未运行。
前两次失败拓扑及其未知效果记录保留在独立测试环境中，不计作成功回收。

## 怎样判断这轮重构完成

- 看一个 Apply，只需沿宿主操作服务 → runtime → SPI 理解执行，不再绕回 API Server。
- 新增 provider 主要增加 SPI 实现与注册；新增接入方式不修改资源执行规则。
- 本地与远程不存在第二套资源执行实现；各类事实有明确的唯一权威写者。
- 同一请求不会重复创建资源；未知效果不会重放；删除不会碰同名替代对象。
- 初始化失败不会报 Ready，清理未完成不会报告资源已释放。
- 安全隔离通过实际权限测试，不能仅以签名、接口或进程名字证明。
- 可从空状态部署，无旧格式读取和旧模式回退；新版本自身中断后仍可解释和恢复。

代码行数不是唯一验收标准。必要的错误处理和真实故障测试不删，重复路径、重复决策和不再支持的兼容代码才删。

## 历史资料

原 84 份文档逐字节保存在 [归档](archive/2026-10-27-documentation.tar.gz)，
[清单](archive/2026-10-27-documentation.json)记录原路径、提交、大小和 SHA-256。
归档是历史证据，不覆盖当前三份文档；其中引用的 `/tmp` 日志、VM 磁盘和远端文件并未包含在文档归档中。

历史 Stage 2/3 验收仍属于原版本和其声明范围，不自动证明新架构通过。
原 P0–P9 测试目录继续保留；新范围确定退出某项能力时，需要明确修改契约和对应验收，不能把 skip 当作通过。
只维护本文件、接口文档和根 README；本轮计划直接在本文件更新，不再新增阶段计划手册。
