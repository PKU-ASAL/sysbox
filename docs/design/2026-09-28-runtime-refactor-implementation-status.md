# Sysbox 生命周期重构：当前实施状态

## 2026-10-18 补充：隔离宿主未知删除闭环

已接通 `reconcile-delete`：完整原始删除锚点 + 整机 fencing + 新 boot 宿主签名
absent/残留消失证明 → 原子提交删除结果 → 终结中断 Run/command 与父 grant →
显式 release。Run 保留 failed/nonrecoverable，不伪装原执行成功。文件后端有
prepared 决策和 pre/post hash 恢复，SQL 一次事务；authority 升至 format5。
范围仅本地 Docker/libvirt、无附件/子任务的节点删除；真实历史现场只读，create、
reset、缺锚点和非空恢复仍未闭合。以下是此前各阶段边界，当前验收见运维记录。


## 2026-10-18 补充：宿主 fencing 证据接入

新增可丢弃验收宿主的请求准入、外部控制器 Ed25519 签名与持久回执。
准入绑定原始 command/Run incarnation、生产 grant 摘要及 journal 观察摘要，
冻结 topology 并阻止同 Agent 跨 topology 的 grant、派发与 Run 领取。
CLI 和验收脚本在断电前绑定请求，真实 Docker/libvirt canary 断电重启与 API
证据接入已有验收入口。初始证据接入使用 authority format3，显式释放扩展到
下述 format4；旧账本明确拒绝，不自动迁移。

后续已接通 fencing 后的只读 recovery-plan 与 owning-Agent observe 派发；请求并发、
领取后重试与终态重试保留同一 command，报告按原始身份/serial/时间/覆盖范围校验，
journal 漂移显示 stale。Agent 的观察完成回报现在保留完整命令证明。

现已补齐新 boot 的宿主签名观察：API 下发 fence 摘要/新 boot 绑定，supervisor
核对 boot、只读观察并重新授权后签名完整报告和投递租约，API 冷读验签。
合法签名仅消除宿主证明缺失项，未知结果和原 operation/guest/reset 阻断仍保留。

新增严格的显式 release：仅对已终态 destroy、全 succeeded journal、空 state、
精确宿主签名 absent 报告、所有独立 grant stop 及零未完成活动齐备的 topology，
原子释放 barrier 与宿主 hold。authority 升为 format4；不迁移真实历史账本。

这一步尚未闭合 unknown 恢复：回执不完成 Run/action，不产生 stop receipt，
未知/有存活资源的恢复仍没有解除 hold 的流程。历史 intent 仍只读处置；P5/P6/P8/P9 的完整故障
矩阵与升级整合仍未关闭。当前接口及限制以 [API](../reference/api.md)、
[升级说明](../operations/upgrades-and-recovery.md) 和
[验收记录](../operations/p9-real-acceptance.md) 为准。下文保留此前阶段状态。


2026-10-18 后续切片：GC 新增 filesystem/shared SQL catalog 的完整范围盘点、
两次观察保留期、预算上限和原子批次顺序调度；无主 libvirt 停止的持久 domain
支持显式 metadata 接管与冷生命周期（外部磁盘保留，reset 不隐式认领磁盘）。
新增历史 aggregate/host ledger 的只读摘要审计。强制宿主 fencing、历史 unknown
处置闭环、跨主机/TAP/nft 全局库存仍未宣称完成；见最新 P9 记录。

2026-10-18 实施更新：单候选 GC 已接 API reservation/outbox → owning Agent →
host supervisor → 精确缺失签名回执，CLI `gc submit` 可提交显式计划。
P5 新增 owning-agent 只读 inspection 和基于已保存宿主证据的公开 confirm，
CLI `import inspect/confirm` 已接入。P6 新增 execution-authority 检查与带幂等键的
revoke/wait/release force-unlock。Aggregate 当前为6，format5 有显式离线归档转换。
Firecracker 删除前停止证据保存在 generation 外，普通未锁 manifest 不再证明
未知 VMM 请求已停止。最新验证见 [P9记录](../operations/p9-real-acceptance.md)。

