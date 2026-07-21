# Artifacts

Sysbox 不把实验 kernel、rootfs 或 qcow2 打包进 runtime image。Topology 通过 `sysbox_image` 和 `sysbox_kernel` 声明 artifact identity。

## Identity Rules

- 研究复现和生产部署固定 immutable digest。
- 明确 `kind`、`architecture` 和 `guest_family`。
- Baseline 只读；可变 guest 使用 provider-owned generation/overlay。
- 大文件放入 registry、object storage 或 host cache，不提交到 Sysbox 仓库。

## OCI Images

使用 registry digest 而不是可变 tag 作为长期 identity。Docker provider inspect image config 以确定有效 ENTRYPOINT/CMD；HCL override 仍属于 topology intent。

## Firecracker Kernel

使用支持 virtio 与 vsock 的 uncompressed `vmlinux`。声明 source 与 SHA-256。Kernel command line 由 kernel resource/provider contract 管理。

## Firecracker Rootfs

Rootfs 是 ext4 block image。仓库脚本从固定 Ubuntu squashfs 构建：

```bash
scripts/prepare-fc-rootfs.sh
```

脚本幂等并使用 Sysbox cache。Guest 需要标准 init 或 shell 作为 `chain_init`；`sysbox-init` 通过 config drive 注入 hostname、SSH key、environment 与 vsock agent。

## Libvirt Images

使用 immutable qcow2/cloud-image baseline。Sysbox 为每个 generation 创建 owned overlay。不要让多个 writable node 直接共享 baseline，也不要把手工创建的 overlay 放进 Sysbox-owned VM 目录。

## Cache And Verification

Artifact cache 命中前后都必须验证 digest。下载中断不能原子提升为有效 artifact。Reset 在 replacement 前重新散列 baseline，避免 plan 后内容被替换。

运行 `examples/microvm` 验证单 microVM artifact；运行 `examples/heterogeneous-matrix` 验证 Docker、Firecracker 和 libvirt 的组合。
