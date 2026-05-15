# sysbox

> AI 红队的 Terraform —— 一键搭起 Linux 攻防战场，实时采集 eBPF 行为轨迹。

## 概览

sysbox 做三件事，且只做这三件事：

1. **拓扑编排**：用 HCL 描述节点/网络/路由/防火墙，底层通过 Docker + linux-bridge 把隔离实验室拉起来。
2. **全量 syscall 采集**：tracee eBPF（mntns 级 scope）持续把每个节点的事件追加写入 `runs/<id>/events/<node>.jsonl`。
3. **Agent 远控**：在指定节点里 host 一个 opencode（或任意 ACP 兼容 agent），暴露 HTTP ACP 接口；调用方通过 ACP 操作 agent 干活。

**不做**的事情：归因、IoC 打分、reward 计算、episode 边界管理。这些都是 sysbox 之上的应用层关注点，由调用方在原始 `events/*.jsonl` 之上自由构建。

```
HCL topology
  └─ sysbox apply       → Docker containers + linux-bridge networks
  └─ sysbox sensor start→ tracee (mntns-scoped) → per-node events/*.jsonl
  └─ opencode actor     → ACP HTTP endpoint (operator calls in)
```

## 要求

- Linux kernel（netns 支持，6.x 推荐）
- Docker daemon（docker substrate 必需）
- Go 1.22+
- `apply` / `destroy` / `sensor` 需要 root（netlink + eBPF）
- 跑 microVM（firecracker substrate）额外需要：`firecracker` 二进制、`mkfs.ext4`、`losetup`、`/dev/kvm`。
  vmlinux 由 `sysbox_kernel` 资源在 apply 时按 URL 自动拉取并缓存；rootfs 用
  `./scripts/prepare-fc-rootfs.sh` 一键生成（基于 firecracker-ci 官方 ubuntu squashfs，参考 [`docs/firecracker-vmbox.md`](docs/firecracker-vmbox.md)）。

## 快速开始

```bash
make build          # 编译 bin/sysbox
make lab-up         # 搭建三节点实验室 + 启动 eBPF 传感器
make lab-status     # 查看节点、状态、传感器
make test-e2e       # 运行 e2e 测试（apply/路由/drift/destroy）
make lab-down       # 销毁实验室
```

## 目录结构

```
sysbox/
├── bin/                        # 编译产物（gitignore）
├── cmd/
│   ├── sysbox/                 # 主 CLI（apply/plan/sensor/state/output）
│   └── sysbox-init/            # firecracker guest PID-1 wrapper（cross-compiled，go:embed 进主二进制）
├── examples/
│   ├── three-nodes/            # Docker 三节点攻防实验室
│   └── microvm/                # Firecracker microVM 拓扑（sysbox_kernel + vsock provisioner）
├── pkg/
│   ├── artifact/               # URL/本地文件解析 + sha256 校验 + 内容寻址缓存
│   ├── config/                 # HCL 解析 + schema
│   ├── graph/                  # 资源依赖图
│   ├── monitor/                # eBPF 监控 Backend 接口 + TraceeBackend
│   ├── provider/               # Docker / firecracker / exec / network 底层实现
│   │   └── firecracker/        # microVM substrate + sysbox-init initbin embed + config drive
│   ├── runtime/                # apply/destroy executor + drift detection
│   ├── sensor/                 # Event schema + tracee JSON 解析
│   ├── sink/                   # JSONLSink + RoutingSink（per-node 文件路由）
│   ├── state/                  # 状态文件（原子写 + 文件锁）
│   ├── substrate/              # 底层抽象注册表
│   └── vsockrpc/               # firecracker provisioner 的 host/guest 共享 RPC 协议
├── runner/                     # Python ACP 客户端
└── tests/
    └── e2e/                    # Go 集成测试（build tag: e2e）
```

## HCL 资源类型

| 资源 | 说明 |
|---|---|
| `sysbox_image` | 镜像声明。`docker_ref` 走 docker registry；`rootfs` 走本地路径或 URL（ext4，firecracker 用），可选 `sha256` 校验 |
| `sysbox_kernel` | Firecracker 用的 kernel 工件。`source` 接 URL/本地路径，可选 `sha256`，自动缓存到 `~/.cache/sysbox/artifacts/` |
| `sysbox_network` | linux-bridge 网络，带 CIDR；`nat=true` 走 Docker bridge 出公网 |
| `sysbox_node` | 节点。docker substrate 是容器；firecracker substrate 是 microVM（通过 sysbox-init + vsock-rpc 提供 provisioner 通道，rootfs 不再需要 sshd） |
| `sysbox_router` | 多接口路由节点 |
| `sysbox_firewall` | nftables 规则附加到指定网络 |
| `sysbox_ssh_access` | 给指定节点开 SSH 入口 + 注入 authorized_keys |
| `sysbox_actor` | 容器内 host 一个 ACP-compatible agent（如 opencode）|
| `sysbox_monitor` | eBPF 监控声明，指定监控节点列表和事件集 |

详细的 firecracker / microVM 用法见 [`examples/microvm/README.md`](examples/microvm/README.md)。

## 事件输出

```
runs/default/events/
  node_attack.jsonl   # 每节点一个 JSONL，sensor 启动后持续 append
  node_web.jsonl
  node_db.jsonl
```

每次 `sensor start` 会在每个文件的首部写一条 meta 事件：

```json
{"node_id":"node_attack","category":"meta","raw":{"meta":"sensor_start","sensor_run_id":"...","started_at":1715...}}
```

下游可以根据这条 marker 自行切片，不需要跟 runner 做任何协调。

## Make targets

```
make build                  编译 bin/sysbox（自动 cross-compile sysbox-init 并 embed）
make build-init             仅 cross-compile sysbox-init.bin（linux/amd64，静态）
make test                   单元测试（无需 Docker）
make test-e2e               Go 拓扑集成测试：apply/route/drift/destroy（需要 Docker + root）
make lint                   fmt + vet
make lab-up                 搭建实验室 + 启动传感器
make lab-down               销毁实验室
make lab-sensor-restart     传感器重启（节点重建后重新解析 mntns）
make lab-logs               tail 传感器日志
make lab-status             查看容器 / state / 传感器状态
make clean                  删除编译产物
```

## 运行一次 Episode

```bash
# 1. 确保 lab 已启动
make lab-up

# 2. 配置环境变量（.env 或 export）
export DEEPSEEK_API_KEY=...
# 或 ANTHROPIC_API_KEY / ANTHROPIC_BASE_URL，取决于 opencode 的 provider 配置

# 3. 通过 ACP 让 agent 跑一轮
uv run python3 examples/three-nodes/run_opencode.py

# 4. 查看原始事件（自己分析）
ls runs/default/events/
tail -f runs/default/events/node_attack.jsonl
```