本次仍未关闭：自动 GC 完整 inventory/retention/batch、无主 libvirt adoption、
Docker/libvirt 未知 daemon 请求 fencing、历史 intent 显式处置、完整双宿主故障/
升级矩阵、最新 main 整合。下方旧阶段条目与版本号仅是历史基线，不能覆盖本更新。

更新：2026-10-07。工作分支：`refactor/observation-driven-runtime`。未合入 main、未推送。

**正在实施P0–P9：** 下文是此前已提交基线。当前改动的完成/未完成项、API schema 13 与 aggregate 4、任务与跨宿主交付进度，以[并行集成跟踪](2026-10-07-parallel-implementation-tracker.md)为准；不能将部分库和分项测试通过解读为分布式执行闭环已经完成。

本文只描述当前实现。历史阶段记录由 Git 保留；[目标架构](2026-09-28-observation-driven-runtime-refactor.md)和[接口清单](2026-09-28-interface-change-inventory.md)中的未交付设计不视为已支持能力。

## 职责与执行模型

Sysbox 接收基础设施期望，负责受理、执行、观察、恢复和可信结果。场景、业务编排、归档及何时创建/销毁资源由调用方负责。

```text
固定 revision / 输入 / 操作身份
        ↓
持久准入 + command outbox → owning Agent claim
        ↓
观察身份 → action intent → provider effect → 验证
        ↓
repository 原子提交 outcome + resource state
        ↓
带执行轮次 / lease proof 的完成回报
```

`unknown` 不等于不存在。空 state 不证明外部资源不存在。checkpoint 仅用于诊断；action journal 是恢复授权来源。恢复只提交已授权且可证明的结果，不重放未知 guest 指令。

## 已交付的合同

| 范围 | 当前实现 |
|---|---|
| 生命周期 repository | 文件、SQLite、Postgres 共享 aggregate、状态转换、身份校验、CAS；backend 负责物理锁/事务 |
| API metadata | Run、Agent、command、NodeOperation、Guest operation 使用共享 codec/状态规则；operation + command 原子 outbox；完成回报同事务核对当前租约 |
| 幂等受理 | Apply 支持 Idempotency-Key；相同请求返回同一 Run，改参409，终态不重执行；三后端禁止同 topology 同时准入不同活跃 Run |
| Run 轮次 | command ID 包含 Run ID/queued_at/revision；claim 必须携 queued_at/revision；完成检查 owner/attempt/lease，旧轮次拒绝 |
| 输入补交 | 敏感输入不写入 Run/command/request 记录；API 重启缺输入返回503，原请求重放补交；GET Run 返回 inputs_required；Agent 503重试保留轮次证明 |
| 配置固定 | keyed Apply 只 materialize immutable snapshot，不改 mutable workspace；预检与执行使用 Run.SnapshotPath；后续生命周期有 placement 约束的已准入 snapshot 解析 |
| 状态证据 | cleanup-inspection 原子读取 state+journal，另外读取 Run/NodeOperation；暴露来源、时间、范围及 unfinished actions；external_presence=unknown，不签发空清理回执 |
| 远端恢复 | run-recovery.v1 经 owning Agent 执行 Observe/Recover；报告包含资源 UID/generation、外部 ID、action/Run 轮次与 lease 证明；持久保存报告 |
| Provider 管理身份 | Docker labels、libvirt metadata/UUID、Firecracker manifest/进程锚点校验 owner/resource/generation；缺身份明确拒绝 |
| Firecracker | manifest v2、进程文件锁、pidfd 信号、冷 discover/adopt、controller crash 接管、reset generation 迁移、基于持久 VM 操作锁的 quiescence proof；vmStore 只是进程内缓存 |
| 网络与 attachment | 网络 ownership v2 保存 namespace device/inode、ifindex/type/alias；目标网络身份独立于节点；veth/TAP 通过固定句柄操作；FC/libvirt 启动前复验 |
| Firewall | 默认引擎创建/发现/恢复/删除；nft 原子 batch、固定 namespace FD、policy generation 与删除证据；旧结果不能授权副作用重放 |
| Guest 传输 | Connection.Exec 为共同入口；SSH 与 vsock 文件传输校验 SHA256、临时文件同步与精确目标替换；未知退出状态不能视为成功 |
| vsock v2 | exec.v2 支持至多8MiB stdin；write_file.v2 要求 SHA256 并返回匹配回执；guest 限定根目录、阻止路径逃逸；取消关闭连接 |

