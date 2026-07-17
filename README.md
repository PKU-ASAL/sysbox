# Sysbox

Sysbox 是一个面向 Linux 实验环境的声明式拓扑控制面。它将 HCL 转换为受状态管理的 Docker 容器、Firecracker microVM、libvirt VM 和 Linux 网络资源，并通过 `validate`、`plan`、`apply`、`reset`、`destroy` 驱动完整生命周期。

Sysbox 适合安全研究、系统实验、网络验证和需要可重复异构环境的平台工程。它不是通用云资源生态，也不尝试兼容任意 Terraform provider；它专注于让单机或受控宿主机上的实验拓扑可解释、可复现、可恢复。

完整技术介绍见 [Sysbox Overview](docs/overview.md)，架构契约与验收证据从 [文档导航](docs/README.md) 进入。

## 可以构建什么

同一份 HCL 可以把 Docker、Firecracker 和 libvirt 节点接入一个声明的隔离 IPv4 网络。镜像类型、架构和 guest family 都是显式契约：

```hcl
substrate "docker"      { alias = "local" }
substrate "firecracker" { alias = "local" }
substrate "libvirt"     { alias = "local" }

resource "sysbox_image" "docker" {
  substrate   = substrate.docker.local
  kind         = "oci"
  source       = "alpine:latest"
  architecture = "amd64"
  guest_family = "linux"
}

resource "sysbox_image" "microvm" {
  substrate   = substrate.firecracker.local
  kind         = "rootfs"
  source       = env("SYSBOX_ROOTFS")
  architecture = "amd64"
  guest_family = "linux"
}

resource "sysbox_image" "vm" {
  substrate   = substrate.libvirt.local
  kind         = "qcow2"
  source       = env("SYSBOX_QCOW2")
  architecture = "amd64"
  guest_family = "linux"
}

resource "sysbox_network" "lab" {
  cidr = "10.44.0.0/24"
}

resource "sysbox_node" "docker" {
  substrate = substrate.docker.local
  image     = sysbox_image.docker.id
  link "lab" {
    network = sysbox_network.lab.id
    ip      = "10.44.0.10/24"
  }
}

# Firecracker and libvirt nodes use the same image + link model and add
# provider-owned kernel or guest network initialization settings.
```

完整的三方拓扑见 [heterogeneous-matrix](examples/heterogeneous-matrix/field.sysbox.hcl)。真实验收已证明三类节点之间的六个有向通信路径、重复 plan 无变化、整拓扑与单节点 reset，以及最终零归属残留。

## 核心能力

| 能力 | 当前支持 |
|---|---|
| 声明式拓扑 | HCL schema、引用求值、依赖图、确定性执行顺序、outputs |
| 异构节点 | Docker、Firecracker、libvirt，共享统一节点和网络契约 |
| 网络 | Linux bridge/netns、veth/TAP、固定 IPv4/MAC、路由器、nftables 防火墙、端口声明 |
| Artifact | 显式 `kind`、`source`、`architecture`、`guest_family` 和不可变 digest 身份 |
| Guest 初始化 | provider-owned `cloud_init` / `preconfigured` 能力，runtime 只检查公共契约 |
| Guest 执行 | 结构化 program/args/shell 请求、provisioner 和镜像入口执行 |
| 生命周期 | `validate`、`plan`、`apply`、整拓扑或 targeted `reset`、`destroy` |
| 状态与恢复 | state schema v6、旧 state 严格拒绝、stored plan 指纹、checkpoint、CAS/锁和快照 |
| 操作方式 | 本地 CLI；可选 API、Agent 和 Web 控制面 |

## 快速开始

Docker-only 示例不要求 KVM 或 libvirt：

```bash
make build
make cli plan TOPO=two-networks
sudo -E make cli apply TOPO=two-networks
sudo -E make cli destroy TOPO=two-networks
```

具备 Docker、KVM、Firecracker 和 libvirt 的宿主机可以运行真实异构验收：

```bash
make test-heterogeneous-matrix
make test-heterogeneous-reset
```

