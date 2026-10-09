# P0–P9 并行实施：集成跟踪

初始集成基线：`566268e`；后续进展按下方条目记录。本文不能作为P0–P9全部验收通过声明。

P6屏障与recovery写入接合：恢复提交在共同锁/事务中核验maintenance ledger，冻结时
拒绝state/journal变更；Postgres额外锁authority行，缺失行先初始化，空损坏记录拒绝。
真实独立Postgres、文件、SQLite的冻结拒绝/准入竞争回归与文件/Postgres锁竞争通过。
这补上显式recovery metadata路径，完整restore编排和其余writer冻结仍待完成。

P6维护屏障基础：授权ledger format2内持久化维护ID/摘要/epoch，Begin原子冻结新grant
并撤销父子授权，End等待全部已确认停止；重试、冷启动、后继屏障和其他拓扑隔离回归
通过。format1明确拒绝并需停旧writer后重建，现有双VM旧数据未动。restore受理/提交/
续作/释放编排、metadata全局冻结与升级演练仍待接入，不能据此关闭P6。

P6恢复前置检查补充：restore与force-unlock统一要求Run/node/guest完整活动注册表；
snapshot恢复拒绝lineage、ResourceType、SchemaVersion变化，避免当前provider私有数据
被旧schema重新解释。文件/SQLite与API失败回归已补；独立控制进程的冻结/撤销/恢复
协议及不同资源集合的显式reconcile仍未完成。

2026-10-09恢复metadata提交原子授权：宿主recover现要求绑定command租约与Run身份的
repository；文件共同锁、SQLite/Postgres同事务校验command/Agent/Run后，复用共享CAS
提交journal/state。真实独立Postgres与文件/SQLite的退休、过期、禁用Agent、活动后继等
拒绝回归通过；文件/Postgres实际锁竞争和等待期间过期也通过。全仓启用Postgres测试通过。
这关闭显式Run恢复单次outcome的授权竞态，不关闭全局maintenance freeze与daemon恢复。

2026-10-09显式Run观察/恢复路由：Agent不再直接读canonical repository或调用provider；
仅转发command租约凭据，宿主独立向API授权并复用原观察引擎。缺socket无本地回退。
真实Docker race在worker独立空SQLite下通过远端UID/generation观察和完整清理；recover
socket测试证明canonical revision驱动journal/state收敛且不重放副作用。长租约和大revision
传输限制已修复。全局周期inventory/console隔离、撤销与metadata提交的原子事务、独立
双VM观察验收仍未据此关闭。

2026-10-09真实provider总门槛：已把旧Docker Run故障夹具迁入生产共用HostRunExecutor、
Unix supervisor和签名host completion。`make test-p9-real`全项通过：文件安装、节点power、
guest task、Run丢回执恢复、Docker adoption、Firecracker生命周期/reset/跨进程/crash、
libvirt代际。这不代替完整P9故障矩阵，也不关闭read-only inventory的worker凭据隔离。
两架构guest helper已按当前源码重建；详情见[验收记录](../operations/p9-real-acceptance.md)。

2026-10-09数据库故障验收：在已有resource checkpoint、600秒后台任务运行时停止
隔离控制VM的PostgreSQL；guest在138.85秒后cancelled、原cgroup清空，数据库重启后
原子任务epoch34回执获确认，父Run收敛failed，普通Destroy/独立无残留/非force元数据
删除全部通过。停止可能等待剩余授权耗尽，并非秒级。首轮错误的45秒验收窗口失败
证据保留且测试资源已正常清理。这只关闭该Docker Run的DB故障场景，完整P9仍开放。

2026-10-09最新进展：资源执行成功后封闭子任务准入、持久checkpoint与Run执行flock
已接入；独立supervisor可在证明旧callback退出、全部子任务停止并上报后收敛失败。
双VM实测SIGKILL运行中的supervisor，4.95秒内冷恢复失败结果，随后普通Destroy、
独立无残留核验和非force metadata DELETE通过。慢子任务使用独立恢复预算。
仅有intent且未到资源checkpoint的旧记录、不可取消daemon恢复、长期服务迁移及
P5–P9其余缺口仍未关闭；详见[验收记录](../operations/p9-real-acceptance.md)。

最新双 VM 验收进展：控制 VM 的双 API/HAProxy/Postgres 与执行 VM 的非特权
Agent/root supervisor 已部署，无共享工作目录。真实 Docker Apply→Destroy→
独立 provider 无残留核验→普通 metadata DELETE 两轮通过，第二轮在主 API 停机、
备用 API 服务期间执行。细节和证据见[双 VM 验收记录](../operations/p9-real-acceptance.md)。
这不关闭活跃 Run 中途切换、长操作续租、未知结果恢复及其余 provider/功能故障矩阵。
首次三轮失败证据保留，其中第三轮的资源/journal仍待未知结果恢复，不能算全部清场。