keyed Apply 后 workspace 当前指针可以不存在；活跃操作列表从 durable Run 显示 active_run_id/active_revision。有持久 placement 的远端 topology 在 Run 结束后仍保留在目录，并显示 placement_agent_id；无本地资源证据时 phase=unknown，不标成 gone。删除 placement 后终态历史不复活 workspace。非空 revision 必须是已发布全局 revision，旧任意标签拒绝。

## 明确不支持或不保证

| 能力 | 边界 |
|---|---|
| 多 API 共享文件系统执行 | 当前限定单 API 执行部署。PG 准入锁验证并发受理，不代表分布式资源端 fencing；全局锁目前偏保守 |
| 同代际 lease epoch 撤销 | 可以拒绝旧回报，不能远程撤销已经开始的 guest/provider effect；Firecracker 已能用持久 VM 操作锁证明宿主侧同步 transition 已退出，Docker/libvirt 尚无等价 provider operation anchor，仍 fail-closed |
| Inventory/projection | 信息性快照，共享校验与原子文件替换；没有生产者 epoch/sequence，不作为所有权或外部不存在证明 |
| 清理检查完整性 | active_operation_kinds 只含 Run/NodeOperation；guest execution/file 不在该列表；列表和 lifecycle 不是同一跨仓储事务 |
| 外部不存在判定 | cleanup-inspection 不执行 provider 查询；远端未观察时明确 owning_agent_not_observed。metadata 删除仍必须通过 repository CAS/journal 校验 |
| 远端 metadata 删除 | 最新 Destroy 的持久回执绑定 owning Agent、topology UID、state serial、Run轮次与租约；当前placement匹配且无后续操作时支持普通删除。缺回执仍409；force仅删除元数据，活跃操作与本地未完成journal仍阻止删除 |
| sysbox_ssh_access | 已迁移到版本化 grant + host supervisor apply/observe/delete；真实独立 sshd E2E 已验证受管 key 生效与删除失效。共享 sshd drop-in 生命周期和完整 owning-agent 终态仍待收尾 |
| Import / 自动 GC | adoption 与 GC 已有持久 plan/grant/effectgate 路径；Docker/network/libvirt/Firecracker 删除 callback 已接入 supervisor。CLI raw execute 迁移、Firecracker orphan 枚举和跨宿主执行矩阵仍待收尾 |
| State 运维 | aggregate backend 未支持 snapshot/restore、lock inspection、force-unlock；HTTP501，不能返回空成功 |
| 后台 SSH/vsock | 已使用统一 durable task manifest、boot/process/cgroup identity 和 recovery Observe/Cancel；完整 Agent/API/helper 独立进程重启故障矩阵仍待验收 |
| stdin | vsock 有界预读；流式输入必须是取消时 Close 能解除 Read 的 ReadCloser。交互输入使用 console；后台 stdin 不支持 |
| 文件传输依赖 | SSH guest 需 sh、mktemp、sha256sum、chmod、mv -T、sync -f；vsock 白名单根目录须已存在；缺工具/校验失败返回错误 |
| Docker NAT 跨 provider | Docker 节点原生 NAT 支持；Firecracker/libvirt 不能猜测 Docker bridge 接入，明确 Unsupported |
| 外部特权 writer | host lock 约束合作进程；不能声称阻止刻意同 ifindex/UUID 重用，或 daemon 按名称接入前的管理员替换 |

