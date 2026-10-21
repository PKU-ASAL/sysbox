# P0–P9 并行实施：集成跟踪

初始集成基线：`566268e`；后续进展按下方条目记录。本文不能作为P0–P9全部验收通过声明。

2026-10-13 SSH 当前切片收尾：新增可信 profile 的容器 sshd 进程适配器及镜像启动
自动安装/恢复，保留 systemd 适配器；真实验证两台 VM kernel 重启、guest service/
短进程 helper 重建、原 UID/代际保持、重启后撤销及 receipt-loss/reset/Destroy。
这关闭已支持布局的 SSH 服务/runtime 主线，不等于自动接管任意宿主工作负载；
其他 provider 矩阵及 P5/P6/P8/P9 剩余事项仍未关闭。最新证据见 P9 文档顶部。

2026-10-13 更新：下方 P7 的“runtime/host/API 未接通”是历史状态，已由后续提交取代。
公开 SSH、本地 CLI、宿主签名子授权、定向 Run 及 reset 后 SSH 原子重建均已实现。
双 VM 公开 API 和 runtime 创建、真实登录、轮换/撤销、API/Agent/host 进程重启、
定向 reset 和无残留 Destroy 已通过。自动受管镜像/服务 enrollment、组合故障及其他
provider 矩阵仍未关闭。当前证据以 [P9 验收记录](../operations/p9-real-acceptance.md)
顶部更新为准；P5/P6 运维、P8 GC 和完整 P9 仍有剩余工作。

P7 Docker 正式连接已暴露完整 SSH access SPI，复用已有 Exec 的 immutable identity/
generation 校验。真实 Docker provider 连接 E2E（race，0.52秒）验证签名修改、发现、
冷连接观察、旧代拒绝、tombstone 和正常 Destroy 后精确缺失；加入 test-p9-real 门槛。
runtime resource、host/API grant/completion 和自动配置发布仍未接通。

P7 共享 sshd 端点规则：新增确定性、有数量上限的 Match LocalAddress/LocalPort
生成器，交集优先，未知地址/端口继续拒绝受限 key。真实双端口 sshd（5.49秒）验证
批准端口可登录、另一端口明确认证拒绝，独立 access 与管理员 key 不受损。此项不等于
drop-in 自动安装/reload、第三方命令冲突检测或 host/API/runtime 已接通。

P7 迁移纠正：撤回 `1015801` 中提前禁用 runtime SSH access 的变更，因为 host callback
仍未替换，禁用不等于完成能力迁移。旧 runtime/host 文件路径仍须在签名 guest 授权、
canonical completion 与新 SPI 接通后一起删除。另修复未知端口绕过 Port 限制，真实
sshd 回归要求明确认证拒绝而非任意断线；无端点限制的 key 轮换/删除验收通过（4.09秒）。

2026-10-10 私网分区验收：真实双VM长任务在execution lan0断开、API/DB两个端口均
不可达时，136.92秒后原cgroup清空；恢复后原task/Run/provider身份保持，原停止回执
获确认，父Run failed且保有host completion。正常Destroy、独立精确ID/owner无残留
及非force metadata DELETE均通过。Postgres进程未重启，NAT管理链路保持。脚本补齐
恢复等待与错误证据持久化；首轮失败证据保留。这关闭该Docker checkpoint后的网络
分区场景，不关闭未知intent、运行中ENOSPC、升级、P5–P8及全provider/transport矩阵。
详见[当前验收记录](../operations/p9-real-acceptance.md)。下方时间线和阶段表含历史状态，
须结合更新条目阅读，不能把旧“未验收”或局部通过直接当作当前总进度。

2026-10-10 P7 SSH access helper slice：新增版本化 guest 配置记录、同 scope 锁、
CAS revision、历史回执和删除 tombstone；旧 revision 删除、损坏记录、符号链接/权限、
外部替换、旧 epoch、并发轮换均 fail-closed。SSH/vsock 共用 guest task 协议，OpenSSH
AuthorizedKeysCommand 只读入口不泄漏部分 key；真实独立 sshd + Docker helper 验证
管理员 key、受管 key 轮换、旧 key 失效、当前代删除及另一项访问保留。host/API canonical
dispatch、共享 sshd drop-in 管理和跨宿主重启仍未完成，不能据此关闭 P7。

真实双VM正常reset及后台task：独立新库部署后，普通Apply→Reset→Destroy及带45秒
后台task的同链路均通过。后台task确认gen1→gen2、exit0、termination confirmed、
reported；销毁后按两代精确Docker ID及owner UID独立确认缺失，正常metadata DELETE
通过。执行期间出现supervisor reconciliation deadline超时，尚未定位。故障续作、
断网/切换及完整P9未关闭；具体Run/任务/容器证据见p9-real-acceptance文档首节。

远端reset停止证据验证已接入：Run终态与宿主签名原始结果原子保存，API bridge重验
签名、原Run incarnation/attempt/lease/终态、拓扑UID及authority历史Stops hash。
普通Save/Restart不能写入/删除proof。三后端（含真实Postgres JSONB）回归与相关完整
race通过。历史条目中“verifier尚未接入”已被本项取代；旧无证据现场仍拒绝，真实
双宿主reset continuation及P0–P9其余缺口未关闭。

Reset入口接线：CLI新增--resume且禁止与--target混用，确认只展示原完整scope。
本地必须提供同步executor返回后写入的.returned回执（绑定checkpoint字节摘要及
原Run incarnation）；无回执、篡改或未知effect阶段拒绝。API受理先验证原checkpoint，
执行还需ResetContinuationVerifier；API signed host-stop verifier尚未接入，执行
安全拒绝。旧异构现场无新回执，仍不能直接恢复。Agent/API/CLI完整race及专项回归通过。

Reset续作受理：aggregate现为format5，完整scope/action版本与缺失intent在同一事务
受理，原请求receipt固定成员并支持进度推进后的精确重试。普通新reset不得接管旧Run。
format4常规读写明确拒绝；一次性import工具默认只预览，显式提交前校验源摘要并
持久保全原文。保留的异构故障现场只读预览通过但未修改，旧写者停止取证、执行入口
和真实reset恢复仍未完成。后文format4为历史阶段记录，不能据此判断当前可读格式。

P7目标身份修复：SSH coordinator不再误把topology名称与UID比较；request摘要、signed
scope和外层socket target分别匹配，command嵌套拓扑/节点也校验。名称不同于UID的
Apply/回执恢复回归及target篡改拒绝通过。这是SSH协议接入修复，不关闭共享sshd、
密钥轮换/精确删除、安装授权和真实SSH完整生命周期。

