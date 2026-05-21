# sysbox

> AI 红队实验环境的 Terraform-like 控制面：用 HCL 声明拓扑，用 CLI/API 可解释地 plan、apply、destroy，并通过可插拔 substrate 运行 Docker、Firecracker 或 VM 资源。

## 概览

sysbox 当前聚焦三层能力：

1. **声明式拓扑运行时**：用 HCL 描述节点、网络、路由、防火墙和 artifact，runtime 负责编图、plan、apply、destroy。
2. **多 substrate provider**：Docker 适合轻量容器实验，Firecracker/microVM/VM substrate 用于更强隔离的节点运行。
3. **服务态 API 控制面**：API 使用独立 workspace、state backend、run/checkpoint/lease，把本地 CLI 能力升级为可服务化的控制面。

sensor、actor、labeler、episode/reward、归因和 IoC 打分属于上层实验应用或可选资源，不再是 sysbox 核心 runtime 的边界。核心 runtime 只关心“期望拓扑是什么、外部资源当前是什么、如何安全收敛到期望状态”。

```
HCL topology
  └─ sysbox plan/apply/destroy  → runtime graph + provider CRUD
  └─ sysbox serve               → HTTP API + state backend + run checkpoints
  └─ artifact cache             → kernels/rootfs/qcow2/tools by explicit mount or on-demand fetch
```

## 要求

- Linux kernel（netns 支持，6.x 推荐）
- Docker daemon（docker substrate 必需）
- Go 1.22+
- `apply` / `destroy` 对部分 substrate 需要 root 或宿主机权限（netlink、tap、KVM、Docker socket）
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

## API / Docker 部署

API 是服务态控制面，默认使用独立数据目录和 Postgres state backend，而不是直接读写 CLI 的 `examples/` 和 `runs/`：

```
data/
├── workspaces/     # API 管理的 HCL
└── runs/           # run metadata + checkpoints；state 默认在 Postgres
```

启动 Docker-only API：

```bash
make docker-up
curl http://127.0.0.1:9876/v1/health
curl http://127.0.0.1:9876/v1/topologies/two-networks/preflight
```

启动 Firecracker 能力时显式挂载宿主机工具和 KVM：

```bash
export SYSBOX_FIRECRACKER_BIN=/home/jiandong/.local/bin/firecracker
make docker-up-fc
curl http://127.0.0.1:9876/v1/capabilities
curl http://127.0.0.1:9876/v1/topologies/mixed/preflight
```

`make docker-seed` 会把 `examples/*/field.sysbox.hcl` 初次复制到
`data/workspaces/`。之后 API 修改的是自己的 workspace 副本。

API 的 state backend 默认是 Postgres，并提供：

- topology state metadata/listing
- serial-based CAS，防止多进程写入时 last-writer-wins
- backend lease/lock metadata
- snapshots 和 checkpoint，帮助解释失败 apply/destroy 的进度

本地 CLI 默认仍使用本地 state 文件；需要共享服务态 state 时，可通过
`SYSBOX_STATE_BACKEND` 指向同一个 backend。

大文件不内置进镜像：kernel/rootfs/qcow2 走 `pkg/artifact` 按需下载或
显式挂载到 `/var/cache/sysbox`；Firecracker/qemu 等宿主机相关二进制
通过 substrate 专属变量（如 `SYSBOX_FIRECRACKER_BIN`）显式注入。

服务级环境变量命名约定：

| 变量 | 含义 |
|---|---|
| `SYSBOX_HOME` | 服务数据根目录，默认 `/var/lib/sysbox` |
| `SYSBOX_CACHE` | artifact/cache 根目录，默认 `/var/cache/sysbox` |
| `SYSBOX_API_LISTEN` | API listen 地址 |
| `SYSBOX_API_TOKEN` | API Bearer token，空值表示本机开发免鉴权 |
| `SYSBOX_WORKSPACES_DIR` | 覆盖 HCL workspace 目录，默认 `$SYSBOX_HOME/workspaces` |
| `SYSBOX_RUNS_DIR` | 覆盖 state/run metadata 目录，默认 `$SYSBOX_HOME/runs` |
| `SYSBOX_STATE_BACKEND` | 服务态 state backend，compose 默认 Postgres |
| `SYSBOX_FIRECRACKER_BIN` | Firecracker binary 的精确路径 |
| `SYSBOX_FIRECRACKER_WORKDIR` | Firecracker 每 VM 工作目录，默认 `$SYSBOX_HOME/firecracker` |