第一条命令验证 Docker、Firecracker、libvirt 的六向 IPv4 通信和清理；第二条执行 3 次整拓扑 reset、3 次 targeted reset，并审计被替换资源和最终残留。环境与 artifact 准备要求见 [heterogeneous-matrix README](examples/heterogeneous-matrix/README.md)。

## 安装与版本

正式版本构建并验证 Linux amd64/arm64 tarball、`SHA256SUMS` 与构建元数据，
并发布 runtime、最小 CLI、最终构建元数据三个多架构 OCI 镜像。宿主机工具
通过元数据中记录的不可变 digest 从 CLI 镜像提取，而不是在特权容器中执行拓扑：

```bash
docker pull ghcr.io/pku-asal/sysbox-cli@sha256:<manifest-digest>
# sysbox-topology 的 make bootstrap 会提取、校验并在宿主机激活 CLI。
```

API 和 Agent 可以固定到同一 OCI 版本：

```bash
SYSBOX_IMAGE=ghcr.io/pku-asal/sysbox:v0.1.0 docker compose \
  -f deploy/docker/compose.yml \
  -f deploy/docker/compose.agent.yml up -d
```

维护者发布流程、GitHub Actions 权限和本地异构验收要求见 [Releasing Sysbox](docs/releasing.md)。

## 生命周期

```bash
bin/sysbox -f examples/two-networks/field.sysbox.hcl validate
bin/sysbox -f examples/two-networks/field.sysbox.hcl plan
sudo -E bin/sysbox -f examples/two-networks/field.sysbox.hcl apply --auto-approve

# 从不可变 baseline 重建所有节点，或精确重建一个节点。
sudo -E bin/sysbox -f examples/two-networks/field.sysbox.hcl reset --auto-approve
sudo -E bin/sysbox -f examples/two-networks/field.sysbox.hcl reset \
  --target sysbox_node.node_a --auto-approve

bin/sysbox -f examples/two-networks/field.sysbox.hcl output --json
bin/sysbox --state .sysbox/runs/two-networks/state.json state list
sudo -E bin/sysbox -f examples/two-networks/field.sysbox.hcl destroy --auto-approve
```

`reset` 保留声明的拓扑、网络身份和不可变 artifact 身份，但替换节点的可变 guest 状态和 provider external ID。旧版 state 不会自动迁移：必须先用创建该 state 的旧版 Sysbox 执行 destroy，再用当前版本 recreate。若直接删除旧 state，当前版本可以重建拓扑，但用户必须自行清理旧 state 对应的外部资源。

## 当前边界

- 正式拓扑网络能力以 IPv4 为主；公共地址族接口为 IPv6 扩展预留，但当前不宣称生产可用的 IPv6 拓扑。
- 已验收范围是 Docker、Firecracker、libvirt 和 Linux guest/artifact 组合，不代表任意操作系统镜像都可直接启动。
- apply/reset/destroy 涉及 Docker socket、netns、veth、TAP、KVM 或 libvirt，要求 root 或等效宿主机能力。
- Sysbox 管理实验拓扑生命周期；sensor、labeler、reward、attribution、IOC 评分等研究语义属于其上的应用层。

## 工作原理

```text
HCL 拓扑声明
  -> validate / plan / apply / reset / destroy
  -> runtime 依赖图 + provider 生命周期
  -> 本地/Postgres/SQLite 状态 + checkpointed 运行记录
  -> 可选 API / Agent / Web 控制面
```

## 环境要求

- 支持网络命名空间的 Linux。
- Docker 守护进程（Docker substrate 示例需要）。
- Go 1.22+。
- 真实 apply/destroy 路径涉及 netns、veth、tap、KVM 或 Docker socket，需要 root 或等效权限。
- Firecracker 示例额外需要 `firecracker`、`/dev/kvm`、`mkfs.ext4` 和 `losetup`。
- libvirt 示例额外需要 libvirt/qemu 工具链及 qcow2 镜像。

大型构件不打包进 sysbox 镜像。内核、rootfs 镜像和 qcow2 镜像应在 HCL 中声明为 `sysbox_kernel` / `sysbox_image` 资源，显式挂载或通过 artifact cache 拉取。Firecracker rootfs 准备见 `scripts/prepare-fc-rootfs.sh` 和 [docs/firecracker-artifacts.md](docs/firecracker-artifacts.md)。

