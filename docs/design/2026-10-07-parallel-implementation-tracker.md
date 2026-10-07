# P0–P9 并行实施：集成跟踪

当前已提交集成基线：`566268e`。本文记录并行集成检查点，不能作为P0–P9全部验收通过声明。

| 阶段 | 已进入代码的内容 | 未完成的验收/接入 |
|---|---|---|
| P0 | 强制按Go依赖重建guest helper、amd64/arm64构建、独立cgroup验收目标 | 全能力矩阵、最终产物与总验收 |
| P1 | authenticated revision、独立Agent物化、artifact来源约束、续租固定快照 | 真正双宿主验收；未把独立目录当双宿主 |
| P2 | authority ledger/session/epoch、签名grant及stop receipt API、当前command事务绑定；guest task、API文件安装和远端pause/resume接入独立host supervisor、可信停止回执与旧epoch重试墓碑 | 普通Run/lifecycle/provisioner/foreground等其余effect全部接入、host持有唯一provider权限；真正跨宿主验收 |
| P3 | 持久task、sealed memfd、cgroup+pidfd、monotonic guard、续租、分页输出、SSH/vsock通用SPI；真实SSH和Docker supervisor任务接续验收 | 完整Agent服务重启接管验收；全执行路径统一隔离 |
| P4 | session+sequence observation ledger、guest活动枚举、receipt失效 | 全API迁移回归、完整lease撤销/guest取消/hoststop集成 |
| P5 | adoption SPI、journal动作、Docker不可变ID sidecar；只读 `/adoption-plan`，agent-authenticated `/adoption-confirm` 绑定 evidence/fingerprint/serial/generation/agent，生成 `adoption_apply` command；宿主 AdoptionCoordinator 复用 effectgate intent/Observe，并接入 Agent grant/RPC/provider Apply+Observe callback；host key completion endpoint 绑定 command lease，并通过 `CommitAdoptionEvidence` 在 ActionRepository CAS 事务中写入 canonical state；libvirt 与 Firecracker provider 均提供 fingerprinted XML/manifest Inspect/Apply/Observe SPI；真实 Firecracker/KVM（6.84s）和 libvirt domain（2.87s）generation/adoption E2E 已通过并清理测试资源 | 多表/同库 receipt 与 action outcome 的统一数据库事务、真实 provider 冷重启后接管验收 |
| P6 | aggregate format2快照、元数据restore monotonic serial、身份约束、API CRUD；restore/force-unlock 共用活动操作检查，活跃 Run、节点操作、guest task/file 或未上报 host stop 时拒绝维护；force-unlock 还拒绝 live state lease，缺少完整活动证据时 fail-closed；Local/SQLite/Postgres restore 统一校验 topology/resource incarnation | grant撤销与freeze、force-unlock 审计回执、恢复较旧资源集合的显式reconcile、后端同一事务内 CAS restore |
| P7 | managed Docker bridge导出/核验、FC/libvirt NIC消费；受管 `ssh_access` 配置合同、规范化摘要校验和 plan-time key/address/port validation；宿主 SSHAccessCoordinator 绑定 grant/effectgate，socket 已支持 apply/observe/delete，local runtime 已能写固定 drop-in 并读回；Agent node-operation 已能申请 SSH grant 并转发 host RPC；新增 API `/ssh-access` 受理并绑定 resource UID/generation；SSH-only supervisor 路由 race 已覆盖；真实独立 sshd E2E 验证受管第二把 key 登录 helper、删除 drop-in 后失效 | `ssh_access` owning-agent 与 API 派发的完整终态 completion、host file-install/grant 的实际授权绑定，旧 key 只撤销自身配置；共享 sshd drop-in 生命周期、网络自动重接、混合netns单VM支持边界 |
| P8 | 依赖合同已明确；GC dry-run 现在把 canonical aggregate snapshots 作为资源引用，快照仍引用的资源不会进入候选；新增 host GC delete coordinator，candidate identity 绑定 inventory digest 并复用 effectgate 的 intent/unknown/Observe | CLI 自动GC授权删除、删除journal、provider Delete 接入及重验 epoch 尚未实施，不算完成 |
| P9 | 分项真实Docker bridge/TAP、private cgroup、KVM测试 | 双宿主、全故障矩阵、升级/main整合，不算完成 |

