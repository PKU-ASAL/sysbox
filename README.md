# Sysbox

[English](README.en.md) | 简体中文

Sysbox 是面向裸机 Linux 宿主机的 Terraform-like 实验拓扑编排工具：用一份声明构建、验证和重置由容器、microVM、虚拟机与 Linux 网络组成的实验环境。

## 为什么需要 Sysbox

真实的网络安全实验很少只有一个节点，也很少只用一种虚拟化技术。攻击端可能适合放在容器里，目标服务需要独立内核，数据库又依赖完整虚拟机；它们之间还需要固定地址、路由、NAT 和访问策略。

把进程隔离、硬件虚拟化、虚拟设备和主机网络逐项拼接起来，确实可以让实验环境运行起来。但“能够启动”并不等于“能够管理”：当前环境与预期是否一致？一次变更会影响什么？执行中断后哪些资源已经创建？重置实验会不会改变网络身份？清理时如何证明资源确实属于这次实验？

Sysbox 从实验本身出发，把这些资源视为一个有依赖、有状态的整体。你只需要声明希望得到的拓扑，后续的变更规划、依赖执行、状态观察和中断恢复由同一套生命周期管理。

## 它如何工作

1. **声明拓扑**：描述节点、镜像、网络、路由、策略及其依赖，而不是编排一串命令。
2. **预览变更**：`validate` 检查配置与依赖图；`plan` 比较声明和 state，并可在执行前刷新外部状态。
3. **执行生命周期**：`apply` 按依赖创建资源，`reset` 从不可变基线恢复 guest，`destroy` 按逆序清理。
4. **观察与恢复**：按需 refresh 和控制面 observation 用于发现 drift；checkpoint 记录关键步骤，支持中断恢复。

这套流程让环境不再是一组“启动成功就算完成”的脚本，而是一个可以解释、重复和恢复的实验对象。同一拓扑可以组合 Docker 容器、Firecracker microVM、libvirt VM、隔离网络、路由、NAT 与 nftables 策略。

## 容器与虚拟机体系

Sysbox 当前可以在同一实验拓扑中组合 Docker 容器、轻量 KVM microVM，以及由 libvirt/QEMU 管理的完整 KVM 虚拟机。KVM 是这些虚拟机共享的底层能力，但不是可以直接互换的 provider 接口；不同 VMM 仍然需要各自的生命周期、网络、状态和恢复实现。

| 隔离形态 | Sysbox 接入对象 | 底层机制 | 状态 | 含义 |
|---|---|---|---|---|
| 应用容器 | Docker Engine | Linux namespace、cgroup；内部可使用 containerd | 支持 | 已有 provider，覆盖当前完整生命周期 |
| 应用容器 | containerd | OCI runtime、Linux namespace、cgroup | 架构可扩展 | 可新增 runtime provider；当前没有直接接入 |
| Kubernetes workload | CRI runtime + CNI | 容器或 VM sandbox | 架构可扩展 | 横跨 pod sandbox、镜像与网络，需要独立集成设计 |
| 应用容器 / pod | Podman / libpod | OCI runtime、Linux namespace、cgroup | 架构可扩展 | 可新增 provider；当前没有直接接入 |
| 系统容器 | LXC / LXD | Linux namespace、cgroup | 架构可扩展 | 需要适配不同的生命周期与网络模型 |
| microVM | Firecracker | KVM | 支持 | 已有 provider，直接管理 VMM 进程与资源 |
| microVM | Cloud Hypervisor | KVM | 架构可扩展 | 底层模型兼容；当前缺少 provider |
| 沙箱容器 | Kata Containers | KVM + 多种 VMM | 架构可扩展 | 横跨容器控制面与 VM runtime，需要独立集成设计 |
| 完整 VM | libvirt → QEMU | KVM | 支持 | 已有 provider，通过 libvirt 管理 QEMU/KVM |
| 完整 VM | QEMU/KVM direct | KVM | 架构可扩展 | 当前通过 libvirt 间接使用 QEMU，没有 direct provider |
| 完整 VM | Xen | Xen hypervisor | 当前范围外 | 需要扩展现有 Linux/KVM 执行假设 |
| 完整 VM | VMware vSphere / ESXi | ESXi | 当前范围外 | 需要不同的控制面、身份与网络集成 |
| 完整 VM | Hyper-V | Windows hypervisor | 当前范围外 | 不属于当前 Linux host provider 范围 |