## 示例拓扑

仓库内的常用拓扑：

| TOPO | 用途 |
|---|---|
| `two-networks` | 两隔离网络 + 一路由器的 Docker 节点 |
| `three-nodes` | Docker attacker/web/db 实验拓扑，可选 actor 资源 |
| `microvm` | Firecracker 专有拓扑 |
| `mixed` | Docker + Firecracker 混合拓扑 |
| `mixed-capture` | 复用 `mixed` 的 opencode + Tetragon 研究采集流程 |
| `libvirt-vm` | Docker + libvirt VM 混合拓扑 |
| `heterogeneous-matrix` | Docker + Firecracker + libvirt 的完整 IPv4 通信与 reset 验收拓扑 |

## Make 命令

Makefile 刻意保持精简。主要命令：

```bash
make build                         # 构建 bin/sysbox
make test                          # 单元测试
make test-e2e                      # 黑盒 API smoke 测试；需先 make api deploy-full
make lint                          # go vet

make cli plan TOPO=two-networks    # plan 示例
sudo -E make cli apply TOPO=two-networks
sudo -E make cli destroy TOPO=two-networks

cp .env.example .env               # 本地 12-factor 配置文件
make api config                    # 查看解析后的 compose 配置
make api build-api                 # 仅重新构建 API/agent 镜像
make api deploy                    # API + Postgres
make api deploy-full               # API + Postgres + Docker agent
make api seed                      # 将示例 HCL 复制到 API workspace
make api build-ui                  # 构建并启动 Web UI
make api down
make api clean                     # 停止 compose，删除 Postgres 卷，清理 API workspace
make api logs
```

顶层兼容别名仍然保留，但推荐使用分组的 `make cli ...` 和 `make api ...` 命令。

## CLI

常用命令：

```bash
bin/sysbox -f examples/two-networks/field.sysbox.hcl --state .sysbox/runs/two-networks/state.json validate
bin/sysbox -f examples/two-networks/field.sysbox.hcl --state .sysbox/runs/two-networks/state.json plan
sudo -E bin/sysbox -f examples/two-networks/field.sysbox.hcl --state .sysbox/runs/two-networks/state.json apply --auto-approve
sudo -E bin/sysbox -f examples/two-networks/field.sysbox.hcl --state .sysbox/runs/two-networks/state.json reset --target sysbox_node.node_a --auto-approve
sudo -E bin/sysbox -f examples/two-networks/field.sysbox.hcl --state .sysbox/runs/two-networks/state.json destroy --auto-approve
```

`output` 子命令用于 HCL 拓扑输出，语义对齐 Terraform：

```bash
bin/sysbox -f examples/three-nodes/field.sysbox.hcl output
bin/sysbox -f examples/three-nodes/field.sysbox.hcl output attacker_lab_ip
bin/sysbox -f examples/three-nodes/field.sysbox.hcl output --json
```

状态/资源查看通过 `state` 子命令：

```bash
bin/sysbox --state .sysbox/runs/two-networks/state.json state list
bin/sysbox --state .sysbox/runs/two-networks/state.json state show sysbox_node.node_a
bin/sysbox --state .sysbox/runs/two-networks/state.json state get sysbox_node.node_a.primary_ip
```

## API / Docker Compose

API 服务是 sysbox 的服务化控制面。Compose 默认使用 Postgres 存储状态、运行记录、checkpoint/action log 及健康快照，因此 API 状态不必与本地 CLI 状态文件共存。本地运行时数据统一置于 `.sysbox/` 下：`.sysbox/api` 存放 API 数据，`.sysbox/runs` 存放 CLI/示例状态。

完整部署模型见 [docs/deployment.md](docs/deployment.md)。

```bash
cp .env.example .env
make api deploy
curl http://127.0.0.1:9876/v1/health
curl http://127.0.0.1:9876/v1/topologies
```

可选 Web UI 是 shadcn 风格的 React 控制台，监听 3001 端口。它与 API 同源部署，API 调用和 WebSocket console 会话均通过 `/v1`。