## 当前收敛规则

libvirt adoption 恢复状态修复：不再返回只有 UUID 的简化 handle，改从真实 domain
metadata 解码完整 provider state，并校验身份、UUID、受管 VMDir/DiskPath。
回归覆盖丢失/篡改恢复坐标拒绝。真实 diskless libvirt VM E2E 已验证接管后
序列化/冷重建 handle、观察 paused、resume；provider race 与 vet 通过，测试 domain
已清理。此 adapter 仍是已有 managed metadata 的同 ownership 恢复，不表示无主
外部 domain import 或跨 owner 转移已经实现。

Docker adoption 冷观察修复：`ObserveNode` 与生命周期操作共用 ownership proof，
支持已验证的 sidecar，不再要求被接管容器拥有不可变的 Sysbox labels。新增真实
`make test-docker-adoption-e2e`：创建无 labels 容器、接管、序列化/重建 provider
handle、观察、旧代拒绝、暂停/恢复、销毁均通过；provider 全包 race 通过。
测试容器已销毁。这里的冷恢复是重新创建 provider 对象，不代表跨宿主或完整 API
接管流程验收。

adoption 启动入口修复：Agent 之前复用 power 的 host-info，要求未被接管的节点已
存在于 canonical state，因而无法开始真正接管。新增只读 `adoption-info` 返回
配置的宿主 boot，Agent 改用该入口；此查询不产生授权。apply/observe 同时验证
外层 TargetRef 与签名请求中的 topology UID、节点和 generation 一致。真实 Unix
socket 的 Agent→adoption-only supervisor 回归通过，provider 使用测试回调，
不将此测试当作真实资源或跨宿主接管验收。

2026-10-08 已补跑独立 Postgres 16 临时实例：adoption 退休 command 完成、证据篡改
拒绝、原回执重试、后继 epoch 和 grant recovery 的 Postgres 子测试实际执行并通过
race，未跳过。共享 NodeOperation transition 新增 Adoption/SSHAccess 请求不可变
约束，防止状态 CAS 改写已派发 payload；失败回归修复后相关三后端测试通过。
临时 tmpfs 数据库已精确清理。本次验证不覆盖双宿主或完整 provider 接管 E2E。

adoption 退休 command 恢复：新增共享 `CompleteAdoptionFromHost` transition，三套
API 存储复用原 command/operation 事务执行。验证 host signature 与持久 authority
后，按原 command ID、请求 digest、topology/resource UID/generation 核对；不再
要求旧 worker 的 lease 存活，也不借用后继 worker 的 lease。退休 command 已删除
payload 时使用保留的 operation 请求与 tombstone 身份交叉验证。普通 worker 的
adoption completion 被 repository 明确拒绝。文件/SQLite HTTP 回归覆盖退休后
完成、重试与后继 epoch 保护；Postgres 本批未配置，不计验收通过。

adoption API 授权释放：专用完成接口先验证回执、提交 canonical evidence 与操作
完成状态，最后 `AcceptHostStop` 释放原 epoch。普通 stopped 接口拒绝 adoption，
防止绕过证据提交提前释放 scope。文件/SQLite 回归覆盖完成失败仍阻塞后继、重试
完成后放行新 epoch、旧回执重放不撤销后继授权。本批 Postgres 未配置，未计为通过。
这仍是可重试的分步持久化，不是多表原子事务，也未解决过期/退休 command 的接管。

adoption 自动回执恢复已接入 supervisor 启动与周期扫描，共用 effectgate 的
completed/pending-report 枚举。先读取持久结果，再补签或复用原回执；不请求新的
worker 授权，不执行 provider callback。回归覆盖 outcome 已提交但尚未签回执、
回执回应丢失、重报成功后跳过、unknown intent 不签停止证明。API 侧授权释放与
跨独立进程的完整接管验收仍待完成；这不是 P5 完成声明。

