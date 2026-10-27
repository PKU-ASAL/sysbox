# 制品

Sysbox 不把实验 kernel、rootfs 或 qcow2 打包进 runtime image。Topology 通过 `sysbox_image` 和 `sysbox_kernel` 声明 artifact identity。

## 身份规则

- 研究复现和生产部署固定 immutable digest。
- 明确 `kind`、`architecture` 和 `guest_family`。
- Baseline 只读；可变 guest 使用 provider-owned generation/overlay。
- 大文件放入 registry、object storage 或 host cache，不提交到 Sysbox 仓库。

## OCI 镜像

使用 registry digest 而不是可变 tag 作为长期 identity。Docker provider inspect image config 以确定有效 ENTRYPOINT/CMD；HCL override 仍属于 topology intent。

## Firecracker 内核

使用支持 virtio 与 vsock 的 uncompressed `vmlinux`。声明 source 与 SHA-256。Kernel command line 由 kernel resource/provider contract 管理。

## Firecracker Rootfs

Rootfs 是 ext4 block image。仓库脚本从固定 Ubuntu squashfs 构建：

```bash
scripts/prepare-fc-rootfs.sh
```

脚本幂等并使用 Sysbox cache。Guest 需要标准 init 或 shell 作为 `chain_init`；`sysbox-init` 通过 config drive 注入 hostname、SSH key、environment 与 vsock agent。

## Libvirt 镜像

使用 immutable qcow2/cloud-image baseline。Sysbox 为每个 generation 创建 owned overlay。不要让多个 writable node 直接共享 baseline，也不要把手工创建的 overlay 放进 Sysbox-owned VM 目录。

## 缓存与校验

Artifact cache 命中前后都必须验证 digest。下载中断不能原子提升为有效 artifact。Reset 在 replacement 前重新散列 baseline，避免 plan 后内容被替换。

运行 `examples/microvm` 验证单 microVM artifact；运行 `examples/heterogeneous-matrix` 验证 Docker、Firecracker 和 libvirt 的组合。

## Docker digest compatibility

OCI `sha256` locks may match the daemon image ID or a digest from its
`RepoDigests`. A successful lock becomes the artifact's recorded identity;
container creation still uses the daemon-local immutable image ID. Reset stores
both values, checks the local ID when observing a container, and rejects a
changed image binding on cold recovery. Legacy reset handles retain their old
ID-equals-baseline interpretation. Mutable tags are not valid reset image IDs.

The runtime image includes the official Docker 29 CLI because preflight invokes
`docker version` independently of the Go SDK. Upgrading only the Sysbox binary
does not upgrade this CLI. The Go SDK continues to negotiate its API version.

Stage-1 acceptance used isolated Docker 20.10.24 and 29.9.1 daemons, with a runtime
CLI at 29.9.1. Both CLI connections and real manifest-locked image resolution
passed; unrelated digests were rejected. Both daemons in these configurations
reported a config digest as image ID: do not assume every Docker 29 installation
returns a manifest digest. Docker Hub direct access timed out; the real pulls
used the host-configured mirror `docker.1ms.run/library/alpine:3.22`.

Run `TestDockerDigestCompatibilityE2E` with the `e2e` tag,
`SYSBOX_DOCKER_COMPAT_E2E=1`, an explicit disposable `DOCKER_HOST`, and optionally
`SYSBOX_DOCKER_COMPAT_IMAGE`. This test does not authorize use of a production
daemon. Existing artifact publication admission/completion remains mandatory.

## VM local check and remote pull

For Firecracker rootfs and kernel files, and for libvirt qcow2 files, `source`
may be a local path or a direct HTTP(S) URL. A pinned URL is inspected in
Sysbox's artifact cache first. On a cache miss, the Agent downloads it at apply
time, verifies SHA-256, and atomically installs the cached file. Planning and
preflight do not download pinned VM files. An unpinned URL retains its older
mutable-source behavior and may be fetched during planning.

To preserve an existing local path while allowing a remote fallback, set
`pull_source` to the URL of the same bytes and pin `sha256`:

~~~hcl
resource "sysbox_kernel" "boot" {
  substrate    = substrate.firecracker.fc
  source       = "/var/lib/sysbox/vm-artifacts/vmlinux"
  pull_source  = "https://artifacts.example.org/vm/vmlinux"
  sha256       = "<kernel file SHA-256>"
  architecture = "amd64"
}

resource "sysbox_image" "app" {
  substrate    = substrate.firecracker.fc
  kind         = "rootfs"
  source       = "/var/lib/sysbox/vm-artifacts/app.ext4"
  pull_source  = "https://artifacts.example.org/vm/app.ext4"
  sha256       = "<rootfs file SHA-256>"
  architecture = "amd64"
  guest_family = "linux"
}
~~~

Sysbox checks the local file first. If it exists, its digest must match;
a mismatch fails without contacting the remote source. If it is absent, Sysbox
uses a verified cache entry or downloads `pull_source`. Firecracker copies the
resolved rootfs for each VM; libvirt creates an overlay from the resolved qcow2.
The same fallback works for `sysbox_kernel`. Docker `oci` images keep their
existing inspect-then-pull flow and do not use `pull_source`.

The remote endpoint must serve the raw ext4, qcow2, or vmlinux bytes named in
the HCL. This flow does not create or publish those artifacts; their URLs and
digests must be supplied when authoring a topology.