```bash
make api deploy-full
make api build-ui
open http://127.0.0.1:3001
# 或从其他机器访问: http://<host-ip>:3001
```

部署遵循 12-factor 风格：部署时选择放在 `.env`，拓扑意图放在 HCL，命令面保持精简。从复制模板开始：

```bash
cp .env.example .env
```

两个部署目标：

```bash
make api deploy       # 仅控制面：API + Postgres
make api deploy-full  # 控制面 + Docker agent
make api seed         # 将示例复制到 API workspace
make api build-ui     # 启动 Web 控制台
make api clean        # 删除 Compose Postgres 卷及 API workspace
```

`deploy` 是纯净控制面模式，不挂载 Docker socket 到 API 容器。`deploy-full` 额外启动 `sysbox-agent`，该容器挂载宿主机 Docker socket，执行 API 分配的 Docker-substrate 运行。

快速 API 驱动 smoke 测试：

```bash
make api deploy-full
make api seed
curl -X POST http://127.0.0.1:9876/v1/topologies/docker-service/apply
curl http://127.0.0.1:9876/v1/runs
```

`make api seed` 仅在 workspace 不存在时将 `examples/*/field.sysbox.hcl` 复制到 `.sysbox/api/workspaces`。部署不再自动 seed 示例，因此全新 API 启动时没有任何 HCL workspace，需要手动创建或 seed。

重要 API endpoint 见 [docs/api.md](docs/api.md)。

产品级 apply 流程：

```bash
curl -X POST http://127.0.0.1:9876/v1/topologies/two-networks/revisions
PLAN_ID=$(curl -s -X POST http://127.0.0.1:9876/v1/topologies/two-networks/plans | jq -r .id)
curl -X POST http://127.0.0.1:9876/v1/topologies/two-networks/apply \
  -H 'Content-Type: application/json' \
  -d "{\"plan_id\":\"${PLAN_ID}\"}"
```

当提供 `plan_id` 时，apply 直接执行已存储的 plan 动作，不再重新 diff。plan 记录了创建时的状态 serial；若期间状态已变更，apply 将拒绝执行。每个 run 绑定其 `revision` 和 `plan_id`，因此 API 重启后 `/v1/runs/{run_id}/events` 仍可追溯。

Run 根据拓扑声明的能力调度到 Agent。API 仅创建和分配命令意图；宿主机 Agent 执行拓扑变更，默认在本地持久化状态和 checkpoint（除非另行配置）。本地 CLI `apply`/`destroy` 和 API 分配的 Agent 运行通过同一执行器执行：CLI 使用本地 Bridge，注册 Agent 使用控制面 Bridge。

```bash
sysbox agent register --api http://127.0.0.1:9876 --id host-a
sysbox agent start
```

`DELETE /v1/topologies/{name}` 仅在拓扑为空时删除 workspace/state 元数据。若状态中仍有资源则返回 `409`；需先调用 `POST /destroy`。`force=true` 仅删除元数据，外部资源保留——这是刻意设计。

## 架构

sysbox 采用分层、单向依赖架构。每层只 import 下层包，包之间无循环依赖。

```
cmd/sysbox ── cmd/sysbox-init
    │
pkg/api          (HTTP + jobs + scheduler + supervisor)
    │
pkg/agentexec    (run 级执行器 + Bridge 接口)
    │
pkg/runtime      (资源级执行引擎：plan / apply / destroy / health)
    │
pkg/controlplane (纯 DTO 层：Run、Plan、Agent、健康投影等)
    │
pkg/state ──► pkg/substrate   (state 有意持有 substrate.NodeHandle)
    │
pkg/provider/{docker,firecracker,libvirt} ──► pkg/transport (SSH、vsock、console)
    │                                           pkg/provider/network
pkg/config / pkg/graph / pkg/util / pkg/vsockrpc / pkg/artifact (叶子包)
```

关键设计决策：