已完成 Run 的回执恢复进一步通过真实双 VM 故障验收：仅阻断 host completion
回报，确认宿主 durable complete 后重启 Agent/supervisor；恢复同一外部容器身份的
原结果，再完成 Destroy、独立 provider 无残留核验与普通 metadata DELETE。
Run 回执现在签署结果摘要，API 支持原 command 退休/重新投递、lease 到期后的
可信完成；实际 Run attempt/owner 变化仍拒绝旧回执。文件/SQLite/真实 Postgres
竞态测试和复审通过。详情见上述验收记录；未知 intent 和长任务续租仍未关闭。

后续异步父 Run 已接通：持久受理后立即返回、Agent轮询与签名续租、独立单调
deadline watchdog、周期在线授权，以及按同一执行身份的取消/冷读取。旧同步
Run RPC已删除。双VM45秒foreground provisioner实测Apply约47.28秒，同epoch
续租持久化、Destroy和非force元数据删除通过。后台派生guest任务续租、未知
intent恢复、运行中故障矩阵及P5–P8剩余能力仍待完成，不代表整个P9关闭。

派生 guest 授权已补持久 `parent_epoch`、幂等派生、显式有界续租接口、父撤销联动
和 guest 续租父身份不变性。父 Run 终态提交前会原子确认子任务全部停止并冻结
父授权，防止新派生插入提交窗口。重复请求、签名父期限、并发停止顺序和时钟
回退回归通过；持续子任务调度/续租与跨VM子任务验收仍未接入，不计为完成。

后续宿主Run子任务已接入持久Dispatcher、持续观察/续租、有限任务退出验证及
有界取消；任务记录保存父Run来源，冷恢复可核验同Run未完成candidate。慢启动
不再持有管理器锁阻塞取消；未响应provider保持unknown。相关race/复审通过。
真实跨VM后台子任务、长期服务跨Apply存续、未知Run intent恢复仍未验收关闭。

后续真实跨VM有限后台子任务通过：45秒任务的父/子epoch均续租，子任务已知
exit0与停止回执先于父Run终态，随后Destroy和普通metadata删除成功，执行VM
独立验证该owner无残留。修复了创建中candidate未提前持久化、state/action拆分
读取产生发布窗口误判，以及父Run结束时间未包含子任务等待的问题。早期失败
round仍留有未知记录/资源；长期服务、故障取消和未知intent恢复没有据此关闭。

后续API失联故障收敛：主资源流程已成功返回且全部子任务停止回执确认时，宿主
可持久提交原Run的失败结果，释放授权供正常Destroy。双VM新轮次已通过停止
确认→父Run failed→Destroy→独立无残留核验→普通metadata DELETE。旧历史
intent缺callback退出证明，仍保持unknown，不自动补造完成；完整恢复目标未关闭。

| 阶段 | 已进入代码的内容 | 未完成的验收/接入 |
|---|---|---|
| P0 | 强制按Go依赖重建guest helper、amd64/arm64构建、独立cgroup验收目标 | 全能力矩阵、最终产物与总验收 |
| P1 | authenticated revision、独立Agent物化、artifact来源约束、续租固定快照 | 真正双宿主验收；未把独立目录当双宿主 |
| P2 | authority ledger/session/epoch、签名grant及stop receipt API、当前command事务绑定；guest task、API文件安装、远端pause/resume、普通 Run 的 topology/resource proof、canonical serial、host grant、host supervisor effectgate、持久 child guest_task grant 和 signed completion route 已接入 | Agent 默认部署仍需强制 host supervisor；真正跨宿主验收 |
| P3 | 持久task、sealed memfd、cgroup+pidfd、monotonic guard、续租、分页输出、SSH/vsock通用SPI；真实SSH和Docker supervisor任务接续验收；Run/SSH/power/adoption/GC 的 supervisor recovery loop 已接入 | 完整 Agent/API/helper 独立进程重启接管验收；全执行路径统一隔离 |
| P4 | session+sequence observation ledger、guest活动枚举、receipt失效；Run、power、SSH access、adoption、GC 完成记录冷恢复只读重报，旧 epoch/session 继续复用统一 repository/effectgate | 全 API 故障切换矩阵、不可取消 daemon 的跨进程追踪与强制 fencing |
| P5 | adoption SPI、journal动作、Docker不可变ID sidecar；只读 `/adoption-plan`，agent-authenticated `/adoption-confirm` 绑定 evidence/fingerprint/serial/generation/agent，生成 `adoption_apply` command；宿主 AdoptionCoordinator 复用 effectgate intent/Observe，并接入 Agent grant/RPC/provider Apply+Observe callback；host key completion endpoint 绑定 command lease，并通过 `CommitAdoptionEvidence` 在 ActionRepository CAS 事务中写入 canonical state；libvirt 与 Firecracker provider 均提供 fingerprinted XML/manifest Inspect/Apply/Observe SPI；真实 Firecracker/KVM（6.84s）和 libvirt domain（2.87s）generation/adoption E2E 已通过并清理测试资源 | 多表/同库 receipt 与 action outcome 的统一数据库事务、真实 provider 冷重启后接管验收 |
| P6 | aggregate format2快照、元数据restore monotonic serial、身份约束、API CRUD；restore/force-unlock 共用活动操作检查，活跃 Run、节点操作、guest task/file 或未上报 host stop 时拒绝维护；force-unlock 还拒绝 live state lease，缺少完整活动证据时 fail-closed；Local/SQLite/Postgres restore 统一校验 topology/resource incarnation | grant撤销与freeze、force-unlock 审计回执、恢复较旧资源集合的显式reconcile、后端同一事务内 CAS restore |
| P7 | managed Docker bridge导出/核验、FC/libvirt NIC消费；受管 `ssh_access` 配置合同、宿主 apply/observe/delete、receipt/recovery、local drop-in 及真实独立 sshd E2E；Docker/network/libvirt GC provider callback 均接入 supervisor | `ssh_access` owning-agent 与 API 派发的完整终态 completion、host file-install/grant 的实际授权绑定，旧 key 只撤销自身配置；共享 sshd drop-in 生命周期、网络自动重接、混合netns单VM支持边界 |
| P8 | GC dry-run、aggregate snapshot 引用、candidate digest、durable journal；API leased `gc_delete` grant；Docker/network/libvirt/Firecracker supervisor delete callback；删除前后 immutable ID/ownership revalidation、receipt 封存与恢复 | Firecracker orphan inventory/TAP candidate 枚举、真实 provider 执行矩阵和跨宿主 CLI/API 调度；CLI raw delete 尚未迁移为唯一入口，默认 dry-run 仍不执行删除 |
| P9 | 分项真实Docker bridge/TAP、private cgroup、KVM、SSH、adoption provider测试；Run/SSH/power/GC supervisor 单测和 race；真实 Docker guest task、node power、file install 的 API/supervisor 重建与旧 epoch E2E 已通过 | 真实 host-supervisor Run E2E、双宿主、完整故障矩阵、升级/main整合，不算完成 |

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