这里的“架构可扩展”表示现有 capability driver 边界允许新增实现，并不代表已经兼容。只有 provider 补齐节点生命周期、网络、状态、观察、恢复和安全删除契约后，才进入 Sysbox 的正式支持范围。

## Quick Start

先从只有 Docker 依赖的最小环境开始。你需要 Linux、Go 1.26，以及当前用户可访问的 Docker Engine。

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

上面的命令运行的是一份普通 HCL。最小模型只需要描述运行环境、网络、镜像和节点之间的关系：

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

当实验需要更强的隔离或完整虚拟机时，不必换一套编排方式。相同的资源地址、依赖图、计划和状态模型也适用于 Firecracker 与 libvirt；完整的混合示例见 [`examples/mixed`](examples/mixed/)。

## 架构与扩展边界

这种一致性来自一条简单的边界：Sysbox core 关心资源“意味着什么”，provider 负责资源“如何实现”。Core 管理拓扑图、计划、状态、观察、恢复和所有权；provider 则以 node lifecycle、NIC、artifact、guest execution、reset、policy 等能力接入。新增运行环境不需要在 core 中加入针对具体虚拟化技术的分支，但需要补齐使用场景所需的生命周期契约。

到了 API/Agent 模式，这条边界仍然不变。宿主机 Agent 注册其配置的 capability，执行前由 preflight 验证实际环境；调度器据此把**整个 topology run** 分配给一台满足全部要求的 Agent。因此当前支持“一个拓扑内组合异构节点”，但不把同一拓扑中的不同节点分别调度到多台 Agent，也不提供通用的跨主机 overlay network。

本地 CLI 与 API/Agent/Web 控制面共享同一套 decoder、planner、executor、state manager 和 provider，不存在第二套拓扑语义。详细契约见 [Architecture](docs/architecture.md)。

## 支持范围

Sysbox 有意把能力范围收敛在可验证的 Linux 实验环境内。当前已经打通的边界如下：

| 领域 | 当前支持 |
|---|---|
| Guest provider | Docker、Firecracker、libvirt |
| 网络 | 隔离 IPv4、静态地址、路由、NAT、Docker alias |
| 策略 | 拓扑独占、原子更新的 nftables IPv4 策略 |
| 生命周期 | Validate、plan、apply、定向/完整 reset、destroy、recovery |
| State | Local、SQLite、Postgres；HTTP/S3 mutation 需要显式 unsafe override |
| 操作面 | CLI、HTTP API、宿主机 Agent、Web console |
| 分发 | Linux amd64/arm64 CLI archive 与 GHCR API/Agent runtime |

这里的 Terraform-like 指声明式计划与生命周期体验，并不表示兼容 Terraform provider。Sysbox 也不是通用云编排器；IPv6 policy、任意 guest OS、跨 Agent 节点放置和通用云资源不在当前保证范围内。正是这段受控边界，让它能够验证身份、观察状态、恢复执行并安全删除资源。

## 文档

README 只提供项目入口。根据你接下来要完成的任务，可以从这些文档继续：

- [Documentation Index](docs/index.md)：按目标选择阅读路径。
- [Design Principles](docs/design-principles.zh-CN.md)：Sysbox 的核心取舍。
- [Architecture](docs/architecture.md)：资源、状态、provider 与恢复契约。
- [Authoring Topologies](docs/guides/authoring-topologies.md)：编写真实拓扑。
- [HCL Reference](docs/reference/hcl.md)：配置字段与约束。
- [Development](docs/development/contributing.md)：参与开发。

## 验证与贡献

普通单元测试可以直接运行；涉及异构运行环境的验证需要对应的宿主机能力：

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
