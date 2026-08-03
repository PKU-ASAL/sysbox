# Heterogeneous Nodes

选择 provider 的依据是实验隔离和 guest 需求，而不是统一语法本身。

| Provider | Use when | Main trade-off |
|---|---|---|
| Docker | 高密度、快速启动、已有 OCI image | 与宿主机共享 kernel |
| Firecracker | 需要 microVM 隔离、确定 kernel/rootfs | 需要 KVM、kernel、rootfs 和 guest init |
| libvirt | 需要完整 VM、qcow2/cloud image 生态 | 启动和存储成本更高 |

## Shared Contract

三类节点共享 image reference、logical link、IP/MAC、route、connection、provisioner、port intent、state、observation 和 reset。网络可以在同一 topology 中连接容器、microVM 和 VM。

## Docker

OCI image 是 artifact。Provider 支持 privileged、PID/cgroup namespace、bind、ENTRYPOINT 和 CMD override。Host port exposure 需要节点连接 `nat=true` managed network。

## Firecracker

Firecracker currently runs as a direct child process because jailer integration is not yet available. Each Firecracker provider block must explicitly set `allow_direct = true`; use this mode only for trusted local experiments. Preflight reports this limitation as a security warning. Sysbox validates PID start time, VM identity, socket path, and the persisted process ownership anchor before cleaning up a stale process.

需要 uncompressed `vmlinux` 和 ext4 rootfs。Provider 配置 chain init、SSH/vsock 和 machine 参数。Guest network configuration 通过声明的 network-init capability 完成，不由 runtime 猜测发行版配置文件。

## libvirt

使用 immutable qcow2 baseline 和 generation overlay。Provider 配置 machine、disk、SSH 与 network init。Domain UUID 和 owned overlay path 是 reset/destroy 的关键 identity。

### Windows 10（实验性 domain-start lifecycle）

Sysbox 可以用原生 libvirt 启动预先安装好的 Windows 10 qcow2，并沿用现有的 image、overlay、domain ownership、observe、reset 和 destroy 生命周期；镜像构建工具使用微软安装介质、Fedora VirtIO 驱动和 `Autounattend.xml`，不依赖 Packer。构建机需要 `virsh`、`qemu-img`、`genisoimage`、`setfacl` 和 `libguestfs-tools`（提供 `virt-ls`）。运行 `make prepare-windows10-media` 前需要显式提供微软官方 HTTPS 下载地址与 SHA-256，凭据、ISO 和 qcow2 默认只保存在仓库外的用户缓存中。安装介质会复制到 `/tmp` 的临时构建目录，并只向 system libvirt 的 QEMU 用户开放必要的读取权限。构建完成后，脚本会离线检查 Windows 系统目录与完成标记，清除 unattended setup 文件、相关日志和自动登录注册表值，再发布 baseline；验证失败的磁盘默认销毁，仅在显式设置 `WINDOWS_KEEP_FAILED_IMAGE=1` 时才会经过相同清理并以 `0600` 权限保留。Ubuntu 将内核镜像设为 `0600` 时，需通过 `sudo setfacl -m u:$USER:r /boot/vmlinuz-$(uname -r)` 允许 libguestfs 读取当前运行内核；脚本会自动选择该内核及对应 modules。

这里的 `domain-start lifecycle` 是有意保留的边界：当前 libvirt provider 的 guest execution 仍是 SSH，guest network init 仍面向 Linux cloud-init。Windows 拓扑因此必须使用 `guest_family = "windows"` 和 `network_init = "preconfigured"`，且暂不声明 link、route 或 provisioner。`make test-windows10-libvirt` 只验证 libvirt domain 启动、重复 plan、reset、destroy 和 owned residue 清理，并不验证 Windows guest readiness；在 WinRM 与 Windows 网络初始化接入现有 capability contract 之前，不能把它描述为完整的 Windows 节点支持。

## Mixed Topologies

在混合拓扑中显式声明 architecture 和 guest family。所有 provider 必须实现该拓扑需要的 NIC、guest execution、state 或 reset capability，否则 planning 阶段失败。

运行 `examples/heterogeneous-matrix` 验证六向 IPv4 通信、重复 plan、targeted reset 和 residue cleanup。Artifact 准备见 [Artifacts](../operations/artifacts.md)。