本地canonical recovery提交已复用事务授权repository：仅匹配无Agent的原失败可恢复Run，
提交时重验incarnation/活动后继/维护冻结。provider观察中途冻结的确定性失败回归通过，
state/serial未变。文件路径同时锁实际authority文件。此修复针对恢复metadata，local
cleanup的provider Delete执行隔离仍未关闭，不能仅凭前置冻结检查视为完成。

P6已提交恢复的后台收尾：API supervisor按持久receipt核验原维护屏障并补做释放，API
重启后无须客户端再次提交；不重复恢复或推进serial。没有receipt的屏障不自动放行，
缺失authority也不补造。配置未启用supervisor时仍需原请求重试；未提交维护的后台
执行、完整冻结及双VM验收仍未完成。

P6公开restore编排已接通：restore-plan固定请求，restore建立屏障/等待停止/事务提交/
释放，202保留原请求和已提交回执供重试，restore-cancel只放弃未提交且已停止的维护。
旧空body直写恢复入口已移除。文件API重启续作和HTTP计划/重试/取消回归通过；后台
自动调度、变化inventory reconcile、完整writer隔离和双VM恢复验收仍未关闭。

P6恢复事务适配已进入文件/SQLite/Postgres：authority与aggregate保持互斥直到state/
receipt提交，真实Postgres及文件竞争测试证明解除屏障不能穿过在途提交。实测另修复
JSONB重排导致snapshot摘要损坏，载荷改base64字节，aggregate现为format4且旧格式
明确重建。现有VM数据未动；公开restore受理/冻结/释放编排尚未完成。

P6恢复提交证据基础：aggregate format3保存绑定snapshot摘要/serial/maintenance epoch
的恢复回执，与state原子提交。文件/SQLite冷重试保留后继状态，相同并发请求只推进一次
serial，源snapshot删除后仍可读回原回执。旧format2明确重建，现有VM数据未动；公开
restore授权/提交/释放编排仍未接入，此primitive本身不授予维护权限。

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

2026-10-09 P8 盘点检查点：Firecracker manifest inventory 和 CLI GC 只读候选发现
已接入，当前 state 与保留 snapshot 的 VM 引用均受保护。损坏 JSON、重复身份、
配置摘要冲突、symlink、非目录 runtime root 和非普通文件 manifest 均拒绝返回部分
清单；adoption lookup 不再把损坏 JSON 当成 NotFound。真实 KVM generation lifecycle
包含实际 VM inventory 验证通过（9.89 秒）。这不是自动 GC 完成：含 Firecracker 的
执行批次在任何 intent/effect 前明确拒绝。待修复生产 GC 重复进入 gate、空 inventory
校验，补齐所有活动引用与计划授权、TAP inventory 和真实孤儿回收验收。P0–P9 仍未整体关闭。

2026-10-09 P8 gate/reference 检查点：宿主 GC provider 回调已从 coordinator 中拆出，
RPC 只进入一次 gate；Docker client 延迟初始化，不再决定其他 provider 或历史恢复
是否可用。空 inventory 校验已删除，完整引用校验未接入前，新删除明确 fenced，
不会写 intent。repository 新增原子 `LoadGCReferences`，统一 state、成功 journal
candidate、snapshot state/journal 的引用与摘要；CLI 已消费此共享层，移除独立
state.json 兼容读取。该模型尚未汇合 API 活动、授权、placement/revision 引用，也
未覆盖删除期间的并发准入；不得据此标记 P8 完成。

2026-10-09 P6/P8 删除证据检查点：metadata Delete（包括 force）已将 lifecycle
UID/serial CAS、全类型 grant 停止证据、maintenance barrier 校验放入同一提交边界。
文件/SQLite/Postgres 使用同一纯状态转换；API cleanup-inspection 公开仍待停止的
execution_grant，解释终态 Run 仍不能清理的原因。授权读取、写入、维护与 restore
共用严格必填字段校验，损坏记录不再被补默认值当成空授权。仍需汇合所有 operation
registry 的准入与引用，并拒绝 metadata 删除之后基于旧 UID 的迟到授权；P6/P8 尚未关闭。

2026-10-09 迟到授权检查点：普通签发、续租、host authorize、派生 guest grant 和
GC 签发已在 authority 提交锁内复核 command 所属 lifecycle UID。三后端并发验证
授权与 metadata 删除不能同时成功，旧命令在删除后不能取得新 epoch。拒绝请求仍
提交持久化时钟下界，不延长 command lease。此项关闭旧 topology UID 迟到授权缺口，
不等同于 resource generation、全类型引用与完整 GC plan 准入完成。

2026-10-09 GC lifecycle 引用授权检查点：签发不再接受任意非空 inventory digest。
签发/续租/host authorize 在 authority/lifecycle 事务中重算同一引用快照，拒绝过期
摘要和被 state/journal/snapshot 引用的 resource UID 或 provider external ID。
新增引用后，已签 grant 的后续授权与续租均拒绝，停止回执仍可回报。三后端定向
race 通过，含真实 Postgres。此项只是 lifecycle 部分，完整 API 活动 registry、
GC plan 与 provider 删除期间的准入排他仍未闭环，P8 继续保持未完成。

2026-10-09 P8 旧 CLI 路径清理：移除 `gc --execute`、独立删除 journal/replay、
CLI 直接 Docker remove 与 libvirt destroy/undefine 实现及对应旧路径测试。
`gc --dry-run` 保留，历史磁盘 journal 不改写、不重放；正式 provider/host gate
实现和测试保留。该清理关闭 CLI 裸删除旁路，不代表自动 GC 已可用。

2026-10-09 P6 活动范围修复：restore/force-unlock 的 quiescence 检查不再把 topology
名作为 NodeOperation workspace 过滤条件，避免跨命名 workspace 下漏掉活动操作。
按实际 Topology 判断，相关操作终态后可恢复，同 workspace 的其他 topology 不误阻塞。
跨 registry 原子准入仍未完成，不能把这一修复计为 P6 整体关闭。

2026-10-09 maintenance admission 检查点：guest execution、guest file 和
node-operation 的 dispatch 已在三后端 authority barrier 事务内受理；冻结时不
发布 operation/command，释放后可恢复 execution 受理。首次 authority 记录不存在
的 execution/freeze 并发竞争已有测试。Run dispatch、其他 metadata writer 和跨
registry 统一准入仍待迁移，P6/P8 保持未关闭。