## GC 当前边界

GC 的授权已经进入持久 command/lease/grant 模型；host supervisor 的 Docker 删除
路径会在 effectgate 内再次核验归属标签、不可变外部 ID 和 generation，并在结果
确认后封存 host stop receipt。CLI journal 对 intent/uncertain 采取 fail-closed，
不会把未知结果自动重放成第二次删除。当前 supervisor 仅接入 Docker provider；
network/libvirt 仍需同样的 credential-owning callback 和真实环境验收。旧 CLI 的
provider 删除代码仍存在于迁移期间，不能把它当成跨宿主宿主 gate 已完成。

2026-10-07集成检查点：真实Postgres启用的全仓测试通过；API/persistence/Agent/runtime/guesttask/effectgate/transport/四provider相关race通过；vet与docs检查通过。最终重建amd64/arm64 helper后，真实KVM5.10持久任务去重、冷读取、输出、取消确认通过（10.04秒）。这证明当前基础可集成，不代表尚未挂接的host supervisor、P5/P6/P7功能和P8/P9已经完成。

2026-10-08主线切片回归：普通 Run 的 host Start/Observe 终态 relay、真实 RunID completion 路由、Run/SSH/power/GC 冷恢复扫描、Docker/network/libvirt GC provider callback 已接入；全仓 Go 测试和关键包 race 通过。真实 Docker guest task、node power、10MiB file install 的 API/supervisor 重建、丢回执、后继 epoch 测试通过。P9 仍未关闭：Run 的真实 provider E2E、双宿主、完整故障矩阵和升级演练尚未完成。

heterogeneous matrix 真实重跑记录：首次失败暴露 Docker image CMD 在本地 CLI 没有 signed guest grant，示例已改为显式清空 entrypoint/command，使该矩阵只验证生命周期与网络；第二次重跑进入 apply 后因前次失败遗留的 nsfs 测试网络命名空间无法在受限环境卸载，触发 ownership conflict。没有发现对应存活进程，需在具备网络命名空间卸载权限的宿主清理后重跑，不能计为 P9 通过。

后续修复与验收：已依据原 ownership marker 的 owner/resource/generation、ifindex、alias 和无端口证明，精确清理遗留 root bridge，备份并归档旧 marker。根因是测试失败时删除了 canonical repository，而清理入口仍只检测旧 state.json；测试现在检测 `.lifecycle`，失败保留状态、密钥和镜像，重跑不再静默删除证据。真实重跑进一步修复了 runtime NIC Observe 漏传 canonical NetworkState/NetworkIdentity、空 root_routes 在冷读后误判 Replace，以及 matrixprobe 读取旧 state 文件的问题。附件验证覆盖 Apply/Refresh/Health/Reset/恢复；路由漂移不能跳过网络归属验证。

本轮混合矩阵最终通过：Docker、Firecracker、libvirt 三节点 Apply，六个方向互通（均零丢包），再次 Plan 为 `0 to add, 0 to replace, 0 to destroy, 8 unchanged`，Destroy 后验证无测试资源残留。全仓 Go 测试、runtime race、vet、docscheck 与独立复审通过。这是同宿主混合网络/生命周期验收，不代替 host-supervisor Run、真正双宿主或完整 P9 故障矩阵。
