# Contributing

## Setup

```bash
go version
go mod download
go build ./cmd/sysbox ./cmd/sysbox-init
go test ./...
```

需要 API/Web 时使用根 Makefile 的 `api` 目标；前端位于 `web/manager`。

## Code Boundaries

- `pkg/config`：HCL 与 schema。
- `pkg/graph`：资源图。
- `pkg/runtime`：handler、planning 和 lifecycle orchestration。
- `pkg/driver`、`pkg/substrate`：capability contract。
- `pkg/provider/*`：具体外部操作。
- `pkg/state`：backend、locking、CAS 和 snapshot。
- `pkg/api`、`pkg/agent`：控制面和远程执行。

新增资源语义应进入 handler；新增宿主机操作应进入 capability/provider。Runtime 不应通过类型判断直接执行 Docker、libvirt、Firecracker 或 nftables 命令。

## Change Rules

- 行为变化先写失败测试。
- State schema 变化必须明确兼容或硬拒绝策略。
- 删除路径必须验证 ownership。
- Secret 不得进入 durable data 或日志。
- Provider 变更至少覆盖 create/observe/reset/destroy 与失败恢复。
- 用户可见行为更新其唯一 canonical 文档；README 只保留入口，不复制 reference。
- 设计讨论保留在 issue/PR/Git 历史，不在正式 docs 树新增临时计划。

提交前运行 [Testing](testing.md) 中与改动风险匹配的门禁。
