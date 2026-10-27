# 异构节点

选择 provider 的依据是实验隔离和 guest 需求，而不是统一语法本身。

| Provider | Use when | Main trade-off |
|---|---|---|
| Docker | 高密度、快速启动、已有 OCI image | 与宿主机共享 kernel |
| Firecracker | 需要 microVM 隔离、确定 kernel/rootfs | 需要 KVM、kernel、rootfs 和 guest init |
| libvirt | 需要完整 VM、qcow2/cloud image 生态 | 启动和存储成本更高 |

## 共享契约

三类节点共享 image reference、logical link、IP/MAC、route、connection、provisioner、port intent、state、observation 和 reset。网络可以在同一 topology 中连接容器、microVM 和 VM。

## Docker

OCI image 是 artifact。Provider 支持 privileged、PID/cgroup namespace、bind、ENTRYPOINT 和 CMD override。Host port exposure 需要节点连接 `nat=true` managed network。

## Firecracker

Firecracker currently runs as a direct child process because jailer integration is not yet available. Each Firecracker provider block must explicitly set `allow_direct = true`; use this mode only for trusted local experiments. Preflight reports this limitation as a security warning. Sysbox validates PID start time, VM identity, socket path, and the persisted process ownership anchor before cleaning up a stale process. A Firecracker VM is currently limited to one network namespace; plans with multiple links are rejected until the VMM namespace contract is expanded.

需要 uncompressed `vmlinux` 和 ext4 rootfs。Provider 配置 chain init、SSH/vsock 和 machine 参数。Guest network configuration 通过声明的 network-init capability 完成，不由 runtime 猜测发行版配置文件。

Firecracker 节点可在 `provider "firecracker"` 中设置 `sysarmor = true`，按需在 VM 创建和 reset 后先安装固定版本的 SysArmor，再执行赛题 provisioner。需要先在 VM artifact mount 中准备 agent archive、安装脚本和独立配置；guest 必须支持 systemd 和 BTF。省略该字段时不安装，Docker 节点不受影响。

## libvirt

使用 immutable qcow2 baseline 和 generation overlay。Provider 配置 machine、disk、SSH 与 network init。Domain UUID 和 owned overlay path 是 reset/destroy 的关键 identity。

### Windows 10（实验性 domain-start lifecycle）

Sysbox 可以用原生 libvirt 启动预先安装好的 Windows 10 qcow2，并沿用现有的 image、overlay、domain ownership、observe、reset 和 destroy 生命周期；镜像构建工具使用微软安装介质、Fedora VirtIO 驱动和 `Autounattend.xml`，不依赖 Packer。构建机需要 `virsh`、`qemu-img`、`genisoimage`、`setfacl` 和 `libguestfs-tools`（提供 `virt-ls`）。运行 `make prepare-windows10-media` 前需要显式提供微软官方 HTTPS 下载地址与 SHA-256，凭据、ISO 和 qcow2 默认只保存在仓库外的用户缓存中。安装介质会复制到 `/tmp` 的临时构建目录，并只向 system libvirt 的 QEMU 用户开放必要的读取权限。构建完成后，脚本会离线检查 Windows 系统目录与完成标记，清除 unattended setup 文件、相关日志和自动登录注册表值，再发布 baseline；验证失败的磁盘默认销毁，仅在显式设置 `WINDOWS_KEEP_FAILED_IMAGE=1` 时才会经过相同清理并以 `0600` 权限保留。Ubuntu 将内核镜像设为 `0600` 时，需通过 `sudo setfacl -m u:$USER:r /boot/vmlinuz-$(uname -r)` 允许 libguestfs 读取当前运行内核；脚本会自动选择该内核及对应 modules。

这里的 `domain-start lifecycle` 是有意保留的边界：当前 libvirt provider 的 guest execution 仍是 SSH，guest network init 仍面向 Linux cloud-init。Windows 拓扑因此必须使用 `guest_family = "windows"` 和 `network_init = "preconfigured"`，且暂不声明 link、route 或 provisioner。`make test-windows10-libvirt` 只验证 libvirt domain 启动、重复 plan、reset、destroy 和 owned residue 清理，并不验证 Windows guest readiness；在 WinRM 与 Windows 网络初始化接入现有 capability contract 之前，不能把它描述为完整的 Windows 节点支持。

## 机制对照

