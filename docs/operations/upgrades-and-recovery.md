# 升级与恢复

## 升级前

1. 记录当前 Sysbox version/commit。
2. 等待 CLI/API run 和 Agent lease 完成。
3. 备份 topology state、checkpoint、API database 和 workspace。
4. 保存 artifact digest 与外部 ownership inventory。
5. 阅读目标 release 的 state/protocol compatibility。

远端 API 与 Agent 使用独立的 workspace、runs 和数据库目录。普通执行与恢复都通过已授权的不可变 revision 传输配置，不共享 API 的 SnapshotPath。API/Agent 必须一起升级；旧 Run 缺少全局 revision 时重新发布并受理操作，不回退读取共享路径。

远端 kernel/rootfs 等二进制产物使用 revision 内相对文件或带 SHA-256 的 HTTP(S) URL。把 API 主机绝对路径改成上述来源；Agent 校验下载内容并使用自己的缓存。CLI 的明确本地路径不受该远端限制影响。大型产物使用带摘要的下载来源，不应塞入有大小限制的 revision JSON。

## State schema 变更

若目标版本拒绝当前 state，不要手工改 JSON 或 private envelope。用创建旧 state 的 binary destroy，再用新版本 apply。若必须保留现场，继续运行旧版本直到能够计划迁移。

删除 state 不会删除外部 container、VM、network 或 policy，只会丢失安全清理所需 identity。

当前重构分支不提供旧格式兼容：Firecracker manifest 必须为 v2；网络 ownership anchor 必须为 v2；attachment 必须包含目标网络身份，libvirt 的桥接记录也必须包含 target。旧记录报“需要重建”时，不要手改 version 或填入猜测的 identity。先用旧二进制清理已确认归属的资源，再重建对应本地 state、数据库和 provider 工作目录。共享数据库或共享宿主机上的其他项目不属于这个清理范围。

CLI、Agent 与 provider 必须一起升级，并共享同一宿主 namespace 视图和 `/var/lib/sysbox/networks` 所有权记录/锁目录。Firecracker/libvirt 直接接 Docker NAT 网络的配置需改为隔离网络；Docker 节点的原生 NAT 接入保留。

本轮同时删除 SQLite 自动补列、旧 Run/command/event 完整记录缺失回退与任意 revision 标签兼容。缺 schema/快照的数据库必须重建；重新发布配置取得全局 revision，不能继续用旧任意字符串标签。

SQLite/Postgres API schema 当前为14：只在空数据库/schema 中初始化，在当前版本上重开；旧版、未知版或无版本的既有 API 表直接拒绝，不运行迁移阶梯。旧库先停写、保留恢复证据并按项目范围明确重建；不删除共享数据库的其他项目。

Guest vsock 的执行/文件协议变为 `exec.v2`/`write_file.v2`。重建 sysbox-init 后重建 VM；旧运行中的 guest 不支持新协议，禁止混用或回退。SSH 文件安装依赖 guest 的 `mktemp`、`sha256sum`、`mv -T` 和 `sync -f`；vsock 文件写入仅限已存在的白名单根目录。两种传输都要求校验成功后才返回安装成功。

vsock 的 ping 只证明连接存活，不代表 guest 已初始化。新版 agent 必须等待 `ready.v1`；在 tmpfiles/systemd 或自定义 boot marker 未完成前，exec/file-write 都被拒绝，不会产生副作用。升级时 guest-init 与 host transport 必须一起更新。

API 重启后，带 Idempotency-Key 的活跃 Apply 若缺敏感输入，claim 返回503。调用方向 Agent 当前连接的 API 重放完全相同的请求补交输入；终态 Run 不会重新执行。该机制不承诺多 API 共享内存或完整多控制器执行隔离。

## 中断的 mutation

保留 action journal、state 与诊断 checkpoint，并按照 [Troubleshooting](../guides/troubleshooting.md) 收集 observation。Recovery 必须从原 topology、state lineage 和 plan fingerprint 开始；checkpoint 不能授权重放副作用。

当 provider 不可达或返回 unknown 时，先修复权限、daemon 或 connectivity。不要将 unknown object 强制标记为 absent。

## Backend 恢复

恢复 Local/SQLite/Postgres 后核对 lineage、serial、snapshot、lock 和 CAS。API database 与 Agent-local topology state 是不同数据层；只恢复其中一个可能留下 run projection 与实际 state 不一致。

## 残留清理

