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

需要 uncompressed `vmlinux` 和 ext4 rootfs。Provider 配置 chain init、SSH/vsock 和 machine 参数。Guest network configuration 通过声明的 network-init capability 完成，不由 runtime 猜测发行版配置文件。

## libvirt

使用 immutable qcow2 baseline 和 generation overlay。Provider 配置 machine、disk、SSH 与 network init。Domain UUID 和 owned overlay path 是 reset/destroy 的关键 identity。

## Mixed Topologies

在混合拓扑中显式声明 architecture 和 guest family。所有 provider 必须实现该拓扑需要的 NIC、guest execution、state 或 reset capability，否则 planning 阶段失败。

运行 `examples/heterogeneous-matrix` 验证六向 IPv4 通信、重复 plan、targeted reset 和 residue cleanup。Artifact 准备见 [Artifacts](../operations/artifacts.md)。
