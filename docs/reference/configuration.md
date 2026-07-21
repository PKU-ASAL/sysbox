# Configuration Reference

Sysbox 配置使用 HCL。文件由 substrate、resource、provider-specific block、引用和变量组成。

## Substrates

```hcl
substrate "docker"      { alias = "local" }
substrate "firecracker" { alias = "local" }
substrate "libvirt"     { alias = "local" }
```

资源通过 `substrate.<kind>.<alias>` 选择执行实现。

## Artifact Resources

`sysbox_image` 支持 OCI、Firecracker rootfs 和 libvirt qcow2；`sysbox_kernel` 声明 Firecracker kernel。公共字段包括 `substrate`、`kind`、`source`、`architecture` 和 `guest_family`。

## Network Resources

`sysbox_network` 声明 `cidr` 和可选 `nat`。Node/router 的 `link`/`interface` block 声明 network reference、IP prefix、MAC、gateway 和 DNS alias。

## Nodes

`sysbox_node` 的公共字段包括 substrate、image、environment、sysctl、port 和 provisioner。Provider 专属字段必须放入对应 block：

```hcl
provider "docker" {
  privileged = false
  binds       = ["./fixture:/data:ro"]
  entrypoint  = ["/usr/local/bin/server"]
  command     = ["--listen", ":8080"]
}
```

Docker 相对 bind source 按 CLI 当前工作目录解析。`entrypoint` 和 `command` 区分 omitted 与显式空数组。

Firecracker 配置 kernel、rootfs/init 与 machine 参数；libvirt 配置 qcow2、cloud-init/SSH 和 domain 参数。不要把 provider 专属字段提升为 node 公共语义。

## References And Variables

资源引用使用 typed address，例如 `sysbox_image.web.id`。环境变量通过配置支持的 `env()` 表达式读取。Secret 使用 secret reference，不能以普通变量嵌入持久 state。

## Lifecycle

显式依赖只用于无法由引用推断的顺序。资源地址变化默认触发 replace。Artifact digest、provider config、immutable guest identity 或网络 attachment 变化可能触发 replacement。

完整可运行配置见仓库 `examples/`。
