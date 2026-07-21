# Agent Management

Agent 是执行 provider 操作的宿主机本地节点。API 负责调度和持久化产品对象，Agent 负责持有 Docker socket、KVM、libvirt、网络权限和拓扑 runtime state。

## Deploy

```bash
cp .env.example .env
make api deploy-full
```

`deploy-full` 注册本地 Agent 并启动 `sysbox-agent`。API 容器本身不挂载 Docker socket。

## Identity And Heartbeats

Agent identity、protocol version、capability、disabled/quarantined 状态与 heartbeat projection 存在 API store。拓扑 state、checkpoint 和 artifact metadata 默认仍在 Agent 宿主机。

管理 API 包括注册、heartbeat、command stream、inventory、run claim/renew/complete。完整 endpoint 见 [API Reference](../reference/api.md)。

## Operating Rules

- 每个 Agent 使用独立 ID 和持久 workspace。
- 只授予它实际需要的 Docker/KVM/libvirt/network capability。
- 长时间失联时先 quarantine，再检查未完成 run 和 lease。
- 不要同时让两个 Agent 写入不支持 locking + CAS 的同一 state。
- 升级 Agent 前等待当前 run 完成，备份 state/checkpoint，并确认 protocol compatibility。