优先使用 action journal 恢复与经过身份验证的 provider delete。手工清理只能在完整记录 ownership evidence、external ID 和依赖后进行，并且不能扩大到名称相似的非受管对象。

远端 topology 的正常流程是：owning Agent 完成 Destroy并提交持久cleanup回执，再执行普通metadata DELETE。回执绑定topology UID、state serial、Run轮次和租约；API重启后仍可使用。新操作或placement重绑会使旧回执失效。旧Destroy终态没有回执时不能补造证明，需要新一轮可验证Destroy；Agent丢失state则拒绝出具回执，须人工核对实际资源。force只删除metadata，活跃操作与本地未完成journal仍阻止删除。

## Compose 维护

`make api clean` 会删除本地 Postgres volume 和 API workspaces，只适合可丢弃开发环境。修改 Postgres password 后，已有 volume 仍保留旧数据库凭据；应执行受控数据库迁移或明确重建。

## Aggregate format 6

GC reservation 使生命周期 aggregate 升为 format 6；format 5 正常读写明确拒绝。
`state import-format5` 提供显式离线转换：先停止全部旧 CLI/API/Agent/host writer，
预览源摘要，再带 `--apply --offline --source-sha256 --archive` 保存原文并提交转换。
转换覆盖保留 snapshot，保留未知 action、reset continuation 和原拓扑/资源身份，
不清理 provider、不补造停止证明。API schema 仍为14。现有实验数据未自动转换。

Firecracker 删除现在在移除 generation 前，将绑定原 action 的进程停止证据写到
受管根目录的 `.locks` 下。冷恢复还需核验同一身份、无目录/socket/进程残留。
普通 unlocked manifest 不再证明 VMM 内未知请求已经结束；跨宿主 reboot/PID 身份
冲突仍保守拒绝。缺旧证据的历史 intent 不能自动升级为成功。

## Execution authority format 5

宿主 fencing 请求、固定控制器公钥、签名回执、显式释放和删除处置记录使用 execution authority format5；
它与 aggregate format6、API database schema14 是不同的版本边界。旧 format2/3/4
ledger 明确拒绝，新 ledger 也不能交给旧 writer。当前没有自动转换。
升级前停写并保留完整旧账本；仅对明确可丢弃的项目建立新的 authority 存储。
不得通过删除 ledger 或修改 version 来解除旧 grant、未知 intent 或宿主冻结。

新增 fencing API 仅用于显式登记的隔离验收宿主。收到有效签名只保存停止证据，
不把未知请求认定成功，也不允许重放。冻结覆盖该 Agent 的其他 topology，且跨
重启保留；普通 maintenance release 无法解除它。显式 release 仅支持已独立完成
删除、全 grant 已停止且签名观察证明 absent 的空 topology；完整锚定的本地 Docker/libvirt 未知删除可走下述 reconcile-delete；其他未知 intent
仍不能自动处置。不要把真实历史现场登记到这个验收接口。

## Fencing 后的只读观察

对已登记的可丢弃验收宿主，先读取
`GET /v1/topologies/{name}/host-fences/{id}/recovery-plan`。收到 fencing 回执且
原始 Run/journal 摘要未变后，可向同一路径下的 `observe` 发 POST，带固定
`Idempotency-Key`，取得 owning Agent 的观察 command ID；再用
`recovery-plan?observation={command_id}` 查看关联报告和剩余阻断项。

该流程只派发 observe，不派发 recover。命令重试不会覆盖旧租约或复活终态命令。
若 journal 已漂移，原 key 仍可查询原命令，新观察请求返回冲突；处置计划将旧报告
标为 stale。API 重启后继续读取同一 durable report。此步不需要新增存储格式，仍用
现有 command/report schema；显式释放及删除处置使用 authority format5。

观察到 present/absent 不足以证明旧副作用的完成结果。新版 supervisor 会对绑定 fence 新 boot 的观察报告签名；API 验证宿主公钥、
fence 摘要、新 boot、完整报告及投递租约后，只消除缺少宿主签名这一项阻断。
旧无签名报告仍只作信息性观察；缺失或轮换宿主公钥会使已有证明无法验证。
原 operation anchor、guest completion 或 reset phase 缺失仍继续阻断。
不得通过修改 journal 或删除 authority 来绕过阻断。真实历史现场仍只生成只读计划，
不纳入可丢弃宿主的准入、重启或处置执行。


