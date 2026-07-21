# Sysbox

Sysbox 是面向 Linux 实验环境的声明式拓扑控制面。它把一份 HCL 转换为受状态管理的 Docker 容器、Firecracker microVM、libvirt VM 和 Linux 网络资源，并用统一的 `validate`、`plan`、`apply`、`reset`、`destroy` 管理完整生命周期。

Sysbox 适合安全研究、系统实验、网络验证，以及要求环境可解释、可重复、可恢复的平台工程。它不是通用云编排器，也不兼容任意 Terraform provider；受控的资源与 provider 范围是其确定性和安全删除能力的基础。

## Why Sysbox

- **异构拓扑**：Docker、Firecracker 和 libvirt 使用统一节点、artifact 与网络模型。
- **先计划再变更**：配置、state、schema、driver 和 artifact 共同绑定执行计划。
- **确定性恢复**：关键生命周期步骤持久化 checkpoint，中断后先观察再恢复或清理。
- **可重复 reset**：从不可变 baseline 替换可变 guest，同时保持声明的网络与 artifact 身份。
- **安全所有权**：只修改或删除能够证明由当前拓扑拥有的宿主机资源。
- **两种操作面**：本地 CLI 与 API/Agent/Web 控制面共享同一 runtime。

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

## Minimal Model

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

相同的资源地址、依赖图、计划和状态模型也适用于 Firecracker 与 libvirt；provider 差异保留在各自配置边界内。

## Supported Scope

| Area | Current support |
|---|---|
| Guest providers | Docker, Firecracker, libvirt |
| Networking | Isolated IPv4, static addresses, routes, NAT, Docker aliases |
| Policy | Topology-owned atomic nftables policy, IPv4 only |
| Lifecycle | Validate, plan, apply, targeted/full reset, destroy, recovery |
| State | Local, SQLite, Postgres; HTTP/S3 require explicit unsafe override for mutation |
| Control plane | CLI, HTTP API, host Agent, Web console |
| Distribution | Linux amd64/arm64 CLI archives and GHCR API/Agent runtime |

IPv6 policy、任意操作系统 guest、任意 Terraform provider 和通用云资源不在当前保证范围内。

## Documentation

- [Documentation Index](docs/index.md)：按目标选择阅读路径。
- [Design Principles](docs/design-principles.zh-CN.md)：Sysbox 的核心取舍。
- [Architecture](docs/architecture.md)：资源、状态、provider 与恢复契约。
- [Authoring Topologies](docs/guides/authoring-topologies.md)：编写真实拓扑。
- [HCL Reference](docs/reference/hcl.md)：配置字段与约束。
- [Development](docs/development/contributing.md)：参与开发。

## Verification

```bash
go test ./...
make test-privileged-container
make test-heterogeneous-matrix
make test-heterogeneous-reset
```

后三个命令需要受控 Linux 宿主机、Docker、KVM、Firecracker 与 libvirt。详细门禁见 [Testing](docs/development/testing.md)。

## License

[MulanPSL-2.0](LICENSE)