2026-10-09 Run admission 检查点：Run 首次受理、queued→assigned outbox 和幂等
重试已接入 maintenance barrier；冻结拒绝不会写 run/request/command，已提交请求
仍可只读重试。三后端 race 与真实 Postgres 全仓回归通过。Run Save/Restart、其他
metadata writer 和统一跨 registry 准入仍未完成，P6/P8 继续未关闭。

2026-10-09 ConsoleSession 活动检查点：restore/force-unlock quiescence 现在枚举所有
持久 ConsoleSession，按实际 topology 阻止 queued/running/缺结束时间的会话；跨
workspace 名称不再漏检。跨 registry 原子准入和 raw writer barrier 仍待完成。

2026-10-09 AgentCommand 活动检查点：quiescence 现在枚举带当前 topology UID 的
非终态 AgentCommand，即使 guest activity 尚未落盘也阻止维护；终态/退休 command
不阻塞。P6 跨 registry 原子准入和 raw writer barrier 仍未完成。

2026-10-09 raw Run writer 检查点更正：fa89866 的顺序测试不证明原子持锁，且未
开启 Postgres；原文“三后端锁顺序完成”撤回。本轮修复文件 authority 锁持有到
append、Postgres 先锁 authority 再锁 Run/command，移除 `{}` 与虚构 legacy UID
豁免。允许 Run 终态排空，拒绝冻结期间 restart；身份未知且有 barrier 时拒绝受理。
另修复 activity callback 每请求重写导致的 race，command 枚举不依赖 Agent 注册。
全 registry 准入与 raw writer 完整审计仍未完成，P6/P8 不关闭。

2026-10-09 console lost 修正：API 重启写入 lost/EndedAt 不能作为任务停止证据。
维护和 metadata Delete 共用检查，lost/未知状态、缺失或倒序时间均阻挡；缺少
console registry 也拒绝。三后端测试实际经过 hub 重建，不用手填 lost 代替恢复。
Console 终态的 authority 绑定及维护期间的原子受理仍待实现。

2026-10-09 cleanup-inspection 对齐：诊断新增 console_session/agent_command，
保留 lost console 与未注册 Agent 的持久 command，按当前 topology 身份过滤。
receipt 显示对应阻挡原因，来源枚举失败明确 503，不输出 payload/环境/内部错误。
仅补齐可观测证据，跨 registry 原子准入和完整 GC 派发仍未完成。

2026-10-09 command preflight 对齐：缺 command registry 时维护明确拒绝；Delete
（含 force）在 metadata 变更前也检查当前 UID 的非终态 command，拒绝时 UID 保持。
退休 command 不阻挡，其他 UID 不误阻塞。共享 helper 仍只是前置快照，完整跨
registry 原子删除/准入协议尚未完成。

2026-10-09 command 身份不完整修正：缺 UID 但 payload 明确引用 topology 的非终态
命令仍阻挡该 topology 的维护/删除，并显示于 cleanup-inspection；名称不用于补 UID
或授予执行权限。明确旧 UID 不绑定新代际，终态不阻挡。完整原子准入仍未完成。

2026-10-09 console 取消语义修正：请求只写 cancelling，不再提前 cancelled；保存
失败不确认、不通知本机。连接断开/attach timeout 只记 lost，不能制造任务终态。
同进程 Update/Cancel 持久化排序和 Snapshot 并发读已修复；多 API CAS、取消可靠派发
及 host 签名完成仍未迁移，不能计为 console 分布式闭环完成。

2026-10-09 console cancel 错误传播：publish 失败不再被忽略；取消意图保留，重试
同 session 重新派发且不重复 audit。HTTP 只有不存在返回404，存储/派发失败返回
脱敏503。可靠 outbox 与远端 stop receipt 仍未完成。

2026-10-09 console create 持久化门槛：hub Create 接收 context 并返回 error，落盘
成功后才发布内存会话；失败返回脱敏503，不再继续 session_open 派发。会话与
outbox 的跨记录原子提交、多 API CAS 和完整执行授权仍待完成。

2026-10-09 console Update 提交边界：使用独立副本，保存成功后才更新内存；失败
传播到创建/Agent 回报/attach，relay 清理路径记录错误。审计保存失败不派发新会话，
完成保存失败不暴露内存终态。command event 与 session 尚非同一事务，多 API CAS
和自动恢复仍待实现。

2026-10-09 console completion 恢复：终态 session_open 的精确回报可从退休 command
读取原结果；直接回报和 supervisor scanner 共用身份/歧义检查，补齐 command 已
提交而 session 保存失败的窗口，不重新派发副作用。恢复使用持久时间/错误，拒绝
只有退休标记、重复绑定或身份冲突。多 API CAS、signed host stop、取消 outbox
仍未完成，不能计为整个 console/P6/P9 闭环。

2026-10-09 console startup 错误边界：读取 registry 或保存重启观察失败时，不再
作为空 registry 或成功 lost 观察发布；hub 拒绝操作，API StartContext 返回初始化
错误。保留原始证据，需修复存储问题后重新启动。多 API CAS/所有权仍待完成。

2026-10-09 console cancel 入队去重：稳定 command ID 绑定不可变 session 身份，
共享 Enqueue 首次写 queued、精确重试读取当前记录，不重置 lease/attempt/status。
三后端热/冷重试只保留一条命令，退休不复活。取消意图与首次入队仍分步，崩溃窗口
和自动补派发、host stop 证明仍待完成。

2026-10-09 console cancel intent 恢复：重启保留 cancelling，supervisor 在普通
outbox 投递前按稳定 ID 补齐首次入队。现存租约/tombstone 保留，冲突明确拒绝，
lost 不自动生成取消请求。已关闭“持久取消意图存在但首次command未写入”的恢复
窗口；session/command 仍是可恢复两步写，尚非跨记录原子事务或远端停止证明。

2026-10-09 console SQLite 完整记录：修复 ProjectID/Workspace 丢失与更新 StartedAt/
Audit 被忽略，移除重复列式读写，按完整记录保存/校验/筛选 workspace。当前 API
schema 提升10（aggregate仍为4），旧 SQL schema 拒绝并要求显式重建；前述 schema9
和 aggregate2 为历史检查点。多后端 CAS 和 outbox 原子受理仍未完成。

2026-10-09 console 多后端 CAS：Save 只允许首次创建或完整相同的重试；Update、
Cancel 与启动恢复改为完整记录 CAS。共享身份校验固定 ID、Agent、拓扑/节点、
项目/workspace 和创建时间的序列化表示，避免取消命令摘要漂移。写入冲突后核对
身份并刷新缓存，原操作仍返回冲突，不自动重放 callback；下一次取消/恢复可继续。
启动恢复对并发活跃记录重新读取并有限重试，已推进的 lost/cancelling/终态直接采纳。
三后端覆盖并发唯一胜者、禁止覆盖、精确重试、独立 hub 冲突与重试、并发启动。
这关闭会话覆盖写入缺口；尚未提供跨记录原子受理、会话宿主所有权和 signed stop。

