# Upgrades And Recovery

## Before Upgrade

1. 记录当前 Sysbox version/commit。
2. 等待 CLI/API run 和 Agent lease 完成。
3. 备份 topology state、checkpoint、API database 和 workspace。
4. 保存 artifact digest 与外部 ownership inventory。
5. 阅读目标 release 的 state/protocol compatibility。

## State Schema Changes

若目标版本拒绝当前 state，不要手工改 JSON 或 private envelope。用创建旧 state 的 binary destroy，再用新版本 apply。若必须保留现场，继续运行旧版本直到能够计划迁移。

删除 state 不会删除外部 container、VM、network 或 policy，只会丢失安全清理所需 identity。

## Interrupted Mutation

保留 checkpoint，并按照 [Troubleshooting](../guides/troubleshooting.md) 收集 observation。Recovery 必须从原 topology、state lineage 和 plan fingerprint 开始。

当 provider 不可达或返回 unknown 时，先修复权限、daemon 或 connectivity。不要将 unknown object 强制标记为 absent。

## Backend Restore

恢复 Local/SQLite/Postgres 后核对 lineage、serial、snapshot、lock 和 CAS。API database 与 Agent-local topology state 是不同数据层；只恢复其中一个可能留下 run projection 与实际 state 不一致。

## Residue Cleanup

优先使用 checkpoint cleanup 和 handler/provider delete。手工清理只能在完整记录 ownership evidence、external ID 和依赖后进行，并且不能扩大到名称相似的非受管对象。

## Compose Maintenance

`make api clean` 会删除本地 Postgres volume 和 API workspaces，只适合可丢弃开发环境。修改 Postgres password 后，已有 volume 仍保留旧数据库凭据；应执行受控数据库迁移或明确重建。