- **`pkg/controlplane`** 持有共享类型（`PlanAction`、`TopologyHealth`、
  `ResourceHealth`、`RecoveryDecision` 等）。它不 import `pkg/runtime`；
  `pkg/runtime` 直接引用 `controlplane` 类型（无别名）。API、Web UI 和 Agent
  的 DTO 永远不依赖执行引擎。
- **`pkg/runtime`** 仅通过 `substrate.Substrate` 接口及可选能力接口
  （`ConnectionWaiter`、`ImageEntryStarter`）调用 provider，不 import 任何具体
  provider 包。唯一的例外是 `pkg/provider/network`——一个纯叶子工具包，仅用于
  链路存在性检查和 netlink 操作，无向上依赖，抽象成本大于收益。
- **`pkg/transport`**（原名 `pkg/provider/exec`）为所有 substrate 实现
  `substrate.Connection`——SSH、vsock、console 会话。重命名为 `transport` 避免
  了与 `os/exec` 的命名冲突。
- **Bridge 模式**：`pkg/agentexec` 定义 `Bridge` 接口；`pkg/api` 实现它
  （`ExecutionBridge`），使 Agent 执行器访问控制面服务时 `agentexec` 无需
  知晓 `api`。没有 import 环，不是临时 shim——这是永久架构。
- **Substrate 注册** 统一为显式：三个 substrate（docker、firecracker、libvirt）
  均在 `cmd/sysbox/main.go` 中显式构造并注册。调度器从 `substrate.Capabilities()`
  直接推导 Agent 能力，不再维护硬编码 switch。
- **Preflight 检查** 共享单一 `substrate.PreflightCheck` 类型；`pkg/runtime` 和
  `pkg/api` 直接使用（三份拷贝合并为一份）。

## 状态后端

| 后端 | 适用场景 | CAS | 锁 | 快照 | 删除 |
|---|---|---|---|---|---|
| **Local**（文件 + flock） | CLI / 单机开发 | serial 文件，原子 rename | flock | 文件快照 | 是 |
| **SQLite**（`sqlite://`） | 本地 API，事务保证 | `UPDATE ... WHERE serial=?` | `BEGIN IMMEDIATE` | 表快照 | 是 |
| **Postgres**（`postgres://`） | 多机生产 | `UPDATE ... WHERE serial=$5` | `pg_try_advisory_lock` | 表快照 | 是 |
| **HTTP**（`https://`） | Terraform HTTP backend 兼容 | 无 | 无（乐观） | 无 | 无 |
| **S3**（`s3://`） | 轻量远端状态（调用 `aws` CLI） | 无 | 无（乐观） | 无 | 无 |

Local 和 SQLite 仅限本地使用。Postgres 是多 Agent 部署的推荐后端。HTTP 和 S3
后端为兼容性提供，但不实现锁、CAS、快照或删除——并发写入可能互相覆盖。需要这些
保证时请使用 Postgres（或单写入方的本地 SQLite）。

API 存储（runs、agents、commands、console sessions、健康快照等）使用同一后端 URL
指定：集群部署用 Postgres，需事务正确性的本地 API 用 SQLite（`sqlite://`），
零依赖快速启动时使用本地 JSONL 文件（不配置后端 URL）。

## 运行时目录布局

生成的本地状态刻意排出版本库表面：

| 路径 | 用途 |
|---|---|
| `.sysbox/api` | API workspace、fallback 状态、run 记录、checkpoint、健康快照 |
| `.sysbox/runs` | CLI/示例/e2e 状态文件及本地事件日志 |
| `~/.cache/sysbox` | 内核、rootfs 镜像、qcow2 文件、下载的工具 |

`.sysbox/`、旧目录 `data/` 和 `runs/` 均被忽略。新命令和文档统一使用 `.sysbox/`，使运行时文件不会散布在仓库根目录。

## 状态与恢复

sysbox 支持本地状态和服务端后端。服务端路径现已包括：

- 从后端获取拓扑元数据/列表
- serial/CAS 写入，防止 last-writer-wins
- 后端 lease/lock 元数据
- API store 中的 run 持久化
- API store 中的 checkpoint/action log 持久化
- API store 中的健康快照持久化
- 基于 checkpoint 的 recover/cleanup（Docker、本地网络、microVM 残留）
- 后端支持的快照功能