2026-10-09 console 维护屏障：创建/CAS 业务规则收拢到共享 ConsoleRepository，
三后端在 records→authority、SQLite 写事务或 Postgres authority→session 锁内
检查并提交。冻结后不能新增记录或引入未解决活动；已有会话的取消、lost 观察、
有效完成和精确重试仍可收敛。维护、cleanup 和恢复共用完成证据判断。初始化
authority 与首次写入竞争、写入超时无残留已实测；其他已知 UID 不受无关屏障影响。
该项不等于 session/outbox 原子受理，也不授予 console 宿主执行权限。

2026-10-09 console 初次受理：HTTP 正常创建改为 session+授权 audit+session_open
一次提交。文件使用 session 内不可变 dispatch seed，后续 CAS 保留，SQL 同事务
插入两个 registry；冲突整次回滚。通知只负责唤醒，失败仍202并由 outbox 恢复。
旧独立 session_open 发布入口明确拒绝。完整 request 冷读、退休优先和并发唯一
胜者已覆盖。同时修复请求体 roles/requested_by 覆盖服务端主体的原有授权漏洞。
取消仍为可恢复两步写，HTTP idempotency key、完整删除事务、host stop 尚待关闭。

2026-10-09 console 取消同提交：共享 ConsoleRepository.Cancel 在原事务内核对
session CAS 和稳定取消 command，已有租约保留，冲突/退休不覆盖。文件同时保存
opening/cancellation 两个不可变 seed，后续更新保留，mutable/tombstone 优先。
HTTP 和 timeout 都经同一路径；删去 standalone cancel enqueue 生产适配器，
repair 也消费原子方法。失败不改审计/状态、不通知本地 relay；成功后的通知失败
不撤销已受理结果。先前“两步写”缺口现已关闭，但 HTTP key、删除事务和 host stop
仍未完成，不能据此宣布 P4/P6/P9 关闭。

2026-10-09 console 入口授权：修复 GET/取消/browser attach 未检查 Console
角色策略的绕过；统一服务层读取+授权，禁止先升级连接再验证。取消 audit 使用
当前操作者角色，拒绝不写入 registry/outbox。权限缺失403、不存在404、内部故障
脱敏503。API race 和独立审查通过；尚不包含已连接会话的持续授权/host stop。

2026-10-09 删除提交活动检查：在已有 authority/lifecycle 提交事务内重新读取
最新 Run 和所有 workspace 的 Console，拒绝预检后新受理的未解决活动；失败保留
原 UID/serial，已结束最新记录和无关 topology 可继续删除。SQLite复用索引/完整
JSON 一致性检查，拒绝预检后记录损坏。Run未知状态/缺失或倒序时间均阻挡维护。
其他 registry 的原始 writer 与完整删除提交检查仍待接入，不代表 P6 全部关闭。

2026-10-09 command 删除提交检查：完整列/JSON扫描纳入现有删除事务，保留退休、
显式 UID 与缺 UID 名称阻挡语义。Postgres raw command 更新改为 authority→command
锁序，与签发/删除一致；首次 authority 事务期间不得发布命令。验收更新为 API
删除同时要求 effect 停止与未解决命令清除，外部重建后的 stale-grant 拒绝仍单独
验证。guest/node/adoption/GC、维护 raw 准入和删除后清理阶段仍未关闭。

2026-10-09 command raw 维护准入：新增共享规则，冻结期间拒绝新命令、领取/
扩展投递租约和首次派发；保留既有执行回报、完成、取消、退休及只读 observe。
显式 UID 与完整 lifecycle 查询统一 scope 判断，未知范围遇活动屏障拒绝，多个
payload 都检查。文件持锁延伸至 authority，SQL沿用原事务边界。恢复 fixture先
建立真实 lifecycle，再受理命令；没有放宽未知身份规则。其他操作 registry 与
删除后清理的恢复流程仍未完成。

2026-10-09 node 原始记录边界：Create/CAS 持 authority 至记录提交，冻结期间
拒绝新活跃状态，允许已有终态收敛；文件 compound dispatch 明确使用已持锁
入口，SQL借用原写事务。删除提交检查完整 node registry，跨 workspace 不遗漏。
仅持文件 authority 也能阻挡 raw node 写入，超时无残留。guest execution/file
与完整删除后清理仍未关闭，不代表 adoption/SSH 能力迁移已完成。

2026-10-09 guest 原始记录边界：execution/file 与 node 复用 operation maintenance
guard，冻结拒绝新增/活跃更新，文件取消意图只在状态不前进时放行。删除提交纳入
完整 guest registry，维护/inspection 共用有效终态证据判定，未知时间不伪装结束。
已验证 authority 等待、无部分写入、跨进程 CAS 与损坏数据拒绝。guest execution
取消处理仍有先写终态再派发的独立窗口，停止证明和删除后清理恢复继续推进。

2026-10-09 Agent 取消源命令：cancel_command 只确认请求传达，不再凭 context.Cancel
伪造目标 cancelled；完成回报获得独立有界 context。执行 token 保证旧清理不会
移除新执行的取消映射。三项旧行为均有失败回归，Agent race及全仓回归通过。
API 取消持久事务与远端停止证明仍未完成，不能计为整个取消闭环完成。

2026-10-10 guest execution 取消：持久 cancel_requested 保留 queued/running，
不再伪造结束时间或结果；取消 outbox ID 绑定执行实例，重启修复缺失投递，重试
保留租约与退休记录。真实完成允许保留结果。生产 grant issue/authorize/renew
在 authority 锁内核对最新 execution，拒绝取消、终态、缺失或身份不匹配；拒绝
仅推进时钟，不延长命令租约。停止回执仍通过原始 authority 接口接受。
API schema 11 / execution record_format 2 拒绝旧数据；file 2、aggregate 4、
authority 2 不变。当前仍是可恢复的两步取消，不是原子提交；已签发授权撤销、
宿主签名 execution 结果及持久回报恢复、删除清理工作流与完整 P9 仍未关闭。

2026-10-10 后续取消提交边界：三后端共用 PrepareGuestExecutionCancellation。
SQLite/Postgres 将取消意图、outbox、已发授权 revoke 一起提交；文件任务记录
原子包含意图与取消 seed，随后写 authority，重启扫描补齐中断的 revoke。
即使任务已终态、源命令和取消投递已退休，也不能跳过撤销恢复。原始 Create/CAS
禁止首次设置取消标志；通知失败不改变已受理结果。execution record_format 3、
API schema 12 拒绝旧写者，明确重建。撤销不等于远端停止，宿主签名结果、持久
回报恢复和 P9 跨宿主故障验收仍待完成。

