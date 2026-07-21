# Agent Operations

Agent 是持有宿主机执行权限的工作节点。API 负责 topology/run 调度，Agent 执行共享 Sysbox runtime。API container 本身不挂载 Docker socket、KVM 或 libvirt 权限。

## Registration And Deployment

本地 Compose 开发环境：

```bash
cp .env.example .env
make api deploy-full
```

`deploy-full` 注册 Agent identity，再启动 `sysbox-agent`。生产环境应持久化 Agent ID、secret 和 workspace，并通过 secret manager 提供凭据。

## Capabilities

Agent 根据已注册 driver 暴露 Docker、Firecracker、libvirt、network/policy 等 capability。Scheduler 只把满足 topology requirement 的 run 分配给 Agent。

不要为了“通用 Agent”授予所有宿主机权限。按工作负载拆分 capability pool，尤其隔离 Docker socket、`/dev/kvm`、libvirt socket 和 host network administration。

## Heartbeat And Inventory

Agent 周期提交 heartbeat、protocol version、capability 和 inventory。Supervisor 将超时 Agent 标记为 offline。Inventory 是调度和调查投影，不替代 topology state。

检查：

```bash
curl http://127.0.0.1:9876/v1/agents
curl http://127.0.0.1:9876/v1/agents/AGENT_ID/inventory
```

## Command And Run Lease

Agent 领取 command 后 ACK、start，并 claim run lease；长操作持续 renew。Lease 防止同一 run 被两个 Agent 执行，但不替代 backend locking/CAS。

失联 Agent 恢复时不得直接重放过期 command。先确认 run/lease 状态，再由控制面决定 resume、recover 或 cleanup。

## Quarantine And Drain

维护前：

1. 禁止新调度或 quarantine Agent；
2. 等待 active run 完成；
3. 检查未完成 command 和 lease；
4. 备份 workspace、state 与 checkpoint；
5. 再停止进程。

## Upgrade

API 与 Agent 必须使用兼容 protocol。升级后确认 registration、heartbeat、capability、inventory、command stream 和一个无副作用 validate/plan，再恢复调度。

Agent 的 topology state 默认位于其 workspace；迁移到另一宿主机时必须同时迁移 state/checkpoint/artifact cache 所需 identity，不能只重新注册相同名称。
