# Sysbox 生命周期重构：当前实施状态

更新：2026-10-07。工作分支：`refactor/observation-driven-runtime`。未合入 main、未推送。

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
| Firecracker | manifest v2、进程文件锁、pidfd 信号、冷 discover/adopt、controller crash 接管、reset generation 迁移；vmStore 只是进程内缓存 |
| 网络与 attachment | 网络 ownership v2 保存 namespace device/inode、ifindex/type/alias；目标网络身份独立于节点；veth/TAP 通过固定句柄操作；FC/libvirt 启动前复验 |
| Firewall | 默认引擎创建/发现/恢复/删除；nft 原子 batch、固定 namespace FD、policy generation 与删除证据；旧结果不能授权副作用重放 |
| Guest 传输 | Connection.Exec 为共同入口；SSH 与 vsock 文件传输校验 SHA256、临时文件同步与精确目标替换；未知退出状态不能视为成功 |
| vsock v2 | exec.v2 支持至多8MiB stdin；write_file.v2 要求 SHA256 并返回匹配回执；guest 限定根目录、阻止路径逃逸；取消关闭连接 |

keyed Apply 后 workspace 当前指针可以不存在；活跃操作列表从 durable Run 显示 active_run_id/active_revision。有持久 placement 的远端 topology 在 Run 结束后仍保留在目录，并显示 placement_agent_id；无本地资源证据时 phase=unknown，不标成 gone。删除 placement 后终态历史不复活 workspace。非空 revision 必须是已发布全局 revision，旧任意标签拒绝。

## 明确不支持或不保证

| 能力 | 边界 |
|---|---|
| 多 API 共享文件系统执行 | 当前限定单 API 执行部署。PG 准入锁验证并发受理，不代表分布式资源端 fencing；全局锁目前偏保守 |
| 同代际 lease epoch 撤销 | 可以拒绝旧回报，不能远程撤销已经开始的 guest/provider effect |
| Inventory/projection | 信息性快照，共享校验与原子文件替换；没有生产者 epoch/sequence，不作为所有权或外部不存在证明 |
| 清理检查完整性 | active_operation_kinds 只含 Run/NodeOperation；guest execution/file 不在该列表；列表和 lifecycle 不是同一跨仓储事务 |
| 外部不存在判定 | cleanup-inspection 不执行 provider 查询；远端未观察时明确 owning_agent_not_observed。metadata 删除仍必须通过 repository CAS/journal 校验 |
| 远端 metadata 删除 | 有 placement 时普通删除409，完成 Destroy 的状态本身不替代持久空状态证明。当前须显式 force，仅删除元数据；活跃 Run/NodeOperation 与本地未完成 journal 仍阻止删除 |
| sysbox_ssh_access | 保留识别/诊断，Create/Read/Delete 明确 Unsupported；已删除不可验证的安装/启动脚本和仅删除 state 的假清理 |
| Import / 自动 GC | 未实现 ownership adoption；import 派发前拒绝。GC 只允许可信 OwnerUID 范围的 dry-run |
| State 运维 | aggregate backend 未支持 snapshot/restore、lock inspection、force-unlock；HTTP501，不能返回空成功 |
| 后台 SSH/vsock | Start 仅代表一次启动回执，不承诺重启后 Observe/Cancel；相关可选 SPI 不实现，不能通过 PID 猜测任务身份 |
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

后续独立项目：资源端同代租约撤销、多宿主故障矩阵、durable guest task 协议、显式 ownership adoption、完整 guest SSH access 管理。它们不通过兼容回退或下游业务逻辑代替。