2026-10-10 宿主 execution 结果：GuestTaskResult 把退出状态是否已知、结果分类、
有界输出及截断标志纳入宿主签名，必须有远端终止证据；worker completion 路由、
类型和调用链已删除。API先保留授权排他，按完整 dispatch 身份和 digest 提交结果，
再释放授权；支持真实退休 tombstone、脱敏后精确重放及 successor 保护。Run派生
任务只关闭 child grant。宿主先保存签名回执再回报，重启可独立补报。
API schema 13 / execution format 4 / host task version 2 明确重建旧格式。
未启动且未发授权任务的终态证明、两 VM 故障验收、删除恢复等主线仍未关闭。

2026-10-10 已取消但未获权的任务收尾：恢复扫描先确认取消意图、退休原投递，再在
共享取消事务中检查完整 Grants/Stops。仅原始 compound admission 身份完整且
没有任何执行授权历史时，落 cancelled/not_started；最后退休取消投递。三个
中断点均保留 durable intent，重启可收敛；已有终态重试不改版本或结果。
不凭 queued/StartedAt 判定，曾发授权但当前已被 successor 替换也不能伪装未启动。
格式仍为 API13/execution4/host task2。未取消的派发前失败自动收尾、删除恢复及
完整 P0–P9 跨宿主故障验收继续推进。

2026-10-10 未取消的派发前失败收尾：supervisor自动扫描失败/拒绝/取消的源投递，
先退休，再复用 GuestExecutionMutation 事务核验受理seed和Grants/Stops。没有
执行历史才failed/not_started，CancelRequested保持false；有历史只revoke当前
匹配授权，等待宿主结果且不影响successor。取消和host终态优先，不覆盖已有结果。
源退休后的文件/SQLite/Postgres写失败均能冷重试；API时钟落后时终态时间不倒序。
取消与失败共用存储适配器和admission校验，无兼容别名；持久格式不变。持久删除、
完整P5–P8能力审计及P9双VM故障矩阵仍未关闭。

明确保留边界：已发授权但宿主没有任务记录，仍需宿主侧的缺失记录/未启动证明；
不能把投递失败或grant过期当作该证明。此次失败收尾仅关闭从未发过授权的任务。

2026-10-10 guest缺失记录封闭基础：新增只取消的独立签名域，绑定完整grant身份，
不能当作Start/Renew凭证。guest持scope→task锁，重查缺失记录及不存在cgroup后
写持久取消记录，迟到Start不再启动。残留目录可恢复，其他缺失记录/悬空引用/
遗留cgroup拒绝；scope先提交而record失败可重试。guest record2与RPC1独立，
旧record1明确重建；amd64/arm64嵌入helper已重建。真实私有cgroup及宿主/API
E2E通过。API待停止授权发现/凭证签发与host无记录收敛尚未接入，不能据此关闭
整个missing-host-record窗口或P0–P9。

2026-10-10 独立guest任务撤销发现已接通：API按owner、revoked授权和不可变源
记录签发stop-only证明，固定epoch上界、64项分页；坏源记录返回issue并继续。
host先持久stop-only记录，再核验canonical UID/generation及provider身份并取消，
不调用Start。新guest boot以签名双boot证明旧任务已停，结果保持unknown；已完成
任务保留真实结果，回报失败后精确重放原回执。host task升级为3并拒绝旧格式，
guest task2/RPC1/scope2/API13/execution4不变。Run child发现仍未接入，API终态
提交后若丢失唯一host receipt仍不能自动补authority release；完整P0–P9未关闭。

2026-10-10 后续Run child发现接通：API以同一ledger父授权和保留的源命令核对
RunRequest digest，将RunID/Topology纳入只取消签名；host按canonical state或原Run
未完成candidate定位，持久记录保留并验证来源。父Revoke同事务标记未停止child
为revoked，修复仅在线拒绝而发现接口看不到的问题。父checkpoint/ProveStopped
比较完整不变身份，允许active→revoked恢复但仍拒绝epoch/parent epoch变化。
真实Docker子任务缺失记录自动停止、旧Start封闭、仅释放child已通过；这不关闭
父Run未知intent、持久删除、完整P5–P8和双VM故障矩阵。

2026-10-10 guest completion release recovery已接通：host completion将grant身份与
receipt hash随终态结果持久保存；SQL结果与authority stopped同事务，文件后端可从
证据冷恢复中断发布。API先核验原authority，精确重放不联系guest、不影响后继epoch。
execution record format 5、API schema 14明确重建旧格式。持久删除、未知Run intent、
P5–P8和完整双VM矩阵仍未关闭。

2026-10-10 持久删除窗口已补齐：删除在 authority-aware state reset 前写入
`delete.intent`（topology UID/serial），publication 遇到 intent 立即拒绝；Delete
和 supervisor 冷启动扫描可继续 workspace/placement/health 清理，成功后才移除
intent。intent 只在 cleanup receipt/activity 检查通过后创建，不冒充 provider 停止
证明。真实双VM delete 故障矩阵仍待补跑。

2026-10-10 P7 host operation completion 边界补强：API store 的 SSH/adoption host
completion 现在也必须先用 receipt hash 验证原 grant authority；HTTP 路由和直接
存储调用共享同一验证，不再能仅凭 operation payload 写终态。operation 状态提交与
authority release 的完全同事务合并仍待下一切片。

2026-10-10 P7 SSH/adoption completion 事务推进：SQLite/Postgres node-operation
completion 现在在同一 DB transaction 内写 operation outcome 和 host-stop
authority receipt；共享 `ApplyHostStopAuthority` 保持 epoch/successor fencing 和
精确重放。文件 backend 仍沿用 records lock 后的可重试两步边界，避免递归 flock；
API store 直接调用同样先验证 authority。下一步是把 file adapter 改成同一发布
mutation，并补 operation receipt 丢响应后的 supervisor 冷恢复。

file adapter 收尾：local NodeOperation completion 在 records lock 内先持久 operation
outcome，再通过独立 filesystem authority mutation 发布 host-stop receipt，避免
records→authority 递归 flock；authority 写失败保留可重试 operation，原 receipt
重放可完成 release。SQLite/Postgres 同事务路径保持不变。

