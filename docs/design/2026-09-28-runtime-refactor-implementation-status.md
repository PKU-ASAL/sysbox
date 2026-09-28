# Sysbox 生命周期重构：实施状态与验证边界

> 日期：2026-09-28
> 工作分支：`refactor/observation-driven-runtime`
> 核心实现提交：`72e227a`；未合入 main，未推送远端。
> Guest metadata 共享仓储提交：`3679f21`。
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

API 的 Run、Agent、命令队列等 metadata 仍在现有 store 中。GuestExecution 和 GuestFileOperation 已迁移到共享 `GuestRepository`；沿用原有物理表和路径，不新增第二套数据副本。它们与生命周期 aggregate 不属于一个跨域原子事务，不能把本次改动描述成“三套存储已全部合并”。

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

API 主机没有可信的 owning-agent 身份时，不能把远端 run 当成本机 run 恢复。远端恢复派发/协议尚需完成；当前应明确拒绝本机观测，不能因为本机没有 manifest 就证明远端资源不存在。

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

## 5. 验证记录

已运行的针对性测试覆盖：FS/SQLite 共用 repository 合同、并发 intent、丢失提交响应、旧阶段覆盖、畸形 aggregate、SQL 事务清理失败、SQLite 重开、CLI/Agent 地址一致性、SQL 列表、metadata 删除、Firecracker 冷恢复/reset，以及 transport 输出上限和未知退出码。

后续复审补充覆盖：Reset 路由重建与验证、guest/image-entry 完成证据、check 截断标志、console 输出 EOF 与退出结果的竞态，以及 pending topology 在列表和 state API 中的一致可见性。

Guest metadata 迁移通过独立复审和全仓无缓存单测/race/vet/docs 检查。合同测试覆盖 FS/SQLite 的重开、并发 CAS、跨进程锁、create 重试、终态和绑定保护、取消、格式损坏、私有字段及 HTTP 错误分类。Postgres 合同套件已接入，但当前未设置 `SYSBOX_TEST_POSTGRES_DSN`，运行时测试明确 skip；设置该变量后测试只在新建的隔离 schema 中操作，并清理该 schema。不得将未连接 Postgres 的通过结果当成真实数据库验收。

本阶段已通过 `go test ./...`、`go test -race ./...`、`go vet ./...`、`make docs-test` 和 `git diff --check`。未配置真实 Postgres 集成库，未启动真实 VM、容器或网络拓扑。带 `tests/e2e` 的普通 `go test ./...` 输出不代表真实部署测试已运行。

合入/发布前仍需：完整 metadata repository 迁移；剩余资源观测能力与 owning-agent 恢复链路；完整 fencing/outbox；真实 Postgres 与 substrate E2E；逐条核对接口清单中的行为变化。