adoption 宿主回报顺序修复：provider callback 仅返回 codec evidence，不再在 effect
完成落盘之前调用 API。协调器复用 effectgate 持久化 outcome、签名回执和已报标记；
Observe 在宿主对象重建后重报同一签名回执，不重新执行 provider。缺少 host key
或 reporter 时在 effect 前拒绝；历史读取校验签名和完整 scope/action/agent。
已覆盖丢 API 回应与宿主 boot 变化后重报。自动扫描、API 授权释放及独立进程端到端
仍未完成，不能据此关闭 P5。

adoption 回执补强：新增签名 `evidence_digest` 绑定 provider codec 输出，API 在
写 journal/state 前校验 evidence、完整派发请求摘要、command/topology UID 和持久
authority。普通 stop 回执不再可用于 adoption 完成；旧缺少 evidence 绑定的回执
明确拒绝。宿主使用 `MarshalProviderState` 输出 private state，而非整个 NodeHandle。
HTTP 回归覆盖篡改 evidence、未签发 epoch 拒绝与原回执幂等提交。此项未解决
宿主先持久化回执再报送、授权释放和跨服务重启的完整闭环，P5 仍未完成。

2026-10-08 adoption 元数据重试修复：`CommitAdoptionEvidence` 以稳定 action identity
定位持久 journal，用包含原 expected serial、inspection evidence 和资源内容的摘要
核对重复请求；本地接收时间不影响摘要。原始请求在 intent 后中断或 outcome 回应丢失
后可冷重开 repository 重试，沿用首条 candidate，不新增 action 或递增 serial。
已有同名/同身份资源但没有对应成功 journal 不算完成证据。文件与 SQLite 的故障
回归通过；API 不再关闭服务器共享的 repository。此项不代表 adoption 完整流程已验收。

停止继续扩散接口，优先恢复全仓绿色并完成独立审查，再形成检查点。测试fixture迁移必须显式创建session/sequence，不得让生产代码兼容缺失证明。所有失败保留原因；真实环境缺失和skip不算通过。

## 普通 Run 宿主迁移：正在实施

下一切片固定为单资源 apply/destroy：`RunRequest` 只传不可变 revision、action、
resource UID/generation、expected serial 和 target 身份，不接收 provider 参数或
主机路径。`RunCoordinator` 先写宿主 intent，effect 返回丢失时只读同一 run/action
记录，不重新调用 provider。当前只有合同与 journal 基础测试通过，尚未接入
canonical resolver/runtime executor、API run grant/receipt 和 Agent 远端路由，
不能计为普通 Run 迁移完成。Run-only supervisor 的 start/observe 路由和
effectgate 绑定已通过幂等、签名和未知结果测试；canonical runtime executor、
API grant/receipt CAS 和 Agent `run_assigned` relay 仍待接入。当前 Run command
模型缺少单资源 UID/generation，不能直接套用 guest/node grant；下一步必须先在
API 派发合同中补齐目标资源证明，再接宿主 executor。

## 远端节点 pause/resume 集成

沿用已批准 P2–P4 设计，将远端节点电源操作接入同一个 host supervisor。
不把整个 Run 作为一个不可观察的 RPC，也不在 worker 保留电源操作回退。
单机 CLI 的显式本地执行不在本批迁移范围。

- [x] `controlplane/node_power.go` 定义不可变 Topology/Node/Operation 请求摘要；
  API 只从持久 command 签发 `node_power` grant，绑定宿主 boot、session、attempt、
  完整 resource scope。`node_operation_service.go` 补齐派发 TopologyUID。
- [x] `effectgate` 沿用已有 intent/complete ledger，增加可信停止回执持久化、重报及
  完成结果只读恢复。旧格式明确重建。仅同步 provider 成功且真实 ObserveNode
  核对目标身份和 paused/running 状态后写 complete；错误或 crash 留 intent，
  不以当前状态看似正确推断 daemon 已无在途调用，不重放、不签停止回执。
