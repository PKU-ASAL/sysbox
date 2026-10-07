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

SQLite/Postgres API schema 升为7：只在空数据库/schema 中初始化，在当前版本上重开；旧版、未知版或无版本的既有 API 表直接拒绝，不运行迁移阶梯。旧库先停写、保留恢复证据并按项目范围明确重建；不删除共享数据库的其他项目。

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