Docker Compose 中 Postgres 是默认后端。本地 CLI 仍默认使用本地状态文件，除非指定 `--backend` 或 `SYSBOX_STATE_BACKEND`。当 `SYSBOX_STATE_BACKEND` 为 Postgres 或 SQLite URL 时，API 也会将 runs/checkpoints/health 存入对应数据库。`.env` 中留空 `SYSBOX_STATE_BACKEND` 则让 Compose 自动选择默认的 API/agent Postgres URL。

## 产品对象

API 提供产品级对象，将 sysbox 映射到类 Terraform Cloud / CloudFormation 的控制面概念：

| 对象 | 当前 sysbox 表示 |
|---|---|
| Project | `/v1/projects`，目前为默认 project 命名空间 |
| Workspace / Topology | `.sysbox/api/workspaces` 下的 HCL workspace 加状态后端条目 |
| Revision | SHA256 寻址的 HCL 修订 |
| Plan | 存储的 workspace revision plan 记录 |
| Run | 异步 apply/destroy/recover 操作，绑定 Agent 所有权 |
| Agent | 通过 `/v1/agents` 注册的宿主机执行节点；Compose `deploy-full` 启动可操作 Docker 的 agent |
| Stack State | 当前状态加后端元数据 |
| Event / Action | Checkpoint/action-log 步骤，暴露为 run 事件 |
| Artifact | sysbox artifact cache 中的文件 |
| Lease | 状态锁/租约元数据 |
| Policy | 策略对象占位，用于 pre-apply 门禁 |
| Snapshot | 状态后端快照/恢复点 |

## 服务配置

API 部署从 `sysbox.yaml` 加载服务默认值，环境变量仅作为部署时覆盖。Docker Compose
将 `deploy/docker/sysbox.yaml` 挂载到 `/etc/sysbox/sysbox.yaml`；设置 `SYSBOX_CONFIG`
可指向其他文件。

```yaml
version: 1
api:
  listen: ":9876"
  # allowed_origins: ["http://localhost:3001"]  # 限制 WebSocket 来源
paths:
  home: /var/lib/sysbox
  cache: /var/cache/sysbox
supervisor:
  policy: observe_only
  interval: 30s
providers:
  default_policy:
    preflight: warn
  docker:
    enabled: true
  network:
    enabled: true
  firecracker:
    enabled: true
    binary: /opt/sysbox/bin/firecracker
    workdir: /var/lib/sysbox/firecracker
  libvirt:
    enabled: true
artifacts:
  policy:
    cache_mode: on_demand
    verify: warn
```

Postgres DSN 由 Compose 根据 `.env` 组装并通过 `SYSBOX_STATE_BACKEND` 传入，因此 `sysbox.yaml` 不携带密码。

推荐的环境变量覆盖：

| 变量 | 含义 |
|---|---|
| `SYSBOX_CONFIG` | 服务配置文件路径，默认 `/etc/sysbox/sysbox.yaml` |
| `SYSBOX_API_HOST_ADDR` | API 宿主机地址，默认 `0.0.0.0` |
| `SYSBOX_API_HOST_PORT` | API 宿主机端口，默认 `9876` |
| `SYSBOX_WEB_HOST_ADDR` | Web UI 宿主机地址，默认 `0.0.0.0` |
| `SYSBOX_WEB_HOST_PORT` | Web UI 宿主机端口，默认 `3001` |
| `SYSBOX_API_TOKEN` | 可选 API Bearer token |
| `SYSBOX_HOST_HOME_DIR` | 宿主机目录，挂载到容器 `/var/lib/sysbox`，默认 `.sysbox/api` |
| `SYSBOX_HOST_CACHE_DIR` | 宿主机目录，挂载到容器 `/var/cache/sysbox`，默认 `~/.cache/sysbox` |
| `SYSBOX_HOST_DOCKER_SOCKET` | `deploy-full` 模式下的宿主机 Docker socket 路径，默认 `/var/run/docker.sock` |
| `SYSBOX_POSTGRES_DATABASE` | Compose Postgres 数据库名 |
| `SYSBOX_POSTGRES_USERNAME` | Compose Postgres 用户名 |
| `SYSBOX_POSTGRES_PASSWORD` | Compose Postgres 密码；仅在本地 `.env` 设置，不要提交真实值 |
| `SYSBOX_POSTGRES_HOST_ADDR` | Postgres 宿主机地址，默认 `127.0.0.1` |
| `SYSBOX_POSTGRES_HOST_PORT` | Postgres 宿主机端口，默认 `55432` |
| `SYSBOX_STATE_BACKEND` | 可选外部 state/API 后端 URL；覆盖 Compose 生成的 DSN |