2026-10-10 P8 GC fencing 审计：现有 `GCInventoryDigest` 已作为计划版本锚点，
而非普通扫描字段。API grant issuance、command lifecycle validation、host
GCCoordinator effect 前 inventory revalidation 三层共同检查它；canonical state
新增 resource、snapshot 引用、resource UID/generation 或 provider external ID
变化都会拒绝旧 grant/renew/delete。相关 API/persistence/host supervisor GC
合同测试和真实 provider candidate 测试通过。剩余是实际自动执行矩阵、Firecracker
orphan inventory/TAP 和跨宿主调度，不再是缺少基本 plan identity fencing。

2026-10-10 `make test-p9-real` 使用真实 Firecracker kernel/rootfs 通过完整分项
provider gate：Docker file/power/guest task/Run receipt-loss/adoption、Firecracker
crash/generation/reset/cross-process recovery、libvirt generation effects 全部通过
并清理资源。P9 尚未关闭：双宿主 API/Agent 故障矩阵、partition/disk-full/upgrade
演练和 main 整合仍待完成。

补充 lease completion fencing：local worker completion 在 adapter 入口先核对当前
command lease owner/attempt/epoch/deadline，并允许 retired command 只做完全相同的
terminal replay；改变结果或旧租约不能重写 operation。


## P5/P6/P8/P9 逐条对账（2026-10-18）

对账基线：`f4e59e1` 之后本轮修改。原始要求来自 [完整实施计划](2026-10-07-full-migration-distributed-execution-plan.md)。这不是完成声明；“门禁通过”仅覆盖列出的测试边界。

本轮补齐 P6 相同 unresolved journal 的保留恢复、摘要绑定的有界 snapshot retention；新增严格验收总账和原始事件归档；修复验收脚本 Python 3.10 兼容。Docker/Firecracker/libvirt 分项真实门禁通过；专用双 VM 当前二进制的 Docker Apply→Reset→Destroy→receipt→metadata DELETE 通过且按 owner 验证零残留。

## 逐项状态

| 条目 | 已具备的实现/证据入口 | 尚未关闭的范围 |
|---|---|---|
| P5.1 | Inspect/plan/confirm，身份、依赖、冲突和能力检查；`adoption-api`, `adoption-libvirt`, `adoption-firecracker` | 本项已具备列出的合同/分项门禁；最终证据以 campaign 核验结果为准 |
| P5.2 | grant+journal、签名接管回执、冷读与丢回报合同；`adoption-api`, `real-docker-api`, `real-libvirt` | 所有 provider 在 ownership write/observe/state commit 各点的 OS kill 矩阵 |
| P5.3 | 无主资源显式接管；不同 owner 拒绝；`adoption-api`, `adoption-libvirt` | 跨 owner 显式转移及旧 owner 退休证明流程未实现 |
| P5.4 | Docker 独立持久 ownership 绑定及真实冷生命周期；`real-docker-provider` | 本项已具备列出的合同/分项门禁；最终证据以 campaign 核验结果为准 |
| P5.5 | Docker/libvirt 导入；Firecracker 完整 manifest 冷接管；`adoption-firecracker`, `real-docker-provider`, `real-libvirt`, `real-firecracker` | 本项已具备列出的合同/分项门禁；最终证据以 campaign 核验结果为准 |
| P5.acceptance | 真实 provider 冷恢复/生命周期分项；`real-docker-api`, `real-docker-provider`, `real-libvirt`, `real-firecracker` | 控制面发布中断与完整接管故障组合尚未全部验收 |
| P6.1 | 三后端快照/校验/删除；新增有界保留策略；`snapshot-api`, `snapshot-storage` | 本项已具备列出的合同/分项门禁；最终证据以 campaign 核验结果为准 |
| P6.2 | 明确 metadata restore 不回滚 VM/磁盘；`snapshot-storage` | 原功能的 VM/磁盘 snapshot 等价清单仍需逐 provider 对账 |
| P6.3 | 冻结、撤销、独立 stop、CAS、幂等 restore 与 serial 递增；`snapshot-api`, `snapshot-storage` | 本项已具备列出的合同/分项门禁；最终证据以 campaign 核验结果为准 |
| P6.4 | 新增双方完全一致 unresolved journal 原样保留；漂移拒绝；`snapshot-api`, `snapshot-storage` | 本项已具备列出的合同/分项门禁；最终证据以 campaign 核验结果为准 |
| P6.5 | 权威 grant/barrier inspection；force-unlock 等待停止证明；`authority-api` | 本项已具备列出的合同/分项门禁；最终证据以 campaign 核验结果为准 |
| P6.acceptance | 三后端事务/冷重试/代际/lineage 拒绝合同；`snapshot-api`, `snapshot-storage`, `authority-api` | 独立进程 restore/crash/旧 worker 全组合未关闭 |
| P8.1 | inventory→candidate→plan→owning-Agent delete→verify；`gc-cli`, `gc-api`, `gc-host` | 本项已具备列出的合同/分项门禁；最终证据以 campaign 核验结果为准 |
| P8.2 | state/journal/snapshot/资源身份引用保护与不完整盘点拒绝；`gc-cli`, `gc-api`, `gc-storage` | revision/artifact/cache 全引用域、离线跨宿主全量 catalog 尚未覆盖 |
| P8.3 | API/宿主两端重验 grant、inventory digest、代际与引用；`gc-api`, `gc-host` | 本项已具备列出的合同/分项门禁；最终证据以 campaign 核验结果为准 |
| P8.4 | 1–128批量、节点先于网络、queue-head、丢回执恢复；`gc-cli`, `gc-api`, `gc-host`, `real-docker-api` | 非 Docker 批量执行故障矩阵、artifact/cache GC 未关闭 |
| P8.acceptance | Docker 真实 GC、旧计划/新引用/错误身份拒绝；`gc-cli`, `gc-api`, `gc-storage`, `gc-host`, `real-docker-api`, `real-docker-provider` | 所有 provider 的真实孤儿清理与受管对象无损矩阵 |
| P9.1 | 本轮双独立 kernel、无共享目录、当前二进制 Docker 生命周期；`fence-api` | 本轮 Docker 双 VM 已通过；机读 campaign 尚仅覆盖 Go gates，跨 VM 证据另列 |
| P9.2 | 三 provider 分项真实门禁；Docker 双 VM 完整基础生命周期；`real-docker-api`, `real-docker-provider`, `real-libvirt`, `real-firecracker` | 每个 provider × transport 的全生命周期组合仍不完整 |
| P9.3 | 部分 crash/丢回报/取消/unknown-delete fencing 验收；`fence-api`, `fence-storage`, `real-docker-api`, `real-firecracker` | 全注入点、乱序、磁盘满、不可取消调用的完整矩阵 |
| P9.4 | PG 双 API 部署与三后端合同；当前 live failover 尚未重验；`upgrade-api`, `snapshot-api`, `gc-api` | 当前版本运行中任务的双 API failover、DB/network outage 尚未重验 |
| P9.5 | 旧 schema 拒绝不变性、空库重建、显式 aggregate import 合同；`upgrade-api`, `upgrade-aggregate` | 停旧 writer→保留证据→资源处置→全组件升级/重建的完整真实演练 |
| P9.6 | 当前源码构建双架构 helper、发布脚本门禁、独立审查；`` | main 差异整合、最终全量同版本验收；未合并、未推送 |
| P9.acceptance | 严格总账拒绝 skip、缺项、包错配、源/产物变化；`` | 有 open 项，不能宣布 P5/P6/P8/P9 全部完成 |

