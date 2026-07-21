# Authoring Topologies

本指南从实验意图组织 HCL。完整字段表见 [HCL Reference](../reference/hcl.md)。

## Start From Resources

先列出需要长期识别和独立生命周期管理的对象：artifact、network、node、router、firewall 和 SSH access。不要从一组 `docker run` 或 shell command 反推拓扑。

```hcl
substrate "docker" { alias = "local" }

resource "sysbox_image" "web" {
  substrate    = substrate.docker.local
  kind         = "oci"
  source       = "nginx:1.29-alpine"
  architecture = "amd64"
  guest_family = "linux"
}

resource "sysbox_network" "web" {
  cidr = "10.30.0.0/24"
  nat  = true
}

resource "sysbox_node" "web" {
  substrate = substrate.docker.local
  image     = sysbox_image.web.id

  link "web" {
    network = sysbox_network.web.id
    ip      = "10.30.0.10/24"
    aliases = ["web"]
  }
}
```

引用自动形成依赖。只在行为依赖无法通过引用表达时使用 `depends_on`。

## Keep Provider Details Local

公共 node block 描述 image、environment、link、port、route、connection 和 provisioner。底层专属参数放在 provider block：

```hcl
provider "docker" {
  binds      = ["./fixtures:/srv/fixtures:ro"]
  entrypoint = ["/usr/local/bin/server"]
  command    = ["--listen", ":8080"]
}
```

相对 bind source 按运行 Sysbox 时的当前目录解析。Entry point 和 command 是直接 argv，不插入 shell。

## Model Artifacts Explicitly

不要在 node block 中隐藏下载或使用未经固定的 VM 文件。Kernel、rootfs 和 qcow2 应独立声明并在生产/研究复现中设置 SHA-256。

## Use Stable Logical Names

Resource label 会进入 canonical address 和 state。选择表达角色而不是临时实现的名称，例如 `sysbox_node.database`，而不是 `sysbox_node.ubuntu_1`。重命名会产生 delete/create，除非显式 state move。

## Provision Only Guest State

Provisioner 用于 guest 内部初始化，不用于创建宿主机 network、firewall、container 或 VM。宿主机对象必须由 resource/provider 管理，才能 observation、reset 和安全 destroy。

Provisioner 应幂等、有超时并产生明确错误。后台服务优先由 image ENTRYPOINT/CMD 或 guest init 管理。

## Validate In Layers

```bash
sysbox -f range.hcl validate
sysbox -f range.hcl plan
sysbox -f range.hcl apply --auto-approve
sysbox -f range.hcl plan
```

首次 apply 后 plan 应 no-op。随后验证业务功能、执行 reset，再验证功能和最终 destroy residue。

## Make A Range Reproducible

一个可重复研究范围应拥有：

- topology HCL；
- immutable image/artifact identity；
- fixture 和 build recipe；
- initialization；
- service、effect 和 cleanup assertion；
- 精确 Sysbox release lock；
- 环境和许可证说明。