- [x] `hostsupervisor` 拆出无 guest connection 依赖的 canonical node resolver；
  新增 host info/power apply/power observe 固定 RPC，并由常驻恢复循环补报回执。
  worker 不可指定外部资源 ID、driver、连接参数、回调或路径。
- [x] `agentexec` 远端 node operation 必须签 grant 并使用 socket。丢响应只查询
  同一 action 的持久结果，不重新执行 pause/resume；只有 stop 回执确认后报成功。
- [x] 单测先覆盖缺 proof、摘要篡改、过期/旧 session、intent 不重放和不签 stop，
  再实现；运行真实 Docker pause→丢回复→冷读取→resume、后继 epoch、旧代拒绝
  验收，相关 race、全仓测试、独立审查后形成检查点。

真实 Docker 正常/不健康容器完成上述链路，既有 guest task E2E 回归通过。
真实 libvirt 验证 pause/resume 的真实 domain XML 身份及旧代拒绝。复审发现并修复
Docker 健康状态覆盖生命周期状态、libvirt 观察结果缺少 ownership 的问题。
真实 Firecracker/KVM 验证宿主 dispatcher 暂停/恢复、冷读取历史结果不重放、旧代
拒绝，且整个电源流程不建立 guest connection；测试 VM 已销毁。
启用真实 Postgres 的全仓和 API/effectgate/hostsupervisor/Agent race 均通过；
provider 修复另有 Docker/runtime/libvirt race 验证。

不可取消 daemon 调用的未知结果保留为阻塞项；通用外部 daemon 的请求追踪与
可验证强制 fencing 尚未实现，不能把本批描述为所有生命周期恢复已经完成。

## 受管文件安装集成

固定安装协议取代远端 `CopyToNode` 拼装调用：安装 ID、guest 绝对路径、明确 mode、
size、SHA256 进入签名摘要；下载地址仅是运输信息，不作为 guest 执行参数。
supervisor 从自身配置的 API 获取 payload，独立校验后传给受信 helper。

guest 只允许写入管理员配置的安装根，拒绝符号链接路径、目录目标及信任根所在
路径。安装和任意命令 task 共用资源 scope/epoch 检查。文件安装是固定代码执行
的特权操作，不允许以 root 运行 worker 提供的命令。取消与 commit 使用同一锁。

同目录 staging inode 保留 root 私有 hardlink anchor，先持久 commit intent，
再 rename、fsync 目录、持久 committed。rename 后崩溃时，只有父目录身份、
anchor/目标 inode、size/hash/mode 全部一致才能恢复 committed；源仍存在且尚未
提交则停止并记 confirmed cancelled；其他情况为 unknown，禁止盲重放和释放 epoch。

API 取消只记录请求并撤销授权，不立即标 cancelled 或删除 payload。宿主签名的
最终安装结果区分 committed 与 confirmed cancelled；取消晚于 commit 时保留
committed。仅凭 worker 的空 Error 不再完成文件操作。旧存储格式明确重建，
新增 guest 安装目录策略由可信部署配置提供，不由 worker 提供。

实施分工：guest 固定安装引擎与共享 scope；SSH/vsock/Docker/helper 传输；
API/repository 签发、取消与宿主最终结果；host supervisor 下载、持久恢复及 Agent
路由。验收覆盖 rename 前后 crash、取消竞争、旧 epoch、控制路径保护、真实
Docker/SSH/vsock，以及三存储后端，不把仅有接口或 mock 通过计为完成。