签名观察在现有 report JSON 中增加可选 `host_observation`，本身不改变 aggregate6 或 API schema14。请同步升级 API、Agent 和 supervisor；旧 writer 可能
忽略或丢弃该可选字段，因此不要把签名记录交给旧 writer 重写。旧报告不补造签名，
缺证明时继续保留冻结。宿主私钥只由 supervisor 持有，API 配置对应公钥；fencing
外部控制器密钥与宿主密钥仍是两个不同的信任来源。


## 显式解除已清理 topology 的 fencing

只有已终态 destroy Run、全 succeeded journal、空 state、签名 absent 报告和独立
host stop receipts 齐备时，才可 POST `.../host-fences/{id}/release`，正文指定
`observation_command_id`，请求头携固定 `Idempotency-Key`。接口同时检查 pending
GC/reset、Run/guest/node/console/command；任一条件不满足则409并保留冻结。

释放回执保存到 authority format5，记录观察报告、公钥、lifecycle 摘要与时间。
同 key 重试只读回原结果，不影响新工作或后续冻结。释放不改写 intent、Run、state，
也不会伪造 grant stop。缺停止证明的旧 unknown intent 仍不能走这条路径。该路径
当前只覆盖已完成删除的空 topology；有存活资源的恢复释放仍不支持。


## 已 fencing 的未知删除闭环

限定隔离验收宿主、原始删除锚点完整、候选与 state 身份精确匹配、无子任务/其他
未完成操作、Docker/libvirt 本地 endpoint、宿主签名确认 absent 且删除残留已消失。
先取得 `observe` 的签名报告，再 POST `.../reconcile-delete`，携固定 key 与
`observation_command_id`。完成后再单独调用 `release`；不能省略任何证据步骤。

处置提交删除 action/state 结果，但将中断 Run 保留为 failed/nonrecoverable，
不会伪装原执行成功。停止记录明确引用 fence-delete 证据。文件后端可能返回错误
但已有 prepared 决策：保留同 key/报告重试，不能删除记录或改版本；只允许原始或
已计划 postimage 继续。SQL 后端在一个事务内完成。authority5 不能由旧 writer 打开，
当前没有旧账本自动迁移。历史现场仍只读，缺原始锚点的 intent 不能补造证明。

快照元数据恢复现在允许保留完全相同的未完成 journal：快照与当前双方的 unresolved
记录必须逐字段一致，当前 journal 不变，serial 只增不减。新增、缺失或发生变化的
未完成证据仍拒绝恢复。恢复不证明外部资源状态，也不解除独立 fencing hold。

保留策略通过 `state/snapshots/retention-plan` 预览与 `retention-apply` 提交；每批
最多128项，至少保留最新一项，保留未完成 journal/workflow 和 restore 回执引用。
完整 catalog 摘要变化使计划失效；删除只影响快照元数据，不执行 provider GC。

### Explicit fenced intent disposition (development branch)

`POST /v1/topologies/{topology}/host-fences/{fence}/reconcile-intents` accepts an
explicit `decision` plus `observation_command_id`; `Idempotency-Key` must equal
`decision.id`. Obtain the original journal digest and verified observation digest
from the existing `recovery-plan` endpoint. The decision binds Run incarnation,
revision, expected state serial, both digests, reason and exact action/resource
choices (`retain` or `absent`). Only admitted whole-host fencing and fresh signed
local-provider observation can validate it; operator acknowledgement alone cannot.

The current slice supports non-composite actions with complete candidate identity,
no unresolved attachments, no replacement/reset continuation and no active child
or unrelated operation. Retention needs usable provider private state. Missing
create identity, Reset, incomplete guest initialization and external effects remain
blocked pending their dedicated disposition protocols. A remote daemon is outside
a local-host fence's cessation scope.

On commit, the original Run fails with its unknown result stated explicitly. Old
action status/result remain unchanged; a separate disposition archive prevents
replay and retains GC references. A retained resource is unknown, not initialized
or healthy. File storage persists its decision before lifecycle/dispatch writes;
SQL commits atomically. Release occurs only after exact postimages are durable.
Retries cannot rewrite a successor. Recovery-plan reports the completed disposition
without describing the historical journal as a currently executable plan.

This path writes aggregate format 15 and authority format 8. Drain old writers and
synchronize API/Agent/CLI before use; old readers must reject the new format.
Previous format 13/14 recovery records remain readable. This branch is not a
production upgrade or a declaration that all Run/Reset/Guest recovery is closed.
