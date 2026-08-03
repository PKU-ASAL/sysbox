# Sysbox

[English](README.en.md) | 简体中文

用一份拓扑描述，构建、验证和反复重置由容器、microVM、虚拟机与 Linux 网络组成的实验环境。

## 为什么需要 Sysbox

真实的系统与安全实验很少只有一台机器。攻击端可能适合放在容器里，目标服务需要独立内核，数据库又依赖完整虚拟机；它们之间还需要固定地址、路由、NAT 和访问策略。

分别调用 Docker、Firecracker、libvirt 和网络脚本可以把环境启动起来，但脚本通常无法回答几个关键问题：当前环境与预期是否一致？一次变更会影响什么？执行中断后哪些资源已经创建？重置实验会不会改变网络身份？清理时如何证明资源确实属于这次实验？

Sysbox 把这些资源视为一个有依赖、有状态的整体。你声明希望得到的拓扑，Sysbox 负责规划变更、按依赖执行、观察实际状态，并在中断后恢复或安全清理。

## 它如何工作

1. **声明拓扑**：描述节点、镜像、网络、路由、策略及其依赖，而不是编排一串命令。
2. **预览变更**：`validate` 检查配置与依赖图；`plan` 比较声明和 state，并可在执行前刷新外部状态。
3. **执行生命周期**：`apply` 按依赖创建资源，`reset` 从不可变基线恢复 guest，`destroy` 按逆序清理。
4. **观察与恢复**：按需 refresh 和控制面 observation 用于发现 drift；checkpoint 记录关键步骤，支持中断恢复。

由此，同一拓扑可以组合 Docker 容器、Firecracker microVM、libvirt VM、隔离网络、路由、NAT 与 nftables 策略。它适合安全研究、系统实验、网络验证，以及需要环境可解释、可重复、可恢复的平台工程。

## Quick Start

要求 Linux、Go 1.26 和当前用户可访问的 Docker Engine。

```bash
git clone https://github.com/PKU-ASAL/sysbox.git
cd sysbox
go build -o bin/sysbox ./cmd/sysbox

bin/sysbox -f examples/docker-service/field.sysbox.hcl validate
bin/sysbox -f examples/docker-service/field.sysbox.hcl plan
bin/sysbox -f examples/docker-service/field.sysbox.hcl apply --auto-approve
bin/sysbox -f examples/docker-service/field.sysbox.hcl destroy --auto-approve
```

完整的首次运行说明见 [Quickstart](docs/quickstart.md)。

## 最小拓扑

```hcl
substrate "docker" { alias = "local" }

resource "sysbox_network" "lab" {
  cidr = "10.44.0.0/24"
  nat  = true
}

resource "sysbox_image" "alpine" {
  substrate    = substrate.docker.local
  kind         = "oci"
  source       = "alpine:3.22"
  architecture = "amd64"
  guest_family = "linux"
}

resource "sysbox_node" "node" {
  substrate = substrate.docker.local
  image     = sysbox_image.alpine.id

  link "lab" {
    network = sysbox_network.lab.id
    ip      = "10.44.0.10/24"
  }
}
```

相同的资源地址、依赖图、计划和状态模型也适用于 Firecracker 与 libvirt。完整的混合示例见 [`examples/mixed`](examples/mixed/)。

## 架构与扩展边界

Sysbox core 定义资源的共同语义：拓扑图、计划、状态、观察、恢复和所有权。具体外部操作由 provider 实现，并以 node lifecycle、NIC、artifact、guest execution、reset、policy 等能力接入。新增运行环境不需要在 core 中加入针对具体虚拟化技术的分支，但必须满足其使用场景所需的完整生命周期契约。

在 API/Agent 模式下，宿主机 Agent 注册其配置的 capability，执行前由 preflight 验证实际环境；调度器据此把**整个 topology run** 分配给一台满足全部要求的 Agent。因此当前支持“一个拓扑内组合异构节点”，但不把同一拓扑中的不同节点分别调度到多台 Agent，也不提供通用的跨主机 overlay network。

本地 CLI 与 API/Agent/Web 控制面共享同一套 decoder、planner、executor、state manager 和 provider，不存在第二套拓扑语义。详细契约见 [Architecture](docs/architecture.md)。

## 支持范围

| 领域 | 当前支持 |
|---|---|
| Guest provider | Docker、Firecracker、libvirt |
| 网络 | 隔离 IPv4、静态地址、路由、NAT、Docker alias |
| 策略 | 拓扑独占、原子更新的 nftables IPv4 策略 |
| 生命周期 | Validate、plan、apply、定向/完整 reset、destroy、recovery |
| State | Local、SQLite、Postgres；HTTP/S3 mutation 需要显式 unsafe override |
| 操作面 | CLI、HTTP API、宿主机 Agent、Web console |
| 分发 | Linux amd64/arm64 CLI archive 与 GHCR API/Agent runtime |

Sysbox 不是通用云编排器，也不兼容任意 Terraform provider。IPv6 policy、任意 guest OS、跨 Agent 节点放置和通用云资源不在当前保证范围内。受控的资源与 provider 范围，是它能够验证身份、观察状态、恢复执行和安全删除的前提。

## 文档

- [Documentation Index](docs/index.md)：按目标选择阅读路径。
- [Design Principles](docs/design-principles.zh-CN.md)：Sysbox 的核心取舍。
- [Architecture](docs/architecture.md)：资源、状态、provider 与恢复契约。
- [Authoring Topologies](docs/guides/authoring-topologies.md)：编写真实拓扑。
- [HCL Reference](docs/reference/hcl.md)：配置字段与约束。
- [Development](docs/development/contributing.md)：参与开发。

## 验证与贡献

```bash
go test ./...
make test-privileged-container
make test-heterogeneous-matrix
make test-heterogeneous-reset
```

后三个命令需要受控 Linux 宿主机、Docker、KVM、Firecracker 与 libvirt。详细门禁见 [Testing](docs/development/testing.md)。

贡献前请阅读 [Contributing](docs/development/contributing.md)，新增资源语义与 provider 能力需要同时满足 observation、recovery 和 safe deletion 契约。

## 许可证

[MulanPSL-2.0](LICENSE)