## 可重复核验

目录与原始要求：`scripts/acceptance/campaign.json`（24条，18组门禁）。
运行结果：`/tmp/sysbox-campaign-final2-1018/`，每组保存原始 `go test -json`、stderr、退出码、源/嵌入二进制摘要及外部 helper/kernel/rootfs 摘要。
命令：`/usr/bin/python3 scripts/acceptance/campaign.py reconcile --evidence /tmp/sysbox-campaign-final2-1018`。返回2表示仍有 open/unverified，不是测试全通过即完成。

## 当前双 VM 证据

专用 lab `/tmp/sb-fence-1018`，新数据库 `sysboxp9_final_1018`，与历史 `/tmp/sysbox-p9-lab-current` 完全分离。
控制 boot `90339cd8-746f-490c-9fe6-9129b25958e4`；执行 boot `428d0093-1eaa-45e0-9e9b-28c91d0df3e9`；独立文件系统、嵌套 KVM 检查通过。
当前部署二进制 SHA256 `af3637dac22ba25dfa33f6915f0408a5a911226021bb9ab5115e3a2d7e4716b6`。
生命周期文件 `/tmp/sb-fence-1018/final-1018/lifecycle.json`，SHA256 `af9b8191248b64d58af2a7c8f317f1c8b3c605223c27ab9dc134fecee8043ddb`，phase=`metadata_deleted`。
隔离证据 `/tmp/sb-fence-1018/isolation.json`，SHA256 `f8c5f07b9f690c7098d226385176242d0bf2193c23570c3cd3a1ccd899b048a9`。
原部署在384MiB控制 VM 软件包安装时 soft lockup；只扩容专用控制 VM至1024MiB，保留旧新库和日志。QEMU socket LAN重连后注册/服务启动成功。历史现场未改变。

## Main 差异与未授权动作

main 为 `b1cc43d`，仍有 PR43/44/45 对应的 keyed apply/claim replay/crash fencing 历史差异待集中整合；重构已有对应合同，不能简单 cherry-pick 覆盖。
本轮未合并 main、未推送、未发布。对账发现的未完成项保持 open；以上内容不能当作交付全部完成凭据。

## 后续进展与新失败（2026-10-18）

- P6.2 已完成能力盘点：main 与重构三个内置 provider 均未实现 VM/磁盘 Snapshot
  SPI；metadata snapshot 不代表外部快照。新增能力回归门禁，移除此项待盘点标记。
- P9.4 首轮运行中 failover、数据库中断、LAN分区均完成原任务证明和正常清理，
  但重复 failover 出现任务取消，下一轮在故障注入前产生 unknown create。该项
  继续 open，不能以首次成功代替稳定性闭合。
- 新验收脚本验证 disposable scope、活跃任务+checkpoint、结果身份、服务恢复、
  exact-owner零残留；新增二进制前后摘要和当前源码构建比对，防止旧执行证据被
  新验证器重新标为当前通过。
- 升级演练只完成专用部署停写/DB+目录归档/服务恢复；完整升级重建仍 open。
- 新 unknown 现场保留在 disposable lab，原历史环境仍未触碰。

当前验收工具还绑定运行服务的 `/proc/<MainPID>/exe`，不是安装路径；缺绑定的旧
round不能计当前通过。重复失败的 known-failed topology 已正常销毁/metadata删除；
后续 `p9-135f615b0b64` unknown 创建保留，没有绕过恢复合同。P9.4 根因与稳定重验
仍待完成，机器总账明确维持 open。

## 修复版 P9.4 重验结果（2026-10-18）

新 disposable lab `/tmp/sb-retry-1018` 使用当前二进制 SHA256
`5f86af09989f4764f5f8905cab66d604e2ac757e100db43d3c18f7cf9f952d64`，四个运行服务
通过 `/proc/<MainPID>/exe` 绑定。两轮独立 API failover、数据库中断、私有 LAN
分区均通过，保存原 task/parent/receipt、service restore、exact-owner residue
和 metadata delete 证据；观察超时日志未再取消成功任务。严格 live-fault gate
在最终源码上通过。此前失败轮次保留为诊断，不被覆盖或重标为成功。

这关闭 P9.4 的当前 live-fault campaign 范围，但仍不关闭 P9 的全部 fault matrix、
disk-full、upgrade/rebuild、全部 transport/provider 组合或 main 整合。

## 第 2 阶段当前逐项对账（2026-10-19，覆盖前文历史状态）

本节是当前状态；前文按时间记录的“revision 删除未实现”“非 Docker 批量
尚未接入”等描述不再代表当前代码。机器清单为
`scripts/acceptance/stage2.json`，从原始 P8 条目汇总 15 组门禁。
阶段交付必须在同一源码摘要和提交上全部通过；不能拼接旧版本证据。
当前验证状态：支持范围交付已于实现提交 `f5a7fd9` 完成统一验收；原始 P8/P9 总验收仍开放。

### 后端与删除范围

| 对象／后端 | 已实施 | 明确边界与验收 |
|---|---|---|
| 文件全局 revision registry | 内容摘要校验、历史 Run 和解码快照引用盘点、保留预览 | 删除返回冲突，不声称跨文件事务；拒绝旧格式 state 和不完整 catalog |
| SQLite 全局 revision | 同库盘点、保留预览、单对象退休删除 | 同一写事务检查历史 Run／lifecycle／快照／其他 revision／命令／检查点／计划，并安装新引用拒绝守卫 |
| PostgreSQL 全局 revision | 与 SQLite 等价的单对象退休，显式 READ COMMITTED | 删除与 Run 引用竞争不能都提交；退休标记永久阻止重新发布后恢复新引用 |
| 独立 registry／另一个数据库／任意共享缓存 | 未实现联邦盘点和删除协议 | 不属于可执行范围，不能以本库无引用推断另一 registry 无引用；原始 P8.2 保留开放 |
| 私有宿主缓存 | SQLite 锁内精确删除；PostgreSQL 持久化 reservation＋触发器冻结；完成／终止／恢复处置 | 必须全部写入方已登记并受同一 catalog 约束；文件 catalog 不授权缓存删除 |