固定 guest 引擎、共享 scope、SSH/vsock/Docker 传输、宿主下载/回执/恢复、Agent
路由与 API 三后端终态规则均已接入。取消采用独立持久标记和短 commit 锁，
复制中的请求能阻止 rename；迟到 Start 被取消墓碑拒绝。真实 SIGKILL 覆盖
commit intent 前后和 rename 后恢复；历史 committed 不受后继覆盖影响。
真实 Docker/API 与 SSH 安装超过 8 MiB payload，校验实际内容和 mode、冷恢复、
丢回执重报及撤销请求拒绝。最终全仓启用真实 Postgres 通过。
真实 KVM/Linux5.10 上的 vsock 10 MiB 安装、冷读取、实际校验和/权限、旧代拒绝、
提交后取消与启动前取消墓碑均通过；amd64/arm64 guest-init 已按当前源码重建。
同时修复 Postgres guest command 标准化、错误 Agent 请求误删 payload、活动安装
payload 过期清理，以及 Firecracker reaper 导致 procfs read 返回 ESRCH 的退出竞争。

## 首批新增验收

- 单独API与Agent目录的真实Docker完整Run、回执丢失与Destroy测试已运行；仍在同一宿主。
- Docker managed bridge + TAP证明、旧代拒绝与同名替换保护已运行。
- private cgroup任务测试覆盖去重、冷读取、进程树取消、supervisor退出、expiry、renewal、输出限制和Linux5.10 freeze/pidfd路径。
- KVM5.10 guest任务已实测启动/去重/退出结果/输出及异步取消确认；继续验收payload隔离和真实SSH helper。
- API全量测试在observation ledger迁移后曾失败；已经修复生产读写路径与测试fixture，最新启用真实临时Postgres的`go test ./...`全仓通过。
- 真实SSH独立sshd/私有cgroup E2E通过；当前guest在KVM Linux5.10上的持久任务E2E通过。任务以签名指定的非root UID执行，默认65534、NoNewPrivs与零有效capability；不允许任意root payload绕过supervisor。
- 复审修复了renew与旧超时竞争、Cancel在副作用前完整身份校验、不同resource的epoch互相误拒绝，以及snapshot历史属性不能盲目还原真实effect目标。

schema变更：API9、aggregate2、文件metadata2、guest scope2、effect ledger2；旧格式明确重建。它们必须在最终capability和配置样例中一起更新后才能交付。

## 独立 host supervisor 集成

API guest execution 不再由 Agent 自行打开 provider connection：缺少 supervisor
socket 会直接失败。新增 `sysbox host-supervisor`，独立校验 canonical resource
和在线授权，持久化启动意图，并在确认 guest 停止后签名回执。签名 grant 固定
guest boot ID；历史 grant 检查、旧 session 撤销和停止回执重放均有明确接口。

配置、身份、密钥、socket、任务日志及文件/SQLite canonical repository 必须满足
可信目录及权限要求。Postgres 的角色隔离仍需部署者配置。这不是整个 Agent 的
权限隔离完成声明：普通 Run、其余生命周期、provisioner 和 foreground 路径仍待迁入。

可重复验收入口：`make test-host-supervisor-e2e`。使用真实 Docker task、Unix
socket 和私有 cgroup，覆盖停止回执丢失、API 与 supervisor 重建后重报、Agent session
替换导致运行中任务自动取消，以及确认停止后的后继 epoch。这里的服务重建是
测试内重新创建服务对象，不等同于双宿主或完整 Agent 进程重启验收。

本轮已修复 Agent 单次输出读取超过 guest 64 KiB 协议上限的问题，以及 supervisor
listener 异常退出时等待 recovery goroutine 导致挂起的问题。集成全仓测试使用
真实隔离 Postgres 通过。独立审查发现的旧授权结果误取消并发续租任务、慢记录
阻塞后续回执恢复均已修复并通过 race 回归：取消前重新比对完整 grant，已签回执
不再依赖在线授权，每条记录拥有独立超时。复审未发现新的阻断问题。

2026-10-07集成检查点：真实Postgres启用的全仓测试通过；API/persistence/Agent/runtime/guesttask/effectgate/transport/四provider相关race通过；vet与docs检查通过。最终重建amd64/arm64 helper后，真实KVM5.10持久任务去重、冷读取、输出、取消确认通过（10.04秒）。这证明当前基础可集成，不代表尚未挂接的host supervisor、P5/P6/P7功能和P8/P9已经完成。