| 通用能力 | Docker driver | Firecracker driver | libvirt driver |
|---|---|---|---|
| 节点类型 | Linux 容器 | KVM 微虚机 | QEMU/KVM 完整虚拟机 |
| 基础镜像 | OCI image | kernel + rootfs | qcow2 |
| 创建节点 | 调用 Docker 创建容器 | 生成配置并启动 Firecracker 进程 | 创建 libvirt domain |
| 网络设备 | veth 或 Docker endpoint | TAP | domain 虚拟 NIC/TAP |
| 目标网络 | 隔离网络或 Docker NAT | 隔离网络 | 隔离网络的受管 root bridge |
| 网卡接入时机 | 支持运行时接入 | 通常在启动前接入 | 通常写入 domain 配置后启动 |
| Guest 命令执行 | Docker Exec | vsock RPC 或 SSH | SSH |
| 状态观察 | Docker Inspect | 进程、socket 和 guest observation | virsh/domain 查询 |
| Pause/Resume | Docker pause/unpause | 暂停/恢复 Firecracker 进程 | virsh suspend/resume |
| Reset | 重新创建容器 | 从只读基线创建新 VM generation | 从 qcow2 基线创建新 overlay/domain |
| 外部身份 | Container ID | VM generation、进程和 socket | Domain UUID |

## 混合拓扑

在混合拓扑中显式声明 architecture 和 guest family。所有 provider 必须实现该拓扑需要的 NIC、guest execution、state 或 reset capability，否则 planning 阶段失败。

运行 `examples/heterogeneous-matrix` 验证六向 IPv4 通信、重复 plan、targeted reset 和 residue cleanup。Artifact 准备见 [Artifacts](../operations/artifacts.md)。

## Adopting an unowned libvirt definition

The public inspection/confirm workflow can claim a stopped persistent libvirt
VM by exact UUID. Inspection and pre-mutation checks compare the full definition
fingerprint and reject foreign ownership or a concurrent configuration change.
The host publishes Sysbox ownership and the original fingerprint with libvirt's
metadata API; after a lost response, cold observation accepts only that exact
operation and generation. Observation never writes metadata.

The imported handle owns the domain definition only. It contains no managed disk
path or VM directory, so Destroy stops/undefines the exact owned UUID and preserves
external disks, backing chains and other storage. Start reuses the existing UUID
and refuses to reconstruct a missing domain. Reset requires a separately planned
replacement from an immutable baseline and is rejected for an external definition.
Guest connectivity and NIC migration are not inferred from arbitrary external XML.
Libvirt metadata updates have no administrator-proof CAS: external writers must be
excluded during the explicit adoption operation; Sysbox serializes its own claims.

### dev 集成能力边界

Windows QGA 的本地命令、文件传输及启动后网络配置保留 UUID 校验；
Windows QGA 持久任务通过逐节点安装的签名 helper 接入
Start/Observe/Cancel、输出读取及续约，普通命令使用独立非管理员账户。
必须提供规范节点身份、issuer 公钥和 helper SHA256，安装与升级边界见
[guest tasks](../reference/guest-tasks.md#windows-tasks-over-qga)。
本适配的 QGA 请求总长限制为 32 KiB，超限在执行前拒绝；
特权 ETW 安装、持久文件发布及 file capture 仍不对外启用。
Firecracker guest firewall 使用签名持久任务执行结构化策略操作，guest 内保存
策略代际和删除墓碑，通过单个 nft transaction 替换规则。需要当前版本的
sysbox-init、config drive 中的节点身份与授权公钥，以及支持 nftables 的内核
和 `/usr/sbin/nft`。旧 guest 必须重建，不能仅更新宿主二进制后启用。
普通任务仍以非特权身份运行；策略内置任务不接受任意程序、环境或工作目录。
远程执行使用 owning-host Run child 调度；未配置签名授权的本地调用会被拒绝。

缓存中的原始 `vmlinux-5.10.245` 不包含 Netfilter netlink，不能用于 guest
firewall。可在构建 VM 中使用 `scripts/kernel/build-policy-kernel.sh` 和随附
配置生成专用内核；输入源码必须提供已核验的 SHA256，输出保留配置、构建日志
和摘要。guest rootfs 还须安装 nft 用户态程序。验收结果见本次适配 PR，Windows
持久任务能力与 Firecracker 策略能力分别验收，不以历史 PR 已合并代替验收。