## 破坏性升级

- 旧 state/aggregate、NodeOperation/Guest record、缺完整 SQLite Run/command/event 快照明确要求重建。SQLite/Postgres API schema7 只初始化空库或打开当前版本；不自动补列、不执行旧迁移阶梯、不回退旧索引字段。
- Firecracker manifest 必须 v2，网络 ownership 必须 v2；缺 attachment target/proof 拒绝。
- CLI/API/Agent 同步升级。vsock exec/file 操作名升级为 v2，旧 guest 不兼容，需重建注入新版 sysbox-init 的 VM。
- 不手工修改 version，不单独删除 state 隐藏残留。旧版按实际归属清理后重建；不自动删除用户基础设施。详见[升级说明](../operations/upgrades-and-recovery.md)。

## 验证与交付界限

本轮三后端合同使用独立临时 PostgreSQL16 验证；全仓 Go 回归、API/Agent/persistence/runtime/transport/state/guest-init 的 race、vet、docs 检查均通过。schema7的新建、并发初始化、重开及旧库不变性测试通过。真实网络/TAP、Docker、libvirt、Firecracker KVM 已有独立测试入口。

新增 `make test-remote-docker-fault-e2e` 使用生产 HTTP/WebSocket Agent、真实 Docker 和分离的 API/Agent SQLite repository：完成已持久化后丢弃 HTTP 回执，重建 API/Agent，原请求重放仍为同一 Run/唯一容器；旧 claim/完成409，远端 Observe 返回同一 UID/generation。它使用同一宿主共享 immutable snapshot 文件，API 是对象重建，不是 OS SIGKILL；不称为跨宿主 HA 或所有 provider 故障矩阵验收。

真实 Firecracker 生命周期测试新增 stdin 与文件校验往返；controller crash/reset/跨进程恢复测试独立保留。传输的取消只保证解除本地等待，除明确远端证明外不宣称远端进程终止。

销毁回执由Agent在topology锁内对state+journal原子检查后生成；零serial/丢失state、剩余资源、未完成或较新action均拒绝。API在同一Run完成事务中核对租约并保存回执；SaveRun/Restart/outbox不能伪造，终态重试不能追加或改写。新Run、NodeOperation或placement重绑使旧回执失效。真实Docker销毁→API重建→无force metadata删除已通过。

顺序核查补强：Agent pause/resume在构造provider handle前核对已派发命令的UID/generation，并在state读取至effect完成期间持有topology锁；迟到旧代不能借当前handle操作新节点。Agent旧import直接SaveState执行链已删除。远端缺失snapshot不再回退可变workspace，CLI显式选择的本地配置入口保留。

本轮KVM恢复测试通过。曾观察到文件安装偶发失败，已定位为 guest RPC 过早可用：sysbox-init 在 systemd/tmpfiles 初始化完成前启动 vsock，tmpfiles 删除上传暂存目录，随后 chmod 报 ENOENT。现在 `ready.v1` 将 liveness 与 initialized 分离：systemd 同时要求 `sysinit.target` 和 `systemd-tmpfiles-setup.service` active/success；自定义 init 必须发布匹配当前 boot ID 的 `/run/sysbox/boot-ready`。Exec/write_file 在 ready 前拒绝；真实延迟 tmpfiles KVM 测试验证 ready 前无副作用、ready 后20次安装成功。

后续必达范围：资源端同代租约撤销、多宿主故障矩阵、durable guest task协议、显式ownership adoption、自动GC、aggregate snapshot/lease运维、完整guest SSH access及网络能力迁移。它们属于用户确认的“完整能力迁移与分布式执行闭环”，不再默认排除在目标之外；实施顺序与验收门槛见[完整迁移实施计划](2026-10-07-full-migration-distributed-execution-plan.md)。当前Unsupported仅是尚未完成状态。
