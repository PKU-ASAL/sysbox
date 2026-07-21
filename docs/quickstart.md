# Quickstart

本指南在本机 Docker Engine 上运行一个最小拓扑。Firecracker 和 libvirt 的宿主机要求见 [Heterogeneous Nodes](guides/heterogeneous-nodes.md) 和 [Artifacts](operations/artifacts.md)。

## Requirements

- Linux
- Go 1.26 或已发布的 Sysbox Linux binary
- Docker Engine，当前用户可以访问 Docker socket

## Build

```bash
git clone https://github.com/PKU-ASAL/sysbox.git
cd sysbox
go build -o bin/sysbox ./cmd/sysbox
./bin/sysbox version
```

也可以从 GitHub Release 下载对应架构的归档，并按 `SHA256SUMS` 校验。

## Create A Topology

创建 `lab.hcl`：

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

## Validate And Apply

```bash
./bin/sysbox -f lab.hcl validate
./bin/sysbox -f lab.hcl plan
./bin/sysbox -f lab.hcl apply --auto-approve
./bin/sysbox -f lab.hcl plan
```

第二次 plan 应为 no-op。完成后销毁全部受管资源：

```bash
./bin/sysbox -f lab.hcl destroy --auto-approve
```

默认 state 与 checkpoint 写入当前工作目录的 `.sysbox/`。不要跨拓扑复用同一 state 文件。

下一步阅读 [Authoring Topologies](guides/authoring-topologies.md)、[HCL Reference](reference/hcl.md) 和 [CLI Reference](reference/cli.md)。