### 批量策略

| 操作 | 实际策略 | 不支持的推断 |
|---|---|---|
| provider GC | 1–128 个候选原子 reservation，节点先于网络，逐个队首授权及签名回执；失败／未知不越过 | 不是并发裸删除，不因租约到期认定前项停止 |
| 缓存 GC | 每次一份精确文件计划，重验路径、inode、内容和 catalog，写 intent 后 unlink | 不递归删目录、不把单对象回执用于重建文件 |
| revision retention preview | keep_last 0–10000、older_than、limit 1–128，`deletion_authorized:false` | 非原子观察，不是批量删除许可 |
| revision delete | 显式 revision＋older_than，每次一个 SQL 对象，事务内重验引用 | 不自动执行保留计划；不是原子批量 keep-last，文件后端不可执行 |

### 故障与真实资源矩阵

| 验收点 | Docker | libvirt | Firecracker | 结论范围 |
|---|---|---|---|---|
| owning-Agent 批次顺序、丢回执、冷 API／coordinator 恢复 | 已有真实 gate | 已有真实 gate | 已有真实 gate，两个成员均验证精确回执重报 | 同一进程内 API 重建／冷实例恢复，不冒充 API 进程 kill |
| 精确目标删除、过时代际拒绝、旁路对象无损 | 真实 provider gate | 旁路域与磁盘 sentinel 保留 | 旁路 VM 仍运行，原 PID/start identity 消失、socket 删除 | 独立验收只覆盖测试拥有资源 |
| provider effect 前子进程退出 | 既有门禁范围单列 | 保留 uncertain，不重放 | 保留 uncertain，不重放 | 精确 `os.Exit` 无 defer 边界，不声称随机 SIGKILL |
| effect 已返回、journal outcome 未提交时退出 | 既有门禁范围单列 | 目标已删但仍 uncertain，无成功回执 | 同左，额外核对进程／socket | 不把 absence 当作原请求成功 |
| outcome 已封存、回报前退出 | 既有门禁范围单列 | 冷恢复签名回执，旧 grant fenced | 同左 | 读取历史结果，无第二次 provider 删除 |
| provider journal 丢失／损坏后的完整处置矩阵 | 未完整验收 | 未完整验收 | 未完整验收 | 保留 unknown；缓存文件的阶段 1 manual review 流程不能代替全部 provider 处置证明 |
| API／Agent 各点 SIGKILL、provider 内部调用半完成、磁盘满与乱序完整矩阵 | 未完成 | 未完成 | 未完成 | 仍属原始 P9.3 等开放项，不能标已验收 |

### 阶段交付与原始总验收的区别

本轮可关闭的交付是：**同 catalog 的全局 revision 保护／SQL 单对象删除，
加上述非 Docker 批量与 coordinator 边界恢复验收，以及不支持范围的拒绝行为**。
这不是“所有后端都能删除”或“全部故障矩阵完成”。文件后端删除、独立 registry
联邦覆盖、自动批量 revision retention 没有被悄悄变成已完成事项。

原始 P8.2、P8.4、P8.acceptance 的 `remaining` 已改为上述真实未覆盖范围；
P9.3、P9.acceptance 继续开放。只有相同提交的 15 组严格门禁、边界测试、
独立审查及常规检查通过后，才能在本节记录“支持范围交付关闭”。

### 本轮集中验收结果

实现提交：`f5a7fd9`。统一门禁：`/tmp/sysbox-stage2-unified-acceptance/`。
15/15 组全部通过，无跳过；已逐组重验原始 Go 事件、源码摘要和外部产物摘要。
机器结果：同目录 `stage2-result.json`，明确
`supported_scope_verified=true`、`original_P8_P9_complete=false`。
同源码全仓 Go 回归 `/tmp/sysbox-stage2-unified-full.log` 通过；vet、68 篇文档
检查、Python 67 项通过。独立审查对保留开放项和边界测试的意见已修正。

结论：本节列出的支持范围交付关闭，可进入下一阶段；不以此关闭独立 registry、
文件 registry 删除、自动 bulk retention 或完整 P9 故障矩阵。原始总清单继续
保留这些差距。后续若决定支持这些范围，必须新增实现和门禁，不得复用本轮
“支持范围交付关闭”作为验收证据。


## 第 2 阶段剩余项继续实施（2026-10-21）

用户要求完成全部剩余项；前文“支持范围交付关闭”不再作为停止工作的理由。
本轮接通 SQL revision 保留策略批量执行：1–128 对象，同事务重验 keep-last、
年龄和引用，失败整批回滚（含退休标记与触发器）。预览变化或真实晚到 Run
会拒绝执行。单对象退休复用同一事务内删除函数。文件／独立 registry 删除与
完整非 Docker 故障处置仍需继续实现，原始总要求没有缩减。


## 文件 registry 与非 Docker 故障补强（2026-10-21，覆盖前述对应条目）

文件 revision 单对象删除已实现：共享 catalog flock 将退休与 Run、生命周期、
checkpoint、宿主 effect ledger、reset 返回回执等最终写入串行化；完整扫描所有
已登记的 consumer 根目录及解码快照。退休先原子重命名，再压缩永久 tombstone，
释放项目内容并阻止冷进程重新发布。同目录默认 registry 与独立 registry 均支持。
独立 registry 使用显式 `revision_registry` 绑定和单调 consumer 清单；缺失或离线
consumer、清单丢失、旧格式证据及不完整扫描均拒绝删除。升级前须排空旧写入方。
文件批量 retention apply 和未登记的远端联邦 registry 仍未实现。

非 Docker 新增取消但 provider 不返回、outcome 落盘失败、journal 丢失／损坏的
故障验证。准入 anchor 在 provider 副作用之前持久化；journal 丢失检查在 topology
锁内再次执行，防止已排队请求重放。完整回执仍可冷恢复补报；unknown 不伪造成功。

**两项总要求尚不能一起标为完成。** 当前还缺非 Docker unknown GC reservation
在显式宿主 fencing 后的核验、处置与释放链；现有 Run fence-delete/release 不能
代替 GC reservation 的协议。完整 API/Agent SIGKILL 和 provider 内部分步故障矩阵
也不能由 coordinator 子进程退出用例代替。原始 P8/P9 清单继续保持开放。