Kernel/rootfs/qcow2 属于 topology artifact 输入，推荐在 HCL 中用
`sysbox_kernel` / `sysbox_image` 的 `source`、`rootfs`、`qcow2` 和
`sha256` 声明。`SYSBOX_ROOTFS` 只作为示例 HCL 的本地 CLI 便利变量，
不建议作为 API 服务配置。

## 目录结构

```
sysbox/
├── bin/                        # 编译产物（gitignore）
├── cmd/
│   ├── sysbox/                 # 主 CLI（plan/apply/destroy/state/serve）
│   └── sysbox-init/            # firecracker guest PID-1 wrapper（cross-compiled，go:embed 进主二进制）
├── examples/
│   ├── three-nodes/            # Docker 三节点攻防实验室
│   └── microvm/                # Firecracker microVM 拓扑（sysbox_kernel + vsock provisioner）
├── pkg/
│   ├── artifact/               # URL/本地文件解析 + sha256 校验 + 内容寻址缓存
│   ├── config/                 # HCL 解析 + schema
│   ├── graph/                  # 资源依赖图
│   ├── provider/               # Docker / firecracker / exec / network 底层实现
│   │   └── firecracker/        # microVM substrate + sysbox-init initbin embed + config drive
│   ├── runtime/                # graph、plan、apply/destroy executor、checkpoint
│   ├── state/                  # local/Postgres/HTTP/S3/SQLite state backend
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
| `sysbox_actor` | 可选：容器内 host 一个 ACP-compatible agent（如 opencode）|

详细的 firecracker / microVM 用法见 [`examples/microvm/README.md`](examples/microvm/README.md)。

## Make targets

```
make build                  编译 bin/sysbox（自动 cross-compile sysbox-init 并 embed）
make test                   单元测试（无需 Docker）
make test-e2e               Go 拓扑集成测试：apply/route/drift/destroy（需要 Docker + root）
make lint                   fmt + vet
make up TOPO=two-networks   CLI apply 示例拓扑
make down TOPO=two-networks CLI destroy 示例拓扑
make docker-up              Docker Compose 启动 API + Postgres
make docker-down            停止 API + Postgres
make clean                  删除编译产物
```

## CLI 输出

`output` 只负责 HCL 中声明的 topology outputs，语义对齐 Terraform：

```bash
bin/sysbox -f examples/three-nodes/field.sysbox.hcl output
bin/sysbox -f examples/three-nodes/field.sysbox.hcl output attacker_lab_ip
bin/sysbox -f examples/three-nodes/field.sysbox.hcl output --json
```

查看 state/resource 属性用 `state` 子命令：

```bash
bin/sysbox --state runs/two-networks/state.json state list
bin/sysbox --state runs/two-networks/state.json state show sysbox_node.node_a
bin/sysbox --state runs/two-networks/state.json state get sysbox_node.node_a.primary_ip
```

## API 工作流

```bash
make docker-up
curl http://127.0.0.1:9876/v1/topologies
curl http://127.0.0.1:9876/v1/topologies/two-networks/plan
curl -X POST http://127.0.0.1:9876/v1/topologies/two-networks/apply
curl http://127.0.0.1:9876/v1/runs/<run_id>
curl http://127.0.0.1:9876/v1/runs/<run_id>/checkpoint
curl -X POST http://127.0.0.1:9876/v1/topologies/two-networks/destroy
```

`DELETE /v1/topologies/{name}` 只删除 workspace/state metadata。若 state
里仍有资源，默认返回 `409`，需要先调用 `POST /destroy` 回收资源；`force=true`
仅用于明确要删除 metadata 而保留外部资源的场景。