容器路径 `/var/lib/sysbox` 和 `/var/cache/sysbox` 由 sysbox 镜像和服务配置固定。`.env` 仅选择这些路径对应的宿主机目录。

内核、rootfs 和 qcow2 是拓扑 artifact，不是服务配置。优先在 HCL 中使用 `sysbox_kernel` 和 `sysbox_image`，并显式配置 `kind`、`source`、`architecture`、`guest_family` 和可选 `sha256`。`SYSBOX_ROOTFS` 仍作为本地示例便利变量存在，不是 API 部署契约。

## HCL 资源

| 资源 | 说明 |
|---|---|
| `sysbox_image` | Docker 镜像、Firecracker rootfs 或 libvirt qcow2 镜像声明 |
| `sysbox_kernel` | Firecracker 内核构件声明 |
| `sysbox_network` | Linux bridge/netns 网络；`nat=true` 使用 Docker bridge |
| `sysbox_node` | Docker 容器、Firecracker microVM 或 VM 节点 |
| `sysbox_router` | 多接口路由器节点 |
| `sysbox_firewall` | 挂载到网络的 nftables 规则 |
| `sysbox_ssh_access` | SSH 入口及 authorized key 注入 |
| `sysbox_actor` | 可选 ACP 兼容 Agent 容器资源 |

`sysbox_node` 支持原生 `port` 块，用来声明节点内部端口及暴露方式：

```hcl
port {
  name      = "http"
  target    = 80
  published = 18080
  protocol  = "http"
  exposure  = "host"
  host_ip   = "127.0.0.1"
}
```

`target` 是容器/虚拟机内端口；`protocol` 支持 `tcp`、`udp`、`http`、`https`，默认 `tcp`；`exposure` 默认 `direct`，表示通过节点 IP 访问。Docker 额外支持 `host`，通过 Docker port binding 发布到宿主机端口，且节点必须连接至少一个 `nat=true` 的 `sysbox_network`。Firecracker/libvirt 当前支持 `none` 和 `direct`，`host` 会明确报不支持。

## 仓库布局

```
cmd/sysbox/                 CLI 及 API 服务入口
cmd/sysbox-init/            Firecracker guest init/RPC 辅助程序
deploy/docker/              Docker Compose 基础文件及能力叠加
docs/                       文档
examples/                   示例拓扑
pkg/artifact/               artifact 解析器/缓存
pkg/api/                    HTTP 控制面、调度、作业、恢复/清理
pkg/config/                 HCL schema 与求值
pkg/controlplane/           产品级对象，如 Project、Plan、Run、Agent
pkg/graph/                  依赖图
pkg/runtime/                plan/apply/destroy/checkpoint 运行时及执行日志原语
pkg/state/                  Local/Postgres/SQLite/HTTP/S3 状态后端
pkg/substrate/              Provider 抽象
pkg/transport/              连接实现（SSH、vsock、console）
pkg/provider/               Docker、Firecracker、network、libvirt provider
pkg/agent/                  Agent 身份与注册
pkg/agentexec/              Agent 命令循环、local/remote Bridge 及 run 级执行器
runner/                     可选 Python episode runner（Agent 示例用）
scripts/                    artifact 准备及验证辅助脚本
tests/e2e/                  黑盒 API e2e 脚本（curl）
.sysbox/                    忽略的本地运行时数据
```

## License

Sysbox 使用 [木兰宽松许可证第 2 版](LICENSE)，SPDX 标识为 `MulanPSL-2.0`。
